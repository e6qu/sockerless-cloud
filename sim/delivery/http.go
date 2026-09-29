package delivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Class is how a cloud reads a receiver's HTTP status.
type Class int

const (
	Accept Class = iota
	Retry
	Reject
)

// Classifier maps a receiver's HTTP status to a Class.
type Classifier func(status int) Class

// Success2xx accepts 2xx and retries everything else.
func Success2xx(status int) Class {
	if status >= 200 && status < 300 {
		return Accept
	}
	return Retry
}

// Request is one webhook POST.
type Request struct {
	URL     string
	Header  http.Header
	Body    []byte
	Timeout time.Duration
}

var client = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Post sends req and classifies the answer. A transport failure or a receiver
// that does not answer within the timeout is retryable, as every cloud treats
// an unreachable endpoint.
func Post(ctx context.Context, req Request, classify Classifier) Outcome {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return Permanent(fmt.Errorf("build request to %s: %w", req.URL, err))
	}
	for name, values := range req.Header {
		httpReq.Header[name] = append([]string(nil), values...)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Retryable(fmt.Errorf("post to %s: %w", req.URL, err))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	switch classify(resp.StatusCode) {
	case Accept:
		return Delivered().WithStatus(resp.StatusCode)
	case Reject:
		return Permanent(fmt.Errorf("%s answered %d", req.URL, resp.StatusCode)).WithStatus(resp.StatusCode)
	default:
		return Retryable(fmt.Errorf("%s answered %d", req.URL, resp.StatusCode)).WithStatus(resp.StatusCode)
	}
}

// Exponential doubles from base up to limit.
func Exponential(base, limit time.Duration) func(int) time.Duration {
	return func(retry int) time.Duration {
		wait := base
		for i := 1; i < retry && wait < limit; i++ {
			wait *= 2
		}
		return min(wait, limit)
	}
}

// Steps waits steps[n-1] before retry n and the last step thereafter.
func Steps(steps ...time.Duration) func(int) time.Duration {
	return func(retry int) time.Duration {
		if retry < 1 {
			retry = 1
		}
		if retry > len(steps) {
			return steps[len(steps)-1]
		}
		return steps[retry-1]
	}
}
