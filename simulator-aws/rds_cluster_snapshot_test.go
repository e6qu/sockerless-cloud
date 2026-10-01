package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func rdsExpectFault(t *testing.T, answer *httptest.ResponseRecorder, code string) {
	t.Helper()
	if answer.Code != http.StatusBadRequest || !strings.Contains(answer.Body.String(), "<Code>"+code+"</Code>") {
		t.Fatalf("answer %d %s, want 400 %s", answer.Code, answer.Body.String(), code)
	}
}

// A DB cluster snapshot is creating until its cluster volume is captured and
// carries the master credential the captured data was written with; a
// restore from it, and a point-in-time restore, is creating until its cluster
// volume is seeded and then serves the restored engine under that credential.
func TestRDSAuroraClusterSnapshotsCarryTheClusterEngine(t *testing.T) {
	rdsResetAuroraStores(t)
	const clusterID = "aurora-snapshots"
	cluster := rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 25440)

	created := rdsFormCall(t, handleRDSCreateClusterSnapshot, url.Values{
		"DBClusterSnapshotIdentifier": {"aurora-snapshots-snap"},
		"DBClusterIdentifier":         {clusterID},
	})
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "<Status>creating</Status>") ||
		!strings.Contains(created.Body.String(), "<PercentProgress>0</PercentProgress>") {
		t.Fatalf("CreateDBClusterSnapshot = %d %s, want a creating snapshot", created.Code, created.Body.String())
	}
	bg.Await()
	snapshot, _ := rdsClusterSnapshots.Get("aurora-snapshots-snap")
	if snapshot.Status != "available" || snapshot.PercentProgress != 100 || !bytes.Equal(snapshot.MasterUserSecret, cluster.MasterUserSecret) ||
		snapshot.DatabaseName != "appdb" {
		t.Fatalf("snapshot %q %d%% database %q, want available at 100%% with the cluster's credential and database", snapshot.Status, snapshot.PercentProgress, snapshot.DatabaseName)
	}

	rdsClusterSnapshots.Put("aurora-snapshots-capturing", RDSClusterSnapshot{DBClusterSnapshotIdentifier: "aurora-snapshots-capturing", Status: "creating", Engine: "aurora-postgresql"})
	for name, call := range map[string]struct {
		handler http.HandlerFunc
		form    url.Values
	}{
		"DeleteDBClusterSnapshot": {handleRDSDeleteClusterSnapshot, url.Values{"DBClusterSnapshotIdentifier": {"aurora-snapshots-capturing"}}},
		"CopyDBClusterSnapshot": {handleRDSCopyClusterSnapshot, url.Values{
			"SourceDBClusterSnapshotIdentifier": {"aurora-snapshots-capturing"}, "TargetDBClusterSnapshotIdentifier": {"aurora-snapshots-copy-early"},
		}},
		"RestoreDBClusterFromSnapshot": {handleRDSRestoreClusterFromSnapshot, url.Values{
			"DBClusterIdentifier": {"aurora-snapshots-early"}, "SnapshotIdentifier": {"aurora-snapshots-capturing"}, "Engine": {"aurora-postgresql"},
		}},
	} {
		answer := rdsFormCall(t, call.handler, call.form)
		rdsExpectFault(t, answer, "InvalidDBClusterSnapshotStateFault")
		if name == "RestoreDBClusterFromSnapshot" {
			if _, ok := rdsClusters.Get("aurora-snapshots-early"); ok {
				t.Fatal("the refused restore was recorded")
			}
		}
	}

	copied := rdsFormCall(t, handleRDSCopyClusterSnapshot, url.Values{
		"SourceDBClusterSnapshotIdentifier": {"aurora-snapshots-snap"},
		"TargetDBClusterSnapshotIdentifier": {"aurora-snapshots-copy"},
	})
	if copied.Code != http.StatusOK || !strings.Contains(copied.Body.String(), "<Status>copying</Status>") ||
		!strings.Contains(copied.Body.String(), "<SourceDBClusterSnapshotArn>"+snapshot.ARN+"</SourceDBClusterSnapshotArn>") {
		t.Fatalf("CopyDBClusterSnapshot = %d %s, want a copying snapshot naming its source", copied.Code, copied.Body.String())
	}
	bg.Await()
	if copy, _ := rdsClusterSnapshots.Get("aurora-snapshots-copy"); copy.Status != "available" || !bytes.Equal(copy.MasterUserSecret, cluster.MasterUserSecret) {
		t.Fatalf("copy %q, want available with the source's credential", copy.Status)
	}

	wrongEngine := rdsFormCall(t, handleRDSRestoreClusterFromSnapshot, url.Values{
		"DBClusterIdentifier": {"aurora-snapshots-mysql"}, "SnapshotIdentifier": {"aurora-snapshots-copy"}, "Engine": {"aurora-mysql"},
	})
	rdsExpectFault(t, wrongEngine, "InvalidParameterCombination")

	restoredFromSnapshot := rdsFormCall(t, handleRDSRestoreClusterFromSnapshot, url.Values{
		"DBClusterIdentifier": {"aurora-snapshots-restored"}, "SnapshotIdentifier": {"aurora-snapshots-copy"}, "Engine": {"aurora-postgresql"},
	})
	t.Cleanup(func() { bg.Await(); _ = rdsStopAuroraDataPlane("aurora-snapshots-restored", false) })
	if restoredFromSnapshot.Code != http.StatusOK || !strings.Contains(restoredFromSnapshot.Body.String(), "<Status>creating</Status>") {
		t.Fatalf("RestoreDBClusterFromSnapshot = %d %s, want a creating cluster", restoredFromSnapshot.Code, restoredFromSnapshot.Body.String())
	}

	pitBad := map[string]url.Values{
		"InvalidRestoreFault": {"DBClusterIdentifier": {"aurora-snapshots-pit"}, "SourceDBClusterIdentifier": {clusterID}, "RestoreToTime": {"2026-01-01T00:00:00Z"}},
		"InvalidParameterCombination": {"DBClusterIdentifier": {"aurora-snapshots-pit"}, "SourceDBClusterIdentifier": {clusterID},
			"RestoreToTime": {"2026-01-01T00:00:00Z"}, "UseLatestRestorableTime": {"true"}},
	}
	for code, form := range pitBad {
		answer := rdsFormCall(t, handleRDSRestoreClusterToPointInTime, form)
		rdsExpectFault(t, answer, code)
	}
	pit := rdsFormCall(t, handleRDSRestoreClusterToPointInTime, url.Values{
		"DBClusterIdentifier": {"aurora-snapshots-pit"}, "SourceDBClusterIdentifier": {clusterID}, "UseLatestRestorableTime": {"true"},
	})
	t.Cleanup(func() { bg.Await(); _ = rdsStopAuroraDataPlane("aurora-snapshots-pit", false) })
	if pit.Code != http.StatusOK || !strings.Contains(pit.Body.String(), "<Status>creating</Status>") {
		t.Fatalf("RestoreDBClusterToPointInTime = %d %s, want a creating cluster", pit.Code, pit.Body.String())
	}
	bg.Await()

	for _, restoredID := range []string{"aurora-snapshots-restored", "aurora-snapshots-pit"} {
		restored, _ := rdsClusters.Get(restoredID)
		if restored.Status != "available" || restored.Port != cluster.Port || restored.DatabaseName != "appdb" || restored.RestoreSourceVolume != "" {
			t.Fatalf("%s %q on port %d database %q, want available on the source's port and database", restoredID, restored.Status, restored.Port, restored.DatabaseName)
		}
		plane, ok := rdsLoadAuroraDataPlane(restoredID)
		if !ok {
			t.Fatalf("%s has no data plane", restoredID)
		}
		if plane.engine.Volume != rdsClusterVolume(restoredID) || !plane.authenticate("admin", "MasterPassword-1", false) {
			t.Fatalf("%s does not serve its own cluster volume under the source's master credential", restoredID)
		}
		member := rdsCreateAuroraMember(t, restoredID, restoredID+"-1", "aurora-postgresql")
		if _, ok := plane.writerTarget(); !ok {
			t.Fatalf("%s's writer endpoint refuses clients once its writer is available", restoredID)
		}
		rdsClusters.Update(restoredID, func(c *RDSCluster) { c.Status = "creating" })
		if _, ok := plane.writerTarget(); ok {
			t.Fatalf("%s's writer endpoint serves while its volume is seeded", restoredID)
		}
		if _, ok := plane.instanceTarget(member.DBInstanceIdentifier); ok {
			t.Fatalf("%s's instance endpoint serves while its volume is seeded", restoredID)
		}
		snapshotting := rdsFormCall(t, handleRDSCreateClusterSnapshot, url.Values{
			"DBClusterSnapshotIdentifier": {restoredID + "-early"}, "DBClusterIdentifier": {restoredID},
		})
		rdsExpectFault(t, snapshotting, "InvalidDBClusterStateFault")
		rdsClusters.Update(restoredID, func(c *RDSCluster) { c.Status = "available" })
	}

	deleted := rdsFormCall(t, handleRDSDeleteCluster, url.Values{
		"DBClusterIdentifier": {clusterID}, "FinalDBSnapshotIdentifier": {"aurora-snapshots-final"},
	})
	if deleted.Code != http.StatusOK {
		t.Fatalf("DeleteDBCluster = %d %s", deleted.Code, deleted.Body.String())
	}
	if final, _ := rdsClusterSnapshots.Get("aurora-snapshots-final"); final.Status != "creating" && final.Status != "available" {
		t.Fatalf("final snapshot %q, want creating until captured", final.Status)
	}
	bg.Await()
	if final, _ := rdsClusterSnapshots.Get("aurora-snapshots-final"); final.Status != "available" || !bytes.Equal(final.MasterUserSecret, cluster.MasterUserSecret) {
		t.Fatalf("final snapshot %q, want available with the cluster's credential", final.Status)
	}
	if _, installed := rdsLoadAuroraDataPlane(clusterID); installed {
		t.Fatal("the deleted cluster's data plane outlived its final snapshot")
	}

	removed := rdsFormCall(t, handleRDSDeleteClusterSnapshot, url.Values{"DBClusterSnapshotIdentifier": {"aurora-snapshots-final"}})
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), "<Status>deleted</Status>") {
		t.Fatalf("DeleteDBClusterSnapshot = %d %s", removed.Code, removed.Body.String())
	}
}
