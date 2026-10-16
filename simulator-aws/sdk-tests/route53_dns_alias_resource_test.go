package aws_sdk_test

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// dnsResolveA asks the simulator's resolver for name's address, as a client
// resolving a load balancer's DNS name does, and returns it with its TTL.
func dnsResolveA(t *testing.T, name string) (string, uint32) {
	t.Helper()
	resp := dnsQuery(t, name, dnsmessage.TypeA)
	require.Equal(t, dnsmessage.RCodeSuccess, resp.RCode, "resolve %s", name)
	require.Len(t, resp.Answers, 1, "resolve %s", name)
	a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok, "expected AResource for %s, got %T", name, resp.Answers[0].Body)
	return net.IP(a.A[:]).String(), resp.Answers[0].Header.TTL
}

// A Route 53 alias to a Network Load Balancer answers the load balancer's
// address with the load balancer's 60-second TTL, and a client that resolves
// the alias reaches the load balancer's listener at it.
func TestRoute53DNSAliasToALoadBalancer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	elb, r53 := elbv2Client(), r53Client()

	vpc, err := ec2Client().CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.161.0.0/16")})
	require.NoError(t, err)
	sn, err := ec2Client().CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.161.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	created, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniqueName("nlb-alias")), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{aws.ToString(sn.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	lb := created.LoadBalancers[0]
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(context.Background(), &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: lb.LoadBalancerArn})
	})
	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(uniqueName("nlb-alias-tg")), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(80),
		VpcId: vpc.Vpc.VpcId, TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	port := elbv2CreateListenerOnFreePort(t, func(port int32) error {
		_, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
			LoadBalancerArn: lb.LoadBalancerArn, Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(port),
			DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: tg.TargetGroups[0].TargetGroupArn}},
		})
		return err
	})

	lbAddress, lbTTL := dnsResolveA(t, aws.ToString(lb.DNSName))
	assert.Equal(t, uint32(60), lbTTL)

	zone := fmt.Sprintf("dns-elb-%s.example", time.Now().Format("150405.000000"))
	zoneID := createZoneForDNS(t, ctx, r53, zone)
	name := "www." + zone
	alias := func(action r53types.ChangeAction) error {
		_, err := r53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID),
			ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
				Action: action,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name: aws.String(name), Type: r53types.RRTypeA,
					AliasTarget: &r53types.AliasTarget{
						HostedZoneId: lb.CanonicalHostedZoneId, DNSName: lb.DNSName,
					},
				},
			}}},
		})
		return err
	}
	require.NoError(t, alias(r53types.ChangeActionCreate))
	t.Cleanup(func() {
		_ = alias(r53types.ChangeActionDelete)
		_, _ = r53.DeleteHostedZone(context.Background(), &route53.DeleteHostedZoneInput{Id: aws.String(zoneID)})
	})

	address, ttl := dnsResolveA(t, name)
	assert.Equal(t, lbAddress, address)
	assert.Equal(t, uint32(60), ttl)

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, strconv.Itoa(int(port))), 5*time.Second)
	require.NoError(t, err, "reach the listener at the alias's address")
	require.NoError(t, conn.Close())
}
