package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// memberSpec mirrors sim/nodeagent's MemberSpec.
type memberSpec struct {
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels"`
	Command    []string          `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	Seed       []seedFile        `json:"seed"`
	StopGrace  time.Duration     `json:"stopGrace"`
}

type memberState struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Labels    map[string]string `json:"labels,omitempty"`
	ExitCode  int               `json:"exitCode"`
	Error     string            `json:"error,omitempty"`
	StartedAt time.Time         `json:"startedAt"`
	StoppedAt time.Time         `json:"stoppedAt"`
}

// member is a process tree a running guest takes on beside its first
// process, from a root filesystem of its own that the guest reads off one
// of its disk slots.
type member struct {
	guest *guest
	spec  memberSpec
	slot  int
	image string

	mu        sync.Mutex
	status    string
	rootfs    bool
	exitCode  int
	err       string
	startedAt time.Time
	stoppedAt time.Time
	exited    chan struct{}
	exitOnce  sync.Once
}

// initProcess is the name that selects the first process where a member
// name selects a member.
const initProcess = "init"

// createMember declares a member of a running guest and gives it the free
// disk slot farthest from the ones a disk attachment takes first.
func (g *guest) createMember(spec memberSpec) (*member, error) {
	if spec.Name == "" || spec.Name == initProcess || !validID(spec.Name) {
		return nil, fmt.Errorf("a member name of letters, digits, dots and dashes other than %q is required", initProcess)
	}
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("a command is required")
	}
	if spec.StopGrace <= 0 {
		spec.StopGrace = 10 * time.Second
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status != statusRunning {
		return nil, fmt.Errorf("%w: a member joins a running guest", errGuestState)
	}
	select {
	case <-g.processDone:
		return nil, fmt.Errorf("%w: the first process has exited", errGuestState)
	default:
	}
	if _, exists := g.members[spec.Name]; exists {
		return nil, errMemberExists
	}
	slot := -1
	for n := len(g.disks) - 1; n >= 0; n-- {
		if !g.usedSlots[n] {
			slot = n
			break
		}
	}
	if slot < 0 {
		return nil, fmt.Errorf("the guest's %d disk slots are all taken", len(g.disks))
	}
	g.usedSlots[slot] = true
	m := &member{guest: g, spec: spec, slot: slot, image: filepath.Join(g.dir, "members", spec.Name+".ext4"), status: statusCreating, exited: make(chan struct{})}
	g.members[spec.Name] = m
	g.memberOrder = append(g.memberOrder, spec.Name)
	return m, nil
}

// membersRunning reports whether any member's process still runs.
func (g *guest) membersRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, m := range g.members {
		m.mu.Lock()
		running := m.status == statusRunning
		m.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

func (g *guest) member(name string) (*member, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.members[name]
	return m, ok
}

// slotHolder is the name of the member that holds a disk slot, or "".
func (g *guest) slotHolder(slot int) string {
	for name, m := range g.members {
		if m.slot == slot {
			return name
		}
	}
	return ""
}

func (g *guest) buildMemberRootFS(ctx context.Context, name string, archive io.Reader) (*member, error) {
	m, ok := g.member(name)
	if !ok {
		return nil, errMemberNotFound
	}
	m.mu.Lock()
	creating := m.status == statusCreating && !m.rootfs
	m.mu.Unlock()
	if !creating {
		return nil, fmt.Errorf("%w: the member has its root filesystem", errGuestState)
	}
	if err := os.MkdirAll(filepath.Dir(m.image), 0o755); err != nil {
		return nil, err
	}
	if err := buildImage(ctx, archive, strings.TrimSuffix(m.image, ".ext4"), m.image, m.spec.Seed); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.rootfs = true
	m.mu.Unlock()
	return m, nil
}

// startMember hands the member's root filesystem to its disk slot and asks
// guestinit to mount it and run the member.
func (g *guest) startMember(ctx context.Context, name string) (*member, error) {
	m, ok := g.member(name)
	if !ok {
		return nil, errMemberNotFound
	}
	m.mu.Lock()
	ready := m.status == statusCreating && m.rootfs
	m.mu.Unlock()
	if !ready {
		return nil, fmt.Errorf("%w: the member needs a root filesystem and must not have started", errGuestState)
	}
	if err := g.patchDrive(ctx, fmt.Sprintf("disk%d", m.slot), m.image); err != nil {
		return nil, fmt.Errorf("attach the root filesystem of member %s: %w", name, err)
	}
	body, _ := json.Marshal(map[string]any{
		"name":       m.spec.Name,
		"device":     slotDevice(m.slot),
		"command":    m.spec.Command,
		"args":       m.spec.Args,
		"env":        m.spec.Env,
		"workingDir": m.spec.WorkingDir,
		"user":       m.spec.User,
		"stopGrace":  m.spec.StopGrace,
	})
	startCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(startCtx, http.MethodPost, "http://guest/members", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.vsockClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("start member %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		err := fmt.Errorf("start member %s: %s", name, strings.TrimSpace(string(out)))
		m.markExited(127, err.Error())
		return nil, err
	}
	m.mu.Lock()
	m.status = statusRunning
	m.startedAt = time.Now().UTC()
	m.mu.Unlock()
	return m, nil
}

// slotDevice is the guest's name for a disk slot's block device: the root
// filesystem is vda and the slots follow it in order.
func slotDevice(slot int) string {
	return fmt.Sprintf("vd%c", 'b'+slot)
}

// stopMember asks guestinit to end the member within its grace and waits for
// the exit, or for the grace and a margin to pass.
func (g *guest) stopMember(ctx context.Context, name string) (*member, error) {
	m, ok := g.member(name)
	if !ok {
		return nil, errMemberNotFound
	}
	m.mu.Lock()
	running := m.status == statusRunning
	m.mu.Unlock()
	if !running {
		return m, nil
	}
	log.Printf("nodeagent: guest %s: member %s: stop requested", g.spec.ID, name)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://guest/members/"+name+"/stop", nil)
	if resp, err := g.vsockClient().Do(req); err == nil {
		_ = resp.Body.Close()
	}
	select {
	case <-m.exited:
	case <-g.exited:
	case <-time.After(m.spec.StopGrace + 15*time.Second):
	case <-ctx.Done():
	}
	return m, nil
}

func (m *member) markExited(code int, message string) {
	m.exitOnce.Do(func() {
		m.mu.Lock()
		m.status = statusExited
		m.exitCode, m.err = code, message
		m.stoppedAt = time.Now().UTC()
		m.mu.Unlock()
		close(m.exited)
	})
}

func (m *member) state() memberState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return memberState{Name: m.spec.Name, Status: m.status, Labels: m.spec.Labels, ExitCode: m.exitCode, Error: m.err, StartedAt: m.startedAt, StoppedAt: m.stoppedAt}
}
