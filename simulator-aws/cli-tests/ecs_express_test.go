package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

func createCLIECSRole(t *testing.T, cli func(args ...string) *exec.Cmd, name, principal string, actions ...string) string {
	t.Helper()
	roleArn := strings.TrimSpace(runCLI(t, cli("iam", "create-role", "--role-name", name,
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"`+principal+`"},"Action":"sts:AssumeRole"}]}`,
		"--query", "Role.Arn", "--output", "text")))
	runCLI(t, cli("iam", "put-role-policy", "--role-name", name, "--policy-name", "infrastructure",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["`+strings.Join(actions, `","`)+`"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		_ = cli("iam", "delete-role-policy", "--role-name", name, "--policy-name", "infrastructure").Run()
		_ = cli("iam", "delete-role", "--role-name", name).Run()
	})
	return roleArn
}

// cliECSVolumeInfrastructureActions are the Amazon EC2 actions the AWS managed
// policy AmazonECSInfrastructureRolePolicyForVolumes grants the role that
// manages a task's Amazon EBS volumes.
var cliECSVolumeInfrastructureActions = []string{
	"ec2:CreateVolume", "ec2:DeleteVolume", "ec2:AttachVolume", "ec2:DetachVolume",
	"ec2:DescribeVolumes", "ec2:DescribeSnapshots", "ec2:ModifyVolume", "ec2:CreateTags",
}

func createCLIECSExpressInfrastructureRole(t *testing.T, name string) string {
	t.Helper()
	return createCLIECSRole(t, awsCLI, name, "ecs.amazonaws.com", "elasticloadbalancing:*", "ec2:*", "acm:*", "application-autoscaling:*")
}

func TestECS_CLI_ExpressModeLifecycle(t *testing.T) {
	t.Parallel()
	cluster := "cli-express-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	createOut := runCLI(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", cluster,
		"--service-name", "cli-web",
		"--infrastructure-role-arn", createCLIECSExpressInfrastructureRole(t, "cli-express-infrastructure"),
		"--primary-container", `{"image":"`+containerCommandImage+`","containerPort":8080,"command":["http","8080","express-ok"]}`,
		"--tags", "key=env,value=test",
		"--output", "json"))
	var created struct {
		Service struct {
			ServiceArn string `json:"serviceArn"`
			Status     struct {
				StatusCode string `json:"statusCode"`
			} `json:"status"`
			ActiveConfigurations []struct {
				Cpu             string `json:"cpu"`
				Memory          string `json:"memory"`
				HealthCheckPath string `json:"healthCheckPath"`
				IngressPaths    []struct {
					AccessType string `json:"accessType"`
					Endpoint   string `json:"endpoint"`
				} `json:"ingressPaths"`
			} `json:"activeConfigurations"`
		} `json:"service"`
	}
	parseJSON(t, createOut, &created)
	if created.Service.ServiceArn == "" {
		t.Fatalf("expected non-empty serviceArn, got: %s", createOut)
	}
	if created.Service.Status.StatusCode != "ACTIVE" {
		t.Fatalf("expected status ACTIVE, got %q", created.Service.Status.StatusCode)
	}
	if len(created.Service.ActiveConfigurations) == 0 {
		t.Fatalf("expected at least one activeConfiguration, got: %s", createOut)
	}
	cfg := created.Service.ActiveConfigurations[0]
	if cfg.Cpu != "256" || cfg.Memory != "512" || cfg.HealthCheckPath != "/ping" {
		t.Fatalf("defaults not applied: cpu=%q memory=%q healthCheckPath=%q", cfg.Cpu, cfg.Memory, cfg.HealthCheckPath)
	}
	if len(cfg.IngressPaths) == 0 {
		t.Fatalf("expected ingressPaths, got: %s", createOut)
	}
	ingress := cfg.IngressPaths[0]
	if ingress.AccessType != "PUBLIC" {
		t.Fatalf("expected accessType PUBLIC, got %q", ingress.AccessType)
	}
	if !strings.HasPrefix(ingress.Endpoint, "https://") {
		t.Fatalf("expected https:// endpoint, got %q", ingress.Endpoint)
	}
	arn := created.Service.ServiceArn

	descOut := runCLI(t, awsCLI("ecs", "describe-express-gateway-service",
		"--service-arn", arn, "--include", "TAGS", "--output", "json"))
	var desc struct {
		Service struct {
			Tags []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"tags"`
		} `json:"service"`
	}
	parseJSON(t, descOut, &desc)
	if len(desc.Service.Tags) != 1 || desc.Service.Tags[0].Key != "env" || desc.Service.Tags[0].Value != "test" {
		t.Fatalf("expected tag env=test, got %s", descOut)
	}

	updOut := runCLI(t, awsCLI("ecs", "update-express-gateway-service",
		"--service-arn", arn, "--cpu", "512", "--memory", "1024", "--output", "json"))
	var upd struct {
		Service struct {
			TargetConfiguration struct {
				Cpu    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"targetConfiguration"`
		} `json:"service"`
	}
	parseJSON(t, updOut, &upd)
	if upd.Service.TargetConfiguration.Cpu != "512" || upd.Service.TargetConfiguration.Memory != "1024" {
		t.Fatalf("update did not apply cpu/memory: %s", updOut)
	}

	delOut := runCLI(t, awsCLI("ecs", "delete-express-gateway-service",
		"--service-arn", arn, "--output", "json"))
	var del struct {
		Service struct {
			Status struct {
				StatusCode string `json:"statusCode"`
			} `json:"status"`
		} `json:"service"`
	}
	parseJSON(t, delOut, &del)
	if del.Service.Status.StatusCode != "DRAINING" {
		t.Fatalf("expected status DRAINING after delete, got %q", del.Service.Status.StatusCode)
	}
}

func TestECS_CLI_ExpressModeErrors(t *testing.T) {
	t.Parallel()
	cluster := "cli-express-err-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	infrastructureRole := createCLIECSExpressInfrastructureRole(t, "cli-express-errors-infrastructure")

	mutexErr := runCLIExpectError(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", cluster,
		"--service-name", "cli-mutex",
		"--infrastructure-role-arn", infrastructureRole,
		"--task-definition-arn", "arn:aws:ecs:us-east-1:000000000000:task-definition/foo:1",
		"--primary-container", `{"image":"mirror.gcr.io/library/busybox:latest"}`))
	if !strings.Contains(mutexErr, "InvalidParameterException") {
		t.Fatalf("expected InvalidParameterException, got: %s", mutexErr)
	}

	nfErr := runCLIExpectError(t, awsCLI("ecs", "describe-express-gateway-service",
		"--service-arn", "arn:aws:ecs:us-east-1:000000000000:express-gateway-service/cli-express-err-cluster/nope"))
	if !strings.Contains(nfErr, "ResourceNotFoundException") {
		t.Fatalf("expected ResourceNotFoundException, got: %s", nfErr)
	}

	clErr := runCLIExpectError(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", "cli-no-such-cluster",
		"--service-name", "cli-orphan",
		"--infrastructure-role-arn", infrastructureRole,
		"--primary-container", `{"image":"mirror.gcr.io/library/busybox:latest"}`))
	if !strings.Contains(clErr, "ClusterNotFoundException") {
		t.Fatalf("expected ClusterNotFoundException, got: %s", clErr)
	}

	notTrusted := createCLIECSRole(t, awsCLI, "cli-express-not-trusted", "lambda.amazonaws.com", "elasticloadbalancing:*")
	roleErr := runCLIExpectError(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", cluster,
		"--service-name", "cli-unassumable",
		"--infrastructure-role-arn", notTrusted,
		"--primary-container", `{"image":"mirror.gcr.io/library/busybox:latest"}`))
	if !strings.Contains(roleErr, "InvalidParameterException") {
		t.Fatalf("expected InvalidParameterException for a role ECS cannot assume, got: %s", roleErr)
	}
}
