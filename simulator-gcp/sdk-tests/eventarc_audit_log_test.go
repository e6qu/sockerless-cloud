package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/eventarc/apiv1/eventarcpb"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/logging/logadmin"
	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	storageapi "google.golang.org/api/storage/v1"
	"google.golang.org/genproto/googleapis/cloud/audit"
	"google.golang.org/protobuf/types/known/durationpb"
)

// cloudEvent is what the log-cloudevent workload wrote for one CloudEvent it
// received: its ce-* attributes and content type, and its data.
type cloudEvent struct {
	Attributes map[string]string `json:"attributes"`
	Data       string            `json:"data"`
}

// awaitServiceCloudEvent waits on the Cloud Run service's stdout log, through
// TailLogEntries, for the first CloudEvent the log-cloudevent workload wrote.
// It opens the tail before reading what the log already holds, so an event
// that lands between the two is in one or the other.
func awaitServiceCloudEvent(t *testing.T, serviceID string) cloudEvent {
	t.Helper()
	filter := `resource.type="cloud_run_revision" AND resource.labels.service_name="` + serviceID + `"`
	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, err := newLoggingV2Client(t).TailLogEntries(tailCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/test-project"},
		Filter:        filter,
		BufferWindow:  durationpb.New(0),
	}))
	parse := func(text string) (cloudEvent, bool) {
		line, ok := strings.CutPrefix(text, "CLOUDEVENT ")
		if !ok {
			return cloudEvent{}, false
		}
		var event cloudEvent
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		return event, true
	}
	it := logadminClient(t).Entries(ctx, logadmin.Filter(filter))
	for {
		entry, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		if text, ok := entry.Payload.(string); ok {
			if event, ok := parse(text); ok {
				return event
			}
		}
	}
	for {
		resp, err := stream.Recv()
		require.NoError(t, err, "service %s never received a CloudEvent", serviceID)
		for _, entry := range resp.GetEntries() {
			if event, ok := parse(entry.GetTextPayload()); ok {
				return event
			}
		}
	}
}

// callerStorage is a Cloud Storage JSON API client that calls as the service
// account, with an access token minted for it through the IAM Service Account
// Credentials API.
func callerStorage(t *testing.T, email string) *storageapi.Service {
	t.Helper()
	token, err := iamCredentialsService(t).Projects.ServiceAccounts.GenerateAccessToken(
		"projects/-/serviceAccounts/"+email,
		&iamcredentials.GenerateAccessTokenRequest{Scope: []string{"https://www.googleapis.com/auth/cloud-platform"}}).Do()
	require.NoError(t, err)
	svc, err := storageapi.NewService(ctx,
		option.WithEndpoint(baseURL+"/storage/v1/"),
		option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.AccessToken})),
	)
	require.NoError(t, err)
	return svc
}

// grantOnProject binds member to role on the project until the test ends.
func grantOnProject(t *testing.T, project, role, member string) {
	t.Helper()
	editProjectPolicy(t, project, func(policy *cloudresourcemanager.Policy) {
		policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{Role: role, Members: []string{member}})
	})
	t.Cleanup(func() {
		editProjectPolicy(t, project, func(policy *cloudresourcemanager.Policy) {
			for _, binding := range policy.Bindings {
				if binding.Role == role {
					binding.Members = slices.DeleteFunc(binding.Members, func(m string) bool { return m == member })
				}
			}
			policy.Bindings = slices.DeleteFunc(policy.Bindings, func(b *cloudresourcemanager.Binding) bool { return len(b.Members) == 0 })
		})
	})
}

// auditLogEntries reads the project's audit entries that match filter from
// one of its Cloud Audit Logs.
func auditLogEntries(t *testing.T, logID, filter string) []*audit.AuditLog {
	t.Helper()
	it := logadminClient(t).Entries(ctx, logadmin.Filter(
		`logName="projects/test-project/logs/cloudaudit.googleapis.com%2F`+logID+`" AND `+filter))
	var out []*audit.AuditLog
	for {
		entry, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out
		}
		require.NoError(t, err)
		payload, ok := entry.Payload.(*audit.AuditLog)
		require.True(t, ok, "an audit entry's payload is a google.cloud.audit.AuditLog, got %T", entry.Payload)
		out = append(out, payload)
	}
}

// TestEventarc_AuditLogTriggerDeliversToCloudRun creates a Cloud Audit Logs
// trigger for storage.buckets.create on one bucket, delivering to a Cloud Run
// service as a service account that may invoke it, and creates that bucket as
// another service account: Cloud Storage writes the Admin Activity entry that
// names the caller, and the service receives the
// google.cloud.audit.log.v1.written CloudEvent carrying it.
func TestEventarc_AuditLogTriggerDeliversToCloudRun(t *testing.T) {
	const project, region = "test-project", "us-central1"
	runClient := newServicesClient(t)
	svc := createInvokableServiceIn(t, runClient, "projects/"+project+"/locations/"+region, "audit-recv",
		&runpb.Service{Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName, Args: []string{"log-cloudevent"},
		}}}})
	serviceID := svc.Name[strings.LastIndex(svc.Name, "/")+1:]
	triggerAccount := createServiceAccount(t, project, uniqueName("ea-trig"))
	grantOnService(t, runClient, svc.Name, "roles/run.invoker", "serviceAccount:"+triggerAccount, nil)
	caller := createServiceAccount(t, project, uniqueName("ea-call"))
	grantOnProject(t, project, "roles/storage.admin", "serviceAccount:"+caller)

	bucket := uniqueName("audit-trigger-bucket")
	client := eventarcClient(t)
	parent := "projects/" + project + "/locations/" + region
	triggerID := uniqueName("audit-trigger")
	create, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: triggerID,
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{
				{Attribute: "type", Value: "google.cloud.audit.log.v1.written"},
				{Attribute: "serviceName", Value: "storage.googleapis.com"},
				{Attribute: "methodName", Value: "storage.buckets.create"},
				{Attribute: "resourceName", Value: "projects/_/buckets/" + bucket},
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
	require.NotEmpty(t, trigger.GetTransport().GetPubsub().GetTopic(), "Eventarc provisions the topic an audit-log trigger delivers through")

	storage := callerStorage(t, caller)
	requireProject(t, project)
	_, err = storage.Buckets.Insert(project, &storageapi.Bucket{Name: bucket, Location: "US-CENTRAL1"}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, storageService(t).Buckets.Delete(bucket).Do()) })

	entries := auditLogEntries(t, "activity",
		`protoPayload.methodName="storage.buckets.create" AND protoPayload.resourceName="projects/_/buckets/`+bucket+`"`)
	require.Len(t, entries, 1, "Cloud Storage writes one Admin Activity entry for the bucket it created")
	assert.Equal(t, "storage.googleapis.com", entries[0].GetServiceName())
	assert.Equal(t, caller, entries[0].GetAuthenticationInfo().GetPrincipalEmail())
	assert.Equal(t, []string{"us-central1"}, entries[0].GetResourceLocation().GetCurrentLocations())
	require.NotEmpty(t, entries[0].GetAuthorizationInfo())
	assert.Equal(t, "storage.buckets.create", entries[0].GetAuthorizationInfo()[0].GetPermission())

	event := awaitServiceCloudEvent(t, serviceID)
	assert.Equal(t, "google.cloud.audit.log.v1.written", event.Attributes["ce-type"])
	assert.Equal(t, "//cloudaudit.googleapis.com/projects/"+project+"/logs/activity", event.Attributes["ce-source"])
	assert.Equal(t, "storage.googleapis.com/projects/_/buckets/"+bucket, event.Attributes["ce-subject"])
	assert.Equal(t, "storage.googleapis.com", event.Attributes["ce-servicename"])
	assert.Equal(t, "storage.buckets.create", event.Attributes["ce-methodname"])
	assert.Equal(t, "projects/_/buckets/"+bucket, event.Attributes["ce-resourcename"])
	assert.NotEmpty(t, event.Attributes["ce-id"])
	assert.True(t, strings.HasPrefix(event.Attributes["content-type"], "application/json"), event.Attributes["content-type"])
	var data struct {
		LogName      string `json:"logName"`
		Severity     string `json:"severity"`
		ProtoPayload struct {
			Type               string `json:"@type"`
			MethodName         string `json:"methodName"`
			AuthenticationInfo struct {
				PrincipalEmail string `json:"principalEmail"`
			} `json:"authenticationInfo"`
		} `json:"protoPayload"`
	}
	require.NoError(t, json.Unmarshal([]byte(event.Data), &data))
	assert.Equal(t, "projects/"+project+"/logs/cloudaudit.googleapis.com%2Factivity", data.LogName)
	assert.Equal(t, "NOTICE", data.Severity)
	assert.Equal(t, "type.googleapis.com/google.cloud.audit.AuditLog", data.ProtoPayload.Type)
	assert.Equal(t, "storage.buckets.create", data.ProtoPayload.MethodName)
	assert.Equal(t, caller, data.ProtoPayload.AuthenticationInfo.PrincipalEmail)
}

// TestAuditLogs_DataAccessAndRPCMethods enables Cloud Storage's DATA_WRITE
// Data Access log in the project's IAM policy and writes an object, which
// writes a storage.objects.create Data Access entry, and creates a Pub/Sub
// topic over gRPC, which writes an Admin Activity entry under the RPC's full
// name.
func TestAuditLogs_DataAccessAndRPCMethods(t *testing.T) {
	const project = "test-project"
	crm := crmService(t)
	setAuditConfigs := func(configs []*cloudresourcemanager.AuditConfig) {
		policy, err := crm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
		require.NoError(t, err)
		policy.AuditConfigs = configs
		_, err = crm.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{
			Policy: policy, UpdateMask: "bindings,etag,auditConfigs",
		}).Do()
		require.NoError(t, err)
	}
	setAuditConfigs([]*cloudresourcemanager.AuditConfig{{
		Service:         "storage.googleapis.com",
		AuditLogConfigs: []*cloudresourcemanager.AuditLogConfig{{LogType: "DATA_WRITE"}},
	}})
	t.Cleanup(func() { setAuditConfigs(nil) })
	policy, err := crm.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, policy.AuditConfigs, 1, "the policy keeps the auditConfigs the mask named")

	bucket := uniqueName("audit-data-bucket")
	svc := storageService(t)
	mustCreateBucket(t, svc, bucket)
	t.Cleanup(func() { assert.NoError(t, svc.Buckets.Delete(bucket).Do()) })
	w := storageClient(t).Bucket(bucket).Object("reports/q1.csv").NewWriter(ctx)
	_, err = w.Write([]byte("a,b\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	t.Cleanup(func() { assert.NoError(t, svc.Objects.Delete(bucket, "reports/q1.csv").Do()) })

	entries := auditLogEntries(t, "data_access", `protoPayload.resourceName="projects/_/buckets/`+bucket+`/objects/reports/q1.csv"`)
	require.Len(t, entries, 1, "DATA_WRITE writes the upload's entry")
	assert.Equal(t, "storage.objects.create", entries[0].GetMethodName())
	assert.Empty(t, auditLogEntries(t, "data_access", `protoPayload.methodName="storage.buckets.get" AND protoPayload.resourceName="projects/_/buckets/`+bucket+`"`),
		"ADMIN_READ stays off")

	topic := "projects/" + project + "/topics/" + uniqueName("audit-topic")
	publisher, _ := psRawClient(t)
	_, err = publisher.CreateTopic(ctx, &pubsubpb.Topic{Name: topic})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := publisher.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: topic})
		assert.NoError(t, err)
	})
	created := auditLogEntries(t, "activity", `protoPayload.resourceName="`+topic+`"`)
	require.Len(t, created, 1)
	assert.Equal(t, "google.pubsub.v1.Publisher.CreateTopic", created[0].GetMethodName())
	assert.Equal(t, "pubsub.googleapis.com", created[0].GetServiceName())
	assert.Equal(t, "type.googleapis.com/google.pubsub.v1.Topic", created[0].GetRequest().GetFields()["@type"].GetStringValue())
}
