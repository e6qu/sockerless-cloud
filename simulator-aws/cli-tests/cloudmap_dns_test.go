package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A public DNS namespace owns a public hosted zone into which a registered
// instance writes an A record under the service's name.
func TestCloudMap_NamespaceHostedZoneHoldsInstanceRecords(t *testing.T) {
	t.Parallel()
	var op struct{ OperationId string }
	parseJSON(t, runCLI(t, awsCLI("servicediscovery", "create-public-dns-namespace",
		"--name", "cli-zone.example", "--output", "json")), &op)
	var got struct {
		Operation struct{ Targets map[string]string }
	}
	parseJSON(t, runCLI(t, awsCLI("servicediscovery", "get-operation", "--operation-id", op.OperationId, "--output", "json")), &got)
	nsID := got.Operation.Targets["NAMESPACE"]
	require.NotEmpty(t, nsID)

	var ns struct {
		Namespace struct {
			Properties struct{ DnsProperties struct{ HostedZoneId string } }
		}
	}
	parseJSON(t, runCLI(t, awsCLI("servicediscovery", "get-namespace", "--id", nsID, "--output", "json")), &ns)
	zoneID := ns.Namespace.Properties.DnsProperties.HostedZoneId
	require.NotEmpty(t, zoneID)

	var zone struct {
		HostedZone struct {
			Name          string
			Config        struct{ PrivateZone bool }
			LinkedService struct{ ServicePrincipal string }
		}
	}
	parseJSON(t, runCLI(t, awsCLI("route53", "get-hosted-zone", "--id", zoneID, "--output", "json")), &zone)
	assert.Equal(t, "cli-zone.example.", zone.HostedZone.Name)
	assert.False(t, zone.HostedZone.Config.PrivateZone)
	assert.Equal(t, "servicediscovery.amazonaws.com", zone.HostedZone.LinkedService.ServicePrincipal)

	var svc struct{ Service struct{ Id string } }
	parseJSON(t, runCLI(t, awsCLI("servicediscovery", "create-service", "--name", "app", "--namespace-id", nsID,
		"--dns-config", "RoutingPolicy=WEIGHTED,DnsRecords=[{Type=A,TTL=20}]", "--output", "json")), &svc)
	parseJSON(t, runCLI(t, awsCLI("servicediscovery", "register-instance", "--service-id", svc.Service.Id,
		"--instance-id", "app-1", "--attributes", "AWS_INSTANCE_IPV4=203.0.113.9", "--output", "json")), &op)
	t.Cleanup(func() {
		runCLI(t, awsCLI("servicediscovery", "deregister-instance", "--service-id", svc.Service.Id, "--instance-id", "app-1"))
		runCLI(t, awsCLI("servicediscovery", "delete-service", "--id", svc.Service.Id))
		runCLI(t, awsCLI("servicediscovery", "delete-namespace", "--id", nsID))
	})

	var sets struct {
		ResourceRecordSets []struct {
			Name, Type, SetIdentifier string
			Weight                    int64
			TTL                       int64
			ResourceRecords           []struct{ Value string }
		}
	}
	parseJSON(t, runCLI(t, awsCLI("route53", "list-resource-record-sets", "--hosted-zone-id", zoneID,
		"--query", "{ResourceRecordSets: ResourceRecordSets[?Type=='A']}", "--output", "json")), &sets)
	require.Len(t, sets.ResourceRecordSets, 1)
	rr := sets.ResourceRecordSets[0]
	assert.Equal(t, "app.cli-zone.example.", rr.Name)
	assert.Equal(t, "app-1", rr.SetIdentifier)
	assert.Equal(t, int64(1), rr.Weight)
	assert.Equal(t, int64(20), rr.TTL)
	require.Len(t, rr.ResourceRecords, 1)
	assert.Equal(t, "203.0.113.9", rr.ResourceRecords[0].Value)
}
