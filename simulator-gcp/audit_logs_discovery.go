package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	_ "google.golang.org/genproto/googleapis/iam/v1/logging"
)

// The audit logs of the APIs Google defines by their Discovery documents
// rather than by RPCs: Compute Engine records each call under its version and
// Discovery method ID, such as v1.compute.instances.insert, and Cloud DNS
// under the method ID alone, such as dns.changes.create.

// auditDiscoveryAPI is a Discovery-defined API whose calls the simulator
// audits.
type auditDiscoveryAPI struct {
	file        string
	serviceName string
	// servicePath is the path the API's method paths are relative to.
	servicePath string
	record      func(call *auditDiscoveryCall) (auditRecord, bool)
}

// auditDiscoveryMethod is one method of a Discovery document.
type auditDiscoveryMethod struct {
	api      *auditDiscoveryAPI
	id       string
	verb     string
	segments []string
	literals int
	// requestRef and responseRef are the schemas of the request body and the
	// response.
	requestRef  string
	responseRef string
}

// auditDiscoveryCall is an audited call in flight.
type auditDiscoveryCall struct {
	method   *auditDiscoveryMethod
	vars     map[string]string
	path     []string
	body     map[string]any
	response map[string]any
	status   map[string]any
}

var auditDiscoveryAPIs = []*auditDiscoveryAPI{
	{file: "discovery/compute-v1.discovery.json.gz", serviceName: "compute.googleapis.com", record: auditComputeRecord},
	{file: "discovery/dns-v1.discovery.json.gz", serviceName: "dns.googleapis.com", record: auditDNSRecord},
}

var (
	auditDiscoveryOnce    sync.Once
	auditDiscoveryMethods map[string][]*auditDiscoveryMethod
	auditDiscoveryErr     error
)

func auditLoadDiscovery() error {
	auditDiscoveryOnce.Do(func() {
		auditDiscoveryMethods = map[string][]*auditDiscoveryMethod{}
		for _, api := range auditDiscoveryAPIs {
			raw, err := readDiscoveryDocument(api.file)
			if err != nil {
				auditDiscoveryErr = err
				return
			}
			type method struct {
				ID         string `json:"id"`
				HTTPMethod string `json:"httpMethod"`
				Path       string `json:"path"`
				FlatPath   string `json:"flatPath"`
				Request    struct {
					Ref string `json:"$ref"`
				} `json:"request"`
				Response struct {
					Ref string `json:"$ref"`
				} `json:"response"`
			}
			type resource struct {
				Methods   map[string]method   `json:"methods"`
				Resources map[string]resource `json:"resources"`
			}
			var doc struct {
				ServicePath string              `json:"servicePath"`
				Resources   map[string]resource `json:"resources"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				auditDiscoveryErr = fmt.Errorf("audit logs: %s: %w", api.file, err)
				return
			}
			api.servicePath = doc.ServicePath
			var walk func(map[string]resource)
			walk = func(resources map[string]resource) {
				for _, r := range resources {
					for _, m := range r.Methods {
						path := m.FlatPath
						if path == "" {
							path = m.Path
						}
						dm := &auditDiscoveryMethod{
							api: api, id: m.ID, verb: m.HTTPMethod,
							segments:    strings.Split(doc.ServicePath+path, "/"),
							requestRef:  m.Request.Ref,
							responseRef: m.Response.Ref,
						}
						for _, s := range dm.segments {
							if !strings.HasPrefix(s, "{") {
								dm.literals++
							}
						}
						key := fmt.Sprintf("%s %d", dm.verb, len(dm.segments))
						auditDiscoveryMethods[key] = append(auditDiscoveryMethods[key], dm)
					}
					walk(r.Resources)
				}
			}
			walk(doc.Resources)
		}
	})
	return auditDiscoveryErr
}

// auditMatchDiscovery is the Discovery method a REST call reaches, and the
// path parameters it binds.
func auditMatchDiscovery(method, path string) (*auditDiscoveryMethod, map[string]string, []string, bool) {
	if auditLoadDiscovery() != nil {
		return nil, nil, nil, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var best *auditDiscoveryMethod
	var bestVars map[string]string
	for _, m := range auditDiscoveryMethods[fmt.Sprintf("%s %d", method, len(parts))] {
		vars := map[string]string{}
		ok := true
		for i, s := range m.segments {
			if name, isVar := strings.CutPrefix(s, "{"); isVar {
				if parts[i] == "" {
					ok = false
					break
				}
				vars[strings.TrimSuffix(name, "}")] = parts[i]
			} else if s != parts[i] {
				ok = false
				break
			}
		}
		if ok && (best == nil || m.literals > best.literals) {
			best, bestVars = m, vars
		}
	}
	return best, bestVars, parts, best != nil
}

// auditResolveDiscovery resolves a call to a Discovery-defined API.
func auditResolveDiscovery(r *http.Request) (*auditPending, bool) {
	m, vars, parts, ok := auditMatchDiscovery(r.Method, r.URL.Path)
	if !ok || strings.HasSuffix(m.id, ".testIamPermissions") {
		return nil, false
	}
	label, _, _ := strings.Cut(m.api.serviceName, ".")
	write := r.Method != http.MethodGet
	return &auditPending{
		serviceLabel:    label,
		captureRequest:  write,
		captureResponse: write,
		complete: func(status int, requestBody, responseBody []byte) (auditRecord, bool) {
			call := &auditDiscoveryCall{method: m, vars: vars, path: parts, status: auditRESTStatus(status, responseBody)}
			if len(requestBody) > 0 {
				_ = json.Unmarshal(requestBody, &call.body)
			}
			if status < 300 && len(responseBody) > 0 {
				_ = json.Unmarshal(responseBody, &call.response)
			}
			return m.api.record(call)
		},
	}, true
}

// methodParts splits a method ID into its resource and method: "instances"
// and "insert" for compute.instances.insert.
func (m *auditDiscoveryMethod) methodParts() (resource, method string) {
	parts := strings.Split(m.id, ".")
	return strings.Join(parts[1:len(parts)-1], "."), parts[len(parts)-1]
}

// auditComputeVerbs are the Compute Engine methods whose permission verb
// differs from the method's name.
var auditComputeVerbs = map[string]string{
	"insert":         "create",
	"bulkInsert":     "create",
	"aggregatedList": "list",
	"patch":          "update",
}

// auditComputeResourceTypes are the monitored resource types of Compute
// Engine's audit entries, and the label carrying the resource's ID.
var auditComputeResourceTypes = map[string]struct{ typ, idLabel string }{
	"instances":             {"gce_instance", "instance_id"},
	"disks":                 {"gce_disk", "disk_id"},
	"regionDisks":           {"gce_disk", "disk_id"},
	"networks":              {"gce_network", "network_id"},
	"subnetworks":           {"gce_subnetwork", "subnetwork_id"},
	"firewalls":             {"gce_firewall_rule", "firewall_rule_id"},
	"routes":                {"gce_route", "route_id"},
	"routers":               {"gce_router", "router_id"},
	"addresses":             {"gce_reserved_address", "reserved_address_id"},
	"globalAddresses":       {"gce_reserved_address", "reserved_address_id"},
	"forwardingRules":       {"gce_forwarding_rule", "forwarding_rule_id"},
	"globalForwardingRules": {"gce_forwarding_rule", "forwarding_rule_id"},
	"backendServices":       {"gce_backend_service", "backend_service_id"},
	"regionBackendServices": {"gce_backend_service", "backend_service_id"},
	"instanceTemplates":     {"gce_instance_template", "instance_template_id"},
	"instanceGroups":        {"gce_instance_group", "instance_group_id"},
	"instanceGroupManagers": {"gce_instance_group_manager", "instance_group_manager_id"},
	"images":                {"gce_image", "image_id"},
	"snapshots":             {"gce_snapshot", "snapshot_id"},
	"urlMaps":               {"gce_url_map", "url_map_id"},
	"healthChecks":          {"gce_health_check", "health_check_id"},
	"sslCertificates":       {"gce_ssl_certificate", "ssl_certificate_id"},
	"targetHttpProxies":     {"gce_target_http_proxy", "target_http_proxy_id"},
	"targetHttpsProxies":    {"gce_target_https_proxy", "target_https_proxy_id"},
}

// auditComputeRecord is a Compute Engine call's entry. A write answers a
// compute#operation, whose targetLink and targetId name the resource, and its
// entries carry the operation's name.
func auditComputeRecord(call *auditDiscoveryCall) (auditRecord, bool) {
	m := call.method
	resourceType, method := m.methodParts()
	project := call.vars["project"]
	if project == "" {
		return auditRecord{}, false
	}
	rel := call.path[strings.Count(m.api.servicePath, "/"):]
	if !slices.Contains([]string{"get", "list", "aggregatedList", "insert", "delete", "patch", "update"}, method) &&
		len(rel) > 0 && rel[len(rel)-1] == method {
		rel = rel[:len(rel)-1]
	}
	resourceName := strings.Join(rel, "/")
	if method == "insert" {
		if name, _ := call.body["name"].(string); name != "" {
			resourceName += "/" + name
		}
	}
	targetID := ""
	if call.response != nil && call.response["kind"] == "compute#operation" {
		if link, _ := call.response["targetLink"].(string); link != "" {
			if _, rest, ok := strings.Cut(link, "/compute/v1/"); ok {
				resourceName = rest
			}
		}
		targetID, _ = call.response["targetId"].(string)
	}
	verb := method
	if mapped, ok := auditComputeVerbs[method]; ok {
		verb = mapped
	}
	logType := auditAdminWrite
	if m.verb == http.MethodGet {
		logType = auditAdminRead
	}
	requestType := "type.googleapis.com/compute." + resourceType + "." + method
	request := map[string]any{"@type": requestType}
	for k, v := range call.body {
		request[k] = v
	}
	location, labels := "global", map[string]string{"project_id": project}
	switch {
	case call.vars["zone"] != "":
		labels["zone"] = call.vars["zone"]
		location = auditZoneRegion(call.vars["zone"])
	case call.vars["region"] != "":
		labels["region"] = call.vars["region"]
		location = call.vars["region"]
	}
	resource := &MonitoredResource{Type: "gce_project", Labels: map[string]string{"project_id": project}}
	if t, ok := auditComputeResourceTypes[resourceType]; ok {
		labels[t.idLabel] = targetID
		resource = &MonitoredResource{Type: t.typ, Labels: labels}
	}
	rec := auditRecord{
		project:      project,
		serviceName:  m.api.serviceName,
		methodName:   "v1." + m.id,
		resourceName: resourceName,
		logType:      logType,
		permission:   "compute." + resourceType + "." + verb,
		resource:     resource,
		location:     location,
		request:      request,
		status:       call.status,
	}
	if logType == auditAdminRead {
		rec.request = nil
	}
	if call.response != nil && call.response["kind"] == "compute#operation" {
		response := map[string]any{"@type": "type.googleapis.com/operation"}
		for k, v := range call.response {
			response[k] = v
		}
		rec.response = response
		name, _ := call.response["name"].(string)
		state, _ := call.response["status"].(string)
		rec.longRunning = &auditOperation{id: name, done: state == "DONE", settled: func() (map[string]any, map[string]any, bool) {
			op, ok := computeOpRegistry.Get(name)
			if !ok || op.Status != "DONE" {
				return nil, nil, false
			}
			if op.ErrorCode != "" {
				return nil, auditRESTStatus(op.HTTPErrorStatusCode, nil), true
			}
			return nil, auditStatus(0, ""), true
		}}
	}
	return rec, true
}

// auditZoneRegion is the region a zone is in: us-central1 for us-central1-a.
func auditZoneRegion(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

// auditDNSRecord is a Cloud DNS call's entry. Cloud DNS names the resource
// relative to the project, as managedZones/{zone}, and records the request as
// its cloud.dns.api request message.
func auditDNSRecord(call *auditDiscoveryCall) (auditRecord, bool) {
	m := call.method
	resourceType, method := m.methodParts()
	project := call.vars["project"]
	if project == "" {
		return auditRecord{}, false
	}
	// dns/v1/projects/{project}/{collection}/{name}/...
	rel := call.path[4:]
	resourceName := "projects/" + project
	switch {
	case len(rel) >= 2:
		resourceName = rel[0] + "/" + rel[1]
	case len(rel) == 1 && method == "create":
		if name, _ := call.body["name"].(string); name != "" {
			resourceName = rel[0] + "/" + name
		}
	}
	verb := method
	if method == "patch" {
		verb = "update"
	}
	logType := auditAdminWrite
	if m.verb == http.MethodGet {
		logType = auditAdminRead
	}
	request := map[string]any{"@type": "type.googleapis.com/cloud.dns.api." + auditTitle(strings.ReplaceAll(resourceType, ".", "")) + auditTitle(method) + "Request"}
	keys := make([]string, 0, len(call.vars))
	for k := range call.vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		request[k] = call.vars[k]
	}
	if call.body != nil && m.requestRef != "" {
		request[strings.ToLower(m.requestRef[:1])+m.requestRef[1:]] = call.body
	}
	resource := &MonitoredResource{Type: "audited_resource", Labels: map[string]string{
		"project_id": project,
		"service":    m.api.serviceName,
		"method":     m.id,
	}}
	if zone := call.vars["managedZone"]; zone != "" || (resourceType == "managedZones" && method == "create") {
		if zone == "" {
			zone, _ = call.body["name"].(string)
		}
		resource = &MonitoredResource{Type: "dns_managed_zone", Labels: map[string]string{
			"project_id": project,
			"zone_name":  zone,
			"location":   "global",
		}}
	}
	rec := auditRecord{
		project:      project,
		serviceName:  m.api.serviceName,
		methodName:   m.id,
		resourceName: resourceName,
		logType:      logType,
		permission:   "dns." + resourceType + "." + verb,
		resource:     resource,
		location:     "global",
		request:      request,
		status:       call.status,
	}
	if call.response != nil && logType == auditAdminWrite && m.responseRef != "" {
		response := map[string]any{"@type": "type.googleapis.com/cloud.dns.api." + m.responseRef}
		for k, v := range call.response {
			response[k] = v
		}
		rec.response = response
	}
	return rec, true
}

func auditTitle(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// auditCRMMethod is a Cloud Resource Manager v1 project method, which the
// service records under its own name.
type auditCRMMethod struct {
	methodName string
	permission string
	logType    string
}

// auditCRMProjectMethods are the v1 project methods by HTTP method and the
// custom method the path carries, "" for none.
var auditCRMProjectMethods = map[string]auditCRMMethod{
	"GET ":              {"GetProject", "resourcemanager.projects.get", auditAdminRead},
	"PUT ":              {"UpdateProject", "resourcemanager.projects.update", auditAdminWrite},
	"DELETE ":           {"DeleteProject", "resourcemanager.projects.delete", auditAdminWrite},
	"POST undelete":     {"UndeleteProject", "resourcemanager.projects.undelete", auditAdminWrite},
	"POST getIamPolicy": {"GetIamPolicy", "resourcemanager.projects.getIamPolicy", auditAdminRead},
	"POST setIamPolicy": {"SetIamPolicy", "resourcemanager.projects.setIamPolicy", auditAdminWrite},
	"POST getAncestry":  {"GetAncestry", "resourcemanager.projects.get", auditAdminRead},
}

// auditResolveCRM resolves a Cloud Resource Manager v1 project call. A
// SetIamPolicy entry carries the policy delta the call made, so it reads the
// policy before the call replaces it.
func auditResolveCRM(r *http.Request) (*auditPending, bool) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/projects")
	if !ok {
		return nil, false
	}
	var project, custom string
	m := auditCRMMethod{"CreateProject", "resourcemanager.projects.create", auditAdminWrite}
	switch {
	case rest == "" && r.Method == http.MethodPost:
	case strings.HasPrefix(rest, "/") && !strings.Contains(rest[1:], "/"):
		project, custom, _ = strings.Cut(rest[1:], ":")
		known, ok := auditCRMProjectMethods[r.Method+" "+custom]
		if !ok {
			return nil, false
		}
		m = known
	default:
		return nil, false
	}
	var before IAMPolicy
	if m.methodName == "SetIamPolicy" && gcpProjectPolicies != nil {
		before, _ = gcpProjectPolicies.Get("project/" + project)
	}
	return &auditPending{
		serviceLabel:    "cloudresourcemanager",
		captureRequest:  m.logType == auditAdminWrite,
		captureResponse: m.logType == auditAdminWrite,
		complete: func(status int, requestBody, responseBody []byte) (auditRecord, bool) {
			var body map[string]any
			if len(requestBody) > 0 {
				_ = json.Unmarshal(requestBody, &body)
			}
			if project == "" {
				project, _ = body["projectId"].(string)
			}
			if project == "" {
				return auditRecord{}, false
			}
			resourceName := "projects/" + project
			rec := auditRecord{
				project:      project,
				serviceName:  "cloudresourcemanager.googleapis.com",
				methodName:   m.methodName,
				resourceName: resourceName,
				logType:      m.logType,
				permission:   m.permission,
				resource:     &MonitoredResource{Type: "project", Labels: map[string]string{"project_id": project}},
				location:     "global",
				status:       auditRESTStatus(status, responseBody),
			}
			if m.logType == auditAdminWrite {
				request := map[string]any{}
				for k, v := range body {
					request[k] = v
				}
				rec.request = request
				if m.methodName != "CreateProject" {
					request["resource"] = project
				}
				if status < 300 && len(responseBody) > 0 {
					var response map[string]any
					if json.Unmarshal(responseBody, &response) == nil {
						rec.response = response
					}
				}
			}
			if m.methodName == "SetIamPolicy" && status < 300 && gcpProjectPolicies != nil {
				request := rec.request
				request["@type"] = "type.googleapis.com/google.iam.v1.SetIamPolicyRequest"
				if rec.response != nil {
					rec.response["@type"] = "type.googleapis.com/google.iam.v1.Policy"
				}
				after, _ := gcpProjectPolicies.Get("project/" + project)
				rec.serviceData = map[string]any{
					"@type":       "type.googleapis.com/google.iam.v1.logging.AuditData",
					"policyDelta": auditPolicyDelta(before, after),
				}
			}
			return rec, true
		},
	}, true
}

// auditPolicyDelta is the google.iam.v1.PolicyDelta between two policies:
// the members each role gained and lost, and the audit log types each
// service's configuration gained and lost.
func auditPolicyDelta(before, after IAMPolicy) map[string]any {
	members := func(p IAMPolicy) map[[2]string]bool {
		out := map[[2]string]bool{}
		for _, b := range p.Bindings {
			for _, m := range b.Members {
				out[[2]string{b.Role, m}] = true
			}
		}
		return out
	}
	logTypes := func(p IAMPolicy) map[[2]string]bool {
		out := map[[2]string]bool{}
		for _, c := range p.AuditConfigs {
			for _, l := range c.AuditLogConfigs {
				out[[2]string{c.Service, l.LogType}] = true
			}
		}
		return out
	}
	diff := func(from, to map[[2]string]bool, action string, add func(action string, key [2]string)) {
		keys := make([][2]string, 0, len(to))
		for k := range to {
			if !from[k] {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
		})
		for _, k := range keys {
			add(action, k)
		}
	}
	var bindingDeltas, auditConfigDeltas []any
	addBinding := func(action string, k [2]string) {
		bindingDeltas = append(bindingDeltas, map[string]any{"action": action, "role": k[0], "member": k[1]})
	}
	addAudit := func(action string, k [2]string) {
		auditConfigDeltas = append(auditConfigDeltas, map[string]any{"action": action, "service": k[0], "logType": k[1]})
	}
	beforeMembers, afterMembers := members(before), members(after)
	diff(afterMembers, beforeMembers, "REMOVE", addBinding)
	diff(beforeMembers, afterMembers, "ADD", addBinding)
	beforeTypes, afterTypes := logTypes(before), logTypes(after)
	diff(afterTypes, beforeTypes, "REMOVE", addAudit)
	diff(beforeTypes, afterTypes, "ADD", addAudit)
	delta := map[string]any{}
	if len(bindingDeltas) > 0 {
		delta["bindingDeltas"] = bindingDeltas
	}
	if len(auditConfigDeltas) > 0 {
		delta["auditConfigDeltas"] = auditConfigDeltas
	}
	return delta
}
