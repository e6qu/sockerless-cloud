package aws_sdk_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/e6qu/sockerless-cloud/testutil/simready"
	"github.com/stretchr/testify/require"
)

// shutdownLog keeps a simulator's stderr and closes closed once the line that
// ends an orderly shutdown arrives.
type shutdownLog struct {
	mu     sync.Mutex
	text   bytes.Buffer
	closed chan struct{}
	once   sync.Once
}

func (l *shutdownLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = os.Stderr.Write(p)
	l.text.Write(p)
	if strings.Contains(l.text.String(), "shutdown: database closed") {
		l.once.Do(func() { close(l.closed) })
	}
	return len(p), nil
}

func (l *shutdownLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// A deployed simulator, which keeps its state and its workloads across a
// restart, stops without waiting out the work its services have in flight: an
// Amazon ECS task stop in its container's two-minute stopTimeout and an AWS
// Lambda asynchronous invocation inside a 15-minute function timeout. Both
// belong to the next process, which finishes the stop and retries the
// invocation; the stopping one only has to let go of them.
func TestSimulatorStopsWithLifecycleWorkInFlight_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)

	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		"SIM_AWS_PORT="+strconv.Itoa(tcpPort),
		"SIM_DNS_PORT="+strconv.Itoa(udpPort),
		"SIM_RUNTIME=docker",
		"SIM_PERSIST=true",
		"SIM_DATA_DIR="+stateDir,
		"SIM_LOG_LEVEL=info",
	)
	cmd.Stdout = os.Stdout
	output := &shutdownLog{closed: make(chan struct{})}
	require.NoError(t, simready.Start(cmd, output))
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	cfg := persistentSDKConfig()
	ecsAPI := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	lambdaAPI := lambda.NewFromConfig(cfg, func(o *lambda.Options) { o.BaseEndpoint = aws.String(endpoint) })
	logsAPI := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("http://logs.localhost:%d", tcpPort))
	})
	testCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const cluster = "shutdown-in-flight"
	_, err := ecsAPI.CreateCluster(testCtx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	// busybox's sleep installs no SIGTERM handler, and as a container's first
	// process it therefore ignores the signal: the stop runs to its timeout.
	definition, err := ecsAPI.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family:      aws.String("shutdown-in-flight"),
		NetworkMode: ecstypes.NetworkModeBridge,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:        aws.String("app"),
			Image:       aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Command:     []string{"sleep", "3600"},
			Essential:   aws.Bool(true),
			StopTimeout: aws.Int32(120),
		}},
	})
	require.NoError(t, err)
	run, err := ecsAPI.RunTask(testCtx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: definition.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	taskID := taskArn[strings.LastIndexByte(taskArn, '/')+1:]
	t.Cleanup(func() { removeContainersLabelled(t, "sockerless-sim-task="+taskID) })
	require.NoError(t, ecs.NewTasksRunningWaiter(ecsAPI, func(o *ecs.TasksRunningWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(testCtx, &ecs.DescribeTasksInput{Cluster: aws.String(cluster), Tasks: []string{taskArn}}, time.Minute))
	stopping, err := ecsAPI.StopTask(testCtx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(taskArn)})
	require.NoError(t, err)
	require.Equal(t, "STOPPED", aws.ToString(stopping.Task.DesiredStatus))
	require.NotEqual(t, "STOPPED", aws.ToString(stopping.Task.LastStatus))

	const functionName = "shutdown-in-flight"
	_, err = lambdaAPI.CreateFunction(testCtx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(functionName),
		Role:          aws.String("arn:aws:iam::123456789012:role/shutdown-in-flight"),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(containerCommandImage)},
		ImageConfig:   &lambdatypes.ImageConfig{Command: []string{"hold"}},
		Architectures: nativeLambdaArchitectures(),
		Timeout:       aws.Int32(900),
	})
	require.NoError(t, err)
	logGroup := "/aws/lambda/" + functionName
	_, err = logsAPI.CreateLogGroup(testCtx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
	require.NoError(t, err)
	groups, err := logsAPI.DescribeLogGroups(testCtx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, groups.LogGroups, 1)
	tail, err := logsAPI.StartLiveTail(testCtx, &cloudwatchlogs.StartLiveTailInput{
		LogGroupIdentifiers: []string{aws.ToString(groups.LogGroups[0].Arn)},
	})
	require.NoError(t, err)
	tailStream := tail.GetStream()
	first, ok := <-tailStream.Events()
	require.True(t, ok, "Live Tail closed before its sessionStart: %v", tailStream.Err())
	require.IsType(t, &cwltypes.StartLiveTailResponseStreamMemberSessionStart{}, first)
	invoked, err := lambdaAPI.Invoke(testCtx, &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        []byte(`{"source":"shutdown-test"}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 202, invoked.StatusCode)
	awaitLiveTailMessage(t, tailStream, "START RequestId:")
	// The Live Tail session is a request the HTTP server's drain would wait on;
	// this test measures the background workers, so it ends the session first.
	require.NoError(t, tailStream.Close())

	signalled := time.Now()
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("the simulator did not stop within 30 seconds of SIGTERM:\n%s", output.String())
	}
	took := time.Since(signalled)
	select {
	case <-output.closed:
	case <-time.After(10 * time.Second):
		t.Fatalf("the simulator exited without reporting an orderly shutdown:\n%s", output.String())
	}
	log := output.String()
	require.Contains(t, log, "shutdown: background workers returned")
	require.NotContains(t, log, "still waiting for background workers",
		"the shutdown waited on a background worker")
	require.Less(t, took, 5*time.Second, "SIGTERM to exit took %s", took)
}

// removeContainersLabelled force-removes the containers carrying label, the
// workloads a persistent simulator leaves running when it stops.
func removeContainersLabelled(t *testing.T, label string) {
	t.Helper()
	ids, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+label).Output()
	require.NoError(t, err)
	for _, id := range strings.Fields(string(ids)) {
		if out, err := exec.Command("docker", "rm", "-f", id).CombinedOutput(); err != nil {
			t.Errorf("remove container %s: %v: %s", id, err, out)
		}
	}
}
