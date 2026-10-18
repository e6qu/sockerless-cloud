package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	Extra      []guestNetwork    `json:"extraNetworks"`
	VCPUs      int               `json:"vcpus"`
	MemoryMiB  int               `json:"memoryMiB"`
	Seed       []seedFile        `json:"seed"`
	StopGrace  time.Duration     `json:"stopGrace"`
	Ingress    []packetRule      `json:"ingress"`
	DiskSlots  int               `json:"diskSlots"`
	Mounts     []shareMount      `json:"mounts"`
}

// shareMount is a file share guestinit mounts at Path: an NFSv4 export at
// Server (host:/export) on Port.
type shareMount struct {
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly"`
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
	Gateway   string            `json:"gateway,omitempty"`
	ExitCode  int               `json:"exitCode"`
	Error     string            `json:"error,omitempty"`
	StartedAt time.Time         `json:"startedAt"`
	ExitedAt  time.Time         `json:"exitedAt,omitempty"`
	StoppedAt time.Time         `json:"stoppedAt"`
	Members   []memberState     `json:"members,omitempty"`
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
	exitedAt  time.Time
	stoppedAt time.Time
	rootfs    bool
	tap       string
	// extraTaps are the taps of the guest's further interfaces, in order.
	extraTaps []string
	cmd       *exec.Cmd
	// exited closes when the machine has stopped; processDone when the
	// first process has ended, which the machine outlives while members run.
	exited       chan struct{}
	exitOnce     sync.Once
	processDone  chan struct{}
	processOnce  sync.Once
	exitListener net.Listener
	disks        []string
	usedSlots    map[int]bool
	members      map[string]*member
	memberOrder  []string
	console      *console
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
	for i, extra := range spec.Extra {
		if _, err := netip.ParsePrefix(extra.Address); err != nil {
			return nil, fmt.Errorf("interface eth%d: address %q: %w", i+1, extra.Address, err)
		}
		if _, err := net.ParseMAC(extra.MAC); err != nil {
			return nil, fmt.Errorf("interface eth%d: mac %q: %w", i+1, extra.MAC, err)
		}
		if _, err := netip.ParseAddr(extra.AgentAddress); err != nil {
			return nil, fmt.Errorf("interface eth%d: agent address %q: %w", i+1, extra.AgentAddress, err)
		}
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
	g := &guest{spec: spec, dir: dir, agent: a, address: address, status: statusCreating, exited: make(chan struct{}), processDone: make(chan struct{}), usedSlots: map[int]bool{}, members: map[string]*member{}, console: newConsole()}
	a.guests[spec.ID] = g
	return g, nil
}

func (a *agent) remove(ctx context.Context, id string) error {
	log.Printf("nodeagent: guest %s: delete requested", id)
	a.mu.Lock()
	g, ok := a.guests[id]
	delete(a.guests, id)
	a.mu.Unlock()
	if !ok {
		return errGuestNotFound
	}
	g.stop(ctx, true)
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
		g.stop(context.Background(), true)
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
	state := guestState{ID: g.spec.ID, Status: g.status, Labels: g.spec.Labels, Gateway: g.spec.Network.Gateway,
		Address: g.address.Addr().String(), ExitCode: g.exitCode, Error: g.err, StartedAt: g.startedAt, ExitedAt: g.exitedAt, StoppedAt: g.stoppedAt}
	for _, name := range g.memberOrder {
		state.Members = append(state.Members, g.members[name].state())
	}
	return state
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
	initBytes, err := os.ReadFile(g.agent.cfg.guestInit)
	if err != nil {
		return err
	}
	files := append([]seedFile{
		{Path: "/sbin/sockerless-init", Mode: 0o755, Data: initBytes},
		{Path: "/etc/sockerless/guest.json", Mode: 0o644, Data: g.guestConfig()},
	}, g.spec.Seed...)
	if err := buildImage(ctx, archive, filepath.Join(g.dir, "rootfs"), filepath.Join(g.dir, "rootfs.ext4"), files); err != nil {
		return err
	}
	g.mu.Lock()
	g.rootfs = true
	g.mu.Unlock()
	return nil
}

// buildImage extracts a root filesystem archive into tree, writes files into
// it, gives it the mount points a running system needs, and packs it into
// the ext4 image.
func buildImage(ctx context.Context, archive io.Reader, tree, image string, files []seedFile) error {
	_ = os.RemoveAll(tree)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tree) }()
	if err := extractTar(archive, tree); err != nil {
		return fmt.Errorf("extract root filesystem: %w", err)
	}
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
	return createExt4(ctx, tree, image)
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
		"mounts":     g.spec.Mounts,
		"interfaces": g.interfaces(),
	}
	data, _ := json.Marshal(cfg)
	return data
}

// interfaces are the guest's further interfaces, eth1 onwards, with the
// address each takes; eth0 is configured by the kernel's command line.
func (g *guest) interfaces() []map[string]string {
	out := make([]map[string]string, 0, len(g.spec.Extra))
	for i, extra := range g.spec.Extra {
		out = append(out, map[string]string{"name": fmt.Sprintf("eth%d", i+1), "address": extra.Address})
	}
	return out
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
		// to wait on; the members of its last boot are gone with it.
		g.exited = make(chan struct{})
		g.exitOnce = sync.Once{}
		g.processDone = make(chan struct{})
		g.processOnce = sync.Once{}
		g.exitCode, g.err, g.exitedAt, g.stoppedAt = 0, "", time.Time{}, time.Time{}
		g.disks = nil
		g.usedSlots = map[int]bool{}
		g.members = map[string]*member{}
		g.memberOrder = nil
	}
	g.mu.Unlock()
	tap, err := g.agent.net.attachTap(ctx, g.spec.ID, g.spec.Network.AgentAddress)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.tap = tap
	g.mu.Unlock()
	var extraTaps []string
	for i, extra := range g.spec.Extra {
		extraTap, err := g.agent.net.attachTap(ctx, fmt.Sprintf("%s-%d", g.spec.ID, i+1), extra.AgentAddress)
		if err != nil {
			g.teardown()
			return err
		}
		extraTaps = append(extraTaps, extraTap)
		g.mu.Lock()
		g.extraTaps = extraTaps
		g.mu.Unlock()
	}
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
	cmd, err := g.launchFirecracker(ctx, tap, extraTaps)
	if err != nil {
		g.teardown()
		return err
	}
	g.mu.Lock()
	g.cmd = cmd
	g.status = statusRunning
	g.startedAt = time.Now().UTC()
	g.mu.Unlock()
	boot, drained := g.console.beginBoot()
	go g.watchProcess(cmd)
	go g.listenExit(boot)
	go g.followGuestConsole(boot, drained)
	if err := g.waitForInit(ctx); err != nil {
		g.stop(context.Background(), true)
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
		} else if g.status == statusRunning && g.err == "" && g.exitCode == 0 && g.exitedAt.IsZero() {
			g.exitCode = 255
			g.err = "the machine stopped before its first process reported an exit"
		}
		if message != "" {
			g.err = message
		}
		g.status = statusExited
		g.stoppedAt = time.Now().UTC()
		if g.exitedAt.IsZero() {
			g.exitedAt = g.stoppedAt
		}
		log.Printf("nodeagent: guest %s: machine stopped (exit %d %s)", g.spec.ID, g.exitCode, g.err)
		members := make([]*member, 0, len(g.members))
		for _, m := range g.members {
			members = append(members, m)
		}
		listener := g.exitListener
		g.exitListener = nil
		g.mu.Unlock()
		for _, m := range members {
			m.markExited(255, "the machine stopped before the member reported an exit")
		}
		g.processOnce.Do(func() { close(g.processDone) })
		if listener != nil {
			_ = listener.Close()
		}
		close(g.exited)
	})
}

// exitReport is what guestinit sends over vsock when a process ends: the
// first process when Member is empty, with how many console lines it wrote,
// so the agent knows when it has read them all.
type exitReport struct {
	Member   string `json:"member"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error"`
	Lines    int    `json:"lines"`
}

// listenExit takes the exit reports guestinit sends over vsock: one per
// process, the first process's before the machine powers off, so the exit
// code is the process's and not the machine's.
func (g *guest) listenExit(boot int) {
	l, err := net.Listen("unix", g.exitSocket())
	if err != nil {
		return
	}
	g.mu.Lock()
	g.exitListener = l
	g.mu.Unlock()
	defer func() { _ = l.Close() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		var report exitReport
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		err = json.NewDecoder(conn).Decode(&report)
		_ = conn.Close()
		if err != nil {
			continue
		}
		g.console.processEnded(boot, report.Member, report.Lines)
		log.Printf("nodeagent: guest %s: process %q exited %d %s", g.spec.ID, report.Member, report.ExitCode, report.Error)
		if report.Member == "" {
			g.mu.Lock()
			g.exitCode, g.err = report.ExitCode, report.Error
			g.exitedAt = time.Now().UTC()
			g.mu.Unlock()
			g.processOnce.Do(func() { close(g.processDone) })
			continue
		}
		g.mu.Lock()
		m := g.members[report.Member]
		g.mu.Unlock()
		if m != nil {
			m.markExited(report.ExitCode, report.Error)
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

// stop asks guestinit to end the first process within its grace, or every
// process when all is set, then gives the machine the grace plus a margin to
// power off, and kills it after. A machine whose members still run outlives
// its first process, so a stop of that process alone waits for the process
// and not the machine.
func (g *guest) stop(ctx context.Context, all bool) {
	g.mu.Lock()
	cmd := g.cmd
	running := g.status == statusRunning
	g.mu.Unlock()
	if !running || cmd == nil {
		return
	}
	log.Printf("nodeagent: guest %s: stop requested (all=%t, members running=%t)", g.spec.ID, all, g.membersRunning())
	target := "http://guest/stop"
	if all {
		target += "?all=1"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if resp, err := g.vsockClient().Do(req); err == nil {
		_ = resp.Body.Close()
	}
	if !all {
		select {
		case <-g.processDone:
			if g.membersRunning() {
				return
			}
			// The power-off follows the last process's exit.
			select {
			case <-g.exited:
				return
			case <-time.After(15 * time.Second):
			}
		case <-time.After(g.spec.StopGrace + 15*time.Second):
			if g.membersRunning() {
				return
			}
		}
	} else {
		select {
		case <-g.exited:
			return
		case <-time.After(g.stopGrace() + 15*time.Second):
		}
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
	extraTaps := g.extraTaps
	g.extraTaps = nil
	listener := g.exitListener
	g.exitListener = nil
	g.mu.Unlock()
	if tap != "" {
		log.Printf("nodeagent: guest %s: tap %s detached", g.spec.ID, tap)
		g.agent.net.detachTap(tap)
	}
	for _, extraTap := range extraTaps {
		g.agent.net.detachTap(extraTap)
	}
	if listener != nil {
		_ = listener.Close()
	}
	_ = os.Remove(g.exitSocket())
}

// stopGrace is the longest grace any of the guest's processes gets.
func (g *guest) stopGrace() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	grace := g.spec.StopGrace
	for _, m := range g.members {
		if m.spec.StopGrace > grace {
			grace = m.spec.StopGrace
		}
	}
	return grace
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
	g.mu.Lock()
	if holder := g.members[g.slotHolder(n)]; holder != nil {
		g.mu.Unlock()
		return fmt.Errorf("disk slot %q carries member %s", slot, holder.spec.Name)
	}
	g.usedSlots[n] = true
	g.mu.Unlock()
	path := g.disks[n]
	if err := os.Truncate(path, int64(sizeMiB)<<20); err != nil {
		return err
	}
	return g.patchDrive(ctx, fmt.Sprintf("disk%d", n), path)
}

func (g *guest) exitSocket() string {
	return filepath.Join(g.dir, fmt.Sprintf("v.sock_%d", exitPort))
}
