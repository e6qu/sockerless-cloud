package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	mathrand "math/rand/v2"
	"testing"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/logging/logadmin"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	ltype "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/protobuf/types/known/durationpb"
)

// A line a Cloud Run service's container writes that is a JSON object lands
// in Cloud Logging as jsonPayload, with the special fields lifted out of it
// into the entry: severity, the labels, the trace, the span, the sampling
// flag, the source location and the time. The message stays in the payload,
// and a trace= filter finds the entry.
func TestSDK_CloudRunV2Services_StructuredLogLine(t *testing.T) {
	t.Parallel()
	serviceID := uniqueName("v2-svc-json")
	trace := fmt.Sprintf("projects/test-project/traces/%032x", uint64(mathrand.Uint32())<<32|uint64(mathrand.Uint32()))
	line, err := json.Marshal(map[string]any{
		"severity":                              "warning",
		"message":                               "structured hello",
		"component":                             "test",
		"time":                                  "2026-01-02T03:04:05.678Z",
		"logging.googleapis.com/labels":         map[string]string{"team": "sim", "tier": "web"},
		"logging.googleapis.com/trace":          trace,
		"logging.googleapis.com/spanId":         "000000000000004a",
		"logging.googleapis.com/trace_sampled":  true,
		"logging.googleapis.com/sourceLocation": map[string]any{"file": "main.go", "line": 42, "function": "main.main"},
		"logging.googleapis.com/operation":      map[string]any{"id": "get_data", "producer": "github.com/sim/app", "first": "true"},
		"httpRequest":                           map[string]any{"requestMethod": "GET", "status": 204, "latency": "1.5s", "responseSize": 7},
	})
	require.NoError(t, err)

	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, early := openReadyTail(t, tailCtx, newLoggingV2Client(t), "test-project",
		`resource.type="cloud_run_revision" AND resource.labels.service_name="`+serviceID+`" AND trace="`+trace+`"`)

	client := newServicesClient(t)
	op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    "projects/test-project/locations/us-central1",
		ServiceId: serviceID,
		Service: &runpb.Service{Template: &runpb.RevisionTemplate{
			Scaling:    &runpb.RevisionScaling{MinInstanceCount: 1},
			Containers: []*runpb.Container{{Image: commandImageName, Args: []string{"log-http", "8080", string(line)}}},
		}},
	})
	require.NoError(t, err)
	svc, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupService(t, client, svc.Name)

	var entry *loggingpb.LogEntry
	if len(early) > 0 {
		entry = early[0]
	}
	for entry == nil {
		resp, err := stream.Recv()
		require.NoError(t, err, "the structured line never reached Cloud Logging")
		if entries := resp.GetEntries(); len(entries) > 0 {
			entry = entries[0]
		}
	}

	payload := entry.GetJsonPayload()
	require.NotNil(t, payload, "a JSON line is a jsonPayload, got textPayload %q", entry.GetTextPayload())
	fields := payload.AsMap()
	assert.Equal(t, "structured hello", fields["message"], "the message stays in the payload")
	assert.Equal(t, "test", fields["component"])
	for _, lifted := range []string{"severity", "time", "httpRequest", "logging.googleapis.com/labels", "logging.googleapis.com/trace",
		"logging.googleapis.com/spanId", "logging.googleapis.com/trace_sampled", "logging.googleapis.com/sourceLocation", "logging.googleapis.com/operation"} {
		assert.NotContains(t, fields, lifted, "a special field is moved out of the payload")
	}
	assert.Equal(t, ltype.LogSeverity_WARNING, entry.GetSeverity())
	assert.Equal(t, "sim", entry.GetLabels()["team"])
	assert.Equal(t, "web", entry.GetLabels()["tier"])
	assert.NotEmpty(t, entry.GetLabels()["instanceId"], "the instance label stays beside the payload's labels")
	assert.Equal(t, trace, entry.GetTrace())
	assert.Equal(t, "000000000000004a", entry.GetSpanId())
	assert.True(t, entry.GetTraceSampled())
	assert.Equal(t, "main.go", entry.GetSourceLocation().GetFile())
	assert.Equal(t, int64(42), entry.GetSourceLocation().GetLine())
	assert.Equal(t, "main.main", entry.GetSourceLocation().GetFunction())
	assert.Equal(t, "get_data", entry.GetOperation().GetId())
	assert.True(t, entry.GetOperation().GetFirst())
	assert.Equal(t, "GET", entry.GetHttpRequest().GetRequestMethod())
	assert.Equal(t, int32(204), entry.GetHttpRequest().GetStatus())
	assert.Equal(t, int64(7), entry.GetHttpRequest().GetResponseSize())
	assert.Equal(t, 1500*time.Millisecond, entry.GetHttpRequest().GetLatency().AsDuration())
	assert.Equal(t, "2026-01-02T03:04:05.678Z", entry.GetTimestamp().AsTime().Format("2006-01-02T15:04:05.000Z07:00"))
	assert.Equal(t, "projects/test-project/logs/run.googleapis.com%2Fstdout", entry.GetLogName())
}

// A line an execution's container writes to stderr lands in the job's
// run.googleapis.com/stderr log, as the stdout lines land in stdout's.
func TestCloudRun_RunJobStderrLineLandsInTheStderrLog(t *testing.T) {
	t.Parallel()
	jobsClient := newJobsClient(t)
	jobID := uniqueName("sdk-run-job-stderr")
	marker := "stderr-output-" + jobID

	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{Template: &runpb.ExecutionTemplate{Template: &runpb.TaskTemplate{
			Containers: []*runpb.Container{{Image: simWorkloadImage, Command: []string{"sh", "-c"}, Args: []string{"echo " + marker + " >&2; echo " + marker + "-out"}}},
			Timeout:    durationpb.New(60 * time.Second),
		}}},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, jobsClient, "projects/test-project/locations/us-central1/jobs/"+jobID)

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{Name: "projects/test-project/locations/us-central1/jobs/" + jobID})
	require.NoError(t, err)
	// The operation completes once the execution's log lines are in place.
	_, err = runOp.Wait(ctx)
	require.NoError(t, err)

	logs := map[string]string{}
	it := logadminClient(t).Entries(ctx, logadmin.Filter(`resource.type="cloud_run_job" AND resource.labels.job_name="`+jobID+`" AND textPayload:"`+marker+`"`))
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		logs[entry.Payload.(string)] = entry.LogName
	}
	// logadmin unescapes the log id's %2F in the LogName it returns.
	assert.Equal(t, map[string]string{
		marker:          "projects/test-project/logs/run.googleapis.com/stderr",
		marker + "-out": "projects/test-project/logs/run.googleapis.com/stdout",
	}, logs)
}
