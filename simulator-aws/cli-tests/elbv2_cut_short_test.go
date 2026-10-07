package aws_cli_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestELBv2CutShortTargetCLI builds an Application Load Balancer with the AWS
// CLI in front of a target that closes its connection in the middle of a
// chunked body: the load balancer has already relayed the target's status and
// headers, so it ends the client's response where the target stopped rather
// than appending an error to it.
func TestELBv2CutShortTargetCLI(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\npartial\r\n")
		_ = buf.Flush()
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
		"--cidr-block", "10.93.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnet1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.93.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subnet2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.93.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	lbOut := strings.Fields(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-cut-short-lb", "--type", "application",
		"--subnets", subnet1, subnet2,
		"--query", "LoadBalancers[0].[LoadBalancerArn,DNSName]", "--output", "text")))
	if len(lbOut) != 2 {
		t.Fatalf("create-load-balancer answered %q", lbOut)
	}
	lbArn, lbDNSName := lbOut[0], lbOut[1]
	tgArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-cut-short-tg",
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

	req, err := http.NewRequest(http.MethodGet, baseURL+"/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = lbDNSName
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET through the load balancer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the target's 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v, want the response to end where the target stopped", err)
	}
	if string(body) != "partial" {
		t.Fatalf("body = %q, want only what the target sent", body)
	}
}
