// Package uniquename names test resources so that tests running at once, and
// a suite run twice against one simulator, never share a name.
package uniquename

import (
	"fmt"
	"math/rand/v2"
	"sync/atomic"
)

var next atomic.Uint32

func init() {
	next.Store(rand.Uint32())
}

// New suffixes prefix with eight lowercase hexadecimal digits, distinct for
// every call in the process and drawn from a random start per process.
func New(prefix string) string {
	return fmt.Sprintf("%s-%08x", prefix, next.Add(1))
}

// Alnum suffixes prefix with the same eight digits and no hyphen, for the
// names that admit only lowercase letters and digits, such as Azure Storage
// accounts and container registries.
func Alnum(prefix string) string {
	return fmt.Sprintf("%s%08x", prefix, next.Add(1))
}
