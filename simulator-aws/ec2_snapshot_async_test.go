//go:build darwin || linux

package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
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
	bg.Await()
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")
	ec2Snapshots = sim.MakeStore[EC2Snapshot](nil, "ec2_snapshots")

	// Hold the capture at its start, so the response and the destination are
	// sampled before any byte moves whatever the scheduler does.
	captureStarted := make(chan string, 1)
	releaseCapture := make(chan struct{})
	ec2SnapshotCaptureStarting = func(snapshotID string) {
		captureStarted <- snapshotID
		<-releaseCapture
	}
	t.Cleanup(func() { ec2SnapshotCaptureStarting = func(string) {} })

	source := t.TempDir()
	payload := make([]byte, 4*1024*1024)
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

	// A capture inside the handler would block on the held hook and never
	// return, so returning at all proves the copy runs behind the response.
	handleCreateSnapshot(rec, req)
	held := <-captureStarted
	returnedAt := ec2SnapshotDirSize(t, ec2SnapshotHostDirPathForResponse(rec.Body.String()))

	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<status>pending</status>",
		"CreateSnapshot must return the snapshot as pending, not after the copy finished")

	start := strings.Index(body, "<snapshotId>")
	require.Greater(t, start, -1, body)
	snapshotID := body[start+len("<snapshotId>"):]
	snapshotID = snapshotID[:strings.Index(snapshotID, "</snapshotId>")]

	require.Equal(t, snapshotID, held)
	stored, ok := ec2Snapshots.Get(snapshotID)
	require.True(t, ok)
	require.Equal(t, "pending", stored.State)
	require.Zero(t, returnedAt, "the destination held data before the capture started")

	// A lazy settle must not call it completed while bytes are still moving:
	// a restore from a half-written snapshot would silently lose data.
	ec2SettleSnapshot(snapshotID)
	settled, ok := ec2Snapshots.Get(snapshotID)
	require.True(t, ok)
	require.NotEqual(t, "completed", settled.State,
		"a snapshot must not report completed before its data is captured")

	// The capture completes it once the data is actually there.
	close(releaseCapture)
	bg.Await()
	current, ok := ec2Snapshots.Get(snapshotID)
	require.True(t, ok)
	require.Equal(t, "completed", current.State)
	require.Equal(t, "100%", current.Progress)

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
