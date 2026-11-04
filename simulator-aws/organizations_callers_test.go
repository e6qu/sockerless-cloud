package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

func readVendoredAWSSpec(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open("../specs/cloud-api/aws/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	reader, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// TestOrgCallerScopesCoverTheModel holds the caller rules to the vendored AWS
// Organizations model: one rule per operation, and the operations that
// declare no AWSOrganizationsNotInUseException are the ones an account in no
// organization may call.
func TestOrgCallerScopesCoverTheModel(t *testing.T) {
	var model struct {
		Shapes map[string]struct {
			Type   string `json:"type"`
			Errors []struct {
				Target string `json:"target"`
			} `json:"errors"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(readVendoredAWSSpec(t, "organizations.smithy.json.gz"), &model); err != nil {
		t.Fatal(err)
	}
	var operations []string
	for id, shape := range model.Shapes {
		if shape.Type != "operation" {
			continue
		}
		op := id[strings.LastIndex(id, "#")+1:]
		operations = append(operations, op)
		scope, ok := orgCallerScopes[op]
		if !ok {
			t.Errorf("%s has no caller rule", op)
			continue
		}
		outsideOrganization := true
		for _, e := range shape.Errors {
			if strings.HasSuffix(e.Target, "#AWSOrganizationsNotInUseException") {
				outsideOrganization = false
			}
		}
		// An invited account accepts its invitation from outside any
		// organization, although AcceptHandshake declares the exception.
		if op == "AcceptHandshake" {
			outsideOrganization = true
		}
		if outsideOrganization != (scope == orgAnyCaller) {
			t.Errorf("%s: callable outside an organization is %v by the model, the rule says %v", op, outsideOrganization, scope == orgAnyCaller)
		}
	}
	sort.Strings(operations)
	if len(operations) != len(orgCallerScopes) {
		t.Errorf("the model has %d operations and the rules name %d", len(operations), len(orgCallerScopes))
	}
}

// TestOrgDelegatedOperationsMatchTheUserGuide holds the delegated rules to the
// AWS Organizations User Guide: a delegated administrator for any service
// reads what "Delegated administrator for AWS services that work with
// Organizations" lists, and the delegation policy opens what "Create a
// resource-based delegation policy" lists.
func TestOrgDelegatedOperationsMatchTheUserGuide(t *testing.T) {
	listed := func(page, after string) map[string]bool {
		body := string(readVendoredAWSSpec(t, page))
		_, rest, found := strings.Cut(body, after)
		if !found {
			t.Fatalf("%s has no %q", page, after)
		}
		ops := map[string]bool{}
		for _, line := range strings.Split(rest, "\n") {
			op, ok := strings.CutPrefix(strings.TrimSpace(line), "+ `")
			if !ok {
				if len(ops) > 0 && strings.TrimSpace(line) != "" {
					break
				}
				continue
			}
			ops[strings.TrimSuffix(op, "`")] = true
		}
		return ops
	}
	readOnly := listed("organizations-delegated-admin.md.gz", "read-only permissions:")
	delegable := listed("organizations-delegation-policy.md.gz", "supported for delegation policy:")
	if len(readOnly) == 0 || len(delegable) == 0 {
		t.Fatal("the guide pages list no operation")
	}
	for op := range readOnly {
		if s := orgCallerScopes[op]; s != orgDelegatedReadOnly && s != orgAnyAccount && s != orgAnyCaller {
			t.Errorf("%s: a delegated administrator reads it, the rule is %d", op, s)
		}
	}
	for op := range delegable {
		if s := orgCallerScopes[op]; s == orgManagementOnly || s == orgMemberAccountOnly {
			t.Errorf("%s: the delegation policy can open it, the rule is %d", op, s)
		}
	}
	for op, s := range orgCallerScopes {
		if s == orgDelegable && !delegable[op] {
			t.Errorf("%s: the rule delegates it, the guide does not", op)
		}
	}
}

// TestIAMDenialMessagesMatchTheVendoredGuide holds each policy kind's wording
// to the IAM User Guide's access-denied message examples.
func TestIAMDenialMessagesMatchTheVendoredGuide(t *testing.T) {
	guide := string(readVendoredAWSSpec(t, "iam-troubleshoot-access-denied.md.gz"))
	for kind, phrases := range iamPolicyKindPhrases {
		if implicit := "because no " + phrases[0] + " allows the "; !strings.Contains(guide, implicit) {
			t.Errorf("kind %d: the guide has no %q", kind, implicit)
		}
		if explicit := "with an explicit deny in " + phrases[1]; !strings.Contains(guide, explicit) {
			t.Errorf("kind %d: the guide has no %q", kind, explicit)
		}
	}
	d := iamDenial{Principal: "arn:aws:iam::123456789012:user/John", Action: "codecommit:ListRepositories", Policy: iamServiceControlPolicy}
	if got, want := d.message(), "User: arn:aws:iam::123456789012:user/John is not authorized to perform: codecommit:ListRepositories because no service control policy allows the codecommit:ListRepositories action"; got != want {
		t.Errorf("implicit: %q, want %q", got, want)
	}
	d.Explicit = true
	if got, want := d.message(), "User: arn:aws:iam::123456789012:user/John is not authorized to perform: codecommit:ListRepositories with an explicit deny in a service control policy"; got != want {
		t.Errorf("explicit: %q, want %q", got, want)
	}
}
