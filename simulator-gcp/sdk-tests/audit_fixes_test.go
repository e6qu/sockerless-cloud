package gcp_sdk_test

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/firestore/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

func requireGoogleErrCode(t *testing.T, err error, code int) {
	t.Helper()
	require.Error(t, err)
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	require.Equal(t, code, gerr.Code)
}

// TestFirestore_UpdateMaskPreservesFields: Patch and Commit honour updateMask
// and keep the fields it does not name.
func TestFirestore_UpdateMaskPreservesFields(t *testing.T) {
	t.Parallel()
	svc, err := firestore.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	parent := "projects/test-project/databases/(default)"
	docName := parent + "/documents/mask-test/doc1"

	_, err = svc.Projects.Databases.Documents.Commit(parent, &firestore.CommitRequest{
		Writes: []*firestore.Write{{
			Update: &firestore.Document{
				Name: docName,
				Fields: map[string]firestore.Value{
					"a": {StringValue: "first"},
					"b": {StringValue: "keepme"},
				},
			},
		}},
	}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Databases.Documents.Patch(docName, &firestore.Document{
		Fields: map[string]firestore.Value{"a": {StringValue: "updated"}},
	}).UpdateMaskFieldPaths("a").Do()
	require.NoError(t, err)

	got, err := svc.Projects.Databases.Documents.Get(docName).Do()
	require.NoError(t, err)
	require.Contains(t, got.Fields, "b", "masked PATCH must preserve unmentioned field b")
	assert.Equal(t, "updated", got.Fields["a"].StringValue)
	assert.Equal(t, "keepme", got.Fields["b"].StringValue)

	_, err = svc.Projects.Databases.Documents.Commit(parent, &firestore.CommitRequest{
		Writes: []*firestore.Write{{
			Update:     &firestore.Document{Name: docName, Fields: map[string]firestore.Value{"a": {StringValue: "v3"}}},
			UpdateMask: &firestore.DocumentMask{FieldPaths: []string{"a"}},
		}},
	}).Do()
	require.NoError(t, err)

	got, err = svc.Projects.Databases.Documents.Get(docName).Do()
	require.NoError(t, err)
	require.Contains(t, got.Fields, "b", "masked COMMIT must preserve unmentioned field b")
	assert.Equal(t, "v3", got.Fields["a"].StringValue)
	assert.Equal(t, "keepme", got.Fields["b"].StringValue)
}

// TestSecretManager_AccessDisabledVersionFails: accessing a DISABLED version,
// or `latest` when the newest version is disabled, answers FAILED_PRECONDITION.
func TestSecretManager_AccessDisabledVersionFails(t *testing.T) {
	t.Parallel()
	ensureProject(t, "sdk-disabled-project")
	svc := secretManagerService(t)
	parent := "projects/sdk-disabled-project"
	secretID := "disabled-access-secret"
	secretName := parent + "/secrets/" + secretID

	_, err := svc.Projects.Secrets.Create(parent, &secretmanager.Secret{}).SecretId(secretID).Do()
	require.NoError(t, err)
	v1, err := svc.Projects.Secrets.AddVersion(secretName, &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString([]byte("topsecret"))},
	}).Do()
	require.NoError(t, err)

	acc, err := svc.Projects.Secrets.Versions.Access(v1.Name).Do()
	require.NoError(t, err)
	require.NotNil(t, acc.Payload)

	_, err = svc.Projects.Secrets.Versions.Disable(v1.Name, &secretmanager.DisableSecretVersionRequest{}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Secrets.Versions.Access(v1.Name).Do()
	requireGoogleErrCode(t, err, 400)

	// `latest` resolves to the disabled newest version; it does not fall back to
	// an older enabled one.
	_, err = svc.Projects.Secrets.Versions.Access(secretName + "/versions/latest").Do()
	requireGoogleErrCode(t, err, 400)
}
