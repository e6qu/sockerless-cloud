package main

import "testing"

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
		_ = matchesFilter(entry, filter)
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
		_, _, _ = bigtableInstanceParts(parent)
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
		_, _ = kmsVersionNumber(name)
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
		field, _ := gcpParseOrderBy(orderBy)
		_ = gcpFieldString(m, field)
	})
}
