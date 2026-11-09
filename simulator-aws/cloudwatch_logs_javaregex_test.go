package main

import "testing"

// Each case is a String.replaceAll result under Java SE 17's
// java.util.regex.Pattern.
func TestCWJavaRegexReplaceAll(t *testing.T) {
	for _, c := range []struct{ pattern, to, in, want string }{
		{`foo(?=bar)`, "X", "foobar foobaz", "Xbar foobaz"},
		{`foo(?!bar)`, "X", "foobar foobaz", "foobar Xbaz"},
		{`(?<=\$)\d+`, "N", "cost $15 and 20", "cost $N and 20"},
		{`(?<!\$)\b\d+`, "N", "cost $15 and 20", "cost $15 and N"},
		{`(\w)\1`, "<$1>", "aabbcd", "<a><b>cd"},
		{`a++a`, "X", "aaaa", "aaaa"},
		{`\d*+5`, "X", "12345", "12345"},
		{`(?>a|ab)c`, "X", "abc ac", "abc X"},
		{`^arn:aws:sts::(?P<account_id>\d{12}):assumed-role/(?P<role_name>[\w+=,.@-]+)/(?P<role_session_name>[\w+=,.@-]+)$`,
			"${account_id}:${role_name}:${role_session_name}",
			"arn:aws:sts::123456789012:assumed-role/MyImportantRole/MySession",
			"123456789012:MyImportantRole:MySession"},
		{`(?<year>\d{4})-(\d{2})`, "$2/${year}", "2024-05", "05/2024"},
		{`\Q.*\E`, "!", "a.*b.c", "a!b.c"},
		{`[a-z&&[^aeiou]]+`, "#", "hello world", "#e#o #o#"},
		{`[a-c[x-z]]+`, "-", "abxyzm", "-m"},
		{`[^a-c[x-z]]`, "-", "abxyzm", "abxyz-"},
		{`\w+`, "W", "café", "Wé"},
		{`(?U)\w+`, "W", "café", "W"},
		{`.`, "x", "a b", "x x"},
		{`(?s).`, "x", "a b", "xxx"},
		{`c$`, "C", "abc\n", "abC\n"},
		{`(?m)^b`, "B", "a\nb\nb", "a\nB\nB"},
		{`(?i)é`, "e", "É", "É"},
		{`(?iu)é`, "e", "É", "e"},
		{`(?i)abc`, "x", "ABC aBc", "x x"},
		{`\p{IsLatin}+`, "L", "abc αβγ", "L αβγ"},
		{`\p{InGreek}+`, "G", "abc αβγ", "abc G"},
		{`\p{Lu}`, "_", "aBcD", "a_c_"},
		{`\p{Alpha}+`, "A", "abé", "Aé"},
		{`\h+`, " ", "a\t b", "a b"},
		{`\R`, "|", "a\r\nb\nc", "a|b|c"},
		{`x*`, "-", "abc", "-a-b-c-"},
		{`a{2,3}`, "X", "aaaaa", "XX"},
		{`a{2,3}?`, "X", "aaaaa", "XXa"},
		{`\x41B\0103\t`, "ok", "ABC\t", "ok"},
		{`(?x) a b # comment`, "X", "ab a b", "X a b"},
		{`(a)|(b)`, "[$1$2]", "ab", "[a][b]"},
	} {
		re, err := cwjCompile(c.pattern)
		if err != nil {
			t.Errorf("%s: %v", c.pattern, err)
			continue
		}
		parts, err := cwtParseReplacement(c.to, re)
		if err != nil {
			t.Errorf("%s -> %s: %v", c.pattern, c.to, err)
			continue
		}
		got, err := re.replaceAll(c.in, parts)
		if err != nil || got != c.want {
			t.Errorf("%q.replaceAll(%q, %q) = %q, %v; want %q", c.in, c.pattern, c.to, got, err, c.want)
		}
	}
}

// Pattern refuses what Java SE 17 refuses.
func TestCWJavaRegexRefusals(t *testing.T) {
	for _, pattern := range []string{
		`(?<=a*)b`, `(?<=a+)b`, `a**`, `(abc`, `abc)`, `[a-`, `\p{NoSuchProperty}`, `(?<n>a)(?<n>b)`, `\k<missing>`, `[z-a]`, `*a`, `(?z)a`,
	} {
		if _, err := cwjCompile(pattern); err == nil {
			t.Errorf("%s compiled; Pattern refuses it", pattern)
		}
	}
}
