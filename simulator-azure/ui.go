package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerUI mounts the console the build embeds or consoleDir holds.
func registerUI(srv *sim.Server, consoleDir string) error {
	bundle, err := consoleBundle(consoleDir)
	if err != nil {
		return fmt.Errorf("console: %w", err)
	}
	if bundle != nil {
		srv.RegisterUI(bundle, azureConsoleOptions(srv))
	}
	return nil
}
