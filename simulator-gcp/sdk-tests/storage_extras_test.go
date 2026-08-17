package gcp_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	storageapi "google.golang.org/api/storage/v1"
)

// These round-trips exercise the storage v1 control-plane routes the
// simulator mounts beyond bucket/object CRUD. Each route is named here so
// the SDK calls below are traceable to the wire path they drive:
//
//	GET /storage/v1/b/{bucket}/acl
//	GET /storage/v1/b/{bucket}/acl/{entity}
//	POST /storage/v1/b/{bucket}/acl
//	PUT /storage/v1/b/{bucket}/acl/{entity}
//	PATCH /storage/v1/b/{bucket}/acl/{entity}
//	DELETE /storage/v1/b/{bucket}/acl/{entity}
//	GET /storage/v1/b/{bucket}/defaultObjectAcl
//	GET /storage/v1/b/{bucket}/defaultObjectAcl/{entity}
//	POST /storage/v1/b/{bucket}/defaultObjectAcl
//	PUT /storage/v1/b/{bucket}/defaultObjectAcl/{entity}
//	PATCH /storage/v1/b/{bucket}/defaultObjectAcl/{entity}
//	DELETE /storage/v1/b/{bucket}/defaultObjectAcl/{entity}
//	GET /storage/v1/b/{bucket}/notificationConfigs
//	GET /storage/v1/b/{bucket}/notificationConfigs/{notification}
//	POST /storage/v1/b/{bucket}/notificationConfigs
//	DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}
//	GET /storage/v1/projects/{projectId}/hmacKeys
//	GET /storage/v1/projects/{projectId}/hmacKeys/{accessId}
//	POST /storage/v1/projects/{projectId}/hmacKeys
//	PUT /storage/v1/projects/{projectId}/hmacKeys/{accessId}
//	DELETE /storage/v1/projects/{projectId}/hmacKeys/{accessId}
//	GET /storage/v1/projects/{projectId}/serviceAccount
//	GET /storage/v1/b/{bucket}/folders
//	GET /storage/v1/b/{bucket}/folders/{folder}
//	POST /storage/v1/b/{bucket}/folders
//	DELETE /storage/v1/b/{bucket}/folders/{folder}
//	POST /storage/v1/b/{bucket}/folders/{folder}/deleteRecursive
//	POST /storage/v1/b/{bucket}/folders/{sourceFolder}/renameTo/folders/{destinationFolder}
//	GET /storage/v1/b/{bucket}/managedFolders
//	GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}
//	POST /storage/v1/b/{bucket}/managedFolders
//	DELETE /storage/v1/b/{bucket}/managedFolders/{managedFolder}
//	GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam
//	PUT /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam
//	GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam/testPermissions
//	GET /storage/v1/b/{bucket}/iam/testPermissions
//	POST /storage/v1/b/{bucket}/lockRetentionPolicy
//	POST /storage/v1/b/{bucket}/restore
//	POST /storage/v1/b/{bucket}/relocate
//	GET /storage/v1/b/{bucket}/operations
//	GET /storage/v1/b/{bucket}/operations/{operationId}
//	POST /storage/v1/b/{bucket}/operations/{operationId}/cancel
//	POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket
//	GET /storage/v1/b/{bucket}/anywhereCaches
//	GET /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}
//	POST /storage/v1/b/{bucket}/anywhereCaches
//	PATCH /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}
//	POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/pause
//	POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/resume
//	POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/disable
//	POST /storage/v1/b/{bucket}/o
//	POST /storage/v1/channels/stop
//
// storageService returns the low-level google.golang.org/api/storage/v1
// service pointed at the simulator's JSON API endpoint. The ACL, default
// object ACL, notification, HMAC-key, folder, managed-folder and IAM
// surfaces are reached through this raw service (the high-level
// cloud.google.com/go/storage client does not expose all of them).
func storageService(t *testing.T) *storageapi.Service {
	t.Helper()
	svc, err := storageapi.NewService(ctx,
		option.WithEndpoint(baseURL+"/storage/v1/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func mustCreateBucket(t *testing.T, svc *storageapi.Service, name string) {
	t.Helper()
	_, err := svc.Buckets.Insert("test-project", &storageapi.Bucket{Name: name}).Do()
	require.NoError(t, err)
}

func TestGCS_BucketACL_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "acl-bucket")

	const entity = "user-liz@example.com"
	created, err := svc.BucketAccessControls.Insert("acl-bucket", &storageapi.BucketAccessControl{
		Entity: entity,
		Role:   "READER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#bucketAccessControl", created.Kind)
	assert.Equal(t, entity, created.Entity)
	assert.Equal(t, "READER", created.Role)
	assert.Equal(t, "liz@example.com", created.Email)

	got, err := svc.BucketAccessControls.Get("acl-bucket", entity).Do()
	require.NoError(t, err)
	assert.Equal(t, "READER", got.Role)

	updated, err := svc.BucketAccessControls.Update("acl-bucket", entity, &storageapi.BucketAccessControl{
		Entity: entity,
		Role:   "OWNER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "OWNER", updated.Role)

	list, err := svc.BucketAccessControls.List("acl-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#bucketAccessControls", list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "OWNER", list.Items[0].Role)

	require.NoError(t, svc.BucketAccessControls.Delete("acl-bucket", entity).Do())

	list, err = svc.BucketAccessControls.List("acl-bucket").Do()
	require.NoError(t, err)
	assert.Empty(t, list.Items)
}

func TestGCS_DefaultObjectACL_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "defacl-bucket")

	const entity = "group-team@example.com"
	created, err := svc.DefaultObjectAccessControls.Insert("defacl-bucket", &storageapi.ObjectAccessControl{
		Entity: entity,
		Role:   "READER",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#objectAccessControl", created.Kind)
	assert.Equal(t, "team@example.com", created.Email)

	got, err := svc.DefaultObjectAccessControls.Get("defacl-bucket", entity).Do()
	require.NoError(t, err)
	assert.Equal(t, "READER", got.Role)

	list, err := svc.DefaultObjectAccessControls.List("defacl-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#objectAccessControls", list.Kind)
	require.Len(t, list.Items, 1)

	require.NoError(t, svc.DefaultObjectAccessControls.Delete("defacl-bucket", entity).Do())
}

func TestGCS_Notifications_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "notif-bucket")

	created, err := svc.Notifications.Insert("notif-bucket", &storageapi.Notification{
		Topic:         "//pubsub.googleapis.com/projects/test-project/topics/my-topic",
		PayloadFormat: "JSON_API_V1",
		EventTypes:    []string{"OBJECT_FINALIZE"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#notification", created.Kind)
	assert.NotEmpty(t, created.Id)
	assert.Equal(t, "JSON_API_V1", created.PayloadFormat)

	got, err := svc.Notifications.Get("notif-bucket", created.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Topic, got.Topic)
	assert.Equal(t, []string{"OBJECT_FINALIZE"}, got.EventTypes)

	list, err := svc.Notifications.List("notif-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#notifications", list.Kind)
	require.Len(t, list.Items, 1)

	require.NoError(t, svc.Notifications.Delete("notif-bucket", created.Id).Do())
}

func TestGCS_HmacKeys_RoundTrip(t *testing.T) {
	svc := storageService(t)

	const sa = "svc@test-project.iam.gserviceaccount.com"
	created, err := svc.Projects.HmacKeys.Create("test-project", sa).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#hmacKey", created.Kind)
	require.NotNil(t, created.Metadata)
	assert.Equal(t, "ACTIVE", created.Metadata.State)
	assert.NotEmpty(t, created.Secret)
	accessID := created.Metadata.AccessId
	require.NotEmpty(t, accessID)

	got, err := svc.Projects.HmacKeys.Get("test-project", accessID).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#hmacKeyMetadata", got.Kind)
	assert.Equal(t, sa, got.ServiceAccountEmail)

	got.State = "INACTIVE"
	updated, err := svc.Projects.HmacKeys.Update("test-project", accessID, got).Do()
	require.NoError(t, err)
	assert.Equal(t, "INACTIVE", updated.State)

	list, err := svc.Projects.HmacKeys.List("test-project").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#hmacKeysMetadata", list.Kind)
	require.Len(t, list.Items, 1)

	require.NoError(t, svc.Projects.HmacKeys.Delete("test-project", accessID).Do())

	// After delete the key is DELETED and excluded from the default list.
	list, err = svc.Projects.HmacKeys.List("test-project").Do()
	require.NoError(t, err)
	assert.Empty(t, list.Items)
}

func TestGCS_ServiceAccount(t *testing.T) {
	svc := storageService(t)
	sa, err := svc.Projects.ServiceAccount.Get("test-project").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#serviceAccount", sa.Kind)
	assert.NotEmpty(t, sa.EmailAddress)
}

func TestGCS_Folders_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "folders-bucket")

	created, err := svc.Folders.Insert("folders-bucket", &storageapi.Folder{Name: "a/"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#folder", created.Kind)
	assert.Equal(t, "a/", created.Name)

	got, err := svc.Folders.Get("folders-bucket", "a/").Do()
	require.NoError(t, err)
	assert.Equal(t, "a/", got.Name)

	list, err := svc.Folders.List("folders-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#folders", list.Kind)
	require.Len(t, list.Items, 1)

	// Rename returns a long-running operation. Read the outcome the way a
	// client does — by polling the operation the rename named until the
	// collection reports it complete.
	op, err := svc.Folders.Rename("folders-bucket", "a/", "b/").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)
	renameID := op.Name[strings.LastIndex(op.Name, "/")+1:]
	finished := awaitLRO(t, op.Name,
		func() (*storageapi.GoogleLongrunningOperation, error) {
			return svc.Operations.Get("folders-bucket", renameID).Do()
		},
		func(o *storageapi.GoogleLongrunningOperation) bool { return o.Done })
	assert.Equal(t, op.Name, finished.Name)
	assert.Nil(t, finished.Error, "the rename completed without an error")

	_, err = svc.Folders.Get("folders-bucket", "b/").Do()
	require.NoError(t, err)
	_, err = svc.Folders.Get("folders-bucket", "a/").Do()
	require.Error(t, err, "the rename moved the folder rather than copying it")

	require.NoError(t, svc.Folders.Delete("folders-bucket", "b/").Do())
}

func TestGCS_ManagedFolders_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "mf-bucket")

	created, err := svc.ManagedFolders.Insert("mf-bucket", &storageapi.ManagedFolder{Name: "mf/"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#managedFolder", created.Kind)

	got, err := svc.ManagedFolders.Get("mf-bucket", "mf/").Do()
	require.NoError(t, err)
	assert.Equal(t, "mf/", got.Name)

	list, err := svc.ManagedFolders.List("mf-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#managedFolders", list.Kind)
	require.Len(t, list.Items, 1)

	// Managed-folder IAM round-trip.
	setPolicy := &storageapi.Policy{
		Bindings: []*storageapi.PolicyBindings{{
			Role:    "roles/storage.objectViewer",
			Members: []string{"user:liz@example.com"},
		}},
	}
	gotPolicy, err := svc.ManagedFolders.SetIamPolicy("mf-bucket", "mf/", setPolicy).Do()
	require.NoError(t, err)
	require.Len(t, gotPolicy.Bindings, 1)

	readPolicy, err := svc.ManagedFolders.GetIamPolicy("mf-bucket", "mf/").Do()
	require.NoError(t, err)
	require.Len(t, readPolicy.Bindings, 1)
	assert.Equal(t, "roles/storage.objectViewer", readPolicy.Bindings[0].Role)

	perms, err := svc.ManagedFolders.TestIamPermissions("mf-bucket", "mf/",
		[]string{"storage.managedFolders.get"}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.managedFolders.get"}, perms.Permissions)

	require.NoError(t, svc.ManagedFolders.Delete("mf-bucket", "mf/").Do())
}

func TestGCS_BucketIAM_TestPermissions(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "iam-perms-bucket")

	setPolicy := &storageapi.Policy{
		Bindings: []*storageapi.PolicyBindings{{
			Role:    "roles/storage.admin",
			Members: []string{"user:liz@example.com"},
		}},
	}
	_, err := svc.Buckets.SetIamPolicy("iam-perms-bucket", setPolicy).Do()
	require.NoError(t, err)

	read, err := svc.Buckets.GetIamPolicy("iam-perms-bucket").Do()
	require.NoError(t, err)
	require.Len(t, read.Bindings, 1)

	perms, err := svc.Buckets.TestIamPermissions("iam-perms-bucket",
		[]string{"storage.buckets.get", "storage.buckets.delete"}).Do()
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"storage.buckets.get", "storage.buckets.delete"},
		perms.Permissions)
}

// buckets.lockRetentionPolicy and buckets.restore each answer with the bucket
// the request named: the resource that comes back is the stored bucket, carrying
// the retention policy the caller put on it, and a bucket the service does not
// hold is NOT_FOUND from both rather than an invented resource.
func TestGCS_BucketLockRetentionPolicy(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "lock-bucket")

	patched, err := svc.Buckets.Patch("lock-bucket", &storageapi.Bucket{
		RetentionPolicy: &storageapi.BucketRetentionPolicy{RetentionPeriod: 60},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, patched.RetentionPolicy, "the bucket keeps the retention policy it was given")
	assert.EqualValues(t, 60, patched.RetentionPolicy.RetentionPeriod)

	b, err := svc.Buckets.LockRetentionPolicy("lock-bucket", 1).Do()
	require.NoError(t, err)
	assert.Equal(t, "lock-bucket", b.Name)
	require.NotNil(t, b.RetentionPolicy,
		"the lock answers with the bucket's own retention policy, not a bare name")
	assert.EqualValues(t, 60, b.RetentionPolicy.RetentionPeriod)

	restored, err := svc.Buckets.Restore("lock-bucket", 1).Do()
	require.NoError(t, err)
	assert.Equal(t, "lock-bucket", restored.Name)
	require.NotNil(t, restored.RetentionPolicy)
	assert.EqualValues(t, 60, restored.RetentionPolicy.RetentionPeriod)

	// Neither verb invents a bucket the service does not hold.
	_, err = svc.Buckets.LockRetentionPolicy("no-such-lock-bucket", 1).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	_, err = svc.Buckets.Restore("no-such-lock-bucket", 1).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestGCS_AnywhereCaches_RoundTrip(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "cache-bucket")

	op, err := svc.AnywhereCaches.Insert("cache-bucket", &storageapi.AnywhereCache{
		Zone: "us-central1-a",
		Ttl:  "7200s",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#operation", op.Kind)
	require.NotEmpty(t, op.Name)
	// Read the insert's outcome off the operations collection the way a client
	// does, rather than trusting the field on the create response.
	insertID := op.Name[strings.LastIndex(op.Name, "/")+1:]
	settled := awaitLRO(t, op.Name,
		func() (*storageapi.GoogleLongrunningOperation, error) {
			return svc.Operations.Get("cache-bucket", insertID).Do()
		},
		func(o *storageapi.GoogleLongrunningOperation) bool { return o.Done })
	assert.Equal(t, op.Name, settled.Name)
	assert.Nil(t, settled.Error, "the cache insert completed without an error")

	list, err := svc.AnywhereCaches.List("cache-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#anywhereCaches", list.Kind)
	require.Len(t, list.Items, 1)
	id := list.Items[0].AnywhereCacheId
	require.NotEmpty(t, id)

	got, err := svc.AnywhereCaches.Get("cache-bucket", id).Do()
	require.NoError(t, err)
	assert.Equal(t, "us-central1-a", got.Zone)

	paused, err := svc.AnywhereCaches.Pause("cache-bucket", id).Do()
	require.NoError(t, err)
	assert.Equal(t, "paused", paused.State)

	resumed, err := svc.AnywhereCaches.Resume("cache-bucket", id).Do()
	require.NoError(t, err)
	assert.Equal(t, "running", resumed.State)

	disabled, err := svc.AnywhereCaches.Disable("cache-bucket", id).Do()
	require.NoError(t, err)
	assert.Equal(t, "disabled", disabled.State)
}

// The bucket's operations collection reports the long-running work the bucket's
// own methods started, and nothing else: an operation identifier no method
// minted is NOT_FOUND, from get, from cancel and from advanceRelocateBucket
// alike.
func TestGCS_BucketOperations_ReportRecordedWork(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "ops-bucket")

	// A bucket that has started no long-running work has no operations.
	empty, err := svc.Operations.List("ops-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#operations", empty.Kind)
	assert.Empty(t, empty.Operations)

	// An identifier the service never minted is not an operation.
	_, err = svc.Operations.Get("ops-bucket", "op-123").Do()
	require.Error(t, err, "a get must not invent an operation")
	assert.Contains(t, err.Error(), "404")
	err = svc.Operations.Cancel("ops-bucket", "op-123").Do()
	require.Error(t, err, "a cancel must not accept an identifier the service has no record of")
	assert.Contains(t, err.Error(), "404")
	err = svc.Operations.AdvanceRelocateBucket("ops-bucket", "op-123",
		&storageapi.AdvanceRelocateBucketOperationRequest{Ttl: "3600s"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

	// A relocation records its operation, and the collection answers about it.
	started, err := svc.Buckets.Relocate("ops-bucket", &storageapi.RelocateBucketRequest{
		DestinationLocation: "us-east1",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, started.Name)
	id := started.Name[strings.LastIndex(started.Name, "/")+1:]
	assert.Equal(t, "projects/_/buckets/ops-bucket/operations/"+id, started.Name)
	assert.Equal(t, "storage#operation", started.Kind)
	assert.True(t, started.Done)
	assert.Contains(t, started.SelfLink, "/storage/v1/b/ops-bucket/operations/"+id)

	got, err := svc.Operations.Get("ops-bucket", id).Do()
	require.NoError(t, err)
	assert.Equal(t, started.Name, got.Name)
	assert.Equal(t, "storage#operation", got.Kind)
	assert.True(t, got.Done)

	listed, err := svc.Operations.List("ops-bucket").Do()
	require.NoError(t, err)
	require.Len(t, listed.Operations, 1)
	assert.Equal(t, started.Name, listed.Operations[0].Name)

	// The AIP-160 term the collection filters on — the one
	// `gcloud storage operations list --server-filter` documents.
	done, err := svc.Operations.List("ops-bucket").Filter("done = true").Do()
	require.NoError(t, err)
	require.Len(t, done.Operations, 1)
	running, err := svc.Operations.List("ops-bucket").Filter("done = false").Do()
	require.NoError(t, err)
	assert.Empty(t, running.Operations)

	// Cancel and advance are best effort against a recorded operation; both
	// resolve the name rather than accepting anything. A client learns what
	// became of the operation by reading it back, and what it reads is the
	// record the relocation wrote: the work was already complete when its name
	// reached the client, so neither verb erases the record nor moves it out of
	// the completed set.
	require.NoError(t, svc.Operations.Cancel("ops-bucket", id).Do())
	afterCancel, err := svc.Operations.Get("ops-bucket", id).Do()
	require.NoError(t, err, "the cancelled operation is still a record the collection answers about")
	assert.Equal(t, started.Name, afterCancel.Name)
	assert.True(t, afterCancel.Done, "a cancel of completed work leaves it completed")

	require.NoError(t, svc.Operations.AdvanceRelocateBucket("ops-bucket", id,
		&storageapi.AdvanceRelocateBucketOperationRequest{Ttl: "3600s"}).Do())
	afterAdvance, err := svc.Operations.Get("ops-bucket", id).Do()
	require.NoError(t, err, "advancing does not discard the operation it advanced")
	assert.Equal(t, started.Name, afterAdvance.Name)
	assert.True(t, afterAdvance.Done)

	stillListed, err := svc.Operations.List("ops-bucket").Do()
	require.NoError(t, err)
	require.Len(t, stillListed.Operations, 1, "the collection still holds the one operation")
	assert.Equal(t, started.Name, stillListed.Operations[0].Name)

	// A bucket the service does not hold has no operations collection.
	_, err = svc.Operations.List("no-such-ops-bucket").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// Operations are parented by their bucket: one bucket's relocation is not in
// another bucket's collection.
func TestGCS_BucketOperations_AreParentedByTheirBucket(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "ops-parent-a")
	mustCreateBucket(t, svc, "ops-parent-b")

	op, err := svc.Buckets.Relocate("ops-parent-a", &storageapi.RelocateBucketRequest{
		DestinationLocation: "us-west1",
	}).Do()
	require.NoError(t, err)
	id := op.Name[strings.LastIndex(op.Name, "/")+1:]

	other, err := svc.Operations.List("ops-parent-b").Do()
	require.NoError(t, err)
	assert.Empty(t, other.Operations)

	_, err = svc.Operations.Get("ops-parent-b", id).Do()
	require.Error(t, err, "an operation belongs to the bucket that started it")
	assert.Contains(t, err.Error(), "404")
}

// The folder and Anywhere Cache long-running methods record their operations
// in the same collection the relocation does.
func TestGCS_BucketOperations_RecordEveryLongRunningMethod(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "ops-lro-bucket")

	_, err := svc.Folders.Insert("ops-lro-bucket", &storageapi.Folder{Name: "src/"}).Do()
	require.NoError(t, err)
	renamed, err := svc.Folders.Rename("ops-lro-bucket", "src/", "dst/").Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#operation", renamed.Kind)

	cache, err := svc.AnywhereCaches.Insert("ops-lro-bucket", &storageapi.AnywhereCache{
		Zone: "us-central1-a",
	}).Do()
	require.NoError(t, err)

	listed, err := svc.Operations.List("ops-lro-bucket").Do()
	require.NoError(t, err)
	names := make([]string, 0, len(listed.Operations))
	for _, op := range listed.Operations {
		names = append(names, op.Name)
	}
	assert.Contains(t, names, renamed.Name)
	assert.Contains(t, names, cache.Name)
}

// buckets.relocate moves the bucket; validateOnly checks the request and
// leaves it where it is.
func TestGCS_BucketRelocate(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "relocate-bucket")

	op, err := svc.Buckets.Relocate("relocate-bucket", &storageapi.RelocateBucketRequest{
		DestinationLocation: "US-EAST1",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "storage#operation", op.Kind)
	assert.True(t, op.Done)

	moved, err := svc.Buckets.Get("relocate-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "US-EAST1", moved.Location, "the relocation moved the bucket it reported moving")

	validated, err := svc.Buckets.Relocate("relocate-bucket", &storageapi.RelocateBucketRequest{
		DestinationLocation: "EUROPE-WEST1",
		ValidateOnly:        true,
	}).Do()
	require.NoError(t, err)
	assert.True(t, validated.Done)

	stayed, err := svc.Buckets.Get("relocate-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "US-EAST1", stayed.Location, "validateOnly must not move the bucket")

	// "If no location is provided, Cloud Storage will use the default
	// location, which is us."
	_, err = svc.Buckets.Relocate("relocate-bucket", &storageapi.RelocateBucketRequest{}).Do()
	require.NoError(t, err)
	defaulted, err := svc.Buckets.Get("relocate-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, "US", defaulted.Location)
}

// channels.stop stops a watch channel. The method's Discovery declaration gives
// it no response schema, so the reply a client has to be able to decode is an
// empty 200 — asserted both through the SDK round-trip and on the wire, because
// a body here would break the generated client's decode.
func TestGCS_ChannelsStop(t *testing.T) {
	svc := storageService(t)
	require.NoError(t, svc.Channels.Stop(&storageapi.Channel{
		Id:         "channel-1",
		ResourceId: "resource-1",
	}).Do())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/storage/v1/channels/stop",
		strings.NewReader(`{"id":"channel-1","resourceId":"resource-1"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := simAuthHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Empty(t, body, "channels.stop declares no response body")
}

func TestGCS_ObjectInsertMetadataOnly(t *testing.T) {
	svc := storageService(t)
	mustCreateBucket(t, svc, "obj-insert-bucket")

	// objects.insert with no media body creates a zero-length object from
	// the supplied resource fields (POST /storage/v1/b/{bucket}/o).
	obj, err := svc.Objects.Insert("obj-insert-bucket", &storageapi.Object{
		Name:        "empty.txt",
		ContentType: "text/plain",
		Metadata:    map[string]string{"origin": "metadata-only"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "empty.txt", obj.Name)
	assert.Equal(t, "obj-insert-bucket", obj.Bucket)
	assert.Equal(t, "text/plain", obj.ContentType)
	assert.EqualValues(t, 0, obj.Size)

	// The insert created the object, so the collection answers about it with
	// the fields the insert supplied.
	got, err := svc.Objects.Get("obj-insert-bucket", "empty.txt").Do()
	require.NoError(t, err)
	assert.Equal(t, "empty.txt", got.Name)
	assert.Equal(t, "obj-insert-bucket", got.Bucket)
	assert.Equal(t, "text/plain", got.ContentType, "the supplied contentType persisted")
	assert.EqualValues(t, 0, got.Size, "a metadata-only insert makes a zero-length object")
	assert.Equal(t, map[string]string{"origin": "metadata-only"}, got.Metadata)

	list, err := svc.Objects.List("obj-insert-bucket").Do()
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "empty.txt", list.Items[0].Name)
}
