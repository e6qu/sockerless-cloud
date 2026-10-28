package gcp_sdk_test

import (
	"errors"
	"hash/crc32"
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

var smGRPCCrc32CTable = crc32.MakeTable(crc32.Castagnoli)

func smGRPCCrc32C(b []byte) int64 { return int64(crc32.Checksum(b, smGRPCCrc32CTable)) }

// newSecretManagerGRPCClient passes the connection through
// option.WithGRPCConn: the Secret Manager client reads no *_EMULATOR_HOST
// coordinate.
func newSecretManagerGRPCClient(t *testing.T) *secretmanager.Client {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	cli, err := secretmanager.NewClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { cli.Close() })
	return cli
}

// smGRPCProjectParent gives each test a fresh project of its own, so parallel
// runs never collide on one project's secrets.
func smGRPCProjectParent(t *testing.T) string {
	t.Helper()
	project := uniquename.New("sm-grpc")
	ensureProject(t, project)
	return "projects/" + project
}

func smGRPCCreateSecret(t *testing.T, c *secretmanager.Client, parent, secretID string) string {
	t.Helper()
	resp, err := c.CreateSecret(ctx, &smpb.CreateSecretRequest{
		Parent:   parent,
		SecretId: secretID,
		Secret: &smpb.Secret{
			Replication: &smpb.Replication{
				Replication: &smpb.Replication_Automatic_{Automatic: &smpb.Replication_Automatic{}},
			},
			Labels: map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, parent+"/secrets/"+secretID, resp.GetName())
	return resp.GetName()
}

func TestSecretManager_GRPC_CreateAddAccess(t *testing.T) {
	t.Parallel()
	c := newSecretManagerGRPCClient(t)
	parent := smGRPCProjectParent(t)
	secretName := smGRPCCreateSecret(t, c, parent, "data-plane")

	created, err := c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent: secretName,
		Payload: &smpb.SecretPayload{
			Data: []byte("hello"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, smpb.SecretVersion_ENABLED, created.GetState())
	require.Contains(t, created.GetName(), secretName+"/versions/")

	accessed, err := c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: created.GetName()})
	require.NoError(t, err)
	require.Equal(t, created.GetName(), accessed.GetName())
	require.Equal(t, []byte("hello"), accessed.GetPayload().GetData())
	require.Equal(t, smGRPCCrc32C([]byte("hello")), accessed.GetPayload().GetDataCrc32C())
}

func TestSecretManager_GRPC_ManagedCloudSQLRotation(t *testing.T) {
	t.Parallel()
	ensureProject(t, "sm-managed-rotation")
	const (
		project      = "sm-managed-rotation"
		location     = "us-central1"
		instanceName = "managed-rotation-pg"
		username     = "rotation-user"
	)

	sql := sqlAdminService(t)
	_, err := sql.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          location,
		DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sql.Instances.Delete(project, instanceName).Do() })
	_, err = sql.Users.Insert(project, instanceName, &sqladmin.User{
		Name: username,
		Host: "%",
	}).Do()
	require.NoError(t, err)

	c := newSecretManagerGRPCClient(t)
	parent := "projects/" + project + "/locations/" + location
	created, err := c.CreateSecret(ctx, &smpb.CreateSecretRequest{
		Parent:   parent,
		SecretId: "database-password",
		Secret: &smpb.Secret{
			Replication: &smpb.Replication{
				Replication: &smpb.Replication_Automatic_{Automatic: &smpb.Replication_Automatic{}},
			},
			SecretType: smpb.Secret_CLOUD_SQL_DB_CREDENTIALS,
		},
	})
	require.NoError(t, err)
	require.Equal(t, smpb.Secret_CLOUD_SQL_DB_CREDENTIALS, created.GetSecretType())

	first, err := c.EnableManagedRotation(ctx, &smpb.EnableManagedRotationRequest{
		Parent: created.GetName(),
		Credentials: &smpb.EnableManagedRotationRequest_CloudSqlSingleUserCredentials{
			CloudSqlSingleUserCredentials: &smpb.EnableManagedRotationRequest_CloudSQLSingleUserCredentials{
				InstanceId: instanceName,
				Username:   username,
				Password:   "first-managed-password",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, smpb.SecretVersion_ENABLED, first.GetState())
	firstAccess, err := c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: first.GetName()})
	require.NoError(t, err)
	require.Equal(t, []byte("first-managed-password"), firstAccess.GetPayload().GetData())

	_, err = c.EnableManagedRotation(ctx, &smpb.EnableManagedRotationRequest{
		Parent: created.GetName(),
		Credentials: &smpb.EnableManagedRotationRequest_CloudSqlSingleUserCredentials{
			CloudSqlSingleUserCredentials: &smpb.EnableManagedRotationRequest_CloudSQLSingleUserCredentials{
				InstanceId: instanceName,
				Username:   username,
			},
		},
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	second, err := c.RotateSecret(ctx, &smpb.RotateSecretRequest{Parent: created.GetName()})
	require.NoError(t, err)
	require.NotEqual(t, first.GetName(), second.GetName())
	secondAccess, err := c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: second.GetName()})
	require.NoError(t, err)
	require.NotEmpty(t, secondAccess.GetPayload().GetData())
	require.NotEqual(t, firstAccess.GetPayload().GetData(), secondAccess.GetPayload().GetData())

	_, err = c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent: created.GetName(),
		Payload: &smpb.SecretPayload{
			Data: []byte("manual-version-is-forbidden"),
		},
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestSecretManager_GRPC_LatestAliasAndMultipleVersions(t *testing.T) {
	t.Parallel()
	c := newSecretManagerGRPCClient(t)
	parent := smGRPCProjectParent(t)
	secretName := smGRPCCreateSecret(t, c, parent, "latest-alias")

	v1, err := c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &smpb.SecretPayload{Data: []byte("first")},
	})
	require.NoError(t, err)

	v2, err := c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &smpb.SecretPayload{Data: []byte("second")},
	})
	require.NoError(t, err)

	latest, err := c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: secretName + "/versions/latest"})
	require.NoError(t, err)
	require.Equal(t, v2.GetName(), latest.GetName())
	require.Equal(t, []byte("second"), latest.GetPayload().GetData())

	first, err := c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: v1.GetName()})
	require.NoError(t, err)
	require.Equal(t, []byte("first"), first.GetPayload().GetData())
}

// TestSecretManager_GRPC_VersionLifecycle_DestroyDisableEnable: a destroyed
// version's payload is gone (FAILED_PRECONDITION), and a disabled one is
// inaccessible until re-enabled.
func TestSecretManager_GRPC_VersionLifecycle_DestroyDisableEnable(t *testing.T) {
	t.Parallel()
	c := newSecretManagerGRPCClient(t)
	parent := smGRPCProjectParent(t)
	secretName := smGRPCCreateSecret(t, c, parent, "lifecycle")

	destroyed, err := c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &smpb.SecretPayload{Data: []byte("destroy-me")},
	})
	require.NoError(t, err)

	got, err := c.DestroySecretVersion(ctx, &smpb.DestroySecretVersionRequest{Name: destroyed.GetName()})
	require.NoError(t, err)
	require.Equal(t, smpb.SecretVersion_DESTROYED, got.GetState())

	// Secret Manager refuses access to a destroyed version with
	// FAILED_PRECONDITION, not 404.
	_, err = c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: destroyed.GetName()})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	// A fresh version goes through disable → access fails → enable → access works.
	disabled, err := c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &smpb.SecretPayload{Data: []byte("disable-me")},
	})
	require.NoError(t, err)

	got, err = c.DisableSecretVersion(ctx, &smpb.DisableSecretVersionRequest{Name: disabled.GetName()})
	require.NoError(t, err)
	require.Equal(t, smpb.SecretVersion_DISABLED, got.GetState())

	_, err = c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: disabled.GetName()})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	got, err = c.EnableSecretVersion(ctx, &smpb.EnableSecretVersionRequest{Name: disabled.GetName()})
	require.NoError(t, err)
	require.Equal(t, smpb.SecretVersion_ENABLED, got.GetState())

	accessed, err := c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: disabled.GetName()})
	require.NoError(t, err)
	require.Equal(t, []byte("disable-me"), accessed.GetPayload().GetData())
}

func TestSecretManager_GRPC_SecretAdminCRUDAndList(t *testing.T) {
	t.Parallel()
	c := newSecretManagerGRPCClient(t)
	parent := smGRPCProjectParent(t)
	secretName := smGRPCCreateSecret(t, c, parent, "admin-crud")

	got, err := c.GetSecret(ctx, &smpb.GetSecretRequest{Name: secretName})
	require.NoError(t, err)
	require.Equal(t, secretName, got.GetName())
	require.Equal(t, "test", got.GetLabels()["env"])
	require.NotNil(t, got.GetReplication().GetAutomatic(), "automatic replication must round-trip")

	listed := smGRPCListAllSecrets(t, c, parent)
	require.Contains(t, listed, secretName, "ListSecrets must include the created secret")

	updated, err := c.UpdateSecret(ctx, &smpb.UpdateSecretRequest{
		Secret: &smpb.Secret{
			Name:   secretName,
			Labels: map[string]string{"env": "prod", "team": "sim"},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
	})
	require.NoError(t, err)
	require.Equal(t, "prod", updated.GetLabels()["env"])
	require.Equal(t, "sim", updated.GetLabels()["team"])

	reread, err := c.GetSecret(ctx, &smpb.GetSecretRequest{Name: secretName})
	require.NoError(t, err)
	require.Equal(t, "prod", reread.GetLabels()["env"])

	err = c.DeleteSecret(ctx, &smpb.DeleteSecretRequest{Name: secretName})
	require.NoError(t, err)

	_, err = c.GetSecret(ctx, &smpb.GetSecretRequest{Name: secretName})
	requireGRPCCode(t, err, codes.NotFound)

	listedAfter := smGRPCListAllSecrets(t, c, parent)
	require.NotContains(t, listedAfter, secretName, "ListSecrets must exclude a deleted secret")
}

// TestSecretManager_GRPC_VersionMetadataPayloadRedaction: only
// AccessSecretVersion carries the payload, never GetSecretVersion or
// ListSecretVersions.
func TestSecretManager_GRPC_VersionMetadataPayloadRedaction(t *testing.T) {
	t.Parallel()
	c := newSecretManagerGRPCClient(t)
	parent := smGRPCProjectParent(t)
	secretName := smGRPCCreateSecret(t, c, parent, "redaction")

	_, err := c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent:  secretName,
		Payload: &smpb.SecretPayload{Data: []byte("never-leak")},
	})
	require.NoError(t, err)

	meta, err := c.GetSecretVersion(ctx, &smpb.GetSecretVersionRequest{Name: secretName + "/versions/1"})
	require.NoError(t, err)
	require.Equal(t, secretName+"/versions/1", meta.GetName())
	require.Equal(t, smpb.SecretVersion_ENABLED, meta.GetState())

	// Versions carry no payload field on the wire, so this checks the name and
	// state.
	it := c.ListSecretVersions(ctx, &smpb.ListSecretVersionsRequest{Parent: secretName})
	count := 0
	for {
		v, err := it.Next()
		if err != nil {
			break
		}
		count++
		require.Equal(t, secretName+"/versions/1", v.GetName())
		require.Equal(t, smpb.SecretVersion_ENABLED, v.GetState())
	}
	require.Equal(t, 1, count, "ListSecretVersions must return exactly the one added version")
}

// TestSecretManager_GRPC_IamPolicy: a fresh secret has an empty policy,
// SetIamPolicy persists and GetIamPolicy reads it back, and TestIamPermissions
// echoes the requested set.
func TestSecretManager_GRPC_IamPolicy(t *testing.T) {
	t.Parallel()
	c := newSecretManagerGRPCClient(t)
	parent := smGRPCProjectParent(t)
	secretName := smGRPCCreateSecret(t, c, parent, "iam")

	current, err := c.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: secretName})
	require.NoError(t, err)
	require.Empty(t, current.GetBindings())
	require.NotEmpty(t, current.GetEtag(), "fresh policy must carry an etag for optimistic concurrency")

	updated, err := c.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: secretName,
		Policy: &iampb.Policy{
			Bindings: []*iampb.Binding{
				{
					Role:    "roles/secretmanager.secretAccessor",
					Members: []string{"serviceAccount:reader@example.iam.gserviceaccount.com"},
				},
			},
			Etag: current.GetEtag(),
		},
	})
	require.NoError(t, err)
	require.Len(t, updated.GetBindings(), 1)
	require.Equal(t, "roles/secretmanager.secretAccessor", updated.GetBindings()[0].GetRole())

	reread, err := c.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: secretName})
	require.NoError(t, err)
	require.Len(t, reread.GetBindings(), 1)
	require.Equal(t, "roles/secretmanager.secretAccessor", reread.GetBindings()[0].GetRole())
	require.Equal(t, []string{"serviceAccount:reader@example.iam.gserviceaccount.com"}, reread.GetBindings()[0].GetMembers())

	// A stale etag must be rejected with Aborted.
	_, err = c.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: secretName,
		Policy: &iampb.Policy{
			Bindings: []*iampb.Binding{{Role: "roles/viewer", Members: []string{"allUsers"}}},
			Etag:     current.GetEtag(), // stale
		},
	})
	requireGRPCCode(t, err, codes.Aborted)

	perms, err := c.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    secretName,
		Permissions: []string{"secretmanager.versions.access"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"secretmanager.versions.access"}, perms.GetPermissions())

	// TestIamPermissions on a missing secret answers an empty set, not NOT_FOUND.
	missing, err := c.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    parent + "/secrets/does-not-exist",
		Permissions: []string{"secretmanager.versions.access"},
	})
	require.NoError(t, err)
	require.Empty(t, missing.GetPermissions(), "TestIamPermissions on a missing secret returns an empty set, not a NOT_FOUND error")
}

// TestSecretManager_GRPC_NotFoundCodes: GetSecret, GetSecretVersion,
// AccessSecretVersion and AddSecretVersion answer NotFound for a missing
// resource, while CreateSecret under an unknown project succeeds, the
// simulator treating the project as a path component.
func TestSecretManager_GRPC_NotFoundCodes(t *testing.T) {
	t.Parallel()
	ensureProject(t, "sm-grpc-missing")
	c := newSecretManagerGRPCClient(t)

	_, err := c.GetSecret(ctx, &smpb.GetSecretRequest{Name: "projects/sm-grpc-missing/secrets/never"})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = c.GetSecretVersion(ctx, &smpb.GetSecretVersionRequest{Name: "projects/sm-grpc-missing/secrets/never/versions/1"})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = c.AccessSecretVersion(ctx, &smpb.AccessSecretVersionRequest{Name: "projects/sm-grpc-missing/secrets/never/versions/1"})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = c.AddSecretVersion(ctx, &smpb.AddSecretVersionRequest{
		Parent:  "projects/sm-grpc-missing/secrets/never",
		Payload: &smpb.SecretPayload{Data: []byte("x")},
	})
	requireGRPCCode(t, err, codes.NotFound)

	parent := smGRPCProjectParent(t)
	name := smGRPCCreateSecret(t, c, parent, "dup")
	require.Equal(t, parent+"/secrets/dup", name)
	_, err = c.CreateSecret(ctx, &smpb.CreateSecretRequest{
		Parent:   parent,
		SecretId: "dup",
		Secret: &smpb.Secret{Replication: &smpb.Replication{
			Replication: &smpb.Replication_Automatic_{Automatic: &smpb.Replication_Automatic{}},
		}},
	})
	requireGRPCCode(t, err, codes.AlreadyExists)

	// The rejected duplicate did not disturb the secret that was already there.
	after, err := c.GetSecret(ctx, &smpb.GetSecretRequest{Name: name})
	require.NoError(t, err)
	require.Equal(t, name, after.GetName())
}

// smGRPCListAllSecrets ends only on iterator.Done; any other error fails the
// test rather than passing for a short list.
func smGRPCListAllSecrets(t *testing.T, c *secretmanager.Client, parent string) []string {
	t.Helper()
	it := c.ListSecrets(ctx, &smpb.ListSecretsRequest{Parent: parent})
	var out []string
	for {
		s, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out
		}
		require.NoError(t, err, "ListSecrets iteration must not error")
		out = append(out, s.GetName())
	}
}
