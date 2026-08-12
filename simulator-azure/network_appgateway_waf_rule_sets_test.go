package main

import (
	"encoding/json"
	"strconv"
	"testing"
)

// The vendored catalog mirrors the documentation exactly; these counts were
// taken per rule group from Microsoft's rule enumeration
// (application-gateway-crs-rulegroups-rules) cross-checked against recorded
// responses of the real service, so a truncated or partially updated vendor
// fails loudly here.

type wafRuleGroupCount struct {
	group string
	rules int
}

type wafRuleSetCount struct {
	ruleSetType    string
	ruleSetVersion string
	groups         []wafRuleGroupCount
}

var wantWafRuleSets = []wafRuleSetCount{
	{"OWASP", "3.2", []wafRuleGroupCount{
		{"General", 3},
		{"REQUEST-911-METHOD-ENFORCEMENT", 1},
		{"REQUEST-913-SCANNER-DETECTION", 5},
		{"REQUEST-920-PROTOCOL-ENFORCEMENT", 39},
		{"REQUEST-921-PROTOCOL-ATTACK", 9},
		{"REQUEST-930-APPLICATION-ATTACK-LFI", 4},
		{"REQUEST-931-APPLICATION-ATTACK-RFI", 4},
		{"REQUEST-932-APPLICATION-ATTACK-RCE", 14},
		{"REQUEST-933-APPLICATION-ATTACK-PHP", 16},
		{"REQUEST-941-APPLICATION-ATTACK-XSS", 28},
		{"REQUEST-942-APPLICATION-ATTACK-SQLI", 46},
		{"REQUEST-943-APPLICATION-ATTACK-SESSION-FIXATION", 3},
		{"REQUEST-944-APPLICATION-ATTACK-JAVA", 8},
		{"Known-CVEs", 7},
	}},
	{"OWASP", "3.1", []wafRuleGroupCount{
		{"General", 1},
		{"REQUEST-911-METHOD-ENFORCEMENT", 1},
		{"REQUEST-913-SCANNER-DETECTION", 5},
		{"REQUEST-920-PROTOCOL-ENFORCEMENT", 41},
		{"REQUEST-921-PROTOCOL-ATTACK", 9},
		{"REQUEST-930-APPLICATION-ATTACK-LFI", 4},
		{"REQUEST-931-APPLICATION-ATTACK-RFI", 4},
		{"REQUEST-932-APPLICATION-ATTACK-RCE", 14},
		{"REQUEST-933-APPLICATION-ATTACK-PHP", 14},
		{"REQUEST-941-APPLICATION-ATTACK-XSS", 27},
		{"REQUEST-942-APPLICATION-ATTACK-SQLI", 45},
		{"REQUEST-943-APPLICATION-ATTACK-SESSION-FIXATION", 3},
		{"REQUEST-944-APPLICATION-ATTACK-JAVA", 8},
		{"Known-CVEs", 7},
	}},
	{"OWASP", "3.0", []wafRuleGroupCount{
		{"General", 1},
		{"REQUEST-911-METHOD-ENFORCEMENT", 1},
		{"REQUEST-913-SCANNER-DETECTION", 5},
		{"REQUEST-920-PROTOCOL-ENFORCEMENT", 36},
		{"REQUEST-921-PROTOCOL-ATTACK", 10},
		{"REQUEST-930-APPLICATION-ATTACK-LFI", 4},
		{"REQUEST-931-APPLICATION-ATTACK-RFI", 4},
		{"REQUEST-932-APPLICATION-ATTACK-RCE", 11},
		{"REQUEST-933-APPLICATION-ATTACK-PHP", 13},
		{"REQUEST-941-APPLICATION-ATTACK-XSS", 26},
		{"REQUEST-942-APPLICATION-ATTACK-SQLI", 41},
		{"REQUEST-943-APPLICATION-ATTACK-SESSION-FIXATION", 3},
		{"Known-CVEs", 5},
	}},
	{"OWASP", "2.2.9", []wafRuleGroupCount{
		{"General", 1},
		{"crs_20_protocol_violations", 23},
		{"crs_21_protocol_anomalies", 8},
		{"crs_23_request_limits", 6},
		{"crs_30_http_policy", 5},
		{"crs_35_bad_robots", 4},
		{"crs_40_generic_attacks", 25},
		{"crs_41_sql_injection_attacks", 55},
		{"crs_41_xss_attacks", 113},
		{"crs_42_tight_security", 1},
		{"crs_45_trojans", 2},
		{"crs_49_inbound_blocking", 2},
	}},
	{"Microsoft_BotManagerRuleSet", "0.1", []wafRuleGroupCount{
		{"KnownBadBots", 1},
	}},
	{"Microsoft_BotManagerRuleSet", "1.0", []wafRuleGroupCount{
		{"BadBots", 2},
		{"GoodBots", 2},
		{"UnknownBots", 7},
	}},
	{"Microsoft_BotManagerRuleSet", "1.1", []wafRuleGroupCount{
		{"BadBots", 3},
		{"GoodBots", 7},
		{"UnknownBots", 7},
	}},
	{"Microsoft_DefaultRuleSet", "2.1", []wafRuleGroupCount{
		{"LFI", 4},
		{"RFI", 4},
		{"RCE", 12},
		{"PHP", 12},
		{"XSS", 30},
		{"SQLI", 40},
		{"FIX", 3},
		{"JAVA", 8},
		{"PROTOCOL-ATTACK", 9},
		{"METHOD-ENFORCEMENT", 1},
		{"PROTOCOL-ENFORCEMENT", 35},
		{"NODEJS", 1},
		{"General", 2},
		{"MS-ThreatIntel-WebShells", 5},
		{"MS-ThreatIntel-AppSec", 2},
		{"MS-ThreatIntel-CVEs", 18},
		{"MS-ThreatIntel-SQLI", 4},
	}},
	{"Microsoft_DefaultRuleSet", "2.2", []wafRuleGroupCount{
		{"General", 2},
		{"METHOD-ENFORCEMENT", 1},
		{"PROTOCOL-ENFORCEMENT", 37},
		{"PROTOCOL-ATTACK", 10},
		{"LFI", 4},
		{"RFI", 4},
		{"RCE", 12},
		{"PHP", 12},
		{"NODEJS", 1},
		{"XSS", 30},
		{"SQLI", 39},
		{"FIX", 3},
		{"JAVA", 8},
		{"MS-ThreatIntel-WebShells", 5},
		{"MS-ThreatIntel-AppSec", 6},
		{"MS-ThreatIntel-SQLI", 6},
		{"MS-ThreatIntel-CVEs", 18},
		{"MS-ThreatIntel-XSS", 2},
	}},
}

// wafVendoredCatalog is the decoded shape of the vendored file, using the
// wire field names of ApplicationGatewayAvailableWafRuleSetsResult.
type wafVendoredCatalog struct {
	Value []struct {
		Name       string `json:"name"`
		ID         string `json:"id"`
		Type       string `json:"type"`
		Properties struct {
			ProvisioningState string   `json:"provisioningState"`
			RuleSetType       string   `json:"ruleSetType"`
			RuleSetVersion    string   `json:"ruleSetVersion"`
			Tiers             []string `json:"tiers"`
			RuleGroups        []struct {
				RuleGroupName string `json:"ruleGroupName"`
				Description   string `json:"description"`
				Rules         []struct {
					RuleID       int    `json:"ruleId"`
					RuleIDString string `json:"ruleIdString"`
					Description  string `json:"description"`
					State        string `json:"state"`
					Action       string `json:"action"`
				} `json:"rules"`
			} `json:"ruleGroups"`
		} `json:"properties"`
	} `json:"value"`
}

func TestApplicationGatewayWafRuleSetsVendoredCatalog(t *testing.T) {
	var catalog wafVendoredCatalog
	if err := json.Unmarshal(applicationGatewayWafRuleSetsJSON, &catalog); err != nil {
		t.Fatalf("vendored catalog does not decode: %v", err)
	}
	if got, want := len(catalog.Value), len(wantWafRuleSets); got != want {
		t.Fatalf("catalog has %d rule sets, want %d", got, want)
	}

	const (
		wantID   = "/subscriptions//resourceGroups//providers/Microsoft.Network/applicationGatewayAvailableWafRuleSets/"
		wantType = "Microsoft.Network/applicationGatewayAvailableWafRuleSets"
	)
	totalRules := 0
	for i, want := range wantWafRuleSets {
		rs := catalog.Value[i]
		p := rs.Properties
		if p.RuleSetType != want.ruleSetType || p.RuleSetVersion != want.ruleSetVersion {
			t.Fatalf("rule set %d is %s %s, want %s %s", i, p.RuleSetType, p.RuleSetVersion, want.ruleSetType, want.ruleSetVersion)
		}
		if wantName := want.ruleSetType + "_" + want.ruleSetVersion; rs.Name != wantName {
			t.Errorf("%s: name %q, want %q", wantName, rs.Name, wantName)
		}
		if rs.ID != wantID {
			t.Errorf("%s %s: id %q, want %q", p.RuleSetType, p.RuleSetVersion, rs.ID, wantID)
		}
		if rs.Type != wantType {
			t.Errorf("%s %s: type %q, want %q", p.RuleSetType, p.RuleSetVersion, rs.Type, wantType)
		}
		if p.ProvisioningState != "Succeeded" {
			t.Errorf("%s %s: provisioningState %q, want Succeeded", p.RuleSetType, p.RuleSetVersion, p.ProvisioningState)
		}
		if len(p.Tiers) == 0 {
			t.Errorf("%s %s: no tiers", p.RuleSetType, p.RuleSetVersion)
		}
		if got, wantGroups := len(p.RuleGroups), len(want.groups); got != wantGroups {
			t.Fatalf("%s %s: %d rule groups, want %d", p.RuleSetType, p.RuleSetVersion, got, wantGroups)
		}
		for j, wantGroup := range want.groups {
			g := p.RuleGroups[j]
			if g.RuleGroupName != wantGroup.group {
				t.Errorf("%s %s group %d: %q, want %q", p.RuleSetType, p.RuleSetVersion, j, g.RuleGroupName, wantGroup.group)
			}
			if len(g.Rules) != wantGroup.rules {
				t.Errorf("%s %s %s: %d rules, want %d", p.RuleSetType, p.RuleSetVersion, g.RuleGroupName, len(g.Rules), wantGroup.rules)
			}
			for _, r := range g.Rules {
				if r.RuleIDString != strconv.Itoa(r.RuleID) {
					t.Errorf("%s %s %s rule %d: ruleIdString %q", p.RuleSetType, p.RuleSetVersion, g.RuleGroupName, r.RuleID, r.RuleIDString)
				}
				if r.Description == "" {
					t.Errorf("%s %s %s rule %d: empty description", p.RuleSetType, p.RuleSetVersion, g.RuleGroupName, r.RuleID)
				}
				if r.State != "Enabled" && r.State != "Disabled" {
					t.Errorf("%s %s %s rule %d: state %q", p.RuleSetType, p.RuleSetVersion, g.RuleGroupName, r.RuleID, r.State)
				}
				switch r.Action {
				case "AnomalyScoring", "Allow", "Block", "Log", "None":
				default:
					t.Errorf("%s %s %s rule %d: action %q", p.RuleSetType, p.RuleSetVersion, g.RuleGroupName, r.RuleID, r.Action)
				}
				totalRules++
			}
		}
	}
	if totalRules != 1194 {
		t.Fatalf("catalog carries %d rules, want 1194", totalRules)
	}

	// Documented canary rules, description verbatim from Microsoft's
	// enumeration and the recorded wire responses.
	canaries := []struct {
		set, group    string
		ruleID        int
		description   string
		state, action string
	}{
		{"Microsoft_DefaultRuleSet_2.1", "SQLI", 942100, "SQL Injection Attack Detected via libinjection", "Enabled", "AnomalyScoring"},
		{"Microsoft_DefaultRuleSet_2.1", "MS-ThreatIntel-CVEs", 99001017, "Attempted Apache Struts file upload exploitation (CVE-2023-50164)", "Enabled", "Log"},
		{"Microsoft_DefaultRuleSet_2.1", "MS-ThreatIntel-CVEs", 99001018, "Attempted React2Shell remote code execution exploitation (CVE-2025-55182)", "Enabled", "AnomalyScoring"},
		{"Microsoft_DefaultRuleSet_2.2", "MS-ThreatIntel-XSS", 99032001, "XSS Filter - Category 2: Event Handler Vector (replacing rule #941120)", "Enabled", "AnomalyScoring"},
		{"Microsoft_DefaultRuleSet_2.2", "PROTOCOL-ENFORCEMENT", 920121, "Attempted multipart/form-data bypass", "Disabled", "AnomalyScoring"},
		{"OWASP_3.2", "Known-CVEs", 800115, "Attempted React2Shell remote code execution exploitation (CVE-2025-55182)", "Enabled", "AnomalyScoring"},
		{"OWASP_2.2.9", "crs_45_trojans", 950110, "Backdoor access", "Enabled", "AnomalyScoring"},
		{"Microsoft_BotManagerRuleSet_1.1", "UnknownBots", 300700, "Other bots", "Enabled", "Log"},
	}
	for _, c := range canaries {
		found := false
		for _, rs := range catalog.Value {
			if rs.Name != c.set {
				continue
			}
			for _, g := range rs.Properties.RuleGroups {
				if g.RuleGroupName != c.group {
					continue
				}
				for _, r := range g.Rules {
					if r.RuleID != c.ruleID {
						continue
					}
					found = true
					if r.Description != c.description {
						t.Errorf("%s %s %d: description %q, want %q", c.set, c.group, c.ruleID, r.Description, c.description)
					}
					if r.State != c.state || r.Action != c.action {
						t.Errorf("%s %s %d: state/action %s/%s, want %s/%s", c.set, c.group, c.ruleID, r.State, r.Action, c.state, c.action)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s %s rule %d missing from the vendored catalog", c.set, c.group, c.ruleID)
		}
	}
}
