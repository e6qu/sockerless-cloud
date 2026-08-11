//go:build !noui

package main

import (
	"embed"
	"io/fs"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

//go:embed all:dist
var uiAssets embed.FS

func registerUI(srv *sim.Server) {
	sub, err := fs.Sub(uiAssets, "dist")
	if err != nil {
		logger := srv.Logger()
		logger.Warn().Err(err).Msg("failed to load embedded UI assets")
		return
	}
	srv.RegisterUI(sub)
}
