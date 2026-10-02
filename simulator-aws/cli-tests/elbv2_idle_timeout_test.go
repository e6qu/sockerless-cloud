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

// TestELBv2IdleTimeoutCLI sets the Application Load Balancer's
// idle_timeout.timeout_seconds with the AWS CLI and holds the data plane to it:
// a target silent past the idle timeout draws a 504 Gateway Timeout, and a
// target that answers within it is served.
func TestELBv2IdleTimeoutCLI(t *testing.T) {
	targetDone := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/silent" {
			select {
			case <-r.Context().Done():
			case <-targetDone:
			}
			return
		}
		_, _ = w.Write([]byte("prompt"))
	}))
	defer targetServer.Close()
	defer close(targetDone)
	targetURL, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetHost, targetPort, err := net.SplitHostPort(targetURL.Host)
	if err != nil {
		t.Fatal(err)
	}

	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.89.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnet1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.89.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subnet2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.89.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	lbOut := strings.Fields(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-idle-lb", "--type", "application",
		"--subnets", subnet1, subnet2,
		"--query", "LoadBalancers[0].[LoadBalancerArn,DNSName]", "--output", "text")))
	if len(lbOut) != 2 {
		t.Fatalf("create-load-balancer answered %q", lbOut)
	}
	lbArn, lbDNSName := lbOut[0], lbOut[1]
	tgArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-idle-tg",
		"--protocol", "HTTP", "--port", "80",
		"--vpc-id", vpcID, "--target-type", "ip",
		"--health-check-path", "/healthz",
		"--health-check-timeout-seconds", "2",
		"--health-check-interval-seconds", "5",
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")))
	runCLI(t, awsCLI("elbv2", "register-targets",
		"--target-group-arn", tgArn,
		"--targets", "Id="+targetHost+",Port="+targetPort))
	listenerArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-listener",
		"--load-balancer-arn", lbArn, "--protocol", "HTTP", "--port", "80",
		"--default-actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Listeners[0].ListenerArn", "--output", "text")))
	waitForELBv2TargetHealthCLI(t, tgArn, "Id="+targetHost+",Port="+targetPort, "healthy")

	out := runCLI(t, awsCLI("elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", lbArn,
		"--attributes", "Key=idle_timeout.timeout_seconds,Value=1",
		"--query", "Attributes[?Key=='idle_timeout.timeout_seconds'].Value", "--output", "text"))
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("modify-load-balancer-attributes answered idle timeout %q, want 1", out)
	}
	errOut := runCLIExpectError(t, awsCLI("elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", lbArn,
		"--attributes", "Key=idle_timeout.timeout_seconds,Value=0"))
	if !strings.Contains(errOut, "InvalidConfigurationRequest") {
		t.Fatalf("an idle timeout of 0 was not refused as InvalidConfigurationRequest: %s", errOut)
	}

	get := func(path string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			t.Fatalf("build data-plane request: %v", err)
		}
		req.Host = lbDNSName
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s through the load balancer: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return resp.StatusCode, string(body)
	}
	if status, body := get("/silent"); status != http.StatusGatewayTimeout || !strings.Contains(body, "504 Gateway Time-out") {
		t.Fatalf("silent target answered %d %q, want 504 Gateway Time-out", status, body)
	}
	if status, body := get("/prompt"); status != http.StatusOK || body != "prompt" {
		t.Fatalf("prompt target answered %d %q, want 200 prompt", status, body)
	}

	runCLI(t, awsCLI("elbv2", "delete-listener", "--listener-arn", listenerArn))
	runCLI(t, awsCLI("elbv2", "delete-target-group", "--target-group-arn", tgArn))
	runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn))
}
