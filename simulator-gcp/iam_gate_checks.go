package main

import (
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

// What a method requires: the permissions it checks and the resource each is
// checked on. A permission comes from, in order, the method tables Cloud
// Storage and Pub/Sub publish, the tables of gateMethodPermissions, the
// permissions the service's vendored audit-logging page records for the
// method, and Google's service.resource.verb convention over the method's
// RPC or Discovery identifier. It is required only when a served service
// defines it; a method whose permission no source names is unsourced.

// gateExemptMethods need no permission: testIamPermissions answers what the
// caller holds, the IAM catalogs are public, and a project, folder or
// organization search answers only the nodes the caller may see.
var gateExemptMethods = map[string]bool{
	"iam.permissions.queryTestablePermissions":  true,
	"iam.roles.queryGrantableRoles":             true,
	"iam.iamPolicies.lintPolicy":                true,
	"iam.iamPolicies.queryAuditableServices":    true,
	"iam.roles.list":                            true,
	"iam.roles.get":                             true,
	"cloudresourcemanager.projects.list":        true,
	"cloudresourcemanager.projects.search":      true,
	"cloudresourcemanager.organizations.search": true,
	"cloudresourcemanager.folders.search":       true,
}

// gateOverride names the permissions of a method its identifier does not
// spell, from the page cited beside it.
type gateOverride func(call *gateCall, resource string) []gateCheck

func gatePermissions(permissions ...string) gateOverride {
	return func(_ *gateCall, resource string) []gateCheck {
		checks := make([]gateCheck, 0, len(permissions))
		for _, p := range permissions {
			checks = append(checks, gateCheck{permission: p, resource: resource})
		}
		return checks
	}
}

// gateMethodPermissions are the permissions of the methods whose identifiers
// do not spell them.
var gateMethodPermissions = map[string]gateOverride{
	// Firestore: "Required permissions for API methods",
	// firestore-security-iam.md.gz.
	"firestore.projects.databases.documents.get":                 gatePermissions("datastore.entities.get"),
	"firestore.projects.databases.documents.batchGet":            gatePermissions("datastore.entities.get"),
	"firestore.projects.databases.documents.list":                gatePermissions("datastore.entities.get", "datastore.entities.list"),
	"firestore.projects.databases.documents.listDocuments":       gatePermissions("datastore.entities.get", "datastore.entities.list"),
	"firestore.projects.databases.documents.listCollectionIds":   gatePermissions("datastore.entities.list"),
	"firestore.projects.databases.documents.partitionQuery":      gatePermissions("datastore.entities.get", "datastore.entities.list"),
	"firestore.projects.databases.documents.runQuery":            gatePermissions("datastore.entities.get", "datastore.entities.list"),
	"firestore.projects.databases.documents.runAggregationQuery": gatePermissions("datastore.entities.get", "datastore.entities.list"),
	"firestore.projects.databases.documents.createDocument":      gatePermissions("datastore.entities.create"),
	"firestore.projects.databases.documents.patch":               gatePermissions("datastore.entities.update"),
	"firestore.projects.databases.documents.delete":              gatePermissions("datastore.entities.delete"),
	"firestore.projects.databases.documents.beginTransaction":    gatePermissions("datastore.databases.get"),
	"firestore.projects.databases.documents.rollback":            gatePermissions("datastore.databases.get"),
	"firestore.projects.databases.documents.commit":              gateFirestoreWrites,
	"firestore.projects.databases.documents.batchWrite":          gateFirestoreWrites,
	// Spanner data plane: the database and session permissions of
	// spanner-iam.md.gz, which name the call each allows.
	"spanner.projects.instances.databases.sessions.create":              gatePermissions("spanner.sessions.create"),
	"spanner.projects.instances.databases.sessions.batchCreate":         gatePermissions("spanner.sessions.create"),
	"spanner.projects.instances.databases.sessions.get":                 gatePermissions("spanner.sessions.get"),
	"spanner.projects.instances.databases.sessions.list":                gatePermissions("spanner.sessions.list"),
	"spanner.projects.instances.databases.sessions.delete":              gatePermissions("spanner.sessions.delete"),
	"spanner.projects.instances.databases.sessions.executeSql":          gateSpannerDatabase("spanner.databases.select"),
	"spanner.projects.instances.databases.sessions.executeStreamingSql": gateSpannerDatabase("spanner.databases.select"),
	"spanner.projects.instances.databases.sessions.read":                gateSpannerDatabase("spanner.databases.read"),
	"spanner.projects.instances.databases.sessions.streamingRead":       gateSpannerDatabase("spanner.databases.read"),
	"spanner.projects.instances.databases.sessions.executeBatchDml":     gateSpannerDatabase("spanner.databases.write"),
	"spanner.projects.instances.databases.sessions.commit":              gateSpannerDatabase("spanner.databases.write"),
	"spanner.projects.instances.databases.sessions.batchWrite":          gateSpannerDatabase("spanner.databases.write"),
	"spanner.projects.instances.databases.sessions.partitionQuery":      gateSpannerDatabase("spanner.databases.partitionQuery"),
	"spanner.projects.instances.databases.sessions.partitionRead":       gateSpannerDatabase("spanner.databases.partitionRead"),
	"spanner.projects.instances.databases.sessions.beginTransaction":    gateSpannerTransaction,
	"spanner.projects.instances.databases.sessions.rollback":            gateSpannerDatabase("spanner.databases.beginOrRollbackReadWriteTransaction"),
	// Service account impersonation: iam-service-account-impersonation.md.gz
	// for an access token, and the Service Account OpenID Connect Identity
	// Token Creator role of iam-roles-iam.md.gz for an ID token.
	"iamcredentials.projects.serviceAccounts.generateAccessToken": gatePermissions("iam.serviceAccounts.getAccessToken"),
	"iamcredentials.projects.serviceAccounts.generateIdToken":     gatePermissions("iam.serviceAccounts.getOpenIdToken"),
	// Cloud Logging: the Logs Writer role of iam-roles-logging.md.gz writes
	// with logging.logEntries.create.
	"logging.entries.write": gatePermissions("logging.logEntries.create"),
	"logging.entries.tail":  gatePermissions("logging.logEntries.list"),
	// Cloud Billing: billing account permissions are billing.accounts.*, per
	// iam-roles-billing.md.gz.
	"cloudbilling.billingAccounts.get":  gatePermissions("billing.accounts.get"),
	"cloudbilling.billingAccounts.list": gatePermissions("billing.accounts.list"),
	// Resource Manager publishes no permission for polling its operations,
	// and a Project Creator, who holds no operations permission
	// (iam-roles-resourcemanager.md.gz), polls the create it started.
	"cloudresourcemanager.operations.get": gateCRMOperation,
	// Cloud Quotas: the quota adjuster's settings take the permissions of
	// cloudquotas-api-overview.md.gz's "Required permissions", which
	// cloudquotas-audit-logging.md.gz names per method.
	"cloudquotas.projects.locations.quotaAdjusterSettings.getQuotaAdjusterSettings":         gatePermissions("cloudquotas.quotas.get"),
	"cloudquotas.projects.locations.quotaAdjusterSettings.updateQuotaAdjusterSettings":      gatePermissions("cloudquotas.quotas.update"),
	"cloudquotas.folders.locations.quotaAdjusterSettings.getQuotaAdjusterSettings":          gatePermissions("cloudquotas.quotas.get"),
	"cloudquotas.folders.locations.quotaAdjusterSettings.updateQuotaAdjusterSettings":       gatePermissions("cloudquotas.quotas.update"),
	"cloudquotas.organizations.locations.quotaAdjusterSettings.getQuotaAdjusterSettings":    gatePermissions("cloudquotas.quotas.get"),
	"cloudquotas.organizations.locations.quotaAdjusterSettings.updateQuotaAdjusterSettings": gatePermissions("cloudquotas.quotas.update"),
}

// gateCRMOperation authorizes a Resource Manager operation poll as a read of
// the project or folder the operation produced.
func gateCRMOperation(_ *gateCall, resource string) []gateCheck {
	if crOperations == nil {
		return nil
	}
	op, ok := crOperations.Get(resource)
	if !ok {
		// The handler answers an operation that does not exist with 404.
		return nil
	}
	response, _ := op.Response.(map[string]any)
	if id, _ := response["projectId"].(string); id != "" {
		return []gateCheck{{permission: "resourcemanager.projects.get", resource: "projects/" + id}}
	}
	name, _ := response["name"].(string)
	switch {
	case strings.HasPrefix(name, "projects/"):
		return []gateCheck{{permission: "resourcemanager.projects.get", resource: name}}
	case strings.HasPrefix(name, "folders/"):
		return []gateCheck{{permission: "resourcemanager.folders.get", resource: name}}
	}
	return []gateCheck{{unsourced: true, resource: resource}}
}

// gateFirestoreWrites are the permissions of a commit or batch write: a
// delete needs datastore.entities.delete, a write that requires the document
// not to exist creates it, one that requires it to exist updates it, and one
// without a precondition may do either.
func gateFirestoreWrites(call *gateCall, resource string) []gateCheck {
	needed := map[string]bool{}
	writes, _ := call.fields()["writes"].([]any)
	for _, w := range writes {
		write, _ := w.(map[string]any)
		if _, ok := write["delete"]; ok {
			needed["datastore.entities.delete"] = true
			continue
		}
		precondition, _ := write["currentDocument"].(map[string]any)
		exists, set := precondition["exists"].(bool)
		switch {
		case set && !exists:
			needed["datastore.entities.create"] = true
		case set && exists:
			needed["datastore.entities.update"] = true
		default:
			needed["datastore.entities.create"] = true
			needed["datastore.entities.update"] = true
		}
	}
	var checks []gateCheck
	for _, p := range []string{"datastore.entities.create", "datastore.entities.update", "datastore.entities.delete"} {
		if needed[p] {
			checks = append(checks, gateCheck{permission: p, resource: resource})
		}
	}
	return checks
}

// gateSpannerDatabase checks a session call's permission on the session's
// database.
func gateSpannerDatabase(permission string) gateOverride {
	return func(_ *gateCall, resource string) []gateCheck {
		database, _, _ := strings.Cut(resource, "/sessions/")
		return []gateCheck{{permission: permission, resource: database}}
	}
}

func gateSpannerTransaction(call *gateCall, resource string) []gateCheck {
	options, _ := call.fields()["options"].(map[string]any)
	permission := "spanner.databases.beginOrRollbackReadWriteTransaction"
	if _, ok := options["readOnly"]; ok {
		permission = "spanner.databases.beginReadOnlyTransaction"
	}
	return gateSpannerDatabase(permission)(call, resource)
}

// gateSpannerOperationPermission is the permission of a Spanner long-running
// operations method: spanner-iam.md.gz names each parent's operations their
// own collection, spanner.databaseOperations.get for a database's.
func gateSpannerOperationPermission(id string) (string, bool) {
	parts := strings.Split(id, ".")
	if len(parts) < 4 || parts[0] != "spanner" || parts[len(parts)-2] != "operations" {
		return "", false
	}
	parent := auditSingular(parts[len(parts)-3])
	return gcpCanonicalPermission("spanner." + parent + "Operations." + parts[len(parts)-1])
}

// gatePermissionPrefixes are the permission prefixes of the APIs whose
// permissions do not start with their Discovery name.
var gatePermissionPrefixes = map[string]string{
	"firestore":            "datastore",
	"bigtableadmin":        "bigtable",
	"cloudresourcemanager": "resourcemanager",
	"sqladmin":             "cloudsql",
	"iamcredentials":       "iam",
	"cloudbilling":         "billing",
}

// gateConventionVerbs are the Discovery method names whose permission verb
// differs.
var gateConventionVerbs = map[string]string{
	"insert": "create", "bulkInsert": "create", "patch": "update", "aggregatedList": "list",
	"batchDelete": "delete", "replaceService": "update", "replaceJob": "update",
}

func gateChecks(call *gateCall, c gateDiscoveryMatch) []gateCheck {
	id := c.method.id
	if strings.HasSuffix(id, ".testIamPermissions") || gateExemptMethods[id] {
		return nil
	}
	if c.method.api.name == "storage" {
		return gateStorageChecks(call, c)
	}
	resource, exempt := gateResource(call, c)
	if exempt {
		return nil
	}
	if reqs, ok := gcpCatalog().methods[id]; ok {
		return gateTableChecks(call, resource, reqs)
	}
	if override, ok := gateMethodPermissions[id]; ok {
		return override(call, resource)
	}
	if p, ok := gateSpannerOperationPermission(id); ok {
		return []gateCheck{{permission: p, resource: resource}}
	}
	for _, candidate := range gateCandidatePermissions(call, c, resource) {
		if p, ok := gcpCanonicalPermission(candidate); ok {
			return []gateCheck{{permission: p, resource: resource}}
		}
	}
	return []gateCheck{{unsourced: true, resource: resource}}
}

// gateCandidatePermissions are the permissions a method may check, most
// authoritative first: the service's vendored audit-logging page, then the
// convention over its RPC, then over its Discovery identifier.
func gateCandidatePermissions(call *gateCall, c gateDiscoveryMatch, resource string) []string {
	var out []string
	collection := auditCollectionOf(resource)
	if vendored, ok := gateVendored(call, c); ok {
		out = append(out, gatePickVendored(vendored, collection)...)
	}
	rpc, request := call.rpc, call.fields
	if rpc == nil {
		var binding *auditHTTPBinding
		var vars map[string]string
		rpc, binding, vars = gateMatchRPC(c.method.api.host, call.httpMethod, call.path)
		if rpc != nil {
			fields := map[string]any{}
			for field, value := range vars {
				auditSetPath(fields, auditJSONPath(rpc.method.Input(), field), value)
			}
			for key, values := range call.query {
				if len(values) == 1 {
					auditSetPath(fields, strings.Split(key, "."), values[0])
				}
			}
			_ = binding
			request = func() map[string]any { return fields }
		}
	}
	if rpc != nil {
		if vendored, ok := auditVendoredLookup(rpc.serviceName, rpc.vendoredKey); ok {
			out = append(out, gatePickVendored(vendored, collection)...)
		}
		out = append(out, rpc.permission(request(), resource))
	}
	return append(append(out, gateConventionPermission(c)), gateNestedConventionPermissions(c)...)
}

// gateNestedConventionPermissions name a nested collection's permissions the
// way Google does, by the singular collections above it: providers under
// workforcePools are iam.workforcePoolProviders.*, their keys
// iam.workforcePoolProviderKeys.* (iam-roles-iam.md.gz). Innermost first; only a
// name the catalog defines counts.
func gateNestedConventionPermissions(c gateDiscoveryMatch) []string {
	parts := strings.Split(c.method.id, ".")
	if len(parts) < 4 {
		return nil
	}
	verb := parts[len(parts)-1]
	if mapped, ok := gateConventionVerbs[verb]; ok {
		verb = mapped
	}
	prefix := c.method.api.name
	if mapped, ok := gatePermissionPrefixes[prefix]; ok {
		prefix = mapped
	}
	var collections []string
	for _, part := range parts[1 : len(parts)-1] {
		switch part {
		case "projects", "locations", "organizations", "folders":
			continue
		}
		collections = append(collections, part)
	}
	var out []string
	for n := 2; n <= len(collections); n++ {
		nested := collections[len(collections)-n:]
		name := gateSingular(nested[0])
		for i, collection := range nested[1:] {
			if i < len(nested)-2 {
				collection = gateSingular(collection)
			}
			name += strings.ToUpper(collection[:1]) + collection[1:]
		}
		out = append(out, prefix+"."+name+"."+verb)
	}
	return out
}

func gateSingular(collection string) string {
	if stem, ok := strings.CutSuffix(collection, "ies"); ok {
		return stem + "y"
	}
	return strings.TrimSuffix(collection, "s")
}

// gateVendored is the vendored audit-logging entry of a method the page names
// by an interface the method does not carry: Discovery-defined APIs, and the
// mixins every One Platform API serves.
func gateVendored(call *gateCall, c gateDiscoveryMatch) (auditVendoredMethod, bool) {
	id := c.method.id
	parts := strings.Split(id, ".")
	verb := parts[len(parts)-1]
	collection := ""
	if len(parts) >= 3 {
		collection = parts[len(parts)-2]
	}
	host := c.method.api.host
	mixins := map[string]string{
		"operations.get": "google.longrunning.Operations.GetOperation", "operations.list": "google.longrunning.Operations.ListOperations",
		"operations.delete": "google.longrunning.Operations.DeleteOperation", "operations.cancel": "google.longrunning.Operations.CancelOperation",
		"operations.wait": "google.longrunning.Operations.WaitOperation", "locations.list": "google.cloud.location.Locations.ListLocations",
		"locations.get": "google.cloud.location.Locations.GetLocation",
	}
	if key, ok := mixins[collection+"."+verb]; ok {
		if m, found := auditVendoredLookup(host, key); found {
			return m, true
		}
	}
	switch c.method.api.name {
	case "bigquery":
		return auditVendoredRPC(host, "google.cloud.bigquery.v2", auditSingular(collection)+"Service", verb, collection)
	case "sqladmin":
		return auditVendoredLookup("cloudsql.googleapis.com", "cloudsql."+collection+"."+auditDiscoveryVerb(verb, collection))
	case "run":
		if strings.HasPrefix(id, "run.namespaces.") || strings.HasPrefix(id, "run.projects.locations.") && !strings.Contains(call.path, "/v2/") {
			return auditVendoredRPC(host, "google.cloud.run.v1", collection, verb, collection)
		}
	}
	return auditVendoredMethod{}, false
}

// gatePickVendored orders a vendored entry's permissions with the ones on the
// addressed resource's own collection first.
func gatePickVendored(m auditVendoredMethod, collection string) []string {
	var own, other []string
	for _, p := range m.Permissions {
		parts := strings.Split(p.Permission, ".")
		if len(parts) == 3 && strings.EqualFold(parts[1], collection) {
			own = append(own, p.Permission)
		} else {
			other = append(other, p.Permission)
		}
	}
	return append(own, other...)
}

// gateConventionPermission spells a Discovery method's permission by Google's
// service.resource.verb convention: run.projects.locations.services.delete
// checks run.services.delete.
func gateConventionPermission(c gateDiscoveryMatch) string {
	parts := strings.Split(c.method.id, ".")
	if len(parts) < 3 {
		return ""
	}
	verb, collection := parts[len(parts)-1], parts[len(parts)-2]
	if mapped, ok := gateConventionVerbs[verb]; ok {
		verb = mapped
	}
	prefix := c.method.api.name
	if mapped, ok := gatePermissionPrefixes[prefix]; ok {
		prefix = mapped
	}
	return prefix + "." + collection + "." + verb
}

// gateResource is the relative name of the resource a method checks its
// permission on, and whether the call needs none. A create names its parent
// in the request where its path does not.
func gateResource(call *gateCall, c gateDiscoveryMatch) (string, bool) {
	id := c.method.id
	resource := c.relative
	switch {
	case id == "cloudresourcemanager.projects.create":
		parent := gateCRMParent(call.fields()["parent"])
		return parent, parent == ""
	case id == "cloudresourcemanager.folders.create":
		parent := call.query.Get("parent")
		if parent == "" {
			parent = gateCRMParent(call.fields()["parent"])
		}
		return parent, false
	case strings.HasPrefix(id, "cloudresourcemanager.") && (strings.HasSuffix(id, ".list") || strings.HasSuffix(id, ".create")) &&
		!strings.Contains(resource, "/"):
		if parent := call.query.Get("parent"); parent != "" {
			return parent, false
		}
		if parent, _ := call.fields()["parent"].(string); parent != "" {
			if _, path, ok := strings.Cut(strings.TrimPrefix(parent, "//"), "/"); ok && strings.HasPrefix(parent, "//") {
				return path, false
			}
			return parent, false
		}
	case id == "iam.locations.workforcePools.list":
		if parent := call.query.Get("parent"); parent != "" {
			return parent, false
		}
	case id == "iam.locations.workforcePools.create":
		if parent, _ := call.fields()["parent"].(string); parent != "" {
			return parent, false
		}
	case id == "logging.entries.write":
		if name, _ := call.fields()["logName"].(string); name != "" {
			return gateLogParent(name), false
		}
	case id == "logging.entries.list":
		if names, _ := call.fields()["resourceNames"].([]any); len(names) > 0 {
			name, _ := names[0].(string)
			return name, false
		}
	case strings.HasPrefix(id, "iam.roles."):
		// A predefined role is public.
		if strings.HasPrefix(resource, "roles") {
			return resource, true
		}
	}
	return resource, false
}

// gateCRMParent is the hierarchy node a Resource Manager request names as a
// new resource's parent, in its v1 or v3 spelling.
func gateCRMParent(value any) string {
	switch parent := value.(type) {
	case string:
		return parent
	case map[string]any:
		kind, _ := parent["type"].(string)
		id, _ := parent["id"].(string)
		if kind != "" && id != "" {
			return kind + "s/" + id
		}
	}
	return ""
}

func gateLogParent(logName string) string {
	parent, _, _ := strings.Cut(logName, "/logs/")
	return parent
}

// gateTableChecks resolves the targets of a method table's requirements.
func gateTableChecks(call *gateCall, resource string, reqs []gcpPermissionRequirement) []gateCheck {
	var checks []gateCheck
	for _, req := range reqs {
		check := gateCheck{permission: req.Permission, resource: resource}
		if _, known := gcpCanonicalPermission(req.Permission); !known {
			check.unsourced = true
		}
		switch {
		case req.On == "project":
			check.resource = gateProjectNode(resource)
		case strings.HasPrefix(req.On, "field:"):
			named, _ := call.fields()[strings.TrimPrefix(req.On, "field:")].(string)
			if named == "" {
				if req.Optional {
					continue
				}
				named = resource
			}
			check.resource = named
		case req.On == "attached:topic" && psSubscriptions != nil:
			if sub, ok := psSubscriptions.Get(resource); ok && sub.Topic != "" {
				check.resource = sub.Topic
			}
		}
		checks = append(checks, check)
	}
	return checks
}

// gateProjectNode is the project a resource lives in.
func gateProjectNode(resource string) string {
	if project := resourceProject(resource); project != "" {
		return "projects/" + project
	}
	return resource
}

// gateStorageChecks resolves a Cloud Storage JSON method through the table
// Cloud Storage publishes, onto the bucket or object each permission is
// checked on.
func gateStorageChecks(call *gateCall, c gateDiscoveryMatch) []gateCheck {
	reqs, ok := gcpCatalog().methods[c.method.id]
	if !ok {
		permission, sourced := gcpCanonicalPermission(gateConventionPermission(c))
		reqs = []gcpPermissionRequirement{{Permission: permission}}
		if !sourced {
			return []gateCheck{{unsourced: true, resource: c.relative, bucket: c.params["bucket"]}}
		}
	}
	p := c.params
	var checks []gateCheck
	for _, req := range reqs {
		if strings.HasPrefix(req.Permission, "storage.anywhereCaches.") && !strings.HasPrefix(c.method.id, "storage.anywhereCaches.") {
			// The table lists the Anywhere Cache permission without a
			// footnote for buckets.list and buckets.update, where only the
			// bucket's Anywhere Cache fields need it.
			continue
		}
		check := gateCheck{permission: req.Permission}
		if _, known := gcpCanonicalPermission(req.Permission); !known {
			// The method table names permissions no vendored role page
			// lists yet; only an Owner holds those.
			check.unsourced = true
		}
		switch req.On {
		case "source":
			check.bucket, check.object = gateFirst(p["sourceBucket"], p["bucket"]), p["sourceObject"]
		case "destination":
			check.bucket, check.object = gateFirst(p["destinationBucket"], p["bucket"]), p["destinationObject"]
		default:
			check.bucket = gateFirst(p["bucket"], p["destinationBucket"])
			if strings.HasPrefix(req.Permission, "storage.objects.") && !slices.Contains([]string{"create", "list"}, gateVerb(req.Permission)) {
				check.object = gateFirst(p["object"], p["destinationObject"])
			}
		}
		if check.bucket == "" {
			project := gateFirst(call.query.Get("project"), p["projectId"])
			check.resource = "projects/" + project
		} else {
			check.resource = "projects/_/buckets/" + check.bucket
			if check.object != "" {
				check.resource += "/objects/" + check.object
			}
		}
		checks = append(checks, check)
	}
	return checks
}

func gateFirst(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func gateVerb(permission string) string {
	return permission[strings.LastIndex(permission, ".")+1:]
}

func gateErrorInfoProto(failed gateCheck) *errdetails.ErrorInfo {
	return &errdetails.ErrorInfo{
		Reason:   "IAM_PERMISSION_DENIED",
		Domain:   "iam.googleapis.com",
		Metadata: map[string]string{"permission": failed.permission, "resource": failed.resource},
	}
}
