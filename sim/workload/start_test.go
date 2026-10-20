package workload

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const groupTestImage = "mirror.gcr.io/library/busybox:latest"

func groupTestNetwork(t *testing.T) string {
	t.Helper()
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sim.PullImage(ctx, groupTestImage, "linux/"+runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-group-" + sim.RandomHex(4)
	netID, _, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{}) })
	return network
}

func groupMember(name, script string, dependsOn ...string) Member {
	cfg := sim.ContainerConfig{
		Image:             groupTestImage,
		Name:              "sockerless-sim-test-group-" + name + "-" + sim.RandomHex(4),
		Command:           []string{"/bin/sh", "-c", script},
		Labels:            map[string]string{"sockerless-sim-test-group": "1"},
		CancelGracePeriod: time.Second,
	}
	return Member{Container: Container{Name: name, Config: cfg}, DependsOn: dependsOn}
}

// fileExists is a startup check that passes once the member wrote path.
func fileExists(path string) func(context.Context, placement.Handle, placement.Handle) error {
	return func(ctx context.Context, member, _ placement.Handle) error {
		result, err := member.Exec(ctx, []string{"/bin/sh", "-c", "until [ -e " + path + " ]; do sleep 0.1; done"})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return errors.New(result.Stderr)
		}
		return nil
	}
}

// Members start in the order their dependencies name, share the first
// member's network namespace, and the group returns once every startup check
// passed.
func TestStartGroupStartsMembersInDependencyOrder(t *testing.T) {
	network := groupTestNetwork(t)
	ctx := context.Background()
	main := groupMember("main", "sleep 300", "db")
	db := groupMember("db", "sleep 1; touch /tmp/db-ready; nc -lk -p 5432 -e true")
	db.Startup = fileExists("/tmp/db-ready")
	cache := groupMember("cache", "sleep 300")
	var mu sync.Mutex
	var placed []int
	group, err := StartGroup(ctx, placement.ContainerBackend{}, []Member{main, db, cache}, StartOptions{
		Owner: func(cfg *sim.ContainerConfig) { cfg.Network = network },
		Placed: func(i int, _ placement.Handle) {
			mu.Lock()
			placed = append(placed, i)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		group.Stop(0)
		<-group.Main.Exited()
	})
	if len(group.Sidecars) != 2 {
		t.Fatalf("sidecars %d", len(group.Sidecars))
	}
	mu.Lock()
	defer mu.Unlock()
	if placed[0] != 1 {
		t.Fatalf("the namespace owner is the first member free to start, the database; placed %v", placed)
	}
	position := map[int]int{}
	for at, i := range placed {
		position[i] = at
	}
	if position[0] < position[1] {
		t.Fatalf("main started before the database it depends on: %v", placed)
	}
	// The members share one network namespace: main reaches the database's
	// port on localhost.
	result, err := group.Main.Exec(ctx, []string{"/bin/sh", "-c", "nc -z localhost 5432 && echo reached"})
	if err != nil || !strings.Contains(result.Stdout, "reached") {
		t.Fatalf("main did not reach the database on localhost: %+v %v", result, err)
	}
}

// A member whose startup check fails fails the group, and every member placed
// is cancelled.
func TestStartGroupCancelsEveryMemberWhenAStartupCheckFails(t *testing.T) {
	network := groupTestNetwork(t)
	main := groupMember("main", "sleep 300")
	broken := groupMember("broken", "sleep 300")
	broken.Startup = func(context.Context, placement.Handle, placement.Handle) error { return errors.New("probe refused") }
	_, err := StartGroup(context.Background(), placement.ContainerBackend{}, []Member{main, broken}, StartOptions{
		Owner: func(cfg *sim.ContainerConfig) { cfg.Network = network },
	})
	var startErr *StartError
	if !errors.As(err, &startErr) || !strings.Contains(err.Error(), `container "broken": probe refused`) {
		t.Fatalf("start returned %v", err)
	}
	select {
	case <-startErr.Stopped():
	case <-time.After(60 * time.Second):
		t.Fatal("the placed members never stopped")
	}
}

// A group whose start is cancelled stops what it placed.
func TestStartGroupStopsWhenItsStartIsCancelled(t *testing.T) {
	network := groupTestNetwork(t)
	main := groupMember("main", "sleep 300")
	never := groupMember("never", "sleep 300")
	never.Startup = fileExists("/tmp/never")
	ctx, cancel := context.WithCancel(context.Background())
	placedMembers := make(chan struct{}, 2)
	go func() {
		<-placedMembers
		<-placedMembers
		cancel()
	}()
	_, err := StartGroup(ctx, placement.ContainerBackend{}, []Member{main, never}, StartOptions{
		Owner:  func(cfg *sim.ContainerConfig) { cfg.Network = network },
		Placed: func(int, placement.Handle) { placedMembers <- struct{}{} },
	})
	var startErr *StartError
	if !errors.As(err, &startErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("start returned %v", err)
	}
	<-startErr.Stopped()
}

// Dependencies on no member, and cycles, are refused before anything starts.
func TestStartOrderRefusesUnknownDependenciesAndCycles(t *testing.T) {
	if _, err := StartOrder([]Member{{Container: Container{Name: "a"}, DependsOn: []string{"b"}}}); err == nil {
		t.Fatal("a dependency on no member was accepted")
	}
	cycle := []Member{
		{Container: Container{Name: "a"}, DependsOn: []string{"b"}},
		{Container: Container{Name: "b"}, DependsOn: []string{"a"}},
	}
	if _, err := StartOrder(cycle); err == nil {
		t.Fatal("a cycle was accepted")
	}
	order, err := StartOrder([]Member{
		{Container: Container{Name: "a"}, DependsOn: []string{"c"}},
		{Container: Container{Name: "b"}},
		{Container: Container{Name: "c"}, DependsOn: []string{"b"}},
	})
	if err != nil || len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 0 {
		t.Fatalf("order %v %v", order, err)
	}
}
