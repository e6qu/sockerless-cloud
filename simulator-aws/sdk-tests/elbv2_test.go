package aws_sdk_test

import (
	"fmt"
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

func elbv2Client() *elbv2.Client {
	return elbv2.NewFromConfig(sdkConfig(), func(o *elbv2.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// Actions exercised: CreateLoadBalancer, DescribeLoadBalancers,
// ModifyLoadBalancerAttributes, DescribeLoadBalancerAttributes,
// DescribeCapacityReservation, SetSecurityGroups, SetSubnets, CreateTargetGroup,
// DescribeTargetGroups, ModifyTargetGroup, ModifyTargetGroupAttributes,
// DescribeTargetGroupAttributes, RegisterTargets, DeregisterTargets,
// DescribeTargetHealth, CreateListener, DescribeListeners, DescribeListenerAttributes,
// ModifyListenerAttributes, DeleteListener,
// AddTags, RemoveTags, DescribeTags, DescribeAccountLimits,
// DeleteTargetGroup, DeleteLoadBalancer.
func TestELBv2_LoadBalancerTargetGroupListenerLifecycle(t *testing.T) {
	ec2c := ec2Client()
	elb := elbv2Client()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		_, _ = w.Write([]byte("proxied"))
	}))
	defer targetServer.Close()
	targetURL, err := url.Parse(targetServer.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.90.0.0/16")})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId

	subnet1, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.90.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnet2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.90.2.0/24"),
		AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	sgOut, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("sdk-elbv2-sg"),
		Description: aws.String("sdk elbv2"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)

	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:           aws.String("sdk-lb"),
		Type:           elbtypes.LoadBalancerTypeEnumApplication,
		Scheme:         elbtypes.LoadBalancerSchemeEnumInternetFacing,
		Subnets:        []string{*subnet1.Subnet.SubnetId, *subnet2.Subnet.SubnetId},
		SecurityGroups: []string{*sgOut.GroupId},
		Tags:           []elbtypes.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	require.Len(t, lbOut.LoadBalancers, 1)
	lbArn := *lbOut.LoadBalancers[0].LoadBalancerArn
	lbDNSName := *lbOut.LoadBalancers[0].DNSName
	assert.Equal(t, elbtypes.LoadBalancerStateEnumActive, lbOut.LoadBalancers[0].State.Code)

	describeLB, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{lbArn},
	})
	require.NoError(t, err)
	require.Len(t, describeLB.LoadBalancers, 1)
	assert.Equal(t, "sdk-lb", *describeLB.LoadBalancers[0].LoadBalancerName)

	_, err = elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
		Attributes: []elbtypes.LoadBalancerAttribute{{
			Key:   aws.String("idle_timeout.timeout_seconds"),
			Value: aws.String("30"),
		}},
	})
	require.NoError(t, err)
	lbAttrs, err := elb.DescribeLoadBalancerAttributes(ctx, &elbv2.DescribeLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
	})
	require.NoError(t, err)
	assert.Contains(t, lbAttrs.Attributes, elbtypes.LoadBalancerAttribute{
		Key:   aws.String("idle_timeout.timeout_seconds"),
		Value: aws.String("30"),
	})
	capacity, err := elb.DescribeCapacityReservation(ctx, &elbv2.DescribeCapacityReservationInput{
		LoadBalancerArn: aws.String(lbArn),
	})
	require.NoError(t, err)
	require.NotEmpty(t, capacity.CapacityReservationState)

	sg2, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("sdk-elbv2-sg-2"),
		Description: aws.String("sdk elbv2 updated"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)
	_, err = elb.SetSecurityGroups(ctx, &elbv2.SetSecurityGroupsInput{
		LoadBalancerArn: aws.String(lbArn),
		SecurityGroups:  []string{*sg2.GroupId},
	})
	require.NoError(t, err)
	_, err = elb.SetSubnets(ctx, &elbv2.SetSubnetsInput{
		LoadBalancerArn: aws.String(lbArn),
		Subnets:         []string{*subnet1.Subnet.SubnetId, *subnet2.Subnet.SubnetId},
	})
	require.NoError(t, err)

	tgOut, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:       aws.String("sdk-tg"),
		Protocol:   elbtypes.ProtocolEnumHttp,
		Port:       aws.Int32(80),
		VpcId:      aws.String(vpcID),
		TargetType: elbtypes.TargetTypeEnumIp,
		Tags:       []elbtypes.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	require.Len(t, tgOut.TargetGroups, 1)
	tgArn := *tgOut.TargetGroups[0].TargetGroupArn

	_, err = elb.ModifyTargetGroup(ctx, &elbv2.ModifyTargetGroupInput{
		TargetGroupArn:             aws.String(tgArn),
		HealthCheckPath:            aws.String("/healthz"),
		HealthyThresholdCount:      aws.Int32(3),
		UnhealthyThresholdCount:    aws.Int32(2),
		HealthCheckIntervalSeconds: aws.Int32(10),
		HealthCheckTimeoutSeconds:  aws.Int32(2),
	})
	require.NoError(t, err)
	describeTG, err := elb.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{tgArn},
	})
	require.NoError(t, err)
	require.Len(t, describeTG.TargetGroups, 1)
	assert.Equal(t, "/healthz", *describeTG.TargetGroups[0].HealthCheckPath)

	_, err = elb.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: aws.String(tgArn),
		Attributes: []elbtypes.TargetGroupAttribute{{
			Key:   aws.String("deregistration_delay.timeout_seconds"),
			Value: aws.String("60"),
		}},
	})
	require.NoError(t, err)
	tgAttrs, err := elb.DescribeTargetGroupAttributes(ctx, &elbv2.DescribeTargetGroupAttributesInput{
		TargetGroupArn: aws.String(tgArn),
	})
	require.NoError(t, err)
	assert.Contains(t, tgAttrs.Attributes, elbtypes.TargetGroupAttribute{
		Key:   aws.String("deregistration_delay.timeout_seconds"),
		Value: aws.String("60"),
	})

	target := elbtypes.TargetDescription{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))}
	unreachableTarget := elbtypes.TargetDescription{Id: aws.String("192.0.2.254"), Port: aws.Int32(80)}
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{target, unreachableTarget},
	})
	require.NoError(t, err)
	health, err := elb.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(tgArn),
	})
	require.NoError(t, err)
	require.Len(t, health.TargetHealthDescriptions, 2)
	assert.Equal(t, elbtypes.TargetHealthStateEnumHealthy, health.TargetHealthDescriptions[0].TargetHealth.State)
	unhealthy, err := elb.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{unreachableTarget},
	})
	require.NoError(t, err)
	require.Len(t, unhealthy.TargetHealthDescriptions, 1)
	assert.Equal(t, elbtypes.TargetHealthStateEnumUnhealthy, unhealthy.TargetHealthDescriptions[0].TargetHealth.State)

	listenerOut, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttp,
		Port:            aws.Int32(80),
		DefaultActions: []elbtypes.Action{{
			Type:           elbtypes.ActionTypeEnumForward,
			TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	require.Len(t, listenerOut.Listeners, 1)
	proxiedReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/proxy-check", nil)
	require.NoError(t, err)
	proxiedReq.Host = lbDNSName
	proxiedResp, err := http.DefaultClient.Do(proxiedReq)
	require.NoError(t, err)
	require.NoError(t, proxiedResp.Body.Close())
	assert.Equal(t, http.StatusOK, proxiedResp.StatusCode, fmt.Sprintf("ELBv2 proxy status = %s", proxiedResp.Status))
	listenerArn := *listenerOut.Listeners[0].ListenerArn
	listeners, err := elb.DescribeListeners(ctx, &elbv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(lbArn),
	})
	require.NoError(t, err)
	require.Len(t, listeners.Listeners, 1)
	listenerAttrs, err := elb.DescribeListenerAttributes(ctx, &elbv2.DescribeListenerAttributesInput{
		ListenerArn: aws.String(listenerArn),
	})
	require.NoError(t, err)
	assert.Contains(t, listenerAttrs.Attributes, elbtypes.ListenerAttribute{
		Key:   aws.String("routing.http.response.server.enabled"),
		Value: aws.String("true"),
	})
	modifiedListenerAttrs, err := elb.ModifyListenerAttributes(ctx, &elbv2.ModifyListenerAttributesInput{
		ListenerArn: aws.String(listenerArn),
		Attributes: []elbtypes.ListenerAttribute{{
			Key:   aws.String("routing.http.response.server.enabled"),
			Value: aws.String("false"),
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, modifiedListenerAttrs.Attributes, elbtypes.ListenerAttribute{
		Key:   aws.String("routing.http.response.server.enabled"),
		Value: aws.String("false"),
	})

	_, err = elb.AddTags(ctx, &elbv2.AddTagsInput{
		ResourceArns: []string{lbArn, tgArn},
		Tags:         []elbtypes.Tag{{Key: aws.String("phase"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	tagOut, err := elb.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: []string{lbArn, tgArn}})
	require.NoError(t, err)
	require.Len(t, tagOut.TagDescriptions, 2)
	_, err = elb.RemoveTags(ctx, &elbv2.RemoveTagsInput{
		ResourceArns: []string{lbArn},
		TagKeys:      []string{"phase"},
	})
	require.NoError(t, err)

	limits, err := elb.DescribeAccountLimits(ctx, &elbv2.DescribeAccountLimitsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, limits.Limits)

	_, err = elb.DeregisterTargets(ctx, &elbv2.DeregisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{target, unreachableTarget},
	})
	require.NoError(t, err)
	_, err = elb.DeleteListener(ctx, &elbv2.DeleteListenerInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	_, err = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(tgArn)})
	require.NoError(t, err)
	_, err = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	require.NoError(t, err)

	_, _ = ec2c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: sg2.GroupId})
	_, _ = ec2c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: sgOut.GroupId})
	_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet2.Subnet.SubnetId})
	_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet1.Subnet.SubnetId})
	_, _ = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
}
