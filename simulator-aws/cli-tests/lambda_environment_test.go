package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliCreateCountingFunction creates an image function on the test handler,
// whose "count" action reports the invocations its execution environment
// served and how the environment was initialised.
func cliCreateCountingFunction(t *testing.T, name string, extra ...string) {
	t.Helper()
	args := append([]string{"lambda", "create-function",
		"--function-name", name,
		"--package-type", "Image",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--code", "ImageUri=" + lambdaHandlerImageName,
		"--architectures", nativeLambdaArchitecture(),
		"--timeout", "30",
	}, extra...)
	runCLI(t, awsCLI(args...))
	t.Cleanup(func() { runCLI(t, awsCLI("lambda", "delete-function", "--function-name", name)) })
}

type cliEnvironmentCount struct {
	Invocations        int    `json:"invocations"`
	InitializationType string `json:"initializationType"`
}

func cliInvokeCount(t *testing.T, name string, qualifierArgs ...string) cliEnvironmentCount {
	t.Helper()
	payload := filepath.Join(t.TempDir(), "payload.json")
	require.NoError(t, os.WriteFile(payload, []byte(`{"action":"count"}`), 0o644))
	output := filepath.Join(t.TempDir(), "output.json")
	args := append([]string{"lambda", "invoke", "--function-name", name,
		"--payload", "fileb://" + payload, "--cli-binary-format", "raw-in-base64-out"}, qualifierArgs...)
	args = append(args, output, "--output", "json")
	runCLI(t, awsCLI(args...))
	body, err := os.ReadFile(output)
	require.NoError(t, err)
	var count cliEnvironmentCount
	parseJSON(t, string(body), &count)
	return count
}

func TestLambda_ExecutionEnvironmentIsReused_CLI(t *testing.T) {
	t.Parallel()
	const name = "cli-env-reuse-fn"
	cliCreateCountingFunction(t, name)
	assert.Equal(t, 1, cliInvokeCount(t, name).Invocations)
	second := cliInvokeCount(t, name)
	assert.Equal(t, 2, second.Invocations, "the second invocation runs in the environment the first initialised")
	assert.Equal(t, "on-demand", second.InitializationType)
}

func TestLambda_ReservedConcurrencyThrottles_CLI(t *testing.T) {
	t.Parallel()
	const name = "cli-env-throttle-fn"
	cliCreateCountingFunction(t, name)

	var reserved struct {
		ReservedConcurrentExecutions int `json:"ReservedConcurrentExecutions"`
	}
	parseJSON(t, runCLI(t, awsCLI("lambda", "put-function-concurrency",
		"--function-name", name, "--reserved-concurrent-executions", "0", "--output", "json")), &reserved)
	assert.Equal(t, 0, reserved.ReservedConcurrentExecutions)

	payload := filepath.Join(t.TempDir(), "payload.json")
	require.NoError(t, os.WriteFile(payload, []byte(`{"action":"count"}`), 0o644))
	throttled := awsCLI("lambda", "invoke", "--function-name", name,
		"--payload", "fileb://"+payload, "--cli-binary-format", "raw-in-base64-out", filepath.Join(t.TempDir(), "out.json"))
	// The CLI retries a throttle on its own; one attempt reports the first.
	throttled.Env = append(throttled.Env, "AWS_MAX_ATTEMPTS=1")
	out := runCLIExpectError(t, throttled)
	assert.Contains(t, out, "TooManyRequestsException", "reserved concurrency of 0 throttles every invocation")

	runCLI(t, awsCLI("lambda", "delete-function-concurrency", "--function-name", name))
	assert.Equal(t, 1, cliInvokeCount(t, name).Invocations)
}

func TestLambda_ProvisionedConcurrency_CLI(t *testing.T) {
	t.Parallel()
	const name = "cli-env-provisioned-fn"
	cliCreateCountingFunction(t, name, "--publish")
	version := strings.TrimSpace(runCLI(t, awsCLI("lambda", "list-versions-by-function",
		"--function-name", name, "--query", "Versions[?Version!='$LATEST'].Version | [0]", "--output", "text")))
	require.NotEmpty(t, version)

	var put struct {
		Requested int    `json:"RequestedProvisionedConcurrentExecutions"`
		Status    string `json:"Status"`
	}
	parseJSON(t, runCLI(t, awsCLI("lambda", "put-provisioned-concurrency-config",
		"--function-name", name, "--qualifier", version, "--provisioned-concurrent-executions", "1", "--output", "json")), &put)
	assert.Equal(t, 1, put.Requested)
	assert.Equal(t, "IN_PROGRESS", put.Status)

	// The API offers nothing to wait on, so poll the allocation's documented
	// Status until READY.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		status := strings.TrimSpace(runCLI(t, awsCLI("lambda", "get-provisioned-concurrency-config",
			"--function-name", name, "--qualifier", version, "--query", "Status", "--output", "text")))
		if status == "READY" {
			break
		}
		require.NotEqual(t, "FAILED", status)
		require.True(t, time.Now().Before(deadline), "provisioned concurrency never became READY")
		<-time.After(time.Second)
	}

	count := cliInvokeCount(t, name, "--qualifier", version)
	assert.Equal(t, 1, count.Invocations)
	assert.Equal(t, "provisioned-concurrency", count.InitializationType)

	runCLI(t, awsCLI("lambda", "delete-provisioned-concurrency-config", "--function-name", name, "--qualifier", version))
}
