package main

import (
	"errors"
	"io"
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

func rdsFormCall(t *testing.T, handler http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func rdsResetAuroraStores(t *testing.T) {
	t.Helper()
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsClusters = sim.MakeStore[RDSCluster](nil, "rds_clusters")
	rdsClusterSnapshots = sim.MakeStore[RDSClusterSnapshot](nil, "rds_cluster_snapshots")
	rdsSnapshots = sim.MakeStore[RDSSnapshot](nil, "rds_snapshots")
	rdsEngineLogs = sim.MakeStore[RDSEngineLogHour](nil, "rds_engine_logs")
	rdsInstanceAutomatedBackups = sim.MakeStore[RDSInstanceAutomatedBackup](nil, "rds_instance_automated_backups")
	rdsClusterAutomatedBackups = sim.MakeStore[RDSClusterAutomatedBackup](nil, "rds_cluster_automated_backups")
	rdsReplicatedBackups = sim.MakeStore[RDSInstanceAutomatedBackup](nil, "rds_replicated_automated_backups")
	kmsKeyMaterial = sim.MakeStore[[]byte](nil, "kms_key_material")
}

func rdsCreateAuroraCluster(t *testing.T, clusterID, engine string, port int) RDSCluster {
	t.Helper()
	created := rdsFormCall(t, handleRDSCreateCluster, url.Values{
		"DBClusterIdentifier": {clusterID},
		"Engine":              {engine},
		"MasterUsername":      {"admin"},
		"MasterUserPassword":  {"MasterPassword-1"},
		"DatabaseName":        {"appdb"},
		"Port":                {strconv.Itoa(port)},
	})
	if created.Code != http.StatusOK {
		t.Fatalf("CreateDBCluster = %d %s", created.Code, created.Body.String())
	}
	t.Cleanup(func() {
		bg.Await()
		_ = rdsStopAuroraDataPlane(clusterID, true)
		rdsRemoveAuroraClusterVolume(clusterID)
		if cluster, ok := rdsClusters.Get(clusterID); ok {
			rdsRemoveAutomatedBackups(cluster)
		}
	})
	// Aurora takes the new cluster's first automated backup in the
	// background, starting its engine where a container runtime is present.
	bg.Await()
	cluster, _ := rdsClusters.Get(clusterID)
	return cluster
}

func rdsCreateAuroraMember(t *testing.T, clusterID, instanceID, engine string) RDSInstance {
	t.Helper()
	created := rdsFormCall(t, handleRDSCreate, url.Values{
		"DBInstanceIdentifier": {instanceID},
		"DBClusterIdentifier":  {clusterID},
		"Engine":               {engine},
		"DBInstanceClass":      {"db.r6g.large"},
	})
	if created.Code != http.StatusOK {
		t.Fatalf("CreateDBInstance = %d %s", created.Code, created.Body.String())
	}
	t.Cleanup(func() { rdsCloseAuroraInstanceEndpoint(instanceID) })
	member, _ := rdsInstances.Get(instanceID)
	return member
}

// Amazon Aurora starts and stops a member only through its cluster, and
// deletes a cluster only once its instances are gone.
func TestRDSAuroraMembersFollowTheirCluster(t *testing.T) {
	rdsResetAuroraStores(t)
	clusterID, memberID := "aurora-lifecycle", "aurora-lifecycle-1"
	cluster := rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 25431)
	rdsCreateAuroraMember(t, clusterID, memberID, "aurora-postgresql")

	for name, handler := range map[string]http.HandlerFunc{"StopDBInstance": handleRDSStopInstance, "StartDBInstance": handleRDSStartInstance} {
		answer := rdsLifecycleCall(t, handler, memberID)
		if answer.Code != http.StatusBadRequest || !strings.Contains(answer.Body.String(), "<Code>InvalidDBClusterStateFault</Code>") ||
			!strings.Contains(answer.Body.String(), "member of DB cluster "+clusterID) {
			t.Fatalf("%s on a cluster member = %d %s, want 400 InvalidDBClusterStateFault", name, answer.Code, answer.Body.String())
		}
		if stored, _ := rdsInstances.Get(memberID); stored.DBInstanceStatus != "available" {
			t.Fatalf("%s moved the member to %q", name, stored.DBInstanceStatus)
		}
	}

	deleteCluster := url.Values{"DBClusterIdentifier": {clusterID}, "SkipFinalSnapshot": {"true"}}
	refused := rdsFormCall(t, handleRDSDeleteCluster, deleteCluster)
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "<Code>InvalidDBClusterStateFault</Code>") ||
		!strings.Contains(refused.Body.String(), "still contains DB instances") {
		t.Fatalf("DeleteDBCluster with a member = %d %s, want 400 InvalidDBClusterStateFault", refused.Code, refused.Body.String())
	}
	if _, ok := rdsClusters.Get(clusterID); !ok {
		t.Fatal("the refused DeleteDBCluster removed the cluster")
	}

	deleteMember := rdsFormCall(t, handleRDSDelete, url.Values{"DBInstanceIdentifier": {memberID}, "SkipFinalSnapshot": {"true"}})
	if deleteMember.Code != http.StatusOK {
		t.Fatalf("DeleteDBInstance = %d %s", deleteMember.Code, deleteMember.Body.String())
	}
	deleted := rdsFormCall(t, handleRDSDeleteCluster, deleteCluster)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), "<Status>deleting</Status>") {
		t.Fatalf("DeleteDBCluster without members = %d %s, want 200 deleting", deleted.Code, deleted.Body.String())
	}
	if _, ok := rdsClusters.Get(clusterID); ok {
		t.Fatal("the deleted cluster is still listed")
	}
	if conn, err := net.Dial("tcp", net.JoinHostPort(cluster.Endpoint, strconv.Itoa(cluster.Port))); err == nil {
		_ = conn.Close()
		t.Fatal("the deleted cluster's endpoint still accepts clients")
	}
}

// A Multi-AZ DB cluster's members terminate with the cluster.
func TestRDSDeleteMultiAZDBClusterTerminatesItsMembers(t *testing.T) {
	rdsResetAuroraStores(t)
	clusterID, memberID := "multi-az-cluster", "multi-az-cluster-1"
	rdsClusters.Put(clusterID, RDSCluster{DBClusterIdentifier: clusterID, Engine: "postgres", Status: "available"})
	rdsInstances.Put(memberID, RDSInstance{DBInstanceIdentifier: memberID, Engine: "postgres", DBInstanceStatus: "available", DBClusterIdentifier: clusterID})
	deleted := rdsFormCall(t, handleRDSDeleteCluster, url.Values{"DBClusterIdentifier": {clusterID}, "SkipFinalSnapshot": {"true"}})
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), "<DBInstanceIdentifier>"+memberID+"</DBInstanceIdentifier>") {
		t.Fatalf("DeleteDBCluster = %d %s, want 200 listing the member it terminates", deleted.Code, deleted.Body.String())
	}
	if _, ok := rdsInstances.Get(memberID); ok {
		t.Fatal("the Multi-AZ DB cluster's member outlived the cluster")
	}
}

// rdsStandInEngine answers each client on address with label followed by
// the client's bytes, so a test sees which engine entrance a relay reached.
func rdsStandInEngine(t *testing.T, address, label string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := conn.Write([]byte(label)); err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
}

func rdsExpectEcho(t *testing.T, endpoint, label string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", endpoint, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write to %s: %v", endpoint, err)
	}
	reply := make([]byte, len(label)+4)
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != label+"ping" {
		t.Fatalf("%s relayed %q, %v; want %q", endpoint, reply, err, label+"ping")
	}
}

func rdsExpectRefused(t *testing.T, endpoint string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("ping"))
	reply := make([]byte, 4)
	n, err := conn.Read(reply)
	var timeout net.Error
	if n != 0 || err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("%s answered %q, %v; want the connection closed", endpoint, reply[:n], err)
	}
}

// The cluster, reader and instance endpoints of an Aurora cluster all reach
// the one engine over the cluster volume, and only while an instance serves
// them. The cluster endpoint and the writer's instance endpoint enter it
// read-write; the reader endpoint and an Aurora Replica's instance endpoint
// enter it read-only, except that the reader endpoint reaches the writer while
// the cluster has no Aurora Replica.
func TestRDSAuroraEndpointsServeTheClusterEngine(t *testing.T) {
	rdsResetAuroraStores(t)
	clusterID := "aurora-endpoints"
	cluster := rdsCreateAuroraCluster(t, clusterID, "aurora-postgresql", 25432)
	writerEndpoint := net.JoinHostPort(cluster.Endpoint, strconv.Itoa(cluster.Port))
	readerEndpoint := net.JoinHostPort(cluster.ReaderEndpoint, strconv.Itoa(cluster.Port))
	if net.ParseIP(cluster.Endpoint) == nil || net.ParseIP(cluster.ReaderEndpoint) == nil || cluster.Endpoint == cluster.ReaderEndpoint {
		t.Fatalf("cluster endpoints %q and %q, want two distinct addresses", cluster.Endpoint, cluster.ReaderEndpoint)
	}
	plane, ok := rdsLoadAuroraDataPlane(clusterID)
	if !ok {
		t.Fatal("the Aurora cluster has no data plane")
	}
	environment, err := plane.environment()
	if err != nil || environment["POSTGRES_USER"] != "admin" || environment["POSTGRES_PASSWORD"] != "MasterPassword-1" || environment["POSTGRES_DB"] != "appdb" {
		t.Fatalf("engine environment %v, %v; want the cluster's master user and database", environment, err)
	}
	if plane.engine.Engine.Family != dbengine.Postgres || plane.engine.Volume != rdsClusterVolume(clusterID) {
		t.Fatalf("engine %s on volume %s, want PostgreSQL on the cluster volume", plane.engine.Engine.Family, plane.engine.Volume)
	}
	if !plane.authenticate("admin", "MasterPassword-1", false) || plane.authenticate("admin", "wrong-password", false) {
		t.Fatal("the cluster engine does not authenticate by the cluster's master-user password")
	}

	// Stand echo services in for the engine's read-write and read-only
	// entrances so the relays are observable without a container runtime.
	if err := plane.engine.Close(); err != nil {
		t.Fatal(err)
	}
	rdsStandInEngine(t, plane.engineAddress, "read-write:")
	rdsStandInEngine(t, plane.replicaAddress, "read-only:")

	rdsExpectRefused(t, writerEndpoint)
	rdsExpectRefused(t, readerEndpoint)

	writer := rdsCreateAuroraMember(t, clusterID, "aurora-endpoints-1", "aurora-postgresql")
	rdsExpectEcho(t, readerEndpoint, "read-write:")
	replica := rdsCreateAuroraMember(t, clusterID, "aurora-endpoints-2", "aurora-postgresql")
	if writer.Port != cluster.Port || writer.MasterUsername != "admin" || writer.Endpoint == cluster.Endpoint {
		t.Fatalf("member endpoint %s:%d user %q, want its own address on the cluster port with the cluster's master user", writer.Endpoint, writer.Port, writer.MasterUsername)
	}
	described := rdsClusterCall(t, handleRDSDescribeClusters, clusterID).Body.String()
	if !strings.Contains(described, "<DBInstanceIdentifier>aurora-endpoints-1</DBInstanceIdentifier><IsClusterWriter>true</IsClusterWriter>") ||
		!strings.Contains(described, "<DBInstanceIdentifier>aurora-endpoints-2</DBInstanceIdentifier><IsClusterWriter>false</IsClusterWriter>") {
		t.Fatalf("DescribeDBClusters %s, want the first member as writer", described)
	}
	for endpoint, label := range map[string]string{
		writerEndpoint: "read-write:",
		readerEndpoint: "read-only:",
		net.JoinHostPort(writer.Endpoint, strconv.Itoa(writer.Port)):   "read-write:",
		net.JoinHostPort(replica.Endpoint, strconv.Itoa(replica.Port)): "read-only:",
	} {
		rdsExpectEcho(t, endpoint, label)
	}

	stop := rdsClusterCall(t, handleRDSStopCluster, clusterID)
	if stop.Code != http.StatusOK {
		t.Fatalf("StopDBCluster = %d %s", stop.Code, stop.Body.String())
	}
	bg.Await()
	if stored, _ := rdsClusters.Get(clusterID); stored.Status != "stopped" {
		t.Fatalf("cluster %q after its engine stopped, want stopped", stored.Status)
	}
	if _, installed := rdsLoadAuroraDataPlane(clusterID); installed {
		t.Fatal("the stopped cluster still has its data plane")
	}
	for _, endpoint := range []string{writerEndpoint, readerEndpoint, net.JoinHostPort(writer.Endpoint, strconv.Itoa(writer.Port))} {
		if conn, err := net.Dial("tcp", endpoint); err == nil {
			_ = conn.Close()
			t.Fatalf("%s accepts clients while the cluster is stopped", endpoint)
		}
	}

	start := rdsClusterCall(t, handleRDSStartCluster, clusterID)
	if start.Code != http.StatusOK {
		t.Fatalf("StartDBCluster = %d %s", start.Code, start.Body.String())
	}
	bg.Await()
	restarted, _ := rdsClusters.Get(clusterID)
	if restarted.Status != "available" || restarted.Endpoint != cluster.Endpoint || restarted.ReaderEndpoint != cluster.ReaderEndpoint {
		t.Fatalf("restarted cluster %q at %s/%s, want available at %s/%s", restarted.Status, restarted.Endpoint, restarted.ReaderEndpoint, cluster.Endpoint, cluster.ReaderEndpoint)
	}
	// A client would start the engine container; binding the address shows
	// the endpoint serves again without one.
	for _, endpoint := range []string{writerEndpoint, readerEndpoint, net.JoinHostPort(writer.Endpoint, strconv.Itoa(writer.Port))} {
		if listener, err := net.Listen("tcp", endpoint); err == nil {
			_ = listener.Close()
			t.Fatalf("nothing serves %s after StartDBCluster", endpoint)
		}
	}
}

// An Aurora member names its cluster's engine, and CreateDBCluster refuses an
// Aurora cluster without its master-user password.
func TestRDSAuroraRequestsFollowTheCluster(t *testing.T) {
	rdsResetAuroraStores(t)
	rdsCreateAuroraCluster(t, "aurora-mysql-cluster", "aurora-mysql", 23306)
	if plane, ok := rdsLoadAuroraDataPlane("aurora-mysql-cluster"); !ok || plane.engine.Engine.Family != dbengine.MySQL || plane.engine.Engine.Image == "" {
		t.Fatal("the Aurora MySQL cluster does not run a MySQL engine")
	}
	mismatched := rdsFormCall(t, handleRDSCreate, url.Values{
		"DBInstanceIdentifier": {"aurora-mysql-wrong"},
		"DBClusterIdentifier":  {"aurora-mysql-cluster"},
		"Engine":               {"mysql"},
	})
	if mismatched.Code != http.StatusBadRequest || !strings.Contains(mismatched.Body.String(), "<Code>InvalidParameterCombination</Code>") {
		t.Fatalf("CreateDBInstance with another engine = %d %s, want InvalidParameterCombination", mismatched.Code, mismatched.Body.String())
	}
	if _, ok := rdsInstances.Get("aurora-mysql-wrong"); ok {
		t.Fatal("the refused member was recorded")
	}
	missing := rdsFormCall(t, handleRDSCreateCluster, url.Values{
		"DBClusterIdentifier": {"aurora-no-password"},
		"Engine":              {"aurora-postgresql"},
		"MasterUsername":      {"admin"},
	})
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "<Code>InvalidParameterValue</Code>") {
		t.Fatalf("CreateDBCluster without a password = %d %s, want InvalidParameterValue", missing.Code, missing.Body.String())
	}
	if _, ok := rdsClusters.Get("aurora-no-password"); ok {
		t.Fatal("the refused cluster was recorded")
	}
}

// An Aurora MySQL endpoint keeps IAM database authentication for a password
// shaped as a presigned rds-db:connect request and relays every other one to
// the engine.
func TestRDSIsIAMAuthToken(t *testing.T) {
	for password, want := range map[string]bool{
		"db.example:3306/?Action=connect&DBUser=app&X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=abc": true,
		"db.example:3306/?Action=connect&DBUser=app":                                                      false,
		"Plain-Password-1": false,
		"pass?word=1&x=2":  false,
	} {
		if got := rdsIsIAMAuthToken(password); got != want {
			t.Errorf("rdsIsIAMAuthToken(%q) = %t, want %t", password, got, want)
		}
	}
}
