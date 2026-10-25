package sim

import "testing"

func TestWildcardMatch(t *testing.T) {
	star := Wildcard{}
	glob := Wildcard{AnyOne: true}
	escaped := Wildcard{Escapes: "*"}
	filter := Wildcard{AnyOne: true, Escapes: `*?\`}
	folded := Wildcard{AnyOne: true, FoldCase: true}
	for _, c := range []struct {
		w    Wildcard
		p, s string
		want bool
	}{
		{star, "*", "", true},
		{star, "a*", "abc", true},
		{star, "*c", "abc", true},
		{star, "a*c", "ac", true},
		{star, "a*b*c", "axxbyyc", true},
		{star, "a*b*c", "axxcyyb", false},
		{star, "ab*ab", "ab", false},
		{star, "a?c", "abc", false},
		{star, "a?c", "a?c", true},
		{star, "[ab]", "a", false},
		{star, "abc", "ABC", false},
		{glob, "a?c", "abc", true},
		{glob, "a?c", "ac", false},
		{glob, "?", "é", true},
		{glob, "s3:Get*", "s3:GetObject", true},
		{glob, "*.example.com", "api.example.com", true},
		{glob, "*.example.com", "example.com", false},
		{escaped, `a\*c`, "a*c", true},
		{escaped, `a\*c`, "abc", false},
		{escaped, `a\b`, `a\b`, true},
		{star, `a\*c`, `a\xc`, true},
		{escaped, `a\\b`, `a\\b`, true},
		{filter, `\*amazon\?\\`, `*amazon?\`, true},
		{filter, `\*amazon\?\\`, `xamazonx\`, false},
		{filter, `ami-?bc*`, `ami-abcdef`, true},
		{folded, "Microsoft.Storage/*/READ", "microsoft.storage/storageAccounts/read", true},
		{folded, "K?LVIN", "kelvin", true},
		{folded, "abc", "abd", false},
	} {
		if got := c.w.Match(c.p, c.s); got != c.want {
			t.Errorf("%+v: %q on %q = %v, want %v", c.w, c.p, c.s, got, c.want)
		}
	}
}
