package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Logging admin resource families (logging/v2). Every admin resource
// (sinks, exclusions, buckets+views+links, savedQueries, recentQueries,
// logScopes, settings, cmekSettings, logs) is exposed by real Cloud Logging
// under FIVE parent scopes — projects/{p}, organizations/{o},
// billingAccounts/{b}, folders/{f}, and a generic top-level form. The sim
// mounts each family under the four concrete scope paths so the parent's full
// resource name (e.g. "organizations/123/locations/global/buckets/b") round-
// trips exactly as the cloud returns it. Sinks and metrics for the project
// scope are mounted in logging.go; the remaining scopes' sinks land here.

// loggingScope describes one parent scope: the mux path-parameter name that
// captures the scope identifier and the parent-name prefix it builds.
type loggingScope struct {
	// pathParam is the {param} name in the mux pattern (e.g. "project").
	pathParam string
	// collection is the scope collection segment ("projects",
	// "organizations", "billingAccounts", "folders").
	collection string
}

// loggingScopes are the four concrete parent scopes the sim mounts. The
// generic {v2Id}/{v2Id1} and {+parent} Discovery spellings are alternate
// templates of these same methods; mounting the concrete scopes covers the
// real client paths.
var loggingScopes = []loggingScope{
	{pathParam: "project", collection: "projects"},
	{pathParam: "org", collection: "organizations"},
	{pathParam: "billing", collection: "billingAccounts"},
	{pathParam: "folder", collection: "folders"},
}

// loggingAdmin* stores keyed by full resource name.
var (
	logExclusions   sim.Store[LogExclusion]
	logBuckets      sim.Store[LogBucket]
	logViews        sim.Store[LogView]
	logLinks        sim.Store[LogLink]
	logSavedQueries sim.Store[SavedQuery]
	logScopes       sim.Store[LogScope]
	logSettings     sim.Store[LoggingSettings]
	logOperations   sim.Store[Operation]
)

// LogExclusion is a logging/v2 LogExclusion resource.
type LogExclusion struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Filter      string `json:"filter,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
}

// LogBucket is a logging/v2 LogBucket resource.
type LogBucket struct {
	Name             string         `json:"name,omitempty"`
	Description      string         `json:"description,omitempty"`
	RetentionDays    int64          `json:"retentionDays,omitempty"`
	Locked           bool           `json:"locked,omitempty"`
	LifecycleState   string         `json:"lifecycleState,omitempty"`
	AnalyticsEnabled bool           `json:"analyticsEnabled,omitempty"`
	RestrictedFields []string       `json:"restrictedFields,omitempty"`
	IndexConfigs     []any          `json:"indexConfigs,omitempty"`
	CmekSettings     map[string]any `json:"cmekSettings,omitempty"`
	CreateTime       string         `json:"createTime,omitempty"`
	UpdateTime       string         `json:"updateTime,omitempty"`
}

// LogView is a logging/v2 LogView resource.
type LogView struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Filter      string `json:"filter,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
}

// LogLink is a logging/v2 Link resource (BigQuery analytics link).
type LogLink struct {
	Name            string         `json:"name,omitempty"`
	Description     string         `json:"description,omitempty"`
	LifecycleState  string         `json:"lifecycleState,omitempty"`
	BigqueryDataset map[string]any `json:"bigqueryDataset,omitempty"`
	CreateTime      string         `json:"createTime,omitempty"`
}

// SavedQuery is a logging/v2 SavedQuery resource.
type SavedQuery struct {
	Name              string         `json:"name,omitempty"`
	DisplayName       string         `json:"displayName,omitempty"`
	Description       string         `json:"description,omitempty"`
	LoggingQuery      map[string]any `json:"loggingQuery,omitempty"`
	OpsAnalyticsQuery map[string]any `json:"opsAnalyticsQuery,omitempty"`
	Visibility        string         `json:"visibility,omitempty"`
	CreateTime        string         `json:"createTime,omitempty"`
	UpdateTime        string         `json:"updateTime,omitempty"`
}

// LogScope is a logging/v2 LogScope resource.
type LogScope struct {
	Name          string   `json:"name,omitempty"`
	Description   string   `json:"description,omitempty"`
	ResourceNames []string `json:"resourceNames,omitempty"`
	CreateTime    string   `json:"createTime,omitempty"`
	UpdateTime    string   `json:"updateTime,omitempty"`
}

// LoggingSettings is a logging/v2 Settings resource (per-scope settings).
type LoggingSettings struct {
	Name                    string         `json:"name,omitempty"`
	KmsKeyName              string         `json:"kmsKeyName,omitempty"`
	KmsServiceAccountId     string         `json:"kmsServiceAccountId,omitempty"`
	StorageLocation         string         `json:"storageLocation,omitempty"`
	DisableDefaultSink      bool           `json:"disableDefaultSink,omitempty"`
	DefaultSinkConfig       map[string]any `json:"defaultSinkConfig,omitempty"`
	LoggingServiceAccountId string         `json:"loggingServiceAccountId,omitempty"`
}

func registerCloudLoggingAdmin(srv *sim.Server) {
	logExclusions = sim.MakeStore[LogExclusion](srv.DB(), "logging_exclusions")
	logBuckets = sim.MakeStore[LogBucket](srv.DB(), "logging_buckets")
	logViews = sim.MakeStore[LogView](srv.DB(), "logging_views")
	logLinks = sim.MakeStore[LogLink](srv.DB(), "logging_links")
	logSavedQueries = sim.MakeStore[SavedQuery](srv.DB(), "logging_saved_queries")
	logScopes = sim.MakeStore[LogScope](srv.DB(), "logging_log_scopes")
	logSettings = sim.MakeStore[LoggingSettings](srv.DB(), "logging_settings")
	logOperations = sim.MakeStore[Operation](srv.DB(), "logging_operations")

	// Top-level (scope-independent) routes.
	srv.HandleFunc("POST /v2/entries:copy", handleLoggingEntriesCopy)
	srv.HandleFunc("POST /v2/entries:tail", handleLoggingEntriesTail)
	srv.HandleFunc("GET /v2/monitoredResourceDescriptors", handleLoggingListMRD)

	for _, sc := range loggingScopes {
		p := "/v2/" + sc.collection + "/{" + sc.pathParam + "}"

		// Sinks (project sinks are registered in logging.go; the project
		// handlers are project-scoped, so the other scopes use the
		// parent-aware variants below).
		if sc.collection != "projects" {
			srv.HandleFunc("POST "+p+"/sinks", handleLoggingScopeCreateSink)
			srv.HandleFunc("GET "+p+"/sinks", handleLoggingScopeListSinks)
			srv.HandleFunc("GET "+p+"/sinks/{sink}", handleLoggingScopeGetSink)
			srv.HandleFunc("PUT "+p+"/sinks/{sink}", handleLoggingScopeUpdateSink)
			srv.HandleFunc("PATCH "+p+"/sinks/{sink}", handleLoggingScopeUpdateSink)
			srv.HandleFunc("DELETE "+p+"/sinks/{sink}", handleLoggingScopeDeleteSink)
		}

		// Exclusions.
		srv.HandleFunc("POST "+p+"/exclusions", handleLoggingCreateExclusion)
		srv.HandleFunc("GET "+p+"/exclusions", handleLoggingListExclusions)
		srv.HandleFunc("GET "+p+"/exclusions/{exclusion}", handleLoggingGetExclusion)
		srv.HandleFunc("PATCH "+p+"/exclusions/{exclusion}", handleLoggingPatchExclusion)
		srv.HandleFunc("DELETE "+p+"/exclusions/{exclusion}", handleLoggingDeleteExclusion)

		// Logs.
		srv.HandleFunc("GET "+p+"/logs", handleLoggingListLogs)
		srv.HandleFunc("DELETE "+p+"/logs/{log}", handleLoggingDeleteLog)

		// Settings / cmekSettings.
		srv.HandleFunc("GET "+p+"/settings", handleLoggingGetSettings)
		srv.HandleFunc("PATCH "+p+"/settings", handleLoggingUpdateSettings)
		srv.HandleFunc("GET "+p+"/cmekSettings", handleLoggingGetCmekSettings)
		srv.HandleFunc("PATCH "+p+"/cmekSettings", handleLoggingUpdateCmekSettings)

		// Locations. The project-scope locations.list collides with Cloud Run /
		// Cloud Functions (same /v2 path) and is served there. The project-scope
		// locations.get is intentionally NOT mounted: a literal
		// "GET .../locations/{location}" route also fans in to Cloud Run's
		// "locations/{location}:exportProjectMetadata" colon-verb method, which
		// would inflate that service's coverage. The other scopes' locations.get
		// has no such sibling, so it mounts cleanly.
		if sc.collection != "projects" {
			srv.HandleFunc("GET "+p+"/locations", handleLoggingListLocations)
			srv.HandleFunc("GET "+p+"/locations/{location}", handleLoggingGetLocation)
		}

		loc := p + "/locations/{location}"

		// Buckets.
		srv.HandleFunc("POST "+loc+"/buckets", handleLoggingCreateBucket)
		srv.HandleFunc("POST "+loc+"/buckets:createAsync", handleLoggingCreateBucketAsync)
		srv.HandleFunc("GET "+loc+"/buckets", handleLoggingListBuckets)
		srv.HandleFunc("GET "+loc+"/buckets/{bucket}", handleLoggingGetBucket)
		srv.HandleFunc("PATCH "+loc+"/buckets/{bucket}", handleLoggingPatchBucket)
		srv.HandleFunc("DELETE "+loc+"/buckets/{bucket}", handleLoggingDeleteBucket)
		// Colon-verb fan-in on the bucket id: "{bucket}:undelete" /
		// "{bucket}:updateAsync".
		srv.HandleFunc("POST "+loc+"/buckets/{bucketAction}", handleLoggingBucketAction)

		// Views.
		bkt := loc + "/buckets/{bucket}"
		srv.HandleFunc("POST "+bkt+"/views", handleLoggingCreateView)
		srv.HandleFunc("GET "+bkt+"/views", handleLoggingListViews)
		srv.HandleFunc("GET "+bkt+"/views/{view}", handleLoggingGetView)
		srv.HandleFunc("GET "+bkt+"/views/{view}/logs", handleLoggingListViewLogs)
		srv.HandleFunc("PATCH "+bkt+"/views/{view}", handleLoggingPatchView)
		srv.HandleFunc("DELETE "+bkt+"/views/{view}", handleLoggingDeleteView)
		// View IAM colon verbs.
		srv.HandleFunc("POST "+bkt+"/views/{viewAction}", handleLoggingViewIAM)

		// Links.
		srv.HandleFunc("POST "+bkt+"/links", handleLoggingCreateLink)
		srv.HandleFunc("GET "+bkt+"/links", handleLoggingListLinks)
		srv.HandleFunc("GET "+bkt+"/links/{link}", handleLoggingGetLink)
		srv.HandleFunc("DELETE "+bkt+"/links/{link}", handleLoggingDeleteLink)

		// SavedQueries.
		srv.HandleFunc("POST "+loc+"/savedQueries", handleLoggingCreateSavedQuery)
		srv.HandleFunc("GET "+loc+"/savedQueries", handleLoggingListSavedQueries)
		srv.HandleFunc("GET "+loc+"/savedQueries/{savedQuery}", handleLoggingGetSavedQuery)
		srv.HandleFunc("PATCH "+loc+"/savedQueries/{savedQuery}", handleLoggingPatchSavedQuery)
		srv.HandleFunc("DELETE "+loc+"/savedQueries/{savedQuery}", handleLoggingDeleteSavedQuery)

		// RecentQueries (list only).
		srv.HandleFunc("GET "+loc+"/recentQueries", handleLoggingListRecentQueries)

		// LogScopes (folders/organizations/projects in the Discovery doc).
		if sc.collection != "billingAccounts" {
			srv.HandleFunc("POST "+loc+"/logScopes", handleLoggingCreateLogScope)
			srv.HandleFunc("GET "+loc+"/logScopes", handleLoggingListLogScopes)
			srv.HandleFunc("GET "+loc+"/logScopes/{logScope}", handleLoggingGetLogScope)
			srv.HandleFunc("PATCH "+loc+"/logScopes/{logScope}", handleLoggingPatchLogScope)
			srv.HandleFunc("DELETE "+loc+"/logScopes/{logScope}", handleLoggingDeleteLogScope)
		}

		// Operations. The project-scope list/get/cancel collide with the
		// shared Cloud Run operations routes (same /v2 path) and are served
		// there; mount the other scopes' operations here.
		if sc.collection != "projects" {
			srv.HandleFunc("GET "+loc+"/operations", handleLoggingListOperations)
			srv.HandleFunc("GET "+loc+"/operations/{operation}", handleLoggingGetOperation)
			srv.HandleFunc("POST "+loc+"/operations/{operationAction}", handleLoggingOperationCancel)
		}
	}
}

// loggingScopeParent reconstructs the parent resource name from whichever
// scope path param the matched route populated.
func loggingScopeParent(r *http.Request) string {
	for _, sc := range loggingScopes {
		if v := sim.PathParam(r, sc.pathParam); v != "" {
			return sc.collection + "/" + v
		}
	}
	return ""
}

// loggingLocationParent appends the location segment to the scope parent.
func loggingLocationParent(r *http.Request) string {
	return loggingScopeParent(r) + "/locations/" + sim.PathParam(r, "location")
}

// ---- Sinks (organizations/billingAccounts/folders scopes) ----
//
// Real Cloud Logging exposes sinks under every parent scope. The project-scope
// handlers in logging.go are project-specific; these mirror their behaviour
// (short name in LogSink.name, full path in resourceName) for the other scopes.

func loggingScopeSinkResponse(parent string, s LoggingSink) LoggingSink {
	short := strings.TrimPrefix(s.Name, parent+"/sinks/")
	s.Name = short
	s.ResourceName = parent + "/sinks/" + short
	return s
}

func handleLoggingScopeCreateSink(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	var sink LoggingSink
	if err := sim.ReadJSON(r, &sink); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid sink body: %v", err)
		return
	}
	short := lastSegment(sink.Name)
	if short == "" {
		short = generateUUID()
	}
	sink.Name = parent + "/sinks/" + short
	if sink.WriterIdentity == "" {
		if loggingUniqueWriter(r, sink) {
			sink.WriterIdentity = fmt.Sprintf("serviceAccount:service-%s@gcp-sa-logging.iam.gserviceaccount.com", short)
		} else {
			sink.WriterIdentity = "serviceAccount:cloud-logs@gcp-sa-logging.iam.gserviceaccount.com"
		}
	}
	logSinks.Put(sink.Name, sink)
	sim.WriteJSON(w, http.StatusOK, loggingScopeSinkResponse(parent, sink))
}

func handleLoggingScopeListSinks(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	prefix := parent + "/sinks/"
	sinks := logSinks.Filter(func(s LoggingSink) bool { return strings.HasPrefix(s.Name, prefix) })
	sort.Slice(sinks, func(i, j int) bool { return sinks[i].Name < sinks[j].Name })
	page, next, ok := paginateList(w, r, sinks)
	if !ok {
		return
	}
	for i := range page {
		page[i] = loggingScopeSinkResponse(parent, page[i])
	}
	resp := map[string]any{"sinks": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingScopeGetSink(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	key := parent + "/sinks/" + sim.PathParam(r, "sink")
	sink, ok := logSinks.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sink %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, loggingScopeSinkResponse(parent, sink))
}

func handleLoggingScopeUpdateSink(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	key := parent + "/sinks/" + sim.PathParam(r, "sink")
	var sink LoggingSink
	if err := sim.ReadJSON(r, &sink); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid sink body: %v", err)
		return
	}
	sink.Name = key
	if sink.WriterIdentity == "" {
		if loggingUniqueWriter(r, sink) {
			sink.WriterIdentity = fmt.Sprintf("serviceAccount:service-%s@gcp-sa-logging.iam.gserviceaccount.com", sim.PathParam(r, "sink"))
		} else {
			sink.WriterIdentity = "serviceAccount:cloud-logs@gcp-sa-logging.iam.gserviceaccount.com"
		}
	}
	logSinks.Put(key, sink)
	sim.WriteJSON(w, http.StatusOK, loggingScopeSinkResponse(parent, sink))
}

func handleLoggingScopeDeleteSink(w http.ResponseWriter, r *http.Request) {
	key := loggingScopeParent(r) + "/sinks/" + sim.PathParam(r, "sink")
	if !logSinks.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sink %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- Exclusions ----

func handleLoggingCreateExclusion(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	var ex LogExclusion
	if err := sim.ReadJSON(r, &ex); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid exclusion: %v", err)
		return
	}
	short := lastSegment(ex.Name)
	if short == "" {
		short = generateUUID()
	}
	ex.Name = parent + "/exclusions/" + short
	now := nowTimestamp()
	ex.CreateTime, ex.UpdateTime = now, now
	logExclusions.Put(ex.Name, ex)
	sim.WriteJSON(w, http.StatusOK, ex)
}

func handleLoggingListExclusions(w http.ResponseWriter, r *http.Request) {
	prefix := loggingScopeParent(r) + "/exclusions/"
	items := logExclusions.Filter(func(e LogExclusion) bool { return strings.HasPrefix(e.Name, prefix) })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"exclusions": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetExclusion(w http.ResponseWriter, r *http.Request) {
	key := loggingScopeParent(r) + "/exclusions/" + sim.PathParam(r, "exclusion")
	ex, ok := logExclusions.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "exclusion %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ex)
}

func handleLoggingPatchExclusion(w http.ResponseWriter, r *http.Request) {
	key := loggingScopeParent(r) + "/exclusions/" + sim.PathParam(r, "exclusion")
	cur, ok := logExclusions.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "exclusion %s not found", key)
		return
	}
	var upd LogExclusion
	if err := sim.ReadJSON(r, &upd); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid exclusion: %v", err)
		return
	}
	if upd.Description != "" {
		cur.Description = upd.Description
	}
	if upd.Filter != "" {
		cur.Filter = upd.Filter
	}
	cur.Disabled = upd.Disabled
	cur.UpdateTime = nowTimestamp()
	logExclusions.Put(key, cur)
	sim.WriteJSON(w, http.StatusOK, cur)
}

func handleLoggingDeleteExclusion(w http.ResponseWriter, r *http.Request) {
	key := loggingScopeParent(r) + "/exclusions/" + sim.PathParam(r, "exclusion")
	if !logExclusions.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "exclusion %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- Logs ----

func handleLoggingListLogs(w http.ResponseWriter, r *http.Request) {
	names := loggingListLogsScopes(loggingScopeParent(r), r.URL.Query()["resourceNames"])
	page, next, ok := paginateList(w, r, names)
	if !ok {
		return
	}
	resp := map[string]any{}
	if len(page) > 0 {
		resp["logNames"] = page
	}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingDeleteLog(w http.ResponseWriter, r *http.Request) {
	key := loggingScopeParent(r) + "/logs/" + sim.PathParam(r, "log")
	logEntries.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- Settings / cmekSettings ----

func handleLoggingGetSettings(w http.ResponseWriter, r *http.Request) {
	name := loggingScopeParent(r) + "/settings"
	s, ok := logSettings.Get(name)
	if !ok {
		s = LoggingSettings{
			Name:                    name,
			LoggingServiceAccountId: "serviceAccount:logging@gcp-sa-logging.iam.gserviceaccount.com",
		}
	}
	s.Name = name
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleLoggingUpdateSettings(w http.ResponseWriter, r *http.Request) {
	name := loggingScopeParent(r) + "/settings"
	var s LoggingSettings
	if err := sim.ReadJSON(r, &s); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid settings: %v", err)
		return
	}
	s.Name = name
	if s.LoggingServiceAccountId == "" {
		s.LoggingServiceAccountId = "serviceAccount:logging@gcp-sa-logging.iam.gserviceaccount.com"
	}
	logSettings.Put(name, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleLoggingGetCmekSettings(w http.ResponseWriter, r *http.Request) {
	name := loggingScopeParent(r) + "/cmekSettings"
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":             name,
		"serviceAccountId": "serviceAccount:cmek@gcp-sa-logging.iam.gserviceaccount.com",
	})
}

func handleLoggingUpdateCmekSettings(w http.ResponseWriter, r *http.Request) {
	name := loggingScopeParent(r) + "/cmekSettings"
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid cmekSettings: %v", err)
		return
	}
	resp := map[string]any{
		"name":             name,
		"serviceAccountId": "serviceAccount:cmek@gcp-sa-logging.iam.gserviceaccount.com",
	}
	if v, ok := body["kmsKeyName"].(string); ok && v != "" {
		resp["kmsKeyName"] = v
	}
	if v, ok := body["kmsKeyVersionName"].(string); ok && v != "" {
		resp["kmsKeyVersionName"] = v
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ---- Locations ----

func handleLoggingListLocations(w http.ResponseWriter, r *http.Request) {
	parent := loggingScopeParent(r)
	locs := []map[string]any{
		{"name": parent + "/locations/global", "locationId": "global"},
		{"name": parent + "/locations/us-central1", "locationId": "us-central1"},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"locations": locs})
}

func handleLoggingGetLocation(w http.ResponseWriter, r *http.Request) {
	loc := sim.PathParam(r, "location")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":       loggingScopeParent(r) + "/locations/" + loc,
		"locationId": loc,
	})
}

// ---- Monitored resource descriptors ----

func handleLoggingListMRD(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"resourceDescriptors": loggingMonitoredResourceDescriptors})
}

// ---- Buckets ----

func handleLoggingCreateBucket(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r)
	id := r.URL.Query().Get("bucketId")
	var b LogBucket
	if err := sim.ReadJSON(r, &b); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid bucket: %v", err)
		return
	}
	if id == "" {
		id = lastSegment(b.Name)
	}
	if id == "" {
		id = generateUUID()
	}
	b.Name = parent + "/buckets/" + id
	b.LifecycleState = "ACTIVE"
	now := nowTimestamp()
	b.CreateTime, b.UpdateTime = now, now
	logBuckets.Put(b.Name, b)
	sim.WriteJSON(w, http.StatusOK, b)
}

func handleLoggingCreateBucketAsync(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r)
	id := r.URL.Query().Get("bucketId")
	var b LogBucket
	if err := sim.ReadJSON(r, &b); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid bucket: %v", err)
		return
	}
	if id == "" {
		id = lastSegment(b.Name)
	}
	if id == "" {
		id = generateUUID()
	}
	b.Name = parent + "/buckets/" + id
	b.LifecycleState = "ACTIVE"
	now := nowTimestamp()
	b.CreateTime, b.UpdateTime = now, now
	logBuckets.Put(b.Name, b)
	sim.WriteJSON(w, http.StatusOK, loggingNewOperation(parent, b, "type.googleapis.com/google.logging.v2.LogBucket"))
}

func handleLoggingListBuckets(w http.ResponseWriter, r *http.Request) {
	prefix := loggingLocationParent(r) + "/buckets/"
	items := logBuckets.Filter(func(b LogBucket) bool {
		// Restrict to direct children (exclude views/links nested deeper).
		rest := strings.TrimPrefix(b.Name, prefix)
		return strings.HasPrefix(b.Name, prefix) && !strings.Contains(rest, "/")
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"buckets": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetBucket(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket")
	b, ok := logBuckets.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, b)
}

func handleLoggingPatchBucket(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket")
	cur, ok := logBuckets.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %s not found", key)
		return
	}
	var upd LogBucket
	if err := sim.ReadJSON(r, &upd); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid bucket: %v", err)
		return
	}
	if upd.Description != "" {
		cur.Description = upd.Description
	}
	if upd.RetentionDays != 0 {
		cur.RetentionDays = upd.RetentionDays
	}
	cur.Locked = upd.Locked
	cur.AnalyticsEnabled = upd.AnalyticsEnabled
	cur.UpdateTime = nowTimestamp()
	logBuckets.Put(key, cur)
	sim.WriteJSON(w, http.StatusOK, cur)
}

func handleLoggingDeleteBucket(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket")
	cur, ok := logBuckets.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %s not found", key)
		return
	}
	// Real Cloud Logging soft-deletes a bucket (DELETE_REQUESTED) so an
	// :undelete within the grace window can restore it.
	cur.LifecycleState = "DELETE_REQUESTED"
	logBuckets.Put(key, cur)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleLoggingBucketAction(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r)
	raw := sim.PathParam(r, "bucketAction")
	id, verb := splitColonVerb(raw)
	key := parent + "/buckets/" + id
	switch verb {
	case "undelete":
		cur, ok := logBuckets.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %s not found", key)
			return
		}
		cur.LifecycleState = "ACTIVE"
		logBuckets.Put(key, cur)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	case "updateAsync":
		cur, ok := logBuckets.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %s not found", key)
			return
		}
		var upd LogBucket
		if err := sim.ReadJSON(r, &upd); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid bucket: %v", err)
			return
		}
		if upd.Description != "" {
			cur.Description = upd.Description
		}
		if upd.RetentionDays != 0 {
			cur.RetentionDays = upd.RetentionDays
		}
		cur.UpdateTime = nowTimestamp()
		logBuckets.Put(key, cur)
		sim.WriteJSON(w, http.StatusOK, loggingNewOperation(parent, cur, "type.googleapis.com/google.logging.v2.LogBucket"))
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unknown bucket verb %q", verb)
	}
}

// ---- Views ----

func handleLoggingCreateView(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket")
	id := r.URL.Query().Get("viewId")
	var v LogView
	if err := sim.ReadJSON(r, &v); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid view: %v", err)
		return
	}
	if id == "" {
		id = lastSegment(v.Name)
	}
	if id == "" {
		id = generateUUID()
	}
	v.Name = parent + "/views/" + id
	now := nowTimestamp()
	v.CreateTime, v.UpdateTime = now, now
	logViews.Put(v.Name, v)
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleLoggingListViews(w http.ResponseWriter, r *http.Request) {
	prefix := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket") + "/views/"
	items := logViews.Filter(func(v LogView) bool { return strings.HasPrefix(v.Name, prefix) })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"views": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetView(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket") + "/views/" + sim.PathParam(r, "view")
	v, ok := logViews.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "view %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, v)
}

func handleLoggingListViewLogs(w http.ResponseWriter, r *http.Request) {
	// Logs visible to a view: list the parent scope's logs.
	handleLoggingViewLogsList(w, r)
}

func handleLoggingViewLogsList(w http.ResponseWriter, r *http.Request) {
	names := loggingListLogsScopes(loggingScopeParent(r), r.URL.Query()["resourceNames"])
	page, next, ok := paginateList(w, r, names)
	if !ok {
		return
	}
	resp := map[string]any{}
	if len(page) > 0 {
		resp["logNames"] = page
	}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingPatchView(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket") + "/views/" + sim.PathParam(r, "view")
	cur, ok := logViews.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "view %s not found", key)
		return
	}
	var upd LogView
	if err := sim.ReadJSON(r, &upd); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid view: %v", err)
		return
	}
	if upd.Description != "" {
		cur.Description = upd.Description
	}
	if upd.Filter != "" {
		cur.Filter = upd.Filter
	}
	cur.UpdateTime = nowTimestamp()
	logViews.Put(key, cur)
	sim.WriteJSON(w, http.StatusOK, cur)
}

func handleLoggingDeleteView(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket") + "/views/" + sim.PathParam(r, "view")
	if !logViews.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "view %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleLoggingViewIAM(w http.ResponseWriter, r *http.Request) {
	_, verb := splitColonVerb(sim.PathParam(r, "viewAction"))
	switch verb {
	case "getIamPolicy", "setIamPolicy":
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"version":  1,
			"etag":     gcpPolicyETag(),
			"bindings": []any{},
		})
	case "testIamPermissions":
		var body struct {
			Permissions []string `json:"permissions"`
		}
		_ = sim.ReadJSON(r, &body)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": body.Permissions})
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unknown view verb %q", verb)
	}
}

// ---- Links ----

func handleLoggingCreateLink(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket")
	id := r.URL.Query().Get("linkId")
	var l LogLink
	if err := sim.ReadJSON(r, &l); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid link: %v", err)
		return
	}
	if id == "" {
		id = lastSegment(l.Name)
	}
	if id == "" {
		id = generateUUID()
	}
	l.Name = parent + "/links/" + id
	l.LifecycleState = "ACTIVE"
	l.CreateTime = nowTimestamp()
	logLinks.Put(l.Name, l)
	sim.WriteJSON(w, http.StatusOK, loggingNewOperation(loggingLocationParent(r), l, "type.googleapis.com/google.logging.v2.Link"))
}

func handleLoggingListLinks(w http.ResponseWriter, r *http.Request) {
	prefix := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket") + "/links/"
	items := logLinks.Filter(func(l LogLink) bool { return strings.HasPrefix(l.Name, prefix) })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"links": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetLink(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/buckets/" + sim.PathParam(r, "bucket") + "/links/" + sim.PathParam(r, "link")
	l, ok := logLinks.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "link %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, l)
}

func handleLoggingDeleteLink(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r)
	key := parent + "/buckets/" + sim.PathParam(r, "bucket") + "/links/" + sim.PathParam(r, "link")
	if !logLinks.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "link %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, loggingNewOperation(parent, nil, "type.googleapis.com/google.protobuf.Empty"))
}

// ---- SavedQueries ----

func handleLoggingCreateSavedQuery(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r)
	id := r.URL.Query().Get("savedQueryId")
	var q SavedQuery
	if err := sim.ReadJSON(r, &q); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid savedQuery: %v", err)
		return
	}
	if id == "" {
		id = lastSegment(q.Name)
	}
	if id == "" {
		id = generateUUID()
	}
	q.Name = parent + "/savedQueries/" + id
	now := nowTimestamp()
	q.CreateTime, q.UpdateTime = now, now
	logSavedQueries.Put(q.Name, q)
	sim.WriteJSON(w, http.StatusOK, q)
}

func handleLoggingListSavedQueries(w http.ResponseWriter, r *http.Request) {
	prefix := loggingLocationParent(r) + "/savedQueries/"
	items := logSavedQueries.Filter(func(q SavedQuery) bool { return strings.HasPrefix(q.Name, prefix) })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"savedQueries": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetSavedQuery(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/savedQueries/" + sim.PathParam(r, "savedQuery")
	q, ok := logSavedQueries.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "savedQuery %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, q)
}

func handleLoggingPatchSavedQuery(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/savedQueries/" + sim.PathParam(r, "savedQuery")
	cur, ok := logSavedQueries.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "savedQuery %s not found", key)
		return
	}
	var upd SavedQuery
	if err := sim.ReadJSON(r, &upd); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid savedQuery: %v", err)
		return
	}
	if upd.DisplayName != "" {
		cur.DisplayName = upd.DisplayName
	}
	if upd.Description != "" {
		cur.Description = upd.Description
	}
	if upd.LoggingQuery != nil {
		cur.LoggingQuery = upd.LoggingQuery
	}
	if upd.Visibility != "" {
		cur.Visibility = upd.Visibility
	}
	cur.UpdateTime = nowTimestamp()
	logSavedQueries.Put(key, cur)
	sim.WriteJSON(w, http.StatusOK, cur)
}

func handleLoggingDeleteSavedQuery(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/savedQueries/" + sim.PathParam(r, "savedQuery")
	if !logSavedQueries.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "savedQuery %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- RecentQueries ----

func handleLoggingListRecentQueries(w http.ResponseWriter, r *http.Request) {
	// Recent queries are a per-user audit list the sim has no source for;
	// return an empty page (a valid ListRecentQueriesResponse).
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- LogScopes ----

func handleLoggingCreateLogScope(w http.ResponseWriter, r *http.Request) {
	parent := loggingLocationParent(r)
	id := r.URL.Query().Get("logScopeId")
	var ls LogScope
	if err := sim.ReadJSON(r, &ls); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid logScope: %v", err)
		return
	}
	if id == "" {
		id = lastSegment(ls.Name)
	}
	if id == "" {
		id = generateUUID()
	}
	ls.Name = parent + "/logScopes/" + id
	now := nowTimestamp()
	ls.CreateTime, ls.UpdateTime = now, now
	logScopes.Put(ls.Name, ls)
	sim.WriteJSON(w, http.StatusOK, ls)
}

func handleLoggingListLogScopes(w http.ResponseWriter, r *http.Request) {
	prefix := loggingLocationParent(r) + "/logScopes/"
	items := logScopes.Filter(func(l LogScope) bool { return strings.HasPrefix(l.Name, prefix) })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"logScopes": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetLogScope(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/logScopes/" + sim.PathParam(r, "logScope")
	ls, ok := logScopes.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "logScope %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ls)
}

func handleLoggingPatchLogScope(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/logScopes/" + sim.PathParam(r, "logScope")
	cur, ok := logScopes.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "logScope %s not found", key)
		return
	}
	var upd LogScope
	if err := sim.ReadJSON(r, &upd); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid logScope: %v", err)
		return
	}
	if upd.Description != "" {
		cur.Description = upd.Description
	}
	if upd.ResourceNames != nil {
		cur.ResourceNames = upd.ResourceNames
	}
	cur.UpdateTime = nowTimestamp()
	logScopes.Put(key, cur)
	sim.WriteJSON(w, http.StatusOK, cur)
}

func handleLoggingDeleteLogScope(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/logScopes/" + sim.PathParam(r, "logScope")
	if !logScopes.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "logScope %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---- Operations (non-project scopes) ----

func handleLoggingListOperations(w http.ResponseWriter, r *http.Request) {
	prefix := loggingLocationParent(r) + "/operations/"
	items := logOperations.Filter(func(o Operation) bool { return strings.HasPrefix(o.Name, prefix) })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page, next, ok := paginateList(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{}
	if len(page) > 0 {
		resp["operations"] = page
	}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleLoggingGetOperation(w http.ResponseWriter, r *http.Request) {
	key := loggingLocationParent(r) + "/operations/" + sim.PathParam(r, "operation")
	op, ok := logOperations.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %s not found", key)
		return
	}
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleLoggingOperationCancel serves CancelOperation on the folder,
// organization and billing-account operation collections. The project scope's
// operations share their URI with the Cloud Run collection and are dispatched
// by the fan-in that owns it, which resolves names across this store too.
func handleLoggingOperationCancel(w http.ResponseWriter, r *http.Request) {
	id, verb := splitColonVerb(sim.PathParam(r, "operationAction"))
	if verb != "cancel" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unknown operation verb %q", verb)
		return
	}
	handleGCPCancelOperation(w, loggingLocationParent(r)+"/operations/"+id)
}

// ---- Entries copy/tail ----

func handleLoggingEntriesCopy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Filter      string `json:"filter"`
		Destination string `json:"destination"`
	}
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid copy request: %v", err)
		return
	}
	op := loggingNewOperation("", map[string]any{"logEntriesCopiedCount": "0"},
		"type.googleapis.com/google.logging.v2.CopyLogEntriesResponse")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleLoggingEntriesTail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceNames []string `json:"resourceNames"`
		Filter        string   `json:"filter"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tail request: %v", err)
		return
	}
	entries, _ := listLogEntries(req.Filter, req.ResourceNames, 0, "", "timestamp desc")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// ---- helpers ----

// loggingNewOperation builds a completed Operation whose response carries the
// given resource wrapped as a protobuf Any (@type). Real Cloud Logging's async
// bucket/link/copy methods return a done operation once the resource settles.
func loggingNewOperation(parent string, resource any, typeName string) Operation {
	opName := parent + "/operations/" + generateUUID()
	if parent == "" {
		opName = "operations/" + generateUUID()
	}
	op := newLROFromResource(opName, resource, typeName)
	logOperations.Put(opName, op)
	return op
}

// newLROFromResource is newLRO's name-explicit form: the operation name is
// supplied directly (logging's operations live under arbitrary scope parents,
// not just projects/{p}/locations/{l}).
func newLROFromResource(name string, resource any, typeName string) Operation {
	var responseMap map[string]any
	if resource != nil {
		responseMap = anyToMap(resource)
		responseMap["@type"] = typeName
	} else {
		responseMap = map[string]any{"@type": typeName}
	}
	return Operation{
		Name: name,
		Done: true,
		Metadata: map[string]any{
			"@type":      "type.googleapis.com/google.logging.v2.OperationMetadata",
			"createTime": nowTimestamp(),
		},
		Response: responseMap,
	}
}

// loggingLogNames returns the distinct, sorted log names under prefix. The
// entries store is keyed by log name; each stored entry carries that same
// LogName, so a dedup over List() yields the names.
func loggingLogNames(prefix string) []string {
	seen := map[string]bool{}
	for _, slice := range logEntries.List() {
		for _, e := range slice {
			if strings.HasPrefix(e.LogName, prefix) {
				seen[e.LogName] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// loggingListLogsScopes returns the distinct, sorted log names a logs.list
// call covers: those under the parent, plus those under every resource name
// the caller widened the listing with. Dropping resourceNames would answer a
// widened listing with the parent's logs alone, which reads as "those are all
// the logs" — a wrong result, not a partial one.
//
// A bucket- or view-scoped resource name resolves to the container that owns
// it: this simulator keeps one copy of a container's entries rather than a
// copy per bucket, so the container's logs are what a view over it sees.
func loggingListLogsScopes(parent string, resourceNames []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, scope := range append([]string{parent}, resourceNames...) {
		if scope == "" {
			continue
		}
		for _, name := range loggingLogNames(loggingResourceContainer(scope) + "/logs/") {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// loggingResourceContainer trims a bucket- or view-scoped resource name back
// to the container that owns it, leaving a container name unchanged.
func loggingResourceContainer(name string) string {
	if i := strings.Index(name, "/locations/"); i >= 0 {
		return name[:i]
	}
	return name
}

func lastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func splitColonVerb(s string) (id, verb string) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func anyToMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("anyToMap: marshal: %w", err))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(fmt.Errorf("anyToMap: unmarshal: %w", err))
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}
