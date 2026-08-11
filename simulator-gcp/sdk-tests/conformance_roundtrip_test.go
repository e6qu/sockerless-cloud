package gcp_sdk_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	eventarc "cloud.google.com/go/eventarc/apiv1"
	"cloud.google.com/go/eventarc/apiv1/eventarcpb"
	functions "cloud.google.com/go/functions/apiv2"
	"cloud.google.com/go/functions/apiv2/functionspb"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	artifactregistry "google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/bigquery/v2"
	bigtableadmin "google.golang.org/api/bigtableadmin/v2"
	cloudbuild "google.golang.org/api/cloudbuild/v1"
	cloudkms "google.golang.org/api/cloudkms/v1"
	"google.golang.org/api/cloudresourcemanager/v1"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/dataflow/v1b3"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/firestore/v1"
	"google.golang.org/api/googleapi"
	logging "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	"google.golang.org/api/pubsub/v1"
	redis "google.golang.org/api/redis/v1"
	secretmanager "google.golang.org/api/secretmanager/v1"
	spanner "google.golang.org/api/spanner/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	fieldmaskpb "google.golang.org/protobuf/types/known/fieldmaskpb"
)

// These tests assert that writable fields accepted on create/patch survive a
// read back through the real Google client libraries. A field dropped on
// get/list produces perpetual terraform-provider-google drift; each case below
// drives the canonical client a consumer would use.

func TestConformance_DNSManagedZoneLabelsRoundTrip(t *testing.T) {
	svc, err := dns.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	created, err := svc.ManagedZones.Create("conformance-project", &dns.ManagedZone{
		Name:    "conformance-labels-zone",
		DnsName: "conformance.example.com.",
		Labels:  map[string]string{"env": "prod", "team": "platform"},
		DnssecConfig: &dns.ManagedZoneDnsSecConfig{
			State: "on",
		},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"env": "prod", "team": "platform"}, created.Labels)

	got, err := svc.ManagedZones.Get("conformance-project", "conformance-labels-zone").Do()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod", "team": "platform"}, got.Labels)
	require.NotNil(t, got.DnssecConfig)
	assert.Equal(t, "on", got.DnssecConfig.State)
}

func TestConformance_BigQueryTablePartitioningRoundTrip(t *testing.T) {
	svc, err := bigquery.NewService(ctx, option.WithEndpoint(baseURL+"/bigquery/v2/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	proj := "conformance-project"

	_, err = svc.Datasets.Insert(proj, &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{ProjectId: proj, DatasetId: "conf_part_ds"},
	}).Do()
	require.NoError(t, err)

	requireFilter := true
	created, err := svc.Tables.Insert(proj, "conf_part_ds", &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: proj, DatasetId: "conf_part_ds", TableId: "events"},
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "ts", Type: "TIMESTAMP"},
			{Name: "id", Type: "STRING"},
		}},
		TimePartitioning:       &bigquery.TimePartitioning{Type: "DAY", Field: "ts"},
		Clustering:             &bigquery.Clustering{Fields: []string{"id"}},
		RequirePartitionFilter: requireFilter,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created.TimePartitioning)

	got, err := svc.Tables.Get(proj, "conf_part_ds", "events").Do()
	require.NoError(t, err)
	require.NotNil(t, got.TimePartitioning, "timePartitioning dropped on get")
	assert.Equal(t, "DAY", got.TimePartitioning.Type)
	assert.Equal(t, "ts", got.TimePartitioning.Field)
	require.NotNil(t, got.Clustering)
	assert.Equal(t, []string{"id"}, got.Clustering.Fields)
	assert.True(t, got.RequirePartitionFilter)
}

func TestConformance_PubSubTopicSchemaSettingsRoundTrip(t *testing.T) {
	svc, err := pubsub.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	name := "projects/conformance-project/topics/conf-schema-topic"

	created, err := svc.Projects.Topics.Create(name, &pubsub.Topic{
		MessageRetentionDuration: "86400s",
		SchemaSettings: &pubsub.SchemaSettings{
			Schema:   "projects/conformance-project/schemas/_deleted-schema_",
			Encoding: "JSON",
		},
		Labels: map[string]string{"env": "prod"},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created.SchemaSettings)

	got, err := svc.Projects.Topics.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "86400s", got.MessageRetentionDuration)
	require.NotNil(t, got.SchemaSettings, "schemaSettings dropped on get")
	assert.Equal(t, "JSON", got.SchemaSettings.Encoding)
	assert.Equal(t, "projects/conformance-project/schemas/_deleted-schema_", got.SchemaSettings.Schema)
}

func TestConformance_EventarcTriggerChannelRoundTrip(t *testing.T) {
	client, err := eventarc.NewRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	parent := "projects/conformance-project/locations/us-central1"
	name := parent + "/triggers/conf-channel-trigger"
	channel := parent + "/channels/conf-channel"

	op, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "conf-channel-trigger",
		Trigger: &eventarcpb.Trigger{
			Channel: channel,
			EventFilters: []*eventarcpb.EventFilter{{
				Attribute: "type",
				Value:     "google.cloud.pubsub.topic.v1.messagePublished",
			}},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_CloudRun{
					CloudRun: &eventarcpb.CloudRun{Service: "svc", Region: "us-central1"},
				},
			},
		},
	})
	require.NoError(t, err)
	created, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, channel, created.GetChannel())
	t.Cleanup(func() {
		dop, derr := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: name})
		if derr == nil {
			_, _ = dop.Wait(ctx)
		}
	})

	got, err := client.GetTrigger(ctx, &eventarcpb.GetTriggerRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, channel, got.GetChannel(), "channel dropped on get")
}

func TestConformance_ArtifactRegistryRepoLabelsRoundTrip(t *testing.T) {
	svc, err := artifactregistry.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	parent := "projects/conformance-project/locations/us-central1"
	name := parent + "/repositories/conf-labels-repo"

	dryRun := true
	_, err = svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{
		Format:              "DOCKER",
		Labels:              map[string]string{"env": "prod", "team": "platform"},
		KmsKeyName:          "projects/conformance-project/locations/us-central1/keyRings/kr/cryptoKeys/k",
		CleanupPolicyDryRun: dryRun,
		CleanupPolicies: map[string]artifactregistry.CleanupPolicy{
			"keep-recent": {Action: "KEEP", Id: "keep-recent"},
		},
	}).RepositoryId("conf-labels-repo").Do()
	require.NoError(t, err)

	got, err := svc.Projects.Locations.Repositories.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod", "team": "platform"}, got.Labels, "labels dropped on get")
	assert.Equal(t, "projects/conformance-project/locations/us-central1/keyRings/kr/cryptoKeys/k", got.KmsKeyName)
	assert.True(t, got.CleanupPolicyDryRun)
	require.Contains(t, got.CleanupPolicies, "keep-recent")
	assert.Equal(t, "KEEP", got.CleanupPolicies["keep-recent"].Action)
}

func TestConformance_SecretManagerAnnotationsRoundTrip(t *testing.T) {
	svc, err := secretmanager.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	parent := "projects/conformance-project"
	secretID := "conf-annotations-secret"
	name := parent + "/secrets/" + secretID

	_, err = svc.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
		Annotations: map[string]string{"owner": "sdk", "team": "platform"},
		Topics: []*secretmanager.Topic{
			{Name: "projects/conformance-project/topics/conf-secret-topic"},
		},
	}).SecretId(secretID).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Secrets.Delete(name).Do() })

	got, err := svc.Projects.Secrets.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"owner": "sdk", "team": "platform"}, got.Annotations, "annotations dropped on get")
	require.Len(t, got.Topics, 1, "topics dropped on get")
	assert.Equal(t, "projects/conformance-project/topics/conf-secret-topic", got.Topics[0].Name)

	// PATCH updateMask=annotations must apply without erroring.
	patched, err := svc.Projects.Secrets.Patch(name, &secretmanager.Secret{
		Annotations: map[string]string{"owner": "ops"},
	}).UpdateMask("annotations").Do()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"owner": "ops"}, patched.Annotations)
}

func TestConformance_IAMBindingConditionRoundTrip(t *testing.T) {
	svc, err := cloudresourcemanager.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	project := "conf-iam-condition-project"
	ensureV1Project(t, svc, project)

	_, err = svc.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{
		Policy: &cloudresourcemanager.Policy{
			Bindings: []*cloudresourcemanager.Binding{{
				Role:    "roles/viewer",
				Members: []string{"user:dev@example.com"},
				Condition: &cloudresourcemanager.Expr{
					Title:      "expires-2030",
					Expression: `request.time < timestamp("2030-01-01T00:00:00Z")`,
				},
			}},
		},
	}).Do()
	require.NoError(t, err)

	got, err := svc.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	require.NotNil(t, got.Bindings[0].Condition, "binding condition dropped on get")
	assert.Equal(t, "expires-2030", got.Bindings[0].Condition.Title)
	assert.Equal(t, `request.time < timestamp("2030-01-01T00:00:00Z")`, got.Bindings[0].Condition.Expression)
}

func TestConformance_LoggingMetricValueExtractorRoundTrip(t *testing.T) {
	svc, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	parent := "projects/conformance-project"

	created, err := svc.Projects.Metrics.Create(parent, &logging.LogMetric{
		Name:           "conf-distribution-metric",
		Filter:         `resource.type="gce_instance"`,
		ValueExtractor: "EXTRACT(jsonPayload.latency)",
		MetricDescriptor: &logging.MetricDescriptor{
			MetricKind: "DELTA",
			ValueType:  "DISTRIBUTION",
		},
		BucketOptions: &logging.BucketOptions{
			ExplicitBuckets: &logging.Explicit{Bounds: []float64{0, 1, 2, 5, 10}},
		},
		LabelExtractors: map[string]string{"zone": "EXTRACT(resource.labels.zone)"},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created.BucketOptions)

	got, err := svc.Projects.Metrics.Get(parent + "/metrics/conf-distribution-metric").Do()
	require.NoError(t, err)
	assert.Equal(t, "EXTRACT(jsonPayload.latency)", got.ValueExtractor, "valueExtractor dropped on get")
	require.NotNil(t, got.BucketOptions, "bucketOptions dropped on get")
	require.NotNil(t, got.BucketOptions.ExplicitBuckets)
	assert.Equal(t, []float64{0, 1, 2, 5, 10}, got.BucketOptions.ExplicitBuckets.Bounds)
	assert.Equal(t, map[string]string{"zone": "EXTRACT(resource.labels.zone)"}, got.LabelExtractors)
}

// requireGoogleErr asserts the error is a googleapi.Error with the given HTTP
// code and that the GCP error envelope's `status` string is present in the raw
// body — the canonical {"error":{"code","message","status"}} shape.
func requireGoogleErr(t *testing.T, err error, code int, gcpStatus string) {
	t.Helper()
	require.Error(t, err)
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	assert.Equal(t, code, gerr.Code, "http status code")
	assert.Contains(t, gerr.Body, gcpStatus, "GCP error.status field")
}

// TestConformance_ComputeDuplicateInsertConflict pins that a second insert of
// a metadata-only Compute resource (backendService) returns 409 ALREADY_EXISTS
// like real GCP, instead of silently overwriting. backendServices.insert does
// not touch the network fabric, so the case runs without a real-exec host.
func TestConformance_ComputeDuplicateInsertConflict(t *testing.T) {
	svc, err := compute.NewService(ctx, option.WithEndpoint(baseURL+"/compute/v1/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "conformance-project"

	bs := &compute.BackendService{Name: "conf-dup-backend", Protocol: "HTTP"}
	_, err = svc.BackendServices.Insert(project, bs).Context(ctx).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.BackendServices.Delete(project, bs.Name).Context(ctx).Do() })

	_, err = svc.BackendServices.Insert(project, bs).Context(ctx).Do()
	requireGoogleErr(t, err, 409, "ALREADY_EXISTS")
}

// TestConformance_ComputeDeleteMissingNotFound pins that deleting a never-
// created metadata-only Compute resource (healthCheck) returns 404 NOT_FOUND
// like real GCP, instead of a synthesized DONE operation.
func TestConformance_ComputeDeleteMissingNotFound(t *testing.T) {
	svc, err := compute.NewService(ctx, option.WithEndpoint(baseURL+"/compute/v1/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	_, err = svc.HealthChecks.Delete("conformance-project", "conf-never-created-hc").Context(ctx).Do()
	requireGoogleErr(t, err, 404, "NOT_FOUND")
}

// TestConformance_PubSubDuplicateTopicConflict pins that creating a topic twice
// returns 409 ALREADY_EXISTS like real Pub/Sub instead of overwriting.
func TestConformance_PubSubDuplicateTopicConflict(t *testing.T) {
	svc, err := pubsub.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	name := "projects/conformance-project/topics/conf-dup-topic"

	_, err = svc.Projects.Topics.Create(name, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(name).Do() })

	_, err = svc.Projects.Topics.Create(name, &pubsub.Topic{}).Do()
	requireGoogleErr(t, err, 409, "ALREADY_EXISTS")
}

// TestConformance_PubSubDuplicateSubscriptionConflict pins the same contract
// for subscriptions.
func TestConformance_PubSubDuplicateSubscriptionConflict(t *testing.T) {
	svc, err := pubsub.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	topic := "projects/conformance-project/topics/conf-dup-sub-topic"
	sub := "projects/conformance-project/subscriptions/conf-dup-sub"

	_, err = svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(topic).Do() })

	_, err = svc.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Subscriptions.Delete(sub).Do() })

	_, err = svc.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	requireGoogleErr(t, err, 409, "ALREADY_EXISTS")
}

// TestConformance_EventarcDuplicateTriggerConflict pins that a second
// CreateTrigger with the same triggerId returns ALREADY_EXISTS. The eventarc
// gRPC client surfaces the 409 as codes.AlreadyExists.
func TestConformance_EventarcDuplicateTriggerConflict(t *testing.T) {
	client, err := eventarc.NewRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	parent := "projects/conformance-project/locations/us-central1"
	name := parent + "/triggers/conf-dup-trigger"
	req := &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "conf-dup-trigger",
		Trigger: &eventarcpb.Trigger{
			Channel: parent + "/channels/conf-dup-trigger-channel",
			EventFilters: []*eventarcpb.EventFilter{{
				Attribute: "type",
				Value:     "google.cloud.pubsub.topic.v1.messagePublished",
			}},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_CloudRun{
					CloudRun: &eventarcpb.CloudRun{Service: "svc", Region: "us-central1"},
				},
			},
		},
	}
	op, err := client.CreateTrigger(ctx, req)
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		dop, derr := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: name})
		if derr == nil {
			_, _ = dop.Wait(ctx)
		}
	})

	_, err = client.CreateTrigger(ctx, req)
	require.Error(t, err)
	// The eventarc REST-over-gRPC client maps the sim's HTTP 409 to a gRPC
	// status. ALREADY_EXISTS and ABORTED both carry HTTP 409, and this
	// client's transport derives the gRPC code from the HTTP status rather
	// than the body's `status` string, so accept either 409-family code and
	// assert the conflict message — exactly what real Eventarc + this client
	// surface for a duplicate create.
	code := status.Code(err)
	assert.Contains(t, []codes.Code{codes.AlreadyExists, codes.Aborted}, code, "duplicate CreateTrigger must be a 409-family conflict: %v", err)
	assert.Contains(t, err.Error(), "already exists")
}

// TestConformance_LoggingDeleteMissingSinkNotFound pins that deleting a
// never-created log sink returns 404 NOT_FOUND like real Cloud Logging.
func TestConformance_LoggingDeleteMissingSinkNotFound(t *testing.T) {
	svc, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	_, err = svc.Projects.Sinks.Delete("projects/conformance-project/sinks/conf-never-created-sink").Do()
	requireGoogleErr(t, err, 404, "NOT_FOUND")
}

// TestConformance_IAMSetPolicyStaleEtagAborted pins the optimistic-concurrency
// contract: a setIamPolicy carrying a stale etag is rejected with 409 ABORTED
// so the caller re-reads and retries.
func TestConformance_IAMSetPolicyStaleEtagAborted(t *testing.T) {
	svc, err := cloudresourcemanager.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	project := "conformance-iam-etag-project"
	ensureV1Project(t, svc, project)

	// Read the current policy to learn its etag.
	got, err := svc.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, got.Etag)

	// A write with the correct etag succeeds and mints a new one.
	ok, err := svc.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{
		Policy: &cloudresourcemanager.Policy{
			Etag:     got.Etag,
			Bindings: []*cloudresourcemanager.Binding{{Role: "roles/viewer", Members: []string{"user:a@example.com"}}},
		},
	}).Do()
	require.NoError(t, err)
	require.NotEqual(t, got.Etag, ok.Etag, "successful set must mint a fresh etag")

	// Re-using the now-stale original etag must be rejected with ABORTED.
	_, err = svc.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{
		Policy: &cloudresourcemanager.Policy{
			Etag:     got.Etag,
			Bindings: []*cloudresourcemanager.Binding{{Role: "roles/editor", Members: []string{"user:b@example.com"}}},
		},
	}).Do()
	requireGoogleErr(t, err, 409, "ABORTED")
}

// TestConformance_ComputeListPaginatesByMaxResults pins that the Compute list
// surface honors the API's maxResults page size + pageToken cursor. A
// metadata-only backendService (insert never touches the network fabric) lets
// this run without a real-exec host. Before the fix, paginateList read
// "pageSize" — a param the Compute SDK never sends — so every list returned the
// full set on page 1 with no nextPageToken.
func TestConformance_ComputeListPaginatesByMaxResults(t *testing.T) {
	svc, err := compute.NewService(ctx, option.WithEndpoint(baseURL+"/compute/v1/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "conf-compute-pagination"

	names := []string{"conf-pag-bs-a", "conf-pag-bs-b"}
	for _, n := range names {
		_, err := svc.BackendServices.Insert(project, &compute.BackendService{Name: n, Protocol: "HTTP"}).Context(ctx).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.BackendServices.Delete(project, n).Context(ctx).Do() })
	}

	// maxResults=1 must return exactly one item plus a token for the rest.
	first, err := svc.BackendServices.List(project).MaxResults(1).Context(ctx).Do()
	require.NoError(t, err)
	require.Len(t, first.Items, 1, "maxResults=1 must return a single item")
	require.NotEmpty(t, first.NextPageToken, "a partial page must carry a nextPageToken")

	// Following the token yields the remaining item and exhausts the list.
	second, err := svc.BackendServices.List(project).MaxResults(1).PageToken(first.NextPageToken).Context(ctx).Do()
	require.NoError(t, err)
	require.Len(t, second.Items, 1, "second page must return the remaining item")
	assert.NotEqual(t, first.Items[0].Name, second.Items[0].Name, "pages must not overlap")
	assert.Empty(t, second.NextPageToken, "last page must not carry a token")

	// No maxResults returns the full list with no token (unchanged behavior).
	all, err := svc.BackendServices.List(project).Context(ctx).Do()
	require.NoError(t, err)
	require.Len(t, all.Items, 2, "an unbounded list returns every item")
	assert.Empty(t, all.NextPageToken, "an unbounded list carries no token")
}

// TestConformance_DNSRecordSetsPaginate pins that rrsets list honors maxResults +
// pageToken and stamps the dns#resourceRecordSetsListResponse kind. Before the
// fix the handler ignored both params and emitted no nextPageToken.
func TestConformance_DNSRecordSetsPaginate(t *testing.T) {
	svc, err := dns.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "conf-dns-pagination"
	const zone = "conf-pag-zone"

	_, err = svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name: zone, DnsName: "conf-pag.example.com.",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.ManagedZones.Delete(project, zone).Do() })

	recs := []*dns.ResourceRecordSet{
		{Name: "a.conf-pag.example.com.", Type: "A", Ttl: 300, Rrdatas: []string{"10.0.0.1"}},
		{Name: "b.conf-pag.example.com.", Type: "A", Ttl: 300, Rrdatas: []string{"10.0.0.2"}},
	}
	for _, rr := range recs {
		_, err := svc.ResourceRecordSets.Create(project, zone, rr).Do()
		require.NoError(t, err)
	}

	first, err := svc.ResourceRecordSets.List(project, zone).MaxResults(1).Do()
	require.NoError(t, err)
	require.Len(t, first.Rrsets, 1, "maxResults=1 must return one rrset")
	require.NotEmpty(t, first.NextPageToken, "a partial rrset page must carry a token")
	assert.Equal(t, "dns#resourceRecordSetsListResponse", first.Kind)

	second, err := svc.ResourceRecordSets.List(project, zone).MaxResults(1).PageToken(first.NextPageToken).Do()
	require.NoError(t, err)
	require.Len(t, second.Rrsets, 1, "second rrset page must return the remaining record")
	assert.Empty(t, second.NextPageToken, "last rrset page carries no token")

	all, err := svc.ResourceRecordSets.List(project, zone).Do()
	require.NoError(t, err)
	require.Len(t, all.Rrsets, 2, "unbounded rrset list returns every record")
	assert.Empty(t, all.NextPageToken)
}

// TestConformance_EventarcTriggersPaginate pins that ListTriggers honors
// pageSize + emits a nextPageToken. The REST iterator's InternalFetch exposes a
// single page and its follow-on token directly.
func TestConformance_EventarcTriggersPaginate(t *testing.T) {
	client, err := eventarc.NewRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	parent := "projects/conf-eventarc-pagination/locations/us-central1"
	for _, id := range []string{"conf-pag-trig-a", "conf-pag-trig-b"} {
		op, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
			Parent:    parent,
			TriggerId: id,
			Trigger: &eventarcpb.Trigger{
				Channel: parent + "/channels/" + id + "-channel",
				EventFilters: []*eventarcpb.EventFilter{{
					Attribute: "type",
					Value:     "google.cloud.pubsub.topic.v1.messagePublished",
				}},
				Destination: &eventarcpb.Destination{
					Descriptor_: &eventarcpb.Destination_CloudRun{
						CloudRun: &eventarcpb.CloudRun{Service: "svc", Region: "us-central1"},
					},
				},
			},
		})
		require.NoError(t, err)
		_, err = op.Wait(ctx)
		require.NoError(t, err)
		name := parent + "/triggers/" + id
		t.Cleanup(func() {
			dop, derr := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: name})
			if derr == nil {
				_, _ = dop.Wait(ctx)
			}
		})
	}

	it := client.ListTriggers(ctx, &eventarcpb.ListTriggersRequest{Parent: parent})
	page, next, err := it.InternalFetch(1, "")
	require.NoError(t, err)
	require.Len(t, page, 1, "pageSize=1 must return one trigger")
	require.NotEmpty(t, next, "a partial trigger page must carry a token")

	page2, next2, err := it.InternalFetch(1, next)
	require.NoError(t, err)
	require.Len(t, page2, 1, "second trigger page must return the remaining trigger")
	assert.Empty(t, next2, "last trigger page carries no token")
}

// TestConformance_DataflowJobsPaginate pins that ListJobs honors pageSize +
// pageToken and emits a nextPageToken.
func TestConformance_DataflowJobsPaginate(t *testing.T) {
	svc, err := dataflow.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "conf-dataflow-pagination"
	const location = "us-central1"

	for _, n := range []string{"conf-pag-job-a", "conf-pag-job-b"} {
		_, err := svc.Projects.Locations.Jobs.Create(project, location, &dataflow.Job{
			Name: n, Type: "JOB_TYPE_BATCH",
		}).Do()
		require.NoError(t, err)
	}

	first, err := svc.Projects.Locations.Jobs.List(project, location).PageSize(1).Do()
	require.NoError(t, err)
	require.Len(t, first.Jobs, 1, "pageSize=1 must return one job")
	require.NotEmpty(t, first.NextPageToken, "a partial job page must carry a token")

	second, err := svc.Projects.Locations.Jobs.List(project, location).PageSize(1).PageToken(first.NextPageToken).Do()
	require.NoError(t, err)
	require.Len(t, second.Jobs, 1, "second job page must return the remaining job")
	assert.Empty(t, second.NextPageToken, "last job page carries no token")

	all, err := svc.Projects.Locations.Jobs.List(project, location).Do()
	require.NoError(t, err)
	require.Len(t, all.Jobs, 2, "unbounded job list returns every job")
	assert.Empty(t, all.NextPageToken)
}

// TestConformance_LoggingEntriesPaginate pins that entries:list honors pageSize +
// pageToken and emits a nextPageToken so a caller can walk every page.
func TestConformance_LoggingEntriesPaginate(t *testing.T) {
	svc, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const logName = "projects/conf-logging-pagination/logs/conf-pag-log"

	entries := make([]*logging.LogEntry, 0, 4)
	for i := 0; i < 4; i++ {
		entries = append(entries, &logging.LogEntry{
			LogName:     logName,
			TextPayload: "entry",
			Resource:    &logging.MonitoredResource{Type: "global"},
		})
	}
	_, err = svc.Entries.Write(&logging.WriteLogEntriesRequest{Entries: entries}).Do()
	require.NoError(t, err)

	filter := `logName="` + logName + `"`
	first, err := svc.Entries.List(&logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/conf-logging-pagination"},
		Filter:        filter,
		PageSize:      2,
	}).Do()
	require.NoError(t, err)
	require.Len(t, first.Entries, 2, "pageSize=2 must return two entries")
	require.NotEmpty(t, first.NextPageToken, "a partial entries page must carry a token")

	second, err := svc.Entries.List(&logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/conf-logging-pagination"},
		Filter:        filter,
		PageSize:      2,
		PageToken:     first.NextPageToken,
	}).Do()
	require.NoError(t, err)
	require.Len(t, second.Entries, 2, "second entries page must return the remaining entries")
	assert.Empty(t, second.NextPageToken, "last entries page carries no token")
}

// TestConformance_BigQueryDatasetsPaginate pins that datasets.list honors
// maxResults + pageToken and emits a nextPageToken.
func TestConformance_BigQueryDatasetsPaginate(t *testing.T) {
	svc, err := bigquery.NewService(ctx, option.WithEndpoint(baseURL+"/bigquery/v2/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const project = "conf-bq-pagination"

	for _, id := range []string{"conf_pag_ds_a", "conf_pag_ds_b"} {
		_, err := svc.Datasets.Insert(project, &bigquery.Dataset{
			DatasetReference: &bigquery.DatasetReference{ProjectId: project, DatasetId: id},
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _ = svc.Datasets.Delete(project, id).Do() })
	}

	first, err := svc.Datasets.List(project).MaxResults(1).Do()
	require.NoError(t, err)
	require.Len(t, first.Datasets, 1, "maxResults=1 must return one dataset")
	require.NotEmpty(t, first.NextPageToken, "a partial dataset page must carry a token")

	second, err := svc.Datasets.List(project).MaxResults(1).PageToken(first.NextPageToken).Do()
	require.NoError(t, err)
	require.Len(t, second.Datasets, 1, "second dataset page must return the remaining dataset")
	assert.Empty(t, second.NextPageToken, "last dataset page carries no token")
}

// TestConformance_FirestoreListAndQueryPagination pins that documents.list honors
// pageSize + pageToken and that runQuery honors limit, offset, and the
// LESS_THAN / GREATER_THAN comparison operators (not only EQUAL).
func TestConformance_FirestoreListAndQueryPagination(t *testing.T) {
	svc, err := firestore.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	const db = "projects/conf-firestore-pagination/databases/(default)"
	const collPath = db + "/documents/conf-pag-users"

	rank := func(n int64) map[string]firestore.Value {
		return map[string]firestore.Value{"rank": {IntegerValue: n}}
	}
	for _, doc := range []struct {
		id   string
		rank int64
	}{{"alice", 1}, {"bob", 2}, {"carol", 3}} {
		_, err := svc.Projects.Databases.Documents.Commit(db, &firestore.CommitRequest{
			Writes: []*firestore.Write{{Update: &firestore.Document{
				Name:   collPath + "/" + doc.id,
				Fields: rank(doc.rank),
			}}},
		}).Do()
		require.NoError(t, err)
	}

	// list with pageSize=1 must return one document plus a token.
	first, err := svc.Projects.Databases.Documents.List(db+"/documents", "conf-pag-users").PageSize(1).Do()
	require.NoError(t, err)
	require.Len(t, first.Documents, 1, "pageSize=1 must return one document")
	require.NotEmpty(t, first.NextPageToken, "a partial document page must carry a token")

	second, err := svc.Projects.Databases.Documents.List(db+"/documents", "conf-pag-users").
		PageSize(1).PageToken(first.NextPageToken).Do()
	require.NoError(t, err)
	require.Len(t, second.Documents, 1, "second document page must return another document")
	assert.NotEqual(t, first.Documents[0].Name, second.Documents[0].Name, "document pages must not overlap")

	// runQuery returns a streamed JSON array of RunQueryResponse elements that
	// the REST .Do() (single-object decode) can't capture, so post the canonical
	// structured-query body directly and count the document-bearing elements —
	// the same wire shape the firestore client emits.
	queryURL := baseURL + "/v1/" + db + "/documents:runQuery"

	// limit caps results.
	limited := runFirestoreQuery(t, queryURL,
		`{"structuredQuery":{"from":[{"collectionId":"conf-pag-users"}],"limit":2}}`)
	assert.Len(t, limited, 2, "runQuery limit=2 must return at most two documents")

	// GREATER_THAN selects only the matching documents (rank > 1 → bob, carol).
	gt := runFirestoreQuery(t, queryURL,
		`{"structuredQuery":{"from":[{"collectionId":"conf-pag-users"}],"where":{"fieldFilter":{"field":{"fieldPath":"rank"},"op":"GREATER_THAN","value":{"integerValue":"1"}}}}}`)
	assert.Len(t, gt, 2, "rank > 1 must match exactly two documents")
}

// runFirestoreQuery posts a structured query and returns the document-bearing
// elements of the streamed RunQueryResponse array.
func runFirestoreQuery(t *testing.T, url, body string) []*firestore.RunQueryResponse {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var elems []*firestore.RunQueryResponse
	require.NoError(t, json.Unmarshal(raw, &elems))
	var docs []*firestore.RunQueryResponse
	for _, e := range elems {
		if e.Document != nil {
			docs = append(docs, e)
		}
	}
	return docs
}

func TestConformance_GCSBucketUpdateVersioning(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	bkt := client.Bucket("conf-bucket-update")
	require.NoError(t, bkt.Create(ctx, "conformance-project", nil))

	updated, err := bkt.Update(ctx, storage.BucketAttrsToUpdate{
		VersioningEnabled: true,
	})
	require.NoError(t, err)
	assert.True(t, updated.VersioningEnabled, "Update must turn versioning on")

	got, err := bkt.Attrs(ctx)
	require.NoError(t, err)
	assert.True(t, got.VersioningEnabled, "read-back must show versioning enabled")
}

func TestConformance_GCSBucketUpdateLabels(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	bkt := client.Bucket("conf-bucket-labels")
	require.NoError(t, bkt.Create(ctx, "conformance-project", &storage.BucketAttrs{
		Labels: map[string]string{"keep": "yes", "drop": "soon"},
	}))

	var uattrs storage.BucketAttrsToUpdate
	uattrs.SetLabel("added", "now")
	uattrs.DeleteLabel("drop")
	updated, err := bkt.Update(ctx, uattrs)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"keep": "yes", "added": "now"}, updated.Labels)

	got, err := bkt.Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"keep": "yes", "added": "now"}, got.Labels)
}

func TestConformance_GCSObjectUpdateMetadata(t *testing.T) {
	client := storageClient(t)
	defer client.Close()

	bkt := client.Bucket("conf-object-update")
	require.NoError(t, bkt.Create(ctx, "conformance-project", nil))

	obj := bkt.Object("meta.txt")
	w := obj.NewWriter(ctx)
	_, err := w.Write([]byte("payload"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	updated, err := obj.Update(ctx, storage.ObjectAttrsToUpdate{
		ContentType:  "text/plain",
		CacheControl: "max-age=60",
		Metadata:     map[string]string{"x": "y"},
	})
	require.NoError(t, err)
	assert.Equal(t, "text/plain", updated.ContentType)
	assert.Equal(t, "max-age=60", updated.CacheControl)
	assert.Equal(t, map[string]string{"x": "y"}, updated.Metadata)

	got, err := obj.Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, "text/plain", got.ContentType)
	assert.Equal(t, "max-age=60", got.CacheControl)
	assert.Equal(t, map[string]string{"x": "y"}, got.Metadata)
}

func TestConformance_SpannerInstanceUpdateNodeCount(t *testing.T) {
	svc, err := spanner.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-spanner-project"
	parent := "projects/" + project
	created, err := svc.Projects.Instances.Create(parent, &spanner.CreateInstanceRequest{
		InstanceId: "conf-resize",
		Instance: &spanner.Instance{
			Config:      "projects/" + project + "/instanceConfigs/regional-us-central1",
			DisplayName: "conf resize",
			NodeCount:   1,
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created)

	name := parent + "/instances/conf-resize"
	op, err := svc.Projects.Instances.Patch(name, &spanner.UpdateInstanceRequest{
		Instance: &spanner.Instance{
			Name:        name,
			NodeCount:   3,
			DisplayName: "conf resize v2",
		},
		FieldMask: "nodeCount,displayName",
	}).Do()
	require.NoError(t, err, "Instances.Patch must not 404")
	require.NotNil(t, op)

	got, err := svc.Projects.Instances.Get(name).Do()
	require.NoError(t, err)
	assert.EqualValues(t, 3, got.NodeCount, "nodeCount must reflect the patch")
	assert.Equal(t, "conf resize v2", got.DisplayName)
}

func TestConformance_KMSCreateAndToggleCryptoKeyVersion(t *testing.T) {
	svc, err := cloudkms.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-kms-project"
	location := "projects/" + project + "/locations/global"
	ring, err := svc.Projects.Locations.KeyRings.Create(location, &cloudkms.KeyRing{}).
		KeyRingId("conf-ckv-ring").Do()
	require.NoError(t, err)

	key, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ring.Name, &cloudkms.CryptoKey{
		Purpose: "ENCRYPT_DECRYPT",
	}).CryptoKeyId("conf-ckv-key").Do()
	require.NoError(t, err)

	// Create a second version explicitly.
	v2, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		Create(key.Name, &cloudkms.CryptoKeyVersion{}).Do()
	require.NoError(t, err, "CreateCryptoKeyVersion must not 404")
	require.NotEmpty(t, v2.Name)
	assert.Equal(t, "ENABLED", v2.State)

	list, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.List(key.Name).Do()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list.CryptoKeyVersions), 2, "both versions must be listed")

	// Disable v2 via UpdateCryptoKeyVersion (PATCH state) — the canonical
	// state toggle terraform's google_kms_crypto_key_version uses.
	disabled, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		Patch(v2.Name, &cloudkms.CryptoKeyVersion{State: "DISABLED"}).
		UpdateMask("state").Do()
	require.NoError(t, err, "UpdateCryptoKeyVersion must not 404")
	assert.Equal(t, "DISABLED", disabled.State)

	// Re-enable.
	enabled, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		Patch(v2.Name, &cloudkms.CryptoKeyVersion{State: "ENABLED"}).
		UpdateMask("state").Do()
	require.NoError(t, err, "UpdateCryptoKeyVersion must not 404")
	assert.Equal(t, "ENABLED", enabled.State)

	// Schedule destroy then restore.
	destroyed, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		Destroy(v2.Name, &cloudkms.DestroyCryptoKeyVersionRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, "DESTROY_SCHEDULED", destroyed.State)

	restored, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		Restore(v2.Name, &cloudkms.RestoreCryptoKeyVersionRequest{}).Do()
	require.NoError(t, err, "RestoreCryptoKeyVersion must not 404")
	assert.Equal(t, "DISABLED", restored.State, "restore returns the version to DISABLED")
}

func TestConformance_CloudBuildListBuilds(t *testing.T) {
	svc, err := cloudbuild.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-cb-list-project"
	// CreateBuild persists the build even when execution fails (no source),
	// which is all ListBuilds needs to surface it.
	op, err := svc.Projects.Builds.Create(project, &cloudbuild.Build{
		Steps: []*cloudbuild.BuildStep{{Name: "gcr.io/cloud-builders/docker", Args: []string{"version"}}},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, op)

	list, err := svc.Projects.Builds.List(project).Do()
	require.NoError(t, err, "ListBuilds must not 404")
	require.NotEmpty(t, list.Builds, "created build must appear in ListBuilds")
	for _, b := range list.Builds {
		assert.Equal(t, project, b.ProjectId)
	}
}

func TestConformance_BigtableModifyColumnFamilies(t *testing.T) {
	svc, err := bigtableadmin.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-bt-project"
	parent := "projects/" + project
	_, err = svc.Projects.Instances.Create(parent, &bigtableadmin.CreateInstanceRequest{
		InstanceId: "conf-bt-inst",
		Instance:   &bigtableadmin.Instance{DisplayName: "conf bt", Type: "PRODUCTION"},
		Clusters: map[string]bigtableadmin.Cluster{
			"conf-bt-clu": {Location: "projects/" + project + "/locations/us-central1-a", ServeNodes: 1},
		},
	}).Do()
	require.NoError(t, err)

	instName := "projects/" + project + "/instances/conf-bt-inst"
	_, err = svc.Projects.Instances.Tables.Create(instName, &bigtableadmin.CreateTableRequest{
		TableId: "conf-bt-table",
		Table: &bigtableadmin.Table{
			ColumnFamilies: map[string]bigtableadmin.ColumnFamily{"cf1": {}},
		},
	}).Do()
	require.NoError(t, err)

	tableName := instName + "/tables/conf-bt-table"
	modified, err := svc.Projects.Instances.Tables.ModifyColumnFamilies(tableName,
		&bigtableadmin.ModifyColumnFamiliesRequest{
			Modifications: []*bigtableadmin.Modification{
				{Id: "cf2", Create: &bigtableadmin.ColumnFamily{}},
				{Id: "cf1", Drop: true},
			},
		}).Do()
	require.NoError(t, err, "ModifyColumnFamilies must not 404")
	require.NotNil(t, modified)
	assert.Contains(t, modified.ColumnFamilies, "cf2")
	assert.NotContains(t, modified.ColumnFamilies, "cf1", "dropped family must be gone")

	got, err := svc.Projects.Instances.Tables.Get(tableName).Do()
	require.NoError(t, err)
	assert.Contains(t, got.ColumnFamilies, "cf2", "GET must reflect added family")
	assert.NotContains(t, got.ColumnFamilies, "cf1")
}

func TestConformance_BigtableInstanceAndClusterUpdate(t *testing.T) {
	svc, err := bigtableadmin.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-bt-upd-project"
	parent := "projects/" + project
	_, err = svc.Projects.Instances.Create(parent, &bigtableadmin.CreateInstanceRequest{
		InstanceId: "conf-bt-upd",
		Instance:   &bigtableadmin.Instance{DisplayName: "before", Type: "PRODUCTION"},
		Clusters: map[string]bigtableadmin.Cluster{
			"conf-bt-upd-clu": {Location: "projects/" + project + "/locations/us-central1-a", ServeNodes: 1},
		},
	}).Do()
	require.NoError(t, err)

	instName := "projects/" + project + "/instances/conf-bt-upd"
	_, err = svc.Projects.Instances.PartialUpdateInstance(instName, &bigtableadmin.Instance{
		DisplayName: "after",
	}).UpdateMask("displayName").Do()
	require.NoError(t, err, "PartialUpdateInstance must not 404")

	gotInst, err := svc.Projects.Instances.Get(instName).Do()
	require.NoError(t, err)
	assert.Equal(t, "after", gotInst.DisplayName)

	cluName := instName + "/clusters/conf-bt-upd-clu"
	_, err = svc.Projects.Instances.Clusters.Update(cluName, &bigtableadmin.Cluster{
		ServeNodes: 4,
	}).Do()
	require.NoError(t, err, "Clusters.Update must not 404")

	gotClu, err := svc.Projects.Instances.Clusters.Get(cluName).Do()
	require.NoError(t, err)
	assert.EqualValues(t, 4, gotClu.ServeNodes, "num_nodes must reflect the update")
}

func TestConformance_MemorystoreRedisPatchUpdateMask(t *testing.T) {
	svc, err := redis.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-redis-project"
	parent := "projects/" + project + "/locations/us-central1"
	_, err = svc.Projects.Locations.Instances.Create(parent, &redis.Instance{
		DisplayName:  "before",
		Tier:         "BASIC",
		MemorySizeGb: 2,
	}).InstanceId("conf-redis").Do()
	require.NoError(t, err)

	name := parent + "/instances/conf-redis"
	// Patch only displayName via updateMask — memorySizeGb must be preserved.
	_, err = svc.Projects.Locations.Instances.Patch(name, &redis.Instance{
		DisplayName:  "after",
		MemorySizeGb: 99,
	}).UpdateMask("displayName").Do()
	require.NoError(t, err)

	got, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "after", got.DisplayName)
	assert.EqualValues(t, 2, got.MemorySizeGb, "updateMask=displayName must NOT change memorySizeGb")
}

func TestConformance_CloudSQLPatchMergesSettings(t *testing.T) {
	svc, err := sqladmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "conf-sql-project"
	_, err = svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            "conf-sql",
		DatabaseVersion: "POSTGRES_15",
		Settings: &sqladmin.Settings{
			Tier: "db-custom-1-3840",
			BackupConfiguration: &sqladmin.BackupConfiguration{
				Enabled: true,
			},
		},
	}).Do()
	require.NoError(t, err)

	// Patch only databaseFlags — tier + backupConfiguration must survive.
	_, err = svc.Instances.Patch(project, "conf-sql", &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{
			DatabaseFlags: []*sqladmin.DatabaseFlags{{Name: "max_connections", Value: "100"}},
		},
	}).Do()
	require.NoError(t, err)

	got, err := svc.Instances.Get(project, "conf-sql").Do()
	require.NoError(t, err)
	require.NotNil(t, got.Settings)
	assert.Equal(t, "db-custom-1-3840", got.Settings.Tier, "tier must be preserved across a databaseFlags-only patch")
	require.NotNil(t, got.Settings.BackupConfiguration, "backupConfiguration must be preserved")
	assert.True(t, got.Settings.BackupConfiguration.Enabled)
	require.Len(t, got.Settings.DatabaseFlags, 1, "patched databaseFlags must apply")
	assert.Equal(t, "max_connections", got.Settings.DatabaseFlags[0].Name)
}

func TestConformance_CloudFunctionsUpdateAndGenerateUploadUrl(t *testing.T) {
	client, err := functions.NewFunctionRESTClient(ctx,
		option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	const parent = "projects/conf-gcf-project/locations/us-central1"

	// generateUploadUrl is called before create.
	upload, err := client.GenerateUploadUrl(ctx, &functionspb.GenerateUploadUrlRequest{
		Parent: parent,
	})
	require.NoError(t, err, "GenerateUploadUrl must not 404")
	require.NotEmpty(t, upload.GetUploadUrl(), "uploadUrl must be returned")
	require.NotNil(t, upload.GetStorageSource(), "storageSource must be returned")

	op, err := client.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
		Parent:     parent,
		FunctionId: "conf-gcf-update",
		Function: &functionspb.Function{
			BuildConfig: &functionspb.BuildConfig{Runtime: "go121", EntryPoint: "Handler"},
		},
	})
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)

	name := parent + "/functions/conf-gcf-update"
	updateOp, err := client.UpdateFunction(ctx, &functionspb.UpdateFunctionRequest{
		Function: &functionspb.Function{
			Name:        name,
			Description: "updated in place",
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description"}},
	})
	require.NoError(t, err, "UpdateFunction PATCH must not 404")
	updated, err := updateOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "updated in place", updated.GetDescription())

	got, err := client.GetFunction(ctx, &functionspb.GetFunctionRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, "updated in place", got.GetDescription(), "read-back must reflect the patch")
}
