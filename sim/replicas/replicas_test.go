package replicas

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

const testImage = "mirror.gcr.io/library/busybox:latest"

type recorder struct {
	mu     sync.Mutex
	events []string
	seen   chan string
}

func (r *recorder) add(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	r.seen <- event
}

// await returns once event was recorded, failing the test after a minute.
func (r *recorder) await(t *testing.T, event string) {
	t.Helper()
	deadline := time.After(time.Minute)
	for {
		select {
		case got := <-r.seen:
			if got == event {
				return
			}
		case <-deadline:
			t.Fatalf("no %q event; saw %v", event, r.events)
		}
	}
}

func (r *recorder) hooks() Hooks {
	return Hooks{
		Started: func(*Replica) { r.add("started") },
		Exited:  func(_ *Replica, result sim.ProcessResult) { r.add("exited") },
		Stopped: func(*Replica) { r.add("stopped") },
	}
}

// newSet is a set of busybox replicas running script on a network of the
// test's own.
func newSet(t *testing.T, script string, restart Restart, hooks Hooks) *Set {
	t.Helper()
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sim.PullImage(ctx, testImage, "linux/"+runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-replicas-" + sim.RandomHex(4)
	netID, _, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	set := New(Spec{
		NewID:   func() string { return "replica-" + sim.RandomHex(4) },
		Restart: restart,
		Start: func(ctx context.Context, id string) (*workload.Group, func(), error) {
			group, err := workload.StartGroup(ctx, placement.ContainerBackend{}, []workload.Member{{
				Container: workload.Container{Name: "main", Config: sim.ContainerConfig{
					Image:             testImage,
					Name:              "sockerless-sim-test-" + id,
					Command:           []string{"/bin/sh", "-c", script},
					Labels:            map[string]string{"sockerless-sim-test-replica": id},
					CancelGracePeriod: time.Second,
				}},
			}}, workload.StartOptions{Owner: func(cfg *sim.ContainerConfig) { cfg.Network = network }})
			return group, nil, err
		},
	}, hooks)
	t.Cleanup(func() {
		_ = set.Stop(context.Background())
		_, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{})
	})
	return set
}

// The set starts replicas up to the count, picks them in turn, and stops the
// newest when the count falls.
func TestScaleStartsAndStopsReplicas(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	set := newSet(t, "sleep 300", Always, rec.hooks())
	if err := set.Scale(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	running := set.Replicas()
	if len(running) != 2 {
		t.Fatalf("%d replicas run, want 2", len(running))
	}
	first, _, _ := set.Pick()
	second, _, _ := set.Pick()
	if first == second {
		t.Fatal("the set picked one replica twice in a row")
	}
	newest := running[1]
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	rec.await(t, "stopped")
	if left := set.Replicas(); len(left) != 1 || left[0] == newest {
		t.Fatalf("scale down kept %v", left)
	}
	select {
	case <-newest.Done():
	default:
		t.Fatal("the stopped replica is still running")
	}
}

// A replica that exits on its own is replaced while the restart policy
// allows it.
func TestAReplicaThatExitsIsReplaced(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	set := newSet(t, "sleep 1; exit 3", OnFailure, rec.hooks())
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	rec.await(t, "started")
	rec.await(t, "exited")
	rec.await(t, "started")
}

// A replica that keeps exiting is replaced after the backoff its consecutive
// exits call for.
func TestConsecutiveExitsBackOff(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	set := newSet(t, "exit 1", Always, rec.hooks())
	var mu sync.Mutex
	var asked []int
	set.spec.Settled = time.Hour
	set.spec.Backoff = func(consecutive int) time.Duration {
		mu.Lock()
		asked = append(asked, consecutive)
		mu.Unlock()
		return 0
	}
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	rec.await(t, "exited")
	rec.await(t, "exited")
	rec.await(t, "exited")
	// The set asks for the third backoff before it starts the replacement.
	rec.await(t, "started")
	mu.Lock()
	defer mu.Unlock()
	if len(asked) < 3 || asked[0] != 1 || asked[1] != 2 || asked[2] != 3 {
		t.Fatalf("backoff asked for %v, want 1, 2, 3", asked)
	}
}

// A replica that exits on its own under Never stays gone.
func TestNeverLeavesAnExitedReplicaGone(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	set := newSet(t, "exit 0", Never, rec.hooks())
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	rec.await(t, "exited")
	if left := set.Replicas(); len(left) != 0 {
		t.Fatalf("an exited replica is still held: %v", left)
	}
}

// A stopped set stops its replicas and starts none after.
func TestStopEndsTheSet(t *testing.T) {
	set := newSet(t, "sleep 300", Always, Hooks{})
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	held := set.Replicas()[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := set.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held.Done():
	default:
		t.Fatal("Stop returned with a replica running")
	}
	if err := set.Scale(context.Background(), 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("scale after stop returned %v", err)
	}
}

// An adopted replica is the set's own: its exit is replaced like any other.
func TestAnAdoptedReplicaIsWatched(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	set := newSet(t, "sleep 300", Always, rec.hooks())
	group, release, err := set.spec.Start(context.Background(), "adopted")
	if err != nil {
		t.Fatal(err)
	}
	adopted := set.Adopt("adopted", group, time.Now(), release)
	group.Main.Cancel()
	rec.await(t, "exited")
	rec.await(t, "started")
	if running := set.Replicas(); len(running) != 1 || running[0] == adopted {
		t.Fatalf("the adopted replica was not replaced: %v", running)
	}
}

// A replica that restarts in place keeps its ID and its place in the set,
// and counts its restarts.
func TestAReplicaRestartsInPlace(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	set := newSet(t, "sleep 1; exit 2", Always, rec.hooks())
	set.spec.InPlace = true
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	first := set.Replicas()[0]
	rec.await(t, "exited")
	rec.await(t, "started")
	held := set.Replicas()
	if len(held) != 1 || held[0] != first {
		t.Fatalf("the replica was replaced instead of restarted: %v", held)
	}
	if first.Restarts() < 1 {
		t.Fatalf("restarts %d", first.Restarts())
	}
	if _, running := first.Group(); !running {
		t.Fatal("the restarted replica has no running containers")
	}
}

// A replacement that fails to start is tried again after the backoff.
func TestAReplacementThatFailsToStartIsRetried(t *testing.T) {
	rec := &recorder{seen: make(chan string, 16)}
	hooks := rec.hooks()
	hooks.Failed = func(string, error) { rec.add("failed") }
	set := newSet(t, "sleep 1; exit 1", Always, hooks)
	start := set.spec.Start
	var mu sync.Mutex
	starts := 0
	set.spec.Start = func(ctx context.Context, id string) (*workload.Group, func(), error) {
		mu.Lock()
		starts++
		n := starts
		mu.Unlock()
		if n == 2 {
			return nil, nil, errors.New("the image is not there yet")
		}
		return start(ctx, id)
	}
	if err := set.Scale(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	rec.await(t, "exited")
	rec.await(t, "failed")
	rec.await(t, "started")
}
