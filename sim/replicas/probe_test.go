package replicas

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scripted is a check that answers each attempt with the next of its results,
// and with the last one once they run out.
type scripted struct {
	mu       sync.Mutex
	results  []error
	attempts int
}

func (s *scripted) check(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.attempts, len(s.results)-1)
	s.attempts++
	return s.results[i]
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

var errDown = errors.New("down")

func TestProbeAwaitPassesAfterItsSuccessThreshold(t *testing.T) {
	s := &scripted{results: []error{errDown, nil, errDown, nil, nil}}
	p := Probe{Check: s.check, Period: time.Millisecond, Timeout: time.Second, FailureThreshold: 3, SuccessThreshold: 2}
	require.NoError(t, p.Await(context.Background(), time.Now(), nil))
	require.Equal(t, 5, s.count(), "a failure resets the passes in a row")
}

func TestProbeAwaitFailsAtItsFailureThreshold(t *testing.T) {
	s := &scripted{results: []error{errDown}}
	p := Probe{Check: s.check, Period: time.Millisecond, Timeout: time.Second, FailureThreshold: 3}
	err := p.Await(context.Background(), time.Now(), nil)
	var failed *FailedError
	require.ErrorAs(t, err, &failed)
	require.Equal(t, 3, failed.Failures)
	require.ErrorIs(t, err, errDown)
	require.Equal(t, 3, s.count())
}

func TestProbeAwaitWaitsForTheInitialDelay(t *testing.T) {
	s := &scripted{results: []error{nil}}
	started := time.Now()
	p := Probe{Check: s.check, InitialDelay: 20 * time.Millisecond, Period: time.Millisecond, Timeout: time.Second}
	require.NoError(t, p.Await(context.Background(), started, nil))
	require.GreaterOrEqual(t, time.Since(started), 20*time.Millisecond)
}

func TestProbeAwaitEndsWhenTheContainerExits(t *testing.T) {
	exited := make(chan struct{})
	attempted := make(chan struct{}, 1)
	p := Probe{Timeout: time.Hour, Period: time.Hour, FailureThreshold: 1, Check: func(ctx context.Context) error {
		attempted <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	go func() {
		<-attempted
		close(exited)
	}()
	require.ErrorIs(t, p.Await(context.Background(), time.Now(), exited), ErrExited,
		"an exit cancels the attempt in progress")
}

func TestProbeAttemptIsBoundedByItsTimeout(t *testing.T) {
	p := Probe{Timeout: 10 * time.Millisecond, Period: time.Millisecond, FailureThreshold: 1, Check: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	err := p.Await(context.Background(), time.Now(), nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestProbeWatchReportsTransitions(t *testing.T) {
	s := &scripted{results: []error{nil, errDown, errDown, nil, nil, nil}}
	exited := make(chan struct{})
	defer close(exited)
	events := make(chan string, 4)
	p := Probe{Check: s.check, Period: time.Millisecond, Timeout: time.Second, FailureThreshold: 2, SuccessThreshold: 2}
	p.Watch(time.Time{}, exited, func() { events <- "pass" }, func() { events <- "fail" })
	require.Equal(t, "fail", <-events, "two failures in a row fail a passing probe")
	require.Equal(t, "pass", <-events, "two passes in a row restore it")
}

func TestProbesRunMakesTheContainerReadyInOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if len(order) == 0 || order[len(order)-1] != name {
				order = append(order, name)
			}
			return nil
		}
	}
	exited := make(chan struct{})
	defer close(exited)
	ready := make(chan bool, 1)
	ps := Probes{
		Startup:   &Probe{Check: record("startup"), Period: time.Millisecond, Timeout: time.Second},
		Readiness: &Probe{Check: record("readiness"), Period: time.Hour, Timeout: time.Second},
	}
	require.NoError(t, ps.Run(context.Background(), time.Now(), exited, func(r bool) { ready <- r }, func() {}))
	require.True(t, <-ready)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"startup", "readiness"}, order)
}

func TestProbesRunRestartsOnALivenessFailure(t *testing.T) {
	exited := make(chan struct{})
	restarted := make(chan struct{})
	ps := Probes{Liveness: &Probe{Check: func(context.Context) error { return errDown },
		Period: time.Millisecond, Timeout: time.Second, FailureThreshold: 2}}
	require.NoError(t, ps.Run(context.Background(), time.Now(), exited, func(bool) {}, func() { close(restarted) }))
	<-restarted
	close(exited)
}

func TestProbesRunNamesTheProbeThatFailed(t *testing.T) {
	ps := Probes{Startup: &Probe{Check: func(context.Context) error { return errDown },
		Period: time.Millisecond, Timeout: time.Second, FailureThreshold: 2}}
	err := ps.Run(context.Background(), time.Now(), nil, func(bool) { t.Error("not ready") }, func() {})
	require.EqualError(t, err, "startup probe failed 2 time(s) in a row: down")

	exited := make(chan struct{})
	close(exited)
	err = Probes{Readiness: &Probe{Check: func(context.Context) error { return errDown }, Period: time.Hour, Timeout: time.Second}}.
		Run(context.Background(), time.Now(), exited, func(bool) {}, func() {})
	require.ErrorIs(t, err, ErrExited)
	require.EqualError(t, err, "the container exited before its readiness probe passed")
}

func TestCrashLoopBackoffDoublesToFiveMinutes(t *testing.T) {
	for consecutive, want := range map[int]time.Duration{
		1: 10 * time.Second, 2: 20 * time.Second, 3: 40 * time.Second, 5: 160 * time.Second, 6: 5 * time.Minute, 20: 5 * time.Minute,
	} {
		require.Equal(t, want, CrashLoopBackoff(consecutive), "after %d exits", consecutive)
	}
}
