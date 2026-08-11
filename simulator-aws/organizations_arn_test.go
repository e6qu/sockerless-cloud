package main

import (
	"compress/gzip"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An AWS Organizations ARN is not free-form: the model constrains each one with
// a smithy.api#pattern, and that pattern is the whole of what a client can rely
// on. It is also where the identity of a resource lives — a handshake's ARN is
// the only place the handshake's purpose appears, a policy's says whether AWS
// manages it, a transfer's says what moves and which way — so an ARN that
// merely looks plausible can still name the wrong thing. These tests drive the
// simulator's own API and hold every ARN it emits to the pattern the vendored
// model declares.

// loadOrganizationsARNPatterns returns the compiled smithy.api#pattern of each
// ARN shape the Organizations model declares.
func loadOrganizationsARNPatterns(t *testing.T) map[string]*regexp.Regexp {
	t.Helper()
	patterns := map[string]*regexp.Regexp{}
	for name, re := range loadOrganizationsStringPatterns(t) {
		if strings.HasSuffix(name, "Arn") {
			patterns[name] = re
		}
	}
	// A missing shape would silently assert nothing, so the shapes the tests
	// below name are required to be present.
	for _, required := range []string{
		"AccountArn", "HandshakeArn", "OrganizationArn", "OrganizationalUnitArn",
		"PolicyArn", "ResourcePolicyArn", "ResponsibilityTransferArn", "RootArn",
	} {
		if patterns[required] == nil {
			t.Fatalf("the vendored model declares no %s pattern", required)
		}
	}
	return patterns
}

func TestOrganizationsARNsMatchTheModelPatterns(t *testing.T) {
	patterns := loadOrganizationsARNPatterns(t)
	srv, _, _ := buildConformanceSimulator(t)

	call := func(operation, body string) map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, jsonTargetReq("AWSOrganizationsV20161128."+operation, body))
		if rr.Code != 200 {
			t.Fatalf("%s: status %d, body %s", operation, rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: decode response: %v", operation, err)
		}
		return out
	}
	// arnOf reads a nested member's Arn, failing rather than skipping when the
	// path is absent — an assertion that quietly matches nothing is the failure
	// mode these tests exist to prevent.
	arnOf := func(response map[string]any, member string) string {
		t.Helper()
		object, ok := response[member].(map[string]any)
		if !ok {
			t.Fatalf("response carries no %s object: %v", member, response)
		}
		arn, ok := object["Arn"].(string)
		if !ok || arn == "" {
			t.Fatalf("%s carries no Arn: %v", member, object)
		}
		return arn
	}
	matches := func(shape, arn string) {
		t.Helper()
		if !patterns[shape].MatchString(arn) {
			t.Errorf("%s %q does not match the model's %s pattern %s",
				shape, arn, shape, patterns[shape].String())
		}
	}

	matches("OrganizationArn", arnOf(call("DescribeOrganization", `{}`), "Organization"))

	roots := call("ListRoots", `{}`)["Roots"].([]any)
	if len(roots) == 0 {
		t.Fatal("the simulator materializes no root, so no root ARN is asserted")
	}
	rootID := roots[0].(map[string]any)["Id"].(string)
	matches("RootArn", roots[0].(map[string]any)["Arn"].(string))

	ou := call("CreateOrganizationalUnit", `{"ParentId":"`+rootID+`","Name":"Engineering"}`)
	matches("OrganizationalUnitArn", arnOf(ou, "OrganizationalUnit"))

	// CreatePolicy answers with the document beside a PolicySummary, and the
	// ARN lives on the summary.
	policy := call("CreatePolicy",
		`{"Name":"Deny","Type":"SERVICE_CONTROL_POLICY","Description":"d","Content":"{}"}`)["Policy"]
	summary, ok := policy.(map[string]any)
	if !ok {
		t.Fatalf("CreatePolicy carries no Policy: %v", policy)
	}
	matches("PolicyArn", arnOf(summary, "PolicySummary"))

	resourcePolicy, ok := call("PutResourcePolicy", `{"Content":"{}"}`)["ResourcePolicy"].(map[string]any)
	if !ok {
		t.Fatal("PutResourcePolicy carries no ResourcePolicy")
	}
	matches("ResourcePolicyArn", arnOf(resourcePolicy, "ResourcePolicySummary"))

	// An AWS-managed policy takes the other arm of the PolicyArn alternation:
	// it belongs to no organization and carries the literal "aws" where a
	// customer policy carries an account. Only that arm admits the uppercase
	// letters in "p-FullAWSAccess", so building it the customer way emits an
	// ARN the model rejects.
	var managed map[string]any
	for _, entry := range call("ListPolicies", `{"Filter":"SERVICE_CONTROL_POLICY"}`)["Policies"].([]any) {
		if summary := entry.(map[string]any); summary["AwsManaged"] == true {
			managed = summary
		}
	}
	if managed == nil {
		t.Fatal("the simulator lists no AWS-managed policy, so the managed ARN arm is not asserted")
	}
	managedARN := managed["Arn"].(string)
	matches("PolicyArn", managedARN)
	if !strings.HasPrefix(managedARN, "arn:aws:organizations::aws:policy/") {
		t.Errorf("AWS-managed policy ARN %q is scoped to an account and an organization; "+
			"an AWS-managed policy belongs to neither", managedARN)
	}

	// A handshake's ARN carries what the handshake is for. Two of them are
	// created by different operations and must not read alike.
	invitation := arnOf(call("InviteAccountToOrganization",
		`{"Target":{"Id":"555555555555","Type":"ACCOUNT"}}`), "Handshake")
	matches("HandshakeArn", invitation)
	if !strings.Contains(invitation, "/invite/") {
		t.Errorf("invitation handshake ARN %q does not name the invitation", invitation)
	}

	allFeatures := arnOf(call("EnableAllFeatures", `{}`), "Handshake")
	matches("HandshakeArn", allFeatures)
	if !strings.Contains(allFeatures, "/enable_all_features/") {
		t.Errorf("enable-all-features handshake ARN %q reads as some other handshake", allFeatures)
	}

	transferInvite := call("InviteOrganizationToTransferResponsibility",
		`{"Type":"BILLING","SourceName":"Payer","Target":{"Id":"666666666666","Type":"ACCOUNT"}}`)
	transferHandshake := arnOf(transferInvite, "Handshake")
	matches("HandshakeArn", transferHandshake)
	if !strings.Contains(transferHandshake, "/transfer_responsibility/") {
		t.Errorf("responsibility-transfer handshake ARN %q reads as some other handshake", transferHandshake)
	}

	transfers := call("ListOutboundResponsibilityTransfers", `{}`)["ResponsibilityTransfers"].([]any)
	if len(transfers) == 0 {
		t.Fatal("the invite recorded no outbound transfer, so no transfer ARN is asserted")
	}
	matches("ResponsibilityTransferArn", transfers[0].(map[string]any)["Arn"].(string))

	accounts := call("ListAccounts", `{}`)["Accounts"].([]any)
	if len(accounts) == 0 {
		t.Fatal("the simulator materializes no account, so no account ARN is asserted")
	}
	matches("AccountArn", accounts[0].(map[string]any)["Arn"].(string))
}

// TestIAMResourceARNs_OrganizationsReadsTheTypeFromTheIdentifier pins the rule
// the derivation rests on: an AWS Organizations request says which resource it
// names by the shape of the identifier, not by the member it arrives under.
// AttachPolicy sends its target under TargetId whether that target is a root,
// an organizational unit or an account, so a derivation keyed on field
// spellings could only ever guess.
func TestIAMResourceARNs_OrganizationsReadsTheTypeFromTheIdentifier(t *testing.T) {
	buildConformanceSimulator(t)

	policy := OrgPolicy{Id: "p-attach0001", Name: "Deny", Type: "SERVICE_CONTROL_POLICY"}
	policy.Arn = orgPolicyArn(policy.Id, policy.Type, policy.AwsManaged)
	orgPolicies.Put(policy.Id, policy)

	transfer := OrgResponsibilityTransfer{Id: "rt-transfer01", Type: "BILLING", Direction: "INBOUND"}
	transfer.Arn = orgResponsibilityTransferArn(transfer.Id, transfer.Type, transfer.Direction)
	orgResponsibilityTransfers.Put(transfer.Id, transfer)

	derive := func(operation, body string) []string {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		return iamDerivedResourceARNs(r, "organizations", operation, "us-east-1", awsAccountID())
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	for _, tc := range []struct {
		name      string
		operation string
		body      string
		want      []string
	}{{
		name:      "a policy and the organizational unit it attaches to",
		operation: "AttachPolicy",
		body:      `{"PolicyId":"p-attach0001","TargetId":"ou-root-attach01"}`,
		want:      []string{orgOUArn("ou-root-attach01"), policy.Arn},
	}, {
		name:      "the same member naming a root",
		operation: "AttachPolicy",
		body:      `{"PolicyId":"p-attach0001","TargetId":"r-root"}`,
		want:      []string{policy.Arn, orgRootArn("r-root")},
	}, {
		name:      "the same member naming an account",
		operation: "AttachPolicy",
		body:      `{"PolicyId":"p-attach0001","TargetId":"999999999999"}`,
		want:      []string{orgAccountArn("999999999999"), policy.Arn},
	}, {
		name:      "a tagging call naming a responsibility transfer",
		operation: "TagResource",
		body:      `{"ResourceId":"rt-transfer01"}`,
		want:      []string{transfer.Arn},
	}, {
		// The reference declares only the account for DescribeEffectivePolicy
		// while the member accepts three types. Emitting the organizational
		// unit's ARN anyway would require a grant on a resource AWS does not
		// check, denying a caller whose policy AWS accepts.
		name:      "a type the action does not declare derives nothing",
		operation: "DescribeEffectivePolicy",
		body:      `{"TargetId":"ou-root-attach01","PolicyType":"TAG_POLICY"}`,
		want:      nil,
	}, {
		// A policy's ARN carries its type and whether AWS manages it, neither
		// of which the request supplies, so an unknown policy cannot be named.
		name:      "a policy the simulator does not hold derives nothing",
		operation: "DescribePolicy",
		body:      `{"PolicyId":"p-unknown0001"}`,
		want:      nil,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := derive(tc.operation, tc.body); !equal(got, tc.want) {
				t.Errorf("%s derived %v, want %v", tc.operation, got, tc.want)
			}
		})
	}
}

// TestOrganizationsReportsShapesTheModelConstrains covers the Organizations
// members whose smithy.api#pattern says more than "a string": a hierarchy path
// and a transfer participant's management account. Both are read by clients as
// identifiers, so a value of the wrong shape is not a cosmetic difference.
func TestOrganizationsReportsShapesTheModelConstrains(t *testing.T) {
	patterns := loadOrganizationsStringPatterns(t)
	srv, _, _ := buildConformanceSimulator(t)

	call := func(operation, body string) map[string]any {
		t.Helper()
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, jsonTargetReq("AWSOrganizationsV20161128."+operation, body))
		if rr.Code != 200 {
			t.Fatalf("%s: status %d, body %s", operation, rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: decode response: %v", operation, err)
		}
		return out
	}

	// A path locates a node in the hierarchy, so it reads from the organization
	// downwards. Reporting the walk the simulator makes — node first, up to the
	// root — would name the same nodes in the order that inverts the meaning.
	accounts := call("ListAccounts", `{}`)["Accounts"].([]any)
	if len(accounts) == 0 {
		t.Fatal("the simulator materializes no account, so no path is asserted")
	}
	accountID := accounts[0].(map[string]any)["Id"].(string)
	path := call("ListEffectivePolicyValidationErrors",
		`{"AccountId":"`+accountID+`","PolicyType":"TAG_POLICY"}`)["Path"].(string)
	if !patterns["Path"].MatchString(path) {
		t.Errorf("Path %q does not match the model's pattern %s", path, patterns["Path"].String())
	}
	if !strings.HasPrefix(path, awsOrgID()+"/") || !strings.HasSuffix(path, "/"+accountID+"/") {
		t.Errorf("Path %q does not run from the organization down to the account", path)
	}

	// Inviting an organization names no account. A management account is
	// learned when the handshake is accepted, and until then the member has no
	// value the model would accept — an organization id in it is an account id
	// that is not one.
	transfer := call("InviteOrganizationToTransferResponsibility",
		`{"Type":"BILLING","SourceName":"Payer","Target":{"Id":"o-invitee9999","Type":"ORGANIZATION"}}`)
	handshakeID := transfer["Handshake"].(map[string]any)["Id"].(string)
	var recorded map[string]any
	for _, entry := range call("ListOutboundResponsibilityTransfers", `{}`)["ResponsibilityTransfers"].([]any) {
		if candidate := entry.(map[string]any); candidate["ActiveHandshakeId"] == handshakeID {
			recorded = candidate
		}
	}
	if recorded == nil {
		t.Fatalf("the invite recorded no transfer for handshake %s", handshakeID)
	}
	target, ok := recorded["Target"].(map[string]any)
	if !ok {
		t.Fatalf("the transfer carries no Target: %v", recorded)
	}
	if id, present := target["ManagementAccountId"]; present {
		t.Errorf("an organization-targeted transfer reports ManagementAccountId %q, "+
			"which no account is", id)
	}
}

// loadOrganizationsStringPatterns returns every compiled smithy.api#pattern the
// Organizations model declares, keyed by shape name.
func loadOrganizationsStringPatterns(t *testing.T) map[string]*regexp.Regexp {
	t.Helper()
	path := filepath.Join("..", "specs", "cloud-api", "aws", "organizations.smithy.json.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	defer gz.Close()
	var doc struct {
		Shapes map[string]struct {
			Traits struct {
				Pattern string `json:"smithy.api#pattern"`
			} `json:"traits"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	out := map[string]*regexp.Regexp{}
	for id, shape := range doc.Shapes {
		if shape.Traits.Pattern == "" {
			continue
		}
		if re, err := regexp.Compile(shape.Traits.Pattern); err == nil {
			out[id[strings.Index(id, "#")+1:]] = re
		}
	}
	if out["Path"] == nil {
		t.Fatal("the vendored model declares no Path pattern")
	}
	return out
}
