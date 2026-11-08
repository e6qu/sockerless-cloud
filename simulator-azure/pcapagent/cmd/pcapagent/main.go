//go:build linux

// Command pcapagent records the frames crossing a machine's interfaces into a
// libpcap file. Network Watcher runs it in the machine's network namespace:
//
//	pcapagent run FILE SNAPLEN LIMIT SECONDS FILTERS
//	pcapagent stop
//	pcapagent read FILE OFFSET LENGTH
//
// run prints "capturing" once its socket receives, and captures until SECONDS pass, the file would grow past LIMIT bytes, or
// stop asks it to end, then prints "stopped <reason>" and waits to be
// terminated. read prints a range of the file in base64, so the bytes survive
// any exec transport.
package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/e6qu/sockerless-cloud/simulator-azure/pcapagent/filter"
)

// stopFile asks a running capture to end; the capture looks for it between
// receives.
const stopFile = "/captures/stop"

func main() {
	if len(os.Args) < 2 {
		fail("usage: pcapagent run|stop|read")
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "stop":
		err = stop()
	case "read":
		err = read(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "pcapagent:", message)
	os.Exit(1)
}

func stop() error {
	return os.WriteFile(stopFile, nil, 0o644)
}

func read(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: pcapagent read FILE OFFSET LENGTH")
	}
	offset, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return err
	}
	length, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return err
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	_, err = os.Stdout.WriteString(base64.StdEncoding.EncodeToString(buf[:n]))
	return err
}

const (
	ethPAll        = 0x0003
	packetOutgoing = 4
	arphrdEther    = 1
	linktypeEther  = 1
	maxFrame       = 1 << 18
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func run(args []string) error {
	if len(args) != 5 {
		return errors.New("usage: pcapagent run FILE SNAPLEN LIMIT SECONDS FILTERS")
	}
	path := args[0]
	snaplen, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return err
	}
	limit, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return err
	}
	var specs []filter.Spec
	if err := json.Unmarshal([]byte(args[4]), &specs); err != nil {
		return err
	}
	filters, err := filter.Parse(specs)
	if err != nil {
		return err
	}
	loopbacks := map[int]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			loopbacks[iface.Index] = true
		}
	}

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(ethPAll)))
	if err != nil {
		return fmt.Errorf("open a packet socket: %w", err)
	}
	// The socket only reads; closing it at exit loses nothing.
	defer func() { _ = syscall.Close(fd) }()
	// Wake at least every 100ms to see the deadline or a stop.
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Usec: 100000}); err != nil {
		return err
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	header := make([]byte, 24)
	binary.LittleEndian.PutUint32(header[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(header[4:], 2)
	binary.LittleEndian.PutUint16(header[6:], 4)
	recordLimit := uint32(maxFrame)
	if snaplen > 0 && snaplen < maxFrame {
		recordLimit = uint32(snaplen)
	}
	binary.LittleEndian.PutUint32(header[16:], recordLimit)
	binary.LittleEndian.PutUint32(header[20:], linktypeEther)
	if _, err := out.Write(header); err != nil {
		return err
	}
	written := int64(len(header))

	terminate := make(chan os.Signal, 1)
	signal.Notify(terminate, syscall.SIGTERM, syscall.SIGINT)
	fmt.Println("capturing")

	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	frame := make([]byte, maxFrame)
	record := make([]byte, 16)
	reason := "TimeExceeded"
	var checked time.Time
	for {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		if now.Sub(checked) >= 100*time.Millisecond {
			checked = now
			if _, err := os.Stat(stopFile); err == nil {
				reason = "Requested"
				break
			}
		}
		n, from, err := syscall.Recvfrom(fd, frame, syscall.MSG_TRUNC)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
				continue
			}
			return fmt.Errorf("receive: %w", err)
		}
		at := time.Now()
		link, ok := from.(*syscall.SockaddrLinklayer)
		if !ok || loopbacks[link.Ifindex] || link.Hatype != arphrdEther {
			continue
		}
		size := min(n, len(frame))
		if !filter.Captures(filters, frame[:size], link.Pkttype == packetOutgoing) {
			continue
		}
		kept := size
		if snaplen > 0 && int64(kept) > snaplen {
			kept = int(snaplen)
		}
		if written+int64(len(record)+kept) > limit {
			reason = "SizeExceeded"
			break
		}
		binary.LittleEndian.PutUint32(record[0:], uint32(at.Unix()))
		binary.LittleEndian.PutUint32(record[4:], uint32(at.Nanosecond()/1000))
		binary.LittleEndian.PutUint32(record[8:], uint32(kept))
		binary.LittleEndian.PutUint32(record[12:], uint32(n))
		if _, err := out.Write(record); err != nil {
			return err
		}
		if _, err := out.Write(frame[:kept]); err != nil {
			return err
		}
		written += int64(len(record) + kept)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	fmt.Println("stopped", reason)
	<-terminate
	return nil
}
