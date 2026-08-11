package aws_cli_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestELBv2LoadBalancerCLI(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		_, _ = w.Write([]byte("cli-proxied"))
	}))
	defer targetServer.Close()
	targetURL, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetHost, targetPort, err := net.SplitHostPort(targetURL.Host)
	if err != nil {
		t.Fatal(err)
	}

	out := runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.91.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text"))
	vpcID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.91.1.0/24",
		"--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnet1 := strings.TrimSpace(out)
	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.91.2.0/24",
		"--availability-zone", "us-east-1b",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnet2 := strings.TrimSpace(out)
	out = runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-elbv2-sg",
		"--description", "cli elbv2",
		"--vpc-id", vpcID,
		"--query", "GroupId",
		"--output", "text"))
	sgID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-lb",
		"--type", "application",
		"--scheme", "internet-facing",
		"--subnets", subnet1, subnet2,
		"--security-groups", sgID,
		"--tags", "Key=env,Value=cli",
		"--query", "LoadBalancers[0].[LoadBalancerArn,DNSName]",
		"--output", "text"))
	lbFields := strings.Fields(out)
	if len(lbFields) != 2 {
		t.Fatalf("expected load balancer ARN and DNS name, got %q", out)
	}
	lbArn := lbFields[0]
	lbDNSName := lbFields[1]
	if !strings.Contains(lbArn, ":loadbalancer/app/cli-lb/") {
		t.Fatalf("expected ELBv2 load balancer ARN, got %q", lbArn)
	}

	out = runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-tg",
		"--protocol", "HTTP",
		"--port", "80",
		"--vpc-id", vpcID,
		"--target-type", "ip",
		"--health-check-timeout-seconds", "2",
		"--query", "TargetGroups[0].TargetGroupArn",
		"--output", "text"))
	tgArn := strings.TrimSpace(out)
	if !strings.Contains(tgArn, ":targetgroup/cli-tg/") {
		t.Fatalf("expected ELBv2 target group ARN, got %q", tgArn)
	}

	runCLI(t, awsCLI("elbv2", "register-targets",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort, "Id=192.0.2.254,Port=80"))
	out = runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort,
		"--query", "TargetHealthDescriptions[0].TargetHealth.State",
		"--output", "text"))
	if strings.TrimSpace(out) != "healthy" {
		t.Fatalf("expected healthy target, got %q", out)
	}
	out = runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id=192.0.2.254,Port=80",
		"--query", "TargetHealthDescriptions[0].TargetHealth.State",
		"--output", "text"))
	if strings.TrimSpace(out) != "unhealthy" {
		t.Fatalf("expected unhealthy target, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "create-listener",
		"--load-balancer-arn", lbArn,
		"--protocol", "HTTP",
		"--port", "80",
		"--default-actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Listeners[0].ListenerArn",
		"--output", "text"))
	listenerArn := strings.TrimSpace(out)
	if !strings.Contains(listenerArn, ":listener/app/cli-lb/") {
		t.Fatalf("expected ELBv2 listener ARN, got %q", listenerArn)
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/proxy-check", nil)
	if err != nil {
		t.Fatalf("build ELBv2 data-plane request: %v", err)
	}
	req.Host = lbDNSName
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET through real ELBv2 data plane: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read ELBv2 proxy response: %v", err)
	}
	if strings.TrimSpace(string(body)) != "cli-proxied" {
		t.Fatalf("expected proxied body, got %q", string(body))
	}

	out = runCLI(t, awsCLI("elbv2", "describe-load-balancers",
		"--load-balancer-arns", lbArn,
		"--query", "LoadBalancers[0].State.Code",
		"--output", "text"))
	if strings.TrimSpace(out) != "active" {
		t.Fatalf("expected active load balancer, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-listener-attributes",
		"--listener-arn", listenerArn,
		"--query", "Attributes[?Key=='routing.http.response.server.enabled'].Value|[0]",
		"--output", "text"))
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("expected default listener server header attribute, got %q", out)
	}
	out = runCLI(t, awsCLI("elbv2", "modify-listener-attributes",
		"--listener-arn", listenerArn,
		"--attributes", "Key=routing.http.response.server.enabled,Value=false",
		"--query", "Attributes[?Key=='routing.http.response.server.enabled'].Value|[0]",
		"--output", "text"))
	if strings.TrimSpace(out) != "false" {
		t.Fatalf("expected modified listener server header attribute, got %q", out)
	}

	runCLI(t, awsCLI("elbv2", "add-tags",
		"--resource-arns", lbArn,
		"--tags", "Key=scenario,Value=cli"))
	out = runCLI(t, awsCLI("elbv2", "describe-tags",
		"--resource-arns", lbArn,
		"--query", "TagDescriptions[0].Tags[?Key=='scenario'].Value|[0]",
		"--output", "text"))
	if strings.TrimSpace(out) != "cli" {
		t.Fatalf("expected scenario tag from describe-tags, got %q", out)
	}

	runCLI(t, awsCLI("elbv2", "delete-listener", "--listener-arn", listenerArn))
	runCLI(t, awsCLI("elbv2", "deregister-targets",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort, "Id=192.0.2.254,Port=80"))
	runCLI(t, awsCLI("elbv2", "delete-target-group", "--target-group-arn", tgArn))
	runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn))
}
