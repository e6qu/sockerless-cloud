package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// "The load balancer sends a health check request to each registered target
// every HealthCheckIntervalSeconds seconds"; a describe reports what the
// checker last recorded and never triggers a check.
//
// A new target enters service on its first successful check, leaves it after
// UnhealthyThresholdCount consecutive failures, and returns after
// HealthyThresholdCount consecutive successes; until then it is `initial`,
// with `Elb.RegistrationInProgress` before the first check and
// `Elb.InitialHealthChecking` after.
//
// A target group no listener rule forwards to is never checked: its targets
// are `unused` with `Target.NotInUse`. A deregistering target is `draining`
// with `Target.DeregistrationInProgress` for the deregistration delay.

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

	// The published description per reason code.
	elbv2DescriptionRegistrationInProgress   = "Target registration is in progress"
	elbv2DescriptionInitialHealthChecking    = "Initial health checks in progress"
	elbv2DescriptionFailedHealthChecks       = "Health checks failed"
	elbv2DescriptionTimeout                  = "Request timed out"
	elbv2DescriptionHealthCheckDisabled      = "Health checks are disabled"
	elbv2DescriptionNotInUse                 = "Target group is not configured to receive traffic from the load balancer"
	elbv2DescriptionDeregistrationInProgress = "Target deregistration is in progress"

	// The only description naming what the target answered.
	elbv2DescriptionResponseCodeMismatchFormat = "Health checks failed with these codes: [%d]"
)

// Defaults: 30-second interval, HealthyThresholdCount 5,
// UnhealthyThresholdCount 2.
const (
	elbv2DefaultHealthCheckInterval     = 30 * time.Second
	elbv2DefaultHealthyThresholdCount   = 5
	elbv2DefaultUnhealthyThresholdCount = 2

	// "The range is 0–3600 seconds. The default value is 300 seconds."
	elbv2DeregistrationDelayAttribute = "deregistration_delay.timeout_seconds"
	elbv2DefaultDeregistrationDelay   = 300 * time.Second
	elbv2MaximumDeregistrationDelay   = 3600 * time.Second

	// The checker's resolution, not the per-target interval, which is
	// HealthCheckIntervalSeconds.
	elbv2TargetHealthSweep = 250 * time.Millisecond
)

type ELBv2TargetHealth struct {
	State       string
	Reason      string
	Description string
}

// Keyed by elbv2TargetHealthKey.
var elbv2TargetHealthTracker = lbplane.NewHealthTracker[string]()

func elbv2TargetHealthKey(targetGroupArn string, target ELBv2TargetDescription) string {
	return targetGroupArn + "|" + target.ID + ":" + strconv.Itoa(target.Port)
}

// A target never in service enters on one success; one taken out returns only
// after HealthyThresholdCount successes.
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

// How long a deregistered target reports `draining`.
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

// "Health checks are performed on all targets registered to a target group
// that is specified in a listener rule"; a listener's default actions are its
// default rule.
func elbv2TargetGroupInUse(targetGroupArn string) bool {
	// Index listeners and rules by the groups their actions forward to; the check
	// runs on a timer per group.
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

// The single-group shorthand and the weighted forward configuration.
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

// `draining` until the delay elapses; `unused` / `Target.NotInUse` when no
// listener forwards to the group; `unavailable` / `Target.HealthCheckDisabled`
// with checks off; `initial` / `Elb.RegistrationInProgress` before the first
// check.
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

// With health checks off there is no checker, and every registered target gets
// traffic.
func elbv2TargetReceivesTraffic(tg ELBv2TargetGroup, target ELBv2TargetDescription) bool {
	switch elbv2TargetHealthFor(tg, target).State {
	case elbv2TargetStateHealthy, elbv2TargetStateUnavailable:
		return true
	default:
		return false
	}
}

func startELBv2TargetHealthChecker(srv *sim.Server) {
	srv.StartBackground("ELB target health checker", func(ctx context.Context) {
		lbplane.SweepEvery(ctx, elbv2TargetHealthSweep, elbv2SweepTargets)
	})
}

// Completes due deregistrations, then checks every target due.
func elbv2SweepTargets(ctx context.Context, now time.Time) {
	elbv2CompleteDueDeregistrations(now)
	elbv2CheckTargetHealth(ctx, now)
}

// "After the deregistration delay elapses, the deregistration process
// completes."
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

func elbv2CheckTargetHealth(ctx context.Context, now time.Time) {
	var targets []lbplane.HealthTarget[string]
	groupOf := map[string]string{}
	for _, tg := range elbv2TargetGroups.List() {
		// Without a checker, forget the verdicts: a group put back under a listener
		// starts its targets from registration.
		if !tg.HealthCheckEnabled || !elbv2TargetGroupInUse(tg.Arn) {
			continue
		}
		policy := elbv2HealthPolicy(tg)
		for _, target := range tg.Targets {
			if !target.DeregisteringAt.IsZero() {
				// A draining target reports `draining` whatever a check says.
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

	// Target health decides whether an Amazon ECS task is in service; nothing else
	// wakes its scheduler.
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

// A code outside the Matcher is `Target.ResponseCodeMismatch`, whose
// description names the code.
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

// A re-registered target starts from registration: "it must pass the initial
// health checks."
func elbv2ForgetTargetHealth(targetGroupArn string, target ELBv2TargetDescription) {
	elbv2TargetHealthTracker.Forget(elbv2TargetHealthKey(targetGroupArn, target))
}

// The default "traffic-port" is the port the target receives traffic on.
func elbv2EffectiveHealthCheckPort(tg ELBv2TargetGroup, target ELBv2TargetDescription) int {
	if port, err := strconv.Atoi(tg.HealthCheckPort); err == nil && port > 0 {
		return port
	}
	if target.Port != 0 {
		return target.Port
	}
	return tg.Port
}
