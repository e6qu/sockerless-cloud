package aws_sdk_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gwlbApplianceUserData is the customer's appliance: it answers the target
// group's TCP health check on port 80, logs each flow it inspects with the
// endpoint the GENEVE options name and the length of its first packet, and
// returns every packet to the Gateway Load Balancer as AWS's tunnel handler
// does.
const gwlbApplianceUserData = `#!/bin/bash
cat > /usr/local/bin/appliance.py <<'EOF'
import http.server, socket, struct, threading
threading.Thread(target=lambda: http.server.HTTPServer(("", 80), http.server.BaseHTTPRequestHandler).serve_forever(), daemon=True).start()
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("", 6081))
seen = set()
log = open("/var/tmp/flows", "a", buffering=1)
while True:
    d, peer = s.recvfrom(65535)
    end = 8 + (d[0] & 0x3f) * 4
    options, off = {}, 8
    while end - off >= 4:
        cls, typ, length = struct.unpack("!HBB", d[off:off + 4])
        length = (length & 0x1f) * 4
        if cls == 0x0108:
            options[typ] = d[off + 4:off + 4 + length]
        off += 4 + length
    ip = d[end:]
    ihl = (ip[0] & 15) * 4
    proto = ip[9]
    sport, dport = struct.unpack("!HH", ip[ihl:ihl + 4]) if proto in (6, 17) else (0, 0)
    flow = (socket.inet_ntoa(ip[12:16]), socket.inet_ntoa(ip[16:20]), proto, sport, dport)
    if flow not in seen:
        seen.add(flow)
        log.write("%s %s %d %d %d vpce-%08x %d\n" % (flow + (int.from_bytes(options.get(1, bytes(8)), "big"), struct.unpack("!H", ip[2:4])[0])))
    s.sendto(d, peer)
EOF
nohup python3 /usr/local/bin/appliance.py >/var/tmp/appliance.log 2>&1 &
`

// A route that names a Gateway Load Balancer endpoint sends a subnet's
// traffic through the load balancer's appliances: each new flow reaches an
// appliance wrapped in GENEVE that names the endpoint, the appliance's return
// carries on to the destination, flows spread over the appliances by their
// 5-tuple by default, and source_ip_dest_ip stickiness keeps every flow
// between two addresses on one appliance.
func TestEC2_GatewayLoadBalancerEndpointSendsFlowsThroughItsAppliances(t *testing.T) {
	t.Parallel()
	c, elb, ecsc, s := ec2Client(), elbv2Client(), ecsClient(), ssmClient()

	vpcWithInternet := func(cidr string) *string {
		vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
		require.NoError(t, err)
		igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
		require.NoError(t, err)
		_, err = c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: igw.InternetGateway.InternetGatewayId, VpcId: vpc.Vpc.VpcId})
		require.NoError(t, err)
		return vpc.Vpc.VpcId
	}
	routeTable := func(vpcID, subnet *string, route ec2.CreateRouteInput) {
		rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpcID})
		require.NoError(t, err)
		_, err = c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: rt.RouteTable.RouteTableId, SubnetId: subnet})
		require.NoError(t, err)
		route.RouteTableId = rt.RouteTable.RouteTableId
		route.DestinationCidrBlock = aws.String("0.0.0.0/0")
		_, err = c.CreateRoute(ctx, &route)
		require.NoError(t, err)
	}
	internetGatewayOf := func(vpcID *string) *string {
		igws, err := c.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
			Filters: []ec2types.Filter{{Name: aws.String("attachment.vpc-id"), Values: []string{aws.ToString(vpcID)}}},
		})
		require.NoError(t, err)
		require.Len(t, igws.InternetGateways, 1)
		return igws.InternetGateways[0].InternetGatewayId
	}

	// The provider VPC holds the load balancer and its appliances.
	provider := vpcWithInternet("10.175.0.0/16")
	appliances := createSubnetFor(t, c, provider, "10.175.1.0/24")
	routeTable(provider, appliances, ec2.CreateRouteInput{GatewayId: internetGatewayOf(provider)})
	// The appliances admit the load balancer's health checks and GENEVE.
	applianceGroup, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String(uniquename.New("gwlb-appliance")), Description: aws.String("appliances behind a Gateway Load Balancer"), VpcId: provider,
	})
	require.NoError(t, err)
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: applianceGroup.GroupId,
		IpPermissions: []ec2types.IpPermission{
			{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("10.175.0.0/16")}}},
			{IpProtocol: aws.String("udp"), FromPort: aws.Int32(6081), ToPort: aws.Int32(6081), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("10.175.0.0/16")}}},
		},
	})
	require.NoError(t, err)
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount: aws.Int32(2), MaxCount: aws.Int32(2), SubnetId: appliances, SecurityGroupIds: []string{aws.ToString(applianceGroup.GroupId)},
		IamInstanceProfile: ssmManagedProfileSpec(t),
		UserData:           aws.String(base64.StdEncoding.EncodeToString([]byte(gwlbApplianceUserData))),
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	var applianceIDs []string
	for _, inst := range run.Instances {
		applianceIDs = append(applianceIDs, aws.ToString(inst.InstanceId))
		waitForEC2InstanceRunning(t, c, aws.ToString(inst.InstanceId))
	}

	lb, err := elb.CreateLoadBalancer(ctx, &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name: aws.String(uniquename.New("gwlb")), Type: elbtypes.LoadBalancerTypeEnumGateway, Subnets: []string{aws.ToString(appliances)},
	})
	require.NoError(t, err)
	lbArn := lb.LoadBalancers[0].LoadBalancerArn
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elasticloadbalancingv2.DeleteLoadBalancerInput{LoadBalancerArn: lbArn})
	})
	tg, err := elb.CreateTargetGroup(ctx, &elasticloadbalancingv2.CreateTargetGroupInput{
		Name: aws.String(uniquename.New("gwlb-tg")), Protocol: elbtypes.ProtocolEnumGeneve, Port: aws.Int32(6081),
		VpcId: provider, TargetType: elbtypes.TargetTypeEnumInstance,
		HealthCheckProtocol: elbtypes.ProtocolEnumTcp, HealthCheckPort: aws.String("80"),
		HealthCheckIntervalSeconds: aws.Int32(5), HealthyThresholdCount: aws.Int32(2),
	})
	require.NoError(t, err)
	tgArn := tg.TargetGroups[0].TargetGroupArn
	t.Cleanup(func() {
		_, _ = elb.DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{TargetGroupArn: tgArn})
	})
	var targets []elbtypes.TargetDescription
	for _, id := range applianceIDs {
		targets = append(targets, elbtypes.TargetDescription{Id: aws.String(id)})
	}
	_, err = elb.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{TargetGroupArn: tgArn, Targets: targets})
	require.NoError(t, err)
	listener, err := elb.CreateListener(ctx, &elasticloadbalancingv2.CreateListenerInput{
		LoadBalancerArn: lbArn,
		DefaultActions:  []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: tgArn}},
	})
	require.NoError(t, err)
	assert.Nil(t, listener.Listeners[0].Port, "a Gateway Load Balancer listener has no port")
	assert.Empty(t, listener.Listeners[0].Protocol, "a Gateway Load Balancer listener has no protocol")
	require.NoError(t, elasticloadbalancingv2.NewTargetInServiceWaiter(elb).Wait(ctx,
		&elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: tgArn, Targets: targets}, 3*time.Minute))

	// The load balancer stands in its subnet at an address of its own.
	lbInterfaces, err := c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{Filters: []ec2types.Filter{
		{Name: aws.String("subnet-id"), Values: []string{aws.ToString(appliances)}},
		{Name: aws.String("interface-type"), Values: []string{"gateway_load_balancer"}},
	}})
	require.NoError(t, err)
	require.Len(t, lbInterfaces.NetworkInterfaces, 1, "the Gateway Load Balancer has a network interface in its subnet")
	assert.True(t, strings.HasPrefix(aws.ToString(lbInterfaces.NetworkInterfaces[0].PrivateIpAddress), "10.175.1."))

	service, err := c.CreateVpcEndpointServiceConfiguration(ctx, &ec2.CreateVpcEndpointServiceConfigurationInput{
		GatewayLoadBalancerArns: []string{aws.ToString(lbArn)}, AcceptanceRequired: aws.Bool(false),
	})
	require.NoError(t, err)
	serviceID := service.ServiceConfiguration.ServiceId
	t.Cleanup(func() {
		_, _ = c.DeleteVpcEndpointServiceConfigurations(ctx, &ec2.DeleteVpcEndpointServiceConfigurationsInput{ServiceIds: []string{aws.ToString(serviceID)}})
	})

	// The consumer VPC routes its application subnet through the endpoint, and
	// the endpoint's subnet to the internet gateway.
	consumer := vpcWithInternet("10.176.0.0/16")
	applications := createSubnetFor(t, c, consumer, "10.176.1.0/24")
	endpointSubnet := createSubnetFor(t, c, consumer, "10.176.2.0/24")
	routeTable(consumer, endpointSubnet, ec2.CreateRouteInput{GatewayId: internetGatewayOf(consumer)})
	endpoint, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
		VpcEndpointType: ec2types.VpcEndpointTypeGatewayLoadBalancer, VpcId: consumer,
		ServiceName: service.ServiceConfiguration.ServiceName, SubnetIds: []string{aws.ToString(endpointSubnet)},
	})
	require.NoError(t, err)
	endpointID := aws.ToString(endpoint.VpcEndpoint.VpcEndpointId)
	waitVpcEndpointAvailable(t, c, endpointID)
	t.Cleanup(func() {
		_, _ = c.DeleteVpcEndpoints(ctx, &ec2.DeleteVpcEndpointsInput{VpcEndpointIds: []string{endpointID}})
	})
	require.Len(t, endpoint.VpcEndpoint.NetworkInterfaceIds, 1)
	endpointInterface, err := c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: endpoint.VpcEndpoint.NetworkInterfaceIds})
	require.NoError(t, err)
	require.Len(t, endpointInterface.NetworkInterfaces, 1)
	assert.Equal(t, ec2types.NetworkInterfaceTypeGatewayLoadBalancerEndpoint, endpointInterface.NetworkInterfaces[0].InterfaceType)
	assert.True(t, strings.HasPrefix(aws.ToString(endpointInterface.NetworkInterfaces[0].PrivateIpAddress), "10.176.2."),
		"the endpoint holds an address in its subnet")
	routeTable(consumer, applications, ec2.CreateRouteInput{VpcEndpointId: aws.String(endpointID)})
	t.Cleanup(func() { stopECSTasksInSubnet(t, ecsc, aws.ToString(applications)) })

	cluster := uniquename.New("gwlb")
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	const connections = 16
	clientLogs := "/ecs/" + uniquename.New("gwlb-client")
	definition, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(uniquename.New("gwlb-client")),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("client"),
			Image:      aws.String("mirror.gcr.io/amazon/aws-cli:2.27.49"),
			EntryPoint: []string{"sh", "-c"},
			// Each connection reports its outcome; a failed one also reports
			// the routes and neighbours the client sent it by.
			Command: []string{fmt.Sprintf(`for i in $(seq %d); do
  out=$(curl -sS -o /dev/null -w '%%{http_code} connect=%%{time_connect}s total=%%{time_total}s' --max-time 20 http://host.docker.internal:%d/health 2>&1); rc=$?
  echo "connection $i: exit $rc $(date +%%T.%%N) $out"
  if [ $rc -ne 0 ]; then cat /proc/net/route /proc/net/arp; exit 1; fi
done`, connections, simPort)},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group": clientLogs, "awslogs-create-group": "true", "awslogs-stream-prefix": "gwlb",
				},
			},
		}},
	})
	require.NoError(t, err)
	// runClient runs the client in a subnet, requires every connection to
	// have made it there and back, and returns its address.
	runClient := func(subnet *string) string {
		t.Helper()
		started, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
			Cluster: aws.String(cluster), TaskDefinition: definition.TaskDefinition.TaskDefinitionArn,
			LaunchType: ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{aws.ToString(subnet)}},
			},
		})
		require.NoError(t, err)
		require.Len(t, started.Tasks, 1)
		taskArn := aws.ToString(started.Tasks[0].TaskArn)
		cleanupECSTask(t, ecsc, cluster, taskArn)
		task := waitTaskStopped(t, ecsc, cluster, taskArn)
		require.NotEmpty(t, task.Containers)
		require.NotNil(t, task.Containers[0].ExitCode, "%s", aws.ToString(task.StoppedReason))
		if code := aws.ToInt32(task.Containers[0].ExitCode); code != 0 {
			t.Fatalf("a connection through the appliances failed (exit %d)\n%s", code, gwlbFailureReport(t, s, clientLogs, taskArn, applianceIDs))
		}
		for _, attachment := range task.Attachments {
			for _, detail := range attachment.Details {
				if aws.ToString(detail.Name) == "privateIPv4Address" {
					return aws.ToString(detail.Value)
				}
			}
		}
		t.Fatalf("task %s reports no address", taskArn)
		return ""
	}
	// flowLines are the appliances' log lines for a client's flows to a port,
	// per appliance.
	flowLines := func(client string, port int) [][][]string {
		t.Helper()
		lines := make([][][]string, len(applianceIDs))
		for i, id := range applianceIDs {
			for _, line := range strings.Split(strings.TrimSpace(ssmShell(t, s, id, "cat /var/tmp/flows 2>/dev/null; true")), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 7 && fields[0] == client && fields[4] == fmt.Sprint(port) {
					lines[i] = append(lines[i], fields)
				}
			}
		}
		return lines
	}
	// flowsFrom counts, per appliance, the client's flows to the simulator's
	// port that appliance inspected, and checks each names the endpoint.
	flowsFrom := func(client, endpoint string) []int {
		t.Helper()
		counts := make([]int, len(applianceIDs))
		for i, lines := range flowLines(client, simPort) {
			for _, fields := range lines {
				assert.Equal(t, endpoint, fields[5], "the GENEVE endpoint option names the endpoint the flow came through")
				counts[i]++
			}
		}
		return counts
	}

	spread := flowsFrom(runClient(applications), endpointID)
	assert.Equal(t, connections, spread[0]+spread[1], "every connection crossed an appliance: %v", spread)
	assert.Positive(t, spread[0], "5-tuple flows spread over both appliances: %v", spread)
	assert.Positive(t, spread[1], "5-tuple flows spread over both appliances: %v", spread)

	_, err = elb.ModifyTargetGroupAttributes(ctx, &elasticloadbalancingv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: tgArn,
		Attributes: []elbtypes.TargetGroupAttribute{
			{Key: aws.String("stickiness.enabled"), Value: aws.String("true")},
			{Key: aws.String("stickiness.type"), Value: aws.String("source_ip_dest_ip")},
		},
	})
	require.NoError(t, err)
	sticky := flowsFrom(runClient(applications), endpointID)
	assert.Equal(t, connections, sticky[0]+sticky[1], "every connection crossed an appliance: %v", sticky)
	assert.True(t, sticky[0] == 0 || sticky[1] == 0, "source_ip_dest_ip keeps one client's flows to one destination on one appliance: %v", sticky)

	// A full-sized packet crosses whole: the VPC carries 9001-byte frames, so
	// an 8,000-byte datagram and its 68 bytes of GENEVE reach the appliance
	// unfragmented, which the node, sending with fragmentation forbidden,
	// would otherwise refuse.
	sender, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(uniquename.New("gwlb-jumbo")),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("sender"),
			Image:      aws.String("mirror.gcr.io/amazon/aws-cli:2.27.49"),
			EntryPoint: []string{"bash", "-c"},
			Command:    []string{`dd if=/dev/zero bs=7972 count=1 status=none > /dev/udp/host.docker.internal/9999`},
		}},
	})
	require.NoError(t, err)
	started, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
		Cluster: aws.String(cluster), TaskDefinition: sender.TaskDefinition.TaskDefinitionArn,
		LaunchType: ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{aws.ToString(applications)}},
		},
	})
	require.NoError(t, err)
	senderArn := aws.ToString(started.Tasks[0].TaskArn)
	cleanupECSTask(t, ecsc, cluster, senderArn)
	senderTask := waitTaskStopped(t, ecsc, cluster, senderArn)
	require.Equal(t, int32(0), aws.ToInt32(senderTask.Containers[0].ExitCode), "%s", aws.ToString(senderTask.StoppedReason))
	var senderAddress string
	for _, attachment := range senderTask.Attachments {
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == "privateIPv4Address" {
				senderAddress = aws.ToString(detail.Value)
			}
		}
	}
	var jumbo []string
	for _, lines := range flowLines(senderAddress, 9999) {
		for _, fields := range lines {
			jumbo = append(jumbo, fields[6])
		}
	}
	assert.Equal(t, []string{"8000"}, jumbo, "the 8,000-byte datagram reached one appliance whole")

	// A second endpoint of the load balancer in the same zone and VPC serves
	// another application subnet, and the GENEVE options of that subnet's
	// traffic name the second endpoint.
	secondSubnet := createSubnetFor(t, c, consumer, "10.176.3.0/24")
	routeTable(consumer, secondSubnet, ec2.CreateRouteInput{GatewayId: internetGatewayOf(consumer)})
	second, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
		VpcEndpointType: ec2types.VpcEndpointTypeGatewayLoadBalancer, VpcId: consumer,
		ServiceName: service.ServiceConfiguration.ServiceName, SubnetIds: []string{aws.ToString(secondSubnet)},
	})
	require.NoError(t, err)
	secondID := aws.ToString(second.VpcEndpoint.VpcEndpointId)
	waitVpcEndpointAvailable(t, c, secondID)
	t.Cleanup(func() {
		_, _ = c.DeleteVpcEndpoints(ctx, &ec2.DeleteVpcEndpointsInput{VpcEndpointIds: []string{secondID}})
	})
	moreApplications := createSubnetFor(t, c, consumer, "10.176.4.0/24")
	routeTable(consumer, moreApplications, ec2.CreateRouteInput{VpcEndpointId: aws.String(secondID)})
	t.Cleanup(func() { stopECSTasksInSubnet(t, ecsc, aws.ToString(moreApplications)) })
	viaSecond := flowsFrom(runClient(moreApplications), secondID)
	assert.Equal(t, connections, viaSecond[0]+viaSecond[1], "every connection through the second endpoint crossed an appliance: %v", viaSecond)
	viaFirst := flowsFrom(runClient(applications), endpointID)
	assert.Equal(t, connections, viaFirst[0]+viaFirst[1], "the first endpoint's subnet still names the first endpoint: %v", viaFirst)
}

// waitVpcEndpointAvailable waits for a Gateway Load Balancer endpoint to
// leave Pending, which it does once the load balancer serves it. The SDK has
// no waiter for endpoints, so it polls the endpoint's own State.
func waitVpcEndpointAvailable(t *testing.T, c *ec2.Client, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out, err := c.DescribeVpcEndpoints(t.Context(), &ec2.DescribeVpcEndpointsInput{VpcEndpointIds: []string{id}})
		require.NoError(t, err)
		require.Len(t, out.VpcEndpoints, 1)
		state := out.VpcEndpoints[0].State
		if state == ec2types.StateAvailable {
			return
		}
		require.Equal(t, ec2types.StatePending, state, "endpoint %s left Pending for %s", id, state)
		require.True(t, time.Now().Before(deadline), "endpoint %s is still Pending", id)
		time.Sleep(time.Second)
	}
}

// gwlbFailureReport is what explains a failed connection through the
// appliances: the client's own account of each connection, and each
// appliance's flow log, program output and network configuration.
func gwlbFailureReport(t *testing.T, s *ssm.Client, logGroup, taskArn string, applianceIDs []string) string {
	t.Helper()
	var b strings.Builder
	taskID := taskArn[strings.LastIndex(taskArn, "/")+1:]
	events, err := cwLogsClient().FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroup), LogStreamNamePrefix: aws.String("gwlb/client/" + taskID),
	})
	if err != nil {
		fmt.Fprintf(&b, "client log: %v\n", err)
	} else {
		b.WriteString("client log:\n")
		for _, e := range events.Events {
			fmt.Fprintf(&b, "  %s\n", aws.ToString(e.Message))
		}
	}
	for _, id := range applianceIDs {
		fmt.Fprintf(&b, "appliance %s:\n%s\n", id, ssmShell(t, s, id,
			"tail -n 40 /var/tmp/flows 2>&1 || true", "cat /var/tmp/appliance.log 2>&1 || true",
			"ip -br addr 2>&1 || true", "ip route 2>&1 || true"))
	}
	return b.String()
}
