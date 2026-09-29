package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/listq"
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
		// A filter either parses to a node or is rejected; a parsed filter's
		// verdict on the same entry never changes, so a list cannot show an
		// entry on one call and drop it on the next.
		node, err := parseLogFilter(filter)
		if (node == nil) == (err == nil) {
			t.Fatalf("parseLogFilter(%q) must return exactly one of a node and an error", filter)
		}
		if err != nil {
			return
		}
		doc, err := listq.ToDoc(entry)
		if err != nil {
			t.Fatal(err)
		}
		if got, again := node.Eval(doc), node.Eval(doc); got != again {
			t.Fatalf("filter %q is not deterministic", filter)
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
		// Found by this target on the nightly run: Atoi accepts a leading sign
		// and leading zeros, so these named version 1 under three more spellings
		// than the resource has.
		"/cryptoKeyVersions/0000000000000000001",
		"/cryptoKeyVersions/01",
		"/cryptoKeyVersions/+1",
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
