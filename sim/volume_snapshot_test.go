package sim

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const volumeSnapshotTestImage = "public.ecr.aws/docker/library/alpine:3.22"

const volumeSnapshotTestFiles = 64

// roundSink records the last round the writer reported and signals each
// report, so the test waits on the writer's own progress.
type roundSink struct {
	mu     sync.Mutex
	round  int
	signal chan struct{}
}

func (s *roundSink) WriteLog(line LogLine) {
	round, err := strconv.Atoi(strings.TrimSpace(line.Text))
	if err != nil {
		return
	}
	s.mu.Lock()
	s.round = round
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *roundSink) awaitRound(ctx context.Context, t *testing.T, above int) int {
	t.Helper()
	for {
		s.mu.Lock()
		round := s.round
		s.mu.Unlock()
		if round > above {
			return round
		}
		select {
		case <-s.signal:
		case <-ctx.Done():
			t.Fatalf("the writer reported no round above %d: %v", above, ctx.Err())
		}
	}
}

// A capture of a volume its writer keeps writing holds one point in time.
//
// The writer replaces files f0..f63 in order with the round number, each by an
// atomic rename, and reports every finished round. At any single instant the
// files read r+1 up to some index and r after it. A copy that walked the files
// while the writer ran would mix rounds out of that shape; a capture taken
// with the writer frozen cannot. Two captures run at once, so the freeze each
// holds must survive the other's thaw, and the writer must resume after both.
func TestSnapshotVolumeCapturesOnePointInTimeOfARunningWriter(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	source := "sockerless-sim-snapshot-source-" + suffix
	captures := []string{"sockerless-sim-snapshot-capture-a-" + suffix, "sockerless-sim-snapshot-capture-b-" + suffix}
	t.Cleanup(func() {
		for _, volume := range append([]string{source}, captures...) {
			RemoveVolumeSettled(volume, "test")
		}
	})

	sink := &roundSink{signal: make(chan struct{}, 1)}
	writer, err := StartContainerSyncContext(t.Context(), ContainerConfig{
		Image:        volumeSnapshotTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh"},
		Args: []string{"-c", fmt.Sprintf(`i=0
while :; do
  i=$((i+1))
  k=0
  while [ $k -lt %d ]; do
    echo $i > /data/.next
    mv /data/.next /data/f$k
    k=$((k+1))
  done
  echo $i
done`, volumeSnapshotTestFiles)},
		Name:              "sockerless-sim-snapshot-writer-" + suffix,
		Timeout:           5 * time.Minute,
		CancelGracePeriod: time.Second,
		Binds:             []string{source + ":/data"},
	}, sink)
	if err != nil {
		t.Fatalf("start writer: %v", err)
	}
	t.Cleanup(func() {
		writer.Cancel()
		_ = writer.Wait()
	})
	sink.awaitRound(ctx, t, 1)

	var wg sync.WaitGroup
	errs := make([]error, len(captures))
	for i, capture := range captures {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = SnapshotVolume(ctx, source, capture)
		}()
	}
	wg.Wait()
	captured := 0
	for i, capture := range captures {
		if errs[i] != nil {
			t.Fatalf("capture %s: %v", capture, errs[i])
		}
		values := readVolumeSnapshotTestFiles(t, capture)
		high, low := values[0], values[len(values)-1]
		if high-low > 1 {
			t.Fatalf("capture %s mixes rounds %d and %d: %v", capture, low, high, values)
		}
		for k := 1; k < len(values); k++ {
			if values[k] > values[k-1] {
				t.Fatalf("capture %s holds f%d at round %d after f%d at round %d, which no instant of the writer has: %v",
					capture, k, values[k], k-1, values[k-1], values)
			}
		}
		captured = max(captured, high)
	}

	sink.awaitRound(ctx, t, captured+1)
}

func readVolumeSnapshotTestFiles(t *testing.T, volume string) []int {
	t.Helper()
	var output volumeSnapshotSink
	reader, err := StartContainerSyncContext(t.Context(), ContainerConfig{
		Image:        volumeSnapshotTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh"},
		Args:         []string{"-c", fmt.Sprintf(`k=0; while [ $k -lt %d ]; do cat /data/f$k; k=$((k+1)); done`, volumeSnapshotTestFiles)},
		Name:         "sockerless-sim-snapshot-reader-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Timeout:      2 * time.Minute,
		Binds:        []string{volume + ":/data:ro"},
	}, &output)
	if err != nil {
		t.Fatalf("start reader of %s: %v", volume, err)
	}
	if result := reader.Wait(); result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("read %s: exit %d, %v: %s", volume, result.ExitCode, result.Error, output.String())
	}
	fields := strings.Fields(output.String())
	if len(fields) != volumeSnapshotTestFiles {
		t.Fatalf("capture %s holds %d of %d files: %q", volume, len(fields), volumeSnapshotTestFiles, output.String())
	}
	values := make([]int, len(fields))
	for k, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("capture %s holds f%d = %q", volume, k, field)
		}
		values[k] = value
	}
	return values
}

// A command into a container a capture holds frozen waits for the thaw and
// then runs, and a capture waits out a command that holds the container
// thawed.
func TestHoldThawedWaitsOutAFreeze(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine, err := StartContainerSyncContext(t.Context(), ContainerConfig{
		Image:             volumeSnapshotTestImage,
		Architecture:      "linux/" + runtime.GOARCH,
		Command:           []string{"sleep"},
		Args:              []string{"600"},
		Name:              "sockerless-sim-thaw-engine-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Timeout:           5 * time.Minute,
		CancelGracePeriod: time.Second,
	}, &volumeSnapshotSink{})
	if err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() {
		engine.Cancel()
		_ = engine.Wait()
	})
	id := engine.ContainerID

	if err := holdVolumeFreeze(ctx, id); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if _, err := dockerClient.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: []string{"true"}}); err == nil {
		t.Fatal("the Docker Engine created an exec in a paused container")
	}
	expired, expire := context.WithCancel(ctx)
	expire()
	if _, err := HoldThawed(expired, id); err == nil {
		t.Fatal("HoldThawed returned while a capture held the container frozen")
	}

	type outcome struct {
		paused bool
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		release, err := HoldThawed(ctx, id)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer release()
		inspected, err := dockerClient.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			done <- outcome{err: err}
			return
		}
		_, err = dockerClient.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: []string{"true"}})
		done <- outcome{paused: inspected.Container.State.Paused, err: err}
	}()
	if err := releaseVolumeFreeze(id); err != nil {
		t.Fatalf("thaw: %v", err)
	}
	result := <-done
	if result.err != nil || result.paused {
		t.Fatalf("command after the thaw: paused %v, err %v", result.paused, result.err)
	}

	release, err := HoldThawed(ctx, id)
	if err != nil {
		t.Fatalf("hold thawed: %v", err)
	}
	frozen := make(chan error, 1)
	go func() { frozen <- holdVolumeFreeze(ctx, id) }()
	inspected, err := dockerClient.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspected.Container.State.Paused {
		t.Fatal("a capture froze a container a command held thawed")
	}
	release()
	if err := <-frozen; err != nil {
		t.Fatalf("freeze after the command: %v", err)
	}
	if err := releaseVolumeFreeze(id); err != nil {
		t.Fatalf("thaw: %v", err)
	}
}
