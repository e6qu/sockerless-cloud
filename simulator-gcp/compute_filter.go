package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// A Compute Engine list filter is one of two languages, never both in one
// request. The Discovery document's filter parameter describes the second:
//
//	fieldname eq unquoted literal
//	fieldname eq 'single quoted literal'
//	fieldname eq "double quoted literal"
//	(fieldname1 eq literal) (fieldname2 ne "literal")
//
// The literal is an RE2 expression that must match the entire field; eq keeps
// the resources whose field matches, ne those whose field does not, and the
// parenthesized terms all have to hold. Everything else is AIP-160.

var computeRegexTermRE = regexp.MustCompile(`(?s)^([A-Za-z_][A-Za-z0-9_.]*)\s+(eq|ne)\s+(.+)$`)

type computeRegexTerm struct {
	field  string
	negate bool
	re     *regexp.Regexp
}

func (t computeRegexTerm) Eval(d listq.Doc) bool {
	value, _ := listq.Field(d, t.field, ".")
	return t.re.MatchString(value) != t.negate
}

func gcpParseComputeFilter(s string) (listq.Node, error) {
	s = strings.TrimSpace(s)
	terms, isRegex, err := computeRegexTerms(s)
	if err != nil {
		return nil, fmt.Errorf("invalid filter %q: %w", s, err)
	}
	if !isRegex {
		return gcpParseFilterExpr(s)
	}
	var node listq.Node = listq.True{}
	for _, term := range terms {
		parsed, err := computeParseRegexTerm(term)
		if err != nil {
			return nil, fmt.Errorf("invalid filter %q: %w", s, err)
		}
		node = listq.And{L: node, R: parsed}
	}
	return node, nil
}

// computeRegexTerms splits a filter into its eq/ne terms and reports whether it
// is written in the regular-expression language at all. A filter that mixes
// the two languages is an error.
func computeRegexTerms(s string) ([]string, bool, error) {
	if !strings.HasPrefix(s, "(") {
		return []string{s}, computeRegexTermRE.MatchString(s), nil
	}
	groups, separated := computeParenthesizedGroups(s)
	regexGroups := 0
	for _, group := range groups {
		if computeRegexTermRE.MatchString(strings.TrimSpace(group)) {
			regexGroups++
		}
	}
	switch {
	case regexGroups == 0:
		return nil, false, nil
	case regexGroups != len(groups) || separated:
		return nil, false, fmt.Errorf("eq/ne regular-expression terms cannot be combined with AIP-160 expressions")
	}
	return groups, true, nil
}

// computeParenthesizedGroups returns the contents of each top-level
// parenthesized group, and reports whether anything other than whitespace
// separates or surrounds them. Parentheses inside quotes, or nested inside a
// group, belong to the group.
func computeParenthesizedGroups(s string) ([]string, bool) {
	var groups []string
	separated := false
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case depth > 0 && (c == '"' || c == '\''):
			quote = c
		case c == '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case c == ')' && depth > 0:
			depth--
			if depth == 0 {
				groups = append(groups, s[start:i])
			}
		case depth == 0 && c != ' ' && c != '\t' && c != '\n':
			separated = true
		}
	}
	if depth != 0 || quote != 0 {
		separated = true
	}
	return groups, separated
}

func computeParseRegexTerm(term string) (listq.Node, error) {
	m := computeRegexTermRE.FindStringSubmatch(strings.TrimSpace(term))
	if m == nil {
		return nil, fmt.Errorf("%q is not a `field eq|ne literal` term", term)
	}
	literal := strings.TrimSpace(m[3])
	if n := len(literal); n >= 2 && (literal[0] == '"' || literal[0] == '\'') && literal[n-1] == literal[0] {
		literal = literal[1 : n-1]
	}
	re, err := regexp.Compile(`^(?:` + literal + `)$`)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression %q: %w", literal, err)
	}
	return computeRegexTerm{field: m[1], negate: m[2] == "ne", re: re}, nil
}
