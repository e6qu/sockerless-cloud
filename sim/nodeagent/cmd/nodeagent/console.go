package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// logLine is one line a guest's first process wrote, as guestinit reports it
// and the agent serves it.
type logLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

// console is everything a guest's first process wrote, across its boots. The
// agent reads it from guestinit while the guest runs and keeps it, so a
// stopped machine's console still reads back and a restarted one continues
// it, as a cloud's serial console does.
type console struct {
	mu        sync.Mutex
	lines     []consoleLine
	followers map[chan logLine]struct{}
	// boot counts the guest's boots; drained closes once the boot in
	// progress has no more lines to come.
	boot    int
	drained chan struct{}
}

type consoleLine struct {
	logLine
	boot int
}

func newConsole() *console {
	drained := make(chan struct{})
	close(drained)
	return &console{followers: map[chan logLine]struct{}{}, drained: drained}
}

// beginBoot opens the next boot and returns its number and the channel its
// reader closes when the boot's lines have all arrived.
func (c *console) beginBoot() (int, chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boot++
	c.drained = make(chan struct{})
	return c.boot, c.drained
}

func (c *console) add(boot int, line logLine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, consoleLine{logLine: line, boot: boot})
	for ch := range c.followers {
		select {
		case ch <- line:
		default:
		}
	}
}

// received is how many lines the boot has contributed, which a reader that
// reconnects to guestinit skips, since guestinit replays a boot from its
// first line.
func (c *console) received(boot int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, line := range c.lines {
		if line.boot == boot {
			n++
		}
	}
	return n
}

// follow returns the lines so far, the whole console or the boot in
// progress alone, a channel carrying the lines that follow, and the channel
// that closes when the boot in progress is drained.
func (c *console) follow(currentBoot bool) (past []logLine, ch chan logLine, drained chan struct{}, stop func()) {
	ch = make(chan logLine, 4096)
	c.mu.Lock()
	for _, line := range c.lines {
		if !currentBoot || line.boot == c.boot {
			past = append(past, line.logLine)
		}
	}
	c.followers[ch] = struct{}{}
	drained = c.drained
	c.mu.Unlock()
	return past, ch, drained, func() {
		c.mu.Lock()
		delete(c.followers, ch)
		c.mu.Unlock()
	}
}

// followGuestConsole reads the boot's lines from guestinit into the console
// until the first process ends, reconnecting while the guest is up, and
// closes drained once no more lines can come.
func (g *guest) followGuestConsole(boot int, drained chan struct{}) {
	defer close(drained)
	for {
		if g.readGuestConsole(boot) {
			return
		}
		select {
		case <-g.exited:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// readGuestConsole streams one connection's worth of lines and reports
// whether guestinit ended the stream itself, which it does once the first
// process has exited and every line is out.
func (g *guest) readGuestConsole(boot int) bool {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://guest/logs?follow=1", nil)
	resp, err := g.vsockClient().Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	skip := g.console.received(boot)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line logLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		g.console.add(boot, line)
	}
	return scanner.Err() == nil
}

// streamLogs writes the console as newline-delimited JSON: the lines so far,
// then, when follow is set, each new line until the boot in progress is
// drained.
func (g *guest) streamLogs(ctx context.Context, w http.ResponseWriter, follow, currentBoot bool) {
	past, ch, drained, stop := g.console.follow(currentBoot)
	defer stop()
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	for _, line := range past {
		_ = enc.Encode(line)
	}
	flush()
	if !follow {
		return
	}
	for {
		select {
		case line := <-ch:
			_ = enc.Encode(line)
			flush()
		case <-drained:
			for {
				select {
				case line := <-ch:
					_ = enc.Encode(line)
				default:
					flush()
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
