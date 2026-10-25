package sim

import (
	"strings"
	"unicode"
)

// Wildcard is a pattern syntax of literal characters and '*', which matches
// any run of characters, none included — the wildcard IAM policies, load
// balancer rules, Azure role actions and event filters share. Its fields add
// the parts of a syntax that are a cloud's own.
type Wildcard struct {
	// AnyOne makes '?' match any one character; otherwise '?' is literal.
	AnyOne bool
	// Escapes are the characters a backslash before them makes literal; a
	// backslash before any other character is itself literal.
	Escapes string
	// FoldCase matches letters regardless of case.
	FoldCase bool
}

type wildcardToken struct {
	r    rune
	kind byte
}

const (
	wildcardLiteral byte = iota
	wildcardStar
	wildcardOne
)

func (w Wildcard) tokens(pattern string) []wildcardToken {
	runes := []rune(pattern)
	out := make([]wildcardToken, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; {
		case r == '\\' && i+1 < len(runes) && strings.ContainsRune(w.Escapes, runes[i+1]):
			out = append(out, wildcardToken{r: runes[i+1]})
			i++
		case r == '*':
			out = append(out, wildcardToken{kind: wildcardStar})
		case w.AnyOne && r == '?':
			out = append(out, wildcardToken{kind: wildcardOne})
		default:
			out = append(out, wildcardToken{r: r})
		}
	}
	return out
}

func (w Wildcard) same(a, b rune) bool {
	if a == b {
		return true
	}
	if !w.FoldCase {
		return false
	}
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

// Match reports whether all of s matches pattern.
func (w Wildcard) Match(pattern, s string) bool {
	p, v := w.tokens(pattern), []rune(s)
	pi, vi, star, mark := 0, 0, -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && p[pi].kind == wildcardStar:
			star, mark = pi, vi
			pi++
		case pi < len(p) && (p[pi].kind == wildcardOne || p[pi].kind == wildcardLiteral && w.same(p[pi].r, v[vi])):
			pi++
			vi++
		case star >= 0:
			pi = star + 1
			mark++
			vi = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi].kind == wildcardStar {
		pi++
	}
	return pi == len(p)
}
