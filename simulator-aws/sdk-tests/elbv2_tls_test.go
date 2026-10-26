package aws_sdk_test

import (
	"crypto/tls"
	"io"
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
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_HTTPSListenerTerminatesTLS expects an HTTPS listener to complete a
// TLS handshake with its ACM certificate and forward the decrypted request
// over plain HTTP to the target.
func TestELBv2_HTTPSListenerTerminatesTLS(t *testing.T) {
	t.Parallel()
	lbName := uniquename.New("alb-tls")
	tgName := uniquename.New("alb-tls-tg")
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
	sn2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.180.2.0/24"), AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	subnet2ID := aws.ToString(sn2.Subnet.SubnetId)

	// An HTTPS listener fronts plain-HTTP targets.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		// The target sees a plain-HTTP request, so the listener terminated TLS.
		_, _ = w.Write([]byte("https-terminated"))
	}))
	defer target.Close()
	parsed, err := url.Parse(target.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	// Drive a DNS-validated certificate to ISSUED so the listener can load its PEM
	// and key.
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

	waitForACMCertificateIssued(t, acmC, certArn)

	// An internet-facing load balancer gets the AWS-shaped DNS name a client
	// resolves.
	alb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(lbName), Type: elbtypes.LoadBalancerTypeEnumApplication, Subnets: []string{subnetID, subnet2ID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(alb.LoadBalancers[0].LoadBalancerArn)
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(tgName), Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(int32(targetPort)),
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

	const listenerPort = 8443
	listenerOut, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttps,
		Port:            aws.Int32(listenerPort),
		Certificates:    []elbtypes.Certificate{{CertificateArn: aws.String(certArn)}},
		DefaultActions:  []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
	})
	require.NoError(t, err)
	listenerArn := aws.ToString(listenerOut.Listeners[0].ListenerArn)

	lbDesc, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dnsName := aws.ToString(lbDesc.LoadBalancers[0].DNSName)
	require.NotContains(t, dnsName, ":", "DNSName must be a hostname")
	lbHost, _ := dnsResolveA(t, dnsName)
	endpoint := net.JoinHostPort(lbHost, strconv.Itoa(listenerPort))

	// The simulator's ACM signs with a CA no system trusts, so skip verification;
	// the handshake still completes against the listener's certificate.
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// The listener forwards only to a target in service.
	waitForELBv2TargetHealth(t, tgArn,
		elbtypes.TargetDescription{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))},
		elbtypes.TargetHealthStateEnumHealthy)

	resp, err := client.Get("https://" + endpoint + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "https-terminated", string(body), "HTTPS listener must terminate TLS and forward plain HTTP to the target")

	// ACM lists the load balancer as the certificate's user and will not delete a
	// certificate in use.
	inUse, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certArn)})
	require.NoError(t, err)
	assert.Equal(t, []string{lbArn}, inUse.Certificate.InUseBy)
	_, err = acmC.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: aws.String(certArn)})
	var refused *acmtypes.ResourceInUseException
	require.ErrorAs(t, err, &refused)

	// A certificate added to the running listener is presented to a client naming
	// its domain.
	const second = "www.elb-tls.test"
	secondOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String(second), ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	secondArn := aws.ToString(secondOut.CertificateArn)
	secondDesc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(secondArn)})
	require.NoError(t, err)
	var secondChanges []r53types.Change
	for _, dvo := range secondDesc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord)
		secondChanges = append(secondChanges, r53types.Change{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name: dvo.ResourceRecord.Name, Type: r53types.RRTypeCname, TTL: aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{{Value: dvo.ResourceRecord.Value}},
			},
		})
	}
	_, err = r53C.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID), ChangeBatch: &r53types.ChangeBatch{Changes: secondChanges},
	})
	require.NoError(t, err)
	waitForACMCertificateIssued(t, acmC, secondArn)
	_, err = elb.AddListenerCertificates(ctx, &elbv2.AddListenerCertificatesInput{
		ListenerArn: aws.String(listenerArn), Certificates: []elbtypes.Certificate{{CertificateArn: aws.String(secondArn)}},
	})
	require.NoError(t, err)
	conn, err := tls.Dial("tcp", endpoint, &tls.Config{ServerName: second, InsecureSkipVerify: true})
	require.NoError(t, err)
	presented := conn.ConnectionState().PeerCertificates[0].DNSNames
	require.NoError(t, conn.Close())
	assert.Contains(t, presented, second, "the running listener presents the certificate added to it")
}

// TestELBv2_NLBTLSListenerTerminatesTLS expects a Network Load Balancer TLS
// listener to terminate with its ACM certificate and forward the decrypted
// byte stream to a raw-TCP target.
func TestELBv2_NLBTLSListenerTerminatesTLS(t *testing.T) {
	t.Parallel()
	lbName := uniquename.New("nlb-tls")
	tgName := uniquename.New("nlb-tls-tg")
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

	// A raw-TCP echo backend: reads a line, writes "tls-echo:<line>".
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
	waitForACMCertificateIssued(t, acmC, certArn)

	nlb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(lbName), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(tgName), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(int32(backendPort)),
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
	lbHost, _ := dnsResolveA(t, dnsName)
	endpoint := net.JoinHostPort(lbHost, strconv.Itoa(listenerPort))

	// The listener forwards only to a target in service.
	waitForELBv2TargetHealth(t, tgArn,
		elbtypes.TargetDescription{Id: aws.String(backendHost), Port: aws.Int32(int32(backendPort))},
		elbtypes.TargetHealthStateEnumHealthy)

	// The handshake completes against the listener's certificate, then the
	// decrypted stream round-trips to the target.
	tlsDialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
	c, err := tlsDialer.DialContext(ctx, "tcp", endpoint)
	require.NoError(t, err, "TLS handshake to NLB TLS listener must complete")
	conn := c.(*tls.Conn)
	defer conn.Close()

	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Write([]byte("hello-nlb-tls\n"))
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "tls-echo:hello-nlb-tls\n", string(buf[:n]), "decrypted byte stream must round-trip through the NLB TLS listener to the target")
}
