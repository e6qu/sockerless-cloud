package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// memberRequest is the agent's request to run a member: its process, and the
// block device that carries its root filesystem.
type memberRequest struct {
	Name   string       `json:"name"`
	Device string       `json:"device"`
	Mounts []shareMount `json:"mounts"`
	processSpec
}

// members is the guest's process table: the first process and the members
// the agent added while the guest runs.
type members struct {
	first *process
	logs  *logBook
	// address is the guest's own, which a member's share mount names as
	// the client's.
	address string
	// done closes once every process has ended, right before the power-off.
	done chan struct{}

	mu      sync.Mutex
	byName  map[string]*process
	running sync.WaitGroup
	closed  bool
}

func newMembers(first *process, logs *logBook, address string) *members {
	return &members{first: first, logs: logs, address: address, done: make(chan struct{}), byName: map[string]*process{}}
}

const membersRoot = "/run/sockerless/members"

// add mounts the member's root filesystem, gives it the kernel filesystems
// and the guest's name resolution, and starts its process. A member's exit
// reaches the agent as the first process's does.
func (m *members) add(req memberRequest) error {
	if req.Name == "" || req.Name == initProcess || strings.ContainsAny(req.Name, "/\x00") {
		return fmt.Errorf("member name %q is not usable", req.Name)
	}
	if req.Device == "" {
		return errors.New("a device is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("the first process has exited; the guest takes no more members")
	}
	if _, exists := m.byName[req.Name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("member %q exists", req.Name)
	}
	m.running.Add(1)
	m.mu.Unlock()
	root := filepath.Join(membersRoot, req.Name)
	p := newProcess(req.Name, root, req.processSpec, m.logs)
	if err := mountMemberRoot(req.Device, root); err != nil {
		m.running.Done()
		return err
	}
	if err := mountShares(req.Mounts, root, m.address); err != nil {
		m.running.Done()
		return err
	}
	if err := p.start(); err != nil {
		m.running.Done()
		return err
	}
	m.mu.Lock()
	m.byName[req.Name] = p
	m.mu.Unlock()
	go func() {
		defer m.running.Done()
		result := p.wait()
		reportExit(req.Name, result.code, result.message, m.logs.count(req.Name))
	}()
	return nil
}

// mountMemberRoot mounts the member's filesystem at root once the kernel has
// taken the device's size, and binds the kernel filesystems and the guest's
// resolver, hosts and hostname files into it, as a container runtime
// prepares a container's root.
func mountMemberRoot(device, root string) error {
	if err := waitForBlockDevice(device, 30*time.Second); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := unix.Mount("/dev/"+device, root, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount /dev/%s at %s: %w", device, root, err)
	}
	for _, dir := range []string{"/proc", "/sys", "/dev"} {
		target := filepath.Join(root, dir)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		if err := unix.Mount(dir, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("bind %s into %s: %w", dir, root, err)
		}
	}
	for _, file := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/hostname"} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		_ = os.Remove(target)
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			return err
		}
		if err := unix.Mount(file, target, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s into %s: %w", file, root, err)
		}
	}
	return nil
}

// placeholderSectors is the size of a drive the agent has not given a
// backing file yet: one MiB, which no root filesystem fits.
const placeholderSectors = 2048

// waitForBlockDevice returns once the kernel reports the device larger than
// the placeholder the guest booted with. The agent swaps the backing file
// before it asks for the member, and the device's size follows the
// hypervisor's configuration change; a kernel uevent announces the change,
// so the wait listens for it rather than polling.
func waitForBlockDevice(device string, timeout time.Duration) error {
	size := func() (int64, bool) {
		data, err := os.ReadFile(filepath.Join("/sys/block", device, "size"))
		if err != nil {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		return n, err == nil
	}
	if n, ok := size(); ok && n > placeholderSectors {
		return nil
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return fmt.Errorf("open the uevent socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		return fmt.Errorf("bind the uevent socket: %w", err)
	}
	deadline := time.Now().Add(timeout)
	// The size may have changed between the read above and the bind; a
	// second read after the bind closes that window.
	if n, ok := size(); ok && n > placeholderSectors {
		return nil
	}
	buf := make([]byte, 64*1024)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			n, _ := size()
			return fmt.Errorf("/dev/%s stayed at %d sectors for %s", device, n, timeout)
		}
		tv := unix.NsecToTimeval(remaining.Nanoseconds())
		if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
			return err
		}
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("read the uevent socket: %w", err)
		}
		if ueventNames(buf[:n], device) {
			if n, ok := size(); ok && n > placeholderSectors {
				return nil
			}
		}
	}
}

// ueventNames reports whether a uevent message, NUL-separated KEY=VALUE
// pairs after its header, is about the block device.
func ueventNames(message []byte, device string) bool {
	for _, field := range strings.Split(string(message), "\x00") {
		if field == "DEVNAME="+device {
			return true
		}
	}
	return false
}

// process finds the process a name selects: the first process for the
// reserved name or an empty one, else the member.
func (m *members) process(name string) (*process, bool) {
	if name == "" || name == initProcess {
		return m.first, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byName[name]
	return p, ok
}

// closeToNew refuses members from now on, once the first process has ended.
func (m *members) closeToNew() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// waitAll returns once every member has ended.
func (m *members) waitAll() { m.running.Wait() }

// stopAll stops every member and the first process, each within its grace.
func (m *members) stopAll() {
	m.mu.Lock()
	all := make([]*process, 0, len(m.byName)+1)
	for _, p := range m.byName {
		all = append(all, p)
	}
	m.mu.Unlock()
	all = append(all, m.first)
	var wg sync.WaitGroup
	for _, p := range all {
		wg.Add(1)
		go func() { defer wg.Done(); p.stop() }()
	}
	wg.Wait()
}

// initProcess is the name that selects the first process where a member
// name selects a member.
const initProcess = "init"
