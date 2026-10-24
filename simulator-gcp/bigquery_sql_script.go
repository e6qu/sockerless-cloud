package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Multi-statement queries, from the vendored procedural-language and
// multi-statement-queries references (specs/cloud-api/gcp/bigquery-procedural-
// language.md.gz, bigquery-multi-statement-queries.md.gz): each statement
// runs as a child job of the script job, variables declared with DECLARE and
// assigned with SET are substituted into later statements, and IF and
// BEGIN...END choose and group statements.

// bqScriptStatement is one top-level statement of a script with its position.
type bqScriptStatement struct {
	tokens     []bqSQLToken
	text       string
	startLine  int
	startCol   int
	endLine    int
	endCol     int
	startIndex int
}

// bqSplitScript splits a script on the semicolons that end its top-level
// statements, keeping BEGIN...END and IF...END IF blocks whole.
func bqSplitScript(script string) ([]bqScriptStatement, error) {
	tokens, err := bqTokenizeSQL(script)
	if err != nil {
		return nil, err
	}
	offsets := make([]int, len(tokens)+1)
	for i, t := range tokens {
		offsets[i+1] = offsets[i] + utf8.RuneCountInString(t.text)
	}
	var out []bqScriptStatement
	start := 0
	depth, blocks := 0, 0
	for i := 0; i <= len(tokens); i++ {
		if i < len(tokens) {
			t := tokens[i]
			switch {
			case t.isPunct("("):
				depth++
			case t.isPunct(")"):
				depth--
			case depth == 0 && (t.isWord("BEGIN") || t.isWord("IF") || t.isWord("WHILE") || t.isWord("LOOP") || t.isWord("CASE")) && bqStartsStatement(tokens, start, i) && !bqTransactionWord(tokens, i):
				blocks++
			case depth == 0 && t.isWord("END"):
				blocks--
				if blocks < 0 {
					return nil, bqQueryErr("Syntax error: Unexpected END")
				}
				// END IF, END WHILE and END LOOP close with a second word.
				if j := bqSignificant(tokens, i+1); j < len(tokens) && (tokens[j].isWord("IF") || tokens[j].isWord("WHILE") || tokens[j].isWord("LOOP")) {
					i = j
				}
			}
			if depth != 0 || blocks != 0 || !t.isPunct(";") {
				continue
			}
		}
		if body := bqTrimSpace(tokens[start:i]); len(body) > 0 {
			first := start
			for first < i && tokens[first].isSpace() {
				first++
			}
			last := i
			for last > first && tokens[last-1].isSpace() {
				last--
			}
			startLine, startCol := bqLineColumn(script, offsets[first])
			endLine, endCol := bqLineColumn(script, offsets[last])
			out = append(out, bqScriptStatement{tokens: body, text: bqSQLText(body), startLine: startLine, startCol: startCol, endLine: endLine, endCol: endCol, startIndex: offsets[first]})
		}
		start = i + 1
	}
	if blocks > 0 {
		return nil, bqQueryErr("Syntax error: Unclosed block; expected END")
	}
	return out, nil
}

// bqStartsStatement reports whether the token at i begins a statement of
// the script segment that started at start.
func bqStartsStatement(tokens []bqSQLToken, start, i int) bool {
	prev := bqPrevSignificant(tokens, i)
	if prev < start {
		return true
	}
	t := tokens[prev]
	return t.isPunct(";") || t.isWord("BEGIN") || t.isWord("THEN") || t.isWord("ELSE") || t.isWord("DO") || t.isWord("LOOP")
}

// bqTransactionWord reports whether BEGIN at i starts BEGIN TRANSACTION.
func bqTransactionWord(tokens []bqSQLToken, i int) bool {
	j := bqSignificant(tokens, i+1)
	return j < len(tokens) && tokens[j].isWord("TRANSACTION")
}

// bqLineColumn is the one-based line and column of a rune offset.
func bqLineColumn(text string, offset int) (int, int) {
	line, col := 1, 1
	for i, r := range []rune(text) {
		if i >= offset {
			break
		}
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// bqVariable is a script variable: its declared type and stored-form value.
type bqVariable struct {
	typ   BQFieldSchema
	value any
}

// bqScriptRunner carries a script's state and makes its child jobs.
type bqScriptRunner struct {
	ctx      context.Context
	r        *http.Request
	project  string
	dataset  string
	scope    *bqScope
	dryRun   bool
	child    func(statement string, frame map[string]any, kind string, result *bqStatementResult, err error) error
	last     *bqStatementResult
	children int
}

// bqRunScript runs a multi-statement query. child records each child job;
// the answer is the last statement's result.
func bqRunScript(ctx context.Context, r *http.Request, scope *bqScope, script string, dryRun bool,
	child func(statement string, frame map[string]any, kind string, result *bqStatementResult, err error) error) (*bqStatementResult, int, error) {
	statements, err := bqSplitScript(script)
	if err != nil {
		return nil, 0, err
	}
	runner := &bqScriptRunner{ctx: ctx, r: r, project: scope.defaultProject, dataset: scope.defaultDataset, scope: scope, dryRun: dryRun, child: child}
	if err := runner.runBlock(statements, map[string]*bqVariable{}, true); err != nil {
		return nil, runner.children, err
	}
	if runner.last == nil {
		runner.last = &bqStatementResult{}
	}
	return runner.last, runner.children, nil
}

// runBlock runs statements with the variables in scope; declarations open a
// block.
func (sr *bqScriptRunner) runBlock(statements []bqScriptStatement, outer map[string]*bqVariable, allowDeclare bool) error {
	vars := map[string]*bqVariable{}
	for k, v := range outer {
		vars[k] = v
	}
	declared := map[string]bool{}
	for _, st := range statements {
		kind := bqLeadingWords(st.tokens, 1)[0]
		if kind != "DECLARE" {
			allowDeclare = false
		}
		switch kind {
		case "DECLARE":
			if !allowDeclare {
				return bqQueryErr("Variable declarations are allowed only at the start of a block or script")
			}
			if err := sr.declare(st, vars, declared); err != nil {
				return err
			}
		case "SET":
			if err := sr.set(st, vars); err != nil {
				return err
			}
		case "BEGIN":
			if bqTransactionWord(st.tokens, 0) {
				return bqQueryErr("The simulator does not support multi-statement transactions")
			}
			inner, err := bqSplitScript(bqSQLText(bqBlockBody(st.tokens)))
			if err != nil {
				return err
			}
			for _, s := range inner {
				if bqLeadingWords(s.tokens, 1)[0] == "EXCEPTION" {
					return bqQueryErr("The simulator does not support BEGIN...EXCEPTION...END")
				}
			}
			if err := sr.runBlock(sr.shiftStatements(inner, st), vars, true); err != nil {
				return err
			}
		case "IF":
			if err := sr.runIf(st, vars); err != nil {
				return err
			}
		case "WHILE", "LOOP", "FOR", "REPEAT", "BREAK", "CONTINUE", "LEAVE", "ITERATE", "RAISE", "RETURN", "CALL", "EXECUTE", "CASE", "COMMIT", "ROLLBACK":
			return bqQueryErr("The simulator does not support the %s procedural statement", kind)
		default:
			if err := sr.runStatement(st, vars); err != nil {
				return err
			}
		}
	}
	return nil
}

// shiftStatements places the statements of a nested block at their position
// in the script.
func (sr *bqScriptRunner) shiftStatements(inner []bqScriptStatement, block bqScriptStatement) []bqScriptStatement {
	for i := range inner {
		inner[i].startLine += block.startLine - 1
		inner[i].endLine += block.startLine - 1
		if inner[i].startLine == block.startLine {
			inner[i].startCol += block.startCol - 1
		}
		if inner[i].endLine == block.startLine {
			inner[i].endCol += block.startCol - 1
		}
	}
	return inner
}

// bqBlockBody is what lies between BEGIN and its END.
func bqBlockBody(tokens []bqSQLToken) []bqSQLToken {
	tokens = bqTrimSpace(tokens)
	end := len(tokens) - 1
	for end > 0 && !tokens[end].isWord("END") {
		end--
	}
	return tokens[bqSignificant(tokens, 1):end]
}

func (sr *bqScriptRunner) frame(st bqScriptStatement, text string) map[string]any {
	return map[string]any{"startLine": st.startLine, "startColumn": st.startCol, "endLine": st.endLine, "endColumn": st.endCol, "text": text}
}

// substitute replaces variable references with their literal values.
func (sr *bqScriptRunner) substitute(tokens []bqSQLToken, vars map[string]*bqVariable) ([]bqSQLToken, error) {
	var out []bqSQLToken
	for i, t := range tokens {
		if t.kind == bqTokIdent && !t.fixed {
			if v, ok := vars[strings.ToLower(t.text)]; ok {
				prev := bqPrevSignificant(tokens, i)
				next := bqSignificant(tokens, i+1)
				afterDot, isCall, isAlias := prev >= 0 && tokens[prev].isPunct("."), next < len(tokens) && tokens[next].isPunct("("), prev >= 0 && tokens[prev].isWord("AS")
				if !afterDot && !isCall && !isAlias {
					lit, err := bqLiteralTokens(v.typ, v.value)
					if err != nil {
						return nil, err
					}
					out = append(out, lit...)
					continue
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// evaluate computes an expression with the variables substituted.
func (sr *bqScriptRunner) evaluate(expr []bqSQLToken, vars map[string]*bqVariable, st bqScriptStatement) (any, BQFieldSchema, error) {
	substituted, err := sr.substitute(expr, vars)
	if err != nil {
		return nil, BQFieldSchema{}, err
	}
	text := bqSQLText(substituted)
	scope := bqChildScope(sr.scope)
	value, typ, err := bqEvaluateConstantQuery(sr.ctx, substituted, scope)
	sr.children++
	if sr.child != nil {
		if cerr := sr.child(text, sr.frame(st, strings.TrimSpace(bqSQLText(expr))), "EXPRESSION", &bqStatementResult{Referenced: scope.referenced()}, err); cerr != nil {
			return nil, BQFieldSchema{}, cerr
		}
	}
	return value, typ, err
}

// bqEvaluateConstantQuery evaluates an expression that may hold a scalar
// subquery over the simulator's tables.
func bqEvaluateConstantQuery(ctx context.Context, expr []bqSQLToken, scope *bqScope) (any, BQFieldSchema, error) {
	from, err := bqRewriteFrom(expr, scope)
	if err != nil {
		return nil, BQFieldSchema{}, err
	}
	rewritten, err := bqRewriteExpressions(from, scope)
	if err != nil {
		return nil, BQFieldSchema{}, err
	}
	typ := bqExprType(rewritten, scope)
	db, err := bqOpenEngine(ctx, scope)
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

func (sr *bqScriptRunner) declare(st bqScriptStatement, vars map[string]*bqVariable, declared map[string]bool) error {
	tokens := st.tokens
	var names []string
	i := bqSignificant(tokens, 1)
	for i < len(tokens) {
		if !tokens[i].isIdent() {
			return bqQueryErr("Syntax error: Expected a variable name in DECLARE")
		}
		names = append(names, tokens[i].text)
		j := bqSignificant(tokens, i+1)
		if j < len(tokens) && tokens[j].isPunct(",") {
			i = bqSignificant(tokens, j+1)
			continue
		}
		i = j
		break
	}
	var typ BQFieldSchema
	hasType := false
	var defaultExpr []bqSQLToken
	if i < len(tokens) && !tokens[i].isWord("DEFAULT") {
		depth := 0
		end := len(tokens)
		for j := i; j < len(tokens); j++ {
			switch {
			case tokens[j].isPunct("("):
				depth++
			case tokens[j].isPunct(")"):
				depth--
			case depth == 0 && tokens[j].isWord("DEFAULT"):
				end = j
				j = len(tokens)
			}
		}
		var err error
		if typ, err = bqParseSQLType(bqSQLText(tokens[i:end])); err != nil {
			return err
		}
		hasType = true
		i = end
	}
	if i < len(tokens) && tokens[i].isWord("DEFAULT") {
		defaultExpr = bqTrimSpace(tokens[i+1:])
	} else if !hasType {
		return bqQueryErr("Syntax error: DECLARE without a type requires a DEFAULT expression")
	}
	for _, name := range names {
		if declared[strings.ToLower(name)] {
			return bqQueryErr("Variable '%s' previously declared as a variable", name)
		}
	}
	var value any
	if defaultExpr != nil {
		v, inferred, err := sr.evaluate(defaultExpr, vars, st)
		if err != nil {
			return err
		}
		if !hasType {
			typ = inferred
		} else if v != nil {
			if v, err = bqCoerceVariable(typ, inferred, v); err != nil {
				return err
			}
		}
		value = v
	}
	if typ.Type == "" {
		typ = BQFieldSchema{Type: "INT64"}
	}
	for _, name := range names {
		declared[strings.ToLower(name)] = true
		vars[strings.ToLower(name)] = &bqVariable{typ: typ, value: value}
	}
	return nil
}

// bqCoerceVariable converts a value to a variable's declared type.
func bqCoerceVariable(typ, from BQFieldSchema, value any) (any, error) {
	if bqType(typ) == bqType(from) || from.Type == "" || value == nil {
		return value, nil
	}
	if bqNested(typ) || bqNested(from) {
		if bqNested(typ) && bqNested(from) {
			return value, nil
		}
		return nil, bqQueryErr("Value of type %s cannot be assigned to a variable of type %s", bqSQLTypeName(from), bqSQLTypeName(typ))
	}
	v, err := bqCastValue(bqSQLiteValueOf(from, value), bqSQLTypeName(typ), from, true)
	if err != nil {
		return nil, bqQueryErr("Value of type %s cannot be assigned to a variable of type %s", bqSQLTypeName(from), bqSQLTypeName(typ))
	}
	return bqResultCell(typ, v)
}

// bqSQLiteValueOf is a stored cell as the engine would hold it.
func bqSQLiteValueOf(f BQFieldSchema, value any) any {
	v, err := bqSQLiteValue(f, value)
	if err != nil {
		return bqScalarText(value)
	}
	return v
}

func (sr *bqScriptRunner) set(st bqScriptStatement, vars map[string]*bqVariable) error {
	tokens := st.tokens
	i := bqSignificant(tokens, 1)
	if i >= len(tokens) {
		return bqQueryErr("Syntax error: Expected a variable name after SET")
	}
	eq := -1
	depth := 0
	for j := i; j < len(tokens); j++ {
		switch {
		case tokens[j].isPunct("("):
			depth++
		case tokens[j].isPunct(")"):
			depth--
		case depth == 0 && tokens[j].isPunct("="):
			eq = j
		}
		if eq >= 0 {
			break
		}
	}
	if eq < 0 {
		return bqQueryErr("Syntax error: Expected = in SET")
	}
	var names []string
	if tokens[i].isPunct("(") {
		for _, n := range bqSplitArgs(tokens[i+1 : bqMatchParen(tokens, i)]) {
			if len(n) != 1 || !n[0].isIdent() {
				return bqQueryErr("Syntax error: Expected variable names in SET (...)")
			}
			names = append(names, n[0].text)
		}
	} else if tokens[i].isIdent() {
		names = []string{tokens[i].text}
	}
	if tokens[i].isPunct("@") {
		return bqQueryErr("The simulator does not support system variables")
	}
	for _, name := range names {
		if _, ok := vars[strings.ToLower(name)]; !ok {
			return bqQueryErr("Undeclared variable: %s", name)
		}
	}
	expr := bqTrimSpace(tokens[eq+1:])
	if len(names) > 1 {
		if len(expr) < 2 || !expr[0].isPunct("(") || bqMatchParen(expr, 0) != len(expr)-1 {
			return bqQueryErr("The simulator supports SET (a, b) = (expr, expr) with one expression per variable")
		}
		parts := bqSplitArgs(expr[1 : len(expr)-1])
		if len(parts) != len(names) {
			return bqQueryErr("SET assigns %d variables from %d expressions", len(names), len(parts))
		}
		for n, name := range names {
			if err := sr.assign(name, parts[n], vars, st); err != nil {
				return err
			}
		}
		return nil
	}
	return sr.assign(names[0], expr, vars, st)
}

func (sr *bqScriptRunner) assign(name string, expr []bqSQLToken, vars map[string]*bqVariable, st bqScriptStatement) error {
	v, inferred, err := sr.evaluate(expr, vars, st)
	if err != nil {
		return err
	}
	variable := vars[strings.ToLower(name)]
	if v, err = bqCoerceVariable(variable.typ, inferred, v); err != nil {
		return err
	}
	variable.value = v
	return nil
}

// runIf reads IF cond THEN ... [ELSEIF cond THEN ...] [ELSE ...] END IF.
func (sr *bqScriptRunner) runIf(st bqScriptStatement, vars map[string]*bqVariable) error {
	tokens := bqTrimSpace(st.tokens)
	// Branches: each starts at IF or ELSEIF (with a condition up to THEN) or
	// at ELSE, and runs to the next branch keyword or END IF at depth 0.
	type branch struct {
		condition []bqSQLToken
		body      []bqSQLToken
	}
	var branches []branch
	depth, blocks := 0, 0
	i := 0
	for i < len(tokens) {
		t := tokens[i]
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		}
		if depth == 0 && blocks == 0 && (t.isWord("IF") && i == 0 || t.isWord("ELSEIF") || t.isWord("ELSE")) {
			var condition []bqSQLToken
			start := i + 1
			if !t.isWord("ELSE") {
				then := -1
				d := 0
				for j := start; j < len(tokens); j++ {
					switch {
					case tokens[j].isPunct("("):
						d++
					case tokens[j].isPunct(")"):
						d--
					case d == 0 && tokens[j].isWord("THEN"):
						then = j
					}
					if then >= 0 {
						break
					}
				}
				if then < 0 {
					return bqQueryErr("Syntax error: Expected THEN in IF")
				}
				condition = bqTrimSpace(tokens[start:then])
				start = then + 1
			}
			end := len(tokens)
			d, b := 0, 0
			for j := start; j < len(tokens); j++ {
				tj := tokens[j]
				switch {
				case tj.isPunct("("):
					d++
				case tj.isPunct(")"):
					d--
				case d == 0 && (tj.isWord("IF") || tj.isWord("BEGIN") || tj.isWord("WHILE") || tj.isWord("LOOP")) && bqStartsStatement(tokens, start, j) && !bqTransactionWord(tokens, j):
					b++
				case d == 0 && tj.isWord("END") && b > 0:
					b--
					if k := bqSignificant(tokens, j+1); k < len(tokens) && (tokens[k].isWord("IF") || tokens[k].isWord("WHILE") || tokens[k].isWord("LOOP")) {
						j = k
					}
				case d == 0 && b == 0 && (tj.isWord("ELSEIF") || tj.isWord("ELSE") || tj.isWord("END")):
					end = j
					j = len(tokens)
				}
			}
			branches = append(branches, branch{condition: condition, body: tokens[start:end]})
			i = end
			continue
		}
		i++
	}
	for _, br := range branches {
		run := br.condition == nil
		if !run {
			v, _, err := sr.evaluate(br.condition, vars, st)
			if err != nil {
				return err
			}
			run = v == "true"
		}
		if !run {
			continue
		}
		inner, err := bqSplitScript(bqSQLText(br.body))
		if err != nil {
			return err
		}
		return sr.runBlock(sr.shiftStatements(inner, st), vars, false)
	}
	return nil
}

func (sr *bqScriptRunner) runStatement(st bqScriptStatement, vars map[string]*bqVariable) error {
	substituted, err := sr.substitute(st.tokens, vars)
	if err != nil {
		return err
	}
	text := bqSQLText(substituted)
	scope := bqChildScope(sr.scope)
	result, err := bqRunStatement(sr.ctx, sr.r, scope, text, sr.dryRun)
	sr.children++
	if sr.child != nil {
		if cerr := sr.child(text, sr.frame(st, st.text), "STATEMENT", result, err); cerr != nil {
			return cerr
		}
	}
	if err != nil {
		return err
	}
	sr.last = result
	return nil
}

// bqLiteralTokens spells a stored-form value of type f as a literal the
// engine reads back to the same value.
func bqLiteralTokens(f BQFieldSchema, value any) ([]bqSQLToken, error) {
	if value == nil {
		return []bqSQLToken{bqWordToken("NULL")}, nil
	}
	if bqNested(f) {
		raw, err := bqSQLiteValue(f, value)
		if err != nil {
			return nil, err
		}
		text, ok := raw.(string)
		if !ok {
			return nil, bqQueryErr("The simulator cannot carry a %s value between statements", bqSQLTypeName(f))
		}
		return []bqSQLToken{bqWordToken("json"), bqPunctToken("("), bqStringToken(text), bqPunctToken(")")}, nil
	}
	text := bqScalarText(value)
	switch bqType(f) {
	case "INT64":
		return []bqSQLToken{{text: text, kind: bqTokNumber, fixed: true}}, nil
	case "FLOAT64":
		if _, err := strconv.ParseFloat(text, 64); err != nil || strings.ContainsAny(text, "NI") {
			return []bqSQLToken{bqWordToken("bq_cast"), bqPunctToken("("), bqStringToken(text), bqPunctToken(","), bqStringToken("FLOAT64"), bqPunctToken(","), bqStringToken("STRING"), bqPunctToken(")")}, nil
		}
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return []bqSQLToken{{text: text, kind: bqTokNumber, fixed: true}}, nil
	case "BOOL":
		return []bqSQLToken{bqWordToken(strings.ToUpper(text))}, nil
	case "TIMESTAMP":
		return []bqSQLToken{bqWordToken("bq_timestamp_micros"), bqPunctToken("("), {text: text, kind: bqTokNumber, fixed: true}, bqPunctToken(")")}, nil
	case "DATE", "DATETIME", "TIME", "STRING", "BYTES", "GEOGRAPHY", "JSON":
		return []bqSQLToken{bqStringToken(text)}, nil
	case "NUMERIC", "BIGNUMERIC":
		return []bqSQLToken{bqWordToken("bq_cast"), bqPunctToken("("), bqStringToken(text), bqPunctToken(","), bqStringToken(bqType(f)), bqPunctToken(","), bqStringToken("STRING"), bqPunctToken(")")}, nil
	}
	return nil, bqQueryErr("The simulator cannot carry a %s value between statements", bqSQLTypeName(f))
}

// bqParameterTokens spells a query parameter as a literal.
func bqParameterTokens(param bqQueryParameter) ([]bqSQLToken, error) {
	f, err := bqParameterField(param.ParameterType)
	if err != nil {
		return nil, err
	}
	value, err := bqParamToStored(f, param.ParameterValue)
	if err != nil {
		return nil, err
	}
	return bqLiteralTokens(f, value)
}

// bqQueryParameter is the Discovery's QueryParameter.
type bqQueryParameter struct {
	Name           string            `json:"name"`
	ParameterType  *bqParameterType  `json:"parameterType"`
	ParameterValue *bqParameterValue `json:"parameterValue"`
}

type bqParameterType struct {
	Type        string                   `json:"type"`
	ArrayType   *bqParameterType         `json:"arrayType"`
	StructTypes []bqParameterStructField `json:"structTypes"`
}

type bqParameterStructField struct {
	Name string           `json:"name"`
	Type *bqParameterType `json:"type"`
}

type bqParameterValue struct {
	Value        *string                     `json:"value"`
	ArrayValues  []bqParameterValue          `json:"arrayValues"`
	StructValues map[string]bqParameterValue `json:"structValues"`
}

func bqParameterField(t *bqParameterType) (BQFieldSchema, error) {
	if t == nil || t.Type == "" {
		return BQFieldSchema{}, bqQueryErr("Parameter type is required")
	}
	switch strings.ToUpper(t.Type) {
	case "ARRAY":
		elem, err := bqParameterField(t.ArrayType)
		if err != nil {
			return BQFieldSchema{}, err
		}
		if bqRepeated(elem) {
			return BQFieldSchema{}, bqQueryErr("Array of arrays is not supported")
		}
		elem.Mode = "REPEATED"
		return elem, nil
	case "STRUCT":
		f := BQFieldSchema{Type: "RECORD", Mode: "NULLABLE"}
		for _, st := range t.StructTypes {
			sub, err := bqParameterField(st.Type)
			if err != nil {
				return BQFieldSchema{}, err
			}
			sub.Name = st.Name
			f.Fields = append(f.Fields, sub)
		}
		return f, nil
	}
	apiType, ok := bqSQLTypeNames[strings.ToUpper(t.Type)]
	if !ok {
		return BQFieldSchema{}, bqQueryErr("Invalid parameter type: %s", t.Type)
	}
	return BQFieldSchema{Type: apiType, Mode: "NULLABLE"}, nil
}

// bqParamToStored converts a parameter's value to the stored form of f.
func bqParamToStored(f BQFieldSchema, v *bqParameterValue) (any, error) {
	if v == nil {
		return nil, nil
	}
	if bqRepeated(f) {
		elem := f
		elem.Mode = "NULLABLE"
		out := make([]any, 0, len(v.ArrayValues))
		for i := range v.ArrayValues {
			item, err := bqParamToStored(elem, &v.ArrayValues[i])
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	}
	if bqType(f) == "STRUCT" {
		if v.StructValues == nil {
			return nil, nil
		}
		out := make(map[string]any, len(f.Fields))
		for _, sub := range f.Fields {
			sv, ok := v.StructValues[sub.Name]
			if !ok {
				out[sub.Name] = nil
				continue
			}
			item, err := bqParamToStored(sub, &sv)
			if err != nil {
				return nil, err
			}
			out[sub.Name] = item
		}
		return out, nil
	}
	if v.Value == nil {
		return nil, nil
	}
	stored, err := bqCanonical(bqType(f), *v.Value)
	if err != nil {
		return nil, bqQueryErr("Invalid %s parameter value: %s", bqType(f), *v.Value)
	}
	return stored, nil
}
