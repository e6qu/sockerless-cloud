//go:build !linux

package realexec

import "fmt"

// StartMirror fails on a host that cannot mirror traffic.
//
// Mirroring means reading frames off one interface through a packet socket and
// transmitting copies out of another, which exists only on Linux and only with
// CAP_NET_RAW. There is no degraded form worth offering: a session that
// reported Running while delivering nothing is precisely the fiction a
// mirroring policy must not be, and a collector cannot tell a policy that
// mirrors nothing from one whose mirrored workload happened to be idle.
func StartMirror(spec MirrorSpec) (*Mirror, error) {
	return nil, fmt.Errorf("mirror %q in namespace %q: %w",
		spec.SourceInterface, spec.SourceNamespace, ErrCaptureUnsupported)
}
