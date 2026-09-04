package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// securityPolicies.listPreconfiguredExpressionSets — Cloud Armor's catalogue of
// preconfigured WAF signatures.
//
// Vendored from Google's own documentation by
// scripts/fetch-gcp-cloud-armor-expression-sets.sh: 70 expression sets, every
// signature with the sensitivity level the page gives it. The signatures live
// in per-version tables and the sets in a status table, joined by the
// identifier, which names both the CRS release and the category; the generator
// checks that every set the status tables declare comes out of that derivation
// and fails if one does not.

//go:embed compute_waf_expression_sets_vendored.json
var wafExpressionSetsJSON []byte

type wafExpressionSetCatalog struct {
	Source         string `json:"source"`
	Retrieved      string `json:"retrieved"`
	ExpressionSets []struct {
		ID          string `json:"id"`
		Expressions []struct {
			ID          string `json:"id"`
			Sensitivity int    `json:"sensitivity"`
		} `json:"expressions"`
	} `json:"expressionSets"`
}

var wafExpressionSets = sync.OnceValue(func() wafExpressionSetCatalog {
	var catalog wafExpressionSetCatalog
	if err := json.Unmarshal(wafExpressionSetsJSON, &catalog); err != nil {
		panic("vendored WAF expression-set catalogue is not valid JSON: " + err.Error())
	}
	if len(catalog.ExpressionSets) == 0 {
		panic("vendored WAF expression-set catalogue is empty")
	}
	return catalog
})

// wafPreconfiguredExpressionSetsBody renders the catalogue in the shape the
// method declares: wafRules.expressionSets, each with its expressions.
func wafPreconfiguredExpressionSetsBody() map[string]any {
	catalog := wafExpressionSets()
	sets := make([]any, 0, len(catalog.ExpressionSets))
	for _, entry := range catalog.ExpressionSets {
		expressions := make([]any, 0, len(entry.Expressions))
		for _, expression := range entry.Expressions {
			expressions = append(expressions, map[string]any{
				"id": expression.ID, "sensitivity": expression.Sensitivity})
		}
		sets = append(sets, map[string]any{"id": entry.ID, "expressions": expressions})
	}
	return map[string]any{
		"preconfiguredExpressionSets": map[string]any{
			"wafRules": map[string]any{"expressionSets": sets},
		},
	}
}

func handleWafPreconfiguredExpressionSets(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, wafPreconfiguredExpressionSetsBody())
}
