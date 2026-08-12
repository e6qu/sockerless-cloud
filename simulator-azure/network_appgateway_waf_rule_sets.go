package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The subscription-scoped web application firewall rule-set catalog:
// ApplicationGateways_ListAvailableWafRuleSets. Like the other application
// gateway catalogs it describes the product, not a deployment — every
// subscription receives the same answer, and the resource id/name/type
// convention (an id with empty subscription and resource-group segments) is
// the one Microsoft.Network itself returns, per the specification's
// ApplicationGatewayAvailableWafRuleSetsGet example.
//
// The catalog is vendored in network_appgateway_waf_rule_sets_vendored.json:
// all nine managed rule sets Application Gateway WAF offers — OWASP 3.2, 3.1,
// 3.0 and 2.2.9, Microsoft_BotManagerRuleSet 0.1, 1.0 and 1.1, and
// Microsoft_DefaultRuleSet 2.1 and 2.2 — with every rule group, rule id,
// description, state, action and supported tier. Sources (retrieved
// 2026-08-12):
//
//   - https://learn.microsoft.com/en-us/azure/web-application-firewall/ag/application-gateway-crs-rulegroups-rules
//     — Microsoft's per-version enumeration of every rule group, rule id and
//     description; the source of record for DRS 2.2, Bot Manager 1.1, and the
//     rules added to OWASP 3.1/3.2 and DRS 2.1 (800114, 800115, 99001018).
//     DRS 2.2 Paranoia Level 2 rules are disabled by default per that page.
//   - Recorded responses of the real service (Azure PowerShell Network test
//     session records, Azure/azure-powershell; a Get-AzApplicationGateway-
//     AvailableWafRuleSet capture, github.com/terenceluk/Azure) — the wire
//     spelling of rule descriptions, group names (DRS groups are FIX, JAVA,
//     NODEJS on the wire, not the documentation headings), group descriptions,
//     per-rule state/action defaults, the supported tiers per rule set, and
//     the full contents of the versions the documentation no longer fully
//     enumerates (OWASP 2.2.9's General and crs_49_inbound_blocking groups,
//     Bot Manager 0.1).
//
// The per-group rule counts are locked by TestApplicationGatewayWafRuleSetsVendoredCatalog
// so a partial vendor fails loudly.
//
//go:embed network_appgateway_waf_rule_sets_vendored.json
var applicationGatewayWafRuleSetsJSON []byte

// applicationGatewayWafRuleSets decodes the vendored catalog once. The
// vendored file is part of the build; if it does not decode, the binary is
// broken and the first request fails loudly rather than serving a truncated
// catalog.
var applicationGatewayWafRuleSets = sync.OnceValue(func() map[string]any {
	var catalog map[string]any
	if err := json.Unmarshal(applicationGatewayWafRuleSetsJSON, &catalog); err != nil {
		panic("vendored application gateway WAF rule-set catalog is not valid JSON: " + err.Error())
	}
	return catalog
})

func registerApplicationGatewayWafRuleSets(srv *sim.Server) {
	srv.HandleFunc("GET "+azureNetworkSubBase()+"/applicationGatewayAvailableWafRuleSets", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, applicationGatewayWafRuleSets())
	})
}
