package gcp_sdk_test

import (
	"sort"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	firestoreapi "google.golang.org/api/firestore/v1"
)

// Firestore pipeline operations through the official Go client's pipeline
// API, which runs them over gRPC ExecutePipeline. The stages and functions
// behave as the firestore-pipeline-*.md.gz pages describe them.

func seedPipelineBooks(t *testing.T, c *firestore.Client, coll string) {
	t.Helper()
	books := map[string]map[string]any{
		"hitchhiker": {"title": "The Hitchhiker's Guide to the Galaxy", "author": map[string]any{"name": "Douglas Adams", "country": "UK"},
			"genre": "Science Fiction", "rating": 4.2, "published": int64(1979), "tags": []any{"comedy", "space", "adventure"}},
		"pride": {"title": "Pride and Prejudice", "author": map[string]any{"name": "Jane Austen", "country": "UK"},
			"genre": "Romance", "rating": 4.5, "published": int64(1813), "tags": []any{"classic", "social commentary", "love"}},
		"lotr": {"title": "The Lord of the Rings", "author": map[string]any{"name": "J.R.R. Tolkien", "country": "UK"},
			"genre": "Fantasy", "rating": 4.7, "published": int64(1954), "tags": []any{"adventure", "magic", "epic"}},
		"dune": {"title": "Dune", "author": map[string]any{"name": "Frank Herbert", "country": "USA"},
			"genre": "Science Fiction", "rating": 4.6, "published": int64(1965), "tags": []any{"politics", "desert", "ecology"}},
		"crime": {"title": "Crime and Punishment", "author": map[string]any{"name": "Fyodor Dostoevsky", "country": "Russia"},
			"genre": "Psychological Thriller", "rating": 4.3, "published": int64(1866), "tags": []any{"philosophy", "crime", "redemption"}},
		"handmaid": {"title": "The Handmaid's Tale", "author": map[string]any{"name": "Margaret Atwood", "country": "Canada"},
			"genre": "Dystopian", "rating": 4.1, "published": int64(1985), "tags": []any{"feminism", "totalitarianism", "resistance"}},
	}
	for id, data := range books {
		_, err := c.Collection(coll).Doc(id).Set(ctx, data)
		require.NoError(t, err)
	}
}

func pipelineRows(t *testing.T, p *firestore.Pipeline) []map[string]any {
	t.Helper()
	results, err := p.Execute(ctx).Results().GetAll()
	require.NoError(t, err)
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		require.True(t, r.Exists())
		out = append(out, r.Data())
	}
	return out
}

func pipelineTitles(rows []map[string]any) []string {
	var titles []string
	for _, r := range rows {
		titles = append(titles, r["title"].(string))
	}
	return titles
}

func TestFirestore_PipelineStages(t *testing.T) {
	t.Parallel()
	c := newFSGRPCClient(t, "fs-pipeline-proj")
	seedPipelineBooks(t, c, "books")
	books := func() *firestore.Pipeline { return c.Pipeline().Collection("books") }

	t.Run("WhereSortLimit", func(t *testing.T) {
		rows := pipelineRows(t, books().
			Where(firestore.GreaterThan(firestore.FieldOf("rating"), 4.25)).
			Sort(firestore.Orders(firestore.Descending(firestore.FieldOf("rating")))).
			Limit(3))
		assert.Equal(t, []string{"The Lord of the Rings", "Dune", "Pride and Prejudice"}, pipelineTitles(rows))
	})

	t.Run("InputDocumentsCarryTheirReference", func(t *testing.T) {
		results, err := books().Where(firestore.Equal(firestore.FieldOf("title"), "Dune")).Execute(ctx).Results().GetAll()
		require.NoError(t, err)
		require.Len(t, results, 1)
		require.NotNil(t, results[0].Ref())
		assert.Equal(t, "dune", results[0].Ref().ID)
	})

	t.Run("OffsetSkips", func(t *testing.T) {
		rows := pipelineRows(t, books().Sort(firestore.Orders(firestore.Ascending(firestore.FieldOf("published")))).Offset(4))
		assert.Equal(t, []string{"The Hitchhiker's Guide to the Galaxy", "The Handmaid's Tale"}, pipelineTitles(rows))
	})

	t.Run("SelectNestedField", func(t *testing.T) {
		rows := pipelineRows(t, books().Where(firestore.Equal(firestore.FieldOf("title"), "Dune")).
			Select(firestore.Fields("title", "author.name")))
		require.Len(t, rows, 1)
		assert.Equal(t, map[string]any{"title": "Dune", "author": map[string]any{"name": "Frank Herbert"}}, rows[0])
	})

	t.Run("AddFieldsAndRemoveFields", func(t *testing.T) {
		rows := pipelineRows(t, books().Where(firestore.Equal(firestore.FieldOf("title"), "Dune")).
			AddFields(firestore.Selectables(firestore.Multiply(firestore.FieldOf("rating"), 2).As("doubled"))).
			RemoveFields(firestore.Fields("tags", "author")))
		require.Len(t, rows, 1)
		assert.InDelta(t, 9.2, rows[0]["doubled"], 1e-9)
		assert.NotContains(t, rows[0], "tags")
		assert.NotContains(t, rows[0], "author")
		assert.Equal(t, "Science Fiction", rows[0]["genre"])
	})

	t.Run("AggregateWithGroups", func(t *testing.T) {
		rows := pipelineRows(t, books().
			Aggregate(firestore.Accumulators(
				firestore.CountAll().As("count"),
				firestore.Average("rating").As("avg"),
			), firestore.WithAggregateGroups("genre")).
			Where(firestore.Equal(firestore.FieldOf("genre"), "Science Fiction")))
		require.Len(t, rows, 1)
		assert.Equal(t, int64(2), rows[0]["count"])
		assert.InDelta(t, 4.4, rows[0]["avg"], 1e-9)
	})

	t.Run("AggregateWithoutGroups", func(t *testing.T) {
		rows := pipelineRows(t, books().Aggregate(firestore.Accumulators(
			firestore.Count("rating").As("rated"),
			firestore.Sum("published").As("years"),
			firestore.Maximum("rating").As("best"),
			firestore.Minimum("published").As("oldest"),
		)))
		require.Len(t, rows, 1)
		assert.Equal(t, int64(6), rows[0]["rated"])
		assert.Equal(t, int64(1979+1813+1954+1965+1866+1985), rows[0]["years"])
		assert.Equal(t, 4.7, rows[0]["best"])
		assert.Equal(t, int64(1813), rows[0]["oldest"])
	})

	t.Run("Distinct", func(t *testing.T) {
		rows := pipelineRows(t, books().Distinct(firestore.Fields("genre")))
		assert.Len(t, rows, 5)
	})

	t.Run("Documents", func(t *testing.T) {
		rows := pipelineRows(t, c.Pipeline().Documents([]*firestore.DocumentRef{
			c.Collection("books").Doc("dune"), c.Collection("books").Doc("lotr"), c.Collection("books").Doc("missing"),
		}).Sort(firestore.Orders(firestore.Ascending(firestore.FieldOf("title")))))
		assert.Equal(t, []string{"Dune", "The Lord of the Rings"}, pipelineTitles(rows))
	})

	t.Run("Literals", func(t *testing.T) {
		rows := pipelineRows(t, c.Pipeline().Literals([]map[string]any{
			{"name": "joe", "age": 10}, {"name": "bob", "age": 30}, {"name": "alice", "age": 40},
		}).Where(firestore.GreaterThan(firestore.FieldOf("age"), 20)))
		assert.Len(t, rows, 2)
	})

	t.Run("Unnest", func(t *testing.T) {
		rows := pipelineRows(t, books().Where(firestore.Equal(firestore.FieldOf("title"), "Dune")).
			UnnestWithAlias("tags", "tag", firestore.WithUnnestIndexField("i")).
			Select(firestore.Fields("tag", "i")))
		sort.Slice(rows, func(a, b int) bool { return rows[a]["i"].(int64) < rows[b]["i"].(int64) })
		assert.Equal(t, []map[string]any{
			{"tag": "politics", "i": int64(0)}, {"tag": "desert", "i": int64(1)}, {"tag": "ecology", "i": int64(2)},
		}, rows)
	})

	t.Run("ReplaceWith", func(t *testing.T) {
		rows := pipelineRows(t, books().Where(firestore.Equal(firestore.FieldOf("title"), "Dune")).ReplaceWith("author"))
		assert.Equal(t, []map[string]any{{"name": "Frank Herbert", "country": "USA"}}, rows)
	})

	t.Run("Union", func(t *testing.T) {
		rows := pipelineRows(t, books().Where(firestore.Equal(firestore.FieldOf("genre"), "Fantasy")).
			Union(books().Where(firestore.Equal(firestore.FieldOf("genre"), "Romance"))).
			Sort(firestore.Orders(firestore.Ascending(firestore.FieldOf("title")))))
		assert.Equal(t, []string{"Pride and Prejudice", "The Lord of the Rings"}, pipelineTitles(rows))
	})

	t.Run("DefineAndVariable", func(t *testing.T) {
		rows := pipelineRows(t, books().
			Define(firestore.AliasedExpressions(firestore.Subtract(firestore.FieldOf("published"), 1900).As("age"))).
			Where(firestore.GreaterThan(firestore.Variable("age"), 70)).
			Select(firestore.Fields("title", firestore.Variable("age").As("age"))))
		assert.Equal(t, []map[string]any{{"title": "The Handmaid's Tale", "age": int64(85)},
			{"title": "The Hitchhiker's Guide to the Galaxy", "age": int64(79)}}, sortByTitle(rows))
	})

	t.Run("CollectionGroupAndSubcollection", func(t *testing.T) {
		_, err := c.Collection("books").Doc("dune").Collection("reviews").Doc("r1").Set(ctx, map[string]any{"reviewer": "Bob", "rating": int64(4)})
		require.NoError(t, err)
		_, err = c.Collection("books").Doc("dune").Collection("reviews").Doc("r2").Set(ctx, map[string]any{"reviewer": "Alice", "rating": int64(5)})
		require.NoError(t, err)
		_, err = c.Collection("books").Doc("lotr").Collection("reviews").Doc("r3").Set(ctx, map[string]any{"reviewer": "Carol", "rating": int64(5)})
		require.NoError(t, err)

		all := pipelineRows(t, c.Pipeline().CollectionGroup("reviews"))
		assert.Len(t, all, 3)

		rows := pipelineRows(t, c.Pipeline().Documents([]*firestore.DocumentRef{c.Collection("books").Doc("dune")}).
			AddFields(firestore.Selectables(
				firestore.Subcollection("reviews").
					Aggregate(firestore.Accumulators(firestore.Average("rating").As("avg"))).
					ToScalarExpression().As("score"),
				firestore.Subcollection("reviews").
					Select(firestore.Fields("reviewer")).
					Sort(firestore.Orders(firestore.Ascending(firestore.FieldOf("reviewer")))).
					ToArrayExpression().As("reviewers"),
			)))
		require.Len(t, rows, 1)
		assert.Equal(t, 4.5, rows[0]["score"])
		assert.Equal(t, []any{map[string]any{"reviewer": "Alice"}, map[string]any{"reviewer": "Bob"}}, rows[0]["reviewers"])
	})
}

func sortByTitle(rows []map[string]any) []map[string]any {
	sort.Slice(rows, func(a, b int) bool { return rows[a]["title"].(string) < rows[b]["title"].(string) })
	return rows
}

func TestFirestore_PipelineFunctions(t *testing.T) {
	t.Parallel()
	c := newFSGRPCClient(t, "fs-pipeline-fn-proj")
	ts := time.Date(2025, 2, 20, 10, 20, 30, 123456000, time.UTC)
	_, err := c.Collection("things").Doc("t1").Set(ctx, map[string]any{
		"name":  "Firestore",
		"n":     int64(20),
		"x":     22.5,
		"tags":  []any{int64(1), int64(5), int64(2)},
		"empty": "",
		"none":  nil,
		"when":  ts,
		"m":     map[string]any{"a": int64(1), "b": int64(2)},
	})
	require.NoError(t, err)

	row := pipelineRows(t, c.Pipeline().Collection("things").Select(firestore.Fields(
		firestore.ToUpper("name").As("upper"),
		firestore.StringConcat("name", "-", firestore.FieldOf("name")).As("concat"),
		firestore.Substring("name", 0, 4).As("sub"),
		firestore.CharLength("empty").As("emptyLen"),
		firestore.Add(firestore.FieldOf("n"), 3).As("sum"),
		firestore.Divide(firestore.FieldOf("n"), 3).As("quot"),
		firestore.Mod(firestore.FieldOf("n"), 3).As("rem"),
		firestore.ArrayLength("tags").As("tagCount"),
		firestore.ArrayGet("tags", -1).As("lastTag"),
		firestore.ArrayMaximum("tags").As("maxTag"),
		firestore.ArraySum("tags").As("tagSum"),
		firestore.MapGet("m", "b").As("mb"),
		firestore.TimestampAdd("when", "day", 5).As("later"),
		firestore.TimestampTruncate("when", "day").As("day"),
		firestore.TimestampToUnixSeconds("when").As("unix"),
		firestore.Conditional(firestore.GreaterThan(firestore.FieldOf("n"), 10), "big", "small").As("size"),
		firestore.IfNull("none", "fallback").As("ifNull"),
		firestore.Type("x").As("xType"),
		firestore.Cmp(firestore.FieldOf("x"), "foo").As("cmp"),
	)))
	require.Len(t, row, 1)
	got := row[0]
	assert.Equal(t, "FIRESTORE", got["upper"])
	assert.Equal(t, "Firestore-Firestore", got["concat"])
	assert.Equal(t, "Fire", got["sub"])
	assert.Equal(t, int64(0), got["emptyLen"], "an empty string is a string, not a missing value")
	assert.Equal(t, int64(23), got["sum"])
	assert.Equal(t, int64(6), got["quot"], "integer division truncates")
	assert.Equal(t, int64(2), got["rem"])
	assert.Equal(t, int64(3), got["tagCount"])
	assert.Equal(t, int64(2), got["lastTag"])
	assert.Equal(t, int64(5), got["maxTag"])
	assert.Equal(t, int64(8), got["tagSum"])
	assert.Equal(t, int64(2), got["mb"])
	assert.Equal(t, ts.AddDate(0, 0, 5), got["later"])
	assert.Equal(t, time.Date(2025, 2, 20, 0, 0, 0, 0, time.UTC), got["day"])
	assert.Equal(t, ts.Unix(), got["unix"])
	assert.Equal(t, "big", got["size"])
	assert.Equal(t, "fallback", got["ifNull"])
	assert.Equal(t, "float64", got["xType"])
	assert.Equal(t, int64(-1), got["cmp"], "numbers order before strings")

	matched := pipelineRows(t, c.Pipeline().Collection("things").Where(firestore.And(
		firestore.Like(firestore.FieldOf("name"), "Fire%"),
		firestore.RegexMatch(firestore.FieldOf("name"), "F.*store"),
		firestore.ArrayContains(firestore.FieldOf("tags"), 5),
		firestore.Not(firestore.FieldExists(firestore.FieldOf("missing"))),
	)))
	assert.Len(t, matched, 1)

	_, err = c.Pipeline().Collection("things").Select(firestore.Fields(
		firestore.Divide(firestore.FieldOf("n"), 0).As("boom"),
	)).Execute(ctx).Results().GetAll()
	require.Error(t, err, "integer division by zero stops the pipeline with an error")
}

// executePipeline over REST runs the same pipeline the gRPC method does.
func TestFirestore_PipelineOverREST(t *testing.T) {
	t.Parallel()
	svc := firestoreAdminService(t)
	const project = "fs-pipeline-rest"
	ensureProject(t, project)
	documents := "projects/" + project + "/databases/(default)/documents"
	for id, rank := range map[string]int64{"a": 1, "b": 2, "c": 3} {
		_, err := svc.Projects.Databases.Documents.Patch(documents+"/ranks/"+id, &firestoreapi.Document{
			Fields: map[string]firestoreapi.Value{"rank": {IntegerValue: rank}},
		}).Do()
		require.NoError(t, err)
	}
	ran, err := svc.Projects.Databases.Documents.ExecutePipeline(documents, &firestoreapi.ExecutePipelineRequest{
		StructuredPipeline: &firestoreapi.StructuredPipeline{Pipeline: &firestoreapi.Pipeline{Stages: []*firestoreapi.Stage{
			{Name: "collection", Args: []*firestoreapi.Value{{ReferenceValue: "/ranks"}}},
			{Name: "where", Args: []*firestoreapi.Value{{FunctionValue: &firestoreapi.Function{
				Name: "greater_than", Args: []*firestoreapi.Value{{FieldReferenceValue: "rank"}, {IntegerValue: 1, ForceSendFields: []string{"IntegerValue"}}},
			}}}},
			{Name: "sort", Args: []*firestoreapi.Value{{MapValue: &firestoreapi.MapValue{Fields: map[string]firestoreapi.Value{
				"direction": {StringValue: "descending"}, "expression": {FieldReferenceValue: "rank"},
			}}}}},
		}}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, ran.Results, 2)
	assert.Equal(t, documents+"/ranks/c", ran.Results[0].Name)
	assert.Equal(t, int64(2), ran.Results[1].Fields["rank"].IntegerValue)
	assert.NotEmpty(t, ran.ExecutionTime)

	_, err = svc.Projects.Databases.Documents.ExecutePipeline(documents, &firestoreapi.ExecutePipelineRequest{
		StructuredPipeline: &firestoreapi.StructuredPipeline{Pipeline: &firestoreapi.Pipeline{Stages: []*firestoreapi.Stage{
			{Name: "where", Args: []*firestoreapi.Value{{BooleanValue: true}}},
		}}},
	}).Do()
	require.Error(t, err, "a pipeline begins with an input stage")
	assert.Contains(t, err.Error(), "400")
}
