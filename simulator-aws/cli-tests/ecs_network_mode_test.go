package aws_cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// An Amazon ECS task definition's networkMode decides the network every
// container in the task joins. These tests read the joined network back from
// the container runtime:
//
//   - awsvpc: the task's own elastic network interface in the VPC, so never
//     the container instance's default bridge.
//   - bridge: the container instance's default Docker bridge.
//   - host: the container instance's own network stack.
//   - none: no connectivity.

func registerNetworkModeTaskDef(q func(...string) string, family, mode string) {
	args := []string{"ecs", "register-task-definition", "--family", family, "--network-mode", mode}
	if mode == "awsvpc" {
		args = append(args, "--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512")
	}
	args = append(args,
		"--container-definitions", `[{"name":"app","image":"`+vpcNetBusybox+`","stopTimeout":2,"entryPoint":["sh","-c"],"command":["trap 'exit 143' TERM; sleep 120 & wait"]}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")
	q(args...)
}

func containerNetworkNames(t *testing.T, taskArn string) []string {
	t.Helper()
	out := dockerOut(t, "inspect", "-f",
		"{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}", taskContainerID(t, taskArn))
	return strings.Fields(out)
}

func containerNetworkMode(t *testing.T, taskArn string) string {
	t.Helper()
	return strings.TrimSpace(dockerOut(t, "inspect", "-f", "{{.HostConfig.NetworkMode}}", taskContainerID(t, taskArn)))
}

// defaultContainerNetworkName reads the network the runtime attaches a
// container to by default: "bridge" under Docker, "podman" under Podman.
func defaultContainerNetworkName(t *testing.T) string {
	t.Helper()
	defaultNetworkOnce.Do(func() {
		name := "sockerless-default-net-probe-" + strconv.Itoa(os.Getpid())
		_ = exec.Command("docker", "rm", "-f", name).Run()
		out, err := exec.Command("docker", "run", "-d", "--name", name, vpcNetBusybox, "sleep", "30").CombinedOutput()
		if err != nil {
			defaultNetworkErr = fmt.Errorf("probe the container runtime's default network: %v\n%s", err, out)
			return
		}
		defer func() { _ = exec.Command("docker", "rm", "-f", name).Run() }()
		inspect, err := exec.Command("docker", "inspect", "-f",
			"{{range $n, $_ := .NetworkSettings.Networks}}{{$n}} {{end}}", name).CombinedOutput()
		if err != nil {
			defaultNetworkErr = fmt.Errorf("inspect the default-network probe: %v\n%s", err, inspect)
			return
		}
		networks := strings.Fields(string(inspect))
		if len(networks) != 1 {
			defaultNetworkErr = fmt.Errorf("default-network probe attached to %v, want exactly one network", networks)
			return
		}
		defaultNetworkName = networks[0]
	})
	if defaultNetworkErr != nil {
		t.Fatal(defaultNetworkErr)
	}
	return defaultNetworkName
}

var (
	defaultNetworkOnce sync.Once
	defaultNetworkName string
	defaultNetworkErr  error
)

// TestECSNetworkModeAwsvpcKeepsTaskOffTheDefaultBridge expects an awsvpc task's
// container on its VPC's network, not the shared default bridge: an elastic
// network interface per task.
func TestECSNetworkModeAwsvpcKeepsTaskOffTheDefaultBridge(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	const vpcRange = "10.180.0.0/16"
	vpcID, subnetID := mkVPCSubnet(t, q, vpcRange, subnetOf(vpcRange))
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-awsvpc", "awsvpc")

	task := runTask(q, "netmode-awsvpc", subnetID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
		stopCLITasksInSubnet(t, "default", subnetID)
		q("ec2", "delete-subnet", "--subnet-id", subnetID)
		q("ec2", "delete-vpc", "--vpc-id", vpcID)
		rmDockerNetworks(ecsVPCNet(vpcID), ecsVPCNet(vpcID)+"-egress")
	})
	waitRunning(t, q, task)

	defaultNet := defaultContainerNetworkName(t)
	networks := containerNetworkNames(t, task)
	for _, name := range networks {
		if name == defaultNet {
			t.Fatalf("awsvpc task landed on the container instance's default network %q; networks=%v",
				defaultNet, networks)
		}
	}

	if eniIP := taskENIIP(q, task); !strings.HasPrefix(eniIP, prefixOf(vpcRange)) {
		t.Fatalf("awsvpc task ENI IP %q is not in the subnet CIDR %s", eniIP, subnetOf(vpcRange))
	}
}

// TestECSNetworkModeBridgeUsesTheContainerInstanceNetwork expects a bridge-mode
// task on the container instance's own network, neither the engine's default
// bridge nor a VPC's network, with no elastic network interface.
func TestECSNetworkModeBridgeUsesTheContainerInstanceNetwork(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-bridge", "bridge")

	task := q("ecs", "run-task", "--cluster", "default", "--task-definition", "netmode-bridge",
		"--query", "tasks[0].taskArn", "--output", "text")
	t.Cleanup(func() { runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task)) })
	waitRunning(t, q, task)

	defaultNet := defaultContainerNetworkName(t)
	networks := containerNetworkNames(t, task)
	if len(networks) != 1 {
		t.Fatalf("bridge-mode task is on %d networks, want the container instance's one: %v", len(networks), networks)
	}
	if networks[0] == defaultNet || strings.HasPrefix(networks[0], "sockerless-sim-vpc-") {
		t.Fatalf("bridge-mode task runs on %q, want the container instance's own network", networks[0])
	}

	desc := q("ecs", "describe-tasks", "--cluster", "default", "--tasks", task, "--output", "json")
	if strings.Contains(desc, "ElasticNetworkInterface") {
		t.Fatalf("bridge-mode task must carry no elastic network interface attachment; describe-tasks: %s", desc)
	}
	if strings.Contains(desc, "networkInterfaces") {
		t.Fatalf("bridge-mode container must report no awsvpc network interfaces; describe-tasks: %s", desc)
	}
}

func TestECSNetworkModeHostUsesTheContainerInstanceNetworkStack(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-host", "host")

	task := q("ecs", "run-task", "--cluster", "default", "--task-definition", "netmode-host",
		"--query", "tasks[0].taskArn", "--output", "text")
	t.Cleanup(func() { runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task)) })
	waitRunning(t, q, task)

	if mode := containerNetworkMode(t, task); mode != "host" {
		t.Fatalf("host-mode task container network mode = %q, want host", mode)
	}

	// The container instance's stack serves the task metadata endpoint, so a
	// host-mode container still reaches it.
	cid := taskContainerID(t, task)
	out, err := exec.Command("docker", "exec", cid, "sh", "-c",
		`wget -T 5 -q -O - "$ECS_CONTAINER_METADATA_URI_V4/task"`).CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"TaskARN"`) {
		t.Fatalf("host-mode task could not read its task metadata endpoint: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), taskID(task)) {
		t.Fatalf("host-mode task metadata is not this task's: %s", out)
	}
}

func TestECSNetworkModeNoneHasNoConnectivity(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-none", "none")

	task := q("ecs", "run-task", "--cluster", "default", "--task-definition", "netmode-none",
		"--query", "tasks[0].taskArn", "--output", "text")
	t.Cleanup(func() { runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task)) })
	waitRunning(t, q, task)

	if mode := containerNetworkMode(t, task); mode != "none" {
		t.Fatalf("none-mode task container network mode = %q, want none", mode)
	}
}

// TestECSNetworkModeRejectsMismatchedNetworkConfiguration expects run-task to
// require networkConfiguration for awsvpc and refuse it for every other mode.
func TestECSNetworkModeRejectsMismatchedNetworkConfiguration(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	const vpcRange = "10.185.0.0/16"
	vpcID, subnetID := mkVPCSubnet(t, q, vpcRange, subnetOf(vpcRange))
	t.Cleanup(func() {
		stopCLITasksInSubnet(t, "default", subnetID)
		q("ec2", "delete-subnet", "--subnet-id", subnetID)
		q("ec2", "delete-vpc", "--vpc-id", vpcID)
	})
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-mismatch-awsvpc", "awsvpc")
	registerNetworkModeTaskDef(q, "netmode-mismatch-bridge", "bridge")

	out := runCLIExpectError(t, awsCLI("ecs", "run-task", "--cluster", "default",
		"--task-definition", "netmode-mismatch-awsvpc"))
	if !strings.Contains(out, "InvalidParameterException") || !strings.Contains(out, "awsvpc") {
		t.Fatalf("awsvpc run-task without --network-configuration must be rejected; got: %s", out)
	}

	out = runCLIExpectError(t, awsCLI("ecs", "run-task", "--cluster", "default",
		"--task-definition", "netmode-mismatch-bridge",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`))
	if !strings.Contains(out, "InvalidParameterException") || !strings.Contains(out, "bridge") {
		t.Fatalf("bridge run-task with --network-configuration must be rejected; got: %s", out)
	}
}
