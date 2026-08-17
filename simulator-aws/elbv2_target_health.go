package main

import (
	"context"
	"errors"
	"fmt"
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
//
// Two states sit outside that machine because the checker never reaches them.
// A target group no listener rule forwards to is not checked at all — "Health
// checks are performed on all targets registered to a target group that is
// specified in a listener rule for your load balancer", and "Before the load
// balancer sends a health check request to a target, you must register it with
// a target group, specify its target group in a listener rule, and ensure that
// the Availability Zone of the target is enabled for the load balancer" — so
// its targets are `unused` with `Target.NotInUse`. A target being deregistered
// is `draining` with `Target.DeregistrationInProgress` for as long as the
// target group's deregistration delay runs: "The initial state of a
// deregistering target is draining. After the deregistration delay elapses,
// the deregistration process completes and the state of the target is unused."

const (
	elbv2TargetStateInitial     = "initial"
	elbv2TargetStateHealthy     = "healthy"
	elbv2TargetStateUnhealthy   = "unhealthy"
	elbv2TargetStateUnavailable = "unavailable"
	elbv2TargetStateUnused      = "unused"
	elbv2TargetStateDraining    = "draining"

	elbv2ReasonRegistrationInProgress   = "Elb.RegistrationInProgress"
	elbv2ReasonInitialHealthChecking    = "Elb.InitialHealthChecking"
	elbv2ReasonFailedHealthChecks       = "Target.FailedHealthChecks"
	elbv2ReasonTimeout                  = "Target.Timeout"
	elbv2ReasonResponseCodeMismatch     = "Target.ResponseCodeMismatch"
	elbv2ReasonHealthCheckDisabled      = "Target.HealthCheckDisabled"
	elbv2ReasonNotInUse                 = "Target.NotInUse"
	elbv2ReasonDeregistrationInProgress = "Target.DeregistrationInProgress"

	// The descriptions Elastic Load Balancing publishes for each reason code.
	elbv2DescriptionRegistrationInProgress   = "Target registration is in progress"
	elbv2DescriptionInitialHealthChecking    = "Initial health checks in progress"
	elbv2DescriptionFailedHealthChecks       = "Health checks failed"
	elbv2DescriptionTimeout                  = "Request timed out"
	elbv2DescriptionHealthCheckDisabled      = "Health checks are disabled"
	elbv2DescriptionNotInUse                 = "Target group is not configured to receive traffic from the load balancer"
	elbv2DescriptionDeregistrationInProgress = "Target deregistration is in progress"

	// Target.ResponseCodeMismatch is the one description that names what the
	// target answered: "Health checks failed with these codes: [code]".
	elbv2DescriptionResponseCodeMismatchFormat = "Health checks failed with these codes: [%d]"
)

// Defaults for target groups whose health-check settings were never set:
// HealthCheckIntervalSeconds defaults to 30 seconds and HealthyThresholdCount
// to 5, UnhealthyThresholdCount to 2.
const (
	elbv2DefaultHealthCheckInterval     = 30 * time.Second
	elbv2DefaultHealthyThresholdCount   = 5
	elbv2DefaultUnhealthyThresholdCount = 2

	// "deregistration_delay.timeout_seconds — The amount of time for Elastic
	// Load Balancing to wait before deregistering a target. The range is 0–3600
	// seconds. The default value is 300 seconds."
	elbv2DeregistrationDelayAttribute = "deregistration_delay.timeout_seconds"
	elbv2DefaultDeregistrationDelay   = 300 * time.Second
	elbv2MaximumDeregistrationDelay   = 3600 * time.Second

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

// elbv2DeregistrationDelay is how long the target group holds a target after
// it is deregistered, which is how long the target reports `draining`.
func elbv2DeregistrationDelay(tg ELBv2TargetGroup) time.Duration {
	seconds, err := strconv.Atoi(tg.Attributes[elbv2DeregistrationDelayAttribute])
	if err != nil || seconds < 0 {
		return elbv2DefaultDeregistrationDelay
	}
	delay := time.Duration(seconds) * time.Second
	if delay > elbv2MaximumDeregistrationDelay {
		return elbv2MaximumDeregistrationDelay
	}
	return delay
}

// elbv2TargetGroupInUse reports whether a listener forwards to the target
// group, which is the condition Elastic Load Balancing health-checks it under:
// "Health checks are performed on all targets registered to a target group
// that is specified in a listener rule for your load balancer." A listener's
// default actions are its default rule, so they count alongside the rules
// created against it.
func elbv2TargetGroupInUse(targetGroupArn string) bool {
	for _, listener := range elbv2Listeners.List() {
		if elbv2ActionsForwardTo(listener.DefaultActions, targetGroupArn) {
			return true
		}
	}
	for _, rule := range elbv2Rules.List() {
		if elbv2ActionsForwardTo(rule.Actions, targetGroupArn) {
			return true
		}
	}
	return false
}

// elbv2ActionsForwardTo reports whether any action forwards to the target
// group, through either the single-target-group shorthand or the weighted
// forward config.
func elbv2ActionsForwardTo(actions []ELBv2Action, targetGroupArn string) bool {
	for _, action := range actions {
		if action.TargetGroupArn == targetGroupArn {
			return true
		}
		if action.Forward == nil {
			continue
		}
		for _, tuple := range action.Forward.TargetGroups {
			if tuple.TargetGroupArn == targetGroupArn {
				return true
			}
		}
	}
	return false
}

// elbv2TargetHealthFor reports the health the checker last recorded for a
// target, or the state that keeps the checker away from it. A target being
// deregistered is `draining` until its target group's deregistration delay
// elapses; a target group no listener forwards to is not checked at all and
// reports `unused` with `Target.NotInUse`; a target group with health checks
// turned off reports every target `unavailable` with
// `Target.HealthCheckDisabled`; and a target the checker has not yet issued a
// first check for is `initial` with `Elb.RegistrationInProgress`.
func elbv2TargetHealthFor(tg ELBv2TargetGroup, target ELBv2TargetDescription) ELBv2TargetHealth {
	if !target.DeregisteringAt.IsZero() {
		return ELBv2TargetHealth{
			State:       elbv2TargetStateDraining,
			Reason:      elbv2ReasonDeregistrationInProgress,
			Description: elbv2DescriptionDeregistrationInProgress,
		}
	}
	if !elbv2TargetGroupInUse(tg.Arn) {
		return ELBv2TargetHealth{
			State:       elbv2TargetStateUnused,
			Reason:      elbv2ReasonNotInUse,
			Description: elbv2DescriptionNotInUse,
		}
	}
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
				elbv2SweepTargets(ctx, time.Now())
			}
		}
	})
}

// elbv2SweepTargets is one turn of the target loop: deregistrations whose
// delay has run complete, and every target still registered that is due a
// health check gets one.
func elbv2SweepTargets(ctx context.Context, now time.Time) {
	elbv2CompleteDueDeregistrations(now)
	elbv2CheckTargetHealth(ctx, now)
}

// elbv2CompleteDueDeregistrations drops the targets whose deregistration delay
// has elapsed: "After the deregistration delay elapses, the deregistration
// process completes."
func elbv2CompleteDueDeregistrations(now time.Time) {
	for _, tg := range elbv2TargetGroups.List() {
		draining := false
		for _, target := range tg.Targets {
			if !target.DeregisteringAt.IsZero() {
				draining = true
				break
			}
		}
		if !draining {
			continue
		}
		delay := elbv2DeregistrationDelay(tg)
		elbv2TargetGroups.Update(tg.Arn, func(group *ELBv2TargetGroup) {
			remaining := make([]ELBv2TargetDescription, 0, len(group.Targets))
			for _, target := range group.Targets {
				if !target.DeregisteringAt.IsZero() &&
					!now.Before(target.DeregisteringAt.Add(delay)) {
					continue
				}
				remaining = append(remaining, target)
			}
			group.Targets = remaining
		})
	}
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
		// Turning health checks off leaves no checker behind, and neither does
		// a target group no listener forwards to, so the verdicts either had
		// reached are forgotten: putting the target group back in a listener
		// rule starts its targets from a registration again.
		if !tg.HealthCheckEnabled || !elbv2TargetGroupInUse(tg.Arn) {
			continue
		}
		for _, target := range tg.Targets {
			if !target.DeregisteringAt.IsZero() {
				// A deregistering target reports `draining` for the whole
				// delay, so no check result could change what it reports.
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
// description Elastic Load Balancing publishes for it. A target that answered
// with a code outside the target group's Matcher is a response code mismatch —
// "Target.ResponseCodeMismatch - The health checks did not return an expected
// HTTP code" — and its description names the code the target returned.
func elbv2HealthCheckFailureReason(err error) (reason, description string) {
	var mismatch elbv2ResponseCodeMismatch
	if errors.As(err, &mismatch) {
		return elbv2ReasonResponseCodeMismatch,
			fmt.Sprintf(elbv2DescriptionResponseCodeMismatchFormat, mismatch.StatusCode)
	}
	var netError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netError) && netError.Timeout()) {
		return elbv2ReasonTimeout, elbv2DescriptionTimeout
	}
	return elbv2ReasonFailedHealthChecks, elbv2DescriptionFailedHealthChecks
}

// elbv2ForgetTargetHealth drops the record of one target, so a target the
// checker has stopped maintaining a verdict for does not answer with a stale
// one. Registering a target again starts it from a registration: "Before a
// target can receive requests from the load balancer, it must pass the initial
// health checks."
func elbv2ForgetTargetHealth(targetGroupArn string, target ELBv2TargetDescription) {
	elbv2TargetHealthMu.Lock()
	defer elbv2TargetHealthMu.Unlock()
	delete(elbv2TargetHealthRecords, elbv2TargetHealthKey(targetGroupArn, target))
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
