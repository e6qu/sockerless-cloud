package doctest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Session is one reader working through a document: a first terminal whose
// working directory and exported variables persist between blocks, and the
// other terminals its background blocks run in.
type Session struct {
	t     *testing.T
	state string
	env   []string
	bg    map[string]*background
}

type background struct {
	cmd  *exec.Cmd
	out  *lineWriter
	done chan error
	mu   sync.Mutex
}

// ReadyTimeout bounds how long a background block may take to print its ready
// line. A simulator builds its engine images before it listens, which takes
// minutes on a cold engine.
var ReadyTimeout = 15 * time.Minute

// NewSession starts a session in an empty working directory. env holds the
// variables the reader's machine has before the document starts; the session
// adds nothing else.
func NewSession(t *testing.T, env []string) *Session {
	t.Helper()
	s := &Session{t: t, state: t.TempDir(), env: env, bg: map[string]*background{}}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(s.state, "cwd"), []byte(home), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stopAll)
	return s
}

func (s *Session) prelude() string {
	return fmt.Sprintf(`set -eo pipefail
[ -f %[1]q ] && source %[1]q
cd "$(cat %[2]q)"
`, filepath.Join(s.state, "env"), filepath.Join(s.state, "cwd"))
}

func (s *Session) epilogue() string {
	return fmt.Sprintf("\nexport -p > %q\npwd > %q\n",
		filepath.Join(s.state, "env"), filepath.Join(s.state, "cwd"))
}

// Do runs one step and fails the test when the step fails.
func (s *Session) Do(ctx context.Context, step Step) {
	s.t.Helper()
	where := fmt.Sprintf("line %d", step.Line)
	switch step.Kind {
	case Skip:
		s.t.Logf("%s: skipped: %s", where, step.Attrs["reason"])
	case Run:
		s.t.Logf("%s: run\n%s", where, step.Script)
		cmd := exec.CommandContext(ctx, "bash", "-c", s.prelude()+step.Script+s.epilogue())
		cmd.Env = s.env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		out := &lineWriter{}
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		s.t.Logf("%s: output\n%s", where, out.buf.String())
		if err != nil {
			s.t.Fatalf("%s: %v", where, err)
		}
		for _, exp := range step.Expect {
			if !regexp.MustCompile(exp).MatchString(out.buf.String()) {
				s.t.Fatalf("%s: output does not match %q", where, exp)
			}
		}
	case Background:
		s.start(ctx, step, where)
	case Stop:
		s.stop(step.Attrs["id"])
	default:
		s.t.Fatalf("%s: cannot do %s", where, step.Kind)
	}
}

func (s *Session) start(ctx context.Context, step Step, where string) {
	s.t.Helper()
	id := step.Attrs["id"]
	if _, ok := s.bg[id]; ok {
		s.t.Fatalf("%s: background %s is already running", where, id)
	}
	ready := regexp.MustCompile(step.Attrs["ready"])
	count := 1
	if v, ok := step.Attrs["count"]; ok {
		count, _ = strconv.Atoi(v)
	}
	s.t.Logf("%s: start %s\n%s", where, id, step.Script)
	b := &background{done: make(chan error, 1)}
	seen := 0
	readyCh := make(chan struct{})
	b.out = &lineWriter{onLine: func(line string) {
		if ready.MatchString(line) {
			seen++
			if seen == count {
				close(readyCh)
			}
		}
	}}
	// A terminal of its own: the session's state as it stands now, and no
	// state written back.
	b.cmd = exec.Command("bash", "-c", s.prelude()+step.Script+"\nwait\n")
	b.cmd.Env = s.env
	b.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	b.cmd.Stdout, b.cmd.Stderr = lockedWriter{b}, lockedWriter{b}
	if err := b.cmd.Start(); err != nil {
		s.t.Fatalf("%s: start %s: %v", where, id, err)
	}
	go func() { b.done <- b.cmd.Wait() }()
	s.bg[id] = b
	select {
	case <-readyCh:
		s.t.Logf("%s: %s is ready", where, id)
	case err := <-b.done:
		s.t.Fatalf("%s: %s exited before it was ready (%v)\n%s", where, id, err, b.output())
	case <-time.After(ReadyTimeout):
		s.t.Fatalf("%s: %s printed no %q within %s\n%s", where, id, ready, ReadyTimeout, b.output())
	case <-ctx.Done():
		s.t.Fatalf("%s: %s: %v", where, id, ctx.Err())
	}
}

type lockedWriter struct{ b *background }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	return w.b.out.Write(p)
}

func (b *background) output() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.out.buf.String()
}

// stop interrupts a background block's process group, as Ctrl-C in its
// terminal does, and kills what is left after a grace period.
func (s *Session) stop(id string) {
	s.t.Helper()
	b, ok := s.bg[id]
	if !ok {
		s.t.Fatalf("stop %s: no such background block", id)
	}
	delete(s.bg, id)
	_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGINT)
	var exit error
	select {
	case exit = <-b.done:
	case <-time.After(time.Minute):
		_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
		exit = <-b.done
	}
	s.t.Logf("stopped %s (%v); its output:\n%s", id, exit, b.output())
}

func (s *Session) stopAll() {
	for id := range s.bg {
		s.stop(id)
	}
}

// RunDoc runs a document: one subtest per part, each in a fresh session that
// first runs the steps every part shares. A document without parts runs as a
// single session.
func RunDoc(t *testing.T, doc *Doc, env []string) {
	t.Helper()
	ctx := t.Context()
	if len(doc.Parts) == 0 {
		s := NewSession(t, env)
		for _, step := range doc.Shared {
			s.Do(ctx, step)
		}
		return
	}
	for _, part := range doc.Parts {
		t.Run(part.Name, func(t *testing.T) {
			s := NewSession(t, env)
			for _, step := range doc.Shared {
				s.Do(t.Context(), step)
			}
			for _, step := range part.Steps {
				s.Do(t.Context(), step)
			}
		})
	}
}
