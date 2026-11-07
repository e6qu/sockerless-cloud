package aws_cli_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2CLI_AuthenticateCognitoRuleSignsUserIn configures an
// authenticate-cognito rule with `aws elbv2 create-rule` and signs a user in
// through it: the load balancer sends the browser to the user pool's domain,
// redeems the code and forwards the request with the user's claims.
func TestELBv2CLI_AuthenticateCognitoRuleSignsUserIn(t *testing.T) {
	t.Parallel()
	suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano()%1_000_000, cognitoCLISeq.Add(1))

	identities := make(chan string, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			identities <- r.Header.Get("X-Amzn-Oidc-Identity")
		}
		_, _ = io.WriteString(w, "target:"+r.URL.Path)
	}))
	defer target.Close()
	targetHost, targetPort, err := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	require.NoError(t, err)

	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.38.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	var subnets []string
	for i, az := range []string{"us-east-1a", "us-east-1b"} {
		subnets = append(subnets, strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpcID,
			"--cidr-block", fmt.Sprintf("10.38.%d.0/24", i+1), "--availability-zone", az, "--query", "Subnet.SubnetId", "--output", "text"))))
	}
	var lb struct {
		LoadBalancers []struct {
			LoadBalancerArn string `json:"LoadBalancerArn"`
			DNSName         string `json:"DNSName"`
		} `json:"LoadBalancers"`
	}
	parseJSON(t, runCLI(t, awsCLI("elbv2", "create-load-balancer", "--name", "cli-auth-"+strings.ReplaceAll(suffix, "-", ""),
		"--type", "application", "--subnets", subnets[0], subnets[1], "--output", "json")), &lb)
	lbArn, lbName := lb.LoadBalancers[0].LoadBalancerArn, lb.LoadBalancers[0].DNSName
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn).Run() })
	tgArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group", "--name", "cli-auth-tg-"+strings.ReplaceAll(suffix, "-", ""),
		"--protocol", "HTTP", "--port", targetPort, "--vpc-id", vpcID, "--target-type", "ip", "--health-check-path", "/healthz",
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")))
	runCLI(t, awsCLI("elbv2", "register-targets", "--target-group-arn", tgArn, "--targets", "Id="+targetHost+",Port="+targetPort))

	port := availableELBv2ListenerPortCLI(t)
	origin := "https://" + lbName + ":" + port
	var pool struct {
		UserPool struct{ Id, Arn string } `json:"UserPool"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "create-user-pool", "--pool-name", "cli-alb-"+suffix, "--output", "json")), &pool)
	domain := "cli-alb-" + suffix
	runCLI(t, awsCLI("cognito-idp", "create-user-pool-domain", "--user-pool-id", pool.UserPool.Id, "--domain", domain))
	clientID := strings.TrimSpace(runCLI(t, awsCLI("cognito-idp", "create-user-pool-client", "--user-pool-id", pool.UserPool.Id,
		"--client-name", "alb", "--generate-secret", "--allowed-o-auth-flows", "code", "--allowed-o-auth-flows-user-pool-client",
		"--allowed-o-auth-scopes", "openid", "--callback-urls", origin+"/oauth2/idpresponse", "--supported-identity-providers", "COGNITO",
		"--query", "UserPoolClient.ClientId", "--output", "text")))
	runCLI(t, awsCLI("cognito-idp", "admin-create-user", "--user-pool-id", pool.UserPool.Id, "--username", "ivy", "--message-action", "SUPPRESS"))
	runCLI(t, awsCLI("cognito-idp", "admin-set-user-password", "--user-pool-id", pool.UserPool.Id, "--username", "ivy",
		"--password", "Ivy-Password-123", "--permanent"))

	actions, err := json.Marshal([]map[string]any{
		{"Type": "authenticate-cognito", "Order": 1, "AuthenticateCognitoConfig": map[string]any{
			"UserPoolArn": pool.UserPool.Arn, "UserPoolClientId": clientID, "UserPoolDomain": domain,
		}},
		{"Type": "forward", "Order": 2, "TargetGroupArn": tgArn},
	})
	require.NoError(t, err)
	actionsFile := filepath.Join(t.TempDir(), "actions.json")
	require.NoError(t, os.WriteFile(actionsFile, actions, 0o600))

	httpListener := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-listener", "--load-balancer-arn", lbArn, "--protocol", "HTTP",
		"--port", availableELBv2ListenerPortCLI(t), "--default-actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Listeners[0].ListenerArn", "--output", "text")))
	refused, err := awsCLI("elbv2", "create-rule", "--listener-arn", httpListener, "--priority", "1",
		"--conditions", "Field=path-pattern,Values=/members/*", "--actions", "file://"+actionsFile).CombinedOutput()
	require.Error(t, err, "authenticate actions are supported only with HTTPS listeners")
	assert.Contains(t, string(refused), "InvalidLoadBalancerAction")

	certArn := importELBv2CertificateCLI(t, lbName)
	listenerArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-listener", "--load-balancer-arn", lbArn, "--protocol", "HTTPS",
		"--port", port, "--certificates", "CertificateArn="+certArn, "--default-actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Listeners[0].ListenerArn", "--output", "text")))
	ruleOut := runCLI(t, awsCLI("elbv2", "create-rule", "--listener-arn", listenerArn, "--priority", "1",
		"--conditions", "Field=path-pattern,Values=/members/*", "--actions", "file://"+actionsFile, "--output", "json"))
	assert.Contains(t, ruleOut, `"UserPoolDomain": "`+domain+`"`)
	assert.NotContains(t, ruleOut, "ClientSecret", "the rule never reports the client secret it read")
	waitForELBv2TargetHealthCLI(t, tgArn, "Id="+targetHost+",Port="+targetPort, "healthy")

	// The suite runs the simulator without its Route 53 Resolver; the load
	// balancer's first listener holds 127.0.0.1 at a port free there.
	simHost := strings.TrimPrefix(baseURL, "http://")
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	lbTransport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: lbName}}
	// The response names the request as the browser made it, so a relative
	// Location resolves against the cloud hostname, not the address dialled.
	browser := &http.Client{Jar: jar, Timeout: 30 * time.Second, Transport: cliRoundTrip(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		transport := lbTransport
		if r.URL.Hostname() == lbName {
			clone.URL.Host = "127.0.0.1:" + port
		} else {
			require.True(t, strings.HasSuffix(r.URL.Hostname(), ".amazoncognito.com"), r.URL.String())
			clone.URL.Scheme, clone.URL.Host = "http", simHost
			transport = http.DefaultTransport.(*http.Transport)
		}
		clone.Host = r.URL.Host
		resp, err := transport.RoundTrip(clone)
		if resp != nil {
			resp.Request = r
		}
		return resp, err
	})}

	resp, err := browser.Get(origin + "/members/home")
	require.NoError(t, err)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, "/login", resp.Request.URL.Path, "the rule sends a user without a session to the user pool: %s", page)
	resp, err = browser.PostForm(resp.Request.URL.String(), url.Values{"username": {"ivy"}, "password": {"Ivy-Password-123"}})
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "target:/members/home", string(body))
	assert.NotEmpty(t, <-identities, "the target sees the user's subject in x-amzn-oidc-identity")

	resp, err = browser.Get(origin + "/open")
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "target:/open", string(body), "a path outside the rule forwards without authentication")
	assert.Empty(t, <-identities)
}

type cliRoundTrip func(*http.Request) (*http.Response, error)

func (f cliRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
