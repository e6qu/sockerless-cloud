package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// securityPolicies.listPreconfiguredExpressionSets serves Cloud Armor's
// preconfigured WAF signatures, vendored by
// scripts/fetch-gcp-cloud-armor-expression-sets.sh: 70 expression sets, each
// signature with its sensitivity level. The generator fails unless every set
// the status tables declare derives from the per-version signature tables.

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
