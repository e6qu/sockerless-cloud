package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/fileshare"
)

// sharesRoot holds one directory per export, each an engine volume the
// simulator bound there: a name ending in -ro is served read-only.
const sharesRoot = "/exports"

// serveShares serves every directory under sharesRoot over NFSv4 on
// SIDECAR_SHARES_LISTEN until ctx ends: the engine volumes a guest's binds
// name, which the simulator's own process cannot read, reach the guest from
// the network namespace this program runs in.
func serveShares(ctx context.Context, out io.Writer) error {
	entries, err := os.ReadDir(sharesRoot)
	if err != nil {
		return err
	}
	exports := map[string]fileshare.Export{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		exports[entry.Name()] = fileshare.Export{
			Root:     filepath.Join(sharesRoot, entry.Name()),
			ReadOnly: strings.HasSuffix(entry.Name(), "-ro"),
		}
	}
	if len(exports) == 0 {
		return errors.New("nothing to serve under " + sharesRoot)
	}
	server, err := fileshare.New(exports)
	if err != nil {
		return err
	}
	listen := envOr("SIDECAR_SHARES_LISTEN", ":2049")
	ln, err := net.Listen("tcp4", listen)
	if err != nil {
		return fmt.Errorf("listen shares %s: %w", listen, err)
	}
	names := make([]string, 0, len(exports))
	for name := range exports {
		names = append(names, name)
	}
	fmt.Fprintf(out, "sidecar: shares %s serving %s\n", ln.Addr(), strings.Join(names, " "))
	return server.Serve(ctx, ln)
}
