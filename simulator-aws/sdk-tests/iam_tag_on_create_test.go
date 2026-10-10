package aws_sdk_test

import (
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AWS authorizes the service's tagging action as well as the create when a
// create carries tags, and sets <service>:CreateAction on that check to the
// create's name. These tests hold a principal allowed to create but not to tag
// to untagged creates, and a tagging grant scoped on CreateAction to the
// tagging of exactly the create it names.

func tagOnCreateConfig(t *testing.T, user, policy string) aws.Config {
	t.Helper()
	akid, secret := restrictedCredential(t, user, policy)
	return aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")}
}

func TestTagOnCreate_ECSCreateWithTagsNeedsTagResource(t *testing.T) {
	cfg := tagOnCreateConfig(t, "ecs-create-no-tag",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:CreateCluster","Resource":"*"}]}`)
	restricted := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(uniqueName("toc-tagged")),
		Tags:        []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}},
	})
	require.Error(t, err, "a tagged create is also an ecs:TagResource, which the grant does not allow")
	assert.Contains(t, err.Error(), "ecs:TagResource")

	name := uniqueName("toc-untagged")
	_, err = restricted.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name)})
	require.NoError(t, err, "an untagged create tags nothing")
	t.Cleanup(func() { _, _ = ecsClient().DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)}) })
}

func TestTagOnCreate_ECSCreateActionScopesTheTaggingGrant(t *testing.T) {
	cfg := tagOnCreateConfig(t, "ecs-tag-on-create-only",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":["ecs:CreateCluster","ecs:RegisterTaskDefinition"],"Resource":"*"},
		  {"Effect":"Allow","Action":"ecs:TagResource","Resource":"*",
		   "Condition":{"StringEquals":{"ecs:CreateAction":"CreateCluster"}}}]}`)
	restricted := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	tags := []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}}

	name := uniqueName("toc-scoped")
	created, err := restricted.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name), Tags: tags})
	require.NoError(t, err, "the grant allows the tagging CreateCluster carries")
	t.Cleanup(func() { _, _ = ecsClient().DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)}) })

	listed, err := ecsClient().ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: created.Cluster.ClusterArn})
	require.NoError(t, err)
	require.Len(t, listed.Tags, 1)
	assert.Equal(t, "blue", aws.ToString(listed.Tags[0].Value))

	_, err = restricted.TagResource(ctx, &ecs.TagResourceInput{ResourceArn: created.Cluster.ClusterArn,
		Tags: []ecstypes.Tag{{Key: aws.String("owner"), Value: aws.String("ops")}}})
	require.Error(t, err, "retagging an existing cluster names no create")
	assert.Contains(t, err.Error(), "not authorized")

	_, err = restricted.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(uniqueName("toc-family")),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String("public.ecr.aws/docker/library/busybox:latest"), Memory: aws.Int32(128)}},
		Tags: tags,
	})
	require.Error(t, err, "the tagging of another create is not the one the grant names")
	assert.Contains(t, err.Error(), "ecs:TagResource")
}

func TestTagOnCreate_ELBCreateActionScopesTheTaggingGrant(t *testing.T) {
	vpc, err := ec2Client().CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ec2Client().DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) })

	denied := elbv2.NewFromConfig(tagOnCreateConfig(t, "elb-create-no-tag",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"elasticloadbalancing:CreateTargetGroup","Resource":"*"}]}`),
		func(o *elbv2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	scoped := elbv2.NewFromConfig(tagOnCreateConfig(t, "elb-tag-on-create-only",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"elasticloadbalancing:CreateTargetGroup","Resource":"*"},
		  {"Effect":"Allow","Action":"elasticloadbalancing:AddTags","Resource":"*",
		   "Condition":{"StringEquals":{"elasticloadbalancing:CreateAction":"CreateTargetGroup"}}}]}`),
		func(o *elbv2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	create := func(client *elbv2.Client, name string, tagged bool) (*elbv2.CreateTargetGroupOutput, error) {
		input := &elbv2.CreateTargetGroupInput{
			Name: aws.String(name), Protocol: elbv2types.ProtocolEnumHttp, Port: aws.Int32(80),
			VpcId: vpc.Vpc.VpcId, TargetType: elbv2types.TargetTypeEnumIp,
		}
		if tagged {
			input.Tags = []elbv2types.Tag{{Key: aws.String("team"), Value: aws.String("blue")}}
		}
		out, err := client.CreateTargetGroup(ctx, input)
		if err == nil {
			t.Cleanup(func() {
				_, _ = elbv2Client().DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: out.TargetGroups[0].TargetGroupArn})
			})
		}
		return out, err
	}

	_, err = create(denied, uniqueName("toc-tg-tagged"), true)
	require.Error(t, err, "a tagged create is also an elasticloadbalancing:AddTags, which the grant does not allow")
	assert.Contains(t, err.Error(), "elasticloadbalancing:AddTags")
	_, err = create(denied, uniqueName("toc-tg-untagged"), false)
	require.NoError(t, err, "an untagged create tags nothing")

	created, err := create(scoped, uniqueName("toc-tg-scoped"), true)
	require.NoError(t, err, "the grant allows the tagging CreateTargetGroup carries")
	_, err = scoped.AddTags(ctx, &elbv2.AddTagsInput{ResourceArns: []string{aws.ToString(created.TargetGroups[0].TargetGroupArn)},
		Tags: []elbv2types.Tag{{Key: aws.String("owner"), Value: aws.String("ops")}}})
	require.Error(t, err, "retagging an existing target group names no create")
	assert.Contains(t, err.Error(), "not authorized")
}

func TestTagOnCreate_EC2CreateActionScopesTheTaggingGrant(t *testing.T) {
	scoped := ec2.NewFromConfig(tagOnCreateConfig(t, "ec2-tag-on-create-only",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":["ec2:CreateVolume","ec2:CreateVpc"],"Resource":"*"},
		  {"Effect":"Allow","Action":"ec2:CreateTags","Resource":"arn:aws:ec2:*:*:volume/*",
		   "Condition":{"StringEquals":{"ec2:CreateAction":"CreateVolume"}}}]}`),
		func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	tags := []ec2types.Tag{{Key: aws.String("team"), Value: aws.String("blue")}}

	volume, err := scoped.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeVolume, Tags: tags}},
	})
	require.NoError(t, err, "the grant allows the tagging CreateVolume carries")
	t.Cleanup(func() { _, _ = ec2Client().DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: volume.VolumeId}) })
	assert.Equal(t, "blue", aws.ToString(volume.Tags[0].Value))

	_, err = scoped.CreateTags(ctx, &ec2.CreateTagsInput{Resources: []string{aws.ToString(volume.VolumeId)}, Tags: tags})
	require.Error(t, err, "retagging an existing volume names no create")
	assert.Contains(t, err.Error(), "not authorized")

	_, err = scoped.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock:         aws.String("10.92.0.0/16"),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeVpc, Tags: tags}},
	})
	require.Error(t, err, "the tagging of another create is not the one the grant names")
	assert.Contains(t, err.Error(), "not authorized")

	vpc, err := scoped.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.93.0.0/16")})
	require.NoError(t, err, "an untagged create tags nothing")
	_, _ = ec2Client().DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId})
}

// The Go SDK speaks RPC v2 CBOR to Amazon CloudWatch, so the tags it carries
// have to be read from that encoding.
func TestTagOnCreate_CloudWatchAlarmWithTagsNeedsTagResource(t *testing.T) {
	restricted := cloudwatch.NewFromConfig(tagOnCreateConfig(t, "cw-alarm-no-tag",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:PutMetricAlarm","Resource":"*"}]}`),
		func(o *cloudwatch.Options) { o.BaseEndpoint = aws.String(baseURL) })
	put := func(name string, tags []cwtypes.Tag) error {
		_, err := restricted.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName: aws.String(name), Namespace: aws.String("TagOnCreate"), MetricName: aws.String("Load"),
			Statistic: cwtypes.StatisticAverage, Period: aws.Int32(60), EvaluationPeriods: aws.Int32(1),
			Threshold: aws.Float64(1), ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			Tags: tags,
		})
		if err == nil {
			t.Cleanup(func() {
				_, _ = cloudwatchClient().DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}})
			})
		}
		return err
	}

	err := put(uniqueName("toc-alarm-tagged"), []cwtypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}})
	require.Error(t, err, "a tagged alarm is also a cloudwatch:TagResource, which the grant does not allow")
	assert.Contains(t, err.Error(), "cloudwatch:TagResource")
	require.NoError(t, put(uniqueName("toc-alarm-untagged"), nil), "an untagged alarm tags nothing")
}

func TestTagOnCreate_LambdaFunctionWithTagsNeedsTagResource(t *testing.T) {
	restricted := lambda.NewFromConfig(tagOnCreateConfig(t, "lambda-create-no-tag",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["lambda:CreateFunction","iam:PassRole"],"Resource":"*"}]}`),
		func(o *lambda.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(uniqueName("toc-fn")),
		Role:         aws.String("arn:aws:iam::123456789012:role/toc-fn-role"),
		Handler:      aws.String("index.handler"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
		Tags:         map[string]string{"team": "blue"},
	})
	require.Error(t, err, "a tagged function is also a lambda:TagResource, which the grant does not allow")
	assert.Contains(t, err.Error(), "lambda:TagResource")
}

// Session tags on an assumption are an sts:TagSession, which the role's trust
// policy has to allow as well as sts:AssumeRole.
func TestTagOnCreate_AssumeRoleWithSessionTagsNeedsTagSession(t *testing.T) {
	admin := iamClient()
	cfg := tagOnCreateConfig(t, "sts-assume-no-tag",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"*"}]}`)
	caller, err := sts.NewFromConfig(cfg, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)

	role := func(actions string) string {
		name := uniqueName("toc-role")
		created, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name),
			AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":{"AWS":"` + aws.ToString(caller.Arn) + `"},"Action":` + actions + `}]}`)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
		return aws.ToString(created.Role.Arn)
	}
	assume := func(roleARN string, tags []ststypes.Tag) error {
		_, err := sts.NewFromConfig(cfg, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) }).
			AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(roleARN), RoleSessionName: aws.String("toc"), Tags: tags})
		return err
	}
	tags := []ststypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}}

	assumeOnly := role(`"sts:AssumeRole"`)
	err = assume(assumeOnly, tags)
	require.Error(t, err, "the trust policy does not allow tagging the session")
	assert.Contains(t, err.Error(), "sts:TagSession")
	require.NoError(t, assume(assumeOnly, nil), "an untagged session needs no sts:TagSession")

	require.NoError(t, assume(role(`["sts:AssumeRole","sts:TagSession"]`), tags),
		"the trust policy allows tagging the session")
}

// A create that mints a resource under a parent its own action names
// authorizes its tagging against the type it mints: an Elastic Load Balancing
// listener under its load balancer, a rule under its listener. A tagging grant
// scoped to the created type allows the tagged create, and one scoped to the
// parent does not.
func TestTagOnCreate_ELBListenerAndRuleTaggingIsScopedToTheCreatedType(t *testing.T) {
	ec2c, elb := ec2Client(), elbv2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.94.0.0/16")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) })
	var subnets []string
	for i, zone := range []string{"us-east-1a", "us-east-1b"} {
		subnet, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId,
			CidrBlock: aws.String(fmt.Sprintf("10.94.%d.0/24", i+1)), AvailabilityZone: aws.String(zone)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet.Subnet.SubnetId}) })
		subnets = append(subnets, aws.ToString(subnet.Subnet.SubnetId))
	}
	lb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{Name: aws.String(uniqueName("toc-lb")),
		Type: elbv2types.LoadBalancerTypeEnumApplication, Subnets: subnets})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: lb.LoadBalancers[0].LoadBalancerArn})
	})
	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{Name: aws.String(uniqueName("toc-ltg")),
		Protocol: elbv2types.ProtocolEnumHttp, Port: aws.Int32(80), VpcId: vpc.Vpc.VpcId, TargetType: elbv2types.TargetTypeEnumIp})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: tg.TargetGroups[0].TargetGroupArn})
	})
	forward := []elbv2types.Action{{Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: tg.TargetGroups[0].TargetGroupArn}}
	tags := []elbv2types.Tag{{Key: aws.String("team"), Value: aws.String("blue")}}

	client := func(user, tagging string) *elbv2.Client {
		return elbv2.NewFromConfig(tagOnCreateConfig(t, user, `{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":["elasticloadbalancing:CreateListener","elasticloadbalancing:CreateRule"],"Resource":"*"},
		  {"Effect":"Allow","Action":"elasticloadbalancing:AddTags","Resource":"`+tagging+`"}]}`),
			func(o *elbv2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	}
	loadBalancers := client("elb-tag-load-balancers", "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/*")
	listeners := client("elb-tag-listeners", "arn:aws:elasticloadbalancing:*:*:listener/app/*")
	rules := client("elb-tag-rules", "arn:aws:elasticloadbalancing:*:*:listener-rule/app/*")

	_, err = loadBalancers.CreateListener(ctx, &elbv2.CreateListenerInput{LoadBalancerArn: lb.LoadBalancers[0].LoadBalancerArn,
		Protocol: elbv2types.ProtocolEnumHttp, Port: aws.Int32(8081), DefaultActions: forward, Tags: tags})
	require.Error(t, err, "the listener is not the load balancer the grant names")
	assert.Contains(t, err.Error(), "elasticloadbalancing:AddTags")

	listener, err := listeners.CreateListener(ctx, &elbv2.CreateListenerInput{LoadBalancerArn: lb.LoadBalancers[0].LoadBalancerArn,
		Protocol: elbv2types.ProtocolEnumHttp, Port: aws.Int32(8080), DefaultActions: forward, Tags: tags})
	require.NoError(t, err, "the grant allows tagging the listener the create mints")
	listenerArn := listener.Listeners[0].ListenerArn

	_, err = listeners.CreateRule(ctx, &elbv2.CreateRuleInput{ListenerArn: listenerArn, Priority: aws.Int32(10),
		Conditions: []elbv2types.RuleCondition{{Field: aws.String("path-pattern"), Values: []string{"/parent/*"}}},
		Actions:    forward, Tags: tags})
	require.Error(t, err, "the rule is not the listener the grant names")
	assert.Contains(t, err.Error(), "elasticloadbalancing:AddTags")

	rule, err := rules.CreateRule(ctx, &elbv2.CreateRuleInput{ListenerArn: listenerArn, Priority: aws.Int32(20),
		Conditions: []elbv2types.RuleCondition{{Field: aws.String("path-pattern"), Values: []string{"/created/*"}}},
		Actions:    forward, Tags: tags})
	require.NoError(t, err, "the grant allows tagging the rule the create mints")

	described, err := elb.DescribeTags(ctx, &elbv2.DescribeTagsInput{
		ResourceArns: []string{aws.ToString(listenerArn), aws.ToString(rule.Rules[0].RuleArn)}})
	require.NoError(t, err)
	require.Len(t, described.TagDescriptions, 2)
	for _, description := range described.TagDescriptions {
		require.Len(t, description.Tags, 1, aws.ToString(description.ResourceArn))
		assert.Equal(t, "blue", aws.ToString(description.Tags[0].Value))
	}
}

// RunTask authorizes against the task definition and mints a task in the
// cluster: its tagging authorizes against task/<cluster>/*.
func TestTagOnCreate_ECSRunTaskTaggingIsScopedToTheTask(t *testing.T) {
	admin := ecsClient()
	cluster, family := uniqueName("toc-run"), uniqueName("toc-run-task")
	_, err := admin.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })
	_, err = admin.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("public.ecr.aws/docker/library/alpine:latest")}}})
	require.NoError(t, err)

	client := func(user, tagging string) *ecs.Client {
		return ecs.NewFromConfig(tagOnCreateConfig(t, user, `{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"ecs:RunTask","Resource":"*"},
		  {"Effect":"Allow","Action":"ecs:TagResource","Resource":"`+tagging+`"}]}`),
			func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	}
	run := func(client *ecs.Client) (*ecs.RunTaskOutput, error) {
		out, err := client.RunTask(ctx, &ecs.RunTaskInput{Cluster: aws.String(cluster), TaskDefinition: aws.String(family),
			Tags: []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}}})
		if err == nil {
			for _, task := range out.Tasks {
				t.Cleanup(func() {
					_, _ = admin.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: task.TaskArn})
				})
			}
		}
		return out, err
	}

	_, err = run(client("ecs-run-tag-definition", "arn:aws:ecs:*:*:task-definition/"+family+":*"))
	require.Error(t, err, "the task is not the task definition the grant names")
	assert.Contains(t, err.Error(), "ecs:TagResource")

	out, err := run(client("ecs-run-tag-task", "arn:aws:ecs:*:*:task/"+cluster+"/*"))
	require.NoError(t, err, "the grant allows tagging the task the run mints in its cluster")
	require.Len(t, out.Tasks, 1)
	listed, err := admin.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: out.Tasks[0].TaskArn})
	require.NoError(t, err)
	require.Len(t, listed.Tags, 1)
	assert.Equal(t, "blue", aws.ToString(listed.Tags[0].Value))
}

// CreateFunction authorizes, and tags, the function its body names.
func TestTagOnCreate_LambdaTaggingIsScopedToTheFunction(t *testing.T) {
	name := uniqueName("toc-fn-scoped")
	restricted := lambda.NewFromConfig(tagOnCreateConfig(t, "lambda-create-tag-function", `{"Version":"2012-10-17","Statement":[
	  {"Effect":"Allow","Action":["lambda:CreateFunction","lambda:TagResource"],"Resource":"arn:aws:lambda:*:*:function:`+name+`"},
	  {"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`),
		func(o *lambda.Options) { o.BaseEndpoint = aws.String(baseURL) })
	create := func(name string) error {
		_, err := restricted.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(name),
			Role:         aws.String("arn:aws:iam::123456789012:role/toc-fn-role"),
			Handler:      aws.String("index.handler"),
			Runtime:      lambdatypes.RuntimeNodejs20x,
			Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
			Tags:         map[string]string{"team": "blue"},
		})
		if err == nil {
			t.Cleanup(func() {
				_, _ = lambdaClient().DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
			})
		}
		return err
	}

	require.NoError(t, create(name), "the grant names the function the create mints")
	tags, err := lambdaClient().ListTags(ctx, &lambda.ListTagsInput{
		Resource: aws.String("arn:aws:lambda:us-east-1:123456789012:function:" + name)})
	require.NoError(t, err)
	assert.Equal(t, "blue", tags.Tags["team"])

	err = create(uniqueName("toc-fn-other"))
	require.Error(t, err, "the grant names another function")
	assert.Contains(t, err.Error(), "lambda:CreateFunction")
}

// The Go SDK's RPC v2 CBOR PutMetricAlarm names the alarm in its body, which
// both the create and its tagging authorize against.
func TestTagOnCreate_CloudWatchAlarmTaggingIsScopedToTheAlarm(t *testing.T) {
	client := func(user, tagging string) *cloudwatch.Client {
		return cloudwatch.NewFromConfig(tagOnCreateConfig(t, user, `{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"cloudwatch:PutMetricAlarm","Resource":"arn:aws:cloudwatch:*:*:alarm:toc-*"},
		  {"Effect":"Allow","Action":"cloudwatch:TagResource","Resource":"`+tagging+`"}]}`),
			func(o *cloudwatch.Options) { o.BaseEndpoint = aws.String(baseURL) })
	}
	put := func(client *cloudwatch.Client, name string) error {
		_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName: aws.String(name), Namespace: aws.String("TagOnCreate"), MetricName: aws.String("Load"),
			Statistic: cwtypes.StatisticAverage, Period: aws.Int32(60), EvaluationPeriods: aws.Int32(1),
			Threshold: aws.Float64(1), ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			Tags: []cwtypes.Tag{{Key: aws.String("team"), Value: aws.String("blue")}},
		})
		if err == nil {
			t.Cleanup(func() {
				_, _ = cloudwatchClient().DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}})
			})
		}
		return err
	}

	err := put(client("cw-alarm-tag-dashboard", "arn:aws:cloudwatch::*:dashboard/*"), uniqueName("toc-alarm-dash"))
	require.Error(t, err, "the alarm is not a dashboard")
	assert.Contains(t, err.Error(), "cloudwatch:TagResource")

	alarms := client("cw-alarm-tag-alarm", "arn:aws:cloudwatch:*:*:alarm:*")
	name := uniqueName("toc-alarm-scoped")
	require.NoError(t, put(alarms, name), "the grants name the alarm the put creates")
	described, err := cloudwatchClient().DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	require.Len(t, described.MetricAlarms, 1)
	tags, err := cloudwatchClient().ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{
		ResourceARN: described.MetricAlarms[0].AlarmArn})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "blue", aws.ToString(tags.Tags[0].Value))

	err = put(alarms, "other-"+name)
	require.Error(t, err, "the create grant names alarms starting toc-")
	assert.Contains(t, err.Error(), "cloudwatch:PutMetricAlarm")
}
