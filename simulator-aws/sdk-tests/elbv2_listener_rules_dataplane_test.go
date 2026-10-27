package aws_sdk_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Application Load Balancer evaluates its listener's rules by priority and
// performs the first matching rule's actions, falling back to the listener's
// default actions: forward to a target group, a fixed response, or a
// redirect built from the request.
func TestELBv2_ListenerRulesRouteRequests(t *testing.T) {
	t.Parallel()
	ec2c := ec2Client()
	elb := elbv2Client()

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.187.0.0/16")})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId
	var subnets []string
	for i, zone := range []string{"us-east-1a", "us-east-1b"} {
		out, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId:            aws.String(vpcID),
			CidrBlock:        aws.String("10.187." + strconv.Itoa(i+1) + ".0/24"),
			AvailabilityZone: aws.String(zone),
		})
		require.NoError(t, err)
		subnets = append(subnets, *out.Subnet.SubnetId)
	}
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("sdk-rules-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: subnets,
	})
	require.NoError(t, err)
	lbArn := *lbOut.LoadBalancers[0].LoadBalancerArn
	lbDNSName := *lbOut.LoadBalancers[0].DNSName

	targets := map[string]elbtypes.TargetDescription{}
	targetGroup := func(name string) string {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		}))
		t.Cleanup(server.Close)
		u, err := url.Parse(server.URL)
		require.NoError(t, err)
		host, portText, err := net.SplitHostPort(u.Host)
		require.NoError(t, err)
		port, err := strconv.Atoi(portText)
		require.NoError(t, err)
		out, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
			Name:                       aws.String(name),
			Protocol:                   elbtypes.ProtocolEnumHttp,
			Port:                       aws.Int32(80),
			VpcId:                      aws.String(vpcID),
			TargetType:                 elbtypes.TargetTypeEnumIp,
			HealthCheckIntervalSeconds: aws.Int32(5),
			HealthCheckTimeoutSeconds:  aws.Int32(2),
		})
		require.NoError(t, err)
		arn := *out.TargetGroups[0].TargetGroupArn
		target := elbtypes.TargetDescription{Id: aws.String(host), Port: aws.Int32(int32(port))}
		_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{TargetGroupArn: aws.String(arn), Targets: []elbtypes.TargetDescription{target}})
		require.NoError(t, err)
		targets[arn] = target
		return arn
	}
	groupA, groupB := targetGroup("sdk-rules-a"), targetGroup("sdk-rules-b")

	fixed := func(status, contentType, body string) []elbtypes.Action {
		return []elbtypes.Action{{
			Type: elbtypes.ActionTypeEnumFixedResponse,
			FixedResponseConfig: &elbtypes.FixedResponseActionConfig{
				StatusCode: aws.String(status), ContentType: aws.String(contentType), MessageBody: aws.String(body),
			},
		}}
	}
	forward := func(arn string) []elbtypes.Action {
		return []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(arn)}}
	}
	listenerOut, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttp,
		Port:            aws.Int32(80),
		DefaultActions:  fixed("404", "text/plain", "no route"),
	})
	require.NoError(t, err)
	listenerArn := *listenerOut.Listeners[0].ListenerArn
	rule := func(priority int32, actions []elbtypes.Action, conditions ...elbtypes.RuleCondition) {
		t.Helper()
		_, err := elb.CreateRule(ctx, &elbv2.CreateRuleInput{
			ListenerArn: aws.String(listenerArn), Priority: aws.Int32(priority), Conditions: conditions, Actions: actions,
		})
		require.NoError(t, err)
	}
	rule(5, fixed("403", "text/plain", "blocked"), elbtypes.RuleCondition{
		Field: aws.String("source-ip"), SourceIpConfig: &elbtypes.SourceIpConditionConfig{Values: []string{"203.0.113.0/24"}},
	})
	rule(10, fixed("500", "text/plain", "wrong host"), elbtypes.RuleCondition{
		Field: aws.String("host-header"), HostHeaderConfig: &elbtypes.HostHeaderConditionConfig{Values: []string{"shop.example.com"}},
	})
	rule(20, forward(groupB),
		elbtypes.RuleCondition{Field: aws.String("path-pattern"), PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{"/b/*"}}},
		elbtypes.RuleCondition{Field: aws.String("http-request-method"), HttpRequestMethodConfig: &elbtypes.HttpRequestMethodConditionConfig{Values: []string{"GET"}}},
	)
	rule(30, fixed("418", "text/plain", "short and stout"), elbtypes.RuleCondition{
		Field: aws.String("http-header"), HttpHeaderConfig: &elbtypes.HttpHeaderConditionConfig{HttpHeaderName: aws.String("X-Mode"), Values: []string{"fix*"}},
	})
	rule(40, []elbtypes.Action{{
		Type: elbtypes.ActionTypeEnumRedirect,
		RedirectConfig: &elbtypes.RedirectActionConfig{
			Protocol: aws.String("HTTPS"), Port: aws.String("443"), Host: aws.String("example.org"),
			Path: aws.String("/landing/#{path}"), Query: aws.String("from=#{host}"),
			StatusCode: elbtypes.RedirectActionStatusCodeEnumHttp302,
		},
	}}, elbtypes.RuleCondition{
		Field: aws.String("query-string"), QueryStringConfig: &elbtypes.QueryStringConditionConfig{
			Values: []elbtypes.QueryStringKeyValuePair{{Key: aws.String("go"), Value: aws.String("else*")}},
		},
	})
	weighted := func(weightA, weightB int32) []elbtypes.Action {
		return []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, ForwardConfig: &elbtypes.ForwardActionConfig{
			TargetGroups: []elbtypes.TargetGroupTuple{
				{TargetGroupArn: aws.String(groupA), Weight: aws.Int32(weightA)},
				{TargetGroupArn: aws.String(groupB), Weight: aws.Int32(weightB)},
			},
		}}}
	}
	rule(45, weighted(1, 3), elbtypes.RuleCondition{
		Field: aws.String("path-pattern"), PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{"/split"}},
	})
	rule(46, weighted(0, 1), elbtypes.RuleCondition{
		Field: aws.String("path-pattern"), PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{"/only-b"}},
	})
	rule(50, forward(groupA),
		elbtypes.RuleCondition{Field: aws.String("host-header"), HostHeaderConfig: &elbtypes.HostHeaderConditionConfig{Values: []string{lbDNSName}}},
		elbtypes.RuleCondition{Field: aws.String("path-pattern"), PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{"/a?*"}}},
	)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method, path string, header http.Header) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, nil)
		require.NoError(t, err)
		req.Host = lbDNSName
		for name, values := range header {
			req.Header[name] = values
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, resp.Header, string(body)
	}
	for arn, target := range targets {
		waitForELBv2TargetHealth(t, arn, target, elbtypes.TargetHealthStateEnumHealthy)
	}

	status, _, body := send(http.MethodGet, "/b/item", nil)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "sdk-rules-b", body, "a GET of /b/* goes to the second group")
	status, header, body := send(http.MethodPost, "/b/item", nil)
	assert.Equal(t, http.StatusNotFound, status, "a POST meets the path but not the method")
	assert.Equal(t, "no route", body)
	assert.Equal(t, "text/plain; charset=utf-8", header.Get("Content-Type"))
	status, _, body = send(http.MethodGet, "/a1", nil)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "sdk-rules-a", body)
	status, _, body = send(http.MethodGet, "/c", http.Header{"X-Mode": {"FIXED"}})
	assert.Equal(t, http.StatusTeapot, status, "header values compare without case")
	assert.Equal(t, "short and stout", body)
	status, header, _ = send(http.MethodGet, "/c/d?go=ELSEWHERE", nil)
	assert.Equal(t, http.StatusFound, status)
	assert.Equal(t, "https://example.org:443/landing/c/d?from="+lbDNSName, header.Get("Location"))
	status, _, body = send(http.MethodGet, "/a", nil)
	assert.Equal(t, http.StatusNotFound, status, "/a?* needs a character after /a")
	assert.Equal(t, "no route", body)

	// A weighted forward divides requests among its target groups by weight;
	// a group weighted 0 receives none.
	served := map[string]int{}
	for range 200 {
		status, _, body := send(http.MethodGet, "/split", nil)
		require.Equal(t, http.StatusOK, status)
		served[body]++
	}
	assert.InDelta(t, 150, served["sdk-rules-b"], 40, "weights 1:3 send about three quarters to the second group: %v", served)
	assert.Equal(t, 200, served["sdk-rules-a"]+served["sdk-rules-b"])
	for range 20 {
		_, _, body := send(http.MethodGet, "/only-b", nil)
		require.Equal(t, "sdk-rules-b", body)
	}

	_, err = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(groupA)})
	var inUse *elbtypes.ResourceInUseException
	require.ErrorAs(t, err, &inUse)

	_, err = elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
		Attributes:      []elbtypes.LoadBalancerAttribute{{Key: aws.String("deletion_protection.enabled"), Value: aws.String("true")}},
	})
	require.NoError(t, err)
	_, err = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	var protected *elbtypes.OperationNotPermittedException
	require.ErrorAs(t, err, &protected)
	_, err = elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
		Attributes:      []elbtypes.LoadBalancerAttribute{{Key: aws.String("deletion_protection.enabled"), Value: aws.String("false")}},
	})
	require.NoError(t, err)
	_, err = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	require.NoError(t, err)
	for _, arn := range []string{groupA, groupB} {
		_, err = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(arn)})
		require.NoError(t, err, "with its load balancer, listener and rules gone, the group is no longer in use")
	}
}
