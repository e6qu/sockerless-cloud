package alb_forwarded_headers_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestALBForwardedHeadersTerraform puts an Application Load Balancer that
// terraform-provider-aws configured with xff_header_processing_mode and
// enable_xff_client_port in front of a target that echoes the headers it
// received. The target sees the client's IP and port appended to
// X-Forwarded-For, the listener's protocol and port, and an X-Amzn-Trace-Id.
func TestALBForwardedHeadersTerraform(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	targetIP, targetPort, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)

	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	vars := []string{"-var", "target_ip=" + targetIP, "-var", "target_port=" + targetPort}
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars...)...)
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	dnsName := outputs["dns_name"].Value
	require.NotEmpty(t, dnsName)

	client := elbv2.New(elbv2.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	port, err := strconv.Atoi(targetPort)
	require.NoError(t, err)
	require.NoError(t, elbv2.NewTargetInServiceWaiter(client).Wait(context.Background(), &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(outputs["target_group_arn"].Value),
		Targets:        []elbtypes.TargetDescription{{Id: aws.String(targetIP), Port: aws.Int32(int32(port))}},
	}, time.Minute))

	req, err := http.NewRequest(http.MethodGet, env.BaseURL+"/headers", nil)
	require.NoError(t, err)
	req.Host = dnsName
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var seen http.Header
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&seen))
	assert.Regexp(t, `^203\.0\.113\.9, 127\.0\.0\.1:\d+$`, seen.Get("X-Forwarded-For"))
	assert.Equal(t, "http", seen.Get("X-Forwarded-Proto"))
	assert.Equal(t, "80", seen.Get("X-Forwarded-Port"))
	assert.Regexp(t, `^Root=1-[0-9a-f]{8}-[0-9a-f]{24}$`, seen.Get("X-Amzn-Trace-Id"))

	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars...)...)
}
