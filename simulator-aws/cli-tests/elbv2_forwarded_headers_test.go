package aws_cli_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestELBv2ForwardedHeadersCLI builds an Application Load Balancer with the AWS
// CLI in front of a target that echoes the headers it received. The load
// balancer appends the client's IP to X-Forwarded-For, names the listener's
// protocol and port, and adds X-Amzn-Trace-Id; with
// routing.http.xff_client_port.enabled set through
// modify-load-balancer-attributes it appends the client's port as well.
func TestELBv2ForwardedHeadersCLI(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
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
		"--cidr-block", "10.85.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnet1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.85.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subnet2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.85.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	lbOut := strings.Fields(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-xff-lb", "--type", "application",
		"--subnets", subnet1, subnet2,
		"--query", "LoadBalancers[0].[LoadBalancerArn,DNSName]", "--output", "text")))
	if len(lbOut) != 2 {
		t.Fatalf("create-load-balancer answered %q", lbOut)
	}
	lbArn, lbDNSName := lbOut[0], lbOut[1]
	tgArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-xff-tg",
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
	t.Cleanup(func() {
		runCLI(t, awsCLI("elbv2", "delete-listener", "--listener-arn", listenerArn))
		runCLI(t, awsCLI("elbv2", "delete-target-group", "--target-group-arn", tgArn))
		runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn))
	})
	waitForELBv2TargetHealthCLI(t, tgArn, "Id="+targetHost+",Port="+targetPort, "healthy")

	received := func() http.Header {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, baseURL+"/headers", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = lbDNSName
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET through the load balancer: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want the target's 200", resp.StatusCode)
		}
		var seen http.Header
		if err := json.NewDecoder(resp.Body).Decode(&seen); err != nil {
			t.Fatal(err)
		}
		return seen
	}

	seen := received()
	if got := seen.Get("X-Forwarded-For"); got != "203.0.113.9, 127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want the client's IP appended", got)
	}
	if got := seen.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", got)
	}
	if got := seen.Get("X-Forwarded-Port"); got != "80" {
		t.Errorf("X-Forwarded-Port = %q, want 80", got)
	}
	if got := seen.Get("X-Amzn-Trace-Id"); !regexp.MustCompile(`^Root=1-[0-9a-f]{8}-[0-9a-f]{24}$`).MatchString(got) {
		t.Errorf("X-Amzn-Trace-Id = %q", got)
	}

	out := runCLI(t, awsCLI("elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", lbArn,
		"--attributes", "Key=routing.http.xff_client_port.enabled,Value=true",
		"--query", "Attributes[?Key=='routing.http.xff_client_port.enabled'].Value", "--output", "text"))
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("modify-load-balancer-attributes answered %q", out)
	}
	if got := received().Get("X-Forwarded-For"); !regexp.MustCompile(`^203\.0\.113\.9, 127\.0\.0\.1:\d+$`).MatchString(got) {
		t.Errorf("X-Forwarded-For with the client port = %q", got)
	}
}
