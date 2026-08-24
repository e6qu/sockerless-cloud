package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every operation in every vendored model is implemented, or its absence is
// declared here with the reason.
//
// The service-conformance floors hold the count of operations *served*, so a
// model re-vendored with new operations trips nothing: the served count is
// unchanged, and the new operations sit silently unimplemented. That is how
// forty-three operations drifted between 2026-08-12 and 2026-08-23 — five
// services' models moved and no gate noticed. This test is the drift sweep
// that found them, made permanent: it fails the moment a vendored model
// declares an operation whose name appears nowhere in the handwritten source.
//
// The check is by name over the source rather than by registration idiom,
// because the simulators register five different ways (awsJson targets, query
// actions, REST mux paths, query-parameter subresources, dispatch switches).
// A name-scan cannot prove an operation works — the conformance and SDK
// suites do that — but it cannot miss one that does not exist at all, which
// is the failure mode floors cannot see.

// modelDriftExemptions names the operations deliberately not implemented.
// Every entry carries its reason; an entry without a live BUGS.md record or a
// routing equivalence is a bug in this table.
var modelDriftExemptions = map[string]string{
	// S3 routes bucket subresources by query parameter, so these operation
	// names never appear as strings; each is served by the subresource table
	// in s3_bucket_subresources.go. The names are listed rather than the
	// files skipped, so a *new* S3 operation still trips the gate.
	"DeleteBucketAnalyticsConfiguration":          "s3 ?analytics subresource",
	"DeleteBucketCors":                            "s3 ?cors subresource",
	"DeleteBucketEncryption":                      "s3 ?encryption subresource",
	"DeleteBucketIntelligentTieringConfiguration": "s3 ?intelligent-tiering subresource",
	"DeleteBucketInventoryConfiguration":          "s3 ?inventory subresource",
	"DeleteBucketLifecycle":                       "s3 ?lifecycle subresource",
	"DeleteBucketMetricsConfiguration":            "s3 ?metrics subresource",
	"DeleteBucketOwnershipControls":               "s3 ?ownershipControls subresource",
	"DeleteBucketPolicy":                          "s3 ?policy subresource",
	"DeleteBucketReplication":                     "s3 ?replication subresource",
	"DeleteBucketTagging":                         "s3 ?tagging subresource",
	"DeleteBucketWebsite":                         "s3 ?website subresource",
	"DeletePublicAccessBlock":                     "s3 ?publicAccessBlock subresource",
	"GetBucketAccelerateConfiguration":            "s3 ?accelerate subresource",
	"GetBucketCors":                               "s3 ?cors subresource",
	"GetBucketEncryption":                         "s3 ?encryption subresource",
	"GetBucketLifecycleConfiguration":             "s3 ?lifecycle subresource",
	"GetBucketLogging":                            "s3 ?logging subresource",
	"GetBucketNotificationConfiguration":          "s3 ?notification subresource",
	"GetBucketOwnershipControls":                  "s3 ?ownershipControls subresource",
	"GetBucketReplication":                        "s3 ?replication subresource",
	"GetBucketRequestPayment":                     "s3 ?requestPayment subresource",
	"GetBucketTagging":                            "s3 ?tagging subresource",
	"GetBucketVersioning":                         "s3 ?versioning subresource",
	"GetBucketWebsite":                            "s3 ?website subresource",
	"GetPublicAccessBlock":                        "s3 ?publicAccessBlock subresource",
	"PutBucketAccelerateConfiguration":            "s3 ?accelerate subresource",
	"PutBucketAnalyticsConfiguration":             "s3 ?analytics subresource",
	"PutBucketCors":                               "s3 ?cors subresource",
	"PutBucketIntelligentTieringConfiguration":    "s3 ?intelligent-tiering subresource",
	"PutBucketInventoryConfiguration":             "s3 ?inventory subresource",
	"PutBucketLifecycleConfiguration":             "s3 ?lifecycle subresource",
	"PutBucketLogging":                            "s3 ?logging subresource",
	"PutBucketMetricsConfiguration":               "s3 ?metrics subresource",
	"PutBucketOwnershipControls":                  "s3 ?ownershipControls subresource",
	"PutBucketPolicy":                             "s3 ?policy subresource",
	"PutBucketReplication":                        "s3 ?replication subresource",
	"PutBucketRequestPayment":                     "s3 ?requestPayment subresource",
	"PutBucketTagging":                            "s3 ?tagging subresource",
	"PutBucketVersioning":                         "s3 ?versioning subresource",
	"PutBucketWebsite":                            "s3 ?website subresource",
	"PutPublicAccessBlock":                        "s3 ?publicAccessBlock subresource",

	// BUG-73: the data plane of S3 Object Lambda, whose control plane is the
	// s3control service — not a vendored slice. Serving the callback without
	// access points would acknowledge writes nothing can read back.
	"WriteGetObjectResponse": "BUG-73: S3 Object Lambda's control plane (s3control) is not a vendored slice",
}

func TestVendoredModelOperationsAreImplementedOrExempt(t *testing.T) {
	source := readAllHandwrittenSource(t)
	models, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "aws", "*.smithy.json.gz"))
	if err != nil || len(models) == 0 {
		t.Fatalf("no vendored models found: %v", err)
	}
	var missing []string
	for _, path := range models {
		for _, operation := range modelOperations(t, path) {
			if strings.Contains(source, operation) {
				continue
			}
			if _, exempt := modelDriftExemptions[operation]; exempt {
				continue
			}
			missing = append(missing,
				filepath.Base(path)+": "+operation)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d model operation(s) appear nowhere in the handwritten source — "+
			"a model was re-vendored with operations the simulator does not implement. "+
			"Implement them, or exempt each with its reason:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}

	// The exemption list may only shrink truthfully: an entry whose operation
	// has since been implemented is stale and hides nothing.
	for operation := range modelDriftExemptions {
		if strings.Contains(source, operation) {
			t.Errorf("exemption for %q is stale: the operation now appears in the source; remove the entry",
				operation)
		}
	}
}

func readAllHandwrittenSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		source.Write(data)
	}
	return source.String()
}

func modelOperations(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		Shapes map[string]struct {
			Type string `json:"type"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatal(err)
	}
	var operations []string
	for shapeID, shape := range model.Shapes {
		if shape.Type != "operation" {
			continue
		}
		operations = append(operations, shapeID[strings.LastIndex(shapeID, "#")+1:])
	}
	sort.Strings(operations)
	return operations
}
