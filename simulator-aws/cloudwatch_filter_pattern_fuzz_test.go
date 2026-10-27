package main

import "testing"

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
		// A malformed pattern returns an error; a well-formed one matches without
		// panicking.
		c, err := cwCompileLogPattern(pattern)
		if err != nil {
			return
		}
		for _, m := range messages {
			_ = c.match(m)
		}
	})
}
