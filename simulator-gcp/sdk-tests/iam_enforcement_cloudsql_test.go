package gcp_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// Cloud SQL's backups take the cloudsql.backupRuns permissions its method
// table names, and its flag catalog takes none.
func TestIAMEnforcement_CloudSQLBackupsTakeBackupRunPermissions(t *testing.T) {
	t.Parallel()
	const project = "iam-enforce-sql-backups"
	ensureProject(t, project)
	email, ts := enforcementPrincipal(t, "enf-sql-viewer")
	asViewer, err := sqladmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(ts))
	require.NoError(t, err)

	_, err = asViewer.Flags.List().Do()
	require.NoError(t, err, "listing flags needs no permission")

	_, err = asViewer.Backups.ListBackups("projects/" + project).Do()
	requireSQLForbidden(t, err, "cloudsql.backupRuns.list")

	crm, err := cloudresourcemanager.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	policy, err := crm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{
		Role: "roles/cloudsql.viewer", Members: []string{"serviceAccount:" + email},
	})
	_, err = crm.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Do()
	require.NoError(t, err)

	_, err = asViewer.Backups.ListBackups("projects/" + project).Do()
	require.NoError(t, err, "the Cloud SQL Viewer holds cloudsql.backupRuns.list")
	_, err = asViewer.Backups.CreateBackup("projects/"+project, &sqladmin.Backup{Instance: "absent"}).Do()
	requireSQLForbidden(t, err, "cloudsql.backupRuns.create")
}

func requireSQLForbidden(t *testing.T, err error, permission string) {
	t.Helper()
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "want a googleapi error, got %v", err)
	require.Equal(t, http.StatusForbidden, apiErr.Code, "%v", err)
	require.Contains(t, apiErr.Message, permission)
}
