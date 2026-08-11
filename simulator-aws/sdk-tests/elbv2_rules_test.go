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

// TestELBv2_ListenerRulesAndModifyListener covers listener-rule
// CRUD (host-header routing) plus ModifyListener changing the default action.
func TestELBv2_ListenerRulesAndModifyListener(t *testing.T) {
	ec2c := ec2Client()
	elb := elbv2Client()

	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sn1, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String("10.91.1.0/24"), AvailabilityZone: aws.String("us-east-1a")})
	require.NoError(t, err)
	sn2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String("10.91.2.0/24"), AvailabilityZone: aws.String("us-east-1b")})
	require.NoError(t, err)

	lb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("rule-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: []string{aws.ToString(sn1.Subnet.SubnetId), aws.ToString(sn2.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:       aws.String("rule-tg"),
		Protocol:   elbtypes.ProtocolEnumHttp,
		Port:       aws.Int32(80),
		VpcId:      aws.String(vpcID),
		TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)

	listener, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttp,
		Port:            aws.Int32(80),
		DefaultActions: []elbtypes.Action{{
			Type:           elbtypes.ActionTypeEnumForward,
			TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(listener.Listeners[0].ListenerArn)

	// CreateRule: host-header → forward to the target group.
	created, err := elb.CreateRule(ctx, &elbv2.CreateRuleInput{
		ListenerArn: aws.String(listenerArn),
		Priority:    aws.Int32(100),
		Conditions: []elbtypes.RuleCondition{{
			Field:            aws.String("host-header"),
			HostHeaderConfig: &elbtypes.HostHeaderConditionConfig{Values: []string{"app.example.com"}},
		}},
		Actions: []elbtypes.Action{{
			Type:           elbtypes.ActionTypeEnumForward,
			TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	require.Len(t, created.Rules, 1)
	ruleArn := aws.ToString(created.Rules[0].RuleArn)
	require.NotEmpty(t, ruleArn)
	assert.Equal(t, "100", aws.ToString(created.Rules[0].Priority))

	// DescribeRules returns the custom rule + the synthesized default rule.
	described, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	require.Len(t, described.Rules, 2)
	var custom, def *elbtypes.Rule
	for i := range described.Rules {
		if aws.ToBool(described.Rules[i].IsDefault) {
			def = &described.Rules[i]
		} else {
			custom = &described.Rules[i]
		}
	}
	require.NotNil(t, def, "a default rule must be present")
	require.NotNil(t, custom, "the created rule must be present")
	require.Len(t, custom.Conditions, 1)
	assert.Equal(t, "host-header", aws.ToString(custom.Conditions[0].Field))
	require.NotNil(t, custom.Conditions[0].HostHeaderConfig)
	assert.Equal(t, []string{"app.example.com"}, custom.Conditions[0].HostHeaderConfig.Values)

	// ModifyRule: change the matched host.
	_, err = elb.ModifyRule(ctx, &elbv2.ModifyRuleInput{
		RuleArn: aws.String(ruleArn),
		Conditions: []elbtypes.RuleCondition{{
			Field:            aws.String("host-header"),
			HostHeaderConfig: &elbtypes.HostHeaderConditionConfig{Values: []string{"api.example.com"}},
		}},
	})
	require.NoError(t, err)
	after, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{RuleArns: []string{ruleArn}})
	require.NoError(t, err)
	require.Len(t, after.Rules, 1)
	assert.Equal(t, []string{"api.example.com"}, after.Rules[0].Conditions[0].HostHeaderConfig.Values)

	// ModifyListener: swap the default action to a fixed response.
	ml, err := elb.ModifyListener(ctx, &elbv2.ModifyListenerInput{
		ListenerArn: aws.String(listenerArn),
		DefaultActions: []elbtypes.Action{{
			Type: elbtypes.ActionTypeEnumFixedResponse,
			FixedResponseConfig: &elbtypes.FixedResponseActionConfig{
				StatusCode:  aws.String("200"),
				ContentType: aws.String("text/plain"),
				MessageBody: aws.String("ok"),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, ml.Listeners, 1)
	require.Len(t, ml.Listeners[0].DefaultActions, 1)
	da := ml.Listeners[0].DefaultActions[0]
	assert.Equal(t, elbtypes.ActionTypeEnumFixedResponse, da.Type)
	require.NotNil(t, da.FixedResponseConfig)
	assert.Equal(t, "200", aws.ToString(da.FixedResponseConfig.StatusCode))

	// DeleteRule removes it.
	_, err = elb.DeleteRule(ctx, &elbv2.DeleteRuleInput{RuleArn: aws.String(ruleArn)})
	require.NoError(t, err)
	final, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	require.Len(t, final.Rules, 1, "only the default rule should remain")
	assert.True(t, aws.ToBool(final.Rules[0].IsDefault))
}

// TestELBv2_DescribeRulesNotFound proves DescribeRules raises RuleNotFound when
// a requested RuleArn doesn't exist, instead of silently returning a short list
// (which made terraform refresh see destroyed rules as still present).
func TestELBv2_DescribeRulesNotFound(t *testing.T) {
	elb := elbv2Client()
	_, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{
		RuleArns: []string{"arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/x/0/0/does-not-exist"},
	})
	require.Error(t, err)
	assertAWSAPIErrorCode(t, err, "RuleNotFound")
}
