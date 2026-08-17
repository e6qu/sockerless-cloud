package gcp_sdk_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// loggingPayloads runs a filter through logadmin.Entries and returns the text
// payloads of the matching entries, in the order Cloud Logging returned them.
func loggingPayloads(t *testing.T, client *logadmin.Client, filter string) []string {
	t.Helper()
	it := client.Entries(ctx, logadmin.Filter(filter))
	var payloads []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			return payloads
		}
		require.NoError(t, err, "filter %q", filter)
		s, ok := entry.Payload.(string)
		require.Truef(t, ok, "entry payload is %T, want a text payload", entry.Payload)
		payloads = append(payloads, s)
	}
}

func TestLogging_FilterByResourceType(t *testing.T) {
	client := logadminClient(t)

	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	// Two entries in one log, differing only in resource type.
	logName := "filter-test"
	logger := writeClient.Logger(logName)
	require.NoError(t, logger.LogSync(ctx, writeEntryWithResource("cloud_run_job", "job-1", "cloud run entry")))
	require.NoError(t, logger.LogSync(ctx, writeEntryWithResource("cloud_run_revision", "svc-1", "cloud function entry")))
	require.NoError(t, writeClient.Close())

	// The log name scopes the query to this test's two entries, so the
	// resource.type clause is the only thing that can separate them: a filter
	// that ignored it would return both.
	byLogName := fmt.Sprintf(`logName="projects/test-project/logs/%s"`, logName)
	scope := byLogName + " AND "

	jobs := loggingPayloads(t, client, scope+`resource.type="cloud_run_job"`)
	assert.Equal(t, []string{"cloud run entry"}, jobs,
		"resource.type=cloud_run_job must match the job entry and exclude the revision entry")

	revisions := loggingPayloads(t, client, scope+`resource.type="cloud_run_revision"`)
	assert.Equal(t, []string{"cloud function entry"}, revisions)

	// Both entries are reachable without the resource.type clause, so the
	// exclusions above are the filter's doing and not a missing write.
	assert.ElementsMatch(t, []string{"cloud run entry", "cloud function entry"},
		loggingPayloads(t, client, byLogName))
}

func TestLogging_FilterByTimestamp(t *testing.T) {
	client := logadminClient(t)

	writeClient, err := newLoggingWriteClient(t)
	require.NoError(t, err)

	logName := "ts-filter-test"
	logger := writeClient.Logger(logName)

	// A cutoff comfortably before the first write, one between the two writes.
	// The early one is truncated to the second so it is unambiguously earlier
	// than either entry's sub-second timestamp.
	beforeAll := time.Now().UTC().Add(-time.Second).Truncate(time.Second).Format(time.RFC3339)
	require.NoError(t, logger.LogSync(ctx, writeEntry("old entry")))
	time.Sleep(50 * time.Millisecond)
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, logger.LogSync(ctx, writeEntry("new entry")))
	require.NoError(t, writeClient.Close())

	scope := fmt.Sprintf(`logName="projects/test-project/logs/%s" AND `, logName)

	// Both directions around the cutoff. A sim that dropped the timestamp
	// predicate would return both entries for the second query.
	assert.Equal(t, []string{"old entry", "new entry"},
		loggingPayloads(t, client, scope+fmt.Sprintf(`timestamp>="%s"`, beforeAll)),
		"a cutoff before both writes must return both entries")
	assert.Equal(t, []string{"new entry"},
		loggingPayloads(t, client, scope+fmt.Sprintf(`timestamp>="%s"`, cutoff)),
		"a cutoff between the writes must exclude the earlier entry")
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

// loggingSinkPage fetches one page of sinks.list, returning the sink names in
// wire order plus the page's nextPageToken. Real Cloud Logging returns the short
// sink identifier in LogSink.name.
func loggingSinkPage(t *testing.T, listURL, pageToken string) (names []string, next string) {
	t.Helper()
	u := listURL
	if pageToken != "" {
		sep := "&"
		if !strings.Contains(u, "?") {
			sep = "?"
		}
		u += sep + "pageToken=" + url.QueryEscape(pageToken)
	}
	req, _ := http.NewRequest("GET", u, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "list %s: %s", u, body)
	var decoded struct {
		Sinks         []map[string]any `json:"sinks"`
		NextPageToken string           `json:"nextPageToken"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	for _, s := range decoded.Sinks {
		n, ok := s["name"].(string)
		require.Truef(t, ok, "sink carries no name: %v", s)
		names = append(names, n)
	}
	return names, decoded.NextPageToken
}

func TestLogging_ListSinks_Pagination(t *testing.T) {
	const project = "test-project"
	want := []string{"pag-sink-a", "pag-sink-b", "pag-sink-c"}
	for _, name := range want {
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
		created, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "create sink %s: %s", name, created)
		t.Cleanup(func() {
			req, _ := http.NewRequest("DELETE",
				fmt.Sprintf("%s/v2/projects/%s/sinks/%s", baseURL, project, name), nil)
			resp, err := http.DefaultClient.Do(req)
			if assert.NoError(t, err, "delete sink %s", name) {
				resp.Body.Close()
			}
		})
	}

	listURL := fmt.Sprintf("%s/v2/projects/%s/sinks", baseURL, project)

	// The project holds exactly these three sinks, so both the unpaginated list
	// and the page-by-page walk are pinned exactly.
	all, next := loggingSinkPage(t, listURL, "")
	require.Equal(t, want, all)
	require.Empty(t, next, "a list without pageSize returns everything and no token")

	// pageSize=1 must yield one sink per page, a token on every page but the
	// last, and no sink twice — a single page carrying all three would satisfy
	// a union-only assertion while paginating nothing.
	seen := map[string]bool{}
	var got []string
	var token string
	for page := 1; page <= len(want); page++ {
		names, nextToken := loggingSinkPage(t, listURL+"?pageSize=1", token)
		require.Lenf(t, names, 1, "pageSize=1 must return exactly one sink (page %d of %d)", page, len(want))
		require.Falsef(t, seen[names[0]], "sink %q was returned on more than one page", names[0])
		seen[names[0]] = true
		got = append(got, names[0])
		if page < len(want) {
			require.NotEmptyf(t, nextToken, "page %d of %d must carry a nextPageToken", page, len(want))
		} else {
			require.Emptyf(t, nextToken, "the last page must carry no nextPageToken, got %q", nextToken)
		}
		token = nextToken
	}
	assert.Equal(t, want, got, "the one-sink-per-page walk must reproduce the unpaginated listing, in order")
}
