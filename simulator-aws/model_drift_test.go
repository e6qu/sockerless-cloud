package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every operation in every vendored model is implemented, or its absence is
// declared here with the reason. The service-conformance floors count operations
// served, so a re-vendored model's new operations would sit silently
// unimplemented; this fails as soon as a vendored model declares an operation
// whose name appears nowhere in the handwritten source. Scan names rather than
// registration idioms, since the simulators register five ways: the scan cannot
// prove an operation works, but it cannot miss one that does not exist.

// modelScopedDriftExemptions exempts an operation for one model only, keyed
// "<model file>:<operation>", for names another service legitimately implements
// (AWS Glue serves its own CreateSession). Scoped entries skip the staleness
// sweep; an entry citing "BUGS.md <id>" waits on that open row, which
// TestScopedDriftExemptionsCiteOpenBugs holds.
var modelScopedDriftExemptions = map[string]string{
	// Amazon EC2 published transit gateway attachment NAT on 2026-10-22, and
	// the aws-sdk-go-v2 release that carries it is still in its adoption
	// quarantine, so no SDK test can drive an implementation yet.
	"ec2.smithy.json.gz:GetTransitGatewayAttachmentNatConfiguration":    "BUGS.md 3574",
	"ec2.smithy.json.gz:ModifyTransitGatewayAttachmentNatConfiguration": "BUGS.md 3574",
	// Amazon EC2 image findings and Amazon ECR replication backfill arrived in
	// the models on 2026-10-27, with the same quarantine on their SDK release.
	"ec2.smithy.json.gz:GetImageFindings":                   "BUGS.md 3794",
	"ecr.smithy.json.gz:DescribeReplicationBackfillResults": "BUGS.md 3794",
	"ecr.smithy.json.gz:ListReplicationBackfills":           "BUGS.md 3794",
	"ecr.smithy.json.gz:StartReplicationBackfill":           "BUGS.md 3794",
	"ecr.smithy.json.gz:StopReplicationBackfill":            "BUGS.md 3794",
}

var modelDriftExemptions = map[string]string{
	// S3 routes bucket subresources by query parameter, so these operation
	// names never appear as strings; each is served by the subresource table
	// in s3_bucket_subresources.go. The names are listed rather than the
	// files skipped, so a *new* S3 operation still trips the gate.
	"DeleteBucketAnalyticsConfiguration":          "s3 ?analytics subresource",
	"PutBucketEncryption":                         "s3 ?encryption subresource",
	"PutBucketNotificationConfiguration":          "s3 ?notification subresource",
	"DeleteBucketCors":                            "s3 ?cors subresource",
	"DeleteBucketEncryption":                      "s3 ?encryption subresource",
	"DeleteBucketIntelligentTieringConfiguration": "s3 ?intelligent-tiering subresource",
	"DeleteBucketLifecycle":                       "s3 ?lifecycle subresource",
	"DeleteBucketMetricsConfiguration":            "s3 ?metrics subresource",
	"DeleteBucketOwnershipControls":               "s3 ?ownershipControls subresource",
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
	"PutBucketLifecycleConfiguration":             "s3 ?lifecycle subresource",
	"PutBucketLogging":                            "s3 ?logging subresource",
	"PutBucketMetricsConfiguration":               "s3 ?metrics subresource",
	"PutBucketOwnershipControls":                  "s3 ?ownershipControls subresource",
	"PutBucketReplication":                        "s3 ?replication subresource",
	"PutBucketRequestPayment":                     "s3 ?requestPayment subresource",
	"PutBucketTagging":                            "s3 ?tagging subresource",
	"PutBucketVersioning":                         "s3 ?versioning subresource",
	"PutBucketWebsite":                            "s3 ?website subresource",
	"PutPublicAccessBlock":                        "s3 ?publicAccessBlock subresource",
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
			if sourceNamesOperation(source, operation) {
				continue
			}
			if _, exempt := modelDriftExemptions[operation]; exempt {
				continue
			}
			if _, exempt := modelScopedDriftExemptions[filepath.Base(path)+":"+operation]; exempt {
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
		if sourceNamesOperation(source, operation) {
			t.Errorf("exemption for %q is stale: the operation now appears in the source; remove the entry",
				operation)
		}
	}
}

// sourceNamesOperation reports whether the source names an operation. A bare
// substring search counts a longer name that merely contains a shorter one —
// DeleteBucketLifecycleConfiguration would mark DeleteBucketLifecycle as
// implemented — and would silently stop covering the shorter operation, so the
// name has to end where the operation's name ends. It may be preceded by
// anything, because handlers are conventionally named for the operation with a
// prefix (handleS3ListJobs serves ListJobs).
func sourceNamesOperation(source, operation string) bool {
	for i := 0; ; {
		j := strings.Index(source[i:], operation)
		if j < 0 {
			return false
		}
		end := i + j + len(operation)
		if !isGoIdentifierRune(source, end) {
			return true
		}
		i = i + j + 1
	}
}

// isGoIdentifierRune reports whether the byte at an index continues an
// identifier, treating an index outside the source as a boundary.
func isGoIdentifierRune(source string, index int) bool {
	if index < 0 || index >= len(source) {
		return false
	}
	c := source[index]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// readAllHandwrittenSource returns the handwritten source with comments
// stripped, so an operation mentioned in prose does not count as implemented.
func readAllHandwrittenSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var source strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		// Parsing without parser.ParseComments drops every comment from the
		// syntax tree, so the re-printed file carries code only.
		parsed, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		var stripped bytes.Buffer
		if printErr := printer.Fprint(&stripped, fset, parsed); printErr != nil {
			t.Fatal(printErr)
		}
		source.Write(stripped.Bytes())
		source.WriteByte('\n')
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

// TestS3SubresourceExemptionsAreRouted holds each S3 exemption to its reason: the
// bucket-subresource table must route the named subresource with the verb the
// operation needs, so a deleted table entry cannot leave its operations exempted
// and unserved.
func TestS3SubresourceExemptionsAreRouted(t *testing.T) {
	for operation, reason := range modelDriftExemptions {
		sub, ok := strings.CutPrefix(reason, "s3 ?")
		if !ok {
			t.Errorf("%s: exemption reason %q is not an S3 subresource claim; this test only "+
				"knows how to verify those, so either state the subresource or verify the new class",
				operation, reason)
			continue
		}
		sub, ok = strings.CutSuffix(sub, " subresource")
		if !ok {
			t.Errorf("%s: exemption reason %q does not end in \" subresource\"", operation, reason)
			continue
		}
		spec, routed := bucketSubresourceHandlers[sub]
		if !routed {
			t.Errorf("%s: exempted as the %q subresource, but the bucket-subresource table does "+
				"not route %q — the operation is unserved, not routed by query parameter",
				operation, sub, sub)
			continue
		}
		switch {
		case strings.HasPrefix(operation, "Put") && spec.putStatus == 0:
			t.Errorf("%s: the %q subresource has no PUT shape, so the exemption's claim that this "+
				"operation is routed there is false", operation, sub)
		case strings.HasPrefix(operation, "Delete") && !spec.hasDelete:
			t.Errorf("%s: the %q subresource does not accept DELETE, so the exemption's claim that "+
				"this operation is routed there is false", operation, sub)
		}
	}
}

// TestScopedDriftExemptionsCiteOpenBugs holds each scoped exemption that cites
// a bug to that bug: the row must still be open in BUGS.md, and the operation
// must still be unserved. Closing the bug without implementing the operation,
// or implementing it without dropping the exemption, fails here.
func TestScopedDriftExemptionsCiteOpenBugs(t *testing.T) {
	bugs, err := os.ReadFile(filepath.Join("..", "BUGS.md"))
	if err != nil {
		t.Fatal(err)
	}
	source := readAllHandwrittenSource(t)
	for key, reason := range modelScopedDriftExemptions {
		id, ok := strings.CutPrefix(reason, "BUGS.md ")
		if !ok {
			continue
		}
		if !strings.Contains(string(bugs), "\n| "+id+" |") {
			t.Errorf("%s: exemption cites BUGS.md %s, which is not an open row", key, id)
		}
		operation := key[strings.LastIndex(key, ":")+1:]
		if sourceNamesOperation(source, operation) {
			t.Errorf("exemption for %q is stale: the operation now appears in the source; remove the entry", key)
		}
	}
}
