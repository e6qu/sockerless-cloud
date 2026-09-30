package main

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRDSModifyDBClusterPasswordWaitsForTheEngine(t *testing.T) {
	rdsResetAuroraStores(t)
	clusterID := "aurora-modify-password"
	rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 0)
	plane, _ := rdsLoadAuroraDataPlane(clusterID)

	modified := rdsFormCall(t, handleRDSModifyCluster, url.Values{
		"DBClusterIdentifier": {clusterID}, "MasterUserPassword": {"Rotated-Password-2"},
	})
	if modified.Code != http.StatusOK {
		t.Fatalf("ModifyDBCluster = %d %s", modified.Code, modified.Body.String())
	}
	if !plane.authenticate("admin", "Rotated-Password-2", false) || plane.authenticate("admin", "MasterPassword-1", false) {
		t.Fatal("clients do not sign in with the new master password")
	}
	environment, err := plane.environment()
	if err != nil || environment["POSTGRES_PASSWORD"] != "MasterPassword-1" {
		t.Fatalf("engine environment %v, %v; the engine starts with the password it was initialised with and then installs the new one", environment, err)
	}
	cluster, _ := rdsClusters.Get(clusterID)
	if len(cluster.BackendMasterUserSecret) == 0 || string(cluster.BackendMasterUserSecret) == string(cluster.MasterUserSecret) {
		t.Fatal("the cluster does not record that its engine still holds the old password")
	}

	for _, bad := range []string{"short", "has/slash-in-it", `has"quote-in-it`, "has@at-in-it", strings.Repeat("a", 42)} {
		refused := rdsFormCall(t, handleRDSModifyCluster, url.Values{
			"DBClusterIdentifier": {clusterID}, "MasterUserPassword": {bad},
		})
		if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "InvalidParameterValue") {
			t.Fatalf("password %q: ModifyDBCluster = %d %s", bad, refused.Code, refused.Body.String())
		}
	}
}

func TestRDSModifyDBClusterMovesTheEndpointsToTheNewPort(t *testing.T) {
	rdsResetAuroraStores(t)
	clusterID := "aurora-modify-port"
	cluster := rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 0)
	member := rdsCreateAuroraMember(t, clusterID, clusterID+"-1", "aurora-postgresql")
	oldPort := cluster.Port

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	newPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	moved := rdsFormCall(t, handleRDSModifyCluster, url.Values{
		"DBClusterIdentifier": {clusterID}, "Port": {strconv.Itoa(newPort)},
	})
	if moved.Code != http.StatusOK || !strings.Contains(moved.Body.String(), "<Port>"+strconv.Itoa(newPort)+"</Port>") {
		t.Fatalf("ModifyDBCluster = %d %s", moved.Code, moved.Body.String())
	}
	stored, _ := rdsInstances.Get(member.DBInstanceIdentifier)
	if stored.Port != newPort {
		t.Fatalf("member port %d, want the cluster's new port %d", stored.Port, newPort)
	}
	for _, host := range []string{cluster.Endpoint, cluster.ReaderEndpoint, member.Endpoint} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(newPort)), 5*time.Second)
		if err != nil {
			t.Fatalf("%s does not listen on the new port: %v", host, err)
		}
		_ = conn.Close()
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(oldPort)), 5*time.Second); err == nil {
			_ = conn.Close()
			t.Fatalf("%s still listens on the old port %d", host, oldPort)
		}
	}

	for _, port := range []string{"80", "65536"} {
		refused := rdsFormCall(t, handleRDSModifyCluster, url.Values{"DBClusterIdentifier": {clusterID}, "Port": {port}})
		if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "InvalidParameterValue") {
			t.Fatalf("port %s: ModifyDBCluster = %d %s", port, refused.Code, refused.Body.String())
		}
	}
}

func TestRDSDeletionProtectionRefusesDeletes(t *testing.T) {
	rdsResetAuroraStores(t)
	clusterID := "aurora-protected"
	rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 0)
	if code := rdsFormCall(t, handleRDSModifyCluster, url.Values{
		"DBClusterIdentifier": {clusterID}, "DeletionProtection": {"true"},
	}).Code; code != http.StatusOK {
		t.Fatalf("ModifyDBCluster = %d", code)
	}
	refused := rdsFormCall(t, handleRDSDeleteCluster, url.Values{"DBClusterIdentifier": {clusterID}, "SkipFinalSnapshot": {"true"}})
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "InvalidParameterCombination") ||
		!strings.Contains(refused.Body.String(), "disable deletion protection") {
		t.Fatalf("DeleteDBCluster on a protected cluster = %d %s", refused.Code, refused.Body.String())
	}

	rdsClusters.Update(clusterID, func(c *RDSCluster) { c.Status = "stopped" })
	busy := rdsFormCall(t, handleRDSModifyCluster, url.Values{"DBClusterIdentifier": {clusterID}, "DeletionProtection": {"false"}})
	if busy.Code != http.StatusBadRequest || !strings.Contains(busy.Body.String(), "InvalidDBClusterStateFault") {
		t.Fatalf("ModifyDBCluster on a stopped cluster = %d %s", busy.Code, busy.Body.String())
	}

	rdsInstances.Put("protected-instance", RDSInstance{DBInstanceIdentifier: "protected-instance", DeletionProtection: true, DBInstanceStatus: "available"})
	refusedInstance := rdsFormCall(t, handleRDSDelete, url.Values{"DBInstanceIdentifier": {"protected-instance"}, "SkipFinalSnapshot": {"true"}})
	if refusedInstance.Code != http.StatusBadRequest || !strings.Contains(refusedInstance.Body.String(), "disable deletion protection") {
		t.Fatalf("DeleteDBInstance on a protected instance = %d %s", refusedInstance.Code, refusedInstance.Body.String())
	}
}
