package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSM_OpsItemLifecycle pins the OpsCenter slice: create an OpsItem, read it
// back, list it via DescribeOpsItems, update its status to Resolved, associate
// and list a related item, list the activity events, then disassociate.
func TestSSM_OpsItemLifecycle(t *testing.T) {
	c := ssmClient()

	created, err := c.CreateOpsItem(ctx, &ssm.CreateOpsItemInput{
		Title:       aws.String("Disk full on host"),
		Description: aws.String("The root volume is at 95% utilization."),
		Source:      aws.String("EC2"),
		Priority:    aws.Int32(2),
		Category:    aws.String("Performance"),
		Severity:    aws.String("3"),
		OperationalData: map[string]ssmtypes.OpsItemDataValue{
			"/aws/resources": {Value: aws.String(`[{"arn":"arn:aws:ec2:us-east-1:123456789012:instance/i-abc"}]`), Type: ssmtypes.OpsItemDataTypeSearchableString},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created.OpsItemId)
	id := *created.OpsItemId
	assert.Regexp(t, `^oi-[0-9a-f]{12}$`, id)
	assert.NotEmpty(t, *created.OpsItemArn)

	// Get it back.
	got, err := c.GetOpsItem(ctx, &ssm.GetOpsItemInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, got.OpsItem)
	assert.Equal(t, "Disk full on host", *got.OpsItem.Title)
	assert.Equal(t, ssmtypes.OpsItemStatusOpen, got.OpsItem.Status)
	assert.Equal(t, int32(2), *got.OpsItem.Priority)
	assert.Equal(t, "1", *got.OpsItem.Version)
	require.Contains(t, got.OpsItem.OperationalData, "/aws/resources")

	// DescribeOpsItems includes it.
	desc, err := c.DescribeOpsItems(ctx, &ssm.DescribeOpsItemsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range desc.OpsItemSummaries {
		if s.OpsItemId != nil && *s.OpsItemId == id {
			found = true
			assert.Equal(t, ssmtypes.OpsItemStatusOpen, s.Status)
		}
	}
	assert.True(t, found, "DescribeOpsItems must include created item")

	// Update to Resolved.
	_, err = c.UpdateOpsItem(ctx, &ssm.UpdateOpsItemInput{
		OpsItemId: aws.String(id),
		Status:    ssmtypes.OpsItemStatusResolved,
		Priority:  aws.Int32(1),
	})
	require.NoError(t, err)
	got2, err := c.GetOpsItem(ctx, &ssm.GetOpsItemInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.OpsItemStatusResolved, got2.OpsItem.Status)
	assert.Equal(t, "2", *got2.OpsItem.Version)

	// Associate a related item.
	assoc, err := c.AssociateOpsItemRelatedItem(ctx, &ssm.AssociateOpsItemRelatedItemInput{
		OpsItemId:       aws.String(id),
		AssociationType: aws.String("RelatesTo"),
		ResourceType:    aws.String("AWS::SSMIncidents::IncidentRecord"),
		ResourceUri:     aws.String("arn:aws:ssm-incidents::123456789012:incident-record/x/y"),
	})
	require.NoError(t, err)
	assocID := *assoc.AssociationId
	assert.NotEmpty(t, assocID)

	rel, err := c.ListOpsItemRelatedItems(ctx, &ssm.ListOpsItemRelatedItemsInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	require.Len(t, rel.Summaries, 1)
	assert.Equal(t, assocID, *rel.Summaries[0].AssociationId)
	assert.Equal(t, "RelatesTo", *rel.Summaries[0].AssociationType)

	// Activity events (create + update were recorded).
	evs, err := c.ListOpsItemEvents(ctx, &ssm.ListOpsItemEventsInput{
		Filters: []ssmtypes.OpsItemEventFilter{
			{
				Key:      ssmtypes.OpsItemEventFilterKeyOpsitemId,
				Values:   []string{id},
				Operator: ssmtypes.OpsItemEventFilterOperatorEqual,
			},
		},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(evs.Summaries), 2)
	for _, e := range evs.Summaries {
		assert.Equal(t, id, *e.OpsItemId)
	}

	// Disassociate.
	_, err = c.DisassociateOpsItemRelatedItem(ctx, &ssm.DisassociateOpsItemRelatedItemInput{
		OpsItemId:     aws.String(id),
		AssociationId: aws.String(assocID),
	})
	require.NoError(t, err)
	rel2, err := c.ListOpsItemRelatedItems(ctx, &ssm.ListOpsItemRelatedItemsInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	assert.Empty(t, rel2.Summaries)

	// Delete the ops item; a subsequent Get reports it gone.
	_, err = c.DeleteOpsItem(ctx, &ssm.DeleteOpsItemInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	_, err = c.GetOpsItem(ctx, &ssm.GetOpsItemInput{OpsItemId: aws.String(id)})
	require.Error(t, err)
}

// TestSSM_MaintenanceWindowExecution pins the execution-side reads against a
// real window+task: the window settles a deterministic SUCCESS execution, with
// one task-execution and invocation, plus the GetMaintenanceWindowTask read and
// the target/task update operations.
func TestSSM_MaintenanceWindowExecution(t *testing.T) {
	c := ssmClient()

	cw, err := c.CreateMaintenanceWindow(ctx, &ssm.CreateMaintenanceWindowInput{
		Name:                     aws.String("sockerless-mwexec-sdk"),
		Schedule:                 aws.String("cron(0 16 ? * TUE *)"),
		Duration:                 aws.Int32(4),
		Cutoff:                   1,
		AllowUnassociatedTargets: true,
	})
	require.NoError(t, err)
	windowID := *cw.WindowId
	t.Cleanup(func() {
		_, _ = c.DeleteMaintenanceWindow(ctx, &ssm.DeleteMaintenanceWindowInput{WindowId: aws.String(windowID)})
	})

	rt, err := c.RegisterTargetWithMaintenanceWindow(ctx, &ssm.RegisterTargetWithMaintenanceWindowInput{
		WindowId:     aws.String(windowID),
		ResourceType: ssmtypes.MaintenanceWindowResourceTypeInstance,
		Targets:      []ssmtypes.Target{{Key: aws.String("tag:Env"), Values: []string{"prod"}}},
	})
	require.NoError(t, err)
	targetID := *rt.WindowTargetId

	rtask, err := c.RegisterTaskWithMaintenanceWindow(ctx, &ssm.RegisterTaskWithMaintenanceWindowInput{
		WindowId:       aws.String(windowID),
		TaskArn:        aws.String("AWS-RunShellScript"),
		TaskType:       ssmtypes.MaintenanceWindowTaskTypeRunCommand,
		Priority:       aws.Int32(1),
		MaxConcurrency: aws.String("1"),
		MaxErrors:      aws.String("1"),
		Targets:        []ssmtypes.Target{{Key: aws.String("WindowTargetIds"), Values: []string{targetID}}},
	})
	require.NoError(t, err)
	windowTaskID := *rtask.WindowTaskId

	// GetMaintenanceWindowTask reads the registered task back.
	gt, err := c.GetMaintenanceWindowTask(ctx, &ssm.GetMaintenanceWindowTaskInput{
		WindowId:     aws.String(windowID),
		WindowTaskId: aws.String(windowTaskID),
	})
	require.NoError(t, err)
	assert.Equal(t, "AWS-RunShellScript", *gt.TaskArn)
	assert.Equal(t, ssmtypes.MaintenanceWindowTaskTypeRunCommand, gt.TaskType)

	// DescribeMaintenanceWindowExecutions settles a deterministic execution.
	de, err := c.DescribeMaintenanceWindowExecutions(ctx, &ssm.DescribeMaintenanceWindowExecutionsInput{
		WindowId: aws.String(windowID),
	})
	require.NoError(t, err)
	require.Len(t, de.WindowExecutions, 1)
	execID := *de.WindowExecutions[0].WindowExecutionId
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, de.WindowExecutions[0].Status)

	// GetMaintenanceWindowExecution returns its task ids.
	ge, err := c.GetMaintenanceWindowExecution(ctx, &ssm.GetMaintenanceWindowExecutionInput{
		WindowExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	require.Len(t, ge.TaskIds, 1)
	taskExecID := ge.TaskIds[0]
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, ge.Status)

	// DescribeMaintenanceWindowExecutionTasks.
	det, err := c.DescribeMaintenanceWindowExecutionTasks(ctx, &ssm.DescribeMaintenanceWindowExecutionTasksInput{
		WindowExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	require.Len(t, det.WindowExecutionTaskIdentities, 1)
	assert.Equal(t, "AWS-RunShellScript", *det.WindowExecutionTaskIdentities[0].TaskArn)

	// GetMaintenanceWindowExecutionTask.
	get, err := c.GetMaintenanceWindowExecutionTask(ctx, &ssm.GetMaintenanceWindowExecutionTaskInput{
		WindowExecutionId: aws.String(execID),
		TaskId:            aws.String(taskExecID),
	})
	require.NoError(t, err)
	assert.Equal(t, "AWS-RunShellScript", *get.TaskArn)
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, get.Status)

	// DescribeMaintenanceWindowExecutionTaskInvocations.
	dei, err := c.DescribeMaintenanceWindowExecutionTaskInvocations(ctx, &ssm.DescribeMaintenanceWindowExecutionTaskInvocationsInput{
		WindowExecutionId: aws.String(execID),
		TaskId:            aws.String(taskExecID),
	})
	require.NoError(t, err)
	require.Len(t, dei.WindowExecutionTaskInvocationIdentities, 1)
	invID := *dei.WindowExecutionTaskInvocationIdentities[0].InvocationId

	// GetMaintenanceWindowExecutionTaskInvocation.
	gei, err := c.GetMaintenanceWindowExecutionTaskInvocation(ctx, &ssm.GetMaintenanceWindowExecutionTaskInvocationInput{
		WindowExecutionId: aws.String(execID),
		TaskId:            aws.String(taskExecID),
		InvocationId:      aws.String(invID),
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, gei.Status)

	// DescribeMaintenanceWindowSchedule + ForTarget see the enabled window.
	ds, err := c.DescribeMaintenanceWindowSchedule(ctx, &ssm.DescribeMaintenanceWindowScheduleInput{
		WindowId: aws.String(windowID),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ds.ScheduledWindowExecutions), 1)

	dft, err := c.DescribeMaintenanceWindowsForTarget(ctx, &ssm.DescribeMaintenanceWindowsForTargetInput{
		ResourceType: ssmtypes.MaintenanceWindowResourceTypeInstance,
		Targets:      []ssmtypes.Target{{Key: aws.String("tag:Env"), Values: []string{"prod"}}},
	})
	require.NoError(t, err)
	foundWin := false
	for _, wi := range dft.WindowIdentities {
		if wi.WindowId != nil && *wi.WindowId == windowID {
			foundWin = true
		}
	}
	assert.True(t, foundWin)

	// UpdateMaintenanceWindowTarget.
	ut, err := c.UpdateMaintenanceWindowTarget(ctx, &ssm.UpdateMaintenanceWindowTargetInput{
		WindowId:       aws.String(windowID),
		WindowTargetId: aws.String(targetID),
		Name:           aws.String("renamed-target"),
		Targets:        []ssmtypes.Target{{Key: aws.String("tag:Env"), Values: []string{"staging"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed-target", *ut.Name)
	require.Len(t, ut.Targets, 1)
	assert.Equal(t, []string{"staging"}, ut.Targets[0].Values)

	// UpdateMaintenanceWindowTask.
	utask, err := c.UpdateMaintenanceWindowTask(ctx, &ssm.UpdateMaintenanceWindowTaskInput{
		WindowId:       aws.String(windowID),
		WindowTaskId:   aws.String(windowTaskID),
		Priority:       aws.Int32(5),
		MaxConcurrency: aws.String("2"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(5), utask.Priority)
	assert.Equal(t, "2", *utask.MaxConcurrency)

	// CancelMaintenanceWindowExecution echoes the id.
	cancel, err := c.CancelMaintenanceWindowExecution(ctx, &ssm.CancelMaintenanceWindowExecutionInput{
		WindowExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	assert.Equal(t, execID, *cancel.WindowExecutionId)
}

// TestSSM_SessionLifecycle pins the Session Manager slice: start a session,
// confirm its connection status, list active sessions, terminate, and confirm
// it moves to the history view.
func TestSSM_SessionLifecycle(t *testing.T) {
	c := ssmClient()
	target := "i-0123456789sockerless"

	start, err := c.StartSession(ctx, &ssm.StartSessionInput{Target: aws.String(target)})
	require.NoError(t, err)
	sessID := *start.SessionId
	assert.NotEmpty(t, sessID)
	assert.NotEmpty(t, *start.TokenValue)
	assert.Contains(t, *start.StreamUrl, sessID)
	t.Cleanup(func() {
		_, _ = c.TerminateSession(ctx, &ssm.TerminateSessionInput{SessionId: aws.String(sessID)})
	})

	// GetConnectionStatus reports connected for a target with an active session.
	cs, err := c.GetConnectionStatus(ctx, &ssm.GetConnectionStatusInput{Target: aws.String(target)})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.ConnectionStatusConnected, cs.Status)
	assert.Equal(t, target, *cs.Target)

	// DescribeSessions (Active) includes it.
	active, err := c.DescribeSessions(ctx, &ssm.DescribeSessionsInput{State: ssmtypes.SessionStateActive})
	require.NoError(t, err)
	foundActive := false
	for _, s := range active.Sessions {
		if s.SessionId != nil && *s.SessionId == sessID {
			foundActive = true
			assert.Equal(t, ssmtypes.SessionStatusConnected, s.Status)
		}
	}
	assert.True(t, foundActive)

	// ResumeSession returns a fresh token/stream.
	resume, err := c.ResumeSession(ctx, &ssm.ResumeSessionInput{SessionId: aws.String(sessID)})
	require.NoError(t, err)
	assert.Equal(t, sessID, *resume.SessionId)
	assert.NotEmpty(t, *resume.TokenValue)

	// Terminate then confirm it's gone from Active and present in History.
	term, err := c.TerminateSession(ctx, &ssm.TerminateSessionInput{SessionId: aws.String(sessID)})
	require.NoError(t, err)
	assert.Equal(t, sessID, *term.SessionId)

	active2, err := c.DescribeSessions(ctx, &ssm.DescribeSessionsInput{State: ssmtypes.SessionStateActive})
	require.NoError(t, err)
	for _, s := range active2.Sessions {
		if s.SessionId != nil {
			assert.NotEqual(t, sessID, *s.SessionId)
		}
	}
	history, err := c.DescribeSessions(ctx, &ssm.DescribeSessionsInput{State: ssmtypes.SessionStateHistory})
	require.NoError(t, err)
	foundHist := false
	for _, s := range history.Sessions {
		if s.SessionId != nil && *s.SessionId == sessID {
			foundHist = true
			assert.Equal(t, ssmtypes.SessionStatusTerminated, s.Status)
		}
	}
	assert.True(t, foundHist)
}

// TestSSM_ConnectionStatusNotConnected pins notconnected for an instance with
// no active session.
func TestSSM_ConnectionStatusNotConnected(t *testing.T) {
	c := ssmClient()
	cs, err := c.GetConnectionStatus(ctx, &ssm.GetConnectionStatusInput{
		Target: aws.String("i-never-had-a-session-sockerless"),
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.ConnectionStatusNotConnected, cs.Status)
}

// TestSSM_ActivationLifecycle pins the hybrid-activation slice: create an
// activation (id + code), list it back, and delete it.
func TestSSM_ActivationLifecycle(t *testing.T) {
	c := ssmClient()

	created, err := c.CreateActivation(ctx, &ssm.CreateActivationInput{
		IamRole:             aws.String("SSMServiceRole"),
		Description:         aws.String("on-prem fleet"),
		DefaultInstanceName: aws.String("edge-node"),
		RegistrationLimit:   aws.Int32(5),
		Tags:                []ssmtypes.Tag{{Key: aws.String("Env"), Value: aws.String("hybrid")}},
	})
	require.NoError(t, err)
	actID := *created.ActivationId
	assert.NotEmpty(t, actID)
	assert.NotEmpty(t, *created.ActivationCode)
	t.Cleanup(func() {
		_, _ = c.DeleteActivation(ctx, &ssm.DeleteActivationInput{ActivationId: aws.String(actID)})
	})

	da, err := c.DescribeActivations(ctx, &ssm.DescribeActivationsInput{})
	require.NoError(t, err)
	found := false
	for _, a := range da.ActivationList {
		if a.ActivationId != nil && *a.ActivationId == actID {
			found = true
			assert.Equal(t, "SSMServiceRole", *a.IamRole)
			assert.Equal(t, int32(5), *a.RegistrationLimit)
			assert.Equal(t, "edge-node", *a.DefaultInstanceName)
		}
	}
	assert.True(t, found, "DescribeActivations must include created activation")

	_, err = c.DeleteActivation(ctx, &ssm.DeleteActivationInput{ActivationId: aws.String(actID)})
	require.NoError(t, err)
	da2, err := c.DescribeActivations(ctx, &ssm.DescribeActivationsInput{})
	require.NoError(t, err)
	for _, a := range da2.ActivationList {
		if a.ActivationId != nil {
			assert.NotEqual(t, actID, *a.ActivationId)
		}
	}
}
