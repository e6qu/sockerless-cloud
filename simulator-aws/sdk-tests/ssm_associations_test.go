package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmEnsureDoc creates a Command document and registers its cleanup.
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

// TestSSM_AssociationLifecycle covers a State Manager association: it settles
// to Success, versions on update, records executions, targets and effective
// instance views, runs once on demand, and deletes.
func TestSSM_AssociationLifecycle(t *testing.T) {
	t.Parallel()
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

	// The association settles from Pending to Success.
	desc, err := c.DescribeAssociation(ctx, &ssm.DescribeAssociationInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	require.NotNil(t, desc.AssociationDescription.Status)
	assert.Equal(t, ssmtypes.AssociationStatusNameSuccess, desc.AssociationDescription.Status.Name)

	list, err := c.ListAssociations(ctx, &ssm.ListAssociationsInput{})
	require.NoError(t, err)
	found := false
	for _, a := range list.Associations {
		if aws.ToString(a.AssociationId) == assocID {
			found = true
		}
	}
	assert.True(t, found, "ListAssociations must include the created association")

	// Update makes version 2.
	upd, err := c.UpdateAssociation(ctx, &ssm.UpdateAssociationInput{
		AssociationId:      aws.String(assocID),
		ScheduleExpression: aws.String("rate(60 minutes)"),
	})
	require.NoError(t, err)
	assert.Equal(t, "2", aws.ToString(upd.AssociationDescription.AssociationVersion))

	vers, err := c.ListAssociationVersions(ctx, &ssm.ListAssociationVersionsInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	assert.Len(t, vers.AssociationVersions, 2)

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

	_, err = c.StartAssociationsOnce(ctx, &ssm.StartAssociationsOnceInput{
		AssociationIds: []string{assocID},
	})
	require.NoError(t, err)

	execs, err := c.DescribeAssociationExecutions(ctx, &ssm.DescribeAssociationExecutionsInput{
		AssociationId: aws.String(assocID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, execs.AssociationExecutions)
	execID := aws.ToString(execs.AssociationExecutions[0].ExecutionId)

	tgts, err := c.DescribeAssociationExecutionTargets(ctx, &ssm.DescribeAssociationExecutionTargetsInput{
		AssociationId: aws.String(assocID),
		ExecutionId:   aws.String(execID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, tgts.AssociationExecutionTargets)
	assert.Equal(t, instanceID, aws.ToString(tgts.AssociationExecutionTargets[0].ResourceId))

	eff, err := c.DescribeEffectiveInstanceAssociations(ctx, &ssm.DescribeEffectiveInstanceAssociationsInput{
		InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, eff.Associations)
	assert.Equal(t, assocID, aws.ToString(eff.Associations[0].AssociationId))

	st, err := c.DescribeInstanceAssociationsStatus(ctx, &ssm.DescribeInstanceAssociationsStatusInput{
		InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, st.InstanceAssociationStatusInfos)
}

// TestSSM_AssociationBatch expects an entry naming an existing document to
// succeed and one naming a missing document to fail.
func TestSSM_AssociationBatch(t *testing.T) {
	t.Parallel()
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

// TestSSM_ChangeRequestRunsRunbooks starts a change request: Automation runs
// the change template's steps and then each runbook it names, as a child
// automation of the change request.
func TestSSM_ChangeRequestRunsRunbooks(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	template := uniquename.New("change-template-sdk")
	_, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(template),
		Content:        aws.String("schemaVersion: '0.3'\nmainSteps:\n- name: settle\n  action: aws:sleep\n  inputs:\n    Duration: PT0S\n"),
		DocumentType:   ssmtypes.DocumentTypeChangeTemplate,
		DocumentFormat: ssmtypes.DocumentFormatYaml,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(template)}) })
	runbook := uniquename.New("change-runbook-sdk")
	ssmCreateRunbook(t, c, runbook, `schemaVersion: '0.3'
parameters:
  ParamName:
    type: String
mainSteps:
- name: put
  action: aws:executeAwsApi
  inputs:
    Service: ssm
    Api: PutParameter
    Name: '{{ ParamName }}'
    Value: changed
    Type: String
`)
	param := "/sdk/change/" + runbook
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(param)}) })

	_, err = c.StartChangeRequestExecution(ctx, &ssm.StartChangeRequestExecutionInput{
		DocumentName: aws.String(runbook),
		Runbooks:     []ssmtypes.Runbook{{DocumentName: aws.String(runbook)}},
	})
	requireAWSErrorCode(t, err, "AutomationDefinitionNotFoundException")

	cr, err := c.StartChangeRequestExecution(ctx, &ssm.StartChangeRequestExecutionInput{
		DocumentName:      aws.String(template),
		ChangeRequestName: aws.String("sdk-change"),
		Runbooks: []ssmtypes.Runbook{{
			DocumentName: aws.String(runbook),
			Parameters:   map[string][]string{"ParamName": {param}},
		}},
	})
	require.NoError(t, err)
	id := aws.ToString(cr.AutomationExecutionId)
	execution := ssmAwaitAutomation(t, c, id, ssmAutomationSettled)
	require.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, execution.AutomationExecutionStatus, aws.ToString(execution.FailureMessage))
	assert.Equal(t, "sdk-change", aws.ToString(execution.ChangeRequestName))
	assert.Equal(t, ssmtypes.AutomationSubtypeChangeRequest, execution.AutomationSubtype)

	children, err := c.DescribeAutomationExecutions(ctx, &ssm.DescribeAutomationExecutionsInput{
		Filters: []ssmtypes.AutomationExecutionFilter{{Key: ssmtypes.AutomationExecutionFilterKeyParentExecutionId, Values: []string{id}}},
	})
	require.NoError(t, err)
	require.Len(t, children.AutomationExecutionMetadataList, 1)
	assert.Equal(t, runbook, aws.ToString(children.AutomationExecutionMetadataList[0].DocumentName))
	got, err := c.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(param)})
	require.NoError(t, err)
	assert.Equal(t, "changed", aws.ToString(got.Parameter.Value))
}

func TestSSM_AutomationExecutionPreview(t *testing.T) {
	t.Parallel()
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

// TestSSM_CommandLifecycle sends a command to a managed instance, reads the
// invocation with the output the command wrote, and cancels; a target that is
// no managed node fails the request.
func TestSSM_CommandLifecycle(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	docName := "sockerless-command-doc-sdk"
	ssmEnsureDoc(t, c, docName)

	_, err := c.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String(docName),
		InstanceIds:  []string{"i-cccc3333dddd44440"},
	})
	requireAWSErrorCode(t, err, "InvalidInstanceId")

	// A running instance whose profile does not let SSM Agent register it is no
	// managed node: Run Command refuses it and DescribeInstanceInformation does
	// not list it.
	ec2c := ec2Client()
	unmanaged := launchInstanceFor(t, ec2c, "10.168.0.0/16")
	waitForEC2InstanceRunning(t, ec2c, unmanaged)
	_, err = c.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String(docName),
		InstanceIds:  []string{unmanaged},
	})
	requireAWSErrorCode(t, err, "InvalidInstanceId")
	_, err = c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{unmanaged}}},
	})
	requireAWSErrorCode(t, err, "InvalidInstanceId")

	instanceID := launchSSMManagedInstanceFor(t, ec2c, "10.205.0.0/16")
	described, err := ec2c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	instance := described.Reservations[0].Instances[0]
	nodes, err := c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{instanceID}}},
	})
	require.NoError(t, err)
	require.Len(t, nodes.InstanceInformationList, 1)
	node := nodes.InstanceInformationList[0]
	assert.Equal(t, ssmtypes.PingStatusOnline, node.PingStatus)
	assert.Equal(t, ssmtypes.ResourceTypeEc2Instance, node.ResourceType)
	assert.Equal(t, ssmtypes.PlatformTypeLinux, node.PlatformType)
	assert.Equal(t, "Amazon Linux", aws.ToString(node.PlatformName), "SSM Agent reports the platform the AMI runs")
	assert.Equal(t, "2023", aws.ToString(node.PlatformVersion))
	assert.Equal(t, aws.ToString(instance.PrivateIpAddress), aws.ToString(node.IPAddress))
	assert.Equal(t, aws.ToString(instance.PrivateDnsName), aws.ToString(node.ComputerName))
	assert.Equal(t, aws.ToString(instance.IamInstanceProfile.Arn)[strings.LastIndex(aws.ToString(instance.IamInstanceProfile.Arn), "/")+1:], aws.ToString(node.IamRole))
	byPlatform, err := c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("PlatformTypes"), Values: []string{"Windows"}}},
	})
	require.NoError(t, err)
	for _, listed := range byPlatform.InstanceInformationList {
		assert.NotEqual(t, instanceID, aws.ToString(listed.InstanceId), "a PlatformTypes filter leaves out a Linux node")
	}

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
	assert.Equal(t, ssmtypes.CommandStatusPending, send.Command.Status)

	list, err := c.ListCommands(ctx, &ssm.ListCommandsInput{CommandId: aws.String(cmdID)})
	require.NoError(t, err)
	require.Len(t, list.Commands, 1)

	inv := waitForSSMCommandExecuted(t, c, cmdID, instanceID)
	assert.Equal(t, ssmtypes.CommandInvocationStatusSuccess, inv.Status)
	assert.Equal(t, int32(0), inv.ResponseCode)
	assert.Equal(t, "hi\n", aws.ToString(inv.StandardOutputContent), "the document's own echo runs on the instance")
	assert.Empty(t, aws.ToString(inv.StandardErrorContent))

	invs, err := c.ListCommandInvocations(ctx, &ssm.ListCommandInvocationsInput{
		CommandId: aws.String(cmdID),
		Details:   true,
	})
	require.NoError(t, err)
	require.Len(t, invs.CommandInvocations, 1)
	assert.Equal(t, instanceID, aws.ToString(invs.CommandInvocations[0].InstanceId))
	require.NotEmpty(t, invs.CommandInvocations[0].CommandPlugins)
	assert.Equal(t, "hi\n", aws.ToString(invs.CommandInvocations[0].CommandPlugins[0].Output))

	failing, err := c.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String("AWS-RunShellScript"),
		InstanceIds:  []string{instanceID},
		Parameters:   map[string][]string{"commands": {"echo oops >&2", "exit 3"}},
	})
	require.NoError(t, err)
	failed := waitForSSMCommandExecuted(t, c, aws.ToString(failing.Command.CommandId), instanceID)
	assert.Equal(t, ssmtypes.CommandInvocationStatusFailed, failed.Status)
	assert.Equal(t, int32(3), failed.ResponseCode)
	assert.Equal(t, "oops\n", aws.ToString(failed.StandardErrorContent))

	_, err = c.CancelCommand(ctx, &ssm.CancelCommandInput{CommandId: aws.String(cmdID)})
	require.NoError(t, err)
}

// waitForSSMCommandExecuted waits with the SDK's CommandExecuted waiter for
// an invocation to finish and returns it.
func waitForSSMCommandExecuted(t *testing.T, c *ssm.Client, commandID, instanceID string) *ssm.GetCommandInvocationOutput {
	t.Helper()
	out, err := ssm.NewCommandExecutedWaiter(c, func(o *ssm.CommandExecutedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ssm.GetCommandInvocationInput{CommandId: aws.String(commandID), InstanceId: aws.String(instanceID)}, 2*time.Minute)
	if err != nil {
		// The waiter stops on Failed too, which is a result to read, not an error.
		got, getErr := c.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{CommandId: aws.String(commandID), InstanceId: aws.String(instanceID)})
		require.NoError(t, getErr)
		require.Contains(t, []ssmtypes.CommandInvocationStatus{ssmtypes.CommandInvocationStatusFailed, ssmtypes.CommandInvocationStatusTimedOut}, got.Status, "waiter: %v", err)
		return got
	}
	return out
}
