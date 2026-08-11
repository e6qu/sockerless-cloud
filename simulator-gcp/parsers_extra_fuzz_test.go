package main

import "testing"

// FuzzBQParseTableRef fuzzes the BigQuery table-reference parser, which splits a
// backtick-quoted `project.dataset.table` string on "." and indexes the result.
func FuzzBQParseTableRef(f *testing.F) {
	seeds := []string{
		"", "t", "d.t", "p.d.t", "`p.d.t`", "...", ".", "a.b.c.d.e",
		"`", "``", "`a`", "p..t", "é.ê.ë",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		_, _, _, _ = bqParseTableRef("defproj", ref)
	})
}

// FuzzArtifactRegistryImageParts fuzzes the AR image-name splitter, which
// SplitN("/", 3)s an untrusted repository path and indexes parts[0..2].
func FuzzArtifactRegistryImageParts(f *testing.F) {
	seeds := []string{
		"", "a", "a/b", "a/b/c", "a/b/c/d", "///", "p/r/img:tag",
		"p/r/img@sha256:deadbeef", "é/ê/ë",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		_, _, _, _, _ = artifactRegistryImageParts(name)
	})
}

// FuzzGCSMultipartBoundary fuzzes the multipart boundary extractor, which does a
// ToLower-prefix-match then slices the *original* Content-Type at the "="
// offset — a class prone to case-folding byte-length slice bugs.
func FuzzGCSMultipartBoundary(f *testing.F) {
	seeds := []string{
		"", "multipart/related; boundary=abc", "BOUNDARY=x",
		"multipart/related;boundary=", "boundary=\"q'", "İ; boundary=x",
		"x; BOUNDARY=İ", "; ; boundary=", "ẞ=1; boundary=2",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ct string) {
		_ = gcsMultipartBoundary(ct)
		_ = gcsLocationType(ct)
	})
}
