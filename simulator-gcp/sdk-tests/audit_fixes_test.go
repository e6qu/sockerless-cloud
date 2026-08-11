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

// TestFirestore_UpdateMaskPreservesFields covers Patch and Commit
// must honor updateMask and preserve unmentioned fields (DocumentRef.Update /
// Set MergeAll) instead of replacing the whole document.
func TestFirestore_UpdateMaskPreservesFields(t *testing.T) {
	svc, err := firestore.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	parent := "projects/test-project/databases/(default)"
	docName := parent + "/documents/mask-test/doc1"

	// Seed a document with two fields.
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

	// PATCH only field "a" with an updateMask — "b" must survive.
	_, err = svc.Projects.Databases.Documents.Patch(docName, &firestore.Document{
		Fields: map[string]firestore.Value{"a": {StringValue: "updated"}},
	}).UpdateMaskFieldPaths("a").Do()
	require.NoError(t, err)

	got, err := svc.Projects.Databases.Documents.Get(docName).Do()
	require.NoError(t, err)
	require.Contains(t, got.Fields, "b", "masked PATCH must preserve unmentioned field b")
	assert.Equal(t, "updated", got.Fields["a"].StringValue)
	assert.Equal(t, "keepme", got.Fields["b"].StringValue)

	// COMMIT with a per-write updateMask — same preservation.
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

// TestSecretManager_AccessDisabledVersionFails covers accessing a
// DISABLED version (or "latest" when the highest version is disabled) must
// return FAILED_PRECONDITION (400), not the payload.
func TestSecretManager_AccessDisabledVersionFails(t *testing.T) {
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

	// Enabled → access works.
	acc, err := svc.Projects.Secrets.Versions.Access(v1.Name).Do()
	require.NoError(t, err)
	require.NotNil(t, acc.Payload)

	// Disable the version.
	_, err = svc.Projects.Secrets.Versions.Disable(v1.Name, &secretmanager.DisableSecretVersionRequest{}).Do()
	require.NoError(t, err)

	// Explicit access now fails FAILED_PRECONDITION (400).
	_, err = svc.Projects.Secrets.Versions.Access(v1.Name).Do()
	requireGoogleErrCode(t, err, 400)

	// "latest" = highest version number (the disabled one) → also fails, it
	// does NOT fall back to an older enabled version.
	_, err = svc.Projects.Secrets.Versions.Access(secretName + "/versions/latest").Do()
	requireGoogleErrCode(t, err, 400)
}
