//go:build console

package main

import (
	"embed"
	"io/fs"

	"github.com/e6qu/sockerless-cloud/sim"
)

//go:embed all:dist
var uiAssets embed.FS

// The embedded console, or the one in dir when the operator names one.
func consoleBundle(dir string) (fs.FS, error) {
	if dir != "" {
		return sim.ConsoleBundle(dir)
	}
	return fs.Sub(uiAssets, "dist")
}
