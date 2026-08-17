package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Elastic Load Balancing keeps the health of every registered target current
// by itself: "The load balancer sends a health check request to each
// registered target every HealthCheckIntervalSeconds seconds, using the
// specified port, protocol, and health check path." A describe reports what
// that continuous checker last recorded — it is not what triggers a check.
//
// The documented state machine has three edges. A freshly registered target
// enters service on its first successful check ("After your target is
// registered, it must pass one health check to be considered healthy"), a
// target in service leaves it on UnhealthyThresholdCount consecutive failures
// ("If the health checks exceed UnhealthyThresholdCount consecutive failures,
// the load balancer takes the target out of service"), and a target out of
// service returns on HealthyThresholdCount consecutive successes ("When the
// health checks exceed HealthyThresholdCount consecutive successes, the load
// balancer puts the target back in service"). Until one of those thresholds
// is reached the target is `initial`, whose documented reason codes are
// `Elb.RegistrationInProgress` before the first check has been issued and
// `Elb.InitialHealthChecking` while checks are still running.

const (
	elbv2TargetStateInitial     = "initial"
	elbv2TargetStateHealthy     = "healthy"
	elbv2TargetStateUnhealthy   = "unhealthy"
	elbv2TargetStateUnavailable = "unavailable"

	elbv2ReasonRegistrationInProgress = "Elb.RegistrationInProgress"
	elbv2ReasonInitialHealthChecking  = "Elb.InitialHealthChecking"
	elbv2ReasonFailedHealthChecks     = "Target.FailedHealthChecks"
	elbv2ReasonTimeout                = "Target.Timeout"
	elbv2ReasonHealthCheckDisabled    = "Target.HealthCheckDisabled"

	// The descriptions Elastic Load Balancing publishes for each reason code.
	elbv2DescriptionRegistrationInProgress = "Target registration is in progress"
	elbv2DescriptionInitialHealthChecking  = "Initial health checks in progress"
	elbv2DescriptionFailedHealthChecks     = "Health checks failed"
	elbv2DescriptionTimeout                = "Request timed out"
	elbv2DescriptionHealthCheckDisabled    = "Health checks are disabled"
)

// Defaults for target groups whose health-check settings were never set:
// HealthCheckIntervalSeconds defaults to 30 seconds and HealthyThresholdCount
// to 5, UnhealthyThresholdCount to 2.
const (
	elbv2DefaultHealthCheckInterval     = 30 * time.Second
	elbv2DefaultHealthyThresholdCount   = 5
	elbv2DefaultUnhealthyThresholdCount = 2

	// elbv2TargetHealthSweep is how often the checker looks for targets whose
	// next check has come due. It bounds the checker's own resolution, not the
	// interval between two checks of one target, which is the target group's
	// configured HealthCheckIntervalSeconds.
	elbv2TargetHealthSweep = 250 * time.Millisecond
)

// ELBv2TargetHealth is one target's recorded health, as DescribeTargetHealth
// reports it.
type ELBv2TargetHealth struct {
	State       string
	Reason      string
	Description string

	// successes and failures count the consecutive checks since the last
	// result of the other kind; nextCheck is when this target is next due.
	successes int
	failures  int
	nextCheck time.Time
}

var (
	elbv2TargetHealthMu      sync.Mutex
	elbv2TargetHealthRecords = map[string]*ELBv2TargetHealth{}
)

func elbv2TargetHealthKey(targetGroupArn string, target ELBv2TargetDescription) string {
	return targetGroupArn + "|" + target.ID + ":" + strconv.Itoa(target.Port)
}

func elbv2HealthCheckInterval(tg ELBv2TargetGroup) time.Duration {
	if tg.HealthCheckInterval <= 0 {
		return elbv2DefaultHealthCheckInterval
	}
	return time.Duration(tg.HealthCheckInterval) * time.Second
}

func elbv2HealthyThreshold(tg ELBv2TargetGroup) int {
	if tg.HealthyThresholdCount <= 0 {
		return elbv2DefaultHealthyThresholdCount
	}
	return tg.HealthyThresholdCount
}

func elbv2UnhealthyThreshold(tg ELBv2TargetGroup) int {
	if tg.UnhealthyThresholdCount <= 0 {
		return elbv2DefaultUnhealthyThresholdCount
	}
	return tg.UnhealthyThresholdCount
}

// elbv2TargetHealthFor reports the health the checker last recorded for a
// target. A target group with health checks turned off reports every target
// `unavailable` with `Target.HealthCheckDisabled`, and a target the checker
// has not yet issued a first check for is `initial` with
// `Elb.RegistrationInProgress`.
func elbv2TargetHealthFor(tg ELBv2TargetGroup, target ELBv2TargetDescription) ELBv2TargetHealth {
	if !tg.HealthCheckEnabled {
		return ELBv2TargetHealth{
			State:       elbv2TargetStateUnavailable,
			Reason:      elbv2ReasonHealthCheckDisabled,
			Description: elbv2DescriptionHealthCheckDisabled,
		}
	}
	elbv2TargetHealthMu.Lock()
	defer elbv2TargetHealthMu.Unlock()
	if record, ok := elbv2TargetHealthRecords[elbv2TargetHealthKey(tg.Arn, target)]; ok {
		return *record
	}
	return ELBv2TargetHealth{
		State:       elbv2TargetStateInitial,
		Reason:      elbv2ReasonRegistrationInProgress,
		Description: elbv2DescriptionRegistrationInProgress,
	}
}

// elbv2TargetReceivesTraffic reports whether a listener may forward to the
// target. A load balancer routes to the targets its health checker has put in
// service; when health checks are turned off for the target group there is no
// checker and every registered target receives traffic.
func elbv2TargetReceivesTraffic(tg ELBv2TargetGroup, target ELBv2TargetDescription) bool {
	switch elbv2TargetHealthFor(tg, target).State {
	case elbv2TargetStateHealthy, elbv2TargetStateUnavailable:
		return true
	default:
		return false
	}
}

// startELBv2TargetHealthChecker runs the health checker for the lifetime of
// the simulator.
func startELBv2TargetHealthChecker(srv *sim.Server) {
	srv.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(elbv2TargetHealthSweep)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				elbv2CheckTargetHealth(ctx, time.Now())
			}
		}
	})
}

// elbv2TargetHealthCheck is one due check the sweep issues.
type elbv2TargetHealthCheck struct {
	key    string
	group  ELBv2TargetGroup
	target ELBv2TargetDescription
}

// elbv2CheckTargetHealth is one sweep: every registered target whose next
// check has come due at now is checked, and the result folded into its
// recorded health. Checks run concurrently, so one unresponsive target costs
// the sweep its own health-check timeout rather than the sum of them.
func elbv2CheckTargetHealth(ctx context.Context, now time.Time) {
	registered := map[string]bool{}
	var due []elbv2TargetHealthCheck
	for _, tg := range elbv2TargetGroups.List() {
		for _, target := range tg.Targets {
			if !tg.HealthCheckEnabled {
				// Turning health checks off leaves no checker behind, so the
				// verdicts it had reached are forgotten and turning them back
				// on starts from a registration again.
				continue
			}
			key := elbv2TargetHealthKey(tg.Arn, target)
			registered[key] = true
			if elbv2ScheduleTargetHealthCheck(key, tg, now) {
				due = append(due, elbv2TargetHealthCheck{key: key, group: tg, target: target})
			}
		}
	}
	elbv2ForgetDeregisteredTargets(registered)

	var pending sync.WaitGroup
	changed := make(chan string, len(due))
	for _, check := range due {
		pending.Add(1)
		go func(check elbv2TargetHealthCheck) {
			defer pending.Done()
			if elbv2RecordTargetHealthCheck(check.key, check.group,
				elbv2ProbeTarget(ctx, check.group, check.target)) {
				changed <- check.group.Arn
			}
		}(check)
	}
	pending.Wait()
	close(changed)

	// A target entering or leaving service is what decides whether an Amazon
	// ECS service's task is in service, and whether its scheduler has to
	// replace it. Nothing else wakes the scheduler for it: target health moves
	// without any task lifecycle transition.
	woken := map[string]bool{}
	for targetGroupArn := range changed {
		if woken[targetGroupArn] {
			continue
		}
		woken[targetGroupArn] = true
		ecsRequestServiceReconcileForTargetGroup(targetGroupArn)
	}
}

// elbv2ScheduleTargetHealthCheck reports whether the target is due for a check
// at now, and books its next one. Each health check request is independent and
// its result lasts for the whole interval, so the next check is scheduled when
// this one is issued rather than when it answers.
func elbv2ScheduleTargetHealthCheck(key string, tg ELBv2TargetGroup, now time.Time) bool {
	elbv2TargetHealthMu.Lock()
	defer elbv2TargetHealthMu.Unlock()
	record, ok := elbv2TargetHealthRecords[key]
	if !ok {
		record = &ELBv2TargetHealth{
			State:       elbv2TargetStateInitial,
			Reason:      elbv2ReasonRegistrationInProgress,
			Description: elbv2DescriptionRegistrationInProgress,
		}
		elbv2TargetHealthRecords[key] = record
	} else if now.Before(record.nextCheck) {
		return false
	}
	record.nextCheck = now.Add(elbv2HealthCheckInterval(tg))
	return true
}

// elbv2RecordTargetHealthCheck folds one check result into a target's recorded
// health, and reports whether that moved the target into or out of service.
func elbv2RecordTargetHealthCheck(key string, tg ELBv2TargetGroup, err error) (changed bool) {
	elbv2TargetHealthMu.Lock()
	defer elbv2TargetHealthMu.Unlock()
	record, ok := elbv2TargetHealthRecords[key]
	if !ok {
		// The target was deregistered while its check was in flight.
		return false
	}
	previous := record.State
	defer func() { changed = record.State != previous }()

	if err == nil {
		record.failures = 0
		record.successes++
		// A target that has never been in service enters it on its first
		// successful check; one taken out of service returns only after
		// HealthyThresholdCount consecutive successes.
		if record.State != elbv2TargetStateUnhealthy ||
			record.successes >= elbv2HealthyThreshold(tg) {
			record.State = elbv2TargetStateHealthy
			record.Reason = ""
			record.Description = ""
		}
		return
	}
	record.successes = 0
	record.failures++
	if record.failures < elbv2UnhealthyThreshold(tg) {
		if record.State == elbv2TargetStateInitial {
			record.Reason = elbv2ReasonInitialHealthChecking
			record.Description = elbv2DescriptionInitialHealthChecking
		}
		return
	}
	record.State = elbv2TargetStateUnhealthy
	record.Reason, record.Description = elbv2HealthCheckFailureReason(err)
	return
}

// elbv2HealthCheckFailureReason maps a failed check to the reason code and
// description Elastic Load Balancing publishes for it.
func elbv2HealthCheckFailureReason(err error) (reason, description string) {
	var netError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netError) && netError.Timeout()) {
		return elbv2ReasonTimeout, elbv2DescriptionTimeout
	}
	return elbv2ReasonFailedHealthChecks, elbv2DescriptionFailedHealthChecks
}

// elbv2ForgetDeregisteredTargets drops the records of targets that are no
// longer registered, so a target registered again is checked from scratch.
func elbv2ForgetDeregisteredTargets(registered map[string]bool) {
	elbv2TargetHealthMu.Lock()
	defer elbv2TargetHealthMu.Unlock()
	for key := range elbv2TargetHealthRecords {
		if !registered[key] {
			delete(elbv2TargetHealthRecords, key)
		}
	}
}

// elbv2EffectiveHealthCheckPort is the port the checker connects to, which
// DescribeTargetHealth reports alongside the target's health. The target group
// default, "traffic-port", is the port the target receives traffic on.
func elbv2EffectiveHealthCheckPort(tg ELBv2TargetGroup, target ELBv2TargetDescription) int {
	if port, err := strconv.Atoi(tg.HealthCheckPort); err == nil && port > 0 {
		return port
	}
	if target.Port != 0 {
		return target.Port
	}
	return tg.Port
}
