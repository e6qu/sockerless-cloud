package aws_sdk_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ec2UserDataInstance launches an Amazon Linux 2023 instance in its own VPC
// with the user data script and waits until it runs.
func ec2UserDataInstance(t *testing.T, c *ec2.Client, cidr, userData string) string {
	t.Helper()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	octets := strings.SplitN(cidr, ".", 3)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, octets[0]+"."+octets[1]+".1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		UserData: aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	waitForEC2InstanceRunning(t, c, instanceID)
	return instanceID
}

// ssmShell runs a shell command on the instance through Run Command and
// returns its standard output.
func ssmShell(t *testing.T, c *ssm.Client, instanceID string, commands ...string) string {
	t.Helper()
	send, err := c.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String("AWS-RunShellScript"),
		InstanceIds:  []string{instanceID},
		Parameters:   map[string][]string{"commands": commands},
	})
	require.NoError(t, err)
	inv := waitForSSMCommandExecuted(t, c, aws.ToString(send.Command.CommandId), instanceID)
	require.Equal(t, "Success", string(inv.Status), "%v: %s", commands, aws.ToString(inv.StandardErrorContent))
	return aws.ToString(inv.StandardOutputContent)
}

// An instance boots the AMI's image, runs its user data as root with the
// instance's own hostname, reaches the instance metadata service through
// the VPC's reserved address the SDKs are pointed at, and writes its console.
func TestEC2_InstanceRunsUserDataAndReadsItsMetadata(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	userData := `#!/bin/sh
echo "user-data ran on $(uname -n) as $(id -un)"
token=$(curl -sS -X PUT "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
curl -sS -H "X-aws-ec2-metadata-token: $token" "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/meta-data/instance-id" > /var/tmp/instance-id
curl -sS -H "X-aws-ec2-metadata-token: $token" "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/meta-data/local-ipv4" > /var/tmp/local-ipv4
`
	instanceID := ec2UserDataInstance(t, c, "10.206.0.0/16", userData)
	desc, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	inst := desc.Reservations[0].Instances[0]
	privateIP := aws.ToString(inst.PrivateIpAddress)
	hostname := "ip-" + strings.ReplaceAll(privateIP, ".", "-")
	assert.Equal(t, types.ArchitectureValuesX8664, inst.Architecture)

	console, err := c.GetConsoleOutput(ctx, &ec2.GetConsoleOutputInput{InstanceId: aws.String(instanceID)})
	require.NoError(t, err)
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(console.Output))
	require.NoError(t, err)
	assert.Contains(t, string(decoded), "user-data ran on "+hostname+" as root", "the console carries the user data's output")

	s := ssmClient()
	assert.Equal(t, instanceID+"\n", ssmShell(t, s, instanceID, "cat /var/tmp/instance-id; echo"), "the metadata service answers the instance's own identity")
	assert.Equal(t, privateIP+"\n", ssmShell(t, s, instanceID, "cat /var/tmp/local-ipv4; echo"))
	assert.Equal(t, hostname+"\n", ssmShell(t, s, instanceID, "uname -n"))
	assert.Contains(t, ssmShell(t, s, instanceID, "cat /etc/os-release"), "Amazon Linux 2023")
}

// Stopping an instance keeps its root disk for the next start; an AMI created
// from the instance carries that disk into the instances launched from it;
// terminating discards it.
func TestEC2_StopKeepsTheRootDiskAndCreateImageCapturesIt(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	s := ssmClient()
	instanceID := ec2UserDataInstance(t, c, "10.207.0.0/16", "#!/bin/sh\necho golden > /var/tmp/marker\n")
	assert.Equal(t, "golden\n", ssmShell(t, s, instanceID, "cat /var/tmp/marker"))
	ssmShell(t, s, instanceID, "echo written-while-running > /var/tmp/disk")

	_, err := c.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	waitForEC2InstanceStopped(t, c, instanceID)
	_, err = s.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String("AWS-RunShellScript"),
		InstanceIds:  []string{instanceID},
		Parameters:   map[string][]string{"commands": {"true"}},
	})
	requireAWSErrorCode(t, err, "InvalidInstanceId")

	_, err = c.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	waitForEC2InstanceRunning(t, c, instanceID)
	assert.Equal(t, "written-while-running\n", ssmShell(t, s, instanceID, "cat /var/tmp/disk"), "the root disk survived the stop")

	img, err := c.CreateImage(ctx, &ec2.CreateImageInput{
		InstanceId: aws.String(instanceID),
		Name:       aws.String("captured-" + instanceID),
	})
	require.NoError(t, err)
	amiID := aws.ToString(img.ImageId)
	t.Cleanup(func() { _, _ = c.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: aws.String(amiID)}) })
	available, err := ec2.NewImageAvailableWaiter(c, func(o *ec2.ImageAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ec2.DescribeImagesInput{ImageIds: []string{amiID}}, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, available.Images, 1)
	assert.Equal(t, types.ArchitectureValuesX8664, available.Images[0].Architecture)
	assert.Equal(t, instanceID, aws.ToString(available.Images[0].SourceInstanceId))

	desc, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(amiID), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: desc.Reservations[0].Instances[0].SubnetId,
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	clone := aws.ToString(run.Instances[0].InstanceId)
	waitForEC2InstanceRunning(t, c, clone)
	assert.Equal(t, "written-while-running\n", ssmShell(t, s, clone, "cat /var/tmp/disk"), "the AMI captured the instance's disk")

	_, err = c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{clone}})
	require.NoError(t, err)
	waitForEC2InstanceTerminated(t, c, clone)
}

// Two VPCs over one CIDR cannot both hold a running instance on one engine:
// the second instance's launch fails with the internal error EC2 reports for
// a launch it could not complete.
func TestEC2_InstancesOfVPCsSharingACIDRCannotCoexist(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	first := ec2UserDataInstance(t, c, "10.208.0.0/16", "")

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.208.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.208.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	second := aws.ToString(run.Instances[0].InstanceId)
	// The InstanceTerminated waiter gives up on a pending instance, which is
	// what a launch that fails is until it fails, so the state is polled.
	var inst types.Instance
	require.Eventually(t, func() bool {
		desc, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{second}})
		require.NoError(t, err)
		inst = desc.Reservations[0].Instances[0]
		return inst.State.Name == types.InstanceStateNameTerminated
	}, 2*time.Minute, waiterMinDelay)
	require.NotNil(t, inst.StateReason)
	assert.Equal(t, "Server.InternalError", aws.ToString(inst.StateReason.Code))

	running, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{first}})
	require.NoError(t, err)
	assert.Equal(t, types.InstanceStateNameRunning, running.Reservations[0].Instances[0].State.Name)
}

// An instance of a different architecture than its AMI is refused at launch.
func TestEC2_RunInstancesRefusesAnArchitectureMismatch(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	_, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT4gNano,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
	})
	requireAWSErrorCode(t, err, "InvalidParameterValue")
	_, err = c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-0123456789abcdef0"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
	})
	requireAWSErrorCode(t, err, "InvalidAMIID.NotFound")
}

// An awsvpc task resolves names through the VPC's Route 53 Resolver, which
// answers the private hosted zones associated with the VPC, and reads its
// task metadata through the same reserved address.
func TestECS_TaskResolvesAPrivateHostedZoneThroughTheVPCResolver(t *testing.T) {
	t.Parallel()
	ecsc := ecsClient()
	r53 := r53Client()
	cw := cwLogsClient()
	vpcID, subnetID := createECSTestVPCSubnet(t, "private-zone")

	zone, err := r53.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("corp.internal."),
		CallerReference: aws.String(uniqueName("private-zone")),
		VPC:             &r53types.VPC{VPCId: aws.String(vpcID), VPCRegion: r53types.VPCRegionUsEast1},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = r53.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: zone.HostedZone.Id}) })
	_, err = r53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: zone.HostedZone.Id,
		ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name: aws.String("db.corp.internal."), Type: r53types.RRTypeA, TTL: aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("10.250.250.7")}},
			},
		}}},
	})
	require.NoError(t, err)

	clusterName := "private-zone-dns"
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	logGroup := "/ecs/private-zone-dns"
	td, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("private-zone-dns"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("resolver"),
			Image: aws.String("mirror.gcr.io/library/busybox:latest"),
			// The agent publishes a task's metadata as its containers come
			// up; the SDKs retry the endpoint, and so does this script.
			Command: []string{"sh", "-c",
				`nslookup db.corp.internal && for i in 1 2 3 4 5 6 7 8 9 10; do wget -qO- "$ECS_CONTAINER_METADATA_URI_V4/task" && exit 0; sleep 1; done; exit 1`},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroup,
					"awslogs-create-group":  "true",
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	})
	require.NoError(t, err)
	run, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	cleanupECSTask(t, ecsc, clusterName, taskArn)
	task := waitTaskStopped(t, ecsc, clusterName, taskArn)
	require.Len(t, task.Containers, 1)
	assert.EqualValues(t, 0, aws.ToInt32(task.Containers[0].ExitCode))

	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(logGroup)})
	require.NoError(t, err)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	logged := strings.Join(messages, "\n")
	assert.Contains(t, logged, "10.250.250.7", "the private zone answers inside its VPC")
	assert.Contains(t, logged, taskArn, "the task metadata endpoint answers this task")
}

// ImportImage takes an OCI image reference as a disk container's Url: the
// task completes with an AMI whose instances boot that image.
func TestEC2_ImportImageFromAnOCIReferenceBoots(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	imp, err := c.ImportImage(ctx, &ec2.ImportImageInput{
		Architecture: aws.String("x86_64"),
		Description:  aws.String("busybox root"),
		DiskContainers: []types.ImageDiskContainer{{
			Format: aws.String("RAW"),
			Url:    aws.String("mirror.gcr.io/library/busybox:latest"),
		}},
	})
	require.NoError(t, err)
	taskID := aws.ToString(imp.ImportTaskId)
	var task types.ImportImageTask
	// EC2 offers no waiter for ImportImage, so the task's own status is read
	// until it settles.
	require.Eventually(t, func() bool {
		out, err := c.DescribeImportImageTasks(ctx, &ec2.DescribeImportImageTasksInput{ImportTaskIds: []string{taskID}})
		require.NoError(t, err)
		require.Len(t, out.ImportImageTasks, 1)
		task = out.ImportImageTasks[0]
		return aws.ToString(task.Status) == "completed" || aws.ToString(task.Status) == "deleted"
	}, 2*time.Minute, waiterMinDelay)
	require.Equal(t, "completed", aws.ToString(task.Status), aws.ToString(task.StatusMessage))
	amiID := aws.ToString(task.ImageId)
	t.Cleanup(func() { _, _ = c.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: aws.String(amiID)}) })
	images, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{amiID}})
	require.NoError(t, err)
	require.Len(t, images.Images, 1)
	assert.Equal(t, "mirror.gcr.io/library/busybox:latest", aws.ToString(images.Images[0].ImageLocation))

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.209.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.209.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(amiID), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	waitForEC2InstanceRunning(t, c, aws.ToString(run.Instances[0].InstanceId))
}
