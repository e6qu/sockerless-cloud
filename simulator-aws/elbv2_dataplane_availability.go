// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"syscall"
)

// elbv2HostCannotOfferAddress reports whether a data-plane bind failed because
// the host will not give the simulator that address and port — it is already
// bound by another process, the port is privileged and the simulator is not,
// or the loopback address does not exist here.
//
// It is deliberately narrow. Anything else (a malformed listener, a target
// group that cannot be resolved) is a real configuration failure and must
// still fail the API call.
func elbv2HostCannotOfferAddress(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EADDRNOTAVAIL)
}
