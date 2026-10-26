package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// SearchVectors scores every indexed item's vector against the query under the
// index's distance function and returns the nearest TopK with their scores.
// Storage order or a score that is not the distance would yield neighbours a
// caller cannot tell from the nearest.

type DDBVectorAttributeDefinition struct {
	AttributeName string `json:"AttributeName"`
}

// Member names are the wire's, so the description round-trips through the
// SDK.
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

// Refuse an unknown function: a default would score by a measure the caller
// did not ask for.
var ddbVectorDistanceFunctions = map[string]bool{"COSINE": true, "DOT_PRODUCT": true, "EUCLIDEAN": true}

func ddbVectorIndexARN(tableName, indexName string) string {
	return ddbTableArn(tableName) + "/vector-index/" + indexName
}

// Accepts a CreateVectorIndexAction or CreateTable's VectorIndex form.
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

// The number may decode as float64 or as DynamoDB's numeric string.
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

// DynamoDB stores a vector as an L of N; read it from the attribute itself.
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

// COSINE and DOT_PRODUCT are similarities, larger nearer; EUCLIDEAN is a
// distance, smaller nearer.
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
			// A zero vector has no direction and so no cosine similarity; 0 would rank it
			// as orthogonal.
			return 0, false
		}
		return dot / (math.Sqrt(qn) * math.Sqrt(cn)), true
	}
	return 0, false
}

func ddbVectorNearerFirst(fn string) bool { return fn != "EUCLIDEAN" }

func ddbTableVectorIndex(t DDBTable, indexName string) (DDBVectorIndexDescription, bool) {
	for _, idx := range t.VectorIndexes {
		if idx.IndexName == indexName {
			return idx, true
		}
	}
	return DDBVectorIndexDescription{}, false
}

// Each update creates or deletes exactly one index.
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
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	idx, ok := ddbTableVectorIndex(t, req.IndexName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Requested resource not found: Vector index %s not found on table %s", req.IndexName, req.TableName)
		return
	}

	query := make([]float64, 0, len(req.SearchVector))
	for _, el := range req.SearchVector {
		n, ok := ddbNumberOf(el["N"])
		if !ok {
			AWSError(w, "ValidationException", "SearchVector must be a list of numbers", http.StatusBadRequest)
			return
		}
		query = append(query, n)
	}
	if int64(len(query)) != idx.Dimensions {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest, "SearchVector has %d dimensions, but vector index %s has %d",
			len(query), idx.IndexName, idx.Dimensions)
		return
	}
	topK := req.TopK
	if topK <= 0 {
		AWSError(w, "ValidationException", "TopK must be greater than zero", http.StatusBadRequest)
		return
	}

	// Refuse a malformed condition before scoring anything.
	condition, err := ddbCompileExpr("SearchConditionExpression", req.SearchConditionExpression,
		req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
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
			// An item without a vector of the index's shape is not indexed.
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
	// Vector search reports capacity in bytes, not read units.
	if !strings.EqualFold(req.ReturnConsumedCapacity, "") && !strings.EqualFold(req.ReturnConsumedCapacity, "NONE") {
		out["ConsumedCapacity"] = map[string]any{"VectorSearchRequestBytes": searchBytes}
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
