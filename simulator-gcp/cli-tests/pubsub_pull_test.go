package gcp_cli_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gcloudPulledMessage struct {
	Message struct {
		Data string `json:"data"`
	} `json:"message"`
}

// TestCLI_PubSubPullWaitsForPublish starts `gcloud pubsub subscriptions pull`
// on an empty subscription, which waits by default, and receives the message a
// later `gcloud pubsub topics publish` delivers.
func TestCLI_PubSubPullWaitsForPublish(t *testing.T) {
	runCLI(t, gcloudCLI("pubsub", "topics", "create", "cli-pull-wait-topic", "--format=json", "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("pubsub", "topics", "delete", "cli-pull-wait-topic", "--quiet").Run()
	})
	runCLI(t, gcloudCLI("pubsub", "subscriptions", "create", "cli-pull-wait-sub",
		"--topic=cli-pull-wait-topic", "--format=json", "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("pubsub", "subscriptions", "delete", "cli-pull-wait-sub", "--quiet").Run()
	})

	var empty []gcloudPulledMessage
	// The GA track drops --return-immediately and always waits; beta keeps it.
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, gcloudCLI("beta", "pubsub", "subscriptions", "pull", "cli-pull-wait-sub",
		"--return-immediately", "--format=json"))), &empty))
	require.Empty(t, empty, "--return-immediately answers an empty subscription with no messages")

	pull := gcloudCLI("pubsub", "subscriptions", "pull", "cli-pull-wait-sub",
		"--limit=1", "--auto-ack", "--format=json")
	var stdout, stderr bytes.Buffer
	pull.Stdout, pull.Stderr = &stdout, &stderr
	require.NoError(t, pull.Start())

	runCLI(t, gcloudCLI("pubsub", "topics", "publish", "cli-pull-wait-topic", "--message=after the pull"))

	require.NoError(t, pull.Wait(), "gcloud pubsub subscriptions pull: %s", stderr.String())
	var messages []gcloudPulledMessage
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &messages), stdout.String())
	require.Len(t, messages, 1)
	data, err := base64.StdEncoding.DecodeString(messages[0].Message.Data)
	require.NoError(t, err)
	assert.Equal(t, "after the pull", string(data))
}
