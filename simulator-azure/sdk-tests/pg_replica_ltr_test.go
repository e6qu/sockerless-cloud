package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Long-term-retention backups and the source-based create modes (Replica,
// GeoRestore) share one substrate with the on-demand backups pg_data_test.go
// exercises: a server's data lives on a named volume, a backup captures that
// volume, and a source-based create clones a volume into the new server
// before its data plane installs. These tests drive that substrate through
// raw ARM requests and, where the host can run the data plane, through a
// stock PostgreSQL driver.

const pgReplicaLtrSub = "00000000-0000-0000-0000-000000000000"

func pgFlexArmBase(rg string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DBforPostgreSQL/flexibleServers", pgReplicaLtrSub, rg)
}

// pgCreateServerRaw creates a flexible server through raw ARM, waits for the
// create's long-running operation, and registers the delete cleanup.
func pgCreateServerRaw(t *testing.T, serverPath, body string) {
	t.Helper()
	resp := armReq(t, "PUT", serverPath, body)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	opURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, opURL)
	resp.Body.Close()
	t.Cleanup(func() { armReq(t, "DELETE", serverPath, "").Body.Close() })
	waitPGDataAsyncOperation(t, opURL)
}

// pgTryDataPlane resolves a server's fullyQualifiedDomainName through the
// simulator's DNS and reports whether the data plane's loopback listener is
// there. On Linux the data plane must install, so absence is a failure; a
// host that cannot provide a loopback alias without root (macOS) proceeds
// with the ARM-level assertions and without the data-plane half.
func pgTryDataPlane(t *testing.T, fqdn string) bool {
	t.Helper()
	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addresses, err := azurePGResolver().LookupHost(lookupCtx, fqdn)
	reachable := err == nil && len(addresses) > 0 && net.ParseIP(addresses[0]) != nil && net.ParseIP(addresses[0]).IsLoopback()
	if !reachable && runtime.GOOS == "linux" {
		t.Fatalf("server %s resolves to %v (err %v): on Linux the data plane must install", fqdn, addresses, err)
	}
	return reachable
}

// pgConnectTLS connects a stock PostgreSQL driver to a server's data plane
// through the simulator's DNS, with the retry budget a fresh engine needs.
func pgConnectTLS(t *testing.T, testCtx context.Context, login, password, host string) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(fmt.Sprintf(
		"postgres://%s@%s:5432/postgres?sslmode=require", login, host))
	require.NoError(t, err)
	config.Password = password
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	config.LookupFunc = azurePGResolver().LookupHost
	var conn *pgx.Conn
	require.Eventually(t, func() bool {
		var connectErr error
		conn, connectErr = pgx.ConnectConfig(testCtx, config)
		return connectErr == nil
	}, 3*time.Minute, 2*time.Second, "the data plane must accept the stock driver")
	return conn
}

// pgReadLedger reads every row of the ledger table, ordered.
func pgReadLedger(t *testing.T, testCtx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(testCtx, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err)
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	return entries
}

// A long-term-retention backup is a stored operation record backed by a real
// volume capture: start creates the record and captures the server's volume
// through the operation's own poll, Get and List read the same record back,
// and a name that was never started does not exist — Get and List agree.
func TestAzurePGFlexibleServer_LtrBackupLifecycle(t *testing.T) {
	testContext, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	const (
		rg            = "pg-ltr-rg"
		serverName    = "pg-ltr-srv"
		adminLogin    = "psqladmin"
		adminPassword = "LtrData-123!"
		backupName    = "pg-ltr-backup"
	)
	serverPath := pgFlexArmBase(rg) + "/" + serverName
	pgCreateServerRaw(t, serverPath, fmt.Sprintf(
		`{"location":"eastus","properties":{"version":"16","administratorLogin":%q,"administratorLoginPassword":%q}}`,
		adminLogin, adminPassword))

	// Give the capture real bytes to carry when the host can run the data
	// plane; the ARM surface behaves identically either way.
	fqdn, _ := pgDataGetServer(t, serverPath)["fullyQualifiedDomainName"].(string)
	require.NotEmpty(t, fqdn)
	if pgTryDataPlane(t, fqdn) {
		conn := pgConnectTLS(t, testContext, adminLogin, adminPassword, fqdn)
		_, err := conn.Exec(testContext, `CREATE TABLE ledger (entry text)`)
		require.NoError(t, err)
		_, err = conn.Exec(testContext, `INSERT INTO ledger VALUES ('ltr-row')`)
		require.NoError(t, err)
		require.NoError(t, conn.Close(testContext))
	}

	// Start the backup and wait the way a real client does: the operation
	// succeeds only once the capture settles.
	resp := armReq(t, "POST", serverPath+"/startLtrBackup", fmt.Sprintf(
		`{"backupSettings":{"backupName":%q},"targetDetails":{"sasUriList":["https://example.blob.core.windows.net/ltr?sas"]}}`,
		backupName))
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	opURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, opURL)
	assert.NotEmpty(t, resp.Header.Get("Location"),
		"startLtrBackup declares its final state via Location")
	resp.Body.Close()
	waitPGDataAsyncOperation(t, opURL)

	resp = armReq(t, "GET", serverPath+"/ltrBackupOperations/"+backupName, "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var got struct {
		Name       string         `json:"name"`
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, backupName, got.Name)
	assert.Equal(t, "Succeeded", got.Properties["status"])
	assert.NotEmpty(t, got.Properties["startTime"])
	assert.NotEmpty(t, got.Properties["endTime"])
	assert.Equal(t, float64(100), got.Properties["percentComplete"])

	resp = armReq(t, "GET", serverPath+"/ltrBackupOperations", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var list struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Value, 1, "the list must hold exactly the backup that was started")
	assert.Equal(t, backupName, list.Value[0].Name)

	// A name that was never started does not exist.
	resp = armReq(t, "GET", serverPath+"/ltrBackupOperations/never-started", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))

	// The request shape holds: a start without targetDetails is refused.
	resp = armReq(t, "POST", serverPath+"/startLtrBackup", `{"backupSettings":{"backupName":"no-target"}}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	assert.Contains(t, string(body), "targetDetails.sasUriList")
}

// createMode=Replica clones the primary's live volume into the new server
// under the primary's credential, marks the pair with the replication
// properties ServerProperties defines, and Replicas_ListByServer lists it.
func TestAzurePGFlexibleServer_ReplicaCloneAndListing(t *testing.T) {
	testContext, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	const (
		rg            = "pg-repl-rg"
		sourceName    = "pg-repl-src"
		replicaName   = "pg-repl-replica"
		adminLogin    = "psqladmin"
		adminPassword = "Replica-123!"
	)
	sourcePath := pgFlexArmBase(rg) + "/" + sourceName
	replicaPath := pgFlexArmBase(rg) + "/" + replicaName
	sourceID := sourcePath

	pgCreateServerRaw(t, sourcePath, fmt.Sprintf(
		`{"location":"eastus","properties":{"version":"16","administratorLogin":%q,"administratorLoginPassword":%q}}`,
		adminLogin, adminPassword))

	sourceFQDN, _ := pgDataGetServer(t, sourcePath)["fullyQualifiedDomainName"].(string)
	require.NotEmpty(t, sourceFQDN)
	dataPlane := pgTryDataPlane(t, sourceFQDN)
	if dataPlane {
		conn := pgConnectTLS(t, testContext, adminLogin, adminPassword, sourceFQDN)
		_, err := conn.Exec(testContext, `CREATE TABLE ledger (entry text)`)
		require.NoError(t, err)
		_, err = conn.Exec(testContext, `INSERT INTO ledger VALUES ('primary-row')`)
		require.NoError(t, err)
		require.NoError(t, conn.Close(testContext))
	}

	pgCreateServerRaw(t, replicaPath, fmt.Sprintf(
		`{"location":"eastus","properties":{"createMode":"Replica","sourceServerResourceId":%q}}`, sourceID))

	replicaProps := pgDataGetServer(t, replicaPath)
	assert.Equal(t, "AsyncReplica", replicaProps["replicationRole"])
	assert.Equal(t, sourceID, replicaProps["sourceServerResourceId"])
	replica, _ := replicaProps["replica"].(map[string]any)
	require.NotNil(t, replica, "a read replica must carry the replica properties object")
	assert.Equal(t, "AsyncReplica", replica["role"])
	assert.Equal(t, "Active", replica["replicationState"])
	// The replica serves under the source's administrator login.
	assert.Equal(t, adminLogin, replicaProps["administratorLogin"])

	// The source became the replication set's primary.
	sourceProps := pgDataGetServer(t, sourcePath)
	assert.Equal(t, "Primary", sourceProps["replicationRole"])
	sourceReplica, _ := sourceProps["replica"].(map[string]any)
	require.NotNil(t, sourceReplica)
	assert.Equal(t, "Primary", sourceReplica["role"])

	// Replicas_ListByServer on the source lists the replica.
	resp := armReq(t, "GET", sourcePath+"/replicas", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var replicas struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal(body, &replicas))
	require.Len(t, replicas.Value, 1, "the source must list exactly its replica")
	assert.Equal(t, replicaName, replicas.Value[0].Name)

	if dataPlane {
		// The clone carried the data: the replica serves the primary's row
		// under the primary's credential.
		replicaFQDN, _ := replicaProps["fullyQualifiedDomainName"].(string)
		require.NotEmpty(t, replicaFQDN)
		conn := pgConnectTLS(t, testContext, adminLogin, adminPassword, replicaFQDN)
		defer func() { _ = conn.Close(context.Background()) }()
		assert.Equal(t, []string{"primary-row"}, pgReadLedger(t, testContext, conn))
	}
}

// createMode=GeoRestore restores the source's latest backup regardless of a
// point in time: rows from before the backup are present, rows written after
// it are not.
func TestAzurePGFlexibleServer_GeoRestoreRestoresLatestBackup(t *testing.T) {
	testContext, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	const (
		rg            = "pg-geo-rg"
		sourceName    = "pg-geo-src"
		restoredName  = "pg-geo-restored"
		adminLogin    = "psqladmin"
		adminPassword = "GeoData-123!"
		backupName    = "pg-geo-backup"
	)
	sourcePath := pgFlexArmBase(rg) + "/" + sourceName
	restoredPath := pgFlexArmBase(rg) + "/" + restoredName

	pgCreateServerRaw(t, sourcePath, fmt.Sprintf(
		`{"location":"eastus","properties":{"version":"16","administratorLogin":%q,"administratorLoginPassword":%q}}`,
		adminLogin, adminPassword))

	sourceFQDN, _ := pgDataGetServer(t, sourcePath)["fullyQualifiedDomainName"].(string)
	require.NotEmpty(t, sourceFQDN)
	dataPlane := pgTryDataPlane(t, sourceFQDN)
	var source *pgx.Conn
	if dataPlane {
		source = pgConnectTLS(t, testContext, adminLogin, adminPassword, sourceFQDN)
		_, err := source.Exec(testContext, `CREATE TABLE ledger (entry text)`)
		require.NoError(t, err)
		_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('before-backup')`)
		require.NoError(t, err)
	}

	resp := armReq(t, "PUT", sourcePath+"/backups/"+backupName, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	backupOpURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, backupOpURL)
	resp.Body.Close()
	waitPGDataAsyncOperation(t, backupOpURL)

	if dataPlane {
		_, err := source.Exec(testContext, `INSERT INTO ledger VALUES ('after-backup')`)
		require.NoError(t, err)
		require.NoError(t, source.Close(testContext))
	}

	// No pointInTimeUTC: geo-restore takes the latest backup.
	pgCreateServerRaw(t, restoredPath, fmt.Sprintf(
		`{"location":"eastus","properties":{"createMode":"GeoRestore","sourceServerResourceId":%q}}`, sourcePath))

	restoredProps := pgDataGetServer(t, restoredPath)
	assert.Equal(t, adminLogin, restoredProps["administratorLogin"],
		"the restored server keeps the source's administrator login")

	if dataPlane {
		restoredFQDN, _ := restoredProps["fullyQualifiedDomainName"].(string)
		require.NotEmpty(t, restoredFQDN)
		conn := pgConnectTLS(t, testContext, adminLogin, adminPassword, restoredFQDN)
		defer func() { _ = conn.Close(context.Background()) }()
		assert.Equal(t, []string{"before-backup"}, pgReadLedger(t, testContext, conn),
			"geo-restore must return to the latest backup: rows from before it, none from after it")
	}
}

// createMode values the create cannot serve are refused with the reason,
// never silently treated as a plain create.
func TestAzurePGFlexibleServer_CreateModeRejections(t *testing.T) {
	const rg = "pg-mode-rg"
	armBase := pgFlexArmBase(rg)
	refuse := func(name, body, reason string) {
		t.Helper()
		path := armBase + "/" + name
		resp := armReq(t, "PUT", path, body)
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(respBody))
		assert.Contains(t, string(respBody), "InvalidParameterValue")
		assert.Contains(t, string(respBody), reason)
		// The refusal never fell through to a plain create.
		getResp := armReq(t, "GET", path, "")
		getResp.Body.Close()
		assert.Equal(t, http.StatusNotFound, getResp.StatusCode,
			"a refused create must not leave a server behind")
	}
	sourceID := armBase + "/pg-mode-ghost"
	refuse("pg-mode-revive",
		fmt.Sprintf(`{"location":"eastus","properties":{"createMode":"ReviveDropped","sourceServerResourceId":%q,"pointInTimeUTC":"2026-01-01T00:00:00Z"}}`, sourceID),
		"retains no dropped server to revive")
	refuse("pg-mode-update",
		`{"location":"eastus","properties":{"createMode":"Update"}}`,
		"requires an existing server")
	refuse("pg-mode-unknown",
		`{"location":"eastus","properties":{"createMode":"SpinUp"}}`,
		"not a creation mode the service defines")
}
