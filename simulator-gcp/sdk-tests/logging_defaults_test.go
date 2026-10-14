package gcp_sdk_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	loggingrpc "google.golang.org/api/logging/v2"
)

// TestLogging_BuiltInBucketsRouteAndRetain_SDK reads a project's built-in
// _Required and _Default buckets and sinks, writes an audit and an
// application entry, finds each in its own bucket, and is refused a change
// to the _Required sink.
func TestLogging_BuiltInBucketsRouteAndRetain_SDK(t *testing.T) {
	t.Parallel()
	svc := loggingRESTService(t)
	const parent = "projects/sdk-logging-defaults"

	sinks, err := svc.Projects.Sinks.List(parent).Do()
	require.NoError(t, err)
	var names []string
	for _, s := range sinks.Sinks {
		names = append(names, s.Name)
	}
	assert.True(t, slices.Contains(names, "_Default") && slices.Contains(names, "_Required"), "sinks: %v", names)

	def, err := svc.Projects.Locations.Buckets.Get(parent + "/locations/global/buckets/_Default").Do()
	require.NoError(t, err)
	assert.EqualValues(t, 30, def.RetentionDays)
	required, err := svc.Projects.Locations.Buckets.Get(parent + "/locations/global/buckets/_Required").Do()
	require.NoError(t, err)
	assert.EqualValues(t, 400, required.RetentionDays)
	assert.True(t, required.Locked)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = svc.Entries.Write(&loggingrpc.WriteLogEntriesRequest{Entries: []*loggingrpc.LogEntry{
		{LogName: parent + "/logs/cloudaudit.googleapis.com%2Factivity", Timestamp: now, TextPayload: "admin activity",
			Resource: &loggingrpc.MonitoredResource{Type: "global"}},
		{LogName: parent + "/logs/app", Timestamp: now, TextPayload: "application",
			Resource: &loggingrpc.MonitoredResource{Type: "global"}},
	}}).Do()
	require.NoError(t, err)
	read := func(bucket string) []string {
		out, err := svc.Entries.List(&loggingrpc.ListLogEntriesRequest{
			ResourceNames: []string{parent + "/locations/global/buckets/" + bucket},
		}).Do()
		require.NoError(t, err)
		var payloads []string
		for _, e := range out.Entries {
			payloads = append(payloads, e.TextPayload)
		}
		return payloads
	}
	assert.Equal(t, []string{"admin activity"}, read("_Required"))
	assert.Equal(t, []string{"application"}, read("_Default"))

	updated, err := svc.Projects.Locations.Buckets.Patch(parent+"/locations/global/buckets/_Default",
		&loggingrpc.LogBucket{RetentionDays: 60}).UpdateMask("retentionDays").Do()
	require.NoError(t, err)
	assert.EqualValues(t, 60, updated.RetentionDays)

	_, err = svc.Projects.Sinks.Delete(parent + "/sinks/_Required").Do()
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "deleting the _Required sink must fail, got %v", err)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
}
