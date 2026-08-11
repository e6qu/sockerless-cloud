package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func elbv2FidVPCSubnet(t *testing.T, cidr, snCidr string) (string, string) {
	t.Helper()
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String(snCidr), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	return aws.ToString(vpc.Vpc.VpcId), aws.ToString(sn.Subnet.SubnetId)
}

func elbv2FidTargetGroup(t *testing.T, elb *elbv2.Client, vpcID, name string) string {
	t.Helper()
	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(name), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(80),
		VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	return aws.ToString(tg.TargetGroups[0].TargetGroupArn)
}

func elbv2FidLoadBalancer(t *testing.T, elb *elbv2.Client, subnetID, name string, lbType elbtypes.LoadBalancerTypeEnum) string {
	t.Helper()
	lb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(name), Type: lbType, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	return aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)
}

// TestELBv2_ListenerAuthAndForwardSDK covers the authenticate-oidc default
// action (the Pomerium/IAP proxy ALB shape), the weighted ForwardConfig on a
// rule, and SetRulePriorities — none of which round-tripped before.
func TestELBv2_ListenerAuthAndForwardSDK(t *testing.T) {
	elb := elbv2Client()
	vpcID, subnetID := elbv2FidVPCSubnet(t, "10.160.0.0/16", "10.160.1.0/24")
	tg1 := elbv2FidTargetGroup(t, elb, vpcID, "fid-auth-tg1")
	tg2 := elbv2FidTargetGroup(t, elb, vpcID, "fid-auth-tg2")
	lbArn := elbv2FidLoadBalancer(t, elb, subnetID, "fid-auth-alb", elbtypes.LoadBalancerTypeEnumApplication)
	certArn := importELBv2Certificate(t, "auth.example.test")
	listenerPort := availableELBv2ListenerPort(t)

	// HTTPS listener whose default action authenticates via OIDC, then forwards.
	ln, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumHttps, Port: aws.Int32(listenerPort),
		Certificates: []elbtypes.Certificate{{CertificateArn: aws.String(certArn)}},
		SslPolicy:    aws.String("ELBSecurityPolicy-TLS13-1-2-2021-06"),
		DefaultActions: []elbtypes.Action{
			{
				Type:  elbtypes.ActionTypeEnumAuthenticateOidc,
				Order: aws.Int32(1),
				AuthenticateOidcConfig: &elbtypes.AuthenticateOidcActionConfig{
					Issuer:                           aws.String("https://idp.example.test"),
					AuthorizationEndpoint:            aws.String("https://idp.example.test/authorize"),
					TokenEndpoint:                    aws.String("https://idp.example.test/token"),
					UserInfoEndpoint:                 aws.String("https://idp.example.test/userinfo"),
					ClientId:                         aws.String("client-123"),
					ClientSecret:                     aws.String("super-secret"),
					Scope:                            aws.String("openid email"),
					SessionCookieName:                aws.String("AWSELBAuthSessionCookie"),
					SessionTimeout:                   aws.Int64(3600),
					OnUnauthenticatedRequest:         elbtypes.AuthenticateOidcActionConditionalBehaviorEnumAuthenticate,
					AuthenticationRequestExtraParams: map[string]string{"prompt": "login"},
				},
			},
			{Type: elbtypes.ActionTypeEnumForward, Order: aws.Int32(2), TargetGroupArn: aws.String(tg1)},
		},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(ln.Listeners[0].ListenerArn)

	desc, err := elb.DescribeListeners(ctx, &elbv2.DescribeListenersInput{ListenerArns: []string{listenerArn}})
	require.NoError(t, err)
	require.Len(t, desc.Listeners[0].DefaultActions, 2)
	oidc := desc.Listeners[0].DefaultActions[0].AuthenticateOidcConfig
	require.NotNil(t, oidc, "authenticate-oidc config must round-trip")
	assert.Equal(t, "https://idp.example.test", aws.ToString(oidc.Issuer))
	assert.Equal(t, "https://idp.example.test/userinfo", aws.ToString(oidc.UserInfoEndpoint))
	assert.Equal(t, "client-123", aws.ToString(oidc.ClientId))
	assert.Equal(t, "openid email", aws.ToString(oidc.Scope))
	assert.Equal(t, int64(3600), aws.ToInt64(oidc.SessionTimeout))
	assert.Equal(t, "login", oidc.AuthenticationRequestExtraParams["prompt"])
	// ClientSecret is write-only — real ELBv2 never returns it from Describe.
	assert.Empty(t, aws.ToString(oidc.ClientSecret), "ClientSecret must NOT be echoed")

	// Weighted forward on a routing rule + target-group stickiness.
	rule, err := elb.CreateRule(ctx, &elbv2.CreateRuleInput{
		ListenerArn: aws.String(listenerArn), Priority: aws.Int32(10),
		Conditions: []elbtypes.RuleCondition{{
			Field: aws.String("path-pattern"), PathPatternConfig: &elbtypes.PathPatternConditionConfig{Values: []string{"/api/*"}},
		}},
		Actions: []elbtypes.Action{{
			Type: elbtypes.ActionTypeEnumForward,
			ForwardConfig: &elbtypes.ForwardActionConfig{
				TargetGroups: []elbtypes.TargetGroupTuple{
					{TargetGroupArn: aws.String(tg1), Weight: aws.Int32(70)},
					{TargetGroupArn: aws.String(tg2), Weight: aws.Int32(30)},
				},
				TargetGroupStickinessConfig: &elbtypes.TargetGroupStickinessConfig{
					Enabled: aws.Bool(true), DurationSeconds: aws.Int32(600),
				},
			},
		}},
	})
	require.NoError(t, err)
	ruleArn := aws.ToString(rule.Rules[0].RuleArn)

	fwd := rule.Rules[0].Actions[0].ForwardConfig
	require.NotNil(t, fwd, "ForwardConfig must round-trip")
	require.Len(t, fwd.TargetGroups, 2)
	assert.Equal(t, int32(70), aws.ToInt32(fwd.TargetGroups[0].Weight))
	assert.Equal(t, int32(30), aws.ToInt32(fwd.TargetGroups[1].Weight))
	require.NotNil(t, fwd.TargetGroupStickinessConfig)
	assert.True(t, aws.ToBool(fwd.TargetGroupStickinessConfig.Enabled))
	assert.Equal(t, int32(600), aws.ToInt32(fwd.TargetGroupStickinessConfig.DurationSeconds))

	// SetRulePriorities reorders the rule.
	_, err = elb.SetRulePriorities(ctx, &elbv2.SetRulePrioritiesInput{
		RulePriorities: []elbtypes.RulePriorityPair{{RuleArn: aws.String(ruleArn), Priority: aws.Int32(20)}},
	})
	require.NoError(t, err)
	rules, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{RuleArns: []string{ruleArn}})
	require.NoError(t, err)
	assert.Equal(t, "20", aws.ToString(rules.Rules[0].Priority), "SetRulePriorities must persist")
}

// TestELBv2_ListenerCertsAndMutualAuthSDK covers AddListenerCertificates /
// DescribeListenerCertificates / RemoveListenerCertificates (the
// aws_lb_listener_certificate resource), ModifyListener SslPolicy, and the
// mutual_authentication round-trip.
func TestELBv2_ListenerCertsAndMutualAuthSDK(t *testing.T) {
	elb := elbv2Client()
	vpcID, subnetID := elbv2FidVPCSubnet(t, "10.161.0.0/16", "10.161.1.0/24")
	tg := elbv2FidTargetGroup(t, elb, vpcID, "fid-cert-tg")
	lbArn := elbv2FidLoadBalancer(t, elb, subnetID, "fid-cert-alb", elbtypes.LoadBalancerTypeEnumApplication)
	defCert := importELBv2Certificate(t, "default.example.test")
	sniCert := importELBv2Certificate(t, "sni.example.test")
	listenerPort := availableELBv2ListenerPort(t)

	ignoreExpiry := true
	ln, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumHttps, Port: aws.Int32(listenerPort),
		Certificates: []elbtypes.Certificate{{CertificateArn: aws.String(defCert)}},
		SslPolicy:    aws.String("ELBSecurityPolicy-2016-08"),
		MutualAuthentication: &elbtypes.MutualAuthenticationAttributes{
			Mode: aws.String("verify"), TrustStoreArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:000000000000:truststore/ts/abc"),
			IgnoreClientCertificateExpiry: &ignoreExpiry,
		},
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tg)}},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(ln.Listeners[0].ListenerArn)

	// mutual_authentication round-trips.
	ma := ln.Listeners[0].MutualAuthentication
	require.NotNil(t, ma, "mutual_authentication must round-trip")
	assert.Equal(t, "verify", aws.ToString(ma.Mode))
	assert.True(t, aws.ToBool(ma.IgnoreClientCertificateExpiry))

	// ModifyListener changes the SSL policy.
	_, err = elb.ModifyListener(ctx, &elbv2.ModifyListenerInput{
		ListenerArn: aws.String(listenerArn), SslPolicy: aws.String("ELBSecurityPolicy-TLS13-1-2-2021-06"),
	})
	require.NoError(t, err)
	desc, err := elb.DescribeListeners(ctx, &elbv2.DescribeListenersInput{ListenerArns: []string{listenerArn}})
	require.NoError(t, err)
	assert.Equal(t, "ELBSecurityPolicy-TLS13-1-2-2021-06", aws.ToString(desc.Listeners[0].SslPolicy), "ModifyListener SslPolicy must persist")

	// SNI certificate lifecycle.
	_, err = elb.AddListenerCertificates(ctx, &elbv2.AddListenerCertificatesInput{
		ListenerArn: aws.String(listenerArn), Certificates: []elbtypes.Certificate{{CertificateArn: aws.String(sniCert)}},
	})
	require.NoError(t, err)
	certs, err := elb.DescribeListenerCertificates(ctx, &elbv2.DescribeListenerCertificatesInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	var sawDefault, sawSNI bool
	for _, c := range certs.Certificates {
		switch aws.ToString(c.CertificateArn) {
		case defCert:
			assert.True(t, aws.ToBool(c.IsDefault), "default cert IsDefault=true")
			sawDefault = true
		case sniCert:
			assert.False(t, aws.ToBool(c.IsDefault), "SNI cert IsDefault=false")
			sawSNI = true
		}
	}
	assert.True(t, sawDefault, "default certificate present")
	assert.True(t, sawSNI, "SNI certificate present after add")

	_, err = elb.RemoveListenerCertificates(ctx, &elbv2.RemoveListenerCertificatesInput{
		ListenerArn: aws.String(listenerArn), Certificates: []elbtypes.Certificate{{CertificateArn: aws.String(sniCert)}},
	})
	require.NoError(t, err)
	certs, err = elb.DescribeListenerCertificates(ctx, &elbv2.DescribeListenerCertificatesInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	for _, c := range certs.Certificates {
		assert.NotEqual(t, sniCert, aws.ToString(c.CertificateArn), "SNI cert removed")
	}
}
