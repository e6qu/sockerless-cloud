package main

import (
	"regexp"
	"testing"
)

// TestWafExpressionSetsVendoredCatalog locks the vendored catalogue so a
// partial vendor fails loudly, and pins the two entries that a pattern match
// would get wrong.
func TestWafExpressionSetsVendoredCatalog(t *testing.T) {
	catalog := wafExpressionSets()

	if catalog.Source == "" || catalog.Retrieved == "" {
		t.Fatal("the catalogue records neither where it came from nor when")
	}
	if got, want := len(catalog.ExpressionSets), 70; got != want {
		t.Errorf("catalogue holds %d expression sets, want %d — regenerate with "+
			"scripts/fetch-gcp-cloud-armor-expression-sets.sh and move this number in the same commit", got, want)
	}

	byID := map[string][]string{}
	signatures := 0
	for _, set := range catalog.ExpressionSets {
		if set.ID == "" {
			t.Fatal("an expression set has no id")
		}
		if len(set.Expressions) == 0 {
			t.Errorf("%s carries no signatures; a set with none is a set that was not read", set.ID)
		}
		for _, expression := range set.Expressions {
			if expression.ID == "" || expression.Sensitivity < 0 {
				t.Errorf("%s: signature %q has sensitivity %d", set.ID, expression.ID, expression.Sensitivity)
			}
			byID[set.ID] = append(byID[set.ID], expression.ID)
			signatures++
		}
	}
	if got, want := signatures, 953; got != want {
		t.Errorf("catalogue holds %d signature slots, want %d", got, want)
	}

	// A stable set and its canary are in sync, which the source states outright
	// for every pair it lists, so they hold the same signatures. The two
	// vulnerability sets have no stable half — cve-canary and json-sqli-canary
	// are canary-only, which is why their names carry no counterpart — so a
	// pair is only checked where the source has one.
	pairs := 0
	for id := range byID {
		stable := regexp.MustCompile(`-canary$`).ReplaceAllString(id, "-stable")
		if stable == id {
			continue
		}
		stableSet, paired := byID[stable]
		if !paired {
			continue
		}
		pairs++
		if len(byID[id]) != len(stableSet) {
			t.Errorf("%s holds %d signatures and %s holds %d, but the source says they are in sync",
				id, len(byID[id]), stable, len(stableSet))
		}
	}
	if got, want := pairs, 34; got != want {
		t.Errorf("%d stable/canary pairs, want %d — the two vulnerability sets are canary-only", got, want)
	}

	// The two sets whose signatures the page gives outside the versioned
	// tables. json-sqli-canary is the one that matters: its identifier carries
	// no CRS-version segment, so composing it by analogy with every other id
	// produces owasp-crs-v030001-id942550-sqli, which exists nowhere.
	jsonSQLi := byID["json-sqli-canary"]
	if len(jsonSQLi) != 1 || jsonSQLi[0] != "owasp-crs-id942550-sqli" {
		t.Errorf("json-sqli-canary holds %v, want the unversioned owasp-crs-id942550-sqli", jsonSQLi)
	}
	if got := len(byID["cve-canary"]); got != 6 {
		t.Errorf("cve-canary holds %d signatures, want 6 — four OWASP Log4j ids and two google-mrs React ids", got)
	}
}
