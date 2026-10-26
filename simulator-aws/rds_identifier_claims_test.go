package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func rdsQueryRequest(form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// A CreateDBInstance repeated while the first still provisions, as the AWS SDK
// retries a request its HTTP client stopped waiting for, finds the identifier
// taken rather than provisioning a second engine on the instance's volume.
func TestRDSCreateRepeatedWhileProvisioningFindsTheIdentifierTaken(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	const id = "retried-create-db"
	releaseFirst, claimed := rdsInstanceIdentifiers.claim(id)
	if !claimed {
		t.Fatal("the first CreateDBInstance could not claim a free identifier")
	}
	recorder := httptest.NewRecorder()
	handleRDSCreate(recorder, rdsQueryRequest(url.Values{
		"DBInstanceIdentifier": {id},
		"DBInstanceClass":      {"db.t3.micro"},
		"Engine":               {"postgres"},
		"EngineVersion":        {"16.15"},
		"MasterUsername":       {"admin"},
		"MasterUserPassword":   {"password123!"},
		"AllocatedStorage":     {"20"},
	}))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "<Code>DBInstanceAlreadyExists</Code>") {
		t.Fatalf("repeated create answered %d %s, want 400 DBInstanceAlreadyExists", recorder.Code, recorder.Body.String())
	}
	if _, served := rdsLoadDataPlane(id); served {
		t.Fatal("the repeated create installed a second data plane")
	}
	if _, stored := rdsInstances.Get(id); stored {
		t.Fatal("the repeated create stored an instance")
	}

	rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: "creating"})
	t.Cleanup(func() { rdsInstances.Delete(id) })
	releaseFirst()
	if _, claimed := rdsInstanceIdentifiers.claim(id); claimed {
		t.Fatal("a stored instance's identifier was claimed again")
	}
}

func TestRDSCreateClusterRepeatedWhileProvisioningFindsTheIdentifierTaken(t *testing.T) {
	rdsClusters = sim.MakeStore[RDSCluster](nil, "rds_clusters")
	const id = "retried-create-cluster"
	releaseFirst, claimed := rdsClusterIdentifiers.claim(id)
	if !claimed {
		t.Fatal("the first CreateDBCluster could not claim a free identifier")
	}
	recorder := httptest.NewRecorder()
	handleRDSCreateCluster(recorder, rdsQueryRequest(url.Values{
		"DBClusterIdentifier": {id},
		"Engine":              {"aurora-postgresql"},
		"MasterUsername":      {"admin"},
		"MasterUserPassword":  {"password123!"},
	}))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "<Code>DBClusterAlreadyExistsFault</Code>") {
		t.Fatalf("repeated create answered %d %s, want 400 DBClusterAlreadyExistsFault", recorder.Code, recorder.Body.String())
	}
	if _, stored := rdsClusters.Get(id); stored {
		t.Fatal("the repeated create stored a cluster")
	}
	releaseFirst()
	releaseSecond, claimed := rdsClusterIdentifiers.claim(id)
	if !claimed {
		t.Fatal("a released identifier stayed claimed")
	}
	releaseSecond()
}

// A rename onto an identifier a create is still provisioning under finds the
// identifier taken, rather than landing beside the create and leaving
// whichever stores second to overwrite the other's record.
func TestRDSRenameOntoAProvisioningIdentifierFindsItTaken(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	const id, newID = "renamed-db", "provisioning-db"
	rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: "available", DbiResourceId: "db-RENAMED"})
	releaseCreate, claimed := rdsInstanceIdentifiers.claim(newID)
	if !claimed {
		t.Fatal("the create could not claim a free identifier")
	}
	defer releaseCreate()

	recorder := httptest.NewRecorder()
	handleRDSModify(recorder, rdsQueryRequest(url.Values{
		"DBInstanceIdentifier":    {id},
		"NewDBInstanceIdentifier": {newID},
		"ApplyImmediately":        {"true"},
	}))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "<Code>DBInstanceAlreadyExists</Code>") {
		t.Fatalf("rename answered %d %s, want 400 DBInstanceAlreadyExists", recorder.Code, recorder.Body.String())
	}
	stored, ok := rdsInstances.Get(id)
	if !ok || stored.DBInstanceStatus != "available" || stored.PendingModifications != nil || stored.Restart != nil {
		t.Fatalf("the refused rename changed the instance: %+v (stored %t)", stored, ok)
	}
	if _, moved := rdsInstances.Get(newID); moved {
		t.Fatal("the refused rename stored a record under the provisioning identifier")
	}
}

// A rename waiting for the maintenance window that comes due while a create
// provisions under its identifier keeps waiting.
func TestRDSPendingRenameWaitsWhileACreateProvisionsItsIdentifier(t *testing.T) {
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	const id, newID, resourceID = "maintained-db", "provisioning-target-db", "db-MAINTAINED"
	rdsInstances.Put(id, RDSInstance{
		DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: "available", DbiResourceId: resourceID,
		PendingModifications: &RDSPendingModifications{DBInstanceIdentifier: newID},
	})
	releaseCreate, claimed := rdsInstanceIdentifiers.claim(newID)
	if !claimed {
		t.Fatal("the create could not claim a free identifier")
	}
	defer releaseCreate()

	rdsRunMaintenance(id, resourceID)
	bg.Await()

	stored, ok := rdsInstances.Get(id)
	if !ok {
		t.Fatal("the instance moved off its identifier while a create held the new one")
	}
	if stored.PendingModifications == nil || stored.PendingModifications.DBInstanceIdentifier != newID || stored.DBInstanceStatus != "available" {
		t.Fatalf("the rename did not keep waiting: status %s, pending %+v", stored.DBInstanceStatus, stored.PendingModifications)
	}
	if _, moved := rdsInstances.Get(newID); moved {
		t.Fatal("the rename stored a record under the provisioning identifier")
	}
}
