package bg

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestAwaitDrainsStartedWorkAndDropsWorkRequestedMidDrain(t *testing.T) {
	Await()
	var completed atomic.Int32
	release := make(chan struct{})
	Go(func() {
		<-release
		completed.Add(1)
	})

	drained := make(chan struct{})
	go func() {
		Await()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("the drain returned while started work was still running")
	case <-time.After(50 * time.Millisecond):
	}

	var admittedMidDrain atomic.Int32
	Go(func() { admittedMidDrain.Add(1) })

	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain did not return after the started work completed")
	}
	if completed.Load() != 1 {
		t.Fatalf("started work must complete inside the drain; completed=%d", completed.Load())
	}
	if admittedMidDrain.Load() != 0 {
		t.Fatalf("work requested mid-drain must be dropped, not run; ran=%d", admittedMidDrain.Load())
	}

	post := make(chan struct{})
	Go(func() { close(post) })
	select {
	case <-post:
	case <-time.After(5 * time.Second):
		t.Fatal("work requested after the drain must run")
	}
	Await()
}

func TestJoinedGoRunsDuringADrain(t *testing.T) {
	Await()
	draining.Store(true)
	ran := make(chan struct{})
	JoinedGo(func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("JoinedGo dropped work its caller waits for")
	}
	draining.Store(false)
	Await()
}

// A drain must see work handed to the server's own lifecycle, or it returns
// while that work still reads the stores the next test replaces.
func TestAwaitDrainsServerLifecycleWork(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "bg-test", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new sim server: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	var finished atomic.Bool
	started := make(chan struct{})
	run, ok := Handoff(func() {
		close(started)
		time.Sleep(250 * time.Millisecond)
		finished.Store(true)
	})
	if !ok {
		t.Fatal("work offered outside a drain was refused")
	}
	srv.StartBackground("test lifecycle step", func(context.Context) { run() })
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the background worker never started")
	}

	Await()

	if !finished.Load() {
		t.Fatal("Await returned while work on the server's lifecycle was still running")
	}
}

func TestHandoffRefusesWorkOnceTheDrainHasBegun(t *testing.T) {
	Await()
	var ran atomic.Bool
	draining.Store(true)
	if run, ok := Handoff(func() { ran.Store(true) }); ok {
		run()
	}
	draining.Store(false)
	if ran.Load() {
		t.Fatal("Handoff admitted work offered during a drain")
	}
}

func TestAwaitWaitsForHandedOffWorkNotYetStarted(t *testing.T) {
	Await()
	var ran atomic.Bool
	run, ok := Handoff(func() { ran.Store(true) })
	if !ok {
		t.Fatal("work offered outside a drain was refused")
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		Await()
	}()
	select {
	case <-drained:
		t.Fatal("Await returned while handed-off work had not started")
	case <-time.After(250 * time.Millisecond):
	}

	run()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("the drain did not complete once the handed-off work ran")
	}
	if ran.Load() {
		t.Fatal("handed-off work that only started during a drain ran")
	}
}

func TestAwaitCancelsATimerThatHasNotFired(t *testing.T) {
	Await()
	var ran atomic.Bool
	AfterFunc(time.Hour, func() { ran.Store(true) })

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		Await()
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("Await waited out a pending timer instead of cancelling it")
	}
	if ran.Load() {
		t.Fatal("a cancelled timer ran")
	}

	fired := make(chan struct{})
	timer := AfterFunc(0, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("a timer armed outside a drain never fired")
	}
	if timer.Stop() {
		t.Fatal("Stop reported stopping a timer that had fired")
	}
	Await()
}

func TestAwaitDetachesAWatchStillWaiting(t *testing.T) {
	Await()
	event := make(chan struct{})
	var ran atomic.Bool
	watch := WatchThen(func() { <-event }, func() { ran.Store(true) })
	if watch == nil {
		t.Fatal("a watch armed outside a drain was refused")
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		Await()
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("Await waited on a watch whose event never arrived")
	}

	close(event)
	select {
	case <-watch.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a detached watch's goroutine did not return once its event arrived")
	}
	if ran.Load() {
		t.Fatal("a watch detached by a drain ran its work when its event arrived")
	}
}

func TestAwaitWaitsForAWatchWhoseEventArrived(t *testing.T) {
	Await()
	started := make(chan struct{})
	var finished atomic.Bool
	WatchThen(func() {}, func() {
		close(started)
		time.Sleep(250 * time.Millisecond)
		finished.Store(true)
	})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("a watch whose wait returned never ran its work")
	}

	Await()

	if !finished.Load() {
		t.Fatal("Await returned while a watch's work was still running")
	}
}

func TestAwaitStopsATimerOrWatchArmedAsTheDrainBegins(t *testing.T) {
	Await()
	never := make(chan struct{})
	defer close(never)
	for range 2000 {
		start := make(chan struct{})
		armed := make(chan struct{})
		go func() {
			<-start
			AfterFunc(time.Hour, func() {})
			WatchThen(func() { <-never }, func() {})
			close(armed)
		}()
		drained := make(chan struct{})
		go func() {
			<-start
			Await()
			close(drained)
		}()
		close(start)
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Fatal("the drain waited on a timer or watch armed as it began")
		}
		<-armed
		Await()
	}
}
