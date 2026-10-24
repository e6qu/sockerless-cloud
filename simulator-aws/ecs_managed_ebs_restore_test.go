//go:build darwin || linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// RunTask is asynchronous on real ECS: it returns while the task is still
// PROVISIONING and the managed-EBS volume hydrates behind an ATTACHING
// attachment. Amazon EC2 answers CreateVolume with the volume creating and
// fills it from the snapshot behind the response, so preparing the volume
// records the hydration the task waits for on its transition path and never
// copies the snapshot's data itself.
func TestPrepareManagedEBSVolumesDefersSnapshotRestore(t *testing.T) {
	t.Setenv("SIM_EBS_DATA_DIR", t.TempDir())
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SIM_DNS_PORT", "0")
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	bg.Await()
	server, _, _, err := buildSimulator(sim.Config{Provider: "aws", ListenAddr: ":0", LogLevel: "error"})
	require.NoError(t, err)
	t.Cleanup(server.StopBackground)
	roleArn := putServiceRole(t, "volume-infrastructure", "ecs.amazonaws.com", "ec2:CreateVolume")

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
			RoleArn:    roleArn,
			SizeInGiB:  8,
			SnapshotId: "snap-defer",
		},
	}}

	taskArn := "arn:aws:ecs:" + awsRegion() + ":" + awsAccountID() + ":task/default/task-defer"
	hosts, attachments, restores, reqErr := ecsPrepareManagedEBSVolumes(context.Background(), td, configs, taskArn, "")
	require.Nil(t, reqErr)
	require.Len(t, attachments, 1)
	require.Equal(t, "ATTACHING", attachments[0].Status)
	require.Len(t, restores, 1, "the hydration must be recorded for the transition path")
	volumeID := ecsTaskDetail(attachments[0].Details, "volumeId")
	require.Equal(t, volumeID, restores[0].HydratingVolumeID)
	owner, ok := ecsManagedVolumeOwners.Get(volumeID)
	require.True(t, ok, "the role that created the volume is recorded for its deletion")
	require.Equal(t, roleArn, owner.RoleArn)

	destination := hosts["workspace"]
	require.NotEmpty(t, destination)

	// The transition path waits for the hydration and only then reports ATTACHED.
	ecsTasks.Put("task-defer", ECSTask{TaskArn: taskArn, Attachments: attachments})
	require.NoError(t, ecsRunPendingEBSRestores(context.Background(), "task-defer", restores))

	restored, err := os.ReadFile(filepath.Join(destination, "workspace.txt"))
	require.NoError(t, err)
	require.Equal(t, "restored", string(restored))

	stored, ok := ecsTasks.Get("task-defer")
	require.True(t, ok)
	require.Len(t, stored.Attachments, 1)
	require.Equal(t, "ATTACHED", stored.Attachments[0].Status)
	volume, ok := ec2Volumes.Get(volumeID)
	require.True(t, ok)
	require.Equal(t, "in-use", volume.State, "the task holds the volume once it is hydrated")
	require.Equal(t, "snap-defer", volume.SnapshotId)
}
