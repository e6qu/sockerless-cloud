package main

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

func TestRDSVolumeNamesNeverCollideAcrossKinds(t *testing.T) {
	names := map[string]string{}
	for _, id := range []string{"x", "cluster-x", "snap-x", "snapshot-x", "instance-x", "cluster-snapshot-x"} {
		for kind, name := range map[string]string{
			"instance " + id:         rdsInstanceVolume(id),
			"cluster " + id:          rdsClusterVolume(id),
			"snapshot " + id:         rdsSnapshotVolume(id),
			"cluster snapshot " + id: rdsClusterSnapshotVolume(id),
		} {
			if other, taken := names[name]; taken {
				t.Fatalf("%s and %s share volume %s", other, kind, name)
			}
			names[name] = kind
		}
	}
}

// An instance cluster-x and a cluster x shared the volume an earlier
// simulator named for both; neither takes it, and a resource alone on its
// earlier name takes that volume.
func TestRDSPreviousVolumeClaims(t *testing.T) {
	rdsResetAuroraStores(t)
	rdsSnapshots = sim.MakeStore[RDSSnapshot](nil, "rds_snapshots")
	rdsInstances.Put("cluster-x", RDSInstance{DBInstanceIdentifier: "cluster-x", Engine: "postgres"})
	rdsClusters.Put("x", RDSCluster{DBClusterIdentifier: "x", Engine: "aurora-postgresql"})
	rdsInstances.Put("x-writer", RDSInstance{DBInstanceIdentifier: "x-writer", Engine: "aurora-postgresql", DBClusterIdentifier: "x"})
	rdsSnapshots.Put("only", RDSSnapshot{DBSnapshotIdentifier: "only"})
	// An earlier simulator dropped an instance and a cluster at once and kept
	// their volumes only for their final snapshots' captures.
	rdsSnapshots.Put("gone-final", RDSSnapshot{DBSnapshotIdentifier: "gone-final", DBInstanceIdentifier: "gone-db", Status: "creating"})
	rdsClusterSnapshots.Put("gone-cluster-final", RDSClusterSnapshot{DBClusterSnapshotIdentifier: "gone-cluster-final", DBClusterIdentifier: "gone-cluster", Status: "creating"})

	claims := rdsPreviousVolumeClaims()
	if shared := claims["sockerless-rds-cluster-x"]; len(shared) != 2 {
		t.Fatalf("sockerless-rds-cluster-x claimants = %+v, want the instance and the cluster", shared)
	}
	if member := claims["sockerless-rds-x-writer"]; len(member) != 0 {
		t.Fatalf("an Aurora member, which has no volume of its own, claims %+v", member)
	}
	only := claims["sockerless-rds-snap-only"]
	if len(only) != 1 || only[0].current != rdsSnapshotVolume("only") {
		t.Fatalf("sockerless-rds-snap-only claimants = %+v, want the snapshot alone", only)
	}
	if final := claims["sockerless-rds-gone-db"]; len(final) != 1 || final[0].current != rdsSnapshotVolume("gone-final") {
		t.Fatalf("sockerless-rds-gone-db claimants = %+v, want the final snapshot it was kept for", final)
	}
	if partial := claims["sockerless-rds-snap-gone-final"]; len(partial) != 0 {
		t.Fatalf("an unfinished capture's own volume is claimed by %+v", partial)
	}
	if final := claims["sockerless-rds-cluster-gone-cluster"]; len(final) != 1 || final[0].current != rdsClusterSnapshotVolume("gone-cluster-final") {
		t.Fatalf("sockerless-rds-cluster-gone-cluster claimants = %+v, want the final cluster snapshot it was kept for", final)
	}
}

// DeleteDBInstance with a final snapshot leaves the instance deleting, which
// holds its identifier, until the snapshot is captured and the engine and
// volume are gone.
func TestRDSDeleteDBInstanceHoldsTheIdentifierUntilItsTeardown(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsSnapshots = sim.MakeStore[RDSSnapshot](nil, "rds_snapshots")
	const id = "held-db"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	engine := &dbengine.Instance{Name: "Amazon RDS " + id, Engine: dbengine.Postgres16}
	engine.Serve(listener)
	rdsDataPlanes.Store(id, &rdsDataPlane{engine: engine, backups: &rdsAutomatedBackups{owner: rdsInstanceBackups{instanceID: id}, engine: engine, volume: rdsInstanceVolume(id)}})
	rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, DbiResourceId: "db-HELD", Engine: "postgres", DBInstanceStatus: "available"})
	t.Cleanup(func() { rdsDataPlanes.Delete(id) })

	// Hold the instance's stop lock, so the teardown cannot finish until the
	// test lets it.
	release := rdsDataPlaneStops.Lock(id)
	deleted := rdsFormCall(t, handleRDSDelete, url.Values{"DBInstanceIdentifier": {id}, "FinalDBSnapshotIdentifier": {id + "-final"}})
	recreated := rdsFormCall(t, handleRDSCreate, url.Values{
		"DBInstanceIdentifier": {id}, "Engine": {"postgres"}, "DBInstanceClass": {"db.t3.micro"},
		"MasterUsername": {"admin"}, "MasterUserPassword": {"MasterPassword-1"},
	})
	again := rdsFormCall(t, handleRDSDelete, url.Values{"DBInstanceIdentifier": {id}, "SkipFinalSnapshot": {"true"}})
	modified := rdsFormCall(t, handleRDSModify, url.Values{"DBInstanceIdentifier": {id}, "DBInstanceClass": {"db.t3.small"}})
	held, _ := rdsInstances.Get(id)
	release()
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), "<DBInstanceStatus>deleting</DBInstanceStatus>") {
		t.Fatalf("DeleteDBInstance = %d %s, want 200 deleting", deleted.Code, deleted.Body.String())
	}
	if held.DBInstanceStatus != "deleting" || held.DbiResourceId != "db-HELD" {
		t.Fatalf("instance during teardown = %q %q, want the deleting instance", held.DBInstanceStatus, held.DbiResourceId)
	}
	rdsExpectFault(t, recreated, "DBInstanceAlreadyExists")
	rdsExpectFault(t, again, "InvalidDBInstanceState")
	rdsExpectFault(t, modified, "InvalidDBInstanceState")

	bg.Await()
	if _, exists := rdsInstances.Get(id); exists {
		t.Fatal("the instance outlived its teardown")
	}
	if final, _ := rdsSnapshots.Get(id + "-final"); final.Status != "available" {
		t.Fatalf("final snapshot %q, want available", final.Status)
	}
	if _, installed := rdsLoadDataPlane(id); installed {
		t.Fatal("the deleted instance kept its data plane")
	}
	if conn, err := net.Dial("tcp", listener.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("the deleted instance's endpoint still accepts clients")
	}
}

// A teardown touches only the generation it was taken for: an instance or
// cluster that carries the identifier under another resource ID keeps its
// record and its data plane.
func TestRDSTeardownSparesAnotherGenerationOfTheIdentifier(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsClusters = sim.MakeStore[RDSCluster](nil, "rds_clusters")
	const id = "reused-db"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	engine := &dbengine.Instance{Name: "Amazon RDS " + id, Engine: dbengine.Postgres16}
	engine.Serve(listener)
	rdsDataPlanes.Store(id, &rdsDataPlane{engine: engine, backups: &rdsAutomatedBackups{owner: rdsInstanceBackups{instanceID: id}, engine: engine, volume: rdsInstanceVolume(id)}})
	t.Cleanup(func() { _ = rdsStopDataPlane(id, false) })

	for _, status := range []string{"available", "deleting"} {
		rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, DbiResourceId: "db-NEW", Engine: "postgres", DBInstanceStatus: status})
		rdsFinishInstanceDeletion(id, "db-OLD")
		if stored, exists := rdsInstances.Get(id); !exists || stored.DbiResourceId != "db-NEW" {
			t.Fatalf("an earlier generation's teardown removed the %s instance", status)
		}
		if _, installed := rdsLoadDataPlane(id); !installed {
			t.Fatalf("an earlier generation's teardown stopped the %s instance's data plane", status)
		}
		rdsClusters.Put(id, RDSCluster{DBClusterIdentifier: id, DbClusterResourceId: "cluster-NEW", Status: status})
		rdsFinishClusterDeletion(id, "cluster-OLD")
		if stored, exists := rdsClusters.Get(id); !exists || stored.DbClusterResourceId != "cluster-NEW" {
			t.Fatalf("an earlier generation's teardown removed the %s cluster", status)
		}
	}
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("the instance's endpoint refuses clients after an earlier generation's teardown: %v", err)
	}
	_ = conn.Close()
}

// DeleteDBCluster with a final snapshot leaves the cluster deleting, which
// holds its identifier, until the snapshot is captured and the engine and
// cluster volume are gone; the identifier is free again afterwards.
func TestRDSDeleteDBClusterHoldsTheIdentifierUntilItsTeardown(t *testing.T) {
	rdsResetAuroraStores(t)
	const clusterID = "held-cluster"
	rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 25450)

	release := rdsDataPlaneStops.Lock("cluster/" + clusterID)
	deleted := rdsFormCall(t, handleRDSDeleteCluster, url.Values{
		"DBClusterIdentifier": {clusterID}, "FinalDBSnapshotIdentifier": {clusterID + "-final"},
	})
	recreated := rdsFormCall(t, handleRDSCreateCluster, url.Values{
		"DBClusterIdentifier": {clusterID}, "Engine": {"aurora-postgresql"},
		"MasterUsername": {"admin"}, "MasterUserPassword": {"MasterPassword-1"},
	})
	again := rdsClusterCall(t, handleRDSDeleteCluster, clusterID)
	snapshotting := rdsFormCall(t, handleRDSCreateClusterSnapshot, url.Values{
		"DBClusterSnapshotIdentifier": {clusterID + "-late"}, "DBClusterIdentifier": {clusterID},
	})
	held, _ := rdsClusters.Get(clusterID)
	release()
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), "<Status>deleting</Status>") {
		t.Fatalf("DeleteDBCluster = %d %s, want 200 deleting", deleted.Code, deleted.Body.String())
	}
	if held.Status != "deleting" {
		t.Fatalf("cluster during teardown is %q, want deleting", held.Status)
	}
	rdsExpectFault(t, recreated, "DBClusterAlreadyExistsFault")
	rdsExpectFault(t, again, "InvalidDBClusterStateFault")
	rdsExpectFault(t, snapshotting, "InvalidDBClusterStateFault")

	bg.Await()
	if _, exists := rdsClusters.Get(clusterID); exists {
		t.Fatal("the cluster outlived its teardown")
	}
	if final, _ := rdsClusterSnapshots.Get(clusterID + "-final"); final.Status != "available" {
		t.Fatalf("final cluster snapshot %q, want available", final.Status)
	}
	if _, installed := rdsLoadAuroraDataPlane(clusterID); installed {
		t.Fatal("the deleted cluster kept its data plane")
	}
	rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 25450)
	if _, installed := rdsLoadAuroraDataPlane(clusterID); !installed {
		t.Fatal("the cluster created under the freed identifier has no data plane")
	}
}
