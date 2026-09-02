package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	firestore "google.golang.org/api/firestore/v1"
	"google.golang.org/api/option"
)

func firestoreAdminService(t *testing.T) *firestore.Service {
	t.Helper()
	svc, err := firestore.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// A change stream watches a database or one collection group for changes.
func TestFirestore_ChangeStreams(t *testing.T) {
	svc := firestoreAdminService(t)
	const project, database = "streams-project", "(default)"
	parent := "projects/" + project + "/databases/" + database

	created, err := svc.Projects.Databases.ChangeStreams.Create(parent,
		&firestore.GoogleFirestoreAdminV1ChangeStream{
			DatabaseScope:   &firestore.GoogleFirestoreAdminV1DatabaseScope{},
			RetentionPeriod: "604800s",
		}).ChangeStreamId("audit").Do()
	require.NoError(t, err)
	assert.Contains(t, created.Name, "changeStreams/audit")
	assert.NotEmpty(t, created.CreateTime)
	assert.NotEmpty(t, created.StartTime,
		"a stream with no start time begins where it was created")

	got, err := svc.Projects.Databases.ChangeStreams.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "604800s", got.RetentionPeriod)

	listed, err := svc.Projects.Databases.ChangeStreams.List(parent).Do()
	require.NoError(t, err)
	require.Len(t, listed.ChangeStreams, 1)

	// A stream watches a database or a collection group, not both — there
	// would be no single scope to read changes from.
	_, err = svc.Projects.Databases.ChangeStreams.Create(parent,
		&firestore.GoogleFirestoreAdminV1ChangeStream{
			DatabaseScope:        &firestore.GoogleFirestoreAdminV1DatabaseScope{},
			CollectionGroupScope: &firestore.GoogleFirestoreAdminV1CollectionGroupScope{},
		}).ChangeStreamId("both").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not both")

	// The same id twice is refused rather than replacing the first.
	_, err = svc.Projects.Databases.ChangeStreams.Create(parent,
		&firestore.GoogleFirestoreAdminV1ChangeStream{
			DatabaseScope: &firestore.GoogleFirestoreAdminV1DatabaseScope{},
		}).ChangeStreamId("audit").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	_, err = svc.Projects.Databases.ChangeStreams.Delete(created.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Databases.ChangeStreams.Get(created.Name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// listen reports the target it was given; executePipeline runs the pipeline it
// was given. Neither reports data the database does not hold.
func TestFirestore_ListenAndExecutePipeline(t *testing.T) {
	svc := firestoreAdminService(t)
	const project, database = "listen-project", "(default)"
	documents := "projects/" + project + "/databases/" + database + "/documents"

	listened, err := svc.Projects.Databases.Documents.Listen(documents,
		&firestore.ListenRequest{
			AddTarget: &firestore.Target{TargetId: 7},
		}).Do()
	require.NoError(t, err)
	require.NotNil(t, listened.TargetChange)
	assert.Equal(t, "ADD", listened.TargetChange.TargetChangeType)
	assert.Equal(t, []int64{7}, listened.TargetChange.TargetIds)

	removed, err := svc.Projects.Databases.Documents.Listen(documents,
		&firestore.ListenRequest{RemoveTarget: 7}).Do()
	require.NoError(t, err)
	assert.Equal(t, "REMOVE", removed.TargetChange.TargetChangeType)

	// A listen that neither adds nor removes is not a request.
	_, err = svc.Projects.Databases.Documents.Listen(documents,
		&firestore.ListenRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adds or removes a target")

	ran, err := svc.Projects.Databases.Documents.ExecutePipeline(documents,
		&firestore.ExecutePipelineRequest{
			StructuredPipeline: &firestore.StructuredPipeline{},
		}).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, ran.ExecutionTime)
	assert.Empty(t, ran.Results, "a database holding nothing returns no rows")

	_, err = svc.Projects.Databases.Documents.ExecutePipeline(documents,
		&firestore.ExecutePipelineRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs the pipeline to run")
}
