package aws_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambda_KinesisEventSourceMapping_CLI maps a function to a Kinesis data
// stream with the aws CLI and sees the function receive the records put to it,
// in order (with-kinesis.md.gz).
func TestLambda_KinesisEventSourceMapping_CLI(t *testing.T) {
	t.Parallel()
	const name = "cli-esm-kinesis"

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	events := make(chan []byte, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		events <- body
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	callbackURL := fmt.Sprintf("http://host.docker.internal:%d/invoke", ln.Addr().(*net.TCPAddr).Port)

	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", name, "--shard-count", "1"))
	t.Cleanup(func() { runCLI(t, awsCLI("kinesis", "delete-stream", "--stream-name", name)) })
	runCLI(t, awsCLI("kinesis", "wait", "stream-exists", "--stream-name", name))
	streamARN := strings.TrimSpace(runCLI(t, awsCLI("kinesis", "describe-stream-summary", "--stream-name", name,
		"--query", "StreamDescriptionSummary.StreamARN", "--output", "text")))

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", name,
		"--package-type", "Image",
		"--role", cliRoleAllowing(t, "lambda.amazonaws.com", "kinesis:GetShardIterator", "kinesis:GetRecords"),
		"--code", "ImageUri="+lambdaHandlerImageName,
		"--architectures", nativeLambdaArchitecture(),
		"--environment", "Variables={INVOCATION_CALLBACK_URL="+callbackURL+"}",
	))
	t.Cleanup(func() { runCLI(t, awsCLI("lambda", "delete-function", "--function-name", name)) })

	for _, record := range []string{"alpha", "beta"} {
		runCLI(t, awsCLI("kinesis", "put-record", "--stream-name", name, "--partition-key", "p",
			"--data", base64.StdEncoding.EncodeToString([]byte(record))))
	}

	var mapping struct {
		UUID                 string `json:"UUID"`
		State                string `json:"State"`
		MaximumRetryAttempts int    `json:"MaximumRetryAttempts"`
		BatchSize            int    `json:"BatchSize"`
	}
	parseJSON(t, runCLI(t, awsCLI("lambda", "create-event-source-mapping",
		"--function-name", name, "--event-source-arn", streamARN, "--starting-position", "TRIM_HORIZON", "--output", "json")), &mapping)
	t.Cleanup(func() { runCLI(t, awsCLI("lambda", "delete-event-source-mapping", "--uuid", mapping.UUID)) })
	assert.Equal(t, -1, mapping.MaximumRetryAttempts)

	// The mapping carries tags of its own, addressed by its ARN.
	mappingARN := strings.TrimSpace(runCLI(t, awsCLI("lambda", "get-event-source-mapping", "--uuid", mapping.UUID,
		"--query", "EventSourceMappingArn", "--output", "text")))
	runCLI(t, awsCLI("lambda", "tag-resource", "--resource", mappingARN, "--tags", "team=blue"))
	assert.Equal(t, "blue", strings.TrimSpace(runCLI(t, awsCLI("lambda", "list-tags", "--resource", mappingARN,
		"--query", "Tags.team", "--output", "text"))))
	runCLI(t, awsCLI("lambda", "untag-resource", "--resource", mappingARN, "--tag-keys", "team"))
	assert.Equal(t, 100, mapping.BatchSize)

	var event struct {
		Records []struct {
			Kinesis struct {
				Data string `json:"data"`
			} `json:"kinesis"`
		} `json:"Records"`
	}
	select {
	case body := <-events:
		require.NoError(t, json.Unmarshal(body, &event))
	case <-time.After(2 * time.Minute):
		t.Fatal("the function never received the stream's records")
	}
	var got []string
	for _, record := range event.Records {
		decoded, err := base64.StdEncoding.DecodeString(record.Kinesis.Data)
		require.NoError(t, err)
		got = append(got, string(decoded))
	}
	assert.Equal(t, []string{"alpha", "beta"}, got)
}
