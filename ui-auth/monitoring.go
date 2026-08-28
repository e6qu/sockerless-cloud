package uiauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	MonitoringPath          = "/monitoring/observation"
	monitoringSchemaVersion = "e6qu.monitoring/v2"
)

type monitoringTokenDigestValue [sha256.Size]byte

func monitoringTokenDigest(token string) (*monitoringTokenDigestValue, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) < 32 || strings.IndexFunc(token, func(character rune) bool {
		return character <= ' ' || character == '\u007f'
	}) >= 0 {
		return nil, errors.New("SIM_MONITORING_TOKEN must contain at least 32 non-whitespace characters")
	}
	digest := monitoringTokenDigestValue(sha256.Sum256([]byte(token)))
	return &digest, nil
}

type monitoringObservation struct {
	SchemaVersion string               `json:"schema_version"`
	ObservedAt    time.Time            `json:"observed_at"`
	Resources     []monitoringResource `json:"resources"`
}

type monitoringResource struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Kind    string             `json:"kind"`
	Health  string             `json:"health"`
	Metrics []monitoringMetric `json:"metrics"`
}

type monitoringMetric struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Status string  `json:"status"`
}

func monitoringAvailable(name, label string, value float64, unit string) monitoringMetric {
	return monitoringMetric{Name: name, Label: label, Value: value, Unit: unit, Status: "available"}
}

func (a *Auth) monitoringHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.monitoringAuthorized(r.Header.Get("Authorization")) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("WWW-Authenticate", `Bearer realm="sockerless-monitoring"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		now := time.Now()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		document := monitoringObservation{
			SchemaVersion: monitoringSchemaVersion,
			ObservedAt:    now.UTC(),
			Resources: []monitoringResource{{
				ID: a.config.ApplicationSlug + "-process", Name: a.config.ApplicationName, Kind: "application", Health: "healthy",
				Metrics: []monitoringMetric{
					monitoringAvailable("sessions.active", "Active operator sessions", float64(a.store.count(now)), "sessions"),
					monitoringAvailable("process.goroutines", "Process goroutines", float64(runtime.NumGoroutine()), "goroutines"),
					monitoringAvailable("process.heap", "Allocated heap", float64(memory.HeapAlloc)/(1024*1024), "MiB"),
					monitoringAvailable("process.uptime", "Process uptime", now.Sub(a.startedAt).Seconds(), "seconds"),
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		encodeJSON(w, document, "monitoring")
	})
}

func (a *Auth) monitoringAuthorized(header string) bool {
	if a == nil || a.monitoringTokenDigest == nil || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	actual := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	return subtle.ConstantTimeCompare(a.monitoringTokenDigest[:], actual[:]) == 1
}
