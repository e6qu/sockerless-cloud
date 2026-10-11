package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func rdsQueryRequest(form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// A CreateDBInstance repeated while the first still provisions — the AWS SDK
// retries a request its HTTP client stopped waiting for — finds the identifier
// taken. Provisioning it a second time ran a second engine container on the
// instance's volume, whose initdb met the first one's files.
func TestRDSCreateRepeatedWhileProvisioningFindsTheIdentifierTaken(t *testing.T) {
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
