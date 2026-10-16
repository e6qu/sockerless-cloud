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
