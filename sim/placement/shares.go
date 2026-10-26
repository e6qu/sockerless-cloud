package placement

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/fileshare"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
)

// shares serves what a guest's binds name. This process serves a host
// directory, or a single host file, over NFSv4, on one port every engine
// network's gateway reaches. An engine volume lives where this process cannot
// read it, so a share container serves a workload's volumes from the node
// agent's network namespace, at the agent's address on the guest's network,
// and goes with the workload.
type shares struct {
	once   sync.Once
	server *fileshare.Server
	port   int
	err    error

	mu sync.Mutex
	// ports are the share containers' ports in the agent's namespace, by
	// the workload each serves.
	ports map[string]int
}

// sharesLabel marks a share container with the workload it serves.
const sharesLabel = "sockerless-sim-shares-of"

// firstSharePort is the lowest port a share container takes in the agent's
// namespace, above the agent's own API port.
const firstSharePort = 20490

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

// bind is one parsed bind of a workload.
type bind struct {
	source   string
	target   string
	readOnly bool
}

func parseBinds(w Workload) ([]bind, error) {
	var binds []bind
	for _, spec := range w.Binds {
		parts := strings.Split(spec, ":")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("%w: %s binds %q", ErrBindMounts, w.Name, spec)
		}
		b := bind{source: parts[0], target: parts[1]}
		if len(parts) > 2 {
			for _, opt := range strings.Split(parts[2], ",") {
				if opt == "ro" {
					b.readOnly = true
				}
			}
		}
		binds = append(binds, b)
	}
	return binds, nil
}

// mountsFor turns the workload's binds into the shares a guest mounts: a
// host directory or file from this process at gateway, an engine volume
// from a share container in the agent's namespace at agentAddress. owner is
// the workload the share container serves, which removeSharesOf ends.
func (s *shares) mountsFor(ctx context.Context, w Workload, owner, gateway, agentAddress, agentContainer string, sink sim.LogSink) ([]nodeagent.Mount, error) {
	binds, err := parseBinds(w)
	if err != nil || len(binds) == 0 {
		return nil, err
	}
	if err := s.start(); err != nil {
		return nil, err
	}
	var mounts []nodeagent.Mount
	var volumes []sidecar.ShareVolume
	for _, b := range binds {
		if !filepath.IsAbs(b.source) {
			name := exportName(b.source, b.readOnly)
			volumes = append(volumes, sidecar.ShareVolume{Volume: b.source, Export: name, ReadOnly: b.readOnly})
			mounts = append(mounts, nodeagent.Mount{Server: agentAddress + ":/" + name, Path: b.target, ReadOnly: b.readOnly})
			continue
		}
		info, err := os.Stat(b.source)
		if err != nil {
			return nil, fmt.Errorf("%w: %s binds %s: %v", ErrBindMounts, w.Name, b.source, err)
		}
		name := exportName(b.source, b.readOnly)
		export := fileshare.Export{Root: b.source, ReadOnly: b.readOnly}
		mount := nodeagent.Mount{Server: gateway + ":/" + name, Port: s.port, Path: b.target, ReadOnly: b.readOnly}
		if !info.IsDir() {
			export.Root, export.Only = filepath.Split(b.source)
			mount.File = export.Only
		}
		if err := s.server.AddExport(name, export); err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	if len(volumes) == 0 {
		return mounts, nil
	}
	port, err := s.serveVolumes(ctx, owner, agentAddress, agentContainer, volumes, sink)
	if err != nil {
		return nil, err
	}
	for i := range mounts {
		if mounts[i].Port == 0 {
			mounts[i].Port = port
		}
	}
	return mounts, nil
}

// serveVolumes starts the share container of the workload's volumes and
// returns the port it serves on.
func (s *shares) serveVolumes(ctx context.Context, owner, agentAddress, agentContainer string, volumes []sidecar.ShareVolume, sink sim.LogSink) (int, error) {
	if agentAddress == "" || agentContainer == "" {
		return 0, fmt.Errorf("%w: %s binds a volume, and the agent has no address on its network", ErrBindMounts, owner)
	}
	s.mu.Lock()
	if s.ports == nil {
		s.ports = map[string]int{}
	}
	if _, held := s.ports[owner]; held {
		s.mu.Unlock()
		return 0, fmt.Errorf("%s already has a share container", owner)
	}
	taken := map[int]bool{}
	for _, p := range s.ports {
		taken[p] = true
	}
	port := firstSharePort
	for taken[port] {
		port++
	}
	s.ports[owner] = port
	s.mu.Unlock()
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Export < volumes[j].Export })
	_, err := sidecar.StartShares(ctx, sidecar.SharesSpec{
		Name:        "sockerless-sim-shares-" + shortName(owner),
		NamespaceOf: agentContainer,
		Port:        port,
		Volumes:     volumes,
		Labels:      map[string]string{sharesLabel: owner},
	}, sink)
	if err != nil {
		s.mu.Lock()
		delete(s.ports, owner)
		s.mu.Unlock()
		return 0, err
	}
	return port, nil
}

// removeSharesOf ends the share container of a workload, if it has one, and
// gives its port back; a container a previous process started is found by
// its label.
func (s *shares) removeSharesOf(owner string) error {
	s.mu.Lock()
	delete(s.ports, owner)
	s.mu.Unlock()
	containers, err := sim.FindExistingContainers(map[string]string{sharesLabel: owner})
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range containers {
		if err := sim.RemoveContainer(c.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// shortName makes a container-name suffix of a workload's identity.
func shortName(owner string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(owner))
	return strconv.FormatUint(h.Sum64(), 36)
}

// exportName names a path's export: a hash of it, with the mode, so the
// same directory served read-only and read-write is two exports.
func exportName(path string, readOnly bool) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	name := strconv.FormatUint(h.Sum64(), 36)
	if readOnly {
		return name + "-ro"
	}
	return name + "-rw"
}
