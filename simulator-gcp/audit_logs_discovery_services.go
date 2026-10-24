package main

import (
	"net"
	"strings"
)

// The Discovery-defined services whose audited methods come from their
// vendored audit-logging pages: Cloud Run Admin v1, BigQuery, Cloud SQL and
// Dataflow. Each resolves a Discovery method to the method the page lists and
// writes the entry under that methodName, category and permission; a method
// the page does not list writes nothing.

// auditDiscoveryRelative is a call's path after its API version, the last
// segment without a custom method's verb: namespaces/ns/services/s for
// apis/serving.knative.dev/v1/namespaces/ns/services/s, and projects/p/jobs/j
// for v1/projects/p/jobs/j:run.
func auditDiscoveryRelative(call *auditDiscoveryCall) []string {
	parts := call.path
	for i, segment := range parts {
		if auditVersionSegment(segment) || segment == "v1beta4" {
			parts = parts[i+1:]
			break
		}
	}
	rel := make([]string, len(parts))
	copy(rel, parts)
	if len(rel) > 0 {
		rel[len(rel)-1], _, _ = strings.Cut(rel[len(rel)-1], ":")
	}
	return rel
}

// auditCanonicalProject is a project's ID for a project named by its ID or
// its number; the audit log of a project is read by its ID.
func auditCanonicalProject(ref string) string {
	if crmProjects != nil {
		if p, ok := crmResolveProject(ref); ok {
			return p.ProjectId
		}
	}
	return ref
}

// auditDiscoveryVerb is the IAM verb of a Discovery method: insert creates,
// patch updates, and an RPC-styled method such as Backups.CreateBackup drops
// the resource it names.
func auditDiscoveryVerb(method, collection string) string {
	words := auditCamelWords(method)
	for i := 1; i < len(words); i++ {
		suffix := strings.Join(words[i:], "")
		if strings.EqualFold(suffix, collection) || strings.EqualFold(suffix, auditSingular(collection)) {
			words = words[:i]
			break
		}
	}
	verb := strings.ToLower(words[0][:1]) + words[0][1:] + strings.Join(words[1:], "")
	switch verb {
	case "insert":
		return "create"
	case "patch":
		return "update"
	}
	return verb
}

// auditVendoredRecord is an audited call's record under the method its
// service's page lists.
func auditVendoredRecord(call *auditDiscoveryCall, vendored auditVendoredMethod, project, resourceName, location string, resource *MonitoredResource) auditRecord {
	request := map[string]any{}
	for k, v := range call.vars {
		request[k] = v
	}
	for k, v := range call.body {
		request[k] = v
	}
	rec := auditRecord{
		project:      project,
		serviceName:  vendored.Service,
		methodName:   vendored.Method,
		resourceName: resourceName,
		logType:      vendored.LogType,
		permission:   vendored.permission(),
		resource:     resource,
		location:     location,
		request:      request,
		status:       call.status,
	}
	if rec.logType != auditAdminWrite {
		rec.request = nil
	} else if call.response != nil {
		rec.response = call.response
	}
	return rec
}

// auditRunResource is the monitored resource of a Cloud Run service's or
// job's audit entry.
func auditRunResource(project, location, resourceName string) *MonitoredResource {
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
	return nil
}

// auditRunV1Record is a Cloud Run Admin v1 call's entry: the Knative-shaped
// API records google.cloud.run.v1.<Collection>.<Rpc>, such as
// google.cloud.run.v1.Services.ReplaceService for services.replaceService.
// The region is the one the regional endpoint names.
func auditRunV1Record(call *auditDiscoveryCall) (auditRecord, bool) {
	m := call.method
	resourceType, verb := m.methodParts()
	collection := resourceType[strings.LastIndex(resourceType, ".")+1:]
	vendored, ok := auditVendoredRPC(m.api.serviceName, "google.cloud.run.v1", collection, verb, collection)
	if !ok {
		return auditRecord{}, false
	}
	rel := auditDiscoveryRelative(call)
	if len(rel) < 2 {
		return auditRecord{}, false
	}
	resourceName := strings.Join(rel, "/")
	if verb == "create" {
		if metadata, _ := call.body["metadata"].(map[string]any); metadata != nil {
			if name, _ := metadata["name"].(string); name != "" {
				resourceName += "/" + name
			}
		}
	}
	project := auditCanonicalProject(rel[1])
	location := call.vars["locationsId"]
	if location == "" {
		host, _, err := net.SplitHostPort(call.host)
		if err != nil {
			host = call.host
		}
		if region, ok := strings.CutSuffix(host, "-run.googleapis.com"); ok {
			location = region
		}
	}
	if location == "" {
		location = "global"
	}
	resource := auditRunResource(project, location, resourceName)
	if resource == nil {
		resource = &MonitoredResource{Type: "audited_resource", Labels: map[string]string{
			"project_id": project, "service": vendored.Service, "method": vendored.Method,
		}}
	}
	return auditVendoredRecord(call, vendored, project, resourceName, location, resource), true
}

// auditBigQueryRecord is a BigQuery call's entry, under the
// google.cloud.bigquery.v2.<Resource>Service.<Rpc> method its page lists,
// such as google.cloud.bigquery.v2.TableService.InsertTable for
// tables.insert. A dataset's or table's entry names the bigquery_dataset
// resource, any other the bigquery_project.
func auditBigQueryRecord(call *auditDiscoveryCall) (auditRecord, bool) {
	m := call.method
	collection, verb := m.methodParts()
	vendored, ok := auditVendoredRPC(m.api.serviceName, "google.cloud.bigquery.v2", auditSingular(collection)+"Service", verb, collection)
	if !ok {
		return auditRecord{}, false
	}
	rel := auditDiscoveryRelative(call)
	if len(rel) < 2 || rel[0] != "projects" {
		return auditRecord{}, false
	}
	project := rel[1]
	resourceName := strings.Join(rel, "/")
	if verb == "insert" {
		for _, reference := range []struct{ field, id string }{
			{"datasetReference", "datasetId"}, {"tableReference", "tableId"}, {"routineReference", "routineId"},
			{"modelReference", "modelId"}, {"jobReference", "jobId"},
		} {
			if ref, _ := call.body[reference.field].(map[string]any); ref != nil {
				if id, _ := ref[reference.id].(string); id != "" {
					resourceName += "/" + id
				}
				break
			}
		}
	}
	location := "global"
	if l, _ := call.body["location"].(string); l != "" {
		location = strings.ToLower(l)
	}
	resource := &MonitoredResource{Type: "bigquery_project", Labels: map[string]string{"project_id": project, "location": ""}}
	if dataset := auditPathSegmentAfter(resourceName, "datasets"); dataset != "" {
		resource = &MonitoredResource{Type: "bigquery_dataset", Labels: map[string]string{"project_id": project, "dataset_id": dataset}}
	}
	return auditVendoredRecord(call, vendored, project, resourceName, location, resource), true
}

// auditCloudSQLRecord is a Cloud SQL Admin call's entry, under the
// cloudsql.<collection>.<verb> method its page lists, such as
// cloudsql.instances.create for instances.insert. An operation the call
// answered carries its entry, ended when the operation settles.
func auditCloudSQLRecord(call *auditDiscoveryCall) (auditRecord, bool) {
	m := call.method
	collection, method := m.methodParts()
	collection = strings.ToLower(collection[:1]) + collection[1:]
	vendored, ok := auditVendoredLookup(m.api.serviceName, "cloudsql."+collection+"."+auditDiscoveryVerb(method, collection))
	if !ok {
		return auditRecord{}, false
	}
	rel := auditDiscoveryRelative(call)
	if len(rel) < 2 || rel[0] != "projects" {
		return auditRecord{}, false
	}
	project := rel[1]
	resourceName := strings.Join(rel, "/")
	if method == "insert" {
		if name, _ := call.body["name"].(string); name != "" {
			resourceName += "/" + name
		}
	}
	instance := auditPathSegmentAfter(resourceName, "instances")
	region, _ := call.body["region"].(string)
	if region == "" && sqlInstances != nil {
		if stored, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); ok {
			region = stored.Region
		}
	}
	location := region
	if location == "" {
		location = "global"
	}
	resource := &MonitoredResource{Type: "cloudsql_database", Labels: map[string]string{
		"project_id": project, "database_id": instance, "region": region,
	}}
	rec := auditVendoredRecord(call, vendored, project, resourceName, location, resource)
	if call.response != nil && call.response["kind"] == "sql#operation" {
		name, _ := call.response["name"].(string)
		state, _ := call.response["status"].(string)
		rec.longRunning = &auditOperation{id: name, done: state == "DONE", settled: func() (map[string]any, map[string]any, bool) {
			op, ok := sqlOperations.Get(sqlOperationKey(project, name))
			if !ok || op.Status != "DONE" {
				return nil, nil, false
			}
			if op.Error != nil && len(op.Error.Errors) > 0 {
				return nil, auditStatus(13, op.Error.Errors[0].Message), true
			}
			return nil, auditStatus(0, ""), true
		}}
	}
	return rec, true
}

// auditDataflowRecord is a Dataflow call's entry: the page lists
// dataflow.jobs.create for a job's creation and dataflow.jobs.cancel for the
// update that requests JOB_STATE_CANCELLED.
func auditDataflowRecord(call *auditDiscoveryCall) (auditRecord, bool) {
	m := call.method
	collection, method := m.methodParts()
	if !strings.HasSuffix(collection, "jobs") {
		return auditRecord{}, false
	}
	key := ""
	switch method {
	case "create":
		key = "dataflow.jobs.create"
	case "update":
		if state, _ := call.body["requestedState"].(string); state == "JOB_STATE_CANCELLED" {
			key = "dataflow.jobs.cancel"
		}
	}
	vendored, ok := auditVendoredLookup(m.api.serviceName, key)
	if !ok {
		return auditRecord{}, false
	}
	project := call.vars["projectId"]
	location := call.vars["location"]
	if location == "" {
		location, _ = call.body["location"].(string)
	}
	jobID := call.vars["jobId"]
	if jobID == "" && call.response != nil {
		jobID, _ = call.response["id"].(string)
	}
	resourceName := "projects/" + project
	if location != "" {
		resourceName += "/locations/" + location
	}
	resourceName += "/jobs"
	if jobID != "" {
		resourceName += "/" + jobID
	}
	if location == "" {
		location = "global"
	}
	resource := &MonitoredResource{Type: "audited_resource", Labels: map[string]string{
		"project_id": project, "service": vendored.Service, "method": vendored.Method,
	}}
	return auditVendoredRecord(call, vendored, project, resourceName, location, resource), true
}
