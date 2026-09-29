package blobstore

import (
	"net/http"
	"strings"
	"time"
)

// Access is what a request does to the resource it addresses, which decides
// how an unmet condition fails.
type Access int

const (
	// Read reads the resource: a condition that says the caller's copy is
	// current answers 304 Not Modified.
	Read Access = iota
	// Modify changes a resource that must exist.
	Modify
	// Create writes the resource whether or not it exists: If-None-Match: *
	// on one that exists is refused as already existing.
	Create
)

// Validators describe the resource a conditional request is evaluated
// against. A resource that does not exist has none.
type Validators struct {
	ETag     string
	Modified time.Time
	Exists   bool
}

// Outcome is what a request's conditions come to.
type Outcome int

const (
	Proceed Outcome = iota
	NotModified
	PreconditionFailed
	// AlreadyExists is a create carrying If-None-Match: * whose resource
	// exists.
	AlreadyExists
)

// EvaluateHTTP evaluates the conditional headers of h in the order RFC 9110
// §13.2.2 gives: If-Match, else If-Unmodified-Since; then If-None-Match, else
// If-Modified-Since. HTTP dates carry whole seconds, so the modification time
// compares truncated to the second.
func EvaluateHTTP(h http.Header, v Validators, access Access) Outcome {
	modified := v.Modified.Truncate(time.Second)
	if match := h.Get("If-Match"); match != "" {
		if !v.Exists || !ETagListMatches(match, v.ETag) {
			return PreconditionFailed
		}
	} else if since, err := http.ParseTime(h.Get("If-Unmodified-Since")); err == nil && v.Exists && modified.After(since) {
		return PreconditionFailed
	}

	unchanged := false
	if noneMatch := h.Get("If-None-Match"); noneMatch != "" {
		unchanged = v.Exists && ETagListMatches(noneMatch, v.ETag)
		if unchanged && access == Create && strings.TrimSpace(noneMatch) == "*" {
			return AlreadyExists
		}
	} else if since, err := http.ParseTime(h.Get("If-Modified-Since")); err == nil && v.Exists {
		unchanged = !modified.After(since)
	}
	switch {
	case !unchanged:
		return Proceed
	case access == Read:
		return NotModified
	default:
		return PreconditionFailed
	}
}

// ETagListMatches reports whether a conditional header's value, a list of
// entity tags or *, names etag. Entity tags compare without their quotes,
// which some clients omit.
func ETagListMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.Trim(candidate, `"`) == strings.Trim(etag, `"`) {
			return true
		}
	}
	return false
}
