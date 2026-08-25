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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A flexible-server backup carries the data, and PointInTimeRestore returns
// to it.
//
// This is the property that separates a backup from a metadata row: rows
// written before the backup exist in the restored server, and rows written
// after it do not. Both halves are asserted through a stock PostgreSQL
// driver against the server's real data plane — the client resolves the
// fullyQualifiedDomainName through the simulator's DNS front to a listener
// the simulator owns at PostgreSQL's own port (the ARM contract carries no
// address), the engine is a real PostgreSQL on a named volume, and the
// restore clones the backup volume into the new server before its data
// plane installs.
//
// Azure's wire defaults hold too: require_secure_transport is ON, so the
// same driver without TLS is refused with SQLSTATE 28000, and the ARM
// surface never echoes administratorLoginPassword back.
func TestAzurePGFlexibleServer_BackupCapturesDataAndRestoreReturnsToIt(t *testing.T) {
	testContext, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	const (
		sub           = "00000000-0000-0000-0000-000000000000"
		rg            = "pg-data-rg"
		serverName    = "pg-data-srv"
		adminLogin    = "psqladmin"
		adminPassword = "DataPlane-123!"
		backupName    = "pg-data-backup"
		restoredName  = "pg-data-restored"
	)
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DBforPostgreSQL/flexibleServers", sub, rg)
	serverPath := armBase + "/" + serverName
	restoredPath := armBase + "/" + restoredName

	resp := armReq(t, "PUT", serverPath, fmt.Sprintf(
		`{"location":"eastus","properties":{"version":"16","administratorLogin":%q,"administratorLoginPassword":%q}}`,
		adminLogin, adminPassword))
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	opURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, opURL)
	resp.Body.Close()
	t.Cleanup(func() { armReq(t, "DELETE", serverPath, "").Body.Close() })
	waitPGDataAsyncOperation(t, opURL)

	server := pgDataGetServer(t, serverPath)
	fqdn, _ := server["fullyQualifiedDomainName"].(string)
	require.NotEmpty(t, fqdn)
	// administratorLoginPassword is x-ms-secret: the wire never echoes it.
	_, echoed := server["administratorLoginPassword"]
	require.False(t, echoed, "GET must not echo administratorLoginPassword")

	azurePGLookupLoopback(t, fqdn)

	connect := func(host string) *pgx.Conn {
		config, parseErr := pgx.ParseConfig(fmt.Sprintf(
			"postgres://%s@%s:5432/postgres?sslmode=require", adminLogin, host))
		require.NoError(t, parseErr)
		config.Password = adminPassword
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true}
		config.LookupFunc = azurePGResolver().LookupHost
		var conn *pgx.Conn
		require.Eventually(t, func() bool {
			var connectErr error
			conn, connectErr = pgx.ConnectConfig(testContext, config)
			return connectErr == nil
		}, 3*time.Minute, 2*time.Second, "the data plane must accept the stock driver")
		return conn
	}

	source := connect(fqdn)
	_, err := source.Exec(testContext, `CREATE TABLE ledger (entry text)`)
	require.NoError(t, err)
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('before-backup')`)
	require.NoError(t, err)

	// require_secure_transport holds its Azure default (ON): the same driver
	// without TLS is refused at the wire with SQLSTATE 28000.
	plainConfig, err := pgx.ParseConfig(fmt.Sprintf(
		"postgres://%s@%s:5432/postgres?sslmode=disable", adminLogin, fqdn))
	require.NoError(t, err)
	plainConfig.Password = adminPassword
	plainConfig.LookupFunc = azurePGResolver().LookupHost
	_, err = pgx.ConnectConfig(testContext, plainConfig)
	require.Error(t, err, "a plaintext client must be refused while require_secure_transport is ON")
	assert.True(t, strings.Contains(err.Error(), "SSL") || strings.Contains(err.Error(), "28000"),
		"the refusal must be the require_secure_transport error, got: %v", err)

	// Back up, and wait for the operation the way a real client does: the
	// long-running operation succeeds only once the capture settles.
	resp = armReq(t, "PUT", serverPath+"/backups/"+backupName, "")
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	backupOpURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, backupOpURL)
	resp.Body.Close()
	waitPGDataAsyncOperation(t, backupOpURL)

	resp = armReq(t, "GET", serverPath+"/backups/"+backupName, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	backupBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(backupBody), `"completedTime"`,
		"a settled backup must carry its completedTime")

	// Rows written after the backup are the half a restore must NOT have.
	_, err = source.Exec(testContext, `INSERT INTO ledger VALUES ('after-backup')`)
	require.NoError(t, err)
	require.NoError(t, source.Close(testContext))

	pointInTime := time.Now().UTC().Format(time.RFC3339Nano)
	sourceServerID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DBforPostgreSQL/flexibleServers/%s", sub, rg, serverName)
	resp = armReq(t, "PUT", restoredPath, fmt.Sprintf(
		`{"location":"eastus","properties":{"createMode":"PointInTimeRestore","sourceServerResourceId":%q,"pointInTimeUTC":%q}}`,
		sourceServerID, pointInTime))
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	restoreOpURL := resp.Header.Get("Azure-AsyncOperation")
	require.NotEmpty(t, restoreOpURL)
	resp.Body.Close()
	t.Cleanup(func() { armReq(t, "DELETE", restoredPath, "").Body.Close() })
	waitPGDataAsyncOperation(t, restoreOpURL)

	restoredServer := pgDataGetServer(t, restoredPath)
	restoredFQDN, _ := restoredServer["fullyQualifiedDomainName"].(string)
	require.NotEmpty(t, restoredFQDN)
	azurePGLookupLoopback(t, restoredFQDN)

	// The restored server keeps the source's administrator credential.
	restored := connect(restoredFQDN)
	defer func() { _ = restored.Close(context.Background()) }()
	rows, err := restored.Query(testContext, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err, "the restored engine must serve the captured schema")
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	rows.Close()
	assert.Equal(t, []string{"before-backup"}, entries,
		"the restore must return to the backup: rows from before it, and none from after it")
}

// pgDataGetServer reads a flexible server back through ARM and returns its
// properties object.
func pgDataGetServer(t *testing.T, path string) map[string]any {
	t.Helper()
	resp := armReq(t, "GET", path, "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var server struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(body, &server))
	require.NotNil(t, server.Properties)
	return server.Properties
}

// azurePGResolver resolves through the simulator's DNS front — the
// coordinate a deployment points its resolver at — instead of the host's.
func azurePGResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(dialCtx, network, simAzureDNSAddr)
		},
	}
}

// azurePGLookupLoopback resolves a server's fullyQualifiedDomainName through
// the simulator's DNS and requires the loopback address the data plane owns.
func azurePGLookupLoopback(t *testing.T, fqdn string) string {
	t.Helper()
	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addresses, err := azurePGResolver().LookupHost(lookupCtx, fqdn)
	if err != nil || len(addresses) == 0 || net.ParseIP(addresses[0]) == nil || !net.ParseIP(addresses[0]).IsLoopback() {
		// The data plane needs PostgreSQL's port on a per-server loopback
		// address. Linux provides loopback aliases natively, so a modeled
		// server there is a defect; macOS cannot provide one without root —
		// a host-kernel capability no test can install.
		if runtime.GOOS == "linux" {
			t.Fatalf("server %s resolves to %v (err %v): on Linux the data plane must install", fqdn, addresses, err)
		}
		t.Skipf("host cannot provide a loopback alias at PostgreSQL's port (name %s, addresses %v, err %v, GOOS %s)",
			fqdn, addresses, err, runtime.GOOS)
	}
	return addresses[0]
}

// waitPGDataAsyncOperation polls an Azure-AsyncOperation URL until the
// operation settles, failing on Failed — the budget covers a real volume
// capture, which pulls the helper image on first use.
func waitPGDataAsyncOperation(t *testing.T, opURL string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opURL, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", simARMBearer)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		if strings.Contains(string(body), `"status":"Succeeded"`) {
			return
		}
		require.NotContains(t, string(body), `"status":"Failed"`, "operation failed: %s", string(body))
		require.True(t, time.Now().Before(deadline), "operation did not settle: %s", string(body))
		time.Sleep(250 * time.Millisecond)
	}
}
