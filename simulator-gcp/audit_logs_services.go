package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// auditRESTClaims resolve a REST call to the API that serves it when two APIs
// publish its path and the call names no host: each claims the calls the
// simulator's own router hands it.
var auditRESTClaims = map[string]func(*http.Request) bool{
	"cloudbuild.googleapis.com": auditCloudBuildClaims,
}

// auditChooseCandidate is the API that serves a call whose path several
// publish: the one its host names, else the one whose claim takes it, else
// the one that claims nothing.
func auditChooseCandidate(r *http.Request, candidates []auditRESTCandidate) (auditRESTCandidate, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	if host := gcpServiceFromHost(r); host != "" {
		for _, c := range candidates {
			if label, _, _ := strings.Cut(c.binding.rpc.serviceName, "."); label == host {
				return c, true
			}
		}
		return auditRESTCandidate{}, false
	}
	for _, c := range candidates {
		if claim := auditRESTClaims[c.binding.rpc.serviceName]; claim != nil && claim(r) {
			return c, true
		}
	}
	for _, c := range candidates {
		if auditRESTClaims[c.binding.rpc.serviceName] == nil {
			return c, true
		}
	}
	return auditRESTCandidate{}, false
}

// auditCloudBuildClaims takes the calls to the regional triggers collection
// Cloud Build and Eventarc share that the simulator routes to Cloud Build.
func auditCloudBuildClaims(r *http.Request) bool {
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	// v1/projects/{p}/locations/{l}/triggers[/{t}]
	if len(segments) < 6 || segments[1] != "projects" || segments[3] != "locations" || segments[5] != "triggers" {
		return false
	}
	if len(segments) == 6 && r.Method == http.MethodPost {
		return r.URL.Query().Get("triggerId") == ""
	}
	project, location := segments[2], segments[4]
	if len(segments) > 6 {
		trigger, _, _ := strings.Cut(segments[6], ":")
		if _, ok := eventarcTriggers.Get(eventarcTriggerKey(project, location, trigger)); ok {
			return false
		}
		if _, ok := cbTriggers.Get(buildTriggerKey(project, location, trigger)); ok {
			return true
		}
	}
	return location == "global"
}

// auditPrepareHooks read, before a REST call runs, what its entry needs from
// state the call may change, and return what completes the entry after it.
var auditPrepareHooks = map[string]func(vars map[string]string) func(*auditRecord, map[string]any){
	"iam.googleapis.com":                  auditPrepareIAM,
	"logging.googleapis.com":              auditPrepareLogging,
	"cloudresourcemanager.googleapis.com": auditPrepareCRMv3,
}

// auditPrepareLogging names the resource a Cloud Logging call concerns where
// its path does not: a ListLogEntries entry lands on the first resource the
// read names, and a sink, exclusion or metric is created under the name its
// body carries.
func auditPrepareLogging(map[string]string) func(*auditRecord, map[string]any) {
	return func(rec *auditRecord, _ map[string]any) {
		switch rec.methodName {
		case "google.logging.v2.LoggingServiceV2.ListLogEntries":
			names, _ := rec.request["resourceNames"].([]any)
			if len(names) == 0 {
				return
			}
			name, _ := names[0].(string)
			rec.resourceName = name
			rec.project = resourceProject(name)
			rec.resource.Labels["project_id"] = rec.project
		case "google.logging.v2.ConfigServiceV2.CreateSink":
			auditLoggingCreatedUnder(rec, "sink", "sinks")
		case "google.logging.v2.ConfigServiceV2.CreateExclusion":
			auditLoggingCreatedUnder(rec, "exclusion", "exclusions")
		case "google.logging.v2.MetricsServiceV2.CreateLogMetric":
			auditLoggingCreatedUnder(rec, "metric", "metrics")
		}
	}
}

func auditLoggingCreatedUnder(rec *auditRecord, field, collection string) {
	body, _ := rec.request[field].(map[string]any)
	name, _ := body["name"].(string)
	if name != "" && !strings.Contains(rec.resourceName, "/"+collection+"/") {
		rec.resourceName += "/" + collection + "/" + name
	}
}

// auditPrepareCRMv3 places a Resource Manager v3 entry in the project it
// concerns: a project's own, a tag key's or value's project parent, and the
// project of the resource a tag binding is bound to. The binding a delete
// removes is read before the call runs.
func auditPrepareCRMv3(vars map[string]string) func(*auditRecord, map[string]any) {
	var boundParent string
	if name, ok := vars["name"]; ok && strings.HasPrefix(name, "tagBindings/") && crmTagBindings != nil {
		if binding, found := crmTagBindings.Get(name); found {
			boundParent = binding.Parent
		}
	}
	return func(rec *auditRecord, response map[string]any) {
		switch {
		case strings.HasPrefix(rec.resourceName, "projects/") || rec.methodName == "CreateProject":
			if rec.methodName == "CreateProject" {
				if project, _ := rec.request["project"].(map[string]any); project != nil {
					if id, _ := project["projectId"].(string); id != "" {
						rec.resourceName = "projects/" + id
					}
				}
			}
			rec.project = auditCanonicalProject(resourceProject(rec.resourceName))
			rec.resource = &MonitoredResource{Type: "project", Labels: map[string]string{"project_id": rec.project}}
			return
		case strings.Contains(rec.methodName, "TagKey"), strings.Contains(rec.methodName, "TagValue"):
			parent := auditCRMTagParent(rec)
			rec.project = auditCRMProjectOf(parent)
		case strings.Contains(rec.methodName, "TagBinding"):
			parent := boundParent
			if binding, _ := rec.request["tagBinding"].(map[string]any); binding != nil {
				if p, _ := binding["parent"].(string); p != "" {
					parent = p
				}
			}
			_, path, _ := strings.Cut(strings.TrimPrefix(parent, "//"), "/")
			rec.project = auditCRMProjectOf(path)
			if created, _ := response["response"].(map[string]any); created != nil {
				if name, _ := created["name"].(string); name != "" {
					rec.resourceName = name
				}
			}
		default:
			return
		}
		if rec.resourceName == "" {
			if created, _ := response["response"].(map[string]any); created != nil {
				rec.resourceName, _ = created["name"].(string)
			}
		}
		rec.resource.Labels["project_id"] = rec.project
	}
}

// auditCRMTagParent is the hierarchy node a tag key or value belongs under:
// the key's own parent, which a value reaches through its key.
func auditCRMTagParent(rec *auditRecord) string {
	if key, _ := rec.request["tagKey"].(map[string]any); key != nil {
		if parent, _ := key["parent"].(string); parent != "" {
			return parent
		}
	}
	keyName := rec.resourceName
	if value, _ := rec.request["tagValue"].(map[string]any); value != nil {
		if parent, _ := value["parent"].(string); parent != "" {
			keyName = parent
		}
	}
	if strings.HasPrefix(keyName, "tagValues/") && crmTagValues != nil {
		if value, ok := crmTagValues.Get(keyName); ok {
			keyName = value.Parent
		}
	}
	if strings.HasPrefix(keyName, "tagKeys/") && crmTagKeys != nil {
		if key, ok := crmTagKeys.Get(keyName); ok {
			return key.Parent
		}
	}
	return ""
}

// auditCRMProjectOf is the ID of the project a hierarchy node or resource
// path lies in, "" for a folder, an organization or nothing.
func auditCRMProjectOf(node string) string {
	project := resourceProject(node)
	if project == "" || project == "_" {
		return ""
	}
	return auditCanonicalProject(project)
}

// auditPrepareIAM resolves the service account an IAM call addresses: IAM
// names it projects/-/serviceAccounts/{uniqueId} in its entries, under the
// service_account monitored resource, and the account is gone once a delete
// has run.
func auditPrepareIAM(vars map[string]string) func(*auditRecord, map[string]any) {
	var account *GCPServiceAccount
	for _, field := range []string{"name", "resource"} {
		if sa, ok := auditLookupServiceAccount(vars[field]); ok {
			account = &sa
			break
		}
	}
	return func(rec *auditRecord, response map[string]any) {
		if account == nil && rec.methodName == "google.iam.admin.v1.CreateServiceAccount" && response != nil {
			created := GCPServiceAccount{}
			created.Email, _ = response["email"].(string)
			created.UniqueId, _ = response["uniqueId"].(string)
			created.ProjectId, _ = response["projectId"].(string)
			if created.UniqueId != "" {
				account = &created
			}
			if account != nil {
				rec.resource = auditServiceAccountResource(*account)
			}
			return
		}
		if account == nil {
			return
		}
		_, rest, _ := strings.Cut(rec.resourceName, "/serviceAccounts/")
		_, tail, hasTail := strings.Cut(rest, "/")
		rec.resourceName = "projects/-/serviceAccounts/" + account.UniqueId
		if hasTail {
			rec.resourceName += "/" + tail
		}
		rec.project = account.ProjectId
		rec.resource = auditServiceAccountResource(*account)
	}
}

func auditServiceAccountResource(sa GCPServiceAccount) *MonitoredResource {
	return &MonitoredResource{Type: "service_account", Labels: map[string]string{
		"email_id":   sa.Email,
		"project_id": sa.ProjectId,
		"unique_id":  sa.UniqueId,
	}}
}

// auditLookupServiceAccount finds the service account a resource name
// addresses by email or unique ID, under its project or the - wildcard; a
// unique ID also finds a deleted account, which undelete addresses.
var iamServiceAccountsByUniqueID sim.GenerationIndex[GCPServiceAccount]

func auditLookupServiceAccount(name string) (GCPServiceAccount, bool) {
	if iamServiceAccounts == nil {
		return GCPServiceAccount{}, false
	}
	_, rest, ok := strings.Cut(name, "/serviceAccounts/")
	if !ok {
		return GCPServiceAccount{}, false
	}
	id, _, _ := strings.Cut(rest, "/")
	if strings.Contains(id, "@") {
		return iamServiceAccounts.Get("projects/" + gcpProjectFromEmail(id) + "/serviceAccounts/" + id)
	}
	if sa, ok := iamServiceAccountsByUniqueID.Lookup(iamServiceAccounts, id,
		func(sa GCPServiceAccount) []string { return []string{sa.UniqueId} }); ok {
		return sa, true
	}
	if deleted, ok := iamDeletedServiceAccounts.Get(id); ok {
		return deleted.Account, true
	}
	return GCPServiceAccount{}, false
}
