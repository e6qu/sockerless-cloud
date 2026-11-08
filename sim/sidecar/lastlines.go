package sidecar

import (
	"strings"
	"sync"
)

// lastLines keeps the last lines a container wrote, for the error that
// reports it ended before it served.
type lastLines struct {
	mu    sync.Mutex
	lines []string
}

const lastLinesKept = 5

func (l *lastLines) add(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(text))
	if len(l.lines) > lastLinesKept {
		l.lines = l.lines[len(l.lines)-lastLinesKept:]
	}
}

func (l *lastLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return "it wrote nothing"
	}
	return "it wrote: " + strings.Join(l.lines, " | ")
}
