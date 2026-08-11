package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestECS_CLI_ServiceFamily(t *testing.T) {
	cluster := "cli-ecs-svc-cluster"
	subnetID := createCLIECSTestSubnet(t, 141)
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-svc-task",
		"--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE",
		"--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"app","image":"`+containerCommandImage+`","command":["hold"],"essential":true}]`))

	// PutClusterCapacityProviders → DescribeClusters echoes them.
	runCLI(t, awsCLI("ecs", "put-cluster-capacity-providers",
		"--cluster", cluster,
		"--capacity-providers", "FARGATE", "FARGATE_SPOT",
		"--default-capacity-provider-strategy", "capacityProvider=FARGATE,weight=1,base=1"))
	descCl := runCLI(t, awsCLI("ecs", "describe-clusters", "--clusters", cluster, "--output", "json"))
	var cl struct {
		Clusters []struct {
			CapacityProviders []string `json:"capacityProviders"`
		} `json:"clusters"`
	}
	parseJSON(t, descCl, &cl)
	require.Len(t, cl.Clusters, 1)
	assert.ElementsMatch(t, []string{"FARGATE", "FARGATE_SPOT"}, cl.Clusters[0].CapacityProviders)

	// CreateService → ACTIVE.
	createOut := runCLI(t, awsCLI("ecs", "create-service",
		"--cluster", cluster, "--service-name", "cli-svc",
		"--task-definition", "cli-svc-task", "--desired-count", "2",
		"--launch-type", "FARGATE",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json"))
	var created struct {
		Service struct {
			Status       string `json:"status"`
			DesiredCount int    `json:"desiredCount"`
		} `json:"service"`
	}
	parseJSON(t, createOut, &created)
	assert.Equal(t, "ACTIVE", created.Service.Status)
	// RunningCount converges asynchronously via the service scheduler; the
	// synchronous read-back is DesiredCount.
	assert.Equal(t, 2, created.Service.DesiredCount)

	descOut := runCLI(t, awsCLI("ecs", "describe-services",
		"--cluster", cluster, "--services", "cli-svc", "--output", "json"))
	var desc struct {
		Services []struct {
			Status string `json:"status"`
		} `json:"services"`
	}
	parseJSON(t, descOut, &desc)
	require.Len(t, desc.Services, 1)
	assert.Equal(t, "ACTIVE", desc.Services[0].Status)

	var running struct {
		TaskArns []string `json:"taskArns"`
	}
	require.Eventually(t, func() bool {
		out := runCLI(t, awsCLI("ecs", "list-tasks",
			"--cluster", cluster, "--service-name", "cli-svc",
			"--desired-status", "RUNNING", "--output", "json"))
		parseJSON(t, out, &running)
		return len(running.TaskArns) == 2
	}, 30*time.Second, 100*time.Millisecond, "service did not launch two real tasks")
	stoppedArn := running.TaskArns[0]
	runCLI(t, awsCLI("ecs", "stop-task", "--cluster", cluster, "--task", stoppedArn))
	require.Eventually(t, func() bool {
		out := runCLI(t, awsCLI("ecs", "list-tasks",
			"--cluster", cluster, "--service-name", "cli-svc",
			"--desired-status", "RUNNING", "--output", "json"))
		parseJSON(t, out, &running)
		return len(running.TaskArns) == 2 &&
			running.TaskArns[0] != stoppedArn &&
			running.TaskArns[1] != stoppedArn
	}, 30*time.Second, 100*time.Millisecond, "service did not replace the stopped task")

	delOut := runCLI(t, awsCLI("ecs", "delete-service",
		"--cluster", cluster, "--service", "cli-svc", "--force", "--output", "json"))
	var del struct {
		Service struct {
			Status string `json:"status"`
		} `json:"service"`
	}
	parseJSON(t, delOut, &del)
	assert.Equal(t, "INACTIVE", del.Service.Status)
}

func cleanupCLIService(t *testing.T, cluster, service string) {
	t.Helper()
	t.Cleanup(func() {
		_ = awsCLI("ecs", "update-service", "--cluster", cluster, "--service", service, "--desired-count", "0").Run()
		_ = awsCLI("ecs", "delete-service", "--cluster", cluster, "--service", service, "--force").Run()
	})
}

func TestECS_CLI_RunTaskAndCheckLogs(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 142)

	// Create cluster
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-ecs-cluster"))

	// Register task definition with echo command and awslogs
	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ecs-task",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", `[{
			"name": "app",
			"image": "alpine:latest",
			"command": ["echo", "hello-from-ecs"],
			"logConfiguration": {
				"logDriver": "awslogs",
				"options": {
					"awslogs-group": "/ecs/cli-task",
					"awslogs-stream-prefix": "ecs"
				}
			}
		}]`,
		"--output", "json",
	))

	var tdResult struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &tdResult)
	require.NotEmpty(t, tdResult.TaskDefinition.TaskDefinitionArn)

	// Run task
	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-ecs-cluster",
		"--task-definition", tdResult.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--count", "1",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json",
	))

	var runResult struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	taskArn := runResult.Tasks[0].TaskArn
	cleanupCLIECSTask(t, "cli-ecs-cluster", taskArn)

	// Poll until the task reaches STOPPED; netns setup on CI can make a fixed
	// sleep race the real container lifecycle.
	out = pollECSTaskStopped(t, "cli-ecs-cluster", taskArn)

	var descResult struct {
		Tasks []struct {
			LastStatus string `json:"lastStatus"`
			Containers []struct {
				ExitCode *int `json:"exitCode"`
			} `json:"containers"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.Tasks, 1)
	assert.Equal(t, "STOPPED", descResult.Tasks[0].LastStatus)
	require.NotEmpty(t, descResult.Tasks[0].Containers)
	require.NotNil(t, descResult.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, 0, *descResult.Tasks[0].Containers[0].ExitCode)

	// Verify CloudWatch logs contain the real output
	out = runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", "/ecs/cli-task",
		"--output", "json",
	))

	var logResult struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &logResult)
	require.NotEmpty(t, logResult.Events)

	found := false
	for _, e := range logResult.Events {
		if strings.Contains(e.Message, "hello-from-ecs") {
			found = true
		}
	}
	assert.True(t, found, "expected 'hello-from-ecs' in CloudWatch logs")
}

func TestECS_CLI_RunTaskContainerOverrideEnvironment(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 148)
	cluster := "cli-ecs-override"
	logGroup := "/ecs/cli-override"

	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ecs-override-task",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", `[{
			"name": "workspace",
			"image": "alpine:latest",
			"command": ["sh", "-c", "echo taskdef:${EDD_WORKSPACE_ID:-missing}:${BASE_ONLY}:${OVERRIDE_ME}"],
			"environment": [
				{"name": "BASE_ONLY", "value": "from-task-definition"},
				{"name": "OVERRIDE_ME", "value": "from-task-definition"}
			],
			"logConfiguration": {
				"logDriver": "awslogs",
				"options": {
					"awslogs-group": "`+logGroup+`",
					"awslogs-stream-prefix": "ecs"
				}
			}
		}]`,
		"--output", "json",
	))
	var tdResult struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &tdResult)
	require.NotEmpty(t, tdResult.TaskDefinition.TaskDefinitionArn)

	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", cluster,
		"--task-definition", tdResult.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--overrides", `{
			"cpu": "512",
			"memory": "1024",
			"containerOverrides": [{
				"name": "workspace",
				"command": ["sh", "-c", "echo override:${EDD_WORKSPACE_ID}:${BASE_ONLY}:${OVERRIDE_ME}"],
				"environment": [
					{"name": "EDD_WORKSPACE_ID", "value": "ws-cli"},
					{"name": "OVERRIDE_ME", "value": "from-runtask"}
				]
			}]
		}`,
		"--output", "json",
	))
	var runResult struct {
		Tasks []struct {
			TaskArn   string `json:"taskArn"`
			Cpu       string `json:"cpu"`
			Memory    string `json:"memory"`
			Overrides struct {
				ContainerOverrides []struct {
					Name        string `json:"name"`
					Environment []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"environment"`
				} `json:"containerOverrides"`
			} `json:"overrides"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	assert.Equal(t, "512", runResult.Tasks[0].Cpu)
	assert.Equal(t, "1024", runResult.Tasks[0].Memory)
	require.Len(t, runResult.Tasks[0].Overrides.ContainerOverrides, 1)
	assert.Equal(t, "workspace", runResult.Tasks[0].Overrides.ContainerOverrides[0].Name)
	taskArn := runResult.Tasks[0].TaskArn
	cleanupCLIECSTask(t, cluster, taskArn)

	pollECSTaskStopped(t, cluster, taskArn)

	require.Eventually(t, func() bool {
		logOut := runCLI(t, awsCLI("logs", "filter-log-events",
			"--log-group-name", logGroup,
			"--output", "json",
		))
		var logResult struct {
			Events []struct {
				Message string `json:"message"`
			} `json:"events"`
		}
		parseJSON(t, logOut, &logResult)
		for _, e := range logResult.Events {
			if strings.Contains(e.Message, "override:ws-cli:from-task-definition:from-runtask") {
				return true
			}
		}
		return false
	}, 10*time.Second, 500*time.Millisecond)
}

func TestECS_CLI_ExecuteCommandRejectedWhenNotEnabled(t *testing.T) {
	if _, err := exec.LookPath("session-manager-plugin"); err != nil {
		t.Fatalf("session-manager-plugin is required because aws ecs execute-command checks it before calling the API; install it (the CI job installs the .deb): %v", err)
	}

	subnetID := createCLIECSTestSubnet(t, 145)

	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-ecs-exec-disabled"))
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ecs-exec-disabled",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", `[{
			"name": "app",
			"image": "public.ecr.aws/docker/library/busybox:latest",
			"entryPoint": ["sh", "-c"],
			"command": ["sleep 30"]
		}]`,
		"--output", "json",
	))

	out := runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-ecs-exec-disabled",
		"--task-definition", "cli-ecs-exec-disabled",
		"--launch-type", "FARGATE",
		"--count", "1",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json",
	))
	var runResult struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	taskArn := runResult.Tasks[0].TaskArn
	cleanupCLIECSTask(t, "cli-ecs-exec-disabled", taskArn)
	waitCLITaskStatus(t, "cli-ecs-exec-disabled", taskArn, "RUNNING")

	errOut := runCLIExpectError(t, awsCLI("ecs", "execute-command",
		"--cluster", "cli-ecs-exec-disabled",
		"--task", taskArn,
		"--container", "app",
		"--command", "echo hello",
		"--interactive",
	))
	assert.Contains(t, errOut, "InvalidParameterException")
	assert.Contains(t, errOut, "execute command was not enabled")
}

func TestECS_CLI_FargateSandboxAllowsChroot(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 146)

	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-ecs-chroot"))
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/ecs/cli-chroot"))
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ecs-chroot",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", `[{
			"name": "app",
			"image": "public.ecr.aws/docker/library/busybox:latest",
			"entryPoint": ["sh", "-c"],
			"command": ["mkdir -p /jail && chroot /jail /definitely-missing; code=$?; if [ \"$code\" = 127 ]; then echo CHROOT_OK; exit 0; fi; exit \"$code\""],
			"logConfiguration": {"logDriver":"awslogs","options":{"awslogs-group":"/ecs/cli-chroot","awslogs-stream-prefix":"ecs"}}
		}]`,
		"--output", "json",
	))

	out := runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-ecs-chroot",
		"--task-definition", "cli-ecs-chroot",
		"--launch-type", "FARGATE",
		"--count", "1",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json",
	))
	var runResult struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	taskArn := runResult.Tasks[0].TaskArn
	cleanupCLIECSTask(t, "cli-ecs-chroot", taskArn)
	waitCLITaskStatus(t, "cli-ecs-chroot", taskArn, "STOPPED")

	out = runCLI(t, awsCLI("ecs", "describe-tasks",
		"--cluster", "cli-ecs-chroot",
		"--tasks", taskArn,
		"--output", "json",
	))
	var descResult struct {
		Tasks []struct {
			Containers []struct {
				ExitCode *int `json:"exitCode"`
			} `json:"containers"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.Tasks, 1)
	require.NotEmpty(t, descResult.Tasks[0].Containers)
	require.NotNil(t, descResult.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, 0, *descResult.Tasks[0].Containers[0].ExitCode)

	out = runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", "/ecs/cli-chroot",
		"--output", "json",
	))
	var logs struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &logs)
	var messages []string
	for _, event := range logs.Events {
		messages = append(messages, event.Message)
	}
	assert.Contains(t, strings.Join(messages, "\n"), "CHROOT_OK")
}

func TestECS_CLI_ManagedEBSVolumeSnapshotRoundTrip(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 143)

	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-ebs-roundtrip"))
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/ecs/cli-ebs-roundtrip"))

	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ebs-writer",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--volumes", `[{"name":"workspace","configuredAtLaunch":true}]`,
		"--container-definitions", `[{
			"name": "writer",
			"image": "`+evalImageName+`",
			"entryPoint": ["sh", "-c"],
			"command": ["printf 'cli-ebs-roundtrip' > /workspace/state.txt"],
			"mountPoints": [{"sourceVolume":"workspace","containerPath":"/workspace"}]
		}]`,
		"--output", "json",
	))
	var writerTD struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &writerTD)

	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-ebs-roundtrip",
		"--task-definition", writerTD.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--volume-configurations", `[{"name":"workspace","managedEBSVolume":{"roleArn":"arn:aws:iam::123456789012:role/ecsInfrastructureRole","sizeInGiB":1,"volumeType":"gp3","terminationPolicy":{"deleteOnTermination":false},"tagSpecifications":[{"resourceType":"volume","tags":[{"key":"purpose","value":"cli-roundtrip"}]}]}}]`,
		"--output", "json",
	))
	var runWriter struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runWriter)
	require.Len(t, runWriter.Tasks, 1)
	writerTaskArn := runWriter.Tasks[0].TaskArn
	waitCLITaskStatus(t, "cli-ebs-roundtrip", writerTaskArn, "STOPPED")

	out = runCLI(t, awsCLI("ecs", "describe-tasks",
		"--cluster", "cli-ebs-roundtrip",
		"--tasks", writerTaskArn,
		"--output", "json",
	))
	var writerDesc struct {
		Tasks []struct {
			Attachments []struct {
				Type    string `json:"type"`
				Details []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"details"`
			} `json:"attachments"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &writerDesc)
	require.Len(t, writerDesc.Tasks, 1)
	volumeID := cliEBSVolumeID(t, writerDesc.Tasks[0].Attachments)
	t.Cleanup(func() {
		runCLI(t, awsCLI("ec2", "delete-volume", "--volume-id", volumeID))
	})

	out = runCLI(t, awsCLI("ec2", "create-snapshot",
		"--volume-id", volumeID,
		"--description", "cli ebs roundtrip",
		"--output", "json",
	))
	var snapResult struct {
		SnapshotId string `json:"SnapshotId"`
	}
	parseJSON(t, out, &snapResult)
	require.NotEmpty(t, snapResult.SnapshotId)
	waitCLISnapshotStatus(t, snapResult.SnapshotId, "completed")
	t.Cleanup(func() {
		runCLI(t, awsCLI("ec2", "delete-snapshot", "--snapshot-id", snapResult.SnapshotId))
	})

	out = runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ebs-reader",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--volumes", `[{"name":"workspace","configuredAtLaunch":true}]`,
		"--container-definitions", `[{
			"name": "reader",
			"image": "`+evalImageName+`",
			"entryPoint": ["sh", "-c"],
			"command": ["test \"$(cat /workspace/state.txt)\" = \"cli-ebs-roundtrip\" && echo CLI_EBS_ROUNDTRIP_OK"],
			"mountPoints": [{"sourceVolume":"workspace","containerPath":"/workspace"}],
			"logConfiguration": {"logDriver":"awslogs","options":{"awslogs-group":"/ecs/cli-ebs-roundtrip","awslogs-stream-prefix":"ecs"}}
		}]`,
		"--output", "json",
	))
	var readerTD struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &readerTD)

	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-ebs-roundtrip",
		"--task-definition", readerTD.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--volume-configurations", `[{"name":"workspace","managedEBSVolume":{"roleArn":"arn:aws:iam::123456789012:role/ecsInfrastructureRole","snapshotId":"`+snapResult.SnapshotId+`","volumeType":"gp3"}}]`,
		"--output", "json",
	))
	var runReader struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runReader)
	require.Len(t, runReader.Tasks, 1)
	waitCLITaskStatus(t, "cli-ebs-roundtrip", runReader.Tasks[0].TaskArn, "STOPPED")

	out = runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", "/ecs/cli-ebs-roundtrip",
		"--output", "json",
	))
	var logs struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &logs)
	var messages []string
	for _, event := range logs.Events {
		messages = append(messages, event.Message)
	}
	assert.Contains(t, strings.Join(messages, "\n"), "CLI_EBS_ROUNDTRIP_OK")
}

func TestECS_CLI_RunTaskNonZeroExit(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 144)

	// Create cluster
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-ecs-fail-cluster"))

	// Register task definition with exit 1
	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ecs-fail-task",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", `[{
			"name": "app",
			"image": "alpine:latest",
			"command": ["sh", "-c", "exit 1"],
			"logConfiguration": {
				"logDriver": "awslogs",
				"options": {
					"awslogs-group": "/ecs/cli-fail-task",
					"awslogs-stream-prefix": "ecs"
				}
			}
		}]`,
		"--output", "json",
	))

	var tdResult struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &tdResult)

	// Run task
	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-ecs-fail-cluster",
		"--task-definition", tdResult.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--count", "1",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json",
	))

	var runResult struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	taskArn := runResult.Tasks[0].TaskArn
	cleanupCLIECSTask(t, "cli-ecs-fail-cluster", taskArn)

	// Poll until the task reaches STOPPED; netns setup on CI can make a fixed
	// sleep race the real container lifecycle.
	out = pollECSTaskStopped(t, "cli-ecs-fail-cluster", taskArn)

	var descResult struct {
		Tasks []struct {
			LastStatus string `json:"lastStatus"`
			Containers []struct {
				ExitCode *int `json:"exitCode"`
			} `json:"containers"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.Tasks, 1)
	assert.Equal(t, "STOPPED", descResult.Tasks[0].LastStatus)
	require.NotEmpty(t, descResult.Tasks[0].Containers)
	require.NotNil(t, descResult.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, 1, *descResult.Tasks[0].Containers[0].ExitCode)
}

// CLI-level coverage for the ECS Tag/Untag handlers.
func TestECS_CLI_TagAndUntagTask(t *testing.T) {
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-tag-cluster"))

	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-tag-task",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", `[{
				"name": "app",
				"image": "alpine:latest",
				"entryPoint": ["sh", "-c"],
				"command": ["sleep 30"]
			}]`,
		"--output", "json",
	))
	var tdResult struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &tdResult)

	resourceArn := tdResult.TaskDefinition.TaskDefinitionArn

	runCLI(t, awsCLI("ecs", "tag-resource",
		"--resource-arn", resourceArn,
		"--tags", "key=sockerless-name,value=cli-task",
	))

	out = runCLI(t, awsCLI("ecs", "list-tags-for-resource",
		"--resource-arn", resourceArn,
		"--output", "json",
	))
	var listResult struct {
		Tags []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"tags"`
	}
	parseJSON(t, out, &listResult)

	found := false
	for _, tag := range listResult.Tags {
		if tag.Key == "sockerless-name" && tag.Value == "cli-task" {
			found = true
		}
	}
	assert.True(t, found, "tag must be visible via list-tags-for-resource after tag-resource")

	runCLI(t, awsCLI("ecs", "untag-resource",
		"--resource-arn", resourceArn,
		"--tag-keys", "sockerless-name",
	))

	out = runCLI(t, awsCLI("ecs", "list-tags-for-resource",
		"--resource-arn", resourceArn,
		"--output", "json",
	))
	listResult.Tags = nil
	parseJSON(t, out, &listResult)
	for _, tag := range listResult.Tags {
		assert.NotEqual(t, "sockerless-name", tag.Key, "untagged key should not be present")
	}
}

func waitCLITaskStatus(t *testing.T, clusterName, taskArn, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		out := runCLI(t, awsCLI("ecs", "describe-tasks",
			"--cluster", clusterName,
			"--tasks", taskArn,
			"--output", "json",
		))
		var desc struct {
			Tasks []struct {
				LastStatus string `json:"lastStatus"`
			} `json:"tasks"`
		}
		parseJSON(t, out, &desc)
		return len(desc.Tasks) == 1 && desc.Tasks[0].LastStatus == want
	}, 20*time.Second, 500*time.Millisecond)
}

func cliEBSVolumeID(t *testing.T, attachments []struct {
	Type    string `json:"type"`
	Details []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"details"`
}) string {
	t.Helper()
	for _, attachment := range attachments {
		if attachment.Type != "AmazonElasticBlockStorage" {
			continue
		}
		for _, detail := range attachment.Details {
			if detail.Name == "volumeId" {
				return detail.Value
			}
		}
	}
	t.Fatal("task did not include an AmazonElasticBlockStorage volume attachment")
	return ""
}
