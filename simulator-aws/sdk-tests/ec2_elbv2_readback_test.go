package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_LaunchTemplateProvenanceTags expects an instance launched from a
// launch template to carry the aws:ec2launchtemplate:{id,version} system tags
// terraform-provider-aws flattens into aws_instance.launch_template (the
// Instance has no launch-template field), and the template's image and type.
func TestEC2_LaunchTemplateProvenanceTags(t *testing.T) {
	t.Parallel()
	ltName := uniquename.New("readback-lt")
	c := ec2Client()
	lt, err := c.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(ltName),
		LaunchTemplateData: &ec2types.RequestLaunchTemplateData{
			ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: ec2types.InstanceTypeT3Nano,
		},
	})
	require.NoError(t, err)
	ltID := aws.ToString(lt.LaunchTemplate.LaunchTemplateId)

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
		LaunchTemplate: &ec2types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(ltID), Version: aws.String("$Latest"),
		},
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	id := aws.ToString(run.Instances[0].InstanceId)

	desc, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	require.NoError(t, err)
	inst := desc.Reservations[0].Instances[0]
	tags := map[string]string{}
	for _, tg := range inst.Tags {
		tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, ltID, tags["aws:ec2launchtemplate:id"], "launch-template id tag must round-trip")
	assert.NotEmpty(t, tags["aws:ec2launchtemplate:version"], "launch-template version tag must round-trip")
	assert.Equal(t, ec2AmazonLinuxAMI(t), aws.ToString(inst.ImageId))
	assert.Equal(t, ec2types.InstanceTypeT3Nano, inst.InstanceType)
}

// TestEC2_RouteNetworkInterfaceId expects a route targeting an ENI to report
// NetworkInterfaceId, or aws_route drifts.
func TestEC2_RouteNetworkInterfaceId(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.41.0.0/16")})
	require.NoError(t, err)
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.41.1.0/24")})
	require.NoError(t, err)
	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: sub.Subnet.SubnetId})
	require.NoError(t, err)
	eniID := aws.ToString(eni.NetworkInterface.NetworkInterfaceId)
	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	rtID := aws.ToString(rt.RouteTable.RouteTableId)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId: aws.String(rtID), DestinationCidrBlock: aws.String("0.0.0.0/0"),
		NetworkInterfaceId: aws.String(eniID),
	})
	require.NoError(t, err)

	desc, err := c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{rtID}})
	require.NoError(t, err)
	var found bool
	for _, route := range desc.RouteTables[0].Routes {
		if aws.ToString(route.DestinationCidrBlock) == "0.0.0.0/0" {
			found = true
			assert.Equal(t, eniID, aws.ToString(route.NetworkInterfaceId))
		}
	}
	require.True(t, found, "0.0.0.0/0 route present")
}

func TestEC2_SecurityGroupEgressIpv6Ranges(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.42.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("ipv6-egress-sg"), Description: aws.String("ipv6"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	// Revoke the default allow-all IPv4 egress rule so the combined IPv4 and IPv6
	// rule is no InvalidPermission.Duplicate.
	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
		GroupId: sg.GroupId,
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)
	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: sg.GroupId,
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			Ipv6Ranges: []ec2types.Ipv6Range{{CidrIpv6: aws.String("::/0")}},
		}},
	})
	require.NoError(t, err)

	desc, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{aws.ToString(sg.GroupId)}})
	require.NoError(t, err)
	require.NotEmpty(t, desc.SecurityGroups[0].IpPermissionsEgress)
	egress := desc.SecurityGroups[0].IpPermissionsEgress[0]
	require.Len(t, egress.Ipv6Ranges, 1, "Ipv6Ranges must round-trip")
	assert.Equal(t, "::/0", aws.ToString(egress.Ipv6Ranges[0].CidrIpv6))
	require.Len(t, egress.IpRanges, 1)
	assert.Equal(t, "0.0.0.0/0", aws.ToString(egress.IpRanges[0].CidrIp))
}

// TestELBv2_ListenerSslPolicy expects DescribeListeners to report the HTTPS
// listener's SslPolicy, or aws_lb_listener drifts.
func TestELBv2_ListenerSslPolicy(t *testing.T) {
	t.Parallel()
	tgName := uniquename.New("sslpolicy-tg")
	lbName := uniquename.New("sslpolicy-lb")
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.43.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	mkSub := func(cidr, az string) string {
		s, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String(cidr), AvailabilityZone: aws.String(az)})
		require.NoError(t, err)
		return aws.ToString(s.Subnet.SubnetId)
	}
	subA, subB := mkSub("10.43.1.0/24", "us-east-1a"), mkSub("10.43.2.0/24", "us-east-1b")

	c := elbv2Client()
	lb, err := c.CreateLoadBalancer(ctx, &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name: aws.String(lbName), Type: elbv2types.LoadBalancerTypeEnumApplication, Subnets: []string{subA, subB},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)
	tg, err := c.CreateTargetGroup(ctx, &elasticloadbalancingv2.CreateTargetGroupInput{
		Name: aws.String(tgName), Protocol: elbv2types.ProtocolEnumHttp, Port: aws.Int32(80), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	const policy = "ELBSecurityPolicy-TLS13-1-2-2021-06"
	certArn := importELBv2Certificate(t, "ssl-policy.example.test")
	listenerPort := elbv2CreateListenerOnFreePort(t, func(port int32) error {
		_, err := c.CreateListener(ctx, &elasticloadbalancingv2.CreateListenerInput{
			LoadBalancerArn: aws.String(lbArn), Protocol: elbv2types.ProtocolEnumHttps, Port: aws.Int32(port),
			SslPolicy:    aws.String(policy),
			Certificates: []elbv2types.Certificate{{CertificateArn: aws.String(certArn)}},
			DefaultActions: []elbv2types.Action{{
				Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: tg.TargetGroups[0].TargetGroupArn,
			}},
		})
		return err
	})

	desc, err := c.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{LoadBalancerArn: aws.String(lbArn)})
	require.NoError(t, err)
	var https *elbv2types.Listener
	for i := range desc.Listeners {
		if aws.ToInt32(desc.Listeners[i].Port) == listenerPort {
			https = &desc.Listeners[i]
		}
	}
	require.NotNil(t, https)
	assert.Equal(t, policy, aws.ToString(https.SslPolicy))
}
