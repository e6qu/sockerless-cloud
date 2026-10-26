package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"

	"errors"

	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// bqFormatOptions is the Discovery's DataFormatOptions: how a TIMESTAMP
// cell travels in tabledata.list, jobs.query and jobs.getQueryResults.
// timestampOutputFormat takes precedence over useInt64Timestamp; the default
// is FLOAT64 seconds since the epoch.
type bqFormatOptions struct {
	timestamp string
}

func bqFormatOptionsOf(timestampOutputFormat string, useInt64 bool) (bqFormatOptions, error) {
	switch timestampOutputFormat {
	case "", "TIMESTAMP_OUTPUT_FORMAT_UNSPECIFIED":
		if useInt64 {
			return bqFormatOptions{"INT64"}, nil
		}
		return bqFormatOptions{"FLOAT64"}, nil
	case "FLOAT64", "INT64", "ISO8601_STRING":
		return bqFormatOptions{timestampOutputFormat}, nil
	}
	return bqFormatOptions{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for formatOptions.timestampOutputFormat: %s", timestampOutputFormat)
}

func bqFormatOptionsFromQuery(q url.Values) (bqFormatOptions, error) {
	useInt64 := false
	if s := q.Get("formatOptions.useInt64Timestamp"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return bqFormatOptions{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for formatOptions.useInt64Timestamp: %s", s)
		}
		useInt64 = b
	}
	return bqFormatOptionsOf(q.Get("formatOptions.timestampOutputFormat"), useInt64)
}

// timestampCell renders a stored TIMESTAMP (epoch microseconds) in the
// requested output format: the FLOAT64 seconds in scientific notation, the
// INT64 microseconds, or ISO 8601 with the twelve fractional digits the
// Discovery's enum description spells.
func (fo bqFormatOptions) timestampCell(stored any) any {
	text := bqScalarText(stored)
	micros, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return stored
	}
	switch fo.timestamp {
	case "INT64":
		return text
	case "ISO8601_STRING":
		t := time.UnixMicro(micros).UTC()
		return t.Format("2006-01-02T15:04:05.000000") + "000000Z"
	}
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(float64(micros)/1e6, 'E', -1, 64), "E")
	n, err := strconv.Atoi(exponent)
	if err != nil {
		return text
	}
	return mantissa + "E" + strconv.Itoa(n)
}

// bqQueryParametersTokens turns the request's parameters into the literal
// tokens the rewrite substitutes.
func bqQueryParametersTokens(mode string, params []bqQueryParameter, scope *bqScope) error {
	if len(params) == 0 {
		return nil
	}
	named := mode == "NAMED" || mode == "" && params[0].Name != ""
	if mode != "" && mode != "NAMED" && mode != "POSITIONAL" {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for parameterMode: %s", mode)
	}
	scope.parameters = map[string][]bqSQLToken{}
	for _, p := range params {
		tokens, err := bqParameterTokens(p)
		if err != nil {
			return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
		}
		if named {
			if p.Name == "" {
				return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Query parameter name is required in NAMED mode")
			}
			scope.parameters[strings.ToLower(p.Name)] = tokens
		} else {
			scope.positional = append(scope.positional, tokens)
		}
	}
	return nil
}

// bqAnonymousDataset is the hidden dataset a project's destination-less query
// results land in; its name begins with an underscore, which hides it from
// the dataset list.
func bqAnonymousDataset(project string) string {
	sum := sha1.Sum([]byte("anonymous:" + project))
	return "_" + hex.EncodeToString(sum[:])[:40]
}

func bqAnonymousTable(jobID string) string {
	sum := sha1.Sum([]byte(jobID))
	return "anon" + hex.EncodeToString(sum[:])[:40]
}

func bqHiddenDataset(dataset string) bool { return strings.HasPrefix(dataset, "_") }

// bqAnonymousExpiration is how long a temporary result table lives.
const bqAnonymousExpiration = 24 * time.Hour

// bqQueryJobRun is what running a query job produced.
type bqQueryJobRun struct {
	job       storedBQJob
	result    *bqStatementResult
	rows      []map[string]any
	schema    *BQSchema
	children  int
	isScript  bool
	statusErr error
}

// bqRunQueryJob runs a query job's statement or script and builds the job
// as jobs.insert and jobs.query record it.
func bqRunQueryJob(ctx context.Context, r *http.Request, project, jobID, location string, configuration map[string]any, cfg bqQueryConfig, dryRun bool) (bqQueryJobRun, error) {
	if location == "" {
		location = "US"
	}
	dataset := ""
	if cfg.DefaultDataset != nil && cfg.DefaultDataset.DatasetID != "" {
		dataset = cfg.DefaultDataset.DatasetID
		if cfg.DefaultDataset.ProjectID != "" {
			dataset = cfg.DefaultDataset.ProjectID + "." + dataset
		}
	}
	scope := bqNewScope(project, dataset)
	if err := bqQueryParametersTokens(cfg.ParameterMode, cfg.QueryParameters, scope); err != nil {
		return bqQueryJobRun{}, err
	}
	statements, err := bqSplitScript(cfg.Query)
	if err != nil {
		return bqQueryJobRun{}, bqQueryRefusal(err)
	}
	if len(statements) == 0 {
		return bqQueryJobRun{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Syntax error: Unexpected end of script")
	}
	now := bqMillisNow()
	run := bqQueryJobRun{}
	job := storedBQJob{BQJob: BQJob{
		Kind:          "bigquery#job",
		Etag:          bqEtag(jobID),
		ID:            project + ":" + jobID,
		SelfLink:      gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/jobs/"+jobID),
		JobReference:  BQJobRef{ProjectID: project, JobID: jobID, Location: location},
		Configuration: configuration,
		Status:        map[string]any{"state": "DONE"},
		Statistics:    map[string]any{"creationTime": now, "startTime": now},
	}}
	queryStats := map[string]any{"cacheHit": false}
	isScript := len(statements) > 1 || bqStatementKind(statements[0].tokens) == "SCRIPT"
	var result *bqStatementResult
	if isScript {
		run.isScript = true
		queryStats["statementType"] = "SCRIPT"
		result, run.children, err = bqRunScript(ctx, r, scope, cfg.Query, dryRun, func(statement string, frame map[string]any, kind string, res *bqStatementResult, serr error) error {
			if dryRun {
				return nil
			}
			return bqRecordChildJob(r, project, jobID, location, statement, frame, kind, res, serr)
		})
		job.Statistics["numChildJobs"] = strconv.Itoa(run.children)
	} else {
		result, err = bqRunStatement(ctx, r, scope, cfg.Query, dryRun)
	}
	if err != nil {
		return bqQueryJobRun{}, bqQueryRefusal(err)
	}
	run.result = result
	if !isScript {
		queryStats["statementType"] = result.StatementType
	}
	if len(result.Referenced) > 0 {
		queryStats["referencedTables"] = result.Referenced
	}
	queryStats["totalBytesProcessed"] = strconv.FormatInt(result.BytesProcessed, 10)
	job.Statistics["totalBytesProcessed"] = strconv.FormatInt(result.BytesProcessed, 10)
	if result.NumDMLAffected != nil {
		queryStats["numDmlAffectedRows"] = strconv.FormatInt(*result.NumDMLAffected, 10)
	}
	if result.DMLStats != nil {
		queryStats["dmlStats"] = result.DMLStats
	}
	if result.DDLOperation != "" {
		queryStats["ddlOperationPerformed"] = result.DDLOperation
	}
	if result.DDLTarget != nil {
		queryStats["ddlTargetTable"] = result.DDLTarget
	}
	if dryRun {
		queryStats["totalBytesProcessedAccuracy"] = "PRECISE"
		if result.Schema != nil {
			queryStats["schema"] = result.Schema
		}
	}
	job.Statistics["query"] = queryStats
	run.schema, run.rows = result.Schema, result.Rows
	if result.isQuery && !dryRun {
		dest, err := bqStoreQueryResult(ctx, r, project, jobID, cfg, result)
		if err != nil {
			run.statusErr = err
		} else {
			queryCfg, _ := job.Configuration["query"].(map[string]any)
			if queryCfg == nil {
				queryCfg = map[string]any{"query": cfg.Query}
			}
			queryCfg["destinationTable"] = dest
			job.Configuration["query"] = queryCfg
		}
	}
	job.Statistics["endTime"] = bqMillisNow()
	if run.statusErr != nil {
		proto := bqJobFailure(run.statusErr)
		job.Status["errorResult"] = proto
		job.Status["errors"] = []any{proto}
	} else {
		job.Result = BQQueryResult{Schema: result.Schema, TotalRows: strconv.Itoa(len(result.Rows)), JobComplete: true, TotalBytesProcessed: strconv.FormatInt(result.BytesProcessed, 10)}
		job.ResultRows = result.Rows
		if result.NumDMLAffected != nil {
			job.Result.NumDMLAffectedRows = strconv.FormatInt(*result.NumDMLAffected, 10)
		}
		if result.Schema == nil {
			job.Result.TotalRows = ""
		}
	}
	run.job = job
	return run, nil
}

// bqStoreQueryResult writes a SELECT's rows to the job's destination table,
// or to an anonymous table in the project's hidden dataset when the job
// names none, and answers where they went.
func bqStoreQueryResult(ctx context.Context, r *http.Request, project, jobID string, cfg bqQueryConfig, result *bqStatementResult) (BQTableRef, error) {
	if cfg.DestinationTable != nil {
		dest := *cfg.DestinationTable
		if dest.ProjectID == "" {
			dest.ProjectID = project
		}
		return dest, bqWriteQueryDestination(ctx, r, project, cfg, result.Schema, result.Rows)
	}
	dest := BQTableRef{ProjectID: project, DatasetID: bqAnonymousDataset(project), TableID: bqAnonymousTable(jobID)}
	dsKey := bqDatasetKey(dest.ProjectID, dest.DatasetID)
	if _, ok := bqDatasets.Get(dsKey); !ok {
		bqDatasets.Put(dsKey, bqApplyDatasetDefaults(r, BQDataset{DatasetReference: BQDatasetRef{ProjectID: dest.ProjectID, DatasetID: dest.DatasetID}}, dest.ProjectID, dest.DatasetID))
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	rows := result.Rows
	if rows == nil {
		rows = []map[string]any{}
	}
	return dest, bqWriteTable(ctx, dest, func() error {
		table := BQTable{TableReference: dest, Schema: result.Schema, ExpirationTime: bqExpiration(time.Now().Add(bqAnonymousExpiration))}
		bqRows.Put(key, BQRowSet{Rows: rows})
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		return nil
	})
}

// bqRecordChildJob stores one statement of a script as a child job.
func bqRecordChildJob(r *http.Request, project, parentID, location, statement string, frame map[string]any, kind string, result *bqStatementResult, serr error) error {
	childID := "job_" + sim.NewUUID()
	now := bqMillisNow()
	job := storedBQJob{BQJob: BQJob{
		Kind:          "bigquery#job",
		Etag:          bqEtag(childID),
		ID:            project + ":" + childID,
		SelfLink:      gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/jobs/"+childID),
		JobReference:  BQJobRef{ProjectID: project, JobID: childID, Location: location},
		Configuration: map[string]any{"query": map[string]any{"query": statement, "useLegacySql": false}, "jobType": "QUERY"},
		Status:        map[string]any{"state": "DONE"},
		Statistics: map[string]any{
			"creationTime": now, "startTime": now, "endTime": bqMillisNow(),
			"parentJobId":      parentID,
			"scriptStatistics": map[string]any{"evaluationKind": kind, "stackFrames": []map[string]any{frame}},
		},
	}}
	queryStats := map[string]any{"cacheHit": false}
	if result != nil {
		if result.StatementType != "" {
			queryStats["statementType"] = result.StatementType
		}
		if len(result.Referenced) > 0 {
			queryStats["referencedTables"] = result.Referenced
		}
		queryStats["totalBytesProcessed"] = strconv.FormatInt(result.BytesProcessed, 10)
		if result.NumDMLAffected != nil {
			queryStats["numDmlAffectedRows"] = strconv.FormatInt(*result.NumDMLAffected, 10)
		}
		if result.DMLStats != nil {
			queryStats["dmlStats"] = result.DMLStats
		}
		if result.DDLOperation != "" {
			queryStats["ddlOperationPerformed"] = result.DDLOperation
		}
		if result.DDLTarget != nil {
			queryStats["ddlTargetTable"] = result.DDLTarget
		}
		job.Result = BQQueryResult{Schema: result.Schema, TotalRows: strconv.Itoa(len(result.Rows)), JobComplete: true}
		job.ResultRows = result.Rows
		if result.Schema == nil {
			job.Result.TotalRows = ""
		}
	}
	job.Statistics["query"] = queryStats
	if serr != nil {
		proto := bqJobFailure(serr)
		job.Status["errorResult"] = proto
		job.Status["errors"] = []any{proto}
	}
	bqJobs.Put(bqJobKey(project, childID), job)
	return nil
}

// bqQueryRefusal maps a statement error to the API error jobs.insert and
// jobs.query answer.
func bqQueryRefusal(err error) error {
	var qerr *bqQueryError
	if errors.As(err, &qerr) {
		status, ok := bqFailureStatuses[qerr.reason]
		if !ok {
			status = bqFailureStatuses["invalidQuery"]
		}
		return apiRefuse(status.code, status.status, "%s", qerr.message)
	}
	var failure *bqLoadFailure
	if errors.As(err, &failure) {
		status, ok := bqFailureStatuses[failure.reason]
		if !ok {
			status = bqFailureStatuses["backendError"]
		}
		return apiRefuse(status.code, status.status, "%s", failure.message)
	}
	return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err.Error())
}

// bqResultPage is one page of a job's result rows under the request's
// paging parameters: maxResults, startIndex and pageToken.
func bqResultPage(q url.Values, rows []map[string]any) (page []map[string]any, nextToken string, err error) {
	start := 0
	if s := q.Get("startIndex"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			return nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for parameter 'startIndex': %s", s)
		}
		start = v
	}
	if tok := q.Get("pageToken"); tok != "" {
		v, ok := listq.Decimal.Decode(tok)
		if !ok || v < 0 {
			return nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid page token: %s", tok)
		}
		start = v
	}
	end := len(rows)
	if s := q.Get("maxResults"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			return nil, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for parameter 'maxResults': %s", s)
		}
		end = start + v
	}
	if start > len(rows) {
		start = len(rows)
	}
	if end > len(rows) {
		end = len(rows)
	}
	if end < len(rows) {
		nextToken = strconv.Itoa(end)
	}
	return rows[start:end], nextToken, nil
}

// bqQueryResponse renders a finished query job as QueryResponse or
// GetQueryResultsResponse.
func bqQueryResponse(job storedBQJob, kind string, q url.Values, fo bqFormatOptions) (map[string]any, error) {
	result := job.Result
	page, next, err := bqResultPage(q, job.ResultRows)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"kind":         kind,
		"jobReference": job.JobReference,
		"jobComplete":  true,
		"cacheHit":     false,
	}
	if stats, ok := job.Statistics["query"].(map[string]any); ok {
		if v, ok := stats["totalBytesProcessed"]; ok {
			resp["totalBytesProcessed"] = v
		}
		if kind == "bigquery#queryResponse" {
			if v, ok := stats["numDmlAffectedRows"]; ok {
				resp["numDmlAffectedRows"] = v
			}
			if v, ok := stats["dmlStats"]; ok {
				resp["dmlStats"] = v
			}
			if v, ok := stats["statementType"]; ok {
				resp["statementType"] = v
			}
			if v, ok := stats["totalBytesProcessed"]; ok {
				resp["totalBytesBilled"] = v
			}
		} else if v, ok := stats["numDmlAffectedRows"]; ok {
			resp["numDmlAffectedRows"] = v
		}
	}
	if result.Schema != nil {
		resp["schema"] = result.Schema
		resp["totalRows"] = result.TotalRows
		resp["rows"] = bqEncodeRows(result.Schema, page, fo)
		if next != "" {
			resp["pageToken"] = next
		}
	}
	return resp, nil
}

// bqJobError answers a finished job's failure the way getQueryResults does.
func bqJobError(w http.ResponseWriter, job storedBQJob) bool {
	proto, failed := job.Status["errorResult"].(map[string]any)
	if !failed {
		return false
	}
	reason, _ := proto["reason"].(string)
	message, _ := proto["message"].(string)
	status, ok := bqFailureStatuses[reason]
	if !ok {
		status = bqFailureStatuses["backendError"]
	}
	bqError(w, status.code, message, status.status)
	return true
}

func bqDecodeDryRun(configuration map[string]any) (bool, error) {
	raw, ok := configuration["dryRun"]
	if !ok || raw == nil {
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value for dryRun: %v", raw)
	}
	return b, nil
}
