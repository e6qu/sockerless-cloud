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
		// Elastic Load Balancing takes a target out of service after
		// UnhealthyThresholdCount consecutive failed checks spaced by this
		// interval; the 30-second default would make the test wait one out.
		"--health-check-interval-seconds", "5",
		"--query", "TargetGroups[0].TargetGroupArn",
		"--output", "text"))
	tgArn := strings.TrimSpace(out)
	if !strings.Contains(tgArn, ":targetgroup/cli-tg/") {
		t.Fatalf("expected ELBv2 target group ARN, got %q", tgArn)
	}

	runCLI(t, awsCLI("elbv2", "register-targets",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort, "Id=192.0.2.254,Port=80"))
	// Registering a target is not on its own enough to have it health-checked:
	// "Before the load balancer sends a health check request to a target, you
	// must register it with a target group, specify its target group in a
	// listener rule, and ensure that the Availability Zone of the target is
	// enabled for the load balancer." Until then its targets are unused.
	out = runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort,
		"--query", "TargetHealthDescriptions[0].TargetHealth.[State,Reason,Description]",
		"--output", "text"))
	if fields := strings.Fields(out); len(fields) < 2 || fields[0] != "unused" || fields[1] != "Target.NotInUse" {
		t.Fatalf("expected an unused target outside every listener rule, got %q", out)
	}
	if !strings.Contains(out, "Target group is not configured to receive traffic from the load balancer") {
		t.Fatalf("expected the published Target.NotInUse description, got %q", out)
	}

	// Naming the target group in the listener's default rule is what puts it
	// under the checker.
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

	waitForELBv2TargetHealthCLI(t, tgArn, "Id="+targetHost+",Port="+targetPort, "healthy")
	waitForELBv2TargetHealthCLI(t, tgArn, "Id=192.0.2.254,Port=80", "unhealthy")
	// An unhealthy target carries the documented reason code and description
	// alongside its state.
	out = runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id=192.0.2.254,Port=80",
		"--query", "TargetHealthDescriptions[0].TargetHealth.Reason",
		"--output", "text"))
	if reason := strings.TrimSpace(out); reason != "Target.Timeout" && reason != "Target.FailedHealthChecks" {
		t.Fatalf("expected a connection-failure reason code, got %q", reason)
	}
	out = runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort,
		"--query", "TargetHealthDescriptions[0].HealthCheckPort",
		"--output", "text"))
	if strings.TrimSpace(out) != targetPort {
		t.Fatalf("expected health check port %s, got %q", targetPort, out)
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

	// Deregistering does not remove a target at once: "The load balancer stops
	// routing requests to a target as soon as it is deregistered. The target
	// enters the `draining` state until in-flight requests have completed."
	// This target group waits the documented default of 300 seconds, so the
	// target reports draining rather than disappearing.
	runCLI(t, awsCLI("elbv2", "deregister-targets",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort, "Id=192.0.2.254,Port=80"))
	out = runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort,
		"--query", "TargetHealthDescriptions[0].TargetHealth.[State,Reason,Description]",
		"--output", "text"))
	if fields := strings.Fields(out); len(fields) < 2 || fields[0] != "draining" ||
		fields[1] != "Target.DeregistrationInProgress" {
		t.Fatalf("expected a draining target during the deregistration delay, got %q", out)
	}
	if !strings.Contains(out, "Target deregistration is in progress") {
		t.Fatalf("expected the published Target.DeregistrationInProgress description, got %q", out)
	}

	runCLI(t, awsCLI("elbv2", "delete-listener", "--listener-arn", listenerArn))
	runCLI(t, awsCLI("elbv2", "delete-target-group", "--target-group-arn", tgArn))
	runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn))
}

// TestELBv2HealthCheckMatcherCLI holds the health check to the target group's
// Matcher: "The codes to use when checking for a successful response from a
// target ... The default value is 200." A target that answers outside those
// codes is unhealthy with `Target.ResponseCodeMismatch` — "The health checks
// did not return an expected HTTP code" — and the description names the code
// the target returned.
func TestELBv2HealthCheckMatcherCLI(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
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

	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.95.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnet1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.95.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subnet2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.95.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	lbArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-matcher-lb", "--type", "application",
		"--subnets", subnet1, subnet2,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))

	tgArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-matcher-tg",
		"--protocol", "HTTP", "--port", "80",
		"--vpc-id", vpcID, "--target-type", "ip",
		"--matcher", "HttpCode=200",
		"--health-check-timeout-seconds", "2",
		"--health-check-interval-seconds", "5",
		"--unhealthy-threshold-count", "2",
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")))
	runCLI(t, awsCLI("elbv2", "register-targets",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort))
	listenerArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-listener",
		"--load-balancer-arn", lbArn, "--protocol", "HTTP", "--port", "80",
		"--default-actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Listeners[0].ListenerArn", "--output", "text")))

	waitForELBv2TargetHealthCLI(t, tgArn, "Id="+targetHost+",Port="+targetPort, "unhealthy")
	out := runCLI(t, awsCLI("elbv2", "describe-target-health",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort,
		"--query", "TargetHealthDescriptions[0].TargetHealth.[Reason,Description]",
		"--output", "text"))
	if !strings.Contains(out, "Target.ResponseCodeMismatch") {
		t.Fatalf("expected Target.ResponseCodeMismatch for a code outside the matcher, got %q", out)
	}
	if !strings.Contains(out, "Health checks failed with these codes: [418]") {
		t.Fatalf("expected the description to name the code the target returned, got %q", out)
	}

	// Widening the matcher to include the code the target answers with puts it
	// back in service, which is the same check read the other way round. A
	// multi-value HttpCode goes in as JSON: the command-line client's shorthand
	// reads the comma as a list separator and rejects the result.
	runCLI(t, awsCLI("elbv2", "modify-target-group",
		"--target-group-arn", tgArn, "--matcher", `{"HttpCode":"200,418"}`))
	waitForELBv2TargetHealthCLI(t, tgArn, "Id="+targetHost+",Port="+targetPort, "healthy")

	runCLI(t, awsCLI("elbv2", "delete-listener", "--listener-arn", listenerArn))
	runCLI(t, awsCLI("elbv2", "delete-target-group", "--target-group-arn", tgArn))
	runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn))
}
