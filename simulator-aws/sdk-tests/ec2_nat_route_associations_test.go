package aws_sdk_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/require"
)

// requireNetnsFabric gates a test on the kernel offering this process network
// namespaces, which the real VPC fabric is built from. A missing ip, nft,
// nsenter or sysctl on a capable host fails the test instead of skipping it.
func requireNetnsFabric(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the real netns fabric needs a Linux kernel")
	}
	body, err := os.ReadFile("/proc/self/status")
	require.NoError(t, err)
	var caps uint64
	for _, line := range strings.Split(string(body), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "CapEff:" {
			caps, err = strconv.ParseUint(fields[1], 16, 64)
			require.NoError(t, err)
		}
	}
	const capNetAdmin, capSysAdmin = 12, 21
	if caps&(1<<capNetAdmin) == 0 || caps&(1<<capSysAdmin) == 0 {
		t.Skip("platform gate: the real netns fabric needs CAP_NET_ADMIN and CAP_SYS_ADMIN")
	}
	for _, bin := range []string{"ip", "nft", "nsenter", "sysctl"} {
		_, err := exec.LookPath(bin)
		require.NoError(t, err, "this host can run the netns fabric but is missing %s", bin)
	}
}

// startSourceProbe listens on the host's primary address and delivers the
// source address of each request, keyed by its path, on the returned channel.
func startSourceProbe(t *testing.T) (string, <-chan [2]string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	seen := make(chan [2]string, 16)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		seen <- [2]string{strings.TrimPrefix(r.URL.Path, "/"), host}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	route, err := exec.Command("ip", "route", "get", "1.1.1.1").CombinedOutput()
	require.NoError(t, err, "%s", route)
	fields := strings.Fields(string(route))
	host := ""
	for i, f := range fields {
		if f == "src" && i+1 < len(fields) {
			host = fields[i+1]
		}
	}
	require.NotEmpty(t, host, "no source address in %q", route)
	return "http://" + net.JoinHostPort(host, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)), seen
}

// TestEC2_NATRouteFollowsRouteTableAssociations runs an Amazon ECS task in a
// subnet associated with a NAT route's table after the route was created, and
// one in a subnet the main route table governs implicitly; each reports to a
// probe on the host, which must see the NAT gateway's Elastic IP address.
func TestEC2_NATRouteFollowsRouteTableAssociations(t *testing.T) {
	requireNetnsFabric(t)
	probe, seen := startSourceProbe(t)
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.65.0.0/16")})
	require.NoError(t, err)
	vpcID := vpc.Vpc.VpcId
	subnet := func(cidr string) *string {
		out, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpcID, CidrBlock: aws.String(cidr), AvailabilityZone: aws.String("us-east-1a")})
		require.NoError(t, err)
		return out.Subnet.SubnetId
	}
	publicSubnet, privateSubnet := subnet("10.65.1.0/24"), subnet("10.65.2.0/24")
	igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	_, err = c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: igw.InternetGateway.InternetGatewayId, VpcId: vpcID})
	require.NoError(t, err)
	publicRT, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpcID})
	require.NoError(t, err)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: publicRT.RouteTable.RouteTableId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: igw.InternetGateway.InternetGatewayId})
	require.NoError(t, err)
	_, err = c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: publicRT.RouteTable.RouteTableId, SubnetId: publicSubnet})
	require.NoError(t, err)
	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{SubnetId: publicSubnet, AllocationId: eip.AllocationId})
	require.NoError(t, err)
	natID := nat.NatGateway.NatGatewayId
	require.NoError(t, ec2.NewNatGatewayAvailableWaiter(c, func(o *ec2.NatGatewayAvailableWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{aws.ToString(natID)}}, time.Minute))

	privateRT, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpcID})
	require.NoError(t, err)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: privateRT.RouteTable.RouteTableId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: natID})
	require.NoError(t, err)
	_, err = c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: privateRT.RouteTable.RouteTableId, SubnetId: privateSubnet})
	require.NoError(t, err)

	mainRT, err := c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{aws.ToString(vpcID)}},
		{Name: aws.String("association.main"), Values: []string{"true"}},
	}})
	require.NoError(t, err)
	require.Len(t, mainRT.RouteTables, 1)
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{RouteTableId: mainRT.RouteTables[0].RouteTableId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: natID})
	require.NoError(t, err)
	implicitSubnet := subnet("10.65.3.0/24")

	natIP := aws.ToString(eip.PublicIp)
	ecsc := ecsClient()
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String("nat-sources")})
	require.NoError(t, err)
	want := map[string]bool{"explicit": true, "implicit": true}
	for name, sn := range map[string]*string{"explicit": privateSubnet, "implicit": implicitSubnet} {
		td, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family:                  aws.String("nat-source-" + name),
			NetworkMode:             ecstypes.NetworkModeAwsvpc,
			RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
			Cpu:                     aws.String("256"),
			Memory:                  aws.String("512"),
			ContainerDefinitions: []ecstypes.ContainerDefinition{{
				Name:        aws.String("app"),
				Image:       aws.String(busyboxImage),
				StopTimeout: aws.Int32(2),
				EntryPoint:  []string{"sh", "-c"},
				Command:     []string{fmt.Sprintf("wget -T 5 -q -O- %s/%s", probe, name)},
			}},
		})
		require.NoError(t, err)
		run, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String("nat-sources"),
			TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
			LaunchType:     ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{aws.ToString(sn)}},
			},
		})
		require.NoError(t, err)
		require.Len(t, run.Tasks, 1)
		task := aws.ToString(run.Tasks[0].TaskArn)
		t.Cleanup(func() { waitForECSTaskStatus(t, ecsc, "nat-sources", task, "STOPPED") })
	}
	guard := time.After(2 * time.Minute)
	for len(want) > 0 {
		select {
		case got := <-seen:
			require.True(t, want[got[0]], "unexpected probe request %q", got[0])
			require.Equal(t, natIP, got[1], "the %s subnet's task must leave the VPC from the NAT gateway's address", got[0])
			delete(want, got[0])
		case <-guard:
			t.Fatalf("no probe request arrived from %v", want)
		}
	}
}
