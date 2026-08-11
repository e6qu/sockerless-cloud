//go:build linux

package realexec

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// StartCapture opens a packet socket on the named interface and records the
// frames that cross it into a libpcap capture, honouring the session's
// filters and its per-frame, total-size and duration bounds.
//
// The socket is AF_PACKET/SOCK_RAW bound to the interface, which is the same
// mechanism tcpdump uses — the frames recorded are the frames on the wire,
// including their Ethernet headers. Opening it requires CAP_NET_RAW, and
// binding it inside a workload's network namespace requires entering that
// namespace first, which is why the socket is created on a thread locked to
// the namespace and the thread is returned to the host namespace afterwards.
func StartCapture(spec CaptureSpec) (*Capture, error) {
	if spec.InterfaceName == "" {
		return nil, fmt.Errorf("capture requires an interface name")
	}
	fd, err := openCaptureSocket(spec.NamespaceName, spec.InterfaceName)
	if err != nil {
		return nil, err
	}

	buf := &captureBuffer{}
	pcap, err := NewPcapWriter(buf, spec.CaptureSnapLength())
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	c := &Capture{
		pcap:    pcap,
		buf:     buf,
		stopped: make(chan struct{}),
		status: CaptureStatus{
			Running:   true,
			StartedAt: time.Now().UTC(),
		},
		closer: func() error { return unix.Close(fd) },
	}
	go c.readFrames(fd, spec)
	if spec.TimeLimit > 0 {
		go func() {
			timer := time.NewTimer(spec.TimeLimit)
			defer timer.Stop()
			select {
			case <-timer.C:
				_ = c.stop(CaptureStopTimeExceeded, nil)
			case <-c.stopped:
			}
		}()
	}
	return c, nil
}

// openCaptureSocket creates the packet socket bound to the interface, entering
// the namespace that holds the interface for exactly as long as it takes to
// resolve and bind it. The socket outlives the namespace switch: a file
// descriptor keeps its binding regardless of which namespace the thread that
// created it is in afterwards.
func openCaptureSocket(namespaceName, interfaceName string) (int, error) {
	if namespaceName == "" {
		return bindPacketSocket(interfaceName)
	}

	// setns applies to the calling THREAD, so the goroutine must be pinned to
	// one and that thread must be returned to the host namespace before it is
	// released back to the scheduler — otherwise unrelated goroutines would
	// later run inside the workload's namespace.
	type result struct {
		fd  int
		err error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hostNS, err := os.Open("/proc/thread-self/ns/net")
		if err != nil {
			done <- result{err: fmt.Errorf("open host network namespace: %w", err)}
			return
		}
		defer hostNS.Close()

		target, err := os.Open("/var/run/netns/" + namespaceName)
		if err != nil {
			done <- result{err: fmt.Errorf("open network namespace %q: %w", namespaceName, err)}
			return
		}
		defer target.Close()

		if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- result{err: fmt.Errorf("enter network namespace %q: %w", namespaceName, err)}
			return
		}
		fd, bindErr := bindPacketSocket(interfaceName)
		if err := unix.Setns(int(hostNS.Fd()), unix.CLONE_NEWNET); err != nil {
			// The thread could not be returned to the host namespace, so it
			// must not go back into the scheduler's pool. Leaking one locked
			// thread is far cheaper than silently running other work in a
			// workload's namespace.
			runtime.Goexit()
		}
		done <- result{fd: fd, err: bindErr}
	}()
	r := <-done
	return r.fd, r.err
}

func bindPacketSocket(interfaceName string) (int, error) {
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return -1, fmt.Errorf("resolve interface %q: %w", interfaceName, err)
	}
	// ETH_P_ALL in network byte order: every protocol, so the capture sees the
	// interface's whole traffic rather than one family of it.
	protocol := hostToNetworkShort(unix.ETH_P_ALL)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(protocol))
	if err != nil {
		return -1, fmt.Errorf("open packet socket (requires CAP_NET_RAW): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: protocol,
		Ifindex:  iface.Index,
	}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("bind packet socket to %q: %w", interfaceName, err)
	}
	// A read timeout keeps the capture loop responsive to Stop on an interface
	// that has gone quiet, instead of blocking in recvfrom indefinitely.
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO,
		&unix.Timeval{Sec: 0, Usec: 250_000}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("set capture read timeout: %w", err)
	}
	return fd, nil
}

func hostToNetworkShort(v uint16) uint16 {
	return v<<8 | v>>8
}

// readFrames is the capture loop: read a frame, apply the filters, record what
// passes, and stop when a bound is reached.
func (c *Capture) readFrames(fd int, spec CaptureSpec) {
	// The read buffer is a full frame regardless of the session's per-packet
	// limit. Sizing it to the limit instead would let the KERNEL truncate the
	// frame, so the length reported back would be the truncated one and the
	// capture would record a 64-byte frame as having been 64 bytes on the wire.
	// Reading whole frames and truncating in the writer keeps the original
	// length, which is what tells a reader the capture was cut short.
	frame := make([]byte, maximumFrameLength)
	for {
		select {
		case <-c.stopped:
			return
		default:
		}

		n, _, err := unix.Recvfrom(fd, frame, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
				// The read timeout expired or a signal interrupted it; neither
				// ends the session.
				continue
			}
			if err == unix.EBADF {
				// The descriptor closed underneath us, which is Stop doing its
				// work — not a capture failure.
				return
			}
			_ = c.stop(CaptureStopError, fmt.Errorf("read frame: %w", err))
			return
		}
		if n <= 0 {
			continue
		}
		captured := frame[:n]
		if !MatchesAny(captured, spec.Filters) {
			continue
		}
		if err := c.pcap.WritePacket(time.Now().UTC(), captured, n); err != nil {
			_ = c.stop(CaptureStopError, fmt.Errorf("write frame: %w", err))
			return
		}
		if spec.TotalBytes > 0 && int64(c.buf.Len()) >= spec.TotalBytes {
			_ = c.stop(CaptureStopSizeExceeded, nil)
			return
		}
	}
}
