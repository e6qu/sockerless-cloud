package sim

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sort"
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
// the deployment names another address; the Scaleway microVM names :6060 so an
// operator reaches it from the host over the tap:
//
//	curl http://172.16.0.2:6060/debug/pprof/goroutine?debug=2
//	curl http://172.16.0.2:6060/debug/inflight

// SlowRequestThreshold is the age at which an in-flight request is reported as
// slow. It bounds nothing and cancels nothing -- it only decides when the
// simulator starts talking about a request it is still serving.
const SlowRequestThreshold = 10 * time.Second

type inFlightRequest struct {
	ID      uint64
	Method  string
	Path    string
	Target  string
	Started time.Time
}

var (
	inFlightMu       sync.Mutex
	inFlight         = map[uint64]*inFlightRequest{}
	inFlightNextID   atomic.Uint64
	slowRequestNoted sync.Map // request id -> struct{}, so each is logged once
)

// InFlightMiddleware records every request while it runs, and reports the ones
// that outlive SlowRequestThreshold. A request that finishes quickly costs a
// map insert and delete.
func InFlightMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := inFlightNextID.Add(1)
		entry := &inFlightRequest{
			ID:      id,
			Method:  r.Method,
			Path:    r.URL.Path,
			Target:  requestOperation(r),
			Started: time.Now(),
		}
		inFlightMu.Lock()
		inFlight[id] = entry
		inFlightMu.Unlock()

		done := make(chan struct{})
		go watchSlowRequest(entry, done)

		defer func() {
			close(done)
			inFlightMu.Lock()
			delete(inFlight, id)
			inFlightMu.Unlock()
			if _, noted := slowRequestNoted.LoadAndDelete(id); noted {
				fmt.Fprintf(os.Stderr, "[sim-slow] finished %s %s %s after %s\n",
					entry.Method, entry.Path, entry.Target, time.Since(entry.Started).Round(time.Second))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func watchSlowRequest(entry *inFlightRequest, done <-chan struct{}) {
	timer := time.NewTimer(SlowRequestThreshold)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		slowRequestNoted.Store(entry.ID, struct{}{})
		fmt.Fprintf(os.Stderr, "[sim-slow] still serving %s %s %s after %s -- goroutine dump: /debug/pprof/goroutine?debug=2\n",
			entry.Method, entry.Path, entry.Target, SlowRequestThreshold)
	}
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
			fmt.Fprintf(w, "%8s  %s %s %s\n",
				now.Sub(entry.Started).Round(time.Millisecond), entry.Method, entry.Path, entry.Target)
		}
	})

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
