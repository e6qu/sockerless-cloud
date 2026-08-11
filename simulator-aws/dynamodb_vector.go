package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon DynamoDB vector search.
//
// A vector index names an attribute holding a fixed-dimension vector, a
// distance function, and a projection. SearchVectors compares a query vector
// against every indexed item's vector under that function and answers with the
// nearest TopK, each carrying the score the comparison produced.
//
// The search is arithmetic, and doing it for real is the whole point: returning
// items in storage order, or a score that is not the distance, produces
// neighbours a caller cannot tell from the nearest ones. That is the failure
// this simulator treats as a defect rather than an approximation.

// DDBVectorAttributeDefinition names the item attribute holding the vector.
type DDBVectorAttributeDefinition struct {
	AttributeName string `json:"AttributeName"`
}

// DDBVectorIndexDescription is a vector index as DescribeTable reports it. The
// member names are the wire's, so the description round-trips through the SDK
// as the modelled shape.
type DDBVectorIndexDescription struct {
	IndexName        string                       `json:"IndexName"`
	VectorAttribute  DDBVectorAttributeDefinition `json:"VectorAttribute"`
	SearchSchema     []map[string]any             `json:"SearchSchema,omitempty"`
	Projection       map[string]any               `json:"Projection,omitempty"`
	Dimensions       int64                        `json:"Dimensions"`
	DistanceFunction string                       `json:"DistanceFunction"`
	IndexStatus      string                       `json:"IndexStatus"`
	IndexArn         string                       `json:"IndexArn"`
	ItemCount        int64                        `json:"ItemCount"`
	IndexSizeBytes   int64                        `json:"IndexSizeBytes"`
}

// ddbVectorDistanceFunctions is the set the model declares. An unknown function
// is refused rather than defaulted, because defaulting would score by a measure
// the caller did not ask for and the answer would look valid.
var ddbVectorDistanceFunctions = map[string]bool{"COSINE": true, "DOT_PRODUCT": true, "EUCLIDEAN": true}

// ddbVectorIndexARN is the index's own ARN, under its table's.
func ddbVectorIndexARN(tableName, indexName string) string {
	return ddbTableArn(tableName) + "/vector-index/" + indexName
}

// ddbParseVectorIndex reads a CreateVectorIndexAction, or the VectorIndex form
// CreateTable takes, and validates what the model marks required.
func ddbParseVectorIndex(tableName string, raw map[string]any) (DDBVectorIndexDescription, error) {
	var idx DDBVectorIndexDescription
	idx.IndexName, _ = raw["IndexName"].(string)
	if idx.IndexName == "" {
		return idx, fmt.Errorf("a vector index must declare an IndexName")
	}
	attr, _ := raw["VectorAttribute"].(map[string]any)
	name, _ := attr["AttributeName"].(string)
	if name == "" {
		return idx, fmt.Errorf("vector index %q must declare a VectorAttribute.AttributeName", idx.IndexName)
	}
	idx.VectorAttribute = DDBVectorAttributeDefinition{AttributeName: name}

	dims, ok := ddbNumberOf(raw["Dimensions"])
	if !ok || dims < 1 {
		return idx, fmt.Errorf("vector index %q must declare a positive Dimensions", idx.IndexName)
	}
	idx.Dimensions = int64(dims)

	fn, _ := raw["DistanceFunction"].(string)
	if !ddbVectorDistanceFunctions[fn] {
		return idx, fmt.Errorf("vector index %q must declare a DistanceFunction of COSINE, DOT_PRODUCT or EUCLIDEAN", idx.IndexName)
	}
	idx.DistanceFunction = fn

	if projection, ok := raw["Projection"].(map[string]any); ok {
		idx.Projection = projection
	} else {
		return idx, fmt.Errorf("vector index %q must declare a Projection", idx.IndexName)
	}
	if schema, ok := raw["SearchSchema"].([]any); ok {
		for _, entry := range schema {
			if m, ok := entry.(map[string]any); ok {
				idx.SearchSchema = append(idx.SearchSchema, m)
			}
		}
	}
	idx.IndexStatus = "ACTIVE"
	idx.IndexArn = ddbVectorIndexARN(tableName, idx.IndexName)
	return idx, nil
}

// ddbNumberOf reads a JSON number that may have decoded as float64 or as the
// string DynamoDB uses for numeric attribute values.
func ddbNumberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// ddbItemVector reads an item's vector attribute. DynamoDB carries a vector as
// a list of numbers (L of N), which is what the item already stores, so the
// vector is read out of the real attribute value rather than a parallel copy.
func ddbItemVector(item map[string]any, attribute string) ([]float64, bool) {
	raw, ok := item[attribute].(map[string]any)
	if !ok {
		return nil, false
	}
	list, ok := raw["L"].([]any)
	if !ok {
		return nil, false
	}
	out := make([]float64, 0, len(list))
	for _, entry := range list {
		el, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		n, ok := ddbNumberOf(el["N"])
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// ddbVectorScore compares two vectors under one of the model's distance
// functions. The score is the measure itself: a similarity for COSINE and
// DOT_PRODUCT, where larger is nearer, and a distance for EUCLIDEAN, where
// smaller is. ddbVectorNearerFirst is what knows which way each one sorts.
func ddbVectorScore(fn string, query, candidate []float64) (float64, bool) {
	if len(query) != len(candidate) || len(query) == 0 {
		return 0, false
	}
	switch fn {
	case "DOT_PRODUCT":
		var dot float64
		for i := range query {
			dot += query[i] * candidate[i]
		}
		return dot, true
	case "EUCLIDEAN":
		var sum float64
		for i := range query {
			d := query[i] - candidate[i]
			sum += d * d
		}
		return math.Sqrt(sum), true
	case "COSINE":
		var dot, qn, cn float64
		for i := range query {
			dot += query[i] * candidate[i]
			qn += query[i] * query[i]
			cn += candidate[i] * candidate[i]
		}
		if qn == 0 || cn == 0 {
			// A zero vector has no direction, so it has no cosine similarity to
			// anything. Scoring it 0 would rank it as merely orthogonal, which
			// is a claim the geometry does not support.
			return 0, false
		}
		return dot / (math.Sqrt(qn) * math.Sqrt(cn)), true
	}
	return 0, false
}

// ddbVectorNearerFirst reports whether a higher score means a nearer neighbour.
func ddbVectorNearerFirst(fn string) bool { return fn != "EUCLIDEAN" }

// ddbTableVectorIndex finds a table's vector index by name.
func ddbTableVectorIndex(t DDBTable, indexName string) (DDBVectorIndexDescription, bool) {
	for _, idx := range t.VectorIndexes {
		if idx.IndexName == indexName {
			return idx, true
		}
	}
	return DDBVectorIndexDescription{}, false
}

// ddbApplyVectorIndexUpdates applies UpdateTable's VectorIndexUpdates, each of
// which creates or deletes exactly one index.
func ddbApplyVectorIndexUpdates(t *DDBTable, updates []map[string]any) error {
	for _, update := range updates {
		create, hasCreate := update["Create"].(map[string]any)
		del, hasDelete := update["Delete"].(map[string]any)
		switch {
		case hasCreate && hasDelete:
			return fmt.Errorf("a VectorIndexUpdate names both Create and Delete")
		case hasCreate:
			idx, err := ddbParseVectorIndex(t.TableName, create)
			if err != nil {
				return err
			}
			if _, exists := ddbTableVectorIndex(*t, idx.IndexName); exists {
				return fmt.Errorf("vector index %q already exists on table %q", idx.IndexName, t.TableName)
			}
			t.VectorIndexes = append(t.VectorIndexes, idx)
		case hasDelete:
			name, _ := del["IndexName"].(string)
			kept := t.VectorIndexes[:0:0]
			found := false
			for _, idx := range t.VectorIndexes {
				if idx.IndexName == name {
					found = true
					continue
				}
				kept = append(kept, idx)
			}
			if !found {
				return fmt.Errorf("vector index %q does not exist on table %q", name, t.TableName)
			}
			t.VectorIndexes = kept
		default:
			return fmt.Errorf("a VectorIndexUpdate names neither Create nor Delete")
		}
	}
	return nil
}

func handleDDBSearchVectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                 string            `json:"TableName"`
		IndexName                 string            `json:"IndexName"`
		SearchVector              []map[string]any  `json:"SearchVector"`
		TopK                      int               `json:"TopK"`
		SearchConditionExpression string            `json:"SearchConditionExpression"`
		ProjectionExpression      string            `json:"ProjectionExpression"`
		ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames"`
		ExpressionAttributeValues map[string]any    `json:"ExpressionAttributeValues"`
		ReturnConsumedCapacity    string            `json:"ReturnConsumedCapacity"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	idx, ok := ddbTableVectorIndex(t, req.IndexName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Requested resource not found: Vector index %s not found on table %s", req.IndexName, req.TableName)
		return
	}

	query := make([]float64, 0, len(req.SearchVector))
	for _, el := range req.SearchVector {
		n, ok := ddbNumberOf(el["N"])
		if !ok {
			sim.AWSError(w, "ValidationException", "SearchVector must be a list of numbers", http.StatusBadRequest)
			return
		}
		query = append(query, n)
	}
	if int64(len(query)) != idx.Dimensions {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest, "SearchVector has %d dimensions, but vector index %s has %d",
			len(query), idx.IndexName, idx.Dimensions)
		return
	}
	topK := req.TopK
	if topK <= 0 {
		sim.AWSError(w, "ValidationException", "TopK must be greater than zero", http.StatusBadRequest)
		return
	}

	// The condition narrows which items are searched, so it is compiled once
	// and a malformed one is refused before anything is scored.
	condition, err := ddbCompileExpr("SearchConditionExpression", req.SearchConditionExpression,
		req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}

	type scored struct {
		item  map[string]any
		score float64
	}
	var matches []scored
	for _, item := range ddbTableItemsSnapshot(req.TableName) {
		if condition != nil && !condition.match(item, true) {
			continue
		}
		vector, ok := ddbItemVector(item, idx.VectorAttribute.AttributeName)
		if !ok || len(vector) != len(query) {
			// An item without a vector of the index's shape is not indexed, so
			// it is not a neighbour — skipping it is what the index means.
			continue
		}
		score, ok := ddbVectorScore(idx.DistanceFunction, query, vector)
		if !ok {
			continue
		}
		matches = append(matches, scored{item: item, score: score})
	}

	nearerFirst := ddbVectorNearerFirst(idx.DistanceFunction)
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return false
		}
		if nearerFirst {
			return matches[i].score > matches[j].score
		}
		return matches[i].score < matches[j].score
	})
	if len(matches) > topK {
		matches = matches[:topK]
	}

	results := make([]map[string]any, 0, len(matches))
	searchBytes := 0.0
	for _, m := range matches {
		results = append(results, map[string]any{
			"Item":  ddbProjectItem(m.item, req.ProjectionExpression, req.ExpressionAttributeNames),
			"Score": m.score,
		})
		searchBytes += float64(ddbItemSizeBytes(m.item))
	}
	out := map[string]any{"SearchResults": results}
	// Vector search reports its capacity in bytes rather than in the read units
	// the rest of the API uses, which is why it has its own capacity shape.
	if !strings.EqualFold(req.ReturnConsumedCapacity, "") && !strings.EqualFold(req.ReturnConsumedCapacity, "NONE") {
		out["ConsumedCapacity"] = map[string]any{"VectorSearchRequestBytes": searchBytes}
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
