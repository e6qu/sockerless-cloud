package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// TestRDSLifecycleActionsRequireTheirSourceState pins Amazon RDS's
// InvalidDBInstanceState answer for a lifecycle action on an instance that is
// not in the state the action runs from — starting an instance that is
// already available used to try to bind its endpoint a second time.
func TestRDSLifecycleActionsRequireTheirSourceState(t *testing.T) {
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	for _, testCase := range []struct {
		name    string
		status  string
		handler http.HandlerFunc
	}{
		{name: "StartDBInstance on an available instance", status: "available", handler: handleRDSStartInstance},
		{name: "StopDBInstance on a stopped instance", status: "stopped", handler: handleRDSStopInstance},
		{name: "RebootDBInstance on a stopped instance", status: "stopped", handler: handleRDSReboot},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			id := "lifecycle-state-db"
			rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: testCase.status})
			t.Cleanup(func() { rdsInstances.Delete(id) })
			form := url.Values{"DBInstanceIdentifier": {id}}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			testCase.handler(recorder, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "<Code>InvalidDBInstanceState</Code>") {
				t.Fatalf("answer = %d %s, want 400 InvalidDBInstanceState", recorder.Code, recorder.Body.String())
			}
			if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != testCase.status {
				t.Fatalf("status moved to %q on a refused action", stored.DBInstanceStatus)
			}
		})
	}
}

// Amazon RDS takes no ModifyDBInstance, CreateDBSnapshot,
// CreateDBInstanceReadReplica or RestoreDBInstanceToPointInTime on an instance
// it is still creating, starting or taking the first automated backup of.
func TestRDSInstanceRefusesActionsUntilAvailable(t *testing.T) {
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsSnapshots = sim.MakeStore[RDSSnapshot](nil, "rds_snapshots")
	id := "bringing-up-db"
	for _, status := range []string{"creating", "starting", "backing-up"} {
		rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: status, BackupRetentionPeriod: 1})
		for name, call := range map[string]*httptest.ResponseRecorder{
			"RestoreDBInstanceToPointInTime": rdsFormCall(t, handleRDSRestoreInstanceToPointInTime, url.Values{
				"SourceDBInstanceIdentifier": {id}, "TargetDBInstanceIdentifier": {id + "-pit"}, "UseLatestRestorableTime": {"true"},
			}),
			"ModifyDBInstance": rdsFormCall(t, handleRDSModify, url.Values{"DBInstanceIdentifier": {id}, "DBInstanceClass": {"db.t3.small"}}),
			"CreateDBSnapshot": rdsFormCall(t, handleRDSCreateSnapshot, url.Values{"DBInstanceIdentifier": {id}, "DBSnapshotIdentifier": {id + "-snap"}}),
			"CreateDBInstanceReadReplica": rdsFormCall(t, handleRDSCreateReadReplica, url.Values{
				"DBInstanceIdentifier": {id + "-replica"}, "SourceDBInstanceIdentifier": {id},
			}),
		} {
			if call.Code != http.StatusBadRequest || !strings.Contains(call.Body.String(), "Instance bringing-up-db is not in available state.") {
				t.Fatalf("%s on a %s instance = %d %s, want InvalidDBInstanceState", name, status, call.Code, call.Body.String())
			}
		}
		if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != status || stored.DBInstanceClass != "" {
			t.Fatalf("a refused action changed the %s instance: %+v", status, stored)
		}
		if _, taken := rdsSnapshots.Get(id + "-snap"); taken {
			t.Fatalf("CreateDBSnapshot recorded a snapshot of a %s instance", status)
		}
		if _, created := rdsInstances.Get(id + "-replica"); created {
			t.Fatalf("CreateDBInstanceReadReplica created a replica of a %s instance", status)
		}
		if _, restored := rdsInstances.Get(id + "-pit"); restored {
			t.Fatalf("RestoreDBInstanceToPointInTime restored a %s instance", status)
		}
	}
}

// Amazon RDS reports an instance it creates or starts backing-up while it
// takes the first automated backup, and available once the instance is up; a
// later automated backup leaves an available instance available.
func TestRDSInstanceBringUpReportsBackingUpThenAvailable(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsSnapshots = sim.MakeStore[RDSSnapshot](nil, "rds_snapshots")
	id := "bring-up-db"
	owner := rdsInstanceBackups{instanceID: id}
	for _, testCase := range []struct{ from, recorded string }{
		{from: "creating", recorded: "backing-up"},
		{from: "starting", recorded: "backing-up"},
		{from: "available", recorded: "available"},
	} {
		rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, DbiResourceId: "db-BRINGUP", Engine: "postgres", DBInstanceStatus: testCase.from})
		snapshotID := rdsAutomatedSnapshotID(id, time.Now()) + "-" + testCase.from
		if !owner.recordAutomatedSnapshot(snapshotID, time.Now()) {
			t.Fatalf("the %s instance recorded no automated snapshot", testCase.from)
		}
		if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != testCase.recorded {
			t.Fatalf("a %s instance taking an automated backup reports %q, want %q", testCase.from, stored.DBInstanceStatus, testCase.recorded)
		}
		rdsFinishInstanceBringUp(id)
		if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != "available" {
			t.Fatalf("a %s instance lands %q, want available", testCase.from, stored.DBInstanceStatus)
		}
	}
}

func rdsLifecycleCall(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"DBInstanceIdentifier": {id}}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// StopDBInstance answers stopping and the instance lands stopped only once
// its engine has stopped and its endpoint no longer accepts clients.
func TestRDSStopDBInstanceStopsTheEngineBeforeItReportsStopped(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	id := "stopping-state-db"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	engine := &dbengine.Instance{Name: "Amazon RDS " + id, Engine: dbengine.Postgres16}
	engine.Serve(listener)
	rdsDataPlanes.Store(id, &rdsDataPlane{engine: engine, backups: &rdsAutomatedBackups{owner: rdsInstanceBackups{instanceID: id}, engine: engine, volume: rdsInstanceVolume(id)}})
	rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: "available"})
	t.Cleanup(func() {
		rdsInstances.Delete(id)
		rdsDataPlanes.Delete(id)
	})

	// Hold the instance's stop lock, as a stop already under way does, so the
	// engine cannot finish stopping until the test lets it.
	release := rdsDataPlaneStops.Lock(id)
	stop := rdsLifecycleCall(t, handleRDSStopInstance, id)
	if stop.Code != http.StatusOK || !strings.Contains(stop.Body.String(), "<DBInstanceStatus>stopping</DBInstanceStatus>") {
		release()
		t.Fatalf("StopDBInstance = %d %s, want 200 stopping", stop.Code, stop.Body.String())
	}
	start := rdsLifecycleCall(t, handleRDSStartInstance, id)
	if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != "stopping" {
		release()
		t.Fatalf("status %q while the engine is still stopping", stored.DBInstanceStatus)
	}
	release()
	if start.Code != http.StatusBadRequest || !strings.Contains(start.Body.String(), "Instance stopping-state-db is not in stopped state and cannot be started.") {
		t.Fatalf("StartDBInstance while stopping = %d %s, want InvalidDBInstanceState", start.Code, start.Body.String())
	}

	bg.Await()
	if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != "stopped" {
		t.Fatalf("status %q after the engine stopped, want stopped", stored.DBInstanceStatus)
	}
	if _, installed := rdsLoadDataPlane(id); installed {
		t.Fatal("the stopped instance still has its data plane")
	}
	if conn, err := net.Dial("tcp", listener.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("the stopped instance's endpoint still accepts clients")
	}
}

func rdsClusterCall(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"DBClusterIdentifier": {id}}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// StopDBCluster and StartDBCluster answer stopping and starting, and land the
// cluster stopped or available only once every member's engine endpoint has
// really closed or been reinstalled.
func TestRDSClusterStopAndStartFollowTheMembersEngines(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsClusters = sim.MakeStore[RDSCluster](nil, "rds_clusters")
	kmsKeyMaterial = sim.MakeStore[[]byte](nil, "kms_key_material")
	clusterID, memberID := "lifecycle-cluster", "lifecycle-cluster-writer"
	rdsClusters.Put(clusterID, RDSCluster{DBClusterIdentifier: clusterID, Engine: "postgres", Status: "available"})
	member := RDSInstance{DBInstanceIdentifier: memberID, Engine: "postgres", DBInstanceStatus: "available", DBClusterIdentifier: clusterID, MasterUsername: "admin"}
	if err := rdsInstallDataPlane(&member, "MasterPassword-1"); err != nil {
		t.Fatal(err)
	}
	rdsInstances.Put(memberID, member)
	t.Cleanup(func() {
		bg.Await()
		_ = rdsStopDataPlane(memberID, false)
	})
	endpoint := net.JoinHostPort(member.Endpoint, strconv.Itoa(member.Port))

	release := rdsDataPlaneStops.Lock(memberID)
	stop := rdsClusterCall(t, handleRDSStopCluster, clusterID)
	if stop.Code != http.StatusOK || !strings.Contains(stop.Body.String(), "<Status>stopping</Status>") ||
		!strings.Contains(stop.Body.String(), "<DBInstanceIdentifier>"+memberID+"</DBInstanceIdentifier><IsClusterWriter>true</IsClusterWriter>") {
		release()
		t.Fatalf("StopDBCluster = %d %s, want 200 stopping listing the writer", stop.Code, stop.Body.String())
	}
	start := rdsClusterCall(t, handleRDSStartCluster, clusterID)
	cluster, _ := rdsClusters.Get(clusterID)
	stored, _ := rdsInstances.Get(memberID)
	release()
	if cluster.Status != "stopping" || stored.DBInstanceStatus != "stopping" {
		t.Fatalf("cluster %q member %q while the member's engine is still stopping", cluster.Status, stored.DBInstanceStatus)
	}
	if start.Code != http.StatusBadRequest || !strings.Contains(start.Body.String(), "<Code>InvalidDBClusterStateFault</Code>") {
		t.Fatalf("StartDBCluster while stopping = %d %s, want InvalidDBClusterStateFault", start.Code, start.Body.String())
	}

	bg.Await()
	cluster, _ = rdsClusters.Get(clusterID)
	stored, _ = rdsInstances.Get(memberID)
	if cluster.Status != "stopped" || stored.DBInstanceStatus != "stopped" {
		t.Fatalf("after the engine stopped: cluster %q member %q, want both stopped", cluster.Status, stored.DBInstanceStatus)
	}
	if conn, err := net.Dial("tcp", endpoint); err == nil {
		_ = conn.Close()
		t.Fatal("the stopped member's endpoint still accepts clients")
	}

	start = rdsClusterCall(t, handleRDSStartCluster, clusterID)
	if start.Code != http.StatusOK || !strings.Contains(start.Body.String(), "<Status>starting</Status>") {
		t.Fatalf("StartDBCluster = %d %s, want 200 starting", start.Code, start.Body.String())
	}
	bg.Await()
	cluster, _ = rdsClusters.Get(clusterID)
	stored, _ = rdsInstances.Get(memberID)
	if cluster.Status != "available" || stored.DBInstanceStatus != "available" {
		t.Fatalf("after the engine started: cluster %q member %q, want both available", cluster.Status, stored.DBInstanceStatus)
	}
	if _, installed := rdsLoadDataPlane(memberID); !installed {
		t.Fatal("the started member has no engine endpoint")
	}
	if stored.Endpoint+":"+strconv.Itoa(stored.Port) != endpoint {
		t.Fatalf("member endpoint moved from %s to %s:%d", endpoint, stored.Endpoint, stored.Port)
	}
}
