package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every condition key the vendored AWS Service Reference declares on an action
// is either resolvable by this simulator's authorization gate or listed here
// with the reason it is not.
//
// iamConditionKeyRegistry (iam_conformance_test.go) proves that the keys the
// gate claims are really built, by running the gate's own populators. It says
// nothing about the keys AWS declares and the gate never mentions: those were
// counted by hand into BUGS.md, and a hand count goes stale the moment a
// reference refresh adds an action. This test closes that side. It is a static
// measure and deliberately makes the weaker claim: that no code in the package
// so much as names the key. A key that IS named still owes its proof to the
// registry probe; a key that is named nowhere cannot possibly reach a policy,
// so a condition written on it silently never matches.
//
// When a reference refresh introduces a key, this test fails until the key is
// either populated or classified below with a concrete reason. "Not implemented
// yet" is not a reason — say what the simulator would have to model first.
//
// A row matches one key, or a family when it ends in "*": the 65
// kms:RecipientAttestation measurements share one reason and are one row. Every
// row must match a key some vendored action declares, and no row may match a
// key the gate now resolves, so the list cannot outlive what it excuses.
type iamUnmodelledConditionKey struct {
	match  string
	reason string
}

var iamUnmodelledConditionKeys = []iamUnmodelledConditionKey{
	{"kms:RecipientAttestation:*", "Each of the 65 measurements (PCR0-31, the Nitro TPM registers, ImageSha384) is read " +
		"out of the signed attestation document an enclave passes in the Recipient parameter. This simulator runs no " +
		"Nitro enclave and mints no attestation document -- handleKMSGenerateRandom says as much about the Recipient " +
		"path -- so there is no measurement to report, and a fabricated PCR would let a policy scoped to one enclave " +
		"image match anything."},

	{"dynamodb:Fis*", "Declared on dynamodb:InjectError, an action only AWS Fault Injection Service calls. The " +
		"simulator serves no FIS experiment and no injection action, so no experiment id or target list exists."},
	{"ec2:Fis*", "Declared on ec2:InjectApiError, which only AWS Fault Injection Service calls; the simulator serves " +
		"neither the action nor an FIS experiment."},
	{"kinesis:Fis*", "Declared on kinesis:InjectApiError, which only AWS Fault Injection Service calls; the simulator " +
		"serves neither the action nor an FIS experiment, so there is no experiment, target set or injection " +
		"percentage."},

	{"glue:LakeFormationPermissions", "The permissions AWS Lake Formation holds over the catalog object a request " +
		"touches. The simulator's Glue catalog has no Lake Formation registration -- nothing registers a resource, " +
		"grants a permission or reads one back -- so the value would be an invented grant set."},
	{"glue:FederatedAuthorizationSource", "Names the federated catalog a Glue object is authorized through. The " +
		"simulator models no federated catalog registration, so a request names no source."},
	{"glue:EnabledForRedshiftAutoDiscovery", "A property of a Glue catalog registered for Amazon Redshift " +
		"auto-discovery, which the simulator's catalog does not carry."},

	{"iam:FIDO*", "The certification level of the FIDO authenticator an EnableMFADevice request registers, read from " +
		"the FIDO Metadata Service entry for that authenticator model. The simulator registers virtual MFA devices " +
		"and SSH keys; it has no security-key registration and no authenticator metadata to look up."},
	{"iam:RegisterSecurityKey", "Set when an EnableMFADevice request registers a FIDO security key. The simulator's " +
		"MFA surface has no security-key path, so no request can be one."},
	{"iam:RoleTemplateARN", "Every IAM role template lives under the literal account 'aws' and is AWS's own published " +
		"catalog. This simulator refuses to fabricate template content (iamRoleTemplateCatalogUnavailable), so no " +
		"request it serves can name a template."},
	{"iam:AssociatedResourceArn", "Declared on iam:PassRole, where AWS sets it to the resource the role is being " +
		"attached to. At PassRole time that resource usually does not exist yet, so supplying it means composing each " +
		"consuming service's predictable ARN from the request (Lambda's FunctionName, ECS's cluster and task " +
		"definition, and so on) -- a per-service table this simulator has not built. Unset, a policy that requires " +
		"the key denies, which beats matching a fabricated ARN. iam:PassedToService, the key that scopes a PassRole " +
		"statement to a service, is populated."},

	{"logs:data_source_*", "Declared on logs:IntegrateWithS3Table and logs:ProcessWithPipeline, CloudWatch Logs " +
		"operations the simulator does not serve: it has no S3 Tables integration and no log-processing pipeline, so " +
		"there is no data source to name or type."},

	{"ssm:resourceTag/*", "The tags of the managed instance a Systems Manager request targets, and for a session the " +
		"session and target ids. Every action that declares one (SendCommand, StartSession, ResumeSession, " +
		"DescribeInstancePatches, DeregisterManagedInstance, UpdateManagedInstanceRole) is a managed-instance or " +
		"Session Manager operation, and the simulator registers no managed instance and runs no session, so the " +
		"targeted resource does not exist. The SSM surface it does serve -- documents, parameters, associations, " +
		"patch baselines, inventory -- resolves its tags through the shared dispatcher."},
	{"ssm:SourceInstanceARN", "The ARN of the EC2 instance whose SSM Agent made the call, on actions only an agent " +
		"makes (PutComplianceItems, UpdateAssociationStatus, UpdateInstanceAssociationStatus). The simulator runs no " +
		"agent and serves none of those actions."},
	{"ec2:SourceInstanceARN", "The same agent-borne identity, declared by Systems Manager on the same agent-only " +
		"actions the simulator does not serve."},
	{"ssm:NodeAccountId", "A property of a registered managed node (RegisterManagedInstance, " +
		"RequestManagedInstanceRoleToken); the simulator registers no nodes."},
	{"ssm:NodeOrgId", "The AWS Organizations id of a registered managed node, which requires both node registration " +
		"and an organization -- the simulator models neither."},
	{"ssm:DocumentCategories", "The categories of the requested document. The vendored Systems Manager model carries " +
		"no category member on CreateDocument or GetDocument (its GetDocumentResult is Name, DisplayName, Version, " +
		"Status, Content, DocumentType, DocumentFormat, Requires, AttachmentsContent, ReviewStatus), so the " +
		"simulator's documents have no categories to report and the API gives no way to set one."},
	{"ssm:SessionDocumentAccessCheck", "Declared on ssm:StartSession, which the simulator does not serve."},

	{"events:ManagedBy", "The service principal that manages a rule another AWS service created on the account's " +
		"behalf -- the key exists so a policy can stop a caller deleting such a rule. Every rule in this simulator is " +
		"created by the caller through PutRule, which carries no managing principal, so no rule is service-managed."},
	{"events:eventBusInvocation", "Set on PutEvents when the events arrive from another event bus rather than a " +
		"client. The simulator routes no bus-to-bus invocation, so every PutEvents it sees is a direct call."},

	{"sts:RequestContext/*", "Declared on sts:SetContext, an operation the simulator does not serve: trusted identity " +
		"propagation needs a context provider to assert the context, and none exists here."},
	{"sts:RequestContextProviders", "Declared on the same unserved sts:SetContext; there are no context providers to " +
		"list."},
	{"s3:JobSuspendedCause", "The reason a batch job is suspended. The vendored S3 Control model types it as a free " +
		"string with no enumeration, and its own documentation says a job is only suspended when it is created " +
		"through the Amazon S3 console and is awaiting confirmation -- the value is service-written, never sent by a " +
		"caller, and no vendored document names one (no occurrence of AwaitingConfirmation in s3-control.smithy, " +
		"s3.smithy, the supplement or the service reference). The simulator's job record has no cause field, so any " +
		"string here would be invented. The other six batch-job and access-grant keys are populated."},

	{"s3:isReplicationPauseRequest", "True when a PutReplicationConfiguration request is the one that pauses " +
		"replication. The vendored Amazon S3 model has no pause: its ReplicationConfiguration is Role and Rules, and a " +
		"ReplicationRule is ID, Priority, Prefix, Filter, Status, SourceSelectionCriteria, ExistingObjectReplication, " +
		"Destination and DeleteMarkerReplication, with no member and no shape naming a pause anywhere in the document. " +
		"Nothing in a request the simulator can receive marks it as a pause."},
	{"s3:destinationRegion", "Declared on s3:PauseReplication, an action the simulator does not serve and the vendored " +
		"model does not define."},
	{"s3:deliverySourceArn", "Declared on s3:AllowVendedLogDeliveryForResource, the authorization a log-vending AWS " +
		"service performs against a destination bucket. The simulator vends no logs into S3, so no delivery source " +
		"reaches the bucket policy."},
	{"s3:resourceArnBeingAuthorized", "The resource a vended log delivery is being authorized for, on the same " +
		"unserved s3:AllowVendedLogDeliveryForResource."},
	{"s3:ObjectCreationOperation", "Declared on s3:PutObject to distinguish how an object is being created. AWS " +
		"publishes no value space for it -- the reference gives only the key and its type, and the vendored S3 model " +
		"has no member carrying it -- so the simulator would have to invent the strings a policy compares against, " +
		"and a wrong spelling silently never matches while looking implemented."},

	{"sts:RoleAuthorizedByIdp", "Set when the identity provider itself authorized the role in the token it issued. " +
		"The simulator's web-identity federation reads the standard registered claims and the provider's " +
		"thumbprint; the provider-asserted role authorization is not part of the OpenID Connect token it mints, so " +
		"no request carries the assertion."},
}

// matches reports whether the row covers a key: exactly, or as a family
// prefix when the row ends in "*".
func (row iamUnmodelledConditionKey) matches(key string) bool {
	if prefix, ok := strings.CutSuffix(row.match, "*"); ok {
		return strings.HasPrefix(strings.ToLower(key), strings.ToLower(prefix))
	}
	return strings.EqualFold(row.match, key)
}

// iamClassifiedUnmodelled returns the row covering a key, if any.
func iamClassifiedUnmodelled(key string) (iamUnmodelledConditionKey, bool) {
	for _, row := range iamUnmodelledConditionKeys {
		if row.matches(key) {
			return row, true
		}
	}
	return iamUnmodelledConditionKey{}, false
}

// iamServiceReferenceDir is the vendored reference, one gzipped document per
// service, refreshed by scripts/fetch-aws-service-reference.sh.
const iamServiceReferenceDir = "../specs/cloud-api/aws/service-reference"

type iamReferenceDoc struct {
	Name    string `json:"Name"`
	Actions []struct {
		Name                string   `json:"Name"`
		ActionConditionKeys []string `json:"ActionConditionKeys"`
	} `json:"Actions"`
}

// iamDeclaredConditionKeys maps each declared condition key to the
// service:Action pairs that declare it.
func iamDeclaredConditionKeys(t *testing.T) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(iamServiceReferenceDir, "*.servicereference.json.gz"))
	if err != nil {
		t.Fatalf("glob the service reference: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no service reference documents under %s — run scripts/fetch-aws-service-reference.sh", iamServiceReferenceDir)
	}
	declared := map[string][]string{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			t.Fatalf("gunzip %s: %v", path, err)
		}
		var doc iamReferenceDoc
		if err := json.NewDecoder(zr).Decode(&doc); err != nil {
			zr.Close()
			f.Close()
			t.Fatalf("decode %s: %v", path, err)
		}
		zr.Close()
		f.Close()
		for _, action := range doc.Actions {
			for _, key := range action.ActionConditionKeys {
				if key == "" {
					continue
				}
				declared[key] = append(declared[key], doc.Name+":"+action.Name)
			}
		}
	}
	return declared
}

// iamConditionKeySpellings is every string literal that would make a key
// reachable. AWS writes a templated key with a placeholder the code never
// contains — aws:ResourceTag/${TagKey}, kms:EncryptionContext:${key},
// secretsmanager:ResourceTag/tag-key — so the code is credited for the literal
// prefix it concatenates the tag onto. The service prefix is optional because
// a per-service populator registered for "kms" writes its keys unprefixed.
func iamConditionKeySpellings(key string) []string {
	forms := map[string]bool{key: true}
	if service, rest, ok := strings.Cut(key, ":"); ok && service != "" && rest != "" {
		forms[rest] = true
	}
	for form := range forms {
		// A templated segment starts at the last separator before the
		// placeholder: everything up to and including it is what the code
		// writes.
		if cut := strings.LastIndexAny(form, "/:"); cut > 0 && cut < len(form)-1 {
			tail := form[cut+1:]
			templated := strings.ContainsAny(tail, "${}<>") || tail == "tag-key" || tail == "key"
			if templated {
				forms[form[:cut+1]] = true
			}
		}
	}
	out := make([]string, 0, len(forms))
	for form := range forms {
		out = append(out, form)
	}
	sort.Strings(out)
	return out
}

// iamPackageSource is every non-test Go file of the simulator package, lowered
// once so the scan is case-insensitive: IAM condition key names are not case
// sensitive, and the services disagree on case (ssm:resourceTag against
// aws:ResourceTag).
func iamPackageSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var b strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		t.Fatal("scanned no package source")
	}
	return strings.ToLower(b.String())
}

// iamServiceResourceTagServices is the set of service prefixes whose targeted
// resource's tags the gate resolves: the dispatcher's own switch cases in
// iam_service_resource_tags.go, plus the two services that resolve theirs in
// iam_condition_context.go and so write a literal.
func iamServiceResourceTagServices(t *testing.T) map[string]bool {
	t.Helper()
	const dispatcher = "iam_service_resource_tags.go"
	data, err := os.ReadFile(dispatcher)
	if err != nil {
		t.Fatalf("read %s: %v", dispatcher, err)
	}
	body := string(data)
	start := strings.Index(body, "func iamPopulateServiceResourceTags")
	if start < 0 {
		t.Fatalf("%s no longer defines iamPopulateServiceResourceTags — this test reads its switch", dispatcher)
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("%s: could not find the end of iamPopulateServiceResourceTags", dispatcher)
	}
	services := map[string]bool{}
	for _, line := range strings.Split(body[start:start+end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "case ") {
			continue
		}
		for _, item := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "case "), ":"), ",") {
			if name := strings.Trim(strings.TrimSpace(item), `"`); name != "" {
				services[strings.ToLower(name)] = true
			}
		}
	}
	if len(services) == 0 {
		t.Fatalf("%s: read no service cases out of the dispatcher", dispatcher)
	}
	return services
}

// TestIAM_DeclaredConditionKeysAreResolvedOrClassified fails when a declared
// key is neither named by the gate nor classified as unmodelled, and when a
// classified key has since been wired (so the list cannot rot into an excuse).
func TestIAM_DeclaredConditionKeysAreResolvedOrClassified(t *testing.T) {
	declared := iamDeclaredConditionKeys(t)
	source := iamPackageSource(t)

	dispatched := iamServiceResourceTagServices(t)
	mentioned := func(key string) bool {
		// <service>:ResourceTag/<k> is written as service+":ResourceTag/"+k,
		// so no literal names the service. It is resolved exactly for the
		// services the dispatcher resolves tags for, and for no others.
		if service, rest, ok := strings.Cut(key, ":"); ok && strings.HasPrefix(strings.ToLower(rest), "resourcetag/") {
			if dispatched[strings.ToLower(service)] {
				return true
			}
		}
		for _, form := range iamConditionKeySpellings(key) {
			if strings.Contains(source, `"`+strings.ToLower(form)) {
				return true
			}
		}
		return false
	}

	var unclassified, stale []string
	var resolved, excused int
	for key, actions := range declared {
		if mentioned(key) {
			resolved++
		} else if _, ok := iamClassifiedUnmodelled(key); ok {
			excused++
		}
		_, classified := iamClassifiedUnmodelled(key)
		switch {
		case mentioned(key) && classified:
			stale = append(stale, key)
		case !mentioned(key) && !classified:
			sort.Strings(actions)
			shown := actions
			if len(shown) > 3 {
				shown = shown[:3]
			}
			unclassified = append(unclassified, key+" ("+strings.Join(shown, ", ")+")")
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)

	t.Logf("declared condition keys: %d over %d actions; %d named by the gate, %d classified unmodelled, %d unaccounted",
		len(declared), func() int {
			actions := map[string]bool{}
			for _, list := range declared {
				for _, action := range list {
					actions[action] = true
				}
			}
			return len(actions)
		}(), resolved, excused, len(unclassified))

	if len(unclassified) > 0 {
		t.Errorf("%d declared condition keys are neither resolved nor classified — populate each or add it to iamUnmodelledConditionKeys with the reason:\n%s",
			len(unclassified), strings.Join(unclassified, "\n"))
	}
	for _, key := range stale {
		t.Errorf("%s is listed as unmodelled and the gate now names it — drop the row", key)
	}
	for _, row := range iamUnmodelledConditionKeys {
		if strings.TrimSpace(row.reason) == "" {
			t.Errorf("%s is classified with no reason", row.match)
		}
		covered := false
		for key := range declared {
			if row.matches(key) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s is classified as unmodelled and no vendored action declares such a key — drop the row", row.match)
		}
	}
}
