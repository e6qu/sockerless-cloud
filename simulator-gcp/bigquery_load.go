package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/e6qu/sockerless-cloud/sim"
)

// handleBQUploadJob serves jobs.insert on its media paths, where a load job
// carries its source data: in one multipart request, or through a resumable
// session begun on either media path.
func handleBQUploadJob(w http.ResponseWriter, r *http.Request) {
	serveMediaUpload(w, r, sim.PathParam(r, "project"), "jobs.insert", true,
		func(project string, request, data []byte, _ string) (any, error) {
			job, err := bqInsertLoadJob(r, project, request, data)
			if err != nil {
				return nil, err
			}
			return job.BQJob, nil
		})
}

// bqLoadConfig is the JobConfigurationLoad members a load from media reads.
type bqLoadConfig struct {
	DestinationTable    *BQTableRef `json:"destinationTable"`
	SourceFormat        string      `json:"sourceFormat"`
	Schema              *BQSchema   `json:"schema"`
	CreateDisposition   string      `json:"createDisposition"`
	WriteDisposition    string      `json:"writeDisposition"`
	SkipLeadingRows     int64       `json:"skipLeadingRows"`
	FieldDelimiter      string      `json:"fieldDelimiter"`
	NullMarker          string      `json:"nullMarker"`
	AllowJaggedRows     bool        `json:"allowJaggedRows"`
	IgnoreUnknownValues bool        `json:"ignoreUnknownValues"`
	MaxBadRecords       int64       `json:"maxBadRecords"`
	Autodetect          bool        `json:"autodetect"`
}

// bqLoadFailure is why a load job finished without writing: an ErrorProto
// reason and message.
type bqLoadFailure struct {
	reason  string
	message string
}

func (f *bqLoadFailure) Error() string { return f.message }

// bqInsertLoadJob runs a load job whose source data arrived as media. A
// request the service cannot accept is refused; a load that fails on the
// table or the data finishes DONE with an errorResult, as BigQuery reports a
// failed job.
func bqInsertLoadJob(r *http.Request, project string, request, data []byte) (storedBQJob, error) {
	var req BQJob
	if len(bytes.TrimSpace(request)) > 0 {
		if err := json.Unmarshal(request, &req); err != nil {
			return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid job body: %v", err)
		}
	}
	rawLoad, ok := req.Configuration["load"]
	if !ok {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT",
			"a job with media must be a load job: configuration.load is required")
	}
	encoded, err := json.Marshal(rawLoad)
	if err != nil {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid load configuration: %v", err)
	}
	var cfg bqLoadConfig
	if err := json.Unmarshal(encoded, &cfg); err != nil {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid load configuration: %v", err)
	}
	if cfg.DestinationTable == nil || cfg.DestinationTable.DatasetID == "" || cfg.DestinationTable.TableID == "" {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: destinationTable")
	}
	format := cfg.SourceFormat
	if format == "" {
		format = "CSV"
	}
	if format != "CSV" && format != "NEWLINE_DELIMITED_JSON" {
		return storedBQJob{}, apiRefuse(http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"the simulator loads CSV and NEWLINE_DELIMITED_JSON source data, not %s", format)
	}
	if cfg.Autodetect && cfg.Schema == nil {
		if _, exists := bqTables.Get(bqTableKey(bqLoadProject(cfg, project), cfg.DestinationTable.DatasetID, cfg.DestinationTable.TableID)); !exists {
			return storedBQJob{}, apiRefuse(http.StatusNotImplemented, "NOT_IMPLEMENTED",
				"the simulator does not detect a schema from source data; give configuration.load.schema")
		}
	}

	jobID := req.JobReference.JobID
	if jobID == "" {
		jobID = "job_" + sim.NewUUID()
	}
	location := req.JobReference.Location
	if location == "" {
		location = "US"
	}
	now := bqMillisNow()
	load := map[string]any{"inputFiles": "1", "inputFileBytes": fmt.Sprint(len(data))}
	job := storedBQJob{BQJob: BQJob{
		Kind:          "bigquery#job",
		Etag:          bqEtag(jobID),
		ID:            project + ":" + jobID,
		SelfLink:      gcpSelfLink(r, "/bigquery/v2/projects/"+project+"/jobs/"+jobID),
		JobReference:  BQJobRef{ProjectID: project, JobID: jobID, Location: location},
		Configuration: req.Configuration,
		Status:        map[string]any{"state": "DONE"},
		Statistics:    map[string]any{"creationTime": now, "startTime": now, "endTime": now, "load": load},
	}}
	loaded, bad, err := bqLoadRows(r, project, cfg, format, data)
	var failure *bqLoadFailure
	switch {
	case errors.As(err, &failure):
		proto := map[string]any{"reason": failure.reason, "message": failure.message}
		job.Status["errorResult"] = proto
		job.Status["errors"] = []any{proto}
	case err != nil:
		return storedBQJob{}, err
	default:
		load["outputRows"] = fmt.Sprint(loaded)
		load["badRecords"] = fmt.Sprint(bad)
	}
	bqJobs.Put(bqJobKey(project, jobID), job)
	return job, nil
}

func bqLoadProject(cfg bqLoadConfig, project string) string {
	if cfg.DestinationTable.ProjectID != "" {
		return cfg.DestinationTable.ProjectID
	}
	return project
}

// bqLoadRows parses data and writes its rows to the destination table under
// the job's create and write dispositions, returning the rows written and the
// bad records skipped.
func bqLoadRows(r *http.Request, project string, cfg bqLoadConfig, format string, data []byte) (loaded, bad int, err error) {
	dest := *cfg.DestinationTable
	dest.ProjectID = bqLoadProject(cfg, project)
	if _, ok := bqDatasets.Get(bqDatasetKey(dest.ProjectID, dest.DatasetID)); !ok {
		return 0, 0, &bqLoadFailure{"notFound", fmt.Sprintf("Not found: Dataset %s:%s", dest.ProjectID, dest.DatasetID)}
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	table, exists := bqTables.Get(key)
	if !exists && cfg.CreateDisposition == "CREATE_NEVER" {
		return 0, 0, &bqLoadFailure{"notFound", fmt.Sprintf("Not found: Table %s:%s.%s", dest.ProjectID, dest.DatasetID, dest.TableID)}
	}
	schema := cfg.Schema
	if exists && table.Schema != nil && len(table.Schema.Fields) > 0 {
		schema = table.Schema
	}
	if schema == nil || len(schema.Fields) == 0 {
		return 0, 0, &bqLoadFailure{"invalid", "No schema specified on job or table."}
	}

	var rows []map[string]any
	switch format {
	case "NEWLINE_DELIMITED_JSON":
		rows, bad, err = bqParseJSONLines(cfg, schema, data)
	default:
		rows, bad, err = bqParseCSV(cfg, schema, data)
	}
	if err != nil {
		return 0, 0, err
	}

	existing, _ := bqRows.Get(key)
	switch cfg.WriteDisposition {
	case "WRITE_TRUNCATE":
		existing.Rows = nil
	case "WRITE_EMPTY":
		if len(existing.Rows) > 0 {
			return 0, 0, &bqLoadFailure{"duplicate", fmt.Sprintf("Already Exists: Table %s:%s.%s", dest.ProjectID, dest.DatasetID, dest.TableID)}
		}
	}
	if !exists {
		table = BQTable{TableReference: dest, Schema: schema}
	}
	existing.Rows = append(existing.Rows, rows...)
	if existing.Rows == nil {
		existing.Rows = []map[string]any{}
	}
	bqRows.Put(key, existing)
	bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
	return len(rows), bad, nil
}

// bqBadRecords counts records that fail to parse, and fails the load once
// more than maxBadRecords have.
type bqBadRecords struct {
	max   int64
	count int
}

func (b *bqBadRecords) skip(line int, reason string) error {
	b.count++
	if int64(b.count) > b.max {
		return &bqLoadFailure{"invalid", fmt.Sprintf("Error while reading data, error message: %s; line %d", reason, line)}
	}
	return nil
}

func bqSchemaFields(schema *BQSchema) map[string]BQFieldSchema {
	fields := make(map[string]BQFieldSchema, len(schema.Fields))
	for _, f := range schema.Fields {
		fields[f.Name] = f
	}
	return fields
}

// bqMissingRequired names the first REQUIRED field row lacks a value for.
func bqMissingRequired(schema *BQSchema, row map[string]any) string {
	for _, f := range schema.Fields {
		if strings.EqualFold(f.Mode, "REQUIRED") && row[f.Name] == nil {
			return f.Name
		}
	}
	return ""
}

func bqParseJSONLines(cfg bqLoadConfig, schema *BQSchema, data []byte) ([]map[string]any, int, error) {
	fields := bqSchemaFields(schema)
	bad := bqBadRecords{max: cfg.MaxBadRecords}
	var rows []map[string]any
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			if err := bad.skip(i+1, "JSON parsing error: "+err.Error()); err != nil {
				return nil, 0, err
			}
			continue
		}
		unknown := ""
		for name := range row {
			if _, ok := fields[name]; ok {
				continue
			}
			if !cfg.IgnoreUnknownValues {
				unknown = name
				break
			}
			delete(row, name)
		}
		if unknown != "" {
			if err := bad.skip(i+1, "no such field: "+unknown+"."); err != nil {
				return nil, 0, err
			}
			continue
		}
		if missing := bqMissingRequired(schema, row); missing != "" {
			if err := bad.skip(i+1, "Missing required field: "+missing+"."); err != nil {
				return nil, 0, err
			}
			continue
		}
		rows = append(rows, row)
	}
	return rows, bad.count, nil
}

func bqParseCSV(cfg bqLoadConfig, schema *BQSchema, data []byte) ([]map[string]any, int, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	if delimiter := cfg.FieldDelimiter; delimiter != "" {
		if delimiter == `\t` || strings.EqualFold(delimiter, "tab") {
			delimiter = "\t"
		}
		comma, size := utf8.DecodeRuneInString(delimiter)
		if size != len(delimiter) {
			return nil, 0, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "fieldDelimiter %q is not a single character", cfg.FieldDelimiter)
		}
		reader.Comma = comma
	}
	bad := bqBadRecords{max: cfg.MaxBadRecords}
	var rows []map[string]any
	for record := int64(0); ; record++ {
		values, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var parse *csv.ParseError
			if !errors.As(err, &parse) {
				return nil, 0, err
			}
			if err := bad.skip(parse.StartLine, parse.Err.Error()); err != nil {
				return nil, 0, err
			}
			continue
		}
		line, _ := reader.FieldPos(0)
		if record < cfg.SkipLeadingRows {
			continue
		}
		if len(values) > len(schema.Fields) {
			if err := bad.skip(line, fmt.Sprintf("Too many values in row starting at position: %d", line)); err != nil {
				return nil, 0, err
			}
			continue
		}
		if len(values) < len(schema.Fields) && !cfg.AllowJaggedRows {
			if err := bad.skip(line, fmt.Sprintf("Missing value(s) in row starting at position: %d", line)); err != nil {
				return nil, 0, err
			}
			continue
		}
		row := make(map[string]any, len(schema.Fields))
		for i, f := range schema.Fields {
			if i >= len(values) || values[i] == cfg.NullMarker {
				row[f.Name] = nil
				continue
			}
			row[f.Name] = values[i]
		}
		if missing := bqMissingRequired(schema, row); missing != "" {
			if err := bad.skip(line, "Missing required field: "+missing+"."); err != nil {
				return nil, 0, err
			}
			continue
		}
		rows = append(rows, row)
	}
	return rows, bad.count, nil
}
