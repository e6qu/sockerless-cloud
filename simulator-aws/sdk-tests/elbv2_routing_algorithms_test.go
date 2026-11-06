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

// TestELBv2_RoutingAlgorithmsAndApplicationStickiness expects a target group
// to route by its load_balancing.algorithm.type and to hold a client with
// application-based stickiness, as the vendored alb-target-group-attributes
// page describes: least outstanding requests passes over a target with a
// request in progress, weighted random reaches every target, and the
// load balancer issues AWSALBAPP-0 once the target sets the configured
// application cookie. It refuses the combinations the page rules out.
func TestELBv2_RoutingAlgorithmsAndApplicationStickiness(t *testing.T) {
	t.Parallel()
	elb := elbv2Client()
	vpcID, subnets := elbv2FidVPCSubnets(t, "10.24.0.0/16", "10.24.1.0/24", "10.24.2.0/24")
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniquename.New("algo-lb")), Type: elbtypes.LoadBalancerTypeEnumApplication, Subnets: subnets,
	})
	require.NoError(t, err)
	lbArn := aws.ToString(lbOut.LoadBalancers[0].LoadBalancerArn)
	lbDNSName := aws.ToString(lbOut.LoadBalancers[0].DNSName)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	})

	// held makes the "slow" target hold a request open until the test
	// releases it.
	held := make(chan struct{})
	release := make(chan struct{})
	target := func(handler http.HandlerFunc) elbtypes.TargetDescription {
		t.Helper()
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		u, err := url.Parse(server.URL)
		require.NoError(t, err)
		host, portText, err := net.SplitHostPort(u.Host)
		require.NoError(t, err)
		port, err := strconv.Atoi(portText)
		require.NoError(t, err)
		return elbtypes.TargetDescription{Id: aws.String(host), Port: aws.Int32(int32(port))}
	}
	named := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/hold" {
				held <- struct{}{}
				<-release
			}
			if r.URL.Query().Get("login") == "1" {
				http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: name})
			}
			_, _ = io.WriteString(w, name)
		}
	}
	// A target is checked only once a listener routes to its group, so the
	// health waits follow the listener.
	registered := map[string][]elbtypes.TargetDescription{}
	group := func(prefix string, attrs map[string]string, members ...elbtypes.TargetDescription) string {
		t.Helper()
		out, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
			Name: aws.String(uniquename.New(prefix)), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(80),
			VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
			HealthCheckIntervalSeconds: aws.Int32(5), HealthCheckTimeoutSeconds: aws.Int32(2),
		})
		require.NoError(t, err)
		arn := aws.ToString(out.TargetGroups[0].TargetGroupArn)
		t.Cleanup(func() {
			_, _ = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(arn)})
		})
		var list []elbtypes.TargetGroupAttribute
		for k, v := range attrs {
			list = append(list, elbtypes.TargetGroupAttribute{Key: aws.String(k), Value: aws.String(v)})
		}
		_, err = elb.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{TargetGroupArn: aws.String(arn), Attributes: list})
		require.NoError(t, err)
		_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{TargetGroupArn: aws.String(arn), Targets: members})
		require.NoError(t, err)
		registered[arn] = members
		return arn
	}
	lor := group("algo-lor", map[string]string{"load_balancing.algorithm.type": "least_outstanding_requests"},
		target(named("lor-a")), target(named("lor-b")))
	random := group("algo-rand", map[string]string{"load_balancing.algorithm.type": "weighted_random"},
		target(named("rand-a")), target(named("rand-b")))
	app := group("algo-app", map[string]string{
		"stickiness.enabled": "true", "stickiness.type": "app_cookie",
		"stickiness.app_cookie.cookie_name": "SESSION", "stickiness.app_cookie.duration_seconds": "300",
	}, target(named("app-a")), target(named("app-b")))

	for _, refusal := range []map[string]string{
		{"load_balancing.algorithm.type": "fastest"},
		{"load_balancing.algorithm.type": "weighted_random", "stickiness.enabled": "true", "stickiness.type": "lb_cookie"},
		{"load_balancing.algorithm.type": "least_outstanding_requests", "slow_start.duration_seconds": "30"},
		{"stickiness.enabled": "true", "stickiness.type": "app_cookie", "stickiness.app_cookie.cookie_name": "AWSALBAPP"},
	} {
		var list []elbtypes.TargetGroupAttribute
		for k, v := range refusal {
			list = append(list, elbtypes.TargetGroupAttribute{Key: aws.String(k), Value: aws.String(v)})
		}
		_, err := elb.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{TargetGroupArn: aws.String(lor), Attributes: list})
		requireELBv2Error(t, err, "InvalidConfigurationRequest", "")
	}

	listener, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(80),
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(lor)}},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(listener.Listeners[0].ListenerArn)
	for priority, route := range map[int32][2]string{10: {"/random", random}, 20: {"/app", app}} {
		_, err := elb.CreateRule(ctx, &elbv2.CreateRuleInput{
			ListenerArn: aws.String(listenerArn), Priority: aws.Int32(priority),
			Actions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(route[1])}},
			Conditions: []elbtypes.RuleCondition{{Field: aws.String("path-pattern"),
				PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{route[0]}}}},
		})
		require.NoError(t, err)
	}

	for arn, members := range registered {
		for _, m := range members {
			waitForELBv2TargetHealth(t, arn, m, elbtypes.TargetHealthStateEnumHealthy)
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

	// One request held at whichever target took it leaves that target with
	// the most outstanding requests, so every request meanwhile goes to the
	// other.
	heldBy := make(chan string, 1)
	go func() {
		body, _ := send(http.DefaultClient, "/hold")
		heldBy <- body
	}()
	<-held
	others := map[string]int{}
	for range 6 {
		body, _ := send(http.DefaultClient, "/")
		others[body]++
	}
	close(release)
	holder := <-heldBy
	require.Len(t, others, 1, "least outstanding requests passes over the busy target: %v", others)
	assert.NotContains(t, others, holder)

	served := map[string]int{}
	for range 60 {
		body, _ := send(http.DefaultClient, "/random")
		served[body]++
	}
	assert.Len(t, served, 2, "weighted random reaches every target: %v", served)

	cookieNamed := func(cookies []*http.Cookie, name string) *http.Cookie {
		for _, c := range cookies {
			if c.Name == name {
				return c
			}
		}
		return nil
	}
	_, cookies := send(http.DefaultClient, "/app")
	assert.Nil(t, cookieNamed(cookies, "AWSALBAPP-0"), "no stickiness cookie before the target sets its application cookie")
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	sticky := &http.Client{Jar: jar}
	first, cookies := send(sticky, "/app?login=1")
	appCookie := cookieNamed(cookies, "AWSALBAPP-0")
	require.NotNil(t, appCookie, "the target's SESSION cookie earns a load balancer application cookie: %v", cookies)
	assert.Equal(t, 7*24*3600, appCookie.MaxAge)
	require.NotNil(t, cookieNamed(cookies, "SESSION"), "the target's own cookie reaches the client too")
	for range 20 {
		body, _ := send(sticky, "/app")
		assert.Equal(t, first, body)
	}
}
