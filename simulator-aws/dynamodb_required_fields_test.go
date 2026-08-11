package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These tests are lever 1 of the #652 "silent incompleteness" prevention work:
// the vendored Smithy model is the source of truth for which input members are
// @required, and CI fails if the simulator under-validates one. A missing
// required member must be a loud ValidationException — never an accepted request
// that succeeds with plausible-wrong / absent data.

// loadDDBRequiredMembers reads the vendored DynamoDB Smithy model and returns,
// per operation, the sorted set of top-level input members carrying the
// smithy.api#required trait.
func loadDDBRequiredMembers(t *testing.T) map[string][]string {
	t.Helper()
	path := filepath.Join("..", "specs", "cloud-api", "aws", "dynamodb.smithy.json.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v — run scripts/fetch-aws-spec.sh", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	defer gz.Close()

	var doc struct {
		Shapes map[string]struct {
			Type  string `json:"type"`
			Input struct {
				Target string `json:"target"`
			} `json:"input"`
			Members map[string]struct {
				Traits map[string]json.RawMessage `json:"traits"`
			} `json:"members"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	short := func(id string) string {
		if i := strings.Index(id, "#"); i >= 0 {
			return id[i+1:]
		}
		return id
	}
	out := map[string][]string{}
	for id, s := range doc.Shapes {
		if s.Type != "operation" {
			continue
		}
		var req []string
		if in, ok := doc.Shapes[s.Input.Target]; ok {
			for m, mv := range in.Members {
				if _, isReq := mv.Traits["smithy.api#required"]; isReq {
					req = append(req, m)
				}
			}
		}
		sort.Strings(req)
		out[short(id)] = req
	}
	return out
}

func ddbRegisteredOps(jsonRouter interface{ Targets() []string }) []string {
	var ops []string
	for _, target := range jsonRouter.Targets() {
		if op, ok := strings.CutPrefix(target, "DynamoDB_20120810."); ok {
			ops = append(ops, op)
		}
	}
	sort.Strings(ops)
	return ops
}

// TestDDBRequiredMembersMatchSpec is the completeness guard: the simulator's
// required-member registry must list exactly the @required top-level input
// members the Smithy model declares for every registered DynamoDB operation.
// If AWS marks a new member required (or the registry drifts), this fails — so a
// required member can't silently go unvalidated.
func TestDDBRequiredMembersMatchSpec(t *testing.T) {
	spec := loadDDBRequiredMembers(t)
	_, jsonRouter, _ := buildConformanceSimulator(t)

	for _, op := range ddbRegisteredOps(jsonRouter) {
		want := spec[op]
		got := append([]string(nil), ddbRequiredMembers[op]...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: registry required members %v, but Smithy model declares %v", op, got, want)
		}
	}
}

// TestDDBRequiredMembersEnforced drives each registered DynamoDB operation with
// raw awsJson and, for every @required input member, sends an otherwise-complete
// request that omits exactly that member, asserting a 400 ValidationException
// that names the member. Validation runs before any handler logic, so the
// placeholder values for the other members never need to be type-valid.
func TestDDBRequiredMembersEnforced(t *testing.T) {
	spec := loadDDBRequiredMembers(t)
	_, jsonRouter, _ := buildConformanceSimulator(t)

	for _, op := range ddbRegisteredOps(jsonRouter) {
		required := spec[op]
		for _, omit := range required {
			body := map[string]any{}
			for _, m := range required {
				if m != omit {
					body[m] = "x" // any non-null placeholder; the handler is never reached
				}
			}
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
			req.Header.Set("X-Amz-Target", "DynamoDB_20120810."+op)
			rec := httptest.NewRecorder()
			jsonRouter.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s omitting %q: status %d, want 400; body=%s", op, omit, rec.Code, rec.Body.String())
				continue
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Errorf("%s omitting %q: undecodable body %q: %v", op, omit, rec.Body.String(), err)
				continue
			}
			if typ, _ := resp["__type"].(string); !strings.Contains(typ, "ValidationException") {
				t.Errorf("%s omitting %q: __type=%q, want ValidationException", op, omit, typ)
			}
			if msg, _ := resp["message"].(string); !strings.Contains(msg, ddbWireMemberLabel(omit)) {
				t.Errorf("%s omitting %q: message %q does not name the member", op, omit, msg)
			}
		}
	}
}
