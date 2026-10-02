package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

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

// auditOnePlatformServices are the RPC services whose calls the simulator
// audits.
var auditOnePlatformServices = []protoreflect.FullName{
	"google.cloud.run.v2.Builds",
	"google.cloud.run.v2.Executions",
	"google.cloud.run.v2.Instances",
	"google.cloud.run.v2.Jobs",
	"google.cloud.run.v2.Revisions",
	"google.cloud.run.v2.Services",
	"google.cloud.run.v2.Tasks",
	"google.cloud.run.v2.WorkerPools",
	"google.pubsub.v1.Publisher",
	"google.pubsub.v1.Subscriber",
	"google.pubsub.v1.SchemaService",
	"google.cloud.secretmanager.v1.SecretManagerService",
	"google.devtools.artifactregistry.v1.ArtifactRegistry",
	"google.cloud.functions.v2.FunctionService",
}

// auditDataPlaneLogTypes are the data-plane RPCs and the Data Access log type
// each writes.
var auditDataPlaneLogTypes = map[protoreflect.FullName]string{
	"google.cloud.secretmanager.v1.SecretManagerService.AccessSecretVersion": auditDataRead,
}

// auditUnloggedRPCs are the RPCs of the audited services the simulator writes
// no audit entry for.
var auditUnloggedRPCs = map[protoreflect.FullName]bool{
	"google.pubsub.v1.Publisher.Publish":                 true,
	"google.pubsub.v1.Subscriber.Pull":                   true,
	"google.pubsub.v1.Subscriber.StreamingPull":          true,
	"google.pubsub.v1.Subscriber.Acknowledge":            true,
	"google.pubsub.v1.Subscriber.ModifyAckDeadline":      true,
	"google.pubsub.v1.Subscriber.Seek":                   true,
	"google.cloud.run.v2.Services.TestIamPermissions":    true,
	"google.cloud.run.v2.Jobs.TestIamPermissions":        true,
	"google.cloud.run.v2.WorkerPools.TestIamPermissions": true,
	"google.pubsub.v1.SchemaService.ValidateSchema":      true,
	"google.pubsub.v1.SchemaService.ValidateMessage":     true,
}

// auditRPC is one audited RPC.
type auditRPC struct {
	method      protoreflect.MethodDescriptor
	serviceName string
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
		for _, name := range auditOnePlatformServices {
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
				rpc := &auditRPC{method: m, serviceName: host, logType: logType}
				rule, _ := proto.GetExtension(m.Options(), annotations.E_Http).(*annotations.HttpRule)
				if rule != nil {
					for _, r := range append([]*annotations.HttpRule{rule}, rule.GetAdditionalBindings()...) {
						binding, err := auditCompileBinding(rpc, r)
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
	if auditUnloggedRPCs[m.FullName()] || m.IsStreamingClient() || m.IsStreamingServer() {
		return "", false
	}
	if logType, ok := auditDataPlaneLogTypes[m.FullName()]; ok {
		return logType, true
	}
	name := string(m.Name())
	if strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List") {
		return auditAdminRead, true
	}
	return auditAdminWrite, true
}

func auditCompileBinding(rpc *auditRPC, rule *annotations.HttpRule) (*auditHTTPBinding, error) {
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
	template = template[1:]
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
// path binds.
func auditMatchREST(method, escapedPath string) (*auditHTTPBinding, map[string]string, bool) {
	if err := auditLoadRPCs(); err != nil {
		return nil, nil, false
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	custom := ""
	if last := parts[len(parts)-1]; auditCustomMethodSuffix(last) {
		i := strings.LastIndex(last, ":")
		custom = last[i+1:]
		parts = append(parts[:len(parts)-1:len(parts)-1], last[:i])
	}
	var best *auditHTTPBinding
	var bestVars map[string]string
	for _, b := range auditBindings {
		vars, ok := b.match(method, parts, custom)
		if !ok {
			continue
		}
		if best == nil || b.literals > best.literals {
			best, bestVars = b, vars
		}
	}
	return best, bestVars, best != nil
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

// resourceName is the resource a call names: the resource its name variable
// binds, or for a create the parent, collection and requested ID.
func (rpc *auditRPC) resourceName(request map[string]any) string {
	if len(rpc.bindings) == 0 {
		return ""
	}
	input := rpc.method.Input()
	binding := rpc.bindings[0]
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
	if len(binding.vars) > 0 {
		return auditGetPath(request, auditJSONPath(input, binding.vars[0].field))
	}
	return ""
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
		"method":     string(rpc.method.FullName()),
	}}
}

// auditOnePlatformRecord assembles an audited call's record from its request
// and response messages in their JSON form.
func auditOnePlatformRecord(rpc *auditRPC, request, response map[string]any, status map[string]any, caller auditCaller) auditRecord {
	resourceName := rpc.resourceName(request)
	project := resourceProject(resourceName)
	request["@type"] = "type.googleapis.com/" + string(rpc.method.Input().FullName())
	rec := auditRecord{
		project:      project,
		serviceName:  rpc.serviceName,
		methodName:   string(rpc.method.FullName()),
		resourceName: resourceName,
		logType:      rpc.logType,
		resource:     auditOnePlatformResource(rpc, project, resourceName),
		location:     auditLocation(resourceName),
		request:      request,
		status:       status,
		caller:       caller,
	}
	output := rpc.method.Output().FullName()
	if response != nil && rpc.logType == auditAdminWrite && output != "google.protobuf.Empty" {
		response["@type"] = "type.googleapis.com/" + string(output)
		rec.response = response
	}
	return rec
}
