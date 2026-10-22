//go:build !console

package main

import (
	"io/fs"

	"github.com/e6qu/sockerless-cloud/sim"
)

// consoleBundle returns the console in dir; this build embeds none, so with no
// dir the simulator serves no console.
func consoleBundle(dir string) (fs.FS, error) {
	if dir == "" {
		return nil, nil
	}
	return sim.ConsoleBundle(dir)
}
