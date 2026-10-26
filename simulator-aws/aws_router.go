package main

import (
	"net/http"
)

// AWS JSON services POST with X-Amz-Target: ServiceName.ActionName.
type AWSRouter struct {
	handlers map[string]http.HandlerFunc
}

func NewAWSRouter() *AWSRouter {
	return &AWSRouter{
		handlers: make(map[string]http.HandlerFunc),
	}
}

// target reads e.g. "AmazonEC2ContainerServiceV20141113.RunTask".
func (r *AWSRouter) Register(target string, handler http.HandlerFunc) {
	r.handlers[target] = handler
}

// The spec-conformance tests check this table against the vendored Smithy
// models.
func (r *AWSRouter) Targets() []string {
	targets := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		targets = append(targets, t)
	}
	return targets
}

// Handler lets one service enter another's implementation without a loopback
// HTTP call.
func (r *AWSRouter) Handler(target string) (http.HandlerFunc, bool) {
	handler, ok := r.handlers[target]
	if !ok {
		return nil, false
	}
	// No request middleware saw an in-process caller's writer, so scope it to the
	// target's service here.
	model := awsErrorModelForTarget(target)
	return func(w http.ResponseWriter, req *http.Request) {
		awsErrorModelScope(w, model, func() { handler(w, req) })
	}, true
}

func (r *AWSRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	target := req.Header.Get("X-Amz-Target")
	if target == "" {
		AWSError(w, "MissingAction", "X-Amz-Target header is required", http.StatusBadRequest)
		return
	}

	handler, ok := r.handlers[target]
	if !ok {
		AWSErrorf(w, "UnknownOperationException", http.StatusBadRequest,
			"Unknown operation: %s", target)
		return
	}

	handler(w, req)
}

// Several services share Action names (ListTagsForResource exists on Amazon
// RDS, Amazon SNS, Amazon ElastiCache and more); the Version form field tells
// them apart. Services with globally unique actions register without a
// version, and that bucket answers when no versioned handler matches.
type AWSQueryRouter struct {
	// versioned[version][action]; "" holds unversioned registrations.
	versioned map[string]map[string]http.HandlerFunc
}

func NewAWSQueryRouter() *AWSQueryRouter {
	return &AWSQueryRouter{
		versioned: make(map[string]map[string]http.HandlerFunc),
	}
}

// Register files action under no version, for services whose actions are
// globally unique.
func (r *AWSQueryRouter) Register(action string, handler http.HandlerFunc) {
	r.RegisterVersioned("", action, handler)
}

// RegisterVersioned is required when several services define the action.
func (r *AWSQueryRouter) RegisterVersioned(version, action string, handler http.HandlerFunc) {
	if r.versioned[version] == nil {
		r.versioned[version] = make(map[string]http.HandlerFunc)
	}
	r.versioned[version][action] = handler
}

// Unversioned actions appear under "". The spec-conformance tests check this
// table against the vendored Smithy models.
func (r *AWSQueryRouter) VersionedActions() map[string][]string {
	out := make(map[string][]string, len(r.versioned))
	for version, actions := range r.versioned {
		for action := range actions {
			out[version] = append(out[version], action)
		}
	}
	return out
}

// Handler lets one service enter a Query service without a signed loopback
// request.
func (r *AWSQueryRouter) Handler(version, action string) (http.HandlerFunc, bool) {
	if version != "" {
		if actions, ok := r.versioned[version]; ok {
			if handler, ok := actions[action]; ok {
				return handler, true
			}
		}
	}
	if actions, ok := r.versioned[""]; ok {
		handler, ok := actions[action]
		return handler, ok
	}
	return nil, false
}

// A matching versioned handler wins; otherwise the unversioned one answers.
func (r *AWSQueryRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		awsWriteError(w, awsErrorEnvelope{Protocol: awsEC2Query}, http.StatusBadRequest, "MalformedInput", "Could not parse form body", "")
		return
	}

	action := req.FormValue("Action")
	if action == "" {
		awsWriteError(w, awsErrorEnvelope{Protocol: awsEC2Query}, http.StatusBadRequest, "MissingAction", "Action parameter is required", "")
		return
	}

	version := req.FormValue("Version")
	if handler, ok := r.Handler(version, action); ok {
		handler(w, req)
		return
	}

	awsWriteError(w, awsErrorEnvelope{Protocol: awsEC2Query}, http.StatusBadRequest, "InvalidAction", "The action "+action+" is not valid", "")
}
