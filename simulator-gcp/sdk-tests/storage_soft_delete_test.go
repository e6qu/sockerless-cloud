package gcp_sdk_test

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storageapi "google.golang.org/api/storage/v1"
)

// The last five methods of the storage v1 document, through the generated Go
// client:
//
//	GET /storage/v1/b/{bucket}/o?softDeleted=true
//	POST /storage/v1/b/{bucket}/o/{object}/restore
//	POST /storage/v1/b/{bucket}/o/bulkRestore
//	POST /storage/v1/b/{bucket}/o/{sourceObject}/moveTo/o/{destinationObject}
//	GET /storage/v1/b/{bucket}/o/{object}/acl
//	GET /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//	POST /storage/v1/b/{bucket}/o/{object}/acl
//	PUT /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//	PATCH /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//	DELETE /storage/v1/b/{bucket}/o/{object}/acl/{entity}

// Upload through the media path so the test carries a real payload.
func mustUploadObject(t *testing.T, svc *storageapi.Service, bucket, name, body string) *storageapi.Object {
	t.Helper()
	obj, err := svc.Objects.Insert(bucket, &storageapi.Object{Name: name}).
		Media(strings.NewReader(body)).Do()
	require.NoError(t, err)
	return obj
}

func TestGCS_SoftDeleteAndRestore(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "soft-delete-bucket")

	created := mustUploadObject(t, svc, "soft-delete-bucket", "notes/one.txt", "first")
	require.NotEmpty(t, created.Generation)

	// A bucket created without a policy of its own still has one, so the
	// delete retires the object rather than destroying it.
	bucket, err := svc.Buckets.Get("soft-delete-bucket").Do()
	require.NoError(t, err)
	require.NotNil(t, bucket.SoftDeletePolicy)
	assert.Equal(t, int64(7*24*60*60), bucket.SoftDeletePolicy.RetentionDurationSeconds)

	require.NoError(t, svc.Objects.Delete("soft-delete-bucket", "notes/one.txt").Do())

	// Gone from the live listing...
	live, err := svc.Objects.List("soft-delete-bucket").Do()
	require.NoError(t, err)
	assert.Empty(t, live.Items, "a deleted object must not appear in the live listing")

	// ...and present in the soft-deleted one, with both timestamps.
	retired, err := svc.Objects.List("soft-delete-bucket").SoftDeleted(true).Do()
	require.NoError(t, err)
	require.Len(t, retired.Items, 1)
	assert.Equal(t, "notes/one.txt", retired.Items[0].Name)
	assert.NotEmpty(t, retired.Items[0].SoftDeleteTime)
	assert.NotEmpty(t, retired.Items[0].HardDeleteTime)

	restored, err := svc.Objects.Restore("soft-delete-bucket", "notes/one.txt", created.Generation).Do()
	require.NoError(t, err)
	assert.Equal(t, "notes/one.txt", restored.Name)

	// The payload survived the round trip, which is the point of retaining
	// the object rather than recording that it once existed.
	assert.Equal(t, "first", downloadObject(t, svc, "soft-delete-bucket", "notes/one.txt"))

	// And it is no longer listed as restorable.
	retired, err = svc.Objects.List("soft-delete-bucket").SoftDeleted(true).Do()
	require.NoError(t, err)
	assert.Empty(t, retired.Items)
}

func TestGCS_SoftDeleteDisabledDestroysTheObject(t *testing.T) {
	svc := storageService(t)
	_, err := svc.Buckets.Insert("hard-delete-bucket", &storageapi.Bucket{
		Name: "hard-delete-bucket",
		SoftDeletePolicy: &storageapi.BucketSoftDeletePolicy{
			RetentionDurationSeconds: 0,
			// The member is omitempty, so turning soft delete off has to be
			// sent explicitly or the bucket arrives carrying an empty policy.
			ForceSendFields: []string{"RetentionDurationSeconds"},
		},
	}).Do()
	require.NoError(t, err)

	mustUploadObject(t, svc, "hard-delete-bucket", "gone.txt", "bytes")
	require.NoError(t, svc.Objects.Delete("hard-delete-bucket", "gone.txt").Do())

	// A bucket that turned soft delete off retains nothing: there is no
	// retired generation to restore, which is also what frees the payload.
	retired, err := svc.Objects.List("hard-delete-bucket").SoftDeleted(true).Do()
	require.NoError(t, err)
	assert.Empty(t, retired.Items)
}

func TestGCS_BulkRestore(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "bulk-restore-bucket")

	for _, name := range []string{"logs/a.txt", "logs/b.txt", "data/c.txt"} {
		mustUploadObject(t, svc, "bulk-restore-bucket", name, name)
		require.NoError(t, svc.Objects.Delete("bulk-restore-bucket", name).Do())
	}

	op, err := svc.Objects.BulkRestore("bulk-restore-bucket", &storageapi.BulkRestoreObjectsRequest{}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	live, err := svc.Objects.List("bulk-restore-bucket").Do()
	require.NoError(t, err)
	names := objectNames(live)
	assert.ElementsMatch(t, []string{"logs/a.txt", "logs/b.txt", "data/c.txt"}, names,
		"an unfiltered bulkRestore restores every retired object")
}

// `**` must cross "/": path.Match semantics would silently restore too little.
func TestGCS_BulkRestoreHonoursMatchGlobs(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "glob-restore-bucket")

	for _, name := range []string{"logs/2026/a.txt", "logs/b.txt", "data/c.txt"} {
		mustUploadObject(t, svc, "glob-restore-bucket", name, name)
		require.NoError(t, svc.Objects.Delete("glob-restore-bucket", name).Do())
	}

	_, err := svc.Objects.BulkRestore("glob-restore-bucket", &storageapi.BulkRestoreObjectsRequest{
		MatchGlobs: []string{"logs/**"},
	}).Do()
	require.NoError(t, err)

	live, err := svc.Objects.List("glob-restore-bucket").Do()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"logs/2026/a.txt", "logs/b.txt"}, objectNames(live))

	retired, err := svc.Objects.List("glob-restore-bucket").SoftDeleted(true).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"data/c.txt"}, softDeletedNames(retired))
}

func TestGCS_BulkRestoreSingleStarDoesNotCrossSeparators(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "star-restore-bucket")

	for _, name := range []string{"logs/2026/a.txt", "logs/b.txt"} {
		mustUploadObject(t, svc, "star-restore-bucket", name, name)
		require.NoError(t, svc.Objects.Delete("star-restore-bucket", name).Do())
	}

	_, err := svc.Objects.BulkRestore("star-restore-bucket", &storageapi.BulkRestoreObjectsRequest{
		MatchGlobs: []string{"logs/*"},
	}).Do()
	require.NoError(t, err)

	live, err := svc.Objects.List("star-restore-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"logs/b.txt"}, objectNames(live))
}

func TestGCS_ObjectMoveRequiresHierarchicalNamespace(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "flat-bucket")
	mustUploadObject(t, svc, "flat-bucket", "a.txt", "payload")

	_, err := svc.Objects.Move("flat-bucket", "a.txt", "b.txt").Do()
	require.Error(t, err, "objects.move is a hierarchical-namespace method")
	assert.Contains(t, err.Error(), "hierarchical namespace")
}

func TestGCS_ObjectMove(t *testing.T) {
	svc := storageService(t)
	_, err := svc.Buckets.Insert("hns-bucket", &storageapi.Bucket{
		Name:                  "hns-bucket",
		HierarchicalNamespace: &storageapi.BucketHierarchicalNamespace{Enabled: true},
	}).Do()
	require.NoError(t, err)

	mustUploadObject(t, svc, "hns-bucket", "before.txt", "payload")

	moved, err := svc.Objects.Move("hns-bucket", "before.txt", "after.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "after.txt", moved.Name)

	// The source is gone and the destination carries the source's bytes.
	_, err = svc.Objects.Get("hns-bucket", "before.txt").Do()
	require.Error(t, err)

	assert.Equal(t, "payload", downloadObject(t, svc, "hns-bucket", "after.txt"))
}

func TestGCS_ObjectACL_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "object-acl-bucket")
	mustUploadObject(t, svc, "object-acl-bucket", "doc.txt", "payload")

	const entity = "user-sam@example.com"
	created, err := svc.ObjectAccessControls.Insert("object-acl-bucket", "doc.txt", &storageapi.ObjectAccessControl{
		Entity: entity,
		Role:   "READER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#objectAccessControl", created.Kind)
	assert.Equal(t, "doc.txt", created.Object)
	assert.Equal(t, "sam@example.com", created.Email)

	got, err := svc.ObjectAccessControls.Get("object-acl-bucket", "doc.txt", entity).Do()
	require.NoError(t, err)
	assert.Equal(t, "READER", got.Role)

	updated, err := svc.ObjectAccessControls.Update("object-acl-bucket", "doc.txt", entity, &storageapi.ObjectAccessControl{
		Entity: entity,
		Role:   "OWNER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "OWNER", updated.Role)

	// The object inherited the bucket's projectPrivate default, so the
	// inserted entry joins three rather than standing alone.
	list, err := svc.ObjectAccessControls.List("object-acl-bucket", "doc.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#objectAccessControls", list.Kind)
	require.Len(t, list.Items, 4)
	assert.Contains(t, defaultACLEntities(list.Items), entity)

	require.NoError(t, svc.ObjectAccessControls.Delete("object-acl-bucket", "doc.txt", entity).Do())
	list, err = svc.ObjectAccessControls.List("object-acl-bucket", "doc.txt").Do()
	require.NoError(t, err)
	assert.NotContains(t, defaultACLEntities(list.Items), entity)
}

// Reaching objects.get through the `{object...}` catch-all answers "object
// \"doc.txt/acl\" not found", which is what let five unserved methods count
// as covered.
func TestGCS_ObjectACLIsNotTheObjectHandler(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "acl-not-object-bucket")

	_, err := svc.ObjectAccessControls.List("acl-not-object-bucket", "absent.txt").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `object "absent.txt" not found`)
	assert.NotContains(t, err.Error(), "absent.txt/acl",
		"the ACL route must not fall through to objects.get")
}

func TestGCS_ObjectACLSeededFromTheBucketDefault(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "seeded-acl-bucket")

	_, err := svc.DefaultObjectAccessControls.Insert("seeded-acl-bucket", &storageapi.ObjectAccessControl{
		Entity: "allUsers",
		Role:   "READER",
	}).Do()
	require.NoError(t, err)

	mustUploadObject(t, svc, "seeded-acl-bucket", "seeded.txt", "payload")

	list, err := svc.ObjectAccessControls.List("seeded-acl-bucket", "seeded.txt").Do()
	require.NoError(t, err)
	require.Len(t, list.Items, 4, "a new object inherits the bucket's default object ACL")
	assert.Contains(t, defaultACLEntities(list.Items), "allUsers")
	for _, item := range list.Items {
		assert.Equal(t, "seeded.txt", item.Object)
	}

	// An object written before a default entry exists does not gain it: the
	// copy happens at creation, not on read.
	_, err = svc.DefaultObjectAccessControls.Insert("seeded-acl-bucket", &storageapi.ObjectAccessControl{
		Entity: "allAuthenticatedUsers",
		Role:   "READER",
	}).Do()
	require.NoError(t, err)
	list, err = svc.ObjectAccessControls.List("seeded-acl-bucket", "seeded.txt").Do()
	require.NoError(t, err)
	assert.Len(t, list.Items, 4, "editing the bucket default must not reach existing objects")
	assert.NotContains(t, defaultACLEntities(list.Items), "allAuthenticatedUsers")
}

func TestGCS_ObjectACLRejectedUnderUniformBucketLevelAccess(t *testing.T) {
	svc := storageService(t)
	_, err := svc.Buckets.Insert("ubla-bucket", &storageapi.Bucket{
		Name: "ubla-bucket",
		IamConfiguration: &storageapi.BucketIamConfiguration{
			UniformBucketLevelAccess: &storageapi.BucketIamConfigurationUniformBucketLevelAccess{
				Enabled: true,
			},
		},
	}).Do()
	require.NoError(t, err)
	mustUploadObject(t, svc, "ubla-bucket", "doc.txt", "payload")

	_, err = svc.ObjectAccessControls.List("ubla-bucket", "doc.txt").Do()
	require.Error(t, err, "uniform bucket-level access disables the legacy ACL surface")
	assert.Contains(t, err.Error(), "uniform bucket-level access")
}

func downloadObject(t *testing.T, svc *storageapi.Service, bucket, object string) string {
	t.Helper()
	reader, err := svc.Objects.Get(bucket, object).Download()
	require.NoError(t, err)
	defer func() { _ = reader.Body.Close() }()
	body, err := io.ReadAll(reader.Body)
	require.NoError(t, err)
	return string(body)
}

func objectNames(list *storageapi.Objects) []string {
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	return names
}

func softDeletedNames(list *storageapi.Objects) []string {
	return objectNames(list)
}
