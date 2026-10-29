package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompute_PreconfiguredExpressionSetsAreTheVendoredCatalog: a caller reads
// securityPolicies.listPreconfiguredExpressionSets, vendored from Google's
// publication, before writing evaluatePreconfiguredExpr into a Cloud Armor
// rule, so the answer carries the signature identifiers and their sensitivity.
func TestCompute_PreconfiguredExpressionSetsAreTheVendoredCatalog(t *testing.T) {
	t.Parallel()
	ensureProject(t, "waf-expression-project")
	svc := computeService(t)
	const project = "waf-expression-project"

	response, err := svc.SecurityPolicies.ListPreconfiguredExpressionSets(project).Do()
	require.NoError(t, err)
	require.NotNil(t, response.PreconfiguredExpressionSets)
	require.NotNil(t, response.PreconfiguredExpressionSets.WafRules)
	sets := response.PreconfiguredExpressionSets.WafRules.ExpressionSets
	require.NotEmpty(t, sets)

	byID := map[string][]string{}
	for _, set := range sets {
		require.NotEmpty(t, set.Id)
		require.NotEmpty(t, set.Expressions, "%s carries signatures", set.Id)
		for _, expression := range set.Expressions {
			byID[set.Id] = append(byID[set.Id], expression.Id)
		}
	}

	// The three CRS families Cloud Armor offers are all present.
	for _, id := range []string{"sqli-v422-stable", "sqli-v33-stable", "sqli-stable"} {
		assert.NotEmpty(t, byID[id], "%s is one of the sets Google's page declares", id)
	}

	// A signature carries the sensitivity a rule filters on.
	var sqli struct{ found bool }
	for _, set := range sets {
		if set.Id != "sqli-v422-stable" {
			continue
		}
		for _, expression := range set.Expressions {
			if expression.Id == "owasp-crs-v042200-id942100-sqli" {
				assert.EqualValues(t, 1, expression.Sensitivity)
				sqli.found = true
			}
		}
	}
	assert.True(t, sqli.found, "the libinjection signature is in the CRS 4.22 SQLi set")

	// The JSON SQLi set's identifier carries no CRS-version segment, so read it
	// rather than compose it by analogy.
	assert.Equal(t, []string{"owasp-crs-id942550-sqli"}, byID["json-sqli-canary"])
	assert.Len(t, byID["cve-canary"], 6, "four OWASP Log4j signatures and two google-mrs React ones")
}
