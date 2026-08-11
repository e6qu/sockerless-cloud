package main

import "testing"

// FuzzCWLogPatternMatches fuzzes the CloudWatch Logs metric-filter pattern matcher
// (both unstructured and structured/JSON grammars).
func FuzzCWLogPatternMatches(f *testing.F) {
	seeds := []string{
		"",
		"ERROR",
		"ERROR -INFO ?WARN",
		"\"a phrase\"",
		"{ $.level = \"error\" }",
		"{ $.code = 500 && $.path = /api* }",
		"{ ($.a = 1 || $.b = 2) && $.c != 3 }",
		"{ $.a[0] = x }",
		"{ $.a[999999999999999999999] = x }",
		"{ }",
		"{",
		"}",
		"{ (((((((((((( }",
		"{ $.a = }",
		"{ = x }",
		"\"unterminated",
		"{ $.a == \" }",
		"é",
		"\xff\xfe",
		"\\",
		"{ $.x < y }",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	messages := []string{
		`{"level":"error","code":500,"path":"/api/v1","a":[1,2,3]}`,
		`plain text message ERROR here`,
		``,
		`not json {`,
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		// A malformed pattern returns a loud error (not a crash); a well-formed
		// one compiles and matches without panicking on any event.
		c, err := cwCompileLogPattern(pattern)
		if err != nil {
			return
		}
		for _, m := range messages {
			_ = c.match(m)
		}
	})
}
