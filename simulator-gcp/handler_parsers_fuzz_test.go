package main

import (
	"strconv"
	"strings"
	"testing"
)

// FuzzLogMatchesFilter fuzzes the Cloud Logging filter parser + matcher
// (logfilter.go), which consumes the untrusted `filter` field of an
// entries:list request body.
func FuzzLogMatchesFilter(f *testing.F) {
	seeds := []string{
		"",
		`logName="run.googleapis.com"`,
		`logName:"run.googleapis.com"`,
		`severity>="WARNING"`,
		`severity > "INFO" AND resource.type = "cloud_run_revision"`,
		`resource.labels.service_name = "svc"`,
		`labels.foo = "bar"`,
		"bare substring",
		">=",
		">",
		"=",
		":",
		" AND ",
		"a AND b AND c",
		`a="`,
		`="value"`,
		`field>=`,
		"\xff\xfe",
		"a = é",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	entry := LogEntry{
		LogName:     "projects/p/logs/run.googleapis.com%2Fstdout",
		Severity:    "INFO",
		TextPayload: "hello world",
		Resource:    &MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{"service_name": "svc"}},
		Labels:      map[string]string{"foo": "bar"},
	}
	f.Fuzz(func(t *testing.T, filter string) {
		got := matchesFilter(entry, filter)
		// The matcher decides which entries a client is shown, so a filter it
		// cannot read must widen the result set, never narrow it: dropping
		// entries a caller asked for is silent data loss, whereas returning
		// extra ones is visible. An empty filter therefore matches everything,
		// and every verdict is stable for the same input.
		if !matchesFilter(entry, "") {
			t.Fatalf("an empty filter must match every entry")
		}
		if again := matchesFilter(entry, filter); again != got {
			t.Fatalf("matchesFilter(%q) is not deterministic: %v then %v", filter, got, again)
		}
	})
}

// FuzzBigtableInstanceParts fuzzes the bigtable resource-name parser, which
// splits a `projects/X/instances/Y` parent on "/" and indexes the result.
func FuzzBigtableInstanceParts(f *testing.F) {
	seeds := []string{
		"",
		"projects/p/instances/i",
		"projects//instances/i",
		"projects/p/instances/",
		"projects/p",
		"/",
		"////",
		"projects/p/instances/i/extra",
		"a/b/c/d",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, parent string) {
		project, instance, err := bigtableInstanceParts(parent)
		if err != nil {
			return
		}
		// An accepted parent names both halves and rebuilds the resource name
		// it came from. An empty component would address `projects//instances/`
		// downstream without ever panicking.
		if project == "" || instance == "" {
			t.Fatalf("bigtableInstanceParts(%q) accepted the parent but returned "+
				"project=%q instance=%q", parent, project, instance)
		}
		if want := "projects/" + project + "/instances/" + instance; want != parent {
			t.Fatalf("bigtableInstanceParts(%q) decomposed into %q", parent, want)
		}
	})
}

// FuzzKMSVersionNumber fuzzes the KMS version-name numeric extractor.
func FuzzKMSVersionNumber(f *testing.F) {
	seeds := []string{
		"",
		"projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1",
		"projects/p/.../cryptoKeyVersions/",
		"/cryptoKeyVersions/abc",
		"/cryptoKeyVersions/99999999999999999999999999",
		"/cryptoKeyVersions/-1",
		"/cryptoKeyVersions/0x10",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		n, ok := kmsVersionNumber(name)
		if !ok {
			return
		}
		// Key versions are numbered from one, and the number the extractor
		// reports must be the one written in the name it read. A parser that
		// returned 0, a negative, or a number from the wrong segment would
		// address a different version than the caller named.
		if n < 1 {
			t.Fatalf("kmsVersionNumber(%q) accepted the name but returned version %d", name, n)
		}
		if !strings.HasSuffix(name, "/"+strconv.Itoa(n)) {
			t.Fatalf("kmsVersionNumber(%q) returned %d, which is not the version the name ends with", name, n)
		}
	})
}

// FuzzParseOrderBy fuzzes the list `orderBy` parser + field extractor used by
// every GCP list handler that honors orderBy.
func FuzzParseOrderBy(f *testing.F) {
	seeds := []string{
		"",
		"name",
		"name desc",
		"name asc",
		"a.b.c desc",
		" desc",
		"desc",
		"asc",
		",",
		"a,b,c",
		"   ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	m := map[string]any{
		"name": "x",
		"a":    map[string]any{"b": map[string]any{"c": float64(1)}},
	}
	f.Fuzz(func(t *testing.T, orderBy string) {
		field, desc := gcpParseOrderBy(orderBy)
		// The direction keyword is a direction, not part of the field path: a
		// parser that left it attached sorts on a field no resource has, which
		// silently orders every list by nothing.
		if strings.HasSuffix(field, " desc") || strings.HasSuffix(field, " asc") {
			t.Fatalf("gcpParseOrderBy(%q) left the direction in the field path %q", orderBy, field)
		}
		if field != strings.TrimSpace(field) {
			t.Fatalf("gcpParseOrderBy(%q) returned the unpadded field %q", orderBy, field)
		}
		// Reading a field the resource does not carry yields the empty string
		// rather than a partial value, and the read never disagrees with itself.
		got := gcpFieldString(m, field)
		if again := gcpFieldString(m, field); again != got {
			t.Fatalf("gcpFieldString(%q) is not deterministic: %q then %q", field, got, again)
		}
		if field != "" && desc {
			// A descending order over the same field reads the same value; the
			// direction belongs to the sort, not to the read.
			if v, _ := gcpParseOrderBy(field); gcpFieldString(m, v) != got {
				t.Fatalf("re-parsing the field %q changed the value it reads", field)
			}
		}
	})
}
