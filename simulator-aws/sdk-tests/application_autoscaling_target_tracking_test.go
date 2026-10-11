package aws_sdk_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppScaling_TargetTrackingScalesECSService proves a target-tracking
// policy actually adjusts capacity: with CPU pinned well above the target the
// service's DesiredCount grows toward MaxCapacity; with CPU dropped below the
// target it shrinks back toward MinCapacity. This is the real Application Auto
// Scaling engine (AnyScaleFrontendService) reading CloudWatch Metrics and
// driving ECS UpdateService — the runner-platform autoscaling flow end-to-end.
func TestAppScaling_TargetTrackingScalesECSService(t *testing.T) {
	t.Parallel()
	family := uniqueName("as-track-task")
	ecsC := ecsClient()
	cwC := cloudwatchClient()
	asC := appAutoScalingClient()

	cluster := uniqueName("as-track-cluster")
	svcName := uniqueName("as-track-svc")
	resourceID := "service/" + cluster + "/" + svcName
	const (
		ns   = aastypes.ServiceNamespaceEcs
		dim  = aastypes.ScalableDimensionECSServiceDesiredCount
		minC = int32(1)
		maxC = int32(10)
	)

	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)

	_, err = ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(svcName),
		TaskDefinition: aws.String(family),
		DesiredCount:   aws.Int32(1),
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, svcName)

	_, err = asC.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		MinCapacity:       aws.Int32(minC),
		MaxCapacity:       aws.Int32(maxC),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asC.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
			ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
		})
	})

	policy, err := asC.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
		PolicyName:        aws.String("cpu-50"),
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		PolicyType:        aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(50.0),
			PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeECSServiceAverageCPUUtilization,
			},
		},
	})
	require.NoError(t, err)

	// Application Auto Scaling manages two CloudWatch alarms for the policy:
	// AlarmHigh over three one-minute periods above the target, and AlarmLow
	// over fifteen below 90% of it.
	require.Len(t, policy.Alarms, 2)
	alarmNames := make([]string, 0, len(policy.Alarms))
	for _, alarm := range policy.Alarms {
		alarmNames = append(alarmNames, aws.ToString(alarm.AlarmName))
	}
	alarms, err := cwC.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarmNames})
	require.NoError(t, err)
	require.Len(t, alarms.MetricAlarms, 2)
	for _, alarm := range alarms.MetricAlarms {
		assert.Equal(t, int32(60), aws.ToInt32(alarm.Period))
		assert.Equal(t, []string{aws.ToString(policy.PolicyARN)}, alarm.AlarmActions)
		switch alarm.ComparisonOperator {
		case cwtypes.ComparisonOperatorGreaterThanThreshold:
			assert.Equal(t, int32(3), aws.ToInt32(alarm.EvaluationPeriods))
			assert.Equal(t, 50.0, aws.ToFloat64(alarm.Threshold))
		case cwtypes.ComparisonOperatorLessThanThreshold:
			assert.Equal(t, int32(15), aws.ToInt32(alarm.EvaluationPeriods))
			assert.Equal(t, 45.0, aws.ToFloat64(alarm.Threshold))
		default:
			t.Fatalf("unexpected alarm %s comparing %s", aws.ToString(alarm.AlarmName), alarm.ComparisonOperator)
		}
	}

	// putCPU records copies datapoints of value for each of the last minutes,
	// as the Amazon ECS agent reports one a minute.
	putCPU := func(value float64, minutes, copies int) {
		now := time.Now()
		var data []cwtypes.MetricDatum
		for minute := 0; minute < minutes; minute++ {
			for range copies {
				data = append(data, cwtypes.MetricDatum{
					MetricName: aws.String("CPUUtilization"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("ClusterName"), Value: aws.String(cluster)},
						{Name: aws.String("ServiceName"), Value: aws.String(svcName)},
					},
					Value:     aws.Float64(value),
					Timestamp: aws.Time(now.Add(-time.Duration(minute) * time.Minute)),
				})
			}
		}
		_, err := cwC.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String("AWS/ECS"), MetricData: data})
		require.NoError(t, err)
	}

	desiredCount := func() int32 {
		out, err := ecsC.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: []string{svcName},
		})
		require.NoError(t, err)
		require.Len(t, out.Services, 1)
		return out.Services[0].DesiredCount
	}

	// Scale-out: three minutes at 90% (target 50%) put AlarmHigh in ALARM,
	// and the policy sets ceil(1 * 90 / 50) = 2.
	putCPU(90.0, 3, 1)
	require.Eventually(t, func() bool {
		return desiredCount() > minC
	}, 30*time.Second, time.Second, "DesiredCount must increase once AlarmHigh is in ALARM")
	assert.LessOrEqual(t, desiredCount(), maxC)

	// Scale-in: fifteen minutes averaging well below 45% put AlarmLow in ALARM.
	putCPU(0.0, 15, 4)
	require.Eventually(t, func() bool {
		return desiredCount() <= minC
	}, 30*time.Second, time.Second, "DesiredCount must decrease toward MinCapacity once AlarmLow is in ALARM")
	assert.GreaterOrEqual(t, desiredCount(), minC)

	// Each capacity change surfaces as a ScalingActivity the alarm caused.
	actOut, err := asC.DescribeScalingActivities(ctx, &applicationautoscaling.DescribeScalingActivitiesInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
	})
	require.NoError(t, err)
	require.Len(t, actOut.ScalingActivities, 2)
	for _, activity := range actOut.ScalingActivities {
		assert.Contains(t, aws.ToString(activity.Cause), "TargetTracking-"+resourceID)
	}

	_, err = asC.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
		PolicyName: aws.String("cpu-50"), ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
	})
	require.NoError(t, err)
	alarms, err = cwC.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarmNames})
	require.NoError(t, err)
	assert.Empty(t, alarms.MetricAlarms, "deleting the policy deletes the alarms Application Auto Scaling created")
}

// TestAppScaling_ALBRequestCountPerTargetTracksTheLoadBalancer puts an
// ALBRequestCountPerTarget policy on an Amazon ECS service: Application Auto
// Scaling's alarms watch the Sum of RequestCountPerTarget for the target group
// the ResourceLabel names, and the Application Load Balancer publishes that
// metric for the requests it routes there.
func TestAppScaling_ALBRequestCountPerTargetTracksTheLoadBalancer(t *testing.T) {
	t.Parallel()
	ec2c := ec2Client()
	elb := elbv2Client()
	ecsC := ecsClient()
	cwC := cloudwatchClient()
	asC := appAutoScalingClient()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer targetServer.Close()
	targetURL, err := url.Parse(targetServer.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.89.0.0/16")})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId
	var subnets []string
	for i, zone := range []string{"us-east-1a", "us-east-1b"} {
		subnet, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId:            aws.String(vpcID),
			CidrBlock:        aws.String("10.89." + strconv.Itoa(i+1) + ".0/24"),
			AvailabilityZone: aws.String(zone),
		})
		require.NoError(t, err)
		subnets = append(subnets, *subnet.Subnet.SubnetId)
	}
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("sdk-rcpt-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: subnets,
	})
	require.NoError(t, err)
	lbArn := *lbOut.LoadBalancers[0].LoadBalancerArn
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	})
	tgOut, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:                       aws.String("sdk-rcpt-tg"),
		Protocol:                   elbtypes.ProtocolEnumHttp,
		Port:                       aws.Int32(80),
		VpcId:                      aws.String(vpcID),
		TargetType:                 elbtypes.TargetTypeEnumIp,
		HealthCheckIntervalSeconds: aws.Int32(5),
		HealthCheckTimeoutSeconds:  aws.Int32(2),
	})
	require.NoError(t, err)
	tgArn := *tgOut.TargetGroups[0].TargetGroupArn
	t.Cleanup(func() {
		_, _ = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(tgArn)})
	})
	target := elbtypes.TargetDescription{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))}
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{target},
	})
	require.NoError(t, err)
	_, err = elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttp,
		Port:            aws.Int32(80),
		DefaultActions: []elbtypes.Action{{
			Type:           elbtypes.ActionTypeEnumForward,
			TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	waitForELBv2TargetHealth(t, tgArn, target, elbtypes.TargetHealthStateEnumHealthy)

	family := uniqueName("as-rcpt-task")
	cluster := uniqueName("as-rcpt-cluster")
	svcName := uniqueName("as-rcpt-svc")
	resourceID := "service/" + cluster + "/" + svcName
	const (
		ns  = aastypes.ServiceNamespaceEcs
		dim = aastypes.ScalableDimensionECSServiceDesiredCount
	)
	_, err = ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	_, err = ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(svcName),
		TaskDefinition: aws.String(family),
		DesiredCount:   aws.Int32(1),
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, svcName)
	_, err = asC.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		MinCapacity:       aws.Int32(1),
		MaxCapacity:       aws.Int32(10),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asC.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
			ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
		})
	})

	loadBalancerPortion := lbArn[strings.Index(lbArn, ":loadbalancer/")+len(":loadbalancer/"):]
	targetGroupPortion := tgArn[strings.LastIndex(tgArn, ":")+1:]
	policy, err := asC.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
		PolicyName:        aws.String("requests-100"),
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		PolicyType:        aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(100),
			PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeALBRequestCountPerTarget,
				ResourceLabel:        aws.String(loadBalancerPortion + "/" + targetGroupPortion),
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, policy.Alarms, 2)
	alarmNames := make([]string, 0, len(policy.Alarms))
	for _, alarm := range policy.Alarms {
		alarmNames = append(alarmNames, aws.ToString(alarm.AlarmName))
	}
	t.Cleanup(func() {
		_, _ = asC.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
			PolicyName: aws.String("requests-100"), ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
		})
	})
	alarms, err := cwC.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarmNames})
	require.NoError(t, err)
	require.Len(t, alarms.MetricAlarms, 2)
	targetGroupDimension := []cwtypes.Dimension{{Name: aws.String("TargetGroup"), Value: aws.String(targetGroupPortion)}}
	for _, alarm := range alarms.MetricAlarms {
		assert.Equal(t, "AWS/ApplicationELB", aws.ToString(alarm.Namespace))
		assert.Equal(t, "RequestCountPerTarget", aws.ToString(alarm.MetricName))
		assert.Equal(t, cwtypes.StatisticSum, alarm.Statistic)
		assert.Equal(t, targetGroupDimension, alarm.Dimensions)
	}

	lbDNSName := *lbOut.LoadBalancers[0].DNSName
	started := time.Now().Add(-time.Minute)
	const requests = 5
	for range requests {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
		require.NoError(t, err)
		req.Host = lbDNSName
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	// The load balancer reports what it routed to CloudWatch after the
	// requests, so read the metric until every request appears in it.
	routed := func() float64 {
		stats, err := cwC.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
			Namespace:  aws.String("AWS/ApplicationELB"),
			MetricName: aws.String("RequestCountPerTarget"),
			Dimensions: targetGroupDimension,
			StartTime:  aws.Time(started),
			EndTime:    aws.Time(time.Now().Add(time.Minute)),
			Period:     aws.Int32(300),
			Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
		})
		require.NoError(t, err)
		var sum float64
		for _, point := range stats.Datapoints {
			sum += aws.ToFloat64(point.Sum)
		}
		return sum
	}
	require.Eventually(t, func() bool { return routed() >= requests }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, float64(requests), routed(), "one target in service receives every request")
}
