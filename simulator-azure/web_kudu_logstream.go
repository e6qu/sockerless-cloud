package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_kudu_logstream.go serves Kudu's log stream on a site's SCM host:
// GET /logstream (and /api/logstream) holds the response open and writes each
// line the site's containers log, as they log it, after a welcome line. While
// nothing is logged it writes a line each minute saying so, and it ends the
// stream once SCM_LOGSTREAM_TIMEOUT passes.

// kuduLogStreams holds the open streams of each site, keyed by the site's
// lower-cased resource ID.
var kuduLogStreams = struct {
	sync.Mutex
	bySite map[string]map[chan string]bool
}{bySite: map[string]map[chan string]bool{}}

// kuduLogStreamBuffer is how many lines a slow reader may fall behind before
// the stream drops lines rather than hold up the site's logging.
const kuduLogStreamBuffer = 1024

func kuduSubscribeLogs(siteID string) (chan string, func()) {
	key := strings.ToLower(siteID)
	ch := make(chan string, kuduLogStreamBuffer)
	kuduLogStreams.Lock()
	if kuduLogStreams.bySite[key] == nil {
		kuduLogStreams.bySite[key] = map[chan string]bool{}
	}
	kuduLogStreams.bySite[key][ch] = true
	kuduLogStreams.Unlock()
	return ch, func() {
		kuduLogStreams.Lock()
		delete(kuduLogStreams.bySite[key], ch)
		if len(kuduLogStreams.bySite[key]) == 0 {
			delete(kuduLogStreams.bySite, key)
		}
		kuduLogStreams.Unlock()
	}
}

func kuduPublishLogLine(siteID, line string) {
	key := strings.ToLower(siteID)
	kuduLogStreams.Lock()
	defer kuduLogStreams.Unlock()
	for ch := range kuduLogStreams.bySite[key] {
		select {
		case ch <- line:
		default:
		}
	}
}

// kuduLogStreamTime is the timestamp Kudu prefixes its own stream lines with.
func kuduLogStreamTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05")
}

func kuduLogStream(w http.ResponseWriter, r *http.Request, site *Site) {
	sim.DeclareOpenEndedWait(r.Context())
	lines, unsubscribe := kuduSubscribeLogs(site.ID)
	defer unsubscribe()
	limit := kuduSettingSeconds(site, "SCM_LOGSTREAM_TIMEOUT")
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	write := func(line string) bool {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(fmt.Sprintf("%s  Welcome, you are now connected to log-streaming service. The default timeout is 2 hours. Change the timeout with the App Setting SCM_LOGSTREAM_TIMEOUT (in seconds). ",
		kuduLogStreamTime(time.Now()))) {
		return
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	quiet := time.NewTicker(time.Minute)
	defer quiet.Stop()
	quietMinutes := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-lines:
			quietMinutes = 0
			quiet.Reset(time.Minute)
			if !write(line) {
				return
			}
		case now := <-quiet.C:
			quietMinutes++
			if !write(fmt.Sprintf("%s  No new trace in the past %d min(s).", kuduLogStreamTime(now), quietMinutes)) {
				return
			}
		case now := <-deadline.C:
			write(fmt.Sprintf("%s  Stream terminated due to timeout %d min(s).", kuduLogStreamTime(now), int(limit/time.Minute)))
			return
		}
	}
}
