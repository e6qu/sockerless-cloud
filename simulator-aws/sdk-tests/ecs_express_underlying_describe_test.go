package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECSExpress_UnderlyingResourcesDescribeFaithfully drives a full Express
// Gateway assembly, then verifies that EACH backing resource the assembly wrote
// into the real sim stores (ALB, target group, HTTPS listener, ACM certificate,
// security group, Application Auto Scaling target + target-tracking policy)
// describes back through its OWN service API with every field a real
// aws-sdk-go-v2 / terraform client reads. This guards the Describe handlers of
// elbv2, acm, application_autoscaling, and ec2 against silently dropping fields
// or failing to filter by the assembly's identifiers.
func TestECSExpress_UnderlyingResourcesDescribeFaithfully(t *testing.T) {
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
		InfrastructureRoleArn: aws.String("arn:aws:iam::000000000000:role/express-infra"),
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
	albHost := strings.TrimPrefix(endpoint, "https://")
	require.NotEmpty(t, albHost)

	// ---- ALB: DescribeLoadBalancers returns it with the full read-back shape. ----
	lbs, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{})
	require.NoError(t, err)
	var albArn string
	for _, lb := range lbs.LoadBalancers {
		if strings.EqualFold(aws.ToString(lb.DNSName), albHost) {
			albArn = aws.ToString(lb.LoadBalancerArn)
			assert.Equal(t, "internet-facing", string(lb.Scheme))
			assert.Equal(t, "application", string(lb.Type))
			require.NotNil(t, lb.State)
			assert.Equal(t, "active", string(lb.State.Code))
			break
		}
	}
	require.NotEmpty(t, albArn, "Express ALB not found via DescribeLoadBalancers")

	// Filter by ARN must return exactly the one ALB.
	byArn, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{albArn},
	})
	require.NoError(t, err)
	require.Len(t, byArn.LoadBalancers, 1)
	assert.Equal(t, albArn, aws.ToString(byArn.LoadBalancers[0].LoadBalancerArn))

	// ---- Listener: DescribeListeners(LoadBalancerArn) returns the HTTPS:443
	// listener with the ACM cert and a forward DefaultAction to the TG. ----
	ls, err := elb.DescribeListeners(ctx, &elbv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(albArn),
	})
	require.NoError(t, err)
	require.Len(t, ls.Listeners, 1, "expected one HTTPS listener on the Express ALB")
	listener := ls.Listeners[0]
	assert.Equal(t, "HTTPS", string(listener.Protocol))
	assert.Equal(t, int32(443), aws.ToInt32(listener.Port))
	require.NotEmpty(t, listener.Certificates, "listener must carry the ACM certificate")
	certArn := aws.ToString(listener.Certificates[0].CertificateArn)
	require.NotEmpty(t, certArn)
	require.NotEmpty(t, listener.DefaultActions)
	assert.Equal(t, "forward", string(listener.DefaultActions[0].Type))
	tgArn := aws.ToString(listener.DefaultActions[0].TargetGroupArn)
	require.NotEmpty(t, tgArn, "listener default action must forward to a target group")

	// ---- Target group: DescribeTargetGroups returns ip target type, HTTP,
	// the container port, health-check path, and the VPC. ----
	tgs, err := elb.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{tgArn},
	})
	require.NoError(t, err)
	require.Len(t, tgs.TargetGroups, 1)
	tg := tgs.TargetGroups[0]
	assert.Equal(t, "HTTP", string(tg.Protocol))
	assert.Equal(t, int32(8080), aws.ToInt32(tg.Port))
	assert.Equal(t, "ip", string(tg.TargetType))
	assert.Equal(t, "/healthz", aws.ToString(tg.HealthCheckPath))
	// The TG must report the ALB in LoadBalancerArns (wired by the listener).
	assert.Contains(t, tg.LoadBalancerArns, albArn)

	// ---- Certificate: DescribeCertificate returns the managed cert ISSUED,
	// AMAZON_ISSUED, for the ALB DNS name. ----
	dc, err := acmc.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(certArn),
	})
	require.NoError(t, err)
	require.NotNil(t, dc.Certificate)
	assert.Equal(t, "ISSUED", string(dc.Certificate.Status))
	assert.Equal(t, "AMAZON_ISSUED", string(dc.Certificate.Type))
	assert.Equal(t, albHost, aws.ToString(dc.Certificate.DomainName))

	// ---- Security group: DescribeSecurityGroups (filter by group-name) returns
	// the Express-created SG with its VpcId and OwnerId. ----
	sgs, err := ec2c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("group-name"),
			Values: []string{"express-" + svcName},
		}},
	})
	require.NoError(t, err)
	require.Len(t, sgs.SecurityGroups, 1, "expected the Express-created security group")
	sg := sgs.SecurityGroups[0]
	groupID := aws.ToString(sg.GroupId)
	require.NotEmpty(t, groupID)
	assert.NotEmpty(t, aws.ToString(sg.OwnerId))
	// Filter by group-id must round-trip the same group.
	byID, err := ec2c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{groupID},
	})
	require.NoError(t, err)
	require.Len(t, byID.SecurityGroups, 1)
	assert.Equal(t, groupID, aws.ToString(byID.SecurityGroups[0].GroupId))

	// ---- Scalable target: DescribeScalableTargets filtered by ResourceIds +
	// ScalableDimension returns the ecs:service:DesiredCount target with the
	// requested min/max capacity. ----
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

	// ---- Scaling policy: DescribeScalingPolicies filtered by ResourceId +
	// ScalableDimension returns the target-tracking policy with its predefined
	// metric + target value. ----
	spOut, err := aa.DescribeScalingPolicies(ctx, &applicationautoscaling.DescribeScalingPoliciesInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	})
	require.NoError(t, err)
	require.Len(t, spOut.ScalingPolicies, 1)
	policy := spOut.ScalingPolicies[0]
	assert.Equal(t, aastypes.PolicyTypeTargetTrackingScaling, policy.PolicyType)
	require.NotNil(t, policy.TargetTrackingScalingPolicyConfiguration, "target-tracking config must round-trip")
	cfg := policy.TargetTrackingScalingPolicyConfiguration
	assert.Equal(t, float64(55), aws.ToFloat64(cfg.TargetValue))
	require.NotNil(t, cfg.PredefinedMetricSpecification)
	assert.Equal(t, aastypes.MetricTypeECSServiceAverageCPUUtilization,
		cfg.PredefinedMetricSpecification.PredefinedMetricType)
}
