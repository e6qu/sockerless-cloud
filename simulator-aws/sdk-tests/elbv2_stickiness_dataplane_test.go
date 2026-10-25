package aws_sdk_test

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Application Load Balancer's weighted forward with target group
// stickiness keeps a client on the target group its first request reached,
// through the AWSALBTG cookie it sets for DurationSeconds; a target group
// with lb_cookie stickiness keeps a client on one target through the AWSALB
// cookie, while clients without a cookie are spread by weight and in turn.
// A forward that enables stickiness without a duration, or outside 1-604800
// seconds, is refused.
func TestELBv2_StickinessKeepsAClientOnOneTargetGroupAndTarget(t *testing.T) {
	t.Parallel()
	elb := elbv2Client()
	vpcID, subnets := elbv2FidVPCSubnets(t, "10.23.0.0/16", "10.23.1.0/24", "10.23.2.0/24")
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniquename.New("sticky-lb")), Type: elbtypes.LoadBalancerTypeEnumApplication, Subnets: subnets,
	})
	require.NoError(t, err)
	lbArn := aws.ToString(lbOut.LoadBalancers[0].LoadBalancerArn)
	lbDNSName := aws.ToString(lbOut.LoadBalancers[0].DNSName)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	})

	// Each target answers with its own name.
	target := func(name string) elbtypes.TargetDescription {
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
		return elbtypes.TargetDescription{Id: aws.String(host), Port: aws.Int32(int32(port))}
	}
	targets := map[string][]elbtypes.TargetDescription{}
	targetGroup := func(name string, members ...elbtypes.TargetDescription) string {
		t.Helper()
		out, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
			Name: aws.String(uniquename.New(name)), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(80),
			VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
			HealthCheckIntervalSeconds: aws.Int32(5), HealthCheckTimeoutSeconds: aws.Int32(2),
		})
		require.NoError(t, err)
		arn := aws.ToString(out.TargetGroups[0].TargetGroupArn)
		t.Cleanup(func() {
			_, _ = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(arn)})
		})
		_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{TargetGroupArn: aws.String(arn), Targets: members})
		require.NoError(t, err)
		targets[arn] = members
		return arn
	}
	groupA := targetGroup("sticky-a", target("a"))
	groupB := targetGroup("sticky-b", target("b"))
	groupC := targetGroup("sticky-c", target("c1"), target("c2"))
	_, err = elb.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: aws.String(groupC),
		Attributes: []elbtypes.TargetGroupAttribute{
			{Key: aws.String("stickiness.enabled"), Value: aws.String("true")},
			{Key: aws.String("stickiness.type"), Value: aws.String("lb_cookie")},
			{Key: aws.String("stickiness.lb_cookie.duration_seconds"), Value: aws.String("120")},
		},
	})
	require.NoError(t, err)

	listenerOut, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(80),
		DefaultActions: []elbtypes.Action{{
			Type: elbtypes.ActionTypeEnumFixedResponse,
			FixedResponseConfig: &elbtypes.FixedResponseActionConfig{
				StatusCode: aws.String("404"), ContentType: aws.String("text/plain"), MessageBody: aws.String("no route"),
			},
		}},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(listenerOut.Listeners[0].ListenerArn)
	weighted := func(stickiness *elbtypes.TargetGroupStickinessConfig) []elbtypes.Action {
		return []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, ForwardConfig: &elbtypes.ForwardActionConfig{
			TargetGroups: []elbtypes.TargetGroupTuple{
				{TargetGroupArn: aws.String(groupA), Weight: aws.Int32(1)},
				{TargetGroupArn: aws.String(groupB), Weight: aws.Int32(1)},
			},
			TargetGroupStickinessConfig: stickiness,
		}}}
	}
	pathRule := func(priority int32, path string, actions []elbtypes.Action) error {
		_, err := elb.CreateRule(ctx, &elbv2.CreateRuleInput{
			ListenerArn: aws.String(listenerArn), Priority: aws.Int32(priority), Actions: actions,
			Conditions: []elbtypes.RuleCondition{{Field: aws.String("path-pattern"), PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{path}}}},
		})
		return err
	}
	requireELBv2Error(t, pathRule(5, "/split", weighted(&elbtypes.TargetGroupStickinessConfig{Enabled: aws.Bool(true)})),
		"ValidationError", "DurationSeconds")
	requireELBv2Error(t, pathRule(5, "/split", weighted(&elbtypes.TargetGroupStickinessConfig{Enabled: aws.Bool(true), DurationSeconds: aws.Int32(604801)})),
		"ValidationError", "604800")
	_, err = elb.ModifyListener(ctx, &elbv2.ModifyListenerInput{
		ListenerArn:    aws.String(listenerArn),
		DefaultActions: weighted(&elbtypes.TargetGroupStickinessConfig{Enabled: aws.Bool(true), DurationSeconds: aws.Int32(0)}),
	})
	requireELBv2Error(t, err, "ValidationError", "604800")
	require.NoError(t, pathRule(10, "/split", weighted(&elbtypes.TargetGroupStickinessConfig{Enabled: aws.Bool(true), DurationSeconds: aws.Int32(60)})))
	require.NoError(t, pathRule(20, "/c", []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(groupC)}}))
	rules, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	require.Len(t, rules.Rules, 3)
	stickiness := rules.Rules[0].Actions[0].ForwardConfig.TargetGroupStickinessConfig
	require.NotNil(t, stickiness)
	assert.True(t, aws.ToBool(stickiness.Enabled))
	assert.EqualValues(t, 60, aws.ToInt32(stickiness.DurationSeconds))

	for arn, members := range targets {
		for _, member := range members {
			waitForELBv2TargetHealth(t, arn, member, elbtypes.TargetHealthStateEnumHealthy)
		}
	}
	send := func(client *http.Client, path string) (string, []*http.Cookie) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		require.NoError(t, err)
		req.Host = lbDNSName
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", path, body)
		return string(body), resp.Cookies()
	}
	cookieNamed := func(cookies []*http.Cookie, name string) *http.Cookie {
		for _, cookie := range cookies {
			if cookie.Name == name {
				return cookie
			}
		}
		return nil
	}

	// A client that keeps its cookies stays on the group its first request
	// reached, for the duration the rule set.
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	sticky := &http.Client{Jar: jar}
	first, cookies := send(sticky, "/split")
	groupCookie := cookieNamed(cookies, "AWSALBTG")
	require.NotNil(t, groupCookie, "the first answer sets the target group stickiness cookie: %v", cookies)
	assert.Equal(t, 60, groupCookie.MaxAge)
	assert.Equal(t, "/", groupCookie.Path)
	for range 30 {
		body, cookies := send(sticky, "/split")
		assert.Equal(t, first, body)
		assert.Nil(t, cookieNamed(cookies, "AWSALBTG"), "a request carrying the cookie is not given another")
	}
	// A client without cookies is spread between the groups by weight, and a
	// forged cookie counts for nothing.
	served := map[string]int{}
	for range 60 {
		body, _ := send(http.DefaultClient, "/split")
		served[body]++
	}
	assert.Len(t, served, 2, "weights 1:1 reach both groups: %v", served)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/split", nil)
	require.NoError(t, err)
	req.Host = lbDNSName
	req.AddCookie(&http.Cookie{Name: "AWSALBTG", Value: "forged"})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.NotNil(t, cookieNamed(resp.Cookies(), "AWSALBTG"), "a cookie the load balancer did not sign is replaced")

	// lb_cookie stickiness keeps a client on one target of the group; clients
	// without the cookie take the targets in turn.
	jar, err = cookiejar.New(nil)
	require.NoError(t, err)
	sticky = &http.Client{Jar: jar}
	first, cookies = send(sticky, "/c")
	targetCookie := cookieNamed(cookies, "AWSALB")
	require.NotNil(t, targetCookie, "the first answer sets the target stickiness cookie: %v", cookies)
	assert.Equal(t, 120, targetCookie.MaxAge)
	for range 20 {
		body, _ := send(sticky, "/c")
		assert.Equal(t, first, body)
	}
	served = map[string]int{}
	for range 10 {
		body, _ := send(http.DefaultClient, "/c")
		served[body]++
	}
	assert.Equal(t, map[string]int{"c1": 5, "c2": 5}, served, "round robin turns among the targets in service")
}
