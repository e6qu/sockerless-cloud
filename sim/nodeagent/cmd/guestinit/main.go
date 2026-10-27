// Command guestinit is the first process of a Firecracker guest the node
// agent boots. It mounts the kernel filesystems, names the host, points the
// resolver at the cloud's, runs the workload the guest exists for as its
// child, and serves the agent over vsock: the workload's output, a command to
// run inside the guest, a stop, and the members the agent adds while the
// guest runs, each from a root filesystem of its own on a block device. When
// the workload and every member have ended it reports the exits to the agent
// and powers the machine off, as a machine whose init has nothing left to
// run does.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	configPath  = "/etc/sockerless/guest.json"
	controlPort = 5000
	exitPort    = 5001
	hostCID     = 2
)

type guestConfig struct {
	Hostname   string            `json:"hostname"`
	Address    string            `json:"address"`
	Hosts      []string          `json:"hosts"`
	Command    []string          `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	DNS        []string          `json:"dns"`
	StopGrace  time.Duration     `json:"stopGrace"`
	Mounts     []shareMount      `json:"mounts"`
	Interfaces []guestInterface  `json:"interfaces"`
}

// shareMount is a file share the guest mounts at Path over its network: an
// NFSv4 export at Server (host:/export) on Port.
type shareMount struct {
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly"`
	File     string `json:"file"`
}

type logLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
	Member string    `json:"member,omitempty"`
}

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sockerless-init: %v\n", err)
	}
	powerOff(code)
}

// run boots the guest's userspace and returns the workload's exit code once
// it has ended, or 1 with the error that kept it from starting.
func run() (int, error) {
	mountKernelFilesystems()
	cfg, err := readConfig()
	if err != nil {
		return 1, err
	}
	if cfg.Hostname != "" {
		_ = unix.Sethostname([]byte(cfg.Hostname))
		_ = os.WriteFile("/etc/hostname", []byte(cfg.Hostname+"\n"), 0o644)
	}
	writeResolvConf(cfg.DNS)
	_ = os.WriteFile("/etc/hosts", []byte(hostsFile(cfg)), 0o644)
	if err := configureInterfaces(cfg.Interfaces); err != nil {
		reportExit("", 127, err.Error(), 0)
		return 127, err
	}
	if err := mountShares(cfg.Mounts, "/", cfg.Address); err != nil {
		reportExit("", 127, err.Error(), 0)
		return 127, err
	}
	logs := newLogBook()
	first := newProcess("", "", processSpec{
		Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, WorkingDir: cfg.WorkingDir, User: cfg.User, StopGrace: cfg.StopGrace,
	}, logs)
	children := make(chan os.Signal, 1)
	signal.Notify(children, syscall.SIGCHLD)
	go exits.reap(children)
	if err := first.start(); err != nil {
		reportExit("", 127, err.Error(), 0)
		return 127, err
	}
	group := newMembers(first, logs, cfg.Address)
	go serveControl(group)
	result := first.wait()
	reportExit("", result.code, result.message, logs.count(""))
	group.closeToNew()
	group.waitAll()
	close(group.done)
	// The console lives in this guest's memory alone until the agent has
	// read it to its end; powering off first would lose what it has not.
	<-logs.read
	return result.code, nil
}

func mountKernelFilesystems() {
	for _, m := range []struct{ source, target, fstype string }{
		{"proc", "/proc", "proc"},
		{"sysfs", "/sys", "sysfs"},
		{"devtmpfs", "/dev", "devtmpfs"},
		{"tmpfs", "/tmp", "tmpfs"},
		{"tmpfs", "/run", "tmpfs"},
	} {
		_ = os.MkdirAll(m.target, 0o755)
		_ = unix.Mount(m.source, m.target, m.fstype, 0, "")
	}
	_ = os.MkdirAll("/dev/pts", 0o755)
	_ = unix.Mount("devpts", "/dev/pts", "devpts", 0, "")
}

// mountShares mounts each file share under root with the kernel's NFSv4
// client, the one network filesystem the guest kernel carries: version 4.0
// over TCP at the share's port, AUTH_SYS, from the guest's own address.
func mountShares(mounts []shareMount, root, address string) error {
	for i, m := range mounts {
		host, _, ok := strings.Cut(m.Server, ":")
		if !ok || host == "" || m.Port == 0 || m.Path == "" {
			return fmt.Errorf("mount %+v names no server, port or path", m)
		}
		target := filepath.Join(root, m.Path)
		if m.File != "" {
			// Mount a share of one file out of the way and bind that file
			// over the path, as a bind of one file does.
			target = filepath.Join(root, sharesRoot, strconv.Itoa(i))
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		var flags uintptr
		if m.ReadOnly {
			flags = unix.MS_RDONLY
		}
		data := fmt.Sprintf("addr=%s,clientaddr=%s,vers=4.0,proto=tcp,port=%d,sec=sys,hard,timeo=600,retrans=2", host, address, m.Port)
		if err := unix.Mount(m.Server, target, "nfs4", flags, data); err != nil {
			return fmt.Errorf("mount %s at %s: %w", m.Server, target, err)
		}
		if m.File != "" {
			if err := bindFile(filepath.Join(target, m.File), filepath.Join(root, m.Path), m.ReadOnly); err != nil {
				return err
			}
		}
	}
	return nil
}

// sharesRoot holds the shares of single files, each bound over its path.
const sharesRoot = "/run/sockerless/shares"

// bindFile binds the file at source over path, which it creates empty when
// nothing is there, read-only when the share is.
func bindFile(source, path string, readOnly bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_ = f.Close()
	if err := unix.Mount(source, path, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind %s over %s: %w", source, path, err)
	}
	if readOnly {
		if err := unix.Mount("", path, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("bind %s read-only: %w", path, err)
		}
	}
	return nil
}

func readConfig() (guestConfig, error) {
	var cfg guestConfig
	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", configPath, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", configPath, err)
	}
	if len(cfg.Command) == 0 {
		return cfg, fmt.Errorf("%s names no command", configPath)
	}
	return cfg, nil
}

func writeResolvConf(dns []string) {
	var b strings.Builder
	for _, server := range dns {
		fmt.Fprintf(&b, "nameserver %s\n", server)
	}
	_ = os.Remove("/etc/resolv.conf")
	_ = os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644)
}

// hostsFile is the guest's /etc/hosts: the loopback names, the guest's own
// name at its address, and the entries the workload was given, as the engine
// writes them for a container.
func hostsFile(cfg guestConfig) string {
	var b strings.Builder
	b.WriteString("127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n")
	if cfg.Hostname != "" && cfg.Address != "" {
		fmt.Fprintf(&b, "%s\t%s\n", cfg.Address, cfg.Hostname)
	}
	for _, entry := range cfg.Hosts {
		name, address, ok := strings.Cut(entry, ":")
		if !ok || name == "" || address == "" {
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\n", address, name)
	}
	return b.String()
}

// logBook keeps the workload's output for the agent to read, and lets a
// follower receive lines as they arrive.
type logBook struct {
	mu        sync.Mutex
	lines     []logLine
	followers map[chan logLine]struct{}
	// read closes once the agent reports it holds every line.
	read     chan struct{}
	readOnce sync.Once
}

func newLogBook() *logBook {
	return &logBook{followers: map[chan logLine]struct{}{}, read: make(chan struct{})}
}

func (b *logBook) markRead() { b.readOnce.Do(func() { close(b.read) }) }

func (b *logBook) add(member, stream, text string) {
	line := logLine{Time: time.Now().UTC(), Stream: stream, Text: text, Member: member}
	b.mu.Lock()
	b.lines = append(b.lines, line)
	for ch := range b.followers {
		select {
		case ch <- line:
		default:
		}
	}
	b.mu.Unlock()
	fmt.Fprintf(os.Stdout, "%s\n", text)
}

// count is how many lines a process has written so far, which its exit
// report carries so the agent knows when it has read them all.
func (b *logBook) count(member string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, line := range b.lines {
		if line.Member == member {
			n++
		}
	}
	return n
}

func (b *logBook) follow() (past []logLine, ch chan logLine, stop func()) {
	ch = make(chan logLine, 1024)
	b.mu.Lock()
	past = append([]logLine(nil), b.lines...)
	b.followers[ch] = struct{}{}
	b.mu.Unlock()
	return past, ch, func() {
		b.mu.Lock()
		delete(b.followers, ch)
		b.mu.Unlock()
	}
}

type exitResult struct {
	code    int
	message string
}

// processSpec is what a process runs: the first process from the guest's
// configuration, a member from the agent's request.
type processSpec struct {
	Command    []string          `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	StopGrace  time.Duration     `json:"stopGrace"`
}

// process is one process tree the guest runs: the first process in the
// guest's own root, or a member, named, in the root mounted at root.
type process struct {
	name   string
	root   string
	spec   processSpec
	logs   *logBook
	cmd    *exec.Cmd
	done   chan struct{}
	result exitResult
}

func newProcess(name, root string, spec processSpec, logs *logBook) *process {
	return &process{name: name, root: root, spec: spec, logs: logs, done: make(chan struct{})}
}

func (p *process) start() error {
	env := environ(p.spec.Env, p.spec.User, p.root)
	cmd, err := p.command(p.spec.Command, p.spec.Args, env)
	if err != nil {
		return err
	}
	cmd.Dir = p.spec.WorkingDir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	pid, err := exits.start(cmd)
	if err != nil {
		return fmt.Errorf("start %s: %w", p.spec.Command[0], err)
	}
	p.cmd = cmd
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); p.copyLines("stdout", stdout); _ = stdout.Close() }()
	go func() { defer pipes.Done(); p.copyLines("stderr", stderr); _ = stderr.Close() }()
	go func() {
		// The process's exit ends its tree, as a container ends when its
		// first process does: the children it left behind would otherwise
		// hold its output open and run on unowned.
		status := exits.wait(pid)
		killGroup(pid)
		pipes.Wait()
		_ = cmd.Process.Release()
		p.result = exitResult{code: exitCode(status)}
		close(p.done)
	}()
	return nil
}

// killGroup kills every process left in the group a process led.
func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// command builds the exec.Cmd for argv in the process's root: the program
// resolves on PATH inside that root, and a member's command runs chrooted
// into it as the user its own passwd file names.
func (p *process) command(command, args []string, env []string) (*exec.Cmd, error) {
	if len(command) == 0 {
		return nil, errors.New("a command is required")
	}
	path, err := lookPath(command[0], pathOf(env), p.root)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{Path: path, Args: append(append([]string{command[0]}, command[1:]...), args...), Env: env}
	// Each process leads a group of its own, so its tree is one thing to
	// end.
	attr := &syscall.SysProcAttr{Setpgid: true}
	if p.root != "" {
		attr.Chroot = p.root
	}
	if p.spec.User != "" {
		credential, err := parseUser(p.spec.User, p.root)
		if err != nil {
			return nil, err
		}
		attr.Credential = credential
	}
	cmd.SysProcAttr = attr
	return cmd, nil
}

// lookPath finds program on the PATH entries under root, as the kernel will
// see it once the process has entered root, and returns its path inside
// root.
func lookPath(program, path, root string) (string, error) {
	executable := func(candidate string) bool {
		info, err := statInRoot(root, candidate)
		return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	}
	if strings.Contains(program, "/") {
		if executable(program) {
			return program, nil
		}
		return "", fmt.Errorf("%s: no such executable", program)
	}
	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, program)
		if executable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: not found on PATH", program)
}

// statInRoot stats path as a process rooted at root sees it: a symbolic
// link along the way resolves inside root, an absolute one from root and
// not from this process's own filesystem.
func statInRoot(root, path string) (os.FileInfo, error) {
	if root == "" {
		return os.Stat(path)
	}
	resolved, err := resolveInRoot(root, path, 0)
	if err != nil {
		return nil, err
	}
	return os.Stat(filepath.Join(root, resolved))
}

func resolveInRoot(root, path string, depth int) (string, error) {
	if depth > 40 {
		return "", fmt.Errorf("%s: too many levels of symbolic links", path)
	}
	resolved := "/"
	parts := strings.Split(strings.TrimPrefix(filepath.Clean("/"+path), "/"), "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		next := filepath.Join(resolved, part)
		info, err := os.Lstat(filepath.Join(root, next))
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		target, err := os.Readlink(filepath.Join(root, next))
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(resolved, target)
		}
		rest := strings.Join(parts[i+1:], "/")
		return resolveInRoot(root, filepath.Join(target, rest), depth+1)
	}
	return resolved, nil
}

func pathOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			return v
		}
	}
	return ""
}

// exitCode is the code a shell reports for a child that ended with status.
func exitCode(status syscall.WaitStatus) int {
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return status.ExitStatus()
}

func (p *process) copyLines(stream string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		p.logs.add(p.name, stream, scanner.Text())
	}
}

func (p *process) wait() exitResult {
	<-p.done
	return p.result
}

// stop sends the process SIGTERM and, once the grace has passed, SIGKILL to
// its whole group.
func (p *process) stop() {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(p.spec.StopGrace):
		_ = p.cmd.Process.Kill()
		killGroup(p.cmd.Process.Pid)
	}
}

// environ is a process's environment: its own variables over a PATH, and
// the HOME a container runtime gives a process from its user's passwd entry
// when the image set none, read in the process's root.
func environ(env map[string]string, user, root string) []string {
	out := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	home := false
	for k, v := range env {
		switch k {
		case "PATH":
			out[0] = "PATH=" + v
			continue
		case "HOME":
			home = true
		}
		out = append(out, k+"="+v)
	}
	if !home {
		out = append(out, "HOME="+homeDir(user, filepath.Join(root, "/etc/passwd")))
	}
	return out
}

// homeDir is the home of the user a workload runs as, from the passwd file,
// as runc resolves it: root when the user is unset, and "/" for a user the
// file does not know.
func homeDir(user, file string) string {
	name, _, _ := strings.Cut(user, ":")
	if name == "" {
		name = "0"
	}
	if entry := passwdEntryIn(name, file); entry != nil && entry.home != "" {
		return entry.home
	}
	return "/"
}

// parseUser resolves a uid[:gid] specification against the passwd and group
// files under root.
func parseUser(spec, root string) (*syscall.Credential, error) {
	uidPart, gidPart, _ := strings.Cut(spec, ":")
	uid, err := lookupID(uidPart, filepath.Join(root, "/etc/passwd"))
	if err != nil {
		return nil, err
	}
	gid := uid
	if gidPart != "" {
		if gid, err = lookupID(gidPart, filepath.Join(root, "/etc/group")); err != nil {
			return nil, err
		}
	} else if entry := passwdEntryIn(uidPart, filepath.Join(root, "/etc/passwd")); entry != nil {
		gid = entry.gid
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

type passwd struct {
	name     string
	uid, gid int
	home     string
}

func passwdEntryIn(name, file string) *passwd {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		uid, err1 := strconv.Atoi(fields[2])
		gid, err2 := strconv.Atoi(fields[3])
		if err1 != nil || err2 != nil {
			continue
		}
		if fields[0] == name || fields[2] == name {
			entry := &passwd{name: fields[0], uid: uid, gid: gid}
			if len(fields) >= 6 {
				entry.home = fields[5]
			}
			return entry
		}
	}
	return nil
}

func lookupID(name, file string) (int, error) {
	if n, err := strconv.Atoi(name); err == nil {
		return n, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("user %q: %w", name, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 && fields[0] == name {
			return strconv.Atoi(fields[2])
		}
	}
	return 0, fmt.Errorf("user %q is not in %s", name, file)
}

// exits is init's one reaper. Init collects every child that ends, its own
// and the orphans the workload leaves it, with one wait on any child, so a
// command's own wait would find its child already gone; a command starts
// through the table and takes its status from it instead.
var exits = &exitTable{waiters: map[int]chan syscall.WaitStatus{}}

type exitTable struct {
	mu      sync.Mutex
	waiters map[int]chan syscall.WaitStatus
}

// reap collects every child that has ended at each SIGCHLD and hands a
// status to the waiter registered for its pid; an orphan's status has none.
func (t *exitTable) reap(signals <-chan os.Signal) {
	for range signals {
		for {
			t.mu.Lock()
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if pid > 0 && err == nil {
				if ch, ok := t.waiters[pid]; ok {
					ch <- status
				}
			}
			t.mu.Unlock()
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}

// start starts cmd and registers its pid under the lock the reaper collects
// under, so the child's status cannot be collected before its waiter exists.
func (t *exitTable) start(cmd *exec.Cmd) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	t.waiters[pid] = make(chan syscall.WaitStatus, 1)
	return pid, nil
}

// wait blocks until the reaper has collected pid's status.
func (t *exitTable) wait(pid int) syscall.WaitStatus {
	t.mu.Lock()
	ch := t.waiters[pid]
	t.mu.Unlock()
	status := <-ch
	t.mu.Lock()
	delete(t.waiters, pid)
	t.mu.Unlock()
	return status
}

// run starts cmd, reads what it writes until it closes its output, and
// returns the output with the child's status.
func (t *exitTable) run(ctx context.Context, cmd *exec.Cmd) (stdout, stderr string, status syscall.WaitStatus, err error) {
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", 0, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", "", 0, err
	}
	pid, err := t.start(cmd)
	if err != nil {
		return "", "", 0, err
	}
	stopKill := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	defer stopKill()
	var out, errs []byte
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); out, _ = io.ReadAll(outPipe); _ = outPipe.Close() }()
	go func() { defer pipes.Done(); errs, _ = io.ReadAll(errPipe); _ = errPipe.Close() }()
	status = t.wait(pid)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		killGroup(pid)
	}
	pipes.Wait()
	_ = cmd.Process.Release()
	return string(out), string(errs), status, nil
}

// serveControl answers the agent over vsock.
func serveControl(group *members) {
	listener, err := listenVsock(controlPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sockerless-init: control listener: %v\n", err)
		return
	}
	logs := group.logs
	mux := http.NewServeMux()
	mux.HandleFunc("GET /logs", func(rw http.ResponseWriter, r *http.Request) {
		past, ch, stop := logs.follow()
		defer stop()
		rw.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(rw)
		flush := func() {
			if f, ok := rw.(http.Flusher); ok {
				f.Flush()
			}
		}
		for _, line := range past {
			_ = enc.Encode(line)
		}
		flush()
		if r.URL.Query().Get("follow") == "" {
			return
		}
		for {
			select {
			case line := <-ch:
				_ = enc.Encode(line)
				flush()
			case <-group.done:
				for {
					select {
					case line := <-ch:
						_ = enc.Encode(line)
					default:
						flush()
						return
					}
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	// The agent posts here once a follow of /logs ended at the workload's
	// end, so it holds every line.
	mux.HandleFunc("POST /logs/read", func(rw http.ResponseWriter, r *http.Request) {
		select {
		case <-group.done:
			logs.markRead()
			rw.WriteHeader(http.StatusNoContent)
		default:
			http.Error(rw, "the workload is still running", http.StatusConflict)
		}
	})
	mux.HandleFunc("POST /exec", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Command []string `json:"command"`
			Process string   `json:"process"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Command) == 0 {
			http.Error(rw, "a command is required", http.StatusBadRequest)
			return
		}
		p, ok := group.process(req.Process)
		if !ok {
			http.Error(rw, fmt.Sprintf("no member %q", req.Process), http.StatusNotFound)
			return
		}
		env := environ(p.spec.Env, p.spec.User, p.root)
		code := 126
		var stdout, stderr string
		cmd, err := p.command(req.Command, nil, env)
		if err == nil {
			var status syscall.WaitStatus
			stdout, stderr, status, err = exits.run(r.Context(), cmd)
			if err == nil {
				code = exitCode(status)
			}
		}
		if err != nil {
			stderr = err.Error()
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"stdout": stdout, "stderr": stderr, "exitCode": code})
	})
	mux.HandleFunc("POST /stop", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "" {
			go group.stopAll()
		} else {
			go group.first.stop()
		}
		rw.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /members", func(rw http.ResponseWriter, r *http.Request) {
		var req memberRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		if err := group.add(req); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /members/{name}/stop", func(rw http.ResponseWriter, r *http.Request) {
		p, ok := group.process(r.PathValue("name"))
		if !ok || p == group.first {
			http.Error(rw, fmt.Sprintf("no member %q", r.PathValue("name")), http.StatusNotFound)
			return
		}
		go p.stop()
		rw.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /route", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Via string `json:"via"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		if err := setDefaultRoute("eth0", req.Via); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /freeze", func(rw http.ResponseWriter, r *http.Request) {
		if err := freezeRoot(fiFreeze); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /thaw", func(rw http.ResponseWriter, r *http.Request) {
		if err := freezeRoot(fiThaw); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ping", func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(rw, "ok")
	})
	server := &http.Server{Handler: mux}
	_ = server.Serve(listener)
}

// The FIFREEZE and FITHAW ioctls, _IOWR('X', 119, int) and _IOWR('X', 120,
// int), which golang.org/x/sys/unix does not name.
const (
	fiFreeze = 0xc0045877
	fiThaw   = 0xc0045878
)

// freezeRoot freezes or thaws the root filesystem. A freeze writes every
// dirty block and the whole journal to the disk and holds writers until the
// thaw, so the agent reads a consistent image of a running guest's disk.
func freezeRoot(request uint) error {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("open /: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.IoctlSetPointerInt(fd, request, 0); err != nil {
		return fmt.Errorf("ioctl %#x on /: %w", request, err)
	}
	return nil
}

// reportExit tells the agent how a process ended, the first process when
// member is empty, with how many lines it wrote; the agent may not be
// listening, in which case the console carries the exit.
func reportExit(member string, code int, message string, lines int) {
	if member == "" {
		fmt.Fprintf(os.Stdout, "sockerless-init: workload exited %d %s\n", code, message)
	} else {
		fmt.Fprintf(os.Stdout, "sockerless-init: member %s exited %d %s\n", member, code, message)
	}
	conn, err := dialVsock(hostCID, exitPort)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = json.NewEncoder(conn).Encode(map[string]any{"member": member, "exitCode": code, "error": message, "lines": lines})
}

// powerOff ends the machine. A power-off on a microVM halts its processor
// and leaves the machine running for the hypervisor, which only a restart
// ends: the kernel resets through the keyboard controller (reboot=k on its
// command line) or PSCI, and Firecracker exits on the reset. Should the
// restart not take, the exit panics the kernel, which reboots after a second
// (panic=1) to the same end.
func powerOff(code int) {
	unix.Sync()
	// Drain the serial console, which carries the exit line when the vsock
	// report found no listener, before the reset discards its queue.
	_ = unix.IoctlSetInt(int(os.Stdout.Fd()), unix.TCSBRK, 1)
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
	os.Exit(code)
}

// vsock listener and dialer over AF_VSOCK, which the net package does not
// speak; the fds run non-blocking under the runtime poller through os.File.

type vsockListener struct {
	fd   int
	port uint32
}

func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock bind %d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock listen %d: %w", port, err)
	}
	return &vsockListener{fd: fd, port: port}, nil
}

func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		nfd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("vsock accept: %w", err)
		}
		return newVsockConn(nfd), nil
	}
}

func (l *vsockListener) Close() error { return unix.Close(l.fd) }

func (l *vsockListener) Addr() net.Addr { return vsockAddr{port: l.port} }

type vsockAddr struct {
	cid, port uint32
}

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string  { return fmt.Sprintf("%d:%d", a.cid, a.port) }

type vsockConn struct {
	*os.File
}

func newVsockConn(fd int) *vsockConn {
	_ = unix.SetNonblock(fd, true)
	return &vsockConn{File: os.NewFile(uintptr(fd), "vsock")}
}

func (c *vsockConn) LocalAddr() net.Addr  { return vsockAddr{} }
func (c *vsockConn) RemoteAddr() net.Addr { return vsockAddr{} }

func dialVsock(cid, port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, err)
	}
	return newVsockConn(fd), nil
}

var _ = context.Background
