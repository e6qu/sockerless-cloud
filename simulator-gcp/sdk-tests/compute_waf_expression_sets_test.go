package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompute_PreconfiguredExpressionSetsAreTheVendoredCatalog covers
// securityPolicies.listPreconfiguredExpressionSets, which used to decline as a
// catalogue only Google can publish. It is published, and it is vendored from
// that publication.
//
// This is the read a caller makes before writing evaluatePreconfiguredExpr into
// a Cloud Armor rule, so the answer has to carry the signature identifiers a
// rule would name and the sensitivity each is filed under.
func TestCompute_PreconfiguredExpressionSetsAreTheVendoredCatalog(t *testing.T) {
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

	// The JSON SQLi set is the one whose identifier carries no CRS-version
	// segment. Composing it by analogy with every other id on the page gives a
	// string that exists nowhere, so it is read rather than built.
	assert.Equal(t, []string{"owasp-crs-id942550-sqli"}, byID["json-sqli-canary"])
	assert.Len(t, byID["cve-canary"], 6, "four OWASP Log4j signatures and two google-mrs React ones")
}
