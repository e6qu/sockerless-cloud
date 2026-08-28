package uiauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const monitoringTestToken = "sockerless-monitoring-token-00000000000000000000"

func monitoringConfig() Config {
	config := testConfig()
	config.ApplicationSlug = "sockerless-test"
	config.MonitoringToken = monitoringTestToken
	return config
}

func TestMonitoringConfigurationFailsClosed(t *testing.T) {
	config := monitoringConfig()
	config.MonitoringToken = "short"
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "SIM_MONITORING_TOKEN") {
		t.Fatalf("weak monitoring token error = %v", err)
	}
	config = Config{MonitoringToken: monitoringTestToken}
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "name and slug") {
		t.Fatalf("unnamed monitoring application error = %v", err)
	}
}

func TestMonitoringObservationAuthenticatesAndPublishesApplicationEvidence(t *testing.T) {
	auth, err := New(monitoringConfig())
	if err != nil {
		t.Fatal(err)
	}
	if auth.config.MonitoringToken != "" {
		t.Fatal("monitoring token plaintext remained in runtime configuration")
	}
	auth.store.put("active", sessionRecord{Expires: time.Now().Add(time.Hour).Unix()})
	mux := http.NewServeMux()
	auth.RegisterMonitoring(mux)

	for _, authorization := range []string{"", "bearer " + monitoringTestToken, "Bearer wrong-monitoring-token-00000000000000000000"} {
		request := httptest.NewRequest(http.MethodGet, MonitoringPath, nil)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, response.Code)
		}
		if response.Header().Get("WWW-Authenticate") != `Bearer realm="sockerless-monitoring"` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unauthorized headers = %#v", response.Header())
		}
	}

	request := httptest.NewRequest(http.MethodGet, MonitoringPath, nil)
	request.Header.Set("Authorization", "Bearer "+monitoringTestToken)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["schema_version"] != monitoringSchemaVersion {
		t.Fatalf("schema_version = %v", document["schema_version"])
	}
	if _, present := document["cost_estimate"]; present {
		t.Fatal("application observation fabricated a cost estimate")
	}
	resource := document["resources"].([]any)[0].(map[string]any)
	if resource["id"] != "sockerless-test-process" || resource["health"] != "healthy" || resource["kind"] != "application" {
		t.Fatalf("resource = %#v", resource)
	}
	metrics := resource["metrics"].([]any)
	if len(metrics) != 4 || metrics[0].(map[string]any)["value"] != float64(1) {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestMonitoringRouteIsAbsentWithoutADeploymentToken(t *testing.T) {
	auth, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	auth.RegisterMonitoring(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, MonitoringPath, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured monitoring route status = %d, want 404", response.Code)
	}
}
