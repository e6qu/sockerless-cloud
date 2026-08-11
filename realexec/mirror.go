package realexec

import (
	"sync"
	"sync/atomic"
	"time"
)

// Traffic mirroring is not traffic capture with a different destination. A
// capture reads frames off an interface and records them; a mirroring policy
// reads them and *delivers a copy to somewhere else*, continuously, for as long
// as the policy exists. The read half is the packet socket the capture already
// uses; the half that does not exist anywhere else in this package is the send
// half, and it is what makes a mirroring policy mirror rather than merely
// record that it is enabled.
//
// A mirrored frame is delivered verbatim — its original Ethernet, IP and
// transport headers intact, addressed to whoever the original packet was
// addressed to. That is what the clouds do and it is the entire point: a
// collector runs an intrusion-detection system over real traffic, so rewriting
// the headers to make delivery tidier would destroy the thing being collected.
// It also means a collector sees frames whose destination MAC is not its own,
// exactly as a real collector does, and must be reading promiscuously to
// observe them.
//
// The socket is Linux-only (mirror_linux.go); every other platform gets the
// unsupported implementation, which fails loudly rather than reporting a
// policy that mirrors nothing.

// MirrorDirection selects which traffic a policy mirrors, relative to the
// mirrored workload.
type MirrorDirection string

const (
	// MirrorIngress mirrors traffic arriving at the mirrored workload.
	MirrorIngress MirrorDirection = "INGRESS"
	// MirrorEgress mirrors traffic leaving the mirrored workload.
	MirrorEgress MirrorDirection = "EGRESS"
	// MirrorBoth mirrors traffic in both directions, and is the default.
	MirrorBoth MirrorDirection = "BOTH"
)

// MirrorTarget names one interface mirrored frames are delivered to — a
// collector's TAP or the host side of its veth pair.
type MirrorTarget struct {
	NamespaceName string
	InterfaceName string
}

// MirrorSpec describes one mirroring session: one mirrored interface, the
// collectors its traffic is copied to, and the filter deciding what counts.
type MirrorSpec struct {
	// SourceNamespace is the network namespace holding the mirrored
	// interface. Empty mirrors an interface in the host namespace.
	SourceNamespace string
	// SourceInterface is the mirrored workload's interface.
	SourceInterface string
	// Collectors receive a copy of every frame that passes the filter. A
	// session with no collectors is rejected: it would read frames and drop
	// them, which is the fiction this type exists to prevent.
	Collectors []MirrorTarget
	// Filters select which frames are mirrored; none mirrors everything.
	Filters []CaptureFilter
	// Direction selects which way traffic must be flowing. Empty is
	// MirrorBoth.
	Direction MirrorDirection
}

// MirrorStatus is a point-in-time view of a mirroring session. The counters are
// what was really delivered, so a policy that is enabled but mirroring nothing
// is visible as such rather than indistinguishable from a working one.
type MirrorStatus struct {
	Running     bool
	StartedAt   time.Time
	StoppedAt   time.Time
	Packets     uint64
	Bytes       uint64
	Undelivered uint64
	Err         error
}

// Mirror is a running mirroring session, constructed by the platform
// implementation of StartMirror.
type Mirror struct {
	mu      sync.Mutex
	status  MirrorStatus
	stopped chan struct{}
	stopOne sync.Once
	closer  func() error

	packets     atomic.Uint64
	bytes       atomic.Uint64
	undelivered atomic.Uint64
}

// Status reports the session's current state with the delivery counters read
// from the mirroring loop.
func (m *Mirror) Status() MirrorStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	status.Packets = m.packets.Load()
	status.Bytes = m.bytes.Load()
	status.Undelivered = m.undelivered.Load()
	return status
}

// Stop ends the session. Stopping an already-stopped mirror is not an error.
func (m *Mirror) Stop() error {
	return m.stop(nil)
}

func (m *Mirror) stop(cause error) error {
	var err error
	m.stopOne.Do(func() {
		m.mu.Lock()
		if m.status.Running {
			m.status.Running = false
			m.status.StoppedAt = time.Now().UTC()
			m.status.Err = cause
		}
		m.mu.Unlock()
		close(m.stopped)
		if m.closer != nil {
			err = m.closer()
		}
	})
	return err
}
