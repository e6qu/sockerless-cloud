package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECS_CLI_ExpressModeLifecycle drives the ECS Express Mode (Express Gateway service)
// lifecycle through the aws CLI:
//
//	aws ecs create-express-gateway-service
//	aws ecs describe-express-gateway-service
//	aws ecs update-express-gateway-service
//	aws ecs delete-express-gateway-service
func TestECS_CLI_ExpressModeLifecycle(t *testing.T) {
	cluster := "cli-express-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	createOut := runCLI(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", cluster,
		"--service-name", "cli-web",
		"--infrastructure-role-arn", "arn:aws:iam::000000000000:role/express-infra",
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

	// Describe with include TAGS round-trips the tag.
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

	// Update cpu/memory → targetConfiguration reflects the new values.
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

	// Delete → DRAINING.
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

// TestECS_CLI_ExpressModeErrors covers the documented Create error cases via
// the CLI.
func TestECS_CLI_ExpressModeErrors(t *testing.T) {
	cluster := "cli-express-err-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	// taskDefinitionArn + primaryContainer together → InvalidParameterException.
	mutexErr := runCLIExpectError(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", cluster,
		"--service-name", "cli-mutex",
		"--infrastructure-role-arn", "arn:aws:iam::000000000000:role/express-infra",
		"--task-definition-arn", "arn:aws:ecs:us-east-1:000000000000:task-definition/foo:1",
		"--primary-container", `{"image":"public.ecr.aws/docker/library/busybox:latest"}`))
	if !strings.Contains(mutexErr, "InvalidParameterException") {
		t.Fatalf("expected InvalidParameterException, got: %s", mutexErr)
	}

	// Bogus serviceArn → ResourceNotFoundException.
	nfErr := runCLIExpectError(t, awsCLI("ecs", "describe-express-gateway-service",
		"--service-arn", "arn:aws:ecs:us-east-1:000000000000:express-gateway-service/cli-express-err-cluster/nope"))
	if !strings.Contains(nfErr, "ResourceNotFoundException") {
		t.Fatalf("expected ResourceNotFoundException, got: %s", nfErr)
	}

	// Nonexistent cluster → ClusterNotFoundException.
	clErr := runCLIExpectError(t, awsCLI("ecs", "create-express-gateway-service",
		"--cluster", "cli-no-such-cluster",
		"--service-name", "cli-orphan",
		"--infrastructure-role-arn", "arn:aws:iam::000000000000:role/express-infra",
		"--primary-container", `{"image":"public.ecr.aws/docker/library/busybox:latest"}`))
	if !strings.Contains(clErr, "ClusterNotFoundException") {
		t.Fatalf("expected ClusterNotFoundException, got: %s", clErr)
	}
}
