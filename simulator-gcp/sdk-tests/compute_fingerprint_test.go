package gcp_sdk_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

// TestCompute_Disks_SetLabelsFingerprintValidation asserts the sim enforces
// optimistic concurrency on setLabels (BUG-1843): a real per-resource
// fingerprint is returned (not a constant), a stale labelFingerprint is
// rejected with 412 (conditionNotMet), and the fingerprint rotates on every
// successful write so the previously-current value becomes stale.
func TestCompute_Disks_SetLabelsFingerprintValidation(t *testing.T) {
	svc := computeService(t)
	const project = "test-project"
	const zone = "us-central1-a"
	const name = "fp-disk-1"

	_, err := svc.Disks.Insert(project, zone, &compute.Disk{Name: name, SizeGb: 10}).Context(ctx).Do()
	require.NoError(t, err)
	got, err := svc.Disks.Get(project, zone, name).Context(ctx).Do()
	require.NoError(t, err)
	require.NotEmpty(t, got.LabelFingerprint, "a real per-resource fingerprint is returned")
	assert.NotEqual(t, "c29ja2VybGVzcw==", got.LabelFingerprint, "must not be the old constant fingerprint")

	// Stale fingerprint -> 412.
	_, err = svc.Disks.SetLabels(project, zone, name, &compute.ZoneSetLabelsRequest{
		Labels:           map[string]string{"a": "1"},
		LabelFingerprint: "stale-fingerprint",
	}).Context(ctx).Do()
	requireGoogleAPICode(t, err, 412)

	// Current fingerprint -> success, and the fingerprint rotates.
	_, err = svc.Disks.SetLabels(project, zone, name, &compute.ZoneSetLabelsRequest{
		Labels:           map[string]string{"a": "1"},
		LabelFingerprint: got.LabelFingerprint,
	}).Context(ctx).Do()
	require.NoError(t, err)
	after, err := svc.Disks.Get(project, zone, name).Context(ctx).Do()
	require.NoError(t, err)
	assert.NotEqual(t, got.LabelFingerprint, after.LabelFingerprint, "fingerprint rotates on a successful write")

	// The previously-current fingerprint is now stale -> 412.
	_, err = svc.Disks.SetLabels(project, zone, name, &compute.ZoneSetLabelsRequest{
		Labels:           map[string]string{"a": "2"},
		LabelFingerprint: got.LabelFingerprint,
	}).Context(ctx).Do()
	requireGoogleAPICode(t, err, 412)

	_, _ = svc.Disks.Delete(project, zone, name).Context(ctx).Do()
}

func requireGoogleAPICode(t *testing.T, err error, code int) {
	t.Helper()
	require.Error(t, err)
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "expected googleapi.Error, got %T: %v", err, err)
	assert.Equal(t, code, gerr.Code)
}
