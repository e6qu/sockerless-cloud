package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_HTTPSListenerTerminatesTLS proves an Application Load Balancer
// HTTPS listener terminates the listener's ACM certificate (real TLS handshake
// on the wire) and forwards the decrypted request over plain HTTP to the
// target — the path a real HTTPS-fronted ALB takes. A client connects over TLS
// (verifying the handshake completes against the cert), and the target
// receives a plain-HTTP request, proving termination + forwarding.
func TestELBv2_HTTPSListenerTerminatesTLS(t *testing.T) {
	elb := elbv2Client()
	acmC := acmClient()
	r53C := r53Client()
	ec2c := ec2Client()

	const domain = "api.elb-tls.test"

	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.180.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.180.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(sn.Subnet.SubnetId)

	// Plain-HTTP backend target. Real ALB HTTPS listeners front HTTP targets.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		// Echo that the request reached the target over plain HTTP (r.TLS is nil
		// for a plain-HTTP request), proving the listener terminated TLS.
		_, _ = w.Write([]byte("https-terminated"))
	}))
	defer target.Close()
	parsed, err := url.Parse(target.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	// Issue a DNS-validated cert and drive it to ISSUED so its PEM + key are
	// available for the listener to load.
	reqOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String(domain),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	certArn := aws.ToString(reqOut.CertificateArn)

	desc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
	require.NoError(t, err)
	var records []acmtypes.ResourceRecord
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord)
		records = append(records, *dvo.ResourceRecord)
	}

	zoneOut, err := r53C.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("elb-tls.test."),
		CallerReference: aws.String("elb-tls-" + certArn[len(certArn)-8:]),
	})
	require.NoError(t, err)
	zoneID := strings.TrimPrefix(aws.ToString(zoneOut.HostedZone.Id), "/hostedzone/")

	var changes []r53types.Change
	for _, rr := range records {
		changes = append(changes, r53types.Change{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name:            rr.Name,
				Type:            r53types.RRTypeCname,
				TTL:             aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{{Value: rr.Value}},
			},
		})
	}
	_, err = r53C.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch:  &r53types.ChangeBatch{Changes: changes},
	})
	require.NoError(t, err)

	waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
	defer waitCancel()
	var status acmtypes.CertificateStatus
	for i := 0; i < 60; i++ {
		d, err := acmC.DescribeCertificate(waitCtx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
		require.NoError(t, err)
		status = d.Certificate.Status
		if status == acmtypes.CertificateStatusIssued {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equal(t, acmtypes.CertificateStatusIssued, status, "ACM cert must reach ISSUED before listener creation")

	// Create an Application Load Balancer (internet-facing) — internet-facing so
	// the sim records the AWS-shaped DNS name a real client resolves.
	alb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String("alb-tls"), Type: elbtypes.LoadBalancerTypeEnumApplication, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(alb.LoadBalancers[0].LoadBalancerArn)
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	// HTTP target group pointing at the plain-HTTP backend.
	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("alb-tls-tg"), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(int32(targetPort)),
		VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
		HealthCheckProtocol: elbtypes.ProtocolEnumHttp, HealthCheckPath: aws.String("/healthz"),
		HealthCheckTimeoutSeconds: aws.Int32(5),
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))}},
	})
	require.NoError(t, err)

	// HTTPS listener on a high port (8443) carrying the issued ACM cert.
	const listenerPort = 8443
	_, err = elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttps,
		Port:            aws.Int32(listenerPort),
		Certificates:    []elbtypes.Certificate{{CertificateArn: aws.String(certArn)}},
		DefaultActions:  []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
	})
	require.NoError(t, err)

	// Resolve the ALB's AWS-shaped DNS name to the TLS proxy host (the hosts
	// entry the sim injects into a workload container), then connect on the
	// listener port over TLS.
	lbDesc, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dnsName := aws.ToString(lbDesc.LoadBalancers[0].DNSName)
	require.NotContains(t, dnsName, ":", "DNSName must be a hostname")
	endpoint := net.JoinHostPort(resolveNLBHostname(dnsName), strconv.Itoa(listenerPort))

	// An HTTPS client that skips verification (the cert is self-signed in the
	// sim, which has no real CA) but proves a real TLS handshake completes
	// against the listener's ACM certificate before any HTTP bytes flow.
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	var body string
	for i := 0; i < 50; i++ {
		resp, err := client.Get("https://" + endpoint + "/")
		if err == nil {
			buf := make([]byte, 64)
			n, _ := resp.Body.Read(buf)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body = string(buf[:n])
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	assert.Equal(t, "https-terminated", body, "HTTPS listener must terminate TLS and forward plain HTTP to the target")
}

// TestELBv2_NLBTLSListenerTerminatesTLS proves a Network Load Balancer TLS
// listener terminates the listener's ACM certificate and forwards the decrypted
// byte stream to a raw-TCP target — the path a TLS-fronted NLB takes. The
// target is a raw-TCP responder (not HTTP); the client connects over TLS, the
// handshake completes against the cert, and the decrypted line round-trips to
// the target.
func TestELBv2_NLBTLSListenerTerminatesTLS(t *testing.T) {
	elb := elbv2Client()
	acmC := acmClient()
	r53C := r53Client()
	ec2c := ec2Client()

	const domain = "nlb.elb-tls.test"

	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.181.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.181.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(sn.Subnet.SubnetId)

	// A raw-TCP echo backend (NOT HTTP): reads a line, writes "tls-echo:<line>".
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(append([]byte("tls-echo:"), buf[:n]...)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	backendHost, backendPortText, err := net.SplitHostPort(backend.Addr().String())
	require.NoError(t, err)
	backendPort, err := strconv.Atoi(backendPortText)
	require.NoError(t, err)

	// Issue + drive to ISSUED the TLS listener's certificate.
	reqOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String(domain),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	certArn := aws.ToString(reqOut.CertificateArn)
	desc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
	require.NoError(t, err)
	var records []acmtypes.ResourceRecord
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord)
		records = append(records, *dvo.ResourceRecord)
	}
	zoneOut, err := r53C.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("nlb-elb-tls.test."),
		CallerReference: aws.String("nlb-elb-tls-" + certArn[len(certArn)-8:]),
	})
	require.NoError(t, err)
	zoneID := strings.TrimPrefix(aws.ToString(zoneOut.HostedZone.Id), "/hostedzone/")
	var changes []r53types.Change
	for _, rr := range records {
		changes = append(changes, r53types.Change{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name:            rr.Name,
				Type:            r53types.RRTypeCname,
				TTL:             aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{{Value: rr.Value}},
			},
		})
	}
	_, err = r53C.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch:  &r53types.ChangeBatch{Changes: changes},
	})
	require.NoError(t, err)
	waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
	defer waitCancel()
	var status acmtypes.CertificateStatus
	for i := 0; i < 60; i++ {
		d, err := acmC.DescribeCertificate(waitCtx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
		require.NoError(t, err)
		status = d.Certificate.Status
		if status == acmtypes.CertificateStatusIssued {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equal(t, acmtypes.CertificateStatusIssued, status)

	nlb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String("nlb-tls"), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("nlb-tls-tg"), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(int32(backendPort)),
		VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
		HealthCheckProtocol: elbtypes.ProtocolEnumTcp, HealthCheckTimeoutSeconds: aws.Int32(5),
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{{Id: aws.String(backendHost), Port: aws.Int32(int32(backendPort))}},
	})
	require.NoError(t, err)

	// A TLS listener on the NLB (port 8444). A real NLB TLS listener
	// terminates the cert at the LB and forwards decrypted TCP to the target.
	const listenerPort = 8444
	_, err = elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumTls,
		Port:            aws.Int32(listenerPort),
		Certificates:    []elbtypes.Certificate{{CertificateArn: aws.String(certArn)}},
		DefaultActions:  []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
	})
	require.NoError(t, err)

	lbDesc, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dnsName := aws.ToString(lbDesc.LoadBalancers[0].DNSName)
	endpoint := net.JoinHostPort(resolveNLBHostname(dnsName), strconv.Itoa(listenerPort))

	// Dial over TLS — the handshake must complete against the listener's cert
	// (proving termination), then the decrypted byte stream round-trips to the
	// raw-TCP target.
	tlsDialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
	var conn *tls.Conn
	for i := 0; i < 50; i++ {
		c, err := tlsDialer.DialContext(ctx, "tcp", endpoint)
		if err == nil {
			conn = c.(*tls.Conn)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NotNil(t, conn, "TLS handshake to NLB TLS listener must complete")
	defer conn.Close()

	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Write([]byte("hello-nlb-tls\n"))
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "tls-echo:hello-nlb-tls\n", string(buf[:n]), "decrypted byte stream must round-trip through the NLB TLS listener to the target")
}
