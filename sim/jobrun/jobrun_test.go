package jobrun

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeWorkload exits when the test tells it to, or with 143 when stopped,
// as a container does on SIGTERM.
type fakeWorkload struct {
	exit chan Exit
	once sync.Once
}

func newFakeWorkload() *fakeWorkload { return &fakeWorkload{exit: make(chan Exit, 1)} }

func (w *fakeWorkload) Wait() Exit { return <-w.exit }

func (w *fakeWorkload) Stop() { w.end(143) }

func (w *fakeWorkload) end(code int) {
	w.once.Do(func() { w.exit <- Exit{Code: code, At: time.Now()} })
}

// harness starts a fake workload per attempt and reports each start on
// started, so a test drives the execution one event at a time.
type harness struct {
	t       *testing.T
	mu      sync.Mutex
	started chan Task
	byTask  map[[2]int]*fakeWorkload
	changes []Task
	verdict Verdict
	counts  Counts
	startFn func(ctx context.Context, task Task) (Workload, error)
}

func newHarness(t *testing.T) *harness {
	return &harness{t: t, started: make(chan Task, 64), byTask: map[[2]int]*fakeWorkload{}}
}

func (h *harness) hooks() Hooks {
	return Hooks{
		Start: func(ctx context.Context, task Task) (Workload, error) {
			if h.startFn != nil {
				return h.startFn(ctx, task)
			}
			w := newFakeWorkload()
			h.mu.Lock()
			h.byTask[[2]int{task.Index, task.Attempt}] = w
			h.mu.Unlock()
			h.started <- task
			return w, nil
		},
		Changed: func(task Task, _ Counts) {
			h.mu.Lock()
			h.changes = append(h.changes, task)
			h.mu.Unlock()
		},
		Finished: func(v Verdict, c Counts) {
			h.mu.Lock()
			h.verdict, h.counts = v, c
			h.mu.Unlock()
		},
	}
}

func (h *harness) workload(index, attempt int) *fakeWorkload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byTask[[2]int{index, attempt}]
}

func (h *harness) nextStart() Task {
	h.t.Helper()
	select {
	case task := <-h.started:
		return task
	case <-time.After(10 * time.Second):
		h.t.Fatal("no attempt started")
	}
	return Task{}
}

func (h *harness) noStart() {
	h.t.Helper()
	select {
	case task := <-h.started:
		h.t.Fatalf("task %d attempt %d started", task.Index, task.Attempt)
	default:
	}
}

func awaitDone(t *testing.T, e *Execution) {
	t.Helper()
	select {
	case <-e.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("execution did not finish: %+v", e.Counts())
	}
}

func TestParallelismBoundsRunningTasks(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 5, Parallelism: 2}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	first, second := h.nextStart(), h.nextStart()
	if first.Index+second.Index != 1 {
		t.Fatalf("the first two starts were tasks %d and %d, want 0 and 1", first.Index, second.Index)
	}
	if c := e.Counts(); c.Running != 2 || c.Pending != 3 {
		t.Fatalf("counts = %+v, want 2 running and 3 pending", c)
	}
	h.noStart()
	for want := 2; want < 5; want++ {
		h.workload(want-2, 0).end(0)
		if got := h.nextStart(); got.Index != want {
			t.Fatalf("started task %d, want %d", got.Index, want)
		}
	}
	h.workload(3, 0).end(0)
	h.workload(4, 0).end(0)
	awaitDone(t, e)
	if h.verdict != Success || h.counts.Succeeded != 5 {
		t.Fatalf("verdict %v counts %+v, want Success with 5 succeeded", h.verdict, h.counts)
	}
}

func TestZeroParallelismRunsEveryTask(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 3}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for range 3 {
		seen[h.nextStart().Index] = true
	}
	if len(seen) != 3 {
		t.Fatalf("started tasks %v, want all three at once", seen)
	}
	for i := range 3 {
		h.workload(i, 0).end(0)
	}
	awaitDone(t, e)
}

func TestFailedAttemptRetriesUntilRetriesRunOut(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 2, MaxRetries: 1}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	h.nextStart()
	h.nextStart()
	h.workload(0, 0).end(1)
	if got := h.nextStart(); got.Index != 0 || got.Attempt != 1 {
		t.Fatalf("started task %d attempt %d, want task 0 attempt 1", got.Index, got.Attempt)
	}
	h.workload(0, 1).end(0)
	h.workload(1, 0).end(2)
	if got := h.nextStart(); got.Index != 1 || got.Attempt != 1 {
		t.Fatalf("started task %d attempt %d, want task 1 attempt 1", got.Index, got.Attempt)
	}
	h.workload(1, 1).end(3)
	awaitDone(t, e)
	tasks := e.Tasks()
	if tasks[0].Status != Succeeded || tasks[0].Attempt != 1 {
		t.Errorf("task 0 = %v attempt %d, want Succeeded after one retry", tasks[0].Status, tasks[0].Attempt)
	}
	if tasks[1].Status != Failed || tasks[1].Last == nil || tasks[1].Last.Code != 3 {
		t.Errorf("task 1 = %+v, want Failed with exit 3", tasks[1])
	}
	if h.verdict != Failure || h.counts.Retried != 2 || h.counts.Failed != 1 || h.counts.Succeeded != 1 {
		t.Fatalf("verdict %v counts %+v", h.verdict, h.counts)
	}
}

func TestRetryHookRefusesARetry(t *testing.T) {
	h := newHarness(t)
	hooks := h.hooks()
	hooks.Retry = func(_ Task, r Result) bool { return r.Code != 7 }
	e, err := Start(Spec{Tasks: 1, MaxRetries: 5}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	h.nextStart()
	h.workload(0, 0).end(7)
	awaitDone(t, e)
	if task := e.Tasks()[0]; task.Status != Failed || task.Attempt != 0 {
		t.Fatalf("task = %v attempt %d, want Failed without a retry", task.Status, task.Attempt)
	}
}

func TestAttemptTimeoutStopsAndRetries(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 1, MaxRetries: 1, AttemptTimeout: 20 * time.Millisecond}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	h.nextStart()
	if got := h.nextStart(); got.Attempt != 1 || got.Last == nil || !got.Last.TimedOut || got.Last.Code != 143 {
		t.Fatalf("second attempt started after %+v, want a timed-out first attempt", got.Last)
	}
	h.workload(0, 1).end(0)
	awaitDone(t, e)
	if h.verdict != Success {
		t.Fatalf("verdict %v, want Success", h.verdict)
	}
}

func TestTaskTimeoutFailsWithoutRetry(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 1, MaxRetries: 3, TaskTimeout: 20 * time.Millisecond}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	h.nextStart()
	awaitDone(t, e)
	task := e.Tasks()[0]
	if task.Status != Failed || task.Attempt != 0 || task.Last == nil || !task.Last.TimedOut {
		t.Fatalf("task = %+v, want Failed by its timeout with no retry", task)
	}
}

func TestCancelStopsRunningAndPendingTasks(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 3, Parallelism: 1, MaxRetries: 3}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	h.nextStart()
	e.Cancel()
	if c := e.Counts(); c.Cancelled != 3 || c.Running != 0 {
		t.Fatalf("counts right after Cancel = %+v, want 3 cancelled", c)
	}
	h.mu.Lock()
	runningStopped := false
	for _, change := range h.changes {
		switch {
		case change.Index == 0 && change.Stopping:
			runningStopped = true
		case change.Index != 0 && change.Stopping:
			t.Errorf("task %d, cancelled before it started, reported stopping", change.Index)
		}
	}
	h.mu.Unlock()
	if !runningStopped {
		t.Error("the running task was never reported stopping")
	}
	awaitDone(t, e)
	h.noStart()
	tasks := e.Tasks()
	if tasks[0].Stopping {
		t.Error("task 0 still stopping once the execution finished")
	}
	if tasks[0].Last == nil || !tasks[0].Last.Stopped || tasks[0].Last.Code != 143 {
		t.Errorf("task 0 last attempt = %+v, want stopped with 143", tasks[0].Last)
	}
	if tasks[1].Last != nil || tasks[1].Status != Cancelled {
		t.Errorf("task 1 = %+v, want cancelled before it started", tasks[1])
	}
	if h.verdict != Cancellation {
		t.Fatalf("verdict %v, want Cancellation", h.verdict)
	}
}

func TestCancelEndsAStartInProgress(t *testing.T) {
	h := newHarness(t)
	entered := make(chan struct{})
	h.startFn = func(ctx context.Context, _ Task) (Workload, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	e, err := Start(Spec{Tasks: 1}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	e.Cancel()
	awaitDone(t, e)
	task := e.Tasks()[0]
	if task.Status != Cancelled || task.Last == nil || !task.Last.Stopped || !errors.Is(task.Last.StartErr, context.Canceled) {
		t.Fatalf("task = %+v, want cancelled while it started", task)
	}
}

func TestStartErrorFailsTheAttempt(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.startFn = func(context.Context, Task) (Workload, error) {
		calls++
		return nil, errors.New("image not found")
	}
	e, err := Start(Spec{Tasks: 1, MaxRetries: 2}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	awaitDone(t, e)
	task := e.Tasks()[0]
	if task.Status != Failed || task.Attempt != 2 || calls != 3 || task.Last.StartErr == nil {
		t.Fatalf("task = %+v after %d starts, want Failed after three failed starts", task, calls)
	}
}

func TestCompletionsStopsTheRestOnceReached(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 3, Policy: Completions(1)}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.nextStart()
	}
	h.workload(1, 0).end(0)
	awaitDone(t, e)
	if h.verdict != Success || h.counts.Succeeded != 1 || h.counts.Cancelled != 2 {
		t.Fatalf("verdict %v counts %+v, want Success with the others cancelled", h.verdict, h.counts)
	}
}

func TestCompletionsFailsOnceUnreachable(t *testing.T) {
	h := newHarness(t)
	e, err := Start(Spec{Tasks: 3, Policy: Completions(2)}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.nextStart()
	}
	h.workload(0, 0).end(1)
	h.workload(2, 0).end(1)
	awaitDone(t, e)
	if h.verdict != Failure || h.counts.Failed != 2 || h.counts.Cancelled != 1 {
		t.Fatalf("verdict %v counts %+v, want Failure with the last task cancelled", h.verdict, h.counts)
	}
}

func TestResumeAdoptsRetriesLostAndStartsPending(t *testing.T) {
	h := newHarness(t)
	survivor := newFakeWorkload()
	hooks := h.hooks()
	hooks.Adopt = func(task Task) (Workload, error) {
		if task.Index == 0 {
			return survivor, nil
		}
		return nil, nil
	}
	started := time.Now().Add(-time.Minute)
	e, err := Resume(Spec{Tasks: 4, Parallelism: 3, MaxRetries: 1}, hooks, []Task{
		{Index: 0, Status: Running, Started: started, FirstStarted: started},
		{Index: 1, Status: Running, Started: started, FirstStarted: started},
		{Index: 2, Status: Succeeded, Ended: started},
		{Index: 3, Status: Pending},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]Task{}
	for range 2 {
		task := h.nextStart()
		got[task.Index] = task
	}
	if lost, ok := got[1]; !ok || lost.Attempt != 1 || lost.Last == nil || !lost.Last.Lost {
		t.Fatalf("task 1 restarted as %+v, want attempt 1 after a lost attempt", got[1])
	}
	if _, ok := got[3]; !ok {
		t.Fatalf("pending task 3 did not start: %v", got)
	}
	survivor.end(0)
	h.workload(1, 1).end(0)
	h.workload(3, 0).end(0)
	awaitDone(t, e)
	if h.verdict != Success || h.counts.Succeeded != 4 || h.counts.Retried != 1 {
		t.Fatalf("verdict %v counts %+v", h.verdict, h.counts)
	}
}

func TestResumeFailsLostTaskWithoutRetries(t *testing.T) {
	h := newHarness(t)
	hooks := h.hooks()
	hooks.Adopt = func(Task) (Workload, error) { return nil, nil }
	e, err := Resume(Spec{Tasks: 1}, hooks, []Task{{Index: 0, Status: Running, Started: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	awaitDone(t, e)
	if task := e.Tasks()[0]; task.Status != Failed || !task.Last.Lost {
		t.Fatalf("task = %+v, want Failed as lost", task)
	}
	if h.verdict != Failure {
		t.Fatalf("verdict %v, want Failure", h.verdict)
	}
}

func TestResumeOfFinishedTasksFinishes(t *testing.T) {
	h := newHarness(t)
	hooks := h.hooks()
	hooks.Adopt = func(Task) (Workload, error) { return nil, nil }
	e, err := Resume(Spec{Tasks: 2}, hooks, []Task{{Index: 0, Status: Succeeded}, {Index: 1, Status: Succeeded}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Done():
	default:
		t.Fatal("an execution resumed with every task ended did not finish at once")
	}
	if h.verdict != Success {
		t.Fatalf("verdict %v, want Success", h.verdict)
	}
}

func TestSpecValidation(t *testing.T) {
	hooks := Hooks{Start: func(context.Context, Task) (Workload, error) { return newFakeWorkload(), nil }}
	for _, spec := range []Spec{{Tasks: 0}, {Tasks: 1, Parallelism: -1}, {Tasks: 1, MaxRetries: -1}} {
		if _, err := Start(spec, hooks); err == nil {
			t.Errorf("Start(%+v) accepted", spec)
		}
	}
	if _, err := Start(Spec{Tasks: 1}, Hooks{}); err == nil {
		t.Error("Start without a Start hook accepted")
	}
	if _, err := Resume(Spec{Tasks: 1}, hooks, []Task{{}}); err == nil {
		t.Error("Resume without an Adopt hook accepted")
	}
}

// graceWorkload holds its Stop until the test releases it, as a container
// that ignores SIGTERM holds a stop for its whole grace.
type graceWorkload struct {
	*fakeWorkload
	stopping chan struct{}
	release  chan struct{}
}

func (w *graceWorkload) Stop() {
	close(w.stopping)
	<-w.release
	w.end(137)
}

func TestCancelAnswersBeforeTheWorkloadsGraceEnds(t *testing.T) {
	h := newHarness(t)
	w := &graceWorkload{fakeWorkload: newFakeWorkload(), stopping: make(chan struct{}), release: make(chan struct{})}
	started := make(chan struct{})
	h.startFn = func(context.Context, Task) (Workload, error) {
		close(started)
		return w, nil
	}
	e, err := Start(Spec{Tasks: 1}, h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancelled := make(chan struct{})
	go func() {
		e.Cancel()
		close(cancelled)
	}()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel waited for the workload's grace")
	}
	<-w.stopping
	if c := e.Counts(); c.Cancelled != 1 {
		t.Fatalf("counts while the workload stops = %+v, want 1 cancelled", c)
	}
	close(w.release)
	awaitDone(t, e)
	if tasks := e.Tasks(); tasks[0].Last == nil || tasks[0].Last.Code != 137 {
		t.Errorf("task 0 last attempt = %+v, want the code it stopped with", tasks[0].Last)
	}
}
