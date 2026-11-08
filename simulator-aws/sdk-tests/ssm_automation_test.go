package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmCreateRunbook creates an Automation runbook from YAML content.
func ssmCreateRunbook(t *testing.T, c *ssm.Client, name, content string) {
	t.Helper()
	_, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(name),
		Content:        aws.String(content),
		DocumentType:   ssmtypes.DocumentTypeAutomation,
		DocumentFormat: ssmtypes.DocumentFormatYaml,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(name)}) })
}

// ssmAwaitAutomation polls GetAutomationExecution, the status Automation
// exposes for an execution, until done reports true.
func ssmAwaitAutomation(t *testing.T, c *ssm.Client, id string, done func(*ssmtypes.AutomationExecution) bool) *ssmtypes.AutomationExecution {
	t.Helper()
	var last *ssmtypes.AutomationExecution
	require.Eventually(t, func() bool {
		out, err := c.GetAutomationExecution(ctx, &ssm.GetAutomationExecutionInput{AutomationExecutionId: aws.String(id)})
		require.NoError(t, err)
		last = out.AutomationExecution
		return done(last)
	}, 2*time.Minute, waiterMinDelay, "automation %s never reached the awaited state", id)
	return last
}

func ssmAutomationSettled(e *ssmtypes.AutomationExecution) bool {
	switch e.AutomationExecutionStatus {
	case ssmtypes.AutomationExecutionStatusPending, ssmtypes.AutomationExecutionStatusInprogress,
		ssmtypes.AutomationExecutionStatusWaiting, ssmtypes.AutomationExecutionStatusCancelling,
		ssmtypes.AutomationExecutionStatusRunbookInprogress, ssmtypes.AutomationExecutionStatusScheduled:
		return false
	}
	return true
}

func ssmStepNamed(t *testing.T, e *ssmtypes.AutomationExecution, name string) ssmtypes.StepExecution {
	t.Helper()
	for _, s := range e.StepExecutions {
		if aws.ToString(s.StepName) == name {
			return s
		}
	}
	require.Failf(t, "missing step", "the execution has no step %s", name)
	return ssmtypes.StepExecution{}
}

const ssmTestRunbook = `schemaVersion: '0.3'
description: Writes a parameter, reads it back, and branches on Mode.
parameters:
  ParamName:
    type: String
  Value:
    type: String
    default: first-value
  BucketName:
    type: String
  Mode:
    type: String
    allowedValues: [pass, fail]
mainSteps:
- name: putParam
  action: aws:executeAwsApi
  inputs:
    Service: ssm
    Api: PutParameter
    Name: '{{ ParamName }}'
    Value: '{{ Value }}'
    Type: String
    Overwrite: true
  outputs:
  - Name: Version
    Selector: $.Version
    Type: Integer
- name: readParam
  action: aws:executeAwsApi
  inputs:
    Service: ssm
    Api: GetParameter
    Name: '{{ ParamName }}'
  outputs:
  - Name: StoredValue
    Selector: $.Parameter.Value
    Type: String
- name: assertParam
  action: aws:assertAwsResourceProperty
  inputs:
    Service: ssm
    Api: GetParameter
    Name: '{{ ParamName }}'
    PropertySelector: $.Parameter.Value
    DesiredValues:
    - '{{ readParam.StoredValue }}'
- name: makeBucket
  action: aws:executeAwsApi
  inputs:
    Service: s3
    Api: CreateBucket
    Bucket: '{{ BucketName }}'
- name: waitParam
  action: aws:waitForAwsResourceProperty
  timeoutSeconds: 60
  inputs:
    Service: ssm
    Api: GetParameter
    Name: '{{ ParamName }}'
    PropertySelector: $.Parameter.Version
    DesiredValues:
    - '{{ putParam.Version }}'
- name: nap
  action: aws:sleep
  inputs:
    Duration: PT1S
- name: choose
  action: aws:branch
  inputs:
    Choices:
    - NextStep: readMissing
      Variable: '{{ Mode }}'
      StringEquals: fail
    Default: finish
- name: readMissing
  action: aws:executeAwsApi
  onFailure: step:cleanup
  inputs:
    Service: ssm
    Api: GetParameter
    Name: '{{ ParamName }}-missing'
- name: cleanup
  action: aws:executeAwsApi
  isEnd: true
  inputs:
    Service: ssm
    Api: DeleteParameter
    Name: '{{ ParamName }}'
- name: finish
  action: aws:executeAwsApi
  isEnd: true
  inputs:
    Service: ssm
    Api: GetParameter
    Name: '{{ ParamName }}'
outputs:
- readParam.StoredValue
- putParam.Version
`

// TestSSM_AutomationRunsRunbook runs a runbook whose steps call AWS Systems
// Manager and Amazon S3 through aws:executeAwsApi, assert and wait on a
// property, sleep and branch, and checks the work the steps did, each step's
// status and outputs, and the execution's outputs. A second run takes the
// failing branch, whose onFailure step runs and leaves the execution Failed.
func TestSSM_AutomationRunsRunbook(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	doc := uniquename.New("automation-runbook")
	ssmCreateRunbook(t, c, doc, ssmTestRunbook)
	param := "/sdk/automation/" + doc
	bucket := strings.ToLower(uniquename.New("automation-bucket"))
	failingBucket := strings.ToLower(uniquename.New("automation-bucket"))
	t.Cleanup(func() {
		_, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(param)})
		for _, b := range []string{bucket, failingBucket} {
			_, _ = s3Client().DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(b)})
		}
	})

	start, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"ParamName": {param}, "BucketName": {bucket}, "Mode": {"pass"}},
	})
	require.NoError(t, err)
	id := aws.ToString(start.AutomationExecutionId)
	execution := ssmAwaitAutomation(t, c, id, ssmAutomationSettled)
	require.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, execution.AutomationExecutionStatus, aws.ToString(execution.FailureMessage))

	got, err := c.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(param)})
	require.NoError(t, err)
	assert.Equal(t, "first-value", aws.ToString(got.Parameter.Value))
	_, err = s3Client().HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err, "the runbook's CreateBucket step created the bucket")

	assert.Equal(t, []string{"first-value"}, execution.Outputs["readParam.StoredValue"])
	assert.Equal(t, []string{"1"}, execution.Outputs["putParam.Version"])
	assert.Equal(t, []string{"first-value"}, execution.Parameters["Value"], "a defaulted parameter reads back")
	for _, name := range []string{"putParam", "readParam", "assertParam", "makeBucket", "waitParam", "nap", "choose", "finish"} {
		step := ssmStepNamed(t, execution, name)
		assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, step.StepStatus, "step %s: %s", name, aws.ToString(step.FailureMessage))
		assert.NotNil(t, step.ExecutionStartTime, name)
		assert.NotNil(t, step.ExecutionEndTime, name)
	}
	assert.Equal(t, []string{"first-value"}, ssmStepNamed(t, execution, "readParam").Outputs["StoredValue"])
	assert.Equal(t, []string{"readMissing", "finish"}, ssmStepNamed(t, execution, "choose").ValidNextSteps)
	for _, name := range []string{"readMissing", "cleanup"} {
		assert.Equal(t, ssmtypes.AutomationExecutionStatusPending, ssmStepNamed(t, execution, name).StepStatus, "the branch skipped %s", name)
	}
	nap := ssmStepNamed(t, execution, "nap")
	assert.GreaterOrEqual(t, aws.ToTime(nap.ExecutionEndTime).Sub(aws.ToTime(nap.ExecutionStartTime)), time.Second, "aws:sleep waited its Duration")
	require.NotNil(t, execution.ProgressCounters)
	assert.Equal(t, int32(10), execution.ProgressCounters.TotalSteps)
	assert.Equal(t, int32(8), execution.ProgressCounters.SuccessSteps)

	steps, err := c.DescribeAutomationStepExecutions(ctx, &ssm.DescribeAutomationStepExecutionsInput{
		AutomationExecutionId: aws.String(id),
		Filters:               []ssmtypes.StepExecutionFilter{{Key: ssmtypes.StepExecutionFilterKeyStepExecutionStatus, Values: []string{"Pending"}}},
	})
	require.NoError(t, err)
	require.Len(t, steps.StepExecutions, 2)

	listed, err := c.DescribeAutomationExecutions(ctx, &ssm.DescribeAutomationExecutionsInput{
		Filters: []ssmtypes.AutomationExecutionFilter{{Key: ssmtypes.AutomationExecutionFilterKeyDocumentNamePrefix, Values: []string{doc}}},
	})
	require.NoError(t, err)
	require.Len(t, listed.AutomationExecutionMetadataList, 1)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, listed.AutomationExecutionMetadataList[0].AutomationExecutionStatus)

	failing, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"ParamName": {param}, "BucketName": {failingBucket}, "Mode": {"fail"}, "Value": {"second-value"}},
	})
	require.NoError(t, err)
	failed := ssmAwaitAutomation(t, c, aws.ToString(failing.AutomationExecutionId), ssmAutomationSettled)
	require.Equal(t, ssmtypes.AutomationExecutionStatusFailed, failed.AutomationExecutionStatus)
	missing := ssmStepNamed(t, failed, "readMissing")
	assert.Equal(t, ssmtypes.AutomationExecutionStatusFailed, missing.StepStatus)
	assert.Contains(t, aws.ToString(missing.FailureMessage), "ParameterNotFound")
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, ssmStepNamed(t, failed, "cleanup").StepStatus, "onFailure ran the cleanup step")
	assert.Equal(t, ssmtypes.AutomationExecutionStatusPending, ssmStepNamed(t, failed, "finish").StepStatus)
	_, err = c.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(param)})
	requireAWSErrorCode(t, err, "ParameterNotFound")
}

// TestSSM_AutomationStartRefusals checks the parameters and runbooks
// StartAutomationExecution refuses.
func TestSSM_AutomationStartRefusals(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	doc := uniquename.New("automation-refusals")
	ssmCreateRunbook(t, c, doc, ssmTestRunbook)

	_, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"ParamName": {"/x"}, "BucketName": {"b"}, "Mode": {"sideways"}},
	})
	requireAWSErrorCode(t, err, "InvalidAutomationExecutionParametersException")
	_, err = c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"BucketName": {"b"}, "Mode": {"pass"}},
	})
	requireAWSErrorCode(t, err, "InvalidAutomationExecutionParametersException")
	_, err = c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"ParamName": {"/x"}, "BucketName": {"b"}, "Mode": {"pass"}, "Undeclared": {"v"}},
	})
	requireAWSErrorCode(t, err, "InvalidAutomationExecutionParametersException")
	_, err = c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc), DocumentVersion: aws.String("7"),
		Parameters: map[string][]string{"ParamName": {"/x"}, "BucketName": {"b"}, "Mode": {"pass"}},
	})
	requireAWSErrorCode(t, err, "AutomationDefinitionVersionNotFoundException")

	command := uniquename.New("automation-command-doc")
	ssmEnsureDoc(t, c, command)
	_, err = c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{DocumentName: aws.String(command)})
	requireAWSErrorCode(t, err, "AutomationDefinitionNotFoundException")
}

// TestSSM_AutomationPauseResumeStop pauses an execution until a Resume signal
// names the step, then stops it in the middle of a long aws:sleep: the
// running step and the execution end Cancelled and the steps after it never
// run.
func TestSSM_AutomationPauseResumeStop(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	doc := uniquename.New("automation-pause")
	ssmCreateRunbook(t, c, doc, `schemaVersion: '0.3'
mainSteps:
- name: hold
  action: aws:pause
  inputs: {}
- name: longSleep
  action: aws:sleep
  inputs:
    Duration: PT1H
- name: after
  action: aws:sleep
  inputs:
    Duration: PT0S
`)
	start, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{DocumentName: aws.String(doc)})
	require.NoError(t, err)
	id := aws.ToString(start.AutomationExecutionId)

	waiting := ssmAwaitAutomation(t, c, id, func(e *ssmtypes.AutomationExecution) bool {
		return e.AutomationExecutionStatus == ssmtypes.AutomationExecutionStatusWaiting
	})
	assert.Equal(t, ssmtypes.AutomationExecutionStatusWaiting, ssmStepNamed(t, waiting, "hold").StepStatus)
	assert.Equal(t, "aws:pause", aws.ToString(waiting.CurrentAction))

	_, err = c.SendAutomationSignal(ctx, &ssm.SendAutomationSignalInput{
		AutomationExecutionId: aws.String(id), SignalType: ssmtypes.SignalTypeResume,
		Payload: map[string][]string{"StepName": {"longSleep"}},
	})
	requireAWSErrorCode(t, err, "AutomationStepNotFoundException")
	_, err = c.SendAutomationSignal(ctx, &ssm.SendAutomationSignalInput{
		AutomationExecutionId: aws.String(id), SignalType: ssmtypes.SignalTypeApprove,
	})
	requireAWSErrorCode(t, err, "InvalidAutomationSignalException")
	_, err = c.SendAutomationSignal(ctx, &ssm.SendAutomationSignalInput{
		AutomationExecutionId: aws.String(id), SignalType: ssmtypes.SignalTypeResume,
		Payload: map[string][]string{"StepName": {"hold"}},
	})
	require.NoError(t, err)

	sleeping := ssmAwaitAutomation(t, c, id, func(e *ssmtypes.AutomationExecution) bool {
		return ssmStepNamed(t, e, "longSleep").StepStatus == ssmtypes.AutomationExecutionStatusInprogress
	})
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, ssmStepNamed(t, sleeping, "hold").StepStatus)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusInprogress, sleeping.AutomationExecutionStatus)

	_, err = c.StopAutomationExecution(ctx, &ssm.StopAutomationExecutionInput{AutomationExecutionId: aws.String(id)})
	require.NoError(t, err)
	stopped := ssmAwaitAutomation(t, c, id, ssmAutomationSettled)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusCancelled, stopped.AutomationExecutionStatus)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusCancelled, ssmStepNamed(t, stopped, "longSleep").StepStatus)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusPending, ssmStepNamed(t, stopped, "after").StepStatus)

	_, err = c.StopAutomationExecution(ctx, &ssm.StopAutomationExecutionInput{AutomationExecutionId: aws.String(id)})
	requireAWSErrorCode(t, err, "InvalidAutomationStatusUpdateException")
	_, err = c.SendAutomationSignal(ctx, &ssm.SendAutomationSignalInput{
		AutomationExecutionId: aws.String(id), SignalType: ssmtypes.SignalTypeResume,
		Payload: map[string][]string{"StepName": {"hold"}},
	})
	requireAWSErrorCode(t, err, "InvalidAutomationSignalException")
}

// TestSSM_AutomationAssumeRole runs a runbook as the role its assumeRole
// names: a role Automation cannot assume fails the execution, and a step whose
// call the role's policy does not allow fails with the service's denial.
func TestSSM_AutomationAssumeRole(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	doc := uniquename.New("automation-assume-role")
	ssmCreateRunbook(t, c, doc, `schemaVersion: '0.3'
assumeRole: '{{ AutomationAssumeRole }}'
parameters:
  AutomationAssumeRole:
    type: String
  ParamName:
    type: String
mainSteps:
- name: put
  action: aws:executeAwsApi
  inputs:
    Service: ssm
    Api: PutParameter
    Name: '{{ ParamName }}'
    Value: by-role
    Type: String
- name: deleteDenied
  action: aws:executeAwsApi
  inputs:
    Service: ssm
    Api: DeleteParameter
    Name: '{{ ParamName }}'
`)
	param := "/sdk/automation/" + doc
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(param)}) })
	role := createServiceRole(t, uniquename.New("automation-role"), "ssm.amazonaws.com", "ssm:PutParameter")
	start, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"AutomationAssumeRole": {role}, "ParamName": {param}},
	})
	require.NoError(t, err)
	execution := ssmAwaitAutomation(t, c, aws.ToString(start.AutomationExecutionId), ssmAutomationSettled)
	require.Equal(t, ssmtypes.AutomationExecutionStatusFailed, execution.AutomationExecutionStatus)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, ssmStepNamed(t, execution, "put").StepStatus)
	assert.Contains(t, aws.ToString(ssmStepNamed(t, execution, "deleteDenied").FailureMessage), "AccessDenied")
	got, err := c.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(param)})
	require.NoError(t, err)
	assert.Equal(t, "by-role", aws.ToString(got.Parameter.Value))

	untrusted := createServiceRole(t, uniquename.New("automation-untrusted"), "lambda.amazonaws.com", "ssm:PutParameter")
	start, err = c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(doc),
		Parameters:   map[string][]string{"AutomationAssumeRole": {untrusted}, "ParamName": {param}},
	})
	require.NoError(t, err)
	execution = ssmAwaitAutomation(t, c, aws.ToString(start.AutomationExecutionId), ssmAutomationSettled)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusFailed, execution.AutomationExecutionStatus)
	assert.Equal(t, "The defined assume role is unable to be assumed.", aws.ToString(execution.FailureMessage))
}

// TestSSM_MaintenanceWindowAutomationTask runs an AUTOMATION task at the
// window's at() time: the task's role starts the runbook, whose step writes a
// parameter, and the invocation records the execution and its outcome.
func TestSSM_MaintenanceWindowAutomationTask(t *testing.T) {
	t.Parallel()
	c := ssmClient()
	doc := uniquename.New("mw-automation-runbook")
	ssmCreateRunbook(t, c, doc, `schemaVersion: '0.3'
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
    Value: from-window
    Type: String
`)
	param := "/sdk/automation/" + doc
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(param)}) })

	runAt := time.Now().UTC().Add(5 * time.Second).Truncate(time.Second)
	cw, err := c.CreateMaintenanceWindow(ctx, &ssm.CreateMaintenanceWindowInput{
		Name:                     aws.String(uniquename.New("mw-automation")),
		Schedule:                 aws.String("at(" + runAt.Format("2006-01-02T15:04:05") + ")"),
		Duration:                 aws.Int32(1),
		Cutoff:                   0,
		AllowUnassociatedTargets: true,
	})
	require.NoError(t, err)
	windowID := aws.ToString(cw.WindowId)
	t.Cleanup(func() {
		_, _ = c.DeleteMaintenanceWindow(ctx, &ssm.DeleteMaintenanceWindowInput{WindowId: aws.String(windowID)})
	})
	role := createServiceRole(t, uniquename.New("mw-automation-task"), "ssm.amazonaws.com", "ssm:StartAutomationExecution", "ssm:PutParameter")
	_, err = c.RegisterTaskWithMaintenanceWindow(ctx, &ssm.RegisterTaskWithMaintenanceWindowInput{
		WindowId:       aws.String(windowID),
		TaskArn:        aws.String(doc),
		TaskType:       ssmtypes.MaintenanceWindowTaskTypeAutomation,
		ServiceRoleArn: aws.String(role),
		Priority:       aws.Int32(1),
		TaskInvocationParameters: &ssmtypes.MaintenanceWindowTaskInvocationParameters{
			Automation: &ssmtypes.MaintenanceWindowAutomationParameters{
				Parameters: map[string][]string{"ParamName": {param}},
			},
		},
	})
	require.NoError(t, err)

	window := awsMaintenanceWindowExecution(t, c, windowID)
	require.Equal(t, ssmtypes.MaintenanceWindowExecutionStatusSuccess, window.Status, aws.ToString(window.StatusDetails))
	tasks, err := c.DescribeMaintenanceWindowExecutionTasks(ctx, &ssm.DescribeMaintenanceWindowExecutionTasksInput{WindowExecutionId: window.WindowExecutionId})
	require.NoError(t, err)
	require.Len(t, tasks.WindowExecutionTaskIdentities, 1)
	invocations, err := c.DescribeMaintenanceWindowExecutionTaskInvocations(ctx, &ssm.DescribeMaintenanceWindowExecutionTaskInvocationsInput{
		WindowExecutionId: window.WindowExecutionId, TaskId: tasks.WindowExecutionTaskIdentities[0].TaskExecutionId,
	})
	require.NoError(t, err)
	require.Len(t, invocations.WindowExecutionTaskInvocationIdentities, 1)
	executionID := aws.ToString(invocations.WindowExecutionTaskInvocationIdentities[0].ExecutionId)
	require.NotEmpty(t, executionID)

	execution, err := c.GetAutomationExecution(ctx, &ssm.GetAutomationExecutionInput{AutomationExecutionId: aws.String(executionID)})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.AutomationExecutionStatusSuccess, execution.AutomationExecution.AutomationExecutionStatus)
	assert.Contains(t, aws.ToString(execution.AutomationExecution.ExecutedBy), ":assumed-role/")
	got, err := c.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(param)})
	require.NoError(t, err)
	assert.Equal(t, "from-window", aws.ToString(got.Parameter.Value))
}
