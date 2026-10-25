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
// `GET /containers/{id}/stats`. What the accounting carries depends on the
// host's cgroup version:
//
//   - cgroup v1: `usage` (memory.usage_in_bytes) and `max_usage`
//     (memory.max_usage_in_bytes), the kernel's own high-water mark for the
//     cgroup, so any reading carries the peak up to that moment;
//   - cgroup v2: `usage` (memory.current) alone. The Engine API documents
//     max_usage as unset on a cgroup v2 host, and the engine does not serve
//     memory.peak, so the peak is the highest usage any reading saw.
//
// The observer therefore keeps the highest figure of every reading: the
// running maximum of the usage samples and, where the engine keeps one, its
// peak counter. It polls the accounting through the engine's one-shot reading
// rather than holding the endpoint's stream open, because a stream's cadence
// is the engine's and far coarser than the workloads measured here — Podman's
// compatibility endpoint emits one streamed sample roughly every five seconds,
// a single reading for a sub-second AWS Lambda invocation. A caller that
// reports the figure at a moment of its own (the end of an invocation) reads
// once more at that moment, so what the container holds then is counted even
// when the workload finished between two polls.

// memoryPeakSampleInterval is the gap between two readings of a container's
// memory accounting. It is short relative to the shortest workload worth
// measuring (a Lambda invocation, whose whole Invoke phase can be a few hundred
// milliseconds) and long relative to the round trip to the engine, so a
// container that lives at all is read several times.
const memoryPeakSampleInterval = 50 * time.Millisecond

// memoryReadTimeout bounds one on-demand reading of the accounting.
const memoryReadTimeout = 5 * time.Second

// memoryPeakObserver holds the highest memory figure the engine reported for
// one container.
type memoryPeakObserver struct {
	cli         *client.Client
	containerID string

	mu   sync.Mutex
	peak uint64

	stopOnce sync.Once
	stopped  chan struct{}
}

func newMemoryPeakObserver(cli *client.Client, containerID string) *memoryPeakObserver {
	return &memoryPeakObserver{cli: cli, containerID: containerID, stopped: make(chan struct{})}
}

// peakBytes reports the highest memory usage observed, or zero if the engine
// reported none.
func (o *memoryPeakObserver) peakBytes() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.peak
}

// read takes one more reading of a container still observed and reports the
// highest memory usage observed, including that reading.
func (o *memoryPeakObserver) read() uint64 {
	select {
	case <-o.stopped:
	default:
		ctx, cancel := context.WithTimeout(context.Background(), memoryReadTimeout)
		o.sample(ctx)
		cancel()
	}
	return o.peakBytes()
}

// stop ends the observation. It is called when the container exits, before the
// engine is asked to remove it.
func (o *memoryPeakObserver) stop() {
	o.stopOnce.Do(func() { close(o.stopped) })
}

// record takes the highest memory figure one stats response carries.
func (o *memoryPeakObserver) record(stats container.StatsResponse) {
	usage := max(stats.MemoryStats.Usage, stats.MemoryStats.MaxUsage)
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
func (o *memoryPeakObserver) observe(ctx context.Context) {
	ticker := time.NewTicker(memoryPeakSampleInterval)
	defer ticker.Stop()
	for {
		if !o.sample(ctx) {
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
// is still serving the container's accounting. The client asks for the
// engine's one-shot reading, which answers at once instead of waiting a second
// for a previous CPU sample.
func (o *memoryPeakObserver) sample(ctx context.Context) bool {
	result, err := o.cli.ContainerStats(ctx, o.containerID, client.ContainerStatsOptions{})
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
