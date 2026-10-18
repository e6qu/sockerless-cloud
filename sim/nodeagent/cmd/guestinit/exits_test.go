package main

import (
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"testing"
)

// One reaper collects every child, and each command still receives its own
// status: a command whose wait raced the reaper found no child at all.
func TestExitTableHandsEachChildItsOwnStatus(t *testing.T) {
	table := &exitTable{waiters: map[int]chan syscall.WaitStatus{}}
	children := make(chan os.Signal, 1)
	signal.Notify(children, syscall.SIGCHLD)
	defer signal.Stop(children)
	go table.reap(children)

	const commands = 8
	var wg sync.WaitGroup
	results := make([]string, commands)
	codes := make([]int, commands)
	for i := range commands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command("sh", "-c", "echo child-"+strconv.Itoa(i)+"; exit "+strconv.Itoa(i))
			stdout, stderr, status, err := table.run(t.Context(), cmd)
			if err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			if stderr != "" {
				t.Errorf("run %d wrote %q to stderr", i, stderr)
			}
			results[i], codes[i] = stdout, exitCode(status)
		}()
	}
	wg.Wait()
	for i := range commands {
		if results[i] != "child-"+strconv.Itoa(i)+"\n" || codes[i] != i {
			t.Errorf("command %d answered %q with code %d", i, results[i], codes[i])
		}
	}

	killed := exec.Command("sh", "-c", "kill -TERM $$")
	_, _, status, err := table.run(t.Context(), killed)
	if err != nil {
		t.Fatal(err)
	}
	if got := exitCode(status); got != 128+int(syscall.SIGTERM) {
		t.Fatalf("a child the TERM signal ended reports %d, want %d", got, 128+int(syscall.SIGTERM))
	}
}
