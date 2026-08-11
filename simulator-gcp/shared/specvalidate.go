package simulator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
)

// Runtime spec validation: when SOCKERLESS_SPEC_VALIDATE names a report
// file, every request/response through the server is handed to the
// validator the simulator registered via SetSpecValidator, and the
// violations it returns are appended to the report as JSON lines. The
// test harnesses run with validation on and gate on the report via
// scripts/check-spec-violations.sh; production runs leave the variable
// unset and pay nothing.

// specCaptureBodyLimit bounds the request body the spec-validate middleware
// materializes per exchange. Validation is test-only (gated by
// SOCKERLESS_SPEC_VALIDATE) but still must not allow an unbounded read; 256 MiB
// covers every legitimate validated request (control-plane JSON/XML, never raw
// blob uploads — those go through the OCI data plane which has its own cap).
const specCaptureBodyLimit = 256 << 20 // 256 MiB

// SpecViolation is one observed divergence between a simulator response
// and the vendored cloud API spec (specs/cloud-api/).
type SpecViolation struct {
	// Op identifies the operation (X-Amz-Target, "METHOD /path", ...).
	Op string `json:"op"`
	// Kind classifies the divergence (unknown-field, type-mismatch, ...).
	Kind string `json:"kind"`
	// Field is the JSON path of the offending member.
	Field string `json:"field"`
	// Detail is a human-readable explanation.
	Detail string `json:"detail"`
}

// Key is the stable identity used by the ratchet allowlist.
func (v SpecViolation) Key() string {
	return v.Op + "\t" + v.Kind + "\t" + v.Field
}

// SpecValidator inspects one exchange and reports spec divergences.
// status/respBody are the simulator's response; reqBody is the request
// payload (already drained and restored on the request the handler saw).
type SpecValidator func(req *http.Request, reqBody []byte, status int, respHeader http.Header, respBody []byte) []SpecViolation

// SetSpecValidator registers the per-cloud validator and, when
// SOCKERLESS_SPEC_VALIDATE is set, arms the capture middleware. Must be
// called before ListenAndServe / ServeHTTP.
func (s *Server) SetSpecValidator(v SpecValidator) {
	report := os.Getenv("SOCKERLESS_SPEC_VALIDATE")
	if report == "" || v == nil {
		return
	}
	f, err := os.OpenFile(report, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// The operator asked for validation; degrading silently would
		// produce a green run that validated nothing.
		s.logger.Fatal().Err(err).Str("report", report).Msg("spec-validate: cannot open report file")
		return
	}
	var mu sync.Mutex
	enc := json.NewEncoder(f)
	s.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody []byte
			if r.Body != nil {
				// Validation snapshots every request body and restores it for the
				// handler. Bound the materialized body so a maliciously large
				// upload can't OOM a validation run; bodies past the cap are
				// rejected loudly rather than silently truncated (a truncated
				// body fed to the handler would corrupt the exchange).
				lr := io.LimitReader(r.Body, specCaptureBodyLimit+1)
				var readErr error
				reqBody, readErr = io.ReadAll(lr)
				_ = r.Body.Close()
				if readErr != nil {
					// A failed body read (e.g. a client disconnect mid-upload) must
					// not feed a truncated body silently to the handler.
					http.Error(w, "failed to read request body: "+readErr.Error(), http.StatusBadRequest)
					return
				}
				if int64(len(reqBody)) > specCaptureBodyLimit {
					http.Error(w, "request body exceeds spec-validate capture limit", http.StatusRequestEntityTooLarge)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}
			rec := &specCaptureWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			violations := v(r, reqBody, rec.status, rec.Header(), rec.body.Bytes())
			if len(violations) == 0 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, viol := range violations {
				if err := enc.Encode(viol); err != nil {
					s.logger.Error().Err(err).Msg("spec-validate: write report")
					return
				}
			}
		})
	})
	s.logger.Info().Str("report", report).Msg("spec-validate: armed")
}

// specCaptureWriter tees the response for validation. Hijack (WebSocket
// upgrades) passes through and marks the exchange unvalidatable.
type specCaptureWriter struct {
	http.ResponseWriter
	status   int
	body     bytes.Buffer
	hijacked bool
}

func (w *specCaptureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *specCaptureWriter) Write(b []byte) (int, error) {
	if !w.hijacked {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *specCaptureWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack lets WebSocket/exec upgrades pass through; a hijacked exchange
// carries no HTTP response body to validate.
func (w *specCaptureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
	}
	w.hijacked = true
	return h.Hijack()
}
