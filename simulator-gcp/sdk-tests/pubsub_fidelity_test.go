package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pubsub "google.golang.org/api/pubsub/v1"
)

// TestPubSub_TopicSubscriptionFidelity proves the topic + subscription fields
// terraform-provider-google reads back round-trip: topic kms_key_name /
// message_retention_duration / message_storage_policy, and subscription
// dead_letter_policy / retry_policy — all were dropped before, drifting every
// plan.
func TestPubSub_TopicSubscriptionFidelity(t *testing.T) {
	svc := pubsubService(t)
	const project = "test-project"
	base := "projects/" + project

	topicName := base + "/topics/fidelity-topic"
	dlqName := base + "/topics/fidelity-dlq"
	for _, n := range []string{dlqName} {
		_, err := svc.Projects.Topics.Create(n, &pubsub.Topic{Name: n}).Do()
		require.NoError(t, err)
		defer svc.Projects.Topics.Delete(n).Do() //nolint:errcheck
	}

	// Topic with kms + retention + storage policy.
	topic, err := svc.Projects.Topics.Create(topicName, &pubsub.Topic{
		Name:                     topicName,
		KmsKeyName:               "projects/test-project/locations/us/keyRings/r/cryptoKeys/k",
		MessageRetentionDuration: "86400s",
		MessageStoragePolicy:     &pubsub.MessageStoragePolicy{AllowedPersistenceRegions: []string{"us-central1"}},
	}).Do()
	require.NoError(t, err)
	defer svc.Projects.Topics.Delete(topicName).Do() //nolint:errcheck

	gotTopic, err := svc.Projects.Topics.Get(topicName).Do()
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/locations/us/keyRings/r/cryptoKeys/k", gotTopic.KmsKeyName, "kms_key_name must round-trip")
	assert.Equal(t, "86400s", gotTopic.MessageRetentionDuration, "message_retention_duration must round-trip")
	require.NotNil(t, gotTopic.MessageStoragePolicy)
	assert.Equal(t, []string{"us-central1"}, gotTopic.MessageStoragePolicy.AllowedPersistenceRegions)
	_ = topic

	// Subscription with dead-letter + retry policy.
	subName := base + "/subscriptions/fidelity-sub"
	_, err = svc.Projects.Subscriptions.Create(subName, &pubsub.Subscription{
		Name:  subName,
		Topic: topicName,
		DeadLetterPolicy: &pubsub.DeadLetterPolicy{
			DeadLetterTopic: dlqName, MaxDeliveryAttempts: 7,
		},
		RetryPolicy: &pubsub.RetryPolicy{MinimumBackoff: "10s", MaximumBackoff: "600s"},
	}).Do()
	require.NoError(t, err)
	defer svc.Projects.Subscriptions.Delete(subName).Do() //nolint:errcheck

	gotSub, err := svc.Projects.Subscriptions.Get(subName).Do()
	require.NoError(t, err)
	require.NotNil(t, gotSub.DeadLetterPolicy, "dead_letter_policy must round-trip")
	assert.Equal(t, dlqName, gotSub.DeadLetterPolicy.DeadLetterTopic)
	assert.Equal(t, int64(7), gotSub.DeadLetterPolicy.MaxDeliveryAttempts)
	require.NotNil(t, gotSub.RetryPolicy, "retry_policy must round-trip")
	assert.Equal(t, "10s", gotSub.RetryPolicy.MinimumBackoff)
	assert.Equal(t, "600s", gotSub.RetryPolicy.MaximumBackoff)

	// PATCH the retry policy via updateMask.
	_, err = svc.Projects.Subscriptions.Patch(subName, &pubsub.UpdateSubscriptionRequest{
		Subscription: &pubsub.Subscription{RetryPolicy: &pubsub.RetryPolicy{MinimumBackoff: "30s", MaximumBackoff: "300s"}},
		UpdateMask:   "retryPolicy",
	}).Do()
	require.NoError(t, err)
	patched, err := svc.Projects.Subscriptions.Get(subName).Do()
	require.NoError(t, err)
	require.NotNil(t, patched.RetryPolicy)
	assert.Equal(t, "30s", patched.RetryPolicy.MinimumBackoff, "patched retry_policy must persist")
}
