package main

import (
	"compress/gzip"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every condition key the vendored AWS Service Reference declares on an action
// is either resolvable by the authorization gate or listed here with the reason
// it is not. iamConditionKeyRegistry proves the keys the gate claims are built;
// this static measure covers the keys AWS declares and the gate never names,
// since a key named nowhere cannot reach a policy and a condition on it never
// matches. A reference refresh that adds a key fails this test until the key is
// populated or classified with a concrete reason: say what the simulator would
// have to model first. A row matches one key, or a family when it ends in "*".
// Every row must match a declared key and none may match a key the gate
// resolves, so the list cannot outlive what it excuses.
type iamUnmodelledConditionKey struct {
	match  string
	reason string
}

var iamUnmodelledConditionKeys = []iamUnmodelledConditionKey{
	{"cloudwatch:HasAccessGrant", "Declared only on cloudwatch:AssumeAccessProfile, CreateAccessGrant and CreateAccessProfile, " +
		"operations the vendored CloudWatch model does not contain and the simulator does not serve."},
	{"events:ContentFilterPresent", "Declared only on events:CreateSubscriber and UpdateSubscriber, operations the vendored " +
		"EventBridge model does not contain and the simulator does not serve."},
	{"events:Metadata/${MetadataKey}", "Declared on events:CreateSubscriber, UpdateSubscriber and PutRawEvents, operations " +
		"the vendored EventBridge model does not contain and the simulator does not serve."},
	{"events:Metadata/${MetadataKey}/Matcher", "Declared only on events:CreateSubscriber and UpdateSubscriber, operations the " +
		"vendored EventBridge model does not contain and the simulator does not serve."},
	{"events:SystemMetadata/AwsDetailType", "Arrived with events:PutRawEvents and is also declared on PutEvents, but neither the " +
		"Service Reference nor the Service Authorization Reference says what value it carries (2026-09-25); a value derived " +
		"from PutEvents' DetailType would be a guess that a policy could match on."},
	{"events:SystemMetadata/AwsSource", "Arrived with events:PutRawEvents and is also declared on PutEvents, but neither the " +
		"Service Reference nor the Service Authorization Reference says what value it carries (2026-09-25); a value derived " +
		"from PutEvents' Source would be a guess that a policy could match on."},
	{"events:SystemMetadata/ContentType", "Arrived with events:PutRawEvents and is also declared on PutEvents, but neither the " +
		"Service Reference nor the Service Authorization Reference says what value it carries (2026-09-25)."},
	{"kms:EncapsulationAlgorithm", "Declared only on kms:Encapsulate and kms:Decapsulate, operations the vendored " +
		"KMS model does not contain and the simulator does not serve."},
	{"lambda:Request/WebFunctionAuthType", "Declared only on lambda:CreateWebFunction, CreateWebFunctionEndpoint and " +
		"UpdateWebFunctionEndpoint, operations the vendored Lambda model does not contain and the simulator does not serve."},
	{"lambda:Resource/WebFunctionAuthType", "Declared only on lambda:DeleteWebFunctionEndpoint, GetWebFunctionEndpoint and " +
		"InvokeWebFunctionEndpoint, operations the vendored Lambda model does not contain and the simulator does not serve."},
	{"kms:RecipientAttestation:*", "Each measurement (the PCRs, the Nitro TPM registers, ImageSha384, PCR8's service name) is read " +
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

	{"s3-outposts:delimiter", "Declared on the Outposts bucket listings, which the simulator does not serve: its one " +
		"s3-outposts operation is ListRegionalBuckets, whose declared keys are the request-shape ones and which takes " +
		"no delimiter."},
	{"s3-outposts:max-keys", "Declared on the same unserved Outposts bucket listings."},
	{"s3-outposts:prefix", "Declared on the same unserved Outposts bucket listings."},
	{"s3-outposts:versionid", "The object version an Outposts request names, on object operations the simulator does " +
		"not serve."},
	{"s3-outposts:x-amz-acl", "A header an Outposts object write carries, on operations the simulator does not serve; " +
		"the general-purpose data plane reads it under the s3 spelling."},
	{"s3-outposts:x-amz-copy-source", "The same, for the copy source of an unserved Outposts object copy."},
	{"s3-outposts:x-amz-metadata-directive", "The same, for an unserved Outposts object copy's metadata directive."},
	{"s3-outposts:x-amz-server-side-encryption", "The same, for an unserved Outposts object write's encryption."},
	{"s3-outposts:x-amz-storage-class", "The same, for an unserved Outposts object write's storage class."},
	{"s3-object-lambda:versionid", "The object version a request through an Object Lambda access point names, on the " +
		"object operations that access point forwards. The simulator serves the transformation callback and the " +
		"access point's own control-plane surface; a versioned read through one is not an operation it answers."},

	{"s3-outposts:DataAccessPoint*", "Declared on the Amazon S3 on Outposts access-point and object operations. The " +
		"simulator serves exactly one s3-outposts operation, ListRegionalBuckets, and models no Outpost -- no outpost " +
		"id, no Outposts bucket, no Outposts access point -- so no request it answers is addressed through one. The " +
		"request-shape keys ListRegionalBuckets does declare are populated."},
	{"s3-outposts:AccessPointNetworkOrigin", "The network origin of an Outposts access point, which the simulator " +
		"models nothing of; see the DataAccessPoint row."},
	{"s3-outposts:ExistingObjectTag/*", "The tags already on an Outposts object. The simulator serves no Outposts " +
		"object operation, so there is no such object to read tags from."},
	{"s3-outposts:RequestObjectTag*", "The tags an Outposts object write carries, on operations the simulator does " +
		"not serve."},
	{"s3express:AllAccessRestrictedToLocalZoneGroup", "Declared on s3express:CreateSession, and the AWS Local Zone " +
		"network border groups the request is restricted to. The simulator places directory buckets in Availability " +
		"Zones only and models no Local Zone or network border group, so no session request is restricted to one."},
	{"s3express:Permissions", "Declared on s3express:CreateSession as \"the permission requested by Access Point " +
		"Scope configuration, such as GetObject, PutObject\". Neither the Service Reference, the Service Authorization " +
		"Reference nor the S3 Express One Zone pages say whether a session request carries the scope of the access " +
		"point it is addressed through, the operations the session will serve, or something else, and the " +
		"CreateSession request has no member naming a permission, so any value would be a guess a policy could " +
		"match on."},
	{"s3:logType", "Declared only on s3:AllowVendedLogDeliveryForResource, the authorization a log-vending AWS " +
		"service performs against a destination bucket. The simulator vends no logs into S3; see the " +
		"s3:deliverySourceArn row."},

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

	{"elasticfilesystem:Access*", "elasticfilesystem:AccessPointArn and AccessedViaMountTarget describe an NFS " +
		"client's mount -- the access point and whether a mount target carries it -- and are declared only on " +
		"elasticfilesystem:ClientMount, ClientRootAccess and ClientWrite, the actions Amazon EFS authorizes when a " +
		"client mounts with IAM authorization, not API operations. The simulator serves a file system to an Amazon " +
		"ECS task or AWS Lambda function as a host directory bound into the container, with no NFS mount and no " +
		"client authorization, so no mount is ever authorized as these actions."},
	{"events:PolicyName", "Declared only on events:PutResourcePolicy, GetResourcePolicy and DeleteResourcePolicy, " +
		"operations the vendored EventBridge model does not contain and the simulator does not serve."},
	{"ssm:AccessRequestId", "The just-in-time node access request a Session Manager session is opened under, " +
		"declared on ssm:StartSession. The StartSession request names no access request, and the simulator models " +
		"no access requests to look one up from."},
	{"sts:AWSServiceName", "Declared only on sts:GetServiceBearerToken, an operation the vendored AWS STS model does " +
		"not contain and the simulator does not serve."},
	{"apigateway:Request/ProductRestEndpointPageEndpointPrefix", "Declared on apigateway:CreateProductRestEndpointPage " +
		"and UpdateProductRestEndpointPage as \"a ProductRestEndpointPage's EndpointPrefix that is passed in the " +
		"request\", but the vendored API Gateway V2 model has no endpoint-prefix member on either request: the " +
		"nearest, DisplayContentOverrides$Endpoint, is a whole endpoint URL override, and no reference says the key " +
		"is read from it, so a value taken from it would be a guess a policy could match on."},

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

// iamConditionKeySpellings is every string literal, lowered, that would make a
// key reachable. AWS writes a templated key with a placeholder the code never
// contains — aws:ResourceTag/${TagKey}, kms:EncryptionContext:${key},
// secretsmanager:ResourceTag/tag-key — so the code is credited for the literal
// prefix it concatenates the tag onto.
//
// A key is spelled whole, service prefix and all. A bare name is an ordinary
// word that any request member, response field or form parameter spells, so it
// credits nothing. An Amazon S3 namespace's keys are written once for every
// namespace, as ctx[service+":TlsVersion"], so the separator-led ":TlsVersion"
// credits the key for the namespaces iamS3ConditionNamespaces lists.
func iamConditionKeySpellings(key string, pkg iamPackageKeys) []string {
	forms := map[string]bool{key: true}
	if service, rest, ok := strings.Cut(key, ":"); ok && service != "" && rest != "" {
		if pkg.composed[strings.ToLower(service)] {
			forms[":"+rest] = true
		}
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
		out = append(out, strings.ToLower(form))
	}
	sort.Strings(out)
	return out
}

// iamKeyTables are the tables a populator ranges over, writing each entry
// under one service prefix -- ctx["codebuild:"+key], ctx["s3:"+header] -- so
// an entry spells the key of that service it names.
var iamKeyTables = map[string]string{
	"codeBuildConditionKeys":       "codebuild",
	"s3RequestHeaderConditionKeys": "s3",
	"s3RequestQueryConditionKeys":  "s3",
}

// iamPackageKeys is what the scan reads out of the simulator package's
// non-test Go files, lowered because IAM condition key names are not case
// sensitive and the services disagree on case (ssm:resourceTag against
// aws:ResourceTag): every string literal, every key an iamKeyTables entry
// spells, and the namespaces iamS3ConditionNamespaces lists. A struct tag
// spells a wire member and a generated wire table spells request members, so
// neither is read.
type iamPackageKeys struct {
	spelled  map[string]bool
	composed map[string]bool
}

func readIAMPackageKeys(t *testing.T) iamPackageKeys {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob the package: %v", err)
	}
	pkg := iamPackageKeys{spelled: map[string]bool{}, composed: map[string]bool{}}
	tablesRead := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		tags := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ImportSpec:
				return false
			case *ast.Field:
				if n.Tag != nil {
					tags[n.Tag] = true
				}
			case *ast.ValueSpec:
				for i, ident := range n.Names {
					service, keyed := iamKeyTables[ident.Name]
					if (!keyed && ident.Name != "iamS3ConditionNamespaces") || i >= len(n.Values) {
						continue
					}
					entries, _ := n.Values[i].(*ast.CompositeLit)
					if entries == nil {
						continue
					}
					tablesRead[ident.Name] = true
					for _, entry := range entries.Elts {
						if kv, ok := entry.(*ast.KeyValueExpr); ok {
							entry = kv.Key
						}
						name, ok := iamStringLiteral(entry)
						if !ok {
							continue
						}
						if keyed {
							pkg.spelled[strings.ToLower(service+":"+name)] = true
						} else {
							pkg.composed[strings.ToLower(name)] = true
						}
					}
				}
			case *ast.BasicLit:
				if value, ok := iamStringLiteral(n); ok && !tags[n] {
					pkg.spelled[strings.ToLower(value)] = true
				}
			}
			return true
		})
	}
	for _, table := range append(slices.Collect(maps.Keys(iamKeyTables)), "iamS3ConditionNamespaces") {
		if !tablesRead[table] {
			t.Fatalf("read no %s out of the package -- this test reads its entries", table)
		}
	}
	return pkg
}

func iamStringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
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

// TestIAM_DeclaredConditionKeysAreResolvedOrClassified fails when a declared key
// is neither named by the gate nor classified as unmodelled, and when a
// classified key is resolved.
func TestIAM_DeclaredConditionKeysAreResolvedOrClassified(t *testing.T) {
	declared := iamDeclaredConditionKeys(t)
	pkg := readIAMPackageKeys(t)
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
		for _, form := range iamConditionKeySpellings(key, pkg) {
			if pkg.spelled[form] {
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
