package sim

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The simulator had no way to answer "what is it doing right now". When it went
// slow -- ECS calls taking minutes while /health still answered in
// milliseconds -- the only available moves were to guess from the outside and
// to restart it, which destroys the evidence. Twice that produced a plausible
// but unproven story. This file exists so the next occurrence is read, not
// inferred: a goroutine dump names the stuck call stack, and the in-flight
// registry names the request that is sitting on it.
//
// The diagnostics listener is deliberately separate from the API listener and
// is not proxied publicly: goroutine dumps expose internal state, and
// /debug/pprof/profile is a denial-of-service handle. It binds loopback unless
// the deployment names another address; a simulator in a microVM can name
// :6060 so an operator reaches it from the host over the tap:
//
//	curl http://<guest address>:6060/debug/pprof/goroutine?debug=2
//	curl http://<guest address>:6060/debug/inflight

// SlowRequestThreshold is the age at which an in-flight request is reported as
// slow. It bounds nothing and cancels nothing -- it only decides when the
// simulator starts talking about a request it is still serving. A request
// whose handler declared a wait (DeclareWait) is measured against the end of
// that wait instead of its start.
const SlowRequestThreshold = 10 * time.Second

// openEndedWait marks a request that blocks for as long as its caller holds
// the connection: a stream, a WebSocket session, a wait with no timeout.
const openEndedWait time.Duration = -1

type inFlightRequest struct {
	ID      uint64
	Method  string
	Path    string
	Target  string
	Started time.Time
	// Wait is how long the request may block by design, as its handler
	// declared it; openEndedWait when it has no bound.
	Wait time.Duration
}

var (
	inFlightMu     sync.Mutex
	inFlight       = map[uint64]*inFlightRequest{}
	inFlightNextID atomic.Uint64
)

type inFlightKey struct{}

// DeclareWait records that the request ctx belongs to may block for up to d by
// design -- a long poll, an operation wait, a synchronous run of the caller's
// own workload bounded by its configured timeout. The slow-request diagnostic
// then reports it only once it outlives d by SlowRequestThreshold. Several
// declarations keep the longest; a context outside InFlightMiddleware ignores
// it.
func DeclareWait(ctx context.Context, d time.Duration) {
	if d < 0 {
		d = 0
	}
	declareWait(ctx, d)
}

// DeclareOpenEndedWait records that the request ctx belongs to blocks for as
// long as its caller keeps the connection -- a stream, or a wait the caller
// left without a timeout -- so the slow-request diagnostic never reports it.
func DeclareOpenEndedWait(ctx context.Context) {
	declareWait(ctx, openEndedWait)
}

func declareWait(ctx context.Context, d time.Duration) {
	entry, ok := ctx.Value(inFlightKey{}).(*inFlightRequest)
	if !ok {
		return
	}
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	if entry.Wait == openEndedWait {
		return
	}
	if d == openEndedWait || d > entry.Wait {
		entry.Wait = d
	}
}

// DeclaredWait reports the wait the handler serving ctx's request declared:
// its bound, or openEnded when it has none. A request that declared nothing,
// or a context outside InFlightMiddleware, reads zero.
func DeclaredWait(ctx context.Context) (wait time.Duration, openEnded bool) {
	entry, ok := ctx.Value(inFlightKey{}).(*inFlightRequest)
	if !ok {
		return 0, false
	}
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	if entry.Wait == openEndedWait {
		return 0, true
	}
	return entry.Wait, false
}

// slowReportDue answers when entry turns slow, or false while its declared
// wait is open-ended.
func slowReportDue(entry *inFlightRequest) (time.Time, time.Duration, bool) {
	inFlightMu.Lock()
	wait := entry.Wait
	inFlightMu.Unlock()
	if wait == openEndedWait {
		return time.Time{}, wait, false
	}
	return entry.Started.Add(wait + SlowRequestThreshold), wait, true
}

// diagnosticsClock is the time source the slow-request watcher reads, so a test
// drives it without waiting out the threshold.
type diagnosticsClock interface {
	Now() time.Time
	// After delivers once d has passed; stop releases it early.
	After(d time.Duration) (fired <-chan time.Time, stop func())
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) After(d time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(d)
	return timer.C, func() { timer.Stop() }
}

// InFlightMiddleware records every request while it runs, and reports the ones
// that outlive SlowRequestThreshold beyond the wait their handler declared. A
// request that finishes quickly costs a map insert and delete.
func InFlightMiddleware(next http.Handler) http.Handler {
	return inFlightMiddleware(next, wallClock{}, os.Stderr)
}

func inFlightMiddleware(next http.Handler, clock diagnosticsClock, log io.Writer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := inFlightNextID.Add(1)
		entry := &inFlightRequest{
			ID:      id,
			Method:  r.Method,
			Path:    r.URL.Path,
			Target:  requestOperation(r),
			Started: clock.Now(),
		}
		// An upgraded connection is a session: it lasts as long as its peers
		// keep it, whatever the handler behind it is.
		if isUpgradeRequest(r) {
			entry.Wait = openEndedWait
		}
		inFlightMu.Lock()
		inFlight[id] = entry
		inFlightMu.Unlock()

		done := make(chan struct{})
		reported := make(chan bool, 1)
		go func() { reported <- watchSlowRequest(entry, done, clock, log) }()

		defer func() {
			close(done)
			if <-reported {
				fmt.Fprintf(log, "[sim-slow] finished %s %s %s after %s\n",
					entry.Method, entry.Path, entry.Target, clock.Now().Sub(entry.Started).Round(time.Second))
			}
			inFlightMu.Lock()
			delete(inFlight, id)
			inFlightMu.Unlock()
		}()

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), inFlightKey{}, entry)))
	})
}

// watchSlowRequest reports entry once it outlives its declared wait by
// SlowRequestThreshold, and answers whether it did. A wait the handler declares
// after the watch began moves the report back when the first deadline comes.
func watchSlowRequest(entry *inFlightRequest, done <-chan struct{}, clock diagnosticsClock, log io.Writer) bool {
	for {
		due, wait, bounded := slowReportDue(entry)
		var fired <-chan time.Time
		stop := func() {}
		if bounded {
			fired, stop = clock.After(due.Sub(clock.Now()))
		}
		select {
		case <-done:
			stop()
			return false
		case <-fired:
			stop()
		}
		if later, _, stillBounded := slowReportDue(entry); !stillBounded || later.After(due) {
			continue
		}
		declared := ""
		if wait > 0 {
			declared = fmt.Sprintf(" (declared wait %s)", wait)
		}
		fmt.Fprintf(log, "[sim-slow] still serving %s %s %s after %s%s -- goroutine dump: /debug/pprof/goroutine?debug=2\n",
			entry.Method, entry.Path, entry.Target, wait+SlowRequestThreshold, declared)
		return true
	}
}

func isUpgradeRequest(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// requestOperation names what the caller asked for, which the path alone does
// not: AWS puts the operation in X-Amz-Target or in the form's Action.
func requestOperation(r *http.Request) string {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		return target
	}
	if action := r.URL.Query().Get("Action"); action != "" {
		return action
	}
	return ""
}

// InFlightSnapshot lists the requests currently being served, oldest first.
func InFlightSnapshot() []inFlightRequest {
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	out := make([]inFlightRequest, 0, len(inFlight))
	for _, entry := range inFlight {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// defaultDiagnosticsAddr keeps the listener off every network the host is on:
// a simulator run on a workstation would otherwise hand its goroutine dumps and
// CPU profiler to anyone who can reach port 6060.
const defaultDiagnosticsAddr = "127.0.0.1:6060"

var diagnosticsOnce sync.Once

// StartDiagnosticsListener serves pprof and the in-flight registry on
// SIM_DIAGNOSTICS_ADDR (default 127.0.0.1:6060). Set SIM_DIAGNOSTICS_ADDR=off
// to disable. It never shares the API listener: see the file comment. The
// registry is the process's, so a process that builds several servers -- the
// test suites do -- serves it once.
func StartDiagnosticsListener() {
	diagnosticsOnce.Do(startDiagnosticsListener)
}

func startDiagnosticsListener() {
	addr := os.Getenv("SIM_DIAGNOSTICS_ADDR")
	if addr == "" {
		addr = defaultDiagnosticsAddr
	}
	if addr == "off" {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/debug/inflight", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		now := time.Now()
		requests := InFlightSnapshot()
		fmt.Fprintf(w, "%d in-flight request(s)\n", len(requests))
		for _, entry := range requests {
			wait := ""
			switch {
			case entry.Wait == openEndedWait:
				wait = "  (open-ended wait)"
			case entry.Wait > 0:
				wait = fmt.Sprintf("  (declared wait %s)", entry.Wait)
			}
			fmt.Fprintf(w, "%8s  %s %s %s%s\n",
				now.Sub(entry.Started).Round(time.Millisecond), entry.Method, entry.Path, entry.Target, wait)
		}
	})

	mux.HandleFunc("/debug/stores", storeDiagnosticsHandler)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// Diagnostics must never stop the simulator from serving.
		fmt.Fprintf(os.Stderr, "[sim-diagnostics] not listening on %s: %v\n", addr, err)
		return
	}
	fmt.Fprintf(os.Stderr, "[sim-diagnostics] pprof and /debug/inflight on %s\n", addr)
	go func() {
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "[sim-diagnostics] stopped: %v\n", err)
		}
	}()
}
