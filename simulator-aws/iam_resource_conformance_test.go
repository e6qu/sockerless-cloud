package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The call-time IAM gate authorizes a request against the resource it names.
// Getting that resource wrong is invisible in every other test the simulator
// has: the request still succeeds for a caller whose policy says Resource "*",
// and only a caller with a resource-scoped grant — the caller least-privilege
// exists for — is denied. These tests bind the gate to the data AWS publishes
// about which resource each action authorizes against, so a service the gate
// silently fails to derive is a counted, failing number rather than a defect a
// consumer discovers in production.

// awsSignedJSONRequest builds the signed awsJson request an AWS SDK sends: the
// operation in X-Amz-Target and the region the gate derives ARNs from in the
// SigV4 credential scope.
func awsSignedJSONRequest(target, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", target)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/aws/aws4_request, SignedHeaders=host;x-amz-target, Signature=00")
	return r
}

// awsServiceReference is the parsed surface of one vendored AWS Service
// Reference document.
type awsServiceReference struct {
	Name    string
	Actions []struct {
		Name      string
		Resources []struct{ Name string }
	}
}

func loadServiceReferences(t *testing.T) map[string]*awsServiceReference {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "aws",
		"service-reference", "*.servicereference.json.gz"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vendored Service Reference documents (glob err: %v) — run scripts/fetch-aws-service-reference.sh", err)
	}
	out := map[string]*awsServiceReference{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gunzip %s: %v", p, err)
		}
		var doc awsServiceReference
		if err := json.NewDecoder(gz).Decode(&doc); err != nil {
			t.Fatalf("decode %s: %v", p, err)
		}
		_ = gz.Close()
		_ = f.Close()
		if doc.Name == "" || len(doc.Actions) == 0 {
			t.Fatalf("%s: not a Service Reference document", p)
		}
		out[doc.Name] = &doc
	}
	return out
}

// resourceTypes returns the resource types the reference declares for an
// action, and whether the action exists at all.
func (s *awsServiceReference) resourceTypes(action string) ([]string, bool) {
	for _, a := range s.Actions {
		if a.Name != action {
			continue
		}
		types := make([]string, 0, len(a.Resources))
		for _, r := range a.Resources {
			types = append(types, r.Name)
		}
		sort.Strings(types)
		return types, true
	}
	return nil, false
}

// TestIAMResourceTypesTableMatchesTheVendoredReference proves the generated
// table is the vendored reference and nothing else. The table is the gate's
// only statement of which resource type an action authorizes against, so a
// hand-edit or a stale regeneration would silently change authorization
// decisions; this rebuilds it from the vendored documents and compares.
func TestIAMResourceTypesTableMatchesTheVendoredReference(t *testing.T) {
	refs := loadServiceReferences(t)

	// The services the generator emits, read back from the table itself so the
	// test does not carry its own copy of the list.
	services := map[string]bool{}
	for action := range iamActionResourceTypes {
		service, _, ok := strings.Cut(action, ":")
		if !ok {
			t.Fatalf("table key %q is not service:Action shaped", action)
		}
		services[service] = true
	}
	if len(services) == 0 {
		t.Fatal("iamActionResourceTypes is empty — run scripts/gen-aws-iam-resource-types.sh")
	}

	want := map[string][]string{}
	for service := range services {
		ref, ok := refs[service]
		if !ok {
			t.Fatalf("the table covers %q but no Service Reference is vendored for it", service)
		}
		for _, a := range ref.Actions {
			if len(a.Resources) == 0 {
				continue
			}
			types, _ := ref.resourceTypes(a.Name)
			want[service+":"+a.Name] = types
		}
	}

	var problems []string
	for action, wantTypes := range want {
		gotTypes, ok := iamActionResourceTypes[action]
		if !ok {
			problems = append(problems, action+": declared by the reference, absent from the table")
			continue
		}
		got := append([]string(nil), gotTypes...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(wantTypes, ",") {
			problems = append(problems, fmt.Sprintf("%s: table has [%s], reference declares [%s]",
				action, strings.Join(got, ", "), strings.Join(wantTypes, ", ")))
		}
	}
	for action := range iamActionResourceTypes {
		if _, ok := want[action]; !ok {
			problems = append(problems, action+": in the table, not declared by the reference")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("iam_resource_types_gen.go is out of date with the vendored Service Reference "+
			"(run scripts/gen-aws-iam-resource-types.sh):\n  %s", strings.Join(problems, "\n  "))
	}
}

// iamServiceForServedOperation resolves the IAM service prefix a served
// operation belongs to. JSON targets go through the production classifier so
// the test measures what the gate actually sees; query actions registered
// without a Version are shared by EC2, IAM and STS, and are resolved by which
// of those services the reference says defines the action.
func iamServiceForServedOperation(t *testing.T, refs map[string]*awsServiceReference,
	target, version, action string) (service, op string, ok bool) {
	t.Helper()
	if target != "" {
		r := awsSignedJSONRequest(target, "{}")
		full, classified := iamActionForRequest(r)
		if !classified {
			return "", "", false
		}
		service, op, _ = strings.Cut(full, ":")
		return service, op, true
	}
	byVersion := map[string]string{
		"2016-11-15": "ec2", "2011-01-01": "autoscaling", "2010-08-01": "cloudwatch",
		"2010-03-31": "sns", "2015-12-01": "elasticloadbalancing", "2014-10-31": "rds",
		"2015-02-02": "elasticache",
	}
	if svc, found := byVersion[version]; found {
		return svc, action, true
	}
	if version != "" {
		return "", "", false
	}
	// The unversioned bucket: EC2, IAM and STS register there because their
	// action names do not collide across AWS.
	for _, candidate := range []string{"ec2", "iam", "sts"} {
		if ref, found := refs[candidate]; found {
			if _, defined := ref.resourceTypes(action); defined {
				return candidate, action, true
			}
		}
	}
	return "", "", false
}

func slicesIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// assertAliasesAreRealFields holds a service's alias table to the fields the
// vendored model declares on operations that authorize against the aliased
// resource type. The aliases are the one hand-written part of a derivation —
// the renamings the Service Reference did not follow — so an alias that is a
// guess, a typo, or one the API has since dropped derives nothing, and nothing
// about that is visible to a caller whose policy says Resource "*". Both AWS
// Glue guesses this rejected were of that kind, and one of them named a
// resource type no action authorizes against at all.
func assertAliasesAreRealFields(t *testing.T, service string, aliases map[string][]string,
	byOperation map[string]map[string]bool) {
	t.Helper()

	typesByVariable := map[string][]string{}
	for key, format := range iamResourceARNFormats {
		svc, resourceType, _ := strings.Cut(key, ":")
		if svc != service {
			continue
		}
		for _, variable := range iamARNFormatVariables(format) {
			typesByVariable[variable] = append(typesByVariable[variable], resourceType)
		}
	}

	var problems []string
	for key, spellings := range aliases {
		// An entry may be keyed "<resourceType>.<variable>" to answer for one
		// type alone, which is how AWS Systems Manager's four ${ResourceId}
		// types are told apart.
		resourceTypes := typesByVariable[key]
		if scopedType, scopedVariable, scoped := strings.Cut(key, "."); scoped {
			resourceTypes = nil
			for _, t := range typesByVariable[scopedVariable] {
				if t == scopedType {
					resourceTypes = []string{t}
				}
			}
			if resourceTypes == nil {
				problems = append(problems, fmt.Sprintf(
					"%s: %s does not identify %s by that variable — the alias is dead", key, service, scopedType))
				continue
			}
		}
		if len(resourceTypes) == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: no %s resource type is identified by this variable — the alias is dead", key, service))
			continue
		}
		for _, alias := range spellings {
			used := false
			for action, declared := range iamActionResourceTypes {
				svc, operation, _ := strings.Cut(action, ":")
				if svc != service || !slicesIntersect(declared, resourceTypes) {
					continue
				}
				if byOperation[operation][alias] {
					used = true
					break
				}
			}
			if !used {
				problems = append(problems, fmt.Sprintf(
					"%s -> %s: no %s operation authorizing against %s takes a %s field",
					key, alias, service, strings.Join(resourceTypes, "/"), alias))
			}
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("the %s alias table does not match the vendored model:\n  %s",
			service, strings.Join(problems, "\n  "))
	}
}

func TestIAMEC2ParameterAliasesAreRealRequestParameters(t *testing.T) {
	assertAliasesAreRealFields(t, "ec2", iamEC2ParameterAliases, loadEC2RequestParameters(t))
}

func TestIAMGlueFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "glue", iamGlueFieldAliases, loadGlueRequestMembers(t))
}

// loadRequestFields returns, per operation of a vendored model, the fields its
// request carries, spelled as they arrive on the wire and lower-cased.
//
// How a member is spelled on the wire is the protocol's business, so each
// caller supplies that rule. It is not cosmetic: Amazon EC2 sends a list under
// the member's singular xmlName — TerminateInstances takes InstanceIds and
// sends InstanceId.1 — so reading member names there would look for parameters
// no request ever carries.
func loadRequestFields(t *testing.T, service string, wireName func(member string, traits map[string]string) string) map[string]map[string]bool {
	t.Helper()
	fields, _ := loadRequestShapes(t, service, wireName)
	return fields
}

// loadRequestShapes returns, per operation, the request fields the model
// declares and — for a member that is a structure rather than a scalar — the
// fields nested inside it.
//
// The nesting matters to the measurement, not just to the derivation. A probe
// that sends a string for every member never puts an object where the API puts
// one, so it cannot exercise a derivation that reads inside a structure, and an
// operation whose resource is only named there would be counted as underived
// while a real request derives it perfectly well.
func loadRequestShapes(t *testing.T, service string, wireName func(member string, traits map[string]string) string) (map[string]map[string]bool, map[string]map[string][]string) {
	t.Helper()
	path := filepath.Join("..", "specs", "cloud-api", "aws", service+".smithy.json.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v — run scripts/fetch-aws-spec.sh %s", path, err, service)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	defer gz.Close()

	type member struct {
		Target string                     `json:"target"`
		Traits map[string]json.RawMessage `json:"traits"`
	}
	var doc struct {
		Shapes map[string]struct {
			Type    string                  `json:"type"`
			Input   struct{ Target string } `json:"input"`
			Members map[string]member       `json:"members"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	fields := map[string]map[string]bool{}
	nested := map[string]map[string][]string{}
	for id, shape := range doc.Shapes {
		if shape.Type != "operation" || shape.Input.Target == "" {
			continue
		}
		operation := id[strings.Index(id, "#")+1:]
		named := map[string]bool{}
		inner := map[string][]string{}
		for name, m := range doc.Shapes[shape.Input.Target].Members {
			traits := map[string]string{}
			for trait, raw := range m.Traits {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					traits[trait] = v
				}
			}
			// The member's own wire name, in its own case: a probe that
			// lower-cased it sent a body no client sends, and the production
			// derivation reads the real member name, so an operation that
			// derives for every real caller was measured as deriving nothing.
			wire := wireName(name, traits)
			named[wire] = true
			if target, ok := doc.Shapes[m.Target]; ok && target.Type == "structure" {
				for innerName := range target.Members {
					inner[wire] = append(inner[wire], innerName)
				}
			}
		}
		fields[operation] = named
		nested[operation] = inner
	}
	if len(fields) == 0 {
		t.Fatalf("%s declares no operations", path)
	}
	return fields, nested
}

// iamProbeBody builds the request body a coverage probe sends: a placeholder for
// every field the operation declares, and an object for a member the model says
// is a structure, so a derivation that reads inside one is exercised rather
// than counted as absent.
func iamProbeBody(members map[string]bool, nested map[string][]string) map[string]any {
	body := make(map[string]any, len(members))
	for name := range members {
		body[name] = iamProbeMemberValue(name, "probe")
	}
	for member, inner := range nested {
		object := make(map[string]any, len(inner))
		for _, name := range inner {
			object[name] = "probe"
		}
		if len(object) > 0 {
			body[member] = object
		}
	}
	return body
}

// The ec2Query protocol does not send a member under its member name: the
// aws.protocols#ec2QueryName trait wins where present, otherwise the
// smithy.api#xmlName trait with its first letter upper-cased.
func ec2WireName(member string, traits map[string]string) string {
	if n := traits["aws.protocols#ec2QueryName"]; n != "" {
		return n
	}
	if n := traits["smithy.api#xmlName"]; n != "" {
		return strings.ToUpper(n[:1]) + n[1:]
	}
	return member
}

// awsJson and awsQuery both send a member under its own name.
func memberWireName(member string, _ map[string]string) string { return member }

func loadEC2RequestParameters(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "ec2", ec2WireName)
}

func loadGlueRequestMembers(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "glue", memberWireName)
}

func loadRDSRequestParameters(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "rds", memberWireName)
}

func loadSSMRequestMembers(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "ssm", memberWireName)
}

func loadElastiCacheRequestParameters(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "elasticache", memberWireName)
}

func loadDynamoDBRequestMembers(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "dynamodb", memberWireName)
}

func loadCloudTrailRequestMembers(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "cloudtrail", memberWireName)
}

func loadKMSRequestMembers(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "kms", memberWireName)
}

// Amazon EventBridge's IAM service prefix is "events" while its vendored model
// carries the service's own name, so the two are named separately here.
func loadEventBridgeRequestMembers(t *testing.T) map[string]map[string]bool {
	return loadRequestFields(t, "eventbridge", memberWireName)
}

// iamEC2DerivesItsResource reports whether the derivation produces an ARN for
// an operation — not whether the table knows which type it would build. It runs
// the production path against a request carrying every parameter the model
// declares for the operation: if nothing is derived from all of them, no real
// request derives anything either. Measuring it this way rather than
// re-deciding the rules here is what makes the count reflect the code, so a
// rule that stops firing shows up as coverage falling.
//
// An operation that creates its resource (CreateInternetGateway) carries no
// identifier for it and correctly derives nothing.
func iamEC2DerivesItsResource(operation string, params map[string]bool) bool {
	types := iamActionResourceTypes["ec2:"+operation]
	if len(types) == 0 {
		return false
	}
	values := make(map[string]string, len(params))
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue // the request already carries these
		}
		values[name] = "probe"
	}
	return len(iamDerivedResourceARNs(iamEC2Request(operation, values), "ec2", operation,
		"us-east-1", "123456789012")) > 0
}

// iamGlueDerivesItsResource runs the production derivation against a request
// carrying every member the model declares for the operation, the same way
// iamEC2DerivesItsResource does: if nothing is derived from all of them, no
// real request derives anything.
func iamGlueDerivesItsResource(operation string, members map[string]bool, nested map[string][]string) bool {
	types := iamActionResourceTypes["glue:"+operation]
	if len(types) == 0 {
		return false
	}
	encoded, err := json.Marshal(iamProbeBody(members, nested))
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "glue", operation, "us-east-1", "123456789012")) > 0
}

// iamRDSDerivesItsResource runs the production derivation against a request
// carrying every parameter the model declares for the operation.
func iamRDSDerivesItsResource(operation string, params map[string]bool) bool {
	types := iamActionResourceTypes["rds:"+operation]
	if len(types) == 0 {
		return false
	}
	form := "Action=" + operation + "&Version=2014-10-31"
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		form += "&" + name + "=probe"
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, "rds", operation, "us-east-1", "123456789012")) > 0
}

// TestIAMRDSFieldAliasesAreRealRequestParameters holds every Amazon RDS alias
// to a parameter the vendored model declares on an operation that authorizes
// against the aliased resource type. RDS needs an alias for almost every one of
// its resource types — the reference says ${DbInstanceName} where the API sends
// DBInstanceIdentifier — so this table is the largest of the three and the one
// most in need of holding to something.
func TestIAMRDSFieldAliasesAreRealRequestParameters(t *testing.T) {
	assertAliasesAreRealFields(t, "rds", iamRDSFieldAliases, loadRDSRequestParameters(t))
}

func TestIAMCloudTrailFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "cloudtrail", iamCloudTrailFieldAliases, loadCloudTrailRequestMembers(t))
}

func TestIAMEventBridgeFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "events", iamEventBridgeFieldAliases, loadEventBridgeRequestMembers(t))
}

func TestIAMKMSFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "kms", iamKMSFieldAliases, loadKMSRequestMembers(t))
}

// iamKMSDerivesItsResource runs the production derivation against a request
// carrying every member the model declares for the operation.
func iamKMSDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["kms:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		body[name] = iamProbeMemberValueFor("kms", name,
			"arn:aws:kms:us-east-1:"+iamProbeAccount+":probe")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "kms", operation, "us-east-1", "123456789012")) > 0
}

// iamOrganizationsProbeState seeds one resource of every AWS Organizations type
// and returns the identifier of each, keyed by the model shape a request member
// targets when it accepts that identifier.
//
// AWS Organizations reads a resource's type out of the identifier itself, so a
// probe filling every member with the same placeholder would measure zero
// however well the derivation works — it would be asking what resource
// "probe" names, which is nothing. The identifiers here are not invented to
// suit the metric: each is the shape the model's own @pattern requires of that
// member, and a request carrying anything else is rejected by the service
// before authorization is ever reached.
//
// A member that accepts several types (ParentId, PolicyTargetId,
// TaggableResourceId, ChildId are alternations over the id prefixes) is given
// the organizational unit, which every one of them admits.
func iamOrganizationsProbeState() map[string]string {
	root := OrgRoot{Id: "r-prb0", Name: "Root"}
	root.Arn = orgRootArn(root.Id)
	orgRoots.Put(root.Id, root)

	ou := OrgOU{Id: "ou-prb0-probe000", Name: "Probe", ParentId: root.Id}
	ou.Arn = orgOUArn(ou.Id)
	orgOUs.Put(ou.Id, ou)

	account := OrgAccount{Id: "123456789012", Name: "Probe", ParentId: root.Id}
	account.Arn = orgAccountArn(account.Id)
	orgAccounts.Put(account.Id, account)

	policy := OrgPolicy{Id: "p-probe0000", Name: "Probe", Type: "SERVICE_CONTROL_POLICY"}
	policy.Arn = orgPolicyArn(policy.Id, policy.Type, policy.AwsManaged)
	orgPolicies.Put(policy.Id, policy)

	handshake := OrgHandshake{Id: "h-probe000", Action: "INVITE", State: "OPEN"}
	handshake.Arn = orgHandshakeArn(handshake.Id, handshake.Action)
	orgHandshakes.Put(handshake.Id, handshake)

	transfer := OrgResponsibilityTransfer{Id: "rt-probe000", Type: "BILLING", Direction: "OUTBOUND"}
	transfer.Arn = orgResponsibilityTransferArn(transfer.Id, transfer.Type, transfer.Direction)
	orgResponsibilityTransfers.Put(transfer.Id, transfer)

	resourcePolicy := OrgResourcePolicy{Id: "rp-probe000"}
	resourcePolicy.Arn = orgResourcePolicyArn(resourcePolicy.Id)
	orgResourcePolicies.Put(orgSingletonKey, resourcePolicy)

	return map[string]string{
		"AccountId":                account.Id,
		"HandshakePartyId":         account.Id,
		"PolicyId":                 policy.Id,
		"RootId":                   root.Id,
		"OrganizationalUnitId":     ou.Id,
		"HandshakeId":              handshake.Id,
		"ResponsibilityTransferId": transfer.Id,
		"ParentId":                 ou.Id,
		"PolicyTargetId":           ou.Id,
		"TaggableResourceId":       ou.Id,
		"ChildId":                  ou.Id,
	}
}

// iamOrganizationsDerivesItsResource runs the production derivation against a
// request naming resources that exist, under the members the model declares.
func iamOrganizationsDerivesItsResource(operation string, members map[string]string,
	ids map[string]string) bool {

	if len(iamActionResourceTypes["organizations:"+operation]) == 0 {
		return false
	}
	body := map[string]any{}
	for path, shape := range members {
		value, ok := ids[shape]
		if !ok {
			value = "probe"
		}
		member, inner, nested := strings.Cut(path, ".")
		if !nested {
			body[member] = value
			continue
		}
		object, _ := body[member].(map[string]any)
		if object == nil {
			object = map[string]any{}
			body[member] = object
		}
		object[inner] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "organizations", operation, "us-east-1", "123456789012")) > 0
}

// loadOrganizationsRequestShapes returns, per operation, every request member
// with the model shape it targets — a member nested in a structure keyed
// "Member.Inner" — so a probe can send an identifier of the shape the member
// accepts rather than a placeholder.
func loadOrganizationsRequestShapes(t *testing.T) map[string]map[string]string {
	t.Helper()
	path := filepath.Join("..", "specs", "cloud-api", "aws", "organizations.smithy.json.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v — run scripts/fetch-aws-spec.sh organizations", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	defer gz.Close()

	var doc struct {
		Shapes map[string]struct {
			Type    string                  `json:"type"`
			Input   struct{ Target string } `json:"input"`
			Members map[string]struct {
				Target string `json:"target"`
			} `json:"members"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	simple := func(target string) string { return target[strings.Index(target, "#")+1:] }

	shapes := map[string]map[string]string{}
	for id, shape := range doc.Shapes {
		if shape.Type != "operation" || shape.Input.Target == "" {
			continue
		}
		members := map[string]string{}
		for name, m := range doc.Shapes[shape.Input.Target].Members {
			target, known := doc.Shapes[m.Target]
			if known && target.Type == "structure" {
				for innerName, inner := range target.Members {
					members[name+"."+innerName] = simple(inner.Target)
				}
				continue
			}
			members[name] = simple(m.Target)
		}
		shapes[id[strings.Index(id, "#")+1:]] = members
	}
	if len(shapes) == 0 {
		t.Fatalf("%s declares no operations", path)
	}
	return shapes
}

// iamELBv2DerivesItsResource runs the production derivation against a request
// carrying every parameter the model declares, with the ARN-bearing ones
// carrying an ARN — which is what a real caller sends, since Elastic Load
// Balancing resources are addressed by ARN and not by parts.
func iamELBv2DerivesItsResource(operation string, params map[string]bool) bool {
	if len(iamActionResourceTypes["elasticloadbalancing:"+operation]) == 0 {
		return false
	}
	form := "Action=" + operation + "&Version=2015-12-01"
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		value := "probe"
		if lower := strings.ToLower(name); strings.HasSuffix(lower, "arn") || strings.HasSuffix(lower, "arns") {
			value = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/probe/0123456789abcdef"
		}
		form += "&" + name + "=" + url.QueryEscape(value)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, "elasticloadbalancing", operation, "us-east-1", "123456789012")) > 0
}

// iamACMDerivesItsResource runs the production derivation against a request
// carrying every member the model declares, with the ARN-bearing ones carrying
// an ARN.
func iamACMDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["acm:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		if strings.HasSuffix(strings.ToLower(name), "arn") {
			body[name] = "arn:aws:acm:us-east-1:123456789012:certificate/0123abcd-ef45-6789-abcd-ef0123456789"
			continue
		}
		body[name] = iamProbeMemberValueFor("acm", name,
			"arn:aws:acm:us-east-1:"+iamProbeAccount+":probe")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "acm", operation, "us-east-1", "123456789012")) > 0
}

// iamCloudWatchDerivesItsResource runs the production derivation against a
// request carrying every member the model declares. A member that is an ARN by
// definition carries one, because that is what a real caller sends — this is
// not the same as filling an ordinary field with an ARN to satisfy the metric.
func iamCloudWatchDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["cloudwatch:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		body[name] = iamProbeMemberValueFor("cloudwatch", name,
			"arn:aws:cloudwatch:us-east-1:"+iamProbeAccount+":alarm:probe")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "cloudwatch", operation, "us-east-1", "123456789012")) > 0
}

func TestIAMCloudWatchFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "cloudwatch", iamCloudWatchFieldAliases,
		loadRequestFields(t, "cloudwatch", memberWireName))
}

// iamCloudMapProbeState seeds a namespace and a service named the way the
// coverage probe fills a request, because AWS Cloud Map's discovery operations
// address them by name while their ARNs carry the identifiers AWS assigned —
// so a probe that only fills fields would measure zero however well the
// derivation works. It is the same seeding Amazon EC2 Auto Scaling needs, and
// it is the state any real caller discovering a service is in.
func iamCloudMapProbeState() {
	namespace := CMNamespace{Id: "ns-probe", Name: "probe",
		Arn: "arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-probe"}
	cmNamespaces.Put(namespace.Id, namespace)
	service := CMService{Id: "srv-probe", Name: "probe", NamespaceId: namespace.Id,
		Arn: "arn:aws:servicediscovery:us-east-1:123456789012:service/srv-probe"}
	cmServices.Put(service.Id, service)
	// GetOperation resolves through the operation record, so the probe's
	// OperationId ("probe", the value the JSON probe fills) must name one.
	cmOperations.Put("probe", CMOperation{OperationId: "probe", Status: "SUCCESS",
		NamespaceId: namespace.Id, ServiceId: service.Id})
}

// iamSQSProbeState seeds the move task the probe's TaskHandle names, because
// cancelling a move is authorized against the source queue the task record —
// not the request — knows.
func iamSQSProbeState() {
	sqsMoveTasks.Put("probe", SQSMessageMoveTask{TaskHandle: "probe", Status: "RUNNING",
		SourceArn:      "arn:aws:sqs:us-east-1:123456789012:probe-dead-letter",
		DestinationArn: "arn:aws:sqs:us-east-1:123456789012:probe"})
}

// iamQueryProbeDerives runs the production derivation against a query-protocol
// request carrying every parameter the model declares.
func iamQueryProbeDerives(service, operation, version string, params map[string]bool) bool {
	if len(iamActionResourceTypes[service+":"+operation]) == 0 {
		return false
	}
	form := "Action=" + operation + "&Version=" + version
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		// One place decides what a member carries. This path used to keep its
		// own copy of that decision, so a rule added to iamProbeMemberValue —
		// an account identifier is twelve digits — did not reach the query
		// services at all, and sts:AssumeRoot stayed undecidable.
		value := iamProbeMemberValueFor(service, name,
			"arn:aws:"+service+":us-east-1:"+iamProbeAccount+":probe")
		form += "&" + name + "=" + url.QueryEscape(value)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, service, operation, "us-east-1", "123456789012")) > 0
}

// iamECRDerivesItsResource, iamKinesisDerivesItsResource and
// iamStatesDerivesItsResource run the production derivation against a request
// carrying every member the model declares. A member that is an ARN by
// definition carries one, because that is what a real caller sends.
func iamJSONProbeDerives(service, operation string, members map[string]bool, arnValue string) bool {
	if len(iamActionResourceTypes[service+":"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		body[name] = iamProbeMemberValueFor(service, name, arnValue)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, service, operation, "us-east-1", "123456789012")) > 0
}

// iamAutoScalingDerivesItsResource runs the production derivation against a
// request naming a group and a launch configuration that exist.
//
// Amazon EC2 Auto Scaling resolves its ARNs from the simulator's state rather
// than from request fields, because both carry an identifier AWS assigns that no
// request supplies. A probe that only fills fields would therefore measure zero
// however well the derivation works, so it seeds the two resources first — which
// is the state any real caller acting on a group is in.
func iamAutoScalingDerivesItsResource(operation string, params map[string]bool) bool {
	if len(iamActionResourceTypes["autoscaling:"+operation]) == 0 {
		return false
	}
	autoScalingGroups.Put("probe", AutoScalingGroup{Name: "probe", ARN: autoScalingGroupARN("probe")})
	asLaunchConfigurations.Put("probe", ASLaunchConfiguration{Name: "probe", ARN: launchConfigurationARN("probe")})

	form := "Action=" + operation + "&Version=2011-01-01"
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		form += "&" + name + "=probe"
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, "autoscaling", operation, "us-east-1", "123456789012")) > 0
}

// iamCloudTrailDerivesItsResource runs the production derivation against a
// request carrying every member the model declares for the operation. The
// tagging members ResourceId and ResourceIdList carry an ARN by definition,
// so they carry one here, exactly as a real caller sends them.
func iamCloudTrailDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["cloudtrail:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		switch strings.ToLower(name) {
		case "resourceid", "resourceidlist":
			body[name] = "arn:aws:cloudtrail:us-east-1:123456789012:trail/probe"
		default:
			body[name] = "probe"
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "cloudtrail", operation, "us-east-1", "123456789012")) > 0
}

// iamEventBridgeDerivesItsResource runs the production derivation against a
// request carrying every member the model declares for the operation.
func iamEventBridgeDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["events:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		body[name] = "probe"
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "events", operation, "us-east-1", "123456789012")) > 0
}

func TestIAMSSMFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "ssm", iamSSMFieldAliases, loadSSMRequestMembers(t))
}

// iamDynamoDBDerivesItsResource runs the production derivation against a request
// carrying every member the model declares for the operation.
func iamDynamoDBDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["dynamodb:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		body[name] = "probe"
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.0")
	return len(iamDerivedResourceARNs(r, "dynamodb", operation, "us-east-1", "123456789012")) > 0
}

// iamElastiCacheDerivesItsResource runs the production derivation against a
// request carrying every parameter the model declares for the operation.
func iamElastiCacheDerivesItsResource(operation string, params map[string]bool) bool {
	if len(iamActionResourceTypes["elasticache:"+operation]) == 0 {
		return false
	}
	form := "Action=" + operation + "&Version=2015-02-02"
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		form += "&" + name + "=probe"
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, "elasticache", operation, "us-east-1", "123456789012")) > 0
}

// iamSSMDerivesItsResource runs the production derivation against a request
// carrying every member the model declares for the operation.
func iamSSMDerivesItsResource(operation string, members map[string]bool) bool {
	if len(iamActionResourceTypes["ssm:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		body[name] = iamProbeMemberValueFor("ssm", name,
			"arn:aws:ssm:us-east-1:"+iamProbeAccount+":probe")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "ssm", operation, "us-east-1", "123456789012")) > 0
}

// iamHandwrittenDerivationServices are the services whose target resource is
// read by a per-service case in iamResourceARNsForRequest rather than from the
// generated resource-type table. They predate the table and are listed here so
// the coverage report does not read them as having no derivation at all — their
// coverage is per-request (the case fires only when the request carries the
// field it reads), which is precisely why the table-driven form replaced it.
var iamHandwrittenDerivationServices = map[string]bool{
	// Amazon DynamoDB's transactions and batches, whose tables ride per item
	// rather than at the top level.
	"dynamodb": true,
	// AWS Lambda's function name, which its REST path carries.
	"lambda": true,
}

// ----- Probing the services whose requests are not a flat awsJson body -------

// iamProductionProbeDerives runs the gate's own entry point —
// iamResourceARNsForRequest, the function the enforcement path calls — against a
// probe request, and reports whether it produced a resource rather than the "*"
// fallback. Every service the coverage measurement counts goes through a probe
// like this one: table membership states only that AWS declares a resource type
// for the action, never that this simulator can read it out of the request.
func iamProductionProbeDerives(r *http.Request, action string) bool {
	arns := iamResourceARNsForRequest(r, action)
	if len(arns) == 0 {
		return false
	}
	for _, arn := range arns {
		if arn == "*" {
			return false
		}
	}
	return true
}

// iamProbeMemberValue is what the probe sends for one request member: an ARN
// where the member is an ARN by definition — which is what a real caller
// sends — and a bare identifier otherwise.
func iamProbeMemberValue(name, arnValue string) string {
	return iamProbeMemberValueFor("", name, arnValue)
}

// iamProbeMemberValueFor is what a client puts in one request member.
//
// It takes the service because some members only accept a value that service
// defines: Systems Manager's ResourceType is a ResourceTypeForTagging, and the
// type is what selects the ARN format its ResourceId fills, so a placeholder
// there leaves the derivation nothing to select with.
//
// There is one of these. Each probe path used to fill members from its own
// copy of this decision — three copies — so a rule added to one reached only
// the services that went through it, and the operations behind the others kept
// measuring as underived while their production readers worked.
func iamProbeMemberValueFor(service, name, arnValue string) string {
	lower := strings.ToLower(name)
	if service == "ssm" && lower == "resourcetype" {
		// Any real member of the enum will do: the test pins all ten, and what
		// matters here is that the probe names one the service declares rather
		// than a placeholder it rejects.
		return "Parameter"
	}
	if strings.HasSuffix(lower, "arn") {
		return arnValue
	}
	// A member that names an AWS account carries twelve digits and nothing
	// else: the service rejects any other shape before authorization runs, so
	// "probe" is a value no client ever sends. Probing with it addresses the
	// operation at an input the service would refuse, and the derivation that
	// reads the account — sts:AssumeRoot's TargetPrincipal is the one this was
	// found through — correctly declines to build an ARN from it, which the
	// gate then reads as an operation that derives nothing.
	if iamProbeAccountMembers[lower] {
		return iamProbeAccount
	}
	// A queue is named by its URL, which is the only spelling Amazon SQS
	// accepts and therefore the only one a client sends.
	if lower == "queueurl" {
		return "http://localhost:4566/" + iamProbeAccount + "/probe"
	}
	return "probe"
}

// iamProbeAccount is the twelve-digit account the probe names, the same one the
// derived ARNs are built against.
const iamProbeAccount = "123456789012"

// iamProbeAccountMembers are the request members whose value is an AWS account
// identifier, under the lower-cased spelling the probe compares.
var iamProbeAccountMembers = map[string]bool{
	"targetprincipal": true,
	"accountid":       true,
	"awsaccountid":    true,
}

// iamAWSJSONProbeRequest is the awsJson request an SDK sends for an operation:
// every member the model declares, in the shape the model gives it — a list
// where the API takes a list, an object where it takes a structure — and an
// ARN in the members that are ARNs by definition.
func iamAWSJSONProbeRequest(
	service, operation, arnValue string, members map[string]string, nested map[string][]string,
) *http.Request {
	body := make(map[string]any, len(members))
	for name, kind := range members {
		value := iamProbeMemberValue(name, arnValue)
		switch kind {
		case "list":
			body[name] = []string{value}
		case "structure":
			object := make(map[string]any, len(nested[name]))
			for _, inner := range nested[name] {
				object[inner] = iamProbeMemberValue(inner, arnValue)
			}
			body[name] = object
		default:
			body[name] = value
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		encoded = []byte("{}")
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", service+"."+operation)
	return r
}

// iamAWSQueryProbeRequest is the form-encoded request an awsQuery service takes,
// with a list member sent under the protocol's indexed .member.N spelling.
func iamAWSQueryProbeRequest(operation, version, arnValue string, members map[string]string) *http.Request {
	form := "Action=" + operation + "&Version=" + version
	for name, kind := range members {
		if name == "Action" || name == "Version" {
			continue
		}
		key := name
		if kind == "list" {
			key = name + ".member.1"
		}
		form += "&" + key + "=" + url.QueryEscape(iamProbeMemberValue(name, arnValue))
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// loadCasedRequestMembers returns, per operation, the request members the model
// declares — under the exact spelling the SDK puts on the wire, and carrying
// each member's shape kind so the probe can send a list where the API takes a
// list and an object where it takes a structure.
//
// Both halves matter to the measurement. loadRequestFields lower-cases every
// name, which suits a derivation that case-folds its lookup; a derivation that
// reads a named member or form parameter — AWS Identity and Access Management
// reads RoleName, Amazon ECS reads taskDefinition — would see nothing under a
// folded name. And a probe that sends the string "probe" for Amazon ECS's
// services or tasks member never puts a list where the API puts one, so a
// derivation that reads inside it finds nothing and the operation is counted as
// underived while a real request derives it perfectly well.
func loadCasedRequestMembers(t *testing.T, service string) map[string]map[string]string {
	t.Helper()
	path := filepath.Join("..", "specs", "cloud-api", "aws", service+".smithy.json.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v — run scripts/fetch-aws-spec.sh %s", path, err, service)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	defer gz.Close()

	var doc struct {
		Shapes map[string]struct {
			Type    string                  `json:"type"`
			Input   struct{ Target string } `json:"input"`
			Members map[string]struct {
				Target string `json:"target"`
			} `json:"members"`
			Member struct {
				Target string `json:"target"`
			} `json:"member"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	out := map[string]map[string]string{}
	for id, shape := range doc.Shapes {
		if shape.Type != "operation" || shape.Input.Target == "" {
			continue
		}
		named := map[string]string{}
		for name, member := range doc.Shapes[shape.Input.Target].Members {
			kind := "string"
			if target, ok := doc.Shapes[member.Target]; ok {
				switch target.Type {
				case "list":
					// Only a list of strings can carry an identifier a flat
					// probe is able to supply.
					if inner, ok := doc.Shapes[target.Member.Target]; ok && inner.Type == "string" {
						kind = "list"
					}
				case "structure":
					kind = "structure"
				}
			}
			named[name] = kind
		}
		out[id[strings.Index(id, "#")+1:]] = named
	}
	if len(out) == 0 {
		t.Fatalf("%s declares no operations", path)
	}
	return out
}

// iamDerivationCoverageFloor is the number of served operations that both
// authorize against a resource type and derive it from the resource type AWS
// declares. It only rises: an operation whose resource the gate cannot derive
// is authorized against a literal "*", which matches only a policy whose
// Resource is itself "*", so every resource-scoped grant written for it is
// denied. Raising this number is how that defect class is burned down; the test
// prints what is left. What remains, per service, is either the part the
// request genuinely does not name, or a request shape this probe cannot
// express — those are pinned against the real shape instead, because teaching
// the probe about one operation, or filling a field with an ARN because the
// metric wanted one, would be measuring the measurement.
//
// Every service in the measurement is probed. The floor once fell from a
// claimed 1788 to a measured 1687 because five services — AWS Identity and Access
// Management, Amazon CloudWatch Logs, Amazon ECS, AWS CodeBuild and AWS WAFv2 —
// were counted by membership in the generated resource-type table with no probe
// behind them, and membership states only that AWS declares a resource type for
// the action, never that this simulator can read the resource out of the
// request. 101 of those 373 operations do not derive, and the number went down
// when they were measured. That is the honest direction for this ratchet: a
// floor that counts operations nothing exercised overstates how much of the
// surface a resource-scoped grant actually works against, and the overstatement
// is exactly the defect the metric exists to expose. Lowering it here loses no
// derivation — no gate code changed — it only stops crediting derivation nobody
// measured.
//
// Amazon EC2's 56: an operation that creates its resource has no identifier
// for it yet, and CancelImportTask's one identifier could belong to either
// type it authorizes against. The tag operations and the hottest of the
// Disassociate/Detach family derive for real callers now — each tagged id's
// type is stated by its prefix (longest match wins), and a route table,
// address or network-interface association resolves to its parent through a
// generation-keyed index over the simulator's own state — but both stay in
// this count because the probe fills identifiers no prefix map or store can
// answer; TestIAMResourceARNs_EC2TagsDeriveEachIdFromItsPrefix and
// TestIAMResourceARNs_EC2ResolvesAssociationsToTheirParents pin the real
// behaviour. The remaining associations (IAM instance profile, subnet and
// VPC CIDR blocks) still derive nothing.
//
// AWS Glue's 25: the data-quality operations name a result or a run rather
// than the ruleset they authorize against, GetDashboardUrl names a resource
// by a bare id and a separate type member, and the rest create something
// that has no identifier yet. The usage profiles, connection types and
// integrations derive now — a usage profile and a connection type are
// name-addressed, and an integration is named by an ARN-valued
// IntegrationIdentifier — and the tagging operations authorize the
// ResourceArn the caller sends, pinned by
// TestIAMResourceARNs_GlueTaggingTakesTheARNTheRequestNames.
//
// Amazon RDS's 22: the copy operations derive both of their ends now — the
// target's ARN is fully determined by the name the request supplies before
// the resource exists, the same argument that derives an AWS Step Functions
// create, and an ARN-named cross-region source is authorized as sent —
// pinned by TestIAMResourceARNs_RDSCopyAuthorizesSourceAndTarget. The
// custom-engine-version creates still need an identifier the request does
// not carry. The tagging and maintenance operations are counted
// here too, but only because the probe fills every field with a placeholder:
// they name their resource by ARN outright, which a real caller supplies and
// the gate does read — TestIAMResourceARNs_RDSTakesTheARNTheRequestNames pins
// that.
//
// Amazon DynamoDB's 18: fourteen name their resource by ARN outright — a
// backup, an export, an import, the export and import family's TableArn, and
// the tagging and resource-policy operations' ResourceArn — which a real
// caller supplies and the gate reads, pinned by
// TestIAMResourceARNs_DynamoDBTakesTheARNTheRequestNames; the probe fills a
// placeholder. The two batches carry their tables per item inside
// RequestItems, a nested shape the flat probe cannot express, which the
// per-request reader derives for real calls. ImportTable creates a resource
// that has no ARN yet, and RestoreTableToPointInTime names a source and a
// target table that does not exist yet.
//
// AWS Systems Manager's 16: the tagging operations name a resource by a bare
// identifier plus a separate ResourceType member rather than by ARN, the
// creates have no identifier yet, and the remainder are scoped by a path or an
// operating system rather than by a resource.
//
// AWS CloudTrail's 7: the resource-policy operations name a ResourceArn a
// real caller supplies where the probe fills a placeholder, the creates have
// no identifier yet, StartQuery names its event data store only inside the SQL
// statement, and ListInsightsData filters by dimensions rather than naming a
// store. The tagging operations' ARN-valued ResourceId and ResourceIdList are
// read now.
//
// Amazon ElastiCache's 4: the tagging operations name their target by an
// ARN-valued ResourceName a real caller supplies where the probe fills a
// placeholder, and CreateGlobalReplicationGroup names a global datastore
// whose id AWS completes with an assigned prefix no request carries. The
// copy operations derive both of their ends now, like Amazon RDS's.
//
// Amazon EC2 Auto Scaling's 3: the tagging operations carry each target inside
// a nested tag entry rather than under a member of its own, and the two
// instance operations name an instance whose group the request does not carry.
// Amazon CloudWatch's 4: three are metric operations the reference associates
// with a dataset while the request names no dataset, and ListAlarmMuteRules
// filters mute rules by the alarm they belong to rather than naming one.
// Elastic Load Balancing's 4: three create an object that has no assigned
// identifier yet, and SetRulePriorities carries each rule's ARN inside a
// priority entry — a nested shape pinned by
// TestIAMResourceARNs_ELBReadsARuleARNNestedInAPriority.
// Amazon EventBridge's 4: the tagging operations name a ResourceARN a real
// caller supplies where the probe fills a placeholder, and PutEvents derives
// now — each entry names the bus it writes to, and every distinct bus a batch
// targets is authorized, the same way each item of an Amazon DynamoDB
// transaction is authorized against its own table. It still measures as
// underived because the probe sends a list member as a list of strings while
// Entries takes objects;
// TestIAMResourceARNs_EventBridgePutEventsNamesItsBuses pins the real
// behaviour. The three left — AllowVendedLogDeliveryForResource,
// InvokeApiDestination and RetrieveConnectionCredentials — declare no request
// members at all.
//
// AWS Budgets' 1: creating a budget action names a budget while the action's
// own ActionId is a UUID AWS assigns, so the create carries nothing to
// assemble the action ARN from. The rest of the action family derives —
// budget plus ActionId fill the published global format, pinned by
// TestIAMResourceARNs_BudgetsAssemblesTheGlobalActionARN — and the tagging
// operations name their target by an ARN the generic reader resolves. Its
// ARNs carry no region — AWS Budgets is global — so the probe supplies one
// of that shape.
//
// The probe sends each member under its own wire name, in its own case. It
// lower-cased them once, which is a body no client sends: the production
// derivation reads the real member name, so an operation whose resource is
// named by a `ResourceARN` derived for every real caller and measured as
// deriving nothing. Any note below that blames "a placeholder the probe fills"
// should be re-checked against that before it is believed.
// AWS Step Functions' 1: creating an alias names the alias, while the type the
// call authorizes against is the state machine the alias points at, which the
// request carries only inside a routing entry's version ARN. Creating a state
// machine or an activity derives instead of falling back — both ARN formats
// end in the name the create request supplies and carry nothing AWS assigns,
// so the ARN is fully determined before the resource exists.
// AWS Organizations' 2: CreatePolicy names a policy that does not exist yet,
// and DescribeEffectivePolicy takes a target that may be a root, an
// organizational unit or an account while the reference declares only the
// account for it — so the two other spellings have no declared type to
// authorize against and derive nothing. The probe gives every member that
// accepts several types the organizational unit, uniformly; picking the
// account for that one member would raise the count by one without changing
// what the gate does for the other two spellings.
// AWS Security Token Service's 1: AssumeRoot names the member account whose
// root user the call becomes as a bare twelve-digit account id, which the
// probe fills with a placeholder no account can be read from —
// TestIAMResourceARNs_STSNamesTheIdentityEachCallIsAbout pins the real shape.
//
// AWS Identity and Access Management's 21: creating an OpenID Connect or SAML
// provider, a service-linked role or a delegation request names something that
// does not exist yet, and the remainder are scoped to the caller
// (ChangePassword), to an access key, or to a report the call is about to
// generate rather than to an IAM resource. Everything named by RoleName,
// UserName, GroupName, PolicyArn or a virtual MFA device's serial number
// derives — TestIAMResourceARNs_IAMIsGlobalAndCarriesNoRegion pins the shape,
// and the ARNs carry no region because IAM is global.
//
// Amazon CloudWatch Logs' 3: the derivation reads a log group and a log
// stream, and now also the families that authorize against a resource type of
// their own — delivery, delivery-destination, delivery-source, destination,
// anomaly-detector, lookup-table and scheduled-query. Every one of their ARNs
// is "<type>:<name>" over a name, id or ARN the request already carries, in
// the format the service reference publishes, and the declared type selects
// which member is read because several of them spell their identifier "name"
// or "id". TestIAMResourceARNs_CloudWatchLogs pins each one's exact ARN, and
// pins that a log-group request is unchanged by them.
//
// The three left name nothing that resolves: GetLogRecord carries an opaque
// record pointer, GetQueryResults a query id, and ListLogAnomalyDetectors
// filters detectors by a log group rather than naming a detector.
//
// Amazon ECS's 8: the daemon family and the Amazon ECS Express Mode operations
// derive now. Most of them name the resource by its own ARN — a daemon, a
// daemon deployment or revision, an Express Mode service, a service deployment
// or revision — and the member is chosen by the type the operation authorizes
// against rather than by whichever ARN the body carries, because CreateDaemon
// carries the task definition's ARN alongside the cluster and name its own ARN
// is built from. Taking the wrong one would let a policy scoped to a task
// definition permit creating a daemon; TestIAMResourceARNs_ECSDaemonAndExpressMode
// pins that case specifically, and the cluster reader learned clusterArn, which
// is how this family spells it.
//
// The eight left: CreateTaskSet and RegisterDaemonTaskDefinition name something
// with no identifier yet, and Poll and StartTelemetrySession carry no members
// at all. PutAttributes and DeleteAttributes are counted among them but do
// derive for a real caller — each attribute carries the container instance it
// is about as its targetId, and TestIAMResourceARNs_ECSAttributesNameTheirContainerInstance
// pins that. The probe cannot express it, because it sends a list member as a
// list of strings and these take a list of objects; the same shape of
// measurement gap the Amazon RDS tagging operations are recorded under.
//
// AWS CodeBuild's 23: the build-scoped operations authorize against the project
// that owns the build, which the derivation reads out of the build id's
// "<projectName>:<uuid>" shape — a shape the probe's flat placeholder is not,
// and which TestIAMResourceARNs_CodeBuild pins against the real one. The
// remainder name a report, a sandbox, a command execution or a fleet by an
// identifier AWS assigns, or delete a report group by an ARN a real caller
// supplies where the probe fills a placeholder.
//
// AWS WAFv2's 7: its ARNs carry the resource's generated id and its scope path,
// which the derivation assembles from the operation's own suffix plus the
// request's Name, Scope and Id — pinned by TestIAMResourceARNs_WAFv2. The seven
// name a Firewall Manager rule group, a logging configuration, a managed rule
// set or a sample of requests, none of which carries that triple.
//
// Amazon SQS and AWS Cloud Map derive completely: CancelMessageMoveTask
// resolves its opaque task handle through the move-task record to the source
// queue it authorizes against, and GetOperation resolves its operation id
// through the operation record to the namespace and service the operation
// acted on — the simulator's own state, the same resolution Amazon RDS uses
// for a custom engine version.
// AWS Systems Manager's tagging operations name their own resource type, and
// that type selects the ARN format its ResourceId fills — without it a bare
// identifier would fill all eleven types those actions declare, authorizing
// against ten resources the request is not about. They still measure as
// underived because the probe fills ResourceType with a placeholder and
// "probe" is not a ResourceTypeForTagging value;
// TestIAMResourceARNs_SSMTaggingNamesTheTypeItTags pins all ten real values,
// and pins that a type the service does not declare derives nothing. This is
// the same measurement gap as the Amazon ECS attribute operations and the
// Amazon RDS tagging family: real callers derive, the probe cannot say so.
//
// Raised from 1792 by closing one instance of exactly that gap. A member
// naming an AWS account carries twelve digits and nothing else — the service
// refuses any other shape before authorization runs — so filling it with
// "probe" addressed sts:AssumeRoot at an input no client sends, and the
// derivation that reads TargetPrincipal correctly declined to build an ARN
// from it. The probe sends an account id there now.
//
// Finding it took removing a duplicate: the query-protocol probe kept its own
// copy of the rule deciding what a member carries, so the account rule added
// to iamProbeMemberValue did not reach the query services at all. There is one
// copy now, which is why the Amazon SQS queue-URL case moved with it.
//
// Raised again, from 1793 to 1797, by collapsing five copies of the rule that
// decides what a probe puts in a request member into one. Each probe path had
// its own copy, so the account rule above reached only the services that went
// through one of them — and Systems Manager kept filling ResourceType with a
// placeholder no ResourceTypeForTagging accepts, which is the gap the previous
// paragraph describes. Naming a real type there took ssm from 15 underived to
// 11. The single rule is service-aware because that is what the case needs: a
// member can only be filled correctly if you know whose member it is.
const iamDerivationCoverageFloor = 1797

// TestIAMResourceDerivationCoverage measures how much of the simulator's served
// surface authorizes against a real resource rather than the "*" fallback, and
// ratchets it. The report names the services still falling back so the next
// increment is a decision about which service to derive, not a rediscovery of
// the gap.
func TestIAMResourceDerivationCoverage(t *testing.T) {
	refs := loadServiceReferences(t)
	_, jsonRouter, queryRouter := buildConformanceSimulator(t)

	type op struct{ service, name string }
	served := map[op]bool{}
	for _, target := range jsonRouter.Targets() {
		if service, name, ok := iamServiceForServedOperation(t, refs, target, "", ""); ok {
			served[op{service, name}] = true
		}
	}
	for version, actions := range queryRouter.VersionedActions() {
		for _, action := range actions {
			if service, name, ok := iamServiceForServedOperation(t, refs, "", version, action); ok {
				served[op{service, name}] = true
			}
		}
	}

	// Amazon EC2 is measured against the request rather than the table. Its
	// derivation is generated for all 112 resource types at once, so table
	// membership would count an operation that creates its resource — and so
	// carries no identifier for it — as covered. The other table-driven
	// services read hand-listed field spellings, for which membership is the
	// only statement available.
	ec2Parameters := loadEC2RequestParameters(t)
	glueMembers, glueNested := loadRequestShapes(t, "glue", memberWireName)
	rdsParameters := loadRDSRequestParameters(t)
	ssmMembers := loadSSMRequestMembers(t)
	elastiCacheParameters := loadElastiCacheRequestParameters(t)
	dynamoDBMembers := loadDynamoDBRequestMembers(t)
	cloudTrailMembers := loadCloudTrailRequestMembers(t)
	autoScalingParameters := loadRequestFields(t, "auto-scaling", memberWireName)
	kmsMembers := loadKMSRequestMembers(t)
	eventBridgeMembers := loadEventBridgeRequestMembers(t)
	organizationsMembers := loadOrganizationsRequestShapes(t)
	elbParameters := loadRequestFields(t, "elastic-load-balancing-v2", memberWireName)
	acmMembers := loadRequestFields(t, "acm", memberWireName)
	cloudWatchMembers := loadRequestFields(t, "cloudwatch", memberWireName)
	ecrMembers := loadRequestFields(t, "ecr", memberWireName)
	kinesisMembers := loadRequestFields(t, "kinesis", memberWireName)
	statesMembers := loadRequestFields(t, "sfn", memberWireName)
	secretsMembers := loadRequestFields(t, "secrets-manager", memberWireName)
	snsParameters := loadRequestFields(t, "sns", memberWireName)
	sqsParameters := loadRequestFields(t, "sqs", memberWireName)
	acmPCAMembers := loadRequestFields(t, "acm-pca", memberWireName)
	cloudMapMembers := loadRequestFields(t, "servicediscovery", memberWireName)
	firehoseMembers := loadRequestFields(t, "firehose", memberWireName)
	budgetsMembers := loadRequestFields(t, "budgets", memberWireName)
	stsParameters := loadRequestFields(t, "sts", memberWireName)
	appAutoScalingMembers := loadRequestFields(t, "application-auto-scaling", memberWireName)
	_, ecsNested := loadRequestShapes(t, "ecs", memberWireName)
	_, logsNested := loadRequestShapes(t, "cloudwatch-logs", memberWireName)
	_, codeBuildNested := loadRequestShapes(t, "codebuild", memberWireName)
	_, wafv2Nested := loadRequestShapes(t, "wafv2", memberWireName)
	ecsMembers := loadCasedRequestMembers(t, "ecs")
	logsMembers := loadCasedRequestMembers(t, "cloudwatch-logs")
	codeBuildMembers := loadCasedRequestMembers(t, "codebuild")
	wafv2Members := loadCasedRequestMembers(t, "wafv2")
	iamMembers := loadCasedRequestMembers(t, "iam")
	iamCloudMapProbeState()
	iamSQSProbeState()
	organizationsIDs := iamOrganizationsProbeState()

	covered := 0
	missingByService := map[string][]string{}
	for o := range served {
		ref, ok := refs[o.service]
		if !ok {
			continue
		}
		types, defined := ref.resourceTypes(o.name)
		if !defined || len(types) == 0 {
			continue // AWS declares no resource type: "*" is the correct request
		}
		_, derived := iamActionResourceTypes[o.service+":"+o.name]
		switch o.service {
		case "ec2":
			derived = iamEC2DerivesItsResource(o.name, ec2Parameters[o.name])
		case "glue":
			derived = iamGlueDerivesItsResource(o.name, glueMembers[o.name], glueNested[o.name])
		case "rds":
			derived = iamRDSDerivesItsResource(o.name, rdsParameters[o.name])
		case "ssm":
			derived = iamSSMDerivesItsResource(o.name, ssmMembers[o.name])
		case "elasticache":
			derived = iamElastiCacheDerivesItsResource(o.name, elastiCacheParameters[o.name])
		case "dynamodb":
			derived = iamDynamoDBDerivesItsResource(o.name, dynamoDBMembers[o.name])
		case "cloudtrail":
			derived = iamCloudTrailDerivesItsResource(o.name, cloudTrailMembers[o.name])
		case "autoscaling":
			derived = iamAutoScalingDerivesItsResource(o.name, autoScalingParameters[o.name])
		case "kms":
			derived = iamKMSDerivesItsResource(o.name, kmsMembers[o.name])
		case "events":
			derived = iamEventBridgeDerivesItsResource(o.name, eventBridgeMembers[o.name])
		case "organizations":
			derived = iamOrganizationsDerivesItsResource(o.name, organizationsMembers[o.name], organizationsIDs)
		case "elasticloadbalancing":
			derived = iamELBv2DerivesItsResource(o.name, elbParameters[o.name])
		case "acm":
			derived = iamACMDerivesItsResource(o.name, acmMembers[o.name])
		case "cloudwatch":
			derived = iamCloudWatchDerivesItsResource(o.name, cloudWatchMembers[o.name])
		case "ecr":
			derived = iamJSONProbeDerives("ecr", o.name, ecrMembers[o.name],
				"arn:aws:ecr:us-east-1:123456789012:repository/probe")
		case "kinesis":
			derived = iamJSONProbeDerives("kinesis", o.name, kinesisMembers[o.name],
				"arn:aws:kinesis:us-east-1:123456789012:stream/probe")
		case "states":
			derived = iamJSONProbeDerives("states", o.name, statesMembers[o.name],
				"arn:aws:states:us-east-1:123456789012:stateMachine:probe")
		case "secretsmanager":
			derived = iamJSONProbeDerives("secretsmanager", o.name, secretsMembers[o.name],
				"arn:aws:secretsmanager:us-east-1:123456789012:secret:probe")
		case "sns":
			derived = iamQueryProbeDerives("sns", o.name, "2010-03-31", snsParameters[o.name])
		case "sqs":
			derived = iamQueryProbeDerives("sqs", o.name, "2012-11-05", sqsParameters[o.name])
		case "acm-pca":
			derived = iamJSONProbeDerives("acm-pca", o.name, acmPCAMembers[o.name],
				"arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/0123abcd-ef45-6789-abcd-ef0123456789")
		case "servicediscovery":
			derived = iamJSONProbeDerives("servicediscovery", o.name, cloudMapMembers[o.name],
				"arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-probe")
		case "firehose":
			derived = iamJSONProbeDerives("firehose", o.name, firehoseMembers[o.name],
				"arn:aws:firehose:us-east-1:123456789012:deliverystream/probe")
		case "budgets":
			// A budget ARN carries no region: AWS Budgets is a global service,
			// and its own reference spells the resource "budget/${BudgetName}".
			derived = iamJSONProbeDerives("budgets", o.name, budgetsMembers[o.name],
				"arn:aws:budgets::123456789012:budget/probe")
		case "sts":
			derived = iamQueryProbeDerives("sts", o.name, "2011-06-15", stsParameters[o.name])
		case "application-autoscaling":
			derived = iamJSONProbeDerives("application-autoscaling", o.name, appAutoScalingMembers[o.name],
				"arn:aws:application-autoscaling:us-east-1:123456789012:scalable-target/probe")
		case "ecs":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"AmazonEC2ContainerServiceV20141113", o.name,
				"arn:aws:ecs:us-east-1:123456789012:cluster/probe",
				ecsMembers[o.name], ecsNested[o.name]), "ecs:"+o.name)
		case "logs":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"Logs_20140328", o.name,
				"arn:aws:logs:us-east-1:123456789012:log-group:probe",
				logsMembers[o.name], logsNested[o.name]), "logs:"+o.name)
		case "codebuild":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"CodeBuild_20161006", o.name,
				"arn:aws:codebuild:us-east-1:123456789012:project/probe",
				codeBuildMembers[o.name], codeBuildNested[o.name]), "codebuild:"+o.name)
		case "wafv2":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"AWSWAF_20190729", o.name,
				"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/probe/0123",
				wafv2Members[o.name], wafv2Nested[o.name]), "wafv2:"+o.name)
		case "iam":
			derived = iamProductionProbeDerives(iamAWSQueryProbeRequest(
				o.name, "2010-05-08", "arn:aws:iam::123456789012:policy/probe",
				iamMembers[o.name]), "iam:"+o.name)
		}
		if derived {
			covered++
			continue
		}
		missingByService[o.service] = append(missingByService[o.service], o.name)
	}

	total := covered
	for _, ops := range missingByService {
		total += len(ops)
	}
	services := make([]string, 0, len(missingByService))
	for service := range missingByService {
		services = append(services, service)
	}
	sort.Slice(services, func(i, j int) bool {
		if len(missingByService[services[i]]) != len(missingByService[services[j]]) {
			return len(missingByService[services[i]]) > len(missingByService[services[j]])
		}
		return services[i] < services[j]
	})
	var report strings.Builder
	fmt.Fprintf(&report, "resource-scoped authorization: %d of %d served operations derive their resource\n",
		covered, total)
	for _, service := range services {
		note := ""
		if iamHandwrittenDerivationServices[service] {
			note = "  (per-request case in iamResourceARNsForRequest)"
		}
		fmt.Fprintf(&report, "  %-24s %3d operations not derived from the declared type%s\n",
			service, len(missingByService[service]), note)
		if os.Getenv("IAM_DERIVATION_LIST_MISSING") != "" {
			sort.Strings(missingByService[service])
			for _, op := range missingByService[service] {
				fmt.Fprintf(&report, "      %s\n", op)
			}
		}
	}
	t.Log(report.String())

	if covered < iamDerivationCoverageFloor {
		t.Fatalf("resource-derivation coverage fell from %d to %d served operations — "+
			"a service lost its derivation, which denies every resource-scoped grant written for it",
			iamDerivationCoverageFloor, covered)
	}
	if covered > iamDerivationCoverageFloor {
		t.Fatalf("resource-derivation coverage rose from %d to %d served operations — "+
			"raise iamDerivationCoverageFloor to %d to hold the gain",
			iamDerivationCoverageFloor, covered, covered)
	}
}
