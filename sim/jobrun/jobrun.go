// Package jobrun runs an execution of tasks to completion: the mechanism
// behind a Cloud Run job execution's tasks and an Azure Container Apps job
// execution's replicas.
//
// An execution has a fixed number of tasks, indexed from 0. At most
// Parallelism of them run at once; the rest wait, lowest index first. A task
// runs as a series of attempts: an attempt that fails runs again while the
// task has retries left and the cloud's Retry hook agrees. An attempt timeout
// bounds each attempt, and a task timeout bounds a task across all of its
// attempts. The execution's Policy turns the tasks' counts into a verdict;
// once it is decided, pending tasks never start and running ones are stopped.
// Cancel stops the execution: tasks not yet started never start, and running
// ones are stopped.
//
// The package holds no wire shapes and no store. The cloud starts each
// attempt's workload through Hooks.Start and writes every change of a task
// and the execution's counts to its own records through Hooks.Changed. After a
// simulator restart the cloud reads those records back into Tasks and hands
// them to Resume, which takes over each running task's workload through
// Hooks.Adopt. An attempt whose workload did not survive ends as lost, and
// the task's retries decide what follows, as for any failed attempt.
//
// The hooks Changed and Finished run with the execution's lock held, in the
// order the changes happen, so the cloud's records never go back in time;
// they must not call the Execution's methods. Start and Adopt run without it.
package jobrun

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Status is where a task is in its run.
type Status int

const (
	// Pending tasks wait for a free slot under the execution's parallelism.
	Pending Status = iota
	// Running tasks have an attempt starting or running.
	Running
	// Succeeded tasks had an attempt exit 0.
	Succeeded
	// Failed tasks had their last attempt fail with no retry left.
	Failed
	// Cancelled tasks were stopped by Cancel or by the verdict, before they
	// started or while they ran.
	Cancelled
)

func (s Status) String() string {
	switch s {
	case Pending:
		return "Pending"
	case Running:
		return "Running"
	case Succeeded:
		return "Succeeded"
	case Failed:
		return "Failed"
	case Cancelled:
		return "Cancelled"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Terminal reports whether a task in status s has finished.
func (s Status) Terminal() bool { return s >= Succeeded }

// Exit is how a workload ended.
type Exit struct {
	// Code is the main container's exit code, -1 when it has none.
	Code int
	// Err is the failure that ended the workload other than its own exit.
	Err error
	// At is when the workload ended.
	At time.Time
}

// Workload is one attempt's running workload.
type Workload interface {
	// Wait blocks until the workload has exited, releases what it held, and
	// reports how it ended.
	Wait() Exit
	// Stop asks the workload to stop, with the grace the cloud gives it;
	// Wait then returns.
	Stop()
}

// Result is how one attempt ended.
type Result struct {
	Exit
	// Started is when the attempt's workload started; zero when it never
	// did.
	Started time.Time
	// StartErr is why the attempt's workload did not start.
	StartErr error
	// TimedOut reports that a timeout stopped the attempt.
	TimedOut bool
	// Stopped reports that the execution stopped the attempt: a Cancel, or
	// the verdict reached while it ran.
	Stopped bool
	// Lost reports that the attempt's workload was gone when the execution
	// resumed after a restart.
	Lost bool
}

// Succeeded reports whether the attempt succeeded: its workload started and
// exited 0 on its own.
func (r Result) Succeeded() bool {
	return r.StartErr == nil && r.Err == nil && r.Code == 0 && !r.TimedOut && !r.Stopped && !r.Lost
}

// Task is one task's state.
type Task struct {
	Index int
	// Attempt is the number of the current or last attempt, from 0, which is
	// how many times the task has been retried.
	Attempt int
	Status  Status
	// FirstStarted is when the task's first attempt started; the task
	// timeout counts from it.
	FirstStarted time.Time
	// Started is when the current or last attempt's workload started; the
	// attempt timeout counts from it. It is zero while an attempt's workload
	// is still being started.
	Started time.Time
	// Ended is when the task reached its terminal status.
	Ended time.Time
	// Last is the latest attempt that ended, nil before the first ends.
	Last *Result
	// Stopping reports a task the execution ended while its workload still
	// starts or runs; it reports the task again once the workload exits.
	Stopping bool
}

// Counts are the execution's tasks by status.
type Counts struct {
	Tasks     int
	Pending   int
	Running   int
	Succeeded int
	Failed    int
	Cancelled int
	// Retried is the number of tasks that have retried at least once.
	Retried int
}

// Verdict is how an execution ends.
type Verdict int

const (
	// Undecided executions run on.
	Undecided Verdict = iota
	// Success is an execution its policy declared succeeded.
	Success
	// Failure is an execution its policy declared failed.
	Failure
	// Cancellation is an execution Cancel stopped.
	Cancellation
)

func (v Verdict) String() string {
	switch v {
	case Undecided:
		return "Undecided"
	case Success:
		return "Success"
	case Failure:
		return "Failure"
	case Cancellation:
		return "Cancellation"
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// Policy decides an execution's verdict from its counts. It returns
// Undecided while the execution runs on; it never returns Cancellation.
type Policy func(Counts) Verdict

// AllSucceed is the policy of an execution that runs every task to its end
// and succeeds only when every task succeeded.
func AllSucceed(c Counts) Verdict {
	switch {
	case c.Pending+c.Running > 0:
		return Undecided
	case c.Succeeded == c.Tasks:
		return Success
	}
	return Failure
}

// Completions is the policy of an execution that succeeds once n tasks have
// succeeded and fails once too few tasks remain to get there.
func Completions(n int) Policy {
	return func(c Counts) Verdict {
		switch {
		case c.Succeeded >= n:
			return Success
		case c.Succeeded+c.Pending+c.Running < n:
			return Failure
		}
		return Undecided
	}
}

// Spec is what an execution runs.
type Spec struct {
	// Tasks is how many tasks the execution runs.
	Tasks int
	// Parallelism is how many tasks run at once; 0 runs every task at once.
	Parallelism int
	// MaxRetries is how many times a task runs again after a failed attempt.
	MaxRetries int
	// AttemptTimeout bounds each attempt; an attempt that outlives it is
	// stopped and counts as failed. Zero sets no bound.
	AttemptTimeout time.Duration
	// TaskTimeout bounds a task across all of its attempts, from its first
	// start; a task that outlives it is stopped and fails without a retry.
	// Zero sets no bound.
	TaskTimeout time.Duration
	// Policy decides the verdict; nil is AllSucceed.
	Policy Policy
}

// Hooks are what the cloud does for an execution.
type Hooks struct {
	// Start starts the workload of task t's attempt t.Attempt. ctx ends when
	// the execution stops the task before its workload started.
	Start func(ctx context.Context, t Task) (Workload, error)
	// Adopt takes over the workload a running task had when the simulator
	// stopped. It returns a nil Workload and no error when the workload is
	// gone. Only Resume calls it.
	Adopt func(t Task) (Workload, error)
	// Retry decides whether task t runs again after attempt r failed, once
	// the task has retries left. Nil retries every failed attempt.
	Retry func(t Task, r Result) bool
	// Changed records task t's new state and the execution's counts.
	Changed func(t Task, c Counts)
	// Finished records the execution's verdict once every workload it
	// started has exited.
	Finished func(v Verdict, c Counts)
}

// Execution is one run of a Spec.
type Execution struct {
	spec  Spec
	hooks Hooks
	done  chan struct{}

	mu       sync.Mutex
	tasks    []Task
	attempts []*attempt
	verdict  Verdict
	finished bool
}

// attempt is the live part of a running task's current attempt.
type attempt struct {
	ctx      context.Context
	cancel   context.CancelFunc
	workload Workload
	// stopped and timedOut say why the execution stopped the attempt.
	stopped     bool
	timedOut    bool
	taskExpired bool
	timers      []*bg.Timer
}

// Start begins an execution of spec with every task pending.
func Start(spec Spec, hooks Hooks) (*Execution, error) {
	e, err := newExecution(spec, hooks)
	if err != nil {
		return nil, err
	}
	for i := range e.tasks {
		e.tasks[i] = Task{Index: i}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.advanceLocked()
	return e, nil
}

// Resume takes over an execution an earlier simulator process ran, from the
// tasks it recorded: a running task's workload is adopted, and an attempt
// whose workload is gone ends as lost. Pending tasks start as slots free.
func Resume(spec Spec, hooks Hooks, tasks []Task) (*Execution, error) {
	e, err := newExecution(spec, hooks)
	if err != nil {
		return nil, err
	}
	if hooks.Adopt == nil {
		return nil, errors.New("jobrun: Resume needs an Adopt hook")
	}
	if len(tasks) != spec.Tasks {
		return nil, fmt.Errorf("jobrun: resume %d tasks of an execution of %d", len(tasks), spec.Tasks)
	}
	for _, t := range tasks {
		if t.Index < 0 || t.Index >= spec.Tasks {
			return nil, fmt.Errorf("jobrun: task index %d outside an execution of %d", t.Index, spec.Tasks)
		}
		e.tasks[t.Index] = t
	}
	type adopted struct {
		index    int
		workload Workload
		err      error
	}
	var found []adopted
	for _, t := range e.tasks {
		if t.Status != Running {
			continue
		}
		w, err := hooks.Adopt(t)
		found = append(found, adopted{t.Index, w, err})
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range found {
		t := &e.tasks[a.index]
		if a.workload == nil {
			result := Result{Started: t.Started, Lost: true, Exit: Exit{Code: -1, Err: a.err, At: time.Now()}}
			e.attemptEndedLocked(a.index, result)
			continue
		}
		at := e.newAttemptLocked(t)
		at.workload = a.workload
		if t.Started.IsZero() {
			t.Started = time.Now()
		}
		if t.FirstStarted.IsZero() {
			t.FirstStarted = t.Started
		}
		e.armTimersLocked(t, at)
		e.watchAttempt(a.index, at)
	}
	e.advanceLocked()
	return e, nil
}

func newExecution(spec Spec, hooks Hooks) (*Execution, error) {
	if spec.Tasks < 1 {
		return nil, fmt.Errorf("jobrun: an execution needs at least one task, got %d", spec.Tasks)
	}
	if spec.Parallelism < 0 || spec.MaxRetries < 0 || spec.AttemptTimeout < 0 || spec.TaskTimeout < 0 {
		return nil, errors.New("jobrun: parallelism, retries and timeouts cannot be negative")
	}
	if hooks.Start == nil {
		return nil, errors.New("jobrun: an execution needs a Start hook")
	}
	if spec.Policy == nil {
		spec.Policy = AllSucceed
	}
	return &Execution{
		spec:     spec,
		hooks:    hooks,
		done:     make(chan struct{}),
		tasks:    make([]Task, spec.Tasks),
		attempts: make([]*attempt, spec.Tasks),
	}, nil
}

// Done closes once the execution has finished: its verdict is decided, every
// workload it started has exited, and Finished has returned.
func (e *Execution) Done() <-chan struct{} { return e.done }

// Tasks returns a copy of every task's state, by index.
func (e *Execution) Tasks() []Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	tasks := make([]Task, len(e.tasks))
	for i := range e.tasks {
		tasks[i] = e.taskLocked(i)
	}
	return tasks
}

func (e *Execution) taskLocked(i int) Task {
	t := e.tasks[i]
	t.Stopping = t.Status.Terminal() && e.attempts[i] != nil
	return t
}

// Counts returns the execution's counts.
func (e *Execution) Counts() Counts {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.countsLocked()
}

// Verdict returns the execution's verdict, Undecided while it runs on.
func (e *Execution) Verdict() Verdict {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.verdict
}

// Cancel stops the execution: pending tasks never start and running ones are
// stopped, and each ends Cancelled. It does nothing once the verdict is
// decided.
func (e *Execution) Cancel() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.verdict != Undecided {
		return
	}
	e.verdict = Cancellation
	e.stopRemainingLocked()
	e.finishIfDoneLocked()
}

func (e *Execution) countsLocked() Counts {
	c := Counts{Tasks: len(e.tasks)}
	for _, t := range e.tasks {
		switch t.Status {
		case Pending:
			c.Pending++
		case Running:
			c.Running++
		case Succeeded:
			c.Succeeded++
		case Failed:
			c.Failed++
		case Cancelled:
			c.Cancelled++
		}
		if t.Attempt > 0 {
			c.Retried++
		}
	}
	return c
}

func (e *Execution) changedLocked(i int) {
	if e.hooks.Changed != nil {
		e.hooks.Changed(e.taskLocked(i), e.countsLocked())
	}
}

// advanceLocked decides the verdict when the policy can, and otherwise starts
// pending tasks into the free slots, lowest index first.
func (e *Execution) advanceLocked() {
	if e.verdict == Undecided {
		if v := e.spec.Policy(e.countsLocked()); v != Undecided {
			e.verdict = v
			e.stopRemainingLocked()
		}
	}
	if e.verdict == Undecided {
		limit := e.spec.Parallelism
		if limit == 0 {
			limit = len(e.tasks)
		}
		running := 0
		for _, t := range e.tasks {
			if t.Status == Running {
				running++
			}
		}
		for i := range e.tasks {
			if running >= limit {
				break
			}
			if e.tasks[i].Status != Pending {
				continue
			}
			running++
			e.tasks[i].Status = Running
			e.startAttemptLocked(i)
		}
	}
	e.finishIfDoneLocked()
}

// stopRemainingLocked ends every task that has not ended as Cancelled and
// stops the workloads that run.
func (e *Execution) stopRemainingLocked() {
	now := time.Now()
	for i := range e.tasks {
		t := &e.tasks[i]
		if t.Status.Terminal() {
			continue
		}
		t.Status = Cancelled
		t.Ended = now
		if at := e.attempts[i]; at != nil {
			at.stopped = true
			at.cancel()
			if at.workload != nil {
				at.workload.Stop()
			}
		}
		e.changedLocked(i)
	}
}

func (e *Execution) finishIfDoneLocked() {
	if e.finished || e.verdict == Undecided {
		return
	}
	for _, at := range e.attempts {
		if at != nil {
			return
		}
	}
	e.finished = true
	if e.hooks.Finished != nil {
		e.hooks.Finished(e.verdict, e.countsLocked())
	}
	close(e.done)
}

func (e *Execution) newAttemptLocked(t *Task) *attempt {
	ctx, cancel := context.WithCancel(context.Background())
	at := &attempt{ctx: ctx, cancel: cancel}
	e.attempts[t.Index] = at
	return at
}

// startAttemptLocked starts the current attempt of running task i.
func (e *Execution) startAttemptLocked(i int) {
	t := &e.tasks[i]
	t.Started = time.Time{}
	at := e.newAttemptLocked(t)
	e.changedLocked(i)
	snapshot := *t
	bg.Go(func() {
		w, err := e.hooks.Start(at.ctx, snapshot)
		e.attemptStarted(i, at, w, err)
	})
}

func (e *Execution) attemptStarted(i int, at *attempt, w Workload, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.attempts[i] != at {
		if w != nil {
			w.Stop()
		}
		return
	}
	if err != nil {
		e.attemptEndedLocked(i, Result{StartErr: err, Stopped: at.stopped, TimedOut: at.taskExpired, Exit: Exit{Code: -1, At: time.Now()}})
		e.advanceLocked()
		return
	}
	if w == nil {
		e.attemptEndedLocked(i, Result{StartErr: errors.New("the workload did not start"), Exit: Exit{Code: -1, At: time.Now()}})
		e.advanceLocked()
		return
	}
	at.workload = w
	t := &e.tasks[i]
	t.Started = time.Now()
	if t.FirstStarted.IsZero() {
		t.FirstStarted = t.Started
	}
	if at.stopped || at.taskExpired {
		w.Stop()
	} else {
		e.armTimersLocked(t, at)
	}
	if t.Status == Running {
		e.changedLocked(i)
	}
	e.watchAttempt(i, at)
}

// armTimersLocked arms the attempt and task timeouts of task t's running
// attempt, counting from when each started.
func (e *Execution) armTimersLocked(t *Task, at *attempt) {
	i := t.Index
	if d := e.spec.AttemptTimeout; d > 0 {
		at.timers = append(at.timers, bg.AfterFunc(max(d-time.Since(t.Started), 0), func() { e.timedOut(i, at, false) }))
	}
	if d := e.spec.TaskTimeout; d > 0 {
		at.timers = append(at.timers, bg.AfterFunc(max(d-time.Since(t.FirstStarted), 0), func() { e.timedOut(i, at, true) }))
	}
}

func (e *Execution) timedOut(i int, at *attempt, task bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.attempts[i] != at || at.stopped {
		return
	}
	at.timedOut = true
	at.taskExpired = at.taskExpired || task
	if at.workload != nil {
		at.workload.Stop()
	}
}

// watchAttempt waits on its own goroutine for the attempt's workload to exit,
// and then takes the execution's lock to record how it ended.
func (e *Execution) watchAttempt(i int, at *attempt) {
	var exit Exit
	bg.WatchThen(func() { exit = at.workload.Wait() }, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.attempts[i] != at {
			return
		}
		if exit.At.IsZero() {
			exit.At = time.Now()
		}
		e.attemptEndedLocked(i, Result{
			Exit:     exit,
			Started:  e.tasks[i].Started,
			TimedOut: at.timedOut,
			Stopped:  at.stopped,
		})
		e.advanceLocked()
	})
}

// attemptEndedLocked records how task i's current attempt ended and moves the
// task on: to Succeeded, to its next attempt, or to Failed. A task the
// execution already ended keeps its status and records the attempt.
func (e *Execution) attemptEndedLocked(i int, r Result) {
	t := &e.tasks[i]
	at := e.attempts[i]
	if at != nil {
		for _, timer := range at.timers {
			timer.Stop()
		}
		at.cancel()
		r.TimedOut = r.TimedOut || at.timedOut
		e.attempts[i] = nil
	}
	t.Last = &r
	if t.Status != Running {
		e.changedLocked(i)
		return
	}
	switch {
	case r.Succeeded():
		t.Status = Succeeded
		t.Ended = r.At
	case e.retries(t, r, at):
		t.Attempt++
		e.startAttemptLocked(i)
		return
	default:
		t.Status = Failed
		t.Ended = r.At
	}
	e.changedLocked(i)
}

func (e *Execution) retries(t *Task, r Result, at *attempt) bool {
	if r.Stopped || t.Attempt >= e.spec.MaxRetries || e.verdict != Undecided {
		return false
	}
	if at != nil && at.taskExpired {
		return false
	}
	if d := e.spec.TaskTimeout; d > 0 && !t.FirstStarted.IsZero() && time.Since(t.FirstStarted) >= d {
		return false
	}
	return e.hooks.Retry == nil || e.hooks.Retry(*t, r)
}
