package sim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
)

// writeFrame appends one frame of Docker's multiplexed log stream: the
// stream byte, three zero bytes, the big-endian payload length, the payload.
func writeFrame(w *bytes.Buffer, stream stdcopy.StdType, payload string) {
	header := make([]byte, 8)
	header[0] = byte(stream)
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	w.Write(header)
	w.WriteString(payload)
}

type recordingSink struct {
	mu    sync.Mutex
	lines []LogLine
}

func (s *recordingSink) WriteLog(line LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

// failingAfter serves its frames, then fails the read the way a dropped
// engine connection does.
type failingAfter struct {
	frames io.Reader
	err    error
}

func (r *failingAfter) Read(p []byte) (int, error) {
	n, err := r.frames.Read(p)
	if err == io.EOF {
		return n, r.err
	}
	return n, err
}

func (r *failingAfter) Close() error { return nil }

func TestStreamDockerLogsDeliversWhatItReadAndReportsTheFailure(t *testing.T) {
	t.Parallel()
	var frames bytes.Buffer
	long := strings.Repeat("x", 100*1024)
	writeFrame(&frames, stdcopy.Stdout, "2026-01-02T03:04:05.000000000Z first\n")
	writeFrame(&frames, stdcopy.Stdout, "2026-01-02T03:04:06.000000000Z "+long+"\n")
	writeFrame(&frames, stdcopy.Stderr, "2026-01-02T03:04:07.000000000Z oops\n")

	broken := errors.New("connection reset by peer")
	sink := &recordingSink{}
	err := streamDockerLogs(&failingAfter{frames: &frames, err: broken}, sink)
	if !errors.Is(err, broken) {
		t.Fatalf("streamDockerLogs returned %v, want the read failure", err)
	}

	got := map[string][]string{}
	for _, line := range sink.lines {
		got[line.Stream] = append(got[line.Stream], line.Text)
	}
	if len(got["stdout"]) != 2 || got["stdout"][0] != "first" || got["stdout"][1] != long {
		t.Fatalf("stdout lines = %d %q…, want first and the 100 KiB line intact", len(got["stdout"]), got["stdout"])
	}
	if len(got["stderr"]) != 1 || got["stderr"][0] != "oops" {
		t.Fatalf("stderr lines = %q, want oops", got["stderr"])
	}
}

func TestStreamDockerLogsEndsCleanlyOnEOF(t *testing.T) {
	t.Parallel()
	var frames bytes.Buffer
	writeFrame(&frames, stdcopy.Stdout, "2026-01-02T03:04:05.000000000Z only\n")
	sink := &recordingSink{}
	if err := streamDockerLogs(io.NopCloser(&frames), sink); err != nil {
		t.Fatalf("streamDockerLogs: %v", err)
	}
	if len(sink.lines) != 1 || sink.lines[0].Text != "only" {
		t.Fatalf("lines = %+v", sink.lines)
	}
}
