package main

// Each target parses untrusted wire input and must return an error or a
// benign zero value, never panic.

import "testing"

func FuzzParseChunkSize(f *testing.F) {
	for _, s := range []string{"0", "a", "ff", "7fffffffffffffff", "10;chunk-signature=abc", "", "  ", ";", "-1", "g", "0x10", "ffffffffffffffffff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		n, err := parseChunkSize(line)
		if err == nil && n < 0 {
			t.Fatalf("parseChunkSize(%q) returned negative size %d with nil error", line, n)
		}
	})
}

func FuzzCloudTrailDecodeToken(f *testing.F) {
	for _, s := range []string{"", "abc", "AAAA", "_____", "AA==", "\x00", "YQB i"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, token string) {
		_, _, _ = cloudTrailDecodeToken(token)
	})
}

func FuzzCWParsePercentile(f *testing.F) {
	for _, s := range []string{"", "p", "p99", "P99.9", "p0", "p100", "pNaN", "p-1", "pInf", "x99", "p1e308", "p"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = cwParsePercentile(s)
	})
}

func FuzzCWParseAggs(f *testing.F) {
	for _, s := range []string{"", "count(*)", "sum(x) by y", "count_distinct(a), avg(b)", "(((", "by", "stats", "x(", ")", "avg() by"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = cwParseAggs(s)
	})
}

func FuzzCWParseSortSpec(f *testing.F) {
	for _, s := range []string{"", "f", "f desc", "f asc x", "  desc", "@timestamp desc", "a b c d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = cwParseSortSpec(s)
	})
}

func FuzzCWParseTimeUnix(f *testing.F) {
	for _, s := range []string{"", "0", "1700000000", "1700000000.5", "abc", "-1", "1e308", "NaN", "Inf", "2026-06-20T00:00:00Z"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = cwParseTimeUnix(s)
	})
}

func FuzzAmplifyParseBuildSpec(f *testing.F) {
	for _, s := range []string{
		"", "version: 1", "frontend:\n  phases:\n    build:\n      commands:\n        - npm run build",
		"version: 1\nartifacts:\n  baseDirectory: dist\n  files:\n    - '**/*'",
		":\n:\n:", "\t\t\t", "- - - -", "version: [1,2,3]",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		_, _ = amplifyParseBuildSpec(text)
	})
}
