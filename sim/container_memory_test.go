package sim

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
)

// memoryPeakTestImage is the image the memory-observation tests run, from
// mirror.gcr.io so a throttled Docker Hub cannot break them.
const memoryPeakTestImage = "mirror.gcr.io/library/alpine:3.22"

// memoryHeldMarker is the line a test container prints once it holds what it
// was asked to hold.
const memoryHeldMarker = "holding"

// readRunningContainerMemoryPeak runs a container that executes script, prints
// memoryHeldMarker and then keeps running, and returns the peak its handle
// reports once the marker arrived, while the container still runs.
func readRunningContainerMemoryPeak(t *testing.T, name, script string) uint64 {
	t.Helper()
	if _, err := InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	handle, err := StartContainerSyncContext(t.Context(), ContainerConfig{
		Image:           memoryPeakTestImage,
		Architecture:    "linux/" + runtime.GOARCH,
		Command:         []string{"sh"},
		Args:            []string{"-c", script + " && echo " + memoryHeldMarker + " && exec sleep 300"},
		Name:            fmt.Sprintf("sockerless-sim-memory-peak-%s-%d", name, time.Now().UnixNano()),
		Timeout:         2 * time.Minute,
		MemoryBytes:     256 * 1024 * 1024,
		TrackMemoryPeak: true,
	}, FuncSink(func(line LogLine) {
		if line.Text == memoryHeldMarker {
			close(held)
		}
	}))
	if err != nil {
		t.Fatalf("start %s container: %v", name, err)
	}
	defer func() {
		handle.Cancel()
		handle.Wait()
	}()
	select {
	case <-held:
	case <-handle.Exited():
		t.Fatalf("%s container exited before it held its memory: %+v", name, handle.Wait())
	}
	return handle.MemoryPeakBytes()
}

// The observed peak is the engine's accounting of the container that ran: a
// container holding tens of megabytes reports them, and one holding nothing
// reports far less. Both figures are read the moment the container says it
// holds its memory, so the reading at the call, not a poll that happened to
// land after the allocation, has to carry them.
func TestContainerMemoryPeakMeasuresTheContainer(t *testing.T) {
	const heldMegabytes = 48
	// The script writes the pages into the container's own shared-memory
	// filesystem and holds them there for the rest of the run.
	held := readRunningContainerMemoryPeak(t, "holding", fmt.Sprintf(
		"dd if=/dev/zero of=/dev/shm/held bs=1M count=%d >/dev/null 2>&1", heldMegabytes))
	idle := readRunningContainerMemoryPeak(t, "idle", "true")

	t.Logf("engine-reported memory peak: holding %d bytes, idle %d bytes", held, idle)

	if idle == 0 {
		t.Fatal("the engine reported no memory accounting for a running container")
	}
	const megabyte = 1024 * 1024
	if held < (heldMegabytes-8)*megabyte {
		t.Fatalf("a container holding %d MB reported a peak of %d bytes", heldMegabytes, held)
	}
	if held <= idle+(heldMegabytes/2)*megabyte {
		t.Fatalf("holding %d MB must show against an idle container: holding %d bytes, idle %d bytes",
			heldMegabytes, held, idle)
	}
}

// The engine's memory accounting differs by the host's cgroup version, and the
// peak is read from either. These are the memory_stats the Docker Engine
// serves on each: on cgroup v1 it carries the kernel's high-water mark
// (max_usage, memory.max_usage_in_bytes) beside the current usage; on cgroup
// v2 it carries memory.current as usage and no max_usage at all, with
// memory.stat's v2 keys.
func TestMemoryPeakObserverReadsBothCgroupLayouts(t *testing.T) {
	const cgroupV1 = `{"memory_stats":{"usage":1454080,"max_usage":4882432,"limit":16872804352,
		"stats":{"active_anon":225280,"cache":0,"rss":409600,"total_rss":409600,"total_inactive_file":0}}}`
	const cgroupV2 = `{"memory_stats":{"usage":%d,"limit":2147483648,
		"stats":{"anon":%d,"file":0,"kernel":561152,"inactive_file":0,"active_anon":0}}}`

	decode := func(t *testing.T, body string) container.StatsResponse {
		t.Helper()
		var stats container.StatsResponse
		if err := json.Unmarshal([]byte(body), &stats); err != nil {
			t.Fatal(err)
		}
		return stats
	}

	t.Run("cgroup v1 reports the kernel's high-water mark", func(t *testing.T) {
		o := newMemoryPeakObserver(nil, "")
		o.record(decode(t, cgroupV1))
		if got := o.peakBytes(); got != 4882432 {
			t.Fatalf("peak %d, want max_usage 4882432 above the current usage", got)
		}
	})

	t.Run("cgroup v2 reports the highest usage read", func(t *testing.T) {
		o := newMemoryPeakObserver(nil, "")
		const before, holding, after = 14 << 20, 402 << 20, 380 << 20
		for _, usage := range []uint64{before, holding, after} {
			o.record(decode(t, fmt.Sprintf(cgroupV2, usage, usage-(4<<20))))
		}
		if got := o.peakBytes(); got != holding {
			t.Fatalf("peak %d, want the highest usage read, %d", got, holding)
		}
	})

	t.Run("a later reading below the peak keeps it", func(t *testing.T) {
		o := newMemoryPeakObserver(nil, "")
		o.record(decode(t, cgroupV1))
		o.record(decode(t, fmt.Sprintf(cgroupV2, 1<<20, 1<<20)))
		if got := o.peakBytes(); got != 4882432 {
			t.Fatalf("peak %d fell below the earlier high-water mark 4882432", got)
		}
	})
}

// A container started without the observation reports no peak, so a caller
// cannot mistake an unmeasured container for one that used nothing.
func TestContainerMemoryPeakIsZeroWithoutObservation(t *testing.T) {
	if _, err := InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	handle, err := StartContainerSyncContext(t.Context(), ContainerConfig{
		Image:        memoryPeakTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh"},
		Args:         []string{"-c", "true"},
		Name:         fmt.Sprintf("sockerless-sim-memory-peak-unobserved-%d", time.Now().UnixNano()),
		Timeout:      2 * time.Minute,
	}, FuncSink(func(LogLine) {}))
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	handle.Wait()
	if peak := handle.MemoryPeakBytes(); peak != 0 {
		t.Fatalf("an unobserved container must report no peak, got %d bytes", peak)
	}
}
