package gcp_sdk_test

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/simready"

	"cloud.google.com/go/bigtable"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"cloud.google.com/go/spanner"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	spanneradmin "google.golang.org/api/spanner/v1"
	"google.golang.org/grpc"
)

// freePersistentSimPorts picks ports below the system's ephemeral range: a
// restart binds the same ports again, and an ephemeral port can go to any
// connection the suite opens while the first process is down.
func freePersistentSimPorts(t *testing.T) (int, int) {
	t.Helper()
	first := ephemeralPortRangeStart(t)
	const lowest = 10000
	require.Greater(t, first, lowest+1, "the ephemeral port range starts at %d, leaving no port below it", first)
	for {
		httpPort := lowest + rand.IntN(first-lowest-1)
		grpcPort := httpPort + 1
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
		if err != nil {
			continue
		}
		ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", grpcPort))
		ln.Close()
		if err != nil {
			continue
		}
		ln2.Close()
		return httpPort, grpcPort
	}
}

func ephemeralPortRangeStart(t *testing.T) int {
	t.Helper()
	var raw string
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
		require.NoError(t, err)
		raw = strings.Fields(string(b))[0]
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "net.inet.ip.portrange.first").Output()
		require.NoError(t, err)
		raw = strings.TrimSpace(string(out))
	default:
		t.Fatalf("no ephemeral port range reader for %s", runtime.GOOS)
	}
	first, err := strconv.Atoi(raw)
	require.NoError(t, err)
	return first
}

// startPersistentSimulator launches an API-only simulator process with SQLite
// persistence on stateDir and waits for both listeners.
func startPersistentSimulator(t *testing.T, stateDir string, httpPort, grpcPort int) *exec.Cmd {
	t.Helper()
	return startPersistentSimulatorOn(t, "process", stateDir, httpPort, grpcPort)
}

// startPersistentSimulatorOn's runtime "process" serves the APIs alone;
// "docker" runs workloads on the container engine.
func startPersistentSimulatorOn(t *testing.T, runtime, stateDir string, httpPort, grpcPort int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binaryPath)
	cmd.Env = append(append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", httpPort),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", grpcPort),
		"SIM_RUNTIME="+runtime,
		"SIM_PERSIST=true",
		"SIM_DATA_DIR="+stateDir,
		"SIM_LOG_LEVEL=warn",
	), simGRPCEnv()...)
	cmd.Stdout = os.Stdout
	// The gRPC listener binds before the HTTP one announces itself.
	require.NoError(t, simready.Start(cmd, os.Stderr))
	return cmd
}

// shutdownPersistentSimulator sends SIGTERM, which the simulator's graceful
// shutdown handles, and waits.
func shutdownPersistentSimulator(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal simulator shutdown: %w", err)
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("wait for simulator shutdown: %w", err)
		}
		return nil
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("simulator did not stop within 15 seconds")
	}
}

// persistentAuthHTTPClient attaches an access token the dedicated process at
// endpoint minted, as simAuthHTTPClient does for the shared process.
func persistentAuthHTTPClient(endpoint string) *http.Client {
	return &http.Client{Transport: &simAuthTransport{
		base: http.DefaultTransport,
		host: strings.TrimPrefix(endpoint, "http://"),
		ts:   simTokenSourceFor(endpoint),
	}}
}

// TestGCPServiceStateSurvivesSimulatorRestart_SDK: Cloud Storage object bytes,
// a committed Cloud Spanner row, a Cloud Bigtable row, a Pub/Sub snapshot's
// backlog, a Compute Engine operation name and a pre-restart bearer token all
// survive a restart on the same data directory.
func TestGCPServiceStateSurvivesSimulatorRestart_SDK(t *testing.T) {
	const project = "persist-proj"
	stateDir := t.TempDir()
	httpPort, grpcPort := freePersistentSimPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	host := fmt.Sprintf("127.0.0.1:%d", httpPort)
	myGRPCAddr := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	cmd := startPersistentSimulator(t, stateDir, httpPort, grpcPort)
	t.Cleanup(func() { _ = shutdownPersistentSimulator(cmd) })

	// The Cloud Storage client reads the endpoint variable it honours for Google's
	// own emulator; the Spanner and Bigtable clients take theirs as an option.
	t.Setenv("STORAGE_EMULATOR_HOST", host)

	const (
		bucketName    = "persist-bucket"
		objectName    = "survivor.txt"
		objectContent = "gcs bytes survived restart"
	)
	resourceManager, err := crm.NewService(ctx, option.WithEndpoint(endpoint), option.WithHTTPClient(persistentAuthHTTPClient(endpoint)))
	require.NoError(t, err)
	_, err = resourceManager.Projects.Create(&crm.Project{ProjectId: project, Parent: "organizations/123456789012"}).Do()
	require.NoError(t, err)
	gcsClient, err := storage.NewClient(ctx, option.WithHTTPClient(persistentAuthHTTPClient(endpoint)))
	require.NoError(t, err)
	require.NoError(t, gcsClient.Bucket(bucketName).Create(ctx, project, nil))
	objWriter := gcsClient.Bucket(bucketName).Object(objectName).NewWriter(ctx)
	_, err = objWriter.Write([]byte(objectContent))
	require.NoError(t, err)
	require.NoError(t, objWriter.Close())
	require.NoError(t, gcsClient.Close())

	const (
		spannerInstance = "persist-inst"
		spannerDBID     = "persist-db"
		spannerDatabase = "projects/" + project + "/instances/" + spannerInstance + "/databases/" + spannerDBID
	)
	spannerAdmin, err := spanneradmin.NewService(ctx,
		serviceHostOptionsVia(endpoint, simTokenSourceFor(endpoint), spannerHost)...)
	require.NoError(t, err)
	_, err = spannerAdmin.Projects.Instances.Create("projects/"+project, &spanneradmin.CreateInstanceRequest{
		InstanceId: spannerInstance,
		Instance:   &spanneradmin.Instance{DisplayName: spannerInstance, NodeCount: 1},
	}).Do()
	require.NoError(t, err)
	_, err = spannerAdmin.Projects.Instances.Databases.Create(
		fmt.Sprintf("projects/%s/instances/%s", project, spannerInstance),
		&spanneradmin.CreateDatabaseRequest{CreateStatement: "CREATE DATABASE `" + spannerDBID + "`"},
	).Do()
	require.NoError(t, err)
	_, err = spannerAdmin.Projects.Instances.Databases.UpdateDdl(spannerDatabase, &spanneradmin.UpdateDatabaseDdlRequest{
		Statements: []string{"CREATE TABLE Survivors (Id INT64 NOT NULL, Name STRING(100)) PRIMARY KEY (Id)"},
	}).Do()
	require.NoError(t, err)

	spannerClient, err := spanner.NewClientWithConfig(ctx, spannerDatabase, spannerClientConfig, simGRPCClientOptionsAt(myGRPCAddr, simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	_, err = spannerClient.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("Survivors", []string{"Id", "Name"}, []interface{}{int64(1), "restart"}),
	})
	require.NoError(t, err)
	spannerClient.Close()

	const (
		btInstance = "persist-bt"
		btTable    = "events"
		btFamily   = "cf"
	)
	btInstanceAdmin, err := bigtable.NewInstanceAdminClient(ctx, project, simGRPCClientOptionsAt(myGRPCAddr, simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	require.NoError(t, btInstanceAdmin.CreateInstance(ctx, &bigtable.InstanceConf{
		InstanceId:  btInstance,
		DisplayName: btInstance,
		ClusterId:   "c1",
		Zone:        "us-east1-b",
		NumNodes:    1,
	}))
	require.NoError(t, btInstanceAdmin.Close())
	btAdmin, err := bigtable.NewAdminClient(ctx, project, btInstance, simGRPCClientOptionsAt(myGRPCAddr, simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	require.NoError(t, btAdmin.CreateTable(ctx, btTable))
	require.NoError(t, btAdmin.CreateColumnFamily(ctx, btTable, btFamily))
	require.NoError(t, btAdmin.Close())
	btClient, err := bigtable.NewClientWithConfig(ctx, project, btInstance, bigtableClientConfig, simGRPCClientOptionsAt(myGRPCAddr, simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	btMut := bigtable.NewMutation()
	btMut.Set(btFamily, "name", bigtable.Now(), []byte("alice"))
	require.NoError(t, btClient.Open(btTable).Apply(ctx, "user#1", btMut))
	require.NoError(t, btClient.Close())

	const (
		psTopic    = "projects/" + project + "/topics/persist-topic"
		psSub      = "projects/" + project + "/subscriptions/persist-sub"
		psSnapshot = "projects/" + project + "/snapshots/persist-snap"
		psPayload  = "snapshot replay survived restart"
	)
	psConn, err := grpc.NewClient(myGRPCAddr, simGRPCDialOptions(simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	publisher := pubsubpb.NewPublisherClient(psConn)
	subscriber := pubsubpb.NewSubscriberClient(psConn)
	_, err = publisher.CreateTopic(ctx, &pubsubpb.Topic{Name: psTopic})
	require.NoError(t, err)
	_, err = subscriber.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: psSub, Topic: psTopic, AckDeadlineSeconds: 60,
	})
	require.NoError(t, err)
	_, err = publisher.Publish(ctx, &pubsubpb.PublishRequest{
		Topic:    psTopic,
		Messages: []*pubsubpb.PubsubMessage{{Data: []byte(psPayload)}},
	})
	require.NoError(t, err)
	// Snapshot the backlog, then drain the queue, so only a snapshot seek can
	// bring the message back after the restart.
	_, err = subscriber.CreateSnapshot(ctx, &pubsubpb.CreateSnapshotRequest{
		Name: psSnapshot, Subscription: psSub,
	})
	require.NoError(t, err)
	pulled, err := subscriber.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: psSub, MaxMessages: 10, ReturnImmediately: true,
	})
	require.NoError(t, err)
	require.Len(t, pulled.GetReceivedMessages(), 1)
	_, err = subscriber.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{
		Subscription: psSub, AckIds: []string{pulled.GetReceivedMessages()[0].GetAckId()},
	})
	require.NoError(t, err)
	require.NoError(t, psConn.Close())

	computeSvc, err := compute.NewService(ctx,
		option.WithEndpoint(endpoint+"/compute/v1/"),
		option.WithTokenSource(simTokenSourceFor(endpoint)))
	require.NoError(t, err)
	firewallOp, err := computeSvc.Firewalls.Insert(project, &compute.Firewall{
		Name:    "persist-firewall",
		Allowed: []*compute.FirewallAllowed{{IPProtocol: "tcp", Ports: []string{"22"}}},
	}).Do()
	require.NoError(t, err)
	operationName := firewallOp.Name
	require.NotEmpty(t, operationName)

	preRestartToken, err := simTokenFetcher{base: endpoint}.Token()
	require.NoError(t, err)

	require.NoError(t, shutdownPersistentSimulator(cmd),
		"persistent simulator must exit cleanly before SQLite is reopened")
	cmd = startPersistentSimulator(t, stateDir, httpPort, grpcPort)

	gcsClient, err = storage.NewClient(ctx, option.WithHTTPClient(persistentAuthHTTPClient(endpoint)))
	require.NoError(t, err)
	defer gcsClient.Close()
	objReader, err := gcsClient.Bucket(bucketName).Object(objectName).NewReader(ctx)
	require.NoError(t, err, "the persisted object must serve its payload, not just its metadata")
	persistedBytes, err := io.ReadAll(objReader)
	require.NoError(t, err)
	require.NoError(t, objReader.Close())
	assert.Equal(t, objectContent, string(persistedBytes))

	spannerClient, err = spanner.NewClientWithConfig(ctx, spannerDatabase, spannerClientConfig, simGRPCClientOptionsAt(myGRPCAddr, simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	defer spannerClient.Close()
	row, err := spannerClient.Single().ReadRow(ctx, "Survivors", spanner.Key{int64(1)}, []string{"Name"})
	require.NoError(t, err, "the committed row must survive the restart")
	var persistedName string
	require.NoError(t, row.Columns(&persistedName))
	assert.Equal(t, "restart", persistedName)

	btClient, err = bigtable.NewClientWithConfig(ctx, project, btInstance, bigtableClientConfig, simGRPCClientOptionsAt(myGRPCAddr, simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	defer btClient.Close()
	persistedRow, err := btClient.Open(btTable).ReadRow(ctx, "user#1")
	require.NoError(t, err)
	require.Len(t, persistedRow[btFamily], 1, "the mutated row must survive the restart")
	assert.Equal(t, "alice", string(persistedRow[btFamily][0].Value))

	psConn, err = grpc.NewClient(myGRPCAddr, simGRPCDialOptions(simTokenSourceFor(endpoint))...)
	require.NoError(t, err)
	defer psConn.Close()
	subscriber = pubsubpb.NewSubscriberClient(psConn)
	_, err = subscriber.Seek(ctx, &pubsubpb.SeekRequest{
		Subscription: psSub,
		Target:       &pubsubpb.SeekRequest_Snapshot{Snapshot: psSnapshot},
	})
	require.NoError(t, err)
	replayed, err := subscriber.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: psSub, MaxMessages: 10, ReturnImmediately: true,
	})
	require.NoError(t, err)
	require.Len(t, replayed.GetReceivedMessages(), 1, "seek must replay the snapshot's captured backlog")
	assert.Equal(t, psPayload, string(replayed.GetReceivedMessages()[0].GetMessage().GetData()))

	computeSvc, err = compute.NewService(ctx,
		option.WithEndpoint(endpoint+"/compute/v1/"),
		option.WithTokenSource(simTokenSourceFor(endpoint)))
	require.NoError(t, err)
	persistedOp, err := computeSvc.GlobalOperations.Get(project, operationName).Do()
	require.NoError(t, err, "a pre-restart operation name must still resolve")
	assert.Equal(t, operationName, persistedOp.Name)
	assert.Equal(t, "DONE", persistedOp.Status)
	waitedOp, err := computeSvc.GlobalOperations.Wait(project, operationName).Do()
	require.NoError(t, err)
	assert.Equal(t, "DONE", waitedOp.Status)

	// The restarted simulator verifies the pre-restart token against the same
	// persisted signing key.
	tokenReq, err := http.NewRequest(http.MethodGet, endpoint+"/storage/v1/b/"+bucketName, nil)
	require.NoError(t, err)
	tokenReq.Header.Set("Authorization", "Bearer "+preRestartToken.AccessToken)
	tokenResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(tokenReq)
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	assert.Equal(t, http.StatusOK, tokenResp.StatusCode,
		"a token minted before the restart must verify against the persisted signing key")
}
