package lbplane

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// HostRouter is the front end of a service that serves each workload on a
// hostname of its own — a Cloud Run service's run.app URL, a container app's
// FQDN, an App Service site's hostnames, an AWS Lambda function URL. A
// simulator answers every hostname on its one endpoint, so the router claims a
// request by its Host header and hands every other request to the simulator's
// own APIs.
type HostRouter struct {
	// Domains, when set, are the suffixes of every hostname the front end
	// serves; a request for any other hostname passes on without a lookup.
	Domains []string
	// Route returns what serves the workload hostname names, which may forward
	// to a container through Serve or invoke a function; false passes the
	// request on.
	Route func(r *http.Request, hostname string) (http.Handler, bool)
	// NotFound, when set, answers a request for a hostname under Domains that
	// Route names nothing for; without it the request passes on.
	NotFound http.Handler
}

// Wrap puts the router in front of next, in the shape sim.Server.WrapHandler
// takes.
func (h HostRouter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname := Hostname(r.Host)
		if hostname == "" || !h.inDomains(hostname) {
			next.ServeHTTP(w, r)
			return
		}
		if target, ok := h.Route(r, hostname); ok {
			target.ServeHTTP(w, r)
			return
		}
		if len(h.Domains) > 0 && h.NotFound != nil {
			h.NotFound.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h HostRouter) inDomains(hostname string) bool {
	if len(h.Domains) == 0 {
		return true
	}
	for _, domain := range h.Domains {
		if strings.HasSuffix(hostname, domain) {
			return true
		}
	}
	return false
}

// Answers are a front end's own responses to a forward that got none from its
// target. Each runs before anything was written to the client.
type Answers struct {
	// NotAdmitted answers a flow Upstream.Admit refused; nil leaves it to
	// Failed.
	NotAdmitted func(w http.ResponseWriter, err error)
	// Timeout answers a request its target did not answer within
	// Upstream.Timeout or Upstream.IdleTimeout; nil leaves it to Failed.
	Timeout func(w http.ResponseWriter, err error)
	// Failed answers every other forward that reached no answer.
	Failed func(w http.ResponseWriter, err error)
}

// Serve forwards r through Forward and finishes the client's response however
// the forward ended. A client that went away gets the 499 a load balancer's
// access log records; a failure after the target's answer started cuts the
// response off, as a proxy that loses its upstream mid-stream does; a forward
// that got no answer is answered through a. A target's answer Upstream.Decline
// refused is left unanswered, and Serve returns ErrDeclined for the caller to
// answer. Serve returns the forward's error.
func Serve(w http.ResponseWriter, r *http.Request, up Upstream, a Answers) error {
	err := Forward(w, r, up)
	switch {
	case err == nil, errors.Is(err, ErrDeclined):
	case errors.Is(err, ErrAnswerStarted):
		AbortStarted(w, err)
	case errors.Is(err, ErrClientWentAway):
		w.WriteHeader(StatusClientClosedRequest)
	case errors.Is(err, ErrNotAdmitted) && a.NotAdmitted != nil:
		a.NotAdmitted(w, err)
	case a.Timeout != nil && errors.As(err, new(*SendError)) &&
		(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrIdleTimeout)):
		a.Timeout(w, err)
	default:
		a.Failed(w, err)
	}
	return err
}

// AbortStarted ends the client's response when a forward failed after the
// target's status and headers reached the client: it sends what the target
// sent and closes the stream. Any other error leaves the answer to the caller.
func AbortStarted(w http.ResponseWriter, err error) {
	if !errors.Is(err, ErrAnswerStarted) {
		return
	}
	_ = http.NewResponseController(w).Flush()
	panic(http.ErrAbortHandler)
}

// CloseWithoutAnswer ends the client's connection with no response, as a
// dropped packet leaves a client: nothing answers and nothing resets.
func CloseWithoutAnswer(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	_ = conn.Close()
}

// ClientProto is the protocol the client reached the front end with, as an
// X-Forwarded-Proto header names it.
func ClientProto(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
