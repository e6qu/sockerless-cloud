package gcp_sdk_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/bigtable"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"cloud.google.com/go/spanner"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	spanneradmin "google.golang.org/api/spanner/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// freePersistentSimPorts allocates the HTTP and gRPC ports for a dedicated
// simulator process. Both listeners stay open while both ports are chosen so
// the OS cannot hand the same port out twice.
func freePersistentSimPorts(t *testing.T) (int, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpPort := ln.Addr().(*net.TCPAddr).Port
	grpcPort := ln2.Addr().(*net.TCPAddr).Port
	ln.Close()
	ln2.Close()
	return httpPort, grpcPort
}

// startPersistentSimulator launches its own simulator process with SQLite
// persistence on stateDir, waiting for both the HTTP and gRPC listeners.
func startPersistentSimulator(t *testing.T, stateDir string, httpPort, grpcPort int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", httpPort),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", grpcPort),
		"SIM_RUNTIME=process",
		"SIM_PERSIST=true",
		"SIM_DATA_DIR="+stateDir,
		"SIM_LOG_LEVEL=warn",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	require.NoError(t, waitForHealth(fmt.Sprintf("http://127.0.0.1:%d/health", httpPort)))
	require.NoError(t, waitForTCP(fmt.Sprintf("127.0.0.1:%d", grpcPort)))
	return cmd
}

// waitForTCP waits until addr accepts a TCP connection — the gRPC server
// starts concurrently with the HTTP listener the health check covers.
func waitForTCP(addr string) error {
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

// shutdownPersistentSimulator asks the process to exit via SIGTERM (the
// signal the simulator's graceful-shutdown path handles) and waits for it.
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

// persistentAuthHTTPClient returns an HTTP client that attaches an access
// token minted by the dedicated simulator process at endpoint — the same
// credential path simAuthHTTPClient provides for the shared process.
func persistentAuthHTTPClient(endpoint string) *http.Client {
	return &http.Client{Transport: &simAuthTransport{
		base: http.DefaultTransport,
		host: strings.TrimPrefix(endpoint, "http://"),
		ts:   simTokenSourceFor(endpoint),
	}}
}

// TestGCPServiceStateSurvivesSimulatorRestart_SDK exercises cloud state that
// must survive a simulator restart on the same data directory: GCS object
// bytes, a committed Cloud Spanner row, a Cloud Bigtable row, a Cloud Pub/Sub
// snapshot's captured backlog, a Compute Engine operation name, and a bearer
// token minted before the restart. Everything runs through the official Go
// SDKs, differing from the real cloud only in coordinates.
func TestGCPServiceStateSurvivesSimulatorRestart_SDK(t *testing.T) {
	const project = "persist-proj"
	stateDir := t.TempDir()
	httpPort, grpcPort := freePersistentSimPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	host := fmt.Sprintf("127.0.0.1:%d", httpPort)
	myGRPCAddr := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	cmd := startPersistentSimulator(t, stateDir, httpPort, grpcPort)
	t.Cleanup(func() { _ = shutdownPersistentSimulator(cmd) })

	// The Google Cloud clients read the emulator coordinates from the same
	// env vars they honor for Google's own emulators.
	t.Setenv("STORAGE_EMULATOR_HOST", host)
	t.Setenv("SPANNER_EMULATOR_HOST", myGRPCAddr)
	t.Setenv("BIGTABLE_EMULATOR_HOST", myGRPCAddr)

	// ── GCS: bucket + object bytes ────────────────────────────────────────
	const (
		bucketName    = "persist-bucket"
		objectName    = "survivor.txt"
		objectContent = "gcs bytes survived restart"
	)
	gcsClient, err := storage.NewClient(ctx, option.WithHTTPClient(persistentAuthHTTPClient(endpoint)))
	require.NoError(t, err)
	require.NoError(t, gcsClient.Bucket(bucketName).Create(ctx, project, nil))
	objWriter := gcsClient.Bucket(bucketName).Object(objectName).NewWriter(ctx)
	_, err = objWriter.Write([]byte(objectContent))
	require.NoError(t, err)
	require.NoError(t, objWriter.Close())
	require.NoError(t, gcsClient.Close())

	// ── Spanner: instance + database + committed row ──────────────────────
	const (
		spannerInstance = "persist-inst"
		spannerDBID     = "persist-db"
		spannerDatabase = "projects/" + project + "/instances/" + spannerInstance + "/databases/" + spannerDBID
	)
	spannerAdmin, err := spanneradmin.NewService(ctx,
		option.WithEndpoint(endpoint+"/spanner/"),
		option.WithTokenSource(simTokenSourceFor(endpoint)))
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

	spannerClient, err := spanner.NewClient(ctx, spannerDatabase)
	require.NoError(t, err)
	_, err = spannerClient.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("Survivors", []string{"Id", "Name"}, []interface{}{int64(1), "restart"}),
	})
	require.NoError(t, err)
	spannerClient.Close()

	// ── Bigtable: instance + table + row ──────────────────────────────────
	const (
		btInstance = "persist-bt"
		btTable    = "events"
		btFamily   = "cf"
	)
	btInstanceAdmin, err := bigtable.NewInstanceAdminClient(ctx, project)
	require.NoError(t, err)
	require.NoError(t, btInstanceAdmin.CreateInstance(ctx, &bigtable.InstanceConf{
		InstanceId:  btInstance,
		DisplayName: btInstance,
		ClusterId:   "c1",
		Zone:        "us-east1-b",
		NumNodes:    1,
	}))
	require.NoError(t, btInstanceAdmin.Close())
	btAdmin, err := bigtable.NewAdminClient(ctx, project, btInstance)
	require.NoError(t, err)
	require.NoError(t, btAdmin.CreateTable(ctx, btTable))
	require.NoError(t, btAdmin.CreateColumnFamily(ctx, btTable, btFamily))
	require.NoError(t, btAdmin.Close())
	btClient, err := bigtable.NewClient(ctx, project, btInstance)
	require.NoError(t, err)
	btMut := bigtable.NewMutation()
	btMut.Set(btFamily, "name", bigtable.Now(), []byte("alice"))
	require.NoError(t, btClient.Open(btTable).Apply(ctx, "user#1", btMut))
	require.NoError(t, btClient.Close())

	// ── Pub/Sub: topic + subscription + snapshot capturing a backlog ──────
	const (
		psTopic    = "projects/" + project + "/topics/persist-topic"
		psSub      = "projects/" + project + "/subscriptions/persist-sub"
		psSnapshot = "projects/" + project + "/snapshots/persist-snap"
		psPayload  = "snapshot replay survived restart"
	)
	psConn, err := grpc.NewClient(myGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	// Snapshot the outstanding backlog, then drain the queue so the only way
	// the message can come back after the restart is a snapshot-seek replay.
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

	// ── Compute Engine: an operation to poll after the restart ────────────
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

	// ── Access token minted before the restart ────────────────────────────
	preRestartToken, err := simTokenFetcher{base: endpoint}.Token()
	require.NoError(t, err)

	require.NoError(t, shutdownPersistentSimulator(cmd),
		"persistent simulator must exit cleanly before SQLite is reopened")
	cmd = startPersistentSimulator(t, stateDir, httpPort, grpcPort)

	// (a) The GCS object's BYTES are readable after the restart.
	gcsClient, err = storage.NewClient(ctx, option.WithHTTPClient(persistentAuthHTTPClient(endpoint)))
	require.NoError(t, err)
	defer gcsClient.Close()
	objReader, err := gcsClient.Bucket(bucketName).Object(objectName).NewReader(ctx)
	require.NoError(t, err, "the persisted object must serve its payload, not just its metadata")
	persistedBytes, err := io.ReadAll(objReader)
	require.NoError(t, err)
	require.NoError(t, objReader.Close())
	assert.Equal(t, objectContent, string(persistedBytes))

	// (b) The committed Spanner row is readable after the restart.
	spannerClient, err = spanner.NewClient(ctx, spannerDatabase)
	require.NoError(t, err)
	defer spannerClient.Close()
	row, err := spannerClient.Single().ReadRow(ctx, "Survivors", spanner.Key{int64(1)}, []string{"Name"})
	require.NoError(t, err, "the committed row must survive the restart")
	var persistedName string
	require.NoError(t, row.Columns(&persistedName))
	assert.Equal(t, "restart", persistedName)

	// (c) The Bigtable row is readable after the restart.
	btClient, err = bigtable.NewClient(ctx, project, btInstance)
	require.NoError(t, err)
	defer btClient.Close()
	persistedRow, err := btClient.Open(btTable).ReadRow(ctx, "user#1")
	require.NoError(t, err)
	require.Len(t, persistedRow[btFamily], 1, "the mutated row must survive the restart")
	assert.Equal(t, "alice", string(persistedRow[btFamily][0].Value))

	// (d) Seeking to the pre-restart snapshot replays its captured backlog.
	psConn, err = grpc.NewClient(myGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	// (e) Polling the pre-restart Compute Engine operation name succeeds.
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

	// (f) The pre-restart access token is still authorized: the restarted
	// simulator verifies it against the same persisted signing key.
	tokenReq, err := http.NewRequest(http.MethodGet, endpoint+"/storage/v1/b/"+bucketName, nil)
	require.NoError(t, err)
	tokenReq.Header.Set("Authorization", "Bearer "+preRestartToken.AccessToken)
	tokenResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(tokenReq)
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	assert.Equal(t, http.StatusOK, tokenResp.StatusCode,
		"a token minted before the restart must verify against the persisted signing key")
}
