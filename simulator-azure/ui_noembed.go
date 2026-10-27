//go:build !console

package main

import (
	"io/fs"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This build embeds no console, so without dir the simulator serves none.
func consoleBundle(dir string) (fs.FS, error) {
	if dir == "" {
		return nil, nil
	}
	return sim.ConsoleBundle(dir)
}
