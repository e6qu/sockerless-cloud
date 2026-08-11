package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The three distance functions the model declares, each against vectors whose
// answer is known by hand rather than by running the code and recording what it
// said.
func TestDDBVectorScoreComputesEachDistanceFunction(t *testing.T) {
	for _, tc := range []struct {
		name, fn         string
		query, candidate []float64
		want             float64
	}{
		// Orthogonal unit vectors: no shared direction.
		{"cosine of orthogonal vectors", "COSINE", []float64{1, 0}, []float64{0, 1}, 0},
		// The same direction at four times the length is still the same direction.
		{"cosine ignores magnitude", "COSINE", []float64{1, 0}, []float64{4, 0}, 1},
		{"cosine of opposed vectors", "COSINE", []float64{1, 0}, []float64{-1, 0}, -1},
		{"dot product", "DOT_PRODUCT", []float64{1, 2, 3}, []float64{4, 5, 6}, 32},
		// 3-4-5 triangle.
		{"euclidean distance", "EUCLIDEAN", []float64{0, 0}, []float64{3, 4}, 5},
		{"euclidean of identical vectors", "EUCLIDEAN", []float64{2, 7}, []float64{2, 7}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ddbVectorScore(tc.fn, tc.query, tc.candidate)
			if !ok {
				t.Fatalf("%s refused vectors it should score", tc.fn)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("%s = %v, want %v", tc.fn, got, tc.want)
			}
		})
	}
}

// A zero vector has no direction, so it has no cosine similarity to anything.
// Scoring it 0 would rank it as merely orthogonal — a claim the geometry does
// not support — so it is refused and the item is not a neighbour.
func TestDDBVectorScoreRefusesACosineWithoutDirection(t *testing.T) {
	if _, ok := ddbVectorScore("COSINE", []float64{0, 0}, []float64{1, 1}); ok {
		t.Error("a zero vector was given a cosine similarity")
	}
	// The same vectors under a dot product are answerable: the product is 0.
	if got, ok := ddbVectorScore("DOT_PRODUCT", []float64{0, 0}, []float64{1, 1}); !ok || got != 0 {
		t.Errorf("dot product of a zero vector = %v (ok=%v), want 0", got, ok)
	}
}

// Mismatched dimensions are not comparable, and an unknown function is refused
// rather than defaulted — defaulting would score by a measure the caller did
// not ask for, and the answer would look valid.
func TestDDBVectorScoreRefusesWhatItCannotCompare(t *testing.T) {
	if _, ok := ddbVectorScore("COSINE", []float64{1, 2}, []float64{1, 2, 3}); ok {
		t.Error("vectors of different dimensions were scored")
	}
	if _, ok := ddbVectorScore("MANHATTAN", []float64{1}, []float64{1}); ok {
		t.Error("an unmodelled distance function was scored instead of refused")
	}
}

// Which way a score sorts is the difference between the nearest neighbours and
// the furthest. A similarity ranks larger-first; a distance ranks smaller-first.
func TestDDBVectorNearerFirstFollowsTheMeasure(t *testing.T) {
	for fn, want := range map[string]bool{"COSINE": true, "DOT_PRODUCT": true, "EUCLIDEAN": false} {
		if got := ddbVectorNearerFirst(fn); got != want {
			t.Errorf("%s ranks higher-is-nearer = %v, want %v", fn, got, want)
		}
	}
}

// A vector is read out of the item's own attribute value — the list of numbers
// DynamoDB already stores — rather than from a parallel copy that could drift
// from what a Put wrote.
func TestDDBItemVectorReadsTheStoredAttribute(t *testing.T) {
	item := map[string]any{
		"pk":        map[string]any{"S": "doc-1"},
		"embedding": map[string]any{"L": []any{map[string]any{"N": "1.5"}, map[string]any{"N": "-2"}}},
		"title":     map[string]any{"S": "not a vector"},
	}
	got, ok := ddbItemVector(item, "embedding")
	if !ok || len(got) != 2 || got[0] != 1.5 || got[1] != -2 {
		t.Fatalf("read %v (ok=%v), want [1.5 -2]", got, ok)
	}
	if _, ok := ddbItemVector(item, "title"); ok {
		t.Error("a string attribute was read as a vector")
	}
	if _, ok := ddbItemVector(item, "absent"); ok {
		t.Error("a missing attribute was read as a vector")
	}
}

// The index's required members are required, and a distance function outside
// the model's three is refused.
func TestDDBParseVectorIndexRequiresWhatTheModelMarksRequired(t *testing.T) {
	valid := map[string]any{
		"IndexName":        "by-embedding",
		"VectorAttribute":  map[string]any{"AttributeName": "embedding"},
		"Dimensions":       float64(2),
		"DistanceFunction": "COSINE",
		"Projection":       map[string]any{"ProjectionType": "ALL"},
	}
	idx, err := ddbParseVectorIndex("docs", valid)
	if err != nil {
		t.Fatalf("a complete vector index was refused: %v", err)
	}
	if idx.IndexStatus != "ACTIVE" || idx.IndexArn == "" {
		t.Errorf("index status/ARN = %q/%q, want ACTIVE and a non-empty ARN", idx.IndexStatus, idx.IndexArn)
	}
	if want := "arn:aws:dynamodb:us-east-1:123456789012:table/docs/vector-index/by-embedding"; idx.IndexArn != want {
		t.Errorf("index ARN = %q, want %q", idx.IndexArn, want)
	}

	for _, missing := range []string{"IndexName", "VectorAttribute", "Dimensions", "DistanceFunction", "Projection"} {
		t.Run("without "+missing, func(t *testing.T) {
			raw := map[string]any{}
			for k, v := range valid {
				if k != missing {
					raw[k] = v
				}
			}
			if _, err := ddbParseVectorIndex("docs", raw); err == nil {
				t.Errorf("a vector index without %s was accepted", missing)
			}
		})
	}
	bad := map[string]any{}
	for k, v := range valid {
		bad[k] = v
	}
	bad["DistanceFunction"] = "MANHATTAN"
	if _, err := ddbParseVectorIndex("docs", bad); err == nil {
		t.Error("an unmodelled distance function was accepted")
	}
}

// UpdateTable's VectorIndexUpdates create and drop one index each, and refuse
// the cases that would leave the table's indexes ambiguous.
func TestDDBVectorIndexUpdatesCreateAndDelete(t *testing.T) {
	create := map[string]any{"Create": map[string]any{
		"IndexName":        "by-embedding",
		"VectorAttribute":  map[string]any{"AttributeName": "embedding"},
		"Dimensions":       float64(3),
		"DistanceFunction": "EUCLIDEAN",
		"Projection":       map[string]any{"ProjectionType": "ALL"},
	}}
	table := DDBTable{TableName: "docs"}
	if err := ddbApplyVectorIndexUpdates(&table, []map[string]any{create}); err != nil {
		t.Fatalf("create was refused: %v", err)
	}
	if len(table.VectorIndexes) != 1 {
		t.Fatalf("table carries %d vector indexes, want 1", len(table.VectorIndexes))
	}
	if err := ddbApplyVectorIndexUpdates(&table, []map[string]any{create}); err == nil {
		t.Error("creating an index that already exists was accepted")
	}
	del := map[string]any{"Delete": map[string]any{"IndexName": "by-embedding"}}
	if err := ddbApplyVectorIndexUpdates(&table, []map[string]any{del}); err != nil {
		t.Fatalf("delete was refused: %v", err)
	}
	if len(table.VectorIndexes) != 0 {
		t.Fatalf("table still carries %d vector indexes after the delete", len(table.VectorIndexes))
	}
	if err := ddbApplyVectorIndexUpdates(&table, []map[string]any{del}); err == nil {
		t.Error("deleting an index that does not exist was accepted")
	}
	both := map[string]any{"Create": create["Create"], "Delete": del["Delete"]}
	if err := ddbApplyVectorIndexUpdates(&table, []map[string]any{both}); err == nil {
		t.Error("an update naming both Create and Delete was accepted")
	}
	if err := ddbApplyVectorIndexUpdates(&table, []map[string]any{{}}); err == nil {
		t.Error("an update naming neither Create nor Delete was accepted")
	}
}

// ddbVectorCall drives one DynamoDB operation through the registered router,
// the way an SDK client reaches it.
func ddbVectorCall(t *testing.T, router interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, op, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.0")
	r.Header.Set("X-Amz-Target", "DynamoDB_20120810."+op)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The end-to-end claim: a search returns the nearest items, in order, with the
// score the comparison produced — not the items in the order they were stored.
//
// The three documents are placed so the answer is decidable by hand. Under
// EUCLIDEAN the query [0,0] is nearest to near (1 away), then mid (5), then far
// (13); the items are written in the opposite order, so storage order and the
// right answer disagree and only one of them can produce this result.
func TestDDBSearchVectorsReturnsTheNearestNeighboursInOrder(t *testing.T) {
	_, jsonRouter, _ := buildConformanceSimulator(t)

	code, _ := ddbVectorCall(t, jsonRouter, "CreateTable", `{
		"TableName":"docs",
		"KeySchema":[{"AttributeName":"pk","KeyType":"HASH"}],
		"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"}],
		"VectorIndexes":[{"IndexName":"by-embedding","VectorAttribute":{"AttributeName":"embedding"},
			"Dimensions":2,"DistanceFunction":"EUCLIDEAN","Projection":{"ProjectionType":"ALL"}}]}`)
	if code != http.StatusOK {
		t.Fatalf("CreateTable with a vector index = %d", code)
	}

	// Written furthest-first, so storage order is the wrong answer.
	for _, item := range []struct{ pk, x, y, kind string }{
		{"far", "5", "12", "archived"},
		{"mid", "3", "4", "live"},
		{"near", "1", "0", "live"},
	} {
		code, out := ddbVectorCall(t, jsonRouter, "PutItem", `{"TableName":"docs","Item":{
			"pk":{"S":"`+item.pk+`"},"kind":{"S":"`+item.kind+`"},
			"embedding":{"L":[{"N":"`+item.x+`"},{"N":"`+item.y+`"}]}}}`)
		if code != http.StatusOK {
			t.Fatalf("PutItem %s = %d (%v)", item.pk, code, out)
		}
	}

	code, out := ddbVectorCall(t, jsonRouter, "SearchVectors", `{"TableName":"docs","IndexName":"by-embedding",
		"SearchVector":[{"N":"0"},{"N":"0"}],"TopK":2}`)
	if code != http.StatusOK {
		t.Fatalf("SearchVectors = %d (%v)", code, out)
	}
	results, _ := out["SearchResults"].([]any)
	if len(results) != 2 {
		t.Fatalf("SearchResults has %d entries, want the 2 asked for: %v", len(results), out)
	}
	wantOrder := []struct {
		pk    string
		score float64
	}{{"near", 1}, {"mid", 5}}
	for i, want := range wantOrder {
		entry, _ := results[i].(map[string]any)
		item, _ := entry["Item"].(map[string]any)
		pk, _ := item["pk"].(map[string]any)
		if pk["S"] != want.pk {
			t.Errorf("result %d is %v, want %s — nearest first, not stored order", i, pk["S"], want.pk)
		}
		score, _ := entry["Score"].(float64)
		if math.Abs(score-want.score) > 1e-9 {
			t.Errorf("result %d score = %v, want %v (the distance itself)", i, score, want.score)
		}
	}
}

// SearchConditionExpression narrows what is searched, so an item excluded by it
// is not a neighbour however near it lies. Here the nearest document is
// archived, and asking only for live ones must answer with the next nearest
// rather than with the archived one.
func TestDDBSearchVectorsHonoursItsCondition(t *testing.T) {
	_, jsonRouter, _ := buildConformanceSimulator(t)
	ddbVectorCall(t, jsonRouter, "CreateTable", `{
		"TableName":"docs2",
		"KeySchema":[{"AttributeName":"pk","KeyType":"HASH"}],
		"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"}],
		"VectorIndexes":[{"IndexName":"by-embedding","VectorAttribute":{"AttributeName":"embedding"},
			"Dimensions":2,"DistanceFunction":"EUCLIDEAN","Projection":{"ProjectionType":"ALL"}}]}`)
	for _, item := range []struct{ pk, x, kind string }{
		{"nearest-archived", "1", "archived"},
		{"next-live", "2", "live"},
	} {
		ddbVectorCall(t, jsonRouter, "PutItem", `{"TableName":"docs2","Item":{
			"pk":{"S":"`+item.pk+`"},"kind":{"S":"`+item.kind+`"},
			"embedding":{"L":[{"N":"`+item.x+`"},{"N":"0"}]}}}`)
	}

	code, out := ddbVectorCall(t, jsonRouter, "SearchVectors", `{"TableName":"docs2","IndexName":"by-embedding",
		"SearchVector":[{"N":"0"},{"N":"0"}],"TopK":5,
		"SearchConditionExpression":"kind = :live","ExpressionAttributeValues":{":live":{"S":"live"}}}`)
	if code != http.StatusOK {
		t.Fatalf("SearchVectors = %d (%v)", code, out)
	}
	results, _ := out["SearchResults"].([]any)
	if len(results) != 1 {
		t.Fatalf("SearchResults has %d entries, want only the live one: %v", len(results), out)
	}
	entry, _ := results[0].(map[string]any)
	item, _ := entry["Item"].(map[string]any)
	pk, _ := item["pk"].(map[string]any)
	if pk["S"] != "next-live" {
		t.Errorf("result is %v, want next-live — the nearer document is excluded by the condition", pk["S"])
	}
}

// A query vector of the wrong length is not comparable to anything the index
// holds, and an index the table does not have is not searchable. Both are
// refused rather than answered with an empty result, which a caller could not
// tell from "no neighbours".
func TestDDBSearchVectorsRefusesWhatItCannotAnswer(t *testing.T) {
	_, jsonRouter, _ := buildConformanceSimulator(t)
	ddbVectorCall(t, jsonRouter, "CreateTable", `{
		"TableName":"docs3",
		"KeySchema":[{"AttributeName":"pk","KeyType":"HASH"}],
		"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"}],
		"VectorIndexes":[{"IndexName":"by-embedding","VectorAttribute":{"AttributeName":"embedding"},
			"Dimensions":2,"DistanceFunction":"COSINE","Projection":{"ProjectionType":"ALL"}}]}`)

	code, out := ddbVectorCall(t, jsonRouter, "SearchVectors", `{"TableName":"docs3","IndexName":"by-embedding",
		"SearchVector":[{"N":"1"},{"N":"2"},{"N":"3"}],"TopK":1}`)
	if code != http.StatusBadRequest {
		t.Errorf("a 3-dimension query against a 2-dimension index = %d, want 400 (%v)", code, out)
	}
	code, out = ddbVectorCall(t, jsonRouter, "SearchVectors", `{"TableName":"docs3","IndexName":"absent",
		"SearchVector":[{"N":"1"},{"N":"2"}],"TopK":1}`)
	if code != http.StatusBadRequest {
		t.Errorf("a search against an index the table has not got = %d, want 400 (%v)", code, out)
	}
	code, out = ddbVectorCall(t, jsonRouter, "SearchVectors", `{"TableName":"docs3","IndexName":"by-embedding",
		"SearchVector":[{"N":"1"},{"N":"2"}],"TopK":0}`)
	if code != http.StatusBadRequest {
		t.Errorf("TopK of zero = %d, want 400 (%v)", code, out)
	}
}

// DescribeTable reports the table's vector indexes, so a client can discover
// what is searchable rather than having to remember what it created.
func TestDDBDescribeTableReportsItsVectorIndexes(t *testing.T) {
	_, jsonRouter, _ := buildConformanceSimulator(t)
	ddbVectorCall(t, jsonRouter, "CreateTable", `{
		"TableName":"docs4",
		"KeySchema":[{"AttributeName":"pk","KeyType":"HASH"}],
		"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"}]}`)

	code, out := ddbVectorCall(t, jsonRouter, "UpdateTable", `{"TableName":"docs4","VectorIndexUpdates":[
		{"Create":{"IndexName":"by-embedding","VectorAttribute":{"AttributeName":"embedding"},
			"Dimensions":4,"DistanceFunction":"DOT_PRODUCT","Projection":{"ProjectionType":"ALL"}}}]}`)
	if code != http.StatusOK {
		t.Fatalf("UpdateTable creating a vector index = %d (%v)", code, out)
	}

	code, out = ddbVectorCall(t, jsonRouter, "DescribeTable", `{"TableName":"docs4"}`)
	if code != http.StatusOK {
		t.Fatalf("DescribeTable = %d", code)
	}
	desc, _ := out["Table"].(map[string]any)
	indexes, _ := desc["VectorIndexes"].([]any)
	if len(indexes) != 1 {
		t.Fatalf("DescribeTable reports %d vector indexes, want 1: %v", len(indexes), desc)
	}
	idx, _ := indexes[0].(map[string]any)
	if idx["IndexName"] != "by-embedding" || idx["DistanceFunction"] != "DOT_PRODUCT" {
		t.Errorf("reported index = %v, want by-embedding under DOT_PRODUCT", idx)
	}
	if idx["IndexStatus"] != "ACTIVE" {
		t.Errorf("index status = %v, want ACTIVE", idx["IndexStatus"])
	}

	code, _ = ddbVectorCall(t, jsonRouter, "UpdateTable", `{"TableName":"docs4","VectorIndexUpdates":[
		{"Delete":{"IndexName":"by-embedding"}}]}`)
	if code != http.StatusOK {
		t.Fatalf("UpdateTable deleting a vector index = %d", code)
	}
	_, out = ddbVectorCall(t, jsonRouter, "DescribeTable", `{"TableName":"docs4"}`)
	desc, _ = out["Table"].(map[string]any)
	if indexes, _ := desc["VectorIndexes"].([]any); len(indexes) != 0 {
		t.Errorf("DescribeTable still reports %d vector indexes after the delete", len(indexes))
	}
}
