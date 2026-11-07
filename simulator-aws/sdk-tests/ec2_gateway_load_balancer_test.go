package aws_sdk_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gwlbApplianceUserData is the customer's appliance: it answers the target
// group's TCP health check on port 80, logs each flow it inspects with the
// endpoint the GENEVE options name, and returns every packet to the Gateway
// Load Balancer as AWS's tunnel handler does.
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
        log.write("%s %s %d %d %d vpce-%08x\n" % (flow + (int.from_bytes(options.get(1, bytes(8)), "big"),)))
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
	_, err = elb.CreateListener(ctx, &elasticloadbalancingv2.CreateListenerInput{
		LoadBalancerArn: lbArn,
		DefaultActions:  []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: tgArn}},
	})
	require.NoError(t, err)
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
			Command: []string{fmt.Sprintf(`for i in $(seq %d); do curl -sS -o /dev/null --max-time 20 http://host.docker.internal:%d/health || exit 1; done`,
				connections, simPort)},
		}},
	})
	require.NoError(t, err)
	// runClient runs the client in the application subnet, requires every
	// connection to have made it there and back, and returns its address.
	runClient := func() string {
		t.Helper()
		started, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
			Cluster: aws.String(cluster), TaskDefinition: definition.TaskDefinition.TaskDefinitionArn,
			LaunchType: ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{aws.ToString(applications)}},
			},
		})
		require.NoError(t, err)
		require.Len(t, started.Tasks, 1)
		taskArn := aws.ToString(started.Tasks[0].TaskArn)
		cleanupECSTask(t, ecsc, cluster, taskArn)
		task := waitTaskStopped(t, ecsc, cluster, taskArn)
		require.NotEmpty(t, task.Containers)
		require.NotNil(t, task.Containers[0].ExitCode, "%s", aws.ToString(task.StoppedReason))
		require.Equal(t, int32(0), aws.ToInt32(task.Containers[0].ExitCode), "a connection through the appliances failed")
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
	// flowsFrom counts, per appliance, the client's flows to the simulator's
	// port that appliance inspected, and checks each names the endpoint.
	flowsFrom := func(client string) []int {
		t.Helper()
		counts := make([]int, len(applianceIDs))
		for i, id := range applianceIDs {
			for _, line := range strings.Split(strings.TrimSpace(ssmShell(t, s, id, "cat /var/tmp/flows 2>/dev/null; true")), "\n") {
				fields := strings.Fields(line)
				if len(fields) != 6 || fields[0] != client || fields[4] != fmt.Sprint(simPort) {
					continue
				}
				assert.Equal(t, endpointID, fields[5], "the GENEVE endpoint option names the endpoint the flow came through")
				counts[i]++
			}
		}
		return counts
	}

	spread := flowsFrom(runClient())
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
	sticky := flowsFrom(runClient())
	assert.Equal(t, connections, sticky[0]+sticky[1], "every connection crossed an appliance: %v", sticky)
	assert.True(t, sticky[0] == 0 || sticky[1] == 0, "source_ip_dest_ip keeps one client's flows to one destination on one appliance: %v", sticky)
}
