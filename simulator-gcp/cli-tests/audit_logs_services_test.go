package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliAuditEntry is the part of a Cloud Audit Logs entry gcloud logging read
// prints that these tests read.
type cliAuditEntry struct {
	LogName   string `json:"logName"`
	Operation struct {
		ID       string `json:"id"`
		Producer string `json:"producer"`
		First    bool   `json:"first"`
		Last     bool   `json:"last"`
	} `json:"operation"`
	Resource struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	ProtoPayload struct {
		ServiceName       string `json:"serviceName"`
		MethodName        string `json:"methodName"`
		ResourceName      string `json:"resourceName"`
		AuthorizationInfo []struct {
			Permission string `json:"permission"`
			Granted    bool   `json:"granted"`
		} `json:"authorizationInfo"`
	} `json:"protoPayload"`
}

// readAuditEntry reads, with gcloud logging read, the one Admin Activity
// entry for a method on a resource.
func readAuditEntry(t *testing.T, methodName, resourceName string) cliAuditEntry {
	t.Helper()
	var entries []cliAuditEntry
	parseJSON(t, runCLI(t, gcloudCLI("logging", "read",
		`logName:"cloudaudit.googleapis.com%2Factivity" AND protoPayload.methodName="`+methodName+`" AND protoPayload.resourceName="`+resourceName+`"`,
		"--format", "json")), &entries)
	require.Len(t, entries, 1, "one Admin Activity entry for %s on %s", methodName, resourceName)
	require.NotEmpty(t, entries[0].ProtoPayload.AuthorizationInfo, "the entry records the permission the call checked")
	return entries[0]
}

// TestAuditLogsCLI_ComputeKMSIAMDNS makes a Compute Engine network, a Cloud
// KMS key ring, a service account and a Cloud DNS zone with gcloud, and reads
// each service's Admin Activity entry back with gcloud logging read.
func TestAuditLogsCLI_ComputeKMSIAMDNS(t *testing.T) {
	const (
		network = "cli-audit-net"
		ring    = "cli-audit-ring"
		account = "cli-audit-sa"
		zone    = "cli-audit-zone"
	)

	runCLI(t, gcloudCLI("compute", "networks", "create", network, "--subnet-mode=custom", "--format=json"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("compute", "networks", "delete", network, "--quiet")) })
	entry := readAuditEntry(t, "v1.compute.networks.insert", "projects/"+project+"/global/networks/"+network)
	assert.Equal(t, "projects/"+project+"/logs/cloudaudit.googleapis.com%2Factivity", entry.LogName)
	assert.Equal(t, "compute.googleapis.com", entry.ProtoPayload.ServiceName)
	assert.Equal(t, "compute.networks.create", entry.ProtoPayload.AuthorizationInfo[0].Permission)
	assert.Equal(t, "compute.googleapis.com", entry.Operation.Producer)
	assert.NotEmpty(t, entry.Operation.ID)
	assert.True(t, entry.Operation.First)
	assert.Equal(t, "gce_network", entry.Resource.Type)

	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location="+location))
	entry = readAuditEntry(t, "CreateKeyRing", "projects/"+project+"/locations/"+location+"/keyRings/"+ring)
	assert.Equal(t, "cloudkms.googleapis.com", entry.ProtoPayload.ServiceName)
	assert.Equal(t, "cloudkms.keyRings.create", entry.ProtoPayload.AuthorizationInfo[0].Permission)
	assert.Equal(t, "cloudkms_keyring", entry.Resource.Type)

	var created struct {
		Email    string `json:"email"`
		UniqueID string `json:"uniqueId"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("iam", "service-accounts", "create", account, "--format=json")), &created)
	runCLI(t, gcloudCLI("iam", "service-accounts", "delete", created.Email, "--quiet"))
	entry = readAuditEntry(t, "google.iam.admin.v1.DeleteServiceAccount", "projects/-/serviceAccounts/"+created.UniqueID)
	assert.Equal(t, "iam.googleapis.com", entry.ProtoPayload.ServiceName)
	assert.Equal(t, "iam.serviceAccounts.delete", entry.ProtoPayload.AuthorizationInfo[0].Permission)
	assert.Equal(t, "service_account", entry.Resource.Type)
	assert.Equal(t, created.Email, entry.Resource.Labels["email_id"])

	runCLI(t, gcloudCLI("dns", "managed-zones", "create", zone,
		"--dns-name=cli-audit.example.com.", "--description=audit"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("dns", "managed-zones", "delete", zone, "--quiet")) })
	entry = readAuditEntry(t, "dns.managedZones.create", "managedZones/"+zone)
	assert.Equal(t, "dns.googleapis.com", entry.ProtoPayload.ServiceName)
	assert.Equal(t, "dns.managedZones.create", entry.ProtoPayload.AuthorizationInfo[0].Permission)
	assert.Equal(t, "dns_managed_zone", entry.Resource.Type)
}
