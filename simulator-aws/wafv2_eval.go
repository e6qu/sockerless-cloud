package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type wafRateWindow struct {
	Key        string
	WebACLARN  string
	RuleName   string
	Aggregate  string
	Addresses  []string
	Timestamps []float64
	Limited    bool
}

type wafEvaluation struct {
	request   *http.Request
	body      []byte
	clientIP  string
	webACLARN string
	ruleName  string
	labels    map[string]struct{}
}

type wafMatchResult struct {
	matched  bool
	action   string
	terminal bool
}

func wafNewEvaluation(r *http.Request, webACLARN string) *wafEvaluation {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	return &wafEvaluation{
		request: r, body: body, clientIP: wafRequestClientIP(r),
		webACLARN: webACLARN, labels: map[string]struct{}{},
	}
}

func wafEvaluateStatement(raw json.RawMessage, evaluation *wafEvaluation, depth int) wafMatchResult {
	if depth > 32 || len(raw) == 0 {
		return wafMatchResult{}
	}
	var statement map[string]json.RawMessage
	if json.Unmarshal(raw, &statement) != nil {
		return wafMatchResult{}
	}
	for kind, body := range statement {
		switch kind {
		case "IPSetReferenceStatement":
			return wafMatchResult{matched: wafMatchIPSet(body, evaluation)}
		case "ByteMatchStatement":
			return wafMatchResult{matched: wafMatchByte(body, evaluation)}
		case "RegexMatchStatement":
			return wafMatchResult{matched: wafMatchRegex(body, evaluation, nil)}
		case "RegexPatternSetReferenceStatement":
			return wafMatchResult{matched: wafMatchRegexPatternSet(body, evaluation)}
		case "SizeConstraintStatement":
			return wafMatchResult{matched: wafMatchSize(body, evaluation)}
		case "SqliMatchStatement":
			return wafMatchResult{matched: wafMatchInjection(body, evaluation, wafSQLiPattern)}
		case "XssMatchStatement":
			return wafMatchResult{matched: wafMatchInjection(body, evaluation, wafXSSPattern)}
		case "LabelMatchStatement":
			return wafMatchResult{matched: wafMatchLabel(body, evaluation)}
		case "GeoMatchStatement":
			return wafMatchResult{matched: wafMatchGeo(body, evaluation)}
		case "AndStatement", "OrStatement":
			var nested struct {
				Statements []json.RawMessage `json:"Statements"`
			}
			_ = json.Unmarshal(body, &nested)
			matched := kind == "AndStatement"
			for _, child := range nested.Statements {
				childResult := wafEvaluateStatement(child, evaluation, depth+1)
				if kind == "AndStatement" {
					matched = matched && childResult.matched
					if !matched {
						break
					}
				} else {
					matched = matched || childResult.matched
					if matched {
						break
					}
				}
			}
			return wafMatchResult{matched: matched}
		case "NotStatement":
			var nested struct {
				Statement json.RawMessage `json:"Statement"`
			}
			_ = json.Unmarshal(body, &nested)
			return wafMatchResult{matched: !wafEvaluateStatement(nested.Statement, evaluation, depth+1).matched}
		case "RateBasedStatement":
			return wafMatchResult{matched: wafMatchRate(body, evaluation, depth)}
		case "RuleGroupReferenceStatement":
			return wafEvaluateRuleGroup(body, evaluation, depth)
		case "ManagedRuleGroupStatement":
			return wafEvaluateManagedRuleGroup(body, evaluation)
		}
	}
	return wafMatchResult{}
}

func wafMatchIPSet(raw json.RawMessage, evaluation *wafEvaluation) bool {
	var statement struct {
		ARN                    string `json:"ARN"`
		IPSetForwardedIPConfig *struct {
			HeaderName       string `json:"HeaderName"`
			FallbackBehavior string `json:"FallbackBehavior"`
			Position         string `json:"Position"`
		} `json:"IPSetForwardedIPConfig"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	addresses := []string{evaluation.clientIP}
	if statement.IPSetForwardedIPConfig != nil {
		addresses = wafForwardedAddresses(evaluation.request.Header.Get(statement.IPSetForwardedIPConfig.HeaderName), statement.IPSetForwardedIPConfig.Position)
		if len(addresses) == 0 && statement.IPSetForwardedIPConfig.FallbackBehavior == "MATCH" {
			return true
		}
	}
	for _, stored := range wafIPSets.List() {
		if stored.IPSet.ARN != statement.ARN {
			continue
		}
		for _, address := range addresses {
			ip, err := netip.ParseAddr(strings.TrimSpace(address))
			if err != nil {
				continue
			}
			for _, cidr := range stored.IPSet.Addresses {
				prefix, parseErr := netip.ParsePrefix(cidr)
				if parseErr == nil && prefix.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}

func wafForwardedAddresses(header, position string) []string {
	var addresses []string
	for _, value := range strings.Split(header, ",") {
		if value = strings.TrimSpace(value); value != "" {
			addresses = append(addresses, value)
		}
	}
	switch position {
	case "FIRST":
		if len(addresses) > 0 {
			return addresses[:1]
		}
	case "LAST":
		if len(addresses) > 0 {
			return addresses[len(addresses)-1:]
		}
	}
	return addresses
}

type wafTextTransformation struct {
	Priority int    `json:"Priority"`
	Type     string `json:"Type"`
}

type wafFieldStatement struct {
	FieldToMatch        json.RawMessage         `json:"FieldToMatch"`
	TextTransformations []wafTextTransformation `json:"TextTransformations"`
}

func wafMatchByte(raw json.RawMessage, evaluation *wafEvaluation) bool {
	var statement struct {
		wafFieldStatement
		SearchString         []byte `json:"SearchString"`
		PositionalConstraint string `json:"PositionalConstraint"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	search := wafTransform(statement.SearchString, statement.TextTransformations)
	for _, field := range wafFields(statement.FieldToMatch, evaluation) {
		value := wafTransform(field, statement.TextTransformations)
		switch statement.PositionalConstraint {
		case "EXACTLY":
			if bytes.Equal(value, search) {
				return true
			}
		case "STARTS_WITH":
			if bytes.HasPrefix(value, search) {
				return true
			}
		case "ENDS_WITH":
			if bytes.HasSuffix(value, search) {
				return true
			}
		case "CONTAINS":
			if bytes.Contains(value, search) {
				return true
			}
		case "CONTAINS_WORD":
			if wafContainsWord(value, search) {
				return true
			}
		}
	}
	return false
}

func wafContainsWord(value, search []byte) bool {
	for start := 0; start <= len(value)-len(search); {
		index := bytes.Index(value[start:], search)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !wafWordByte(value[index-1])
		after := index + len(search)
		afterOK := after == len(value) || !wafWordByte(value[after])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
	}
	return false
}

func wafWordByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func wafMatchRegex(raw json.RawMessage, evaluation *wafEvaluation, expressions []string) bool {
	var statement struct {
		wafFieldStatement
		RegexString string `json:"RegexString"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	if expressions == nil {
		expressions = []string{statement.RegexString}
	}
	for _, expression := range expressions {
		compiled, err := regexp.Compile(expression)
		if err != nil {
			continue
		}
		for _, field := range wafFields(statement.FieldToMatch, evaluation) {
			if compiled.Match(wafTransform(field, statement.TextTransformations)) {
				return true
			}
		}
	}
	return false
}

func wafMatchRegexPatternSet(raw json.RawMessage, evaluation *wafEvaluation) bool {
	var statement struct {
		wafFieldStatement
		ARN string `json:"ARN"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	var expressions []string
	for _, stored := range wafRegexSets.List() {
		if stored.RegexSet.ARN != statement.ARN {
			continue
		}
		var list []struct {
			RegexString string `json:"RegexString"`
		}
		_ = json.Unmarshal(stored.RegexSet.RegularExpressionList, &list)
		for _, item := range list {
			expressions = append(expressions, item.RegexString)
		}
		break
	}
	encoded, _ := json.Marshal(statement)
	return wafMatchRegex(encoded, evaluation, expressions)
}

func wafMatchSize(raw json.RawMessage, evaluation *wafEvaluation) bool {
	var statement struct {
		wafFieldStatement
		ComparisonOperator string `json:"ComparisonOperator"`
		Size               int64  `json:"Size"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	for _, field := range wafFields(statement.FieldToMatch, evaluation) {
		size := int64(len(wafTransform(field, statement.TextTransformations)))
		switch statement.ComparisonOperator {
		case "EQ":
			if size == statement.Size {
				return true
			}
		case "NE":
			if size != statement.Size {
				return true
			}
		case "LT":
			if size < statement.Size {
				return true
			}
		case "LE":
			if size <= statement.Size {
				return true
			}
		case "GT":
			if size > statement.Size {
				return true
			}
		case "GE":
			if size >= statement.Size {
				return true
			}
		}
	}
	return false
}

var (
	wafSQLiPattern = regexp.MustCompile(`(?i)(\bunion\s+(?:all\s+)?select\b|\bselect\b.+\bfrom\b|\b(?:or|and)\b\s+['"]?[0-9a-z]+['"]?\s*=\s*['"]?[0-9a-z]+|--|/\*|\bxp_cmdshell\b)`)
	wafXSSPattern  = regexp.MustCompile(`(?i)(<\s*script\b|javascript\s*:|on(?:error|load|click|mouseover)\s*=|<\s*(?:iframe|object|embed|svg)\b|document\.(?:cookie|location))`)
)

func wafMatchInjection(raw json.RawMessage, evaluation *wafEvaluation, detector *regexp.Regexp) bool {
	var statement wafFieldStatement
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	for _, field := range wafFields(statement.FieldToMatch, evaluation) {
		if detector.Match(wafTransform(field, statement.TextTransformations)) {
			return true
		}
	}
	return false
}

func wafMatchLabel(raw json.RawMessage, evaluation *wafEvaluation) bool {
	var statement struct {
		Scope string `json:"Scope"`
		Key   string `json:"Key"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	for label := range evaluation.labels {
		if statement.Scope == "NAMESPACE" && strings.HasPrefix(label, statement.Key) || statement.Scope == "LABEL" && label == statement.Key {
			return true
		}
	}
	return false
}

func wafMatchGeo(raw json.RawMessage, evaluation *wafEvaluation) bool {
	var statement struct {
		CountryCodes []string `json:"CountryCodes"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	country := strings.ToUpper(strings.TrimSpace(evaluation.request.Header.Get("CloudFront-Viewer-Country")))
	return country != "" && slices.Contains(statement.CountryCodes, country)
}

func wafMatchRate(raw json.RawMessage, evaluation *wafEvaluation, depth int) bool {
	var statement struct {
		Limit               int64  `json:"Limit"`
		EvaluationWindowSec int64  `json:"EvaluationWindowSec"`
		AggregateKeyType    string `json:"AggregateKeyType"`
		ForwardedIPConfig   *struct {
			HeaderName       string `json:"HeaderName"`
			FallbackBehavior string `json:"FallbackBehavior"`
		} `json:"ForwardedIPConfig"`
		ScopeDownStatement json.RawMessage   `json:"ScopeDownStatement"`
		CustomKeys         []json.RawMessage `json:"CustomKeys"`
	}
	if json.Unmarshal(raw, &statement) != nil || statement.Limit <= 0 {
		return false
	}
	if len(statement.ScopeDownStatement) > 0 && !wafEvaluateStatement(statement.ScopeDownStatement, evaluation, depth+1).matched {
		return false
	}
	aggregate := evaluation.clientIP
	addresses := []string{evaluation.clientIP}
	switch statement.AggregateKeyType {
	case "FORWARDED_IP":
		if statement.ForwardedIPConfig == nil {
			return false
		}
		addresses = wafForwardedAddresses(evaluation.request.Header.Get(statement.ForwardedIPConfig.HeaderName), "FIRST")
		if len(addresses) == 0 {
			return statement.ForwardedIPConfig.FallbackBehavior == "MATCH"
		}
		aggregate = addresses[0]
	case "CONSTANT":
		aggregate = "constant"
		addresses = nil
	case "CUSTOM_KEYS":
		var parts []string
		for _, customKey := range statement.CustomKeys {
			parts = append(parts, wafCustomAggregateKey(customKey, evaluation))
		}
		aggregate = strings.Join(parts, "\x00")
		addresses = nil
	}
	windowSeconds := statement.EvaluationWindowSec
	if windowSeconds == 0 {
		windowSeconds = 300
	}
	now := float64(time.Now().UTC().UnixNano()) / float64(time.Second)
	cutoff := now - float64(windowSeconds)
	key := evaluation.webACLARN + "\x00" + evaluation.ruleName + "\x00" + aggregate
	limited := false
	wafRateWindows.Upsert(key, func(window *wafRateWindow) {
		window.Key = key
		window.WebACLARN = evaluation.webACLARN
		window.RuleName = evaluation.ruleName
		window.Aggregate = aggregate
		window.Addresses = append([]string(nil), addresses...)
		filtered := window.Timestamps[:0]
		for _, timestamp := range window.Timestamps {
			if timestamp >= cutoff {
				filtered = append(filtered, timestamp)
			}
		}
		window.Timestamps = append(filtered, now)
		window.Limited = int64(len(window.Timestamps)) > statement.Limit
		limited = window.Limited
	})
	return limited
}

func wafCustomAggregateKey(raw json.RawMessage, evaluation *wafEvaluation) string {
	var key map[string]json.RawMessage
	if json.Unmarshal(raw, &key) != nil {
		return ""
	}
	for kind, body := range key {
		switch kind {
		case "HTTPMethod":
			return evaluation.request.Method
		case "ForwardedIP":
			return evaluation.clientIP
		case "Header":
			var value struct {
				Name string `json:"Name"`
			}
			_ = json.Unmarshal(body, &value)
			return evaluation.request.Header.Get(value.Name)
		case "QueryArgument":
			var value struct {
				Name string `json:"Name"`
			}
			_ = json.Unmarshal(body, &value)
			return evaluation.request.URL.Query().Get(value.Name)
		case "Cookie":
			var value struct {
				Name string `json:"Name"`
			}
			_ = json.Unmarshal(body, &value)
			cookie, _ := evaluation.request.Cookie(value.Name)
			if cookie != nil {
				return cookie.Value
			}
		case "LabelNamespace":
			var value struct {
				Namespace string `json:"Namespace"`
			}
			_ = json.Unmarshal(body, &value)
			var labels []string
			for label := range evaluation.labels {
				if strings.HasPrefix(label, value.Namespace) {
					labels = append(labels, label)
				}
			}
			slices.Sort(labels)
			return strings.Join(labels, ",")
		}
	}
	return ""
}

func wafEvaluateRuleGroup(raw json.RawMessage, evaluation *wafEvaluation, depth int) wafMatchResult {
	var reference struct {
		ARN           string `json:"ARN"`
		ExcludedRules []struct {
			Name string `json:"Name"`
		} `json:"ExcludedRules"`
		RuleActionOverrides []struct {
			Name        string                     `json:"Name"`
			ActionToUse map[string]json.RawMessage `json:"ActionToUse"`
		} `json:"RuleActionOverrides"`
	}
	if json.Unmarshal(raw, &reference) != nil {
		return wafMatchResult{}
	}
	excluded := map[string]struct{}{}
	for _, rule := range reference.ExcludedRules {
		excluded[rule.Name] = struct{}{}
	}
	overrides := map[string]map[string]json.RawMessage{}
	for _, override := range reference.RuleActionOverrides {
		overrides[override.Name] = override.ActionToUse
	}
	for _, stored := range wafRuleGroups.List() {
		if stored.RuleGroup.ARN != reference.ARN {
			continue
		}
		var rules []wafRule
		_ = json.Unmarshal(stored.RuleGroup.Rules, &rules)
		sortWAFRules(rules)
		for _, rule := range rules {
			if _, skip := excluded[rule.Name]; skip {
				continue
			}
			priorName := evaluation.ruleName
			evaluation.ruleName = rule.Name
			result := wafEvaluateStatement(rule.Statement, evaluation, depth+1)
			evaluation.ruleName = priorName
			if !result.matched {
				continue
			}
			wafApplyRuleLabels(rule, stored.RuleGroup.LabelNamespace, evaluation)
			action := rule.Action
			if override, ok := overrides[rule.Name]; ok {
				action = override
			}
			result.action, result.terminal = wafRuleAction(action)
			return result
		}
	}
	return wafMatchResult{}
}

func wafEvaluateManagedRuleGroup(raw json.RawMessage, evaluation *wafEvaluation) wafMatchResult {
	var statement struct {
		Name       string `json:"Name"`
		VendorName string `json:"VendorName"`
	}
	if json.Unmarshal(raw, &statement) != nil || statement.VendorName != "AWS" {
		return wafMatchResult{}
	}
	var matched bool
	switch statement.Name {
	case "AWSManagedRulesAdminProtectionRuleSet":
		matched = regexp.MustCompile(`(?i)^/(admin|administrator|wp-admin)(/|$)`).MatchString(evaluation.request.URL.Path)
	case "AWSManagedRulesKnownBadInputsRuleSet":
		host := strings.ToLower(evaluation.request.Host)
		matched = strings.HasPrefix(host, "localhost") || wafXSSPattern.MatchString(evaluation.request.URL.RequestURI()) ||
			strings.Contains(strings.ToLower(evaluation.request.URL.Path), "/.env")
	case "AWSManagedRulesSQLiRuleSet":
		matched = wafSQLiPattern.MatchString(evaluation.request.URL.RawQuery) || wafSQLiPattern.Match(evaluation.body)
	case "AWSManagedRulesCommonRuleSet":
		matched = evaluation.request.UserAgent() == "" || len(evaluation.body) > 8*1024
	}
	if matched {
		return wafMatchResult{matched: true, action: "BLOCK", terminal: true}
	}
	return wafMatchResult{}
}

func wafApplyRuleLabels(rule wafRule, namespace string, evaluation *wafEvaluation) {
	for _, label := range rule.RuleLabels {
		name := label.Name
		if !strings.Contains(name, ":") {
			name = namespace + name
		}
		evaluation.labels[name] = struct{}{}
	}
}

func sortWAFRules(rules []wafRule) {
	slices.SortStableFunc(rules, func(a, b wafRule) int { return a.Priority - b.Priority })
}

func wafFields(raw json.RawMessage, evaluation *wafEvaluation) [][]byte {
	var field map[string]json.RawMessage
	if json.Unmarshal(raw, &field) != nil {
		return nil
	}
	for kind, config := range field {
		switch kind {
		case "UriPath":
			return [][]byte{[]byte(evaluation.request.URL.Path)}
		case "QueryString":
			return [][]byte{[]byte(evaluation.request.URL.RawQuery)}
		case "AllQueryArguments":
			var values [][]byte
			for name, entries := range evaluation.request.URL.Query() {
				for _, entry := range entries {
					values = append(values, []byte(name+"="+entry))
				}
			}
			return values
		case "SingleQueryArgument":
			var match struct {
				Name string `json:"Name"`
			}
			_ = json.Unmarshal(config, &match)
			return [][]byte{[]byte(evaluation.request.URL.Query().Get(match.Name))}
		case "Method":
			return [][]byte{[]byte(evaluation.request.Method)}
		case "SingleHeader":
			var match struct {
				Name string `json:"Name"`
			}
			_ = json.Unmarshal(config, &match)
			return [][]byte{[]byte(evaluation.request.Header.Get(match.Name))}
		case "Headers":
			return wafHeaderFields(config, evaluation.request.Header)
		case "Cookies":
			return wafCookieFields(config, evaluation.request.Cookies())
		case "Body":
			return [][]byte{evaluation.body}
		case "JsonBody":
			return wafJSONFields(config, evaluation.body)
		case "HeaderOrder":
			var names []string
			for name := range evaluation.request.Header {
				names = append(names, strings.ToLower(name))
			}
			slices.Sort(names)
			return [][]byte{[]byte(strings.Join(names, ":"))}
		case "JA3Fingerprint":
			return [][]byte{[]byte(evaluation.request.Header.Get("X-Amzn-Waf-Ja3-Fingerprint"))}
		case "JA4Fingerprint":
			return [][]byte{[]byte(evaluation.request.Header.Get("X-Amzn-Waf-Ja4-Fingerprint"))}
		}
	}
	return nil
}

func wafHeaderFields(raw json.RawMessage, headers http.Header) [][]byte {
	var config struct {
		MatchScope   string `json:"MatchScope"`
		MatchPattern struct {
			All             json.RawMessage `json:"All"`
			IncludedHeaders []string        `json:"IncludedHeaders"`
			ExcludedHeaders []string        `json:"ExcludedHeaders"`
		} `json:"MatchPattern"`
	}
	_ = json.Unmarshal(raw, &config)
	included := map[string]struct{}{}
	for _, name := range config.MatchPattern.IncludedHeaders {
		included[strings.ToLower(name)] = struct{}{}
	}
	excluded := map[string]struct{}{}
	for _, name := range config.MatchPattern.ExcludedHeaders {
		excluded[strings.ToLower(name)] = struct{}{}
	}
	var fields [][]byte
	for name, values := range headers {
		lowerName := strings.ToLower(name)
		if len(included) > 0 {
			if _, ok := included[lowerName]; !ok {
				continue
			}
		}
		if _, skip := excluded[lowerName]; skip {
			continue
		}
		for _, value := range values {
			switch config.MatchScope {
			case "KEY":
				fields = append(fields, []byte(lowerName))
			case "VALUE":
				fields = append(fields, []byte(value))
			default:
				fields = append(fields, []byte(lowerName+":"+value))
			}
		}
	}
	return fields
}

func wafCookieFields(raw json.RawMessage, cookies []*http.Cookie) [][]byte {
	var config struct {
		MatchScope   string `json:"MatchScope"`
		MatchPattern struct {
			All             json.RawMessage `json:"All"`
			IncludedCookies []string        `json:"IncludedCookies"`
			ExcludedCookies []string        `json:"ExcludedCookies"`
		} `json:"MatchPattern"`
	}
	_ = json.Unmarshal(raw, &config)
	var fields [][]byte
	for _, cookie := range cookies {
		if len(config.MatchPattern.IncludedCookies) > 0 && !slices.Contains(config.MatchPattern.IncludedCookies, cookie.Name) ||
			slices.Contains(config.MatchPattern.ExcludedCookies, cookie.Name) {
			continue
		}
		switch config.MatchScope {
		case "KEY":
			fields = append(fields, []byte(cookie.Name))
		case "VALUE":
			fields = append(fields, []byte(cookie.Value))
		default:
			fields = append(fields, []byte(cookie.Name+"="+cookie.Value))
		}
	}
	return fields
}

func wafJSONFields(raw, body []byte) [][]byte {
	var config struct {
		MatchScope string `json:"MatchScope"`
	}
	_ = json.Unmarshal(raw, &config)
	var value any
	if json.Unmarshal(body, &value) != nil {
		return nil
	}
	var fields [][]byte
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if config.MatchScope != "VALUES" {
					fields = append(fields, []byte(key))
				}
				if config.MatchScope != "KEYS" {
					walk(child)
				}
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			fields = append(fields, []byte(typed))
		case float64:
			fields = append(fields, []byte(strconv.FormatFloat(typed, 'f', -1, 64)))
		case bool:
			fields = append(fields, []byte(strconv.FormatBool(typed)))
		}
	}
	walk(value)
	return fields
}

func wafTransform(input []byte, transformations []wafTextTransformation) []byte {
	output := append([]byte(nil), input...)
	slices.SortStableFunc(transformations, func(a, b wafTextTransformation) int { return a.Priority - b.Priority })
	for _, transformation := range transformations {
		switch transformation.Type {
		case "NONE":
		case "LOWERCASE":
			output = bytes.ToLower(output)
		case "URL_DECODE", "URL_DECODE_UNI":
			if decoded, err := url.QueryUnescape(string(output)); err == nil {
				output = []byte(decoded)
			}
		case "HTML_ENTITY_DECODE":
			output = []byte(html.UnescapeString(string(output)))
		case "COMPRESS_WHITE_SPACE":
			output = []byte(strings.Join(strings.FieldsFunc(string(output), unicode.IsSpace), " "))
		case "REMOVE_NULLS":
			output = bytes.ReplaceAll(output, []byte{0}, nil)
		case "REPLACE_NULLS":
			output = bytes.ReplaceAll(output, []byte{0}, []byte{' '})
		case "CMD_LINE":
			replacer := strings.NewReplacer(`\`, "", `"`, "", `'`, "", "^", "", ",", " ", ";", " ")
			output = []byte(strings.ToLower(strings.Join(strings.Fields(replacer.Replace(string(output))), " ")))
		case "BASE64_DECODE":
			if decoded, err := base64.StdEncoding.DecodeString(string(output)); err == nil {
				output = decoded
			}
		case "BASE64_DECODE_EXT":
			clean := regexp.MustCompile(`[^A-Za-z0-9+/=]`).ReplaceAll(output, nil)
			if decoded, err := base64.StdEncoding.DecodeString(string(clean)); err == nil {
				output = decoded
			}
		case "HEX_DECODE":
			if decoded, err := hex.DecodeString(string(output)); err == nil {
				output = decoded
			}
		case "MD5":
			sum := md5.Sum(output)
			output = sum[:]
		case "SHA1":
			sum := sha1.Sum(output)
			output = sum[:]
		case "SQL_HEX_DECODE":
			output = wafSQLHexDecode(output)
		case "ESCAPE_SEQ_DECODE", "CSS_DECODE":
			if decoded, err := strconv.Unquote(`"` + strings.ReplaceAll(string(output), `"`, `\"`) + `"`); err == nil {
				output = []byte(decoded)
			}
		case "UTF8_TO_UNICODE":
			var builder strings.Builder
			for _, runeValue := range string(output) {
				if runeValue <= 0x7f {
					builder.WriteRune(runeValue)
				} else {
					_, _ = builder.WriteString(`\u` + strings.ToUpper(strconv.FormatInt(int64(runeValue), 16)))
				}
			}
			output = []byte(builder.String())
		}
	}
	return output
}

func wafSQLHexDecode(input []byte) []byte {
	return regexp.MustCompile(`(?i)0x([0-9a-f]+)`).ReplaceAllFunc(input, func(match []byte) []byte {
		decoded, err := hex.DecodeString(string(match[2:]))
		if err != nil {
			return match
		}
		return decoded
	})
}
