package main

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Query Blob Contents (`POST ?comp=query`). The operation runs a SQL-shaped
// query over the blob's stored bytes and streams the result back in the Avro
// object-container framing Azure uses, whose records carry the result data, the
// scan progress, and the end-of-stream total.
//
// The evaluated grammar is the one the official SDKs emit:
//
//	SELECT * FROM BlobStorage [WHERE <predicate>]
//	SELECT <column>[, <column>…] FROM BlobStorage [WHERE <predicate>]
//
// where a column is `_N` (1-based positional, the spelling used for headerless
// delimited input) or a header name, and a predicate is a conjunction or
// disjunction of `<column> <op> <literal>` comparisons with op one of
// = <> != > >= < <=. Anything outside that — a join, an aggregate, a function
// call, a sub-query — is refused with a ParseError naming the construct rather
// than answered with data the query did not ask for.

// blobQueryRequest is the <QueryRequest> body of the operation.
type blobQueryRequest struct {
	XMLName             xml.Name                `xml:"QueryRequest"`
	QueryType           string                  `xml:"QueryType"`
	Expression          string                  `xml:"Expression"`
	InputSerialization  *blobQuerySerialization `xml:"InputSerialization"`
	OutputSerialization *blobQuerySerialization `xml:"OutputSerialization"`
}

type blobQuerySerialization struct {
	Format blobQueryFormat `xml:"Format"`
}

type blobQueryFormat struct {
	Type              string                  `xml:"Type"`
	DelimitedTextConf *blobQueryDelimitedConf `xml:"DelimitedTextConfiguration"`
	JSONTextConf      *blobQueryJSONConf      `xml:"JsonTextConfiguration"`
}

type blobQueryDelimitedConf struct {
	ColumnSeparator string `xml:"ColumnSeparator"`
	FieldQuote      string `xml:"FieldQuote"`
	RecordSeparator string `xml:"RecordSeparator"`
	EscapeChar      string `xml:"EscapeChar"`
	HasHeaders      bool   `xml:"HasHeaders"`
}

type blobQueryJSONConf struct {
	RecordSeparator string `xml:"RecordSeparator"`
}

func handleBlobQuery(w http.ResponseWriter, r *http.Request, account, container, blob string) {
	defer r.Body.Close()
	var req blobQueryRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"XML specified is not syntactically valid.", http.StatusBadRequest)
		return
	}
	if req.QueryType != "" && !strings.EqualFold(req.QueryType, "SQL") {
		writeStorageError(w, "InvalidQueryParameterValue",
			"Value for one of the query parameters specified in the request URI is invalid: QueryType.",
			http.StatusBadRequest)
		return
	}
	b, ok := lookupBlob(r, account, container, blob)
	if !ok {
		writeStorageError(w, "BlobNotFound",
			"The specified blob does not exist.", http.StatusNotFound)
		return
	}
	if !blobLeaseAccessOK(w, r, b.Lease, "blob") {
		return
	}

	query, err := parseBlobQueryExpression(req.Expression)
	if err != nil {
		writeStorageError(w, "ParseError", err.Error(), http.StatusBadRequest)
		return
	}
	rows, headers, err := decodeBlobQueryInput(b.Data, req.InputSerialization)
	if err != nil {
		writeStorageError(w, "ParseError", err.Error(), http.StatusBadRequest)
		return
	}
	selected, err := query.run(rows, headers)
	if err != nil {
		writeStorageError(w, "ParseError", err.Error(), http.StatusBadRequest)
		return
	}
	payload, err := encodeBlobQueryOutput(selected, headers, query, req.OutputSerialization)
	if err != nil {
		writeStorageError(w, "InvalidQueryParameterValue", err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "avro/binary")
	w.Header().Set("ETag", b.ETag)
	w.Header().Set("Last-Modified", b.LastModified)
	w.Header().Set("x-ms-blob-type", b.BlobType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encodeBlobQueryAvro(payload, int64(len(b.Data))))
}

// Input / output serialization

// decodeBlobQueryInput turns the stored bytes into rows of string fields. For
// delimited input the header row, when declared, becomes the column names; for
// JSON-lines input each object's keys are the column names.
func decodeBlobQueryInput(data []byte, in *blobQuerySerialization) ([][]string, []string, error) {
	format := "delimited"
	if in != nil && in.Format.Type != "" {
		format = strings.ToLower(in.Format.Type)
	}
	switch format {
	case "delimited":
		conf := blobQueryDelimitedConf{ColumnSeparator: ",", FieldQuote: `"`, RecordSeparator: "\n"}
		if in != nil && in.Format.DelimitedTextConf != nil {
			got := *in.Format.DelimitedTextConf
			if got.ColumnSeparator != "" {
				conf.ColumnSeparator = got.ColumnSeparator
			}
			if got.FieldQuote != "" {
				conf.FieldQuote = got.FieldQuote
			}
			conf.HasHeaders = got.HasHeaders
		}
		reader := csv.NewReader(bytes.NewReader(data))
		reader.Comma = rune(conf.ColumnSeparator[0])
		reader.FieldsPerRecord = -1
		reader.LazyQuotes = true
		records, err := reader.ReadAll()
		if err != nil {
			return nil, nil, fmt.Errorf("the delimited input could not be parsed: %v", err)
		}
		if conf.HasHeaders && len(records) > 0 {
			return records[1:], records[0], nil
		}
		return records, nil, nil
	case "json":
		var rows [][]string
		var headers []string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				return nil, nil, fmt.Errorf("the JSON input could not be parsed: %v", err)
			}
			if headers == nil {
				headers = blobQuerySortedKeys(obj)
			}
			row := make([]string, 0, len(headers))
			for _, key := range headers {
				row = append(row, blobQueryScalarString(obj[key]))
			}
			rows = append(rows, row)
		}
		return rows, headers, nil
	}
	return nil, nil, fmt.Errorf("the input serialization format %q is not supported by this query engine", format)
}

func blobQuerySortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	// json.Unmarshal loses field order, so the column order is the sorted key
	// order — stable across every row of the same document shape.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func blobQueryScalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		raw, _ := json.Marshal(t)
		return string(raw)
	}
}

// encodeBlobQueryOutput renders the selected rows in the requested output
// serialization.
func encodeBlobQueryOutput(rows [][]string, headers []string, q *blobQuery, out *blobQuerySerialization) ([]byte, error) {
	format := "delimited"
	if out != nil && out.Format.Type != "" {
		format = strings.ToLower(out.Format.Type)
	}
	names := q.outputColumnNames(headers)
	switch format {
	case "delimited":
		separator := ","
		if out != nil && out.Format.DelimitedTextConf != nil && out.Format.DelimitedTextConf.ColumnSeparator != "" {
			separator = out.Format.DelimitedTextConf.ColumnSeparator
		}
		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		writer.Comma = rune(separator[0])
		if out != nil && out.Format.DelimitedTextConf != nil && out.Format.DelimitedTextConf.HasHeaders && len(names) > 0 {
			if err := writer.Write(names); err != nil {
				return nil, err
			}
		}
		for _, row := range rows {
			if err := writer.Write(row); err != nil {
				return nil, err
			}
		}
		writer.Flush()
		return buf.Bytes(), writer.Error()
	case "json":
		separator := "\n"
		if out != nil && out.Format.JSONTextConf != nil && out.Format.JSONTextConf.RecordSeparator != "" {
			separator = out.Format.JSONTextConf.RecordSeparator
		}
		var buf bytes.Buffer
		for _, row := range rows {
			obj := map[string]string{}
			for i, cell := range row {
				name := "_" + strconv.Itoa(i+1)
				if i < len(names) {
					name = names[i]
				}
				obj[name] = cell
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			buf.Write(raw)
			buf.WriteString(separator)
		}
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("the output serialization format %q is not supported by this query engine", format)
}

// Query

type blobQuery struct {
	selectAll bool
	columns   []string
	where     []blobQueryPredicate
	whereOr   bool
}

type blobQueryPredicate struct {
	column string
	op     string
	value  string
}

// parseBlobQueryExpression parses the supported SELECT grammar, failing loudly
// on anything outside it.
func parseBlobQueryExpression(expr string) (*blobQuery, error) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), ";"))
	if s == "" {
		return nil, fmt.Errorf("the query expression is empty")
	}
	upper := strings.ToUpper(s)
	if !strings.HasPrefix(upper, "SELECT ") {
		return nil, fmt.Errorf("only SELECT queries are supported; got %q", expr)
	}
	fromIdx := strings.Index(upper, " FROM ")
	if fromIdx < 0 {
		return nil, fmt.Errorf("the query has no FROM clause: %q", expr)
	}
	projection := strings.TrimSpace(s[len("SELECT "):fromIdx])
	remainder := strings.TrimSpace(s[fromIdx+len(" FROM "):])

	source := remainder
	whereClause := ""
	if idx := sim.CaseInsensitiveIndex(remainder, " WHERE "); idx >= 0 {
		source = strings.TrimSpace(remainder[:idx])
		whereClause = strings.TrimSpace(remainder[idx+len(" WHERE "):])
	}
	if !strings.EqualFold(source, "BlobStorage") {
		return nil, fmt.Errorf("only the BlobStorage source is queryable; got %q", source)
	}

	q := &blobQuery{}
	if projection == "*" {
		q.selectAll = true
	} else {
		for _, part := range strings.Split(projection, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				return nil, fmt.Errorf("the projection list has an empty column")
			}
			if strings.ContainsAny(name, "()") {
				return nil, fmt.Errorf("function calls and aggregates are not supported: %q", name)
			}
			q.columns = append(q.columns, blobQueryUnquote(name))
		}
	}
	if whereClause == "" {
		return q, nil
	}
	if strings.ContainsAny(whereClause, "()") {
		return nil, fmt.Errorf("parenthesised predicates are not supported: %q", whereClause)
	}
	separator := " AND "
	if strings.Contains(strings.ToUpper(whereClause), " OR ") {
		if strings.Contains(strings.ToUpper(whereClause), " AND ") {
			return nil, fmt.Errorf("a predicate mixing AND and OR is not supported: %q", whereClause)
		}
		separator, q.whereOr = " OR ", true
	}
	for _, part := range blobQuerySplitFold(whereClause, separator) {
		pred, err := parseBlobQueryPredicate(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		q.where = append(q.where, pred)
	}
	return q, nil
}

// blobQuerySplitFold splits on a separator case-insensitively.
func blobQuerySplitFold(s, sep string) []string {
	var out []string
	rest := s
	for {
		idx := sim.CaseInsensitiveIndex(rest, sep)
		if idx < 0 {
			return append(out, rest)
		}
		out = append(out, rest[:idx])
		rest = rest[idx+len(sep):]
	}
}

func parseBlobQueryPredicate(s string) (blobQueryPredicate, error) {
	for _, op := range []string{">=", "<=", "<>", "!=", "=", ">", "<"} {
		idx := strings.Index(s, op)
		if idx <= 0 {
			continue
		}
		column := blobQueryUnquote(strings.TrimSpace(s[:idx]))
		value := strings.TrimSpace(s[idx+len(op):])
		if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
			value = value[1 : len(value)-1]
		}
		if column == "" || value == "" {
			return blobQueryPredicate{}, fmt.Errorf("the predicate %q is malformed", s)
		}
		return blobQueryPredicate{column: column, op: op, value: value}, nil
	}
	return blobQueryPredicate{}, fmt.Errorf("the predicate %q has no supported comparison operator", s)
}

func blobQueryUnquote(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '[' && s[len(s)-1] == ']')) {
		return s[1 : len(s)-1]
	}
	return s
}

// run filters and projects the rows.
func (q *blobQuery) run(rows [][]string, headers []string) ([][]string, error) {
	var out [][]string
	for _, row := range rows {
		keep := true
		if len(q.where) > 0 {
			keep = !q.whereOr
			for _, pred := range q.where {
				idx, err := q.columnIndex(pred.column, headers)
				if err != nil {
					return nil, err
				}
				got := ""
				if idx < len(row) {
					got = row[idx]
				}
				match := blobQueryCompare(got, pred.op, pred.value)
				if q.whereOr {
					keep = keep || match
				} else {
					keep = keep && match
				}
			}
		}
		if !keep {
			continue
		}
		if q.selectAll {
			out = append(out, row)
			continue
		}
		projected := make([]string, 0, len(q.columns))
		for _, column := range q.columns {
			idx, err := q.columnIndex(column, headers)
			if err != nil {
				return nil, err
			}
			if idx < len(row) {
				projected = append(projected, row[idx])
				continue
			}
			projected = append(projected, "")
		}
		out = append(out, projected)
	}
	return out, nil
}

// columnIndex resolves a column reference: `_N` is 1-based positional, anything
// else is a header name.
func (q *blobQuery) columnIndex(name string, headers []string) (int, error) {
	if strings.HasPrefix(name, "_") {
		n, err := strconv.Atoi(name[1:])
		if err == nil && n >= 1 {
			return n - 1, nil
		}
	}
	for i, header := range headers {
		if strings.EqualFold(header, name) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("the column %q is not present in the input; name a header column or a positional _N reference", name)
}

// outputColumnNames names the columns of the result, for an output
// serialization that carries headers.
func (q *blobQuery) outputColumnNames(headers []string) []string {
	if !q.selectAll {
		return q.columns
	}
	return headers
}

func blobQueryCompare(got, op, want string) bool {
	gotNum, gotErr := strconv.ParseFloat(got, 64)
	wantNum, wantErr := strconv.ParseFloat(want, 64)
	numeric := gotErr == nil && wantErr == nil
	switch op {
	case "=":
		if numeric {
			return gotNum == wantNum
		}
		return got == want
	case "<>", "!=":
		if numeric {
			return gotNum != wantNum
		}
		return got != want
	case ">":
		if numeric {
			return gotNum > wantNum
		}
		return got > want
	case ">=":
		if numeric {
			return gotNum >= wantNum
		}
		return got >= want
	case "<":
		if numeric {
			return gotNum < wantNum
		}
		return got < want
	case "<=":
		if numeric {
			return gotNum <= wantNum
		}
		return got <= want
	}
	return false
}

// Avro object-container framing

// blobQueryAvroSchema is the union of record types Query Blob Contents streams,
// exactly as the real service declares it in the file header. Clients read the
// schema out of the header, so it has to be the real one.
const blobQueryAvroSchema = `[` +
	`{"type":"record","name":"com.microsoft.azure.storage.queryBlobContents.resultData","doc":"","fields":[{"name":"data","type":"bytes"}]},` +
	`{"type":"record","name":"com.microsoft.azure.storage.queryBlobContents.error","doc":"","fields":[{"name":"fatal","type":"boolean"},{"name":"name","type":"string"},{"name":"description","type":"string"},{"name":"position","type":"long"}]},` +
	`{"type":"record","name":"com.microsoft.azure.storage.queryBlobContents.progress","doc":"","fields":[{"name":"bytesScanned","type":"long"},{"name":"totalBytes","type":"long"}]},` +
	`{"type":"record","name":"com.microsoft.azure.storage.queryBlobContents.end","doc":"","fields":[{"name":"totalBytes","type":"long"}]}` +
	`]`

// encodeBlobQueryAvro frames the query result as an Avro object container file
// carrying a resultData record, a progress record and an end record.
func encodeBlobQueryAvro(payload []byte, scanned int64) []byte {
	sync := make([]byte, 16)
	if _, err := rand.Read(sync); err != nil {
		panic("blob query: crypto/rand: " + err.Error())
	}

	var body bytes.Buffer
	// resultData — union branch 0, one bytes field.
	avroLong(&body, 0)
	avroBytes(&body, payload)
	// progress — union branch 2, bytesScanned + totalBytes.
	avroLong(&body, 2)
	avroLong(&body, scanned)
	avroLong(&body, scanned)
	// end — union branch 3, totalBytes.
	avroLong(&body, 3)
	avroLong(&body, scanned)

	var out bytes.Buffer
	out.WriteString("Obj")
	out.WriteByte(1)
	// The header metadata map: the writer schema and the (null) codec.
	avroLong(&out, 2)
	avroString(&out, "avro.schema")
	avroBytes(&out, []byte(blobQueryAvroSchema))
	avroString(&out, "avro.codec")
	avroBytes(&out, []byte("null"))
	avroLong(&out, 0)
	out.Write(sync)

	avroLong(&out, 3) // three records in this block
	avroLong(&out, int64(body.Len()))
	out.Write(body.Bytes())
	out.Write(sync)
	return out.Bytes()
}

// avroLong writes Avro's zigzag variable-length integer encoding.
func avroLong(w io.Writer, v int64) {
	u := uint64(v<<1) ^ uint64(v>>63)
	var buf [10]byte
	n := 0
	for u&^0x7f != 0 {
		buf[n] = byte(u&0x7f) | 0x80
		u >>= 7
		n++
	}
	buf[n] = byte(u)
	n++
	_, _ = w.Write(buf[:n])
}

func avroBytes(w io.Writer, b []byte) {
	avroLong(w, int64(len(b)))
	_, _ = w.Write(b)
}

func avroString(w io.Writer, s string) {
	avroBytes(w, []byte(s))
}
