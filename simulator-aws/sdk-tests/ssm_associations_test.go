package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmEnsureDoc creates a Command document the association/automation/command
// flows reference and registers tolerant cleanup.
func ssmEnsureDoc(t *testing.T, c *ssm.Client, name string) {
	t.Helper()
	content := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"s","inputs":{"runCommand":["echo hi"]}}]}`
	_, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(name),
		Content:        aws.String(content),
		DocumentType:   ssmtypes.DocumentTypeCommand,
		DocumentFormat: ssmtypes.DocumentFormatJson,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(name)})
	})
}

// TestSSM_AssociationLifecycle pins the State Manager association control
// plane: create a binding, describe it (settles to Success), list it, version
// it via update, read execution history + targets + effective-instance views,
// run it once, and delete.
func TestSSM_AssociationLifecycle(t *testing.T) {
	c := ssmClient()
	docName := "sockerless-assoc-doc-sdk"
	ssmEnsureDoc(t, c, docName)

	instanceID := "i-0123456789abcdef0"
	created, err := c.CreateAssociation(ctx, &ssm.CreateAssociationInput{
		Name:            aws.String(docName),
		AssociationName: aws.String("sdk-assoc"),
		Targets: []ssmtypes.Target{
			{Key: aws.String("InstanceIds"), Values: []string{instanceID}},
		},
		ScheduleExpression: aws.String("rate(30 minutes)"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.AssociationDescription)
	assocID := aws.ToString(created.AssociationDescription.AssociationId)
	require.NotEmpty(t, assocID)
	assert.Equal(t, "1", aws.ToString(created.AssociationDescription.AssociationVersion))
	t.Cleanup(func() {
		_, _ = c.DeleteAssociation(ctx, &ssm.DeleteAssociationInput{AssociationId: aws.String(assocID)})
	})

	// Describe settles Pending -> Success.
	desc, err := c.DescribeAssociation(ctx, &ssm.DescribeAssociationInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	require.NotNil(t, desc.AssociationDescription.Status)
	assert.Equal(t, ssmtypes.AssociationStatusNameSuccess, desc.AssociationDescription.Status.Name)

	// List includes our association.
	list, err := c.ListAssociations(ctx, &ssm.ListAssociationsInput{})
	require.NoError(t, err)
	found := false
	for _, a := range list.Associations {
		if aws.ToString(a.AssociationId) == assocID {
			found = true
		}
	}
	assert.True(t, found, "ListAssociations must include the created association")

	// Update bumps the version to 2.
	upd, err := c.UpdateAssociation(ctx, &ssm.UpdateAssociationInput{
		AssociationId:      aws.String(assocID),
		ScheduleExpression: aws.String("rate(60 minutes)"),
	})
	require.NoError(t, err)
	assert.Equal(t, "2", aws.ToString(upd.AssociationDescription.AssociationVersion))

	// Version history has both 1 and 2.
	vers, err := c.ListAssociationVersions(ctx, &ssm.ListAssociationVersionsInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	assert.Len(t, vers.AssociationVersions, 2)

	// UpdateAssociationStatus by (Name, InstanceId).
	_, err = c.UpdateAssociationStatus(ctx, &ssm.UpdateAssociationStatusInput{
		Name:       aws.String(docName),
		InstanceId: aws.String(instanceID),
		AssociationStatus: &ssmtypes.AssociationStatus{
			Date:    aws.Time(created.AssociationDescription.Date.UTC()),
			Name:    ssmtypes.AssociationStatusNameSuccess,
			Message: aws.String("applied"),
		},
	})
	require.NoError(t, err)

	// StartAssociationsOnce produces an execution.
	_, err = c.StartAssociationsOnce(ctx, &ssm.StartAssociationsOnceInput{
		AssociationIds: []string{assocID},
	})
	require.NoError(t, err)

	// Execution history.
	execs, err := c.DescribeAssociationExecutions(ctx, &ssm.DescribeAssociationExecutionsInput{
		AssociationId: aws.String(assocID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, execs.AssociationExecutions)
	execID := aws.ToString(execs.AssociationExecutions[0].ExecutionId)

	// Execution targets.
	tgts, err := c.DescribeAssociationExecutionTargets(ctx, &ssm.DescribeAssociationExecutionTargetsInput{
		AssociationId: aws.String(assocID),
		ExecutionId:   aws.String(execID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, tgts.AssociationExecutionTargets)
	assert.Equal(t, instanceID, aws.ToString(tgts.AssociationExecutionTargets[0].ResourceId))

	// Effective instance associations.
	eff, err := c.DescribeEffectiveInstanceAssociations(ctx, &ssm.DescribeEffectiveInstanceAssociationsInput{
		InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, eff.Associations)
	assert.Equal(t, assocID, aws.ToString(eff.Associations[0].AssociationId))

	// Instance association status.
	st, err := c.DescribeInstanceAssociationsStatus(ctx, &ssm.DescribeInstanceAssociationsStatusInput{
		InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, st.InstanceAssociationStatusInfos)
}

// TestSSM_AssociationBatch pins CreateAssociationBatch — one entry referencing
// a real document succeeds, one referencing a missing document fails.
func TestSSM_AssociationBatch(t *testing.T) {
	c := ssmClient()
	docName := "sockerless-assoc-batch-doc-sdk"
	ssmEnsureDoc(t, c, docName)

	out, err := c.CreateAssociationBatch(ctx, &ssm.CreateAssociationBatchInput{
		Entries: []ssmtypes.CreateAssociationBatchRequestEntry{
			{
				Name: aws.String(docName),
				Targets: []ssmtypes.Target{
					{Key: aws.String("InstanceIds"), Values: []string{"i-aaaa1111bbbb2222"}},
				},
			},
			{Name: aws.String("nonexistent-document-xyz")},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Successful, 1)
	require.Len(t, out.Failed, 1)
	t.Cleanup(func() {
		for _, a := range out.Successful {
			_, _ = c.DeleteAssociation(ctx, &ssm.DeleteAssociationInput{AssociationId: a.AssociationId})
		}
	})
}

// TestSSM_AutomationLifecycle pins the Automation control plane: start an
// execution against a document, read it back (Success with a step), list,
// inspect step executions, signal, stop, plus a change-request execution.
func TestSSM_AutomationLifecycle(t *testing.T) {
	c := ssmClient()
	docName := "sockerless-automation-doc-sdk"
	ssmEnsureDoc(t, c, docName)

	start, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(docName),
		Parameters:   map[string][]string{"Param": {"v"}},
	})
	require.NoError(t, err)
	execID := aws.ToString(start.AutomationExecutionId)
	require.NotEmpty(t, execID)

	got, err := c.GetAutomationExecution(ctx, &ssm.GetAutomationExecutionInput{
		AutomationExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	require.NotNil(t, got.AutomationExecution)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, got.AutomationExecution.AutomationExecutionStatus)
	require.NotEmpty(t, got.AutomationExecution.StepExecutions)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, got.AutomationExecution.StepExecutions[0].StepStatus)

	desc, err := c.DescribeAutomationExecutions(ctx, &ssm.DescribeAutomationExecutionsInput{})
	require.NoError(t, err)
	foundExec := false
	for _, e := range desc.AutomationExecutionMetadataList {
		if aws.ToString(e.AutomationExecutionId) == execID {
			foundExec = true
		}
	}
	assert.True(t, foundExec, "DescribeAutomationExecutions must include the started execution")

	steps, err := c.DescribeAutomationStepExecutions(ctx, &ssm.DescribeAutomationStepExecutionsInput{
		AutomationExecutionId: aws.String(execID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, steps.StepExecutions)

	_, err = c.SendAutomationSignal(ctx, &ssm.SendAutomationSignalInput{
		AutomationExecutionId: aws.String(execID),
		SignalType:            ssmtypes.SignalTypeResume,
	})
	require.NoError(t, err)

	_, err = c.StopAutomationExecution(ctx, &ssm.StopAutomationExecutionInput{
		AutomationExecutionId: aws.String(execID),
	})
	require.NoError(t, err)

	cr, err := c.StartChangeRequestExecution(ctx, &ssm.StartChangeRequestExecutionInput{
		DocumentName:      aws.String(docName),
		ChangeRequestName: aws.String("sdk-change"),
		Runbooks: []ssmtypes.Runbook{
			{DocumentName: aws.String(docName)},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(cr.AutomationExecutionId))
}

// TestSSM_AutomationExecutionPreview pins StartExecutionPreview /
// GetExecutionPreview.
func TestSSM_AutomationExecutionPreview(t *testing.T) {
	c := ssmClient()
	docName := "sockerless-preview-doc-sdk"
	ssmEnsureDoc(t, c, docName)

	start, err := c.StartExecutionPreview(ctx, &ssm.StartExecutionPreviewInput{
		DocumentName: aws.String(docName),
	})
	require.NoError(t, err)
	previewID := aws.ToString(start.ExecutionPreviewId)
	require.NotEmpty(t, previewID)

	get, err := c.GetExecutionPreview(ctx, &ssm.GetExecutionPreviewInput{
		ExecutionPreviewId: aws.String(previewID),
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.ExecutionPreviewStatusSuccess, get.Status)
	require.NotNil(t, get.ExecutionPreview)
}

// TestSSM_CommandLifecycle pins Run Command: send a command to instances,
// list it, list invocations, fetch a per-instance invocation, then cancel.
func TestSSM_CommandLifecycle(t *testing.T) {
	c := ssmClient()
	docName := "sockerless-command-doc-sdk"
	ssmEnsureDoc(t, c, docName)

	instanceID := "i-cccc3333dddd44440"
	send, err := c.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String(docName),
		InstanceIds:  []string{instanceID},
		Comment:      aws.String("sdk run"),
		Parameters:   map[string][]string{"commands": {"echo hi"}},
	})
	require.NoError(t, err)
	require.NotNil(t, send.Command)
	cmdID := aws.ToString(send.Command.CommandId)
	require.NotEmpty(t, cmdID)
	assert.Equal(t, ssmtypes.CommandStatusSuccess, send.Command.Status)

	list, err := c.ListCommands(ctx, &ssm.ListCommandsInput{CommandId: aws.String(cmdID)})
	require.NoError(t, err)
	require.Len(t, list.Commands, 1)

	invs, err := c.ListCommandInvocations(ctx, &ssm.ListCommandInvocationsInput{
		CommandId: aws.String(cmdID),
		Details:   true,
	})
	require.NoError(t, err)
	require.Len(t, invs.CommandInvocations, 1)
	assert.Equal(t, instanceID, aws.ToString(invs.CommandInvocations[0].InstanceId))
	require.NotEmpty(t, invs.CommandInvocations[0].CommandPlugins)

	inv, err := c.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
		CommandId:  aws.String(cmdID),
		InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.CommandInvocationStatusSuccess, inv.Status)
	assert.Equal(t, int32(0), inv.ResponseCode)

	_, err = c.CancelCommand(ctx, &ssm.CancelCommandInput{CommandId: aws.String(cmdID)})
	require.NoError(t, err)
}
