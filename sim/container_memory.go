package sim

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Measuring what a container's memory actually reached.
//
// A cloud product that reports a workload's memory consumption has to read it
// from the thing that ran the workload. The container engine accounts for a
// running container's memory in its cgroup and serves that accounting on
// `GET /containers/{id}/stats`, whose memory_stats carries the current usage
// and — on a cgroup v1 host, where the kernel keeps the counter — the maximum
// ever recorded ("maximum usage ever recorded", moby's own field
// documentation for max_usage). On a cgroup v2 host the kernel's peak counter
// is not part of that accounting, and engines differ in what they fill in, so
// the observer takes the highest of every memory figure the engine reports over
// the container's life: the running maximum of the usage samples, and the
// engine's own peak counter where it keeps one. Both are the engine's numbers.
//
// The samples are taken by polling rather than by holding the endpoint's
// stream open, because a stream's cadence is the engine's and is far coarser
// than the workloads measured here — Podman's compatibility endpoint emits one
// streamed sample roughly every five seconds, which for a sub-second AWS Lambda
// invocation is a single reading taken before the handler has allocated
// anything. Polling reads the same accounting at a rate the measurement needs.

// memoryPeakSampleInterval is the gap between two readings of a container's
// memory accounting. It is short relative to the shortest workload worth
// measuring (a Lambda invocation, whose whole Invoke phase can be a few hundred
// milliseconds) and long relative to the round trip to the engine, so a
// container that lives at all is read several times.
const memoryPeakSampleInterval = 50 * time.Millisecond

// memoryPeakObserver holds the highest memory figure the engine reported for
// one container.
type memoryPeakObserver struct {
	mu   sync.Mutex
	peak uint64

	stopOnce sync.Once
	stopped  chan struct{}
}

func newMemoryPeakObserver() *memoryPeakObserver {
	return &memoryPeakObserver{stopped: make(chan struct{})}
}

// peakBytes reports the highest memory usage observed, or zero if the engine
// reported none.
func (o *memoryPeakObserver) peakBytes() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.peak
}

// stop ends the observation. It is called when the container exits, before the
// engine is asked to remove it.
func (o *memoryPeakObserver) stop() {
	o.stopOnce.Do(func() { close(o.stopped) })
}

// record takes the highest memory figure one stats response carries.
func (o *memoryPeakObserver) record(stats container.StatsResponse) {
	usage := stats.MemoryStats.Usage
	if stats.MemoryStats.MaxUsage > usage {
		usage = stats.MemoryStats.MaxUsage
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if usage > o.peak {
		o.peak = usage
	}
}

// observe reads the container's memory accounting from the engine until the
// container exits, the caller's context ends, or the engine stops serving the
// accounting — which is what it does for a container it is no longer running,
// and therefore the natural end of the observation.
func (o *memoryPeakObserver) observe(ctx context.Context, cli *client.Client, containerID string) {
	ticker := time.NewTicker(memoryPeakSampleInterval)
	defer ticker.Stop()
	for {
		if !o.sample(ctx, cli, containerID) {
			return
		}
		select {
		case <-o.stopped:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// sample reads one stats response and records it, reporting whether the engine
// is still serving the container's accounting.
func (o *memoryPeakObserver) sample(ctx context.Context, cli *client.Client, containerID string) bool {
	result, err := cli.ContainerStats(ctx, containerID, client.ContainerStatsOptions{})
	if err != nil {
		return false
	}
	defer func() { _ = result.Body.Close() }()
	var stats container.StatsResponse
	if err := json.NewDecoder(result.Body).Decode(&stats); err != nil {
		return false
	}
	o.record(stats)
	return true
}
