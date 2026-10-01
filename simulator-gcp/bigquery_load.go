package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
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

// bqLoadConfig is the JobConfigurationLoad members a load reads.
type bqLoadConfig struct {
	DestinationTable    *BQTableRef       `json:"destinationTable"`
	SourceURIs          []string          `json:"sourceUris"`
	SourceFormat        string            `json:"sourceFormat"`
	Schema              *BQSchema         `json:"schema"`
	CreateDisposition   string            `json:"createDisposition"`
	WriteDisposition    string            `json:"writeDisposition"`
	SkipLeadingRows     *int64            `json:"skipLeadingRows"`
	FieldDelimiter      string            `json:"fieldDelimiter"`
	Quote               *string           `json:"quote"`
	AllowQuotedNewlines bool              `json:"allowQuotedNewlines"`
	Encoding            string            `json:"encoding"`
	NullMarker          string            `json:"nullMarker"`
	NullMarkers         []string          `json:"nullMarkers"`
	AllowJaggedRows     bool              `json:"allowJaggedRows"`
	IgnoreUnknownValues bool              `json:"ignoreUnknownValues"`
	MaxBadRecords       int64             `json:"maxBadRecords"`
	Autodetect          bool              `json:"autodetect"`
	UseAvroLogicalTypes bool              `json:"useAvroLogicalTypes"`
	ParquetOptions      *bqParquetOptions `json:"parquetOptions"`
}

// bqSource is one file of a load's source data.
type bqSource struct {
	uri  string
	data []byte
}

// bqLoadFailure is why a job finished without writing: an ErrorProto reason
// and message.
type bqLoadFailure struct {
	reason  string
	message string
}

func (f *bqLoadFailure) Error() string { return f.message }

func bqFail(reason, format string, args ...any) error {
	return &bqLoadFailure{reason, fmt.Sprintf(format, args...)}
}

// bqDecodeConfig reads one member of a job's configuration into v.
func bqDecodeConfig(configuration map[string]any, member string, v any) error {
	encoded, err := json.Marshal(configuration[member])
	if err != nil {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s configuration: %v", member, err)
	}
	if err := json.Unmarshal(encoded, v); err != nil {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s configuration: %v", member, err)
	}
	return nil
}

// bqParseLoadConfig checks a load configuration the way jobs.insert does
// before it accepts the job, and names its source format.
func bqParseLoadConfig(configuration map[string]any) (bqLoadConfig, string, error) {
	var cfg bqLoadConfig
	if err := bqDecodeConfig(configuration, "load", &cfg); err != nil {
		return cfg, "", err
	}
	if cfg.DestinationTable == nil || cfg.DestinationTable.DatasetID == "" || cfg.DestinationTable.TableID == "" {
		return cfg, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: destinationTable")
	}
	format := cfg.SourceFormat
	if format == "" {
		format = "CSV"
	}
	switch format {
	case "CSV", "NEWLINE_DELIMITED_JSON", "AVRO", "PARQUET":
	default:
		return cfg, "", apiRefuse(http.StatusNotImplemented, "NOT_IMPLEMENTED",
			"the simulator loads CSV, NEWLINE_DELIMITED_JSON, AVRO and PARQUET source data, not %s", format)
	}
	switch strings.ToUpper(cfg.Encoding) {
	case "", "UTF-8", "UTF8", "ISO-8859-1", "UTF-16BE", "UTF-16LE", "UTF-32BE", "UTF-32LE":
	default:
		return cfg, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid encoding: %s", cfg.Encoding)
	}
	if _, _, err := bqCSVDelimiters(cfg); err != nil {
		return cfg, "", err
	}
	if cfg.NullMarker != "" && len(cfg.NullMarkers) > 0 {
		return cfg, "", apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "null_marker and null_markers cannot be set at the same time.")
	}
	return cfg, format, nil
}

func bqLoadProject(cfg bqLoadConfig, project string) string {
	if cfg.DestinationTable.ProjectID != "" {
		return cfg.DestinationTable.ProjectID
	}
	return project
}

// bqInsertLoadJob starts a load job whose source data arrived as media. A
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
	if _, ok := req.Configuration["load"]; !ok {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT",
			"a job with media must be a load job: configuration.load is required")
	}
	cfg, format, err := bqParseLoadConfig(req.Configuration)
	if err != nil {
		return storedBQJob{}, err
	}
	host := r.Clone(context.Background())
	sources := []bqSource{{uri: "media", data: data}}
	return bqStartJob(bqNewJob(r, project, req), "load", func(ctx context.Context, stats map[string]any) error {
		return bqRunLoad(ctx, host, project, cfg, format, func() ([]bqSource, error) { return sources, nil }, stats)
	})
}

// bqInsertURILoadJob starts a load job that reads its source data from the
// Cloud Storage objects configuration.load.sourceUris names.
func bqInsertURILoadJob(r *http.Request, project string, req BQJob) (storedBQJob, error) {
	cfg, format, err := bqParseLoadConfig(req.Configuration)
	if err != nil {
		return storedBQJob{}, err
	}
	if len(cfg.SourceURIs) == 0 {
		return storedBQJob{}, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "Required parameter is missing: sourceUris")
	}
	host := r.Clone(context.Background())
	return bqStartJob(bqNewJob(r, project, req), "load", func(ctx context.Context, stats map[string]any) error {
		return bqRunLoad(ctx, host, project, cfg, format, func() ([]bqSource, error) { return bqReadSourceURIs(cfg.SourceURIs) }, stats)
	})
}

// bqSplitGCSURI splits a gs:// URI into its bucket and object name.
func bqSplitGCSURI(uri string) (bucket, object string, ok bool) {
	rest, found := strings.CutPrefix(uri, "gs://")
	if !found {
		return "", "", false
	}
	bucket, object, found = strings.Cut(rest, "/")
	return bucket, object, found && bucket != "" && object != ""
}

// bqReadSourceURIs reads the objects each URI names. A URI holds at most one
// '*', after the bucket name, which matches any run of characters in an
// object name; the objects a pattern matches load in name order.
func bqReadSourceURIs(uris []string) ([]bqSource, error) {
	var sources []bqSource
	for _, uri := range uris {
		bucket, object, ok := bqSplitGCSURI(uri)
		if !ok || strings.Contains(bucket, "*") {
			return nil, bqFail("invalid", "Invalid source URI: %s. A Cloud Storage URI has the form gs://bucket/object.", uri)
		}
		if strings.Count(object, "*") > 1 {
			return nil, bqFail("invalid", "Source URI %s contains more than one '*' wildcard.", uri)
		}
		if _, ok := gcsBuckets.Get(bucket); !ok {
			return nil, bqFail("notFound", "Not found: URI %s", uri)
		}
		var objects []GCSObject
		if prefix, suffix, wildcard := strings.Cut(object, "*"); wildcard {
			for _, obj := range gcsBucketObjects(bucket, prefix) {
				if len(obj.Name) >= len(prefix)+len(suffix) && strings.HasSuffix(obj.Name, suffix) && !strings.HasSuffix(obj.Name, "/") {
					objects = append(objects, obj)
				}
			}
		} else if obj, ok := gcsObjects.Get(bucket + "/" + object); ok {
			objects = append(objects, obj)
		}
		if len(objects) == 0 {
			return nil, bqFail("notFound", "Not found: URI %s", uri)
		}
		for _, obj := range objects {
			data, err := gcsObjectBytes(obj)
			if err != nil {
				return nil, fmt.Errorf("read gs://%s/%s: %w", bucket, obj.Name, err)
			}
			sources = append(sources, bqSource{uri: "gs://" + bucket + "/" + obj.Name, data: data})
		}
	}
	return sources, nil
}

// bqGunzip inflates a gzip-compressed source, which BigQuery reads in place of
// its uncompressed contents.
func bqGunzip(src bqSource) (bqSource, error) {
	if len(src.data) < 2 || src.data[0] != 0x1f || src.data[1] != 0x8b {
		return src, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(src.data))
	if err != nil {
		return src, bqFail("invalid", "Error while reading data, error message: %s: %v", src.uri, err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return src, bqFail("invalid", "Error while reading data, error message: %s: %v", src.uri, err)
	}
	src.data = data
	return src, nil
}

// bqLoadSchema is the schema a load writes under when one is known before the
// data is read: the destination's own, unless the load truncates it, or the
// job's. Nil leaves the schema to be read from the data.
func bqLoadSchema(cfg bqLoadConfig, format string, table BQTable, exists bool) *BQSchema {
	tableSchema := exists && table.Schema != nil && len(table.Schema.Fields) > 0
	if tableSchema && cfg.WriteDisposition != "WRITE_TRUNCATE" {
		return table.Schema
	}
	if cfg.Schema != nil && len(cfg.Schema.Fields) > 0 {
		return cfg.Schema
	}
	if cfg.Autodetect || format == "AVRO" || format == "PARQUET" {
		return nil
	}
	if tableSchema {
		return table.Schema
	}
	return nil
}

// bqRunLoad reads a load's sources and writes their rows to the destination
// table under the job's create and write dispositions.
func bqRunLoad(ctx context.Context, r *http.Request, project string, cfg bqLoadConfig, format string, read func() ([]bqSource, error), stats map[string]any) error {
	dest := *cfg.DestinationTable
	dest.ProjectID = bqLoadProject(cfg, project)
	if _, ok := bqDatasets.Get(bqDatasetKey(dest.ProjectID, dest.DatasetID)); !ok {
		return bqFail("notFound", "Not found: Dataset %s:%s", dest.ProjectID, dest.DatasetID)
	}
	sources, err := read()
	if err != nil {
		return err
	}
	var inputBytes int64
	for i := range sources {
		inputBytes += int64(len(sources[i].data))
		if sources[i], err = bqGunzip(sources[i]); err != nil {
			return err
		}
	}
	stats["inputFiles"] = strconv.Itoa(len(sources))
	stats["inputFileBytes"] = strconv.FormatInt(inputBytes, 10)

	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	table, exists := bqTables.Get(key)
	if !exists && cfg.CreateDisposition == "CREATE_NEVER" {
		return bqFail("notFound", "Not found: Table %s:%s.%s", dest.ProjectID, dest.DatasetID, dest.TableID)
	}
	schema := bqLoadSchema(cfg, format, table, exists)
	if schema == nil && (format == "CSV" || format == "NEWLINE_DELIMITED_JSON") && !cfg.Autodetect {
		return bqFail("invalid", "No schema specified on job or table.")
	}
	bad := &bqBadRecords{max: cfg.MaxBadRecords}
	var rows []map[string]any
	switch format {
	case "NEWLINE_DELIMITED_JSON":
		rows, schema, err = bqParseJSONSources(cfg, schema, sources, bad)
	case "AVRO":
		rows, schema, err = bqParseAvroSources(cfg, schema, sources)
	case "PARQUET":
		rows, schema, err = bqParseParquetSources(cfg, schema, sources)
	default:
		rows, schema, err = bqParseCSVSources(cfg, schema, sources, bad)
	}
	stats["badRecords"] = strconv.Itoa(bad.count)
	if err != nil {
		return err
	}

	return bqWriteTable(ctx, dest, func() error {
		table, exists := bqTables.Get(key)
		if !exists && cfg.CreateDisposition == "CREATE_NEVER" {
			return bqFail("notFound", "Not found: Table %s:%s.%s", dest.ProjectID, dest.DatasetID, dest.TableID)
		}
		existing, _ := bqRows.Get(key)
		if cfg.WriteDisposition == "WRITE_EMPTY" && len(existing.Rows) > 0 {
			return bqFail("duplicate", "Already Exists: Table %s:%s.%s", dest.ProjectID, dest.DatasetID, dest.TableID)
		}
		switch {
		case !exists:
			table = BQTable{TableReference: dest, Schema: schema}
		case cfg.WriteDisposition == "WRITE_TRUNCATE" || table.Schema == nil || len(table.Schema.Fields) == 0:
			table.Schema = schema
		}
		if cfg.WriteDisposition == "WRITE_TRUNCATE" || cfg.WriteDisposition == "WRITE_TRUNCATE_DATA" {
			existing.Rows = nil
		}
		existing.Rows = append(existing.Rows, rows...)
		if existing.Rows == nil {
			existing.Rows = []map[string]any{}
		}
		bqRows.Put(key, existing)
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		stats["outputRows"] = strconv.Itoa(len(rows))
		stats["outputBytes"] = strconv.FormatInt(bqRowsSize(table.Schema, rows), 10)
		return nil
	})
}

// bqBadRecords counts records that fail to parse, and fails the load once
// more than maxBadRecords have.
type bqBadRecords struct {
	max   int64
	count int
}

func (b *bqBadRecords) skip(uri string, line int, reason string) error {
	b.count++
	if int64(b.count) > b.max {
		return bqFail("invalid", "Error while reading data, error message: %s; line %d, file: %s", reason, line, uri)
	}
	return nil
}

// bqDecodeText decodes source data in the load's character encoding.
func bqDecodeText(data []byte, encoding string) string {
	switch strings.ToUpper(encoding) {
	case "ISO-8859-1":
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		return string(runes)
	case "UTF-16BE", "UTF-16LE":
		order := binary.ByteOrder(binary.BigEndian)
		if strings.HasSuffix(strings.ToUpper(encoding), "LE") {
			order = binary.LittleEndian
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[2*i:])
		}
		return strings.TrimPrefix(string(utf16.Decode(units)), "\uFEFF")
	case "UTF-32BE", "UTF-32LE":
		order := binary.ByteOrder(binary.BigEndian)
		if strings.HasSuffix(strings.ToUpper(encoding), "LE") {
			order = binary.LittleEndian
		}
		runes := make([]rune, len(data)/4)
		for i := range runes {
			runes[i] = rune(order.Uint32(data[4*i:]))
		}
		return strings.TrimPrefix(string(runes), "\uFEFF")
	default:
		return strings.TrimPrefix(string(data), "\uFEFF")
	}
}

// bqCSVDelimiters reads the field separator and quote character of a CSV
// load. A quote of "" turns quoting off.
func bqCSVDelimiters(cfg bqLoadConfig) (delim, quote rune, err error) {
	delim, quote = ',', '"'
	if d := cfg.FieldDelimiter; d != "" {
		if d == `\t` || strings.EqualFold(d, "tab") {
			d = "\t"
		}
		r, size := utf8.DecodeRuneInString(d)
		if size != len(d) {
			return 0, 0, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "fieldDelimiter %q is not a single character", cfg.FieldDelimiter)
		}
		delim = r
	}
	if cfg.Quote != nil {
		if *cfg.Quote == "" {
			quote = 0
		} else {
			r, size := utf8.DecodeRuneInString(*cfg.Quote)
			if size != len(*cfg.Quote) {
				return 0, 0, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "quote %q is not a single character", *cfg.Quote)
			}
			quote = r
		}
	}
	if delim == quote {
		return 0, 0, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "fieldDelimiter and quote must differ")
	}
	return delim, quote, nil
}

// bqCSVRecord is one record of a CSV file and the line it starts on. err
// names what makes it unreadable.
type bqCSVRecord struct {
	line   int
	values []string
	err    string
}

// bqSplitCSV splits CSV text into records. A quoted field may hold the
// separator and, doubled, the quote character; it may hold a newline only
// when quotedNewlines is set. Blank lines hold no record.
func bqSplitCSV(text string, delim, quote rune, quotedNewlines bool) []bqCSVRecord {
	const (
		unquoted = iota
		quoted
		closed
		broken
	)
	var (
		records  []bqCSVRecord
		values   []string
		field    strings.Builder
		state    = unquoted
		line     = 1
		start    = 1
		sawQuote bool
		problem  string
	)
	endField := func() {
		values = append(values, field.String())
		field.Reset()
		state = unquoted
	}
	endRecord := func() {
		endField()
		if len(values) > 1 || values[0] != "" || sawQuote || problem != "" {
			records = append(records, bqCSVRecord{line: start, values: values, err: problem})
		}
		values, sawQuote, problem = nil, false, ""
		start = line
	}
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\r' && i+1 < len(runes) && runes[i+1] == '\n' && state != quoted {
			continue
		}
		switch state {
		case quoted:
			switch {
			case c == quote && i+1 < len(runes) && runes[i+1] == quote:
				field.WriteRune(quote)
				i++
			case c == quote:
				state = closed
			case c == '\n' && !quotedNewlines:
				line++
				problem = "Missing close double quote (\") character."
				endRecord()
			case c == '\n':
				line++
				field.WriteRune(c)
			default:
				field.WriteRune(c)
			}
		case closed:
			switch c {
			case delim:
				endField()
			case '\n':
				line++
				endRecord()
			default:
				problem = "Data between close double quote (\") and field separator."
				state = broken
			}
		case broken:
			if c == '\n' {
				line++
				endRecord()
			}
		default:
			switch {
			case quote != 0 && c == quote && field.Len() == 0:
				state = quoted
				sawQuote = true
			case c == delim:
				endField()
			case c == '\n':
				line++
				endRecord()
			default:
				field.WriteRune(c)
			}
		}
	}
	if state == quoted {
		problem = "Missing close double quote (\") character."
	}
	if field.Len() > 0 || len(values) > 0 || sawQuote || problem != "" {
		endRecord()
	}
	return records
}

// bqCSVFile is one CSV source split into the records that hold data and the
// record that may name the columns.
type bqCSVFile struct {
	uri    string
	header *bqCSVRecord
	data   []bqCSVRecord
}

// bqSplitCSVFile drops the rows skipLeadingRows skips. Under autodetect the
// last skipped row, or the first row when skipLeadingRows is unset, may be a
// header and is held apart.
func bqSplitCSVFile(cfg bqLoadConfig, src bqSource, delim, quote rune) bqCSVFile {
	records := bqSplitCSV(bqDecodeText(src.data, cfg.Encoding), delim, quote, cfg.AllowQuotedNewlines)
	file := bqCSVFile{uri: src.uri}
	skip := 0
	if cfg.SkipLeadingRows != nil {
		skip = int(*cfg.SkipLeadingRows)
	}
	candidate := -1
	switch {
	case cfg.Autodetect && cfg.SkipLeadingRows == nil:
		candidate = 0
	case cfg.Autodetect && skip > 0:
		candidate = skip - 1
	}
	for i := range records {
		switch {
		case i == candidate:
			file.header = &records[i]
		case i < skip:
		default:
			file.data = append(file.data, records[i])
		}
	}
	return file
}

// bqAutodetectSample is how many rows schema detection reads.
const bqAutodetectSample = 500

// bqCSVHasHeader reports whether candidate names the columns: every value in
// it reads only as a STRING while some column below it holds another type.
func bqCSVHasHeader(candidate *bqCSVRecord, columns []string) bool {
	if candidate == nil || candidate.err != "" {
		return false
	}
	for _, v := range candidate.values {
		if strings.TrimSpace(v) == "" || bqDetectText(v) != "STRING" {
			return false
		}
	}
	for _, t := range columns {
		if t != "" && t != "STRING" {
			return true
		}
	}
	return false
}

// bqDetectCSVColumns is the type each column's sampled values share.
func bqDetectCSVColumns(rows []bqCSVRecord) []string {
	var columns []string
	for i, row := range rows {
		if i >= bqAutodetectSample {
			break
		}
		if row.err != "" {
			continue
		}
		for c, v := range row.values {
			for len(columns) <= c {
				columns = append(columns, "")
			}
			if strings.TrimSpace(v) == "" {
				continue
			}
			columns[c] = bqMergeType(columns[c], bqDetectText(v))
		}
	}
	return columns
}

func bqParseCSVSources(cfg bqLoadConfig, schema *BQSchema, sources []bqSource, bad *bqBadRecords) ([]map[string]any, *BQSchema, error) {
	delim, quote, err := bqCSVDelimiters(cfg)
	if err != nil {
		return nil, nil, err
	}
	files := make([]bqCSVFile, 0, len(sources))
	for _, src := range sources {
		files = append(files, bqSplitCSVFile(cfg, src, delim, quote))
	}
	if cfg.Autodetect && len(files) > 0 {
		columns := bqDetectCSVColumns(files[0].data)
		header := files[0].header
		hasHeader := bqCSVHasHeader(header, columns)
		for i := range files {
			if files[i].header != nil && !hasHeader && cfg.SkipLeadingRows == nil {
				files[i].data = append([]bqCSVRecord{*files[i].header}, files[i].data...)
			}
		}
		if schema == nil {
			if !hasHeader && header != nil && cfg.SkipLeadingRows == nil {
				columns = bqDetectCSVColumns(files[0].data)
			}
			schema = bqCSVSchema(columns, header, hasHeader)
		}
	}
	for _, f := range schema.Fields {
		if bqNested(f) {
			return nil, nil, bqFail("invalid", "CSV data cannot load into a nested or repeated field: %s", f.Name)
		}
	}
	markers := cfg.NullMarkers
	if len(markers) == 0 {
		markers = []string{cfg.NullMarker}
	}
	var rows []map[string]any
	for _, file := range files {
		for _, record := range file.data {
			row, reason := bqCSVRow(cfg, schema, markers, record)
			if reason != "" {
				if err := bad.skip(file.uri, record.line, reason); err != nil {
					return nil, nil, err
				}
				continue
			}
			rows = append(rows, row)
		}
	}
	return rows, schema, nil
}

// bqCSVSchema names detected CSV columns from the header row, or, without
// one, by type and position.
func bqCSVSchema(columns []string, header *bqCSVRecord, hasHeader bool) *BQSchema {
	schema := &BQSchema{}
	seen := map[string]bool{}
	for i, t := range columns {
		if t == "" {
			t = "STRING"
		}
		name := ""
		if hasHeader && i < len(header.values) {
			name = bqColumnName(header.values[i])
		}
		if name == "" || seen[strings.ToLower(name)] {
			name = fmt.Sprintf("%s_field_%d", bqDetectedTypePrefix(t), i)
		}
		seen[strings.ToLower(name)] = true
		schema.Fields = append(schema.Fields, BQFieldSchema{Name: name, Type: t, Mode: "NULLABLE"})
	}
	return schema
}

func bqDetectedTypePrefix(t string) string {
	switch t {
	case "INTEGER":
		return "int64"
	case "FLOAT":
		return "double"
	case "BOOLEAN":
		return "bool"
	default:
		return strings.ToLower(t)
	}
}

// bqColumnName turns a header value into a column name: letters, digits and
// underscores, not starting with a digit.
func bqColumnName(v string) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(v) {
		if c == '_' || c < utf8.RuneSelf && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// bqCSVRow converts a record's values to a row, or names why it is bad.
func bqCSVRow(cfg bqLoadConfig, schema *BQSchema, markers []string, record bqCSVRecord) (map[string]any, string) {
	if record.err != "" {
		return nil, fmt.Sprintf("Error detected while parsing row starting at position: %d. Error: %s", record.line, record.err)
	}
	if len(record.values) > len(schema.Fields) {
		return nil, fmt.Sprintf("Too many values in row starting at position: %d", record.line)
	}
	if len(record.values) < len(schema.Fields) && !cfg.AllowJaggedRows {
		return nil, fmt.Sprintf("Missing value(s) in row starting at position: %d", record.line)
	}
	row := make(map[string]any, len(schema.Fields))
	for i, f := range schema.Fields {
		if i >= len(record.values) || bqIsNullMarker(markers, record.values[i], bqType(f)) {
			if strings.EqualFold(f.Mode, "REQUIRED") {
				return nil, "Missing required field: " + f.Name + "."
			}
			row[f.Name] = nil
			continue
		}
		v, err := bqCoerce(f, record.values[i], false)
		if err != nil {
			return nil, err.Error()
		}
		row[f.Name] = v
	}
	return row, ""
}

// bqIsNullMarker reports whether a CSV value is NULL. An empty value is NULL
// under the default marker; under a custom one it is an empty STRING or BYTES.
func bqIsNullMarker(markers []string, v, typ string) bool {
	for _, m := range markers {
		if v == m {
			return true
		}
	}
	return v == "" && typ != "STRING" && typ != "BYTES"
}

// bqParseJSONSources reads newline-delimited JSON, detecting the schema from
// the records when none is known.
func bqParseJSONSources(cfg bqLoadConfig, schema *BQSchema, sources []bqSource, bad *bqBadRecords) ([]map[string]any, *BQSchema, error) {
	type record struct {
		uri   string
		line  int
		value any
		err   string
	}
	var records []record
	for _, src := range sources {
		text := bqDecodeText(src.data, cfg.Encoding)
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			value, err := bqDecodeOrderedJSON([]byte(line))
			rec := record{uri: src.uri, line: i + 1, value: value}
			if err != nil {
				rec.err = "JSON parsing error in row starting at position " + strconv.Itoa(i+1) + ": " + err.Error()
			} else if _, ok := value.(*bqOrderedMap); !ok {
				rec.err = "JSON parsing error in row starting at position " + strconv.Itoa(i+1) + ": the record is not a JSON object"
			}
			records = append(records, rec)
		}
	}
	if schema == nil {
		var fields []BQFieldSchema
		sampled := 0
		for _, rec := range records {
			if rec.err != "" {
				continue
			}
			if sampled++; sampled > bqAutodetectSample {
				break
			}
			obj, ok := rec.value.(*bqOrderedMap)
			if !ok {
				continue
			}
			for _, k := range obj.keys {
				if f, ok := bqDetectJSONField(k, obj.values[k]); ok {
					fields = bqMergeFields(fields, f)
				}
			}
		}
		schema = &BQSchema{Fields: fields}
		if len(fields) == 0 {
			return nil, nil, bqFail("invalid", "Schema detection found no fields in the source data.")
		}
	}
	var rows []map[string]any
	for _, rec := range records {
		if rec.err != "" {
			if err := bad.skip(rec.uri, rec.line, rec.err); err != nil {
				return nil, nil, err
			}
			continue
		}
		obj, _ := bqPlainJSON(rec.value).(map[string]any)
		row, err := bqCoerceRecord("", schema.Fields, obj, cfg.IgnoreUnknownValues)
		if err != nil {
			if err := bad.skip(rec.uri, rec.line, err.Error()); err != nil {
				return nil, nil, err
			}
			continue
		}
		rows = append(rows, row)
	}
	return rows, schema, nil
}

// bqOrderedMap is a JSON object that remembers the order of its members, the
// order schema detection names columns in.
type bqOrderedMap struct {
	keys   []string
	values map[string]any
}

func bqDecodeOrderedJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := bqDecodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return v, nil
}

func bqDecodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &bqOrderedMap{values: map[string]any{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyTok.(string)
				v, err := bqDecodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := obj.values[key]; !dup {
					obj.keys = append(obj.keys, key)
				}
				obj.values[key] = v
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			items := []any{}
			for dec.More() {
				v, err := bqDecodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			}
			_, err := dec.Token()
			return items, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// bqPlainJSON drops the member order of decoded JSON.
func bqPlainJSON(v any) any {
	switch t := v.(type) {
	case *bqOrderedMap:
		out := make(map[string]any, len(t.values))
		for k, item := range t.values {
			out[k] = bqPlainJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = bqPlainJSON(item)
		}
		return out
	default:
		return v
	}
}

// bqDetectText is the type a text value reads as under schema detection.
func bqDetectText(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "true") || strings.EqualFold(v, "false") {
		return "BOOLEAN"
	}
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return "INTEGER"
	}
	if bqDecimalRE.MatchString(v) {
		return "FLOAT"
	}
	if bqDateRE.MatchString(v) {
		return "DATE"
	}
	if bqTimeRE.MatchString(v) {
		return "TIME"
	}
	if bqTimestampRE.MatchString(v) {
		if _, err := bqParseTimestamp(v, v); err == nil {
			return "TIMESTAMP"
		}
	}
	return "STRING"
}

// bqMergeType is the type that holds values of both a and b.
func bqMergeType(a, b string) string {
	switch {
	case a == "" || a == b:
		return b
	case b == "":
		return a
	case (a == "INTEGER" && b == "FLOAT") || (a == "FLOAT" && b == "INTEGER"):
		return "FLOAT"
	default:
		return "STRING"
	}
}

// bqDetectJSONField is the field a JSON value detects as. A NULL or an empty
// array says nothing about the field's type.
func bqDetectJSONField(name string, v any) (BQFieldSchema, bool) {
	f := BQFieldSchema{Name: name, Mode: "NULLABLE"}
	switch t := v.(type) {
	case bool:
		f.Type = "BOOLEAN"
	case json.Number:
		f.Type = "FLOAT"
		if _, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			f.Type = "INTEGER"
		}
	case string:
		f.Type = "STRING"
		if detected := bqDetectText(t); detected == "DATE" || detected == "TIME" || detected == "TIMESTAMP" {
			f.Type = detected
		}
	case *bqOrderedMap:
		f.Type = "RECORD"
		for _, k := range t.keys {
			if sub, ok := bqDetectJSONField(k, t.values[k]); ok {
				f.Fields = bqMergeFields(f.Fields, sub)
			}
		}
		if len(f.Fields) == 0 {
			return f, false
		}
	case []any:
		var elem *BQFieldSchema
		for _, item := range t {
			if _, nested := item.([]any); nested {
				continue
			}
			if e, ok := bqDetectJSONField(name, item); ok {
				if elem == nil {
					elem = &e
				} else {
					merged := bqMergeField(*elem, e)
					elem = &merged
				}
			}
		}
		if elem == nil {
			return f, false
		}
		f = *elem
		f.Mode = "REPEATED"
	default:
		return f, false
	}
	return f, true
}

func bqMergeField(a, b BQFieldSchema) BQFieldSchema {
	out := a
	if a.Type == "RECORD" && b.Type == "RECORD" {
		for _, sub := range b.Fields {
			out.Fields = bqMergeFields(out.Fields, sub)
		}
	} else {
		out.Type = bqMergeType(a.Type, b.Type)
		if a.Type == "RECORD" || b.Type == "RECORD" {
			out.Type, out.Fields = "STRING", nil
		}
	}
	if a.Mode == "REPEATED" || b.Mode == "REPEATED" {
		out.Mode = "REPEATED"
	}
	return out
}

// bqMergeFields adds f to fields, merging it into a field of the same name.
func bqMergeFields(fields []BQFieldSchema, f BQFieldSchema) []BQFieldSchema {
	for i := range fields {
		if strings.EqualFold(fields[i].Name, f.Name) {
			fields[i] = bqMergeField(fields[i], f)
			return fields
		}
	}
	return append(fields, f)
}

// bqSchemaByName matches a self-describing source's columns to an existing
// schema by name and keeps the existing schema's order.
func bqSchemaByName(schema *BQSchema, fileFields []BQFieldSchema) error {
	byName := map[string]bool{}
	for _, f := range fileFields {
		byName[strings.ToLower(f.Name)] = true
	}
	var missing []string
	for _, f := range schema.Fields {
		if strings.EqualFold(f.Mode, "REQUIRED") && !byName[strings.ToLower(f.Name)] {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return bqFail("invalid", "Provided Schema does not match Table. Field %s is missing in new schema", missing[0])
	}
	return nil
}
