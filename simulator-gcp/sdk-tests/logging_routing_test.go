package gcp_sdk_test

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	loggingrpc "google.golang.org/api/logging/v2"
)

// TestLogging_AggregatedSinkAndDefaultSinkExclusions_SDK creates a folder and
// a project in it, routes the project's errors through a folder sink with
// includeChildren into a bucket of another project, reads that bucket, and
// finds an exclusion the exclusions API created on the project's _Default
// sink and the built-in views on its _Default bucket.
func TestLogging_AggregatedSinkAndDefaultSinkExclusions_SDK(t *testing.T) {
	t.Parallel()
	svc := loggingRESTService(t)
	crmSvc := crmV3Service(t)

	op, err := crmSvc.Folders.Create(&crm.Folder{DisplayName: "logging-routing", Parent: "organizations/123456789012"}).Do()
	require.NoError(t, err)
	folder := crmOpResourceName(t, op)
	project := uniquename.New("log-child")
	_, err = crmSvc.Projects.Create(&crm.Project{ProjectId: project, Parent: folder}).Do()
	require.NoError(t, err)
	central := uniquename.New("log-central")
	requireProject(t, central)
	bucket := "projects/" + central + "/locations/global/buckets/errors"
	_, err = svc.Projects.Locations.Buckets.Create("projects/"+central+"/locations/global", &loggingrpc.LogBucket{}).BucketId("errors").Do()
	require.NoError(t, err)
	_, err = svc.Folders.Sinks.Create(folder, &loggingrpc.LogSink{
		Name: "child-errors", Destination: "logging.googleapis.com/" + bucket,
		Filter: "severity>=ERROR", IncludeChildren: true,
	}).Do()
	require.NoError(t, err)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = svc.Entries.Write(&loggingrpc.WriteLogEntriesRequest{Entries: []*loggingrpc.LogEntry{
		{LogName: "projects/" + project + "/logs/app", Timestamp: now, Severity: "ERROR", TextPayload: "routed by the folder",
			Resource: &loggingrpc.MonitoredResource{Type: "global"}},
		{LogName: "projects/" + project + "/logs/app", Timestamp: now, Severity: "INFO", TextPayload: "kept in the project",
			Resource: &loggingrpc.MonitoredResource{Type: "global"}},
	}}).Do()
	require.NoError(t, err)
	out, err := svc.Entries.List(&loggingrpc.ListLogEntriesRequest{ResourceNames: []string{bucket}}).Do()
	require.NoError(t, err)
	require.Len(t, out.Entries, 1)
	assert.Equal(t, "routed by the folder", out.Entries[0].TextPayload)

	parent := "projects/" + project
	_, err = svc.Projects.Exclusions.Create(parent, &loggingrpc.LogExclusion{Name: "drop-debug", Filter: "severity=DEBUG"}).Do()
	require.NoError(t, err)
	sink, err := svc.Projects.Sinks.Get(parent + "/sinks/_Default").Do()
	require.NoError(t, err)
	require.Len(t, sink.Exclusions, 1)
	assert.Equal(t, "drop-debug", sink.Exclusions[0].Name)

	views, err := svc.Projects.Locations.Buckets.Views.List(parent + "/locations/global/buckets/_Default").Do()
	require.NoError(t, err)
	var names []string
	for _, v := range views.Views {
		names = append(names, v.Name)
	}
	assert.ElementsMatch(t, []string{
		parent + "/locations/global/buckets/_Default/views/_AllLogs",
		parent + "/locations/global/buckets/_Default/views/_Default",
	}, names)
}
