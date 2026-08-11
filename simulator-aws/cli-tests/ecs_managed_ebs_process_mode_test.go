package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
)

// startProcessModeSim starts a second simulator-aws instance with
// SIM_RUNTIME=process (the API-only mode that never initialises a Docker
// client) on a fresh port and returns its base URL. Killed on test cleanup.
func startProcessModeSim(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cmd := exec.Command(binaryPath)
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		"SIM_DNS_PORT=0",
		"SIM_RUNTIME=process",
		"SIM_LOG_LEVEL=warn",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process-mode sim: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHealth(url + "/health"); err != nil {
		t.Fatalf("process-mode sim health: %v", err)
	}
	return url
}

// TestECS_CLI_ManagedEBSProcessMode covers issue #569 via the aws CLI: a
// managed-EBS run-task under SIM_RUNTIME=process must not panic the simulator
// on the nil Docker client during the deleteOnTermination cleanup. After the
// run-task transition, subsequent CLI calls must still succeed (a panic would
// crash the process and every later call would get a connection error).
func TestECS_CLI_ManagedEBSProcessMode(t *testing.T) {
	url := startProcessModeSim(t)
	cli := func(args ...string) *exec.Cmd {
		cmd := exec.Command("aws", args...)
		cmd.Env = append(os.Environ(),
			"AWS_ENDPOINT_URL="+url,
			"AWS_ACCESS_KEY_ID=test",
			"AWS_SECRET_ACCESS_KEY=test",
			"AWS_DEFAULT_REGION=us-east-1",
			"AWS_PAGER=",
		)
		return cmd
	}

	vpcOut := runCLI(t, cli("ec2", "create-vpc", "--cidr-block", "10.191.0.0/16"))
	var vpc struct {
		Vpc struct {
			VpcId string `json:"VpcId"`
		} `json:"Vpc"`
	}
	if err := json.Unmarshal([]byte(vpcOut), &vpc); err != nil {
		t.Fatalf("parse create-vpc: %v\n%s", err, vpcOut)
	}
	subOut := runCLI(t, cli("ec2", "create-subnet", "--vpc-id", vpc.Vpc.VpcId, "--cidr-block", "10.191.1.0/24"))
	var sub struct {
		Subnet struct {
			SubnetId string `json:"SubnetId"`
		} `json:"Subnet"`
	}
	if err := json.Unmarshal([]byte(subOut), &sub); err != nil {
		t.Fatalf("parse create-subnet: %v\n%s", err, subOut)
	}

	runCLI(t, cli("ecs", "create-cluster", "--cluster-name", "managed-ebs-process-cli"))

	tdOut := runCLI(t, cli("ecs", "register-task-definition",
		"--family", "managed-ebs-process-cli",
		"--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE",
		"--cpu", "256", "--memory", "512",
		"--volumes", `[{"name":"workspace","configuredAtLaunch":true}]`,
		"--container-definitions", `[{"name":"app","image":"public.ecr.aws/docker/library/alpine:3.20","mountPoints":[{"sourceVolume":"workspace","containerPath":"/workspace"}]}]`,
	))
	var td struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	if err := json.Unmarshal([]byte(tdOut), &td); err != nil {
		t.Fatalf("parse register-task-definition: %v\n%s", err, tdOut)
	}

	// deleteOnTermination=true exercises the volume cleanup that dereferenced
	// the nil Docker client in process mode.
	volCfg := `[{"name":"workspace","managedEBSVolume":{"roleArn":"arn:aws:iam::123456789012:role/ecsInfrastructureRole","sizeInGiB":1,"volumeType":"gp3","terminationPolicy":{"deleteOnTermination":true}}}]`
	runOut := runCLI(t, cli("ecs", "run-task",
		"--cluster", "managed-ebs-process-cli",
		"--task-definition", td.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--network-configuration", fmt.Sprintf("awsvpcConfiguration={subnets=[%s]}", sub.Subnet.SubnetId),
		"--volume-configurations", volCfg,
	))
	var run struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(runOut), &run); err != nil {
		t.Fatalf("parse run-task: %v\n%s", err, runOut)
	}
	if len(run.Tasks) != 1 || run.Tasks[0].TaskArn == "" {
		t.Fatalf("expected 1 task with an ARN, got: %s", runOut)
	}

	// The async transition runs the deleteOnTermination cleanup. Subsequent
	// CLI calls must still succeed — a panic would have crashed the sim.
	runCLI(t, cli("ecs", "describe-tasks", "--cluster", "managed-ebs-process-cli", "--tasks", run.Tasks[0].TaskArn))
	runCLI(t, cli("ecs", "list-clusters"))
}
