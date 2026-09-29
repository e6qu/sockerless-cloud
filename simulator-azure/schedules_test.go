package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

type scheduleTestSimulator struct {
	*sim.Server
	token string
}

func buildScheduleTestSimulator(t *testing.T) scheduleTestSimulator {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(bg.Await)
	t.Cleanup(srv.StopBackground)
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Azure Resource Manager token: %v", err)
	}
	return scheduleTestSimulator{Server: srv, token: token}
}

func armCall(t *testing.T, srv scheduleTestSimulator, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://localhost:4568"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+srv.token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestContainerAppsScheduledJobStartsExecutions drives the job schedule ticker
// at chosen instants: a Schedule-triggered job starts an execution at each
// occurrence of its UTC cronExpression, and a malformed cronExpression is
// refused when the job is written.
func TestContainerAppsScheduledJobStartsExecutions(t *testing.T) {
	srv := buildScheduleTestSimulator(t)
	const jobPath = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/jobs/nightly?api-version=2024-03-01"
	bad := `{"location":"eastus","properties":{"configuration":{"triggerType":"Schedule","replicaTimeout":60,"scheduleTriggerConfig":{"cronExpression":"0 2 * *"}}}}`
	if rec := armCall(t, srv, http.MethodPut, jobPath, bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("a four-field cronExpression was accepted: %d %s", rec.Code, rec.Body.String())
	}
	good := `{"location":"eastus","properties":{"configuration":{"triggerType":"Schedule","replicaTimeout":60,"scheduleTriggerConfig":{"cronExpression":"0 2 * * *"}}}}`
	if rec := armCall(t, srv, http.MethodPut, jobPath, good); rec.Code != http.StatusCreated {
		t.Fatalf("PUT job: %d %s", rec.Code, rec.Body.String())
	}
	executions := sim.NewStateStore[JobExecution]()
	ticker := cron.NewTicker(sim.NewStateStore[cron.Record](), func() []cron.Entry {
		return acaJobScheduleEntries(acaJobs, executions)
	})
	ticker.Tick(time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC))
	if n := executions.Len(); n != 0 {
		t.Fatalf("%d executions started before 02:00", n)
	}
	ticker.Tick(time.Date(2026, 6, 10, 2, 0, 1, 0, time.UTC))
	ticker.Tick(time.Date(2026, 6, 10, 2, 0, 2, 0, time.UTC))
	list := executions.List()
	if len(list) != 1 || !strings.HasPrefix(list[0].ID, "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/jobs/nightly/executions/nightly-") {
		t.Fatalf("executions after 02:00 = %+v, want one", list)
	}
}

func TestLogicRecurrenceOccurrences(t *testing.T) {
	saved := time.Date(2026, 6, 10, 12, 34, 56, 0, time.UTC)
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		recurrence map[string]any
		after      time.Time
		first      time.Time
		then       time.Time
	}{
		{
			name:       "every 15 minutes from the save",
			recurrence: map[string]any{"frequency": "Minute", "interval": 15},
			after:      saved,
			first:      saved,
			then:       saved.Add(15 * time.Minute),
		},
		{
			name:       "every 2 hours from a past startTime",
			recurrence: map[string]any{"frequency": "Hour", "interval": 2, "startTime": "2026-06-01T00:00:00Z"},
			after:      saved,
			first:      time.Date(2026, 6, 10, 14, 0, 0, 0, time.UTC),
			then:       time.Date(2026, 6, 10, 16, 0, 0, 0, time.UTC),
		},
		{
			name: "weekdays at 09:30 Pacific",
			recurrence: map[string]any{"frequency": "Week", "interval": 1, "startTime": "2026-06-01T00:00:00",
				"timeZone": "Pacific Standard Time",
				"schedule": map[string]any{"weekDays": []string{"Monday", "Friday"}, "hours": []int{9}, "minutes": []int{30}}},
			after: saved, // Wednesday
			first: time.Date(2026, 6, 12, 9, 30, 0, 0, pacific),
			then:  time.Date(2026, 6, 15, 9, 30, 0, 0, pacific),
		},
		{
			name:       "monthly on the 1st from startTime",
			recurrence: map[string]any{"frequency": "Month", "interval": 1, "startTime": "2026-01-01T06:00:00Z"},
			after:      saved,
			first:      time.Date(2026, 7, 1, 6, 0, 0, 0, time.UTC),
			then:       time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		r, err := parseLogicRecurrence(c.recurrence, saved)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		first, ok := r.first(c.after)
		if !ok || !first.Equal(c.first) {
			t.Errorf("%s: first = %s, want %s", c.name, first, c.first)
		}
		then, ok := r.next(first)
		if !ok || !then.Equal(c.then) {
			t.Errorf("%s: next = %s, want %s", c.name, then, c.then)
		}
	}
	for _, bad := range []map[string]any{
		{"frequency": "Fortnight", "interval": 1},
		{"frequency": "Day", "interval": 0},
		{"frequency": "Day", "interval": 1, "timeZone": "Mars Standard Time"},
		{"frequency": "Hour", "interval": 1, "schedule": map[string]any{"hours": []int{3}}},
		{"frequency": "Week", "interval": 1, "schedule": map[string]any{"weekDays": []string{"Caturday"}}},
	} {
		if _, err := parseLogicRecurrence(bad, saved); err == nil {
			t.Errorf("recurrence %v was accepted", bad)
		}
	}
}

// TestLogicRecurrenceTriggerRunsTheWorkflow drives the recurrence ticker: an
// enabled workflow's Recurrence trigger runs it at each occurrence, and a
// disabled workflow does not run.
func TestLogicRecurrenceTriggerRunsTheWorkflow(t *testing.T) {
	srv := buildScheduleTestSimulator(t)
	const path = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Logic/workflows/hourly?api-version=2019-05-01"
	body := `{"location":"eastus","properties":{"definition":{"triggers":{"every-hour":{"type":"Recurrence",` +
		`"recurrence":{"frequency":"Hour","interval":1,"startTime":"2026-06-10T00:00:00Z"}}},"actions":{}}}}`
	if rec := armCall(t, srv, http.MethodPut, path, body); rec.Code != http.StatusOK {
		t.Fatalf("PUT workflow: %d %s", rec.Code, rec.Body.String())
	}
	badBody := strings.Replace(body, `"Hour"`, `"Fortnight"`, 1)
	if rec := armCall(t, srv, http.MethodPut, strings.Replace(path, "hourly", "broken", 1), badBody); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown frequency was accepted: %d", rec.Code)
	}
	runs := func() int {
		return len(logicRuns.Filter(func(run LogicWorkflowRun) bool { return strings.Contains(run.ID, "/workflows/hourly/runs/") }))
	}
	ticker := cron.NewTicker(sim.NewStateStore[cron.Record](), func() []cron.Entry { return logicRecurrenceEntries(logicWorkflows) })
	ticker.Tick(time.Date(2026, 6, 10, 2, 30, 0, 0, time.UTC))
	if n := runs(); n != 0 {
		t.Fatalf("%d runs before the next hour", n)
	}
	ticker.Tick(time.Date(2026, 6, 10, 3, 0, 1, 0, time.UTC))
	if n := runs(); n != 1 {
		t.Fatalf("%d runs after 03:00, want 1", n)
	}
	disable := strings.Replace(path, "?api-version", "/disable?api-version", 1)
	if rec := armCall(t, srv, http.MethodPost, disable, ""); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	ticker.Tick(time.Date(2026, 6, 10, 4, 0, 1, 0, time.UTC))
	if n := runs(); n != 1 {
		t.Fatalf("a disabled workflow ran: %d runs", n)
	}
}
