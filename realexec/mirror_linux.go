//go:build linux

package realexec

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// StartMirror opens a packet socket on the mirrored interface and one on each
// collector, then copies every frame that passes the filter from the first to
// the rest for as long as the session runs.
//
// Delivery is a sendto on an AF_PACKET/SOCK_RAW socket bound to the collector's
// interface, which transmits the frame out of that interface. For the TAP
// backing a Firecracker guest, transmitting out of the host-side TAP is the
// guest receiving it, so the collector workload really sees the mirrored
// traffic; the same holds for the host side of a veth pair.
func StartMirror(spec MirrorSpec) (*Mirror, error) {
	if spec.SourceInterface == "" {
		return nil, fmt.Errorf("mirroring requires a mirrored interface name")
	}
	if len(spec.Collectors) == 0 {
		// A session with nowhere to deliver would read frames and drop them
		// while reporting itself healthy, which is exactly the fiction a
		// mirroring policy must not be.
		return nil, fmt.Errorf("mirroring requires at least one collector interface")
	}

	sourceFD, err := openCaptureSocket(spec.SourceNamespace, spec.SourceInterface)
	if err != nil {
		return nil, err
	}

	sinks := make([]mirrorSink, 0, len(spec.Collectors))
	closeAll := func() {
		_ = unix.Close(sourceFD)
		for _, s := range sinks {
			_ = unix.Close(s.fd)
		}
	}
	for _, collector := range spec.Collectors {
		fd, ifindex, err := openInjectSocket(collector.NamespaceName, collector.InterfaceName)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("collector %s/%s: %w",
				collector.NamespaceName, collector.InterfaceName, err)
		}
		sinks = append(sinks, mirrorSink{fd: fd, ifindex: ifindex})
	}

	m := &Mirror{
		stopped: make(chan struct{}),
		status: MirrorStatus{
			Running:   true,
			StartedAt: time.Now().UTC(),
		},
	}
	m.closer = func() error {
		errs := []error{unix.Close(sourceFD)}
		for _, s := range sinks {
			errs = append(errs, unix.Close(s.fd))
		}
		return errors.Join(errs...)
	}
	go m.mirrorFrames(sourceFD, sinks, spec)
	return m, nil
}

// mirrorSink is one collector's injection socket and the interface it delivers
// out of.
type mirrorSink struct {
	fd      int
	ifindex int
}

// openInjectSocket binds a packet socket used for sending on the collector's
// interface, entering the namespace holding it exactly as the capture socket
// does and for the same reason: setns applies to the calling thread.
func openInjectSocket(namespaceName, interfaceName string) (int, int, error) {
	if namespaceName == "" {
		return bindInjectSocket(interfaceName)
	}
	type result struct {
		fd      int
		ifindex int
		err     error
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
		fd, ifindex, bindErr := bindInjectSocket(interfaceName)
		if err := unix.Setns(int(hostNS.Fd()), unix.CLONE_NEWNET); err != nil {
			// The thread could not be returned to the host namespace, so it must
			// not go back into the scheduler's pool.
			runtime.Goexit()
		}
		done <- result{fd: fd, ifindex: ifindex, err: bindErr}
	}()
	r := <-done
	return r.fd, r.ifindex, r.err
}

func bindInjectSocket(interfaceName string) (int, int, error) {
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return -1, 0, fmt.Errorf("resolve interface %q: %w", interfaceName, err)
	}
	protocol := hostToNetworkShort(unix.ETH_P_ALL)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(protocol))
	if err != nil {
		return -1, 0, fmt.Errorf("open packet socket (requires CAP_NET_RAW): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: protocol,
		Ifindex:  iface.Index,
	}); err != nil {
		_ = unix.Close(fd)
		return -1, 0, fmt.Errorf("bind packet socket to %q: %w", interfaceName, err)
	}
	return fd, iface.Index, nil
}

// mirrorFrames is the mirroring loop: read a frame, decide whether the policy
// selects it, and deliver a copy to every collector.
func (m *Mirror) mirrorFrames(fd int, sinks []mirrorSink, spec MirrorSpec) {
	frame := make([]byte, maximumFrameLength)
	for {
		select {
		case <-m.stopped:
			return
		default:
		}

		n, from, err := unix.Recvfrom(fd, frame, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
				continue
			}
			if err == unix.EBADF {
				// Stop closed the descriptor.
				return
			}
			_ = m.stop(fmt.Errorf("read frame: %w", err))
			return
		}
		if n <= 0 {
			continue
		}
		mirrored := frame[:n]

		// PACKET_OUTGOING means the host transmitted the frame out of this
		// interface. On the host side of a workload's TAP or veth, that is the
		// frame travelling TOWARDS the workload — so the host's "outgoing" is
		// the workload's ingress, and everything else is the workload's egress.
		// Direction in a mirroring policy is expressed from the workload's
		// point of view, so it is read through that inversion.
		workloadIngress := false
		if link, ok := from.(*unix.SockaddrLinklayer); ok {
			workloadIngress = link.Pkttype == unix.PACKET_OUTGOING
		}
		if !spec.Direction.mirrors(workloadIngress) {
			continue
		}
		if !MatchesAny(mirrored, spec.Filters) {
			continue
		}

		delivered := false
		for _, sink := range sinks {
			if err := unix.Sendto(sink.fd, mirrored, 0, &unix.SockaddrLinklayer{
				Protocol: hostToNetworkShort(unix.ETH_P_ALL),
				Ifindex:  sink.ifindex,
				Halen:    6,
			}); err != nil {
				// One collector refusing a frame — a full queue, an interface
				// that went down — does not end the session or stop the other
				// collectors receiving it, but it is counted rather than
				// swallowed so a policy delivering nothing is visible.
				m.undelivered.Add(1)
				continue
			}
			delivered = true
		}
		if delivered {
			m.packets.Add(1)
			m.bytes.Add(uint64(n))
		}
	}
}

// mirrors reports whether a frame flowing in the given direction is selected.
// The direction is expressed relative to the mirrored workload, which is the
// inverse of the host's view: see mirror_linux.go.
func (d MirrorDirection) mirrors(workloadIngress bool) bool {
	switch d {
	case MirrorIngress:
		return workloadIngress
	case MirrorEgress:
		return !workloadIngress
	default:
		return true
	}
}
