// Package simready starts a simulator and waits for it to bind its port.
package simready

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// startupBound caps the wait for the banner, for a simulator that hangs before
// it binds. Registration creates every persistent store table first, which a
// loaded hosted disk has taken tens of seconds to do.
const startupBound = 120 * time.Second

// Start starts cmd with its standard error copied to out, and returns once the
// simulator prints the banner line it writes after binding its port, or with
// an error when the process closes its standard error first.
func Start(cmd *exec.Cmd, out io.Writer) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	_ = pw.Close()

	ready := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		defer func() { _ = pr.Close() }()
		announced := false
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(out, line)
			if !announced && strings.HasPrefix(strings.TrimSpace(line), "Listening on ") {
				announced = true
				close(ready)
			}
		}
		_, _ = io.Copy(out, pr)
	}()

	select {
	case <-ready:
		return nil
	case <-closed:
		return errors.New("the simulator exited before it was listening")
	case <-time.After(startupBound):
		return fmt.Errorf("the simulator did not start listening within %s", startupBound)
	}
}
