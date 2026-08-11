package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_SessionAndStatements_SDK exercises the Interactive Sessions +
// Statements families: CreateSession -> GetSession -> ListSessions ->
// GetSessionEndpoint -> RunStatement -> GetStatement -> ListStatements ->
// CancelStatement -> StopSession -> DeleteSession.
func TestGlue_SessionAndStatements_SDK(t *testing.T) {
	c := glueClient()
	id := "glue-sdk-session"

	t.Cleanup(func() {
		_, _ = c.DeleteSession(ctx, &glue.DeleteSessionInput{Id: aws.String(id)})
	})

	create, err := c.CreateSession(ctx, &glue.CreateSessionInput{
		Id:          aws.String(id),
		Role:        aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		Description: aws.String("sdk session"),
		Command: &gluetypes.SessionCommand{
			Name:          aws.String("glueetl"),
			PythonVersion: aws.String("3"),
		},
		GlueVersion: aws.String("3.0"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.Session)
	assert.Equal(t, id, aws.ToString(create.Session.Id))
	assert.Equal(t, gluetypes.SessionStatusReady, create.Session.Status)

	get, err := c.GetSession(ctx, &glue.GetSessionInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "sdk session", aws.ToString(get.Session.Description))
	assert.Equal(t, gluetypes.SessionStatusReady, get.Session.Status)

	list, err := c.ListSessions(ctx, &glue.ListSessionsInput{})
	require.NoError(t, err)
	assert.Contains(t, list.Ids, id)
	foundSess := false
	for _, s := range list.Sessions {
		if aws.ToString(s.Id) == id {
			foundSess = true
		}
	}
	assert.True(t, foundSess, "ListSessions should return the created session")

	ep, err := c.GetSessionEndpoint(ctx, &glue.GetSessionEndpointInput{SessionId: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, ep.SparkConnect)
	assert.NotEmpty(t, aws.ToString(ep.SparkConnect.Url))

	run, err := c.RunStatement(ctx, &glue.RunStatementInput{
		SessionId: aws.String(id),
		Code:      aws.String("print('hello')"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), run.Id)

	stmt, err := c.GetStatement(ctx, &glue.GetStatementInput{
		SessionId: aws.String(id),
		Id:        run.Id,
	})
	require.NoError(t, err)
	require.NotNil(t, stmt.Statement)
	assert.Equal(t, int32(1), stmt.Statement.Id)
	assert.Equal(t, gluetypes.StatementStateAvailable, stmt.Statement.State)
	require.NotNil(t, stmt.Statement.Output)
	assert.Equal(t, gluetypes.StatementStateAvailable, stmt.Statement.Output.Status)

	stmts, err := c.ListStatements(ctx, &glue.ListStatementsInput{SessionId: aws.String(id)})
	require.NoError(t, err)
	require.Len(t, stmts.Statements, 1)
	assert.Equal(t, int32(1), stmts.Statements[0].Id)

	_, err = c.CancelStatement(ctx, &glue.CancelStatementInput{
		SessionId: aws.String(id),
		Id:        run.Id,
	})
	require.NoError(t, err)
	stmt2, err := c.GetStatement(ctx, &glue.GetStatementInput{SessionId: aws.String(id), Id: run.Id})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.StatementStateCancelled, stmt2.Statement.State)

	stop, err := c.StopSession(ctx, &glue.StopSessionInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(stop.Id))

	del, err := c.DeleteSession(ctx, &glue.DeleteSessionInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(del.Id))

	_, err = c.GetSession(ctx, &glue.GetSessionInput{Id: aws.String(id)})
	assert.Error(t, err, "GetSession after delete should error")
}

// TestGlue_DevEndpointLifecycle_SDK exercises the Dev Endpoints family:
// CreateDevEndpoint -> GetDevEndpoint -> GetDevEndpoints ->
// BatchGetDevEndpoints -> ListDevEndpoints -> UpdateDevEndpoint ->
// DeleteDevEndpoint.
func TestGlue_DevEndpointLifecycle_SDK(t *testing.T) {
	c := glueClient()
	name := "glue-sdk-devep"

	t.Cleanup(func() {
		_, _ = c.DeleteDevEndpoint(ctx, &glue.DeleteDevEndpointInput{EndpointName: aws.String(name)})
	})

	create, err := c.CreateDevEndpoint(ctx, &glue.CreateDevEndpointInput{
		EndpointName: aws.String(name),
		RoleArn:      aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		GlueVersion:  aws.String("3.0"),
		WorkerType:   gluetypes.WorkerTypeG1x,
		Arguments:    map[string]string{"k": "v"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(create.EndpointName))
	assert.Equal(t, "READY", aws.ToString(create.Status))

	get, err := c.GetDevEndpoint(ctx, &glue.GetDevEndpointInput{EndpointName: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, get.DevEndpoint)
	assert.Equal(t, "READY", aws.ToString(get.DevEndpoint.Status))
	assert.Equal(t, "v", get.DevEndpoint.Arguments["k"])

	gets, err := c.GetDevEndpoints(ctx, &glue.GetDevEndpointsInput{})
	require.NoError(t, err)
	foundDE := false
	for _, de := range gets.DevEndpoints {
		if aws.ToString(de.EndpointName) == name {
			foundDE = true
		}
	}
	assert.True(t, foundDE, "GetDevEndpoints should return created endpoint")

	batch, err := c.BatchGetDevEndpoints(ctx, &glue.BatchGetDevEndpointsInput{
		DevEndpointNames: []string{name, "missing-endpoint"},
	})
	require.NoError(t, err)
	require.Len(t, batch.DevEndpoints, 1)
	assert.Equal(t, name, aws.ToString(batch.DevEndpoints[0].EndpointName))
	assert.Contains(t, batch.DevEndpointsNotFound, "missing-endpoint")

	list, err := c.ListDevEndpoints(ctx, &glue.ListDevEndpointsInput{})
	require.NoError(t, err)
	assert.Contains(t, list.DevEndpointNames, name)

	_, err = c.UpdateDevEndpoint(ctx, &glue.UpdateDevEndpointInput{
		EndpointName: aws.String(name),
		AddArguments: map[string]string{"k2": "v2"},
	})
	require.NoError(t, err)
	get2, err := c.GetDevEndpoint(ctx, &glue.GetDevEndpointInput{EndpointName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "v2", get2.DevEndpoint.Arguments["k2"])

	_, err = c.DeleteDevEndpoint(ctx, &glue.DeleteDevEndpointInput{EndpointName: aws.String(name)})
	require.NoError(t, err)
	_, err = c.GetDevEndpoint(ctx, &glue.GetDevEndpointInput{EndpointName: aws.String(name)})
	assert.Error(t, err, "GetDevEndpoint after delete should error")
}

// TestGlue_BlueprintLifecycle_SDK exercises the Blueprints family:
// CreateBlueprint -> GetBlueprint -> BatchGetBlueprints -> ListBlueprints ->
// UpdateBlueprint -> StartBlueprintRun -> GetBlueprintRun -> GetBlueprintRuns
// -> DeleteBlueprint.
func TestGlue_BlueprintLifecycle_SDK(t *testing.T) {
	c := glueClient()
	name := "glue-sdk-blueprint"

	t.Cleanup(func() {
		_, _ = c.DeleteBlueprint(ctx, &glue.DeleteBlueprintInput{Name: aws.String(name)})
	})

	create, err := c.CreateBlueprint(ctx, &glue.CreateBlueprintInput{
		Name:              aws.String(name),
		Description:       aws.String("sdk blueprint"),
		BlueprintLocation: aws.String("s3://bucket/blueprint.zip"),
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(create.Name))

	get, err := c.GetBlueprint(ctx, &glue.GetBlueprintInput{Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, get.Blueprint)
	assert.Equal(t, "s3://bucket/blueprint.zip", aws.ToString(get.Blueprint.BlueprintLocation))
	assert.Equal(t, gluetypes.BlueprintStatusActive, get.Blueprint.Status)

	batch, err := c.BatchGetBlueprints(ctx, &glue.BatchGetBlueprintsInput{
		Names: []string{name, "missing-blueprint"},
	})
	require.NoError(t, err)
	require.Len(t, batch.Blueprints, 1)
	assert.Equal(t, name, aws.ToString(batch.Blueprints[0].Name))
	assert.Contains(t, batch.MissingBlueprints, "missing-blueprint")

	list, err := c.ListBlueprints(ctx, &glue.ListBlueprintsInput{})
	require.NoError(t, err)
	assert.Contains(t, list.Blueprints, name)

	upd, err := c.UpdateBlueprint(ctx, &glue.UpdateBlueprintInput{
		Name:              aws.String(name),
		BlueprintLocation: aws.String("s3://bucket/blueprint-v2.zip"),
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(upd.Name))

	run, err := c.StartBlueprintRun(ctx, &glue.StartBlueprintRunInput{
		BlueprintName: aws.String(name),
		RoleArn:       aws.String("arn:aws:iam::123456789012:role/GlueRole"),
	})
	require.NoError(t, err)
	runID := aws.ToString(run.RunId)
	require.NotEmpty(t, runID)

	getRun, err := c.GetBlueprintRun(ctx, &glue.GetBlueprintRunInput{
		BlueprintName: aws.String(name),
		RunId:         aws.String(runID),
	})
	require.NoError(t, err)
	require.NotNil(t, getRun.BlueprintRun)
	assert.Equal(t, gluetypes.BlueprintRunStateSucceeded, getRun.BlueprintRun.State)

	runs, err := c.GetBlueprintRuns(ctx, &glue.GetBlueprintRunsInput{BlueprintName: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, runs.BlueprintRuns, 1)
	assert.Equal(t, runID, aws.ToString(runs.BlueprintRuns[0].RunId))

	_, err = c.DeleteBlueprint(ctx, &glue.DeleteBlueprintInput{Name: aws.String(name)})
	require.NoError(t, err)
	_, err = c.GetBlueprint(ctx, &glue.GetBlueprintInput{Name: aws.String(name)})
	assert.Error(t, err, "GetBlueprint after delete should error")
}
