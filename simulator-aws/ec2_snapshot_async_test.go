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

// CreateSnapshot returns the snapshot pending at 0% and captures the data
// behind the response while the caller polls; a capture inside the handler
// would stall every request for the length of the copy.
func TestCreateSnapshotReturnsBeforeCapturingData(t *testing.T) {
	t.Setenv("SIM_EBS_DATA_DIR", t.TempDir())
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")
	ec2Snapshots = sim.MakeStore[EC2Snapshot](nil, "ec2_snapshots")

	// Hold the capture at its start so response and destination are sampled
	// before any byte moves.
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

	// A capture inside the handler would block on the held hook, so returning
	// proves it runs behind the response.
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

	// A settle must not report completed while bytes are moving; a restore from a
	// half-written snapshot would lose data.
	ec2SettleSnapshot(snapshotID)
	settled, ok := ec2Snapshots.Get(snapshotID)
	require.True(t, ok)
	require.NotEqual(t, "completed", settled.State,
		"a snapshot must not report completed before its data is captured")

	// The capture completes the snapshot once the data is there.
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

// Returns where the snapshot's host-path data lands.
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

// A missing directory counts as zero.
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
