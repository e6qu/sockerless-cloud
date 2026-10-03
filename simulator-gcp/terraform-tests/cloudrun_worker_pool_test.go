package gcp_tf_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2WorkerPoolRunsInstances applies a
// google_cloud_run_v2_worker_pool whose instances each write a file through a
// Cloud Storage volume, and receives the bucket's object notifications until
// every instance's file arrived. Lowering manual_instance_count stops the
// surplus instance before the apply finishes, and its stop is an object too.
func TestTerraformCloudRunV2WorkerPoolRunsInstances(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "cloudrun-worker-pool")
	cleanTerraformFixture(t, fixtureDir)
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve", "-var", "instances=1"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve", "-var", "instances=2"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	subscription := outputs.must(t, "subscription")
	bucket := outputs.must(t, "bucket")

	var started []string
	for len(started) < 2 {
		for _, name := range pullObjectNames(t, subscription) {
			if strings.HasPrefix(name, "started-") {
				started = append(started, name)
			}
		}
	}
	assert.Len(t, started, 2, "each of the pool's two instances wrote through its volume")

	out, err = runTimed(t, "terraform apply (scale down)", terraformCmdInDir(fixtureDir, "apply", "-auto-approve", "-var", "instances=1"))
	require.NoError(t, err, "terraform apply with one instance failed:\n%s", out)
	var listing struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodGet, "/storage/v1/b/"+bucket+"/o?prefix=stopped-", ""), &listing))
	assert.Len(t, listing.Items, 1, "lowering the instance count stopped one instance")
}

// pullObjectNames receives the next Cloud Storage notifications on
// subscription, acknowledges them and returns the object names they carry.
// Pull without returnImmediately holds the call until a message arrives.
func pullObjectNames(t *testing.T, subscription string) []string {
	t.Helper()
	var pulled struct {
		ReceivedMessages []struct {
			AckID   string `json:"ackId"`
			Message struct {
				Data       string            `json:"data"`
				Attributes map[string]string `json:"attributes"`
			} `json:"message"`
		} `json:"receivedMessages"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodPost, "/v1/"+subscription+":pull", `{"maxMessages":10}`), &pulled))
	var names, ackIDs []string
	for _, received := range pulled.ReceivedMessages {
		ackIDs = append(ackIDs, received.AckID)
		data, err := base64.StdEncoding.DecodeString(received.Message.Data)
		require.NoError(t, err)
		var object struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(data, &object))
		names = append(names, object.Name)
	}
	if len(ackIDs) > 0 {
		body, err := json.Marshal(map[string]any{"ackIds": ackIDs})
		require.NoError(t, err)
		simCall(t, http.MethodPost, "/v1/"+subscription+":acknowledge", string(body))
	}
	return names
}
