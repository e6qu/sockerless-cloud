package aws_sdk_test

import (
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/require"
)

// A subnet's route table decides whether a task's packets leave the VPC: from
// a subnet whose table names no 0.0.0.0/0 route the task has no way out, and
// once the table routes 0.0.0.0/0 to an internet gateway the same task
// definition reaches the simulator outside the VPC.
func TestEC2_SubnetRouteTableDecidesATasksWayOut(t *testing.T) {
	t.Parallel()
	ec2c, ecsc := ec2Client(), ecsClient()
	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.165.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)
	subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.165.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)
	tableOut, err := ec2c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	tableID := aws.ToString(tableOut.RouteTable.RouteTableId)
	_, err = ec2c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: aws.String(tableID), SubnetId: aws.String(subnetID)})
	require.NoError(t, err)
	igwOut, err := ec2c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	igwID := aws.ToString(igwOut.InternetGateway.InternetGatewayId)
	_, err = ec2c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: aws.String(igwID), VpcId: aws.String(vpcID)})
	require.NoError(t, err)

	cluster := "subnet-route-out"
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		stopECSTasksInSubnet(t, ecsc, subnetID)
		_, err := ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
		require.NoError(t, err)
		_, err = ec2c.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{RouteTableId: aws.String(tableID)})
		require.NoError(t, err)
		_, err = ec2c.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{InternetGatewayId: aws.String(igwID), VpcId: aws.String(vpcID)})
		require.NoError(t, err)
		_, err = ec2c.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{InternetGatewayId: aws.String(igwID)})
		require.NoError(t, err)
		_, err = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
		require.NoError(t, err)
	})

	definition, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("subnet-route-out"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("probe"),
			Image:      aws.String("mirror.gcr.io/library/busybox:latest"),
			EntryPoint: []string{"sh", "-c"},
			// wget exits 1 for a connection it cannot make; 7 says the
			// simulator answered.
			Command: []string{fmt.Sprintf("wget -q -T 5 -O /dev/null http://host.docker.internal:%d/health && exit 7", simPort)},
		}},
	})
	require.NoError(t, err)
	exitOfATaskIn := func() int32 {
		t.Helper()
		started, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String(cluster),
			TaskDefinition: definition.TaskDefinition.TaskDefinitionArn,
			LaunchType:     ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
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

	require.Equal(t, int32(1), exitOfATaskIn(), "a task in a subnet whose table names no route out reached the simulator")

	_, err = ec2c.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId: aws.String(tableID), DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String(igwID),
	})
	require.NoError(t, err)
	require.Equal(t, int32(7), exitOfATaskIn(), "a task in a subnet routed to an internet gateway did not reach the simulator")

	routes, err := ec2c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{tableID}})
	require.NoError(t, err)
	require.Contains(t, routeTargets(routes.RouteTables[0].Routes), igwID)
}

func routeTargets(routes []ec2types.Route) []string {
	var out []string
	for _, route := range routes {
		out = append(out, aws.ToString(route.GatewayId))
	}
	return out
}
