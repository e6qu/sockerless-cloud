package sim

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// The watch has to fire on a real process ending, so start a child, watch it
// and kill it, and hold the watch to noticing. A watch that fired for every
// input would pass a happy-path test on its own, so check the inputs it must
// refuse too.

func TestWatchParentFiresWhenTheProcessExits(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() { _ = child.Process.Kill() }()

	fired := make(chan struct{})
	if !watchParent(strconv.Itoa(child.Process.Pid), func() { close(fired) }) {
		t.Fatal("a live pid must start a watch")
	}

	select {
	case <-fired:
		t.Fatal("the watch fired while its process was still running")
	default:
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = child.Wait()

	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not fire after its process exited")
	}
}

func TestWatchParentIgnoresWhatItCannotWatch(t *testing.T) {
	for name, value := range map[string]string{
		"unset":        "",
		"not a number": "not-a-pid",
		"zero":         "0",
		"negative":     "-1",
		// Watching itself would end the process the moment it looked.
		"this process": strconv.Itoa(os.Getpid()),
	} {
		t.Run(name, func(t *testing.T) {
			if watchParent(value, func() { t.Error("no watch should have started") }) {
				t.Errorf("%q started a watch", value)
			}
		})
	}
}
