// Command guestinit is the first process of a Firecracker guest the node
// agent boots. It mounts the kernel filesystems, names the host, points the
// resolver at the cloud's, runs the workload the guest exists for as its
// child, and serves the agent over vsock: the workload's output, a command to
// run inside the guest, and a stop. When the workload ends it reports the
// exit code to the agent and powers the machine off, as a machine whose init
// has nothing left to run does.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
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
}

type logLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
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
	logs := newLogBook()
	workload := newWorkload(cfg, logs)
	children := make(chan os.Signal, 1)
	signal.Notify(children, syscall.SIGCHLD)
	go exits.reap(children)
	if err := workload.start(); err != nil {
		reportExit(127, err.Error())
		return 127, err
	}
	go serveControl(workload, logs)
	result := workload.wait()
	reportExit(result.code, result.message)
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
}

func newLogBook() *logBook { return &logBook{followers: map[chan logLine]struct{}{}} }

func (b *logBook) add(stream, text string) {
	line := logLine{Time: time.Now().UTC(), Stream: stream, Text: text}
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

type workload struct {
	cfg    guestConfig
	logs   *logBook
	cmd    *exec.Cmd
	done   chan struct{}
	result exitResult
}

func newWorkload(cfg guestConfig, logs *logBook) *workload {
	return &workload{cfg: cfg, logs: logs, done: make(chan struct{})}
}

func (w *workload) start() error {
	env := environ(w.cfg.Env, w.cfg.User)
	// A relative command resolves on this process's own PATH before the
	// command's environment applies, and the kernel starts init with none.
	if err := os.Setenv("PATH", strings.TrimPrefix(env[0], "PATH=")); err != nil {
		return err
	}
	cmd := exec.Command(w.cfg.Command[0], append(w.cfg.Command[1:], w.cfg.Args...)...)
	cmd.Env = env
	cmd.Dir = w.cfg.WorkingDir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	if w.cfg.User != "" {
		credential, err := parseUser(w.cfg.User)
		if err != nil {
			return err
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
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
		return fmt.Errorf("start %s: %w", w.cfg.Command[0], err)
	}
	w.cmd = cmd
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); w.copyLines("stdout", stdout); _ = stdout.Close() }()
	go func() { defer pipes.Done(); w.copyLines("stderr", stderr); _ = stderr.Close() }()
	go func() {
		pipes.Wait()
		status := exits.wait(pid)
		_ = cmd.Process.Release()
		w.result = exitResult{code: exitCode(status)}
		close(w.done)
	}()
	return nil
}

// exitCode is the code a shell reports for a child that ended with status.
func exitCode(status syscall.WaitStatus) int {
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return status.ExitStatus()
}

func (w *workload) copyLines(stream string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		w.logs.add(stream, scanner.Text())
	}
}

func (w *workload) wait() exitResult {
	<-w.done
	return w.result
}

// stop sends the workload SIGTERM and, once the grace has passed, SIGKILL.
func (w *workload) stop() {
	if w.cmd == nil || w.cmd.Process == nil {
		return
	}
	_ = w.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
	case <-time.After(w.cfg.StopGrace):
		_ = w.cmd.Process.Kill()
	}
}

// environ is the workload's environment: its own variables over a PATH, and
// the HOME a container runtime gives a process from its user's passwd entry
// when the image set none.
func environ(env map[string]string, user string) []string {
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
		out = append(out, "HOME="+homeDir(user, "/etc/passwd"))
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

func parseUser(spec string) (*syscall.Credential, error) {
	uidPart, gidPart, _ := strings.Cut(spec, ":")
	uid, err := lookupID(uidPart, "/etc/passwd")
	if err != nil {
		return nil, err
	}
	gid := uid
	if gidPart != "" {
		if gid, err = lookupID(gidPart, "/etc/group"); err != nil {
			return nil, err
		}
	} else if entry := passwdEntry(uidPart); entry != nil {
		gid = entry.gid
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

type passwd struct {
	name     string
	uid, gid int
	home     string
}

func passwdEntry(name string) *passwd {
	return passwdEntryIn(name, "/etc/passwd")
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
func (t *exitTable) run(cmd *exec.Cmd) (stdout, stderr string, status syscall.WaitStatus, err error) {
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
	var out, errs []byte
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); out, _ = io.ReadAll(outPipe); _ = outPipe.Close() }()
	go func() { defer pipes.Done(); errs, _ = io.ReadAll(errPipe); _ = errPipe.Close() }()
	pipes.Wait()
	status = t.wait(pid)
	_ = cmd.Process.Release()
	return string(out), string(errs), status, nil
}

// serveControl answers the agent over vsock.
func serveControl(w *workload, logs *logBook) {
	listener, err := listenVsock(controlPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sockerless-init: control listener: %v\n", err)
		return
	}
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
			case <-w.done:
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
	mux.HandleFunc("POST /exec", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Command []string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Command) == 0 {
			http.Error(rw, "a command is required", http.StatusBadRequest)
			return
		}
		cmd := exec.CommandContext(r.Context(), req.Command[0], req.Command[1:]...)
		cmd.Env = environ(w.cfg.Env, w.cfg.User)
		stdout, stderr, status, err := exits.run(cmd)
		code := 126
		if err != nil {
			stderr = err.Error()
		} else {
			code = exitCode(status)
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"stdout": stdout, "stderr": stderr, "exitCode": code})
	})
	mux.HandleFunc("POST /stop", func(rw http.ResponseWriter, r *http.Request) {
		go w.stop()
		rw.WriteHeader(http.StatusAccepted)
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

// reportExit tells the agent how the workload ended; the agent may not be
// listening, in which case the console carries the exit.
func reportExit(code int, message string) {
	fmt.Fprintf(os.Stdout, "sockerless-init: workload exited %d %s\n", code, message)
	conn, err := dialVsock(hostCID, exitPort)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = json.NewEncoder(conn).Encode(map[string]any{"exitCode": code, "error": message})
}

func powerOff(code int) {
	unix.Sync()
	time.Sleep(100 * time.Millisecond)
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
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
