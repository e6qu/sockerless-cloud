//go:build darwin || linux

package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CreateSnapshot is asynchronous on real EC2: it registers the snapshot, returns
// it as pending with 0% progress, and captures the data behind that while the
// caller polls DescribeSnapshots. The simulator used to copy the whole volume
// inside the handler, which held its HTTP server for the length of the copy --
// about eight minutes for an EDD workspace volume, during which every request
// including /health went unanswered and callers gave up against a simulator that
// looked dead but was only busy.
func TestCreateSnapshotReturnsBeforeCapturingData(t *testing.T) {
	t.Setenv("SIM_EBS_DATA_DIR", t.TempDir())
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")
	ec2Snapshots = sim.MakeStore[EC2Snapshot](nil, "ec2_snapshots")

	// A host-path volume holding a file large enough that a synchronous copy
	// would still be running when the handler returns.
	source := t.TempDir()
	payload := make([]byte, 256*1024*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(source, "blob"), payload, 0o644))
	ec2Volumes.Put("vol-async", EC2Volume{
		VolumeId: "vol-async",
		Size:     8,
		State:    "available",
		HostPath: source,
	})

	req := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{
		"Action":   {"CreateSnapshot"},
		"VolumeId": {"vol-async"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handleCreateSnapshot(rec, req)
	// Sampled the instant the handler returns. A synchronous copy would have
	// finished the whole 256 MiB before responding, so an equal-sized
	// destination here is the signature of the bug this guards against.
	returnedAt := ec2SnapshotDirSize(t, ec2SnapshotHostDirPathForResponse(rec.Body.String()))

	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<status>pending</status>",
		"CreateSnapshot must return the snapshot as pending, not after the copy finished")

	start := strings.Index(body, "<snapshotId>")
	require.Greater(t, start, -1, body)
	snapshotID := body[start+len("<snapshotId>"):]
	snapshotID = snapshotID[:strings.Index(snapshotID, "</snapshotId>")]

	stored, ok := ec2Snapshots.Get(snapshotID)
	require.True(t, ok)
	require.Equal(t, "pending", stored.State)
	require.Less(t, returnedAt, int64(len(payload)),
		"CreateSnapshot returned only after copying the volume; the copy belongs behind the response")

	// A lazy settle must not call it completed while bytes are still moving:
	// a restore from a half-written snapshot would silently lose data.
	ec2SettleSnapshot(snapshotID)
	settled, ok := ec2Snapshots.Get(snapshotID)
	require.True(t, ok)
	require.NotEqual(t, "completed", settled.State,
		"a snapshot must not report completed before its data is captured")

	// The capture goroutine completes it once the data is actually there.
	deadline := time.Now().Add(60 * time.Second)
	for {
		current, ok := ec2Snapshots.Get(snapshotID)
		require.True(t, ok)
		if current.State == "completed" {
			require.Equal(t, "100%", current.Progress)
			break
		}
		require.NotEqual(t, "error", current.State, "capture failed")
		require.False(t, time.Now().After(deadline), "snapshot never completed")
		time.Sleep(100 * time.Millisecond)
	}

	copied, err := os.ReadFile(filepath.Join(stored.HostPath, "blob"))
	require.NoError(t, err)
	require.Equal(t, len(payload), len(copied))
}

// ec2SnapshotHostDirPathForResponse pulls the snapshot id out of a
// CreateSnapshot response and returns where its host-path data lands.
func ec2SnapshotHostDirPathForResponse(body string) string {
	start := strings.Index(body, "<snapshotId>")
	if start < 0 {
		return ""
	}
	rest := body[start+len("<snapshotId>"):]
	end := strings.Index(rest, "</snapshotId>")
	if end < 0 {
		return ""
	}
	return ebsSnapshotHostDirPath(rest[:end])
}

// ec2SnapshotDirSize totals the bytes present under dir, treating a missing
// directory as zero: before the capture goroutine runs there is nothing there.
func ec2SnapshotDirSize(t *testing.T, dir string) int64 {
	t.Helper()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}
