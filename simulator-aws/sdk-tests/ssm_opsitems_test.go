package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSM_OpsItemLifecycle(t *testing.T) {
	t.Parallel()
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
	identity, err := stsClient().GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:ssm:us-east-1:"+*identity.Account+":opsitem/"+id, *created.OpsItemArn)

	got, err := c.GetOpsItem(ctx, &ssm.GetOpsItemInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, got.OpsItem)
	assert.Equal(t, "Disk full on host", *got.OpsItem.Title)
	assert.Equal(t, ssmtypes.OpsItemStatusOpen, got.OpsItem.Status)
	assert.Equal(t, int32(2), *got.OpsItem.Priority)
	assert.Equal(t, "1", *got.OpsItem.Version)
	require.Contains(t, got.OpsItem.OperationalData, "/aws/resources")
	assert.Equal(t, *identity.Arn, *got.OpsItem.CreatedBy, "the creator is the identity that signed the create")
	assert.Equal(t, *identity.Arn, *got.OpsItem.LastModifiedBy)

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
	assert.Equal(t, *identity.Arn, *got2.OpsItem.LastModifiedBy, "an update records the identity that signed it")

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

	// The create and the update are activity events.
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

	_, err = c.DisassociateOpsItemRelatedItem(ctx, &ssm.DisassociateOpsItemRelatedItemInput{
		OpsItemId:     aws.String(id),
		AssociationId: aws.String(assocID),
	})
	require.NoError(t, err)
	rel2, err := c.ListOpsItemRelatedItems(ctx, &ssm.ListOpsItemRelatedItemsInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	assert.Empty(t, rel2.Summaries)

	_, err = c.DeleteOpsItem(ctx, &ssm.DeleteOpsItemInput{OpsItemId: aws.String(id)})
	require.NoError(t, err)
	_, err = c.GetOpsItem(ctx, &ssm.GetOpsItemInput{OpsItemId: aws.String(id)})
	require.Error(t, err)
}

// TestSSM_MaintenanceWindowExecution schedules a window a few seconds ahead
// with a Run Command task on a managed node, waits for the window to run, and
// reads the execution, its task and the invocation, whose command wrote its
// output on the node.
func TestSSM_MaintenanceWindowExecution(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	instanceID := launchSSMManagedInstanceFor(t, ec2Client(), "10.241.0.0/16")

	runAt := time.Now().UTC().Add(5 * time.Second).Truncate(time.Second)
	cw, err := c.CreateMaintenanceWindow(ctx, &ssm.CreateMaintenanceWindowInput{
		Name:                     aws.String("sockerless-mwexec-sdk"),
		Schedule:                 aws.String("at(" + runAt.Format("2006-01-02T15:04:05") + ")"),
		Duration:                 aws.Int32(1),
		Cutoff:                   0,
		AllowUnassociatedTargets: true,
	})
	require.NoError(t, err)
	windowID := *cw.WindowId
	t.Cleanup(func() {
		_, _ = c.DeleteMaintenanceWindow(ctx, &ssm.DeleteMaintenanceWindowInput{WindowId: aws.String(windowID)})
	})
	gw, err := c.GetMaintenanceWindow(ctx, &ssm.GetMaintenanceWindowInput{WindowId: aws.String(windowID)})
	require.NoError(t, err)
	assert.Equal(t, runAt.Format("2006-01-02T15:04:05Z"), aws.ToString(gw.NextExecutionTime))

	ds, err := c.DescribeMaintenanceWindowSchedule(ctx, &ssm.DescribeMaintenanceWindowScheduleInput{
		WindowId: aws.String(windowID),
	})
	require.NoError(t, err)
	require.Len(t, ds.ScheduledWindowExecutions, 1)
	assert.Equal(t, runAt.Format("2006-01-02T15:04:05Z"), aws.ToString(ds.ScheduledWindowExecutions[0].ExecutionTime))

	rt, err := c.RegisterTargetWithMaintenanceWindow(ctx, &ssm.RegisterTargetWithMaintenanceWindowInput{
		WindowId:         aws.String(windowID),
		ResourceType:     ssmtypes.MaintenanceWindowResourceTypeInstance,
		Targets:          []ssmtypes.Target{{Key: aws.String("InstanceIds"), Values: []string{instanceID}}},
		OwnerInformation: aws.String("sdk-owner"),
	})
	require.NoError(t, err)
	targetID := *rt.WindowTargetId

	// Systems Manager User Guide: a maintenance window task runs as a role of
	// the caller's own that trusts ssm.amazonaws.com.
	role := createServiceRole(t, uniquename.New("mw-task"), "ssm.amazonaws.com", "ssm:SendCommand", "ssm:CancelCommand")
	rtask, err := c.RegisterTaskWithMaintenanceWindow(ctx, &ssm.RegisterTaskWithMaintenanceWindowInput{
		WindowId:       aws.String(windowID),
		TaskArn:        aws.String("AWS-RunShellScript"),
		TaskType:       ssmtypes.MaintenanceWindowTaskTypeRunCommand,
		ServiceRoleArn: aws.String(role),
		Priority:       aws.Int32(1),
		MaxConcurrency: aws.String("1"),
		MaxErrors:      aws.String("1"),
		Targets:        []ssmtypes.Target{{Key: aws.String("WindowTargetIds"), Values: []string{targetID}}},
		TaskInvocationParameters: &ssmtypes.MaintenanceWindowTaskInvocationParameters{
			RunCommand: &ssmtypes.MaintenanceWindowRunCommandParameters{
				Parameters: map[string][]string{"commands": {"echo maintenance window ran"}},
			},
		},
	})
	require.NoError(t, err)
	windowTaskID := *rtask.WindowTaskId

	gt, err := c.GetMaintenanceWindowTask(ctx, &ssm.GetMaintenanceWindowTaskInput{
		WindowId:     aws.String(windowID),
		WindowTaskId: aws.String(windowTaskID),
	})
	require.NoError(t, err)
	assert.Equal(t, "AWS-RunShellScript", *gt.TaskArn)
	assert.Equal(t, ssmtypes.MaintenanceWindowTaskTypeRunCommand, gt.TaskType)
	require.NotNil(t, gt.TaskInvocationParameters)
	assert.Equal(t, []string{"echo maintenance window ran"}, gt.TaskInvocationParameters.RunCommand.Parameters["commands"])

	execution := awsMaintenanceWindowExecution(t, c, windowID)
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, execution.Status)
	assert.False(t, aws.ToTime(execution.StartTime).Before(runAt), "the window runs at its at() time, not before")
	execID := *execution.WindowExecutionId

	ge, err := c.GetMaintenanceWindowExecution(ctx, &ssm.GetMaintenanceWindowExecutionInput{
		WindowExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	require.Len(t, ge.TaskIds, 1)
	taskExecID := ge.TaskIds[0]
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, ge.Status)

	det, err := c.DescribeMaintenanceWindowExecutionTasks(ctx, &ssm.DescribeMaintenanceWindowExecutionTasksInput{
		WindowExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	require.Len(t, det.WindowExecutionTaskIdentities, 1)
	assert.Equal(t, "AWS-RunShellScript", *det.WindowExecutionTaskIdentities[0].TaskArn)

	get, err := c.GetMaintenanceWindowExecutionTask(ctx, &ssm.GetMaintenanceWindowExecutionTaskInput{
		WindowExecutionId: aws.String(execID),
		TaskId:            aws.String(taskExecID),
	})
	require.NoError(t, err)
	assert.Equal(t, "AWS-RunShellScript", *get.TaskArn)
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, get.Status)
	assert.Equal(t, int32(1), get.Priority)

	dei, err := c.DescribeMaintenanceWindowExecutionTaskInvocations(ctx, &ssm.DescribeMaintenanceWindowExecutionTaskInvocationsInput{
		WindowExecutionId: aws.String(execID),
		TaskId:            aws.String(taskExecID),
	})
	require.NoError(t, err)
	require.Len(t, dei.WindowExecutionTaskInvocationIdentities, 1)
	identity := dei.WindowExecutionTaskInvocationIdentities[0]
	assert.Equal(t, targetID, aws.ToString(identity.WindowTargetId))
	assert.Equal(t, "sdk-owner", aws.ToString(identity.OwnerInformation))
	require.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, identity.Status, aws.ToString(identity.StatusDetails))
	commandID := aws.ToString(identity.ExecutionId)

	gei, err := c.GetMaintenanceWindowExecutionTaskInvocation(ctx, &ssm.GetMaintenanceWindowExecutionTaskInvocationInput{
		WindowExecutionId: aws.String(execID),
		TaskId:            aws.String(taskExecID),
		InvocationId:      identity.InvocationId,
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, gei.Status, aws.ToString(gei.StatusDetails))
	assert.Equal(t, commandID, aws.ToString(gei.ExecutionId), "a Run Command invocation names its command")

	ran, err := c.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
		CommandId: aws.String(commandID), InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	assert.Equal(t, "maintenance window ran\n", aws.ToString(ran.StandardOutputContent))

	after, err := c.DescribeMaintenanceWindowSchedule(ctx, &ssm.DescribeMaintenanceWindowScheduleInput{
		WindowId: aws.String(windowID),
	})
	require.NoError(t, err)
	assert.Empty(t, after.ScheduledWindowExecutions, "an at() window runs once")

	dft, err := c.DescribeMaintenanceWindowsForTarget(ctx, &ssm.DescribeMaintenanceWindowsForTargetInput{
		ResourceType: ssmtypes.MaintenanceWindowResourceTypeInstance,
		Targets:      []ssmtypes.Target{{Key: aws.String("InstanceIds"), Values: []string{instanceID}}},
	})
	require.NoError(t, err)
	require.Len(t, dft.WindowIdentities, 1)
	assert.Equal(t, windowID, aws.ToString(dft.WindowIdentities[0].WindowId))

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

	utask, err := c.UpdateMaintenanceWindowTask(ctx, &ssm.UpdateMaintenanceWindowTaskInput{
		WindowId:       aws.String(windowID),
		WindowTaskId:   aws.String(windowTaskID),
		Priority:       aws.Int32(5),
		MaxConcurrency: aws.String("2"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(5), utask.Priority)
	assert.Equal(t, "2", *utask.MaxConcurrency)

	cancel, err := c.CancelMaintenanceWindowExecution(ctx, &ssm.CancelMaintenanceWindowExecutionInput{
		WindowExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	assert.Equal(t, execID, *cancel.WindowExecutionId)
	_, err = c.CancelMaintenanceWindowExecution(ctx, &ssm.CancelMaintenanceWindowExecutionInput{
		WindowExecutionId: aws.String("00000000-0000-0000-0000-000000000000"),
	})
	requireAWSErrorCode(t, err, "DoesNotExistException")
}

// awsMaintenanceWindowExecution polls DescribeMaintenanceWindowExecutions,
// the only view Systems Manager gives of a window's runs, until the window's
// first execution finished.
func awsMaintenanceWindowExecution(t *testing.T, c *ssm.Client, windowID string) ssmtypes.MaintenanceWindowExecution {
	t.Helper()
	var execution ssmtypes.MaintenanceWindowExecution
	seen := "no execution"
	require.Eventually(t, func() bool {
		out, err := c.DescribeMaintenanceWindowExecutions(ctx, &ssm.DescribeMaintenanceWindowExecutionsInput{
			WindowId: aws.String(windowID),
		})
		require.NoError(t, err)
		if len(out.WindowExecutions) == 0 {
			return false
		}
		execution = out.WindowExecutions[0]
		seen = string(execution.Status)
		switch execution.Status {
		case ssmtypes.MaintenanceWindowExecutionStatusPending, ssmtypes.MaintenanceWindowExecutionStatusInProgress,
			ssmtypes.MaintenanceWindowExecutionStatusCancelling:
			return false
		}
		return true
	}, 2*time.Minute, waiterMinDelay, "window %s never finished a run; last seen %s", windowID, &seen)
	return execution
}

// TestSSM_SessionLifecycle expects a session to report connected, list as
// Active, resume with a new token, and move to History when terminated.
func TestSSM_SessionLifecycle(t *testing.T) {
	t.Parallel()
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

	cs, err := c.GetConnectionStatus(ctx, &ssm.GetConnectionStatusInput{Target: aws.String(target)})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.ConnectionStatusConnected, cs.Status)
	assert.Equal(t, target, *cs.Target)

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

	resume, err := c.ResumeSession(ctx, &ssm.ResumeSessionInput{SessionId: aws.String(sessID)})
	require.NoError(t, err)
	assert.Equal(t, sessID, *resume.SessionId)
	assert.NotEmpty(t, *resume.TokenValue)

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

// TestSSM_ConnectionStatusNotConnected expects notconnected for an instance
// without an active session.
func TestSSM_ConnectionStatusNotConnected(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	cs, err := c.GetConnectionStatus(ctx, &ssm.GetConnectionStatusInput{
		Target: aws.String("i-never-had-a-session-sockerless"),
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.ConnectionStatusNotConnected, cs.Status)
}

func TestSSM_ActivationLifecycle(t *testing.T) {
	t.Parallel()
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
