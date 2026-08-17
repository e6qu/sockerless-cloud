package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
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
	elbv2LoadBalancers = sim.MakeStore[ELBv2LoadBalancer](nil, "elbv2_load_balancers")
	elbv2TargetGroups = sim.MakeStore[ELBv2TargetGroup](nil, "elbv2_target_groups")
	elbv2Listeners = sim.MakeStore[ELBv2Listener](nil, "elbv2_listeners")
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

// putELBv2HealthCheckedTargetGroup registers one target group holding one
// target on 127.0.0.1.
func putELBv2HealthCheckedTargetGroup(name string, port int, tune func(*ELBv2TargetGroup)) ELBv2TargetGroup {
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
	}
	if tune != nil {
		tune(&tg)
	}
	elbv2TargetGroups.Put(tg.Arn, tg)
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
// enough, and the reason code names how the check failed.
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
	require.Equal(t, elbv2ReasonFailedHealthChecks, health.Reason)
	require.Equal(t, elbv2DescriptionFailedHealthChecks, health.Description)
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
