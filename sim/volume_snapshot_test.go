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
	writer, err := StartContainerSync(ContainerConfig{
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
	reader, err := StartContainerSync(ContainerConfig{
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
