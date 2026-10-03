package main

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	_ "cloud.google.com/go/firestore/apiv1/admin/adminpb"
	_ "cloud.google.com/go/iam/admin/apiv1/adminpb"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// The audit logs of the APIs served from their protocol buffer definitions.
// Their audit methodName is the RPC's full name, such as
// google.cloud.run.v2.Services.CreateService, their serviceName the API's
// google.api.default_host, and a REST call reaches its RPC through the
// google.api.http binding the definition declares.

// auditAPI is an RPC service whose calls the simulator audits.
type auditAPI struct {
	service protoreflect.FullName
	// restPrefix is the path the simulator serves the API's REST bindings
	// under, "" for the bindings' own paths.
	restPrefix string
	// methodName is the methodName the service records for an RPC; nil for
	// the RPC's full name.
	methodName func(protoreflect.MethodDescriptor) string
}

// auditOnePlatformServices are the RPC services whose calls the simulator
// audits.
var auditOnePlatformServices = []auditAPI{
	{service: "google.cloud.run.v2.Builds"},
	{service: "google.cloud.run.v2.Executions"},
	{service: "google.cloud.run.v2.Instances"},
	{service: "google.cloud.run.v2.Jobs"},
	{service: "google.cloud.run.v2.Revisions"},
	{service: "google.cloud.run.v2.Services"},
	{service: "google.cloud.run.v2.Tasks"},
	{service: "google.cloud.run.v2.WorkerPools"},
	{service: "google.pubsub.v1.Publisher"},
	{service: "google.pubsub.v1.Subscriber"},
	{service: "google.pubsub.v1.SchemaService"},
	{service: "google.cloud.secretmanager.v1.SecretManagerService"},
	{service: "google.devtools.artifactregistry.v1.ArtifactRegistry"},
	{service: "google.cloud.functions.v2.FunctionService"},
	{service: "google.cloud.kms.v1.KeyManagementService", methodName: auditSimpleMethodName},
	{service: "google.devtools.cloudbuild.v1.CloudBuild"},
	{service: "google.cloud.eventarc.v1.Eventarc"},
	{service: "google.cloud.redis.v1.CloudRedis"},
	{service: "google.spanner.admin.instance.v1.InstanceAdmin", restPrefix: "/spanner"},
	{service: "google.spanner.admin.database.v1.DatabaseAdmin", restPrefix: "/spanner"},
	{service: "google.bigtable.admin.v2.BigtableInstanceAdmin"},
	{service: "google.bigtable.admin.v2.BigtableTableAdmin"},
	{service: "google.firestore.admin.v1.FirestoreAdmin"},
	{service: "google.iam.admin.v1.IAM", methodName: auditIAMAdminMethodName},
}

// auditSimpleMethodName is the RPC's simple name, which Cloud KMS records:
// "CreateKeyRing", "Decrypt".
func auditSimpleMethodName(m protoreflect.MethodDescriptor) string {
	return string(m.Name())
}

// auditIAMAdminMethodName is the methodName IAM records: the RPC's name in
// its proto package, with IAM capitalised in the policy methods, as in
// google.iam.admin.v1.CreateServiceAccount and google.iam.admin.v1.SetIAMPolicy.
func auditIAMAdminMethodName(m protoreflect.MethodDescriptor) string {
	return string(m.ParentFile().Package()) + "." + strings.Replace(string(m.Name()), "IamPolicy", "IAMPolicy", 1)
}

// auditRPCLogTypes are the RPCs whose audit log type their name does not
// tell: the data-plane calls and the reads that are not named Get or List.
var auditRPCLogTypes = map[protoreflect.FullName]string{
	"google.cloud.secretmanager.v1.SecretManagerService.AccessSecretVersion": auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.Encrypt":                       auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.Decrypt":                       auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.RawEncrypt":                    auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.RawDecrypt":                    auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.AsymmetricSign":                auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.AsymmetricDecrypt":             auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.MacSign":                       auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.MacVerify":                     auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.GenerateRandomBytes":           auditDataRead,
	"google.cloud.kms.v1.KeyManagementService.Decapsulate":                   auditDataRead,
	"google.bigtable.admin.v2.BigtableTableAdmin.CheckConsistency":           auditAdminRead,
	"google.bigtable.admin.v2.BigtableTableAdmin.GenerateConsistencyToken":   auditAdminRead,
}

// auditUnloggedRPCs are the RPCs of the audited services the simulator writes
// no audit entry for. No service audits TestIamPermissions.
var auditUnloggedRPCs = map[protoreflect.FullName]bool{
	"google.pubsub.v1.Publisher.Publish":               true,
	"google.pubsub.v1.Subscriber.Pull":                 true,
	"google.pubsub.v1.Subscriber.StreamingPull":        true,
	"google.pubsub.v1.Subscriber.Acknowledge":          true,
	"google.pubsub.v1.Subscriber.ModifyAckDeadline":    true,
	"google.pubsub.v1.Subscriber.Seek":                 true,
	"google.pubsub.v1.SchemaService.ValidateSchema":    true,
	"google.pubsub.v1.SchemaService.ValidateMessage":   true,
	"google.iam.admin.v1.IAM.QueryGrantableRoles":      true,
	"google.iam.admin.v1.IAM.QueryTestablePermissions": true,
	"google.iam.admin.v1.IAM.QueryAuditableServices":   true,
	"google.iam.admin.v1.IAM.LintPolicy":               true,
	"google.iam.admin.v1.IAM.SignBlob":                 true,
	"google.iam.admin.v1.IAM.SignJwt":                  true,
}

// auditRPC is one audited RPC.
type auditRPC struct {
	method      protoreflect.MethodDescriptor
	serviceName string
	methodName  string
	logType     string
	bindings    []*auditHTTPBinding
}

// auditHTTPBinding is one google.api.http binding of an RPC: an HTTP method
// and a compiled path template.
type auditHTTPBinding struct {
	rpc      *auditRPC
	verb     string
	segments []auditTemplateSegment
	custom   string
	body     string
	vars     []auditTemplateVar
	literals int
}

// auditTemplateSegment matches one path segment: a literal, "*" for any one
// segment, or "**" for the rest of the path.
type auditTemplateSegment struct {
	literal string
	varIdx  int
}

// auditTemplateVar is a template variable: the request field it fills and
// the segments it spans.
type auditTemplateVar struct {
	field string
	first int
	last  int
	// next is the literal segment after the variable, the collection a
	// parent variable creates into.
	next string
}

var (
	auditRPCsOnce   sync.Once
	auditRPCsByName map[string]*auditRPC
	auditBindings   []*auditHTTPBinding
	auditRPCsErr    error
)

func auditLoadRPCs() error {
	auditRPCsOnce.Do(func() {
		auditRPCsByName = map[string]*auditRPC{}
		for _, api := range auditOnePlatformServices {
			name := api.service
			desc, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
			if err != nil {
				auditRPCsErr = fmt.Errorf("audit logs: service %s: %w", name, err)
				return
			}
			service, ok := desc.(protoreflect.ServiceDescriptor)
			if !ok {
				auditRPCsErr = fmt.Errorf("audit logs: %s is not a service", name)
				return
			}
			host, _ := proto.GetExtension(service.Options(), annotations.E_DefaultHost).(string)
			if host == "" {
				auditRPCsErr = fmt.Errorf("audit logs: service %s declares no default host", name)
				return
			}
			methods := service.Methods()
			for i := range methods.Len() {
				m := methods.Get(i)
				logType, audited := auditRPCLogType(m)
				if !audited {
					continue
				}
				rpc := &auditRPC{method: m, serviceName: host, methodName: string(m.FullName()), logType: logType}
				if api.methodName != nil {
					rpc.methodName = api.methodName(m)
				}
				rule, _ := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule)
				if rule != nil {
					for _, r := range append([]*annotations.HttpRule{rule}, rule.GetAdditionalBindings()...) {
						binding, err := auditCompileBinding(rpc, r, api.restPrefix)
						if err != nil {
							auditRPCsErr = fmt.Errorf("audit logs: %s: %w", m.FullName(), err)
							return
						}
						rpc.bindings = append(rpc.bindings, binding)
						auditBindings = append(auditBindings, binding)
					}
				}
				auditRPCsByName[string(m.FullName())] = rpc
			}
		}
	})
	return auditRPCsErr
}

// auditRPCLogType is the audit log type an RPC writes: a configuration read
// is ADMIN_READ, a data-plane call its own type, any other call ADMIN_WRITE.
func auditRPCLogType(m protoreflect.MethodDescriptor) (string, bool) {
	if auditUnloggedRPCs[m.FullName()] || m.Name() == "TestIamPermissions" || m.IsStreamingClient() || m.IsStreamingServer() {
		return "", false
	}
	if logType, ok := auditRPCLogTypes[m.FullName()]; ok {
		return logType, true
	}
	name := string(m.Name())
	if strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List") {
		return auditAdminRead, true
	}
	return auditAdminWrite, true
}

func auditCompileBinding(rpc *auditRPC, rule *annotations.HttpRule, prefix string) (*auditHTTPBinding, error) {
	b := &auditHTTPBinding{rpc: rpc, body: rule.GetBody()}
	var template string
	switch p := rule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		b.verb, template = "GET", p.Get
	case *annotations.HttpRule_Post:
		b.verb, template = "POST", p.Post
	case *annotations.HttpRule_Put:
		b.verb, template = "PUT", p.Put
	case *annotations.HttpRule_Patch:
		b.verb, template = "PATCH", p.Patch
	case *annotations.HttpRule_Delete:
		b.verb, template = "DELETE", p.Delete
	case *annotations.HttpRule_Custom:
		b.verb, template = p.Custom.GetKind(), p.Custom.GetPath()
	default:
		return nil, fmt.Errorf("http rule has no pattern")
	}
	if !strings.HasPrefix(template, "/") {
		return nil, fmt.Errorf("path template %q does not start with /", template)
	}
	template = strings.TrimPrefix(prefix+template, "/")
	depth, colon := 0, -1
	for i, c := range template {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
		case ':':
			if depth == 0 {
				colon = i
			}
		}
	}
	if colon >= 0 {
		b.custom = template[colon+1:]
		template = template[:colon]
	}
	for _, token := range auditSplitTemplate(template) {
		if !strings.HasPrefix(token, "{") {
			if len(b.vars) > 0 && b.vars[len(b.vars)-1].next == "" && b.vars[len(b.vars)-1].last == len(b.segments)-1 {
				b.vars[len(b.vars)-1].next = token
			}
			b.segments = append(b.segments, auditTemplateSegment{literal: token, varIdx: -1})
			if token != "*" && token != "**" {
				b.literals++
			}
			continue
		}
		field, pattern, found := strings.Cut(strings.Trim(token, "{}"), "=")
		if !found {
			pattern = "*"
		}
		v := auditTemplateVar{field: field, first: len(b.segments)}
		for _, part := range strings.Split(pattern, "/") {
			b.segments = append(b.segments, auditTemplateSegment{literal: part, varIdx: len(b.vars)})
			if part != "*" && part != "**" {
				b.literals++
			}
		}
		v.last = len(b.segments) - 1
		b.vars = append(b.vars, v)
	}
	return b, nil
}

// auditSplitTemplate splits a path template at the slashes outside its
// variables.
func auditSplitTemplate(template string) []string {
	var tokens []string
	depth, start := 0, 0
	for i := 0; i < len(template); i++ {
		switch template[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '/':
			if depth == 0 {
				tokens = append(tokens, template[start:i])
				start = i + 1
			}
		}
	}
	return append(tokens, template[start:])
}

// match binds the binding's variables from a request path split at its
// slashes, the last segment without the custom method it may carry.
func (b *auditHTTPBinding) match(method string, parts []string, custom string) (map[string]string, bool) {
	if method != b.verb || custom != b.custom {
		return nil, false
	}
	values := make([][]string, len(b.vars))
	pi := 0
	for si, segment := range b.segments {
		if segment.literal == "**" {
			if si != len(b.segments)-1 {
				return nil, false
			}
			if segment.varIdx >= 0 {
				values[segment.varIdx] = append(values[segment.varIdx], parts[pi:]...)
			}
			pi = len(parts)
			break
		}
		if pi >= len(parts) || parts[pi] == "" {
			return nil, false
		}
		if segment.literal != "*" && segment.literal != parts[pi] {
			return nil, false
		}
		if segment.varIdx >= 0 {
			values[segment.varIdx] = append(values[segment.varIdx], parts[pi])
		}
		pi++
	}
	if pi != len(parts) {
		return nil, false
	}
	bound := make(map[string]string, len(b.vars))
	for i, v := range b.vars {
		joined, err := url.PathUnescape(strings.Join(values[i], "/"))
		if err != nil {
			return nil, false
		}
		bound[v.field] = joined
	}
	return bound, true
}

// auditCustomMethodSuffix reports whether a path's last segment ends in an
// AIP-136 custom method, ":verb", which a binding without one does not serve.
func auditCustomMethodSuffix(segment string) bool {
	i := strings.LastIndex(segment, ":")
	if i < 0 || i == len(segment)-1 {
		return false
	}
	verb := segment[i+1:]
	if verb[0] < 'a' || verb[0] > 'z' {
		return false
	}
	for _, c := range verb {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// auditMatchREST is the audited RPC a REST call reaches and the variables its
// path binds, when exactly one API publishes the path.
func auditMatchREST(method, escapedPath string) (*auditHTTPBinding, map[string]string, bool) {
	candidates := auditMatchRESTCandidates(method, escapedPath)
	if len(candidates) == 0 {
		return nil, nil, false
	}
	return candidates[0].binding, candidates[0].vars, true
}

// auditRESTCandidate is a binding a REST call's path matches.
type auditRESTCandidate struct {
	binding *auditHTTPBinding
	vars    map[string]string
}

// auditMatchRESTCandidates are the bindings that match a REST call most
// specifically, one per API: two APIs can publish the same path, which their
// hosts tell apart on Google Cloud.
func auditMatchRESTCandidates(method, escapedPath string) []auditRESTCandidate {
	if err := auditLoadRPCs(); err != nil {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	custom := ""
	if last := parts[len(parts)-1]; auditCustomMethodSuffix(last) {
		i := strings.LastIndex(last, ":")
		custom = last[i+1:]
		parts = append(parts[:len(parts)-1:len(parts)-1], last[:i])
	}
	var best []auditRESTCandidate
	for _, b := range auditBindings {
		vars, ok := b.match(method, parts, custom)
		if !ok {
			continue
		}
		switch {
		case len(best) == 0 || b.literals > best[0].binding.literals:
			best = []auditRESTCandidate{{b, vars}}
		case b.literals == best[0].binding.literals && b.rpc.serviceName != best[0].binding.rpc.serviceName:
			best = append(best, auditRESTCandidate{b, vars})
		}
	}
	return best
}

// auditJSONPath is a dotted proto field path of the message spelled in the
// JSON names its request body uses.
func auditJSONPath(message protoreflect.MessageDescriptor, fieldPath string) []string {
	var out []string
	for _, name := range strings.Split(fieldPath, ".") {
		if message == nil {
			out = append(out, name)
			continue
		}
		field := message.Fields().ByName(protoreflect.Name(name))
		if field == nil {
			out = append(out, name)
			message = nil
			continue
		}
		out = append(out, field.JSONName())
		message = field.Message()
	}
	return out
}

func auditSetPath(doc map[string]any, path []string, value any) {
	for _, key := range path[:len(path)-1] {
		next, ok := doc[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			doc[key] = next
		}
		doc = next
	}
	doc[path[len(path)-1]] = value
}

func auditGetPath(doc map[string]any, path []string) string {
	for _, key := range path[:len(path)-1] {
		next, ok := doc[key].(map[string]any)
		if !ok {
			return ""
		}
		doc = next
	}
	value, _ := doc[path[len(path)-1]].(string)
	return value
}

// auditBindingFor is the binding a request's fields fill: the first whose
// path variables the request sets.
func (rpc *auditRPC) auditBindingFor(request map[string]any) *auditHTTPBinding {
	input := rpc.method.Input()
	for _, b := range rpc.bindings {
		filled := true
		for _, v := range b.vars {
			if auditGetPath(request, auditJSONPath(input, v.field)) == "" {
				filled = false
				break
			}
		}
		if filled {
			return b
		}
	}
	if len(rpc.bindings) > 0 {
		return rpc.bindings[0]
	}
	return nil
}

// resourceName is the resource a call names: the resource its name variable
// binds, for a create the parent, collection and requested ID, and for a
// binding without either the path its variables fill.
func (rpc *auditRPC) resourceName(request map[string]any) string {
	binding := rpc.auditBindingFor(request)
	if binding == nil {
		return ""
	}
	input := rpc.method.Input()
	for _, v := range binding.vars {
		if v.field == "name" || strings.HasSuffix(v.field, ".name") || v.field == "resource" {
			return auditGetPath(request, auditJSONPath(input, v.field))
		}
	}
	for _, v := range binding.vars {
		if v.field != "parent" {
			continue
		}
		parent := auditGetPath(request, auditJSONPath(input, v.field))
		if v.next == "" || parent == "" {
			return parent
		}
		fields := input.Fields()
		for i := range fields.Len() {
			f := fields.Get(i)
			if f.Kind() != protoreflect.StringKind || f.IsList() || !strings.HasSuffix(string(f.Name()), "_id") {
				continue
			}
			if id, _ := request[f.JSONName()].(string); id != "" {
				return parent + "/" + v.next + "/" + id
			}
		}
		return parent
	}
	for _, v := range binding.vars {
		if v.last > v.first {
			return auditGetPath(request, auditJSONPath(input, v.field))
		}
	}
	var parts []string
	for i, segment := range binding.segments {
		switch {
		case segment.varIdx >= 0:
			if binding.vars[segment.varIdx].first == i {
				parts = append(parts, auditGetPath(request, auditJSONPath(input, binding.vars[segment.varIdx].field)))
			}
		case len(parts) > 0 || segment.literal == "projects":
			parts = append(parts, segment.literal)
		}
	}
	return strings.Join(parts, "/")
}

// auditVersionSegment reports whether a path segment is an API version, v1
// or v2beta1.
func auditVersionSegment(segment string) bool {
	return len(segment) > 1 && segment[0] == 'v' && segment[1] >= '0' && segment[1] <= '9'
}

// auditPermissionPrefixes are the IAM permission prefixes of the APIs whose
// permissions do not start with the label of their host.
var auditPermissionPrefixes = map[string]string{
	"bigtableadmin.googleapis.com": "bigtable",
	"firestore.googleapis.com":     "datastore",
}

// auditPermissionCollections renames the collections whose permissions name
// another resource type: a service account key's are iam.serviceAccountKeys.*,
// a Cloud Build trigger's cloudbuild.builds.*, and Artifact Registry spells
// its format-specific collections in lower case.
var auditPermissionCollections = map[string]map[string]string{
	"iam.googleapis.com":        {"keys": "serviceAccountKeys"},
	"cloudbuild.googleapis.com": {"triggers": "builds"},
	"artifactregistry.googleapis.com": {
		"dockerImages":   "dockerimages",
		"mavenArtifacts": "mavenartifacts",
		"npmPackages":    "npmpackages",
		"pythonPackages": "pythonpackages",
	},
}

// auditPermissionVerbs are the method verbs whose permission verb differs.
var auditPermissionVerbs = map[string]string{"patch": "update", "partialUpdate": "update", "batchDelete": "delete"}

// auditRPCPermissions are the permissions of the RPCs that check one their
// name and resource do not spell.
var auditRPCPermissions = map[protoreflect.FullName]string{
	"google.cloud.kms.v1.KeyManagementService.Encrypt":                       "cloudkms.cryptoKeyVersions.useToEncrypt",
	"google.cloud.kms.v1.KeyManagementService.RawEncrypt":                    "cloudkms.cryptoKeyVersions.useToEncrypt",
	"google.cloud.kms.v1.KeyManagementService.Decrypt":                       "cloudkms.cryptoKeyVersions.useToDecrypt",
	"google.cloud.kms.v1.KeyManagementService.RawDecrypt":                    "cloudkms.cryptoKeyVersions.useToDecrypt",
	"google.cloud.kms.v1.KeyManagementService.AsymmetricDecrypt":             "cloudkms.cryptoKeyVersions.useToDecrypt",
	"google.cloud.kms.v1.KeyManagementService.AsymmetricSign":                "cloudkms.cryptoKeyVersions.useToSign",
	"google.cloud.kms.v1.KeyManagementService.MacSign":                       "cloudkms.cryptoKeyVersions.useToSign",
	"google.cloud.kms.v1.KeyManagementService.MacVerify":                     "cloudkms.cryptoKeyVersions.useToVerify",
	"google.cloud.kms.v1.KeyManagementService.Decapsulate":                   "cloudkms.cryptoKeyVersions.useToDecapsulate",
	"google.cloud.kms.v1.KeyManagementService.GetPublicKey":                  "cloudkms.cryptoKeyVersions.viewPublicKey",
	"google.cloud.kms.v1.KeyManagementService.GenerateRandomBytes":           "cloudkms.locations.generateRandomBytes",
	"google.cloud.kms.v1.KeyManagementService.ImportCryptoKeyVersion":        "cloudkms.cryptoKeyVersions.create",
	"google.cloud.kms.v1.KeyManagementService.UpdateCryptoKeyPrimaryVersion": "cloudkms.cryptoKeys.update",
	"google.cloud.secretmanager.v1.SecretManagerService.AddSecretVersion":    "secretmanager.versions.add",
	"google.pubsub.v1.Publisher.DetachSubscription":                          "pubsub.topics.detachSubscription",
	"google.pubsub.v1.Publisher.ListTopicSubscriptions":                      "pubsub.topics.get",
	"google.pubsub.v1.Publisher.ListTopicSnapshots":                          "pubsub.topics.get",
	"google.cloud.run.v2.Builds.SubmitBuild":                                 "run.builds.create",
	"google.cloud.functions.v2.FunctionService.GenerateUploadUrl":            "cloudfunctions.functions.sourceCodeSet",
	"google.cloud.functions.v2.FunctionService.GenerateDownloadUrl":          "cloudfunctions.functions.sourceCodeGet",
	"google.devtools.cloudbuild.v1.CloudBuild.RunBuildTrigger":               "cloudbuild.builds.create",
	"google.devtools.cloudbuild.v1.CloudBuild.RetryBuild":                    "cloudbuild.builds.create",
	"google.devtools.cloudbuild.v1.CloudBuild.CancelBuild":                   "cloudbuild.builds.update",
	"google.spanner.admin.database.v1.DatabaseAdmin.UpdateDatabaseDdl":       "spanner.databases.updateDdl",
	"google.spanner.admin.database.v1.DatabaseAdmin.GetDatabaseDdl":          "spanner.databases.getDdl",
	"google.spanner.admin.database.v1.DatabaseAdmin.RestoreDatabase":         "spanner.backups.restoreDatabase",
	"google.bigtable.admin.v2.BigtableTableAdmin.ModifyColumnFamilies":       "bigtable.tables.update",
	"google.bigtable.admin.v2.BigtableTableAdmin.DropRowRange":               "bigtable.tables.mutateRows",
	"google.firestore.admin.v1.FirestoreAdmin.ExportDocuments":               "datastore.databases.export",
	"google.firestore.admin.v1.FirestoreAdmin.ImportDocuments":               "datastore.databases.import",
	"google.firestore.admin.v1.FirestoreAdmin.RestoreDatabase":               "datastore.backups.restoreDatabase",
	"google.iam.admin.v1.IAM.UploadServiceAccountKey":                        "iam.serviceAccountKeys.create",
	"google.cloud.redis.v1.CloudRedis.GetInstanceAuthString":                 "redis.instances.getAuthString",
	"google.bigtable.admin.v2.BigtableTableAdmin.CreateTableFromSnapshot":    "bigtable.tables.create",
}

// permission is the IAM permission the call checks, spelled the way IAM
// spells it: the service, the collection of the resource the call addresses
// and the method's verb, as in run.services.create or
// secretmanager.versions.access.
func (rpc *auditRPC) permission(request map[string]any, resourceName string) string {
	if permission, ok := auditRPCPermissions[rpc.method.FullName()]; ok {
		return permission
	}
	binding := rpc.auditBindingFor(request)
	if binding == nil {
		return ""
	}
	collection := ""
	resourceVar := func(v auditTemplateVar) bool {
		return v.field == "name" || strings.HasSuffix(v.field, ".name") || v.field == "resource" || v.field == "parent" || v.last > v.first
	}
	for _, v := range binding.vars {
		if !resourceVar(v) {
			continue
		}
		if v.next != "" {
			collection = v.next
		} else {
			collection = auditCollectionOf(auditGetPath(request, auditJSONPath(rpc.method.Input(), v.field)))
		}
		break
	}
	if collection == "" {
		for _, segment := range binding.segments {
			if segment.varIdx < 0 && !auditVersionSegment(segment.literal) {
				collection = segment.literal
			}
		}
	}
	if collection == "" {
		collection = auditCollectionOf(resourceName)
	}
	if collection == "" {
		return ""
	}
	nouns := []string{collection}
	if renamed, ok := auditPermissionCollections[rpc.serviceName][collection]; ok {
		collection = renamed
		nouns = append(nouns, renamed)
	}
	if rpc.serviceName == "artifactregistry.googleapis.com" {
		collection = strings.ToLower(collection)
	}
	verb := rpc.permissionVerb(nouns)
	prefix, ok := auditPermissionPrefixes[rpc.serviceName]
	if !ok {
		prefix, _, _ = strings.Cut(rpc.serviceName, ".")
	}
	return prefix + "." + collection + "." + verb
}

// permissionVerb is the RPC's name without the resource type it acts on, in
// lower camel case: "create" for CreateService, "access" for
// AccessSecretVersion, "list" for ListSecretVersions and "checkConsistency"
// for CheckConsistency.
func (rpc *auditRPC) permissionVerb(collections []string) string {
	name := string(rpc.method.Name())
	nouns := auditResourceTypeNames(rpc.method.ParentFile())
	for _, collection := range collections {
		nouns = append(nouns, collection, auditSingular(collection))
	}
	words := auditCamelWords(name)
	verbWords := words
	for i := 1; i < len(words); i++ {
		suffix := strings.Join(words[i:], "")
		if slices.ContainsFunc(nouns, func(noun string) bool { return strings.EqualFold(suffix, noun) }) {
			verbWords = words[:i]
			break
		}
	}
	verb := strings.ToLower(verbWords[0]) + strings.Join(verbWords[1:], "")
	if mapped, ok := auditPermissionVerbs[verb]; ok {
		return mapped
	}
	return verb
}

// auditResourceTypeNames are the resource types a proto file defines, in
// the singular and the plural: SecretVersion and SecretVersions.
func auditResourceTypeNames(file protoreflect.FileDescriptor) []string {
	var names []string
	add := func(typ string) {
		if _, short, ok := strings.Cut(typ, "/"); ok {
			names = append(names, short, short+"s", short+"es", strings.TrimSuffix(short, "y")+"ies")
		}
	}
	addDefinitions := func(f protoreflect.FileDescriptor) {
		definitions, _ := proto.GetExtension(f.Options(), annotations.E_ResourceDefinition).([]*annotations.ResourceDescriptor)
		for _, d := range definitions {
			add(d.GetType())
		}
	}
	addDefinitions(file)
	var walk func(protoreflect.MessageDescriptors)
	walk = func(messages protoreflect.MessageDescriptors) {
		for i := range messages.Len() {
			m := messages.Get(i)
			if d, _ := proto.GetExtension(m.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor); d != nil {
				add(d.GetType())
			}
			walk(m.Messages())
		}
	}
	walk(file.Messages())
	imports := file.Imports()
	for i := range imports.Len() {
		if imported := imports.Get(i); imported.Package() == file.Package() {
			walk(imported.Messages())
			addDefinitions(imported)
		}
	}
	return names
}

// auditCamelWords splits a CamelCase name at its words, keeping an acronym
// whole: Get, VPCSC, Config for GetVPCSCConfig.
func auditCamelWords(name string) []string {
	var words []string
	start := 0
	for i := 1; i < len(name); i++ {
		upper := name[i] >= 'A' && name[i] <= 'Z'
		prevLower := name[i-1] >= 'a' && name[i-1] <= 'z'
		nextLower := i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z'
		prevUpper := name[i-1] >= 'A' && name[i-1] <= 'Z'
		if upper && (prevLower || (prevUpper && nextLower)) {
			words = append(words, name[start:i])
			start = i
		}
	}
	return append(words, name[start:])
}

// auditSingular is the singular of a collection name.
func auditSingular(plural string) string {
	switch {
	case strings.HasSuffix(plural, "ies"):
		return strings.TrimSuffix(plural, "ies") + "y"
	case strings.HasSuffix(plural, "sses"), strings.HasSuffix(plural, "xes"), strings.HasSuffix(plural, "uses"):
		return strings.TrimSuffix(plural, "es")
	default:
		return strings.TrimSuffix(plural, "s")
	}
}

// auditCollectionOf is the collection a resource name's resource is in:
// "secrets" for projects/p/secrets/s, and a singleton's own name, as
// "googleChannelConfig" for projects/p/locations/l/googleChannelConfig.
func auditCollectionOf(name string) string {
	segments := strings.Split(name, "/")
	if len(segments) < 2 {
		return ""
	}
	if len(segments)%2 == 1 {
		return segments[len(segments)-1]
	}
	return segments[len(segments)-2]
}

// auditOnePlatformResource is the monitored resource an API's audit entries
// name.
func auditOnePlatformResource(rpc *auditRPC, project, resourceName string) *MonitoredResource {
	location := auditLocation(resourceName)
	switch rpc.serviceName {
	case "run.googleapis.com":
		if service := auditPathSegmentAfter(resourceName, "services"); service != "" {
			return &MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{
				"project_id":         project,
				"location":           location,
				"service_name":       service,
				"configuration_name": "",
				"revision_name":      auditPathSegmentAfter(resourceName, "revisions"),
			}}
		}
		if job := auditPathSegmentAfter(resourceName, "jobs"); job != "" {
			return &MonitoredResource{Type: "cloud_run_job", Labels: map[string]string{
				"project_id": project,
				"location":   location,
				"job_name":   job,
			}}
		}
	case "pubsub.googleapis.com":
		if topic := auditPathSegmentAfter(resourceName, "topics"); topic != "" {
			return &MonitoredResource{Type: "pubsub_topic", Labels: map[string]string{"project_id": project, "topic_id": topic}}
		}
		if subscription := auditPathSegmentAfter(resourceName, "subscriptions"); subscription != "" {
			return &MonitoredResource{Type: "pubsub_subscription", Labels: map[string]string{"project_id": project, "subscription_id": subscription}}
		}
	case "cloudkms.googleapis.com":
		keyRing := auditPathSegmentAfter(resourceName, "keyRings")
		cryptoKey := auditPathSegmentAfter(resourceName, "cryptoKeys")
		labels := map[string]string{"project_id": project, "location": location, "key_ring_id": keyRing}
		switch {
		case cryptoKey != "" && auditPathSegmentAfter(resourceName, "cryptoKeyVersions") != "":
			labels["crypto_key_id"] = cryptoKey
			labels["crypto_key_version_id"] = auditPathSegmentAfter(resourceName, "cryptoKeyVersions")
			return &MonitoredResource{Type: "cloudkms_cryptokeyversion", Labels: labels}
		case cryptoKey != "":
			labels["crypto_key_id"] = cryptoKey
			return &MonitoredResource{Type: "cloudkms_cryptokey", Labels: labels}
		case keyRing != "":
			return &MonitoredResource{Type: "cloudkms_keyring", Labels: labels}
		}
	case "spanner.googleapis.com":
		if instance := auditPathSegmentAfter(resourceName, "instances"); instance != "" {
			return &MonitoredResource{Type: "spanner_instance", Labels: map[string]string{
				"project_id":      project,
				"instance_id":     instance,
				"instance_config": "",
				"location":        "",
			}}
		}
	case "cloudfunctions.googleapis.com":
		if function := auditPathSegmentAfter(resourceName, "functions"); function != "" {
			return &MonitoredResource{Type: "cloud_function", Labels: map[string]string{
				"project_id":    project,
				"region":        location,
				"function_name": function,
			}}
		}
	}
	return &MonitoredResource{Type: "audited_resource", Labels: map[string]string{
		"project_id": project,
		"service":    rpc.serviceName,
		"method":     rpc.methodName,
	}}
}

// auditOnePlatformRecord assembles an audited call's record from its request
// and response messages in their JSON form.
func auditOnePlatformRecord(rpc *auditRPC, request, response map[string]any, status map[string]any, caller auditCaller) auditRecord {
	resourceName := rpc.resourceName(request)
	project := resourceProject(resourceName)
	permission := rpc.permission(request, resourceName)
	request["@type"] = "type.googleapis.com/" + string(rpc.method.Input().FullName())
	rec := auditRecord{
		project:      project,
		serviceName:  rpc.serviceName,
		methodName:   rpc.methodName,
		resourceName: resourceName,
		logType:      rpc.logType,
		resource:     auditOnePlatformResource(rpc, project, resourceName),
		location:     auditLocation(resourceName),
		permission:   permission,
		request:      request,
		status:       status,
		caller:       caller,
	}
	output := rpc.method.Output().FullName()
	if response != nil && rpc.logType == auditAdminWrite && output != "google.protobuf.Empty" {
		if output == "google.longrunning.Operation" {
			rec.longRunning = auditLongRunningOperation(response)
		}
		response["@type"] = "type.googleapis.com/" + string(output)
		rec.response = response
	}
	return rec
}
