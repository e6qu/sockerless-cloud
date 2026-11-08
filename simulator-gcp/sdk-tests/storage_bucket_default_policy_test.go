package gcp_sdk_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	storageapi "google.golang.org/api/storage/v1"
)

// A bucket carries Cloud Storage's default policy from creation: the
// project's owners and editors hold the legacy bucket and object owner roles,
// its viewers the reader roles. A client that grants a member and later
// revokes it — the Terraform google_storage_bucket_iam_member lifecycle —
// sets the defaults back, never an empty policy.
func TestGCS_BucketCarriesDefaultPolicyFromCreation(t *testing.T) {
	t.Parallel()
	svc := storageService(t)
	bucket := uniquename.New("default-policy-bucket")
	requireProject(t, "default-policy-project")
	_, err := svc.Buckets.Insert("default-policy-project", &storageapi.Bucket{Name: bucket,
		IamConfiguration: &storageapi.BucketIamConfiguration{
			UniformBucketLevelAccess: &storageapi.BucketIamConfigurationUniformBucketLevelAccess{Enabled: true},
		}}).Do()
	require.NoError(t, err)

	initial, err := svc.Buckets.GetIamPolicy(bucket).Do()
	require.NoError(t, err)
	roles := map[string][]string{}
	for _, b := range initial.Bindings {
		roles[b.Role] = b.Members
	}
	assert.ElementsMatch(t, []string{"projectEditor:default-policy-project", "projectOwner:default-policy-project"}, roles["roles/storage.legacyBucketOwner"])
	assert.ElementsMatch(t, []string{"projectViewer:default-policy-project"}, roles["roles/storage.legacyBucketReader"])
	assert.ElementsMatch(t, []string{"projectEditor:default-policy-project", "projectOwner:default-policy-project"}, roles["roles/storage.legacyObjectOwner"])
	assert.ElementsMatch(t, []string{"projectViewer:default-policy-project"}, roles["roles/storage.legacyObjectReader"])

	granted := &storageapi.Policy{Etag: initial.Etag, Bindings: append(initial.Bindings, &storageapi.PolicyBindings{
		Role:    "roles/storage.admin",
		Members: []string{"serviceAccount:builder@default-policy-project.iam.gserviceaccount.com"},
	})}
	withMember, err := svc.Buckets.SetIamPolicy(bucket, granted).Do()
	require.NoError(t, err)
	require.Len(t, withMember.Bindings, 5)

	revoked := &storageapi.Policy{Etag: withMember.Etag}
	for _, b := range withMember.Bindings {
		if b.Role != "roles/storage.admin" {
			revoked.Bindings = append(revoked.Bindings, b)
		}
	}
	afterRevoke, err := svc.Buckets.SetIamPolicy(bucket, revoked).Do()
	require.NoError(t, err, "revoking the only granted member leaves the default bindings, which the service accepts")
	assert.Len(t, afterRevoke.Bindings, 4)

	_, err = svc.Buckets.SetIamPolicy(bucket, &storageapi.Policy{Etag: afterRevoke.Etag}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr, "a policy with no bindings at all is refused")
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	assert.Contains(t, apiErr.Message, "at least one binding")
}

// In a bucket without uniform bucket-level access, an upload that names no
// ACL takes the bucket's default object ACL, projectPrivate, with the uploader
// as the object's OWNER; one that names a predefined ACL takes that instead,
// and objects.patch's predefinedAcl replaces the ACL keeping the owner.
func TestGCS_FineGrainedObjectACLAtCreation(t *testing.T) {
	t.Parallel()
	svc := storageService(t)
	bucket := uniquename.New("fine-grained-bucket")
	mustCreateBucket(t, svc, bucket)

	mustUploadObject(t, svc, bucket, "default.txt", "projectPrivate")
	defaulted, err := svc.Objects.Get(bucket, "default.txt").Projection("full").Do()
	require.NoError(t, err)
	require.NotNil(t, defaulted.Owner, "the full projection names the uploader")
	require.True(t, strings.HasPrefix(defaulted.Owner.Entity, "user-"), defaulted.Owner.Entity)
	grants := map[string]string{}
	for _, entry := range defaulted.Acl {
		grants[entry.Entity] = entry.Role
	}
	assert.Equal(t, "OWNER", grants[defaulted.Owner.Entity], "the uploader owns its object")
	teams := map[string]string{}
	for entity, role := range grants {
		if strings.HasPrefix(entity, "project-") {
			teams[entity[:strings.LastIndex(entity, "-")]] = role
		}
	}
	assert.Equal(t, map[string]string{"project-owners": "OWNER", "project-editors": "OWNER", "project-viewers": "READER"}, teams)

	_, err = svc.Objects.Insert(bucket, &storageapi.Object{Name: "public.txt"}).
		PredefinedAcl("publicRead").Media(strings.NewReader("publicRead")).Do()
	require.NoError(t, err)
	public, err := svc.ObjectAccessControls.List(bucket, "public.txt").Do()
	require.NoError(t, err)
	grants = map[string]string{}
	for _, entry := range public.Items {
		grants[entry.Entity] = entry.Role
	}
	assert.Equal(t, map[string]string{defaulted.Owner.Entity: "OWNER", "allUsers": "READER"}, grants)

	_, err = svc.Objects.Patch(bucket, "public.txt", &storageapi.Object{}).PredefinedAcl("private").Do()
	require.NoError(t, err)
	private, err := svc.ObjectAccessControls.List(bucket, "public.txt").Do()
	require.NoError(t, err)
	require.Len(t, private.Items, 1, "private leaves the owner alone")
	assert.Equal(t, defaulted.Owner.Entity, private.Items[0].Entity)
}
