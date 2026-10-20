package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// logLine is one line a guest's process wrote, the first process when
// Member is empty, as guestinit reports it and the agent serves it.
type logLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
	Member string    `json:"member,omitempty"`
}

// console is everything a guest's processes wrote, across its boots. The
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
	// ends records each process's exit in a boot with how many lines it
	// wrote, so a reader of that process alone knows when it has them all.
	ends map[processKey]*processEnd
}

type consoleLine struct {
	logLine
	boot int
}

type processKey struct {
	boot   int
	member string
}

type processEnd struct {
	lines int
	done  chan struct{}
}

func newConsole() *console {
	drained := make(chan struct{})
	close(drained)
	return &console{followers: map[chan logLine]struct{}{}, drained: drained, ends: map[processKey]*processEnd{}}
}

// processEnded records that a process of the boot exited after writing
// lines lines.
func (c *console) processEnded(boot int, member string, lines int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	end := c.end(boot, member)
	end.lines = lines
	select {
	case <-end.done:
	default:
		close(end.done)
	}
}

func (c *console) end(boot int, member string) *processEnd {
	key := processKey{boot, member}
	end, ok := c.ends[key]
	if !ok {
		end = &processEnd{done: make(chan struct{})}
		c.ends[key] = end
	}
	return end
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

// selection is what a reader of the console asks for: every process's lines
// or one process's, the whole console or the boot in progress alone.
type selection struct {
	currentBoot bool
	// process is the first process for initProcess, a member for its name,
	// and every process when empty.
	process string
}

func (sel selection) wants(line consoleLine, boot int) bool {
	if sel.currentBoot && line.boot != boot {
		return false
	}
	return sel.wantsProcess(line.logLine)
}

// wantsProcess is wants for a line of the boot in progress.
func (sel selection) wantsProcess(line logLine) bool {
	switch sel.process {
	case "":
		return true
	case initProcess:
		return line.Member == ""
	default:
		return line.Member == sel.process
	}
}

// follow returns the selected lines so far, a channel carrying every line
// that follows, the channel that closes when the boot in progress is
// drained, and, for one process, its end in the boot in progress.
func (c *console) follow(sel selection) (past []logLine, ch chan logLine, drained chan struct{}, end *processEnd, stop func()) {
	ch = make(chan logLine, 4096)
	c.mu.Lock()
	for _, line := range c.lines {
		if sel.wants(line, c.boot) {
			past = append(past, line.logLine)
		}
	}
	c.followers[ch] = struct{}{}
	drained = c.drained
	if sel.process != "" {
		member := sel.process
		if member == initProcess {
			member = ""
		}
		end = c.end(c.boot, member)
	}
	c.mu.Unlock()
	return past, ch, drained, end, func() {
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
	if scanner.Err() != nil {
		return false
	}
	// guestinit holds the machine up until the console is read.
	ack, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://guest/logs/read", nil)
	ackResp, err := g.vsockClient().Do(ack)
	if err != nil {
		return false
	}
	_ = ackResp.Body.Close()
	return ackResp.StatusCode == http.StatusNoContent
}

// streamLogs writes the console as newline-delimited JSON: the selected lines
// so far, then, when follow is set, each new line until the boot in progress
// is drained, or, for one process, until that process has exited and every
// line it wrote is out.
func (g *guest) streamLogs(ctx context.Context, w http.ResponseWriter, follow, currentBoot bool, process string) {
	sel := selection{currentBoot: currentBoot, process: process}
	past, ch, drained, end, stop := g.console.follow(sel)
	defer stop()
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	delivered := 0
	for _, line := range past {
		_ = enc.Encode(line)
		delivered++
	}
	flush()
	if !follow {
		return
	}
	var ended <-chan struct{}
	if end != nil {
		ended = end.done
	}
	complete := func() bool {
		if end == nil {
			return false
		}
		select {
		case <-end.done:
		default:
			return false
		}
		g.console.mu.Lock()
		defer g.console.mu.Unlock()
		return delivered >= end.lines
	}
	if complete() {
		return
	}
	for {
		select {
		case line := <-ch:
			if !sel.wantsProcess(line) {
				continue
			}
			_ = enc.Encode(line)
			delivered++
			flush()
			if complete() {
				return
			}
		case <-ended:
			ended = nil
			if complete() {
				return
			}
		case <-drained:
			for {
				select {
				case line := <-ch:
					if sel.wantsProcess(line) {
						_ = enc.Encode(line)
					}
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
