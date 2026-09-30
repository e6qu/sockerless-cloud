package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	storageapi "google.golang.org/api/storage/v1"
)

// Object IAM, through the generated Go client:
//
//	GET /storage/v1/b/{bucket}/o/{object}/iam
//	PUT /storage/v1/b/{bucket}/o/{object}/iam
//	GET /storage/v1/b/{bucket}/o/{object}/iam/testPermissions

func TestGCS_ObjectIamPolicyRoundTrip(t *testing.T) {
	bucketObjectIamBucket := uniqueName("object-iam-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketObjectIamBucket)
	mustUploadObject(t, svc, bucketObjectIamBucket, "reports/q3.txt", "figures")

	// An object with no policy of its own still has one to read.
	policy, err := svc.Objects.GetIamPolicy(bucketObjectIamBucket, "reports/q3.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#policy", policy.Kind)
	assert.Equal(t, "projects/_/buckets/"+bucketObjectIamBucket+"/objects/reports/q3.txt", policy.ResourceId)
	assert.NotEmpty(t, policy.Etag)
	assert.Empty(t, policy.Bindings)

	set, err := svc.Objects.SetIamPolicy(bucketObjectIamBucket, "reports/q3.txt", &storageapi.Policy{
		Bindings: []*storageapi.PolicyBindings{{
			Role:    "roles/storage.objectViewer",
			Members: []string{"user:reader@example.com"},
		}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	assert.Equal(t, "roles/storage.objectViewer", set.Bindings[0].Role)

	// The binding is stored against the object, not merely echoed.
	reread, err := svc.Objects.GetIamPolicy(bucketObjectIamBucket, "reports/q3.txt").Do()
	require.NoError(t, err)
	require.Len(t, reread.Bindings, 1)
	assert.Equal(t, []string{"user:reader@example.com"}, reread.Bindings[0].Members)

	// A sibling object carries its own policy, so the write did not land on
	// the bucket or on every object under it.
	mustUploadObject(t, svc, bucketObjectIamBucket, "reports/q4.txt", "later")
	sibling, err := svc.Objects.GetIamPolicy(bucketObjectIamBucket, "reports/q4.txt").Do()
	require.NoError(t, err)
	assert.Empty(t, sibling.Bindings)

	permissions, err := svc.Objects.TestIamPermissions(bucketObjectIamBucket, "reports/q3.txt",
		[]string{"storage.objects.get", "storage.objects.update"}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.get", "storage.objects.update"}, permissions.Permissions)
}

// The route names the object rather than falling into the objects.get
// catch-all, so an absent object reports itself and an absent bucket reports
// the bucket.
func TestGCS_ObjectIamReportsWhatIsMissing(t *testing.T) {
	bucketObjectIamAbsent := uniqueName("object-iam-absent")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketObjectIamAbsent)

	_, err := svc.Objects.GetIamPolicy(bucketObjectIamAbsent, "nothing.txt").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `object "nothing.txt" not found`)

	_, err = svc.Objects.GetIamPolicy("no-such-object-iam-bucket", "nothing.txt").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket")
}

// A service account's object permissions come from the policies that govern
// the object — the bucket's, the object's own and, while uniform bucket-level
// access is off, the object's ACL — and not from what it asks about.
func TestGCS_ObjectTestPermissionsAnswerFromPoliciesAndACLs(t *testing.T) {
	owner := storageService(t)
	bucket := uniqueName("object-perms")
	mustCreateBucket(t, owner, bucket)
	mustUploadObject(t, owner, bucket, "held.txt", "figures")

	accountID := uniqueName("objperm")
	_, _, keyFile := mintServiceAccountKeyFile(t, accountID)
	email := accountID + "@test-project.iam.gserviceaccount.com"
	asAccount, err := storageapi.NewService(ctx,
		option.WithEndpoint(baseURL+"/storage/v1/"),
		option.WithTokenSource(tokenSourceFromKeyFile(t, keyFile).TokenSource))
	require.NoError(t, err)

	asked := []string{"storage.objects.get", "storage.objects.update", "storage.objects.delete"}
	held := func() []string {
		t.Helper()
		answer, err := asAccount.Objects.TestIamPermissions(bucket, "held.txt", asked).Do()
		require.NoError(t, err)
		return answer.Permissions
	}

	assert.Empty(t, held(), "no policy or ACL names the account")

	_, err = owner.Objects.SetIamPolicy(bucket, "held.txt", &storageapi.Policy{
		Bindings: []*storageapi.PolicyBindings{{
			Role: "roles/storage.legacyObjectReader", Members: []string{"serviceAccount:" + email},
		}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.get"}, held(), "the object's policy grants the read")

	_, err = owner.ObjectAccessControls.Insert(bucket, "held.txt", &storageapi.ObjectAccessControl{
		Entity: "user-" + email, Role: "OWNER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.get", "storage.objects.update"}, held(),
		"an OWNER ACL grant adds the legacy object owner's update")

	_, err = owner.Buckets.Patch(bucket, &storageapi.Bucket{
		IamConfiguration: &storageapi.BucketIamConfiguration{
			UniformBucketLevelAccess: &storageapi.BucketIamConfigurationUniformBucketLevelAccess{Enabled: true},
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.get"}, held(),
		"uniform bucket-level access disables the ACL")

	bucketPolicy, err := owner.Buckets.GetIamPolicy(bucket).Do()
	require.NoError(t, err)
	bucketPolicy.Bindings = append(bucketPolicy.Bindings, &storageapi.PolicyBindings{
		Role: "roles/storage.objectAdmin", Members: []string{"serviceAccount:" + email},
	})
	_, err = owner.Buckets.SetIamPolicy(bucket, bucketPolicy).Do()
	require.NoError(t, err)
	assert.Equal(t, asked, held(), "the bucket's policy reaches its objects")

	bucketHeld, err := asAccount.Buckets.TestIamPermissions(bucket,
		[]string{"storage.objects.list", "storage.buckets.update"}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.list"}, bucketHeld.Permissions)
}
