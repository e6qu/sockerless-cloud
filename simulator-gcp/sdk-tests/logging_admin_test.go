package gcp_sdk_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/logging"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	loggingrpc "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// These round-trips drive the Cloud Logging admin families (sinks, exclusions,
// buckets, views, links) across more than one parent scope, exercising the
// multi-scope mounting in simulator-gcp/logging_admin.go: the project scope and
// the organizations scope share the same handler bodies mounted under different
// parent paths.

func TestLogging_ExclusionCRUD_ProjectScope(t *testing.T) {
	svc := loggingRESTService(t)
	parent := "projects/excl-test-project"
	name := parent + "/exclusions/sdk-exclusion"

	created, err := svc.Projects.Exclusions.Create(parent, &loggingrpc.LogExclusion{
		Name:   "sdk-exclusion",
		Filter: `severity < WARNING`,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, name, created.Name)
	assert.Equal(t, `severity < WARNING`, created.Filter)

	got, err := svc.Projects.Exclusions.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)

	list, err := svc.Projects.Exclusions.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, e := range list.Exclusions {
		if e.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created exclusion must appear in list")

	patched, err := svc.Projects.Exclusions.Patch(name, &loggingrpc.LogExclusion{
		Description: "updated",
		Disabled:    true,
	}).UpdateMask("description,disabled").Do()
	require.NoError(t, err)
	assert.Equal(t, "updated", patched.Description)
	assert.True(t, patched.Disabled)

	_, err = svc.Projects.Exclusions.Delete(name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Exclusions.Get(name).Do()
	require.Error(t, err)
}

func TestLogging_ExclusionCRUD_OrganizationScope(t *testing.T) {
	svc := loggingRESTService(t)
	parent := "organizations/123456789"
	name := parent + "/exclusions/org-exclusion"

	created, err := svc.Organizations.Exclusions.Create(parent, &loggingrpc.LogExclusion{
		Name:   "org-exclusion",
		Filter: `resource.type = "gce_instance"`,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, name, created.Name)

	got, err := svc.Organizations.Exclusions.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)

	_, err = svc.Organizations.Exclusions.Delete(name).Do()
	require.NoError(t, err)
}

func TestLogging_SinkCRUD_OrganizationScope(t *testing.T) {
	svc := loggingRESTService(t)
	parent := "organizations/987654321"
	resourceName := parent + "/sinks/org-sink"

	created, err := svc.Organizations.Sinks.Create(parent, &loggingrpc.LogSink{
		Name:        "org-sink",
		Destination: "storage.googleapis.com/org-log-bucket",
		Filter:      `severity >= ERROR`,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "org-sink", created.Name)
	assert.Equal(t, resourceName, created.ResourceName)

	got, err := svc.Organizations.Sinks.Get(resourceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "org-sink", got.Name)
	assert.Equal(t, resourceName, got.ResourceName)

	list, err := svc.Organizations.Sinks.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, s := range list.Sinks {
		if s.Name == "org-sink" {
			found = true
		}
	}
	assert.True(t, found, "created org sink must appear in list")

	updated, err := svc.Organizations.Sinks.Update(resourceName, &loggingrpc.LogSink{
		Name:        "org-sink",
		Destination: "bigquery.googleapis.com/projects/p/datasets/org_logs",
	}).Do()
	require.NoError(t, err)
	assert.Contains(t, updated.Destination, "bigquery")

	_, err = svc.Organizations.Sinks.Delete(resourceName).Do()
	require.NoError(t, err)
	_, err = svc.Organizations.Sinks.Get(resourceName).Do()
	require.Error(t, err)
}

func TestLogging_BucketAndViewCRUD_ProjectScope(t *testing.T) {
	svc := loggingRESTService(t)
	parent := "projects/bucket-test-project/locations/global"
	bucketName := parent + "/buckets/sdk-bucket"

	created, err := svc.Projects.Locations.Buckets.Create(parent, &loggingrpc.LogBucket{
		Description:   "sdk test bucket",
		RetentionDays: 30,
	}).BucketId("sdk-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, bucketName, created.Name)
	assert.Equal(t, int64(30), created.RetentionDays)
	assert.Equal(t, "ACTIVE", created.LifecycleState)

	got, err := svc.Projects.Locations.Buckets.Get(bucketName).Do()
	require.NoError(t, err)
	assert.Equal(t, bucketName, got.Name)

	list, err := svc.Projects.Locations.Buckets.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, b := range list.Buckets {
		if b.Name == bucketName {
			found = true
		}
	}
	assert.True(t, found, "created bucket must appear in list")

	patched, err := svc.Projects.Locations.Buckets.Patch(bucketName, &loggingrpc.LogBucket{
		RetentionDays: 60,
	}).UpdateMask("retentionDays").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(60), patched.RetentionDays)

	// View CRUD under the bucket.
	viewName := bucketName + "/views/sdk-view"
	createdView, err := svc.Projects.Locations.Buckets.Views.Create(bucketName, &loggingrpc.LogView{
		Description: "sdk view",
		Filter:      `resource.type = "global"`,
	}).ViewId("sdk-view").Do()
	require.NoError(t, err)
	assert.Equal(t, viewName, createdView.Name)

	gotView, err := svc.Projects.Locations.Buckets.Views.Get(viewName).Do()
	require.NoError(t, err)
	assert.Equal(t, viewName, gotView.Name)

	viewList, err := svc.Projects.Locations.Buckets.Views.List(bucketName).Do()
	require.NoError(t, err)
	viewFound := false
	for _, v := range viewList.Views {
		if v.Name == viewName {
			viewFound = true
		}
	}
	assert.True(t, viewFound, "created view must appear in list")

	_, err = svc.Projects.Locations.Buckets.Views.Delete(viewName).Do()
	require.NoError(t, err)

	// Bucket delete is a soft-delete (DELETE_REQUESTED); undelete restores it.
	_, err = svc.Projects.Locations.Buckets.Delete(bucketName).Do()
	require.NoError(t, err)
	afterDelete, err := svc.Projects.Locations.Buckets.Get(bucketName).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", afterDelete.LifecycleState)

	_, err = svc.Projects.Locations.Buckets.Undelete(bucketName, &loggingrpc.UndeleteBucketRequest{}).Do()
	require.NoError(t, err)
	afterUndelete, err := svc.Projects.Locations.Buckets.Get(bucketName).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", afterUndelete.LifecycleState)
}

func TestLogging_BucketCRUD_OrganizationScope(t *testing.T) {
	svc := loggingRESTService(t)
	parent := "organizations/555000111/locations/global"
	bucketName := parent + "/buckets/org-bucket"

	created, err := svc.Organizations.Locations.Buckets.Create(parent, &loggingrpc.LogBucket{
		Description: "org bucket",
	}).BucketId("org-bucket").Do()
	require.NoError(t, err)
	assert.Equal(t, bucketName, created.Name)

	got, err := svc.Organizations.Locations.Buckets.Get(bucketName).Do()
	require.NoError(t, err)
	assert.Equal(t, bucketName, got.Name)

	_, err = svc.Organizations.Locations.Buckets.Delete(bucketName).Do()
	require.NoError(t, err)
}

func TestLogging_LinkCreateAndList_ProjectScope(t *testing.T) {
	svc := loggingRESTService(t)
	locParent := "projects/link-test-project/locations/global"
	bucketName := locParent + "/buckets/link-bucket"

	_, err := svc.Projects.Locations.Buckets.Create(locParent, &loggingrpc.LogBucket{
		AnalyticsEnabled: true,
	}).BucketId("link-bucket").Do()
	require.NoError(t, err)

	// CreateLink returns a long-running Operation.
	op, err := svc.Projects.Locations.Buckets.Links.Create(bucketName, &loggingrpc.Link{
		Description: "sdk link",
	}).LinkId("sdk-link").Do()
	require.NoError(t, err)
	require.NotNil(t, op)
	assert.True(t, op.Done)

	linkName := bucketName + "/links/sdk-link"
	gotLink, err := svc.Projects.Locations.Buckets.Links.Get(linkName).Do()
	require.NoError(t, err)
	assert.Equal(t, linkName, gotLink.Name)

	list, err := svc.Projects.Locations.Buckets.Links.List(bucketName).Do()
	require.NoError(t, err)
	found := false
	for _, l := range list.Links {
		if l.Name == linkName {
			found = true
		}
	}
	assert.True(t, found, "created link must appear in list")
}

// TestLogging_EntriesCopy routes a log into a user-defined log bucket through a
// sink, copies the bucket's error entries into a Cloud Storage bucket, and
// reads back what the copy wrote: the count the operation reports, and the
// entries themselves in the exported object.
func TestLogging_EntriesCopy(t *testing.T) {
	svc := loggingRESTService(t)
	const project = "copy-test-project"
	const logID = "copy-source"
	locParent := "projects/" + project + "/locations/global"
	bucketName := locParent + "/buckets/copy-bucket"

	_, err := svc.Projects.Locations.Buckets.Create(locParent, &loggingrpc.LogBucket{}).BucketId("copy-bucket").Do()
	require.NoError(t, err)
	_, err = svc.Projects.Sinks.Create("projects/"+project, &loggingrpc.LogSink{
		Name:        "copy-sink",
		Destination: "logging.googleapis.com/" + bucketName,
		Filter:      fmt.Sprintf(`logName="projects/%s/logs/%s"`, project, logID),
	}).Do()
	require.NoError(t, err)

	gcs := storageClient(t)
	dest := gcs.Bucket("copy-dest-bucket")
	require.NoError(t, dest.Create(ctx, project, nil))

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	writer, err := logging.NewClient(ctx, project, option.WithGRPCConn(conn))
	require.NoError(t, err)
	logger := writer.Logger(logID)
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "copy info", Severity: logging.Info}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "copy error one", Severity: logging.Error}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "copy error two", Severity: logging.Critical}))
	require.NoError(t, writer.Logger("not-routed").LogSync(ctx, logging.Entry{Payload: "unrouted error", Severity: logging.Error}))
	require.NoError(t, writer.Close())

	op, err := svc.Entries.Copy(&loggingrpc.CopyLogEntriesRequest{
		Name:        bucketName,
		Filter:      `severity >= ERROR`,
		Destination: "storage.googleapis.com/copy-dest-bucket",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	var response loggingrpc.CopyLogEntriesResponse
	require.NoError(t, json.Unmarshal(op.Response, &response))
	assert.Equal(t, int64(2), response.LogEntriesCopiedCount, "the two routed entries at or above ERROR")
	var metadata loggingrpc.CopyLogEntriesMetadata
	require.NoError(t, json.Unmarshal(op.Metadata, &metadata))
	assert.Equal(t, bucketName, metadata.Source)
	assert.Equal(t, "storage.googleapis.com/copy-dest-bucket", metadata.Destination)
	assert.Equal(t, "OPERATION_STATE_SUCCEEDED", metadata.State)

	fetched, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, op.Response, fetched.Response)

	var payloads []string
	it := dest.Objects(ctx, &storage.Query{Prefix: logID + "/"})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		r, err := dest.Object(attrs.Name).NewReader(ctx)
		require.NoError(t, err)
		body, err := io.ReadAll(r)
		require.NoError(t, r.Close())
		require.NoError(t, err)
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var entry loggingrpc.LogEntry
			require.NoError(t, json.Unmarshal([]byte(line), &entry))
			assert.Equal(t, fmt.Sprintf("projects/%s/logs/%s", project, logID), entry.LogName)
			payloads = append(payloads, entry.TextPayload)
		}
	}
	assert.ElementsMatch(t, []string{"copy error one", "copy error two"}, payloads)

	_, err = svc.Entries.Copy(&loggingrpc.CopyLogEntriesRequest{
		Name:        locParent + "/buckets/no-such-bucket",
		Destination: "storage.googleapis.com/copy-dest-bucket",
	}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

// TestLogging_EntriesTail writes a spread of severities into one log and tails
// it: entries.tail must return exactly the entries at or above the filter's
// severity, carrying the payload, severity and log name they were written with.
func TestLogging_EntriesTail(t *testing.T) {
	svc := loggingRESTService(t)

	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)
	const logID = "tail-test"
	logName := "projects/test-project/logs/" + logID
	logger := writeClient.Logger(logID)
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "tail debug", Severity: logging.Debug}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "tail info", Severity: logging.Info}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "tail warning", Severity: logging.Warning}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "tail error", Severity: logging.Error}))
	require.NoError(t, writeClient.Close())

	tail := func(filter string) map[string]string {
		t.Helper()
		resp, err := svc.Entries.Tail(&loggingrpc.TailLogEntriesRequest{
			ResourceNames: []string{"projects/test-project"},
			Filter:        filter,
		}).Do()
		require.NoError(t, err)
		require.NotNil(t, resp)
		bySeverity := map[string]string{}
		for _, e := range resp.Entries {
			assert.Equal(t, logName, e.LogName, "the log-name clause must scope the tail")
			bySeverity[e.Severity] = e.TextPayload
		}
		return bySeverity
	}

	scope := fmt.Sprintf("logName=%q AND ", logName)
	assert.Equal(t, map[string]string{
		"INFO":    "tail info",
		"WARNING": "tail warning",
		"ERROR":   "tail error",
	}, tail(scope+"severity>=INFO"), "severity>=INFO excludes the DEBUG entry")

	assert.Equal(t, map[string]string{
		"ERROR": "tail error",
	}, tail(scope+"severity>=ERROR"))

	// Without the severity clause every entry of the log is tailed, so the
	// exclusions above are the filter's doing and not a missing write.
	assert.Len(t, tail(fmt.Sprintf("logName=%q", logName)), 4)
}

func TestLogging_MonitoredResourceDescriptors_List(t *testing.T) {
	svc := loggingRESTService(t)
	list, err := svc.MonitoredResourceDescriptors.List().Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.ResourceDescriptors)
	types := map[string]bool{}
	for _, d := range list.ResourceDescriptors {
		types[d.Type] = true
	}
	assert.True(t, types["global"], "global descriptor must be listed")
}
