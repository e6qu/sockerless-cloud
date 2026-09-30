package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func ec2AppStatusCall(t *testing.T, handler http.HandlerFunc, form url.Values) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder.Body.String()
}

// ec2AppStatusBackend is an instance's application: it answers every check
// with the status the test sets, and counts the checks it received.
func ec2AppStatusBackend(t *testing.T) (port int, status *atomic.Int32, probes *atomic.Int32) {
	t.Helper()
	status, probes = &atomic.Int32{}, &atomic.Int32{}
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(server.Close)
	return server.Listener.Addr().(*net.TCPAddr).Port, status, probes
}

func ec2AppStatusSetup(t *testing.T) {
	t.Helper()
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	ec2AppStatusChecks = sim.MakeStore[EC2ApplicationStatusCheck](nil, "ec2_app_status_checks")
	ec2AppStatusAssociations = sim.MakeStore[EC2ApplicationStatusCheckAssociation](nil, "ec2_app_status_associations")
	ec2AppStatusTracker.Reset()
	t.Cleanup(ec2AppStatusTracker.Reset)
}

func ec2AppStatusCreateAndAssociate(t *testing.T, instanceID string, port int, extra url.Values) string {
	t.Helper()
	form := url.Values{"Protocol": {"http"}, "Port": {strconv.Itoa(port)}, "Path": {"/health"}}
	for key, values := range extra {
		form[key] = values
	}
	created := ec2AppStatusCall(t, handleCreateApplicationStatusCheck, form)
	match := regexp.MustCompile(`<applicationStatusCheckId>([^<]+)</applicationStatusCheckId>`).FindStringSubmatch(created)
	if match == nil {
		t.Fatalf("CreateApplicationStatusCheck answered %s", created)
	}
	associated := ec2AppStatusCall(t, handleAssociateApplicationStatusCheck, url.Values{
		"ApplicationStatusCheckId": {match[1]}, "InstanceId.1": {instanceID},
	})
	if !strings.Contains(associated, "<associationValue>"+instanceID+"</associationValue>") {
		t.Fatalf("AssociateApplicationStatusCheck answered %s", associated)
	}
	return match[1]
}

func ec2AppStatusOf(t *testing.T, instanceID string) (instanceStatus, checkStatus, detail string) {
	t.Helper()
	body := ec2AppStatusCall(t, handleDescribeApplicationStatus, url.Values{"InstanceId.1": {instanceID}})
	instance := regexp.MustCompile(`<applicationStatus><status>([^<]+)</status>`).FindStringSubmatch(body)
	check := regexp.MustCompile(`<detailSet><item>.*?<status>([^<]+)</status>(.*?)</item>`).FindStringSubmatch(body)
	if instance == nil || check == nil {
		t.Fatalf("DescribeApplicationStatus answered %s", body)
	}
	return instance[1], check[1], check[2]
}

// The checker probes each association on the check's Interval, and the check
// passes or fails only after SuccessThreshold or FailureThreshold consecutive
// results — whenever DescribeApplicationStatus is called.
func TestEC2ApplicationStatusFollowsIntervalAndThresholds(t *testing.T) {
	ec2AppStatusSetup(t)
	port, status, probes := ec2AppStatusBackend(t)
	start := time.Now()
	ec2Instances.Put("i-app-status", EC2Instance{
		InstanceId: "i-app-status", State: "running", PrivateIpAddress: "127.0.0.1",
		LaunchTime: start.Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	ec2AppStatusCreateAndAssociate(t, "i-app-status", port, url.Values{"SuccessThreshold": {"2"}, "FailureThreshold": {"2"}})

	if instance, check, _ := ec2AppStatusOf(t, "i-app-status"); instance != "initializing" || check != "initializing" || probes.Load() != 0 {
		t.Fatalf("before any check: instance %q check %q after %d probes, want initializing and no probe from Describe", instance, check, probes.Load())
	}
	ctx := context.Background()
	steps := []struct {
		offset     time.Duration
		answer     int32
		probes     int32
		instance   string
		check      string
		reasonPart string
	}{
		{0, http.StatusOK, 1, "initializing", "initializing", "<code>ResponseCodeMatched</code><statusCode>200</statusCode><protocol>HTTP</protocol>"},
		{30 * time.Second, http.StatusOK, 1, "initializing", "initializing", "ResponseCodeMatched"},
		{60 * time.Second, http.StatusOK, 2, "ok", "passed", "ResponseCodeMatched"},
		{120 * time.Second, http.StatusServiceUnavailable, 3, "ok", "passed", "<code>ResponseCodeMismatch</code><statusCode>503</statusCode>"},
		{180 * time.Second, http.StatusServiceUnavailable, 4, "impaired", "failed", "ResponseCodeMismatch"},
	}
	for _, step := range steps {
		status.Store(step.answer)
		ec2CheckApplicationStatus(ctx, start.Add(step.offset))
		instance, check, detail := ec2AppStatusOf(t, "i-app-status")
		if probes.Load() != step.probes || instance != step.instance || check != step.check || !strings.Contains(detail, step.reasonPart) {
			t.Fatalf("at +%s: %d probes, instance %q, check %q, detail %s; want %d probes, %q, %q, %s",
				step.offset, probes.Load(), instance, check, detail, step.probes, step.instance, step.check, step.reasonPart)
		}
	}
}

// No check runs before the instance's InitializationGracePeriodSeconds has
// passed since launch.
func TestEC2ApplicationStatusWaitsOutTheGracePeriod(t *testing.T) {
	ec2AppStatusSetup(t)
	port, _, probes := ec2AppStatusBackend(t)
	launched := time.Now()
	ec2Instances.Put("i-app-grace", EC2Instance{
		InstanceId: "i-app-grace", State: "running", PrivateIpAddress: "127.0.0.1",
		LaunchTime: launched.UTC().Format(time.RFC3339),
	})
	ec2AppStatusCreateAndAssociate(t, "i-app-grace", port, url.Values{"SuccessThreshold": {"1"}, "InitializationGracePeriodSeconds": {"300"}})
	ctx := context.Background()
	ec2CheckApplicationStatus(ctx, launched.Add(100*time.Second))
	if instance, _, _ := ec2AppStatusOf(t, "i-app-grace"); probes.Load() != 0 || instance != "initializing" {
		t.Fatalf("inside the grace period: %d probes, instance %q", probes.Load(), instance)
	}
	ec2CheckApplicationStatus(ctx, launched.Add(301*time.Second))
	if instance, check, _ := ec2AppStatusOf(t, "i-app-grace"); probes.Load() != 1 || instance != "ok" || check != "passed" {
		t.Fatalf("after the grace period: %d probes, instance %q check %q", probes.Load(), instance, check)
	}
}

// A check refuses the schedule the Amazon EC2 model does not admit.
func TestEC2ApplicationStatusCheckRefusesAnInvalidSchedule(t *testing.T) {
	ec2AppStatusSetup(t)
	for name, form := range map[string]url.Values{
		"Interval 30":         {"Port": {"80"}, "Interval": {"30"}},
		"Timeout 60":          {"Port": {"80"}, "Timeout": {"60"}},
		"FailureThreshold 0":  {"Port": {"80"}, "FailureThreshold": {"0"}},
		"Aggregation partial": {"Port": {"80"}, "Aggregation": {"partial"}},
	} {
		answer := ec2AppStatusCall(t, handleCreateApplicationStatusCheck, form)
		if !strings.Contains(answer, "<Code>InvalidParameterValue</Code>") {
			t.Fatalf("%s: CreateApplicationStatusCheck answered %s, want InvalidParameterValue", name, answer)
		}
	}
	if checks := ec2AppStatusChecks.List(); len(checks) != 0 {
		t.Fatalf("refused checks were stored: %v", checks)
	}
}
