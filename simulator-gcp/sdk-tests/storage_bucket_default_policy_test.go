package gcp_sdk_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	storageapi "google.golang.org/api/storage/v1"
)

// A bucket carries Cloud Storage's default policy from creation: the
// project's owners and editors hold the legacy bucket owner role, its viewers
// the reader role, and a bucket created with uniform bucket-level access also
// grants them the legacy object roles. A client that grants a member and later
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

// A bucket created without uniform bucket-level access grants the project's
// convenience values only the legacy bucket roles; their access to objects
// comes from the default object ACL each new object copies.
func TestGCS_FineGrainedBucketGrantsObjectsThroughTheDefaultObjectACL(t *testing.T) {
	t.Parallel()
	svc := storageService(t)
	bucket := uniquename.New("fine-grained-bucket")
	requireProject(t, "default-policy-project")
	created, err := svc.Buckets.Insert("default-policy-project", &storageapi.Bucket{Name: bucket}).Do()
	require.NoError(t, err)

	policy, err := svc.Buckets.GetIamPolicy(bucket).Do()
	require.NoError(t, err)
	roles := map[string][]string{}
	for _, b := range policy.Bindings {
		roles[b.Role] = b.Members
	}
	assert.ElementsMatch(t, []string{"roles/storage.legacyBucketOwner", "roles/storage.legacyBucketReader"}, mapKeys(roles))

	_, err = svc.Objects.Insert(bucket, &storageapi.Object{Name: "note.txt"}).Media(strings.NewReader("hello")).Do()
	require.NoError(t, err)
	acl, err := svc.ObjectAccessControls.List(bucket, "note.txt").Do()
	require.NoError(t, err)
	grants := map[string]string{}
	for _, entry := range acl.Items {
		grants[entry.Entity] = entry.Role
	}
	assert.Equal(t, "READER", grants["project-viewers-"+strconv.FormatUint(created.ProjectNumber, 10)],
		"the project's viewers read the object through the ACL copied from the default object ACL")
	assert.Equal(t, "OWNER", grants["project-owners-"+strconv.FormatUint(created.ProjectNumber, 10)])
}

func mapKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
