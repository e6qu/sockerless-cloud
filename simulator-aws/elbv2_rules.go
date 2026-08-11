package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ELBv2 listener rules + ModifyListener. Listeners were read-only
// after creation; the `aws_alb_listener_rule` resource (host-header /
// path-pattern routing — the IAP-proxy ALB shape) needs rule CRUD, and
// updating a listener's default action / certificates needs ModifyListener.

type ELBv2Rule struct {
	Arn         string
	ListenerArn string
	Priority    string
	Conditions  []ELBv2RuleCondition
	Actions     []ELBv2Action
	IsDefault   bool
}

type ELBv2RuleCondition struct {
	Field          string
	Values         []string
	HttpHeaderName string
	QueryStrings   []ELBv2QueryStringKV
}

type ELBv2QueryStringKV struct {
	Key   string
	Value string
}

var elbv2Rules sim.Store[ELBv2Rule]

func registerELBv2Rules(r *sim.AWSQueryRouter, srv *sim.Server) {
	elbv2Rules = sim.MakeStore[ELBv2Rule](srv.DB(), "elbv2_rules")
	r.RegisterVersioned(elbv2APIVersion, "CreateRule", handleELBv2CreateRule)
	r.RegisterVersioned(elbv2APIVersion, "DescribeRules", handleELBv2DescribeRules)
	r.RegisterVersioned(elbv2APIVersion, "ModifyRule", handleELBv2ModifyRule)
	r.RegisterVersioned(elbv2APIVersion, "DeleteRule", handleELBv2DeleteRule)
	r.RegisterVersioned(elbv2APIVersion, "SetRulePriorities", handleELBv2SetRulePriorities)
	r.RegisterVersioned(elbv2APIVersion, "ModifyListener", handleELBv2ModifyListener)
	r.RegisterVersioned(elbv2APIVersion, "AddListenerCertificates", handleELBv2AddListenerCertificates)
	r.RegisterVersioned(elbv2APIVersion, "RemoveListenerCertificates", handleELBv2RemoveListenerCertificates)
	r.RegisterVersioned(elbv2APIVersion, "DescribeListenerCertificates", handleELBv2DescribeListenerCertificates)
}

func handleELBv2CreateRule(w http.ResponseWriter, r *http.Request) {
	listenerArn := r.FormValue("ListenerArn")
	listener, ok := elbv2Listeners.Get(listenerArn)
	if !ok {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rule := ELBv2Rule{
		Arn:         elbv2RuleArn(listener.Arn, generateUUID()[:12]),
		ListenerArn: listenerArn,
		Priority:    r.FormValue("Priority"),
		Conditions:  parseELBv2Conditions(r),
		Actions:     parseELBv2ActionsPrefix(r, "Actions"),
	}
	elbv2Rules.Put(rule.Arn, rule)
	elbv2XMLResponse(w, "CreateRule", "<Rules>"+elbv2RuleXML(rule)+"</Rules>", sim.RequestID(r.Context()))
}

func handleELBv2DescribeRules(w http.ResponseWriter, r *http.Request) {
	ruleArns := queryList(r, "RuleArns")
	listenerArn := r.FormValue("ListenerArn")

	var rules []ELBv2Rule
	switch {
	case len(ruleArns) > 0:
		for _, arn := range ruleArns {
			rl, ok := elbv2Rules.Get(arn)
			if !ok {
				elbv2ErrorXML(w, "RuleNotFound", "Rule '"+arn+"' not found",
					http.StatusBadRequest, sim.RequestID(r.Context()))
				return
			}
			rules = append(rules, rl)
		}
	case listenerArn != "":
		listener, ok := elbv2Listeners.Get(listenerArn)
		if !ok {
			elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		for _, rl := range elbv2Rules.List() {
			if rl.ListenerArn == listenerArn {
				rules = append(rules, rl)
			}
		}
		sort.Slice(rules, func(i, j int) bool {
			return atoiDefault(rules[i].Priority, 1<<30) < atoiDefault(rules[j].Priority, 1<<30)
		})
		// The implicit default rule (carrying the listener's default action)
		// always sorts last, exactly as real ELBv2 reports it.
		rules = append(rules, elbv2DefaultRule(listener))
	default:
		elbv2ErrorXML(w, "ValidationError", "Either ListenerArn or RuleArns must be specified",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}

	var b strings.Builder
	b.WriteString("<Rules>")
	for _, rl := range rules {
		b.WriteString(elbv2RuleXML(rl))
	}
	b.WriteString("</Rules>")
	elbv2XMLResponse(w, "DescribeRules", b.String(), sim.RequestID(r.Context()))
}

func handleELBv2ModifyRule(w http.ResponseWriter, r *http.Request) {
	ruleArn := r.FormValue("RuleArn")
	rule, ok := elbv2Rules.Get(ruleArn)
	if !ok {
		elbv2ErrorXML(w, "RuleNotFound", "Rule not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if conds := parseELBv2Conditions(r); len(conds) > 0 {
		rule.Conditions = conds
	}
	if actions := parseELBv2ActionsPrefix(r, "Actions"); len(actions) > 0 {
		rule.Actions = actions
	}
	elbv2Rules.Put(ruleArn, rule)
	elbv2XMLResponse(w, "ModifyRule", "<Rules>"+elbv2RuleXML(rule)+"</Rules>", sim.RequestID(r.Context()))
}

func handleELBv2DeleteRule(w http.ResponseWriter, r *http.Request) {
	ruleArn := r.FormValue("RuleArn")
	if _, ok := elbv2Rules.Get(ruleArn); !ok {
		elbv2ErrorXML(w, "RuleNotFound", "Rule not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2Rules.Delete(ruleArn)
	elbv2XMLResponse(w, "DeleteRule", "", sim.RequestID(r.Context()))
}

func handleELBv2ModifyListener(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ListenerArn")
	previous, ok := elbv2Listeners.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	listener := previous
	if p := r.FormValue("Port"); p != "" {
		listener.Port = atoiDefault(p, listener.Port)
	}
	if proto := r.FormValue("Protocol"); proto != "" {
		listener.Protocol = proto
	}
	if actions := parseELBv2ActionsPrefix(r, "DefaultActions"); len(actions) > 0 {
		listener.DefaultActions = actions
	}
	if certs := parseELBv2Certificates(r); len(certs) > 0 {
		listener.Certificates = certs
	}
	if ssl := r.FormValue("SslPolicy"); ssl != "" {
		listener.SslPolicy = ssl
	}
	if alpn := queryList(r, "AlpnPolicy"); len(alpn) > 0 {
		listener.AlpnPolicy = alpn
	}
	if ma := parseELBv2MutualAuth(r); ma != nil {
		listener.MutualAuth = ma
	}
	// A change to the protocol, port, or certificate set on a TLS-terminating
	// or stream listener re-binds the local realization transactionally. A
	// failed bind restores both the prior control-plane value and its prior
	// listener rather than leaving a resource whose advertised data plane is
	// absent.
	if r.FormValue("Port") != "" || r.FormValue("Protocol") != "" || len(parseELBv2Certificates(r)) > 0 {
		elbv2StopTLSProxy(arn)
		elbv2StopNLBProxy(arn)
		elbv2Listeners.Put(arn, listener)
		if err := elbv2StartListenerDataPlane(listener); err != nil {
			elbv2Listeners.Put(arn, previous)
			elbv2StopTLSProxy(arn)
			elbv2StopNLBProxy(arn)
			if rollbackErr := elbv2StartListenerDataPlane(previous); rollbackErr != nil {
				elbv2ErrorXML(w, "InvalidConfigurationRequest",
					fmt.Sprintf("Could not modify listener data plane: %v; restoring the previous listener also failed: %v", err, rollbackErr),
					http.StatusBadRequest, sim.RequestID(r.Context()))
				return
			}
			elbv2ErrorXML(w, "InvalidConfigurationRequest",
				"Could not modify listener data plane: "+err.Error(),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	} else {
		elbv2Listeners.Put(arn, listener)
	}
	elbv2XMLResponse(w, "ModifyListener", "<Listeners>"+elbv2ListenerXML(listener)+"</Listeners>", sim.RequestID(r.Context()))
}

// handleELBv2SetRulePriorities reorders non-default rules. The provider uses it
// when an aws_lb_listener_rule's priority changes.
func handleELBv2SetRulePriorities(w http.ResponseWriter, r *http.Request) {
	var updated []ELBv2Rule
	for i := 1; ; i++ {
		base := fmt.Sprintf("RulePriorities.member.%d", i)
		ruleArn := r.FormValue(base + ".RuleArn")
		if ruleArn == "" {
			break
		}
		rule, ok := elbv2Rules.Get(ruleArn)
		if !ok {
			elbv2ErrorXML(w, "RuleNotFound", "Rule not found", http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		if p := r.FormValue(base + ".Priority"); p != "" {
			rule.Priority = p
		}
		elbv2Rules.Put(ruleArn, rule)
		updated = append(updated, rule)
	}
	var b strings.Builder
	b.WriteString("<Rules>")
	for _, rl := range updated {
		b.WriteString(elbv2RuleXML(rl))
	}
	b.WriteString("</Rules>")
	elbv2XMLResponse(w, "SetRulePriorities", b.String(), sim.RequestID(r.Context()))
}

// handleELBv2AddListenerCertificates appends SNI certificates to a listener.
// These are the extra (non-default) certs the aws_lb_listener_certificate
// resource manages, distinct from the listener's default certificate.
func handleELBv2AddListenerCertificates(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ListenerArn")
	listener, ok := elbv2Listeners.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	for _, c := range parseELBv2Certificates(r) {
		listener.SNICertificates = appendUnique(listener.SNICertificates, c)
	}
	elbv2Listeners.Put(arn, listener)
	elbv2XMLResponse(w, "AddListenerCertificates",
		elbv2ListenerCertificatesXML(listener, false), sim.RequestID(r.Context()))
}

func handleELBv2RemoveListenerCertificates(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ListenerArn")
	listener, ok := elbv2Listeners.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	remove := map[string]bool{}
	for _, c := range parseELBv2Certificates(r) {
		remove[c] = true
	}
	kept := listener.SNICertificates[:0]
	for _, c := range listener.SNICertificates {
		if !remove[c] {
			kept = append(kept, c)
		}
	}
	listener.SNICertificates = kept
	elbv2Listeners.Put(arn, listener)
	elbv2XMLResponse(w, "RemoveListenerCertificates", "", sim.RequestID(r.Context()))
}

// handleELBv2DescribeListenerCertificates returns the default certificate
// (IsDefault=true) plus the SNI certificates (IsDefault=false). The
// aws_lb_listener_certificate resource filters to the non-default ones.
func handleELBv2DescribeListenerCertificates(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ListenerArn")
	listener, ok := elbv2Listeners.Get(arn)
	if !ok {
		elbv2ErrorXML(w, "ListenerNotFound", "Listener not found", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	elbv2XMLResponse(w, "DescribeListenerCertificates",
		elbv2ListenerCertificatesXML(listener, true), sim.RequestID(r.Context()))
}

// elbv2ListenerCertificatesXML renders the <Certificates> member list. When
// includeDefault is set, the listener's default cert is emitted first with
// IsDefault=true (the DescribeListenerCertificates shape); AddListenerCertificates
// returns only the SNI certs it just added.
func elbv2ListenerCertificatesXML(listener ELBv2Listener, includeDefault bool) string {
	var b strings.Builder
	b.WriteString("<Certificates>")
	if includeDefault {
		for _, c := range listener.Certificates {
			fmt.Fprintf(&b, "<member><CertificateArn>%s</CertificateArn><IsDefault>true</IsDefault></member>", xmlEscape(c))
		}
	}
	for _, c := range listener.SNICertificates {
		fmt.Fprintf(&b, "<member><CertificateArn>%s</CertificateArn><IsDefault>false</IsDefault></member>", xmlEscape(c))
	}
	b.WriteString("</Certificates>")
	return b.String()
}

// ---- helpers ----

// elbv2RuleArn builds a rule ARN from its listener ARN. Real ELBv2 rule ARNs
// use the `listener-rule/` resource prefix (not `rule/`); terraform-provider-aws
// reconstructs aws_lb_listener_rule.listener_arn from the rule ARN by requiring
// exactly the `listener-rule/<type>/<lb>/<lbid>/<listenerid>/<ruleid>` shape, so
// the wrong prefix nulls listener_arn and forces replacement every plan.
func elbv2RuleArn(listenerArn, ruleID string) string {
	return strings.Replace(listenerArn, ":listener/", ":listener-rule/", 1) + "/" + ruleID
}

func elbv2DefaultRule(listener ELBv2Listener) ELBv2Rule {
	return ELBv2Rule{
		Arn:         elbv2RuleArn(listener.Arn, "default"),
		ListenerArn: listener.Arn,
		Priority:    "default",
		IsDefault:   true,
		Actions:     listener.DefaultActions,
	}
}

func parseELBv2Certificates(r *http.Request) []string {
	var certs []string
	for i := 1; ; i++ {
		arn := r.FormValue(fmt.Sprintf("Certificates.member.%d.CertificateArn", i))
		if arn == "" {
			break
		}
		certs = append(certs, arn)
	}
	return certs
}

// parseELBv2Conditions parses a rule's match conditions, accepting BOTH the
// legacy top-level Values (the aws CLI `Field=...,Values=...` shorthand) and
// the typed *Config blocks (terraform-provider-aws), merging them so either
// client round-trips.
func parseELBv2Conditions(r *http.Request) []ELBv2RuleCondition {
	var conds []ELBv2RuleCondition
	for i := 1; ; i++ {
		base := fmt.Sprintf("Conditions.member.%d", i)
		field := r.FormValue(base + ".Field")
		if field == "" {
			break
		}
		c := ELBv2RuleCondition{Field: field, Values: queryList(r, base+".Values")}
		switch field {
		case "host-header":
			c.Values = mergeUnique(c.Values, queryList(r, base+".HostHeaderConfig.Values"))
		case "path-pattern":
			c.Values = mergeUnique(c.Values, queryList(r, base+".PathPatternConfig.Values"))
		case "http-request-method":
			c.Values = mergeUnique(c.Values, queryList(r, base+".HttpRequestMethodConfig.Values"))
		case "source-ip":
			c.Values = mergeUnique(c.Values, queryList(r, base+".SourceIpConfig.Values"))
		case "http-header":
			c.HttpHeaderName = r.FormValue(base + ".HttpHeaderConfig.HttpHeaderName")
			c.Values = mergeUnique(c.Values, queryList(r, base+".HttpHeaderConfig.Values"))
		case "query-string":
			for j := 1; ; j++ {
				kvbase := fmt.Sprintf("%s.QueryStringConfig.Values.member.%d", base, j)
				k := r.FormValue(kvbase + ".Key")
				v := r.FormValue(kvbase + ".Value")
				if k == "" && v == "" {
					break
				}
				c.QueryStrings = append(c.QueryStrings, ELBv2QueryStringKV{Key: k, Value: v})
			}
		}
		conds = append(conds, c)
	}
	return conds
}

func elbv2RuleXML(rule ELBv2Rule) string {
	priority := rule.Priority
	if rule.IsDefault {
		priority = "default"
	}
	return fmt.Sprintf("<member><RuleArn>%s</RuleArn><Priority>%s</Priority>%s%s<IsDefault>%t</IsDefault></member>",
		xmlEscape(rule.Arn), xmlEscape(priority), elbv2ConditionsXML(rule.Conditions),
		elbv2ActionsXML("Actions", rule.Actions), rule.IsDefault)
}

func elbv2ConditionsXML(conds []ELBv2RuleCondition) string {
	var b strings.Builder
	b.WriteString("<Conditions>")
	for _, c := range conds {
		b.WriteString("<member>")
		fmt.Fprintf(&b, "<Field>%s</Field>", xmlEscape(c.Field))
		if len(c.Values) > 0 {
			b.WriteString(elbv2StringMembersXML("Values", c.Values))
		}
		switch c.Field {
		case "host-header":
			b.WriteString("<HostHeaderConfig>" + elbv2StringMembersXML("Values", c.Values) + "</HostHeaderConfig>")
		case "path-pattern":
			b.WriteString("<PathPatternConfig>" + elbv2StringMembersXML("Values", c.Values) + "</PathPatternConfig>")
		case "http-request-method":
			b.WriteString("<HttpRequestMethodConfig>" + elbv2StringMembersXML("Values", c.Values) + "</HttpRequestMethodConfig>")
		case "source-ip":
			b.WriteString("<SourceIpConfig>" + elbv2StringMembersXML("Values", c.Values) + "</SourceIpConfig>")
		case "http-header":
			b.WriteString("<HttpHeaderConfig>")
			fmt.Fprintf(&b, "<HttpHeaderName>%s</HttpHeaderName>", xmlEscape(c.HttpHeaderName))
			b.WriteString(elbv2StringMembersXML("Values", c.Values))
			b.WriteString("</HttpHeaderConfig>")
		case "query-string":
			b.WriteString("<QueryStringConfig><Values>")
			for _, kv := range c.QueryStrings {
				fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>", xmlEscape(kv.Key), xmlEscape(kv.Value))
			}
			b.WriteString("</Values></QueryStringConfig>")
		}
		b.WriteString("</member>")
	}
	b.WriteString("</Conditions>")
	return b.String()
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
