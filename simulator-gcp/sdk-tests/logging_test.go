package gcp_sdk_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"cloud.google.com/go/logging"
	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	loggingrpc "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func logadminClient(t *testing.T) *logadmin.Client {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client, err := logadmin.NewClient(ctx, "test-project", option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	return client
}

func TestLogging_SeverityFilterUsesCloudOrdering(t *testing.T) {
	client := logadminClient(t)
	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	logger := writeClient.Logger("severity-order-test")
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "debug payload", Severity: logging.Debug}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "error payload", Severity: logging.Error}))
	require.NoError(t, logger.LogSync(ctx, logging.Entry{Payload: "critical payload", Severity: logging.Critical}))
	require.NoError(t, writeClient.Close())

	it := client.Entries(ctx, logadmin.Filter(`logName="projects/test-project/logs/severity-order-test" AND severity>=ERROR`))
	var payloads []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if s, ok := entry.Payload.(string); ok {
			payloads = append(payloads, s)
		}
	}
	assert.ElementsMatch(t, []string{"error payload", "critical payload"}, payloads)
}

func TestLogging_WriteAndListEntries(t *testing.T) {
	client := logadminClient(t)

	// Write entries via gRPC using the logging.Client (write client)
	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	err = writeClient.Logger("test-log-write").LogSync(ctx, writeEntry("hello from gRPC"))
	require.NoError(t, err)
	err = writeClient.Logger("test-log-write").LogSync(ctx, writeEntry("second entry"))
	require.NoError(t, err)
	err = writeClient.Close()
	require.NoError(t, err)

	// List entries via logadmin
	it := client.Entries(ctx, logadmin.Filter(`logName="projects/test-project/logs/test-log-write"`))

	var messages []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if entry.Payload != nil {
			if s, ok := entry.Payload.(string); ok {
				messages = append(messages, s)
			}
		}
	}

	require.Len(t, messages, 2)
	assert.Equal(t, "hello from gRPC", messages[0])
	assert.Equal(t, "second entry", messages[1])
}

func TestLogging_FilterByResourceType(t *testing.T) {
	client := logadminClient(t)

	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	// Write entries with different resource types
	err = writeClient.Logger("filter-test").LogSync(ctx, writeEntryWithResource("cloud_run_job", "job-1", "cloud run entry"))
	require.NoError(t, err)
	err = writeClient.Logger("filter-test").LogSync(ctx, writeEntryWithResource("cloud_run_revision", "svc-1", "cloud function entry"))
	require.NoError(t, err)
	err = writeClient.Close()
	require.NoError(t, err)

	// Filter by resource.type
	it := client.Entries(ctx, logadmin.Filter(`resource.type="cloud_run_job"`))

	var count int
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		assert.Equal(t, "cloud_run_job", entry.Resource.Type)
		count++
	}

	assert.GreaterOrEqual(t, count, 1)
}

func TestLogging_FilterByTimestamp(t *testing.T) {
	client := logadminClient(t)

	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	err = writeClient.Logger("ts-filter-test").LogSync(ctx, writeEntry("old entry"))
	require.NoError(t, err)
	err = writeClient.Logger("ts-filter-test").LogSync(ctx, writeEntry("new entry"))
	require.NoError(t, err)
	err = writeClient.Close()
	require.NoError(t, err)

	// Use a filter that should match all (timestamp >= a very old date)
	it := client.Entries(ctx, logadmin.Filter(`timestamp>="2020-01-01T00:00:00Z"`))

	var count int
	for {
		_, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		count++
	}

	assert.GreaterOrEqual(t, count, 2, "should return entries with timestamps >= 2020")
}

func TestLogging_FilterByTimestampStrictGT(t *testing.T) {
	// This test verifies the strict greater-than (>) timestamp filter used
	// by backends in follow mode: timestamp>"<cutoff>".
	client := logadminClient(t)
	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	// Use a dedicated log name to isolate from other tests
	logName := "ts-strict-gt-test"
	logger := writeClient.Logger(logName)

	// Write first entry
	err = logger.LogSync(ctx, writeEntry("entry-before"))
	require.NoError(t, err)

	// Small delay to ensure distinct timestamps
	time.Sleep(50 * time.Millisecond)

	// Record the cutoff timestamp in RFC3339Nano (between entries)
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)

	// Small delay again
	time.Sleep(50 * time.Millisecond)

	// Write second and third entries (after cutoff)
	err = logger.LogSync(ctx, writeEntry("entry-after-1"))
	require.NoError(t, err)
	err = logger.LogSync(ctx, writeEntry("entry-after-2"))
	require.NoError(t, err)

	err = writeClient.Close()
	require.NoError(t, err)

	// Query with strict greater-than filter on the log name + timestamp
	filter := fmt.Sprintf(`logName="projects/test-project/logs/%s" AND timestamp>"%s"`, logName, cutoff)
	it := client.Entries(ctx, logadmin.Filter(filter))

	var messages []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if s, ok := entry.Payload.(string); ok {
			messages = append(messages, s)
		}
	}

	// Only entries written after the cutoff should be returned
	require.Len(t, messages, 2, "strict > filter should exclude the first entry")
	assert.Equal(t, "entry-after-1", messages[0])
	assert.Equal(t, "entry-after-2", messages[1])
}

func loggingRESTService(t *testing.T) *loggingrpc.Service {
	t.Helper()
	svc, err := loggingrpc.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestLogging_SinkCRUD(t *testing.T) {
	svc := loggingRESTService(t)
	project := "sink-test-project"
	parent := "projects/" + project
	sinkResourceName := parent + "/sinks/sdk-test-sink"

	sink := &loggingrpc.LogSink{
		Name:        "sdk-test-sink",
		Destination: "bigquery.googleapis.com/projects/" + project + "/datasets/sdk_logs",
		Filter:      `severity >= WARNING`,
	}
	created, err := svc.Projects.Sinks.Create(parent, sink).Do()
	require.NoError(t, err)
	// Real Cloud Logging returns the short identifier in LogSink.name and the
	// full path in the separate output-only resourceName field.
	assert.Equal(t, "sdk-test-sink", created.Name)
	assert.Equal(t, sinkResourceName, created.ResourceName)
	assert.Equal(t, sink.Destination, created.Destination)

	// Get sink.
	got, err := svc.Projects.Sinks.Get(sinkResourceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-test-sink", got.Name)
	assert.Equal(t, sinkResourceName, got.ResourceName)

	// List sinks — must contain created sink by short name.
	list, err := svc.Projects.Sinks.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, s := range list.Sinks {
		if s.Name == "sdk-test-sink" {
			found = true
		}
	}
	assert.True(t, found, "created sink must appear in list")

	// Update (PUT) — change destination.
	updated, err := svc.Projects.Sinks.Update(sinkResourceName,
		&loggingrpc.LogSink{
			Name:        "sdk-test-sink",
			Destination: "storage.googleapis.com/" + project + "-backup-logs",
			Filter:      sink.Filter,
		}).Do()
	require.NoError(t, err)
	assert.Contains(t, updated.Destination, "backup-logs")

	// Delete sink.
	_, err = svc.Projects.Sinks.Delete(sinkResourceName).Do()
	require.NoError(t, err)

	// Get after delete must fail.
	_, err = svc.Projects.Sinks.Get(sinkResourceName).Do()
	require.Error(t, err)
}

func TestLogging_MetricCRUD(t *testing.T) {
	svc := loggingRESTService(t)
	project := "metric-test-project"
	parent := "projects/" + project
	metricResourceName := parent + "/metrics/sdk-test-metric"

	metric := &loggingrpc.LogMetric{
		Name:   "sdk-test-metric",
		Filter: `severity >= ERROR`,
	}
	created, err := svc.Projects.Metrics.Create(parent, metric).Do()
	require.NoError(t, err)
	// Real Cloud Logging returns the short metric identifier in LogMetric.name
	// (the [METRIC_ID] part), not the full resource path.
	assert.Equal(t, "sdk-test-metric", created.Name)
	assert.Equal(t, `severity >= ERROR`, created.Filter)

	// Get metric.
	got, err := svc.Projects.Metrics.Get(metricResourceName).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-test-metric", got.Name)

	// List metrics — must contain created metric by short name.
	list, err := svc.Projects.Metrics.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, m := range list.Metrics {
		if m.Name == "sdk-test-metric" {
			found = true
		}
	}
	assert.True(t, found, "created metric must appear in list")

	// Update — change filter.
	updated, err := svc.Projects.Metrics.Update(metricResourceName,
		&loggingrpc.LogMetric{
			Name:   "sdk-test-metric",
			Filter: `severity >= CRITICAL`,
		}).Do()
	require.NoError(t, err)
	assert.Equal(t, `severity >= CRITICAL`, updated.Filter)

	// Delete metric.
	_, err = svc.Projects.Metrics.Delete(metricResourceName).Do()
	require.NoError(t, err)

	// Get after delete must fail.
	_, err = svc.Projects.Metrics.Get(metricResourceName).Do()
	require.Error(t, err)
}

func TestLogging_ListSinks_Pagination(t *testing.T) {
	const project = "test-project"
	for _, name := range []string{"pag-sink-a", "pag-sink-b", "pag-sink-c"} {
		body, _ := json.Marshal(map[string]any{
			"name":        name,
			"destination": "storage.googleapis.com/my-bucket",
			"filter":      `severity="ERROR"`,
		})
		req, _ := http.NewRequest("POST",
			fmt.Sprintf("%s/v2/projects/%s/sinks", baseURL, project),
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		n := name
		t.Cleanup(func() {
			req, _ := http.NewRequest("DELETE",
				fmt.Sprintf("%s/v2/projects/%s/sinks/%s", baseURL, project, n), nil)
			http.DefaultClient.Do(req)
		})
	}

	seen := map[string]bool{}
	var pageToken string
	for {
		u := fmt.Sprintf("%s/v2/projects/%s/sinks?pageSize=1", baseURL, project)
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, _ := http.NewRequest("GET", u, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var body struct {
			Sinks         []map[string]any `json:"sinks"`
			NextPageToken string           `json:"nextPageToken"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		resp.Body.Close()
		for _, s := range body.Sinks {
			if n, ok := s["name"].(string); ok {
				seen[n] = true
			}
		}
		pageToken = body.NextPageToken
		if pageToken == "" {
			break
		}
	}
	// Real Cloud Logging returns the short sink identifier in LogSink.name.
	for _, n := range []string{"pag-sink-a", "pag-sink-b", "pag-sink-c"} {
		assert.True(t, seen[n], "sink %s should appear via pagination", n)
	}
}
