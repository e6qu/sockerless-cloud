package realexec

import (
	"errors"
	"sync"
	"time"
)

// A traffic capture records the frames that actually cross a workload's
// interface. Nothing here manufactures a session: a capture reports Running
// only while a socket is really reading frames off the interface, and the file
// it produces holds the bytes that were on the wire. A capture that reported
// progress with no packets behind it would be fiction, which is why the socket
// is the thing this package provides and the cloud-shaped operations are built
// on top of it in each simulator.
//
// The socket is Linux-only (capture_linux.go); every other platform gets the
// unsupported implementation in capture_unsupported.go, which fails loudly
// rather than pretending to capture.

// CaptureStopReason explains why a capture is no longer running. The values
// mirror the reasons a cloud reports back on a capture session.
type CaptureStopReason string

const (
	// CaptureStopRequested is an explicit stop by the operator.
	CaptureStopRequested CaptureStopReason = "Requested"
	// CaptureStopTimeExceeded is the session's time limit elapsing.
	CaptureStopTimeExceeded CaptureStopReason = "TimeExceeded"
	// CaptureStopSizeExceeded is the session's total-bytes limit being
	// reached.
	CaptureStopSizeExceeded CaptureStopReason = "SizeExceeded"
	// CaptureStopError is the capture failing; Status carries the error.
	CaptureStopError CaptureStopReason = "Error"
)

// CaptureSpec describes one capture session against one interface.
type CaptureSpec struct {
	// NamespaceName is the network namespace holding the interface, as
	// created by CreateNetworkNamespace. Empty captures in the host namespace.
	NamespaceName string
	// InterfaceName is the interface to read frames from — the TAP backing a
	// Firecracker guest, or the host side of a container's veth pair.
	InterfaceName string
	// BytesPerPacket truncates each captured frame. Zero captures whole
	// frames.
	BytesPerPacket int
	// TotalBytes bounds the capture file. Zero is unbounded.
	TotalBytes int64
	// TimeLimit bounds the session's duration. Zero is unbounded.
	TimeLimit time.Duration
	// Filters select which frames are captured; none captures everything.
	Filters []CaptureFilter
}

// CaptureStatus is a point-in-time view of a session.
type CaptureStatus struct {
	Running    bool
	StartedAt  time.Time
	StoppedAt  time.Time
	StopReason CaptureStopReason
	Packets    uint64
	Bytes      uint64
	Err        error
}

// ErrCaptureUnsupported is returned when the host cannot capture traffic.
// Callers surface it rather than substituting an empty capture.
var ErrCaptureUnsupported = errors.New(
	"traffic capture requires a Linux host with packet-socket support")

// Capture is a running capture session. It is constructed by the platform
// implementation of StartCapture, which owns the socket that feeds it.
type Capture struct {
	mu      sync.Mutex
	status  CaptureStatus
	pcap    *PcapWriter
	buf     *captureBuffer
	stopped chan struct{}
	stopOne sync.Once
	closer  func() error
}

// Status reports the session's current state, with the packet and byte counts
// read from the writer so they reflect what has really been recorded.
func (c *Capture) Status() CaptureStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.status
	if c.pcap != nil {
		status.Packets, status.Bytes = c.pcap.Counts()
	}
	return status
}

// Bytes returns the capture file produced so far, a complete libpcap capture
// that any reader can open whether or not the session has stopped.
func (c *Capture) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf == nil {
		return nil
	}
	return c.buf.Snapshot()
}

// Stop ends the session. Stopping an already-stopped capture is not an error,
// so a stop racing the time limit behaves the same as one that won.
func (c *Capture) Stop() error {
	return c.stop(CaptureStopRequested, nil)
}

func (c *Capture) stop(reason CaptureStopReason, cause error) error {
	var err error
	c.stopOne.Do(func() {
		c.mu.Lock()
		if c.status.Running {
			c.status.Running = false
			c.status.StoppedAt = time.Now().UTC()
			c.status.StopReason = reason
			c.status.Err = cause
		}
		c.mu.Unlock()
		close(c.stopped)
		if c.closer != nil {
			err = c.closer()
		}
	})
	return err
}

// captureBuffer accumulates the capture file. Reads take a copy so a status
// query or an upload never races the capture loop appending to it.
type captureBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *captureBuffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

func (b *captureBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// maximumFrameLength is large enough to receive any frame a link will carry,
// including jumbo frames. It is the read-buffer size, never a truncation
// limit — truncation belongs to the writer so the frame's original length
// survives into the capture file.
const maximumFrameLength = 65535

// CaptureSnapLength is the per-frame limit recorded in the capture file:
// BytesPerPacket when the spec sets one, and otherwise a length that truncates
// nothing in practice.
func (s CaptureSpec) CaptureSnapLength() int {
	if s.BytesPerPacket > 0 {
		return s.BytesPerPacket
	}
	return maximumFrameLength
}
