package main

import (
	"net/http"
	"strings"
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
	"iam.googleapis.com": auditPrepareIAM,
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
// addresses by email or unique ID, under its project or the - wildcard.
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
	for _, sa := range iamServiceAccounts.Filter(func(sa GCPServiceAccount) bool { return sa.UniqueId == id }) {
		return sa, true
	}
	return GCPServiceAccount{}, false
}
