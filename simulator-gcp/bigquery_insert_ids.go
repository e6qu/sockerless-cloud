package main

import (
	"regexp"
	"sync"
	"time"
)

// bqTemplateSuffixRE is what a templateSuffix may contain: letters, numbers
// and underscores (vendored streaming reference,
// specs/cloud-api/gcp/bigquery-streaming.md.gz).
var bqTemplateSuffixRE = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// bqInsertIDWindow is how long BigQuery remembers an insertId for best-effort
// de-duplication, as the streaming reference states.
const bqInsertIDWindow = time.Minute

// bqInsertIDLog remembers the insertIds each table received within the
// de-duplication window.
type bqInsertIDLog struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

var bqInsertIDs = &bqInsertIDLog{seen: map[string]time.Time{}}

// has reports whether the table at key stored a row under insertID within
// the window.
func (l *bqInsertIDLog) has(key, insertID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, at := range l.seen {
		if now.Sub(at) > bqInsertIDWindow {
			delete(l.seen, k)
		}
	}
	_, dup := l.seen[key+"\x00"+insertID]
	return dup
}

// record notes the insertIds of rows the table at key stored.
func (l *bqInsertIDLog) record(key string, insertIDs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, id := range insertIDs {
		l.seen[key+"\x00"+id] = now
	}
}
