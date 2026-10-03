package gcp_sdk_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	"cloud.google.com/go/eventarc/apiv1/eventarcpb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/logging"
	"cloud.google.com/go/logging/logadmin"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
	dns "google.golang.org/api/dns/v1"
	firestore "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/iterator"
	spanneradmin "google.golang.org/api/spanner/v1"
	"google.golang.org/genproto/googleapis/cloud/audit"
	iamlogging "google.golang.org/genproto/googleapis/iam/v1/logging"
)

// auditActivityEntry reads the one Admin Activity entry the project's audit
// log holds for a method on a resource.
func auditActivityEntry(t *testing.T, methodName, resourceName string) (*logging.Entry, *audit.AuditLog) {
	t.Helper()
	it := logadminClient(t).Entries(ctx, logadmin.Filter(
		`logName="projects/test-project/logs/cloudaudit.googleapis.com%2Factivity" AND protoPayload.methodName="`+
			methodName+`" AND protoPayload.resourceName="`+resourceName+`"`))
	var entries []*logging.Entry
	for {
		entry, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		entries = append(entries, entry)
	}
	require.Len(t, entries, 1, "one Admin Activity entry for %s on %s", methodName, resourceName)
	payload, ok := entries[0].Payload.(*audit.AuditLog)
	require.True(t, ok, "the payload is a google.cloud.audit.AuditLog, got %T", entries[0].Payload)
	return entries[0], payload
}

func auditPermission(t *testing.T, payload *audit.AuditLog) string {
	t.Helper()
	require.NotEmpty(t, payload.GetAuthorizationInfo(), "the entry records the permission the call checked")
	assert.True(t, payload.GetAuthorizationInfo()[0].GetGranted())
	return payload.GetAuthorizationInfo()[0].GetPermission()
}

// TestAuditLogs_ComputeEngine inserts a VPC network: Compute Engine writes one
// Admin Activity entry under v1.compute.networks.insert, for the operation the
// call started, marked both first and last since the operation was done when
// the call answered.
func TestAuditLogs_ComputeEngine(t *testing.T) {
	const project = "test-project"
	svc := computeService(t)
	name := uniqueName("audit-net")
	op, err := svc.Networks.Insert(project, &compute.Network{Name: name, AutoCreateSubnetworks: false, ForceSendFields: []string{"AutoCreateSubnetworks"}}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := svc.Networks.Delete(project, name).Do()
		assert.NoError(t, err)
	})

	entry, payload := auditActivityEntry(t, "v1.compute.networks.insert", "projects/"+project+"/global/networks/"+name)
	assert.Equal(t, "compute.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "compute.networks.create", auditPermission(t, payload))
	assert.Equal(t, "type.googleapis.com/compute.networks.insert", payload.GetRequest().GetFields()["@type"].GetStringValue())
	assert.Equal(t, name, payload.GetRequest().GetFields()["name"].GetStringValue())
	assert.Equal(t, "type.googleapis.com/operation", payload.GetResponse().GetFields()["@type"].GetStringValue())
	require.NotNil(t, entry.Operation)
	assert.Equal(t, op.Name, entry.Operation.GetId())
	assert.Equal(t, "compute.googleapis.com", entry.Operation.GetProducer())
	assert.True(t, entry.Operation.GetFirst())
	assert.True(t, entry.Operation.GetLast())
	assert.Equal(t, "gce_network", entry.Resource.GetType())
	assert.Equal(t, fmt.Sprint(op.TargetId), entry.Resource.GetLabels()["network_id"])
}

// TestAuditLogs_IAMServiceAccounts creates and deletes a service account: IAM
// writes google.iam.admin.v1.CreateServiceAccount on the project and
// google.iam.admin.v1.DeleteServiceAccount on the account, which it names by
// its unique ID.
func TestAuditLogs_IAMServiceAccounts(t *testing.T) {
	const project = "test-project"
	svc := iamService(t)
	accountID := uniqueName("audit-sa")
	created, err := svc.Projects.ServiceAccounts.Create("projects/"+project,
		&iam.CreateServiceAccountRequest{AccountId: accountID}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.ServiceAccounts.Delete(created.Name).Do()
	require.NoError(t, err)

	it := logadminClient(t).Entries(ctx, logadmin.Filter(
		`logName="projects/test-project/logs/cloudaudit.googleapis.com%2Factivity" AND protoPayload.methodName="google.iam.admin.v1.CreateServiceAccount" AND resource.labels.email_id="`+created.Email+`"`))
	entry, err := it.Next()
	require.NoError(t, err)
	payload := entry.Payload.(*audit.AuditLog)
	assert.Equal(t, "iam.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "projects/"+project, payload.GetResourceName())
	assert.Equal(t, "iam.serviceAccounts.create", auditPermission(t, payload))
	assert.Equal(t, "service_account", entry.Resource.GetType())
	assert.Equal(t, created.UniqueId, entry.Resource.GetLabels()["unique_id"])

	deleted, payload := auditActivityEntry(t, "google.iam.admin.v1.DeleteServiceAccount", "projects/-/serviceAccounts/"+created.UniqueId)
	assert.Equal(t, "iam.serviceAccounts.delete", auditPermission(t, payload))
	assert.Equal(t, created.Email, deleted.Resource.GetLabels()["email_id"])
}

// TestAuditLogs_ResourceManagerSetIamPolicy grants a role on the project:
// Cloud Resource Manager writes SetIamPolicy with the binding delta the call
// made in its serviceData.
func TestAuditLogs_ResourceManagerSetIamPolicy(t *testing.T) {
	const project = "test-project"
	member := "serviceAccount:" + uniqueName("audit-member") + "@test-project.iam.gserviceaccount.com"
	grantOnProject(t, project, "roles/browser", member)

	it := logadminClient(t).Entries(ctx, logadmin.Filter(
		`logName="projects/test-project/logs/cloudaudit.googleapis.com%2Factivity" AND protoPayload.methodName="SetIamPolicy" AND `+
			`protoPayload.serviceData.policyDelta.bindingDeltas.member="`+member+`" AND protoPayload.serviceData.policyDelta.bindingDeltas.action="ADD"`))
	entry, err := it.Next()
	require.NoError(t, err)
	payload := entry.Payload.(*audit.AuditLog)
	assert.Equal(t, "cloudresourcemanager.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "projects/"+project, payload.GetResourceName())
	assert.Equal(t, "resourcemanager.projects.setIamPolicy", auditPermission(t, payload))
	var data iamlogging.AuditData
	require.NoError(t, payload.GetServiceData().UnmarshalTo(&data))
	assert.True(t, slices.ContainsFunc(data.GetPolicyDelta().GetBindingDeltas(), func(d *iampb.BindingDelta) bool {
		return d.GetMember() == member && d.GetRole() == "roles/browser" && d.GetAction().String() == "ADD"
	}), "the delta names the binding the call added: %v", data.GetPolicyDelta())
}

// TestAuditLogs_CloudDNS creates a managed zone: Cloud DNS writes
// dns.managedZones.create on managedZones/{zone}.
func TestAuditLogs_CloudDNS(t *testing.T) {
	const project = "test-project"
	svc := dnsService(t)
	zone := uniqueName("audit-zone")
	_, err := svc.ManagedZones.Create(project, &dns.ManagedZone{Name: zone, DnsName: zone + ".example.com.", Description: "audit"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, svc.ManagedZones.Delete(project, zone).Do()) })

	entry, payload := auditActivityEntry(t, "dns.managedZones.create", "managedZones/"+zone)
	assert.Equal(t, "dns.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "dns.managedZones.create", auditPermission(t, payload))
	assert.Equal(t, "type.googleapis.com/cloud.dns.api.ManagedZonesCreateRequest", payload.GetRequest().GetFields()["@type"].GetStringValue())
	assert.Equal(t, "dns_managed_zone", entry.Resource.GetType())
	assert.Equal(t, zone, entry.Resource.GetLabels()["zone_name"])
}

// TestAuditLogs_RPCDefinedServices creates a key ring over gRPC, a Spanner
// instance and a Firestore database over REST, and a Bigtable instance over
// gRPC: each service writes its entry with the permission the call checked,
// and the long-running calls carry their operation.
func TestAuditLogs_RPCDefinedServices(t *testing.T) {
	const project = "test-project"

	kmsClient := newKMSGRPCClient(t)
	ringID := uniqueName("audit-ring")
	ring, err := kmsClient.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent: "projects/" + project + "/locations/us-central1", KeyRingId: ringID, KeyRing: &kmspb.KeyRing{},
	})
	require.NoError(t, err)
	entry, payload := auditActivityEntry(t, "CreateKeyRing", ring.GetName())
	assert.Equal(t, "cloudkms.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "cloudkms.keyRings.create", auditPermission(t, payload))
	assert.Equal(t, "cloudkms_keyring", entry.Resource.GetType())
	assert.Equal(t, ringID, entry.Resource.GetLabels()["key_ring_id"])

	spanner := spannerAdminService(t, ctx)
	instance := uniqueName("audit-sp")
	op, err := spanner.Projects.Instances.Create("projects/"+project, &spanneradmin.CreateInstanceRequest{
		InstanceId: instance,
		Instance:   &spanneradmin.Instance{DisplayName: instance, NodeCount: 1},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := spanner.Projects.Instances.Delete("projects/" + project + "/instances/" + instance).Do()
		assert.NoError(t, err)
	})
	entry, payload = auditActivityEntry(t, "google.spanner.admin.instance.v1.InstanceAdmin.CreateInstance", "projects/"+project+"/instances/"+instance)
	assert.Equal(t, "spanner.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "spanner.instances.create", auditPermission(t, payload))
	assert.Equal(t, "spanner_instance", entry.Resource.GetType())
	require.NotNil(t, entry.Operation)
	assert.Equal(t, op.Name, entry.Operation.GetId())
	assert.Equal(t, "spanner.googleapis.com", entry.Operation.GetProducer())
	assert.True(t, entry.Operation.GetFirst())

	fs := firestoreAdminService(t)
	database := uniqueName("audit-db")
	_, err = fs.Projects.Databases.Create("projects/"+project, &firestore.GoogleFirestoreAdminV1Database{
		LocationId: "nam5", Type: "FIRESTORE_NATIVE",
	}).DatabaseId(database).Do()
	require.NoError(t, err)
	_, payload = auditActivityEntry(t, "google.firestore.admin.v1.FirestoreAdmin.CreateDatabase", "projects/"+project+"/databases/"+database)
	assert.Equal(t, "firestore.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "datastore.databases.create", auditPermission(t, payload))

	instanceAdmin, _, _ := bigtableAdminGRPCConn(t)
	btInstance, _ := bigtableAdminGRPCInstance(t, instanceAdmin, project, uniqueName("audit-bt"), "c1")
	t.Cleanup(func() {
		_, err := instanceAdmin.DeleteInstance(ctx, &adminpb.DeleteInstanceRequest{Name: btInstance})
		assert.NoError(t, err)
	})
	entry, payload = auditActivityEntry(t, "google.bigtable.admin.v2.BigtableInstanceAdmin.CreateInstance", btInstance)
	assert.Equal(t, "bigtableadmin.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "bigtable.instances.create", auditPermission(t, payload))
	require.NotNil(t, entry.Operation)
	assert.True(t, entry.Operation.GetFirst() && entry.Operation.GetLast(), "the operation was done when the call answered")
}

// TestEventarc_AuditLogTriggerForCloudKMS routes Cloud KMS's CreateKeyRing
// entries for one key ring to a Cloud Run service: creating the key ring over
// gRPC delivers the google.cloud.audit.log.v1.written CloudEvent.
func TestEventarc_AuditLogTriggerForCloudKMS(t *testing.T) {
	const project, region = "test-project", "us-central1"
	runClient := newServicesClient(t)
	svc := createInvokableServiceIn(t, runClient, "projects/"+project+"/locations/"+region, "kms-audit",
		&runpb.Service{Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName, Args: []string{"log-cloudevent"},
		}}}})
	serviceID := svc.Name[strings.LastIndex(svc.Name, "/")+1:]
	triggerAccount := createServiceAccount(t, project, uniqueName("ea-kms"))
	grantOnService(t, runClient, svc.Name, "roles/run.invoker", "serviceAccount:"+triggerAccount, nil)

	ringID := uniqueName("audit-trigger-ring")
	ringName := "projects/" + project + "/locations/" + region + "/keyRings/" + ringID
	client := eventarcClient(t)
	create, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    "projects/" + project + "/locations/" + region,
		TriggerId: uniqueName("kms-audit"),
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{
				{Attribute: "type", Value: "google.cloud.audit.log.v1.written"},
				{Attribute: "serviceName", Value: "cloudkms.googleapis.com"},
				{Attribute: "methodName", Value: "CreateKeyRing"},
				{Attribute: "resourceName", Value: ringName},
			},
			ServiceAccount: triggerAccount,
			Destination: &eventarcpb.Destination{Descriptor_: &eventarcpb.Destination_CloudRun{
				CloudRun: &eventarcpb.CloudRun{Service: serviceID, Region: region},
			}},
		},
	})
	require.NoError(t, err)
	trigger, err := create.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		op, err := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: trigger.GetName()})
		if assert.NoError(t, err) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err)
		}
	})

	_, err = newKMSGRPCClient(t).CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent: "projects/" + project + "/locations/" + region, KeyRingId: ringID, KeyRing: &kmspb.KeyRing{},
	})
	require.NoError(t, err)

	event := awaitServiceCloudEvent(t, serviceID)
	assert.Equal(t, "google.cloud.audit.log.v1.written", event.Attributes["ce-type"])
	assert.Equal(t, "cloudkms.googleapis.com", event.Attributes["ce-servicename"])
	assert.Equal(t, "CreateKeyRing", event.Attributes["ce-methodname"])
	assert.Equal(t, ringName, event.Attributes["ce-resourcename"])
	assert.Equal(t, "cloudkms.googleapis.com/"+ringName, event.Attributes["ce-subject"])
	assert.Contains(t, event.Data, `"permission":"cloudkms.keyRings.create"`)
}
