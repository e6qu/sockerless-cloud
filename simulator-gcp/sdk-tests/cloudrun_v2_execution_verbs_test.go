package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// The custom methods the Cloud Run v2 executions collection publishes.
//
// The document declares exactly one POST custom method on an execution —
// executions.cancel — and the simulator mounts one handler on
// "executions/{execAction}" to serve it, because Go's mux cannot spell a
// colon verb. The handler therefore sees every verb a client can name on an
// execution, and the only thing that distinguishes "the service serves this
// method" from "the service does not" is its own answer.
//
// The canonical client can only spell the published method, so the served half
// is driven through it; the refused half is a raw request naming a method the
// service does not publish, which is exactly what a client of a non-existent
// method would put on the wire.

func TestCloudRunV2_Executions_CancelIsServedAndUnknownVerbsAreNot(t *testing.T) {
	svc := newRunV2RESTService(t)
	const jobID = "exec-verb-job"
	jobName := crV2Parent + "/jobs/" + jobID

	createOp, err := svc.Projects.Locations.Jobs.Create(crV2Parent, &runv2.GoogleCloudRunV2Job{
		Template: &runv2.GoogleCloudRunV2ExecutionTemplate{
			Template: &runv2.GoogleCloudRunV2TaskTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{
					Image:   "alpine:latest",
					Command: []string{"sleep"},
					Args:    []string{"30"},
				}},
			},
		},
	}).JobId(jobID).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, createOp)
	t.Cleanup(func() { _, _ = svc.Projects.Locations.Jobs.Delete(jobName).Do() })

	runOp, err := svc.Projects.Locations.Jobs.Run(jobName, &runv2.GoogleCloudRunV2RunJobRequest{}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, runOp)

	executions, err := svc.Projects.Locations.Jobs.Executions.List(jobName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, executions.Executions)
	execName := executions.Executions[0].Name

	// A verb the service does not publish is refused as an unknown method,
	// not silently treated as the one verb the collection does publish.
	status, body := postRunV2Verb(t, execName, "teleport")
	assert.Equal(t, http.StatusNotFound, status, "body: %s", body)
	assert.Contains(t, body, "unknown action")
	assert.Contains(t, body, "teleport")

	// A request with no verb at all names no method either.
	status, body = postRunV2Verb(t, execName, "")
	assert.Equal(t, http.StatusNotFound, status, "body: %s", body)
	assert.Contains(t, body, "unknown action")

	// The refused requests left the execution alone, and the published verb
	// still works through the canonical client.
	before, err := svc.Projects.Locations.Jobs.Executions.Get(execName).Do()
	require.NoError(t, err)
	assert.Empty(t, before.CancelledCount)

	cancelOp, err := svc.Projects.Locations.Jobs.Executions.Cancel(execName,
		&runv2.GoogleCloudRunV2CancelExecutionRequest{}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, cancelOp)

	cancelled, err := svc.Projects.Locations.Jobs.Executions.Get(execName).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, cancelled.CompletionTime, "cancel settled the execution")
}

// postRunV2Verb POSTs an AIP-136 custom method on a Cloud Run v2 resource by
// its resource name, the way a client of that method would.
func postRunV2Verb(t *testing.T, resourceName, verb string) (int, string) {
	t.Helper()
	uri := baseURL + "/v2/" + resourceName
	if verb != "" {
		uri += ":" + verb
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	// The refusal must be a Google error envelope, not a bare mux miss.
	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error struct {
				Code    int    `json:"code"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &envelope), "body: %s", body)
		require.Equal(t, resp.StatusCode, envelope.Error.Code)
		require.Equal(t, "NOT_FOUND", envelope.Error.Status)
	}
	return resp.StatusCode, string(body)
}
