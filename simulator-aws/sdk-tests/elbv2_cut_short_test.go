package aws_sdk_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_AbortsAResponseTheTargetCutsShort covers a target that closes its
// connection in the middle of a chunked body: the Application Load Balancer
// has already relayed the target's status and headers, so it ends the client's
// response where the target stopped rather than appending an error to it.
func TestELBv2_AbortsAResponseTheTargetCutsShort(t *testing.T) {
	ec2c := ec2Client()
	elb := elbv2Client()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\npartial\r\n")
		_ = buf.Flush()
	}))
	defer targetServer.Close()
	targetURL, err := url.Parse(targetServer.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId
	subnet1, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.91.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnet2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.91.2.0/24"), AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("sdk-cut-short-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: []string{*subnet1.Subnet.SubnetId, *subnet2.Subnet.SubnetId},
	})
	require.NoError(t, err)
	lbArn := *lbOut.LoadBalancers[0].LoadBalancerArn
	tgOut, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:                       aws.String("sdk-cut-short-tg"),
		Protocol:                   elbtypes.ProtocolEnumHttp,
		Port:                       aws.Int32(80),
		VpcId:                      aws.String(vpcID),
		TargetType:                 elbtypes.TargetTypeEnumIp,
		HealthCheckPath:            aws.String("/healthz"),
		HealthCheckIntervalSeconds: aws.Int32(5),
		HealthCheckTimeoutSeconds:  aws.Int32(2),
	})
	require.NoError(t, err)
	tgArn := *tgOut.TargetGroups[0].TargetGroupArn
	target := elbtypes.TargetDescription{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))}
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn), Targets: []elbtypes.TargetDescription{target},
	})
	require.NoError(t, err)
	listenerOut, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttp,
		Port:            aws.Int32(80),
		DefaultActions: []elbtypes.Action{{
			Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = elb.DeleteListener(ctx, &elbv2.DeleteListenerInput{ListenerArn: listenerOut.Listeners[0].ListenerArn})
		_, _ = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(tgArn)})
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
		_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet2.Subnet.SubnetId})
		_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet1.Subnet.SubnetId})
		_, _ = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
	})
	waitForELBv2TargetHealth(t, tgArn, target, elbtypes.TargetHealthStateEnumHealthy)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/work", nil)
	require.NoError(t, err)
	req.Host = *lbOut.LoadBalancers[0].DNSName
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the target's status reached the client")
	body, err := io.ReadAll(resp.Body)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the response ends where the target stopped")
	assert.Equal(t, "partial", string(body), "nothing follows what the target sent")
}
