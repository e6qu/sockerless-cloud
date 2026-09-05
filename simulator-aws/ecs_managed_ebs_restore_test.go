//go:build darwin || linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// RunTask is asynchronous on real ECS: it returns while the task is still
// PROVISIONING and the managed-EBS volume hydrates behind an ATTACHING
// attachment. The simulator used to copy the snapshot's data inline in the
// request handler, so RunTask blocked for the length of the copy — roughly 5s
// per GiB — and a reverse proxy in front of the simulator turned that into a
// 502 for the caller. Preparing the volume must therefore record the restore
// without performing it.
func TestPrepareManagedEBSVolumesDefersSnapshotRestore(t *testing.T) {
	t.Setenv("SIM_EBS_DATA_DIR", t.TempDir())
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2Snapshots = sim.MakeStore[EC2Snapshot](nil, "ec2_snapshots")
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	snapshotPayload := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(snapshotPayload, "workspace.txt"), []byte("restored"), 0o644))
	ec2Snapshots.Put("snap-defer", EC2Snapshot{
		SnapshotId: "snap-defer",
		State:      "completed",
		VolumeSize: 8,
		HostPath:   snapshotPayload,
	})

	td := ECSTaskDefinition{Volumes: []ECSVolume{{Name: "workspace", ConfiguredAtLaunch: true}}}
	configs := []ECSTaskVolumeConfiguration{{
		Name: "workspace",
		ManagedEBSVolume: &ECSTaskManagedEBSVolumeConfiguration{
			SizeInGiB:  8,
			SnapshotId: "snap-defer",
		},
	}}

	hosts, attachments, restores, reqErr := ecsPrepareManagedEBSVolumes(
		context.Background(), td, configs, "task-defer", "")
	require.Nil(t, reqErr)
	require.Len(t, attachments, 1)
	require.Equal(t, "ATTACHING", attachments[0].Status)
	require.Len(t, restores, 1, "the snapshot restore must be recorded for the transition path")

	destination := hosts["workspace"]
	require.NotEmpty(t, destination)
	entries, err := os.ReadDir(destination)
	require.NoError(t, err)
	require.Empty(t, entries, "the RunTask request path must not copy snapshot data")

	// The transition path performs the copy and only then reports ATTACHED.
	ecsTasks.Put("task-defer", ECSTask{TaskArn: "arn:task-defer", Attachments: attachments})
	require.NoError(t, ecsRunPendingEBSRestores(context.Background(), "task-defer", restores))

	restored, err := os.ReadFile(filepath.Join(destination, "workspace.txt"))
	require.NoError(t, err)
	require.Equal(t, "restored", string(restored))

	stored, ok := ecsTasks.Get("task-defer")
	require.True(t, ok)
	require.Len(t, stored.Attachments, 1)
	require.Equal(t, "ATTACHED", stored.Attachments[0].Status)
}
