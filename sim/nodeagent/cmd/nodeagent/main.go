// Command nodeagent boots Firecracker microVMs for the simulator that started
// it. It runs as a container holding /dev/kvm and the network capability
// inside its own namespace, bridges each guest's tap onto the engine network
// it joined, filters at the tap what the guest's rules allow, and serves the
// simulator's placement backend over HTTP with the token the simulator minted
// for it. It is the one place in the simulators where a guest, a tap or a
// packet filter exists.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type config struct {
	listen      string
	token       string
	stateDir    string
	kernel      string
	firecracker string
	guestInit   string
}

func configFromEnv() (config, error) {
	c := config{
		listen:      envOr("NODEAGENT_LISTEN", ":8700"),
		token:       os.Getenv("NODEAGENT_TOKEN"),
		stateDir:    envOr("NODEAGENT_STATE_DIR", "/var/lib/sockerless"),
		kernel:      envOr("NODEAGENT_KERNEL", "/usr/local/lib/sockerless/vmlinux"),
		firecracker: envOr("NODEAGENT_FIRECRACKER", "/usr/local/bin/firecracker"),
		guestInit:   envOr("NODEAGENT_GUESTINIT", "/usr/local/lib/sockerless/guestinit"),
	}
	if c.token == "" {
		return c, errors.New("NODEAGENT_TOKEN is empty")
	}
	for _, path := range []string{c.kernel, c.firecracker, c.guestInit} {
		if _, err := os.Stat(path); err != nil {
			return c, err
		}
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("nodeagent: %v", err)
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		log.Fatalf("nodeagent: /dev/kvm: %v (the agent's container needs the device)", err)
	}
	agent, err := newAgent(cfg)
	if err != nil {
		log.Fatalf("nodeagent: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	server := &http.Server{Addr: cfg.listen, Handler: agent.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		agent.stopAll()
	}()
	log.Printf("nodeagent: serving on %s", cfg.listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("nodeagent: %v", err)
	}
}

func (a *agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /v1/guests", a.handleList)
	mux.HandleFunc("PUT /v1/guests/{id}", a.handleCreate)
	mux.HandleFunc("PUT /v1/guests/{id}/rootfs", a.handleRootFS)
	mux.HandleFunc("POST /v1/guests/{id}/start", a.handleStart)
	mux.HandleFunc("GET /v1/guests/{id}", a.handleGet)
	mux.HandleFunc("GET /v1/guests/{id}/wait", a.handleWait)
	mux.HandleFunc("GET /v1/guests/{id}/logs", a.handleLogs)
	mux.HandleFunc("GET /v1/guests/{id}/rootfs", a.handleExport)
	mux.HandleFunc("POST /v1/guests/{id}/exec", a.handleExec)
	mux.HandleFunc("POST /v1/guests/{id}/stop", a.handleStop)
	mux.HandleFunc("PUT /v1/guests/{id}/ingress", a.handleIngress)
	mux.HandleFunc("PUT /v1/guests/{id}/disks/{slot}", a.handleDisk)
	mux.HandleFunc("PUT /v1/guests/{id}/members/{name}", a.handleCreateMember)
	mux.HandleFunc("PUT /v1/guests/{id}/members/{name}/rootfs", a.handleMemberRootFS)
	mux.HandleFunc("POST /v1/guests/{id}/members/{name}/start", a.handleStartMember)
	mux.HandleFunc("GET /v1/guests/{id}/members/{name}/wait", a.handleWaitMember)
	mux.HandleFunc("POST /v1/guests/{id}/members/{name}/stop", a.handleStopMember)
	mux.HandleFunc("DELETE /v1/guests/{id}", a.handleDelete)
	mux.HandleFunc("DELETE /v1/networks/{address}", a.handleLeaveNetwork)
	return a.authenticate(mux)
}

func (a *agent) authenticate(next http.Handler) http.Handler {
	want := []byte("Bearer " + a.cfg.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (a *agent) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.list())
}

func (a *agent) handleCreate(w http.ResponseWriter, r *http.Request) {
	var spec guestSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("guest spec: %w", err))
		return
	}
	spec.ID = r.PathValue("id")
	g, err := a.create(spec)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, g.state())
}

func (a *agent) handleRootFS(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	if err := g.buildRootFS(r.Context(), r.Body); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, g.state())
}

func (a *agent) handleStart(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	if err := g.start(r.Context()); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, g.state())
}

func (a *agent) handleGet(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	writeJSON(w, http.StatusOK, g.state())
}

func (a *agent) handleWait(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	processDone := g.processDone
	if r.URL.Query().Get("machine") != "" {
		processDone = nil
	}
	select {
	case <-processDone:
		// With no member left to run, the power-off follows the first
		// process's exit at once, and the caller's next move — a restart
		// of a kept guest — wants the machine stopped.
		if !g.membersRunning() {
			select {
			case <-g.exited:
			case <-time.After(30 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
	case <-g.exited:
	case <-r.Context().Done():
		return
	}
	writeJSON(w, http.StatusOK, g.state())
}

func (a *agent) handleLogs(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	query := r.URL.Query()
	g.streamLogs(r.Context(), w, query.Get("follow") != "", query.Get("boot") == "current", query.Get("process"))
}

// handleExport streams the guest's root filesystem as a tar archive. An
// error after the first byte cannot change the status, so it ends the
// connection short instead, which the client reads as a failed archive.
func (a *agent) handleExport(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	started := &headerOnFirstWrite{ResponseWriter: w}
	if err := g.exportRootFS(r.Context(), started); err != nil {
		if started.wrote {
			log.Printf("nodeagent: export %s: %v", g.spec.ID, err)
			panic(http.ErrAbortHandler)
		}
		writeError(w, statusFor(err), err)
	}
}

// headerOnFirstWrite remembers whether the body has begun, so an error that
// arrives before it still gets a status of its own.
type headerOnFirstWrite struct {
	http.ResponseWriter
	wrote bool
}

func (h *headerOnFirstWrite) Write(p []byte) (int, error) {
	h.wrote = true
	return h.ResponseWriter.Write(p)
}

func (a *agent) handleExec(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resp, err := g.exec(r.Context(), body)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

func (a *agent) handleStop(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	g.stop(r.Context(), false)
	writeJSON(w, http.StatusOK, g.state())
}

func (a *agent) handleLeaveNetwork(w http.ResponseWriter, r *http.Request) {
	if err := a.net.release(r.Context(), r.PathValue("address")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agent) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	var spec memberSpec
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("member spec: %w", err))
		return
	}
	spec.Name = r.PathValue("name")
	m, err := g.createMember(spec)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, m.state())
}

func (a *agent) handleMemberRootFS(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	m, err := g.buildMemberRootFS(r.Context(), r.PathValue("name"), r.Body)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, m.state())
}

func (a *agent) handleStartMember(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	m, err := g.startMember(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, m.state())
}

func (a *agent) handleWaitMember(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	m, ok := g.member(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, errMemberNotFound)
		return
	}
	select {
	case <-m.exited:
	case <-g.exited:
	case <-r.Context().Done():
		return
	}
	writeJSON(w, http.StatusOK, m.state())
}

func (a *agent) handleStopMember(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	m, err := g.stopMember(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, m.state())
}

func (a *agent) handleIngress(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	var rules []packetRule
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&rules); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := g.applyIngress(r.Context(), rules); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agent) handleDisk(w http.ResponseWriter, r *http.Request) {
	g, ok := a.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errGuestNotFound)
		return
	}
	var req struct {
		SizeMiB int `json:"sizeMiB"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := g.attachDisk(r.Context(), r.PathValue("slot"), req.SizeMiB); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agent) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.remove(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var (
	errGuestNotFound  = errors.New("no such guest")
	errGuestExists    = errors.New("a guest of that id exists")
	errGuestState     = errors.New("the guest is not in a state for that")
	errMemberNotFound = errors.New("no such member")
	errMemberExists   = errors.New("a member of that name exists")
)

func statusFor(err error) int {
	switch {
	case errors.Is(err, errGuestNotFound), errors.Is(err, errMemberNotFound):
		return http.StatusNotFound
	case errors.Is(err, errGuestExists), errors.Is(err, errMemberExists):
		return http.StatusConflict
	case errors.Is(err, errGuestState), errors.Is(err, errNetworkInUse):
		return http.StatusConflict
	case strings.Contains(err.Error(), "required"):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}
