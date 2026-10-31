package main

import (
	"net/http"
	"sort"
	"strings"
)

// Google's API front end answers a browser's cross-origin call to a service
// host the way specs/cloud-api/gcp/googleapis-cors.http.gz records: it admits
// any origin, answers a preflight itself before the service authenticates the
// call, and exposes the response's own headers.
const (
	gcpCORSAllowMethods = "DELETE,GET,HEAD,OPTIONS,PATCH,POST,PUT"
	gcpCORSMaxAge       = "3600"
)

// The front end exposes these after the headers the service sets.
var gcpCORSFrontEndExposed = []string{"content-encoding", "date", "server", "content-length"}

// gcpCORSMiddleware serves CORS on the requests that name a Google API host,
// the only ones a browser sends cross-origin: the console's own origin is the
// simulator's bare coordinate.
func gcpCORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || gcpServiceFromHost(r) == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h["Vary"] = []string{"origin", "referer", "x-origin"}
			h.Set("Access-Control-Allow-Methods", gcpCORSAllowMethods)
			if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
				h.Set("Access-Control-Allow-Headers", requested)
			}
			h.Set("Access-Control-Max-Age", gcpCORSMaxAge)
			h.Set("Content-Type", "text/html")
			h.Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(&gcpCORSResponseWriter{ResponseWriter: w, origin: origin}, r)
	})
}

type gcpCORSResponseWriter struct {
	http.ResponseWriter
	origin      string
	wroteHeader bool
}

func (w *gcpCORSResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		h := w.Header()
		h.Del("Vary")
		h["Vary"] = []string{"X-Origin", "Referer", "Origin"}
		var exposed []string
		names := make([]string, 0, len(h))
		for name := range h {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			lower := strings.ToLower(name)
			if gcpCORSSafelisted[lower] || strings.HasPrefix(lower, "access-control-") {
				continue
			}
			for range h[name] {
				exposed = append(exposed, lower)
			}
		}
		h.Set("Access-Control-Allow-Origin", w.origin)
		h.Set("Access-Control-Expose-Headers", strings.Join(append(exposed, gcpCORSFrontEndExposed...), ","))
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gcpCORSResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gcpCORSResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wroteHeader {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

func (w *gcpCORSResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// gcpCORSSafelisted are the CORS-safelisted response header names, which a
// browser exposes without being told and the front end does not list.
var gcpCORSSafelisted = map[string]bool{
	"cache-control": true, "content-language": true, "content-length": true, "content-type": true,
	"expires": true, "last-modified": true, "pragma": true,
}
