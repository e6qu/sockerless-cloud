package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storageapi "google.golang.org/api/storage/v1"
)

// Object IAM, through the generated Go client:
//
//	GET /storage/v1/b/{bucket}/o/{object}/iam
//	PUT /storage/v1/b/{bucket}/o/{object}/iam
//	GET /storage/v1/b/{bucket}/o/{object}/iam/testPermissions

func TestGCS_ObjectIamPolicyRoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "object-iam-bucket")
	mustUploadObject(t, svc, "object-iam-bucket", "reports/q3.txt", "figures")

	// An object with no policy of its own still has one to read.
	policy, err := svc.Objects.GetIamPolicy("object-iam-bucket", "reports/q3.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#policy", policy.Kind)
	assert.Equal(t, "projects/_/buckets/object-iam-bucket/objects/reports/q3.txt", policy.ResourceId)
	assert.NotEmpty(t, policy.Etag)
	assert.Empty(t, policy.Bindings)

	set, err := svc.Objects.SetIamPolicy("object-iam-bucket", "reports/q3.txt", &storageapi.Policy{
		Bindings: []*storageapi.PolicyBindings{{
			Role:    "roles/storage.objectViewer",
			Members: []string{"user:reader@example.com"},
		}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	assert.Equal(t, "roles/storage.objectViewer", set.Bindings[0].Role)

	// The binding is stored against the object, not merely echoed.
	reread, err := svc.Objects.GetIamPolicy("object-iam-bucket", "reports/q3.txt").Do()
	require.NoError(t, err)
	require.Len(t, reread.Bindings, 1)
	assert.Equal(t, []string{"user:reader@example.com"}, reread.Bindings[0].Members)

	// A sibling object carries its own policy, so the write did not land on
	// the bucket or on every object under it.
	mustUploadObject(t, svc, "object-iam-bucket", "reports/q4.txt", "later")
	sibling, err := svc.Objects.GetIamPolicy("object-iam-bucket", "reports/q4.txt").Do()
	require.NoError(t, err)
	assert.Empty(t, sibling.Bindings)

	permissions, err := svc.Objects.TestIamPermissions("object-iam-bucket", "reports/q3.txt",
		[]string{"storage.objects.get", "storage.objects.update"}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.get", "storage.objects.update"}, permissions.Permissions)
}

// The route names the object rather than falling into the objects.get
// catch-all, so an absent object reports itself and an absent bucket reports
// the bucket.
func TestGCS_ObjectIamReportsWhatIsMissing(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "object-iam-absent")

	_, err := svc.Objects.GetIamPolicy("object-iam-absent", "nothing.txt").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `object "nothing.txt" not found`)

	_, err = svc.Objects.GetIamPolicy("no-such-object-iam-bucket", "nothing.txt").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket")
}
