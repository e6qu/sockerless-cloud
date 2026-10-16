package aws_sdk_test

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

func cmNamespaceZoneID(t *testing.T, cm *servicediscovery.Client, nsID string) string {
	t.Helper()
	ns, err := cm.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(nsID)})
	require.NoError(t, err)
	require.NotNil(t, ns.Namespace.Properties)
	require.NotNil(t, ns.Namespace.Properties.DnsProperties)
	zoneID := aws.ToString(ns.Namespace.Properties.DnsProperties.HostedZoneId)
	require.NotEmpty(t, zoneID)
	return zoneID
}

func cmRegister(t *testing.T, cm *servicediscovery.Client, serviceID, instanceID string, attrs map[string]string) {
	t.Helper()
	out, err := cm.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId: aws.String(serviceID), InstanceId: aws.String(instanceID), Attributes: attrs,
	})
	require.NoError(t, err)
	awaitCloudMapOperation(t, cm, out.OperationId)
}

func cmDeregister(t *testing.T, cm *servicediscovery.Client, serviceID, instanceID string) {
	t.Helper()
	out, err := cm.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
		ServiceId: aws.String(serviceID), InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	awaitCloudMapOperation(t, cm, out.OperationId)
}

func zoneRecordSets(t *testing.T, r53 *route53.Client, zoneID string) []r53types.ResourceRecordSet {
	t.Helper()
	out, err := r53.ListResourceRecordSets(ctx, &route53.ListResourceRecordSetsInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)
	return out.ResourceRecordSets
}

// A private DNS namespace owns a private hosted zone, created by Cloud Map and
// associated with the namespace's VPC, into which every registered instance
// writes a multivalue record set; deleting the namespace deletes the zone.
func TestCloudMap_PrivateNamespaceOwnsItsHostedZone(t *testing.T) {
	t.Parallel()
	cm, r53 := cmClient(), r53Client()
	vpc, err := ec2Client().CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.97.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	created, err := cm.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("zone-owner.internal"), Vpc: aws.String(vpcID),
		Properties: &sdtypes.PrivateDnsNamespaceProperties{DnsProperties: &sdtypes.PrivateDnsPropertiesMutable{
			SOA: &sdtypes.SOA{TTL: aws.Int64(15)},
		}},
	})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, cm, created.OperationId)
	zoneID := cmNamespaceZoneID(t, cm, nsID)

	zone, err := r53.GetHostedZone(ctx, &route53.GetHostedZoneInput{Id: aws.String(zoneID)})
	require.NoError(t, err)
	assert.Equal(t, "zone-owner.internal.", aws.ToString(zone.HostedZone.Name))
	require.NotNil(t, zone.HostedZone.Config)
	assert.True(t, zone.HostedZone.Config.PrivateZone)
	require.NotNil(t, zone.HostedZone.LinkedService, "Cloud Map owns the zone")
	assert.Equal(t, "servicediscovery.amazonaws.com", aws.ToString(zone.HostedZone.LinkedService.ServicePrincipal))
	require.Len(t, zone.VPCs, 1)
	assert.Equal(t, vpcID, aws.ToString(zone.VPCs[0].VPCId))

	byVPC, err := r53.ListHostedZonesByVPC(ctx, &route53.ListHostedZonesByVPCInput{
		VPCId: aws.String(vpcID), VPCRegion: zone.VPCs[0].VPCRegion,
	})
	require.NoError(t, err)
	require.Len(t, byVPC.HostedZoneSummaries, 1)
	assert.Equal(t, "zone-owner.internal.", aws.ToString(byVPC.HostedZoneSummaries[0].Name))

	for _, rr := range zoneRecordSets(t, r53, zoneID) {
		if rr.Type == r53types.RRTypeSoa {
			assert.Equal(t, int64(15), aws.ToInt64(rr.TTL), "the namespace's SOA TTL")
		}
	}

	svc, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("api"), NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			RoutingPolicy: sdtypes.RoutingPolicyMultivalue,
			DnsRecords:    []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}},
		},
	})
	require.NoError(t, err)
	svcID := aws.ToString(svc.Service.Id)
	cmRegister(t, cm, svcID, "i-one", map[string]string{"AWS_INSTANCE_IPV4": "10.97.0.11"})
	cmRegister(t, cm, svcID, "i-two", map[string]string{"AWS_INSTANCE_IPV4": "10.97.0.12"})

	var api []r53types.ResourceRecordSet
	for _, rr := range zoneRecordSets(t, r53, zoneID) {
		if aws.ToString(rr.Name) == "api.zone-owner.internal." {
			api = append(api, rr)
		}
	}
	require.Len(t, api, 2, "one record set per instance")
	sort.Slice(api, func(i, j int) bool { return aws.ToString(api[i].SetIdentifier) < aws.ToString(api[j].SetIdentifier) })
	for i, want := range []struct{ id, ip string }{{"i-one", "10.97.0.11"}, {"i-two", "10.97.0.12"}} {
		assert.Equal(t, r53types.RRTypeA, api[i].Type)
		assert.Equal(t, want.id, aws.ToString(api[i].SetIdentifier))
		assert.True(t, aws.ToBool(api[i].MultiValueAnswer))
		assert.Equal(t, int64(10), aws.ToInt64(api[i].TTL))
		require.Len(t, api[i].ResourceRecords, 1)
		assert.Equal(t, want.ip, aws.ToString(api[i].ResourceRecords[0].Value))
	}

	_, err = cm.UpdateService(ctx, &servicediscovery.UpdateServiceInput{
		Id: aws.String(svcID),
		Service: &sdtypes.ServiceChange{DnsConfig: &sdtypes.DnsConfigChange{
			DnsRecords: []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(45)}},
		}},
	})
	require.NoError(t, err)
	for _, rr := range zoneRecordSets(t, r53, zoneID) {
		if aws.ToString(rr.Name) == "api.zone-owner.internal." {
			assert.Equal(t, int64(45), aws.ToInt64(rr.TTL), "UpdateService rewrites the records' TTL")
		}
	}

	cmDeregister(t, cm, svcID, "i-one")
	cmDeregister(t, cm, svcID, "i-two")
	for _, rr := range zoneRecordSets(t, r53, zoneID) {
		assert.Contains(t, []r53types.RRType{r53types.RRTypeNs, r53types.RRTypeSoa}, rr.Type, "deregistering removes the instances' records")
	}

	_, err = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	require.NoError(t, err)
	deleted, err := cm.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	require.NoError(t, err)
	awaitCloudMapOperation(t, cm, deleted.OperationId)
	_, err = r53.GetHostedZone(ctx, &route53.GetHostedZoneInput{Id: aws.String(zoneID)})
	var noZone *r53types.NoSuchHostedZone
	require.ErrorAs(t, err, &noZone, "deleting the namespace deletes its zone")
}

// A public DNS namespace's records answer on the Route 53 resolver: a weighted
// A record per instance, and an SRV record naming each instance's own host
// name, which Cloud Map backs with an A record.
func TestCloudMap_PublicNamespaceAnswersDNS(t *testing.T) {
	t.Parallel()
	cm := cmClient()
	zone := fmt.Sprintf("cm-pub-%s.example", time.Now().Format("150405000000"))
	created, err := cm.CreatePublicDnsNamespace(ctx, &servicediscovery.CreatePublicDnsNamespaceInput{Name: aws.String(zone)})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, cm, created.OperationId)

	web, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("web"), NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			RoutingPolicy: sdtypes.RoutingPolicyWeighted,
			DnsRecords:    []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(30)}},
		},
	})
	require.NoError(t, err)
	srv, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("_db._tcp"), NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			RoutingPolicy: sdtypes.RoutingPolicyMultivalue,
			DnsRecords:    []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeSrv, TTL: aws.Int64(30)}},
		},
	})
	require.NoError(t, err)
	webID, srvID := aws.ToString(web.Service.Id), aws.ToString(srv.Service.Id)
	cmRegister(t, cm, webID, "w1", map[string]string{"AWS_INSTANCE_IPV4": "198.51.100.7"})
	cmRegister(t, cm, srvID, "db1", map[string]string{"AWS_INSTANCE_IPV4": "198.51.100.20", "AWS_INSTANCE_PORT": "5432"})
	t.Cleanup(func() {
		cmDeregister(t, cm, webID, "w1")
		cmDeregister(t, cm, srvID, "db1")
		_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(webID)})
		_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(srvID)})
		_, _ = cm.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	})

	a := dnsQuery(t, "web."+zone, dnsmessage.TypeA)
	require.Equal(t, dnsmessage.RCodeSuccess, a.RCode)
	require.Len(t, a.Answers, 1)
	body, ok := a.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok, "expected AResource, got %T", a.Answers[0].Body)
	assert.Equal(t, "198.51.100.7", net.IP(body.A[:]).String())

	srvAnswer := dnsQuery(t, "_db._tcp."+zone, dnsmessage.TypeSRV)
	require.Equal(t, dnsmessage.RCodeSuccess, srvAnswer.RCode)
	require.Len(t, srvAnswer.Answers, 1)
	record, ok := srvAnswer.Answers[0].Body.(*dnsmessage.SRVResource)
	require.True(t, ok, "expected SRVResource, got %T", srvAnswer.Answers[0].Body)
	assert.Equal(t, uint16(1), record.Priority)
	assert.Equal(t, uint16(1), record.Weight)
	assert.Equal(t, uint16(5432), record.Port)
	target := "db1._db._tcp." + zone + "."
	assert.Equal(t, target, strings.ToLower(record.Target.String()))

	host := dnsQuery(t, target, dnsmessage.TypeA)
	require.Len(t, host.Answers, 1, "Cloud Map backs the SRV target with an A record")
	hostA, ok := host.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok)
	assert.Equal(t, "198.51.100.20", net.IP(hostA.A[:]).String())
}

// An instance of a service with a custom health check keeps answering for 30
// seconds after it is reported UNHEALTHY, then drops out of the multivalue
// answer while a healthy instance remains; its record set stays in the zone.
func TestCloudMap_CustomHealthStopsRoutingAfterThirtySeconds(t *testing.T) {
	t.Parallel()
	cm, r53 := cmClient(), r53Client()
	zone := fmt.Sprintf("cm-health-%s.example", time.Now().Format("150405000000"))
	created, err := cm.CreatePublicDnsNamespace(ctx, &servicediscovery.CreatePublicDnsNamespaceInput{Name: aws.String(zone)})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, cm, created.OperationId)
	svc, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("api"), NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			RoutingPolicy: sdtypes.RoutingPolicyMultivalue,
			DnsRecords:    []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}},
		},
		HealthCheckCustomConfig: &sdtypes.HealthCheckCustomConfig{},
	})
	require.NoError(t, err)
	svcID := aws.ToString(svc.Service.Id)
	cmRegister(t, cm, svcID, "a1", map[string]string{"AWS_INSTANCE_IPV4": "198.51.100.31"})
	cmRegister(t, cm, svcID, "a2", map[string]string{"AWS_INSTANCE_IPV4": "198.51.100.32"})
	t.Cleanup(func() {
		cmDeregister(t, cm, svcID, "a1")
		cmDeregister(t, cm, svcID, "a2")
		_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
		_, _ = cm.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	})
	answered := func() []string {
		msg := dnsQuery(t, "api."+zone, dnsmessage.TypeA)
		require.Equal(t, dnsmessage.RCodeSuccess, msg.RCode)
		var ips []string
		for _, rr := range msg.Answers {
			if a, ok := rr.Body.(*dnsmessage.AResource); ok {
				ips = append(ips, net.IP(a.A[:]).String())
			}
		}
		sort.Strings(ips)
		return ips
	}
	require.Equal(t, []string{"198.51.100.31", "198.51.100.32"}, answered())

	_, err = cm.UpdateInstanceCustomHealthStatus(ctx, &servicediscovery.UpdateInstanceCustomHealthStatusInput{
		ServiceId: aws.String(svcID), InstanceId: aws.String("a1"), Status: sdtypes.CustomHealthStatusUnhealthy,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"198.51.100.31", "198.51.100.32"}, answered(), "the status waits 30 seconds")
	awaitCustomHealth(t, cm, aws.String(svcID), "a1", sdtypes.HealthStatusUnhealthy)
	assert.Equal(t, []string{"198.51.100.32"}, answered())
	assert.Len(t, zoneRecordSets(t, r53, cmNamespaceZoneID(t, cm, nsID)), 4, "NS, SOA and both instances' record sets stay in the zone")

	_, err = cm.UpdateInstanceCustomHealthStatus(ctx, &servicediscovery.UpdateInstanceCustomHealthStatusInput{
		ServiceId: aws.String(svcID), InstanceId: aws.String("a2"), Status: sdtypes.CustomHealthStatusUnhealthy,
	})
	require.NoError(t, err)
	awaitCustomHealth(t, cm, aws.String(svcID), "a2", sdtypes.HealthStatusUnhealthy)
	assert.Equal(t, []string{"198.51.100.31", "198.51.100.32"}, answered(), "with every instance unhealthy, Route 53 answers them all")
}

// A service with a HealthCheckConfig gets a Route 53 health check per
// instance, linked to Cloud Map and attached to the instance's record set; an
// instance whose endpoint fails its check reports UNHEALTHY and drops out of
// the answer, and deregistering it deletes the check.
func TestCloudMap_HealthCheckConfigCreatesRoute53HealthChecks(t *testing.T) {
	t.Parallel()
	cm, r53 := cmClient(), r53Client()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	openPort := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedPort := fmt.Sprint(closed.Addr().(*net.TCPAddr).Port)
	require.NoError(t, closed.Close())

	zone := fmt.Sprintf("cm-hc-%s.example", time.Now().Format("150405000000"))
	created, err := cm.CreatePublicDnsNamespace(ctx, &servicediscovery.CreatePublicDnsNamespaceInput{Name: aws.String(zone)})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, cm, created.OperationId)
	svc, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name: aws.String("tcp"), NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			RoutingPolicy: sdtypes.RoutingPolicyMultivalue,
			DnsRecords:    []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}},
		},
		HealthCheckConfig: &sdtypes.HealthCheckConfig{Type: sdtypes.HealthCheckTypeTcp, FailureThreshold: aws.Int32(1)},
	})
	require.NoError(t, err)
	svcID, svcArn := aws.ToString(svc.Service.Id), aws.ToString(svc.Service.Arn)
	cmRegister(t, cm, svcID, "up", map[string]string{"AWS_INSTANCE_IPV4": "127.0.0.1", "AWS_INSTANCE_PORT": openPort})
	cmRegister(t, cm, svcID, "down", map[string]string{"AWS_INSTANCE_IPV4": "127.0.0.2", "AWS_INSTANCE_PORT": closedPort})
	t.Cleanup(func() {
		cmDeregister(t, cm, svcID, "up")
		_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
		_, _ = cm.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	})

	linked := func() map[string]r53types.HealthCheck {
		out := map[string]r53types.HealthCheck{}
		pager := route53.NewListHealthChecksPaginator(r53, &route53.ListHealthChecksInput{})
		for pager.HasMorePages() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			for _, hc := range page.HealthChecks {
				if hc.LinkedService != nil && aws.ToString(hc.LinkedService.Description) == svcArn {
					out[aws.ToString(hc.HealthCheckConfig.IPAddress)] = hc
				}
			}
		}
		return out
	}
	checks := linked()
	require.Len(t, checks, 2)
	up := checks["127.0.0.1"]
	assert.Equal(t, "servicediscovery.amazonaws.com", aws.ToString(up.LinkedService.ServicePrincipal))
	assert.Equal(t, r53types.HealthCheckTypeTcp, up.HealthCheckConfig.Type)
	assert.Equal(t, openPort, fmt.Sprint(aws.ToInt32(up.HealthCheckConfig.Port)))

	byInstance := map[string]string{}
	for _, rr := range zoneRecordSets(t, r53, cmNamespaceZoneID(t, cm, nsID)) {
		if rr.SetIdentifier != nil {
			byInstance[aws.ToString(rr.SetIdentifier)] = aws.ToString(rr.HealthCheckId)
		}
	}
	assert.Equal(t, aws.ToString(up.Id), byInstance["up"], "the instance's record set carries its health check")

	awaitCustomHealth(t, cm, aws.String(svcID), "down", sdtypes.HealthStatusUnhealthy)
	health, err := cm.GetInstancesHealthStatus(ctx, &servicediscovery.GetInstancesHealthStatusInput{ServiceId: aws.String(svcID)})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.HealthStatusHealthy, health.Status["up"])
	msg := dnsQuery(t, "tcp."+zone, dnsmessage.TypeA)
	require.Len(t, msg.Answers, 1)
	a, ok := msg.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", net.IP(a.A[:]).String())

	cmDeregister(t, cm, svcID, "down")
	assert.Len(t, linked(), 1, "deregistering an instance deletes its health check")
}
