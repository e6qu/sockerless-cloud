package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func buildQuotaTestSimulator(t *testing.T, project string) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, project)
	return srv
}

func cpuContainers(limits ...string) []Container {
	out := make([]Container, len(limits))
	for i, l := range limits {
		out[i] = Container{Name: "c", Resources: &ResourceRequirements{Limits: map[string]string{"cpu": l}}}
	}
	return out
}

// setCPUQuotaPreference sets the project's run.googleapis.com CPU quota in a
// region through a Cloud Quotas QuotaPreference and returns the status.
func setCPUQuotaPreference(t *testing.T, srv *sim.Server, project, id, region, value, ignore string) (int, map[string]any) {
	t.Helper()
	path := "/v1/projects/" + project + "/locations/global/quotaPreferences/" + id + "?allowMissing=true"
	if ignore != "" {
		path += "&" + ignore
	}
	return gcpHostCall(t, srv, "cloudquotas.googleapis.com", http.MethodPatch, path,
		`{"service":"run.googleapis.com","quotaId":"CpuAllocPerProjectRegion","dimensions":{"region":"`+region+`"},`+
			`"quotaConfig":{"preferredValue":"`+value+`"},"contactEmail":"ops@example.com"}`)
}

// Without a preference the quota in effect is CpuAllocPerProjectRegion's
// default, which a few instances do not reach.
func TestRegionalCPUQuota_DefaultLimitWithoutPreference(t *testing.T) {
	buildQuotaTestSimulator(t, "quota-unit-default")
	if got := cloudQuotaLimit(cloudRunCPUQuota(), "quota-unit-default", "us-central1"); got != 800000 {
		t.Fatalf("default limit = %d, want 800000", got)
	}
	for i := 0; i < 3; i++ {
		hold, err := cloudRunHoldCPU("quota-unit-default", "us-central1", cpuContainers("2"))
		if err != nil {
			t.Fatalf("hold %d: %v", i, err)
		}
		t.Cleanup(hold.release)
	}
	if got := cloudRunAllocatedCPU("quota-unit-default", "us-central1"); got != 6000 {
		t.Fatalf("allocated = %d, want 6000", got)
	}
}

// A preference's granted value bounds the CPU the region's instances hold:
// the start that would pass it is refused, and a stopped instance's CPU is
// free again. Each region has its own value.
func TestRegionalCPUQuota_PreferenceBoundsAllocatedCPU(t *testing.T) {
	const project = "quota-unit-bound"
	srv := buildQuotaTestSimulator(t, project)
	if code, body := setCPUQuotaPreference(t, srv, project, "cpu-us-central1", "us-central1", "2000", "ignoreSafetyChecks=QUOTA_DECREASE_PERCENTAGE_TOO_HIGH"); code != http.StatusOK {
		t.Fatalf("set preference answered %d: %v", code, body)
	}
	first, err := cloudRunHoldCPU(project, "us-central1", cpuContainers("1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := cloudRunHoldCPU(project, "us-central1", cpuContainers("500m", "500m"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	if _, err := cloudRunHoldCPU(project, "us-central1", cpuContainers("0.5")); !errors.Is(err, errCloudRunCPUQuota) {
		t.Fatalf("a start past the preferred 2000 milli vCPU = %v, want the quota error", err)
	}
	if cloudRunDeployFits(project, "us-central1", ServiceV2{Template: &RevisionTemplate{Containers: cpuContainers("1")}}) {
		t.Fatal("a deploy into a full region fits")
	}
	other, err := cloudRunHoldCPU(project, "europe-west1", cpuContainers("4"))
	if err != nil {
		t.Fatalf("europe-west1 has its own quota: %v", err)
	}
	defer other.release()

	first.release()
	first.release()
	third, err := cloudRunHoldCPU(project, "us-central1", cpuContainers("1"))
	if err != nil {
		t.Fatalf("a stopped instance's CPU is free again: %v", err)
	}
	defer third.release()
	if got := cloudRunAllocatedCPU(project, "us-central1"); got != 2000 {
		t.Fatalf("allocated = %d, want 2000: a second release gives nothing back", got)
	}
}

// Cloud Quotas refuses a decrease of more than 10 percent, or below the usage,
// unless the request waives that check.
func TestRegionalCPUQuota_DecreaseSafetyChecks(t *testing.T) {
	const project = "quota-unit-safety"
	srv := buildQuotaTestSimulator(t, project)
	code, body := setCPUQuotaPreference(t, srv, project, "cpu", "us-central1", "700000", "")
	if code != http.StatusBadRequest || !strings.Contains(body["error"].(map[string]any)["status"].(string), "FAILED_PRECONDITION") {
		t.Fatalf("a 12.5 percent decrease answered %d %v, want FAILED_PRECONDITION", code, body)
	}
	if code, body := setCPUQuotaPreference(t, srv, project, "cpu", "us-central1", "750000", ""); code != http.StatusOK {
		t.Fatalf("a decrease within 10 percent answered %d %v", code, body)
	}

	hold, err := cloudRunHoldCPU(project, "us-central1", cpuContainers("2"))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.release()
	if code, _ := setCPUQuotaPreference(t, srv, project, "cpu", "us-central1", "1000", "ignoreSafetyChecks=QUOTA_DECREASE_PERCENTAGE_TOO_HIGH"); code != http.StatusBadRequest {
		t.Fatalf("a decrease below the 2000 in use answered %d, want 400", code)
	}
	if code, body := setCPUQuotaPreference(t, srv, project, "cpu", "us-central1", "1000",
		"ignoreSafetyChecks=QUOTA_DECREASE_PERCENTAGE_TOO_HIGH&ignoreSafetyChecks=QUOTA_DECREASE_BELOW_USAGE"); code != http.StatusOK {
		t.Fatalf("a decrease waiving both checks answered %d %v", code, body)
	}
	if got := cloudQuotaLimit(cloudRunCPUQuota(), project, "us-central1"); got != 1000 {
		t.Fatalf("limit = %d, want 1000", got)
	}
}

// Whole ("1"), fractional ("0.5") and milli-vCPU ("500m") limits convert to
// the same vCPU count.
func TestContainerCPULoad_ParsesAllFormats(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 1.0},
		{"1", 1.0},
		{"2", 2.0},
		{"0.5", 0.5},
		{"500m", 0.5},
		{"100m", 0.1},
		{"  1  ", 1.0},
	}
	for _, c := range cases {
		got := containerCPULoad(c.in)
		if got != c.want {
			t.Errorf("containerCPULoad(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// A multi-container instance holds the sum of its containers' CPU limits.
func TestCloudRunInstanceMilliCPU_SumsAcrossContainers(t *testing.T) {
	if got := cloudRunInstanceMilliCPU(cpuContainers("1", "1", "1")); got != 3000 {
		t.Errorf("3-container instance CPU = %d, want 3000", got)
	}
	if got := cloudRunInstanceMilliCPU(cpuContainers("100m")); got != 100 {
		t.Errorf("100m instance CPU = %d, want 100", got)
	}
}

// The error payload matches Cloud Run's exactly, since clients match on its
// message.
func TestRegionalCPUQuotaErrorJSON_Format(t *testing.T) {
	rec := httptest.NewRecorder()
	regionalCPUQuotaErrorJSON(rec, "projects/p/locations/us-central1/services/foo")
	body := rec.Body.String()
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(body, "Quota exceeded for total allowable CPU per project per region") {
		t.Errorf("body missing the canonical quota message; got %q", body)
	}
	if !strings.Contains(body, "INVALID_ARGUMENT") {
		t.Errorf("body missing INVALID_ARGUMENT status; got %q", body)
	}
	if !strings.Contains(body, "projects/p/locations/us-central1/services/foo") {
		t.Errorf("body missing resource name; got %q", body)
	}
}
