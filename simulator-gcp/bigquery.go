package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// BigQuery v2 REST surface. The simulator implements dataset/table
// lifecycle, streaming inserts, query jobs, synchronous queries, and
// tabledata paging against persisted rows.

type BQDataset struct {
	Kind             string            `json:"kind"`
	Etag             string            `json:"etag,omitempty"`
	ID               string            `json:"id"`
	SelfLink         string            `json:"selfLink,omitempty"`
	DatasetReference BQDatasetRef      `json:"datasetReference"`
	FriendlyName     string            `json:"friendlyName,omitempty"`
	Description      string            `json:"description,omitempty"`
	Location         string            `json:"location,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	CreationTime     string            `json:"creationTime,omitempty"`
	LastModifiedTime string            `json:"lastModifiedTime,omitempty"`
}

type BQDatasetRef struct {
	ProjectID string `json:"projectId,omitempty"`
	DatasetID string `json:"datasetId"`
}

type BQTable struct {
	Kind             string            `json:"kind"`
	Etag             string            `json:"etag,omitempty"`
	ID               string            `json:"id"`
	SelfLink         string            `json:"selfLink,omitempty"`
	TableReference   BQTableRef        `json:"tableReference"`
	FriendlyName     string            `json:"friendlyName,omitempty"`
	Description      string            `json:"description,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	Schema           *BQSchema         `json:"schema,omitempty"`
	Type             string            `json:"type,omitempty"`
	Location         string            `json:"location,omitempty"`
	CreationTime     string            `json:"creationTime,omitempty"`
	LastModifiedTime string            `json:"lastModifiedTime,omitempty"`
	ExpirationTime   string            `json:"expirationTime,omitempty"`
	NumRows          string            `json:"numRows"`
	NumBytes         string            `json:"numBytes"`
	// Nested writable definitions the sim persists verbatim so the
	// terraform-provider-google read path round-trips without drift.
	TimePartitioning       json.RawMessage `json:"timePartitioning,omitempty"`
	RangePartitioning      json.RawMessage `json:"rangePartitioning,omitempty"`
	Clustering             json.RawMessage `json:"clustering,omitempty"`
	View                   json.RawMessage `json:"view,omitempty"`
	MaterializedView       json.RawMessage `json:"materializedView,omitempty"`
	RequirePartitionFilter *bool           `json:"requirePartitionFilter,omitempty"`
}

type BQTableRef struct {
	ProjectID string `json:"projectId,omitempty"`
	DatasetID string `json:"datasetId"`
	TableID   string `json:"tableId"`
}

type BQSchema struct {
	Fields []BQFieldSchema `json:"fields,omitempty"`
}

type BQFieldSchema struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Mode string `json:"mode,omitempty"`
}

type BQRowSet struct {
	Rows []map[string]any `json:"rows"`
}

type BQJob struct {
	Kind          string         `json:"kind"`
	Etag          string         `json:"etag,omitempty"`
	ID            string         `json:"id"`
	SelfLink      string         `json:"selfLink,omitempty"`
	JobReference  BQJobRef       `json:"jobReference"`
	Configuration map[string]any `json:"configuration,omitempty"`
	Status        map[string]any `json:"status"`
	Statistics    map[string]any `json:"statistics,omitempty"`
	UserEmail     string         `json:"user_email,omitempty"`
}

// storedBQJob is the persisted row backing a query job: the wire-shape
// Job (which has no result member — query results ride GetQueryResults)
// plus the materialized result rows the GetQueryResults handler serves.
// The embedding flattens on json.Marshal, so sim.Store persistence keeps
// the same row shape the result has always been recovered from.
type storedBQJob struct {
	BQJob
	// Result is the evaluated query result served by GetQueryResults.
	// Store-only: never emitted as a Job member.
	Result BQQueryResult `json:"result,omitempty"`
}

type BQJobRef struct {
	ProjectID string `json:"projectId"`
	JobID     string `json:"jobId"`
	Location  string `json:"location,omitempty"`
}

// BQQueryResult is the shared result shape behind jobs.query
// (QueryResponse) and jobs.getQueryResults (GetQueryResultsResponse).
// totalBytesBilled is a QueryResponse-only member — the GetQueryResults
// handler clears it because GetQueryResultsResponse declares only
// totalBytesProcessed.
type BQQueryResult struct {
	Kind                string       `json:"kind,omitempty"`
	Schema              *BQSchema    `json:"schema,omitempty"`
	Rows                []BQTableRow `json:"rows,omitempty"`
	TotalRows           string       `json:"totalRows"`
	JobComplete         bool         `json:"jobComplete"`
	CacheHit            bool         `json:"cacheHit"`
	TotalBytesBilled    string       `json:"totalBytesBilled,omitempty"`
	TotalBytesProcessed string       `json:"totalBytesProcessed,omitempty"`
}

type BQTableRow struct {
	F []map[string]any `json:"f"`
}

// BQModel mirrors the Discovery "Model" schema members the sim persists
// and round-trips. Models have no `kind` member in the Discovery schema.
type BQModel struct {
	Etag             string            `json:"etag,omitempty"`
	ModelReference   BQModelRef        `json:"modelReference"`
	CreationTime     string            `json:"creationTime,omitempty"`
	LastModifiedTime string            `json:"lastModifiedTime,omitempty"`
	Description      string            `json:"description,omitempty"`
	FriendlyName     string            `json:"friendlyName,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	ExpirationTime   string            `json:"expirationTime,omitempty"`
	Location         string            `json:"location,omitempty"`
	ModelType        string            `json:"modelType,omitempty"`
}

type BQModelRef struct {
	ProjectID string `json:"projectId,omitempty"`
	DatasetID string `json:"datasetId,omitempty"`
	ModelID   string `json:"modelId,omitempty"`
}

// BQRoutine mirrors the Discovery "Routine" schema members the sim
// persists and round-trips. Routines have no `kind` member.
type BQRoutine struct {
	Etag              string          `json:"etag,omitempty"`
	RoutineReference  BQRoutineRef    `json:"routineReference"`
	RoutineType       string          `json:"routineType,omitempty"`
	CreationTime      string          `json:"creationTime,omitempty"`
	LastModifiedTime  string          `json:"lastModifiedTime,omitempty"`
	Language          string          `json:"language,omitempty"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
	ReturnType        json.RawMessage `json:"returnType,omitempty"`
	ReturnTableType   json.RawMessage `json:"returnTableType,omitempty"`
	ImportedLibraries []string        `json:"importedLibraries,omitempty"`
	DefinitionBody    string          `json:"definitionBody,omitempty"`
	Description       string          `json:"description,omitempty"`
	DeterminismLevel  string          `json:"determinismLevel,omitempty"`
	StrictMode        *bool           `json:"strictMode,omitempty"`
}

type BQRoutineRef struct {
	ProjectID string `json:"projectId,omitempty"`
	DatasetID string `json:"datasetId,omitempty"`
	RoutineID string `json:"routineId,omitempty"`
}

// BQRowAccessPolicy mirrors the Discovery "RowAccessPolicy" schema. No
// `kind` member.
type BQRowAccessPolicy struct {
	Etag                     string               `json:"etag,omitempty"`
	RowAccessPolicyReference BQRowAccessPolicyRef `json:"rowAccessPolicyReference"`
	FilterPredicate          string               `json:"filterPredicate,omitempty"`
	Grantees                 []string             `json:"grantees,omitempty"`
	CreationTime             string               `json:"creationTime,omitempty"`
	LastModifiedTime         string               `json:"lastModifiedTime,omitempty"`
}

type BQRowAccessPolicyRef struct {
	ProjectID string `json:"projectId,omitempty"`
	DatasetID string `json:"datasetId,omitempty"`
	TableID   string `json:"tableId,omitempty"`
	PolicyID  string `json:"policyId,omitempty"`
}

var (
	bqDatasets sim.Store[BQDataset]
	bqTables   sim.Store[BQTable]
	bqRows     sim.Store[BQRowSet]
	bqJobs     sim.Store[storedBQJob]
	bqModels   sim.Store[BQModel]
	bqRoutines sim.Store[BQRoutine]
	bqRAPs     sim.Store[BQRowAccessPolicy]

	bqFromRE  = regexp.MustCompile("(?i)\\bfrom\\s+`?([^`\\s]+)`?")
	bqWhereRE = regexp.MustCompile(`(?i)\bwhere\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*('([^']*)'|"([^"]*)"|[^\s;]+)`)
)

func registerBigQuery(srv *sim.Server) {
	bqDatasets = sim.MakeStore[BQDataset](srv.DB(), "bigquery_datasets")
	bqTables = sim.MakeStore[BQTable](srv.DB(), "bigquery_tables")
	bqRows = sim.MakeStore[BQRowSet](srv.DB(), "bigquery_rows")
	bqJobs = sim.MakeStore[storedBQJob](srv.DB(), "bigquery_jobs")
	bqModels = sim.MakeStore[BQModel](srv.DB(), "bigquery_models")
	bqRoutines = sim.MakeStore[BQRoutine](srv.DB(), "bigquery_routines")
	bqRAPs = sim.MakeStore[BQRowAccessPolicy](srv.DB(), "bigquery_row_access_policies")

	srv.HandleFunc("GET /bigquery/v2/projects", handleBQListProjects)

	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets", handleBQInsertDataset)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets", handleBQListDatasets)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}", handleBQGetDataset)
	srv.HandleFunc("PATCH /bigquery/v2/projects/{project}/datasets/{dataset}", handleBQPatchDataset)
	srv.HandleFunc("PUT /bigquery/v2/projects/{project}/datasets/{dataset}", handleBQPatchDataset)
	srv.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}", handleBQDeleteDataset)
	// datasets.undelete is a POST colon-verb on the dataset name; the
	// {datasetVerb} param captures "<dataset>:undelete".
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{datasetVerb}", handleBQDatasetVerb)

	srv.HandleFunc("GET /bigquery/v2/projects/{project}/serviceAccount", handleBQGetServiceAccount)

	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables", handleBQInsertTable)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables", handleBQListTables)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", handleBQGetTable)
	srv.HandleFunc("PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", handleBQPatchTable)
	srv.HandleFunc("PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", handleBQPatchTable)
	srv.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}", handleBQDeleteTable)
	// tables IAM verbs are POST colon-verbs on the table name; {tableVerb}
	// captures "<table>:getIamPolicy" etc.
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{tableVerb}", handleBQTableVerb)

	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/insertAll", handleBQInsertAll)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/data", handleBQTableDataList)

	// Models — list/get/patch/delete (no insert: models are produced by ML
	// query jobs, not created directly via REST).
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/models", handleBQListModels)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}", handleBQGetModel)
	srv.HandleFunc("PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}", handleBQPatchModel)
	srv.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}", handleBQDeleteModel)

	// Routines — list/insert/get/update/delete + IAM verbs.
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines", handleBQListRoutines)
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines", handleBQInsertRoutine)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}", handleBQGetRoutine)
	srv.HandleFunc("PUT /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}", handleBQUpdateRoutine)
	srv.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}", handleBQDeleteRoutine)
	// Routine IAM verbs ("<routine>:getIamPolicy" etc.) fan in on {routineVerb}.
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routineVerb}", handleBQRoutineVerb)

	// Row access policies — list/insert(+batchDelete)/get/update/delete + IAM.
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies", handleBQListRAPs)
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies", handleBQInsertRAP)
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies:batchDelete", handleBQBatchDeleteRAP)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}", handleBQGetRAP)
	srv.HandleFunc("PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}", handleBQUpdateRAP)
	srv.HandleFunc("DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}", handleBQDeleteRAP)
	// Row-access-policy IAM verbs ("<policy>:getIamPolicy"/":testIamPermissions").
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policyVerb}", handleBQRAPVerb)

	srv.HandleFunc("POST /bigquery/v2/projects/{project}/queries", handleBQQuery)
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/jobs", handleBQInsertJob)
	// jobs.insert also rides the media /upload path, which is how a load job
	// carries its bytes; the JSON body is the same Job resource either way.
	srv.HandleFunc("POST /upload/bigquery/v2/projects/{project}/jobs", handleBQInsertJob)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/jobs", handleBQListJobs)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/jobs/{job}", handleBQGetJob)
	srv.HandleFunc("POST /bigquery/v2/projects/{project}/jobs/{job}/cancel", handleBQCancelJob)
	srv.HandleFunc("DELETE /bigquery/v2/projects/{project}/jobs/{job}/delete", handleBQDeleteJob)
	srv.HandleFunc("GET /bigquery/v2/projects/{project}/queries/{job}", handleBQGetQueryResults)
}

func bqDatasetKey(project, dataset string) string {
	return project + "/" + dataset
}

func bqTableKey(project, dataset, table string) string {
	return project + "/" + dataset + "/" + table
}

func bqJobKey(project, job string) string {
	return project + "/" + job
}

func bqModelKey(project, dataset, model string) string {
	return project + "/" + dataset + "/" + model
}

func bqRoutineKey(project, dataset, routine string) string {
	return project + "/" + dataset + "/" + routine
}

func bqRAPKey(project, dataset, table, policy string) string {
	return project + "/" + dataset + "/" + table + "/" + policy
}

func bqMillisNow() string {
	return strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
}

func bqEtag(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf(`"%x"`, len(b))
}

func bqDatasetSelfLink(r *http.Request, project, dataset string) string {
	return gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/datasets/"+dataset)
}

func bqTableSelfLink(r *http.Request, project, dataset, table string) string {
	return gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/datasets/"+dataset+"/tables/"+table)
}

func bqApplyDatasetDefaults(r *http.Request, d BQDataset, project, dataset string) BQDataset {
	now := bqMillisNow()
	d.Kind = "bigquery#dataset"
	d.ID = project + ":" + dataset
	d.DatasetReference.ProjectID = project
	d.DatasetReference.DatasetID = dataset
	d.SelfLink = bqDatasetSelfLink(r, project, dataset)
	if d.Location == "" {
		d.Location = "US"
	}
	if d.CreationTime == "" {
		d.CreationTime = now
	}
	d.LastModifiedTime = now
	d.Etag = bqEtag(d)
	return d
}

func bqApplyTableDefaults(r *http.Request, t BQTable, project, dataset, table string) BQTable {
	now := bqMillisNow()
	t.Kind = "bigquery#table"
	t.ID = project + ":" + dataset + "." + table
	t.TableReference.ProjectID = project
	t.TableReference.DatasetID = dataset
	t.TableReference.TableID = table
	t.SelfLink = bqTableSelfLink(r, project, dataset, table)
	if t.Type == "" {
		t.Type = "TABLE"
	}
	if t.Location == "" {
		t.Location = "US"
	}
	if t.CreationTime == "" {
		t.CreationTime = now
	}
	t.LastModifiedTime = now
	if rows, ok := bqRows.Get(bqTableKey(project, dataset, table)); ok {
		t.NumRows = strconv.Itoa(len(rows.Rows))
	} else if t.NumRows == "" {
		t.NumRows = "0"
	}
	t.NumBytes = strconv.Itoa(len(t.NumRows))
	t.Etag = bqEtag(t)
	return t
}

func handleBQInsertDataset(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req BQDataset
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid dataset body: %v", err)
		return
	}
	dataset := req.DatasetReference.DatasetID
	if dataset == "" {
		sim.GCPError(w, http.StatusBadRequest, "datasetReference.datasetId is required", "INVALID_ARGUMENT")
		return
	}
	key := bqDatasetKey(project, dataset)
	if _, ok := bqDatasets.Get(key); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Already Exists: Dataset %s:%s", project, dataset)
		return
	}
	req = bqApplyDatasetDefaults(r, req, project, dataset)
	bqDatasets.Put(key, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBQGetDataset(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	d, ok := bqDatasets.Get(bqDatasetKey(project, dataset))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleBQListDatasets(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	all := bqDatasets.Filter(func(d BQDataset) bool {
		return d.DatasetReference.ProjectID == project
	})
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	all = gcpApplyListParams(all, r)
	items := make([]map[string]any, 0, len(all))
	for _, d := range all {
		items = append(items, map[string]any{
			"kind":             "bigquery#dataset",
			"id":               d.ID,
			"datasetReference": d.DatasetReference,
			"friendlyName":     d.FriendlyName,
			"labels":           d.Labels,
			"location":         d.Location,
		})
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"kind": "bigquery#datasetList", "datasets": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQPatchDataset(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	key := bqDatasetKey(project, dataset)
	current, ok := bqDatasets.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	var req BQDataset
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid dataset body: %v", err)
		return
	}
	if req.FriendlyName != "" {
		current.FriendlyName = req.FriendlyName
	}
	if req.Description != "" {
		current.Description = req.Description
	}
	if req.Location != "" {
		current.Location = req.Location
	}
	if req.Labels != nil {
		current.Labels = req.Labels
	}
	current = bqApplyDatasetDefaults(r, current, project, dataset)
	bqDatasets.Put(key, current)
	sim.WriteJSON(w, http.StatusOK, current)
}

func handleBQDeleteDataset(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	if !bqDatasets.Delete(bqDatasetKey(project, dataset)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	prefix := project + "/" + dataset + "/"
	for _, t := range bqTables.List() {
		if strings.HasPrefix(bqTableKey(t.TableReference.ProjectID, t.TableReference.DatasetID, t.TableReference.TableID), prefix) {
			key := bqTableKey(t.TableReference.ProjectID, t.TableReference.DatasetID, t.TableReference.TableID)
			bqTables.Delete(key)
			bqRows.Delete(key)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleBQInsertTable(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	if _, ok := bqDatasets.Get(bqDatasetKey(project, dataset)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	var req BQTable
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid table body: %v", err)
		return
	}
	table := req.TableReference.TableID
	if table == "" {
		sim.GCPError(w, http.StatusBadRequest, "tableReference.tableId is required", "INVALID_ARGUMENT")
		return
	}
	key := bqTableKey(project, dataset, table)
	if _, ok := bqTables.Get(key); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Already Exists: Table %s:%s.%s", project, dataset, table)
		return
	}
	req = bqApplyTableDefaults(r, req, project, dataset, table)
	bqTables.Put(key, req)
	bqRows.Put(key, BQRowSet{Rows: []map[string]any{}})
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBQGetTable(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	t, ok := bqTables.Get(bqTableKey(project, dataset, table))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	sim.WriteJSON(w, http.StatusOK, bqApplyTableDefaults(r, t, project, dataset, table))
}

func handleBQListTables(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	all := bqTables.Filter(func(t BQTable) bool {
		return t.TableReference.ProjectID == project && t.TableReference.DatasetID == dataset
	})
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	items := make([]map[string]any, 0, len(all))
	for _, t := range all {
		items = append(items, map[string]any{
			"kind":           "bigquery#table",
			"id":             t.ID,
			"tableReference": t.TableReference,
			"type":           t.Type,
			"friendlyName":   t.FriendlyName,
			"labels":         t.Labels,
		})
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"kind": "bigquery#tableList", "tables": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQPatchTable(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	key := bqTableKey(project, dataset, table)
	current, ok := bqTables.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	var req BQTable
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid table body: %v", err)
		return
	}
	if req.FriendlyName != "" {
		current.FriendlyName = req.FriendlyName
	}
	if req.Description != "" {
		current.Description = req.Description
	}
	if req.Labels != nil {
		current.Labels = req.Labels
	}
	if req.Schema != nil {
		current.Schema = req.Schema
	}
	current = bqApplyTableDefaults(r, current, project, dataset, table)
	bqTables.Put(key, current)
	sim.WriteJSON(w, http.StatusOK, current)
}

func handleBQDeleteTable(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	key := bqTableKey(project, dataset, table)
	if !bqTables.Delete(key) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	bqRows.Delete(key)
	w.WriteHeader(http.StatusNoContent)
}

func handleBQInsertAll(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	key := bqTableKey(project, dataset, table)
	if _, ok := bqTables.Get(key); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	var req struct {
		Rows []struct {
			InsertID string         `json:"insertId,omitempty"`
			JSON     map[string]any `json:"json"`
		} `json:"rows"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid insertAll body: %v", err)
		return
	}
	rowSet, _ := bqRows.Get(key)
	for _, row := range req.Rows {
		copied := make(map[string]any, len(row.JSON))
		for k, v := range row.JSON {
			copied[k] = v
		}
		rowSet.Rows = append(rowSet.Rows, copied)
	}
	bqRows.Put(key, rowSet)
	if t, ok := bqTables.Get(key); ok {
		t = bqApplyTableDefaults(r, t, project, dataset, table)
		bqTables.Put(key, t)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "bigquery#tableDataInsertAllResponse"})
}

func handleBQTableDataList(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	t, ok := bqTables.Get(bqTableKey(project, dataset, table))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	rowSet, _ := bqRows.Get(bqTableKey(project, dataset, table))
	// Absent params take their defaults (startIndex 0, all rows); a present but
	// non-numeric/negative value is rejected, as real BigQuery does, rather than
	// silently parsing to 0.
	start := 0
	if s := r.URL.Query().Get("startIndex"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for parameter 'startIndex': %s", s)
			return
		}
		start = v
	}
	max := 0
	if s := r.URL.Query().Get("maxResults"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for parameter 'maxResults': %s", s)
			return
		}
		max = v
	}
	if max <= 0 {
		max = len(rowSet.Rows)
	}
	end := start + max
	if start > len(rowSet.Rows) {
		start = len(rowSet.Rows)
	}
	if end > len(rowSet.Rows) {
		end = len(rowSet.Rows)
	}
	rows := bqEncodeRows(t.Schema, rowSet.Rows[start:end])
	resp := map[string]any{
		"kind":      "bigquery#tableDataList",
		"totalRows": strconv.Itoa(len(rowSet.Rows)),
		"rows":      rows,
	}
	if end < len(rowSet.Rows) {
		resp["pageToken"] = strconv.Itoa(end)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQQuery(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req struct {
		Query    string `json:"query"`
		Location string `json:"location,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid query body: %v", err)
		return
	}
	result, err := bqEvaluateQuery(project, req.Query)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	jobID := "job_" + generateUUID()
	job := bqDoneQueryJob(r, project, jobID, req.Location, req.Query, result)
	bqJobs.Put(bqJobKey(project, jobID), job)
	result.Kind = "bigquery#queryResponse"
	sim.WriteJSON(w, http.StatusOK, result)
}

func handleBQInsertJob(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req BQJob
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid job body: %v", err)
		return
	}
	jobID := req.JobReference.JobID
	if jobID == "" {
		jobID = "job_" + generateUUID()
	}
	location := req.JobReference.Location
	query := ""
	if q, ok := req.Configuration["query"].(map[string]any); ok {
		query, _ = q["query"].(string)
	}
	result := BQQueryResult{JobComplete: true, TotalRows: "0", CacheHit: false, TotalBytesProcessed: "0"}
	if query != "" {
		var err error
		result, err = bqEvaluateQuery(project, query)
		if err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
	}
	job := bqDoneQueryJob(r, project, jobID, location, query, result)
	job.Configuration = req.Configuration
	bqJobs.Put(bqJobKey(project, jobID), job)
	sim.WriteJSON(w, http.StatusOK, job.BQJob)
}

func handleBQGetJob(w http.ResponseWriter, r *http.Request) {
	project, jobID := sim.PathParam(r, "project"), sim.PathParam(r, "job")
	job, ok := bqJobs.Get(bqJobKey(project, jobID))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Job %s:%s", project, jobID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, job.BQJob)
}

func handleBQListJobs(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	all := bqJobs.Filter(func(j storedBQJob) bool { return j.JobReference.ProjectID == project })
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	// jobs.list items follow the lighter JobListJobs shape: the running state
	// is a top-level `state` field (mirrored from status.state), not nested.
	items := make([]map[string]any, 0, len(all))
	for _, j := range all {
		state, _ := j.Status["state"].(string)
		items = append(items, map[string]any{
			"kind":          "bigquery#job",
			"id":            j.ID,
			"jobReference":  j.JobReference,
			"state":         state,
			"status":        j.Status,
			"statistics":    j.Statistics,
			"configuration": j.Configuration,
			"user_email":    j.UserEmail,
		})
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"kind": "bigquery#jobList", "jobs": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQGetQueryResults(w http.ResponseWriter, r *http.Request) {
	project, jobID := sim.PathParam(r, "project"), sim.PathParam(r, "job")
	job, ok := bqJobs.Get(bqJobKey(project, jobID))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Job %s:%s", project, jobID)
		return
	}
	result := job.Result
	result.Kind = "bigquery#getQueryResultsResponse"
	// GetQueryResultsResponse declares totalBytesProcessed but not
	// totalBytesBilled (a QueryResponse-only member).
	result.TotalBytesBilled = ""
	sim.WriteJSON(w, http.StatusOK, result)
}

func bqDoneQueryJob(r *http.Request, project, jobID, location, query string, result BQQueryResult) storedBQJob {
	if location == "" {
		location = "US"
	}
	return storedBQJob{
		BQJob: BQJob{
			Kind:     "bigquery#job",
			Etag:     bqEtag(jobID),
			ID:       project + ":" + jobID,
			SelfLink: gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/jobs/"+jobID),
			JobReference: BQJobRef{
				ProjectID: project,
				JobID:     jobID,
				Location:  location,
			},
			Configuration: map[string]any{"query": map[string]any{"query": query}},
			Status:        map[string]any{"state": "DONE"},
			Statistics: map[string]any{
				"creationTime": bqMillisNow(),
				"startTime":    bqMillisNow(),
				"endTime":      bqMillisNow(),
				"query":        map[string]any{"totalBytesProcessed": "0", "cacheHit": false},
			},
		},
		Result: result,
	}
}

func bqEvaluateQuery(defaultProject, query string) (BQQueryResult, error) {
	query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	m := bqFromRE.FindStringSubmatch(query)
	if len(m) < 2 {
		return BQQueryResult{}, fmt.Errorf("only SELECT queries with FROM are supported")
	}
	project, dataset, table, err := bqParseTableRef(defaultProject, m[1])
	if err != nil {
		return BQQueryResult{}, err
	}
	t, ok := bqTables.Get(bqTableKey(project, dataset, table))
	if !ok {
		return BQQueryResult{}, fmt.Errorf("not found: Table %s:%s.%s", project, dataset, table)
	}
	rowSet, _ := bqRows.Get(bqTableKey(project, dataset, table))
	rows := rowSet.Rows
	if wm := bqWhereRE.FindStringSubmatch(query); len(wm) >= 3 {
		field := wm[1]
		want := strings.Trim(wm[2], `'"`)
		filtered := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if fmt.Sprint(row[field]) == want {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	return BQQueryResult{
		Schema:              t.Schema,
		Rows:                bqEncodeRows(t.Schema, rows),
		TotalRows:           strconv.Itoa(len(rows)),
		JobComplete:         true,
		CacheHit:            false,
		TotalBytesBilled:    "0",
		TotalBytesProcessed: "0",
	}, nil
}

// bqParseTableRef resolves a `dataset.table` or `project.dataset.table`
// reference. Every component must be present: an empty one addresses
// `projects//datasets//tables/…`, which identifies nothing, so a reference
// missing a component is invalid rather than a partial address.
func bqParseTableRef(defaultProject, ref string) (string, string, string, error) {
	ref = strings.Trim(ref, "`")
	parts := strings.Split(ref, ".")
	var project, dataset, table string
	switch len(parts) {
	case 2:
		project, dataset, table = defaultProject, parts[0], parts[1]
	case 3:
		project, dataset, table = parts[0], parts[1], parts[2]
	default:
		return "", "", "", fmt.Errorf("invalid table reference %q", ref)
	}
	if project == "" || dataset == "" || table == "" {
		return "", "", "", fmt.Errorf("invalid table reference %q", ref)
	}
	// Trimming quotes from the ends of the whole reference leaves any backtick
	// inside it in place, so "0.`0" parsed to a table literally named "`0" — an
	// identifier BigQuery cannot have, addressing a table that can never exist
	// while looking to the caller like a reference that parsed. A backtick
	// delimits a reference; it is never part of one.
	for _, component := range []string{project, dataset, table} {
		if strings.Contains(component, "`") {
			return "", "", "", fmt.Errorf("invalid table reference %q", ref)
		}
	}
	return project, dataset, table, nil
}

func handleBQListProjects(w http.ResponseWriter, r *http.Request) {
	// Enumerate every project referenced by an existing dataset so the list
	// reflects real persisted state rather than a fabricated catalog.
	seen := map[string]bool{}
	for _, d := range bqDatasets.List() {
		if p := d.DatasetReference.ProjectID; p != "" {
			seen[p] = true
		}
	}
	projects := make([]string, 0, len(seen))
	for p := range seen {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	items := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		items = append(items, map[string]any{
			"kind":             "bigquery#project",
			"id":               p,
			"numericId":        "0",
			"projectReference": map[string]any{"projectId": p},
			"friendlyName":     p,
		})
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{
		"kind":       "bigquery#projectList",
		"etag":       bqEtag(items),
		"projects":   page,
		"totalItems": len(items),
	}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQGetServiceAccount(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":  "bigquery#getServiceAccountResponse",
		"email": fmt.Sprintf("bq-%s@bigquery-encryption.iam.gserviceaccount.com", project),
	})
}

func handleBQCancelJob(w http.ResponseWriter, r *http.Request) {
	project, jobID := sim.PathParam(r, "project"), sim.PathParam(r, "job")
	job, ok := bqJobs.Get(bqJobKey(project, jobID))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Job %s:%s", project, jobID)
		return
	}
	// Query jobs in the sim complete synchronously, so a cancel request on an
	// already-DONE job is a no-op that returns the job's terminal state — the
	// same shape real BigQuery returns when cancelling a finished job.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind": "bigquery#jobCancelResponse",
		"job":  job.BQJob,
	})
}

func handleBQDeleteJob(w http.ResponseWriter, r *http.Request) {
	project, jobID := sim.PathParam(r, "project"), sim.PathParam(r, "job")
	if !bqJobs.Delete(bqJobKey(project, jobID)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Job %s:%s", project, jobID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleBQDatasetVerb(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	name, verb, _ := strings.Cut(sim.PathParam(r, "datasetVerb"), ":")
	switch verb {
	case "undelete":
		key := bqDatasetKey(project, name)
		d, ok := bqDatasets.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, name)
			return
		}
		d = bqApplyDatasetDefaults(r, d, project, name)
		bqDatasets.Put(key, d)
		sim.WriteJSON(w, http.StatusOK, d)
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown dataset verb: %s", verb)
	}
}

func handleBQTableVerb(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	name, verb, _ := strings.Cut(sim.PathParam(r, "tableVerb"), ":")
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		resource := "bigquery:" + bqTableKey(project, dataset, name)
		handleResourceIAM(w, r, gcpResourceIAMStore(), resource, verb)
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown table verb: %s", verb)
	}
}

func bqApplyModelDefaults(m BQModel, project, dataset, model string) BQModel {
	now := bqMillisNow()
	m.ModelReference.ProjectID = project
	m.ModelReference.DatasetID = dataset
	m.ModelReference.ModelID = model
	if m.Location == "" {
		m.Location = "US"
	}
	if m.CreationTime == "" {
		m.CreationTime = now
	}
	m.LastModifiedTime = now
	m.Etag = bqEtag(m)
	return m
}

func handleBQListModels(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	if _, ok := bqDatasets.Get(bqDatasetKey(project, dataset)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	all := bqModels.Filter(func(m BQModel) bool {
		return m.ModelReference.ProjectID == project && m.ModelReference.DatasetID == dataset
	})
	sort.Slice(all, func(i, j int) bool { return all[i].ModelReference.ModelID < all[j].ModelReference.ModelID })
	items := make([]any, len(all))
	for i, m := range all {
		items[i] = m
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"models": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQGetModel(w http.ResponseWriter, r *http.Request) {
	project, dataset, model := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "model")
	m, ok := bqModels.Get(bqModelKey(project, dataset, model))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Model %s:%s.%s", project, dataset, model)
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handleBQPatchModel(w http.ResponseWriter, r *http.Request) {
	project, dataset, model := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "model")
	key := bqModelKey(project, dataset, model)
	current, ok := bqModels.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Model %s:%s.%s", project, dataset, model)
		return
	}
	var req BQModel
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid model body: %v", err)
		return
	}
	if req.FriendlyName != "" {
		current.FriendlyName = req.FriendlyName
	}
	if req.Description != "" {
		current.Description = req.Description
	}
	if req.ExpirationTime != "" {
		current.ExpirationTime = req.ExpirationTime
	}
	if req.Labels != nil {
		current.Labels = req.Labels
	}
	current = bqApplyModelDefaults(current, project, dataset, model)
	bqModels.Put(key, current)
	sim.WriteJSON(w, http.StatusOK, current)
}

func handleBQDeleteModel(w http.ResponseWriter, r *http.Request) {
	project, dataset, model := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "model")
	if !bqModels.Delete(bqModelKey(project, dataset, model)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Model %s:%s.%s", project, dataset, model)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bqApplyRoutineDefaults(rt BQRoutine, project, dataset, routine string) BQRoutine {
	now := bqMillisNow()
	rt.RoutineReference.ProjectID = project
	rt.RoutineReference.DatasetID = dataset
	rt.RoutineReference.RoutineID = routine
	if rt.CreationTime == "" {
		rt.CreationTime = now
	}
	rt.LastModifiedTime = now
	rt.Etag = bqEtag(rt)
	return rt
}

func handleBQListRoutines(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	if _, ok := bqDatasets.Get(bqDatasetKey(project, dataset)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	all := bqRoutines.Filter(func(rt BQRoutine) bool {
		return rt.RoutineReference.ProjectID == project && rt.RoutineReference.DatasetID == dataset
	})
	sort.Slice(all, func(i, j int) bool { return all[i].RoutineReference.RoutineID < all[j].RoutineReference.RoutineID })
	items := make([]any, len(all))
	for i, rt := range all {
		items[i] = rt
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"routines": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQInsertRoutine(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	if _, ok := bqDatasets.Get(bqDatasetKey(project, dataset)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Dataset %s:%s", project, dataset)
		return
	}
	var req BQRoutine
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid routine body: %v", err)
		return
	}
	routine := req.RoutineReference.RoutineID
	if routine == "" {
		sim.GCPError(w, http.StatusBadRequest, "routineReference.routineId is required", "INVALID_ARGUMENT")
		return
	}
	key := bqRoutineKey(project, dataset, routine)
	if _, ok := bqRoutines.Get(key); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Already Exists: Routine %s:%s.%s", project, dataset, routine)
		return
	}
	req = bqApplyRoutineDefaults(req, project, dataset, routine)
	bqRoutines.Put(key, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBQGetRoutine(w http.ResponseWriter, r *http.Request) {
	project, dataset, routine := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "routine")
	rt, ok := bqRoutines.Get(bqRoutineKey(project, dataset, routine))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Routine %s:%s.%s", project, dataset, routine)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rt)
}

func handleBQUpdateRoutine(w http.ResponseWriter, r *http.Request) {
	project, dataset, routine := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "routine")
	key := bqRoutineKey(project, dataset, routine)
	current, ok := bqRoutines.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Routine %s:%s.%s", project, dataset, routine)
		return
	}
	var req BQRoutine
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid routine body: %v", err)
		return
	}
	// routines.update is a full PUT replacement; preserve immutable creation
	// time and re-derive the reference + etag.
	req.CreationTime = current.CreationTime
	req = bqApplyRoutineDefaults(req, project, dataset, routine)
	bqRoutines.Put(key, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBQDeleteRoutine(w http.ResponseWriter, r *http.Request) {
	project, dataset, routine := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "routine")
	if !bqRoutines.Delete(bqRoutineKey(project, dataset, routine)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Routine %s:%s.%s", project, dataset, routine)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleBQRoutineVerb(w http.ResponseWriter, r *http.Request) {
	project, dataset := sim.PathParam(r, "project"), sim.PathParam(r, "dataset")
	name, verb, _ := strings.Cut(sim.PathParam(r, "routineVerb"), ":")
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		resource := "bigquery:" + bqRoutineKey(project, dataset, name)
		handleResourceIAM(w, r, gcpResourceIAMStore(), resource, verb)
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown routine verb: %s", verb)
	}
}

func bqApplyRAPDefaults(rap BQRowAccessPolicy, project, dataset, table, policy string) BQRowAccessPolicy {
	now := bqMillisNow()
	rap.RowAccessPolicyReference.ProjectID = project
	rap.RowAccessPolicyReference.DatasetID = dataset
	rap.RowAccessPolicyReference.TableID = table
	rap.RowAccessPolicyReference.PolicyID = policy
	if rap.CreationTime == "" {
		rap.CreationTime = now
	}
	rap.LastModifiedTime = now
	rap.Etag = bqEtag(rap)
	return rap
}

func handleBQListRAPs(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	if _, ok := bqTables.Get(bqTableKey(project, dataset, table)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	all := bqRAPs.Filter(func(rap BQRowAccessPolicy) bool {
		ref := rap.RowAccessPolicyReference
		return ref.ProjectID == project && ref.DatasetID == dataset && ref.TableID == table
	})
	sort.Slice(all, func(i, j int) bool {
		return all[i].RowAccessPolicyReference.PolicyID < all[j].RowAccessPolicyReference.PolicyID
	})
	items := make([]any, len(all))
	for i, rap := range all {
		items[i] = rap
	}
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"rowAccessPolicies": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleBQInsertRAP(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	if _, ok := bqTables.Get(bqTableKey(project, dataset, table)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	var req BQRowAccessPolicy
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid row access policy body: %v", err)
		return
	}
	policy := req.RowAccessPolicyReference.PolicyID
	if policy == "" {
		sim.GCPError(w, http.StatusBadRequest, "rowAccessPolicyReference.policyId is required", "INVALID_ARGUMENT")
		return
	}
	key := bqRAPKey(project, dataset, table, policy)
	if _, ok := bqRAPs.Get(key); ok {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "Already Exists: Row access policy %s", policy)
		return
	}
	req = bqApplyRAPDefaults(req, project, dataset, table, policy)
	bqRAPs.Put(key, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBQGetRAP(w http.ResponseWriter, r *http.Request) {
	project, dataset, table, policy := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table"), sim.PathParam(r, "policy")
	rap, ok := bqRAPs.Get(bqRAPKey(project, dataset, table, policy))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Row access policy %s", policy)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rap)
}

func handleBQUpdateRAP(w http.ResponseWriter, r *http.Request) {
	project, dataset, table, policy := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table"), sim.PathParam(r, "policy")
	key := bqRAPKey(project, dataset, table, policy)
	current, ok := bqRAPs.Get(key)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Row access policy %s", policy)
		return
	}
	var req BQRowAccessPolicy
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid row access policy body: %v", err)
		return
	}
	req.CreationTime = current.CreationTime
	req = bqApplyRAPDefaults(req, project, dataset, table, policy)
	bqRAPs.Put(key, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBQDeleteRAP(w http.ResponseWriter, r *http.Request) {
	project, dataset, table, policy := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table"), sim.PathParam(r, "policy")
	if !bqRAPs.Delete(bqRAPKey(project, dataset, table, policy)) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Row access policy %s", policy)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleBQBatchDeleteRAP(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	if _, ok := bqTables.Get(bqTableKey(project, dataset, table)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Table %s:%s.%s", project, dataset, table)
		return
	}
	var req struct {
		PolicyIds []string `json:"policyIds"`
		Force     bool     `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid batchDelete body: %v", err)
		return
	}
	for _, policy := range req.PolicyIds {
		key := bqRAPKey(project, dataset, table, policy)
		if !bqRAPs.Delete(key) && !req.Force {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Not found: Row access policy %s", policy)
			return
		}
	}
	// batchDelete returns an empty body (response: null in Discovery).
	w.WriteHeader(http.StatusNoContent)
}

func handleBQRAPVerb(w http.ResponseWriter, r *http.Request) {
	project, dataset, table := sim.PathParam(r, "project"), sim.PathParam(r, "dataset"), sim.PathParam(r, "table")
	name, verb, _ := strings.Cut(sim.PathParam(r, "policyVerb"), ":")
	switch verb {
	// Row access policies expose only getIamPolicy + testIamPermissions
	// (no setIamPolicy) per the Discovery document.
	case "getIamPolicy", "testIamPermissions":
		resource := "bigquery:" + bqRAPKey(project, dataset, table, name)
		handleResourceIAM(w, r, gcpResourceIAMStore(), resource, verb)
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Unknown row access policy verb: %s", verb)
	}
}

func bqEncodeRows(schema *BQSchema, rows []map[string]any) []BQTableRow {
	fields := []string{}
	if schema != nil {
		for _, f := range schema.Fields {
			fields = append(fields, f.Name)
		}
	}
	if len(fields) == 0 && len(rows) > 0 {
		for k := range rows[0] {
			fields = append(fields, k)
		}
		sort.Strings(fields)
	}
	out := make([]BQTableRow, 0, len(rows))
	for _, row := range rows {
		tr := BQTableRow{F: make([]map[string]any, 0, len(fields))}
		for _, f := range fields {
			tr.F = append(tr.F, map[string]any{"v": row[f]})
		}
		out = append(out, tr)
	}
	return out
}
