package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueSessionAndStatementsCLI exercises the Interactive Sessions +
// Statements families via the aws CLI: create-session -> get-session ->
// list-sessions -> get-session-endpoint -> run-statement -> get-statement ->
// list-statements -> cancel-statement -> stop-session -> delete-session.
func TestGlueSessionAndStatementsCLI(t *testing.T) {
	id := "glue-cli-session"
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-session", "--id", id))
	})

	out := runCLI(t, awsCLI("glue", "create-session",
		"--id", id,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
		"--description", "cli session",
		"--command", `{"Name":"glueetl","PythonVersion":"3"}`,
		"--glue-version", "3.0",
	))
	var created struct {
		Session struct {
			Id     string `json:"Id"`
			Status string `json:"Status"`
		} `json:"Session"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, id, created.Session.Id)
	assert.Equal(t, "READY", created.Session.Status)

	out = runCLI(t, awsCLI("glue", "get-session", "--id", id))
	var got struct {
		Session struct {
			Description string `json:"Description"`
			Status      string `json:"Status"`
		} `json:"Session"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, "cli session", got.Session.Description)
	assert.Equal(t, "READY", got.Session.Status)

	out = runCLI(t, awsCLI("glue", "list-sessions"))
	var listed struct {
		Ids []string `json:"Ids"`
	}
	parseJSON(t, out, &listed)
	assert.Contains(t, listed.Ids, id)

	out = runCLI(t, awsCLI("glue", "run-statement", "--session-id", id, "--code", "print('hi')"))
	var run struct {
		Id int `json:"Id"`
	}
	parseJSON(t, out, &run)
	assert.Equal(t, 1, run.Id)

	out = runCLI(t, awsCLI("glue", "get-statement", "--session-id", id, "--id", "1"))
	var stmt struct {
		Statement struct {
			Id    int    `json:"Id"`
			State string `json:"State"`
		} `json:"Statement"`
	}
	parseJSON(t, out, &stmt)
	assert.Equal(t, 1, stmt.Statement.Id)
	assert.Equal(t, "AVAILABLE", stmt.Statement.State)

	out = runCLI(t, awsCLI("glue", "list-statements", "--session-id", id))
	var stmts struct {
		Statements []struct {
			Id int `json:"Id"`
		} `json:"Statements"`
	}
	parseJSON(t, out, &stmts)
	require.Len(t, stmts.Statements, 1)

	runCLIIgnore(awsCLI("glue", "cancel-statement", "--session-id", id, "--id", "1"))
	out = runCLI(t, awsCLI("glue", "get-statement", "--session-id", id, "--id", "1"))
	parseJSON(t, out, &stmt)
	assert.Equal(t, "CANCELLED", stmt.Statement.State)

	out = runCLI(t, awsCLI("glue", "stop-session", "--id", id))
	var stop struct {
		Id string `json:"Id"`
	}
	parseJSON(t, out, &stop)
	assert.Equal(t, id, stop.Id)

	out = runCLI(t, awsCLI("glue", "delete-session", "--id", id))
	var del struct {
		Id string `json:"Id"`
	}
	parseJSON(t, out, &del)
	assert.Equal(t, id, del.Id)
}

// TestGlueDevEndpointCLI exercises the Dev Endpoints family via the aws CLI:
// create-dev-endpoint -> get-dev-endpoint -> get-dev-endpoints ->
// batch-get-dev-endpoints -> list-dev-endpoints -> update-dev-endpoint ->
// delete-dev-endpoint.
func TestGlueDevEndpointCLI(t *testing.T) {
	name := "glue-cli-devep"
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-dev-endpoint", "--endpoint-name", name))
	})

	out := runCLI(t, awsCLI("glue", "create-dev-endpoint",
		"--endpoint-name", name,
		"--role-arn", "arn:aws:iam::123456789012:role/GlueRole",
		"--glue-version", "3.0",
		"--arguments", `{"k":"v"}`,
	))
	var created struct {
		EndpointName string `json:"EndpointName"`
		Status       string `json:"Status"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, name, created.EndpointName)
	assert.Equal(t, "READY", created.Status)

	out = runCLI(t, awsCLI("glue", "get-dev-endpoint", "--endpoint-name", name))
	var got struct {
		DevEndpoint struct {
			Status    string            `json:"Status"`
			Arguments map[string]string `json:"Arguments"`
		} `json:"DevEndpoint"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, "READY", got.DevEndpoint.Status)
	assert.Equal(t, "v", got.DevEndpoint.Arguments["k"])

	out = runCLI(t, awsCLI("glue", "get-dev-endpoints"))
	var gets struct {
		DevEndpoints []struct {
			EndpointName string `json:"EndpointName"`
		} `json:"DevEndpoints"`
	}
	parseJSON(t, out, &gets)
	foundDE := false
	for _, de := range gets.DevEndpoints {
		if de.EndpointName == name {
			foundDE = true
		}
	}
	assert.True(t, foundDE)

	out = runCLI(t, awsCLI("glue", "batch-get-dev-endpoints", "--dev-endpoint-names", name, "missing-endpoint"))
	var batch struct {
		DevEndpoints []struct {
			EndpointName string `json:"EndpointName"`
		} `json:"DevEndpoints"`
		DevEndpointsNotFound []string `json:"DevEndpointsNotFound"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.DevEndpoints, 1)
	assert.Contains(t, batch.DevEndpointsNotFound, "missing-endpoint")

	out = runCLI(t, awsCLI("glue", "list-dev-endpoints"))
	var listed struct {
		DevEndpointNames []string `json:"DevEndpointNames"`
	}
	parseJSON(t, out, &listed)
	assert.Contains(t, listed.DevEndpointNames, name)

	runCLIIgnore(awsCLI("glue", "update-dev-endpoint", "--endpoint-name", name,
		"--add-arguments", `{"k2":"v2"}`))
	out = runCLI(t, awsCLI("glue", "get-dev-endpoint", "--endpoint-name", name))
	parseJSON(t, out, &got)
	assert.Equal(t, "v2", got.DevEndpoint.Arguments["k2"])

	runCLIIgnore(awsCLI("glue", "delete-dev-endpoint", "--endpoint-name", name))
}

// TestGlueBlueprintCLI exercises the Blueprints family via the aws CLI:
// create-blueprint -> get-blueprint -> batch-get-blueprints ->
// list-blueprints -> update-blueprint -> start-blueprint-run ->
// get-blueprint-run -> get-blueprint-runs -> delete-blueprint.
func TestGlueBlueprintCLI(t *testing.T) {
	name := "glue-cli-blueprint"
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-blueprint", "--name", name))
	})

	out := runCLI(t, awsCLI("glue", "create-blueprint",
		"--name", name,
		"--description", "cli blueprint",
		"--blueprint-location", "s3://bucket/blueprint.zip",
	))
	var created struct {
		Name string `json:"Name"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, name, created.Name)

	out = runCLI(t, awsCLI("glue", "get-blueprint", "--name", name))
	var got struct {
		Blueprint struct {
			BlueprintLocation string `json:"BlueprintLocation"`
			Status            string `json:"Status"`
		} `json:"Blueprint"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, "s3://bucket/blueprint.zip", got.Blueprint.BlueprintLocation)
	assert.Equal(t, "ACTIVE", got.Blueprint.Status)

	out = runCLI(t, awsCLI("glue", "batch-get-blueprints", "--names", name, "missing-blueprint"))
	var batch struct {
		Blueprints []struct {
			Name string `json:"Name"`
		} `json:"Blueprints"`
		MissingBlueprints []string `json:"MissingBlueprints"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Blueprints, 1)
	assert.Contains(t, batch.MissingBlueprints, "missing-blueprint")

	out = runCLI(t, awsCLI("glue", "list-blueprints"))
	var listed struct {
		Blueprints []string `json:"Blueprints"`
	}
	parseJSON(t, out, &listed)
	assert.Contains(t, listed.Blueprints, name)

	runCLIIgnore(awsCLI("glue", "update-blueprint", "--name", name,
		"--blueprint-location", "s3://bucket/blueprint-v2.zip"))

	out = runCLI(t, awsCLI("glue", "start-blueprint-run",
		"--blueprint-name", name,
		"--role-arn", "arn:aws:iam::123456789012:role/GlueRole",
	))
	var run struct {
		RunId string `json:"RunId"`
	}
	parseJSON(t, out, &run)
	require.NotEmpty(t, run.RunId)

	out = runCLI(t, awsCLI("glue", "get-blueprint-run", "--blueprint-name", name, "--run-id", run.RunId))
	var getRun struct {
		BlueprintRun struct {
			State string `json:"State"`
		} `json:"BlueprintRun"`
	}
	parseJSON(t, out, &getRun)
	assert.Equal(t, "SUCCEEDED", getRun.BlueprintRun.State)

	out = runCLI(t, awsCLI("glue", "get-blueprint-runs", "--blueprint-name", name))
	var runs struct {
		BlueprintRuns []struct {
			RunId string `json:"RunId"`
		} `json:"BlueprintRuns"`
	}
	parseJSON(t, out, &runs)
	require.Len(t, runs.BlueprintRuns, 1)
	assert.Equal(t, run.RunId, runs.BlueprintRuns[0].RunId)

	runCLIIgnore(awsCLI("glue", "delete-blueprint", "--name", name))
}
