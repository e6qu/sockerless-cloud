package simulator

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// memoryPeakTestImage is the image the memory-observation tests run, from the
// Amazon ECR Public Gallery so a throttled Docker Hub cannot break them.
const memoryPeakTestImage = "public.ecr.aws/docker/library/alpine:3.22"

// startMemoryPeakTestContainer runs one container to completion with the
// engine's memory accounting observed, and returns the peak it reported.
func startMemoryPeakTestContainer(t *testing.T, name, script string) uint64 {
	t.Helper()
	InitDocker("aws", true, t.TempDir())
	handle, err := StartContainerSync(ContainerConfig{
		Image:        memoryPeakTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh"},
		Args:         []string{"-c", script},
		Name:         fmt.Sprintf("sockerless-sim-memory-peak-%s-%d", name, time.Now().UnixNano()),
		Timeout:      2 * time.Minute,
		MemoryBytes:  256 * 1024 * 1024,
		// The measurement under test.
		TrackMemoryPeak: true,
	}, FuncSink(func(LogLine) {}))
	if err != nil {
		t.Fatalf("start %s container: %v", name, err)
	}
	if result := handle.Wait(); result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("%s container failed: exit %d, %v", name, result.ExitCode, result.Error)
	}
	return handle.MemoryPeakBytes()
}

// The observed peak is the engine's accounting of the container that ran: a
// container holding tens of megabytes reports them, and one holding nothing
// reports far less. Both figures come from the engine, so the difference
// between them is the difference in what the two containers did.
func TestContainerMemoryPeakMeasuresTheContainer(t *testing.T) {
	const heldMegabytes = 48
	// The pages are written into the container's own shared-memory filesystem
	// and held there for the rest of the run, so the engine accounts for them
	// throughout rather than in one instant a sample might fall between.
	held := startMemoryPeakTestContainer(t, "holding", fmt.Sprintf(
		"dd if=/dev/zero of=/dev/shm/held bs=1M count=%d >/dev/null 2>&1 && sleep 1", heldMegabytes))
	idle := startMemoryPeakTestContainer(t, "idle", "sleep 1")

	t.Logf("engine-reported memory peak: holding %d bytes, idle %d bytes", held, idle)

	if idle == 0 {
		t.Fatal("the engine reported no memory accounting for a container that ran for a second")
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

// A container started without the observation reports no peak, so a caller
// cannot mistake an unmeasured container for one that used nothing.
func TestContainerMemoryPeakIsZeroWithoutObservation(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	handle, err := StartContainerSync(ContainerConfig{
		Image:        memoryPeakTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh"},
		Args:         []string{"-c", "sleep 1"},
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
