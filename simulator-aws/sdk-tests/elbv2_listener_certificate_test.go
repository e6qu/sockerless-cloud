package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_HTTPSListenerCertificateRoundTrip verifies an HTTPS listener
// created with an ACM certificate reports that certificate from
// DescribeListeners. The cert arrives as the structured
// Certificates.member.N.CertificateArn parameter; reading it as a flat scalar
// dropped it, so the standard "read cert off the listener" pattern got nothing.
func TestELBv2_HTTPSListenerCertificateRoundTrip(t *testing.T) {
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.74.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sub, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.74.1.0/24"),
	})
	require.NoError(t, err)

	certArn := importELBv2Certificate(t, "listener.example.test")
	listenerPort := availableELBv2ListenerPort(t)

	c := elbv2Client()
	lb, err := c.CreateLoadBalancer(ctx, &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name:    aws.String("https-listener-lb"),
		Type:    elbv2types.LoadBalancerTypeEnumApplication,
		Subnets: []string{aws.ToString(sub.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)

	tg, err := c.CreateTargetGroup(ctx, &elasticloadbalancingv2.CreateTargetGroupInput{
		Name: aws.String("https-listener-tg"), Protocol: elbv2types.ProtocolEnumHttp,
		Port: aws.Int32(80), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)

	_, err = c.CreateListener(ctx, &elasticloadbalancingv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbv2types.ProtocolEnumHttps,
		Port:            aws.Int32(listenerPort),
		Certificates:    []elbv2types.Certificate{{CertificateArn: aws.String(certArn)}},
		DefaultActions: []elbv2types.Action{{
			Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)

	desc, err := c.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(lbArn),
	})
	require.NoError(t, err)

	var https *elbv2types.Listener
	for i := range desc.Listeners {
		if aws.ToInt32(desc.Listeners[i].Port) == listenerPort {
			https = &desc.Listeners[i]
		}
	}
	require.NotNil(t, https, "HTTPS listener present")
	require.Len(t, https.Certificates, 1, "Certificates must round-trip on the HTTPS listener")
	assert.Equal(t, certArn, aws.ToString(https.Certificates[0].CertificateArn))
}
