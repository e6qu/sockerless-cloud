package main

import (
	"net/url"
	"strings"
	"testing"
)

// gateMethodTotal and gateUnsourcedCeil are the counts docs/iam.md, the README
// and BUGS.md quote. A method that gains a sourced permission lowers the
// ceiling; a refreshed Discovery document moves the total.
const (
	gateMethodTotal   = 2891
	gateUnsourcedCeil = 715
)

// TestIAMGate_SourcedPermissionCoverage renders every Discovery method's first
// path template and counts the methods the gate admits only an Owner to.
func TestIAMGate_SourcedPermissionCoverage(t *testing.T) {
	methods, err := gateDiscoveryMethods()
	if err != nil {
		t.Fatal(err)
	}
	unsourced := 0
	for _, m := range methods {
		if len(m.templates) == 0 {
			continue
		}
		tmpl := m.templates[0]
		req := make([]string, 0, len(tmpl.segs))
		params := map[string]string{}
		for _, seg := range tmpl.segs {
			if seg.param == "" {
				req = append(req, seg.literal)
				continue
			}
			params[seg.param] = "x"
			req = append(req, seg.prefix+"x"+seg.suffix)
		}
		call := &gateCall{httpMethod: m.httpMethod, path: "/" + strings.Join(req, "/"), host: m.api.host,
			query: url.Values{}, fields: func() map[string]any { return map[string]any{} }}
		match := gateDiscoveryMatch{method: m, params: params, relative: gateRelativeName(req, tmpl)}
		for _, check := range gateChecks(call, match) {
			if check.unsourced {
				unsourced++
				break
			}
		}
	}
	if len(methods) != gateMethodTotal || unsourced != gateUnsourcedCeil {
		t.Fatalf("%d of %d Discovery methods have no sourced permission; the docs say %d of %d — update them and these constants",
			unsourced, len(methods), gateUnsourcedCeil, gateMethodTotal)
	}
}
