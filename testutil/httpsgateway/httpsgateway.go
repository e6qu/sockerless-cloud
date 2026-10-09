// Package httpsgateway starts the repository's Caddy HTTPS gateway and waits
// until it serves every name it is configured for.
package httpsgateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// startupBound caps the wait for a gateway that hangs before it caches a
// certificate it was configured to issue.
const startupBound = 60 * time.Second

// Caddy's http app logs the names it manages certificates for just before it
// hands them to certmagic, and certmagic's tls.cache logger logs each
// certificate as it enters the cache TLS handshakes read. make/https-gateway's
// Caddyfile routes tls.cache to standard error as JSON at debug level.
const (
	managingMessage = "enabling automatic TLS certificate management"
	cachedMessage   = "added certificate to cache"
)

type logEntry struct {
	Logger   string   `json:"logger"`
	Msg      string   `json:"msg"`
	Domains  []string `json:"domains"`
	Subjects []string `json:"subjects"`
}

// Start starts cmd, a `caddy run` of make/https-gateway/Caddyfile, with its
// standard error copied to out, and returns once the gateway has cached a
// certificate for every name it manages, or with an error when the process
// closes its standard error first.
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
	var pendingMu sync.Mutex
	var pending map[string]bool
	go func() {
		defer close(closed)
		defer func() { _ = pr.Close() }()
		cached := map[string]bool{}
		announced := false
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			_, _ = fmt.Fprintln(out, string(line))
			if announced {
				continue
			}
			var entry logEntry
			if json.Unmarshal(line, &entry) != nil {
				continue
			}
			pendingMu.Lock()
			switch {
			case entry.Logger == "http" && entry.Msg == managingMessage:
				pending = make(map[string]bool, len(entry.Domains))
				for _, d := range entry.Domains {
					if !cached[d] {
						pending[d] = true
					}
				}
			case entry.Logger == "tls.cache" && entry.Msg == cachedMessage:
				for _, s := range entry.Subjects {
					cached[s] = true
					delete(pending, s)
				}
			default:
				pendingMu.Unlock()
				continue
			}
			if pending != nil && len(pending) == 0 {
				announced = true
				close(ready)
			}
			pendingMu.Unlock()
		}
		_, _ = io.Copy(out, pr)
	}()

	select {
	case <-ready:
		return nil
	case <-closed:
		return errors.New("the HTTPS gateway exited before it served every name it manages")
	case <-time.After(startupBound):
		pendingMu.Lock()
		names := make([]string, 0, len(pending))
		for name := range pending {
			names = append(names, name)
		}
		pendingMu.Unlock()
		sort.Strings(names)
		// A Go process answers SIGQUIT with every goroutine's stack on
		// standard error, which is copied to out: the dump shows where the
		// issuance of the pending names is stuck.
		_ = cmd.Process.Signal(syscall.SIGQUIT)
		select {
		case <-closed:
		case <-time.After(10 * time.Second):
		}
		return fmt.Errorf("the HTTPS gateway did not cache a certificate for every name it manages within %s; still pending: %s",
			startupBound, strings.Join(names, ", "))
	}
}
