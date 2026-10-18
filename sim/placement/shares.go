package placement

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim/fileshare"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
)

// shares serves the host directories a guest's binds name over NFSv4 from
// this process, on one port every engine network's gateway reaches, so a
// guest mounts what a container would have bound.
type shares struct {
	once   sync.Once
	server *fileshare.Server
	port   int
	err    error
}

func (s *shares) start() error {
	s.once.Do(func() {
		server, err := fileshare.New(nil)
		if err != nil {
			s.err = err
			return
		}
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			s.err = fmt.Errorf("file shares: %w", err)
			return
		}
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			s.err = fmt.Errorf("file shares: listening on %s, not a TCP address", ln.Addr())
			_ = ln.Close()
			return
		}
		s.server = server
		s.port = addr.Port
		go func() { _ = server.Serve(context.Background(), ln) }()
	})
	return s.err
}

// mountsFor turns the workload's binds into the shares a guest mounts from
// its network's gateway: a host directory is served; a named volume, which
// lives in the engine where this process cannot read it, and a single file,
// which NFS exports no view of, are refused.
func (s *shares) mountsFor(w Workload, gateway string) ([]nodeagent.Mount, error) {
	if len(w.Binds) == 0 {
		return nil, nil
	}
	if err := s.start(); err != nil {
		return nil, err
	}
	var mounts []nodeagent.Mount
	for _, bind := range w.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 || parts[1] == "" {
			return nil, fmt.Errorf("%w: %s binds %q", ErrBindMounts, w.Name, bind)
		}
		source, target := parts[0], parts[1]
		readOnly := false
		if len(parts) > 2 {
			for _, opt := range strings.Split(parts[2], ",") {
				if opt == "ro" {
					readOnly = true
				}
			}
		}
		if !filepath.IsAbs(source) {
			return nil, fmt.Errorf("%w: %s binds the volume %s, which the engine holds", ErrBindMounts, w.Name, source)
		}
		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("%w: %s binds %s: %v", ErrBindMounts, w.Name, source, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: %s binds the file %s, which a share cannot carry alone", ErrBindMounts, w.Name, source)
		}
		name := exportName(source, readOnly)
		if err := s.server.AddExport(name, fileshare.Export{Root: source, ReadOnly: readOnly}); err != nil {
			return nil, err
		}
		mounts = append(mounts, nodeagent.Mount{Server: gateway + ":/" + name, Port: s.port, Path: target, ReadOnly: readOnly})
	}
	return mounts, nil
}

// exportName names a directory's export: a hash of its path, with the mode,
// so the same directory served read-only and read-write is two exports.
func exportName(path string, readOnly bool) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	name := strconv.FormatUint(h.Sum64(), 36)
	if readOnly {
		return name + "-ro"
	}
	return name + "-rw"
}
