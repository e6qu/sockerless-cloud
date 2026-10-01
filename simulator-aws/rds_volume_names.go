package main

import (
	"context"
	"fmt"
	"log"

	"github.com/e6qu/sockerless-cloud/sim"
)

// rdsVolumeClaim is one resource's claim on the volume an earlier simulator
// named without a kind separator.
type rdsVolumeClaim struct {
	owner   string
	current string
	labels  map[string]string
}

// rdsMoveVolumesToKindNames moves each RDS resource's data from the volume an
// earlier simulator named sockerless-rds-<instance>,
// sockerless-rds-cluster-<cluster> or sockerless-rds-snap-<snapshot> into the
// volume rdsVolume names. An engine container still on the old volume goes
// first, and the engine starts on the moved volume at its next client. Two
// resources could share an old name — an instance cluster-x and a cluster x —
// and such a volume holds both resources' writes, so it stays where it is for
// the operator and neither resource takes it.
func rdsMoveVolumesToKindNames() error {
	if sim.RequireContainerRuntime("moving Amazon RDS volumes") != nil {
		return nil
	}
	for previous, claimants := range rdsPreviousVolumeClaims() {
		if !sim.VolumeExists(previous) {
			continue
		}
		if len(claimants) > 1 {
			owners := make([]string, len(claimants))
			for i, c := range claimants {
				owners[i] = c.owner
			}
			log.Printf("Amazon RDS: volume %s holds the data of %v together; it stays in place and none of them takes it", previous, owners)
			continue
		}
		if err := rdsMoveVolume(previous, claimants[0]); err != nil {
			return fmt.Errorf("move %s's volume %s: %w", claimants[0].owner, previous, err)
		}
	}
	return nil
}

// rdsPreviousVolumeClaims maps each volume name an earlier simulator gave an
// RDS resource to the resources whose data it may hold.
func rdsPreviousVolumeClaims() map[string][]rdsVolumeClaim {
	claims := map[string][]rdsVolumeClaim{}
	claim := func(previous string, c rdsVolumeClaim) { claims[previous] = append(claims[previous], c) }
	for _, instance := range rdsInstances.List() {
		if rdsIsAurora(instance.Engine) && instance.DBClusterIdentifier != "" {
			continue
		}
		id := instance.DBInstanceIdentifier
		claim("sockerless-rds-"+id, rdsVolumeClaim{
			owner: "DB instance " + id, current: rdsInstanceVolume(id),
			labels: map[string]string{"sockerless-rds-instance": id},
		})
	}
	for _, cluster := range rdsClusters.List() {
		id := cluster.DBClusterIdentifier
		claim("sockerless-rds-cluster-"+id, rdsVolumeClaim{
			owner: "DB cluster " + id, current: rdsClusterVolume(id),
			labels: map[string]string{"sockerless-rds-cluster": id},
		})
	}
	for _, snapshot := range rdsSnapshots.List() {
		id := snapshot.DBSnapshotIdentifier
		if rdsSnapshotSourceGone(snapshot) {
			// An earlier simulator dropped the instance at once and kept its
			// volume only for this capture, so that volume holds the data.
			instanceID := snapshot.DBInstanceIdentifier
			claim("sockerless-rds-"+instanceID, rdsVolumeClaim{
				owner: "DB snapshot " + id, current: rdsSnapshotVolume(id),
				labels: map[string]string{"sockerless-rds-instance": instanceID},
			})
			continue
		}
		claim("sockerless-rds-snap-"+id, rdsVolumeClaim{owner: "DB snapshot " + id, current: rdsSnapshotVolume(id)})
	}
	for _, snapshot := range rdsClusterSnapshots.List() {
		if rdsClusterSnapshotSourceGone(snapshot) {
			id, clusterID := snapshot.DBClusterSnapshotIdentifier, snapshot.DBClusterIdentifier
			claim("sockerless-rds-cluster-"+clusterID, rdsVolumeClaim{
				owner: "DB cluster snapshot " + id, current: rdsClusterSnapshotVolume(id),
				labels: map[string]string{"sockerless-rds-cluster": clusterID},
			})
		}
	}
	return claims
}

// rdsSnapshotSourceGone reports whether a capturing snapshot's instance is no
// longer the generation it captures, which leaves nothing to capture from.
func rdsSnapshotSourceGone(snapshot RDSSnapshot) bool {
	if snapshot.Status != "creating" || snapshot.SourceDBSnapshotIdentifier != "" {
		return false
	}
	instance, ok := rdsInstances.Get(snapshot.DBInstanceIdentifier)
	return !ok || instance.DbiResourceId != snapshot.DbiResourceId
}

// rdsClusterSnapshotSourceGone reports whether a capturing cluster snapshot's
// cluster is no longer the generation it captures.
func rdsClusterSnapshotSourceGone(snapshot RDSClusterSnapshot) bool {
	if snapshot.Status != "creating" {
		return false
	}
	cluster, ok := rdsClusters.Get(snapshot.DBClusterIdentifier)
	return !ok || cluster.DbClusterResourceId != snapshot.DbClusterResourceId
}

// rdsMoveVolume removes the old volume only once the copy has finished, so a
// current volume found beside it is a copy an interrupted move left partial.
func rdsMoveVolume(previous string, c rdsVolumeClaim) error {
	if sim.VolumeExists(c.current) {
		if err := sim.RemoveVolume(c.current); err != nil {
			return fmt.Errorf("remove partial copy %s: %w", c.current, err)
		}
	}
	if c.labels != nil {
		engines, err := sim.FindExistingContainers(c.labels)
		if err != nil {
			return err
		}
		for _, engine := range engines {
			if err := sim.RemoveExistingContainer(engine.ID); err != nil {
				return fmt.Errorf("remove engine container %s: %w", engine.ID, err)
			}
		}
	}
	if _, err := sim.SnapshotVolume(context.Background(), previous, c.current); err != nil {
		return err
	}
	sim.RemoveVolumeSettled(previous, "rds")
	return nil
}
