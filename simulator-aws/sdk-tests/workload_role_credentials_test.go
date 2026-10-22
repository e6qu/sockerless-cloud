package aws_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exportDefaultChainCredentials makes the AWS CLI resolve credentials through
// its default chain inside the workload and print them as a credential
// process hands them over: the session an AWS SDK in the workload signs with.
var exportDefaultChainCredentials = []string{"aws", "configure", "export-credentials", "--format", "process"}

// awsCLIImage is the image the AWS CLI runs from in a workload.
const awsCLIImage = "mirror.gcr.io/amazon/aws-cli:2.27.49"

// createWorkloadRole creates a role the service principal may assume.
func createWorkloadRole(t *testing.T, name, principal string) string {
	t.Helper()
	iamc := iamClient()
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(name),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` +
			principal + `"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
	return aws.ToString(role.Role.Arn)
}

// assertRoleSession reads the credentials a workload printed in the
// credential process format and checks they are a live session of roleArn
// named sessionName.
func assertRoleSession(t *testing.T, output, roleArn, sessionName string) {
	t.Helper()
	start, end := strings.Index(output, "{"), strings.LastIndex(output, "}")
	require.True(t, start >= 0 && end > start, "the workload printed no credentials:\n%s", output)
	var cred struct {
		Version                                                int
		AccessKeyId, SecretAccessKey, SessionToken, Expiration string
	}
	require.NoError(t, json.Unmarshal([]byte(output[start:end+1]), &cred), output)
	assert.Equal(t, 1, cred.Version)
	expiration, err := time.Parse(time.RFC3339, cred.Expiration)
	require.NoError(t, err)
	assert.True(t, expiration.After(time.Now()))

	session := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.Credentials = credentials.NewStaticCredentialsProvider(cred.AccessKeyId, cred.SecretAccessKey, cred.SessionToken)
	})
	identity, err := session.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	parts := strings.Split(roleArn, ":")
	roleName := parts[5][strings.LastIndex(parts[5], "/")+1:]
	assert.Equal(t, "arn:aws:sts::"+parts[4]+":assumed-role/"+roleName+"/"+sessionName, aws.ToString(identity.Arn))
}

func logStreamText(t *testing.T, group, stream string) string {
	t.Helper()
	out, err := cwLogsClient().GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	var lines []string
	for _, event := range out.Events {
		lines = append(lines, aws.ToString(event.Message))
	}
	return strings.Join(lines, "\n")
}

// A build's AWS CLI resolves sessions of its service role, named for the
// build, through its default credential chain: the project's role, or the
// role the build names in serviceRoleOverride, which a retry of the build
// keeps.
func TestCodeBuild_BuildHoldsItsServiceRoleSession_SDK(t *testing.T) {
	t.Parallel()
	c := codebuildClient()
	name := uniqueName("cb-role-session")
	projectRole := createWorkloadRole(t, name, "codebuild.amazonaws.com")
	overrideRole := createWorkloadRole(t, name+"-override", "codebuild.amazonaws.com")
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(name),
		Source: &cbtypes.ProjectSource{
			Type:      cbtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - " + strings.Join(exportDefaultChainCredentials, " ") + "\n"),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String(awsCLIImage),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String(projectRole),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(context.Background(), &codebuild.DeleteProjectInput{Name: aws.String(name)})
	})

	assertBuildSession := func(buildID, roleArn string) {
		t.Helper()
		var build cbtypes.Build
		require.Eventually(t, func() bool {
			out, err := c.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{buildID}})
			require.NoError(t, err)
			build = out.Builds[0]
			return build.BuildStatus != cbtypes.StatusTypeInProgress
		}, cbBuildCompletionBudget, 100*time.Millisecond)
		require.Equal(t, cbtypes.StatusTypeSucceeded, build.BuildStatus)
		uuid := buildID[strings.LastIndex(buildID, ":")+1:]
		assertRoleSession(t, logStreamText(t, "/aws/codebuild/"+name, aws.ToString(build.Logs.StreamName)), roleArn, "AWSCodeBuild-"+uuid)
	}

	plain, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(name)})
	require.NoError(t, err)
	overridden, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(name), ServiceRoleOverride: aws.String(overrideRole)})
	require.NoError(t, err)
	assertBuildSession(aws.ToString(plain.Build.Id), projectRole)
	assertBuildSession(aws.ToString(overridden.Build.Id), overrideRole)

	retried, err := c.RetryBuild(ctx, &codebuild.RetryBuildInput{Id: overridden.Build.Id})
	require.NoError(t, err)
	assertBuildSession(aws.ToString(retried.Build.Id), overrideRole)
}

// A batch build that names a serviceRoleOverride runs as that role, not as its
// project's: its build command succeeds only when the AWS CLI's caller is a
// session of the override role.
func TestCodeBuild_BuildBatchRunsAsItsServiceRoleOverride_SDK(t *testing.T) {
	t.Parallel()
	c := codebuildClient()
	name := uniqueName("cb-batch-role")
	projectRole := createWorkloadRole(t, name, "codebuild.amazonaws.com")
	overrideRole := createWorkloadRole(t, name+"-override", "codebuild.amazonaws.com")
	command := "aws sts get-caller-identity --query Arn --output text | grep -q 'assumed-role/" + name + "-override/'"
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(name),
		Source: &cbtypes.ProjectSource{
			Type:      cbtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - \"" + command + "\"\n"),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String(awsCLIImage),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String(projectRole),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(context.Background(), &codebuild.DeleteProjectInput{Name: aws.String(name)})
	})

	started, err := c.StartBuildBatch(ctx, &codebuild.StartBuildBatchInput{ProjectName: aws.String(name), ServiceRoleOverride: aws.String(overrideRole)})
	require.NoError(t, err)
	assert.Equal(t, overrideRole, aws.ToString(started.BuildBatch.ServiceRole))
	var batch cbtypes.BuildBatch
	require.Eventually(t, func() bool {
		out, err := c.BatchGetBuildBatches(ctx, &codebuild.BatchGetBuildBatchesInput{Ids: []string{aws.ToString(started.BuildBatch.Id)}})
		require.NoError(t, err)
		batch = out.BuildBatches[0]
		return batch.BuildBatchStatus != cbtypes.StatusTypeInProgress
	}, cbBuildCompletionBudget, 100*time.Millisecond)
	require.Equal(t, cbtypes.StatusTypeSucceeded, batch.BuildBatchStatus)
	assert.Equal(t, overrideRole, aws.ToString(batch.ServiceRole))
}

// A job's AWS CLI resolves sessions of its job definition's job role through
// its default credential chain, as an Amazon ECS task's resolves its task
// role.
func TestBatch_JobHoldsItsJobRoleSession_SDK(t *testing.T) {
	t.Parallel()
	c := batchClient()
	name := uniqueName("batch-role-session")
	roleArn := createWorkloadRole(t, name, "ecs-tasks.amazonaws.com")
	queue := createBatchQueue(t, c, name, batchtypes.CEStateEnabled, 4)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String(name),
		ContainerProperties: &batchtypes.ContainerProperties{
			Image:      aws.String(awsCLIImage),
			Command:    exportDefaultChainCredentials[1:],
			JobRoleArn: aws.String(roleArn),
		},
	})
	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String(name), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
	})
	require.NoError(t, err)
	job := awaitBatchJob(t, c, aws.ToString(submit.JobId), batchJobSettled)
	require.Equal(t, batchtypes.JobStatusSucceeded, job.Status, aws.ToString(job.StatusReason))
	stream := aws.ToString(job.Container.LogStreamName)
	assertRoleSession(t, batchAttemptLog(t, stream), roleArn, stream[strings.LastIndex(stream, "/")+1:])
}

// A Python shell job run reads sessions of the job's role, which AWS Glue
// names GlueJobRunnerSession, from the container credentials endpoint its
// environment names. The job's image carries no AWS SDK for Python, so the
// script reads the endpoint itself.
func TestGlue_JobRunHoldsItsRoleSession_SDK(t *testing.T) {
	t.Parallel()
	ensureGluePythonShellImage(t)
	c := glueClient()
	s3c := s3Client()
	name := uniqueName("glue-role-session")
	roleArn := createWorkloadRole(t, name, "glue.amazonaws.com")
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = s3c.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(name), Key: aws.String("script.py")})
		_, _ = s3c.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(name)})
	})
	script := `import json, os, urllib.request
relative = os.environ.get("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
uri = "http://169.254.170.2" + relative if relative else os.environ["AWS_CONTAINER_CREDENTIALS_FULL_URI"]
d = json.load(urllib.request.urlopen(uri))
print(json.dumps({"Version": 1, "AccessKeyId": d["AccessKeyId"], "SecretAccessKey": d["SecretAccessKey"], "SessionToken": d["Token"], "Expiration": d["Expiration"]}))
`
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(name), Key: aws.String("script.py"), Body: bytes.NewReader([]byte(script))})
	require.NoError(t, err)
	_, err = c.CreateJob(ctx, &glue.CreateJobInput{
		Name: aws.String(name), Role: aws.String(roleArn),
		Command: &gluetypes.JobCommand{Name: aws.String("pythonshell"), ScriptLocation: aws.String("s3://" + name + "/script.py")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteJob(context.Background(), &glue.DeleteJobInput{JobName: aws.String(name)}) })

	run, err := c.StartJobRun(ctx, &glue.StartJobRunInput{JobName: aws.String(name)})
	require.NoError(t, err)
	var state *glue.GetJobRunOutput
	require.Eventually(t, func() bool {
		state, err = c.GetJobRun(ctx, &glue.GetJobRunInput{JobName: aws.String(name), RunId: run.JobRunId})
		require.NoError(t, err)
		return state.JobRun.JobRunState == gluetypes.JobRunStateSucceeded || state.JobRun.JobRunState == gluetypes.JobRunStateFailed
	}, glueJobRunCompletionBudget, 100*time.Millisecond)
	require.Equal(t, gluetypes.JobRunStateSucceeded, state.JobRun.JobRunState, aws.ToString(state.JobRun.ErrorMessage))
	assertRoleSession(t, logStreamText(t, "/aws-glue/jobs/output", aws.ToString(run.JobRunId)), roleArn, "GlueJobRunnerSession")
}
