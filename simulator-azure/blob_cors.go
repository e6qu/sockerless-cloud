package main

import (
	"net/http"
	"strconv"
	"strings"
)

// Cross-origin resource sharing on the Blob service. An account's CORS rules
// live in its Blob service properties, which Set Blob Service Properties writes
// on the data plane and `PUT .../blobServices/default` writes through Azure
// Resource Manager; both read and write the one document. The service answers
// a browser's preflight against the rules and stamps the access-control
// headers on a request whose origin and method a rule allows, as described in
// "Cross-Origin Resource Sharing (CORS) support for Azure Storage".

const blobCorsPreflightFailure = "CORS not enabled or no matching rule found for this request."

func corsList(value string) []string {
	var out []string
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

func corsOriginMatches(rule BlobCorsRule, origin string) (matched, wildcard bool) {
	for _, allowed := range corsList(rule.AllowedOrigins) {
		if allowed == "*" {
			return true, true
		}
		if strings.EqualFold(allowed, origin) {
			return true, false
		}
	}
	return false, false
}

func corsMethodAllowed(rule BlobCorsRule, method string) bool {
	for _, allowed := range corsList(rule.AllowedMethods) {
		if strings.EqualFold(allowed, method) {
			return true
		}
	}
	return false
}

// corsHeaderAllowed matches a requested header against a rule's allowed
// headers, each a literal name, a prefix ending in "*", or "*" for any.
func corsHeaderAllowed(rule BlobCorsRule, header string) bool {
	header = strings.ToLower(strings.TrimSpace(header))
	for _, allowed := range corsList(rule.AllowedHeaders) {
		allowed = strings.ToLower(allowed)
		if allowed == "*" || allowed == header {
			return true
		}
		if prefix, ok := strings.CutSuffix(allowed, "*"); ok && strings.HasPrefix(header, prefix) {
			return true
		}
	}
	return false
}

// blobMatchCorsRule returns the first rule that allows the origin, the method
// and every requested header, in the order the rules were written.
func blobMatchCorsRule(rules []BlobCorsRule, origin, method string, headers []string) (BlobCorsRule, bool, bool) {
	for _, rule := range rules {
		matched, wildcard := corsOriginMatches(rule, origin)
		if !matched || !corsMethodAllowed(rule, method) {
			continue
		}
		allHeaders := true
		for _, header := range headers {
			if !corsHeaderAllowed(rule, header) {
				allHeaders = false
				break
			}
		}
		if allHeaders {
			return rule, wildcard, true
		}
	}
	return BlobCorsRule{}, false, false
}

// blobHandleCORS answers a preflight request and returns true, or stamps the
// access-control headers an allowed cross-origin request carries back and
// returns false so the request proceeds.
func blobHandleCORS(w http.ResponseWriter, r *http.Request, account string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	props := blobServiceProperties(account)
	var rules []BlobCorsRule
	if props.Cors != nil {
		rules = props.Cors.CorsRule
	}
	header := w.Header()
	requestedMethod := r.Header.Get("Access-Control-Request-Method")
	if r.Method == http.MethodOptions && requestedMethod != "" {
		requested := corsList(r.Header.Get("Access-Control-Request-Headers"))
		rule, wildcard, ok := blobMatchCorsRule(rules, origin, requestedMethod, requested)
		if !ok {
			writeStorageError(w, "CorsPreflightFailure", blobCorsPreflightFailure, http.StatusForbidden)
			return true
		}
		if wildcard {
			header.Set("Access-Control-Allow-Origin", "*")
		} else {
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Allow-Credentials", "true")
			header.Set("Vary", "Origin")
		}
		header.Set("Access-Control-Allow-Methods", strings.ToUpper(requestedMethod))
		if len(requested) > 0 {
			header.Set("Access-Control-Allow-Headers", strings.Join(requested, ","))
		}
		header.Set("Access-Control-Max-Age", strconv.Itoa(int(rule.MaxAgeInSeconds)))
		w.WriteHeader(http.StatusOK)
		return true
	}
	rule, wildcard, ok := blobMatchCorsRule(rules, origin, r.Method, nil)
	if !ok {
		return false
	}
	if wildcard {
		header.Set("Access-Control-Allow-Origin", "*")
	} else {
		header.Set("Access-Control-Allow-Origin", origin)
		header.Set("Access-Control-Allow-Credentials", "true")
		header.Set("Vary", "Origin")
	}
	if exposed := corsList(rule.ExposedHeaders); len(exposed) > 0 {
		header.Set("Access-Control-Expose-Headers", strings.Join(exposed, ","))
	}
	return false
}

// blobARMCors reads ARM's cors.corsRules, whose members are string arrays,
// into the data plane's rule document, whose members are comma-joined.
func blobARMCors(props map[string]any) (*BlobCorsRules, bool) {
	cors, ok := props["cors"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, _ := cors["corsRules"].([]any)
	join := func(rule map[string]any, key string) string {
		values, _ := rule[key].([]any)
		parts := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	}
	out := &BlobCorsRules{}
	for _, entry := range raw {
		rule, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		maxAge, _ := rule["maxAgeInSeconds"].(float64)
		out.CorsRule = append(out.CorsRule, BlobCorsRule{
			AllowedOrigins:  join(rule, "allowedOrigins"),
			AllowedMethods:  join(rule, "allowedMethods"),
			AllowedHeaders:  join(rule, "allowedHeaders"),
			ExposedHeaders:  join(rule, "exposedHeaders"),
			MaxAgeInSeconds: int32(maxAge),
		})
	}
	return out, true
}

// blobCorsARM renders the data plane's rules in ARM's shape.
func blobCorsARM(rules *BlobCorsRules) map[string]any {
	out := []any{}
	if rules != nil {
		for _, rule := range rules.CorsRule {
			out = append(out, map[string]any{
				"allowedOrigins":  corsList(rule.AllowedOrigins),
				"allowedMethods":  corsList(rule.AllowedMethods),
				"allowedHeaders":  corsList(rule.AllowedHeaders),
				"exposedHeaders":  corsList(rule.ExposedHeaders),
				"maxAgeInSeconds": rule.MaxAgeInSeconds,
			})
		}
	}
	return map[string]any{"corsRules": out}
}

// blobSetCors records the account's Blob CORS rules in the data-plane
// service-properties document the service evaluates requests against.
func blobSetCors(account string, rules *BlobCorsRules) {
	cfg, ok := blobServicePropsStore.Get(account)
	if !ok {
		cfg = BlobServiceConfig{Account: account, Properties: defaultBlobServiceProperties()}
	}
	cfg.Properties.Cors = rules
	blobServicePropsStore.Put(account, cfg)
}
