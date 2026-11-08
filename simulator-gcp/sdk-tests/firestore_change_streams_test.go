package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

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

func TestFirestore_ChangeStreams(t *testing.T) {
	t.Parallel()
	svc := firestoreAdminService(t)
	const project, database = "streams-project", "(default)"
	ensureProject(t, project)
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

	// A stream watches a database or a collection group, not both: there would be
	// no single scope to read changes from.
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

// listen streams a watch over REST: the target's documents, CURRENT, and
// then each change a write makes, as a JSON array that stays open while the
// client reads. executePipeline runs the pipeline it was given.
func TestFirestore_ListenAndExecutePipeline(t *testing.T) {
	t.Parallel()
	svc := firestoreAdminService(t)
	const project, database = "listen-project", "(default)"
	ensureProject(t, project)
	documents := "projects/" + project + "/databases/" + database + "/documents"
	doc := documents + "/cities/rest-listen"
	_, err := svc.Projects.Databases.Documents.Patch(doc, &firestore.Document{
		Fields: map[string]firestore.Value{"population": {IntegerValue: 1}},
	}).Do()
	require.NoError(t, err)

	token, err := simTokenSource().Token()
	require.NoError(t, err)
	listenCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	body := `{"addTarget":{"targetId":7,"documents":{"documents":["` + doc + `"]}}}`
	req, err := http.NewRequestWithContext(listenCtx, http.MethodPost, baseURL+"/v1/"+documents+":listen", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	stream := json.NewDecoder(resp.Body)
	open, err := stream.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('['), open, "the responses stream as a JSON array")
	next := func() firestore.ListenResponse {
		t.Helper()
		require.True(t, stream.More())
		var r firestore.ListenResponse
		require.NoError(t, stream.Decode(&r))
		return r
	}
	until := func(match func(firestore.ListenResponse) bool) firestore.ListenResponse {
		t.Helper()
		for {
			if r := next(); match(r) {
				return r
			}
		}
	}

	added := next()
	require.NotNil(t, added.TargetChange)
	assert.Equal(t, "ADD", added.TargetChange.TargetChangeType)
	assert.Equal(t, []int64{7}, added.TargetChange.TargetIds)
	initial := until(func(r firestore.ListenResponse) bool { return r.DocumentChange != nil })
	assert.Equal(t, doc, initial.DocumentChange.Document.Name)
	until(func(r firestore.ListenResponse) bool {
		return r.TargetChange != nil && r.TargetChange.TargetChangeType == "CURRENT"
	})

	_, err = svc.Projects.Databases.Documents.Patch(doc, &firestore.Document{
		Fields: map[string]firestore.Value{"population": {IntegerValue: 2}},
	}).Do()
	require.NoError(t, err)
	changed := until(func(r firestore.ListenResponse) bool { return r.DocumentChange != nil })
	assert.Equal(t, int64(2), changed.DocumentChange.Document.Fields["population"].IntegerValue, "the write reaches the open watch")
	cancel()

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

// Firestore keeps a timestampValue to the microsecond, rounding any further
// precision down, and a query compares timestamps as instants, so another
// spelling of the stored time matches it.
func TestFirestore_RESTTimestampsAreInstants(t *testing.T) {
	t.Parallel()
	svc := firestoreAdminService(t)
	const project = "timestamp-project"
	ensureProject(t, project)
	documents := "projects/" + project + "/databases/(default)/documents"
	doc := documents + "/events/launch"
	written, err := svc.Projects.Databases.Documents.Patch(doc, &firestore.Document{
		Fields: map[string]firestore.Value{"at": {TimestampValue: "2024-01-02T03:04:05.123456789Z"}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "2024-01-02T03:04:05.123456Z", written.Fields["at"].TimestampValue)

	read, err := svc.Projects.Databases.Documents.Get(doc).Do()
	require.NoError(t, err)
	assert.Equal(t, "2024-01-02T03:04:05.123456Z", read.Fields["at"].TimestampValue)

	rows := runFirestoreQuery(t, baseURL+"/v1/"+documents+":runQuery",
		`{"structuredQuery":{"from":[{"collectionId":"events"}],"where":{"fieldFilter":{"field":{"fieldPath":"at"},"op":"EQUAL","value":{"timestampValue":"2024-01-02T04:04:05.1234560+01:00"}}}}}`)
	var matched []string
	for _, row := range rows {
		matched = append(matched, row.Document.Name)
	}
	assert.Equal(t, []string{doc}, matched)

	_, err = svc.Projects.Databases.Documents.Patch(documents+"/events/bad", &firestore.Document{
		Fields: map[string]firestore.Value{"at": {TimestampValue: "yesterday"}},
	}).Do()
	require.Error(t, err, "a timestampValue that is no RFC 3339 time is refused")
	assert.Contains(t, err.Error(), "400")
}
