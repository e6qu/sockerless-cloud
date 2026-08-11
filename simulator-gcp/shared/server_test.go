package simulator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthReportsProcessRuntimeCapability(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "gcp", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got struct {
		Runtime      string          `json:"runtime"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if got.Runtime != "process" || got.Capabilities["workloadExecution"] {
		t.Fatalf("health capability = %#v, want process runtime without workload execution", got)
	}
}
