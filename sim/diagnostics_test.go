package sim

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The point of the registry is to name a request that is still hanging, at the
// moment it hangs. A snapshot that only lists finished work would be useless
// for the case it exists to diagnose.
func TestInFlightMiddlewareNamesARequestWhileItIsStillRunning(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})

	handler := InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))

	req := httptest.NewRequest("POST", "/?Action=CreateSnapshot", nil)
	req.Header.Set("X-Amz-Target", "AmazonEC2.CreateSnapshot")
	served := make(chan struct{})
	go func() {
		defer close(served)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()

	<-entered
	snapshot := InFlightSnapshot()
	require.Len(t, snapshot, 1, "a request being served must appear in the registry")
	require.Equal(t, "POST", snapshot[0].Method)
	require.Equal(t, "AmazonEC2.CreateSnapshot", snapshot[0].Target,
		"the operation must be named: the path alone does not say what AWS was asked to do")

	close(release)
	<-served
	require.Empty(t, InFlightSnapshot(), "a finished request must leave the registry")
}

// A form-encoded EC2 call carries its operation in Action rather than a header.
func TestRequestOperationReadsTheFormAction(t *testing.T) {
	req := httptest.NewRequest("POST", "/?Action=DescribeSnapshots", nil)
	require.Equal(t, "DescribeSnapshots", requestOperation(req))
}

// manualClock advances only when the test says so. It announces every timer
// the watcher arms on armed, so the test sees each decision the watcher makes.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []manualTimer
	armed  chan time.Time
}

type manualTimer struct {
	at time.Time
	ch chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0), armed: make(chan time.Time, 16)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	timer := manualTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		timer.ch <- c.now
	} else {
		c.timers = append(c.timers, timer)
	}
	c.mu.Unlock()
	c.armed <- timer.at
	return timer.ch, func() {}
}

// advance moves the clock to at and fires every timer due by then.
func (c *manualClock) advance(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
	pending := c.timers[:0]
	for _, timer := range c.timers {
		if timer.at.After(at) {
			pending = append(pending, timer)
			continue
		}
		timer.ch <- at
	}
	c.timers = pending
}

// diagnosticLog collects the diagnostic's lines, which the watcher writes from
// its own goroutine, and signals each one on wrote.
type diagnosticLog struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	wrote chan struct{}
}

func newDiagnosticLog() *diagnosticLog {
	return &diagnosticLog{wrote: make(chan struct{}, 16)}
}

func (l *diagnosticLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.buf.Write(p)
	l.wrote <- struct{}{}
	return n, err
}

func (l *diagnosticLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// heldRequest serves one request through the middleware on clock, with a
// handler that runs declare and then blocks until the returned release is
// called; release returns once the middleware has finished with the request.
func heldRequest(t *testing.T, clock *manualClock, log *diagnosticLog, header http.Header, declare func(ctx context.Context)) (release func()) {
	t.Helper()
	hold := make(chan struct{})
	entered := make(chan struct{})
	handler := inFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		declare(r.Context())
		close(entered)
		<-hold
	}), clock, log)
	req := httptest.NewRequest("POST", "/v1/operations/op-1:wait", nil)
	for name, values := range header {
		req.Header[name] = values
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-entered
	return func() {
		close(hold)
		<-served
	}
}

func TestSlowRequestWithoutDeclaredWaitIsReportedAtTheThreshold(t *testing.T) {
	clock := newManualClock()
	log := newDiagnosticLog()
	start := clock.Now()
	release := heldRequest(t, clock, log, nil, func(context.Context) {})

	require.Equal(t, start.Add(SlowRequestThreshold), <-clock.armed)
	clock.advance(start.Add(SlowRequestThreshold))
	<-log.wrote
	release()

	lines := log.String()
	require.Contains(t, lines, "[sim-slow] still serving POST /v1/operations/op-1:wait  after 10s -- goroutine dump")
	require.Contains(t, lines, "[sim-slow] finished POST /v1/operations/op-1:wait")
	require.NotContains(t, lines, "declared wait")
}

// A long poll that blocks for its declared wait is the method working; only
// overrunning the declared wait by the threshold is worth a report.
func TestSlowRequestIsMeasuredFromTheEndOfItsDeclaredWait(t *testing.T) {
	clock := newManualClock()
	log := newDiagnosticLog()
	start := clock.Now()
	const wait = 2 * time.Minute
	release := heldRequest(t, clock, log, nil, func(ctx context.Context) { DeclareWait(ctx, wait) })

	due := start.Add(wait + SlowRequestThreshold)
	// The watcher may arm before the handler declares; that first deadline
	// then moves back to the end of the declared wait instead of reporting.
	if first := <-clock.armed; first.Before(due) {
		clock.advance(first)
		require.Equal(t, due, <-clock.armed)
	}
	clock.advance(due.Add(-time.Second))
	require.Empty(t, log.String(), "a request inside its declared wait must not be reported")

	clock.advance(due)
	<-log.wrote
	release()
	lines := log.String()
	require.Contains(t, lines, "[sim-slow] still serving POST /v1/operations/op-1:wait  after 2m10s (declared wait 2m0s)")
	require.Contains(t, lines, "[sim-slow] finished")
}

func TestDeclareWaitKeepsTheLongestDeclaration(t *testing.T) {
	clock := newManualClock()
	log := newDiagnosticLog()
	start := clock.Now()
	release := heldRequest(t, clock, log, nil, func(ctx context.Context) {
		DeclareWait(ctx, time.Minute)
		DeclareWait(ctx, 20*time.Second)
	})
	due := start.Add(time.Minute + SlowRequestThreshold)
	if first := <-clock.armed; first.Before(due) {
		clock.advance(first)
		require.Equal(t, due, <-clock.armed)
	}
	release()
	require.Empty(t, log.String())
}

func TestOpenEndedWaitIsNeverReported(t *testing.T) {
	clock := newManualClock()
	log := newDiagnosticLog()
	start := clock.Now()
	release := heldRequest(t, clock, log, nil, func(ctx context.Context) {
		DeclareWait(ctx, time.Minute)
		DeclareOpenEndedWait(ctx)
		DeclareWait(ctx, time.Minute)
	})

	snapshot := InFlightSnapshot()
	require.Len(t, snapshot, 1)
	require.Equal(t, openEndedWait, snapshot[0].Wait, "a bounded declaration must not shorten an open-ended one")

	// Firing whatever the watcher armed before the declaration must not
	// produce a report.
	clock.advance(start.Add(24 * time.Hour))
	release()
	require.Empty(t, log.String(), "an open-ended wait has no deadline to overrun")
}

// A WebSocket or other upgraded session lasts as long as its peers keep it,
// so the middleware treats it as open-ended without the handler declaring it.
func TestUpgradedConnectionIsAnOpenEndedWait(t *testing.T) {
	clock := newManualClock()
	log := newDiagnosticLog()
	header := http.Header{"Connection": {"keep-alive, Upgrade"}, "Upgrade": {"websocket"}}
	release := heldRequest(t, clock, log, header, func(context.Context) {})
	clock.advance(clock.Now().Add(24 * time.Hour))
	release()
	require.Empty(t, log.String())
	require.Empty(t, clock.armed, "an upgraded connection arms no slow-request deadline")
}

// A handler served outside the middleware -- a unit test, a batch sub-request
// -- still declares its wait, to no effect.
func TestDeclareWaitOutsideTheMiddlewareIsIgnored(t *testing.T) {
	ctx := context.Background()
	DeclareWait(ctx, time.Minute)
	DeclareOpenEndedWait(ctx)
	wait, openEnded := DeclaredWait(ctx)
	require.Zero(t, wait)
	require.False(t, openEnded)
}

func TestDeclaredWaitReadsBackWhatTheHandlerDeclared(t *testing.T) {
	type declared struct {
		wait      time.Duration
		openEnded bool
	}
	serve := func(declare func(ctx context.Context)) declared {
		var got declared
		InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			declare(r.Context())
			got.wait, got.openEnded = DeclaredWait(r.Context())
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		return got
	}
	require.Equal(t, declared{}, serve(func(context.Context) {}))
	require.Equal(t, declared{wait: 20 * time.Second}, serve(func(ctx context.Context) { DeclareWait(ctx, 20*time.Second) }))
	require.Equal(t, declared{}, serve(func(ctx context.Context) { DeclareWait(ctx, -time.Second) }),
		"a negative wait declares nothing")
	require.Equal(t, declared{openEnded: true}, serve(DeclareOpenEndedWait))
}

func TestUpgradeRequestNeedsBothHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Upgrade", "websocket")
	require.False(t, isUpgradeRequest(req))
	req.Header.Set("Connection", "Upgrade")
	require.True(t, isUpgradeRequest(req))
}
