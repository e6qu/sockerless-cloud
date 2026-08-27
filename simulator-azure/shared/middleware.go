package simulator

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

// Caps how much of a 5xx body statusWriter.Write keeps for the request log.
const loggedErrorBodyLimit = 4096

type contextKey int

const (
	requestIDKey contextKey = iota
	identityKey
)

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

// Azure Resource Manager matches URL paths case-insensitively, and its clients
// disagree: the azurerm Terraform provider sends `microsoft.cache/redis` where
// SDK clients send `Microsoft.Cache/Redis`. Both reach real Azure, so both must
// reach the simulator. Canonicalize to the casing the routes register.
func AzurePathNormalizationMiddleware(next http.Handler) http.Handler {
	// Resource-type and provider segments map to canonical mixed case.
	// Action and sub-resource verbs map to lowercase, because clients vary
	// (`appSettings` from terraform-provider-azurerm, `appsettings` from
	// azurestack) and the handlers register one casing.
	replacements := map[string]string{
		// No trailing slash: the segment also ends the SDK's
		// list-resource-groups URL.
		"/resourcegroups":            "/resourceGroups",
		"/microsoft.cache/redis":     "/Microsoft.Cache/Redis",
		"/microsoft.cache":           "/Microsoft.Cache",
		"/microsoft.servicebus":      "/Microsoft.ServiceBus",
		"/microsoft.apimanagement":   "/Microsoft.ApiManagement",
		"/microsoft.dbforpostgresql": "/Microsoft.DBforPostgreSQL",
		"/microsoft.keyvault":        "/Microsoft.KeyVault",
		"/microsoft.storage":         "/Microsoft.Storage",
		// azure-mgmt-web spells the namespace "microsoft.Web" in several
		// StaticSites URL templates.
		"/microsoft.web": "/Microsoft.Web",

		"/appsettings":                        "/appsettings",
		"/connectionstrings":                  "/connectionstrings",
		"/slotconfignames":                    "/slotconfignames",
		"/listsecrets":                        "/listsecrets",
		"/listcredentials":                    "/listcredentials",
		"/checknameavailability":              "/checknameavailability",
		"/authsettings":                       "/authsettings",
		"/authsettingsv2":                     "/authsettingsv2",
		"/publishingcredentials":              "/publishingcredentials",
		"/azurestorageaccounts":               "/azurestorageaccounts",
		"/basicpublishingcredentialspolicies": "/basicpublishingcredentialspolicies",
		"/deletedvaults":                      "/deletedVaults",
		"/deletedworkspaces":                  "/deletedWorkspaces",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		lower := strings.ToLower(path)
		for lowerSeg, canonical := range replacements {
			if idx := strings.Index(lower, lowerSeg); idx >= 0 {
				path = path[:idx] + canonical + path[idx+len(lowerSeg):]
				lower = strings.ToLower(path)
			}
		}
		if path != r.URL.Path {
			r2 := r.Clone(r.Context())
			r2.URL.Path = path
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	// extra copy.
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

// Lets WebSocket upgrades through the middleware chain.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}
