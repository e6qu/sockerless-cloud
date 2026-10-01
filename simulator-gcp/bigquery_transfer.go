package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

type bqCopyConfig struct {
	SourceTable       *BQTableRef  `json:"sourceTable"`
	SourceTables      []BQTableRef `json:"sourceTables"`
	DestinationTable  *BQTableRef  `json:"destinationTable"`
	CreateDisposition string       `json:"createDisposition"`
	WriteDisposition  string       `json:"writeDisposition"`
	OperationType     string       `json:"operationType"`
}

// bqInsertCopyJob starts a copy job: the rows of one or more tables that
// share a schema, written to the destination as one atomic update.
func bqInsertCopyJob(r *http.Request, project string, req BQJob) (storedBQJob, error) {
	var cfg bqCopyConfig
	if err := bqDecodeConfig(req.Configuration, "copy", &cfg); err != nil {
		return storedBQJob{}, err
	}
	sources := cfg.SourceTables
	if len(sources) == 0 && cfg.SourceTable != nil {
		sources = []BQTableRef{*cfg.SourceTable}
	}
	if len(sources) == 0 {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: sourceTables")
	}
	if cfg.DestinationTable == nil || cfg.DestinationTable.DatasetID == "" || cfg.DestinationTable.TableID == "" {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: destinationTable")
	}
	switch cfg.OperationType {
	case "", "OPERATION_TYPE_UNSPECIFIED", "COPY":
	default:
		return storedBQJob{}, apiRefuse(http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"the simulator runs copy jobs of operationType COPY, not %s", cfg.OperationType)
	}
	for i := range sources {
		if sources[i].DatasetID == "" || sources[i].TableID == "" {
			return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid source table reference")
		}
		if sources[i].ProjectID == "" {
			sources[i].ProjectID = project
		}
	}
	dest := *cfg.DestinationTable
	if dest.ProjectID == "" {
		dest.ProjectID = project
	}
	host := r.Clone(context.Background())
	return bqStartJob(bqNewJob(r, project, req), "copy", func(ctx context.Context, stats map[string]any) error {
		return bqRunCopy(ctx, host, cfg, sources, dest, stats)
	})
}

func bqTableName(ref BQTableRef) string {
	return ref.ProjectID + ":" + ref.DatasetID + "." + ref.TableID
}

func bqSchemaFields(schema *BQSchema) []BQFieldSchema {
	if schema == nil {
		return nil
	}
	return schema.Fields
}

// bqSameSchema compares two schemas field by field, folding the legacy type
// names and the default NULLABLE mode.
func bqSameSchema(a, b []BQFieldSchema) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		modeA, modeB := strings.ToUpper(a[i].Mode), strings.ToUpper(b[i].Mode)
		if modeA == "" {
			modeA = "NULLABLE"
		}
		if modeB == "" {
			modeB = "NULLABLE"
		}
		if !strings.EqualFold(a[i].Name, b[i].Name) || bqType(a[i]) != bqType(b[i]) || modeA != modeB || !bqSameSchema(a[i].Fields, b[i].Fields) {
			return false
		}
	}
	return true
}

func bqRunCopy(ctx context.Context, r *http.Request, cfg bqCopyConfig, sources []BQTableRef, dest BQTableRef, stats map[string]any) error {
	var (
		first BQTable
		rows  []map[string]any
	)
	for i, ref := range sources {
		table, ok := bqTables.Get(bqTableKey(ref.ProjectID, ref.DatasetID, ref.TableID))
		if !ok {
			return bqFail("notFound", "Not found: Table %s", bqTableName(ref))
		}
		if table.Type != "" && table.Type != "TABLE" {
			return bqFail("invalid", "Cannot copy %s %s: a copy job reads tables.", strings.ToLower(table.Type), bqTableName(ref))
		}
		if i == 0 {
			first = table
		} else if !bqSameSchema(bqSchemaFields(first.Schema), bqSchemaFields(table.Schema)) {
			return bqFail("invalid", "Incompatible table schemas: %s and %s must share a schema to be copied together.",
				bqTableName(sources[0]), bqTableName(ref))
		}
		set, _ := bqRows.Get(bqTableKey(ref.ProjectID, ref.DatasetID, ref.TableID))
		for _, row := range set.Rows {
			copied := make(map[string]any, len(row))
			for k, v := range row {
				copied[k] = v
			}
			rows = append(rows, copied)
		}
	}
	if _, ok := bqDatasets.Get(bqDatasetKey(dest.ProjectID, dest.DatasetID)); !ok {
		return bqFail("notFound", "Not found: Dataset %s:%s", dest.ProjectID, dest.DatasetID)
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	return bqWriteTable(ctx, dest, func() error {
		table, exists := bqTables.Get(key)
		if !exists && cfg.CreateDisposition == "CREATE_NEVER" {
			return bqFail("notFound", "Not found: Table %s", bqTableName(dest))
		}
		existing, _ := bqRows.Get(key)
		switch cfg.WriteDisposition {
		case "WRITE_TRUNCATE":
			existing.Rows = nil
		case "WRITE_APPEND":
			if exists && !bqSameSchema(bqSchemaFields(table.Schema), bqSchemaFields(first.Schema)) {
				return bqFail("invalid", "Provided Schema does not match Table %s.", bqTableName(dest))
			}
		default:
			if len(existing.Rows) > 0 {
				return bqFail("duplicate", "Already Exists: Table %s", bqTableName(dest))
			}
		}
		if !exists {
			table = BQTable{
				TableReference:    dest,
				TimePartitioning:  first.TimePartitioning,
				RangePartitioning: first.RangePartitioning,
				Clustering:        first.Clustering,
			}
		}
		table.Schema = first.Schema
		existing.Rows = append(existing.Rows, rows...)
		if existing.Rows == nil {
			existing.Rows = []map[string]any{}
		}
		bqRows.Put(key, existing)
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		stats["copiedRows"] = strconv.Itoa(len(rows))
		stats["copiedLogicalBytes"] = strconv.FormatInt(bqRowsSize(first.Schema, rows), 10)
		return nil
	})
}

type bqExtractConfig struct {
	SourceTable         *BQTableRef    `json:"sourceTable"`
	SourceModel         map[string]any `json:"sourceModel"`
	DestinationURIs     []string       `json:"destinationUris"`
	DestinationURI      string         `json:"destinationUri"`
	DestinationFormat   string         `json:"destinationFormat"`
	Compression         string         `json:"compression"`
	FieldDelimiter      string         `json:"fieldDelimiter"`
	PrintHeader         *bool          `json:"printHeader"`
	UseAvroLogicalTypes bool           `json:"useAvroLogicalTypes"`
}

// bqExtractCompressions is the compression each destination format takes.
var bqExtractCompressions = map[string][]string{
	"CSV":                    {"NONE", "GZIP"},
	"NEWLINE_DELIMITED_JSON": {"NONE", "GZIP"},
	"AVRO":                   {"NONE", "DEFLATE", "SNAPPY"},
	"PARQUET":                {"NONE", "GZIP", "SNAPPY", "ZSTD"},
}

// bqExtractFileLimit is the most table data BigQuery writes to one file; a
// larger export needs a wildcard URI to shard into.
const bqExtractFileLimit = 1 << 30

// bqInsertExtractJob starts an extract job, which writes a table's rows to
// Cloud Storage.
func bqInsertExtractJob(r *http.Request, project string, req BQJob) (storedBQJob, error) {
	var cfg bqExtractConfig
	if err := bqDecodeConfig(req.Configuration, "extract", &cfg); err != nil {
		return storedBQJob{}, err
	}
	if cfg.SourceModel != nil {
		return storedBQJob{}, apiRefuse(http.StatusNotImplemented, "NOT_IMPLEMENTED", "the simulator extracts tables, not models")
	}
	if cfg.SourceTable == nil || cfg.SourceTable.DatasetID == "" || cfg.SourceTable.TableID == "" {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: sourceTable")
	}
	uris := cfg.DestinationURIs
	if len(uris) == 0 && cfg.DestinationURI != "" {
		uris = []string{cfg.DestinationURI}
	}
	if len(uris) == 0 {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: destinationUris")
	}
	for _, uri := range uris {
		bucket, object, ok := bqSplitGCSURI(uri)
		if !ok || strings.Contains(bucket, "*") || strings.Count(object, "*") > 1 {
			return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT",
				"Invalid extract destination URI '%s'. Must be a valid Google Storage path with at most one '*' in the object name.", uri)
		}
	}
	format := cfg.DestinationFormat
	if format == "" {
		format = "CSV"
	}
	compressions, ok := bqExtractCompressions[format]
	if !ok {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid destination format for a table extract: %s", format)
	}
	compression := strings.ToUpper(cfg.Compression)
	if compression == "" {
		compression = "NONE"
	}
	supported := false
	for _, c := range compressions {
		supported = supported || c == compression
	}
	if !supported {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT",
			"Compression %s is not supported for destination format %s", compression, format)
	}
	delim := ','
	if d := cfg.FieldDelimiter; d != "" {
		if d == `\t` || strings.EqualFold(d, "tab") {
			d = "\t"
		}
		r, size := utf8.DecodeRuneInString(d)
		if size != len(d) {
			return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "fieldDelimiter %q is not a single character", cfg.FieldDelimiter)
		}
		delim = r
	}
	source := *cfg.SourceTable
	if source.ProjectID == "" {
		source.ProjectID = project
	}
	return bqStartJob(bqNewJob(r, project, req), "extract", func(ctx context.Context, stats map[string]any) error {
		return bqRunExtract(ctx, cfg, source, uris, format, compression, delim, stats)
	})
}

// bqExtractShard is one file an extract writes.
type bqExtractShard struct {
	bucket, object string
	data           []byte
}

func bqRunExtract(ctx context.Context, cfg bqExtractConfig, source BQTableRef, uris []string, format, compression string, delim rune, stats map[string]any) error {
	table, ok := bqTables.Get(bqTableKey(source.ProjectID, source.DatasetID, source.TableID))
	if !ok {
		return bqFail("notFound", "Not found: Table %s", bqTableName(source))
	}
	if table.Type != "" && table.Type != "TABLE" {
		return bqFail("invalid", "Cannot extract %s %s: an extract job reads tables.", strings.ToLower(table.Type), bqTableName(source))
	}
	fields := bqSchemaFields(table.Schema)
	if format == "CSV" {
		for _, f := range fields {
			if bqNested(f) {
				return bqFail("invalid", "Operation cannot be performed on a nested schema. Field: %s", f.Name)
			}
		}
	}
	for _, uri := range uris {
		bucket, _, _ := bqSplitGCSURI(uri)
		if _, ok := gcsBuckets.Get(bucket); !ok {
			return bqFail("notFound", "Not found: URI %s", uri)
		}
	}
	set, _ := bqRows.Get(bqTableKey(source.ProjectID, source.DatasetID, source.TableID))
	stats["inputBytes"] = strconv.FormatInt(bqRowsSize(table.Schema, set.Rows), 10)

	var shards []bqExtractShard
	counts := make([]string, 0, len(uris))
	for i, uri := range uris {
		part := set.Rows[len(set.Rows)*i/len(uris) : len(set.Rows)*(i+1)/len(uris)]
		files, err := bqEncodeExtract(cfg, fields, part, format, compression, delim)
		if err != nil {
			return err
		}
		bucket, object, _ := bqSplitGCSURI(uri)
		prefix, suffix, wildcard := strings.Cut(object, "*")
		if !wildcard && len(files) > 1 {
			return bqFail("invalid", "Table %s too large to be exported to a single file. Specify a uri including a * to shard export.", bqTableName(source))
		}
		for n, data := range files {
			name := object
			if wildcard {
				name = fmt.Sprintf("%s%012d%s", prefix, n, suffix)
			}
			shards = append(shards, bqExtractShard{bucket: bucket, object: name, data: data})
		}
		counts = append(counts, strconv.Itoa(len(files)))
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	for _, shard := range shards {
		if _, err := persistGCSObjectBytes(shard.bucket, shard.object, shard.data,
			GCSObject{ContentType: "application/octet-stream"}, gcsPreconditions{}); err != nil {
			return err
		}
	}
	stats["destinationUriFileCounts"] = counts
	return nil
}

// bqEncodeExtract writes rows in format, starting a new file whenever one
// reaches bqExtractFileLimit. A destination receives at least one file, which
// for CSV holds the header alone when there are no rows.
func bqEncodeExtract(cfg bqExtractConfig, fields []BQFieldSchema, rows []map[string]any, format, compression string, delim rune) ([][]byte, error) {
	switch format {
	case "AVRO":
		data, err := bqEncodeAvro(fields, rows, compression, cfg.UseAvroLogicalTypes)
		return [][]byte{data}, err
	case "PARQUET":
		data, err := bqEncodeParquet(fields, rows, compression)
		return [][]byte{data}, err
	}
	header := cfg.PrintHeader == nil || *cfg.PrintHeader
	var (
		files [][]byte
		buf   bytes.Buffer
	)
	start := func() error {
		buf.Reset()
		if format == "CSV" && header {
			w := csv.NewWriter(&buf)
			w.Comma = delim
			names := make([]string, len(fields))
			for i, f := range fields {
				names[i] = f.Name
			}
			if err := w.Write(names); err != nil {
				return err
			}
			w.Flush()
			return w.Error()
		}
		return nil
	}
	finish := func() error {
		data := append([]byte(nil), buf.Bytes()...)
		if compression == "GZIP" {
			var zipped bytes.Buffer
			zw := gzip.NewWriter(&zipped)
			if _, err := zw.Write(data); err != nil {
				return err
			}
			if err := zw.Close(); err != nil {
				return err
			}
			data = zipped.Bytes()
		}
		files = append(files, data)
		return nil
	}
	if err := start(); err != nil {
		return nil, err
	}
	empty := true
	for _, row := range rows {
		line, err := bqEncodeExtractRow(fields, row, format, delim)
		if err != nil {
			return nil, err
		}
		if !empty && buf.Len()+len(line) > bqExtractFileLimit {
			if err := finish(); err != nil {
				return nil, err
			}
			if err := start(); err != nil {
				return nil, err
			}
		}
		buf.Write(line)
		empty = false
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return files, nil
}

func bqEncodeExtractRow(fields []BQFieldSchema, row map[string]any, format string, delim rune) ([]byte, error) {
	if format == "NEWLINE_DELIMITED_JSON" {
		line, err := json.Marshal(bqRowJSON(fields, row))
		return append(line, '\n'), err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = delim
	values := make([]string, len(fields))
	for i, f := range fields {
		if v := row[f.Name]; v != nil {
			values[i] = bqCellText(f, v)
		}
	}
	if err := w.Write(values); err != nil {
		return nil, err
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}
