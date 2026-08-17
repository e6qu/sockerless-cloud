package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pubsub "google.golang.org/api/pubsub/v1"
)

// avroDef is a minimal valid Avro schema definition used across the
// schema round-trip tests.
const avroDef = `{"type":"record","name":"Avro","fields":[{"name":"id","type":"string"}]}`

// TestPubSub_SchemaLifecycle exercises the Schemas resource end-to-end:
// create → get → list → commit (new revision) → listRevisions →
// rollback → deleteRevision → delete.
func TestPubSub_SchemaLifecycle(t *testing.T) {
	svc := pubsubService(t)
	const project = "schema-project"
	parent := "projects/" + project
	schemaName := parent + "/schemas/avro-schema"

	created, err := svc.Projects.Schemas.Create(parent, &pubsub.Schema{
		Type:       "AVRO",
		Definition: avroDef,
	}).SchemaId("avro-schema").Do()
	require.NoError(t, err)
	assert.Equal(t, schemaName, created.Name)
	assert.Equal(t, "AVRO", created.Type)
	assert.NotEmpty(t, created.RevisionId, "create yields a revision id")
	assert.NotEmpty(t, created.RevisionCreateTime)
	firstRev := created.RevisionId

	got, err := svc.Projects.Schemas.Get(schemaName).Do()
	require.NoError(t, err)
	assert.Equal(t, schemaName, got.Name)
	assert.Equal(t, avroDef, got.Definition)

	// BASIC view omits the definition.
	basic, err := svc.Projects.Schemas.Get(schemaName).View("BASIC").Do()
	require.NoError(t, err)
	assert.Empty(t, basic.Definition, "BASIC view omits the definition")

	list, err := svc.Projects.Schemas.List(parent).Do()
	require.NoError(t, err)
	var found bool
	for _, s := range list.Schemas {
		if s.Name == schemaName {
			found = true
		}
	}
	assert.True(t, found, "created schema appears in the list")

	// Commit a second revision (mutated definition).
	newDef := `{"type":"record","name":"Avro","fields":[{"name":"id","type":"string"},{"name":"n","type":"long"}]}`
	committed, err := svc.Projects.Schemas.Commit(schemaName, &pubsub.CommitSchemaRequest{
		Schema: &pubsub.Schema{Name: schemaName, Type: "AVRO", Definition: newDef},
	}).Do()
	require.NoError(t, err)
	assert.NotEqual(t, firstRev, committed.RevisionId, "commit yields a fresh revision id")
	secondRev := committed.RevisionId

	revs, err := svc.Projects.Schemas.ListRevisions(schemaName).Do()
	require.NoError(t, err)
	assert.Len(t, revs.Schemas, 2, "two revisions after one commit")

	// Rollback to the first revision creates a third revision matching it.
	rolled, err := svc.Projects.Schemas.Rollback(schemaName, &pubsub.RollbackSchemaRequest{
		RevisionId: firstRev,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, avroDef, rolled.Definition, "rollback restores the first revision's definition")
	assert.NotEqual(t, firstRev, rolled.RevisionId)

	// Three revisions exist at this point: the create, the commit, the rollback.
	revsBefore, err := svc.Projects.Schemas.ListRevisions(schemaName).Do()
	require.NoError(t, err)
	require.Len(t, revsBefore.Schemas, 3, "create + commit + rollback are three revisions")

	// DeleteRevision removes the revision it names — that one and no other.
	_, err = svc.Projects.Schemas.DeleteRevision(schemaName).RevisionId(secondRev).Do()
	require.NoError(t, err)
	revs2, err := svc.Projects.Schemas.ListRevisions(schemaName).Do()
	require.NoError(t, err)
	assert.Len(t, revs2.Schemas, 2, "two revisions remain after deleting one of three")
	remaining := make([]string, 0, len(revs2.Schemas))
	for _, s := range revs2.Schemas {
		remaining = append(remaining, s.RevisionId)
	}
	assert.NotContains(t, remaining, secondRev, "the named revision is the one that went")
	assert.ElementsMatch(t, []string{firstRev, rolled.RevisionId}, remaining,
		"the revisions the delete did not name are all still there")

	// A revision id the schema does not have is NOT_FOUND, so the delete is
	// resolving the id rather than dropping whichever revision it reaches first.
	_, err = svc.Projects.Schemas.DeleteRevision(schemaName).RevisionId(secondRev).Do()
	require.Error(t, err, "the revision is already gone")

	_, err = svc.Projects.Schemas.Delete(schemaName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Schemas.Get(schemaName).Do()
	assert.Error(t, err, "schema gone after delete")
}

// TestPubSub_SchemaValidate covers the collection-level validate and
// validateMessage verbs, in both directions: what each verb accepts, and what
// each rejects with INVALID_ARGUMENT / NOT_FOUND. A verb that accepted
// everything would pass the acceptance half alone.
func TestPubSub_SchemaValidate(t *testing.T) {
	svc := pubsubService(t)
	const project = "schema-validate-project"
	parent := "projects/" + project

	// validate a well-formed schema.
	_, err := svc.Projects.Schemas.Validate(parent, &pubsub.ValidateSchemaRequest{
		Schema: &pubsub.Schema{Type: "AVRO", Definition: avroDef},
	}).Do()
	require.NoError(t, err, "validate accepts a typed, non-empty schema")

	// A schema type outside the enum is INVALID_ARGUMENT.
	_, err = svc.Projects.Schemas.Validate(parent, &pubsub.ValidateSchemaRequest{
		Schema: &pubsub.Schema{Type: "BOGUS", Definition: avroDef},
	}).Do()
	require.Error(t, err, "validate rejects a type outside the schema enum")
	assert.Contains(t, err.Error(), "400")

	// So is a schema with no definition to validate.
	_, err = svc.Projects.Schemas.Validate(parent, &pubsub.ValidateSchemaRequest{
		Schema: &pubsub.Schema{Type: "AVRO"},
	}).Do()
	require.Error(t, err, "validate rejects a schema carrying no definition")
	assert.Contains(t, err.Error(), "400")

	// validate against an ad-hoc schema + a message.
	_, err = svc.Projects.Schemas.ValidateMessage(parent, &pubsub.ValidateMessageRequest{
		Schema:   &pubsub.Schema{Type: "AVRO", Definition: avroDef},
		Message:  "eyJpZCI6ImEifQ==", // base64 {"id":"a"}
		Encoding: "JSON",
	}).Do()
	require.NoError(t, err, "validateMessage accepts an ad-hoc schema + message")

	// validateMessage against a named schema.
	named := parent + "/schemas/named-for-validate"
	_, err = svc.Projects.Schemas.Create(parent, &pubsub.Schema{Type: "AVRO", Definition: avroDef}).
		SchemaId("named-for-validate").Do()
	require.NoError(t, err)
	_, err = svc.Projects.Schemas.ValidateMessage(parent, &pubsub.ValidateMessageRequest{
		Name:     named,
		Message:  "eyJpZCI6ImEifQ==",
		Encoding: "JSON",
	}).Do()
	require.NoError(t, err, "validateMessage accepts a named schema + message")

	// A name no schema was created under is NOT_FOUND, so validateMessage is
	// resolving the schema it was pointed at.
	_, err = svc.Projects.Schemas.ValidateMessage(parent, &pubsub.ValidateMessageRequest{
		Name:     parent + "/schemas/no-such-schema",
		Message:  "eyJpZCI6ImEifQ==",
		Encoding: "JSON",
	}).Do()
	require.Error(t, err, "validateMessage rejects a schema name the project does not hold")
	assert.Contains(t, err.Error(), "404")

	// Neither a schema nor a name leaves nothing to validate against.
	_, err = svc.Projects.Schemas.ValidateMessage(parent, &pubsub.ValidateMessageRequest{
		Message:  "eyJpZCI6ImEifQ==",
		Encoding: "JSON",
	}).Do()
	require.Error(t, err, "validateMessage needs either a name or an inline schema")
	assert.Contains(t, err.Error(), "400")

	// A request with a schema but no message has nothing to validate.
	_, err = svc.Projects.Schemas.ValidateMessage(parent, &pubsub.ValidateMessageRequest{
		Schema:   &pubsub.Schema{Type: "AVRO", Definition: avroDef},
		Encoding: "JSON",
	}).Do()
	require.Error(t, err, "validateMessage requires the message it validates")
	assert.Contains(t, err.Error(), "400")
}

// TestPubSub_SchemaIAM round-trips an IAM policy on a schema resource
// and verifies testIamPermissions echoes the requested set.
func TestPubSub_SchemaIAM(t *testing.T) {
	svc := pubsubService(t)
	const project = "schema-iam-project"
	parent := "projects/" + project
	name := parent + "/schemas/iam-schema"
	_, err := svc.Projects.Schemas.Create(parent, &pubsub.Schema{Type: "AVRO", Definition: avroDef}).
		SchemaId("iam-schema").Do()
	require.NoError(t, err)
	assertIAMRoundTrip(t,
		func() (*pubsub.Policy, error) { return svc.Projects.Schemas.GetIamPolicy(name).Do() },
		func(p *pubsub.SetIamPolicyRequest) (*pubsub.Policy, error) {
			return svc.Projects.Schemas.SetIamPolicy(name, p).Do()
		},
		func(req *pubsub.TestIamPermissionsRequest) (*pubsub.TestIamPermissionsResponse, error) {
			return svc.Projects.Schemas.TestIamPermissions(name, req).Do()
		},
		"pubsub.schemas.get",
	)
}

// TestPubSub_ResourceIAM round-trips IAM policies on topics,
// subscriptions, and snapshots — the AIP-141 three-verb cluster on each.
func TestPubSub_ResourceIAM(t *testing.T) {
	svc := pubsubService(t)
	const project = "ps-iam-project"
	parent := "projects/" + project
	topic := parent + "/topics/iam-topic"
	sub := parent + "/subscriptions/iam-sub"
	snap := parent + "/snapshots/iam-snap"

	_, err := svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Snapshots.Create(snap, &pubsub.CreateSnapshotRequest{Subscription: sub}).Do()
	require.NoError(t, err)

	t.Run("topic", func(t *testing.T) {
		assertIAMRoundTrip(t,
			func() (*pubsub.Policy, error) { return svc.Projects.Topics.GetIamPolicy(topic).Do() },
			func(p *pubsub.SetIamPolicyRequest) (*pubsub.Policy, error) {
				return svc.Projects.Topics.SetIamPolicy(topic, p).Do()
			},
			func(req *pubsub.TestIamPermissionsRequest) (*pubsub.TestIamPermissionsResponse, error) {
				return svc.Projects.Topics.TestIamPermissions(topic, req).Do()
			},
			"pubsub.topics.get",
		)
	})
	t.Run("subscription", func(t *testing.T) {
		assertIAMRoundTrip(t,
			func() (*pubsub.Policy, error) { return svc.Projects.Subscriptions.GetIamPolicy(sub).Do() },
			func(p *pubsub.SetIamPolicyRequest) (*pubsub.Policy, error) {
				return svc.Projects.Subscriptions.SetIamPolicy(sub, p).Do()
			},
			func(req *pubsub.TestIamPermissionsRequest) (*pubsub.TestIamPermissionsResponse, error) {
				return svc.Projects.Subscriptions.TestIamPermissions(sub, req).Do()
			},
			"pubsub.subscriptions.get",
		)
	})
	t.Run("snapshot", func(t *testing.T) {
		assertIAMRoundTrip(t,
			func() (*pubsub.Policy, error) { return svc.Projects.Snapshots.GetIamPolicy(snap).Do() },
			func(p *pubsub.SetIamPolicyRequest) (*pubsub.Policy, error) {
				return svc.Projects.Snapshots.SetIamPolicy(snap, p).Do()
			},
			func(req *pubsub.TestIamPermissionsRequest) (*pubsub.TestIamPermissionsResponse, error) {
				return svc.Projects.Snapshots.TestIamPermissions(snap, req).Do()
			},
			"pubsub.snapshots.get",
		)
	})
}

// assertIAMRoundTrip runs the getIamPolicy → setIamPolicy → getIamPolicy
// → testIamPermissions sequence. The policy half is the behavioural half: the
// binding the caller set is the binding a later read returns.
//
// The testIamPermissions half pins the route and the response shape only. The
// simulator serves a single caller who owns every resource, so the reply is the
// requested set — it is not an authorization decision about the binding above,
// and the permission asked for is therefore one the bound role really carries,
// so the assertion cannot be read as a claim the caller was granted something
// the policy withheld.
func assertIAMRoundTrip(
	t *testing.T,
	get func() (*pubsub.Policy, error),
	set func(*pubsub.SetIamPolicyRequest) (*pubsub.Policy, error),
	test func(*pubsub.TestIamPermissionsRequest) (*pubsub.TestIamPermissionsResponse, error),
	perm string,
) {
	t.Helper()
	pol, err := get()
	require.NoError(t, err, "getIamPolicy")
	require.NotNil(t, pol)

	pol.Bindings = []*pubsub.Binding{{
		Role:    "roles/pubsub.viewer",
		Members: []string{"user:viewer@example.com"},
	}}
	updated, err := set(&pubsub.SetIamPolicyRequest{Policy: pol})
	require.NoError(t, err, "setIamPolicy")
	require.Len(t, updated.Bindings, 1)
	assert.Equal(t, "roles/pubsub.viewer", updated.Bindings[0].Role)

	reread, err := get()
	require.NoError(t, err, "getIamPolicy after set")
	require.Len(t, reread.Bindings, 1, "binding persisted")
	assert.Equal(t, "user:viewer@example.com", reread.Bindings[0].Members[0])

	res, err := test(&pubsub.TestIamPermissionsRequest{Permissions: []string{perm}})
	require.NoError(t, err, "testIamPermissions")
	assert.Equal(t, []string{perm}, res.Permissions, "the response carries the requested set")
}

// TestPubSub_TopicSubCollections lists the snapshots and subscriptions
// that reference a topic via the topic sub-collection endpoints.
func TestPubSub_TopicSubCollections(t *testing.T) {
	svc := pubsubService(t)
	const project = "ps-subcoll-project"
	parent := "projects/" + project
	topic := parent + "/topics/subcoll-topic"
	sub := parent + "/subscriptions/subcoll-sub"
	snap := parent + "/snapshots/subcoll-snap"

	_, err := svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Snapshots.Create(snap, &pubsub.CreateSnapshotRequest{Subscription: sub}).Do()
	require.NoError(t, err)

	subs, err := svc.Projects.Topics.Subscriptions.List(topic).Do()
	require.NoError(t, err)
	assert.Contains(t, subs.Subscriptions, sub, "topic lists its subscription")

	snaps, err := svc.Projects.Topics.Snapshots.List(topic).Do()
	require.NoError(t, err)
	assert.Contains(t, snaps.Snapshots, snap, "topic lists its snapshot")
}

// TestPubSub_ModifyPushConfigAndDetach exercises the modifyPushConfig and
// detach verbs on a subscription.
func TestPubSub_ModifyPushConfigAndDetach(t *testing.T) {
	svc := pubsubService(t)
	const project = "ps-detach-project"
	parent := "projects/" + project
	topic := parent + "/topics/detach-topic"
	sub := parent + "/subscriptions/detach-sub"

	_, err := svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)

	// Convert to a push subscription, then back to pull (empty PushConfig).
	_, err = svc.Projects.Subscriptions.ModifyPushConfig(sub, &pubsub.ModifyPushConfigRequest{
		PushConfig: &pubsub.PushConfig{PushEndpoint: "https://example.com/push"},
	}).Do()
	require.NoError(t, err)
	got, err := svc.Projects.Subscriptions.Get(sub).Do()
	require.NoError(t, err)
	require.NotNil(t, got.PushConfig)
	assert.Equal(t, "https://example.com/push", got.PushConfig.PushEndpoint)

	_, err = svc.Projects.Subscriptions.ModifyPushConfig(sub, &pubsub.ModifyPushConfigRequest{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Subscriptions.Get(sub).Do()
	require.NoError(t, err)
	assert.Nil(t, got.PushConfig, "empty PushConfig converts back to a pull subscription")

	// Detach the subscription from its topic.
	_, err = svc.Projects.Subscriptions.Detach(sub).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Subscriptions.Get(sub).Do()
	require.NoError(t, err)
	assert.True(t, got.Detached, "subscription reports detached after detach")
}

// TestPubSub_SnapshotPatch updates a snapshot's labels via the patch verb.
func TestPubSub_SnapshotPatch(t *testing.T) {
	svc := pubsubService(t)
	const project = "ps-snappatch-project"
	parent := "projects/" + project
	topic := parent + "/topics/snappatch-topic"
	sub := parent + "/subscriptions/snappatch-sub"
	snap := parent + "/snapshots/snappatch-snap"

	_, err := svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Snapshots.Create(snap, &pubsub.CreateSnapshotRequest{Subscription: sub}).Do()
	require.NoError(t, err)

	patched, err := svc.Projects.Snapshots.Patch(snap, &pubsub.UpdateSnapshotRequest{
		Snapshot:   &pubsub.Snapshot{Name: snap, Labels: map[string]string{"team": "infra"}},
		UpdateMask: "labels",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "infra", patched.Labels["team"], "patch applies the labels field")
}
