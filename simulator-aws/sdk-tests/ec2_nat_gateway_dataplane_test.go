package aws_sdk_test

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outsideListener is a listener of the test's own on an address a workload
// in a VPC reaches only by leaving the VPC: the host's own address, where
// the test records who connected.
func outsideListener(t *testing.T) (address string, lastRemote func() string) {
	t.Helper()
	probe, err := net.Dial("udp4", "192.0.2.1:9")
	require.NoError(t, err)
	host := probe.LocalAddr().(*net.UDPAddr).IP.String()
	_ = probe.Close()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var remote string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remote, _, _ = net.SplitHostPort(r.RemoteAddr)
		mu.Unlock()
		_, _ = io.WriteString(w, "outside\n")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return net.JoinHostPort(host, port), func() string {
		mu.Lock()
		defer mu.Unlock()
		return remote
	}
}

// An instance in a private subnet leaves the VPC through the NAT gateway its
// route table names: what it opens outside arrives from the gateway and not
// from the instance, and the gateway's metrics count the connection and its
// bytes. Rerouted through the internet gateway the instance leaves the NAT
// gateway's metrics alone, and once the gateway is deleted the route to it
// leads nowhere.
func TestEC2_NatGatewayCarriesAPrivateSubnetsConnections(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.248.0.0/16")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) })
	public := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.248.1.0/24")
	private := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.248.2.0/24")

	igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	_, err = c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: igw.InternetGateway.InternetGatewayId, VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	routeTable := func(subnet *string) *string {
		rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpc.Vpc.VpcId})
		require.NoError(t, err)
		_, err = c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: rt.RouteTable.RouteTableId, SubnetId: subnet})
		require.NoError(t, err)
		return rt.RouteTable.RouteTableId
	}
	publicRT := routeTable(public)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: publicRT, DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: igw.InternetGateway.InternetGatewayId})
	require.NoError(t, err)

	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId}) })
	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{SubnetId: public, AllocationId: eip.AllocationId})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)
	t.Cleanup(func() { _, _ = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)}) })
	require.NoError(t, ec2.NewNatGatewayAvailableWaiter(c).Wait(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}}, 2*time.Minute))
	privateRT := routeTable(private)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: privateRT, DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: aws.String(natID)})
	require.NoError(t, err)

	outside, lastRemote := outsideListener(t)
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: private,
		UserData: aws.String(base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho up\n"))),
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	running := waitForEC2InstanceRunning(t, c, instanceID)
	instanceIP := aws.ToString(running.Reservations[0].Instances[0].PrivateIpAddress)

	s := ssmClient()
	reach := func() string {
		return strings.TrimSpace(ssmShell(t, s, instanceID, fmt.Sprintf("curl -sS -m 5 http://%s/ >/dev/null 2>&1; echo rc=$?", outside)))
	}
	start := time.Now().UTC().Add(-time.Minute)
	require.Equal(t, "rc=0", reach(), "the instance does not reach the outside through the NAT gateway")
	assert.NotEqual(t, instanceIP, lastRemote(), "the connection arrived from the instance's own address, not the gateway's")

	cw := cloudwatchClient()
	sum := func(metric string) float64 {
		stats, err := cw.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
			Namespace:  aws.String("AWS/NATGateway"),
			MetricName: aws.String(metric),
			Dimensions: []cwtypes.Dimension{{Name: aws.String("NatGatewayId"), Value: aws.String(natID)}},
			StartTime:  aws.Time(start),
			EndTime:    aws.Time(time.Now().UTC().Add(time.Minute)),
			Period:     aws.Int32(3600),
			Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
		})
		require.NoError(t, err)
		total := 0.0
		for _, point := range stats.Datapoints {
			total += aws.ToFloat64(point.Sum)
		}
		return total
	}
	// The gateway reports a connection's bytes when it ends, which the
	// request's close brings about; the metric follows on the cloud's own
	// cadence.
	require.Eventually(t, func() bool { return sum("BytesOutToDestination") > 0 }, time.Minute, 2*time.Second, "the gateway counted no bytes toward the destination")
	assert.GreaterOrEqual(t, sum("ConnectionAttemptCount"), 1.0)
	assert.GreaterOrEqual(t, sum("ConnectionEstablishedCount"), 1.0)
	assert.Greater(t, sum("BytesInFromDestination"), 0.0)
	attempts := sum("ConnectionAttemptCount")

	_, err = c.ReplaceRoute(ctx, &ec2.ReplaceRouteInput{RouteTableId: privateRT, DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: igw.InternetGateway.InternetGatewayId})
	require.NoError(t, err)
	ssmShell(t, s, instanceID, fmt.Sprintf("curl -sS -m 5 http://%s/ >/dev/null 2>&1; true", outside))
	assert.Equal(t, attempts, sum("ConnectionAttemptCount"), "a subnet rerouted through the internet gateway still counted on the NAT gateway")

	_, err = c.ReplaceRoute(ctx, &ec2.ReplaceRouteInput{RouteTableId: privateRT, DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: aws.String(natID)})
	require.NoError(t, err)
	require.Equal(t, "rc=0", reach(), "the instance does not reach the outside once routed through the NAT gateway again")
	_, err = c.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)})
	require.NoError(t, err)
	assert.NotEqual(t, "rc=0", reach(), "a route to a deleted NAT gateway still leads outside")
}
