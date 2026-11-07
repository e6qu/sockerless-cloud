package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/require"
)

// A request a task sends through a NAT gateway reaches AWS from the gateway's
// Elastic IP address, so an aws:SourceIp condition naming that address admits
// it, and one naming another address does not.
func TestEC2_NatGatewaysPublicAddressIsTheSourceOfItsTraffic(t *testing.T) {
	t.Parallel()
	c, ecsc, iamc := ec2Client(), ecsClient(), iamClient()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.167.0.0/16")})
	require.NoError(t, err)
	vpcID := vpc.Vpc.VpcId
	public := createSubnetFor(t, c, vpcID, "10.167.1.0/24")
	private := createSubnetFor(t, c, vpcID, "10.167.2.0/24")
	igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	_, err = c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: igw.InternetGateway.InternetGatewayId, VpcId: vpcID})
	require.NoError(t, err)
	routeTable := func(subnet *string) *string {
		rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpcID})
		require.NoError(t, err)
		_, err = c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: rt.RouteTable.RouteTableId, SubnetId: subnet})
		require.NoError(t, err)
		return rt.RouteTable.RouteTableId
	}
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: routeTable(public), DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: igw.InternetGateway.InternetGatewayId})
	require.NoError(t, err)
	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: ec2types.DomainTypeVpc})
	require.NoError(t, err)
	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{SubnetId: public, AllocationId: eip.AllocationId})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)
	require.NoError(t, ec2.NewNatGatewayAvailableWaiter(c).Wait(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}}, 2*time.Minute))
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: routeTable(private), DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: aws.String(natID)})
	require.NoError(t, err)
	t.Cleanup(func() {
		stopECSTasksInSubnet(t, ecsc, aws.ToString(private))
		_, _ = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)})
		require.NoError(t, ec2.NewNatGatewayDeletedWaiter(c).Wait(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}}, 2*time.Minute))
		_, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId})
	})

	roleName := "nat-source-ip-task"
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamc.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("from-nat")})
		_, _ = iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
	})
	allowFrom := func(cidr string) {
		t.Helper()
		_, err := iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName: aws.String(roleName), PolicyName: aws.String("from-nat"),
			PolicyDocument: aws.String(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":%q}}}]}`, cidr)),
		})
		require.NoError(t, err)
	}

	cluster := "nat-source-ip"
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	definition, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("nat-source-ip"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		TaskRoleArn:             role.Role.Arn,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("aws-cli"),
			Image:      aws.String("mirror.gcr.io/amazon/aws-cli:2.27.49"),
			EntryPoint: []string{"aws"},
			Command:    []string{"s3api", "list-buckets", "--query", "Owner.ID", "--output", "text"},
			Environment: []ecstypes.KeyValuePair{
				{Name: aws.String("AWS_ENDPOINT_URL"), Value: aws.String(fmt.Sprintf("http://host.docker.internal:%d", simPort))},
				{Name: aws.String("AWS_REGION"), Value: aws.String("us-east-1")},
				{Name: aws.String("AWS_EC2_METADATA_DISABLED"), Value: aws.String("true")},
			},
		}},
	})
	require.NoError(t, err)
	listBucketsExit := func() int32 {
		t.Helper()
		started, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
			Cluster: aws.String(cluster), TaskDefinition: definition.TaskDefinition.TaskDefinitionArn,
			LaunchType: ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{aws.ToString(private)}},
			},
		})
		require.NoError(t, err)
		require.Len(t, started.Tasks, 1)
		taskArn := aws.ToString(started.Tasks[0].TaskArn)
		cleanupECSTask(t, ecsc, cluster, taskArn)
		task := waitTaskStopped(t, ecsc, cluster, taskArn)
		require.NotEmpty(t, task.Containers)
		require.NotNil(t, task.Containers[0].ExitCode, "%s", aws.ToString(task.StoppedReason))
		return aws.ToInt32(task.Containers[0].ExitCode)
	}

	allowFrom(aws.ToString(eip.PublicIp) + "/32")
	require.Equal(t, int32(0), listBucketsExit(), "a policy admitting the NAT gateway's Elastic IP refused the task's request")
	allowFrom("198.51.100.7/32")
	require.NotEqual(t, int32(0), listBucketsExit(), "a policy admitting another address admitted the task's request")
}
