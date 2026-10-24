package main

import (
	"context"

	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The DML statements, from the vendored dml-syntax reference
// (specs/cloud-api/gcp/bigquery-dml.md.gz): each runs in the engine against
// the loaded copy of its target, which is then written back whole.

// bqStatementKind classifies a statement by its first word.
func bqStatementKind(tokens []bqSQLToken) string {
	for _, t := range tokens {
		if t.isSpace() {
			continue
		}
		if t.kind == bqTokIdent {
			switch upper := strings.ToUpper(t.text); upper {
			case "SELECT", "WITH":
				return "QUERY"
			case "INSERT", "UPDATE", "DELETE", "MERGE":
				return "DML"
			case "CREATE", "DROP", "ALTER", "TRUNCATE":
				return "DDL"
			case "DECLARE", "SET", "BEGIN", "IF", "WHILE", "LOOP", "FOR", "REPEAT", "RAISE", "RETURN", "CALL", "EXECUTE", "BREAK", "CONTINUE", "LEAVE", "ITERATE", "ELSEIF", "ELSE", "END", "EXCEPTION", "COMMIT", "ROLLBACK":
				return "SCRIPT"
			default:
				return upper
			}
		}
		if t.isPunct("(") {
			return "QUERY"
		}
		return t.text
	}
	return ""
}

// bqRunStatement runs one statement and answers its result and statistics.
func bqRunStatement(ctx context.Context, r *http.Request, scope *bqScope, statement string, dryRun bool) (*bqStatementResult, error) {
	tokens, err := bqTokenizeSQL(strings.TrimSuffix(strings.TrimSpace(statement), ";"))
	if err != nil {
		return nil, err
	}
	switch bqStatementKind(tokens) {
	case "QUERY":
		schema, rows, err := bqEvaluateQueryWith(ctx, scope, statement)
		if err != nil {
			return nil, err
		}
		return &bqStatementResult{StatementType: "SELECT", Schema: schema, Rows: rows, Referenced: scope.referenced(), BytesProcessed: bqBytesProcessed(scope, tokens), isQuery: true}, nil
	case "DML":
		return bqRunDML(ctx, r, scope, tokens, dryRun)
	case "DDL":
		return bqRunDDL(ctx, r, scope, tokens, dryRun)
	case "SCRIPT":
		return nil, bqQueryErr("Syntax error: %s is a procedural statement and must be part of a multi-statement query", bqLeadingWords(tokens, 1)[0])
	case "":
		return nil, bqQueryErr("Syntax error: Unexpected end of script")
	default:
		return nil, bqQueryErr("Syntax error: Unexpected keyword %s", bqLeadingWords(tokens, 1)[0])
	}
}

// bqBytesProcessed estimates what a query read: the stored size of every
// column the statement names in each table it references.
func bqBytesProcessed(scope *bqScope, tokens []bqSQLToken) int64 {
	named := map[string]bool{}
	for _, t := range tokens {
		switch {
		case t.isIdent():
			named[strings.ToLower(t.text)] = true
		case t.isPunct("*"):
			named["*"] = true
		}
	}
	var total int64
	for _, t := range scope.seen {
		set, _ := bqRows.Get(bqTableKey(t.ref.ProjectID, t.ref.DatasetID, t.ref.TableID))
		for _, f := range t.schema {
			if !named[strings.ToLower(f.Name)] && !named["*"] {
				continue
			}
			for _, row := range set.Rows {
				total += bqCellSize(f, row[f.Name])
			}
		}
	}
	return total
}

// bqRunDML runs INSERT, UPDATE, DELETE or MERGE.
func bqRunDML(ctx context.Context, r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	kind := bqLeadingWords(tokens, 1)[0]
	if kind == "MERGE" {
		return bqRunMerge(ctx, r, scope, tokens, dryRun)
	}
	rewritten, err := bqRewriteFrom(tokens, scope)
	if err != nil {
		return nil, err
	}
	if len(scope.items) == 0 || scope.items[0].table == nil {
		return nil, bqQueryErr("Syntax error: Expected a table name in %s", kind)
	}
	target := *scope.items[0].table
	rewritten, err = bqRewriteExpressions(rewritten, scope)
	if err != nil {
		return nil, err
	}
	if kind == "UPDATE" || kind == "DELETE" {
		if !bqHasDepthZeroWord(rewritten, "WHERE") {
			return nil, bqQueryErr("%s must have a WHERE clause", kind)
		}
	}
	if kind == "INSERT" {
		rewritten = bqDropInsertAlias(rewritten)
	}
	if kind == "UPDATE" {
		rewritten, err = bqRewriteUpdateSet(rewritten, scope.items[0])
		if err != nil {
			return nil, err
		}
	}
	db, err := bqOpenEngine(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	res, err := db.ExecContext(ctx, bqSQLText(rewritten))
	if err != nil {
		return nil, bqEngineError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	rows, err := bqReadTableRows(ctx, db, target)
	if err != nil {
		return nil, err
	}
	if err := bqCheckRequired(target.schema, rows); err != nil {
		return nil, err
	}
	result := &bqStatementResult{StatementType: kind, NumDMLAffected: &affected, Referenced: scope.referenced(), DMLStats: map[string]string{"insertedRowCount": "0", "updatedRowCount": "0", "deletedRowCount": "0"}}
	result.DMLStats[map[string]string{"INSERT": "insertedRowCount", "UPDATE": "updatedRowCount", "DELETE": "deletedRowCount"}[kind]] = strconv.FormatInt(affected, 10)
	if dryRun {
		return result, nil
	}
	return result, bqStoreTableRows(ctx, r, target.ref, rows)
}

// bqStoreTableRows replaces a table's rows with what a statement left.
func bqStoreTableRows(ctx context.Context, r *http.Request, ref BQTableRef, rows []map[string]any) error {
	key := bqTableKey(ref.ProjectID, ref.DatasetID, ref.TableID)
	return bqWriteTable(ctx, ref, func() error {
		table, ok := bqLiveTable(key)
		if !ok {
			return bqQueryErrReason("notFound", "Not found: Table %s", bqTableName(ref))
		}
		if table.Type != "" && table.Type != "TABLE" {
			return bqQueryErrReason("invalid", "DML over %s %s is not supported.", strings.ToLower(table.Type), bqTableName(ref))
		}
		bqRows.Put(key, BQRowSet{Rows: rows})
		bqTables.Put(key, bqApplyTableDefaults(r, table, ref.ProjectID, ref.DatasetID, ref.TableID))
		return nil
	})
}

// bqCheckRequired refuses rows that leave a REQUIRED column NULL.
func bqCheckRequired(fields []BQFieldSchema, rows []map[string]any) error {
	for _, f := range fields {
		if !strings.EqualFold(f.Mode, "REQUIRED") {
			continue
		}
		for _, row := range rows {
			if row[f.Name] == nil {
				return bqQueryErr("Required field %s cannot be null", f.Name)
			}
		}
	}
	return nil
}

func bqHasDepthZeroWord(tokens []bqSQLToken, word string) bool {
	depth := 0
	for _, t := range tokens {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isIdent() && strings.EqualFold(t.text, word):
			return true
		}
	}
	return false
}

// bqDropInsertAlias removes the alias the FROM rewrite gives INSERT's target,
// which SQLite's INSERT does not take before a column list.
func bqDropInsertAlias(tokens []bqSQLToken) []bqSQLToken {
	for i := 0; i+4 < len(tokens); i++ {
		if tokens[i].kind == bqTokQuoted && tokens[i].fixed && strings.HasPrefix(tokens[i].text, "`bq_table_") {
			j := bqSignificant(tokens, i+1)
			k := bqSignificant(tokens, j+1)
			if j < len(tokens) && tokens[j].isWord("AS") && k < len(tokens) && tokens[k].kind == bqTokQuoted {
				return append(append([]bqSQLToken{}, tokens[:i+1]...), tokens[k+1:]...)
			}
			return tokens
		}
	}
	return tokens
}

// bqRewriteUpdateSet strips the target's qualifier from the columns an UPDATE
// assigns, which SQLite names bare.
func bqRewriteUpdateSet(tokens []bqSQLToken, target bqFromItem) ([]bqSQLToken, error) {
	set := -1
	depth := 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("SET"):
			set = i
		}
		if set >= 0 {
			break
		}
	}
	if set < 0 {
		return nil, bqQueryErr("Syntax error: Expected SET in UPDATE")
	}
	end := len(tokens)
	depth = 0
	for i := set + 1; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && (t.isWord("FROM") || t.isWord("WHERE")):
			end = i
			i = len(tokens)
		}
	}
	alias := bqQuotedToken(target.sqlName).text
	var assignments []bqSQLToken
	for n, item := range bqSplitArgs(tokens[set+1 : end]) {
		if n > 0 {
			assignments = append(assignments, bqPunctToken(","), bqSpaceToken())
		}
		if len(item) >= 3 && item[0].kind == bqTokQuoted && item[0].text == alias && item[1].isPunct(".") {
			item = item[2:]
		}
		for _, t := range item {
			if t.fixed && strings.EqualFold(t.text, "json_extract") {
				return nil, bqQueryErr("The simulator does not support updating a field inside a STRUCT column")
			}
		}
		assignments = append(assignments, item...)
	}
	out := append([]bqSQLToken{}, tokens[:set+1]...)
	out = append(out, bqSpaceToken())
	out = append(out, assignments...)
	out = append(out, bqSpaceToken())
	return append(out, tokens[end:]...), nil
}

// bqMergeClause is one WHEN clause of a MERGE.
type bqMergeClause struct {
	kind      string // MATCHED, NOT_MATCHED_BY_TARGET, NOT_MATCHED_BY_SOURCE
	condition []bqSQLToken
	action    string // UPDATE, DELETE, INSERT
	set       []bqSQLToken
	columns   []bqSQLToken
	values    []bqSQLToken
	insertRow bool
}

// bqRunMerge runs a MERGE: the target and source join once on the merge
// condition, as the tables stood when the statement started, and each WHEN
// clause acts in order on the rows no earlier clause took.
func bqRunMerge(ctx context.Context, r *http.Request, scope *bqScope, tokens []bqSQLToken, dryRun bool) (*bqStatementResult, error) {
	on := -1
	depth := 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("ON") && on < 0:
			on = i
		}
	}
	if on < 0 {
		return nil, bqQueryErr("Syntax error: Expected ON in MERGE")
	}
	head, err := bqRewriteFrom(tokens[:on], scope)
	if err != nil {
		return nil, err
	}
	if len(scope.items) != 2 || scope.items[0].table == nil {
		return nil, bqQueryErr("Syntax error: MERGE needs a target table and a USING source")
	}
	target, source := scope.items[0], scope.items[1]
	rest, err := bqRewriteExpressions(tokens[on+1:], scope)
	if err != nil {
		return nil, err
	}
	condition, clauses, err := bqParseMergeClauses(rest)
	if err != nil {
		return nil, err
	}
	sourceSQL := bqMergeSourceSQL(head, source)
	db, err := bqOpenEngine(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	targetSQL := `"` + target.table.name + `"`
	t := bqQuotedToken(target.sqlName).text
	s := bqQuotedToken(source.sqlName).text
	exec := func(stmt string) (int64, error) {
		res, err := db.ExecContext(ctx, stmt)
		if err != nil {
			return 0, bqEngineError(err)
		}
		return res.RowsAffected()
	}
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE TEMP TABLE bq_src AS SELECT %s.* FROM %s`, s, sourceSQL),
		fmt.Sprintf(`CREATE TEMP TABLE bq_match AS SELECT %s.rowid AS trow, %s.rowid AS srow FROM %s AS %s JOIN bq_src AS %s ON %s`, t, s, targetSQL, t, s, bqSQLText(condition)),
		`CREATE TEMP TABLE bq_done_t (trow INTEGER PRIMARY KEY)`,
		`CREATE TEMP TABLE bq_done_s (srow INTEGER PRIMARY KEY)`,
	} {
		if _, err := exec(stmt); err != nil {
			return nil, err
		}
	}
	for _, c := range clauses {
		if c.kind == "MATCHED" {
			var dup int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT trow FROM bq_match GROUP BY trow HAVING COUNT(*) > 1)`).Scan(&dup); err != nil {
				return nil, bqEngineError(err)
			}
			if dup > 0 {
				return nil, bqQueryErr("UPDATE/MERGE must match at most one source row for each target row")
			}
			break
		}
	}
	stats := map[string]int64{"inserted": 0, "updated": 0, "deleted": 0}
	for _, c := range clauses {
		cond := "1"
		if len(c.condition) > 0 {
			cond = "(" + bqSQLText(c.condition) + ")"
		}
		var now string
		switch c.kind {
		case "MATCHED":
			now = fmt.Sprintf(`CREATE TEMP TABLE bq_now AS SELECT m.trow AS trow, m.srow AS srow FROM bq_match m JOIN %s AS %s ON %s.rowid = m.trow JOIN bq_src AS %s ON %s.rowid = m.srow WHERE %s AND m.trow NOT IN (SELECT trow FROM bq_done_t)`, targetSQL, t, t, s, s, cond)
		case "NOT_MATCHED_BY_TARGET":
			now = fmt.Sprintf(`CREATE TEMP TABLE bq_now AS SELECT NULL AS trow, %s.rowid AS srow FROM bq_src AS %s WHERE %s.rowid NOT IN (SELECT srow FROM bq_match) AND %s AND %s.rowid NOT IN (SELECT srow FROM bq_done_s)`, s, s, s, cond, s)
		case "NOT_MATCHED_BY_SOURCE":
			now = fmt.Sprintf(`CREATE TEMP TABLE bq_now AS SELECT %s.rowid AS trow, NULL AS srow FROM %s AS %s WHERE %s.rowid NOT IN (SELECT trow FROM bq_match) AND %s AND %s.rowid NOT IN (SELECT trow FROM bq_done_t)`, t, targetSQL, t, t, cond, t)
		}
		if _, err := exec(now); err != nil {
			return nil, err
		}
		var action string
		switch c.action {
		case "UPDATE":
			set, err := bqMergeSetList(c.set, t)
			if err != nil {
				return nil, err
			}
			if c.kind == "MATCHED" {
				action = fmt.Sprintf(`UPDATE %s AS %s SET %s FROM bq_now n JOIN bq_src AS %s ON %s.rowid = n.srow WHERE %s.rowid = n.trow`, targetSQL, t, set, s, s, t)
			} else {
				action = fmt.Sprintf(`UPDATE %s AS %s SET %s WHERE %s.rowid IN (SELECT trow FROM bq_now)`, targetSQL, t, set, t)
			}
		case "DELETE":
			action = fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (SELECT trow FROM bq_now)`, targetSQL)
		case "INSERT":
			if c.insertRow {
				action = fmt.Sprintf(`INSERT INTO %s SELECT %s.* FROM bq_src AS %s WHERE %s.rowid IN (SELECT srow FROM bq_now)`, targetSQL, s, s, s)
			} else {
				columns := ""
				if len(c.columns) > 0 {
					columns = " (" + bqSQLText(c.columns) + ")"
				}
				action = fmt.Sprintf(`INSERT INTO %s%s SELECT %s FROM bq_src AS %s WHERE %s.rowid IN (SELECT srow FROM bq_now)`, targetSQL, columns, bqSQLText(c.values), s, s)
			}
		}
		n, err := exec(action)
		if err != nil {
			return nil, err
		}
		stats[map[string]string{"UPDATE": "updated", "DELETE": "deleted", "INSERT": "inserted"}[c.action]] += n
		for _, stmt := range []string{
			`INSERT OR IGNORE INTO bq_done_t SELECT trow FROM bq_now WHERE trow IS NOT NULL`,
			`INSERT OR IGNORE INTO bq_done_s SELECT srow FROM bq_now WHERE srow IS NOT NULL`,
			`DROP TABLE bq_now`,
		} {
			if _, err := exec(stmt); err != nil {
				return nil, err
			}
		}
	}
	rows, err := bqReadTableRows(ctx, db, *target.table)
	if err != nil {
		return nil, err
	}
	if err := bqCheckRequired(target.table.schema, rows); err != nil {
		return nil, err
	}
	result := &bqStatementResult{StatementType: "MERGE", Referenced: scope.referenced(), DMLStats: map[string]string{
		"insertedRowCount": strconv.FormatInt(stats["inserted"], 10),
		"updatedRowCount":  strconv.FormatInt(stats["updated"], 10),
		"deletedRowCount":  strconv.FormatInt(stats["deleted"], 10),
	}}
	if dryRun {
		return result, nil
	}
	return result, bqStoreTableRows(ctx, r, target.table.ref, rows)
}

// bqMergeSourceSQL is the FROM item of the source as the rewritten head
// spelled it: the loaded table or the subquery, with its alias.
func bqMergeSourceSQL(head []bqSQLToken, source bqFromItem) string {
	using := -1
	for i, t := range head {
		if t.isWord("USING") {
			using = i
		}
	}
	if using < 0 {
		return ""
	}
	return strings.TrimSpace(bqSQLText(head[using+1:]))
}

// bqMergeSetList strips the target alias from UPDATE SET assignments.
func bqMergeSetList(set []bqSQLToken, alias string) (string, error) {
	var parts []string
	for _, item := range bqSplitArgs(set) {
		if len(item) >= 3 && item[0].kind == bqTokQuoted && item[0].text == alias && item[1].isPunct(".") {
			item = item[2:]
		}
		for _, t := range item {
			if t.fixed && strings.EqualFold(t.text, "json_extract") {
				return "", bqQueryErr("The simulator does not support updating a field inside a STRUCT column")
			}
		}
		parts = append(parts, bqSQLText(item))
	}
	if len(parts) == 0 {
		return "", bqQueryErr("Syntax error: Expected assignments after SET")
	}
	return strings.Join(parts, ", "), nil
}

// bqParseMergeClauses reads the merge condition and the WHEN clauses that
// follow ON.
func bqParseMergeClauses(tokens []bqSQLToken) ([]bqSQLToken, []bqMergeClause, error) {
	var whens []int
	depth := 0
	for i, t := range tokens {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case depth == 0 && t.isWord("WHEN"):
			whens = append(whens, i)
		}
	}
	if len(whens) == 0 {
		return nil, nil, bqQueryErr("Syntax error: Expected at least one WHEN clause in MERGE")
	}
	condition := bqTrimSpace(tokens[:whens[0]])
	var clauses []bqMergeClause
	for n, start := range whens {
		end := len(tokens)
		if n+1 < len(whens) {
			end = whens[n+1]
		}
		clause, err := bqParseMergeClause(bqTrimSpace(tokens[start+1 : end]))
		if err != nil {
			return nil, nil, err
		}
		clauses = append(clauses, clause)
	}
	return condition, clauses, nil
}

func bqParseMergeClause(tokens []bqSQLToken) (bqMergeClause, error) {
	var c bqMergeClause
	i := 0
	next := func() bqSQLToken {
		i = bqSignificant(tokens, i)
		if i >= len(tokens) {
			return bqSQLToken{}
		}
		t := tokens[i]
		i++
		return t
	}
	switch first := next(); {
	case first.isWord("MATCHED"):
		c.kind = "MATCHED"
	case first.isWord("NOT"):
		if !next().isWord("MATCHED") {
			return c, bqQueryErr("Syntax error: Expected MATCHED after NOT")
		}
		c.kind = "NOT_MATCHED_BY_TARGET"
		if j := bqSignificant(tokens, i); j < len(tokens) && tokens[j].isWord("BY") {
			i = j + 1
			switch by := next(); {
			case by.isWord("TARGET"):
			case by.isWord("SOURCE"):
				c.kind = "NOT_MATCHED_BY_SOURCE"
			default:
				return c, bqQueryErr("Syntax error: Expected TARGET or SOURCE after BY")
			}
		}
	default:
		return c, bqQueryErr("Syntax error: Expected MATCHED or NOT MATCHED after WHEN")
	}
	then := -1
	depth := 0
	for j := i; j < len(tokens); j++ {
		switch {
		case tokens[j].isPunct("("):
			depth++
		case tokens[j].isPunct(")"):
			depth--
		case depth == 0 && tokens[j].isWord("THEN"):
			then = j
		}
		if then >= 0 {
			break
		}
	}
	if then < 0 {
		return c, bqQueryErr("Syntax error: Expected THEN in WHEN clause")
	}
	if j := bqSignificant(tokens, i); j < then {
		if !tokens[j].isWord("AND") {
			return c, bqQueryErr("Syntax error: Expected AND or THEN after %s", c.kind)
		}
		c.condition = bqTrimSpace(tokens[j+1 : then])
	}
	body := bqTrimSpace(tokens[then+1:])
	if len(body) == 0 {
		return c, bqQueryErr("Syntax error: Expected UPDATE, DELETE or INSERT after THEN")
	}
	switch {
	case body[0].isWord("UPDATE"):
		set := bqSignificant(body, 1)
		if set >= len(body) || !body[set].isWord("SET") {
			return c, bqQueryErr("Syntax error: Expected SET after UPDATE")
		}
		c.action, c.set = "UPDATE", bqTrimSpace(body[set+1:])
	case body[0].isWord("DELETE"):
		c.action = "DELETE"
	case body[0].isWord("INSERT"):
		c.action = "INSERT"
		j := bqSignificant(body, 1)
		if j < len(body) && body[j].isWord("ROW") {
			c.insertRow = true
			break
		}
		if j < len(body) && body[j].isPunct("(") {
			end := bqMatchParen(body, j)
			c.columns = body[j+1 : end]
			j = bqSignificant(body, end+1)
		}
		if j >= len(body) || !body[j].isWord("VALUES") {
			return c, bqQueryErr("Syntax error: Expected VALUES or ROW after INSERT")
		}
		open := bqSignificant(body, j+1)
		if open >= len(body) || !body[open].isPunct("(") || bqMatchParen(body, open) != len(body)-1 {
			return c, bqQueryErr("Syntax error: Expected (values) after VALUES")
		}
		c.values = body[open+1 : len(body)-1]
	default:
		return c, bqQueryErr("Syntax error: Expected UPDATE, DELETE or INSERT after THEN")
	}
	if c.kind == "NOT_MATCHED_BY_TARGET" && c.action != "INSERT" || c.kind != "NOT_MATCHED_BY_TARGET" && c.action == "INSERT" {
		return c, bqQueryErr("Syntax error: %s does not take %s", strings.ReplaceAll(c.kind, "_", " "), c.action)
	}
	return c, nil
}
