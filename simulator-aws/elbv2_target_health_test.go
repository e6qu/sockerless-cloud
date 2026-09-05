package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Elastic Load Balancing health-checks registered targets continuously, on the
// target group's configured interval, and reports the result of that checker.
// These tests hold the simulator to the published state machine: a describe
// never checks a target, a registration enters service on its first successful
// check, and leaving and re-entering service take UnhealthyThresholdCount and
// HealthyThresholdCount consecutive checks respectively.

// elbv2TargetHealthTestStores gives one test its own stores and its own empty
// target health. The checker reads the Amazon ECS task store to resolve a
// container's published port and the service store to wake service schedulers,
// so both must be real stores even when a test registers targets directly.
func elbv2TargetHealthTestStores() {
	// Work started by whatever ran before this must finish before the stores it
	// is reading are replaced. A sweep reacts to target health by requesting a
	// service reconciliation, so draining has to cover the work the checker
	// itself started, not only the work the previous test asked for directly.
	AwaitSimulatorBackground()
	elbv2LoadBalancers = sim.MakeStore[ELBv2LoadBalancer](nil, "elbv2_load_balancers")
	elbv2TargetGroups = sim.MakeStore[ELBv2TargetGroup](nil, "elbv2_target_groups")
	elbv2Listeners = sim.MakeStore[ELBv2Listener](nil, "elbv2_listeners")
	// Whether a listener rule forwards to a target group decides whether it is
	// health-checked at all, so the rule store is as much a part of the
	// checker's input as the listener store.
	elbv2Rules = sim.MakeStore[ELBv2Rule](nil, "elbv2_rules")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsServices = sim.MakeStore[ECSService](nil, "ecs_services")
	elbv2TargetHealthMu.Lock()
	elbv2TargetHealthRecords = map[string]*ELBv2TargetHealth{}
	elbv2TargetHealthMu.Unlock()
}

// switchableHealthCheckTarget serves the health check and can be switched
// between answering 200 and answering 503.
func switchableHealthCheckTarget(t *testing.T) (port int, serving *atomic.Bool) {
	t.Helper()
	serving = &atomic.Bool{}
	serving.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !serving.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	port, err = strconv.Atoi(portText)
	require.NoError(t, err)
	return port, serving
}

// closedHealthCheckTarget is a port nothing listens on, so a health check
// against it is refused rather than timing out.
func closedHealthCheckTarget(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

// putELBv2UnusedTargetGroup registers one target group holding one target on
// 127.0.0.1, with no listener forwarding to it.
func putELBv2UnusedTargetGroup(name string, port int, tune func(*ELBv2TargetGroup)) ELBv2TargetGroup {
	tg := ELBv2TargetGroup{
		Arn:                     "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/" + name + "/0123456789abcdef",
		Name:                    name,
		Protocol:                "HTTP",
		Port:                    port,
		TargetType:              "ip",
		HealthCheckProtocol:     "HTTP",
		HealthCheckPath:         "/",
		HealthCheckEnabled:      true,
		HealthCheckInterval:     30,
		HealthCheckTimeout:      2,
		HealthyThresholdCount:   5,
		UnhealthyThresholdCount: 2,
		Targets:                 []ELBv2TargetDescription{{ID: "127.0.0.1", Port: port}},
		Attributes:              defaultELBv2TargetGroupAttributes(),
	}
	if tune != nil {
		tune(&tg)
	}
	elbv2TargetGroups.Put(tg.Arn, tg)
	return tg
}

// putELBv2ListenerForTargetGroup puts a listener whose default action forwards
// to the target group. A listener's default actions are its default rule, so
// this is what makes the target group one Elastic Load Balancing health-checks:
// "Health checks are performed on all targets registered to a target group that
// is specified in a listener rule for your load balancer."
func putELBv2ListenerForTargetGroup(tg ELBv2TargetGroup) ELBv2Listener {
	loadBalancerArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/" +
		tg.Name + "/0123456789abcdef"
	listener := ELBv2Listener{
		Arn:             loadBalancerArn + "/fedcba9876543210",
		LoadBalancerArn: loadBalancerArn,
		Protocol:        "HTTP",
		Port:            80,
		DefaultActions:  []ELBv2Action{{Type: "forward", TargetGroupArn: tg.Arn}},
	}
	elbv2Listeners.Put(listener.Arn, listener)
	return listener
}

// putELBv2HealthCheckedTargetGroup registers one target group holding one
// target on 127.0.0.1, forwarded to by a listener so the checker checks it.
func putELBv2HealthCheckedTargetGroup(name string, port int, tune func(*ELBv2TargetGroup)) ELBv2TargetGroup {
	tg := putELBv2UnusedTargetGroup(name, port, tune)
	putELBv2ListenerForTargetGroup(tg)
	return tg
}

func elbv2OnlyTarget(tg ELBv2TargetGroup) ELBv2TargetDescription { return tg.Targets[0] }

func elbv2DescribeTargetHealthRequest(targetGroupArn string) *http.Request {
	form := "Action=DescribeTargetHealth&Version=" + elbv2APIVersion +
		"&TargetGroupArn=" + targetGroupArn
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// TestDescribeTargetHealthDoesNotProbeTargets measures the read against a
// registered target that accepts connections and never answers. Elastic Load
// Balancing checks target health on the target group's configured interval and
// answers a describe from what that checker recorded; it does not check a
// target because someone asked about it. A read that checks pays one full
// health-check timeout per registered target, so this read must answer in far
// less than the configured timeout.
func TestDescribeTargetHealthDoesNotProbeTargets(t *testing.T) {
	elbv2TargetHealthTestStores()
	port := hangingHealthCheckTarget(t)
	const healthCheckTimeout = 5
	tg := putELBv2HealthCheckedTargetGroup("read-tg", port, func(tg *ELBv2TargetGroup) {
		tg.HealthCheckTimeout = healthCheckTimeout
	})

	recorder := httptest.NewRecorder()
	started := time.Now()
	handleELBv2DescribeTargetHealth(recorder, elbv2DescribeTargetHealthRequest(tg.Arn))
	elapsed := time.Since(started)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	t.Logf("DescribeTargetHealth answered in %s against an unanswering target "+
		"(health check timeout %ds)", elapsed, healthCheckTimeout)

	require.Lessf(t, elapsed, time.Second,
		"DescribeTargetHealth took %s: it health-checked the target group's registered "+
			"targets on the request path, which costs one health-check timeout per "+
			"unresponsive target", elapsed)

	// A target the checker has not reached yet is registering, not healthy.
	require.Contains(t, recorder.Body.String(), "<State>initial</State>")
	require.Contains(t, recorder.Body.String(), "<Reason>Elb.RegistrationInProgress</Reason>")
	require.Contains(t, recorder.Body.String(),
		"<Description>Target registration is in progress</Description>")
	require.Contains(t, recorder.Body.String(),
		"<HealthCheckPort>"+strconv.Itoa(port)+"</HealthCheckPort>")
}

// TestTargetEntersServiceOnItsFirstSuccessfulHealthCheck holds the simulator to
// "After your target is registered, it must pass one health check to be
// considered healthy." A healthy target carries neither a reason code nor a
// description.
func TestTargetEntersServiceOnItsFirstSuccessfulHealthCheck(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := switchableHealthCheckTarget(t)
	tg := putELBv2HealthCheckedTargetGroup("first-check-tg", port, nil)
	target := elbv2OnlyTarget(tg)

	require.Equal(t, elbv2TargetStateInitial, elbv2TargetHealthFor(tg, target).State)
	require.False(t, elbv2TargetReceivesTraffic(tg, target),
		"a load balancer must not forward to a target that has not passed a health check")

	elbv2CheckTargetHealth(context.Background(), time.Now())

	health := elbv2TargetHealthFor(tg, target)
	require.Equal(t, elbv2TargetStateHealthy, health.State)
	require.Empty(t, health.Reason)
	require.Empty(t, health.Description)
	require.True(t, elbv2TargetReceivesTraffic(tg, target))
}

// TestTargetLeavesServiceAfterConsecutiveFailedHealthChecks holds the simulator
// to "If the health checks exceed UnhealthyThresholdCount consecutive failures,
// the load balancer takes the target out of service." One failure is not
// enough, and the reason code names how the check failed — here the target
// answers 503, which the default Matcher of 200 excludes.
func TestTargetLeavesServiceAfterConsecutiveFailedHealthChecks(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, serving := switchableHealthCheckTarget(t)
	tg := putELBv2HealthCheckedTargetGroup("out-of-service-tg", port, func(tg *ELBv2TargetGroup) {
		tg.HealthCheckInterval = 5
		tg.UnhealthyThresholdCount = 3
	})
	target := elbv2OnlyTarget(tg)

	now := time.Now()
	elbv2CheckTargetHealth(context.Background(), now)
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)

	serving.Store(false)
	interval := time.Duration(tg.HealthCheckInterval) * time.Second
	for failure := 1; failure < tg.UnhealthyThresholdCount; failure++ {
		now = now.Add(interval)
		elbv2CheckTargetHealth(context.Background(), now)
		require.Equalf(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State,
			"the target left service after %d of %d consecutive failed checks",
			failure, tg.UnhealthyThresholdCount)
	}

	now = now.Add(interval)
	elbv2CheckTargetHealth(context.Background(), now)
	health := elbv2TargetHealthFor(tg, target)
	require.Equal(t, elbv2TargetStateUnhealthy, health.State)
	require.Equal(t, elbv2ReasonResponseCodeMismatch, health.Reason)
	require.Equal(t, "Health checks failed with these codes: [503]", health.Description)
	require.False(t, elbv2TargetReceivesTraffic(tg, target))

	// "When the health checks exceed HealthyThresholdCount consecutive
	// successes, the load balancer puts the target back in service" — one
	// success is not enough for a target that is out of service.
	serving.Store(true)
	for success := 1; success < tg.HealthyThresholdCount; success++ {
		now = now.Add(interval)
		elbv2CheckTargetHealth(context.Background(), now)
		require.Equalf(t, elbv2TargetStateUnhealthy, elbv2TargetHealthFor(tg, target).State,
			"the target returned to service after %d of %d consecutive successful checks",
			success, tg.HealthyThresholdCount)
	}
	now = now.Add(interval)
	elbv2CheckTargetHealth(context.Background(), now)
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)
}

// TestUnansweredHealthCheckReportsTimeout separates the two documented
// connection-failure reason codes: a target that accepts the connection and
// never answers times the check out, and one that refuses the connection fails
// it.
func TestUnansweredHealthCheckReportsTimeout(t *testing.T) {
	elbv2TargetHealthTestStores()
	hanging := putELBv2HealthCheckedTargetGroup("timeout-tg", hangingHealthCheckTarget(t),
		func(tg *ELBv2TargetGroup) {
			tg.HealthCheckTimeout = 2
			tg.UnhealthyThresholdCount = 2
			tg.HealthCheckInterval = 5
		})
	refused := putELBv2HealthCheckedTargetGroup("refused-tg", closedHealthCheckTarget(t),
		func(tg *ELBv2TargetGroup) {
			tg.HealthCheckTimeout = 2
			tg.UnhealthyThresholdCount = 2
			tg.HealthCheckInterval = 5
		})

	now := time.Now()
	for check := 0; check < 2; check++ {
		elbv2CheckTargetHealth(context.Background(), now)
		now = now.Add(5 * time.Second)
	}

	timedOut := elbv2TargetHealthFor(hanging, elbv2OnlyTarget(hanging))
	require.Equal(t, elbv2TargetStateUnhealthy, timedOut.State)
	require.Equal(t, elbv2ReasonTimeout, timedOut.Reason)
	require.Equal(t, elbv2DescriptionTimeout, timedOut.Description)

	connectionRefused := elbv2TargetHealthFor(refused, elbv2OnlyTarget(refused))
	require.Equal(t, elbv2TargetStateUnhealthy, connectionRefused.State)
	require.Equal(t, elbv2ReasonFailedHealthChecks, connectionRefused.Reason)
	require.Equal(t, elbv2DescriptionFailedHealthChecks, connectionRefused.Description)
}

// TestTargetHealthIsCheckedOnTheConfiguredInterval proves the checker honours
// HealthCheckIntervalSeconds rather than checking whenever it is asked to
// sweep. A target that stops answering keeps its state until its next check
// comes due.
func TestTargetHealthIsCheckedOnTheConfiguredInterval(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, serving := switchableHealthCheckTarget(t)
	tg := putELBv2HealthCheckedTargetGroup("interval-tg", port, func(tg *ELBv2TargetGroup) {
		tg.HealthCheckInterval = 60
		tg.UnhealthyThresholdCount = 2
	})
	target := elbv2OnlyTarget(tg)

	now := time.Now()
	elbv2CheckTargetHealth(context.Background(), now)
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)

	serving.Store(false)
	for _, elapsed := range []time.Duration{time.Second, 30 * time.Second, 59 * time.Second} {
		elbv2CheckTargetHealth(context.Background(), now.Add(elapsed))
	}
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State,
		"the checker ran a check before the configured interval elapsed")

	elbv2CheckTargetHealth(context.Background(), now.Add(60*time.Second))
	elbv2CheckTargetHealth(context.Background(), now.Add(120*time.Second))
	require.Equal(t, elbv2TargetStateUnhealthy, elbv2TargetHealthFor(tg, target).State)
}

// TestHealthChecksDisabledReportsUnavailable covers the target group whose
// health checks are turned off: "unavailable — Health checks are disabled for
// the target group", reason `Target.HealthCheckDisabled`. Such a target group
// has no checker, so every registered target receives traffic.
func TestHealthChecksDisabledReportsUnavailable(t *testing.T) {
	elbv2TargetHealthTestStores()
	tg := putELBv2HealthCheckedTargetGroup("disabled-tg", closedHealthCheckTarget(t),
		func(tg *ELBv2TargetGroup) { tg.HealthCheckEnabled = false })
	target := elbv2OnlyTarget(tg)

	elbv2CheckTargetHealth(context.Background(), time.Now())

	health := elbv2TargetHealthFor(tg, target)
	require.Equal(t, elbv2TargetStateUnavailable, health.State)
	require.Equal(t, elbv2ReasonHealthCheckDisabled, health.Reason)
	require.Equal(t, elbv2DescriptionHealthCheckDisabled, health.Description)
	require.True(t, elbv2TargetReceivesTraffic(tg, target))
}

// TestDeregisteredTargetHealthIsForgotten proves a target that is registered
// again is checked from scratch instead of inheriting the verdict its address
// carried the last time it was in the group.
func TestDeregisteredTargetHealthIsForgotten(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := switchableHealthCheckTarget(t)
	tg := putELBv2HealthCheckedTargetGroup("rereg-tg", port, nil)
	target := elbv2OnlyTarget(tg)

	elbv2CheckTargetHealth(context.Background(), time.Now())
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)

	elbv2TargetGroups.Update(tg.Arn, func(group *ELBv2TargetGroup) { group.Targets = nil })
	elbv2CheckTargetHealth(context.Background(), time.Now())

	elbv2TargetGroups.Update(tg.Arn, func(group *ELBv2TargetGroup) {
		group.Targets = []ELBv2TargetDescription{target}
	})
	require.Equal(t, elbv2TargetStateInitial, elbv2TargetHealthFor(tg, target).State,
		"a re-registered target kept the health recorded for the address before it left")
}

// countingHealthCheckTarget serves the health check with a fixed status and
// counts the checks it received, so a test can prove a target was never
// checked rather than only that its reported state did not change.
func countingHealthCheckTarget(t *testing.T, status int) (port int, checks *atomic.Int64) {
	t.Helper()
	checks = &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		checks.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	port, err = strconv.Atoi(portText)
	require.NoError(t, err)
	return port, checks
}

// TestTargetGroupOutsideEveryListenerRuleReportsUnused holds the simulator to
// the precondition Elastic Load Balancing states for health checking: "Before
// the load balancer sends a health check request to a target, you must register
// it with a target group, specify its target group in a listener rule, and
// ensure that the Availability Zone of the target is enabled for the load
// balancer", and "Health checks are performed on all targets registered to a
// target group that is specified in a listener rule for your load balancer."
// A target group outside every listener rule is therefore never checked, and
// its targets report `unused` with `Target.NotInUse`.
func TestTargetGroupOutsideEveryListenerRuleReportsUnused(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, checks := countingHealthCheckTarget(t, http.StatusOK)
	tg := putELBv2UnusedTargetGroup("unused-tg", port, nil)
	target := elbv2OnlyTarget(tg)

	now := time.Now()
	elbv2SweepTargets(context.Background(), now)
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()

	health := elbv2TargetHealthFor(tg, target)
	require.Equal(t, elbv2TargetStateUnused, health.State)
	require.Equal(t, elbv2ReasonNotInUse, health.Reason)
	require.Equal(t, "Target group is not configured to receive traffic from the load balancer",
		health.Description)
	require.Zerof(t, checks.Load(),
		"the checker sent %d health check(s) to a target group no listener rule forwards to",
		checks.Load())
	require.False(t, elbv2TargetReceivesTraffic(tg, target))

	// Naming the target group in a listener rule is what puts it under the
	// checker, and its targets then start from a registration.
	putELBv2ListenerForTargetGroup(tg)
	require.Equal(t, elbv2TargetStateInitial, elbv2TargetHealthFor(tg, target).State)
	elbv2SweepTargets(context.Background(), now.Add(time.Second))
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)
	require.Positive(t, checks.Load())
}

// TestUnusedTargetGroupIsReportedThroughDescribeTargetHealth reads the same
// condition back through the wire shape a client sees.
func TestUnusedTargetGroupIsReportedThroughDescribeTargetHealth(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := countingHealthCheckTarget(t, http.StatusOK)
	tg := putELBv2UnusedTargetGroup("unused-xml-tg", port, nil)

	recorder := httptest.NewRecorder()
	handleELBv2DescribeTargetHealth(recorder, elbv2DescribeTargetHealthRequest(tg.Arn))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := recorder.Body.String()
	require.Contains(t, body, "<State>unused</State>")
	require.Contains(t, body, "<Reason>Target.NotInUse</Reason>")
	require.Contains(t, body,
		"<Description>Target group is not configured to receive traffic from the load balancer</Description>")
}

// TestHealthCheckMatcherGradesTheResponseCode holds the simulator to the
// Matcher setting: "The codes to use when checking for a successful response
// from a target ... You can specify multiple values (for example, "200,202") or
// a range of values (for example, "200-299"). The default value is 200." A
// target that answers outside those codes is unhealthy with
// `Target.ResponseCodeMismatch` — "The health checks did not return an expected
// HTTP code" — and the description names what it answered.
func TestHealthCheckMatcherGradesTheResponseCode(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, checks := countingHealthCheckTarget(t, http.StatusNotFound)

	defaulted := putELBv2HealthCheckedTargetGroup("matcher-default-tg", port,
		func(tg *ELBv2TargetGroup) {
			tg.MatcherHttpCode = elbv2DefaultMatcher()
			tg.HealthCheckInterval = 5
			tg.UnhealthyThresholdCount = 2
		})
	listed := putELBv2HealthCheckedTargetGroup("matcher-listed-tg", port,
		func(tg *ELBv2TargetGroup) {
			tg.MatcherHttpCode = "200,404"
			tg.HealthCheckInterval = 5
		})
	ranged := putELBv2HealthCheckedTargetGroup("matcher-range-tg", port,
		func(tg *ELBv2TargetGroup) {
			tg.MatcherHttpCode = "400-499"
			tg.HealthCheckInterval = 5
		})

	now := time.Now()
	for check := 0; check < 2; check++ {
		elbv2SweepTargets(context.Background(), now)
		now = now.Add(5 * time.Second)
	}
	require.Positive(t, checks.Load())

	mismatched := elbv2TargetHealthFor(defaulted, elbv2OnlyTarget(defaulted))
	require.Equal(t, elbv2TargetStateUnhealthy, mismatched.State)
	require.Equal(t, elbv2ReasonResponseCodeMismatch, mismatched.Reason)
	require.Equal(t, "Health checks failed with these codes: [404]", mismatched.Description)
	require.False(t, elbv2TargetReceivesTraffic(defaulted, elbv2OnlyTarget(defaulted)))

	// The same answer is a success for a target group whose Matcher names the
	// code, whether it names it outright or covers it with a range.
	for _, tg := range []ELBv2TargetGroup{listed, ranged} {
		health := elbv2TargetHealthFor(tg, elbv2OnlyTarget(tg))
		require.Equalf(t, elbv2TargetStateHealthy, health.State,
			"target group with Matcher %q rejected the code it names", tg.MatcherHttpCode)
		require.Empty(t, health.Reason)
	}
}

// TestHTTPSHealthCheckGradesTheResponseCode covers the same Matcher over an
// HTTPS health check. The load balancer "establishes TLS connections with the
// targets using certificates that you install on the targets. The load balancer
// does not validate these certificates", so a self-signed target is reachable
// and its response code is what decides the verdict.
func TestHTTPSHealthCheckGradesTheResponseCode(t *testing.T) {
	elbv2TargetHealthTestStores()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	tg := putELBv2HealthCheckedTargetGroup("matcher-https-tg", port,
		func(tg *ELBv2TargetGroup) {
			tg.Protocol = "HTTPS"
			tg.HealthCheckProtocol = "HTTPS"
			tg.HealthCheckInterval = 5
			tg.UnhealthyThresholdCount = 2
		})

	now := time.Now()
	for check := 0; check < 2; check++ {
		elbv2SweepTargets(context.Background(), now)
		now = now.Add(5 * time.Second)
	}

	health := elbv2TargetHealthFor(tg, elbv2OnlyTarget(tg))
	require.Equal(t, elbv2TargetStateUnhealthy, health.State)
	require.Equal(t, elbv2ReasonResponseCodeMismatch, health.Reason)
	require.Equal(t, "Health checks failed with these codes: [404]", health.Description)
}

// elbv2DeregisterTargetsRequest is the DeregisterTargets call for one target.
func elbv2DeregisterTargetsRequest(targetGroupArn string, target ELBv2TargetDescription) *http.Request {
	form := "Action=DeregisterTargets&Version=" + elbv2APIVersion +
		"&TargetGroupArn=" + targetGroupArn +
		"&Targets.member.1.Id=" + target.ID +
		"&Targets.member.1.Port=" + strconv.Itoa(target.Port)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// TestDeregisteringTargetDrainsForTheDeregistrationDelay holds the simulator to
// the deregistration delay: "Elastic Load Balancing stops sending requests to
// targets that are deregistering. By default, Elastic Load Balancing waits 300
// seconds before completing the deregistration process, which can help
// in-flight requests to the target to complete ... The initial state of a
// deregistering target is `draining`. After the deregistration delay elapses,
// the deregistration process completes."
func TestDeregisteringTargetDrainsForTheDeregistrationDelay(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, checks := countingHealthCheckTarget(t, http.StatusOK)
	const delaySeconds = 60
	tg := putELBv2HealthCheckedTargetGroup("draining-tg", port, func(tg *ELBv2TargetGroup) {
		tg.Attributes[elbv2DeregistrationDelayAttribute] = strconv.Itoa(delaySeconds)
	})
	target := elbv2OnlyTarget(tg)

	now := time.Now()
	elbv2SweepTargets(context.Background(), now)
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)
	checked := checks.Load()

	recorder := httptest.NewRecorder()
	handleELBv2DeregisterTargets(recorder, elbv2DeregisterTargetsRequest(tg.Arn, target))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	// The target group still holds the target while the delay runs, and reports
	// it draining rather than healthy.
	drained, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, drained.Targets, 1)
	health := elbv2TargetHealthFor(drained, drained.Targets[0])
	require.Equal(t, elbv2TargetStateDraining, health.State)
	require.Equal(t, elbv2ReasonDeregistrationInProgress, health.Reason)
	require.Equal(t, "Target deregistration is in progress", health.Description)
	require.False(t, elbv2TargetReceivesTraffic(drained, drained.Targets[0]),
		"a load balancer stops routing requests to a target as soon as it is deregistered")

	describe := httptest.NewRecorder()
	handleELBv2DescribeTargetHealth(describe, elbv2DescribeTargetHealthRequest(tg.Arn))
	require.Contains(t, describe.Body.String(), "<State>draining</State>")
	require.Contains(t, describe.Body.String(), "<Reason>Target.DeregistrationInProgress</Reason>")

	// A draining target is not health-checked, and the delay has to run out
	// before the deregistration completes. The delay runs from when the target
	// was deregistered, which is what the target group recorded.
	deregisteredAt := drained.Targets[0].DeregisteringAt
	require.False(t, deregisteredAt.IsZero())
	elbv2SweepTargets(context.Background(), deregisteredAt.Add((delaySeconds-1)*time.Second))
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	require.Equal(t, checked, checks.Load(),
		"the checker health-checked a target that is deregistering")
	stillDraining, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, stillDraining.Targets, 1,
		"the target left the target group before its deregistration delay elapsed")

	elbv2SweepTargets(context.Background(), deregisteredAt.Add(delaySeconds*time.Second))
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	deregistered, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Empty(t, deregistered.Targets,
		"the target stayed in the target group after its deregistration delay elapsed")
}

// TestZeroDeregistrationDelayCompletesAtOnce covers the bottom of the
// documented 0–3600 second range: a target group that waits zero seconds has
// nothing to drain, so the call that deregisters the target is the call that
// removes it.
func TestZeroDeregistrationDelayCompletesAtOnce(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := countingHealthCheckTarget(t, http.StatusOK)
	tg := putELBv2HealthCheckedTargetGroup("no-drain-tg", port, func(tg *ELBv2TargetGroup) {
		tg.Attributes[elbv2DeregistrationDelayAttribute] = "0"
	})
	target := elbv2OnlyTarget(tg)

	recorder := httptest.NewRecorder()
	handleELBv2DeregisterTargets(recorder, elbv2DeregisterTargetsRequest(tg.Arn, target))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	deregistered, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Empty(t, deregistered.Targets)
}

// TestReRegisteringADrainingTargetReturnsItToService covers "You can register
// the target with the target group again when you are ready for it to resume
// receiving requests": registering a draining target again cancels the
// deregistration, and the target is checked from scratch.
func TestReRegisteringADrainingTargetReturnsItToService(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := countingHealthCheckTarget(t, http.StatusOK)
	tg := putELBv2HealthCheckedTargetGroup("re-register-tg", port, func(tg *ELBv2TargetGroup) {
		tg.Attributes[elbv2DeregistrationDelayAttribute] = "300"
	})
	target := elbv2OnlyTarget(tg)

	now := time.Now()
	elbv2SweepTargets(context.Background(), now)
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(tg, target).State)

	deregister := httptest.NewRecorder()
	handleELBv2DeregisterTargets(deregister, elbv2DeregisterTargetsRequest(tg.Arn, target))
	require.Equal(t, http.StatusOK, deregister.Code, deregister.Body.String())

	form := "Action=RegisterTargets&Version=" + elbv2APIVersion +
		"&TargetGroupArn=" + tg.Arn +
		"&Targets.member.1.Id=" + target.ID +
		"&Targets.member.1.Port=" + strconv.Itoa(target.Port)
	register := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	register.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	registered := httptest.NewRecorder()
	handleELBv2RegisterTargets(registered, register)
	require.Equal(t, http.StatusOK, registered.Code, registered.Body.String())

	returned, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, returned.Targets, 1)
	require.Equal(t, elbv2TargetStateInitial, elbv2TargetHealthFor(returned, returned.Targets[0]).State)

	elbv2SweepTargets(context.Background(), now.Add(time.Minute))
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	inService, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, inService.Targets, 1)
	require.Equal(t, elbv2TargetStateHealthy, elbv2TargetHealthFor(inService, inService.Targets[0]).State)
}

// TestServiceScaleInDrainsItsTargetRatherThanDroppingIt holds the Amazon ECS
// service reconciler to the same deregistration Elastic Load Balancing performs
// however the removal was requested. A service whose task stops deregisters
// that task's target, and "the initial state of a deregistering target is
// draining" — the target group keeps it, and stops routing to it, until the
// deregistration delay elapses. A reconciler that removes the target outright
// makes a scaled-in task disappear in a way a real one does not.
func TestServiceScaleInDrainsItsTargetRatherThanDroppingIt(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := countingHealthCheckTarget(t, http.StatusOK)
	const delaySeconds = 60
	tg := putELBv2HealthCheckedTargetGroup("scale-in-tg", port, func(tg *ELBv2TargetGroup) {
		tg.Attributes[elbv2DeregistrationDelayAttribute] = strconv.Itoa(delaySeconds)
		// The reconciler registers the service's tasks; nothing is registered
		// before it runs.
		tg.Targets = nil
	})

	service := elbv2ServiceBoundToTargetGroup(t, tg)
	task := elbv2ServiceTaskAtAddress(service, "127.0.0.1", ECSTaskStatusRunning)
	ecsTasks.Put(task.TaskID(), task)

	ecsSyncServiceLoadBalancerTargets(service)
	registered, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, registered.Targets, 1,
		"the reconciler did not register the running task's target")
	require.True(t, registered.Targets[0].DeregisteringAt.IsZero())

	elbv2SweepTargets(context.Background(), time.Now())
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	require.Equal(t, elbv2TargetStateHealthy,
		elbv2TargetHealthFor(registered, registered.Targets[0]).State)

	// The task stops, as it does when the service scales in.
	task.LastStatus = ECSTaskStatusStopped
	ecsTasks.Put(task.TaskID(), task)
	ecsSyncServiceLoadBalancerTargets(service)

	drained, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, drained.Targets, 1,
		"the reconciler removed the target instead of deregistering it, so it never drained")
	health := elbv2TargetHealthFor(drained, drained.Targets[0])
	require.Equal(t, elbv2TargetStateDraining, health.State)
	require.Equal(t, elbv2ReasonDeregistrationInProgress, health.Reason)
	require.False(t, elbv2TargetReceivesTraffic(drained, drained.Targets[0]),
		"the load balancer kept routing to a target the service deregistered")

	deregisteredAt := drained.Targets[0].DeregisteringAt
	require.False(t, deregisteredAt.IsZero())
	elbv2SweepTargets(context.Background(), deregisteredAt.Add((delaySeconds-1)*time.Second))
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	stillDraining, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, stillDraining.Targets, 1,
		"the target left the target group before its deregistration delay elapsed")

	elbv2SweepTargets(context.Background(), deregisteredAt.Add(delaySeconds*time.Second))
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	gone, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Empty(t, gone.Targets,
		"the target stayed in the target group after its deregistration delay elapsed")
}

// TestServiceTaskRunningAgainCancelsItsDrain covers the other half of the
// reconciler's deregistration: a task running again at an address that is still
// draining returns that target to service, as registering it again does.
func TestServiceTaskRunningAgainCancelsItsDrain(t *testing.T) {
	elbv2TargetHealthTestStores()
	port, _ := countingHealthCheckTarget(t, http.StatusOK)
	tg := putELBv2HealthCheckedTargetGroup("scale-out-tg", port, func(tg *ELBv2TargetGroup) {
		tg.Attributes[elbv2DeregistrationDelayAttribute] = "300"
		tg.Targets = nil
	})

	service := elbv2ServiceBoundToTargetGroup(t, tg)
	task := elbv2ServiceTaskAtAddress(service, "127.0.0.1", ECSTaskStatusRunning)
	ecsTasks.Put(task.TaskID(), task)
	ecsSyncServiceLoadBalancerTargets(service)

	task.LastStatus = ECSTaskStatusStopped
	ecsTasks.Put(task.TaskID(), task)
	ecsSyncServiceLoadBalancerTargets(service)
	draining, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, draining.Targets, 1)
	require.False(t, draining.Targets[0].DeregisteringAt.IsZero())

	task.LastStatus = ECSTaskStatusRunning
	ecsTasks.Put(task.TaskID(), task)
	ecsSyncServiceLoadBalancerTargets(service)

	returned, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Len(t, returned.Targets, 1)
	require.True(t, returned.Targets[0].DeregisteringAt.IsZero(),
		"the target stayed in deregistration after its task was running again")

	elbv2SweepTargets(context.Background(), time.Now())
	// A sweep can request a service reconciliation; let it finish before the
	// test reads or replaces what it touches.
	AwaitSimulatorBackground()
	inService, ok := elbv2TargetGroups.Get(tg.Arn)
	require.True(t, ok)
	require.Equal(t, elbv2TargetStateHealthy,
		elbv2TargetHealthFor(inService, inService.Targets[0]).State)
}

// elbv2ServiceBoundToTargetGroup stores a service whose load-balancer binding
// forwards the target group's port to a named container, which is what the
// reconciler reads to derive its targets.
func elbv2ServiceBoundToTargetGroup(t *testing.T, tg ELBv2TargetGroup) ECSService {
	t.Helper()
	bindings, err := json.Marshal([]map[string]any{{
		"targetGroupArn": tg.Arn,
		"containerName":  "web",
		"containerPort":  tg.Port,
	}})
	require.NoError(t, err)
	service := ECSService{
		ServiceArn:    ecsArn("service", "target-health/"+tg.Name+"-svc"),
		ServiceName:   tg.Name + "-svc",
		ClusterArn:    ecsArn("cluster", "target-health"),
		LoadBalancers: bindings,
	}
	ecsServices.Put(service.ServiceArn, service)
	return service
}

// elbv2ServiceTaskAtAddress builds a task in the service's group whose `web`
// container holds the given address, which is the target an `ip` target group
// registers.
func elbv2ServiceTaskAtAddress(service ECSService, address string, status ECSTaskStatus) ECSTask {
	return ECSTask{
		TaskArn:       ecsArn("task", "target-health/"+address),
		ClusterArn:    service.ClusterArn,
		Group:         ecsServiceTaskGroup(service.ServiceName),
		LastStatus:    status,
		DesiredStatus: ECSTaskStatusRunning,
		Containers: []ECSTaskContainer{{
			Name:              "web",
			NetworkInterfaces: []ECSNetworkInterface{{PrivateIpv4Address: address}},
		}},
	}
}
