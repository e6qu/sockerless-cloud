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

// The Amazon Elastic Container Service (ECS) task definition's networkMode
// decides the network fabric every container in the task lands on. These tests
// assert the fabric the container actually joined, read back from the container
// runtime, not just the shape of the API response:
//
//   - awsvpc — the task gets its own elastic network interface in the VPC, so
//     the container is never on the container instance's default bridge.
//   - bridge — the container instance's default Docker bridge.
//   - host   — the container instance's own network stack.
//   - none   — no connectivity.
//
// The awsvpc assertion is tier-agnostic: on a Linux host with CAP_NET_ADMIN the
// task shares a pause container's VPC network namespace (no Docker network at
// all), and elsewhere it is pinned to the per-VPC user-defined Docker network.
// Neither is the default bridge.

// registerNetworkModeTaskDef registers a single-container task definition in
// the requested network mode running a long sleep.
func registerNetworkModeTaskDef(q func(...string) string, family, mode string) {
	args := []string{"ecs", "register-task-definition", "--family", family, "--network-mode", mode}
	if mode == "awsvpc" {
		args = append(args, "--requires-compatibilities", "FARGATE", "--cpu", "256", "--memory", "512")
	}
	args = append(args,
		"--container-definitions", `[{"name":"app","image":"`+vpcNetBusybox+`","entryPoint":["sh","-c"],"command":["sleep 120"]}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")
	q(args...)
}

// containerNetworkNames lists the Docker networks a task's container is
// attached to.
func containerNetworkNames(t *testing.T, taskArn string) []string {
	t.Helper()
	out := dockerOut(t, "inspect", "-f",
		"{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}", taskContainerID(t, taskArn))
	return strings.Fields(out)
}

// containerNetworkMode reads the container's configured network mode.
func containerNetworkMode(t *testing.T, taskArn string) string {
	t.Helper()
	return strings.TrimSpace(dockerOut(t, "inspect", "-f", "{{.HostConfig.NetworkMode}}", taskContainerID(t, taskArn)))
}

// defaultContainerNetworkName is the network the container runtime attaches a
// container to when none is requested — the container instance's own bridge.
// It is read from the runtime rather than assumed, because the name differs by
// runtime ("bridge" under Docker, "podman" under Podman).
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

// TestECSNetworkModeAwsvpcKeepsTaskOffTheDefaultBridge proves an awsvpc task's
// container is wired into its VPC's own network rather than the container
// instance's shared default bridge — the elastic-network-interface-per-task
// model real ECS implements.
func TestECSNetworkModeAwsvpcKeepsTaskOffTheDefaultBridge(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	octet := unusedDockerVPCOctet(t, 180, nil)
	vpcID, subnetID := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-awsvpc", "awsvpc")

	task := runTask(q, "netmode-awsvpc", subnetID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task))
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

	// The task's reported ENI address must come from the subnet's CIDR.
	if eniIP := taskENIIP(q, task); !strings.HasPrefix(eniIP, vpcPrefix(octet)) {
		t.Fatalf("awsvpc task ENI IP %q is not in the subnet CIDR %s", eniIP, subnetCIDR(octet))
	}
}

// TestECSNetworkModeBridgeUsesTheContainerInstanceBridge proves a bridge-mode
// task runs on the container instance's default Docker bridge and is allocated
// no elastic network interface.
func TestECSNetworkModeBridgeUsesTheContainerInstanceBridge(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	q("ecs", "create-cluster", "--cluster-name", "default", "--query", "cluster.clusterName", "--output", "text")
	registerNetworkModeTaskDef(q, "netmode-bridge", "bridge")

	task := q("ecs", "run-task", "--cluster", "default", "--task-definition", "netmode-bridge",
		"--query", "tasks[0].taskArn", "--output", "text")
	t.Cleanup(func() { runCLI(t, awsCLI("ecs", "stop-task", "--cluster", "default", "--task", task)) })
	waitRunning(t, q, task)

	defaultNet := defaultContainerNetworkName(t)
	networks := containerNetworkNames(t, task)
	found := false
	for _, name := range networks {
		if name == defaultNet {
			found = true
		}
	}
	if !found {
		t.Fatalf("bridge-mode task is not on the container instance's default bridge %q; networks=%v",
			defaultNet, networks)
	}

	desc := q("ecs", "describe-tasks", "--cluster", "default", "--tasks", task, "--output", "json")
	if strings.Contains(desc, "ElasticNetworkInterface") {
		t.Fatalf("bridge-mode task must carry no elastic network interface attachment; describe-tasks: %s", desc)
	}
	if strings.Contains(desc, "networkInterfaces") {
		t.Fatalf("bridge-mode container must report no awsvpc network interfaces; describe-tasks: %s", desc)
	}
}

// TestECSNetworkModeHostUsesTheContainerInstanceNetworkStack proves a host-mode
// task's container runs in the container instance's own network namespace.
func TestECSNetworkModeHostUsesTheContainerInstanceNetworkStack(t *testing.T) {
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

	// The task metadata endpoint is served on the container instance's own
	// stack, so a host-mode container must still reach it.
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

// TestECSNetworkModeNoneHasNoConnectivity proves a none-mode task's container
// gets no network at all.
func TestECSNetworkModeNoneHasNoConnectivity(t *testing.T) {
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

// TestECSNetworkModeRejectsMismatchedNetworkConfiguration proves run-task
// enforces the networkMode ↔ networkConfiguration contract in both directions:
// awsvpc requires it, every other mode refuses it.
func TestECSNetworkModeRejectsMismatchedNetworkConfiguration(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	octet := unusedDockerVPCOctet(t, 185, nil)
	vpcID, subnetID := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	t.Cleanup(func() {
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
