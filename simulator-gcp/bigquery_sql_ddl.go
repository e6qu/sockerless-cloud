package main

import (
	"context"
	"encoding/json"

	"net/http"
	"strconv"
	"strings"
	"time"
)

// GoogleSQL types and the DDL statements the simulator runs, from the vendored
// data-definition-language reference
// (specs/cloud-api/gcp/bigquery-ddl.md.gz).

// bqSQLTypeNames maps each GoogleSQL type spelling to the API's table schema
// type name.
var bqSQLTypeNames = map[string]string{
	"INT64": "INTEGER", "INT": "INTEGER", "SMALLINT": "INTEGER", "INTEGER": "INTEGER", "BIGINT": "INTEGER", "TINYINT": "INTEGER", "BYTEINT": "INTEGER",
	"FLOAT64": "FLOAT", "FLOAT": "FLOAT",
	"NUMERIC": "NUMERIC", "DECIMAL": "NUMERIC", "BIGNUMERIC": "BIGNUMERIC", "BIGDECIMAL": "BIGNUMERIC",
	"BOOL": "BOOLEAN", "BOOLEAN": "BOOLEAN",
	"STRING": "STRING", "BYTES": "BYTES", "DATE": "DATE", "DATETIME": "DATETIME", "TIME": "TIME", "TIMESTAMP": "TIMESTAMP",
	"GEOGRAPHY": "GEOGRAPHY", "JSON": "JSON", "INTERVAL": "INTERVAL", "RECORD": "RECORD", "STRUCT": "RECORD",
}

// bqParseSQLType reads a GoogleSQL type: a simple type with optional
// parameters, ARRAY<T>, or STRUCT<name T, ...>.
func bqParseSQLType(text string) (BQFieldSchema, error) {
	tokens, err := bqTokenizeSQL(text)
	if err != nil {
		return BQFieldSchema{}, err
	}
	tokens = bqTrimSpace(tokens)
	f, rest, err := bqParseTypeTokens(tokens)
	if err != nil {
		return BQFieldSchema{}, err
	}
	if len(bqTrimSpace(rest)) > 0 {
		return BQFieldSchema{}, bqQueryErr("Syntax error: Unexpected %q in type %s", bqSQLText(rest), text)
	}
	return f, nil
}

// bqParseTypeTokens reads one type from the start of tokens and answers what
// follows it.
func bqParseTypeTokens(tokens []bqSQLToken) (BQFieldSchema, []bqSQLToken, error) {
	tokens = bqTrimSpace(tokens)
	if len(tokens) == 0 || !tokens[0].isIdent() {
		return BQFieldSchema{}, nil, bqQueryErr("Syntax error: Expected a type")
	}
	name := strings.ToUpper(tokens[0].text)
	rest := tokens[1:]
	switch name {
	case "ARRAY":
		inner, after, err := bqParseAngleList(rest)
		if err != nil {
			return BQFieldSchema{}, nil, err
		}
		elem, left, err := bqParseTypeTokens(inner)
		if err != nil {
			return BQFieldSchema{}, nil, err
		}
		left = bqTrimSpace(left)
		if len(left) == 2 && left[0].isWord("NOT") && left[1].isWord("NULL") {
			left = nil
		}
		if len(left) > 0 {
			return BQFieldSchema{}, nil, bqQueryErr("Syntax error: Unexpected %q in ARRAY type", bqSQLText(left))
		}
		if bqRepeated(elem) {
			return BQFieldSchema{}, nil, bqQueryErr("Array of arrays is not supported")
		}
		elem.Mode = "REPEATED"
		return elem, after, nil
	case "STRUCT":
		inner, after, err := bqParseAngleList(rest)
		if err != nil {
			return BQFieldSchema{}, nil, err
		}
		f := BQFieldSchema{Type: "RECORD", Mode: "NULLABLE"}
		for _, fieldTokens := range bqSplitTypeArgs(inner) {
			fieldTokens = bqTrimSpace(fieldTokens)
			if len(fieldTokens) < 2 || !fieldTokens[0].isIdent() || bqSQLTypeNames[strings.ToUpper(fieldTokens[0].text)] != "" && len(fieldTokens) == 1 {
				return BQFieldSchema{}, nil, bqQueryErr("The simulator requires every STRUCT field to be named: %s", bqSQLText(fieldTokens))
			}
			sub, err := bqParseColumnSchema(fieldTokens[1:])
			if err != nil {
				return BQFieldSchema{}, nil, err
			}
			sub.Name = fieldTokens[0].text
			f.Fields = append(f.Fields, sub)
		}
		return f, after, nil
	}
	apiType, ok := bqSQLTypeNames[name]
	if !ok {
		return BQFieldSchema{}, nil, bqQueryErr("Type not found: %s", tokens[0].text)
	}
	f := BQFieldSchema{Type: apiType, Mode: "NULLABLE"}
	if next := bqSignificant(rest, 0); next < len(rest) && rest[next].isPunct("(") {
		end := bqMatchParen(rest, next)
		if end < 0 {
			return BQFieldSchema{}, nil, bqQueryErr("Syntax error: Unmatched ( in type")
		}
		rest = rest[end+1:]
	}
	return f, rest, nil
}

// bqWithoutSpace drops the space tokens of a clause read by token pattern.
func bqWithoutSpace(tokens []bqSQLToken) []bqSQLToken {
	out := make([]bqSQLToken, 0, len(tokens))
	for _, t := range tokens {
		if !t.isSpace() {
			out = append(out, t)
		}
	}
	return out
}

// bqSplitTypeArgs splits a list whose items may hold ARRAY<...> and
// STRUCT<...> types on its top-level commas.
func bqSplitTypeArgs(tokens []bqSQLToken) [][]bqSQLToken {
	var out [][]bqSQLToken
	depth := 0
	start := 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("), t.isPunct("["), t.isPunct("<"):
			depth++
		case t.isPunct(")"), t.isPunct("]"), t.isPunct(">"):
			depth--
		case t.isPunct(">>"):
			depth -= 2
		case depth == 0 && t.isPunct(","):
			out = append(out, bqTrimSpace(tokens[start:i]))
			start = i + 1
		}
	}
	if rest := bqTrimSpace(tokens[start:]); len(rest) > 0 || len(out) > 0 {
		out = append(out, rest)
	}
	return out
}

// bqParseAngleList reads <...> and answers its contents and what follows.
func bqParseAngleList(tokens []bqSQLToken) ([]bqSQLToken, []bqSQLToken, error) {
	start := bqSignificant(tokens, 0)
	if start >= len(tokens) || !tokens[start].isPunct("<") {
		return nil, nil, bqQueryErr("Syntax error: Expected < in type")
	}
	depth := 0
	for i := start; i < len(tokens); i++ {
		switch {
		case tokens[i].isPunct("<"):
			depth++
		case tokens[i].isPunct(">"):
			depth--
			if depth == 0 {
				return tokens[start+1 : i], tokens[i+1:], nil
			}
		case tokens[i].isPunct(">>"):
			depth -= 2
			if depth == 0 {
				return tokens[start+1 : i], tokens[i+1:], nil
			}
			if depth < 0 {
				// The outer `>` of `>>` closes this list; the other belongs
				// to the enclosing one.
				outer := append([]bqSQLToken{bqPunctToken(">")}, tokens[i+1:]...)
				return tokens[start+1 : i], outer, nil
			}
		}
	}
	return nil, nil, bqQueryErr("Syntax error: Unmatched < in type")
}

// bqParseColumnSchema reads a column_schema: a type with an optional NOT NULL
// and OPTIONS clause.
func bqParseColumnSchema(tokens []bqSQLToken) (BQFieldSchema, error) {
	f, rest, err := bqParseTypeTokens(tokens)
	if err != nil {
		return f, err
	}
	rest = bqTrimSpace(rest)
	for len(rest) > 0 {
		switch {
		case rest[0].isWord("NOT") && len(rest) > 1 && bqTrimSpace(rest[1:])[0].isWord("NULL"):
			if bqRepeated(f) {
				return f, bqQueryErr("Syntax error: Unexpected NOT NULL on an ARRAY column")
			}
			f.Mode = "REQUIRED"
			rest = bqTrimSpace(rest[bqSignificant(rest, 1)+1:])
		case rest[0].isWord("OPTIONS"):
			open := bqSignificant(rest, 1)
			end := bqMatchParen(rest, open)
			if open >= len(rest) || !rest[open].isPunct("(") || end < 0 {
				return f, bqQueryErr("Syntax error: Expected ( after OPTIONS")
			}
			for _, opt := range bqSplitArgs(rest[open+1 : end]) {
				if len(opt) >= 3 && opt[0].isWord("description") && opt[2].kind == bqTokString {
					f.Description = bqStringValue(opt[2])
				}
			}
			rest = bqTrimSpace(rest[end+1:])
		case rest[0].isWord("DEFAULT"):
			return f, bqQueryErr("The simulator does not support DEFAULT values on columns")
		case rest[0].isWord("PRIMARY") || rest[0].isWord("REFERENCES") || rest[0].isWord("COLLATE") || rest[0].isWord("GENERATED"):
			return f, bqQueryErr("The simulator does not support %s on columns", strings.ToUpper(rest[0].text))
		default:
			return f, bqQueryErr("Syntax error: Unexpected %q in column definition", bqSQLText(rest))
		}
	}
	return f, nil
}

// bqSQLTypeName is the GoogleSQL spelling of a field's type.
func bqSQLTypeName(f BQFieldSchema) string {
	var name string
	switch bqType(f) {
	case "STRUCT":
		parts := make([]string, len(f.Fields))
		for i, sub := range f.Fields {
			parts[i] = sub.Name + " " + bqSQLTypeName(sub)
		}
		name = "STRUCT<" + strings.Join(parts, ", ") + ">"
	default:
		name = bqType(f)
	}
	if bqRepeated(f) {
		return "ARRAY<" + name + ">"
	}
	return name
}

// bqStatementResult is what a statement answered: a query's rows, or the DML
// and DDL statistics JobStatistics2 declares.
type bqStatementResult struct {
	StatementType  string
	Schema         *BQSchema
	Rows           []map[string]any
	NumDMLAffected *int64
	DMLStats       map[string]string
	DDLOperation   string
	DDLTarget      *BQTableRef
	Referenced     []BQTableRef
	BytesProcessed int64
	isQuery        bool
}

// bqDDLTarget resolves the table a DDL statement names.
func bqDDLTarget(path []string, scope *bqScope) (BQTableRef, error) {
	var parts []string
	for _, p := range path {
		parts = append(parts, strings.Split(p, ".")...)
	}
	path = parts
	ref := strings.Join(path, ".")
	if len(path) == 1 {
		if scope.defaultDataset == "" {
			return BQTableRef{}, bqQueryErr("Table %q must be qualified with a dataset (e.g. dataset.table).", ref)
		}
		ref = scope.defaultDataset + "." + ref
	}
	project, dataset, table, err := bqParseTableRef(scope.defaultProject, strings.Replace(ref, ":", ".", 1))
	if err != nil {
		return BQTableRef{}, bqQueryErr("Invalid table name: %s", ref)
	}
	return BQTableRef{ProjectID: project, DatasetID: dataset, TableID: table}, nil
}

// bqReadTablePath reads an identifier path at i and reports the index after it.
func bqReadTablePath(tokens []bqSQLToken, i int) ([]string, int, error) {
	i = bqSignificant(tokens, i)
	if i >= len(tokens) || !tokens[i].isIdent() {
		return nil, i, bqQueryErr("Syntax error: Expected a table name")
	}
	path, end := bqReadPath(tokens, i)
	return path, end + 1, nil
}

// bqRunDDL runs a CREATE, DROP, ALTER or TRUNCATE statement against the
// simulator's tables.
func bqRunDDL(ctx context.Context, r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	tokens = bqTrimSpace(tokens)
	words := bqLeadingWords(tokens, 6)
	switch {
	case words[0] == "CREATE" && bqContainsWord(words, "TABLE"):
		return bqRunCreateTable(ctx, r, scope, tokens, dryRun)
	case words[0] == "CREATE" && bqContainsWord(words, "VIEW"):
		return bqRunCreateView(ctx, r, scope, tokens, dryRun)
	case words[0] == "DROP" && (words[1] == "TABLE" || words[1] == "VIEW"):
		return bqRunDrop(r, scope, tokens, words[1], dryRun)
	case words[0] == "ALTER" && words[1] == "TABLE":
		return bqRunAlterTable(r, scope, tokens, dryRun)
	case words[0] == "TRUNCATE" && words[1] == "TABLE":
		return bqRunTruncate(ctx, r, scope, tokens, dryRun)
	}
	return nil, bqQueryErr("The simulator does not support %s statements", strings.Join(bqTrimWords(words), " "))
}

func bqTrimWords(words []string) []string {
	var out []string
	for _, w := range words {
		if w != "" {
			out = append(out, w)
		}
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

// bqLeadingWords is the upper-cased first n bare words of a statement.
func bqLeadingWords(tokens []bqSQLToken, n int) []string {
	words := make([]string, n)
	i := 0
	for _, t := range tokens {
		if i >= n {
			break
		}
		if t.isSpace() {
			continue
		}
		if t.kind == bqTokIdent {
			words[i] = strings.ToUpper(t.text)
		} else {
			words[i] = t.text
		}
		i++
	}
	return words
}

func bqContainsWord(words []string, word string) bool {
	for _, w := range words {
		if w == word {
			return true
		}
	}
	return false
}

// bqDDLHead reads the modifiers between CREATE and the object name:
// OR REPLACE, TEMP, IF NOT EXISTS; it answers the index of the name.
func bqDDLHead(tokens []bqSQLToken, object string) (orReplace, ifNotExists, temp bool, nameAt int, err error) {
	i := bqSignificant(tokens, 1)
	for i < len(tokens) {
		switch {
		case tokens[i].isWord("OR"):
			j := bqSignificant(tokens, i+1)
			if j >= len(tokens) || !tokens[j].isWord("REPLACE") {
				return false, false, false, 0, bqQueryErr("Syntax error: Expected REPLACE after OR")
			}
			orReplace = true
			i = bqSignificant(tokens, j+1)
		case tokens[i].isWord("TEMP") || tokens[i].isWord("TEMPORARY"):
			temp = true
			i = bqSignificant(tokens, i+1)
		case tokens[i].isWord(object):
			i = bqSignificant(tokens, i+1)
			if i < len(tokens) && tokens[i].isWord("IF") {
				j := bqSignificant(tokens, i+1)
				k := bqSignificant(tokens, j+1)
				if j >= len(tokens) || !tokens[j].isWord("NOT") || k >= len(tokens) || !tokens[k].isWord("EXISTS") {
					return false, false, false, 0, bqQueryErr("Syntax error: Expected NOT EXISTS after IF")
				}
				ifNotExists = true
				i = bqSignificant(tokens, k+1)
			}
			if orReplace && ifNotExists {
				return false, false, false, 0, bqQueryErr("Syntax error: OR REPLACE and IF NOT EXISTS cannot both be specified")
			}
			return orReplace, ifNotExists, temp, i, nil
		default:
			return false, false, false, 0, bqQueryErr("The simulator does not support CREATE %s", strings.ToUpper(tokens[i].text))
		}
	}
	return false, false, false, 0, bqQueryErr("Syntax error: Unexpected end of statement")
}

// bqDDLClauses splits what follows a CREATE TABLE name on its depth-0
// clause keywords.
func bqDDLClauses(tokens []bqSQLToken) map[string][]bqSQLToken {
	clauses := map[string][]bqSQLToken{}
	current := ""
	start := 0
	depth := 0
	flush := func(end int) {
		if current != "" {
			clauses[current] = bqTrimSpace(tokens[start:end])
		}
	}
	for i, t := range tokens {
		// Everything after AS is the query.
		if current == "AS" {
			break
		}
		switch {
		case t.isPunct("("):
			if depth == 0 && current == "" {
				flush(i)
				current, start = "COLUMNS", i
			}
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && (t.isWord("PARTITION") || t.isWord("CLUSTER") || t.isWord("OPTIONS") || t.isWord("AS") || t.isWord("DEFAULT") || t.isWord("WITH")):
			flush(i)
			current, start = strings.ToUpper(t.text), i
		}
	}
	flush(len(tokens))
	return clauses
}

// bqParseColumnList reads the parenthesized column definitions of CREATE
// TABLE, skipping table constraints.
func bqParseColumnList(tokens []bqSQLToken) ([]BQFieldSchema, error) {
	tokens = bqTrimSpace(tokens)
	if len(tokens) < 2 || !tokens[0].isPunct("(") || bqMatchParen(tokens, 0) != len(tokens)-1 {
		return nil, bqQueryErr("Syntax error: Expected a parenthesized column list")
	}
	var fields []BQFieldSchema
	seen := map[string]bool{}
	for _, col := range bqSplitTypeArgs(tokens[1 : len(tokens)-1]) {
		col = bqTrimSpace(col)
		if len(col) == 0 {
			continue
		}
		if col[0].isWord("PRIMARY") || col[0].isWord("FOREIGN") || col[0].isWord("CONSTRAINT") {
			continue
		}
		if len(col) < 2 || !col[0].isIdent() {
			return nil, bqQueryErr("Syntax error: Expected column name and type, got %q", bqSQLText(col))
		}
		f, err := bqParseColumnSchema(col[1:])
		if err != nil {
			return nil, err
		}
		f.Name = col[0].text
		if seen[strings.ToLower(f.Name)] {
			return nil, bqQueryErr("Duplicate column name: %s", f.Name)
		}
		seen[strings.ToLower(f.Name)] = true
		fields = append(fields, f)
	}
	return fields, nil
}

// bqParsePartitionBy reads a partition_expression into the table's
// timePartitioning or rangePartitioning.
func bqParsePartitionBy(tokens []bqSQLToken, fields []BQFieldSchema) (timePartitioning, rangePartitioning json.RawMessage, err error) {
	tokens = bqTrimSpace(tokens)
	tokens = bqWithoutSpace(tokens)
	if len(tokens) < 2 || !tokens[0].isWord("PARTITION") || !tokens[1].isWord("BY") {
		return nil, nil, bqQueryErr("Syntax error: Expected PARTITION BY")
	}
	expr := bqTrimSpace(tokens[2:])
	fieldType := func(name string) (string, error) {
		f, ok := bqFieldAtPath(fields, []string{name})
		if !ok {
			return "", bqQueryErr("Unrecognized name: %s", name)
		}
		if bqRepeated(f) || strings.EqualFold(f.Mode, "REPEATED") {
			return "", bqQueryErr("Cannot partition on a repeated column: %s", name)
		}
		return bqType(f), nil
	}
	timeSpec := func(field, kind string) (json.RawMessage, error) {
		spec := map[string]any{"type": kind}
		if field != "" {
			spec["field"] = field
		}
		raw, err := json.Marshal(spec)
		return raw, err
	}
	if len(expr) == 1 && expr[0].isIdent() {
		if strings.EqualFold(expr[0].text, "_PARTITIONDATE") {
			raw, err := timeSpec("", "DAY")
			return raw, nil, err
		}
		t, err := fieldType(expr[0].text)
		if err != nil {
			return nil, nil, err
		}
		if t != "DATE" {
			return nil, nil, bqQueryErr("PARTITION BY expression must be DATE(<timestamp_column>), a DATE column, TIMESTAMP_TRUNC, DATETIME_TRUNC, DATE_TRUNC or RANGE_BUCKET; column %s is %s", expr[0].text, t)
		}
		raw, err := timeSpec(expr[0].text, "DAY")
		return raw, nil, err
	}
	if len(expr) >= 4 && expr[0].isIdent() && expr[1].isPunct("(") && bqMatchParen(expr, 1) == len(expr)-1 {
		fn := strings.ToUpper(expr[0].text)
		args := bqSplitArgs(expr[2 : len(expr)-1])
		switch fn {
		case "DATE":
			if len(args) == 1 && len(args[0]) == 1 && args[0][0].isIdent() {
				name := args[0][0].text
				if strings.EqualFold(name, "_PARTITIONTIME") {
					raw, err := timeSpec("", "DAY")
					return raw, nil, err
				}
				t, err := fieldType(name)
				if err != nil {
					return nil, nil, err
				}
				if t != "TIMESTAMP" && t != "DATETIME" {
					return nil, nil, bqQueryErr("DATE(%s) in PARTITION BY needs a TIMESTAMP or DATETIME column, not %s", name, t)
				}
				raw, err := timeSpec(name, "DAY")
				return raw, nil, err
			}
		case "TIMESTAMP_TRUNC", "DATETIME_TRUNC", "DATE_TRUNC":
			if len(args) == 2 && len(args[0]) == 1 && args[0][0].isIdent() && len(args[1]) == 1 && args[1][0].isIdent() {
				name, part := args[0][0].text, strings.ToUpper(args[1][0].text)
				allowed := map[string]bool{"DAY": true, "HOUR": true, "MONTH": true, "YEAR": true}
				if fn == "DATE_TRUNC" {
					allowed = map[string]bool{"MONTH": true, "YEAR": true}
				}
				if !allowed[part] {
					return nil, nil, bqQueryErr("%s in PARTITION BY does not support the %s part", fn, part)
				}
				if strings.EqualFold(name, "_PARTITIONTIME") && fn == "TIMESTAMP_TRUNC" {
					raw, err := timeSpec("", part)
					return raw, nil, err
				}
				if _, err := fieldType(name); err != nil {
					return nil, nil, err
				}
				raw, err := timeSpec(name, part)
				return raw, nil, err
			}
		case "RANGE_BUCKET":
			if len(args) == 2 && len(args[0]) == 1 && args[0][0].isIdent() {
				t, err := fieldType(args[0][0].text)
				if err != nil {
					return nil, nil, err
				}
				if t != "INT64" {
					return nil, nil, bqQueryErr("RANGE_BUCKET in PARTITION BY needs an INT64 column, not %s", t)
				}
				gen := bqTrimSpace(args[1])
				if len(gen) < 4 || !gen[0].isWord("GENERATE_ARRAY") || !gen[1].isPunct("(") {
					return nil, nil, bqQueryErr("RANGE_BUCKET in PARTITION BY needs GENERATE_ARRAY(start, end[, interval])")
				}
				bounds := bqSplitArgs(gen[2 : len(gen)-1])
				if len(bounds) < 2 || len(bounds) > 3 {
					return nil, nil, bqQueryErr("GENERATE_ARRAY in PARTITION BY takes a start, an end and an optional interval")
				}
				values := []string{"", "", "1"}
				for i, b := range bounds {
					text := bqSQLText(b)
					if _, err := strconv.ParseInt(strings.ReplaceAll(text, " ", ""), 10, 64); err != nil {
						return nil, nil, bqQueryErr("GENERATE_ARRAY in PARTITION BY takes INT64 literals, not %s", text)
					}
					values[i] = strings.ReplaceAll(text, " ", "")
				}
				raw, err := json.Marshal(map[string]any{"field": args[0][0].text, "range": map[string]string{"start": values[0], "end": values[1], "interval": values[2]}})
				return nil, raw, err
			}
		}
	}
	return nil, nil, bqQueryErr("Unsupported PARTITION BY expression: %s", bqSQLText(expr))
}

// bqParseClusterBy reads CLUSTER BY columns into the table's clustering.
func bqParseClusterBy(tokens []bqSQLToken, fields []BQFieldSchema) (json.RawMessage, error) {
	tokens = bqTrimSpace(tokens)
	tokens = bqWithoutSpace(tokens)
	if len(tokens) < 3 || !tokens[0].isWord("CLUSTER") || !tokens[1].isWord("BY") {
		return nil, bqQueryErr("Syntax error: Expected CLUSTER BY")
	}
	var names []string
	for _, col := range bqSplitArgs(tokens[2:]) {
		if len(col) != 1 || !col[0].isIdent() {
			return nil, bqQueryErr("Syntax error: Expected a column name in CLUSTER BY")
		}
		f, ok := bqFieldAtPath(fields, []string{col[0].text})
		if !ok {
			return nil, bqQueryErr("Unrecognized name: %s", col[0].text)
		}
		if bqNested(f) {
			return nil, bqQueryErr("Clustering is not supported on a nested or repeated column: %s", col[0].text)
		}
		names = append(names, col[0].text)
	}
	if len(names) > 4 {
		return nil, bqQueryErr("Clustering supports at most four columns; %d were given", len(names))
	}
	return json.Marshal(map[string]any{"fields": names})
}

// bqTableOptions is what a table_option_list sets on the table.
type bqTableOptions struct {
	description, friendlyName       string
	labels                          map[string]string
	expirationTime                  string
	requirePartitionFilter          *bool
	partitionExpirationMs           string
	hasDescription, hasFriendlyName bool
}

// bqParseTableOptions reads OPTIONS(name=value, ...), evaluating each value
// as the constant expression the reference allows.
func bqParseTableOptions(ctx context.Context, tokens []bqSQLToken, scope *bqScope) (bqTableOptions, error) {
	var opts bqTableOptions
	tokens = bqTrimSpace(tokens)
	if len(tokens) < 3 || !tokens[0].isWord("OPTIONS") {
		return opts, bqQueryErr("Syntax error: Expected OPTIONS")
	}
	open := bqSignificant(tokens, 1)
	end := bqMatchParen(tokens, open)
	if open >= len(tokens) || !tokens[open].isPunct("(") || end < 0 {
		return opts, bqQueryErr("Syntax error: Expected ( after OPTIONS")
	}
	for _, opt := range bqSplitArgs(tokens[open+1 : end]) {
		eq := -1
		for i, t := range opt {
			if t.isPunct("=") {
				eq = i
				break
			}
		}
		if eq < 1 || !opt[0].isIdent() {
			return opts, bqQueryErr("Syntax error: Expected NAME=VALUE in OPTIONS, got %q", bqSQLText(opt))
		}
		name := strings.ToLower(opt[0].text)
		valueTokens := bqTrimSpace(opt[eq+1:])
		if name == "labels" || name == "tags" {
			labels, err := bqParseLabelsOption(valueTokens)
			if err != nil {
				return opts, err
			}
			if name == "labels" {
				opts.labels = labels
			}
			continue
		}
		value, typ, err := bqEvaluateConstant(ctx, valueTokens, scope)
		if err != nil {
			return opts, err
		}
		if value == nil {
			continue
		}
		text := bqScalarText(value)
		switch name {
		case "description":
			opts.description, opts.hasDescription = text, true
		case "friendly_name":
			opts.friendlyName, opts.hasFriendlyName = text, true
		case "expiration_timestamp":
			if bqType(typ) != "TIMESTAMP" {
				return opts, bqQueryErr("Option expiration_timestamp must be a TIMESTAMP, got %s", bqSQLTypeName(typ))
			}
			micros, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return opts, bqQueryErr("Option expiration_timestamp has an invalid value: %s", text)
			}
			opts.expirationTime = strconv.FormatInt(micros/1000, 10)
		case "require_partition_filter":
			b := text == "true"
			opts.requirePartitionFilter = &b
		case "partition_expiration_days":
			days, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return opts, bqQueryErr("Option partition_expiration_days must be a FLOAT64, got %s", text)
			}
			opts.partitionExpirationMs = strconv.FormatInt(int64(days*86400000), 10)
		case "kms_key_name", "default_rounding_mode", "enable_change_history", "max_staleness", "enable_fine_grained_mutations", "storage_uri", "file_format", "table_format":
			return opts, bqQueryErr("The simulator does not support the table option %s", name)
		default:
			return opts, bqQueryErr("Unknown option: %s", name)
		}
	}
	return opts, nil
}

// bqParseLabelsOption reads labels=[("key", "value"), ...].
func bqParseLabelsOption(tokens []bqSQLToken) (map[string]string, error) {
	tokens = bqTrimSpace(tokens)
	if len(tokens) < 2 || !tokens[0].isPunct("[") || !tokens[len(tokens)-1].isPunct("]") {
		return nil, bqQueryErr("Option labels must be an ARRAY<STRUCT<STRING, STRING>> literal")
	}
	labels := map[string]string{}
	for _, pair := range bqSplitArgs(tokens[1 : len(tokens)-1]) {
		pair = bqTrimSpace(pair)
		if len(pair) < 2 || !pair[0].isPunct("(") || bqMatchParen(pair, 0) != len(pair)-1 {
			return nil, bqQueryErr("Option labels must be an ARRAY<STRUCT<STRING, STRING>> literal")
		}
		kv := bqSplitArgs(pair[1 : len(pair)-1])
		if len(kv) != 2 || len(kv[0]) != 1 || len(kv[1]) != 1 || kv[0][0].kind != bqTokString || kv[1][0].kind != bqTokString {
			return nil, bqQueryErr("Option labels must be an ARRAY<STRUCT<STRING, STRING>> literal")
		}
		labels[bqStringValue(kv[0][0])] = bqStringValue(kv[1][0])
	}
	return labels, nil
}

// bqEvaluateConstant evaluates a constant expression in the engine and
// answers its stored-form value and type.
func bqEvaluateConstant(ctx context.Context, tokens []bqSQLToken, scope *bqScope) (any, BQFieldSchema, error) {
	inner := bqNewScope(scope.defaultProject, scope.defaultDataset)
	inner.now = scope.now
	inner.parameters, inner.positional = scope.parameters, scope.positional
	rewritten, err := bqRewriteExpressions(tokens, inner)
	if err != nil {
		return nil, BQFieldSchema{}, err
	}
	typ := bqExprType(rewritten, inner)
	db, err := bqOpenEngine(ctx, inner)
	if err != nil {
		return nil, BQFieldSchema{}, err
	}
	defer func() { _ = db.Close() }()
	var value any
	if err := db.QueryRowContext(ctx, "SELECT "+bqSQLText(rewritten)).Scan(&value); err != nil {
		return nil, BQFieldSchema{}, bqEngineError(err)
	}
	if typ.Type == "" {
		switch value.(type) {
		case int64:
			typ = BQFieldSchema{Type: "INT64"}
		case float64:
			typ = BQFieldSchema{Type: "FLOAT64"}
		case nil:
		default:
			typ = BQFieldSchema{Type: "STRING"}
		}
	}
	stored, err := bqResultCell(typ, value)
	if err != nil {
		return nil, BQFieldSchema{}, err
	}
	return stored, typ, nil
}

func bqRunCreateTable(ctx context.Context, r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	orReplace, ifNotExists, temp, nameAt, err := bqDDLHead(tokens, "TABLE")
	if err != nil {
		return nil, err
	}
	if temp {
		return nil, bqQueryErr("The simulator does not support temporary tables")
	}
	path, after, err := bqReadTablePath(tokens, nameAt)
	if err != nil {
		return nil, err
	}
	dest, err := bqDDLTarget(path, scope)
	if err != nil {
		return nil, err
	}
	clauses := bqDDLClauses(tokens[after:])
	for name := range clauses {
		switch name {
		case "COLUMNS", "PARTITION", "CLUSTER", "OPTIONS", "AS":
		default:
			return nil, bqQueryErr("The simulator does not support the %s clause of CREATE TABLE", name)
		}
	}
	var fields []BQFieldSchema
	if cols, ok := clauses["COLUMNS"]; ok {
		if fields, err = bqParseColumnList(cols); err != nil {
			return nil, err
		}
	}
	result := &bqStatementResult{StatementType: "CREATE_TABLE", DDLTarget: &dest}
	var rows []map[string]any
	if as, ok := clauses["AS"]; ok {
		result.StatementType = "CREATE_TABLE_AS_SELECT"
		query := bqTrimSpace(as[1:])
		schema, queryRows, err := bqEvaluateQueryWith(ctx, bqChildScope(scope), bqSQLText(query))
		if err != nil {
			return nil, err
		}
		result.Referenced = scope.referenced()
		if fields == nil {
			fields = schema.Fields
			for i := range fields {
				if fields[i].Mode == "REQUIRED" {
					fields[i].Mode = "NULLABLE"
				}
			}
			rows = queryRows
		} else {
			if len(fields) != len(schema.Fields) {
				return nil, bqQueryErr("The number of columns in the column list (%d) does not match the number of columns in the query (%d)", len(fields), len(schema.Fields))
			}
			rows = make([]map[string]any, 0, len(queryRows))
			for _, row := range queryRows {
				renamed := make(map[string]any, len(fields))
				for i, f := range fields {
					v, err := bqCoerce(f, row[schema.Fields[i].Name], true)
					if err != nil {
						return nil, bqQueryErr("%s", err.Error())
					}
					renamed[f.Name] = v
				}
				rows = append(rows, renamed)
			}
		}
	}
	if fields == nil {
		return nil, bqQueryErr("Syntax error: CREATE TABLE needs a column list, an AS query, or both")
	}
	table := BQTable{TableReference: dest, Schema: &BQSchema{Fields: fields}}
	if part, ok := clauses["PARTITION"]; ok {
		if table.TimePartitioning, table.RangePartitioning, err = bqParsePartitionBy(part, fields); err != nil {
			return nil, err
		}
	}
	if cluster, ok := clauses["CLUSTER"]; ok {
		if table.Clustering, err = bqParseClusterBy(cluster, fields); err != nil {
			return nil, err
		}
	}
	if options, ok := clauses["OPTIONS"]; ok {
		opts, err := bqParseTableOptions(ctx, options, scope)
		if err != nil {
			return nil, err
		}
		bqApplyTableOptions(&table, opts)
	}
	if _, ok := bqDatasets.Get(bqDatasetKey(dest.ProjectID, dest.DatasetID)); !ok {
		return nil, bqQueryErrReason("notFound", "Not found: Dataset %s:%s", dest.ProjectID, dest.DatasetID)
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	err = bqWriteTable(ctx, dest, func() error {
		existing, exists := bqLiveTable(key)
		switch {
		case exists && ifNotExists:
			result.DDLOperation = "SKIP"
			return nil
		case exists && orReplace:
			if existing.Type != "" && existing.Type != "TABLE" {
				return bqQueryErrReason("invalid", "Cannot replace a %s with a table: %s", strings.ToLower(existing.Type), bqTableName(dest))
			}
			result.DDLOperation = "REPLACE"
		case exists:
			return bqQueryErrReason("duplicate", "Already Exists: Table %s", bqTableName(dest))
		default:
			result.DDLOperation = "CREATE"
		}
		if dryRun {
			return nil
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		bqRows.Put(key, BQRowSet{Rows: rows})
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		return nil
	})
	return result, err
}

func bqApplyTableOptions(table *BQTable, opts bqTableOptions) {
	if opts.hasDescription {
		table.Description = opts.description
	}
	if opts.hasFriendlyName {
		table.FriendlyName = opts.friendlyName
	}
	if opts.labels != nil {
		table.Labels = opts.labels
	}
	if opts.expirationTime != "" {
		table.ExpirationTime = opts.expirationTime
	}
	if opts.requirePartitionFilter != nil {
		table.RequirePartitionFilter = opts.requirePartitionFilter
	}
	if opts.partitionExpirationMs != "" && table.TimePartitioning != nil {
		var spec map[string]any
		if json.Unmarshal(table.TimePartitioning, &spec) == nil {
			spec["expirationMs"] = opts.partitionExpirationMs
			if raw, err := json.Marshal(spec); err == nil {
				table.TimePartitioning = raw
			}
		}
	}
}

// bqChildScope is a scope for a statement's embedded query sharing the
// statement's parameters and start time.
func bqChildScope(scope *bqScope) *bqScope {
	child := bqNewScope(scope.defaultProject, scope.defaultDataset)
	child.now = scope.now
	child.parameters, child.positional = scope.parameters, scope.positional
	child.seen, child.tables = scope.seen, scope.tables
	return child
}

// referenced lists the tables the scope loaded.
func (s *bqScope) referenced() []BQTableRef {
	refs := make([]BQTableRef, 0, len(s.seen))
	for _, t := range s.seen {
		refs = append(refs, t.ref)
	}
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && bqTableName(refs[j]) < bqTableName(refs[j-1]); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
	return refs
}

func bqRunCreateView(ctx context.Context, r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	orReplace, ifNotExists, _, nameAt, err := bqDDLHead(tokens, "VIEW")
	if err != nil {
		return nil, err
	}
	path, after, err := bqReadTablePath(tokens, nameAt)
	if err != nil {
		return nil, err
	}
	dest, err := bqDDLTarget(path, scope)
	if err != nil {
		return nil, err
	}
	clauses := bqDDLClauses(tokens[after:])
	as, ok := clauses["AS"]
	if !ok {
		return nil, bqQueryErr("Syntax error: Expected AS query in CREATE VIEW")
	}
	query := strings.TrimSpace(bqSQLText(bqTrimSpace(as[1:])))
	viewScope := bqChildScope(scope)
	viewScope.defaultDataset = ""
	schema, _, err := bqEvaluateQueryWith(ctx, viewScope, query)
	if err != nil {
		return nil, err
	}
	result := &bqStatementResult{StatementType: "CREATE_VIEW", DDLTarget: &dest, Referenced: scope.referenced()}
	if cols, ok := clauses["COLUMNS"]; ok {
		names := bqSplitArgs(bqTrimSpace(cols)[1 : len(bqTrimSpace(cols))-1])
		if len(names) != len(schema.Fields) {
			return nil, bqQueryErr("The number of view column names (%d) does not match the number of columns in the query (%d)", len(names), len(schema.Fields))
		}
		for i, n := range names {
			if len(n) < 1 || !n[0].isIdent() {
				return nil, bqQueryErr("Syntax error: Expected a column name in the view column list")
			}
			schema.Fields[i].Name = n[0].text
		}
	}
	definition, err := json.Marshal(map[string]any{"query": query, "useLegacySql": false})
	if err != nil {
		return nil, err
	}
	table := BQTable{TableReference: dest, Schema: schema, Type: "VIEW", View: definition}
	if options, ok := clauses["OPTIONS"]; ok {
		opts, err := bqParseTableOptions(ctx, options, scope)
		if err != nil {
			return nil, err
		}
		bqApplyTableOptions(&table, opts)
	}
	if _, ok := bqDatasets.Get(bqDatasetKey(dest.ProjectID, dest.DatasetID)); !ok {
		return nil, bqQueryErrReason("notFound", "Not found: Dataset %s:%s", dest.ProjectID, dest.DatasetID)
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	err = bqWriteTable(ctx, dest, func() error {
		_, exists := bqLiveTable(key)
		switch {
		case exists && ifNotExists:
			result.DDLOperation = "SKIP"
			return nil
		case exists && orReplace:
			result.DDLOperation = "REPLACE"
		case exists:
			return bqQueryErrReason("duplicate", "Already Exists: Table %s", bqTableName(dest))
		default:
			result.DDLOperation = "CREATE"
		}
		if dryRun {
			return nil
		}
		bqRows.Put(key, BQRowSet{Rows: []map[string]any{}})
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		return nil
	})
	return result, err
}

func bqRunDrop(_ *http.Request, scope *bqScope, tokens []bqSQLToken, object string, dryRun bool) (*bqStatementResult, error) {
	i := bqSignificant(tokens, bqSignificant(tokens, 1)+1)
	ifExists := false
	if i < len(tokens) && tokens[i].isWord("IF") {
		j := bqSignificant(tokens, i+1)
		if j >= len(tokens) || !tokens[j].isWord("EXISTS") {
			return nil, bqQueryErr("Syntax error: Expected EXISTS after IF")
		}
		ifExists = true
		i = j + 1
	}
	path, after, err := bqReadTablePath(tokens, i)
	if err != nil {
		return nil, err
	}
	if rest := bqTrimSpace(tokens[after:]); len(rest) > 0 {
		return nil, bqQueryErr("Syntax error: Unexpected %q", bqSQLText(rest))
	}
	dest, err := bqDDLTarget(path, scope)
	if err != nil {
		return nil, err
	}
	result := &bqStatementResult{StatementType: "DROP_" + object, DDLTarget: &dest}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	wantType := "TABLE"
	if object == "VIEW" {
		wantType = "VIEW"
	}
	return result, bqWriteTable(context.Background(), dest, func() error {
		existing, exists := bqLiveTable(key)
		if exists && existing.Type != wantType && (existing.Type != "" || wantType != "TABLE") {
			exists = false
		}
		if !exists {
			if ifExists {
				result.DDLOperation = "SKIP"
				return nil
			}
			return bqQueryErrReason("notFound", "Not found: %s %s", strings.Title(strings.ToLower(object)), bqTableName(dest))
		}
		result.DDLOperation = "DROP"
		if !dryRun {
			bqTables.Delete(key)
			bqRows.Delete(key)
		}
		return nil
	})
}

func bqRunAlterTable(r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	i := bqSignificant(tokens, bqSignificant(tokens, 1)+1)
	ifExists := false
	if i < len(tokens) && tokens[i].isWord("IF") {
		j := bqSignificant(tokens, i+1)
		if j >= len(tokens) || !tokens[j].isWord("EXISTS") {
			return nil, bqQueryErr("Syntax error: Expected EXISTS after IF")
		}
		ifExists = true
		i = j + 1
	}
	path, after, err := bqReadTablePath(tokens, i)
	if err != nil {
		return nil, err
	}
	dest, err := bqDDLTarget(path, scope)
	if err != nil {
		return nil, err
	}
	result := &bqStatementResult{StatementType: "ALTER_TABLE", DDLTarget: &dest}
	type addition struct {
		field       BQFieldSchema
		ifNotExists bool
	}
	var additions []addition
	for _, action := range bqSplitTypeArgs(tokens[after:]) {
		action = bqTrimSpace(action)
		if len(action) < 3 || !action[0].isWord("ADD") || !action[bqSignificant(action, 1)].isWord("COLUMN") {
			return nil, bqQueryErr("The simulator supports ALTER TABLE ADD COLUMN; %q is not supported", bqSQLText(action))
		}
		j := bqSignificant(action, bqSignificant(action, 1)+1)
		add := addition{}
		if j < len(action) && action[j].isWord("IF") {
			k := bqSignificant(action, j+1)
			l := bqSignificant(action, k+1)
			if k >= len(action) || !action[k].isWord("NOT") || l >= len(action) || !action[l].isWord("EXISTS") {
				return nil, bqQueryErr("Syntax error: Expected NOT EXISTS after IF")
			}
			add.ifNotExists = true
			j = bqSignificant(action, l+1)
		}
		if j >= len(action) || !action[j].isIdent() {
			return nil, bqQueryErr("Syntax error: Expected a column name after ADD COLUMN")
		}
		f, err := bqParseColumnSchema(action[j+1:])
		if err != nil {
			return nil, err
		}
		f.Name = action[j].text
		if f.Mode == "REQUIRED" {
			return nil, bqQueryErr("Cannot add required columns to an existing schema. (field: %s)", f.Name)
		}
		additions = append(additions, add)
		additions[len(additions)-1].field = f
	}
	if len(additions) == 0 {
		return nil, bqQueryErr("Syntax error: Expected ADD COLUMN")
	}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	return result, bqWriteTable(context.Background(), dest, func() error {
		table, exists := bqLiveTable(key)
		if !exists {
			if ifExists {
				result.DDLOperation = "SKIP"
				return nil
			}
			return bqQueryErrReason("notFound", "Not found: Table %s", bqTableName(dest))
		}
		if table.Type != "" && table.Type != "TABLE" {
			return bqQueryErrReason("invalid", "Cannot alter a %s with ALTER TABLE: %s", strings.ToLower(table.Type), bqTableName(dest))
		}
		fields := bqSchemaFields(table.Schema)
		var added []BQFieldSchema
		for _, add := range additions {
			if _, ok := bqFieldAtPath(fields, []string{add.field.Name}); ok {
				if add.ifNotExists {
					continue
				}
				return bqQueryErrReason("invalid", "Column already exists: %s", add.field.Name)
			}
			fields = append(fields, add.field)
			added = append(added, add.field)
		}
		if dryRun {
			return nil
		}
		table.Schema = &BQSchema{Fields: fields}
		set, _ := bqRows.Get(key)
		for _, row := range set.Rows {
			for _, f := range added {
				if bqRepeated(f) {
					row[f.Name] = []any{}
				} else {
					row[f.Name] = nil
				}
			}
		}
		bqRows.Put(key, set)
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		return nil
	})
}

func bqRunTruncate(ctx context.Context, r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	path, after, err := bqReadTablePath(tokens, bqSignificant(tokens, bqSignificant(tokens, 1)+1))
	if err != nil {
		return nil, err
	}
	if rest := bqTrimSpace(tokens[after:]); len(rest) > 0 {
		return nil, bqQueryErr("Syntax error: Unexpected %q", bqSQLText(rest))
	}
	dest, err := bqDDLTarget(path, scope)
	if err != nil {
		return nil, err
	}
	result := &bqStatementResult{StatementType: "TRUNCATE_TABLE"}
	key := bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID)
	return result, bqWriteTable(ctx, dest, func() error {
		table, exists := bqLiveTable(key)
		if !exists {
			return bqQueryErrReason("notFound", "Not found: Table %s", bqTableName(dest))
		}
		if table.Type != "" && table.Type != "TABLE" {
			return bqQueryErrReason("invalid", "Truncating a %s is not supported: %s", strings.ToLower(table.Type), bqTableName(dest))
		}
		set, _ := bqRows.Get(key)
		result.DMLStats = map[string]string{"insertedRowCount": "0", "updatedRowCount": "0", "deletedRowCount": strconv.Itoa(len(set.Rows))}
		if dryRun {
			return nil
		}
		bqRows.Put(key, BQRowSet{Rows: []map[string]any{}})
		bqTables.Put(key, bqApplyTableDefaults(r, table, dest.ProjectID, dest.DatasetID, dest.TableID))
		return nil
	})
}

// bqExpiration is the milliseconds-since-epoch text of a time.
func bqExpiration(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
