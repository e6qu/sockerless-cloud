package main

import (
	"context"
	"log"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// An Aurora cluster's data lives in its one cluster volume, so a DB cluster
// snapshot captures that volume and a cluster restore seeds the new cluster's
// volume from it before the engine first starts.
func rdsClusterSnapshotVolume(snapshotID string) string {
	return rdsVolume("cluster-snapshot", snapshotID)
}

// rdsCaptureClusterSnapshotData captures the cluster volume into the
// snapshot's volume and settles the snapshot. A cluster whose engine never
// started has no volume and nothing to capture: its restore starts a fresh
// engine under the same master user and database, which is all it held.
func rdsCaptureClusterSnapshotData(snapshotID, clusterID string) {
	if err := sim.CaptureVolume(context.Background(),
		rdsClusterVolume(clusterID), rdsClusterSnapshotVolume(snapshotID), "rds"); err != nil {
		rdsSettleClusterSnapshot(snapshotID, "failed", err.Error())
		return
	}
	rdsSettleClusterSnapshot(snapshotID, "available", "")
}

// rdsCopyClusterSnapshotData clones the source snapshot's volume into the
// copy's and settles the copy.
func rdsCopyClusterSnapshotData(targetID, sourceID string) {
	if err := sim.CaptureVolume(context.Background(),
		rdsClusterSnapshotVolume(sourceID), rdsClusterSnapshotVolume(targetID), "rds"); err != nil {
		rdsSettleClusterSnapshot(targetID, "failed", err.Error())
		return
	}
	rdsSettleClusterSnapshot(targetID, "available", "")
}

func rdsSettleClusterSnapshot(snapshotID, status, reason string) {
	if !rdsClusterSnapshots.Update(snapshotID, func(snapshot *RDSClusterSnapshot) {
		snapshot.Status = status
		snapshot.StatusReason = reason
		if status == "available" {
			snapshot.PercentProgress = 100
		}
	}) {
		sim.RemoveVolumeSettled(rdsClusterSnapshotVolume(snapshotID), "rds")
	}
}

// rdsFinishClusterRestore seeds a restored cluster's volume from its source —
// a cluster snapshot's volume, a DB snapshot's volume, the source cluster's
// volume for a restore to the latest restorable time, or the source cluster's
// base backup for a restore to a time — and lands the cluster available. A
// restore to a time also replays the source's log onto the base backup. A
// creating cluster's endpoints refuse clients, so its engine cannot start on a
// half-seeded volume. A seed that fails lands the cluster
// incompatible-restore.
func rdsFinishClusterRestore(clusterID string) {
	cluster, ok := rdsClusters.Get(clusterID)
	if !ok || cluster.Status != "creating" {
		return
	}
	status := "available"
	err := sim.CaptureVolume(context.Background(), cluster.RestoreSourceVolume, rdsClusterVolume(clusterID), "rds")
	if err == nil && cluster.RestoreToTime != "" {
		err = rdsReplayLogToRestoreTime(cluster)
	}
	if err != nil {
		log.Printf("Amazon RDS cluster %s: restore from volume %s: %v", clusterID, cluster.RestoreSourceVolume, err)
		status = "incompatible-restore"
	}
	rdsClusters.Update(clusterID, func(stored *RDSCluster) {
		if stored.Status == "creating" {
			stored.Status = status
			stored.RestoreSourceVolume = ""
			stored.RestoreLogVolume = ""
			stored.RestoreToTime = ""
		}
	})
}

// rdsRecoverClusterSnapshots resumes the captures and copies a previous
// process started but did not settle.
func rdsRecoverClusterSnapshots() {
	for _, snapshot := range rdsClusterSnapshots.List() {
		id := snapshot.DBClusterSnapshotIdentifier
		switch snapshot.Status {
		case "creating":
			if rdsClusterSnapshotSourceGone(snapshot) {
				// rdsMoveVolumesToKindNames moved the cluster's data into the
				// snapshot's volume.
				rdsSettleClusterSnapshot(id, "available", "")
				continue
			}
			clusterID, resourceID := snapshot.DBClusterIdentifier, snapshot.DbClusterResourceId
			bg.Go(func() {
				rdsCaptureClusterSnapshotData(id, clusterID)
				// A final snapshot's cluster waited in deleting for this capture.
				rdsFinishClusterDeletion(clusterID, resourceID)
			})
		case "copying":
			source, ok := findRDSClusterSnapshotByARN(snapshot.SourceDBClusterSnapshotArn)
			if !ok {
				rdsSettleClusterSnapshot(id, "failed", "the source DB cluster snapshot no longer exists")
				continue
			}
			sourceID := source.DBClusterSnapshotIdentifier
			bg.Go(func() { rdsCopyClusterSnapshotData(id, sourceID) })
		}
	}
}
