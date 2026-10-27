package gcp_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	storageapi "google.golang.org/api/storage/v1"
)

// The last five methods of the storage v1 document, through the generated Go
// client:
//
//   GET /storage/v1/b/{bucket}/o?softDeleted=true
//   POST /storage/v1/b/{bucket}/o/{object}/restore
//   POST /storage/v1/b/{bucket}/o/bulkRestore
//   POST /storage/v1/b/{bucket}/o/{sourceObject}/moveTo/o/{destinationObject}
//   GET /storage/v1/b/{bucket}/o/{object}/acl
//   GET /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   POST /storage/v1/b/{bucket}/o/{object}/acl
//   PUT /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   PATCH /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   DELETE /storage/v1/b/{bucket}/o/{object}/acl/{entity}

func mustUploadObject(t *testing.T, svc *storageapi.Service, bucket, name, body string) *storageapi.Object {
	t.Helper()
	obj, err := svc.Objects.Insert(bucket, &storageapi.Object{Name: name}).
		Media(strings.NewReader(body)).Do()
	require.NoError(t, err)
	return obj
}

func TestGCS_SoftDeleteAndRestore(t *testing.T) {
	t.Parallel()
	bucketSoftDeleteBucket := uniquename.New("soft-delete-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketSoftDeleteBucket)

	created := mustUploadObject(t, svc, bucketSoftDeleteBucket, "notes/one.txt", "first")
	require.NotEmpty(t, created.Generation)

	// A bucket created without a policy of its own still has one, so the
	// delete retires the object rather than destroying it.
	bucket, err := svc.Buckets.Get(bucketSoftDeleteBucket).Do()
	require.NoError(t, err)
	require.NotNil(t, bucket.SoftDeletePolicy)
	assert.Equal(t, int64(7*24*60*60), bucket.SoftDeletePolicy.RetentionDurationSeconds)

	require.NoError(t, svc.Objects.Delete(bucketSoftDeleteBucket, "notes/one.txt").Do())

	// Gone from the live listing...
	live, err := svc.Objects.List(bucketSoftDeleteBucket).Do()
	require.NoError(t, err)
	assert.Empty(t, live.Items, "a deleted object must not appear in the live listing")

	// ...and present in the soft-deleted one, with both timestamps.
	retired, err := svc.Objects.List(bucketSoftDeleteBucket).SoftDeleted(true).Do()
	require.NoError(t, err)
	require.Len(t, retired.Items, 1)
	assert.Equal(t, "notes/one.txt", retired.Items[0].Name)
	assert.NotEmpty(t, retired.Items[0].SoftDeleteTime)
	assert.NotEmpty(t, retired.Items[0].HardDeleteTime)

	restored, err := svc.Objects.Restore(bucketSoftDeleteBucket, "notes/one.txt", created.Generation).Do()
	require.NoError(t, err)
	assert.Equal(t, "notes/one.txt", restored.Name)
	// A restore writes the object anew, so the live object has a generation
	// of its own, newer than the one it was restored from.
	assert.Greater(t, restored.Generation, created.Generation)
	assert.Equal(t, int64(1), restored.Metageneration)
	current, err := svc.Objects.Get(bucketSoftDeleteBucket, "notes/one.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, restored.Generation, current.Generation)

	assert.Equal(t, "first", downloadObject(t, svc, bucketSoftDeleteBucket, "notes/one.txt"))

	// And it is no longer listed as restorable.
	retired, err = svc.Objects.List(bucketSoftDeleteBucket).SoftDeleted(true).Do()
	require.NoError(t, err)
	assert.Empty(t, retired.Items)
}

// objects.restore judges ifGenerationMatch against the live object it would
// replace: 0 refuses while one exists, its generation lets the restore replace
// it.
func TestGCS_RestorePreconditionOnTheLiveObject(t *testing.T) {
	t.Parallel()
	bucketRestorePreconditionBucket := uniquename.New("restore-precondition-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketRestorePreconditionBucket)
	first := mustUploadObject(t, svc, bucketRestorePreconditionBucket, "doc.txt", "first")
	require.NoError(t, svc.Objects.Delete(bucketRestorePreconditionBucket, "doc.txt").Do())
	second := mustUploadObject(t, svc, bucketRestorePreconditionBucket, "doc.txt", "second")

	_, err := svc.Objects.Restore(bucketRestorePreconditionBucket, "doc.txt", first.Generation).IfGenerationMatch(0).Do()
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	assert.Equal(t, http.StatusPreconditionFailed, gerr.Code)
	assert.Equal(t, "second", downloadObject(t, svc, bucketRestorePreconditionBucket, "doc.txt"))

	restored, err := svc.Objects.Restore(bucketRestorePreconditionBucket, "doc.txt", first.Generation).
		IfGenerationMatch(second.Generation).Do()
	require.NoError(t, err)
	assert.Greater(t, restored.Generation, second.Generation)
	assert.Equal(t, "first", downloadObject(t, svc, bucketRestorePreconditionBucket, "doc.txt"))
}

func TestGCS_SoftDeleteDisabledDestroysTheObject(t *testing.T) {
	t.Parallel()
	bucketHardDeleteBucket := uniquename.New("hard-delete-bucket")
	svc := storageService(t)
	_, err := svc.Buckets.Insert("test-project", &storageapi.Bucket{
		Name: bucketHardDeleteBucket,
		SoftDeletePolicy: &storageapi.BucketSoftDeletePolicy{
			RetentionDurationSeconds: 0,
			// The member is omitempty, so turning soft delete off must be sent
			// explicitly.
			ForceSendFields: []string{"RetentionDurationSeconds"},
		},
	}).Do()
	require.NoError(t, err)

	mustUploadObject(t, svc, bucketHardDeleteBucket, "gone.txt", "bytes")
	require.NoError(t, svc.Objects.Delete(bucketHardDeleteBucket, "gone.txt").Do())

	// A bucket with soft delete off retains no generation to restore.
	retired, err := svc.Objects.List(bucketHardDeleteBucket).SoftDeleted(true).Do()
	require.NoError(t, err)
	assert.Empty(t, retired.Items)
}

func TestGCS_BulkRestore(t *testing.T) {
	t.Parallel()
	bucketBulkRestoreBucket := uniquename.New("bulk-restore-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketBulkRestoreBucket)

	for _, name := range []string{"logs/a.txt", "logs/b.txt", "data/c.txt"} {
		mustUploadObject(t, svc, bucketBulkRestoreBucket, name, name)
		require.NoError(t, svc.Objects.Delete(bucketBulkRestoreBucket, name).Do())
	}

	op, err := svc.Objects.BulkRestore(bucketBulkRestoreBucket, &storageapi.BulkRestoreObjectsRequest{}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	live, err := svc.Objects.List(bucketBulkRestoreBucket).Do()
	require.NoError(t, err)
	names := objectNames(live)
	assert.ElementsMatch(t, []string{"logs/a.txt", "logs/b.txt", "data/c.txt"}, names,
		"an unfiltered bulkRestore restores every retired object")
}

// `**` must cross "/": path.Match semantics would restore too little.
func TestGCS_BulkRestoreHonoursMatchGlobs(t *testing.T) {
	t.Parallel()
	bucketGlobRestoreBucket := uniquename.New("glob-restore-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketGlobRestoreBucket)

	for _, name := range []string{"logs/2026/a.txt", "logs/b.txt", "data/c.txt"} {
		mustUploadObject(t, svc, bucketGlobRestoreBucket, name, name)
		require.NoError(t, svc.Objects.Delete(bucketGlobRestoreBucket, name).Do())
	}

	_, err := svc.Objects.BulkRestore(bucketGlobRestoreBucket, &storageapi.BulkRestoreObjectsRequest{
		MatchGlobs: []string{"logs/**"},
	}).Do()
	require.NoError(t, err)

	live, err := svc.Objects.List(bucketGlobRestoreBucket).Do()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"logs/2026/a.txt", "logs/b.txt"}, objectNames(live))

	retired, err := svc.Objects.List(bucketGlobRestoreBucket).SoftDeleted(true).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"data/c.txt"}, softDeletedNames(retired))
}

func TestGCS_BulkRestoreSingleStarDoesNotCrossSeparators(t *testing.T) {
	t.Parallel()
	bucketStarRestoreBucket := uniquename.New("star-restore-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketStarRestoreBucket)

	for _, name := range []string{"logs/2026/a.txt", "logs/b.txt"} {
		mustUploadObject(t, svc, bucketStarRestoreBucket, name, name)
		require.NoError(t, svc.Objects.Delete(bucketStarRestoreBucket, name).Do())
	}

	_, err := svc.Objects.BulkRestore(bucketStarRestoreBucket, &storageapi.BulkRestoreObjectsRequest{
		MatchGlobs: []string{"logs/*"},
	}).Do()
	require.NoError(t, err)

	live, err := svc.Objects.List(bucketStarRestoreBucket).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"logs/b.txt"}, objectNames(live))
}

func TestGCS_ObjectMoveRequiresHierarchicalNamespace(t *testing.T) {
	t.Parallel()
	bucketFlatBucket := uniquename.New("flat-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketFlatBucket)
	mustUploadObject(t, svc, bucketFlatBucket, "a.txt", "payload")

	_, err := svc.Objects.Move(bucketFlatBucket, "a.txt", "b.txt").Do()
	require.Error(t, err, "objects.move is a hierarchical-namespace method")
	assert.Contains(t, err.Error(), "hierarchical namespace")
}

func TestGCS_ObjectMove(t *testing.T) {
	t.Parallel()
	bucketHnsBucket := uniquename.New("hns-bucket")
	svc := storageService(t)
	_, err := svc.Buckets.Insert("test-project", &storageapi.Bucket{
		Name:                  bucketHnsBucket,
		HierarchicalNamespace: &storageapi.BucketHierarchicalNamespace{Enabled: true},
	}).Do()
	require.NoError(t, err)

	mustUploadObject(t, svc, bucketHnsBucket, "before.txt", "payload")

	moved, err := svc.Objects.Move(bucketHnsBucket, "before.txt", "after.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "after.txt", moved.Name)

	// The source is gone and the destination carries the source's bytes.
	_, err = svc.Objects.Get(bucketHnsBucket, "before.txt").Do()
	require.Error(t, err)

	assert.Equal(t, "payload", downloadObject(t, svc, bucketHnsBucket, "after.txt"))
}

func TestGCS_ObjectACL_RoundTrip(t *testing.T) {
	t.Parallel()
	bucketObjectAclBucket := uniquename.New("object-acl-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketObjectAclBucket)
	mustUploadObject(t, svc, bucketObjectAclBucket, "doc.txt", "payload")

	const entity = "user-sam@example.com"
	created, err := svc.ObjectAccessControls.Insert(bucketObjectAclBucket, "doc.txt", &storageapi.ObjectAccessControl{
		Entity: entity,
		Role:   "READER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#objectAccessControl", created.Kind)
	assert.Equal(t, "doc.txt", created.Object)
	assert.Equal(t, "sam@example.com", created.Email)

	got, err := svc.ObjectAccessControls.Get(bucketObjectAclBucket, "doc.txt", entity).Do()
	require.NoError(t, err)
	assert.Equal(t, "READER", got.Role)

	updated, err := svc.ObjectAccessControls.Update(bucketObjectAclBucket, "doc.txt", entity, &storageapi.ObjectAccessControl{
		Entity: entity,
		Role:   "OWNER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "OWNER", updated.Role)

	// The object inherited the bucket's projectPrivate default, so the inserted
	// entry joins three.
	list, err := svc.ObjectAccessControls.List(bucketObjectAclBucket, "doc.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#objectAccessControls", list.Kind)
	require.Len(t, list.Items, 4)
	assert.Contains(t, defaultACLEntities(list.Items), entity)

	require.NoError(t, svc.ObjectAccessControls.Delete(bucketObjectAclBucket, "doc.txt", entity).Do())
	list, err = svc.ObjectAccessControls.List(bucketObjectAclBucket, "doc.txt").Do()
	require.NoError(t, err)
	assert.NotContains(t, defaultACLEntities(list.Items), entity)
}

// Through the `{object...}` route, objects.get would answer
// "object \"doc.txt/acl\" not found" and pass for a served method.
func TestGCS_ObjectACLIsNotTheObjectHandler(t *testing.T) {
	t.Parallel()
	bucketAclNotObjectBucket := uniquename.New("acl-not-object-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketAclNotObjectBucket)

	_, err := svc.ObjectAccessControls.List(bucketAclNotObjectBucket, "absent.txt").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `object "absent.txt" not found`)
	assert.NotContains(t, err.Error(), "absent.txt/acl",
		"the ACL route must not fall through to objects.get")
}

func TestGCS_ObjectACLSeededFromTheBucketDefault(t *testing.T) {
	t.Parallel()
	bucketSeededAclBucket := uniquename.New("seeded-acl-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucketSeededAclBucket)

	_, err := svc.DefaultObjectAccessControls.Insert(bucketSeededAclBucket, &storageapi.ObjectAccessControl{
		Entity: "allUsers",
		Role:   "READER",
	}).Do()
	require.NoError(t, err)

	mustUploadObject(t, svc, bucketSeededAclBucket, "seeded.txt", "payload")

	list, err := svc.ObjectAccessControls.List(bucketSeededAclBucket, "seeded.txt").Do()
	require.NoError(t, err)
	require.Len(t, list.Items, 4, "a new object inherits the bucket's default object ACL")
	assert.Contains(t, defaultACLEntities(list.Items), "allUsers")
	for _, item := range list.Items {
		assert.Equal(t, "seeded.txt", item.Object)
	}

	// The default copies at creation, not on read, so an earlier object does not
	// gain it.
	_, err = svc.DefaultObjectAccessControls.Insert(bucketSeededAclBucket, &storageapi.ObjectAccessControl{
		Entity: "allAuthenticatedUsers",
		Role:   "READER",
	}).Do()
	require.NoError(t, err)
	list, err = svc.ObjectAccessControls.List(bucketSeededAclBucket, "seeded.txt").Do()
	require.NoError(t, err)
	assert.Len(t, list.Items, 4, "editing the bucket default must not reach existing objects")
	assert.NotContains(t, defaultACLEntities(list.Items), "allAuthenticatedUsers")
}

func TestGCS_ObjectACLRejectedUnderUniformBucketLevelAccess(t *testing.T) {
	t.Parallel()
	bucketUblaBucket := uniquename.New("ubla-bucket")
	svc := storageService(t)
	_, err := svc.Buckets.Insert("test-project", &storageapi.Bucket{
		Name: bucketUblaBucket,
		IamConfiguration: &storageapi.BucketIamConfiguration{
			UniformBucketLevelAccess: &storageapi.BucketIamConfigurationUniformBucketLevelAccess{
				Enabled: true,
			},
		},
	}).Do()
	require.NoError(t, err)
	mustUploadObject(t, svc, bucketUblaBucket, "doc.txt", "payload")

	_, err = svc.ObjectAccessControls.List(bucketUblaBucket, "doc.txt").Do()
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
