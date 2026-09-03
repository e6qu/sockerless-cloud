package aws_sdk_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	codebuildtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workloadRestartBudget is how long each half of this test waits for three
// real container workloads — an Amazon ECS task, an AWS Batch job and an AWS
// CodeBuild build whose buildspec sleeps eight seconds — to reach their state.
// The budget is sized from measurement rather than from the idle case: the
// whole test takes about fifteen seconds on an unloaded engine and about
// twenty-five with one other container suite running beside it, so a thirty
// second budget has almost no headroom on a host running two. It still fails
// loudly on a recovery that never happens, which is what this test guards.
const workloadRestartBudget = 90 * time.Second

func TestRunningAmazonECSAWSBatchAndCodeBuildWorkloadsSurviveSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	ecsAPI := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	batchAPI := batch.NewFromConfig(cfg, func(o *batch.Options) { o.BaseEndpoint = aws.String(endpoint) })
	codeBuildAPI := codebuild.NewFromConfig(cfg, func(o *codebuild.Options) { o.BaseEndpoint = aws.String(endpoint) })
	testCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const (
		clusterName = "persistent-workload-cluster"
		familyName  = "persistent-workload-task"
		batchCEName = "persistent-workload-compute"
		batchQName  = "persistent-workload-queue"
		batchJDName = "persistent-workload-job"
		projectName = "persistent-workload-build"
	)
	_, err := ecsAPI.CreateCluster(testCtx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	_, err = ecsAPI.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(familyName),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("application"),
			Image:     aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Essential: aws.Bool(true),
			Command:   []string{"sh", "-c", "sleep 8"},
		}},
	})
	require.NoError(t, err)
	task, err := ecsAPI.RunTask(testCtx, &ecs.RunTaskInput{
		Cluster: aws.String(clusterName), TaskDefinition: aws.String(familyName),
	})
	require.NoError(t, err)
	require.Len(t, task.Tasks, 1)
	taskARN := aws.ToString(task.Tasks[0].TaskArn)

	_, err = batchAPI.CreateComputeEnvironment(testCtx, &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String(batchCEName),
		Type:                   batchtypes.CETypeManaged,
		State:                  batchtypes.CEStateEnabled,
	})
	require.NoError(t, err)
	computeEnvironmentARN := "arn:aws:batch:us-east-1:123456789012:compute-environment/" + batchCEName
	_, err = batchAPI.CreateJobQueue(testCtx, &batch.CreateJobQueueInput{
		JobQueueName: aws.String(batchQName),
		State:        batchtypes.JQStateEnabled,
		Priority:     aws.Int32(10),
		ComputeEnvironmentOrder: []batchtypes.ComputeEnvironmentOrder{{
			Order: aws.Int32(1), ComputeEnvironment: aws.String(computeEnvironmentARN),
		}},
	})
	require.NoError(t, err)
	jobDefinition, err := batchAPI.RegisterJobDefinition(testCtx, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String(batchJDName),
		Type:              batchtypes.JobDefinitionTypeContainer,
		ContainerProperties: &batchtypes.ContainerProperties{
			Image:   aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Command: []string{"sh", "-c", "sleep 8"},
			Vcpus:   aws.Int32(1),
			Memory:  aws.Int32(512),
		},
	})
	require.NoError(t, err)
	submittedJob, err := batchAPI.SubmitJob(testCtx, &batch.SubmitJobInput{
		JobName:       aws.String("persistent-running-job"),
		JobQueue:      aws.String(batchQName),
		JobDefinition: jobDefinition.JobDefinitionArn,
	})
	require.NoError(t, err)
	jobID := aws.ToString(submittedJob.JobId)

	_, err = codeBuildAPI.CreateProject(testCtx, &codebuild.CreateProjectInput{
		Name: aws.String(projectName),
		Source: &codebuildtypes.ProjectSource{
			Type:      codebuildtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - sleep 8\n"),
		},
		Artifacts: &codebuildtypes.ProjectArtifacts{Type: codebuildtypes.ArtifactsTypeNoArtifacts},
		Environment: &codebuildtypes.ProjectEnvironment{
			Type:        codebuildtypes.EnvironmentTypeLinuxContainer,
			Image:       aws.String("public.ecr.aws/docker/library/busybox:latest"),
			ComputeType: codebuildtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/persistent-codebuild"),
	})
	require.NoError(t, err)
	build, err := codeBuildAPI.StartBuild(testCtx, &codebuild.StartBuildInput{
		ProjectName: aws.String(projectName),
	})
	require.NoError(t, err)
	buildID := aws.ToString(build.Build.Id)

	var observed workloadObservation
	require.Eventually(t, func() bool {
		tasks, taskErr := ecsAPI.DescribeTasks(testCtx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName), Tasks: []string{taskARN},
		})
		jobs, jobErr := batchAPI.DescribeJobs(testCtx, &batch.DescribeJobsInput{Jobs: []string{jobID}})
		builds, buildErr := codeBuildAPI.BatchGetBuilds(testCtx, &codebuild.BatchGetBuildsInput{
			Ids: []string{buildID},
		})
		observed = workloadStates(tasks, taskErr, jobs, jobErr, builds, buildErr)
		return taskErr == nil && jobErr == nil && buildErr == nil &&
			len(tasks.Tasks) == 1 && aws.ToString(tasks.Tasks[0].LastStatus) == "RUNNING" &&
			len(jobs.Jobs) == 1 && jobs.Jobs[0].Status == batchtypes.JobStatusRunning &&
			len(builds.Builds) == 1 && builds.Builds[0].BuildStatus == codebuildtypes.StatusTypeInProgress
	}, workloadRestartBudget, 100*time.Millisecond,
		"the three workloads never all reached their running state; last seen: %s", &observed)

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	ecsAPI = ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	batchAPI = batch.NewFromConfig(cfg, func(o *batch.Options) { o.BaseEndpoint = aws.String(endpoint) })
	codeBuildAPI = codebuild.NewFromConfig(cfg, func(o *codebuild.Options) { o.BaseEndpoint = aws.String(endpoint) })

	require.Eventually(t, func() bool {
		tasks, taskErr := ecsAPI.DescribeTasks(testCtx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName), Tasks: []string{taskARN},
		})
		jobs, jobErr := batchAPI.DescribeJobs(testCtx, &batch.DescribeJobsInput{Jobs: []string{jobID}})
		builds, buildErr := codeBuildAPI.BatchGetBuilds(testCtx, &codebuild.BatchGetBuildsInput{
			Ids: []string{buildID},
		})
		observed = workloadStates(tasks, taskErr, jobs, jobErr, builds, buildErr)
		return taskErr == nil && jobErr == nil && buildErr == nil &&
			len(tasks.Tasks) == 1 && aws.ToString(tasks.Tasks[0].LastStatus) == "STOPPED" &&
			len(tasks.Tasks[0].Containers) == 1 && aws.ToInt32(tasks.Tasks[0].Containers[0].ExitCode) == 0 &&
			len(jobs.Jobs) == 1 && jobs.Jobs[0].Status == batchtypes.JobStatusSucceeded &&
			aws.ToInt32(jobs.Jobs[0].Container.ExitCode) == 0 &&
			len(builds.Builds) == 1 && builds.Builds[0].BuildStatus == codebuildtypes.StatusTypeSucceeded
	}, workloadRestartBudget, 100*time.Millisecond,
		"the three workloads never all completed after the restart; last seen: %s", &observed)

	tasks, err := ecsAPI.DescribeTasks(testCtx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName), Tasks: []string{taskARN},
	})
	require.NoError(t, err)
	assert.Equal(t, "Essential container in task exited", aws.ToString(tasks.Tasks[0].StoppedReason))
}

// workloadObservation is the last state the restart test saw from each of the
// three services, so a wait that runs out of budget says what it was waiting on
// rather than only that it waited. A timeout here has meant an engine too
// loaded to start containers as often as it has meant a recovery that failed,
// and the two read identically without this.
type workloadObservation struct{ text string }

func (o *workloadObservation) String() string {
	if o.text == "" {
		return "nothing — no describe call returned before the budget ran out"
	}
	return o.text
}

func workloadStates(
	tasks *ecs.DescribeTasksOutput, taskErr error,
	jobs *batch.DescribeJobsOutput, jobErr error,
	builds *codebuild.BatchGetBuildsOutput, buildErr error,
) workloadObservation {
	describe := func(err error, state string) string {
		if err != nil {
			return "error: " + err.Error()
		}
		if state == "" {
			return "absent"
		}
		return state
	}
	task, job, build := "", "", ""
	if taskErr == nil && len(tasks.Tasks) == 1 {
		task = aws.ToString(tasks.Tasks[0].LastStatus)
	}
	if jobErr == nil && len(jobs.Jobs) == 1 {
		job = string(jobs.Jobs[0].Status)
	}
	if buildErr == nil && len(builds.Builds) == 1 {
		build = string(builds.Builds[0].BuildStatus)
	}
	return workloadObservation{text: fmt.Sprintf(
		"Amazon ECS task %s, AWS Batch job %s, AWS CodeBuild build %s",
		describe(taskErr, task), describe(jobErr, job), describe(buildErr, build))}
}
