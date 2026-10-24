package main

import (
	"cmp"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// elbv2RulesByListener indexes the rules by the listener they belong to, which
// every request a listener accepts looks up.
var elbv2RulesByListener sim.GenerationIndex[ELBv2Rule]

// elbv2ServeListener answers a request an Application Load Balancer listener
// accepted: "Rules are evaluated in priority order, from the lowest value to
// the highest value. The default rule is evaluated last." The first rule
// whose conditions the request meets supplies the actions, performed in
// order.
func elbv2ServeListener(w http.ResponseWriter, r *http.Request, listener ELBv2Listener) {
	actions := listener.DefaultActions
	rules := elbv2RulesByListener.LookupAll(elbv2Rules, listener.Arn, func(rule ELBv2Rule) []string {
		if rule.IsDefault {
			return nil
		}
		return []string{rule.ListenerArn}
	})
	slices.SortStableFunc(rules, func(a, b ELBv2Rule) int {
		return cmp.Compare(atoiDefault(a.Priority, 1<<30), atoiDefault(b.Priority, 1<<30))
	})
	for _, rule := range rules {
		if elbv2RuleMatches(r, rule.Conditions) {
			actions = rule.Actions
			break
		}
	}
	ordered := slices.Clone(actions)
	slices.SortStableFunc(ordered, func(a, b ELBv2Action) int { return cmp.Compare(a.Order, b.Order) })
	for _, action := range ordered {
		switch action.Type {
		case "forward":
			elbv2ForwardToHealthyTarget(w, r, listener, []ELBv2Action{action})
			return
		case "fixed-response":
			elbv2FixedResponse(w, action.FixedResponse)
			return
		case "redirect":
			elbv2Redirect(w, r, listener, action.Redirect)
			return
		}
	}
	http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
}

// elbv2RuleMatches reports whether the request meets every condition of a
// rule, each of which it meets by matching any one of the condition's values.
func elbv2RuleMatches(r *http.Request, conditions []ELBv2RuleCondition) bool {
	for _, c := range conditions {
		if !elbv2ConditionMatches(r, c) {
			return false
		}
	}
	return true
}

func elbv2ConditionMatches(r *http.Request, c ELBv2RuleCondition) bool {
	switch c.Field {
	case "host-header":
		// "Host conditions ... not case-sensitive" and matched against the
		// host name, which carries no port.
		host := elbv2RequestHostname(r)
		return slices.ContainsFunc(c.Values, func(v string) bool { return elbv2FoldedWildcard.Match(v, host) })
	case "path-pattern":
		// "A path pattern is case-sensitive ... The path pattern is applied
		// only to the path of the URL, not to its query parameters."
		return slices.ContainsFunc(c.Values, func(v string) bool { return elbv2Wildcard.Match(v, r.URL.Path) })
	case "http-header":
		// "The comparison strings are not case-sensitive." The header name
		// is not either, as HTTP has it.
		values := r.Header.Values(c.HttpHeaderName)
		return slices.ContainsFunc(c.Values, func(v string) bool {
			return slices.ContainsFunc(values, func(got string) bool {
				return elbv2FoldedWildcard.Match(v, got)
			})
		})
	case "http-request-method":
		// "The comparison is case-sensitive. Wildcards are not supported."
		return slices.Contains(c.Values, r.Method)
	case "query-string":
		// "The comparison strings are not case-sensitive. ... The key is
		// optional."
		query := r.URL.Query()
		return slices.ContainsFunc(c.QueryStrings, func(kv ELBv2QueryStringKV) bool {
			for key, values := range query {
				if kv.Key != "" && !elbv2FoldedWildcard.Match(kv.Key, key) {
					continue
				}
				if slices.ContainsFunc(values, func(got string) bool {
					return elbv2FoldedWildcard.Match(kv.Value, got)
				}) {
					return true
				}
			}
			return false
		})
	case "source-ip":
		// "If a client is behind a proxy, this is the IP address of the
		// proxy, not the IP address of the client."
		ip, _ := lbplane.ClientAddress(r)
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return false
		}
		addr = addr.Unmap()
		return slices.ContainsFunc(c.Values, func(v string) bool {
			prefix, err := netip.ParsePrefix(v)
			return err == nil && prefix.Contains(addr)
		})
	}
	return false
}

// elbv2Wildcard is a rule condition's wildcard, in which * stands for any run
// of characters and ? for one; elbv2FoldedWildcard is it for the conditions
// that compare without regard to case.
var (
	elbv2Wildcard       = sim.Wildcard{AnyOne: true}
	elbv2FoldedWildcard = sim.Wildcard{AnyOne: true, FoldCase: true}
)

func elbv2RequestHostname(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		return host
	}
	return r.Host
}

// elbv2FixedResponse answers with the status, content type and body a
// fixed-response action names.
func elbv2FixedResponse(w http.ResponseWriter, cfg *ELBv2FixedResponseConfig) {
	if cfg == nil {
		http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
		return
	}
	status, err := strconv.Atoi(cfg.StatusCode)
	if err != nil {
		http.Error(w, fmt.Sprintf("fixed-response status %q", cfg.StatusCode), http.StatusInternalServerError)
		return
	}
	if cfg.ContentType != "" {
		w.Header().Set("Content-Type", cfg.ContentType+"; charset=utf-8")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, cfg.MessageBody)
}

// elbv2Redirect answers with the location a redirect action builds from the
// request: "You can reuse URI components using the following reserved
// keywords: #{protocol}, #{host}, #{port}, #{path} (the leading "/" is
// removed), #{query}", each component defaulting to the request's own.
func elbv2Redirect(w http.ResponseWriter, r *http.Request, listener ELBv2Listener, cfg *ELBv2RedirectConfig) {
	if cfg == nil {
		http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
		return
	}
	keywords := strings.NewReplacer(
		"#{protocol}", strings.ToLower(listener.Protocol),
		"#{host}", elbv2RequestHostname(r),
		"#{port}", strconv.Itoa(listener.Port),
		"#{path}", strings.TrimPrefix(r.URL.EscapedPath(), "/"),
		"#{query}", r.URL.RawQuery,
	)
	component := func(value, fallback string) string {
		if value == "" {
			value = fallback
		}
		return keywords.Replace(value)
	}
	location := strings.ToLower(component(cfg.Protocol, "#{protocol}")) + "://" +
		component(cfg.Host, "#{host}") + ":" + component(cfg.Port, "#{port}") +
		component(cfg.Path, "/#{path}")
	if query := component(cfg.Query, "#{query}"); query != "" {
		location += "?" + query
	}
	status := http.StatusMovedPermanently
	if cfg.StatusCode == "HTTP_302" {
		status = http.StatusFound
	}
	w.Header().Set("Location", location)
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status)
	text := fmt.Sprintf("%d %s", status, http.StatusText(status))
	_, _ = io.WriteString(w, "<html>\r\n<head><title>"+text+"</title></head>\r\n<body>\r\n<center><h1>"+text+"</h1></center>\r\n</body>\r\n</html>\r\n")
}
