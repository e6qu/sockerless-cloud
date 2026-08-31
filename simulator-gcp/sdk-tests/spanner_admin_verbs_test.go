package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	spanneradmin "google.golang.org/api/spanner/v1"
)

// The Spanner admin verbs that shape a database rather than its data: where it
// splits, which quorum it is in, the samples it has produced, and the sessions
// a wire-protocol adapter drives.
func TestSpanner_SplitPointsQuorumScansAndAdapter(t *testing.T) {
	svc := spannerAdminService(t, ctx)
	const project, instance, database = "spanner-verbs", "prod", "app"
	parent := "projects/" + project + "/instances/" + instance
	name := parent + "/databases/" + database

	_, err := svc.Projects.Instances.Create(parent[:len(parent)-len("/instances/"+instance)],
		&spanneradmin.CreateInstanceRequest{
			InstanceId: instance,
			Instance:   &spanneradmin.Instance{Config: "regional-us-central1", NodeCount: 1},
		}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Databases.Create(parent,
		&spanneradmin.CreateDatabaseRequest{CreateStatement: "CREATE DATABASE `" + database + "`"}).Do()
	require.NoError(t, err)

	// A split point names the table or index it splits; one naming neither
	// describes no split.
	_, err = svc.Projects.Instances.Databases.AddSplitPoints(name,
		&spanneradmin.AddSplitPointsRequest{
			SplitPoints: []*spanneradmin.SplitPoints{{Table: "Orders"}},
		}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Instances.Databases.AddSplitPoints(name,
		&spanneradmin.AddSplitPointsRequest{SplitPoints: []*spanneradmin.SplitPoints{{}}}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names the table or index")

	_, err = svc.Projects.Instances.Databases.AddSplitPoints(name,
		&spanneradmin.AddSplitPointsRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs the split points")

	// Changing the quorum is recorded on the database, so a read afterwards
	// reports the quorum it is actually in.
	_, err = svc.Projects.Instances.Databases.Changequorum(name,
		&spanneradmin.ChangeQuorumRequest{
			QuorumType: &spanneradmin.QuorumType{SingleRegion: &spanneradmin.SingleRegionQuorum{}},
		}).Do()
	require.NoError(t, err)
	got, err := svc.Projects.Instances.Databases.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.QuorumInfo)

	_, err = svc.Projects.Instances.Databases.Changequorum(name,
		&spanneradmin.ChangeQuorumRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quorum type")

	// A database nothing has queried has a scan resource with no data in it.
	scan, err := svc.Projects.Instances.Databases.GetScans(name).Do()
	require.NoError(t, err)
	assert.Contains(t, scan.Name, database)
	assert.Nil(t, scan.ScanData, "no traffic means no sampled access patterns")

	// An adapter session is a session like any other; the adapter is how it is
	// spoken to, not what it is.
	session, err := svc.Projects.Instances.Databases.Sessions.Adapter(name,
		&spanneradmin.AdapterSession{}).Do()
	require.NoError(t, err)
	require.Contains(t, session.Name, "/sessions/")

	adapted, err := svc.Projects.Instances.Databases.Sessions.AdaptMessage(session.Name,
		&spanneradmin.AdaptMessageRequest{Protocol: "POSTGRESQL"}).Do()
	require.NoError(t, err)
	assert.True(t, adapted.Last)

	// A message with no protocol cannot be carried.
	_, err = svc.Projects.Instances.Databases.Sessions.AdaptMessage(session.Name,
		&spanneradmin.AdaptMessageRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protocol")

	// And a database that is not there holds no adapter session.
	_, err = svc.Projects.Instances.Databases.Sessions.Adapter(parent+"/databases/absent",
		&spanneradmin.AdapterSession{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
