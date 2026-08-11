package aws_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sfnSyncEndpoint returns the simulator's Step Functions endpoint coordinate.
// It has the same shape as the real endpoint a CLI resolves
// (`states.us-east-1.amazonaws.com`), so botocore prepends the `sync-` host
// prefix that StartSyncExecution and TestState carry and sends the request to
// `sync-states.localhost`. Callers pair it with awsCLIHostPrefixed, which is
// what makes that name reach the loopback sim.
func sfnSyncEndpoint(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("http://states.localhost:%s", portFromBaseURL(t))
}

// portFromBaseURL extracts the port from the suite baseURL (http://127.0.0.1:PORT).
func portFromBaseURL(t *testing.T) string {
	t.Helper()
	i := strings.LastIndex(baseURL, ":")
	require.GreaterOrEqual(t, i, 0)
	return baseURL[i+1:]
}

// TestSFNCLI_Activities covers create-activity / describe-activity /
// list-activities / get-activity-task / send-task-success /
// send-task-failure / send-task-heartbeat / delete-activity.
func TestSFNCLI_Activities(t *testing.T) {
	out := runCLI(t, awsCLI("stepfunctions", "create-activity", "--name", "sfn-cli-activity"))
	var created struct {
		ActivityArn string `json:"activityArn"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.ActivityArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-activity", "--activity-arn", created.ActivityArn))
	})

	out = runCLI(t, awsCLI("stepfunctions", "describe-activity", "--activity-arn", created.ActivityArn))
	var desc struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &desc)
	assert.Equal(t, "sfn-cli-activity", desc.Name)

	out = runCLI(t, awsCLI("stepfunctions", "list-activities"))
	var list struct {
		Activities []struct {
			Name string `json:"name"`
		} `json:"activities"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, a := range list.Activities {
		if a.Name == "sfn-cli-activity" {
			found = true
		}
	}
	assert.True(t, found)

	runCLI(t, awsCLI("stepfunctions", "tag-resource",
		"--resource-arn", created.ActivityArn, "--tags", "key=worker,value=cli"))
	out = runCLI(t, awsCLI("stepfunctions", "list-tags-for-resource", "--resource-arn", created.ActivityArn))
	var tags struct {
		Tags []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"tags"`
	}
	parseJSON(t, out, &tags)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "worker", tags.Tags[0].Key)

	definition := fmt.Sprintf(`{"StartAt":"Work","States":{"Work":{"Type":"Task","Resource":%q,"End":true}}}`, created.ActivityArn)
	out = runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-activity-sm", "--definition", definition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role"))
	var machine struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &machine)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", machine.StateMachineArn))
	})
	out = runCLI(t, awsCLI("stepfunctions", "start-execution",
		"--state-machine-arn", machine.StateMachineArn, "--input", `{"job":"real"}`))
	var execution struct {
		ExecutionArn string `json:"executionArn"`
	}
	parseJSON(t, out, &execution)

	out = runCLI(t, awsCLI("stepfunctions", "get-activity-task",
		"--activity-arn", created.ActivityArn, "--worker-name", "w1"))
	var task struct {
		TaskToken string `json:"taskToken"`
		Input     string `json:"input"`
	}
	parseJSON(t, out, &task)
	require.NotEmpty(t, task.TaskToken)
	assert.JSONEq(t, `{"job":"real"}`, task.Input)
	runCLI(t, awsCLI("stepfunctions", "send-task-heartbeat", "--task-token", task.TaskToken))
	runCLI(t, awsCLI("stepfunctions", "send-task-success",
		"--cli-input-json", fmt.Sprintf(`{"taskToken":%q,"output":"{\"completed\":true}"}`, task.TaskToken)))
	require.Eventually(t, func() bool {
		result := runCLI(t, awsCLI("stepfunctions", "describe-execution", "--execution-arn", execution.ExecutionArn))
		var described struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}
		parseJSON(t, result, &described)
		return described.Status == "SUCCEEDED" && described.Output == `{"completed":true}`
	}, 10*time.Second, 100*time.Millisecond)

	out = runCLI(t, awsCLI("stepfunctions", "start-execution",
		"--state-machine-arn", machine.StateMachineArn, "--input", `{"job":"fail"}`))
	var failedExecution struct {
		ExecutionArn string `json:"executionArn"`
	}
	parseJSON(t, out, &failedExecution)
	out = runCLI(t, awsCLI("stepfunctions", "get-activity-task",
		"--activity-arn", created.ActivityArn, "--worker-name", "w1"))
	var failedTask struct {
		TaskToken string `json:"taskToken"`
	}
	parseJSON(t, out, &failedTask)
	runCLI(t, awsCLI("stepfunctions", "send-task-failure",
		"--task-token", failedTask.TaskToken, "--error", "Worker.Failed", "--cause", "real worker failure"))
	require.Eventually(t, func() bool {
		result := runCLI(t, awsCLI("stepfunctions", "describe-execution", "--execution-arn", failedExecution.ExecutionArn))
		var described struct {
			Status string `json:"status"`
		}
		parseJSON(t, result, &described)
		return described.Status == "FAILED"
	}, 10*time.Second, 100*time.Millisecond)
}

// TestSFNCLI_VersionsAndAliases covers publish-state-machine-version,
// list-state-machine-versions,
// create-state-machine-alias, describe-state-machine-alias,
// list-state-machine-aliases, update-state-machine-alias,
// delete-state-machine-alias, delete-state-machine-version.
func TestSFNCLI_VersionsAndAliases(t *testing.T) {
	definition := `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
	out := runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-ver-sm",
		"--definition", definition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role"))
	var sm struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &sm)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", sm.StateMachineArn))
	})

	out = runCLI(t, awsCLI("stepfunctions", "publish-state-machine-version",
		"--state-machine-arn", sm.StateMachineArn, "--description", "v1"))
	var pub struct {
		StateMachineVersionArn string `json:"stateMachineVersionArn"`
	}
	parseJSON(t, out, &pub)
	require.NotEmpty(t, pub.StateMachineVersionArn)

	out = runCLI(t, awsCLI("stepfunctions", "list-state-machine-versions",
		"--state-machine-arn", sm.StateMachineArn))
	var versions struct {
		StateMachineVersions []struct {
			StateMachineVersionArn string `json:"stateMachineVersionArn"`
		} `json:"stateMachineVersions"`
	}
	parseJSON(t, out, &versions)
	require.Len(t, versions.StateMachineVersions, 1)
	assert.Equal(t, pub.StateMachineVersionArn, versions.StateMachineVersions[0].StateMachineVersionArn)

	out = runCLI(t, awsCLI("stepfunctions", "create-state-machine-alias",
		"--name", "PROD",
		"--routing-configuration", "stateMachineVersionArn="+pub.StateMachineVersionArn+",weight=100"))
	var alias struct {
		StateMachineAliasArn string `json:"stateMachineAliasArn"`
	}
	parseJSON(t, out, &alias)
	require.NotEmpty(t, alias.StateMachineAliasArn)
	aliasDeleted := false
	t.Cleanup(func() {
		if !aliasDeleted {
			runCLI(t, awsCLI("stepfunctions", "delete-state-machine-alias",
				"--state-machine-alias-arn", alias.StateMachineAliasArn))
		}
	})

	out = runCLI(t, awsCLI("stepfunctions", "describe-state-machine-alias",
		"--state-machine-alias-arn", alias.StateMachineAliasArn))
	var da struct {
		Name                 string `json:"name"`
		RoutingConfiguration []struct {
			StateMachineVersionArn string `json:"stateMachineVersionArn"`
			Weight                 int    `json:"weight"`
		} `json:"routingConfiguration"`
	}
	parseJSON(t, out, &da)
	assert.Equal(t, "PROD", da.Name)
	require.Len(t, da.RoutingConfiguration, 1)
	assert.Equal(t, 100, da.RoutingConfiguration[0].Weight)

	out = runCLI(t, awsCLI("stepfunctions", "list-state-machine-aliases",
		"--state-machine-arn", sm.StateMachineArn))
	var la struct {
		StateMachineAliases []struct {
			StateMachineAliasArn string `json:"stateMachineAliasArn"`
		} `json:"stateMachineAliases"`
	}
	parseJSON(t, out, &la)
	require.Len(t, la.StateMachineAliases, 1)

	runCLI(t, awsCLI("stepfunctions", "update-state-machine-alias",
		"--state-machine-alias-arn", alias.StateMachineAliasArn, "--description", "updated"))

	deleteError := runCLIExpectError(t, awsCLI("stepfunctions", "delete-state-machine-version",
		"--state-machine-version-arn", pub.StateMachineVersionArn))
	assert.Contains(t, deleteError, "ConflictException")
	runCLI(t, awsCLI("stepfunctions", "delete-state-machine-alias",
		"--state-machine-alias-arn", alias.StateMachineAliasArn))
	aliasDeleted = true
	runCLI(t, awsCLI("stepfunctions", "delete-state-machine-version",
		"--state-machine-version-arn", pub.StateMachineVersionArn))
}

// TestSFNCLI_TestState runs a single Pass state synchronously. test-state
// carries a `sync-` host prefix, so it targets the sync endpoint.
func TestSFNCLI_TestState(t *testing.T) {
	endpoint := sfnSyncEndpoint(t)
	out := runCLI(t, awsCLIHostPrefixed("stepfunctions", "test-state",
		"--definition", `{"Type":"Pass","Result":{"hello":"world"},"End":true}`,
		"--input", `{"x":1}`,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role",
		"--endpoint-url", endpoint))
	var res struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	parseJSON(t, out, &res)
	assert.Equal(t, "SUCCEEDED", res.Status)
	assert.JSONEq(t, `{"hello":"world"}`, res.Output)
}

// TestSFNCLI_StartSyncExecution runs a whole EXPRESS state machine
// synchronously. start-sync-execution carries a `sync-` host prefix, so it
// targets the sync endpoint.
func TestSFNCLI_StartSyncExecution(t *testing.T) {
	endpoint := sfnSyncEndpoint(t)
	definition := `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"ok":true},"End":true}}}`
	out := runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-sync-sm",
		"--definition", definition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role",
		"--type", "EXPRESS"))
	var sm struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &sm)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", sm.StateMachineArn))
	})

	out = runCLI(t, awsCLIHostPrefixed("stepfunctions", "start-sync-execution",
		"--state-machine-arn", sm.StateMachineArn, "--input", "{}",
		"--endpoint-url", endpoint))
	var res struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	parseJSON(t, out, &res)
	assert.Equal(t, "SUCCEEDED", res.Status)
	assert.JSONEq(t, `{"ok":true}`, res.Output)
}

// TestSFNCLI_NestedWorkflowIntegration exercises the optimized
// states:startExecution.sync:2 Task resource with state machines created,
// started, described, and listed exclusively through the AWS CLI.
func TestSFNCLI_NestedWorkflowIntegration(t *testing.T) {
	childDefinition := `{"StartAt":"Complete","States":{"Complete":{"Type":"Pass","Parameters":{"child.$":"$"},"End":true}}}`
	out := runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-nested-child",
		"--definition", childDefinition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role"))
	var child struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &child)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", child.StateMachineArn))
	})

	parentDefinition := fmt.Sprintf(
		`{"StartAt":"Child","States":{"Child":{"Type":"Task","Resource":"arn:aws:states:::states:startExecution.sync:2","Parameters":{"StateMachineArn":%q,"Input":{"order":"A-1"}},"OutputPath":"$.Output","End":true}}}`,
		child.StateMachineArn,
	)
	out = runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-nested-parent",
		"--definition", parentDefinition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role"))
	var parent struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &parent)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", parent.StateMachineArn))
	})

	out = runCLI(t, awsCLI("stepfunctions", "start-execution", "--state-machine-arn", parent.StateMachineArn))
	var started struct {
		ExecutionArn string `json:"executionArn"`
	}
	parseJSON(t, out, &started)
	require.Eventually(t, func() bool {
		describedJSON := runCLI(t, awsCLI("stepfunctions", "describe-execution", "--execution-arn", started.ExecutionArn))
		var described struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}
		parseJSON(t, describedJSON, &described)
		return described.Status == "SUCCEEDED" && described.Output == `{"child":{"order":"A-1"}}`
	}, 10*time.Second, 100*time.Millisecond)
	out = runCLI(t, awsCLI("stepfunctions", "list-executions", "--state-machine-arn", child.StateMachineArn))
	var listed struct {
		Executions []struct {
			Status string `json:"status"`
		} `json:"executions"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed.Executions, 1)
	assert.Equal(t, "SUCCEEDED", listed.Executions[0].Status)
}

// TestSFNCLI_DescribeForExecutionAndRedrive covers
// describe-state-machine-for-execution and redrive-execution, plus
// list-map-runs (empty).
func TestSFNCLI_DescribeForExecutionAndRedrive(t *testing.T) {
	definition := `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E","Cause":"boom"}}}`
	out := runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-redrive-sm",
		"--definition", definition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role"))
	var sm struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &sm)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", sm.StateMachineArn))
	})

	out = runCLI(t, awsCLI("stepfunctions", "start-execution",
		"--state-machine-arn", sm.StateMachineArn, "--input", "{}"))
	var started struct {
		ExecutionArn string `json:"executionArn"`
	}
	parseJSON(t, out, &started)

	out = runCLI(t, awsCLI("stepfunctions", "describe-state-machine-for-execution",
		"--execution-arn", started.ExecutionArn))
	var dfe struct {
		Name       string `json:"name"`
		Definition string `json:"definition"`
	}
	parseJSON(t, out, &dfe)
	assert.Equal(t, "sfn-cli-redrive-sm", dfe.Name)
	assert.Contains(t, dfe.Definition, "Fail")

	var exec struct {
		Status string `json:"status"`
	}
	require.Eventually(t, func() bool {
		o := runCLI(t, awsCLI("stepfunctions", "describe-execution", "--execution-arn", started.ExecutionArn))
		parseJSON(t, o, &exec)
		return exec.Status == "FAILED"
	}, 10*time.Second, 100*time.Millisecond)

	out = runCLI(t, awsCLI("stepfunctions", "redrive-execution", "--execution-arn", started.ExecutionArn))
	// The CLI renders the epoch redriveDate as an ISO-8601 timestamp string.
	var rd struct {
		RedriveDate string `json:"redriveDate"`
	}
	parseJSON(t, out, &rd)
	assert.NotEmpty(t, rd.RedriveDate)

	out = runCLI(t, awsCLI("stepfunctions", "list-map-runs", "--execution-arn", started.ExecutionArn))
	var lmr struct {
		MapRuns []any `json:"mapRuns"`
	}
	parseJSON(t, out, &lmr)
	assert.Empty(t, lmr.MapRuns)
}

// TestSFNCLI_DistributedMapRun drives a real Distributed Map through the
// vendor CLI, then observes and updates its Map Run while child workflows run.
func TestSFNCLI_DistributedMapRun(t *testing.T) {
	definition := `{
		"StartAt":"Distributed",
		"States":{"Distributed":{
			"Type":"Map",
			"MaxConcurrency":1,
			"ItemProcessor":{
				"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"EXPRESS"},
				"StartAt":"Delay",
				"States":{"Delay":{"Type":"Wait","Seconds":1,"End":true}}
			},
			"End":true
		}}
	}`
	out := runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", "sfn-cli-maprun-sm", "--definition", definition,
		"--role-arn", "arn:aws:iam::123456789012:role/sfn-role"))
	var machine struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, out, &machine)
	t.Cleanup(func() {
		runCLI(t, awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", machine.StateMachineArn))
	})
	out = runCLI(t, awsCLI("stepfunctions", "start-execution",
		"--state-machine-arn", machine.StateMachineArn, "--input", `[{"id":1},{"id":2}]`))
	var execution struct {
		ExecutionArn string `json:"executionArn"`
	}
	parseJSON(t, out, &execution)

	var mapRunArn string
	require.Eventually(t, func() bool {
		listed := runCLI(t, awsCLI("stepfunctions", "list-map-runs", "--execution-arn", execution.ExecutionArn))
		var response struct {
			MapRuns []struct {
				MapRunArn string `json:"mapRunArn"`
			} `json:"mapRuns"`
		}
		parseJSON(t, listed, &response)
		if len(response.MapRuns) != 1 {
			return false
		}
		mapRunArn = response.MapRuns[0].MapRunArn
		return mapRunArn != ""
	}, 10*time.Second, 100*time.Millisecond)

	runCLI(t, awsCLI("stepfunctions", "update-map-run",
		"--map-run-arn", mapRunArn, "--max-concurrency", "2"))
	out = runCLI(t, awsCLI("stepfunctions", "describe-map-run", "--map-run-arn", mapRunArn))
	var described struct {
		MaxConcurrency int `json:"maxConcurrency"`
		ItemCounts     struct {
			Total int `json:"total"`
		} `json:"itemCounts"`
	}
	parseJSON(t, out, &described)
	assert.Equal(t, 2, described.MaxConcurrency)
	assert.Equal(t, 2, described.ItemCounts.Total)
	require.Eventually(t, func() bool {
		result := runCLI(t, awsCLI("stepfunctions", "describe-map-run", "--map-run-arn", mapRunArn))
		var terminal struct {
			Status     string `json:"status"`
			ItemCounts struct {
				Succeeded      int `json:"succeeded"`
				ResultsWritten int `json:"resultsWritten"`
			} `json:"itemCounts"`
		}
		parseJSON(t, result, &terminal)
		return terminal.Status == "SUCCEEDED" &&
			terminal.ItemCounts.Succeeded == 2 &&
			terminal.ItemCounts.ResultsWritten == 2
	}, 10*time.Second, 100*time.Millisecond)
	out = runCLI(t, awsCLI("stepfunctions", "list-executions", "--map-run-arn", mapRunArn))
	var children struct {
		Executions []struct {
			Status          string `json:"status"`
			MapRunArn       string `json:"mapRunArn"`
			StateMachineArn string `json:"stateMachineArn"`
		} `json:"executions"`
	}
	parseJSON(t, out, &children)
	require.Len(t, children.Executions, 2)
	for _, child := range children.Executions {
		assert.Equal(t, "SUCCEEDED", child.Status)
		assert.Equal(t, mapRunArn, child.MapRunArn)
		assert.Contains(t, child.StateMachineArn, "/Distributed")
	}
}
