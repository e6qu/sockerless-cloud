package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
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

// ELBv2TargetHealth is one target's health, as DescribeTargetHealth reports
// it.
type ELBv2TargetHealth struct {
	State       string
	Reason      string
	Description string
}

// elbv2TargetHealthTracker holds what the checker last recorded for every
// target it checks, keyed by elbv2TargetHealthKey.
var elbv2TargetHealthTracker = lbplane.NewHealthTracker[string]()

func elbv2TargetHealthKey(targetGroupArn string, target ELBv2TargetDescription) string {
	return targetGroupArn + "|" + target.ID + ":" + strconv.Itoa(target.Port)
}

// elbv2HealthPolicy is the target group's health check schedule. A target that
// has never been in service enters it on its first successful check; one taken
// out of service returns only after HealthyThresholdCount consecutive
// successes.
func elbv2HealthPolicy(tg ELBv2TargetGroup) lbplane.HealthPolicy {
	return lbplane.HealthPolicy{
		Interval:                elbv2HealthCheckInterval(tg),
		InitialHealthyThreshold: 1,
		HealthyThreshold:        elbv2HealthyThreshold(tg),
		UnhealthyThreshold:      elbv2UnhealthyThreshold(tg),
	}
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
	// A target group is reached through the actions that forward to it, so
	// both stores are indexed by the groups their actions name — the same
	// question, asked of a key instead of of every row, for a health check
	// that runs on a timer per target group.
	if _, ok := elbv2ListenersByForwardTarget.Lookup(elbv2Listeners, targetGroupArn,
		func(l ELBv2Listener) []string { return elbv2ActionForwardTargets(l.DefaultActions) }); ok {
		return true
	}
	_, ok := elbv2RulesByForwardTarget.Lookup(elbv2Rules, targetGroupArn,
		func(rule ELBv2Rule) []string { return elbv2ActionForwardTargets(rule.Actions) })
	return ok
}

var (
	elbv2ListenersByForwardTarget sim.GenerationIndex[ELBv2Listener]
	elbv2RulesByForwardTarget     sim.GenerationIndex[ELBv2Rule]
)

// elbv2ActionForwardTargets returns every target group an action set forwards
// to, through either the single-target-group shorthand or the weighted forward
// configuration — the two places an action can name a target group.
func elbv2ActionForwardTargets(actions []ELBv2Action) []string {
	var targets []string
	for _, action := range actions {
		if action.TargetGroupArn != "" {
			targets = append(targets, action.TargetGroupArn)
		}
		if action.Forward == nil {
			continue
		}
		for _, tuple := range action.Forward.TargetGroups {
			if tuple.TargetGroupArn != "" {
				targets = append(targets, tuple.TargetGroupArn)
			}
		}
	}
	return targets
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
	health, _ := elbv2TargetHealthTracker.Health(elbv2TargetHealthKey(tg.Arn, target))
	switch health.State {
	case lbplane.HealthHealthy:
		return ELBv2TargetHealth{State: elbv2TargetStateHealthy}
	case lbplane.HealthUnhealthy:
		reason, description := elbv2HealthCheckFailureReason(health.LastFailure)
		return ELBv2TargetHealth{State: elbv2TargetStateUnhealthy, Reason: reason, Description: description}
	}
	if health.Failures > 0 {
		return ELBv2TargetHealth{
			State:       elbv2TargetStateInitial,
			Reason:      elbv2ReasonInitialHealthChecking,
			Description: elbv2DescriptionInitialHealthChecking,
		}
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
	srv.StartBackground("ELB target health checker", func(ctx context.Context) {
		lbplane.SweepEvery(ctx, elbv2TargetHealthSweep, elbv2SweepTargets)
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

// elbv2CheckTargetHealth is one sweep: every registered target whose next
// check has come due at now is checked, and the result folded into its
// recorded health.
func elbv2CheckTargetHealth(ctx context.Context, now time.Time) {
	var targets []lbplane.HealthTarget[string]
	groupOf := map[string]string{}
	for _, tg := range elbv2TargetGroups.List() {
		// Turning health checks off leaves no checker behind, and neither does
		// a target group no listener forwards to, so the verdicts either had
		// reached are forgotten: putting the target group back in a listener
		// rule starts its targets from a registration again.
		if !tg.HealthCheckEnabled || !elbv2TargetGroupInUse(tg.Arn) {
			continue
		}
		policy := elbv2HealthPolicy(tg)
		for _, target := range tg.Targets {
			if !target.DeregisteringAt.IsZero() {
				// A deregistering target reports `draining` for the whole
				// delay, so no check result could change what it reports.
				continue
			}
			key := elbv2TargetHealthKey(tg.Arn, target)
			groupOf[key] = tg.Arn
			group, member := tg, target
			targets = append(targets, lbplane.HealthTarget[string]{
				Key:    key,
				Policy: policy,
				Probe: func(ctx context.Context) (int, error) {
					return elbv2ProbeTarget(ctx, group, member)
				},
			})
		}
	}
	changed := elbv2TargetHealthTracker.Sweep(ctx, now, targets)

	// A target entering or leaving service is what decides whether an Amazon
	// ECS service's task is in service, and whether its scheduler has to
	// replace it. Nothing else wakes the scheduler for it: target health moves
	// without any task lifecycle transition.
	woken := map[string]bool{}
	for _, key := range changed {
		targetGroupArn := groupOf[key]
		if woken[targetGroupArn] {
			continue
		}
		woken[targetGroupArn] = true
		ecsRequestServiceReconcileForTargetGroup(targetGroupArn)
	}
}

// elbv2HealthCheckFailureReason maps a failed check to the reason code and
// description Elastic Load Balancing publishes for it. A target that answered
// with a code outside the target group's Matcher is a response code mismatch —
// "Target.ResponseCodeMismatch - The health checks did not return an expected
// HTTP code" — and its description names the code the target returned.
func elbv2HealthCheckFailureReason(err error) (reason, description string) {
	var mismatch *lbplane.StatusMismatchError
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
	elbv2TargetHealthTracker.Forget(elbv2TargetHealthKey(targetGroupArn, target))
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
