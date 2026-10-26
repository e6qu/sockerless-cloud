package aws_sdk_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmManagedNodePolicy grants what SSM Agent needs to register a managed node,
// as AmazonSSMManagedInstanceCore does on AWS.
const ssmManagedNodePolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:UpdateInstanceInformation","ssmmessages:*","ec2messages:*"],"Resource":"*"}]}`

func ssmManagedInstanceProfile(t *testing.T) string {
	t.Helper()
	iamc := iamClient()
	name := uniquename.New("ssm-managed")
	_, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("ssm-managed-node"), PolicyDocument: aws.String(ssmManagedNodePolicy),
	})
	require.NoError(t, err)
	_, err = iamc.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(name)})
	require.NoError(t, err)
	_, err = iamc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(name), RoleName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = iamc.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{InstanceProfileName: aws.String(name), RoleName: aws.String(name)})
		_, _ = iamc.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(name)})
		_, _ = iamc.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("ssm-managed-node")})
		_, _ = iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	return name
}

func ssmManagedProfileSpec(t *testing.T) *types.IamInstanceProfileSpecification {
	t.Helper()
	return &types.IamInstanceProfileSpecification{Name: aws.String(ssmManagedInstanceProfile(t))}
}

func launchSSMManagedInstanceFor(t *testing.T, c *ec2.Client, cidr string) string {
	t.Helper()
	return ec2UserDataInstance(t, c, cidr, "")
}

// ec2UserDataInstance launches an Amazon Linux 2023 instance in its own VPC
// with the user data script, as a managed node Run Command reaches, and waits
// until it runs.
func ec2UserDataInstance(t *testing.T, c *ec2.Client, cidr, userData string) string {
	t.Helper()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	octets := strings.SplitN(cidr, ".", 3)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, octets[0]+"."+octets[1]+".1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		IamInstanceProfile: ssmManagedProfileSpec(t),
		UserData:           aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	waitForEC2InstanceRunning(t, c, instanceID)
	return instanceID
}

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

// An instance boots the AMI's image, runs its user data as root under its own
// hostname, reaches the instance metadata service through the VPC's reserved
// address the SDKs use, and writes its console.
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

	// The VPC's resolver answers the reverse of another interface's address
	// with that interface's private DNS name; the instance's own address is
	// in its /etc/hosts, which getent reads first.
	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: inst.SubnetId})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: eni.NetworkInterface.NetworkInterfaceId})
	})
	eniIP, eniName := aws.ToString(eni.NetworkInterface.PrivateIpAddress), aws.ToString(eni.NetworkInterface.PrivateDnsName)
	require.NotEmpty(t, eniName)
	assert.Equal(t, eniIP+" "+eniName, strings.Join(strings.Fields(ssmShell(t, s, instanceID, "getent hosts "+eniIP)), " "),
		"the VPC's resolver answers the address's PTR record with the interface's private DNS name")
}

// Stopping keeps the root disk for the next start; an AMI created from the
// instance carries it into the instances launched from it; terminating
// discards it.
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
		IamInstanceProfile: ssmManagedProfileSpec(t),
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
// the second launch fails with the internal error Amazon EC2 reports for a
// launch it could not complete.
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
	// The InstanceTerminated waiter gives up on a pending instance, which a failing
	// launch is until it fails, so poll the state.
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
// answers the private hosted zones associated with the VPC, and reads its task
// metadata through the same reserved address.
func TestECS_TaskResolvesAPrivateHostedZoneThroughTheVPCResolver(t *testing.T) {
	t.Parallel()
	ecsc := ecsClient()
	r53 := r53Client()
	cw := cwLogsClient()
	vpcID, subnetID := createECSTestVPCSubnet(t, "private-zone")

	zone, err := r53.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("corp.internal."),
		CallerReference: aws.String(uniquename.New("private-zone")),
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
			// The task metadata answers from the container's first moment.
			Command: []string{"sh", "-c",
				`nslookup db.corp.internal && wget -qO- "$ECS_CONTAINER_METADATA_URI_V4/task"`},
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
	// Amazon EC2 has no waiter for ImportImage, so poll the task's status until it
	// settles.
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

// An instance receives what its security groups admit and nothing else: a
// neighbour reaches the port a rule names and not another, a rule authorized
// while the instance runs admits its port at once, and a revoked one refuses it
// again.
func TestEC2_SecurityGroupFiltersWhatAnInstanceReceives(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.246.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.246.1.0/24")
	web, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sg-filter-web"), Description: aws.String("web"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	permission := func(port int32) []types.IpPermission {
		return []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(port), ToPort: aws.Int32(port),
			IpRanges: []types.IpRange{{CidrIp: aws.String("10.246.0.0/16")}},
		}}
	}
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: web.GroupId, IpPermissions: permission(80)})
	require.NoError(t, err)

	profile := ssmManagedProfileSpec(t)
	launch := func(userData string, groups []string) string {
		run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
			MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet, SecurityGroupIds: groups,
			IamInstanceProfile: profile,
			UserData:           aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
		})
		require.NoError(t, err)
		terminateEC2InstancesOnCleanup(t, c, run)
		instanceID := aws.ToString(run.Instances[0].InstanceId)
		waitForEC2InstanceRunning(t, c, instanceID)
		return instanceID
	}
	serverID := launch("#!/bin/sh\nmkdir -p /srv/www && echo ok > /srv/www/index.html\n"+
		"cd /srv/www && (python3 -m http.server 80 >/dev/null 2>&1 &) && (python3 -m http.server 81 >/dev/null 2>&1 &)\n", []string{aws.ToString(web.GroupId)})
	clientID := launch("#!/bin/sh\necho client up\n", nil)
	desc, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{serverID}})
	require.NoError(t, err)
	serverIP := aws.ToString(desc.Reservations[0].Instances[0].PrivateIpAddress)

	s := ssmClient()
	probe := func(port int) string {
		return strings.TrimSpace(ssmShell(t, s, clientID, fmt.Sprintf("curl -sS -m 5 http://%s:%d/ >/dev/null 2>&1; echo rc=$?", serverIP, port)))
	}
	require.Eventually(t, func() bool { return probe(80) == "rc=0" }, 30*time.Second, time.Second, "the server never answered on the admitted port")
	assert.Equal(t, "rc=28", probe(81), "port 81, which no rule admits, must time out")

	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: web.GroupId, IpPermissions: permission(81)})
	require.NoError(t, err)
	assert.Equal(t, "rc=0", probe(81), "the rule authorized while the instance runs admits its port")

	_, err = c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: web.GroupId, IpPermissions: permission(80)})
	require.NoError(t, err)
	assert.Equal(t, "rc=28", probe(80), "the revoked rule's port times out again")
}
