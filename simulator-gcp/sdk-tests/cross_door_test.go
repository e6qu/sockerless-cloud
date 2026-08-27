package gcp_sdk_test

import (
	"encoding/base64"
	"testing"

	"cloud.google.com/go/bigtable"
	adminpb "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bigtableadmin "google.golang.org/api/bigtableadmin/v2"
	cloudkms "google.golang.org/api/cloudkms/v1"
	firestore "google.golang.org/api/firestore/v1"
	logging "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
	secretmanager "google.golang.org/api/secretmanager/v1"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Several Google Cloud services are reached over two protocols, and this
// simulator serves both from one set of stores. That is the whole claim: one
// cloud state, two doors onto it.
//
// Nothing checked the claim. Every suite drove one door and read back through
// the same door, so a handler that answered plausibly while doing nothing
// stayed invisible as long as its sibling behaved — which is exactly how Cloud
// Bigtable's REST dropRowRange came to acknowledge deletes it never performed
// while the gRPC spelling deleted for real. A test that dropped over REST and
// read over gRPC would have failed the day that divergence appeared.
//
// So every test here crosses: it writes through one protocol and observes
// through the other, in both directions. A service keeping a second copy of
// its state, or a handler that only pretends, fails here and nowhere else.
//
// simulator-gcp/cross_door_test.go holds this file to the gRPC services the
// server actually mounts, so a two-door service cannot arrive uncrossed.

// crossDoorBase64 is how the REST doors carry bytes.
func crossDoorBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func crossDoorBytes(t *testing.T, encoded string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	return raw
}

func TestCrossDoor_BigtableAdminAndData(t *testing.T) {
	t.Setenv("BIGTABLE_EMULATOR_HOST", grpcAddr)
	const project, instanceID, family = "cross-door-bt", "inst", "cf"
	ia, ta, _ := bigtableAdminGRPCConn(t)

	// Created over gRPC.
	instance, _ := bigtableAdminGRPCInstance(t, ia, project, instanceID, "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", family)

	rest, err := bigtableadmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	// Read over REST.
	gotInstance, err := rest.Projects.Instances.Get(instance).Do()
	require.NoError(t, err)
	assert.Equal(t, instance, gotInstance.Name)

	gotTable, err := rest.Projects.Instances.Tables.Get(table).Do()
	require.NoError(t, err)
	require.Contains(t, gotTable.ColumnFamilies, family,
		"the column family the gRPC create declared must be visible over REST")

	// Created over REST, read over gRPC.
	restTable, err := rest.Projects.Instances.Tables.Create(instance, &bigtableadmin.CreateTableRequest{
		TableId: "rest-made",
		Table:   &bigtableadmin.Table{ColumnFamilies: map[string]bigtableadmin.ColumnFamily{"rf": {}}},
	}).Do()
	require.NoError(t, err)
	viaGRPC, err := ta.GetTable(ctx, &adminpb.GetTableRequest{Name: restTable.Name})
	require.NoError(t, err)
	require.Contains(t, viaGRPC.GetColumnFamilies(), "rf",
		"a table created over REST must read back over gRPC")

	// Rows written over gRPC, dropped over REST, gone over gRPC. This is the
	// crossing the REST no-op survived: both doors address one row store.
	client, err := bigtable.NewClient(ctx, project, instanceID)
	require.NoError(t, err)
	defer client.Close()
	tbl := client.Open("events")
	for _, key := range []string{"user#1", "user#2", "admin#1"} {
		mut := bigtable.NewMutation()
		mut.Set(family, "name", bigtable.Now(), []byte(key))
		require.NoError(t, tbl.Apply(ctx, key, mut))
	}
	require.Equal(t, []string{"admin#1", "user#1", "user#2"}, bigtableRowKeys(t, tbl))

	_, err = rest.Projects.Instances.Tables.DropRowRange(table, &bigtableadmin.DropRowRangeRequest{
		RowKeyPrefix: crossDoorBase64([]byte("user#")),
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"admin#1"}, bigtableRowKeys(t, tbl),
		"a prefix dropped over REST must be gone when read over gRPC")

	_, err = rest.Projects.Instances.Tables.DropRowRange(table, &bigtableadmin.DropRowRangeRequest{
		DeleteAllDataFromTable: true,
	}).Do()
	require.NoError(t, err)
	assert.Empty(t, bigtableRowKeys(t, tbl),
		"dropping the whole table over REST must empty it for a gRPC reader")
}

func TestCrossDoor_CloudKMS(t *testing.T) {
	grpcClient := newKMSGRPCClient(t)
	rest := kmsService(t)
	ringName := kmsKeyRingForTest(t, grpcClient, "cross-door")

	// Created over REST.
	keyName := ringName + "/cryptoKeys/crossing"
	_, err := rest.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose:         "ENCRYPT_DECRYPT",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION"},
	}).CryptoKeyId("crossing").Do()
	require.NoError(t, err)

	viaGRPC, err := grpcClient.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: keyName})
	require.NoError(t, err)
	require.Equal(t, keyName, viaGRPC.GetName())

	// Encrypted over gRPC, decrypted over REST — only possible if both doors
	// hold one key's material rather than a copy each.
	plaintext := []byte("one key, two doors")
	encrypted, err := grpcClient.Encrypt(ctx, &kmspb.EncryptRequest{Name: keyName, Plaintext: plaintext})
	require.NoError(t, err)
	decrypted, err := rest.Projects.Locations.KeyRings.CryptoKeys.Decrypt(keyName, &cloudkms.DecryptRequest{
		Ciphertext: crossDoorBase64(encrypted.GetCiphertext()),
	}).Do()
	require.NoError(t, err)
	require.Equal(t, plaintext, crossDoorBytes(t, decrypted.Plaintext))

	// And the reverse.
	restEncrypted, err := rest.Projects.Locations.KeyRings.CryptoKeys.Encrypt(keyName, &cloudkms.EncryptRequest{
		Plaintext: crossDoorBase64(plaintext),
	}).Do()
	require.NoError(t, err)
	grpcDecrypted, err := grpcClient.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       keyName,
		Ciphertext: crossDoorBytes(t, restEncrypted.Ciphertext),
	})
	require.NoError(t, err)
	require.Equal(t, plaintext, grpcDecrypted.GetPlaintext())
}

func TestCrossDoor_SecretManager(t *testing.T) {
	rest := secretManagerService(t)
	grpcClient := newSecretManagerGRPCClient(t)
	const parent = "projects/cross-door-secrets"

	// Created over REST.
	secret, err := rest.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId("crossing").Do()
	require.NoError(t, err)

	// A version added over gRPC, accessed over REST.
	payload := []byte("the same secret through either door")
	version, err := grpcClient.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secret.Name,
		Payload: &secretmanagerpb.SecretPayload{Data: payload},
	})
	require.NoError(t, err)

	accessed, err := rest.Projects.Secrets.Versions.Access(version.GetName()).Do()
	require.NoError(t, err)
	require.Equal(t, payload, crossDoorBytes(t, accessed.Payload.Data))

	// Destroyed over gRPC, and the REST access refuses it.
	_, err = grpcClient.DestroySecretVersion(ctx, &secretmanagerpb.DestroySecretVersionRequest{Name: version.GetName()})
	require.NoError(t, err)
	_, err = rest.Projects.Secrets.Versions.Access(version.GetName()).Do()
	require.Error(t, err, "a version destroyed over gRPC must not be accessible over REST")
}

func TestCrossDoor_Firestore(t *testing.T) {
	rest, err := firestore.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	grpcClient := fsRawGRPCClient(t)
	const parent = "projects/cross-door-fs/databases/(default)/documents"

	// Created over REST, read over gRPC.
	created, err := rest.Projects.Databases.Documents.CreateDocument(parent, "crossing", &firestore.Document{
		Fields: map[string]firestore.Value{"who": {StringValue: "rest"}},
	}).DocumentId("doc-1").Do()
	require.NoError(t, err)

	viaGRPC, err := grpcClient.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: created.Name})
	require.NoError(t, err)
	require.Equal(t, "rest", viaGRPC.GetFields()["who"].GetStringValue())

	// Updated over gRPC, read over REST.
	_, err = grpcClient.UpdateDocument(ctx, &firestorepb.UpdateDocumentRequest{
		Document: &firestorepb.Document{
			Name:   created.Name,
			Fields: map[string]*firestorepb.Value{"who": {ValueType: &firestorepb.Value_StringValue{StringValue: "grpc"}}},
		},
		UpdateMask: &firestorepb.DocumentMask{FieldPaths: []string{"who"}},
	})
	require.NoError(t, err)

	viaREST, err := rest.Projects.Databases.Documents.Get(created.Name).Do()
	require.NoError(t, err)
	require.Equal(t, "grpc", viaREST.Fields["who"].StringValue,
		"a gRPC update must be what the REST read reports")

	// Deleted over gRPC, gone over REST.
	_, err = grpcClient.DeleteDocument(ctx, &firestorepb.DeleteDocumentRequest{Name: created.Name})
	require.NoError(t, err)
	_, err = rest.Projects.Databases.Documents.Get(created.Name).Do()
	require.Error(t, err, "a document deleted over gRPC must be gone over REST")
}

func TestCrossDoor_PubSub(t *testing.T) {
	rest := pubsubService(t)
	const project = "projects/cross-door-pubsub"
	topicName := project + "/topics/crossing"
	subName := project + "/subscriptions/crossing-sub"

	// Topic and subscription created over REST.
	_, err := rest.Projects.Topics.Create(topicName, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	_, err = rest.Projects.Subscriptions.Create(subName, &pubsub.Subscription{Topic: topicName}).Do()
	require.NoError(t, err)

	// Published over gRPC.
	publisher, subscriber := psRawClient(t)
	published, err := publisher.Publish(ctx, &pubsubpb.PublishRequest{
		Topic:    topicName,
		Messages: []*pubsubpb.PubsubMessage{{Data: []byte("published over gRPC")}},
	})
	require.NoError(t, err)
	require.Len(t, published.GetMessageIds(), 1)

	// Pulled over REST — one queue, two doors.
	pulled, err := rest.Projects.Subscriptions.Pull(subName, &pubsub.PullRequest{MaxMessages: 10}).Do()
	require.NoError(t, err)
	require.Len(t, pulled.ReceivedMessages, 1,
		"a message published over gRPC must be pullable over REST")
	require.Equal(t, []byte("published over gRPC"), crossDoorBytes(t, pulled.ReceivedMessages[0].Message.Data))

	// Acknowledged over gRPC, and the REST pull no longer sees it.
	_, err = subscriber.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{
		Subscription: subName,
		AckIds:       []string{pulled.ReceivedMessages[0].AckId},
	})
	require.NoError(t, err)
	again, err := rest.Projects.Subscriptions.Pull(subName, &pubsub.PullRequest{MaxMessages: 10}).Do()
	require.NoError(t, err)
	require.Empty(t, again.ReceivedMessages,
		"a message acknowledged over gRPC must not be redelivered over REST")
}

func TestCrossDoor_CloudLogging(t *testing.T) {
	rest, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	grpcClient := newLoggingV2Client(t)
	const project = "cross-door-logging"
	logName := "projects/" + project + "/logs/crossing"

	// Written over gRPC.
	_, err = grpcClient.WriteLogEntries(ctx, &loggingpb.WriteLogEntriesRequest{
		LogName:  logName,
		Resource: &mrpb.MonitoredResource{Type: "global"},
		Entries: []*loggingpb.LogEntry{{
			Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "written over gRPC"},
		}},
	})
	require.NoError(t, err)

	// Listed over REST.
	listed, err := rest.Entries.List(&logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        `logName="` + logName + `"`,
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, listed.Entries, "an entry written over gRPC must be listed over REST")
	require.Equal(t, "written over gRPC", listed.Entries[0].TextPayload)

	logs, err := rest.Projects.Logs.List("projects/" + project).Do()
	require.NoError(t, err)
	require.Contains(t, logs.LogNames, logName)

	// Deleted over gRPC, gone from the REST listing.
	err = grpcClient.DeleteLog(ctx, &loggingpb.DeleteLogRequest{LogName: logName})
	require.NoError(t, err)
	logs, err = rest.Projects.Logs.List("projects/" + project).Do()
	require.NoError(t, err)
	require.NotContains(t, logs.LogNames, logName,
		"a log deleted over gRPC must be gone from the REST listing")
}

func TestCrossDoor_PubSubSchemas(t *testing.T) {
	rest := pubsubService(t)
	const project = "cross-door-ps-schemas"
	parent := "projects/" + project
	const definition = `{"type":"record","name":"Crossing","fields":[{"name":"who","type":"string"}]}`

	// Created over REST.
	created, err := rest.Projects.Schemas.Create(parent, &pubsub.Schema{
		Type:       "AVRO",
		Definition: definition,
	}).SchemaId("crossing").Do()
	require.NoError(t, err)

	// Read over gRPC, carrying the definition the REST create stored.
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	schemaClient := pubsubpb.NewSchemaServiceClient(conn)
	viaGRPC, err := schemaClient.GetSchema(ctx, &pubsubpb.GetSchemaRequest{
		Name: created.Name,
		View: pubsubpb.SchemaView_FULL,
	})
	require.NoError(t, err)
	require.Equal(t, definition, viaGRPC.GetDefinition(),
		"a schema created over REST must read back over gRPC with its definition")

	// Deleted over gRPC, gone from the REST listing.
	_, err = schemaClient.DeleteSchema(ctx, &pubsubpb.DeleteSchemaRequest{Name: created.Name})
	require.NoError(t, err)
	listed, err := rest.Projects.Schemas.List(parent).Do()
	require.NoError(t, err)
	for _, s := range listed.Schemas {
		require.NotEqual(t, created.Name, s.Name,
			"a schema deleted over gRPC must be gone from the REST listing")
	}
}

// TestCrossDoor_Operations crosses the long-running Operations service: an
// operation minted by a gRPC call is one the REST operations door reports.
func TestCrossDoor_Operations(t *testing.T) {
	ia, ta, _ := bigtableAdminGRPCConn(t)
	const project = "cross-door-ops"
	instance, _ := bigtableAdminGRPCInstance(t, ia, project, "inst", "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", "cf")

	op, err := ta.UndeleteTable(ctx, &adminpb.UndeleteTableRequest{Name: table})
	require.NoError(t, err)
	require.NotEmpty(t, op.GetName())

	rest, err := bigtableadmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	listed, err := rest.Operations.Projects.Operations.List("operations/projects/" + project).Do()
	require.NoError(t, err)
	var names []string
	for _, o := range listed.Operations {
		names = append(names, o.Name)
	}
	require.Contains(t, names, op.GetName(),
		"an operation a gRPC call returned must be reported by the REST operations door")

	// And fetched by name through it. operations.get takes the whole remaining
	// path (`v2/{+name}` over `^operations/.*$`), so the name's own slashes are
	// part of it: a route matching a single segment answers the flat names and
	// 404s these, which is exactly what a client polling a create hits.
	fetched, err := rest.Operations.Get(op.GetName()).Do()
	require.NoError(t, err, "the REST operations door must fetch an operation by its full name")
	require.Equal(t, op.GetName(), fetched.Name)
	require.True(t, fetched.Done)
}

func TestCrossDoor_Spanner(t *testing.T) {
	const project, instanceID, databaseID = "cross-door-spanner", "inst", "db"
	rest := spannerAdminService(t, ctx)
	spannerProvision(t, ctx, project, instanceID, databaseID,
		[]string{`CREATE TABLE crossing (id INT64 NOT NULL, who STRING(MAX)) PRIMARY KEY (id)`})
	database := "projects/" + project + "/instances/" + instanceID + "/databases/" + databaseID

	// Written over the REST session surface.
	spannerWriteRows(t, rest, database, `INSERT INTO crossing (id, who) VALUES (1, "rest")`)

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	grpcClient := sppb.NewSpannerClient(conn)
	session, err := grpcClient.CreateSession(ctx, &sppb.CreateSessionRequest{Database: database})
	require.NoError(t, err)

	// Read over gRPC.
	read, err := grpcClient.ExecuteSql(ctx, &sppb.ExecuteSqlRequest{
		Session: session.GetName(),
		Sql:     "SELECT who FROM crossing WHERE id = 1",
	})
	require.NoError(t, err)
	require.Len(t, read.GetRows(), 1,
		"a row written over the REST session surface must be readable over gRPC")
	require.Equal(t, "rest", read.GetRows()[0].GetValues()[0].GetStringValue())

	// Written over gRPC, in its own read-write transaction, then read over REST.
	txn, err := grpcClient.BeginTransaction(ctx, &sppb.BeginTransactionRequest{
		Session: session.GetName(),
		Options: &sppb.TransactionOptions{
			Mode: &sppb.TransactionOptions_ReadWrite_{ReadWrite: &sppb.TransactionOptions_ReadWrite{}},
		},
	})
	require.NoError(t, err)
	_, err = grpcClient.ExecuteSql(ctx, &sppb.ExecuteSqlRequest{
		Session:     session.GetName(),
		Sql:         `INSERT INTO crossing (id, who) VALUES (2, "grpc")`,
		Transaction: &sppb.TransactionSelector{Selector: &sppb.TransactionSelector_Id{Id: txn.GetId()}},
	})
	require.NoError(t, err)
	_, err = grpcClient.Commit(ctx, &sppb.CommitRequest{
		Session:     session.GetName(),
		Transaction: &sppb.CommitRequest_TransactionId{TransactionId: txn.GetId()},
	})
	require.NoError(t, err)

	viaREST := spannerQueryRows(t, rest, database, "SELECT who FROM crossing WHERE id = 2")
	require.Len(t, viaREST, 1, "a row written over gRPC must be readable over REST")
	require.Equal(t, "grpc", viaREST[0][0])
}
