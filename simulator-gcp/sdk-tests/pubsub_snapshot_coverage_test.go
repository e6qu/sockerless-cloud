package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pubsub "google.golang.org/api/pubsub/v1"
)

// TestPubSub_SnapshotLifecycle covers the previously-untested snapshot ops:
// create (off a subscription), get, list, seek-to-snapshot, and delete.
func TestPubSub_SnapshotLifecycle(t *testing.T) {
	svc := pubsubService(t)
	const project = "snap-project"
	topic := "projects/" + project + "/topics/snap-topic"
	subName := "projects/" + project + "/subscriptions/snap-sub"
	snapName := "projects/" + project + "/snapshots/snap1"

	_, err := svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Subscriptions.Create(subName, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)

	snap, err := svc.Projects.Snapshots.Create(snapName, &pubsub.CreateSnapshotRequest{Subscription: subName}).Do()
	require.NoError(t, err)
	assert.Equal(t, snapName, snap.Name)
	assert.NotEmpty(t, snap.ExpireTime, "snapshot carries an expireTime")

	got, err := svc.Projects.Snapshots.Get(snapName).Do()
	require.NoError(t, err)
	assert.Equal(t, snapName, got.Name)
	assert.Equal(t, topic, got.Topic, "snapshot records its source topic")

	list, err := svc.Projects.Snapshots.List("projects/" + project).Do()
	require.NoError(t, err)
	var found bool
	for _, s := range list.Snapshots {
		if s.Name == snapName {
			found = true
		}
	}
	assert.True(t, found, "created snapshot appears in the list")

	// Seek the subscription back to the snapshot.
	_, err = svc.Projects.Subscriptions.Seek(subName, &pubsub.SeekRequest{Snapshot: snapName}).Do()
	require.NoError(t, err, "subscriptions.seek to a snapshot")

	_, err = svc.Projects.Snapshots.Delete(snapName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Snapshots.Get(snapName).Do()
	assert.Error(t, err, "snapshot gone after delete")
}
