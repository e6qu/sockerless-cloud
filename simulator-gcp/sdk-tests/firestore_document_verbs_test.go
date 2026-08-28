package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	firestore "google.golang.org/api/firestore/v1"
	"google.golang.org/api/option"
)

// The custom methods on a document parent, plus documents:write and the
// databases clone/restore pair:
//
//	POST /v1/projects/{project}/databases/{database}/documents:write
//	POST /v1/projects/{project}/{databasesVerb}

func firestoreVerbService(t *testing.T) *firestore.Service {
	t.Helper()
	svc, err := firestore.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// seedFirestoreDocument writes one document and returns the database parent.
func seedFirestoreDocument(t *testing.T, svc *firestore.Service, parent, name string, fields map[string]firestore.Value) {
	t.Helper()
	_, err := svc.Projects.Databases.Documents.Commit(parent, &firestore.CommitRequest{
		Writes: []*firestore.Write{{
			Update: &firestore.Document{Name: parent + "/documents/" + name, Fields: fields},
		}},
	}).Do()
	require.NoError(t, err)
}

func intValue(v int64) firestore.Value   { return firestore.Value{IntegerValue: v} }
func strValue(v string) firestore.Value  { return firestore.Value{StringValue: v} }
func dblValue(v float64) firestore.Value { return firestore.Value{DoubleValue: v} }

func TestFirestore_ListCollectionIds(t *testing.T) {
	svc := firestoreVerbService(t)
	parent := "projects/fs-verbs/databases/(default)"

	seedFirestoreDocument(t, svc, parent, "orders/one", map[string]firestore.Value{"n": intValue(1)})
	seedFirestoreDocument(t, svc, parent, "invoices/two", map[string]firestore.Value{"n": intValue(2)})

	list, err := svc.Projects.Databases.Documents.ListCollectionIds(parent+"/documents",
		&firestore.ListCollectionIdsRequest{}).Do()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"orders", "invoices"}, list.CollectionIds)
}

func TestFirestore_RunAggregationQuery(t *testing.T) {
	svc := firestoreVerbService(t)
	parent := "projects/fs-agg/databases/(default)"

	for name, amount := range map[string]int64{"a": 10, "b": 20, "c": 30} {
		seedFirestoreDocument(t, svc, parent, "sales/"+name, map[string]firestore.Value{
			"amount": intValue(amount),
			"region": strValue("north"),
		})
	}
	seedFirestoreDocument(t, svc, parent, "sales/d", map[string]firestore.Value{
		"amount": intValue(100),
		"region": strValue("south"),
	})

	query := &firestore.StructuredQuery{From: []*firestore.CollectionSelector{{CollectionId: "sales"}}}
	response, err := svc.Projects.Databases.Documents.RunAggregationQuery(parent+"/documents",
		&firestore.RunAggregationQueryRequest{
			StructuredAggregationQuery: &firestore.StructuredAggregationQuery{
				StructuredQuery: query,
				Aggregations: []*firestore.Aggregation{
					{Alias: "total", Count: &firestore.Count{}},
					{Alias: "sum", Sum: &firestore.Sum{Field: &firestore.FieldReference{FieldPath: "amount"}}},
					{Alias: "avg", Avg: &firestore.Avg{Field: &firestore.FieldReference{FieldPath: "amount"}}},
				},
			},
		}).Do()
	require.NoError(t, err)
	require.NotNil(t, response.Result)
	fields := response.Result.AggregateFields
	assert.Equal(t, int64(4), fields["total"].IntegerValue)
	assert.Equal(t, int64(160), fields["sum"].IntegerValue)
	assert.Equal(t, float64(40), fields["avg"].DoubleValue)
}

// The aggregation runs over the documents runQuery selects, so a filter must
// narrow it exactly as it narrows the query.
func TestFirestore_AggregationHonoursTheQueryFilter(t *testing.T) {
	svc := firestoreVerbService(t)
	parent := "projects/fs-agg-filter/databases/(default)"

	seedFirestoreDocument(t, svc, parent, "items/keep", map[string]firestore.Value{
		"kind": strValue("widget"), "price": dblValue(2.5),
	})
	seedFirestoreDocument(t, svc, parent, "items/skip", map[string]firestore.Value{
		"kind": strValue("gadget"), "price": dblValue(99),
	})

	response, err := svc.Projects.Databases.Documents.RunAggregationQuery(parent+"/documents",
		&firestore.RunAggregationQueryRequest{
			StructuredAggregationQuery: &firestore.StructuredAggregationQuery{
				StructuredQuery: &firestore.StructuredQuery{
					From: []*firestore.CollectionSelector{{CollectionId: "items"}},
					Where: &firestore.Filter{FieldFilter: &firestore.FieldFilter{
						Field: &firestore.FieldReference{FieldPath: "kind"},
						Op:    "EQUAL",
						Value: &firestore.Value{StringValue: "widget"},
					}},
				},
				Aggregations: []*firestore.Aggregation{
					{Alias: "n", Count: &firestore.Count{}},
					{Alias: "s", Sum: &firestore.Sum{Field: &firestore.FieldReference{FieldPath: "price"}}},
				},
			},
		}).Do()
	require.NoError(t, err)
	require.NotNil(t, response.Result)
	assert.Equal(t, int64(1), response.Result.AggregateFields["n"].IntegerValue)
	// A non-integral sum comes back as a double, as the service reports it.
	assert.Equal(t, 2.5, response.Result.AggregateFields["s"].DoubleValue)
}

// Averaging no documents has no value, which the service reports as null.
func TestFirestore_AggregationOverNoDocuments(t *testing.T) {
	svc := firestoreVerbService(t)
	parent := "projects/fs-agg-empty/databases/(default)"

	response, err := svc.Projects.Databases.Documents.RunAggregationQuery(parent+"/documents",
		&firestore.RunAggregationQueryRequest{
			StructuredAggregationQuery: &firestore.StructuredAggregationQuery{
				StructuredQuery: &firestore.StructuredQuery{
					From: []*firestore.CollectionSelector{{CollectionId: "absent"}},
				},
				Aggregations: []*firestore.Aggregation{
					{Alias: "n", Count: &firestore.Count{}},
					{Alias: "a", Avg: &firestore.Avg{Field: &firestore.FieldReference{FieldPath: "x"}}},
				},
			},
		}).Do()
	require.NoError(t, err)
	require.NotNil(t, response.Result)
	assert.Equal(t, int64(0), response.Result.AggregateFields["n"].IntegerValue)
	assert.Equal(t, "NULL_VALUE", response.Result.AggregateFields["a"].NullValue)
}

func TestFirestore_PartitionQuery(t *testing.T) {
	svc := firestoreVerbService(t)
	parent := "projects/fs-partition/databases/(default)"

	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		seedFirestoreDocument(t, svc, parent, "events/"+name, map[string]firestore.Value{"k": strValue(name)})
	}

	response, err := svc.Projects.Databases.Documents.PartitionQuery(parent+"/documents",
		&firestore.PartitionQueryRequest{
			PartitionCount:  3,
			StructuredQuery: &firestore.StructuredQuery{From: []*firestore.CollectionSelector{{CollectionId: "events"}}},
		}).Do()
	require.NoError(t, err)
	// Three partitions are described by two cursors.
	require.Len(t, response.Partitions, 2)
	assert.NotEmpty(t, response.Partitions[0].Values[0].ReferenceValue)

	// Fewer documents than partitions yields no cursor at all, which is a
	// complete answer rather than an empty one.
	sparse := "projects/fs-partition-sparse/databases/(default)"
	seedFirestoreDocument(t, svc, sparse, "events/only", map[string]firestore.Value{"k": strValue("only")})
	response, err = svc.Projects.Databases.Documents.PartitionQuery(sparse+"/documents",
		&firestore.PartitionQueryRequest{
			PartitionCount:  4,
			StructuredQuery: &firestore.StructuredQuery{From: []*firestore.CollectionSelector{{CollectionId: "events"}}},
		}).Do()
	require.NoError(t, err)
	assert.Empty(t, response.Partitions)
}

func TestFirestore_DocumentsWrite(t *testing.T) {
	svc := firestoreVerbService(t)
	parent := "projects/fs-write/databases/(default)"
	docName := parent + "/documents/streamed/one"

	response, err := svc.Projects.Databases.Documents.Write(parent, &firestore.WriteRequest{
		Writes: []*firestore.Write{{
			Update: &firestore.Document{Name: docName, Fields: map[string]firestore.Value{
				"via": strValue("write"),
			}},
		}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, response.WriteResults, 1)
	// The first message opens the stream, so it names it.
	assert.NotEmpty(t, response.StreamId)
	assert.NotEmpty(t, response.StreamToken)

	// The write applied to the same store every other document path reads.
	doc, err := svc.Projects.Databases.Documents.Get(docName).Do()
	require.NoError(t, err)
	assert.Equal(t, "write", doc.Fields["via"].StringValue)
}

func TestFirestore_CloneAndRestoreDatabase(t *testing.T) {
	svc := firestoreVerbService(t)

	_, err := svc.Projects.Databases.Create("projects/fs-clone",
		&firestore.GoogleFirestoreAdminV1Database{Type: "FIRESTORE_NATIVE", LocationId: "nam5"}).
		DatabaseId("source").Do()
	require.NoError(t, err)

	op, err := svc.Projects.Databases.Clone("projects/fs-clone",
		&firestore.GoogleFirestoreAdminV1CloneDatabaseRequest{
			DatabaseId: "copy",
			PitrSnapshot: &firestore.GoogleFirestoreAdminV1PitrSnapshot{
				Database: "projects/fs-clone/databases/source",
			},
		}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	cloned, err := svc.Projects.Databases.Get("projects/fs-clone/databases/copy").Do()
	require.NoError(t, err)
	assert.Equal(t, "FIRESTORE_NATIVE", cloned.Type, "the clone carries the source's configuration")

	// Cloning onto a name already taken is a conflict.
	_, err = svc.Projects.Databases.Clone("projects/fs-clone",
		&firestore.GoogleFirestoreAdminV1CloneDatabaseRequest{
			DatabaseId: "copy",
			PitrSnapshot: &firestore.GoogleFirestoreAdminV1PitrSnapshot{
				Database: "projects/fs-clone/databases/source",
			},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Cloning from a database that does not exist reports that.
	_, err = svc.Projects.Databases.Clone("projects/fs-clone",
		&firestore.GoogleFirestoreAdminV1CloneDatabaseRequest{
			DatabaseId:   "other",
			PitrSnapshot: &firestore.GoogleFirestoreAdminV1PitrSnapshot{Database: "projects/fs-clone/databases/absent"},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Database not found")

	// Restoring from a backup that does not exist reports that too.
	_, err = svc.Projects.Databases.Restore("projects/fs-clone",
		&firestore.GoogleFirestoreAdminV1RestoreDatabaseRequest{
			DatabaseId: "restored",
			Backup:     "projects/fs-clone/locations/nam5/backups/absent",
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Backup not found")
}
