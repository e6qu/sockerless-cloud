package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECSExpress_UnderlyingResourcesDescribeFaithfully follows each resource
// DescribeServiceRevisions reports for an Express service (load balancer, HTTPS
// listener, listener rule, target group, certificate, security groups, scalable
// target, scaling policy) into its own service, where it describes back with
// every field aws-sdk-go-v2 and terraform clients read.
func TestECSExpress_UnderlyingResourcesDescribeFaithfully(t *testing.T) {
	t.Parallel()
	ecsc := ecsClient()
	elb := elbv2Client()
	aa := appAutoScalingClient()
	acmc := acmClient()
	ec2c := ec2Client()

	cluster := expressCreateCluster(t, ecsc, "express-underlying")
	const svcName = "api"

	out, err := ecsc.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
		Cluster:               aws.String(cluster),
		ServiceName:           aws.String(svcName),
		InfrastructureRoleArn: aws.String(createECSExpressInfrastructureRole(t, "express-underlying")),
		HealthCheckPath:       aws.String("/healthz"),
		PrimaryContainer: &ecstypes.ExpressGatewayContainer{
			Image:         aws.String(containerCommandImage),
			ContainerPort: aws.Int32(8080),
			Command:       []string{"http", "8080", "express-ok"},
		},
		ScalingTarget: &ecstypes.ExpressGatewayScalingTarget{
			MinTaskCount:           aws.Int32(2),
			MaxTaskCount:           aws.Int32(7),
			AutoScalingMetric:      ecstypes.ExpressGatewayServiceScalingMetricAverageCPUUtilization,
			AutoScalingTargetValue: aws.Int32(55),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ecsc.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{ServiceArn: out.Service.ServiceArn})
	})
	require.NotEmpty(t, out.Service.ActiveConfigurations)
	endpoint := aws.ToString(out.Service.ActiveConfigurations[0].IngressPaths[0].Endpoint)
	hostname := strings.TrimPrefix(endpoint, "https://")
	require.NotEmpty(t, hostname)

	managed := expressManagedResources(t, ecsc, out.Service)
	require.Len(t, managed.IngressPaths, 1)
	path := managed.IngressPaths[0]
	assert.Equal(t, ecstypes.AccessTypePublic, path.AccessType)
	assert.Equal(t, endpoint, aws.ToString(path.Endpoint))
	for name, resource := range map[string]ecstypes.ManagedResourceStatus{
		"loadBalancer": path.LoadBalancer.Status,
		"certificate":  path.Certificate.Status,
		"listener":     path.Listener.Status,
		"rule":         path.Rule.Status,
	} {
		assert.Equal(t, ecstypes.ManagedResourceStatusActive, resource, name)
	}
	require.Len(t, path.TargetGroups, 1)
	assert.Equal(t, ecstypes.ManagedResourceStatusActive, path.TargetGroups[0].Status)
	assert.Equal(t, "/healthz", aws.ToString(path.TargetGroups[0].HealthCheckPath))
	assert.Equal(t, int32(8080), path.TargetGroups[0].Port)

	// DescribeLoadBalancers returns the load balancer in the scheme, subnets and
	// security groups the managed resource names.
	albArn := aws.ToString(path.LoadBalancer.Arn)
	byArn, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{albArn}})
	require.NoError(t, err)
	require.Len(t, byArn.LoadBalancers, 1)
	lb := byArn.LoadBalancers[0]
	assert.Equal(t, "internet-facing", string(lb.Scheme))
	assert.Equal(t, aws.ToString(path.LoadBalancer.Scheme), string(lb.Scheme))
	assert.Equal(t, "application", string(lb.Type))
	require.NotNil(t, lb.State)
	assert.Equal(t, "active", string(lb.State.Code))
	var subnets []string
	for _, zone := range lb.AvailabilityZones {
		subnets = append(subnets, aws.ToString(zone.SubnetId))
	}
	assert.ElementsMatch(t, path.LoadBalancer.SubnetIds, subnets)
	assert.ElementsMatch(t, path.LoadBalancer.SecurityGroupIds, lb.SecurityGroups)

	// DescribeListeners returns the HTTPS:443 listener with the service's
	// certificate.
	ls, err := elb.DescribeListeners(ctx, &elbv2.DescribeListenersInput{LoadBalancerArn: aws.String(albArn)})
	require.NoError(t, err)
	require.Len(t, ls.Listeners, 1, "expected one HTTPS listener on the Express ALB")
	listener := ls.Listeners[0]
	assert.Equal(t, aws.ToString(path.Listener.Arn), aws.ToString(listener.ListenerArn))
	assert.Equal(t, "HTTPS", string(listener.Protocol))
	assert.Equal(t, int32(443), aws.ToInt32(listener.Port))
	certArn := aws.ToString(path.Certificate.Arn)
	require.NotEmpty(t, certArn)
	var onListener bool
	for _, certificate := range listener.Certificates {
		onListener = onListener || aws.ToString(certificate.CertificateArn) == certArn
	}
	assert.True(t, onListener, "the listener carries the service's certificate")

	// DescribeRules holds the service's host-header rule forwarding to its target
	// group.
	rules, err := elb.DescribeRules(ctx, &elbv2.DescribeRulesInput{ListenerArn: listener.ListenerArn})
	require.NoError(t, err)
	tgArn := aws.ToString(path.TargetGroups[0].Arn)
	var routed bool
	for _, rule := range rules.Rules {
		if aws.ToString(rule.RuleArn) != aws.ToString(path.Rule.Arn) {
			continue
		}
		routed = true
		require.Len(t, rule.Conditions, 1)
		assert.Equal(t, "host-header", aws.ToString(rule.Conditions[0].Field))
		require.NotNil(t, rule.Conditions[0].HostHeaderConfig)
		assert.Equal(t, []string{hostname}, rule.Conditions[0].HostHeaderConfig.Values)
		require.Len(t, rule.Actions, 1)
		assert.Equal(t, "forward", string(rule.Actions[0].Type))
		assert.Equal(t, tgArn, aws.ToString(rule.Actions[0].TargetGroupArn))
	}
	assert.True(t, routed, "the listener holds the service's rule")

	// DescribeTargetGroups returns the ip target type, HTTP, the container port,
	// the health check path and the load balancer.
	tgs, err := elb.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}})
	require.NoError(t, err)
	require.Len(t, tgs.TargetGroups, 1)
	tg := tgs.TargetGroups[0]
	assert.Equal(t, "HTTP", string(tg.Protocol))
	assert.Equal(t, int32(8080), aws.ToInt32(tg.Port))
	assert.Equal(t, "ip", string(tg.TargetType))
	assert.Equal(t, "/healthz", aws.ToString(tg.HealthCheckPath))
	assert.Contains(t, tg.LoadBalancerArns, albArn)

	// DescribeCertificate returns the managed certificate ISSUED and AMAZON_ISSUED
	// for the service's hostname.
	dc, err := acmc.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
	require.NoError(t, err)
	require.NotNil(t, dc.Certificate)
	assert.Equal(t, "ISSUED", string(dc.Certificate.Status))
	assert.Equal(t, "AMAZON_ISSUED", string(dc.Certificate.Type))
	assert.Equal(t, hostname, aws.ToString(dc.Certificate.DomainName))
	assert.Equal(t, hostname, aws.ToString(path.Certificate.DomainName))

	// DescribeSecurityGroups describes the load balancer's group and the tasks',
	// each with VpcId and OwnerId.
	require.Len(t, path.LoadBalancerSecurityGroups, 1)
	require.Len(t, managed.ServiceSecurityGroups, 1)
	groupIDs := map[string]string{}
	for name, group := range map[string]ecstypes.ManagedSecurityGroup{
		"load balancer": path.LoadBalancerSecurityGroups[0],
		"service":       managed.ServiceSecurityGroups[0],
	} {
		assert.Equal(t, ecstypes.ManagedResourceStatusActive, group.Status, name)
		arn := aws.ToString(group.Arn)
		groupID := arn[strings.LastIndex(arn, "/")+1:]
		require.True(t, strings.HasPrefix(groupID, "sg-"), "%s security group ARN %q", name, arn)
		groupIDs[name] = groupID
		described, err := ec2c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{groupID}})
		require.NoError(t, err, name)
		require.Len(t, described.SecurityGroups, 1, name)
		assert.Equal(t, groupID, aws.ToString(described.SecurityGroups[0].GroupId))
		assert.Equal(t, aws.ToString(lb.VpcId), aws.ToString(described.SecurityGroups[0].VpcId))
		assert.NotEmpty(t, aws.ToString(described.SecurityGroups[0].OwnerId))
	}
	assert.Contains(t, lb.SecurityGroups, groupIDs["load balancer"])
	// The tasks run in the group Amazon ECS created for them.
	backing, err := ecsc.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: []string{svcName}})
	require.NoError(t, err)
	require.Len(t, backing.Services, 1)
	require.NotNil(t, backing.Services[0].NetworkConfiguration)
	require.NotNil(t, backing.Services[0].NetworkConfiguration.AwsvpcConfiguration)
	assert.Equal(t, []string{groupIDs["service"]}, backing.Services[0].NetworkConfiguration.AwsvpcConfiguration.SecurityGroups)

	// DescribeScalableTargets returns the ecs:service:DesiredCount target with the
	// requested capacity bounds, which the managed resource repeats.
	require.NotNil(t, managed.AutoScaling)
	require.NotNil(t, managed.AutoScaling.ScalableTarget)
	assert.Equal(t, ecstypes.ManagedResourceStatusActive, managed.AutoScaling.ScalableTarget.Status)
	assert.Equal(t, int32(2), managed.AutoScaling.ScalableTarget.MinCapacity)
	assert.Equal(t, int32(7), managed.AutoScaling.ScalableTarget.MaxCapacity)
	resourceID := "service/" + cluster + "/" + svcName
	stOut, err := aa.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceIds:       []string{resourceID},
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	})
	require.NoError(t, err)
	require.Len(t, stOut.ScalableTargets, 1)
	st := stOut.ScalableTargets[0]
	assert.Equal(t, resourceID, aws.ToString(st.ResourceId))
	assert.Equal(t, aastypes.ScalableDimensionECSServiceDesiredCount, st.ScalableDimension)
	assert.Equal(t, int32(2), aws.ToInt32(st.MinCapacity))
	assert.Equal(t, int32(7), aws.ToInt32(st.MaxCapacity))
	assert.Equal(t, aws.ToString(managed.AutoScaling.ScalableTarget.Arn), aws.ToString(st.ScalableTargetARN))

	// DescribeScalingPolicies returns the target-tracking policy with its
	// predefined metric and target value, and the alarms the managed resources
	// list.
	require.Len(t, managed.AutoScaling.ApplicationAutoScalingPolicies, 1)
	managedPolicy := managed.AutoScaling.ApplicationAutoScalingPolicies[0]
	assert.Equal(t, ecstypes.ManagedResourceStatusActive, managedPolicy.Status)
	assert.Equal(t, "TargetTrackingScaling", aws.ToString(managedPolicy.PolicyType))
	assert.Equal(t, "ECSServiceAverageCPUUtilization", aws.ToString(managedPolicy.Metric))
	assert.Equal(t, float64(55), managedPolicy.TargetValue)
	spOut, err := aa.DescribeScalingPolicies(ctx, &applicationautoscaling.DescribeScalingPoliciesInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	})
	require.NoError(t, err)
	require.Len(t, spOut.ScalingPolicies, 1)
	policy := spOut.ScalingPolicies[0]
	assert.Equal(t, aws.ToString(managedPolicy.Arn), aws.ToString(policy.PolicyARN))
	assert.Equal(t, aastypes.PolicyTypeTargetTrackingScaling, policy.PolicyType)
	require.NotNil(t, policy.TargetTrackingScalingPolicyConfiguration, "target-tracking config must round-trip")
	cfg := policy.TargetTrackingScalingPolicyConfiguration
	assert.Equal(t, float64(55), aws.ToFloat64(cfg.TargetValue))
	require.NotNil(t, cfg.PredefinedMetricSpecification)
	assert.Equal(t, aastypes.MetricTypeECSServiceAverageCPUUtilization,
		cfg.PredefinedMetricSpecification.PredefinedMetricType)
	var alarmArns []string
	for _, alarm := range policy.Alarms {
		alarmArns = append(alarmArns, aws.ToString(alarm.AlarmARN))
	}
	var managedAlarms []string
	for _, alarm := range managed.MetricAlarms {
		assert.Equal(t, ecstypes.ManagedResourceStatusActive, alarm.Status)
		managedAlarms = append(managedAlarms, aws.ToString(alarm.Arn))
	}
	assert.ElementsMatch(t, alarmArns, managedAlarms)
}
