package sim

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type contextKey int

const (
	requestIDKey contextKey = iota
	identityKey
)

const loggedErrorBodyLimit = 4096

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func RequestIDMiddleware(provider string) func(http.Handler) http.Handler {
	headerName := requestIDHeader(provider)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := generateRequestID()
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			w.Header().Set(headerName, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func LoggingMiddleware(logger zerolog.Logger, provider string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: 200}

			next.ServeHTTP(sw, r)

			event := logger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", sw.status).
				Dur("duration", time.Since(start)).
				Str("request_id", RequestID(r.Context()))

			switch provider {
			case "aws":
				if target := r.Header.Get("X-Amz-Target"); target != "" {
					event.Str("amz_target", target)
				} else {
					if action := r.FormValue("Action"); action != "" {
						event.Str("aws_query_action", action)
					}
					if version := r.FormValue("Version"); version != "" {
						event.Str("aws_query_version", version)
					}
				}
			case "azure":
				if v := r.URL.Query().Get("api-version"); v != "" {
					event.Str("api_version", v)
				}
			}

			// Log streaming-envelope sentinels so "handler reads raw
			// body without consuming the chunked envelope" stays
			// greppable. Present only when the header is.
			if ce := r.Header.Get("Content-Encoding"); ce != "" {
				event.Str("content_encoding", ce)
			}
			if te := r.Header.Get("Transfer-Encoding"); te != "" {
				event.Str("transfer_encoding", te)
			}
			if sha := r.Header.Get("x-amz-content-sha256"); strings.HasPrefix(sha, "STREAMING-") {
				event.Str("streaming_variant", sha)
			}
			if dcl := r.Header.Get("x-amz-decoded-content-length"); dcl != "" {
				event.Str("decoded_content_length", dcl)
			}
			if k := r.Header.Get("x-ms-encryption-key-sha256"); k != "" {
				event.Bool("azure_sse_c", true)
			}
			if k := r.Header.Get("x-goog-encryption-key-sha256"); k != "" {
				event.Bool("gcs_sse_c", true)
			}
			if sw.status >= 500 && sw.body.Len() > 0 {
				event.Str("error_body", sw.body.String())
			}

			event.Msg("request")
		})
	}
}

// Extract the caller identity without validating credentials, and accept a
// request that carries no auth headers at all.
func AuthPassthroughMiddleware(provider string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := extractIdentity(r, provider)
			ctx := context.WithValue(r.Context(), identityKey, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractIdentity(r *http.Request, provider string) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "anonymous"
	}

	switch provider {
	case "aws":
		// AWS SigV4: "AWS4-HMAC-SHA256 Credential=AKID/date/region/service/aws4_request, ..."
		if strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
			if idx := strings.Index(auth, "Credential="); idx >= 0 {
				cred := auth[idx+len("Credential="):]
				if slash := strings.Index(cred, "/"); slash > 0 {
					return cred[:slash]
				}
			}
		}
		return "aws-user"
	case "gcp":
		if strings.HasPrefix(auth, "Bearer ") {
			return "gcp-user"
		}
		return "gcp-user"
	case "azure":
		if strings.HasPrefix(auth, "Bearer ") {
			return "azure-user"
		}
		return "azure-user"
	}
	return "unknown"
}

func requestIDHeader(provider string) string {
	switch provider {
	case "aws":
		return "x-amzn-RequestId"
	case "gcp":
		return "x-goog-request-id"
	case "azure":
		return "x-ms-request-id"
	default:
		return "x-request-id"
	}
}

func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type statusWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	// Buffer error bodies only; large 2xx OCI/S3 transfers must not pay the
	// extra copy. WriteHeader has already run, so the status is known.
	if w.status >= 500 && w.body.Len() < loggedErrorBodyLimit {
		remaining := loggedErrorBodyLimit - w.body.Len()
		if len(p) > remaining {
			_, _ = w.body.Write(p[:remaining])
		} else {
			_, _ = w.body.Write(p)
		}
	}
	return w.ResponseWriter.Write(p)
}

// Without this the flush stops at the middleware and streaming handlers
// buffer their whole response.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Lets WebSocket upgrades through the middleware chain.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}
