// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"syscall"
)

// True only when the host will not give the address and port: in use,
// privileged, or a missing loopback address. Any other failure is real
// configuration and still fails the call.
func elbv2HostCannotOfferAddress(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EADDRNOTAVAIL)
}
