//go:build !linux

package realexec

import "fmt"

// StartCapture fails on a host that cannot capture traffic.
//
// Capturing means reading frames off a workload's interface through a packet
// socket, which exists only on Linux and only with CAP_NET_RAW. There is no
// degraded form worth offering: a capture that returned an empty file, or a
// session that reported Running with nothing behind it, would be a fabricated
// artifact — and a caller cannot tell a fabricated capture from a real one that
// happened to see no traffic. Failing here means the operation that wanted a
// capture reports honestly that this host cannot produce one.
func StartCapture(spec CaptureSpec) (*Capture, error) {
	return nil, fmt.Errorf("capture %q in namespace %q: %w",
		spec.InterfaceName, spec.NamespaceName, ErrCaptureUnsupported)
}
