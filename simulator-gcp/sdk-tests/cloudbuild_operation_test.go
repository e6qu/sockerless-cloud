package gcp_sdk_test

import (
	"encoding/json"
	"testing"

	cloudbuildv2 "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/stretchr/testify/require"
)

// newCloudBuildClient is the Cloud Build Go SDK over its REST transport.
func newCloudBuildClient(t *testing.T) *cloudbuildv2.Client {
	t.Helper()
	client, err := cloudbuildv2.NewRESTClient(ctx, serviceHostOptions(cloudBuildHost)...)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// startedBuild is what a method that starts a build answers with: the
// operation tracking the build, and the build its BuildOperationMetadata
// carries.
type startedBuild struct {
	Operation string
	BuildID   string
}

// readStartedBuild decodes the operation a build-starting method returned and
// requires it to be the one Cloud Build hands back at once: not done yet, with
// the queued build in its metadata.
func readStartedBuild(t *testing.T, raw string) startedBuild {
	t.Helper()
	var op struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Metadata struct {
			Type  string `json:"@type"`
			Build struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"build"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &op), "build operation: %s", raw)
	require.NotEmpty(t, op.Name, "build operation: %s", raw)
	require.False(t, op.Done, "the operation comes back before the build runs: %s", raw)
	require.Equal(t, "type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata", op.Metadata.Type, raw)
	require.NotEmpty(t, op.Metadata.Build.ID, "the metadata carries the build: %s", raw)
	require.Equal(t, "QUEUED", op.Metadata.Build.Status, raw)
	return startedBuild{Operation: op.Name, BuildID: op.Metadata.Build.ID}
}

// waitBuild waits on a build's operation through the Cloud Build SDK, which
// completes it when the build ends: the Build when it succeeded, the
// operation's error otherwise.
func waitBuild(t *testing.T, started startedBuild) (*cloudbuildpb.Build, error) {
	t.Helper()
	return newCloudBuildClient(t).CreateBuildOperation(started.Operation).Wait(ctx)
}
