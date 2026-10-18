package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// guestSpec and the other wire types mirror sim/nodeagent's; the agent reads
// JSON and keeps no dependency on the framework.
type guestSpec struct {
	ID         string            `json:"id"`
	Hostname   string            `json:"hostname"`
	Hosts      []string          `json:"hosts"`
	Labels     map[string]string `json:"labels"`
	Command    []string          `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	Network    guestNetwork      `json:"network"`
	VCPUs      int               `json:"vcpus"`
	MemoryMiB  int               `json:"memoryMiB"`
	Seed       []seedFile        `json:"seed"`
	StopGrace  time.Duration     `json:"stopGrace"`
	Ingress    []packetRule      `json:"ingress"`
	DiskSlots  int               `json:"diskSlots"`
}

type guestNetwork struct {
	AgentAddress string   `json:"agentAddress"`
	Address      string   `json:"address"`
	Gateway      string   `json:"gateway"`
	MAC          string   `json:"mac"`
	DNS          []string `json:"dns"`
}

type seedFile struct {
	Path string `json:"path"`
	Mode int64  `json:"mode"`
	Data []byte `json:"data"`
}

type packetRule struct {
	Protocol   string `json:"protocol"`
	SourceCIDR string `json:"sourceCIDR"`
	FromPort   int    `json:"fromPort"`
	ToPort     int    `json:"toPort"`
	Action     string `json:"action"`
}

type guestState struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Labels    map[string]string `json:"labels,omitempty"`
	Address   string            `json:"address"`
	ExitCode  int               `json:"exitCode"`
	Error     string            `json:"error,omitempty"`
	StartedAt time.Time         `json:"startedAt"`
	StoppedAt time.Time         `json:"stoppedAt"`
}

const (
	statusCreating = "creating"
	statusRunning  = "running"
	statusExited   = "exited"
)

type agent struct {
	cfg    config
	net    *networks
	mu     sync.Mutex
	guests map[string]*guest
}

func newAgent(cfg config) (*agent, error) {
	if err := os.MkdirAll(filepath.Join(cfg.stateDir, "guests"), 0o755); err != nil {
		return nil, err
	}
	return &agent{cfg: cfg, net: newNetworks(), guests: map[string]*guest{}}, nil
}

type guest struct {
	spec    guestSpec
	dir     string
	agent   *agent
	address netip.Prefix

	mu        sync.Mutex
	status    string
	exitCode  int
	err       string
	startedAt time.Time
	stoppedAt time.Time
	rootfs    bool
	tap       string
	cmd       *exec.Cmd
	exited    chan struct{}
	exitOnce  sync.Once
	disks     []string
	console   *console
}

func (a *agent) list() []guestState {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]guestState, 0, len(a.guests))
	for _, g := range a.guests {
		out = append(out, g.state())
	}
	return out
}

func (a *agent) get(id string) (*guest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.guests[id]
	return g, ok
}

func (a *agent) create(spec guestSpec) (*guest, error) {
	if spec.ID == "" || !validID(spec.ID) {
		return nil, errors.New("a guest id of letters, digits, dots and dashes is required")
	}
	if len(spec.Command) == 0 {
		return nil, errors.New("a command is required")
	}
	address, err := netip.ParsePrefix(spec.Network.Address)
	if err != nil {
		return nil, fmt.Errorf("network address %q is required in CIDR form: %w", spec.Network.Address, err)
	}
	if _, err := netip.ParseAddr(spec.Network.Gateway); err != nil {
		return nil, fmt.Errorf("gateway %q: %w", spec.Network.Gateway, err)
	}
	if _, err := net.ParseMAC(spec.Network.MAC); err != nil {
		return nil, fmt.Errorf("mac %q: %w", spec.Network.MAC, err)
	}
	if _, err := netip.ParseAddr(spec.Network.AgentAddress); err != nil {
		return nil, fmt.Errorf("agent address %q: %w", spec.Network.AgentAddress, err)
	}
	if spec.VCPUs <= 0 {
		spec.VCPUs = 1
	}
	if spec.MemoryMiB <= 0 {
		spec.MemoryMiB = 512
	}
	if spec.DiskSlots <= 0 {
		spec.DiskSlots = 4
	}
	if spec.StopGrace <= 0 {
		spec.StopGrace = 10 * time.Second
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.guests[spec.ID]; exists {
		return nil, errGuestExists
	}
	dir := filepath.Join(a.cfg.stateDir, "guests", spec.ID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	g := &guest{spec: spec, dir: dir, agent: a, address: address, status: statusCreating, exited: make(chan struct{}), console: newConsole()}
	a.guests[spec.ID] = g
	return g, nil
}

func (a *agent) remove(ctx context.Context, id string) error {
	a.mu.Lock()
	g, ok := a.guests[id]
	delete(a.guests, id)
	a.mu.Unlock()
	if !ok {
		return errGuestNotFound
	}
	g.stop(ctx)
	g.teardown()
	return os.RemoveAll(g.dir)
}

func (a *agent) stopAll() {
	a.mu.Lock()
	all := make([]*guest, 0, len(a.guests))
	for _, g := range a.guests {
		all = append(all, g)
	}
	a.mu.Unlock()
	for _, g := range all {
		g.stop(context.Background())
		g.teardown()
	}
}

func validID(id string) bool {
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	return len(id) <= 64
}

func (g *guest) state() guestState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return guestState{ID: g.spec.ID, Status: g.status, Labels: g.spec.Labels, Address: g.address.Addr().String(), ExitCode: g.exitCode, Error: g.err, StartedAt: g.startedAt, StoppedAt: g.stoppedAt}
}

// buildRootFS turns the uploaded root filesystem archive — an OCI image's
// filesystem as the engine exports it — into the guest's ext4 image, with
// guestinit, its configuration and the seed files placed in it.
func (g *guest) buildRootFS(ctx context.Context, archive io.Reader) error {
	g.mu.Lock()
	if g.status != statusCreating {
		g.mu.Unlock()
		return errGuestState
	}
	g.mu.Unlock()
	tree := filepath.Join(g.dir, "rootfs")
	_ = os.RemoveAll(tree)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return err
	}
	if err := extractTar(archive, tree); err != nil {
		return fmt.Errorf("extract root filesystem: %w", err)
	}
	initBytes, err := os.ReadFile(g.agent.cfg.guestInit)
	if err != nil {
		return err
	}
	files := append([]seedFile{
		{Path: "/sbin/sockerless-init", Mode: 0o755, Data: initBytes},
		{Path: "/etc/sockerless/guest.json", Mode: 0o644, Data: g.guestConfig()},
	}, g.spec.Seed...)
	for _, f := range files {
		target := filepath.Join(tree, filepath.FromSlash(strings.TrimPrefix(f.Path, "/")))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(target, f.Data, mode); err != nil {
			return err
		}
	}
	for _, dir := range []string{"proc", "sys", "dev", "tmp", "run", "etc"} {
		_ = os.MkdirAll(filepath.Join(tree, dir), 0o755)
	}
	image := filepath.Join(g.dir, "rootfs.ext4")
	if err := createExt4(ctx, tree, image); err != nil {
		return err
	}
	_ = os.RemoveAll(tree)
	g.mu.Lock()
	g.rootfs = true
	g.mu.Unlock()
	return nil
}

func (g *guest) guestConfig() []byte {
	cfg := map[string]any{
		"hostname":   g.spec.Hostname,
		"address":    g.address.Addr().String(),
		"hosts":      g.hosts(),
		"command":    g.spec.Command,
		"args":       g.spec.Args,
		"env":        g.spec.Env,
		"workingDir": g.spec.WorkingDir,
		"user":       g.spec.User,
		"dns":        g.spec.Network.DNS,
		"stopGrace":  g.spec.StopGrace,
	}
	data, _ := json.Marshal(cfg)
	return data
}

// hosts are the guest's extra hosts entries with the engine's host-gateway
// name, which stands for the network's gateway, resolved to it.
func (g *guest) hosts() []string {
	out := make([]string, 0, len(g.spec.Hosts))
	for _, entry := range g.spec.Hosts {
		name, address, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		if address == "host-gateway" {
			address = g.spec.Network.Gateway
		}
		out = append(out, name+":"+address)
	}
	return out
}

// extractTar unpacks an archive into dir, keeping owners and modes, refusing
// a member that would land outside dir.
func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	var links []*tar.Header
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean("/" + hdr.Name)
		if name == "/" {
			continue
		}
		target := filepath.Join(dir, name)
		if !strings.HasPrefix(target, dir+string(os.PathSeparator)) {
			return fmt.Errorf("archive member %q escapes the root", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&os.ModePerm); err != nil {
				return err
			}
			_ = os.Chmod(target, os.FileMode(hdr.Mode)&os.ModePerm)
			_ = os.Lchown(target, hdr.Uid, hdr.Gid)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&os.ModePerm)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			_ = os.Chmod(target, os.FileMode(hdr.Mode)&os.ModePerm|setBits(hdr.Mode))
			_ = os.Lchown(target, hdr.Uid, hdr.Gid)
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			_ = os.Lchown(target, hdr.Uid, hdr.Gid)
		case tar.TypeLink:
			links = append(links, hdr)
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// Device nodes come from devtmpfs at boot; the image's are left out.
		}
	}
	for _, hdr := range links {
		target := filepath.Join(dir, filepath.Clean("/"+hdr.Name))
		source := filepath.Join(dir, filepath.Clean("/"+hdr.Linkname))
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		_ = os.Remove(target)
		if err := os.Link(source, target); err != nil {
			return err
		}
	}
	return nil
}

func setBits(mode int64) os.FileMode {
	var out os.FileMode
	if mode&0o4000 != 0 {
		out |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		out |= os.ModeSetgid
	}
	return out
}

// createExt4 writes the tree into a fresh ext4 image sized to hold it with
// room to grow.
func createExt4(ctx context.Context, tree, image string) error {
	out, err := exec.CommandContext(ctx, "du", "-sk", tree).Output()
	if err != nil {
		return fmt.Errorf("size root filesystem: %w", err)
	}
	fields := strings.Fields(string(out))
	usedKB, _ := strconv.ParseUint(fields[0], 10, 64)
	const mib = uint64(1 << 20)
	size := usedKB*1024*2 + 256*mib
	if size < 1024*mib {
		size = 1024 * mib
	}
	size += 64*mib - size%(64*mib)
	f, err := os.OpenFile(image, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(size)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-d", tree, image).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// start boots the guest: a tap bridged onto the agent's interface on the
// network, the filter at the tap, the Firecracker process over its API, and
// the wait for guestinit to answer over vsock.
func (g *guest) start(ctx context.Context) error {
	g.mu.Lock()
	if g.status == statusRunning || !g.rootfs {
		g.mu.Unlock()
		return fmt.Errorf("%w: the guest needs a root filesystem and must not be running", errGuestState)
	}
	if g.status == statusExited {
		// A kept guest boots again on its root filesystem, with a fresh exit
		// to wait on.
		g.exited = make(chan struct{})
		g.exitOnce = sync.Once{}
		g.exitCode, g.err, g.stoppedAt = 0, "", time.Time{}
		g.disks = nil
	}
	g.mu.Unlock()
	tap, err := g.agent.net.attachTap(ctx, g.spec.ID, g.spec.Network.AgentAddress)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.tap = tap
	g.mu.Unlock()
	if err := g.applyIngress(ctx, g.spec.Ingress); err != nil {
		g.teardown()
		return err
	}
	for i := 0; i < g.spec.DiskSlots; i++ {
		path := filepath.Join(g.dir, fmt.Sprintf("disk%d.img", i))
		if _, err := os.Stat(path); err != nil {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				g.teardown()
				return err
			}
			if err := os.Truncate(path, 1<<20); err != nil {
				g.teardown()
				return err
			}
		}
		g.disks = append(g.disks, path)
	}
	cmd, err := g.launchFirecracker(ctx, tap)
	if err != nil {
		g.teardown()
		return err
	}
	g.mu.Lock()
	g.cmd = cmd
	g.status = statusRunning
	g.startedAt = time.Now().UTC()
	g.mu.Unlock()
	go g.watchProcess(cmd)
	go g.listenExit()
	go g.followGuestConsole(g.console.beginBoot())
	if err := g.waitForInit(ctx); err != nil {
		g.stop(context.Background())
		g.teardown()
		return err
	}
	return nil
}

// watchProcess records the machine's end when the Firecracker process ends,
// which follows the guest's power-off or a stop.
func (g *guest) watchProcess(cmd *exec.Cmd) {
	_ = cmd.Wait()
	g.markExited(-1, "")
}

func (g *guest) markExited(code int, message string) {
	g.exitOnce.Do(func() {
		g.mu.Lock()
		if code >= 0 {
			g.exitCode = code
		} else if g.status == statusRunning && g.err == "" && g.exitCode == 0 {
			g.exitCode = 255
			g.err = "the machine stopped before its first process reported an exit"
		}
		if message != "" {
			g.err = message
		}
		g.status = statusExited
		g.stoppedAt = time.Now().UTC()
		g.mu.Unlock()
		close(g.exited)
	})
}

// listenExit takes the exit report guestinit sends over vsock before it powers
// the machine off; the report lands before the process ends, so the exit code
// is the first process's and not the machine's.
func (g *guest) listenExit() {
	l, err := net.Listen("unix", g.exitSocket())
	if err != nil {
		return
	}
	defer func() { _ = l.Close() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		var report struct {
			ExitCode int    `json:"exitCode"`
			Error    string `json:"error"`
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		err = json.NewDecoder(conn).Decode(&report)
		_ = conn.Close()
		if err == nil {
			g.mu.Lock()
			g.exitCode, g.err = report.ExitCode, report.Error
			g.mu.Unlock()
			return
		}
	}
}

func (g *guest) waitForInit(ctx context.Context) error {
	deadline, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		req, _ := http.NewRequestWithContext(deadline, http.MethodGet, "http://guest/ping", nil)
		resp, err := g.vsockClient().Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-g.exited:
			return fmt.Errorf("the machine stopped before guestinit answered: %s", g.consoleTail())
		case <-deadline.Done():
			return fmt.Errorf("guestinit did not answer within two minutes: %s", g.consoleTail())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (g *guest) consoleTail() string {
	data, err := os.ReadFile(filepath.Join(g.dir, "console.log"))
	if err != nil {
		return ""
	}
	if len(data) > 4096 {
		data = data[len(data)-4096:]
	}
	return strings.TrimSpace(string(data))
}

func (g *guest) exec(ctx context.Context, body []byte) ([]byte, error) {
	g.mu.Lock()
	running := g.status == statusRunning
	g.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("%w: the guest is not running", errGuestState)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/exec", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.vsockClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("exec in guest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exec in guest: %s", strings.TrimSpace(string(out)))
	}
	return out, nil
}

// stop asks guestinit to end the first process within its grace, then gives
// the machine the grace plus a margin to power off, and kills it after.
func (g *guest) stop(ctx context.Context) {
	g.mu.Lock()
	cmd := g.cmd
	running := g.status == statusRunning
	g.mu.Unlock()
	if !running || cmd == nil {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/stop", nil)
	if resp, err := g.vsockClient().Do(req); err == nil {
		_ = resp.Body.Close()
	}
	select {
	case <-g.exited:
		return
	case <-time.After(g.spec.StopGrace + 15*time.Second):
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-g.exited:
	case <-time.After(10 * time.Second):
		g.markExited(-1, "the machine did not stop")
	}
}

func (g *guest) teardown() {
	g.mu.Lock()
	tap := g.tap
	g.tap = ""
	g.mu.Unlock()
	if tap != "" {
		g.agent.net.detachTap(tap)
	}
	_ = os.Remove(g.exitSocket())
}

func (g *guest) applyIngress(ctx context.Context, rules []packetRule) error {
	g.mu.Lock()
	tap := g.tap
	g.spec.Ingress = rules
	g.mu.Unlock()
	if tap == "" {
		return nil
	}
	return g.agent.net.applyIngress(ctx, tap, rules)
}

// attachDisk gives a slot a block device of the size asked for; Firecracker
// takes a new backing file for a drive it already holds, which is why every
// slot exists from boot.
func (g *guest) attachDisk(ctx context.Context, slot string, sizeMiB int) error {
	n, err := strconv.Atoi(slot)
	if err != nil || n < 0 || n >= len(g.disks) {
		return fmt.Errorf("disk slot %q is not one of the guest's %d", slot, len(g.disks))
	}
	if sizeMiB <= 0 {
		return errors.New("a disk size in MiB is required")
	}
	path := g.disks[n]
	if err := os.Truncate(path, int64(sizeMiB)<<20); err != nil {
		return err
	}
	return g.patchDrive(ctx, fmt.Sprintf("disk%d", n), path)
}

func (g *guest) exitSocket() string {
	return filepath.Join(g.dir, fmt.Sprintf("v.sock_%d", exitPort))
}
