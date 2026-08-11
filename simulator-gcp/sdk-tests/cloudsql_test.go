package gcp_sdk_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

func sqlAdminService(t *testing.T) *sqladmin.Service {
	t.Helper()
	svc, err := sqladmin.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestCloudSQL_InstanceDatabaseUserLifecycle exercises:
// Insert instance → Get → List → Patch → Insert database → Insert
// user → List databases → List users → Delete instance.
func TestCloudSQL_InstanceDatabaseUserLifecycle(t *testing.T) {
	svc := sqlAdminService(t)
	project := "test-project"
	instanceName := "lifecycle-pg"

	op, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "DONE", op.Status)

	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, instanceName, inst.Name)
	assert.Equal(t, "RUNNABLE", inst.State)
	assert.Equal(t, "POSTGRES_15", inst.DatabaseVersion)
	assert.NotEmpty(t, inst.ConnectionName)

	list, err := svc.Instances.List(project).Do()
	require.NoError(t, err)
	found := false
	for _, x := range list.Items {
		if x.Name == instanceName {
			found = true
		}
	}
	assert.True(t, found)

	// Insert a database.
	_, err = svc.Databases.Insert(project, instanceName, &sqladmin.Database{
		Name: "appdb",
	}).Do()
	require.NoError(t, err)

	dbList, err := svc.Databases.List(project, instanceName).Do()
	require.NoError(t, err)
	require.Len(t, dbList.Items, 1)
	assert.Equal(t, "appdb", dbList.Items[0].Name)

	// Insert a user.
	_, err = svc.Users.Insert(project, instanceName, &sqladmin.User{
		Name: "appuser",
		Host: "%",
	}).Do()
	require.NoError(t, err)

	uList, err := svc.Users.List(project, instanceName).Do()
	require.NoError(t, err)
	require.Len(t, uList.Items, 1)
	assert.Equal(t, "appuser", uList.Items[0].Name)

	// Delete instance (cascade).
	_, err = svc.Instances.Delete(project, instanceName).Do()
	require.NoError(t, err)
}

func TestCloudSQL_NewPublishedInstanceFieldsRoundTrip(t *testing.T) {
	const (
		project  = "published-fields-project"
		instance = "published-fields"
	)
	createBody := []byte(`{
		"name":"published-fields",
		"databaseVersion":"MYSQL_8_0",
		"databaseCenterIntegrationEnabled":true,
		"onPremisesConfiguration":{"hostPort":"db.example.test:3306"}
	}`)
	req, err := http.NewRequest(http.MethodPost,
		baseURL+"/v1/projects/"+project+"/instances", bytes.NewReader(createBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	resp, err = http.Get(baseURL + "/v1/projects/" + project + "/instances/" + instance)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got struct {
		DatabaseCenterIntegrationEnabled *bool          `json:"databaseCenterIntegrationEnabled"`
		OnPremisesConfiguration          map[string]any `json:"onPremisesConfiguration"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.NotNil(t, got.DatabaseCenterIntegrationEnabled)
	assert.True(t, *got.DatabaseCenterIntegrationEnabled)
	assert.Equal(t, "db.example.test:3306", got.OnPremisesConfiguration["hostPort"])
	assert.Equal(t, false, got.OnPremisesConfiguration["dmsManaged"])

	userBody := []byte(`{"name":"operator","host":"%","serverRoles":["cloudsqlsuperuser"]}`)
	req, err = http.NewRequest(http.MethodPost,
		baseURL+"/v1/projects/"+project+"/instances/"+instance+"/users", bytes.NewReader(userBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	resp, err = http.Get(baseURL + "/v1/projects/" + project + "/instances/" + instance + "/users/operator?host=%25")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var user struct {
		ServerRoles []string `json:"serverRoles"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&user))
	assert.Equal(t, []string{"cloudsqlsuperuser"}, user.ServerRoles)
}

func TestCloudSQL_SQLServerRolesUpdateSemantics(t *testing.T) {
	svc := sqlAdminService(t)
	project := "test-project"
	instanceName := "roles-sqlserver"

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "SQLSERVER_2022_STANDARD",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	_, err = svc.Users.Insert(project, instanceName, &sqladmin.User{
		Name: "role-user",
		Host: "%",
		SqlserverUserDetails: &sqladmin.SqlServerUserDetails{
			ServerRoles: []string{"securityadmin"},
		},
	}).Do()
	require.NoError(t, err)

	_, err = svc.Users.Update(project, instanceName, &sqladmin.User{
		SqlserverUserDetails: &sqladmin.SqlServerUserDetails{
			ServerRoles: []string{"processadmin"},
		},
	}).Name("role-user").Host("%").Do()
	require.NoError(t, err)
	user, err := svc.Users.Get(project, instanceName, "role-user").Host("%").Do()
	require.NoError(t, err)
	require.NotNil(t, user.SqlserverUserDetails)
	assert.ElementsMatch(t, []string{"securityadmin", "processadmin"}, user.SqlserverUserDetails.ServerRoles)

	_, err = svc.Users.Update(project, instanceName, &sqladmin.User{
		SqlserverUserDetails: &sqladmin.SqlServerUserDetails{
			ServerRoles: []string{"bulkadmin"},
		},
	}).Name("role-user").Host("%").Do(
		googleapi.QueryParameter("revokeExistingServerRoles", "true"),
	)
	require.NoError(t, err)
	user, err = svc.Users.Get(project, instanceName, "role-user").Host("%").Do()
	require.NoError(t, err)
	require.NotNil(t, user.SqlserverUserDetails)
	assert.Equal(t, []string{"bulkadmin"}, user.SqlserverUserDetails.ServerRoles)
}

func TestCloudSQL_BackupRunsReturnOperations(t *testing.T) {
	svc := sqlAdminService(t)
	project := "test-project"
	instanceName := "backup-pg"

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	insertOp, err := svc.BackupRuns.Insert(project, instanceName, &sqladmin.BackupRun{}).Do()
	require.NoError(t, err)
	require.Equal(t, "sql#operation", insertOp.Kind)
	require.Equal(t, "DONE", insertOp.Status)
	require.Equal(t, "BACKUP_VOLUME", insertOp.OperationType)

	list, err := svc.BackupRuns.List(project, instanceName).Do()
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	deleteOp, err := svc.BackupRuns.Delete(project, instanceName, list.Items[0].Id).Do()
	require.NoError(t, err)
	require.Equal(t, "sql#operation", deleteOp.Kind)
	require.Equal(t, "DONE", deleteOp.Status)
	require.Equal(t, "DELETE_BACKUP", deleteOp.OperationType)
}

// TestCloudSQL_InstanceUpdateAndActions exercises instances.update (PUT) and
// a representative instances.* action verb (restart), both of which return
// the canonical DONE Operation.
func TestCloudSQL_InstanceUpdateAndActions(t *testing.T) {
	svc := sqlAdminService(t)
	project := "test-project"
	instanceName := "update-pg"

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	// instances.update — full-replace PUT returning an Operation.
	updateOp, err := svc.Instances.Update(project, instanceName, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		DatabaseVersion: "POSTGRES_16",
		Settings:        &sqladmin.Settings{Tier: "db-custom-2-7680"},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "sql#operation", updateOp.Kind)
	require.Equal(t, "DONE", updateOp.Status)
	require.Equal(t, "UPDATE", updateOp.OperationType)

	inst, err := svc.Instances.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_16", inst.DatabaseVersion)

	// instances.restart — action verb returning an Operation.
	restartOp, err := svc.Instances.Restart(project, instanceName).Do()
	require.NoError(t, err)
	require.Equal(t, "DONE", restartOp.Status)
	require.Equal(t, "RESTART", restartOp.OperationType)
}

// TestCloudSQL_FlagsAndTiers exercises the static reference surfaces
// flags.list and tiers.list, asserting real Cloud SQL flag/tier shapes.
func TestCloudSQL_FlagsAndTiers(t *testing.T) {
	svc := sqlAdminService(t)

	flags, err := svc.Flags.List().Do()
	require.NoError(t, err)
	require.NotEmpty(t, flags.Items)
	byName := map[string]*sqladmin.Flag{}
	for _, f := range flags.Items {
		byName[f.Name] = f
	}
	mc, ok := byName["max_connections"]
	require.True(t, ok, "max_connections flag present")
	assert.Equal(t, "INTEGER", mc.Type)
	assert.NotEmpty(t, mc.AppliesTo)

	tiers, err := svc.Tiers.List("test-project").Do()
	require.NoError(t, err)
	require.NotEmpty(t, tiers.Items)
	foundTier := false
	for _, x := range tiers.Items {
		if x.Tier == "db-f1-micro" {
			foundTier = true
			assert.Greater(t, x.RAM, int64(0))
			assert.NotEmpty(t, x.Region)
		}
	}
	assert.True(t, foundTier, "db-f1-micro tier present")
}

// TestCloudSQL_BackupsLifecycle exercises the projects.backups resource
// (distinct from the per-instance backupRuns): CreateBackup → ListBackups →
// GetBackup → UpdateBackup → DeleteBackup.
func TestCloudSQL_BackupsLifecycle(t *testing.T) {
	svc := sqlAdminService(t)
	project := "test-project"
	parent := "projects/" + project
	instanceName := "backups-pg"

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	createOp, err := svc.Backups.CreateBackup(parent, &sqladmin.Backup{
		Instance:    parent + "/instances/" + instanceName,
		Description: "nightly",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "DONE", createOp.Status)
	require.Equal(t, "CREATE_BACKUP", createOp.OperationType)

	list, err := svc.Backups.ListBackups(parent).Do()
	require.NoError(t, err)
	require.Len(t, list.Backups, 1)
	backupName := list.Backups[0].Name
	require.NotEmpty(t, backupName)

	got, err := svc.Backups.GetBackup(backupName).Do()
	require.NoError(t, err)
	assert.Equal(t, "nightly", got.Description)
	assert.Equal(t, "SUCCESSFUL", got.State)

	updateOp, err := svc.Backups.UpdateBackup(backupName, &sqladmin.Backup{
		Description: "weekly",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "DONE", updateOp.Status)
	require.Equal(t, "UPDATE_BACKUP", updateOp.OperationType)

	got2, err := svc.Backups.GetBackup(backupName).Do()
	require.NoError(t, err)
	assert.Equal(t, "weekly", got2.Description)

	deleteOp, err := svc.Backups.DeleteBackup(backupName).Do()
	require.NoError(t, err)
	require.Equal(t, "DONE", deleteOp.Status)
	require.Equal(t, "DELETE_BACKUP", deleteOp.OperationType)
}

// TestCloudSQL_SslCertsAndConnect exercises the sslCerts resource lifecycle
// plus connect.get (connectSettings) and instances.listServerCas.
func TestCloudSQL_SslCertsAndConnect(t *testing.T) {
	svc := sqlAdminService(t)
	project := "test-project"
	instanceName := "ssl-pg"

	_, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instanceName).Do() })

	ins, err := svc.SslCerts.Insert(project, instanceName, &sqladmin.SslCertsInsertRequest{
		CommonName: "client-cert",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, ins.Operation)
	require.Equal(t, "DONE", ins.Operation.Status)

	certs, err := svc.SslCerts.List(project, instanceName).Do()
	require.NoError(t, err)
	require.Len(t, certs.Items, 1)
	fp := certs.Items[0].Sha1Fingerprint
	require.NotEmpty(t, fp)

	got, err := svc.SslCerts.Get(project, instanceName, fp).Do()
	require.NoError(t, err)
	assert.Equal(t, fp, got.Sha1Fingerprint)

	cs, err := svc.Connect.Get(project, instanceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_15", cs.DatabaseVersion)
	require.NotNil(t, cs.ServerCaCert)

	cas, err := svc.Instances.ListServerCas(project, instanceName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, cas.Certs)

	_, err = svc.SslCerts.Delete(project, instanceName, fp).Do()
	require.NoError(t, err)
}
