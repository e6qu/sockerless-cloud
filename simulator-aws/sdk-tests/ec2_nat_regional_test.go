package aws_sdk_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func describeNatGateway(t *testing.T, c *ec2.Client, natID string) types.NatGateway {
	t.Helper()
	out, err := c.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}})
	require.NoError(t, err)
	require.Len(t, out.NatGateways, 1)
	return out.NatGateways[0]
}

func natAddressZones(n types.NatGateway) []string {
	var zones []string
	for _, a := range n.NatGatewayAddresses {
		zones = append(zones, aws.ToString(a.AvailabilityZone))
	}
	return zones
}

func createZoneSubnet(t *testing.T, c *ec2.Client, vpcID *string, cidr, zone string) string {
	t.Helper()
	out, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpcID, CidrBlock: aws.String(cidr), AvailabilityZone: aws.String(zone)})
	require.NoError(t, err)
	return aws.ToString(out.Subnet.SubnetId)
}

func allocateEIP(t *testing.T, c *ec2.Client) string {
	t.Helper()
	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId}) })
	return aws.ToString(eip.AllocationId)
}

// A regional NAT gateway created without per-zone addresses manages its zones
// itself: it covers each zone the VPC has a subnet in, with an address of its
// own there, and leaves a zone whose last subnet goes.
func TestEC2_RegionalNatGatewayAutomaticZones(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.75.0.0/16")})
	require.NoError(t, err)

	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{VpcId: vpc.Vpc.VpcId, AvailabilityMode: types.AvailabilityModeRegional})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)
	t.Cleanup(func() { _, _ = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)}) })
	require.NoError(t, ec2.NewNatGatewayAvailableWaiter(c).Wait(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}}, 2*time.Minute))

	n := describeNatGateway(t, c, natID)
	assert.Equal(t, types.AvailabilityModeRegional, n.AvailabilityMode)
	assert.Equal(t, types.AutoProvisionZonesStateEnabled, n.AutoProvisionZones)
	assert.Equal(t, types.AutoScalingIpsStateEnabled, n.AutoScalingIps)
	assert.Nil(t, n.SubnetId, "a regional NAT gateway has no subnet")
	assert.Equal(t, aws.ToString(vpc.Vpc.VpcId), aws.ToString(n.VpcId))
	assert.Equal(t, natID, aws.ToString(n.RouteTableId))
	assert.Empty(t, n.NatGatewayAddresses, "a VPC without subnets leaves the gateway in no zone")

	createZoneSubnet(t, c, vpc.Vpc.VpcId, "10.75.1.0/24", "us-east-1a")
	subnetB := createZoneSubnet(t, c, vpc.Vpc.VpcId, "10.75.2.0/24", "us-east-1b")
	n = describeNatGateway(t, c, natID)
	assert.ElementsMatch(t, []string{"us-east-1a", "us-east-1b"}, natAddressZones(n))
	for _, a := range n.NatGatewayAddresses {
		assert.NotEmpty(t, aws.ToString(a.AllocationId))
		assert.NotEmpty(t, aws.ToString(a.PublicIp))
		assert.NotEmpty(t, aws.ToString(a.AssociationId))
		assert.NotEmpty(t, aws.ToString(a.NetworkInterfaceId))
		assert.NotEmpty(t, aws.ToString(a.AvailabilityZoneId))
		assert.Equal(t, types.NatGatewayAddressStatusSucceeded, a.Status)
		prefix := map[string]string{"us-east-1a": "10.75.1.", "us-east-1b": "10.75.2."}[aws.ToString(a.AvailabilityZone)]
		assert.True(t, strings.HasPrefix(aws.ToString(a.PrivateIp), prefix), "the gateway's address in %s is %s", aws.ToString(a.AvailabilityZone), aws.ToString(a.PrivateIp))
	}

	_, err = c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetB)})
	require.NoError(t, err)
	n = describeNatGateway(t, c, natID)
	assert.Equal(t, []string{"us-east-1a"}, natAddressZones(n), "the gateway retracts from a zone without subnets")

	filtered, err := c.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{Filter: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{aws.ToString(vpc.Vpc.VpcId)}}}})
	require.NoError(t, err)
	require.Len(t, filtered.NatGateways, 1)
}

// A regional NAT gateway given per-zone Elastic IP addresses covers exactly
// those zones, takes more addresses in a zone the request names, and lets
// any of them go.
func TestEC2_RegionalNatGatewayManualZones(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.76.0.0/16")})
	require.NoError(t, err)
	eips := []string{allocateEIP(t, c), allocateEIP(t, c), allocateEIP(t, c)}

	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		VpcId: vpc.Vpc.VpcId, AvailabilityMode: types.AvailabilityModeRegional,
		AvailabilityZoneAddresses: []types.AvailabilityZoneAddress{
			{AvailabilityZone: aws.String("us-east-1a"), AllocationIds: []string{eips[0]}},
			{AvailabilityZoneId: aws.String("useast1-az2"), AllocationIds: []string{eips[1]}},
		},
	})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)
	t.Cleanup(func() { _, _ = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)}) })
	assert.Equal(t, types.AutoProvisionZonesStateDisabled, nat.NatGateway.AutoProvisionZones)
	assert.Equal(t, types.AutoScalingIpsStateDisabled, nat.NatGateway.AutoScalingIps)
	assert.ElementsMatch(t, []string{"us-east-1a", "us-east-1b"}, natAddressZones(*nat.NatGateway))
	for _, a := range nat.NatGateway.NatGatewayAddresses {
		assert.Nil(t, a.PrivateIp, "a zone without a subnet holds no address of the gateway")
	}

	// A subnet in a zone the gateway does not cover leaves its coverage as it is.
	createZoneSubnet(t, c, vpc.Vpc.VpcId, "10.76.3.0/24", "us-east-1c")
	createZoneSubnet(t, c, vpc.Vpc.VpcId, "10.76.1.0/24", "us-east-1a")
	n := describeNatGateway(t, c, natID)
	assert.ElementsMatch(t, []string{"us-east-1a", "us-east-1b"}, natAddressZones(n))

	assoc, err := c.AssociateNatGatewayAddress(ctx, &ec2.AssociateNatGatewayAddressInput{
		NatGatewayId: aws.String(natID), AllocationIds: []string{eips[2]}, AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"us-east-1a", "us-east-1a", "us-east-1b"}, natAddressZones(types.NatGateway{NatGatewayAddresses: assoc.NatGatewayAddresses}))

	_, err = c.AssociateNatGatewayAddress(ctx, &ec2.AssociateNatGatewayAddressInput{
		NatGatewayId: aws.String(natID), AllocationIds: []string{eips[2]}, AvailabilityZone: aws.String("us-east-1c"),
	})
	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "Resource.AlreadyAssociated", apiErr.ErrorCode())

	var zoneB string
	for _, a := range n.NatGatewayAddresses {
		if aws.ToString(a.AvailabilityZone) == "us-east-1b" {
			zoneB = aws.ToString(a.AssociationId)
		}
	}
	_, err = c.DisassociateNatGatewayAddress(ctx, &ec2.DisassociateNatGatewayAddressInput{NatGatewayId: aws.String(natID), AssociationIds: []string{zoneB}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"us-east-1a", "us-east-1a"}, natAddressZones(describeNatGateway(t, c, natID)), "the only address of a zone goes, and the zone with it")
}

// CreateNatGateway refuses what a regional gateway does not take, and a
// gateway whose Elastic IP address cannot be associated fails with the
// model's failure code.
func TestEC2_RegionalNatGatewayRefusals(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.77.0.0/16")})
	require.NoError(t, err)
	subnet := createZoneSubnet(t, c, vpc.Vpc.VpcId, "10.77.1.0/24", "us-east-1a")

	code := func(in *ec2.CreateNatGatewayInput) string {
		_, err := c.CreateNatGateway(ctx, in)
		var apiErr smithy.APIError
		require.ErrorAs(t, err, &apiErr)
		return apiErr.ErrorCode()
	}
	assert.Equal(t, "MissingParameter", code(&ec2.CreateNatGatewayInput{AvailabilityMode: types.AvailabilityModeRegional}))
	assert.Equal(t, "InvalidParameterCombination", code(&ec2.CreateNatGatewayInput{AvailabilityMode: types.AvailabilityModeRegional, VpcId: vpc.Vpc.VpcId, SubnetId: aws.String(subnet)}))
	assert.Equal(t, "InvalidVpcID.NotFound", code(&ec2.CreateNatGatewayInput{AvailabilityMode: types.AvailabilityModeRegional, VpcId: aws.String("vpc-00000000000000000")}))
	assert.Equal(t, "InvalidParameterValue", code(&ec2.CreateNatGatewayInput{
		AvailabilityMode: types.AvailabilityModeRegional, VpcId: vpc.Vpc.VpcId,
		AvailabilityZoneAddresses: []types.AvailabilityZoneAddress{{AvailabilityZone: aws.String("eu-west-1a"), AllocationIds: []string{allocateEIP(t, c)}}},
	}))

	failed, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		AvailabilityMode: types.AvailabilityModeRegional, VpcId: vpc.Vpc.VpcId,
		AvailabilityZoneAddresses: []types.AvailabilityZoneAddress{{AvailabilityZone: aws.String("us-east-1a"), AllocationIds: []string{"eipalloc-0123456789abcdef0"}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: failed.NatGateway.NatGatewayId})
	})
	n := describeNatGateway(t, c, aws.ToString(failed.NatGateway.NatGatewayId))
	assert.Equal(t, types.NatGatewayStateFailed, n.State)
	assert.Equal(t, "InvalidAllocationID.NotFound", aws.ToString(n.FailureCode))
	assert.Equal(t, "Elastic IP address eipalloc-0123456789abcdef0 could not be associated with this NAT gateway", aws.ToString(n.FailureMessage))
}

// Private subnets in two zones routed to one regional NAT gateway each leave
// the VPC through the gateway's address in their own zone, and a zone the
// gateway stops covering has no way out.
func TestEC2_RegionalNatGatewayCarriesEachZonesConnections(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.244.0.0/16")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) })
	igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	_, err = c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: igw.InternetGateway.InternetGatewayId, VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)

	zones := []string{"us-east-1a", "us-east-1b"}
	eips := []string{allocateEIP(t, c), allocateEIP(t, c)}
	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		VpcId: vpc.Vpc.VpcId, AvailabilityMode: types.AvailabilityModeRegional,
		AvailabilityZoneAddresses: []types.AvailabilityZoneAddress{
			{AvailabilityZone: aws.String(zones[0]), AllocationIds: []string{eips[0]}},
			{AvailabilityZone: aws.String(zones[1]), AllocationIds: []string{eips[1]}},
		},
	})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)
	t.Cleanup(func() { _, _ = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)}) })
	require.NoError(t, ec2.NewNatGatewayAvailableWaiter(c).Wait(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}}, 2*time.Minute))

	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: rt.RouteTable.RouteTableId, DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: aws.String(natID)})
	require.NoError(t, err)

	outside, lastRemote := outsideListener(t)
	s := ssmClient()
	instances := make([]string, len(zones))
	for i, zone := range zones {
		subnet := createZoneSubnet(t, c, vpc.Vpc.VpcId, fmt.Sprintf("10.244.%d.0/24", i+1), zone)
		_, err = c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: rt.RouteTable.RouteTableId, SubnetId: aws.String(subnet)})
		require.NoError(t, err)
		run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
			MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: aws.String(subnet),
			IamInstanceProfile: ssmManagedProfileSpec(t),
			UserData:           aws.String(base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho up\n"))),
		})
		require.NoError(t, err)
		terminateEC2InstancesOnCleanup(t, c, run)
		instances[i] = aws.ToString(run.Instances[0].InstanceId)
	}
	reach := func(instanceID string) string {
		return strings.TrimSpace(ssmShell(t, s, instanceID, fmt.Sprintf("curl -sS -m 5 http://%s/ >/dev/null 2>&1; echo rc=$?", outside)))
	}

	gateway := describeNatGateway(t, c, natID)
	zoneAddress := map[string]string{}
	for _, a := range gateway.NatGatewayAddresses {
		zoneAddress[aws.ToString(a.AvailabilityZone)] = aws.ToString(a.PrivateIp)
	}
	require.NotEqual(t, zoneAddress[zones[0]], zoneAddress[zones[1]], "each zone has an address of the gateway's own")
	for i, zone := range zones {
		running := waitForEC2InstanceRunning(t, c, instances[i])
		instanceIP := aws.ToString(running.Reservations[0].Instances[0].PrivateIpAddress)
		require.Equal(t, "rc=0", reach(instances[i]), "the instance in %s does not reach the outside through the regional NAT gateway", zone)
		assert.NotEqual(t, instanceIP, lastRemote(), "the connection from %s arrived from the instance's own address", zone)
		assert.Equal(t, zoneAddress[zone], lastRemote(), "the connection from %s left through another zone's address", zone)
	}

	for _, a := range gateway.NatGatewayAddresses {
		if aws.ToString(a.AvailabilityZone) == zones[1] {
			_, err = c.DisassociateNatGatewayAddress(ctx, &ec2.DisassociateNatGatewayAddressInput{NatGatewayId: aws.String(natID), AssociationIds: []string{aws.ToString(a.AssociationId)}})
			require.NoError(t, err)
		}
	}
	assert.NotEqual(t, "rc=0", reach(instances[1]), "a zone the gateway no longer covers still leads outside")
	assert.Equal(t, "rc=0", reach(instances[0]), "the zone the gateway still covers lost its way out")
}
