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

// The call-time gate authorizes a request against the resource it names. A
// wrong resource is invisible to a caller whose policy says Resource "*" and
// denies only a caller with a resource-scoped grant. Bind the gate to the data
// AWS publishes about which resource each action authorizes against, so an
// underived service is a counted, failing number.

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
// vendored model declares on operations that authorize against the aliased type.
// An alias that is a guess, a typo or a dropped member derives nothing, which a
// caller whose policy says Resource "*" never sees.
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
// declares and, for a structure member, the fields nested inside it: a probe
// that sends a string for every member never exercises a derivation that reads
// inside a structure.
func loadRequestShapes(t *testing.T, service string, wireName func(member string, traits map[string]string) string) (map[string]map[string]bool, map[string]map[string]map[string]string) {
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
			Member  member                  `json:"member"`
			Value   member                  `json:"value"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	fields := map[string]map[string]bool{}
	nested := map[string]map[string]map[string]string{}
	for id, shape := range doc.Shapes {
		if shape.Type != "operation" || shape.Input.Target == "" {
			continue
		}
		operation := id[strings.Index(id, "#")+1:]
		named := map[string]bool{}
		inner := map[string]map[string]string{}
		for name, m := range doc.Shapes[shape.Input.Target].Members {
			traits := map[string]string{}
			for trait, raw := range m.Traits {
				var v string
				if json.Unmarshal(raw, &v) == nil {
					traits[trait] = v
				}
			}
			// The member's wire name in its own case: the production derivation reads the real member name.
			wire := wireName(name, traits)
			named[wire] = true
			// The members a probe fills inside this one: a structure's own, and
			// for a list or a map, those of the element it holds.
			if target, ok := doc.Shapes[m.Target]; ok {
				element := target
				switch target.Type {
				case "list":
					element = doc.Shapes[target.Member.Target]
				case "map":
					element = doc.Shapes[target.Value.Target]
				}
				if element.Type == "structure" {
					members := map[string]string{}
					for innerName, innerMember := range element.Members {
						// The inner member's own kind, so a list inside a
						// structure is sent as the array a client sends —
						// filling it with a scalar is a body nothing accepts.
						kind := "string"
						if target, known := doc.Shapes[innerMember.Target]; known && target.Type == "list" {
							kind = "list"
						}
						members[innerName] = kind
					}
					inner[wire] = members
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

// iamProbeARN is the ARN a probe puts in an ARN-valued member: the action's own
// declared type where the reference publishes a format for it, otherwise the
// caller's service-wide ARN.
func iamProbeARN(service, operation, serviceWide string) string {
	if arn := iamProbeARNForAction(service, operation); arn != "" {
		return arn
	}
	return serviceWide
}

// iamQueryProbeForm renders a query-protocol request body the way a client
// does. The query protocol boxes a list in ".member.N" and puts a structure's
// members after a dot, so an operation naming its resource inside a list entry
// — Elastic Load Balancing's SetRulePriorities carries each rule's ARN as
// RulePriorities.member.1.RuleArn — is reachable only when the probe spells it
// that way. A flat member of the same name is a body no client sends.
func iamQueryProbeForm(service, operation, version, arnValue string,
	members map[string]string, nested map[string]map[string]string, fixtures map[string]string,
) string {
	form := "Action=" + operation + "&Version=" + version
	add := func(key, value string) {
		form += "&" + key + "=" + url.QueryEscape(value)
	}
	for name, kind := range members {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		value := iamProbeMemberValue(service, name, arnValue)
		// A resource the simulator holds, named the way its own API named it —
		// substituted rather than appended, because a form carrying the member
		// twice is read at its first value.
		if created, seeded := fixtures[service+":"+operation+":"+name]; seeded {
			value = created
		}
		switch kind {
		case "list":
			add(name+".member.1", value)
		case "structure":
			// Flat, deliberately. A structure's members arrive after a dot, and
			// the query parameter reader drops any key with a dot left in it
			// after the index — its contract being members, not paths — so
			// spelling one out puts the value somewhere nothing reads. The
			// member's own name is what the readers match on.
			add(name, value)
		case "list-structure":
			for inner := range nested[name] {
				add(name+".member.1."+inner, iamProbeMemberValue(service, inner, arnValue))
			}
		default:
			add(name, value)
		}
	}
	return form
}

func iamProbeBody(service, arnValue string, members map[string]bool, nested map[string]map[string]string) map[string]any {
	body := make(map[string]any, len(members))
	for name := range members {
		body[name] = iamProbeMemberValue(service, name, arnValue)
	}
	for member, inner := range nested {
		object := make(map[string]any, len(inner))
		for name, kind := range inner {
			if kind == "list" {
				object[name] = []string{iamProbeMemberValue(service, name, arnValue)}
				continue
			}
			object[name] = iamProbeMemberValue(service, name, arnValue)
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

// iamEC2DerivesItsResource runs the production derivation against a request
// carrying every parameter the model declares: if nothing derives from all of
// them, no real request derives anything. An operation that creates its resource
// (CreateInternetGateway) carries no identifier for it and correctly derives
// nothing.
func iamEC2DerivesItsResource(operation string, params map[string]bool, fixtures map[string]string) bool {
	types := iamActionResourceTypes["ec2:"+operation]
	if len(types) == 0 {
		return false
	}
	values := make(map[string]string, len(params))
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue // the request already carries these
		}
		if created, seeded := fixtures["ec2:"+operation+":"+name]; seeded {
			// A resource the simulator holds, named the way its own API named
			// it — which is what the derivation resolves through.
			values[name] = created
			continue
		}
		if name == "InstanceCreditSpecification" {
			// A list of structures, which the query protocol spells out and a
			// flat member cannot express.
			values[name+".1.InstanceId"] = "i-0123456789abcdef0"
			continue
		}
		values[name] = iamProbeMemberValue("ec2", name, iamProbeARN("ec2", operation,
			"arn:aws:ec2:us-east-1:"+iamProbeAccount+":instance/i-0123456789abcdef0"))
	}
	return len(iamDerivedResourceARNs(iamEC2Request(operation, values), "ec2", operation,
		"us-east-1", "123456789012")) > 0
}

// iamGlueDerivesItsResource runs the production derivation against a request
// carrying every member the model declares for the operation, the same way
// iamEC2DerivesItsResource does: if nothing is derived from all of them, no
// real request derives anything.
func iamGlueDerivesItsResource(operation string, members map[string]bool,
	nested map[string]map[string]string, fixtures map[string]string,
) bool {
	types := iamActionResourceTypes["glue:"+operation]
	if len(types) == 0 {
		return false
	}
	// An ARN member gets an ARN of a type the action declares, which is what a
	// client sends: AWS Glue's tagging operations name the resource by ARN alone.
	body := iamProbeBody("glue", iamProbeARNForAction("glue", operation), members, nested)
	for member := range members {
		if created, seeded := fixtures["glue:"+operation+":"+member]; seeded {
			body[member] = created
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "glue", operation, "us-east-1", "123456789012")) > 0
}

// iamRDSDerivesItsResource runs the production derivation against a request
// carrying every parameter the model declares for the operation.
func iamRDSDerivesItsResource(operation string, params map[string]bool, fixtures map[string]string) bool {
	types := iamActionResourceTypes["rds:"+operation]
	if len(types) == 0 {
		return false
	}
	form := "Action=" + operation + "&Version=2014-10-31"
	for name := range params {
		if strings.EqualFold(name, "action") || strings.EqualFold(name, "version") {
			continue
		}
		value := iamProbeMemberValue("rds", name,
			iamProbeARN("rds", operation, "arn:aws:rds:us-east-1:"+iamProbeAccount+":db:probe"))
		if created, seeded := fixtures["rds:"+operation+":"+name]; seeded {
			value = created
		}
		form += "&" + name + "=" + url.QueryEscape(value)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, "rds", operation, "us-east-1", "123456789012")) > 0
}

// TestIAMRDSFieldAliasesAreRealRequestParameters holds every Amazon RDS alias to
// a parameter the vendored model declares on an operation that authorizes
// against the aliased type. RDS needs an alias for almost every resource type
// (${DbInstanceName} arrives as DBInstanceIdentifier).
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
		body[name] = iamProbeMemberValue("kms", name,
			iamProbeARN("kms", operation, "arn:aws:kms:us-east-1:"+iamProbeAccount+":probe"))
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
// and returns each identifier, keyed by the model shape a request member targets
// when it accepts that identifier. Organizations reads a resource's type from
// its identifier, so each takes the shape the model's @pattern requires; the
// service rejects anything else before authorization. A member accepting several
// types (ParentId, PolicyTargetId, TaggableResourceId, ChildId) gets the
// organizational unit, which each admits.
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
		// An effective policy is read for a target, which a client names as an
		// account, an organizational unit or the root.
		"TargetId":           account.Id,
		"TaggableResourceId": ou.Id,
		"ChildId":            ou.Id,
	}
}

// iamOrganizationsDerivesItsResource runs the production derivation against a
// request naming resources that exist, under the members the model declares.
func iamOrganizationsDerivesItsResource(operation string, members map[string]string,
	ids map[string]string, fixtures map[string]string) bool {

	if len(iamActionResourceTypes["organizations:"+operation]) == 0 {
		return false
	}
	body := map[string]any{}
	for path, shape := range members {
		value, ok := ids[shape]
		if !ok {
			value = "probe"
		}
		// A shape shared between operations cannot say what any one of them
		// names: PolicyTargetId is an organizational unit where a policy is
		// attached and an account where an effective policy is read, and only
		// the operation says which.
		if created, seeded := fixtures["organizations:"+operation+":"+path]; seeded {
			value = created
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
func iamELBv2DerivesItsResource(operation string, params map[string]string, nested map[string]map[string]string) bool {
	if len(iamActionResourceTypes["elasticloadbalancing:"+operation]) == 0 {
		return false
	}
	form := iamQueryProbeForm("elasticloadbalancing", operation, "2015-12-01",
		iamProbeARN("elasticloadbalancing", operation,
			"arn:aws:elasticloadbalancing:us-east-1:"+iamProbeAccount+
				":targetgroup/probe/0123456789abcdef"),
		params, nested, nil)
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
		body[name] = iamProbeMemberValue("acm", name,
			iamProbeARN("acm", operation, "arn:aws:acm:us-east-1:"+iamProbeAccount+
				":certificate/0123abcd-ef45-6789-abcd-ef0123456789"))
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
// definition carries one, as a real caller sends.
func iamCloudWatchDerivesItsResource(operation string, members map[string]bool, fixtures map[string]string) bool {
	if len(iamActionResourceTypes["cloudwatch:"+operation]) == 0 {
		return false
	}
	body := make(map[string]string, len(members))
	for name := range members {
		if created, seeded := fixtures["cloudwatch:"+operation+":"+name]; seeded {
			body[name] = created
			continue
		}
		body[name] = iamProbeMemberValue("cloudwatch", name,
			iamProbeARN("cloudwatch", operation, "arn:aws:cloudwatch:us-east-1:"+iamProbeAccount+":alarm:probe"))
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

// iamCloudMapProbeState seeds a namespace and a service named the way the probe
// fills a request: AWS Cloud Map's discovery operations address them by name
// while their ARNs carry assigned ids.
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
		// Use the shared rule for what a member carries.
		value := iamProbeMemberValue(service, name,
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
		body[name] = iamProbeMemberValue(service, name, arnValue)
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
// request naming a group and a launch configuration that exist: Amazon EC2 Auto
// Scaling ARNs carry an assigned id no request supplies, so the derivation reads
// the simulator's state.
func iamAutoScalingDerivesItsResource(operation string, params map[string]string,
	nested map[string]map[string]string, fixtures map[string]string,
) bool {
	if len(iamActionResourceTypes["autoscaling:"+operation]) == 0 {
		return false
	}
	// The tagging calls name their group inside a tag, so the form has to be
	// rendered the way the query protocol spells one.
	form := iamQueryProbeForm("autoscaling", operation, "2011-01-01",
		"arn:aws:autoscaling:us-east-1:"+iamProbeAccount+
			":autoScalingGroup:0123:autoScalingGroupName/probe", params, nested, fixtures)
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
		body[name] = iamProbeMemberValue("cloudtrail", name,
			iamProbeARN("cloudtrail", operation, "arn:aws:cloudtrail:us-east-1:"+iamProbeAccount+":trail/probe"))
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return false
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return len(iamDerivedResourceARNs(r, "cloudtrail", operation, "us-east-1", "123456789012")) > 0
}

func TestIAMSSMFieldAliasesAreRealRequestMembers(t *testing.T) {
	assertAliasesAreRealFields(t, "ssm", iamSSMFieldAliases, loadSSMRequestMembers(t))
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
		form += "&" + name + "=" + url.QueryEscape(iamProbeMemberValue("elasticache", name,
			iamProbeARN("elasticache", operation, "arn:aws:elasticache:us-east-1:"+iamProbeAccount+":cluster:probe")))
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return len(iamDerivedResourceARNs(r, "elasticache", operation, "us-east-1", "123456789012")) > 0
}

// iamHandwrittenDerivationServices are the services a per-service case in
// iamResourceARNsForRequest derives rather than the generated table; list them
// so the report does not read them as underived.
var iamHandwrittenDerivationServices = map[string]bool{
	// Amazon DynamoDB's transactions and batches, whose tables ride per item
	// rather than at the top level.
	"dynamodb": true,
	// AWS Lambda's function name, which its REST path carries.
	"lambda": true,
}

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

// iamProbeARNForAction builds the ARN a client names an action's resource by: the
// action's own declared type, filled from its published format. One ARN per
// service would address wafv2:PutLoggingConfiguration with a web ACL rather than
// a logging configuration. With no published format it returns "" and the
// caller's service-wide ARN stands in.
func iamProbeARNForAction(service, operation string) string {
	for _, resourceType := range iamActionResourceTypes[service+":"+operation] {
		format, declared := iamResourceARNFormats[service+":"+resourceType]
		if !declared {
			continue
		}
		if arn := iamRenderProbeARN(format); arn != "" {
			return arn
		}
	}
	return ""
}

// iamRenderProbeARN fills every variable a format declares, not only the first:
// a WAFv2 web ACL is spelled ${Scope}/webacl/${Name}/${Id}, and an ARN with two
// of those left empty is one no client sends and no reader recognises.
func iamRenderProbeARN(format string) string {
	if !strings.HasPrefix(format, "arn:") {
		return ""
	}
	var out strings.Builder
	rest := format
	for {
		open := strings.Index(rest, "${")
		if open < 0 {
			out.WriteString(rest)
			break
		}
		closing := strings.Index(rest[open:], "}")
		if closing < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:open])
		switch name := rest[open+2 : open+closing]; name {
		case "Partition":
			out.WriteString("aws")
		case "Region":
			out.WriteString("us-east-1")
		case "Account":
			out.WriteString(iamProbeAccount)
		case "Scope":
			// WAFv2's scope is one of two words the service defines, and a
			// client sends one of them.
			out.WriteString("regional")
		default:
			out.WriteString("probe")
		}
		rest = rest[open+closing+1:]
	}
	return out.String()
}

func iamProbeMemberValue(service, name, arnValue string) string {
	lower := strings.ToLower(name)
	// The query-protocol database services name a resource by ARN under
	// members that do not end in "arn": ResourceName on the tagging
	// operations, ResourceIdentifier on Amazon RDS's maintenance ones.
	if (service == "rds" || service == "elasticache") &&
		(lower == "resourcename" || lower == "resourceidentifier") {
		return arnValue
	}
	if service == "logs" && lower == "logrecordpointer" {
		// The pointer the service issues, which says where the record is.
		return "probe|probe-stream|0"
	}
	if service == "cloudtrail" && lower == "querystatement" {
		// A Lake query names the store it reads, which is how a client writes
		// one — a bare word there names no store.
		return "SELECT eventName FROM 01234567-89ab-cdef-0123-456789abcdef"
	}
	// AWS CloudTrail's ResourceId and ResourceIdList carry an ARN.
	if service == "cloudtrail" && (lower == "resourceid" || lower == "resourceidlist") {
		return arnValue
	}
	// A service-linked-role deletion task spells the role it is deleting, so a
	// placeholder there names none.
	if service == "iam" && lower == "deletiontaskid" {
		return "task/aws-service-role/ecs.amazonaws.com/AWSServiceRoleForECS/" +
			"11111111-2222-3333-4444-555555555555"
	}
	// A virtual MFA device's serial number is its ARN — that is the identifier
	// IAM assigns and the only one a client has to send — so a placeholder
	// there leaves the derivation nothing to name the device with.
	if service == "iam" && lower == "serialnumber" {
		return "arn:aws:iam::" + iamProbeAccount + ":mfa/probe"
	}
	// An OpenID Connect provider is named by the issuer URL it is created for.
	if service == "iam" && lower == "url" {
		return "https://oidc.probe.example/provider"
	}
	// A load balancer's kind is one of three the service defines, and its ARN
	// carries the kind — so a placeholder names no balancer at all.
	if service == "elasticloadbalancing" && lower == "type" {
		return "application"
	}
	// A CodeBuild report is named by its own ARN, and the group it belongs to
	// is the segment inside it — so a project-shaped ARN there would name no
	// group, and the probe sends a report-shaped one.
	if service == "codebuild" && (lower == "reportarn" || lower == "reportarns") {
		return "arn:aws:codebuild:us-east-1:" + iamProbeAccount +
			":report/probe:11111111-2222-3333-4444-555555555555"
	}
	// An AWS CodeBuild id carries its parent before a colon
	// ("<projectName>:<uuid>"), the resource CodeBuild authorizes against.
	if service == "codebuild" && (lower == "id" || lower == "ids") {
		return "probe:11111111-2222-3333-4444-555555555555"
	}
	if service == "ec2" && lower == "importtaskid" {
		// An import task id says which kind of import it is, and a client
		// cancelling one sends the id the import returned.
		return "import-ami-0123456789abcdef0"
	}
	// An Amazon EC2 id states its type in its prefix, which the derivation
	// reads to pick among the hundred-odd types CreateTags declares.
	if service == "ec2" && lower == "resourceid" {
		return "i-0123456789abcdef0"
	}
	if service == "ssm" && lower == "key" {
		// A target's key says what its values select, and a machine is what a
		// just-in-time access request selects.
		return "InstanceIds"
	}
	// A Systems Manager instance id is an Amazon EC2 "i-" or a managed "mi-"
	// id; the prefix tells the two apart.
	if service == "ssm" && (lower == "instanceid" || lower == "instanceids" ||
		lower == "target" || lower == "values") {
		return "i-0123456789abcdef0"
	}
	if service == "glue" && lower == "resourcetype" {
		// The kind of thing a dashboard is asked for, as the caller names it.
		return "session"
	}
	if service == "autoscaling" && lower == "resourcetype" {
		// The only kind of resource Auto Scaling tags.
		return "auto-scaling-group"
	}
	if service == "ssm" && lower == "resourcetype" {
		// Any real member of the enum will do: the test pins all ten, and what
		// matters here is that the probe names one the service declares rather
		// than a placeholder it rejects.
		return "Parameter"
	}
	// A member holding one ARN or a list of them is spelled either way.
	if strings.HasSuffix(lower, "arn") || strings.HasSuffix(lower, "arns") {
		return arnValue
	}
	// A member naming an AWS account carries twelve digits: the service
	// rejects any other shape before authorization runs.
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
	service, target, operation, arnValue string, members map[string]string,
	nested map[string]map[string]string, fixtures map[string]string,
) *http.Request {
	// Pass the service rather than read it off the wire target: "AmazonSSM" carries no date to split on.
	return iamAWSJSONProbeRequestFor(service, target, operation, arnValue, members, nested, fixtures)
}

func iamAWSJSONProbeRequestFor(
	service, target, operation, arnValue string, members map[string]string,
	nested map[string]map[string]string, fixtures map[string]string,
) *http.Request {
	body := make(map[string]any, len(members))
	element := func(name string) map[string]any {
		object := make(map[string]any, len(nested[name]))
		for inner, kind := range nested[name] {
			value := iamProbeMemberValue(service, inner, arnValue)
			if kind == "list" {
				// A list inside a structure arrives as the array a client
				// sends: a scalar there is a body nothing accepts, and a
				// reader that rightly refuses it measures as absent.
				object[inner] = []string{value}
				continue
			}
			object[inner] = value
		}
		return object
	}
	for name, kind := range members {
		value := iamProbeMemberValue(service, name, arnValue)
		// A resource the simulator holds, named the way its own API named it.
		if created, seeded := fixtures[service+":"+operation+":"+name]; seeded {
			body[name] = created
			continue
		}
		switch kind {
		case "list":
			body[name] = []string{value}
		case "structure":
			body[name] = element(name)
		case "list-structure":
			// One element, filled the way a client fills it: the identifier a
			// batch is about sits inside the element, not beside it.
			body[name] = []any{element(name)}
		case "map":
			// The key is the identifier — DynamoDB's RequestItems is keyed by
			// table name — so it is filled the way any other member is, and
			// the value carries the element's own members.
			body[name] = map[string]any{value: element(name)}
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
func iamAWSQueryProbeRequest(service, operation, version, arnValue string,
	members map[string]string, fixtures map[string]string,
) *http.Request {
	form := "Action=" + operation + "&Version=" + version
	for name, kind := range members {
		if name == "Action" || name == "Version" {
			continue
		}
		key := name
		if kind == "list" {
			key = name + ".member.1"
		}
		value := iamProbeMemberValue(service, name, arnValue)
		if created, seeded := fixtures[service+":"+operation+":"+name]; seeded {
			value = created
		}
		form += "&" + key + "=" + url.QueryEscape(value)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// A real caller signs as a principal the service holds, and an operation
	// authorized against the caller — iam:ChangePassword — can be derived only
	// from one. The signature itself is verified before the derivation ever
	// runs, so what matters here is that the key names a principal.
	if caller, seeded := fixtures["caller:AccessKeyId"]; seeded {
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+caller+
			"/20260801/us-east-1/"+service+"/aws4_request, SignedHeaders=host, Signature=00")
	}
	return r
}

// loadCasedRequestMembers returns, per operation, the request members under the
// exact spelling the SDK puts on the wire, with each member's shape kind, so the
// probe sends a list where the API takes one and an object where it takes a
// structure. A derivation that reads a named member (IAM RoleName, Amazon ECS
// taskDefinition) sees nothing under a folded name.
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
			Value struct {
				Target string `json:"target"`
			} `json:"value"`
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
					if inner, ok := doc.Shapes[target.Member.Target]; ok {
						switch inner.Type {
						case "string":
							kind = "list"
						case "structure":
							// A list of structures is how a service spells a batch, with the identifier
							// inside each element: Amazon ECS attributes[].targetId, EventBridge
							// entries[].EventBusName.
							kind = "list-structure"
						}
					}
				case "structure":
					kind = "structure"
				case "map":
					// A map keyed by the resource: DynamoDB's RequestItems is
					// keyed by table name, so the identifier is the key rather
					// than anything under it.
					kind = "map"
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
// authorize against a resource type and derive it. It only rises: an underived
// operation authorizes against a literal "*", which denies every resource-scoped
// grant written for it. Every service in the measurement is probed, since table
// membership says only that AWS declares a type, not that the simulator reads the
// resource from the request.
//
// What remains is either a resource the request genuinely does not name, or a
// shape this probe cannot express, which a TestIAMResourceARNs_* test pins
// against the real shape instead. The eight operations that name no resource
// (AWS Glue's GetMLTransforms, ListMLTransforms, ListDataQualityResults,
// ListDataQualityRuleRecommendationRuns and ListIntegrationResourceProperties;
// Amazon CloudWatch's GetMetricData, ListMetrics and PutMetricData) carry only
// filters, page tokens and data. They authorize against "*", as in AWS, which
// scopes the CloudWatch metric operations with cloudwatch:namespace instead.
// Deriving a resource for them would invent an association AWS's model does not
// state.
const iamDerivationCoverageFloor = 2023

// TestIAMResourceDerivationCoverage measures how much of the simulator's served
// surface authorizes against a real resource rather than the "*" fallback, and
// ratchets it. The report names the services still falling back so the next
// increment is a decision about which service to derive, not a rediscovery of
// the gap.
func TestIAMResourceDerivationCoverage(t *testing.T) {
	refs := loadServiceReferences(t)
	_, jsonRouter, queryRouter := buildConformanceSimulator(t)
	// The resources the state-resolving derivations look up, created the way a
	// client creates them so the probe addresses something that exists.
	fixtures := iamSeedDerivationFixtures(t, queryRouter, jsonRouter)

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
	// AWS Systems Manager goes through the shared probe like every other
	// awsJson service: CreateAssociationBatch names each document and machine
	// inside a batch entry, a shape only the shared renderer sends.
	_, ssmNestedShapes := loadRequestShapes(t, "ssm", memberWireName)
	ssmMembers := loadCasedRequestMembers(t, "ssm")
	elastiCacheParameters := loadElastiCacheRequestParameters(t)
	// Amazon DynamoDB goes through the shared probe: RequestItems is a map keyed by
	// table name, which only the shared renderer sends.
	_, dynamoDBNested := loadRequestShapes(t, "dynamodb", memberWireName)
	dynamoDBMembers := loadCasedRequestMembers(t, "dynamodb")
	cloudTrailMembers := loadCloudTrailRequestMembers(t)
	// The tagging calls name their group inside a tag, which needs the member
	// kinds and the tag's own members.
	_, autoScalingNested := loadRequestShapes(t, "auto-scaling", memberWireName)
	autoScalingCasedParameters := loadCasedRequestMembers(t, "auto-scaling")
	kmsMembers := loadKMSRequestMembers(t)
	// Amazon EventBridge goes through the shared probe: PutEvents names each event's
	// bus under entries[].EventBusName.
	_, eventBridgeNested := loadRequestShapes(t, "eventbridge", memberWireName)
	eventBridgeMembers := loadCasedRequestMembers(t, "eventbridge")
	organizationsMembers := loadOrganizationsRequestShapes(t)
	// Elastic Load Balancing needs the member kinds and the element members
	// beside them: SetRulePriorities names each rule inside a list entry.
	_, elbNested := loadRequestShapes(t, "elastic-load-balancing-v2", memberWireName)
	elbParameters := loadCasedRequestMembers(t, "elastic-load-balancing-v2")
	acmMembers := loadRequestFields(t, "acm", memberWireName)
	cloudWatchMembers := loadRequestFields(t, "cloudwatch", memberWireName)
	ecrMembers := loadRequestFields(t, "ecr", memberWireName)
	kinesisMembers := loadRequestFields(t, "kinesis", memberWireName)
	// An alias names the machine it routes to inside its routing entries, a
	// shape only the shared renderer sends.
	_, statesNested := loadRequestShapes(t, "sfn", memberWireName)
	statesCased := loadCasedRequestMembers(t, "sfn")
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
	// An effective policy is read for an account, which is the only type the
	// action declares — the shape its target member carries is shared with the
	// attachments, where it is an organizational unit.
	fixtures["organizations:DescribeEffectivePolicy:TargetId"] = "123456789012"

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
		// A real client names the resource the action is about, so the probe
		// does too: the action's own declared type where the reference
		// publishes a format for it, and the service-wide ARN only where it
		// does not.
		probeARN := func(serviceWide string) string {
			if arn := iamProbeARNForAction(o.service, o.name); arn != "" {
				return arn
			}
			return serviceWide
		}
		switch o.service {
		case "ec2":
			derived = iamEC2DerivesItsResource(o.name, ec2Parameters[o.name], fixtures)
		case "glue":
			derived = iamGlueDerivesItsResource(o.name, glueMembers[o.name], glueNested[o.name], fixtures)
		case "rds":
			derived = iamRDSDerivesItsResource(o.name, rdsParameters[o.name], fixtures)
		case "ssm":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"ssm", "AmazonSSM", o.name,
				probeARN("arn:aws:ssm:us-east-1:"+iamProbeAccount+":probe"),
				ssmMembers[o.name], ssmNestedShapes[o.name], fixtures), "ssm:"+o.name)
		case "elasticache":
			derived = iamElastiCacheDerivesItsResource(o.name, elastiCacheParameters[o.name])
		case "dynamodb":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"dynamodb", "DynamoDB_20120810", o.name,
				probeARN("arn:aws:dynamodb:us-east-1:"+iamProbeAccount+":table/probe"),
				dynamoDBMembers[o.name], dynamoDBNested[o.name], fixtures), "dynamodb:"+o.name)
		case "cloudtrail":
			derived = iamCloudTrailDerivesItsResource(o.name, cloudTrailMembers[o.name])
		case "autoscaling":
			derived = iamAutoScalingDerivesItsResource(o.name,
				autoScalingCasedParameters[o.name], autoScalingNested[o.name], fixtures)
		case "kms":
			derived = iamKMSDerivesItsResource(o.name, kmsMembers[o.name])
		case "events":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"events", "AWSEvents", o.name,
				probeARN("arn:aws:events:us-east-1:"+iamProbeAccount+":event-bus/probe"),
				eventBridgeMembers[o.name], eventBridgeNested[o.name], fixtures), "events:"+o.name)
		case "organizations":
			derived = iamOrganizationsDerivesItsResource(o.name,
				organizationsMembers[o.name], organizationsIDs, fixtures)
		case "elasticloadbalancing":
			derived = iamELBv2DerivesItsResource(o.name, elbParameters[o.name], elbNested[o.name])
		case "acm":
			derived = iamACMDerivesItsResource(o.name, acmMembers[o.name])
		case "cloudwatch":
			derived = iamCloudWatchDerivesItsResource(o.name, cloudWatchMembers[o.name], fixtures)
		case "ecr":
			derived = iamJSONProbeDerives("ecr", o.name, ecrMembers[o.name],
				probeARN("arn:aws:ecr:us-east-1:123456789012:repository/probe"))
		case "kinesis":
			derived = iamJSONProbeDerives("kinesis", o.name, kinesisMembers[o.name],
				probeARN("arn:aws:kinesis:us-east-1:123456789012:stream/probe"))
		case "states":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"states", "AWSStepFunctions", o.name,
				probeARN("arn:aws:states:us-east-1:123456789012:stateMachine:probe"),
				statesCased[o.name], statesNested[o.name], fixtures), "states:"+o.name)
		case "secretsmanager":
			derived = iamJSONProbeDerives("secretsmanager", o.name, secretsMembers[o.name],
				probeARN("arn:aws:secretsmanager:us-east-1:123456789012:secret:probe"))
		case "sns":
			derived = iamQueryProbeDerives("sns", o.name, "2010-03-31", snsParameters[o.name])
		case "sqs":
			derived = iamQueryProbeDerives("sqs", o.name, "2012-11-05", sqsParameters[o.name])
		case "acm-pca":
			derived = iamJSONProbeDerives("acm-pca", o.name, acmPCAMembers[o.name],
				probeARN("arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/0123abcd-ef45-6789-abcd-ef0123456789"))
		case "servicediscovery":
			derived = iamJSONProbeDerives("servicediscovery", o.name, cloudMapMembers[o.name],
				probeARN("arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-probe"))
		case "firehose":
			derived = iamJSONProbeDerives("firehose", o.name, firehoseMembers[o.name],
				probeARN("arn:aws:firehose:us-east-1:123456789012:deliverystream/probe"))
		case "budgets":
			// A budget ARN carries no region: AWS Budgets is a global service,
			// and its own reference spells the resource "budget/${BudgetName}".
			derived = iamJSONProbeDerives("budgets", o.name, budgetsMembers[o.name],
				probeARN("arn:aws:budgets::123456789012:budget/probe"))
		case "sts":
			derived = iamQueryProbeDerives("sts", o.name, "2011-06-15", stsParameters[o.name])
		case "application-autoscaling":
			derived = iamJSONProbeDerives("application-autoscaling", o.name, appAutoScalingMembers[o.name],
				probeARN("arn:aws:application-autoscaling:us-east-1:123456789012:scalable-target/probe"))
		case "ecs":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"ecs", "AmazonEC2ContainerServiceV20141113", o.name,
				probeARN("arn:aws:ecs:us-east-1:123456789012:cluster/probe"),
				ecsMembers[o.name], ecsNested[o.name], fixtures), "ecs:"+o.name)
		case "logs":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"logs", "Logs_20140328", o.name,
				probeARN("arn:aws:logs:us-east-1:123456789012:log-group:probe"),
				logsMembers[o.name], logsNested[o.name], fixtures), "logs:"+o.name)
		case "codebuild":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"codebuild", "CodeBuild_20161006", o.name,
				probeARN("arn:aws:codebuild:us-east-1:123456789012:project/probe"),
				codeBuildMembers[o.name], codeBuildNested[o.name], fixtures), "codebuild:"+o.name)
		case "wafv2":
			derived = iamProductionProbeDerives(iamAWSJSONProbeRequest(
				"wafv2", "AWSWAF_20190729", o.name,
				probeARN("arn:aws:wafv2:us-east-1:123456789012:regional/webacl/probe/0123"),
				wafv2Members[o.name], wafv2Nested[o.name], fixtures), "wafv2:"+o.name)
		case "iam":
			derived = iamProductionProbeDerives(iamAWSQueryProbeRequest(
				"iam", o.name, "2010-05-08", probeARN("arn:aws:iam::"+iamProbeAccount+":policy/probe"),
				iamMembers[o.name], fixtures), "iam:"+o.name)
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
