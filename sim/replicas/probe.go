package replicas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Probe is one container probe as Kubernetes defines it and Cloud Run and
// Container Apps adopt it: a check run on a schedule, whose consecutive
// results move the container between passing and failing. The cloud fills in
// its own defaults; Check is one attempt, usually lbplane.ProbeHTTP,
// lbplane.ProbeTCP or lbplane.ProbeGRPC against the container.
type Probe struct {
	// Check makes one attempt; its context ends once Timeout passes.
	Check func(ctx context.Context) error
	// InitialDelay is how long after the container starts the first attempt
	// waits; Period is the time from one attempt's start to the next's.
	InitialDelay time.Duration
	Period       time.Duration
	Timeout      time.Duration
	// FailureThreshold and SuccessThreshold are the attempts in a row that
	// fail the probe and that pass it; zero counts as one.
	FailureThreshold int
	SuccessThreshold int
}

// ErrExited is the error a probe awaited on a container that exited ends with.
var ErrExited = errors.New("the container exited")

// FailedError is a probe that failed its failure threshold in a row.
type FailedError struct {
	Failures int
	Last     error
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("failed %d time(s) in a row: %v", e.Failures, e.Last)
}

func (e *FailedError) Unwrap() error { return e.Last }

func threshold(n int) int { return max(n, 1) }

// attempt runs one check bounded by the probe's timeout.
func (p Probe) attempt(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	return p.Check(ctx)
}

// Await runs the probe from InitialDelay after started until it has passed
// SuccessThreshold times in a row. It fails with a *FailedError once it has
// failed FailureThreshold times in a row, with ErrExited once exited closes,
// and with ctx's error once ctx ends; an exit or end cancels an attempt in
// progress.
func (p Probe) Await(ctx context.Context, started time.Time, exited <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-exited:
			cancel()
		case <-ctx.Done():
		}
	}()
	stopped := func(last error) error {
		select {
		case <-exited:
			return ErrExited
		default:
		}
		if last != nil {
			return fmt.Errorf("%w: %w", ctx.Err(), last)
		}
		return ctx.Err()
	}
	next := started.Add(p.InitialDelay)
	passed, failed := 0, 0
	var last error
	for {
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return stopped(last)
		case <-timer.C:
		}
		select {
		case <-exited:
			return ErrExited
		default:
		}
		next = time.Now().Add(p.Period)
		if last = p.attempt(ctx); last == nil {
			failed = 0
			if passed++; passed >= threshold(p.SuccessThreshold) {
				return nil
			}
			continue
		}
		if ctx.Err() != nil {
			return stopped(last)
		}
		passed = 0
		if failed++; failed >= threshold(p.FailureThreshold) {
			return &FailedError{Failures: failed, Last: last}
		}
	}
}

// Watch runs the probe on its period, its first attempt at InitialDelay after
// started, for as long as the container runs, which ends when exited closes.
// It calls fail once the probe has failed FailureThreshold times in a row
// since it last passed, and pass once it has passed SuccessThreshold times in
// a row since it last failed; the probe starts out passing.
func (p Probe) Watch(started time.Time, exited <-chan struct{}, pass, fail func()) {
	bg.Go(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-exited
			cancel()
		}()
		next := started.Add(p.InitialDelay)
		if now := time.Now(); next.Before(now) {
			next = now.Add(p.Period)
		}
		healthy := true
		passed, failed := 0, 0
		for {
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			next = time.Now().Add(p.Period)
			err := p.attempt(ctx)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				failed = 0
				if passed++; !healthy && passed >= threshold(p.SuccessThreshold) {
					healthy = true
					pass()
				}
				continue
			}
			passed = 0
			if failed++; healthy && failed >= threshold(p.FailureThreshold) {
				healthy = false
				fail()
			}
		}
	})
}

// Probes are a container's startup, readiness and liveness probes; nil is a
// probe the container does not run.
type Probes struct {
	Startup, Readiness, Liveness *Probe
}

// Run starts a container's probes the way the kubelet does. The startup probe
// runs first; once it passes, the liveness probe starts watching, calling
// restart when it fails, and the readiness probe runs until it first passes,
// which makes the container ready. From then on the readiness probe keeps
// ready current. A container without a readiness probe is ready once its
// startup probe passes. Run returns once the container is ready, or with the
// error that kept it from becoming so.
func (ps Probes) Run(ctx context.Context, started time.Time, exited <-chan struct{}, ready func(bool), restart func()) error {
	if ps.Startup != nil {
		if err := ps.Startup.Await(ctx, started, exited); err != nil {
			return roleError("startup", err)
		}
	}
	if ps.Liveness != nil {
		ps.Liveness.Watch(started, exited, func() {}, restart)
	}
	if ps.Readiness != nil {
		if err := ps.Readiness.Await(ctx, started, exited); err != nil {
			return roleError("readiness", err)
		}
		ps.Readiness.Watch(time.Time{}, exited, func() { ready(true) }, func() { ready(false) })
	}
	ready(true)
	return nil
}

func roleError(role string, err error) error {
	var failed *FailedError
	switch {
	case errors.Is(err, ErrExited):
		return fmt.Errorf("%w before its %s probe passed", ErrExited, role)
	case errors.As(err, &failed):
		return fmt.Errorf("%s probe %w", role, err)
	default:
		return fmt.Errorf("%s probe: %w", role, err)
	}
}

// Watch resumes the probes of a container that is already ready, such as one
// a restarted simulator adopts: the readiness probe keeps ready current and
// the liveness probe calls restart when it fails.
func (ps Probes) Watch(exited <-chan struct{}, ready func(bool), restart func()) {
	if ps.Readiness != nil {
		ps.Readiness.Watch(time.Time{}, exited, func() { ready(true) }, func() { ready(false) })
	}
	if ps.Liveness != nil {
		ps.Liveness.Watch(time.Time{}, exited, func() {}, restart)
	}
}
