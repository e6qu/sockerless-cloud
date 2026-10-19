package sim

import (
	"bytes"
	"context"
	"net/http"
)

// A service that acts on another's behalf reaches that service through its
// API, as the clouds' own services do: Amazon EventBridge sends to an Amazon
// SQS queue with SendMessage, Eventarc publishes to Pub/Sub with
// topics.publish, Azure Event Grid writes a dead letter to Blob Storage with
// Put Blob. Call serves such a request through the whole chain a client's
// request takes — authentication, authorization, audit recording,
// specification validation — so the receiving service applies its own
// checks and records the call, and no service reads another's state.

// Caller is the identity an in-process call is made as when no credential
// carries it: a cloud service principal acting for one of its resources, as
// Amazon EventBridge delivers to a target as events.amazonaws.com for a rule.
// A call made as a role or a service account carries that identity's own
// credential instead, as any client's request does.
type Caller struct {
	// Service is the calling service's principal, such as
	// events.amazonaws.com.
	Service string
	// Source names the resource the service acts for, such as the rule's ARN;
	// the receiving service's resource policy can condition on it.
	Source string
	// Account is the account or project the source belongs to.
	Account string
}

type callerKey struct{}

// WithCaller returns ctx carrying caller. Only an in-process call carries it:
// a request from outside the simulator has no way to set a context value.
func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// CallerOf returns the service principal an in-process call is made as.
func CallerOf(ctx context.Context) (Caller, bool) {
	caller, ok := ctx.Value(callerKey{}).(Caller)
	return caller, ok
}

// InProcess reports whether a request is one Call is serving.
func InProcess(r *http.Request) bool {
	_, ok := r.Context().Value(inProcessKey{}).(bool)
	return ok
}

type inProcessKey struct{}

// CallResponse is what a handler answered an in-process call with.
type CallResponse struct {
	StatusCode int
	header     http.Header
	Body       bytes.Buffer
}

func (c *CallResponse) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}

func (c *CallResponse) Write(b []byte) (int, error) {
	if c.StatusCode == 0 {
		c.StatusCode = http.StatusOK
	}
	return c.Body.Write(b)
}

func (c *CallResponse) WriteHeader(status int) {
	if c.StatusCode == 0 {
		c.StatusCode = status
	}
}

// Call serves r in-process through the server's whole handler chain and
// returns what the handler answered. r's context is the caller's: its
// deadline bounds the call, and a Caller on it is the identity the call is
// made as when r carries no credential of its own.
func (s *Server) Call(r *http.Request) *CallResponse {
	r = r.WithContext(context.WithValue(r.Context(), inProcessKey{}, true))
	if r.RemoteAddr == "" {
		r.RemoteAddr = "in-process"
	}
	response := &CallResponse{}
	s.ServeHTTP(response, r)
	if response.StatusCode == 0 {
		response.StatusCode = http.StatusOK
	}
	return response
}
