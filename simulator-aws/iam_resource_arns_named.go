package main

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// A request carrying the ARN of the resource it is about names it outright.
// Services nest that ARN wherever their shapes put it (WAFv2
// LoggingConfiguration.ResourceArn, Elastic Load Balancing
// RulePriorities.member.N.RuleArn, CloudWatch Logs filterLogGroupArn), so read
// the whole request. Requests also carry ARNs they are not about (a KMS key
// beside a backup, a role beside a cluster), so take an ARN only when it matches
// a format published for a type the action declares. Read an identifier as one
// path segment: a missed derivation costs a resource-scoped grant, while a loose
// match hands out one the request never asked for.
func iamARNsNamingADeclaredType(r *http.Request, service string, types []string, region, account string) []string {
	if len(types) == 0 {
		return nil
	}
	var matchers []*regexp.Regexp
	for _, resourceType := range types {
		format, declared := iamResourceARNFormats[service+":"+resourceType]
		if !declared {
			continue
		}
		if matcher := iamARNFormatMatcher(format); matcher != nil {
			matchers = append(matchers, matcher)
		}
	}
	if len(matchers) == 0 {
		return nil
	}

	var out []string
	seen := map[string]struct{}{}
	consider := func(candidate string) {
		if !strings.HasPrefix(candidate, "arn:") {
			return
		}
		if _, dup := seen[candidate]; dup {
			return
		}
		for _, matcher := range matchers {
			if matcher.MatchString(candidate) {
				seen[candidate] = struct{}{}
				out = append(out, candidate)
				return
			}
		}
	}

	for _, candidate := range iamARNLiteral.FindAllString(string(iamRequestBody(r)), -1) {
		consider(candidate)
	}
	for _, values := range iamQueryRequestParameters(r) {
		for _, value := range values {
			consider(value)
		}
	}
	sort.Strings(out)
	return out
}

// An ARN as it appears inside a JSON body or a form value: the punctuation that
// ends one is the punctuation of the document around it, never of the ARN.
var iamARNLiteral = regexp.MustCompile(`arn:[a-z0-9-]*:[^"'\s,}\]&]+`)

// iamARNPathVariable reports whether a published ARN format's identifier may
// itself contain "/": a secret named team/app/db, an SSM parameter hierarchy, an
// IAM name under its path. Every other identifier ends at the next "/".
func iamARNPathVariable(name string) bool {
	switch name {
	case "SecretId", "ParameterNameWithoutLeadingSlash", "EntityPath":
		return true
	}
	return strings.HasSuffix(name, "WithPath")
}

// iamARNFormatMatcher turns a published ARN format into the test for whether a
// given ARN is one. Region and account are wildcards rather than the request's
// own: a request may legitimately name a resource in another account, and what
// is being decided is the ARN's shape, not its ownership.
func iamARNFormatMatcher(format string) *regexp.Regexp {
	if !strings.HasPrefix(format, "arn:") {
		return nil
	}
	var pattern strings.Builder
	pattern.WriteString(`\A`)
	rest := format
	for {
		open := strings.Index(rest, "${")
		if open < 0 {
			pattern.WriteString(regexp.QuoteMeta(rest))
			break
		}
		close := strings.Index(rest[open:], "}")
		if close < 0 {
			pattern.WriteString(regexp.QuoteMeta(rest))
			break
		}
		pattern.WriteString(regexp.QuoteMeta(rest[:open]))
		switch rest[open+2 : open+close] {
		case "Partition":
			pattern.WriteString(`[a-z0-9-]+`)
		case "Region", "Account":
			// Both are routinely empty in a published format's own service —
			// an IAM ARN carries no region — so neither may be required.
			pattern.WriteString(`[a-z0-9-]*`)
		default:
			if iamARNPathVariable(rest[open+2 : open+close]) {
				pattern.WriteString(`[^:]+`)
			} else {
				pattern.WriteString(`[^:/]+`)
			}
		}
		rest = rest[open+close+1:]
	}
	pattern.WriteString(`\z`)
	matcher, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil
	}
	return matcher
}
