package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
	_ "modernc.org/sqlite"
)

// A query runs in an in-memory SQLite database holding the tables it names:
// each BigQuery column becomes a column of the SQLite type that compares the
// same way (INT64 and BOOL as INTEGER, TIMESTAMP as epoch microseconds,
// FLOAT64 as REAL, a RECORD or REPEATED value as its JSON), every table
// reference becomes the quoted name of its loaded copy, and SQLite evaluates
// the rest of the statement.

// bqQueryError carries a message BigQuery answers a query with, worded as
// BigQuery words it.
type bqQueryError struct{ message string }

func (e *bqQueryError) Error() string { return e.message }

func bqQueryErr(format string, args ...any) error {
	return &bqQueryError{message: fmt.Sprintf(format, args...)}
}

// bqQueryTable is a table a query reads.
type bqQueryTable struct {
	ref    BQTableRef
	name   string
	schema []BQFieldSchema
}

// bqSQLToken is one lexical token of a query; only identifiers, backtick
// references and the punctuation around them matter to the rewrite.
type bqSQLToken struct {
	text  string
	ident bool
	quote bool
}

// bqTokenizeSQL splits a query into tokens, keeping string literals and
// comments whole so nothing inside them reads as a table reference.
func bqTokenizeSQL(query string) ([]bqSQLToken, error) {
	var out []bqSQLToken
	rs := []rune(query)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			j := i
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				j++
			}
			out = append(out, bqSQLToken{text: string(rs[i:j])})
			i = j
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-', c == '#':
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			out = append(out, bqSQLToken{text: " "})
			i = j
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			end := strings.Index(string(rs[i+2:]), "*/")
			if end < 0 {
				return nil, bqQueryErr("Syntax error: Unclosed comment")
			}
			skip := len([]rune(string(rs[i+2:])[:end])) + 4
			out = append(out, bqSQLToken{text: " "})
			i += skip
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(rs) && rs[j] != c {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(rs) {
				return nil, bqQueryErr("Syntax error: Unclosed string literal")
			}
			out = append(out, bqSQLToken{text: string(rs[i : j+1])})
			i = j + 1
		case c == '`':
			j := i + 1
			for j < len(rs) && rs[j] != '`' {
				j++
			}
			if j >= len(rs) {
				return nil, bqQueryErr("Syntax error: Unclosed identifier literal")
			}
			out = append(out, bqSQLToken{text: string(rs[i+1 : j]), ident: true, quote: true})
			i = j + 1
		case c == '_' || unicode.IsLetter(c):
			j := i
			for j < len(rs) && (rs[j] == '_' || unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) {
				j++
			}
			out = append(out, bqSQLToken{text: string(rs[i:j]), ident: true})
			i = j
		default:
			out = append(out, bqSQLToken{text: string(c)})
			i++
		}
	}
	return out, nil
}

// bqRewriteTableRefs replaces each table reference after FROM or JOIN, and
// each comma-joined one in a FROM list, with the quoted name of the table's
// loaded copy, and returns the tables the query reads.
func bqRewriteTableRefs(defaultProject, defaultDataset, query string) (string, []bqQueryTable, error) {
	tokens, err := bqTokenizeSQL(query)
	if err != nil {
		return "", nil, err
	}
	var out strings.Builder
	var tables []bqQueryTable
	seen := map[string]string{}
	expectTable := false
	inFrom := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !tok.ident {
			if tok.text == "," && inFrom {
				expectTable = true
			}
			if tok.text == "(" || tok.text == ")" {
				inFrom = false
			}
			if strings.TrimSpace(tok.text) != "" {
				expectTable = expectTable && tok.text == ","
			}
			out.WriteString(tok.text)
			continue
		}
		upper := strings.ToUpper(tok.text)
		if !tok.quote {
			switch upper {
			case "FROM", "JOIN":
				expectTable, inFrom = true, upper == "FROM"
				out.WriteString(tok.text)
				continue
			case "WHERE", "GROUP", "ORDER", "LIMIT", "HAVING", "ON", "USING", "UNION", "WINDOW", "QUALIFY", "SELECT":
				inFrom, expectTable = false, false
				out.WriteString(tok.text)
				continue
			}
		}
		if !expectTable {
			if tok.quote {
				out.WriteString(`"` + strings.ReplaceAll(tok.text, `"`, `""`) + `"`)
			} else {
				out.WriteString(tok.text)
			}
			continue
		}
		path := []string{tok.text}
		for i+2 < len(tokens) && tokens[i+1].text == "." && tokens[i+2].ident {
			path = append(path, tokens[i+2].text)
			i += 2
		}
		ref := strings.Join(path, ".")
		if len(path) == 1 && !strings.Contains(ref, ".") {
			if defaultDataset == "" {
				return "", nil, bqQueryErr("Table %q must be qualified with a dataset (e.g. dataset.table).", ref)
			}
			ref = defaultDataset + "." + ref
		}
		project, dataset, table, err := bqParseTableRef(defaultProject, strings.Replace(ref, ":", ".", 1))
		if err != nil {
			return "", nil, err
		}
		key := bqTableKey(project, dataset, table)
		name, ok := seen[key]
		if !ok {
			t, exists := bqTables.Get(key)
			if !exists {
				return "", nil, bqQueryErr("Not found: Table %s:%s.%s was not found in location US", project, dataset, table)
			}
			name = fmt.Sprintf("bq_table_%d", len(tables))
			seen[key] = name
			var fields []BQFieldSchema
			if t.Schema != nil {
				fields = t.Schema.Fields
			}
			tables = append(tables, bqQueryTable{ref: BQTableRef{ProjectID: project, DatasetID: dataset, TableID: table}, name: name, schema: fields})
		}
		out.WriteString(`"` + name + `"`)
		// A reference the query does not alias keeps its table name as one,
		// so the query's table.column references still resolve.
		if next := bqNextWord(tokens, i+1); next == "" || next == "," || bqClauseWord(next) {
			out.WriteString(` AS "` + table + `"`)
		}
		expectTable = false
	}
	return out.String(), tables, nil
}

func bqNextWord(tokens []bqSQLToken, from int) string {
	for _, t := range tokens[from:] {
		if strings.TrimSpace(t.text) == "" {
			continue
		}
		if t.ident && !t.quote {
			return strings.ToUpper(t.text)
		}
		if t.ident {
			return "`"
		}
		return t.text
	}
	return ""
}

func bqClauseWord(word string) bool {
	switch word {
	case "WHERE", "GROUP", "ORDER", "LIMIT", "HAVING", "ON", "USING", "UNION", "WINDOW", "QUALIFY",
		"JOIN", "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "OUTER", ")", ";":
		return true
	}
	return false
}

// bqSQLiteType is the SQLite type that holds a column so it compares as
// BigQuery compares it.
func bqSQLiteType(f BQFieldSchema) string {
	if bqNested(f) {
		return "TEXT"
	}
	switch bqType(f) {
	case "INT64", "BOOL", "TIMESTAMP":
		return "INTEGER"
	case "FLOAT64":
		return "REAL"
	case "NUMERIC", "BIGNUMERIC":
		return "NUMERIC"
	}
	return "TEXT"
}

// bqSQLiteValue is a stored cell, in the form bqCoerce stores, as its SQLite
// column holds it.
func bqSQLiteValue(f BQFieldSchema, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if bqNested(f) {
		raw, err := json.Marshal(v)
		return string(raw), err
	}
	text := bqScalarText(v)
	switch bqType(f) {
	case "INT64", "TIMESTAMP":
		return strconv.ParseInt(text, 10, 64)
	case "BOOL":
		if text == "true" {
			return int64(1), nil
		}
		return int64(0), nil
	case "FLOAT64":
		return strconv.ParseFloat(text, 64)
	}
	return text, nil
}

// bqEvaluateSQL runs a GoogleSQL query over the tables it names.
func bqEvaluateSQL(ctx context.Context, defaultProject, defaultDataset, query string) (*BQSchema, []map[string]any, error) {
	rewritten, tables, err := bqRewriteTableRefs(defaultProject, defaultDataset, strings.TrimSuffix(strings.TrimSpace(query), ";"))
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:bq-"+sim.NewUUID()+"?mode=memory&cache=private")
	if err != nil {
		return nil, nil, fmt.Errorf("open query engine: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := bqLoadQueryTables(ctx, db, tables); err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, rewritten)
	if err != nil {
		return nil, nil, bqQueryErr("Syntax error: %s", bqSQLiteMessage(err))
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var raw [][]any
	for rows.Next() {
		cells := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		raw = append(raw, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("%s", bqSQLiteMessage(err))
	}
	fields := bqResultFields(columns, tables, raw)
	out := make([]map[string]any, 0, len(raw))
	for _, cells := range raw {
		row := make(map[string]any, len(columns))
		for i, f := range fields {
			v, err := bqResultCell(f, cells[i])
			if err != nil {
				return nil, nil, err
			}
			row[f.Name] = v
		}
		out = append(out, row)
	}
	return &BQSchema{Fields: fields}, out, nil
}

func bqSQLiteMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "SQL logic error: "); i >= 0 {
		msg = msg[i+len("SQL logic error: "):]
	}
	return strings.TrimSuffix(msg, " (1)")
}

func bqLoadQueryTables(ctx context.Context, db *sql.DB, tables []bqQueryTable) error {
	for _, t := range tables {
		if len(t.schema) == 0 {
			return bqQueryErr("Table %s:%s.%s has no schema", t.ref.ProjectID, t.ref.DatasetID, t.ref.TableID)
		}
		cols := make([]string, len(t.schema))
		marks := make([]string, len(t.schema))
		for i, f := range t.schema {
			cols[i] = fmt.Sprintf(`"%s" %s`, f.Name, bqSQLiteType(f))
			marks[i] = "?"
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE "%s" (%s)`, t.name, strings.Join(cols, ", "))); err != nil {
			return fmt.Errorf("load %s: %w", t.ref.TableID, err)
		}
		stored, _ := bqRows.Get(bqTableKey(t.ref.ProjectID, t.ref.DatasetID, t.ref.TableID))
		insert, err := db.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO "%s" VALUES (%s)`, t.name, strings.Join(marks, ", ")))
		if err != nil {
			return fmt.Errorf("load %s: %w", t.ref.TableID, err)
		}
		for _, row := range stored.Rows {
			args := make([]any, len(t.schema))
			for i, f := range t.schema {
				v, err := bqSQLiteValue(f, row[f.Name])
				if err != nil {
					_ = insert.Close()
					return fmt.Errorf("load %s: %w", t.ref.TableID, err)
				}
				args[i] = v
			}
			if _, err := insert.ExecContext(ctx, args...); err != nil {
				_ = insert.Close()
				return fmt.Errorf("load %s: %w", t.ref.TableID, err)
			}
		}
		if err := insert.Close(); err != nil {
			return err
		}
	}
	return nil
}

// bqResultFields types each result column: a column that names a column of
// a table the query read keeps that column's type, and any other takes the
// type of the values SQLite computed for it.
func bqResultFields(columns []string, tables []bqQueryTable, raw [][]any) []BQFieldSchema {
	fields := make([]BQFieldSchema, len(columns))
	for i, name := range columns {
		if f, ok := bqSourceField(name, tables); ok {
			f.Name = name
			fields[i] = f
			continue
		}
		fields[i] = BQFieldSchema{Name: name, Type: "STRING", Mode: "NULLABLE"}
	cells:
		for _, cells := range raw {
			switch cells[i].(type) {
			case nil:
				continue
			case int64:
				fields[i].Type = "INTEGER"
			case float64:
				fields[i].Type = "FLOAT"
			}
			break cells
		}
	}
	return fields
}

func bqSourceField(name string, tables []bqQueryTable) (BQFieldSchema, bool) {
	for _, t := range tables {
		for _, f := range t.schema {
			if strings.EqualFold(f.Name, name) {
				return f, true
			}
		}
	}
	return BQFieldSchema{}, false
}

// bqResultCell turns a SQLite value back into the stored cell form of its
// result column.
func bqResultCell(f BQFieldSchema, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	if bqNested(f) {
		text, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("column %s holds %T, not a JSON value", f.Name, v)
		}
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, fmt.Errorf("column %s: %w", f.Name, err)
		}
		return decoded, nil
	}
	switch bqType(f) {
	case "BOOL":
		if n, ok := v.(int64); ok {
			return strconv.FormatBool(n != 0), nil
		}
	case "TIMESTAMP":
		if n, ok := v.(int64); ok {
			return strconv.FormatInt(n, 10), nil
		}
	case "INT64":
		switch n := v.(type) {
		case int64:
			return strconv.FormatInt(n, 10), nil
		case float64:
			if n == math.Trunc(n) {
				return strconv.FormatInt(int64(n), 10), nil
			}
		}
	case "FLOAT64":
		switch n := v.(type) {
		case float64:
			return bqFormatFloat(n), nil
		case int64:
			return bqFormatFloat(float64(n)), nil
		}
	}
	switch n := v.(type) {
	case int64:
		return strconv.FormatInt(n, 10), nil
	case float64:
		return bqFormatFloat(n), nil
	case time.Time:
		return strconv.FormatInt(n.UnixMicro(), 10), nil
	}
	return fmt.Sprint(v), nil
}
