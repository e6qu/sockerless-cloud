package gcp_sdk_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
)

func pubsubService(t *testing.T) *pubsub.Service {
	t.Helper()
	svc, err := pubsub.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestPubSub_TopicAndSubscriptionLifecycle exercises the canonical
// fan-out flow: create topic → create subscription → publish → pull
// → ack. Regression guard (Pub/Sub slice).
func TestPubSub_TopicAndSubscriptionLifecycle(t *testing.T) {
	svc := pubsubService(t)
	project := "test-project"
	topicName := "projects/" + project + "/topics/lifecycle-topic"
	subName := "projects/" + project + "/subscriptions/lifecycle-sub"

	topic, err := svc.Projects.Topics.Create(topicName, &pubsub.Topic{Name: topicName}).Do()
	require.NoError(t, err)
	assert.Equal(t, topicName, topic.Name)
	t.Cleanup(func() {
		_, _ = svc.Projects.Topics.Delete(topicName).Do()
	})

	// List should include the new topic.
	list, err := svc.Projects.Topics.List("projects/" + project).Do()
	require.NoError(t, err)
	found := false
	for _, x := range list.Topics {
		if x.Name == topicName {
			found = true
			break
		}
	}
	assert.True(t, found, "topic should appear in ListTopics")

	// Create subscription.
	sub, err := svc.Projects.Subscriptions.Create(subName, &pubsub.Subscription{
		Topic:              topicName,
		AckDeadlineSeconds: 30,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, topicName, sub.Topic)
	assert.Equal(t, int64(30), sub.AckDeadlineSeconds)
	t.Cleanup(func() {
		_, _ = svc.Projects.Subscriptions.Delete(subName).Do()
	})

	// Publish two messages.
	pubResp, err := svc.Projects.Topics.Publish(topicName, &pubsub.PublishRequest{
		Messages: []*pubsub.PubsubMessage{
			{Data: base64.StdEncoding.EncodeToString([]byte("hello"))},
			{Data: base64.StdEncoding.EncodeToString([]byte("world"))},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, pubResp.MessageIds, 2)

	// Pull them back.
	pullResp, err := svc.Projects.Subscriptions.Pull(subName, &pubsub.PullRequest{
		MaxMessages: 10,
	}).Do()
	require.NoError(t, err)
	require.Len(t, pullResp.ReceivedMessages, 2)

	// Verify payloads decode correctly.
	bodies := map[string]bool{}
	var ackIds []string
	for _, m := range pullResp.ReceivedMessages {
		assertProtoJSONTimestamp(t, m.Message.PublishTime)
		decoded, err := base64.StdEncoding.DecodeString(m.Message.Data)
		require.NoError(t, err)
		bodies[string(decoded)] = true
		ackIds = append(ackIds, m.AckId)
	}
	assert.True(t, bodies["hello"], "expected 'hello' payload")
	assert.True(t, bodies["world"], "expected 'world' payload")

	// Acknowledge.
	_, err = svc.Projects.Subscriptions.Acknowledge(subName, &pubsub.AcknowledgeRequest{
		AckIds: ackIds,
	}).Do()
	require.NoError(t, err)

	// Second pull should be empty (queue drained by ack).
	pullResp2, err := svc.Projects.Subscriptions.Pull(subName, &pubsub.PullRequest{
		MaxMessages: 10,
	}).Do()
	require.NoError(t, err)
	assert.Empty(t, pullResp2.ReceivedMessages,
		"subscription queue should be empty after ack")
}

// TestPubSub_NonExistentTopic confirms canonical 404 envelope.
func TestPubSub_NonExistentTopic(t *testing.T) {
	svc := pubsubService(t)
	_, err := svc.Projects.Topics.Get("projects/test-project/topics/missing").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Topic not found")
}

// TestPubSub_PatchSubscription exercises projects.subscriptions.patch.
// The terraform-provider-google + the SDK's *ProjectsSubscriptionsService.Patch
// both emit this verb to mutate ackDeadlineSeconds, messageRetention,
// pushConfig, etc. without recreating the subscription. updateMask
// selects which fields apply; fields not in the mask retain prior values.
func TestPubSub_PatchSubscription(t *testing.T) {
	svc := pubsubService(t)
	project := "test-project"
	topicName := "projects/" + project + "/topics/patch-topic"
	subName := "projects/" + project + "/subscriptions/patch-sub"

	_, err := svc.Projects.Topics.Create(topicName, &pubsub.Topic{Name: topicName}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(topicName).Do() })

	sub, err := svc.Projects.Subscriptions.Create(subName, &pubsub.Subscription{
		Topic:                    topicName,
		AckDeadlineSeconds:       10,
		MessageRetentionDuration: "604800s",
		Filter:                   "attributes.kind = \"news\"",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, int64(10), sub.AckDeadlineSeconds)
	require.Equal(t, "604800s", sub.MessageRetentionDuration)
	t.Cleanup(func() { _, _ = svc.Projects.Subscriptions.Delete(subName).Do() })

	// PATCH ackDeadlineSeconds + messageRetentionDuration only; Filter
	// stays unchanged because it's not in the mask.
	patched, err := svc.Projects.Subscriptions.Patch(subName, &pubsub.UpdateSubscriptionRequest{
		Subscription: &pubsub.Subscription{
			AckDeadlineSeconds:       60,
			MessageRetentionDuration: "86400s",
			Filter:                   "this-should-not-apply",
		},
		UpdateMask: "ackDeadlineSeconds,messageRetentionDuration",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(60), patched.AckDeadlineSeconds, "ackDeadlineSeconds should be updated")
	assert.Equal(t, "86400s", patched.MessageRetentionDuration, "messageRetentionDuration should be updated")
	assert.Equal(t, "attributes.kind = \"news\"", patched.Filter,
		"Filter must be unchanged — not in updateMask")

	// GET back to confirm persistence.
	got, err := svc.Projects.Subscriptions.Get(subName).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(60), got.AckDeadlineSeconds)
	assert.Equal(t, "86400s", got.MessageRetentionDuration)
	assert.Equal(t, "attributes.kind = \"news\"", got.Filter)

	// Unknown updateMask paths must return 400 InvalidArgument —
	// silently skipping would hide a caller-side typo.
	_, err = svc.Projects.Subscriptions.Patch(subName, &pubsub.UpdateSubscriptionRequest{
		Subscription: &pubsub.Subscription{Labels: map[string]string{"env": "test"}},
		UpdateMask:   "labels,unknownField",
	}).Do()
	require.Error(t, err, "unknown updateMask path must surface as 400")
	assert.Contains(t, err.Error(), "unknown updateMask path")

	// Confirm the failed PATCH was atomic — the prior `labels` path
	// in the same mask must NOT have been applied (the request as
	// a whole rejected, not partially persisted).
	got2, err := svc.Projects.Subscriptions.Get(subName).Do()
	require.NoError(t, err)
	assert.Empty(t, got2.Labels, "failed PATCH must not partially apply labels")
}

// TestPubSub_PatchTopic exercises projects.topics.patch. The only
// mutable field on real Pub/Sub topics today is `labels`.
func TestPubSub_PatchTopic(t *testing.T) {
	svc := pubsubService(t)
	topicName := "projects/test-project/topics/patch-topic-labels"
	_, err := svc.Projects.Topics.Create(topicName, &pubsub.Topic{
		Name:   topicName,
		Labels: map[string]string{"env": "before"},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(topicName).Do() })

	_, err = svc.Projects.Topics.Patch(topicName, &pubsub.UpdateTopicRequest{
		Topic:      &pubsub.Topic{Labels: map[string]string{"env": "after", "team": "platform"}},
		UpdateMask: "labels",
	}).Do()
	require.NoError(t, err)

	got, err := svc.Projects.Topics.Get(topicName).Do()
	require.NoError(t, err)
	assert.Equal(t, "after", got.Labels["env"])
	assert.Equal(t, "platform", got.Labels["team"])
}

func TestPubSub_TopicListPagination(t *testing.T) {
	svc := pubsubService(t)
	proj := "pagination-ps-project"

	// Create 3 topics.
	topics := []string{
		"projects/" + proj + "/topics/topic-a",
		"projects/" + proj + "/topics/topic-b",
		"projects/" + proj + "/topics/topic-c",
	}
	for _, name := range topics {
		_, err := svc.Projects.Topics.Create(name, &pubsub.Topic{Name: name}).Do()
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, name := range topics {
			_, _ = svc.Projects.Topics.Delete(name).Do()
		}
	})

	// Page through with pageSize=1.
	var got []string
	pageToken := ""
	for {
		call := svc.Projects.Topics.List("projects/" + proj).PageSize(1)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		require.NoError(t, err)
		require.Len(t, resp.Topics, 1, "each page must have exactly 1 topic with pageSize=1")
		got = append(got, resp.Topics[0].Name)
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	require.Len(t, got, 3)
	for _, name := range topics {
		require.Contains(t, got, name)
	}
}

func TestPubSub_SubscriptionListPagination(t *testing.T) {
	svc := pubsubService(t)
	proj := "pagination-ps-sub-project"
	topicName := "projects/" + proj + "/topics/pagination-topic"

	_, err := svc.Projects.Topics.Create(topicName, &pubsub.Topic{Name: topicName}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(topicName).Do() })

	subs := []string{
		"projects/" + proj + "/subscriptions/sub-a",
		"projects/" + proj + "/subscriptions/sub-b",
		"projects/" + proj + "/subscriptions/sub-c",
	}
	for _, name := range subs {
		_, err = svc.Projects.Subscriptions.Create(name, &pubsub.Subscription{
			Name:  name,
			Topic: topicName,
		}).Do()
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, name := range subs {
			_, _ = svc.Projects.Subscriptions.Delete(name).Do()
		}
	})

	// Page through with pageSize=1.
	var got []string
	pageToken := ""
	for {
		call := svc.Projects.Subscriptions.List("projects/" + proj).PageSize(1)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		require.NoError(t, err)
		require.Len(t, resp.Subscriptions, 1, "each page must have exactly 1 subscription with pageSize=1")
		got = append(got, resp.Subscriptions[0].Name)
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	require.Len(t, got, 3)
	for _, name := range subs {
		require.Contains(t, got, name)
	}
}
