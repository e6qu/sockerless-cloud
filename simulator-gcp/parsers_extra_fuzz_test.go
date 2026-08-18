package main

import (
	"mime"
	"strings"
	"testing"
)

// FuzzBQParseTableRef fuzzes the BigQuery table-reference parser, which splits a
// backtick-quoted `project.dataset.table` string on "." and indexes the result.
//
// Property: a reference the parser accepts names all three components. A parser
// that indexed past the split and returned an empty dataset or table would
// address `projects//datasets//tables/` downstream — a bug that never panics,
// so "did not crash" would not see it.
func FuzzBQParseTableRef(f *testing.F) {
	seeds := []string{
		"", "t", "d.t", "p.d.t", "`p.d.t`", "...", ".", "a.b.c.d.e",
		"`", "``", "`a`", "p..t", "é.ê.ë",
		// Found by this target on the nightly run: trimming quotes from the
		// ends leaves one in the middle, so this parsed to a table named "`0".
		"0.`0", "`p`.`d`.`t`", "p.d.`t",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		project, dataset, table, err := bqParseTableRef("defproj", ref)
		if err != nil {
			return
		}
		if project == "" || dataset == "" || table == "" {
			t.Fatalf("bqParseTableRef(%q) accepted the reference but returned an "+
				"incomplete address: project=%q dataset=%q table=%q", ref, project, dataset, table)
		}
		if strings.ContainsAny(project+dataset+table, ".`") {
			t.Fatalf("bqParseTableRef(%q) left reference punctuation in a component: "+
				"project=%q dataset=%q table=%q", ref, project, dataset, table)
		}
	})
}

// FuzzArtifactRegistryImageParts fuzzes the AR image-name splitter, which
// SplitN("/", 3)s an untrusted repository path and indexes parts[0..2].
//
// Property: an accepted name yields four non-empty components that rejoin into
// the name it was given. A splitter that dropped or duplicated a segment would
// resolve a push to the wrong repository without ever panicking.
func FuzzArtifactRegistryImageParts(f *testing.F) {
	seeds := []string{
		"", "a", "a/b", "a/b/c", "a/b/c/d", "///", "p/r/img:tag",
		"p/r/img@sha256:deadbeef", "é/ê/ë",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		project, location, repoID, imagePath, ok := artifactRegistryImageParts(name)
		if !ok {
			return
		}
		if project == "" || location == "" || repoID == "" || imagePath == "" {
			t.Fatalf("artifactRegistryImageParts(%q) accepted the name but returned an "+
				"incomplete address: project=%q location=%q repo=%q image=%q",
				name, project, location, repoID, imagePath)
		}
		if !strings.HasSuffix(name, repoID+"/"+imagePath) {
			t.Fatalf("artifactRegistryImageParts(%q) returned repo=%q image=%q, which do "+
				"not come from the end of the name", name, repoID, imagePath)
		}
	})
}

// FuzzGCSMultipartBoundary fuzzes the multipart boundary extractor, which does a
// ToLower-prefix-match then slices the *original* Content-Type at the "="
// offset — a class prone to case-folding byte-length slice bugs, because
// lowercasing İ or ẞ changes the byte length and moves every later offset.
//
// Two properties catch that class without a panic:
//
//   - The extractor never invents a boundary. A non-empty result requires the
//     header to actually carry a `boundary=` parameter.
//   - When the standard parser cannot read the header — which is exactly when
//     the hand-rolled offset path runs — the boundary it returns must appear
//     verbatim in the header it was sliced out of. A slice taken at a
//     lowercased offset lands on different bytes and fails this.
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
		got := gcsMultipartBoundary(ct)
		if got != "" && !strings.Contains(strings.ToLower(ct), "boundary=") {
			t.Fatalf("gcsMultipartBoundary(%q) invented the boundary %q", ct, got)
		}
		if _, _, err := mime.ParseMediaType(ct); err != nil && got != "" {
			if !strings.Contains(ct, got) {
				t.Fatalf("gcsMultipartBoundary(%q) returned %q, which is not a slice of the "+
					"header — the offset was taken on the case-folded copy", ct, got)
			}
		}
		if again := gcsMultipartBoundary(ct); again != got {
			t.Fatalf("gcsMultipartBoundary(%q) is not deterministic: %q then %q", ct, got, again)
		}

		// A bucket's location classification is a closed set; anything else
		// would reach a client as an undeclared enum value.
		switch kind := gcsLocationType(ct); kind {
		case "multi-region", "dual-region", "region":
		default:
			t.Fatalf("gcsLocationType(%q) = %q, which is not a location type", ct, kind)
		}
	})
}
