package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestECS_TaskDefEnvironment(t *testing.T) {
	client := ecsClient()

	envVars := []ecstypes.KeyValuePair{
		{Name: aws.String("APP_ENV"), Value: aws.String("production")},
		{Name: aws.String("DB_HOST"), Value: aws.String("db.example.com")},
		{Name: aws.String("LOG_LEVEL"), Value: aws.String("debug")},
	}

	regOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("env-roundtrip"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:        aws.String("app"),
				Image:       aws.String("alpine:latest"),
				Environment: envVars,
			},
		},
	})
	require.NoError(t, err)

	// Describe the task definition and verify env vars round-trip
	descOut, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: regOut.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)

	td := descOut.TaskDefinition
	require.Len(t, td.ContainerDefinitions, 1)

	gotEnv := td.ContainerDefinitions[0].Environment
	require.Len(t, gotEnv, 3, "should have 3 environment variables")

	envMap := make(map[string]string)
	for _, kv := range gotEnv {
		envMap[*kv.Name] = *kv.Value
	}
	assert.Equal(t, "production", envMap["APP_ENV"])
	assert.Equal(t, "db.example.com", envMap["DB_HOST"])
	assert.Equal(t, "debug", envMap["LOG_LEVEL"])
}

func TestECS_TaskDefCPUMemory(t *testing.T) {
	client := ecsClient()

	regOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("cpu-mem-test"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("512"),
		Memory:                  aws.String("1024"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:   aws.String("app"),
				Image:  aws.String("alpine:latest"),
				Memory: aws.Int32(1024),
			},
		},
	})
	require.NoError(t, err)

	descOut, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: regOut.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)

	td := descOut.TaskDefinition
	assert.Equal(t, "512", *td.Cpu, "task-level CPU should be 512")
	assert.Equal(t, "1024", *td.Memory, "task-level memory should be 1024MB")
	assert.Equal(t, ecstypes.NetworkModeAwsvpc, td.NetworkMode)
	require.Len(t, td.RequiresCompatibilities, 1)
	assert.Equal(t, ecstypes.CompatibilityFargate, td.RequiresCompatibilities[0])
}

func TestECS_TaskDefMountPoints(t *testing.T) {
	client := ecsClient()

	regOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("mount-points-test"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes: []ecstypes.Volume{
			{
				Name: aws.String("data-vol"),
			},
			{
				Name: aws.String("config-vol"),
			},
		},
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:  aws.String("app"),
				Image: aws.String("alpine:latest"),
				MountPoints: []ecstypes.MountPoint{
					{
						SourceVolume:  aws.String("data-vol"),
						ContainerPath: aws.String("/data"),
						ReadOnly:      aws.Bool(false),
					},
					{
						SourceVolume:  aws.String("config-vol"),
						ContainerPath: aws.String("/config"),
						ReadOnly:      aws.Bool(true),
					},
				},
			},
		},
	})
	require.NoError(t, err)

	descOut, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: regOut.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)

	td := descOut.TaskDefinition

	// Verify volumes
	require.Len(t, td.Volumes, 2)
	volNames := map[string]bool{}
	for _, v := range td.Volumes {
		volNames[*v.Name] = true
	}
	assert.True(t, volNames["data-vol"], "should have data-vol")
	assert.True(t, volNames["config-vol"], "should have config-vol")

	// Verify mount points on the container
	require.Len(t, td.ContainerDefinitions, 1)
	mounts := td.ContainerDefinitions[0].MountPoints
	require.Len(t, mounts, 2)

	mountMap := map[string]ecstypes.MountPoint{}
	for _, m := range mounts {
		mountMap[*m.SourceVolume] = m
	}
	assert.Equal(t, "/data", *mountMap["data-vol"].ContainerPath)
	assert.Equal(t, false, *mountMap["data-vol"].ReadOnly)
	assert.Equal(t, "/config", *mountMap["config-vol"].ContainerPath)
	assert.Equal(t, true, *mountMap["config-vol"].ReadOnly)
}

func TestECS_RunTaskTags(t *testing.T) {
	client := ecsClient()

	clusterName := "tags-test-cluster"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "run-task-tags")

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("tags-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:  aws.String("app"),
				Image: aws.String("alpine:latest"),
			},
		},
	})
	require.NoError(t, err)

	tags := []ecstypes.Tag{
		{Key: aws.String("environment"), Value: aws.String("test")},
		{Key: aws.String("team"), Value: aws.String("platform")},
		{Key: aws.String("cost-center"), Value: aws.String("engineering")},
	}

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(*tdOut.TaskDefinition.TaskDefinitionArn),
		Count:          aws.Int32(1),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Tags:           tags,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets: []string{subnetID},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := *runOut.Tasks[0].TaskArn
	cleanupECSTask(t, client, clusterName, taskArn)

	// Describe with TAGS include to get tag data
	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
		Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)

	gotTags := descOut.Tasks[0].Tags
	tagMap := map[string]string{}
	for _, tag := range gotTags {
		tagMap[*tag.Key] = *tag.Value
	}
	assert.Equal(t, "test", tagMap["environment"])
	assert.Equal(t, "platform", tagMap["team"])
	assert.Equal(t, "engineering", tagMap["cost-center"])
}

func TestECS_RunTaskNetworkConfig(t *testing.T) {
	client := ecsClient()

	clusterName := "netcfg-test-cluster"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("netcfg-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:  aws.String("app"),
				Image: aws.String("alpine:latest"),
			},
		},
	})
	require.NoError(t, err)

	// Create VPC and security groups via EC2 API first
	ec2C := ec2Client()
	vpcOut, err := ec2C.CreateVpc(ctx, &ec2sdk.CreateVpcInput{
		CidrBlock: aws.String("10.0.0.0/16"),
	})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId

	sg1Out, err := ec2C.CreateSecurityGroup(ctx, &ec2sdk.CreateSecurityGroupInput{
		GroupName:   aws.String("sg-netcfg-1"),
		Description: aws.String("test sg 1"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)
	sg2Out, err := ec2C.CreateSecurityGroup(ctx, &ec2sdk.CreateSecurityGroupInput{
		GroupName:   aws.String("sg-netcfg-2"),
		Description: aws.String("test sg 2"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)

	// Real ECS validates subnets exist in EC2; create them up front so
	// the simulator's RunTask hits the real-AWS validation contract.
	sub1Out, err := ec2C.CreateSubnet(ctx, &ec2sdk.CreateSubnetInput{
		VpcId:     aws.String(vpcID),
		CidrBlock: aws.String("10.0.10.0/24"),
	})
	require.NoError(t, err)
	sub2Out, err := ec2C.CreateSubnet(ctx, &ec2sdk.CreateSubnetInput{
		VpcId:     aws.String(vpcID),
		CidrBlock: aws.String("10.0.20.0/24"),
	})
	require.NoError(t, err)
	subnets := []string{*sub1Out.Subnet.SubnetId, *sub2Out.Subnet.SubnetId}
	securityGroups := []string{*sg1Out.GroupId, *sg2Out.GroupId}

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(*tdOut.TaskDefinition.TaskDefinitionArn),
		Count:          aws.Int32(1),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        subnets,
				SecurityGroups: securityGroups,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := *runOut.Tasks[0].TaskArn
	cleanupECSTask(t, client, clusterName, taskArn)

	// Poll until the task's ENI attachment is populated with its subnet and IP.
	// The attachment is filled in as the task reaches RUNNING; a fixed sleep
	// races on a loaded CI runner where that transition can take seconds.
	var foundSubnet, foundIP bool
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName),
			Tasks:   []string{taskArn},
		})
		require.NoError(t, err)
		require.Len(t, descOut.Tasks, 1)

		foundSubnet, foundIP = false, false
		for _, att := range descOut.Tasks[0].Attachments {
			for _, detail := range att.Details {
				if detail.Name != nil && detail.Value != nil {
					if *detail.Name == "subnetId" && *detail.Value != "" {
						foundSubnet = true
					}
					if *detail.Name == "privateIPv4Address" && *detail.Value != "" {
						foundIP = true
					}
				}
			}
		}
		if foundSubnet && foundIP {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	assert.True(t, foundSubnet, "task attachment should have a subnetId")
	assert.True(t, foundIP, "task attachment should have a privateIPv4Address")
}
