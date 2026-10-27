package main

// PartiQL statements translate onto the item engine in dynamodb.go: WHERE
// clauses render to ConditionExpression form for ddbEvalExpr, and UPDATE to an
// UpdateExpression, so a statement's result equals the equivalent classic API
// call.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

func registerDDBPartiQL(r *AWSRouter) {
	reg := func(target string, h http.HandlerFunc) {
		op := strings.TrimPrefix(target, "DynamoDB_20120810.")
		r.Register(target, ddbRequire(ddbRequiredMembers[op], h))
	}
	reg("DynamoDB_20120810.ExecuteStatement", handleDDBExecuteStatement)
	reg("DynamoDB_20120810.BatchExecuteStatement", handleDDBBatchExecuteStatement)
	reg("DynamoDB_20120810.ExecuteTransaction", handleDDBExecuteTransaction)
}

// A Go stack overflow is fatal and cannot be recovered, so bound nesting
// depth; bound total work so a huge flat statement cannot hang.
const (
	partiQLMaxDepth = 500
	partiQLMaxNodes = 200000
)

type partiQLKind int

const (
	pqlSelect partiQLKind = iota
	pqlInsert
	pqlUpdate
	pqlDelete
)

// Only the fields of its Kind are set.
type partiQLStmt struct {
	Kind  partiQLKind
	Table string
	Index string // SELECT ... FROM "table"."index"

	SelectAll  bool
	SelectCols []string // top-level attribute names (resolved literals)
	Where      partiQLExpr
	OrderByKey string
	OrderDesc  bool

	InsertValue map[string]any // attribute-value map (wire shape)

	Sets    []partiQLSet
	Removes []string // top-level attribute names to remove

	Returning string // "ALLOLD", "ALLNEW", "MODIFIEDOLD", "MODIFIEDNEW" or "".
}

type partiQLSet struct {
	Path  string // top-level attribute name
	Value partiQLTermVal
}

// The executor builds an UpdateExpression values map from these.
type partiQLTermVal struct {
	AV map[string]any // attribute-value wire shape, e.g. {"S":"x"} / {"N":"1"}
}

// The WHERE tree holds exactly the operators DynamoDB PartiQL supports and
// renders to a condition string for ddbEvalExpr, allocating #n / :v
// placeholders into shared maps.

type partiQLExpr interface {
	render(*partiQLRenderCtx) string
}

type partiQLRenderCtx struct {
	names  map[string]string // "#n0" -> attribute name
	values map[string]any    // ":v0" -> attribute value
	nN, nV int
}

func (c *partiQLRenderCtx) name(attr string) string {
	ph := fmt.Sprintf("#n%d", c.nN)
	c.nN++
	c.names[ph] = attr
	return ph
}

func (c *partiQLRenderCtx) value(av map[string]any) string {
	ph := fmt.Sprintf(":v%d", c.nV)
	c.nV++
	c.values[ph] = av
	return ph
}

type partiQLAnd struct{ l, r partiQLExpr }

func (e partiQLAnd) render(c *partiQLRenderCtx) string {
	return "(" + e.l.render(c) + " AND " + e.r.render(c) + ")"
}

type partiQLOr struct{ l, r partiQLExpr }

func (e partiQLOr) render(c *partiQLRenderCtx) string {
	return "(" + e.l.render(c) + " OR " + e.r.render(c) + ")"
}

type partiQLNot struct{ inner partiQLExpr }

func (e partiQLNot) render(c *partiQLRenderCtx) string {
	return "(NOT " + e.inner.render(c) + ")"
}

type partiQLCompare struct {
	col string
	op  string
	val map[string]any
}

func (e partiQLCompare) render(c *partiQLRenderCtx) string {
	return c.name(e.col) + " " + e.op + " " + c.value(e.val)
}

type partiQLBetween struct {
	col    string
	lo, hi map[string]any
}

func (e partiQLBetween) render(c *partiQLRenderCtx) string {
	return c.name(e.col) + " BETWEEN " + c.value(e.lo) + " AND " + c.value(e.hi)
}

type partiQLIn struct {
	col  string
	vals []map[string]any
}

func (e partiQLIn) render(c *partiQLRenderCtx) string {
	parts := make([]string, len(e.vals))
	for i, v := range e.vals {
		parts[i] = c.value(v)
	}
	return c.name(e.col) + " IN (" + strings.Join(parts, ", ") + ")"
}

// IS [NOT] MISSING renders as attribute_not_exists / attribute_exists.
type partiQLFunc struct {
	name string // attribute_exists / attribute_not_exists / begins_with / contains / attribute_type
	col  string
	arg  *map[string]any // second argument for begins_with/contains/attribute_type
}

func (e partiQLFunc) render(c *partiQLRenderCtx) string {
	n := c.name(e.col)
	if e.arg == nil {
		return e.name + "(" + n + ")"
	}
	return e.name + "(" + n + ", " + c.value(*e.arg) + ")"
}

type pqlTokKind int

const (
	pqlEOF         pqlTokKind = iota
	pqlIdent                  // bare identifier / keyword
	pqlQuotedIdent            // "double-quoted identifier"
	pqlString                 // 'single-quoted string literal'
	pqlNumber                 // numeric literal
	pqlParam                  // ?
	pqlOp                     // = <> < <= > >=
	pqlLParen
	pqlRParen
	pqlLBrace
	pqlRBrace
	pqlLBracket
	pqlRBracket
	pqlComma
	pqlColon // map literal key:value separator
	pqlDot   // table.index separator
	pqlStar  // '*' (SELECT projection)
)

type pqlTok struct {
	kind pqlTokKind
	text string
}

// sim.Scanner keeps indexing safe on any input. An unterminated string runs to
// EOF and the parser fails on the shape.
func pqlTokenize(s string) []pqlTok {
	var toks []pqlTok
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			sc.Next()
		case c == '(':
			sc.Next()
			toks = append(toks, pqlTok{pqlLParen, "("})
		case c == ')':
			sc.Next()
			toks = append(toks, pqlTok{pqlRParen, ")"})
		case c == '{':
			sc.Next()
			toks = append(toks, pqlTok{pqlLBrace, "{"})
		case c == '}':
			sc.Next()
			toks = append(toks, pqlTok{pqlRBrace, "}"})
		case c == '[':
			sc.Next()
			toks = append(toks, pqlTok{pqlLBracket, "["})
		case c == ']':
			sc.Next()
			toks = append(toks, pqlTok{pqlRBracket, "]"})
		case c == ',':
			sc.Next()
			toks = append(toks, pqlTok{pqlComma, ","})
		case c == ':':
			sc.Next()
			toks = append(toks, pqlTok{pqlColon, ":"})
		case c == '.':
			sc.Next()
			toks = append(toks, pqlTok{pqlDot, "."})
		case c == '?':
			sc.Next()
			toks = append(toks, pqlTok{pqlParam, "?"})
		case c == '*':
			sc.Next()
			toks = append(toks, pqlTok{pqlStar, "*"})
		case c == '=':
			sc.Next()
			toks = append(toks, pqlTok{pqlOp, "="})
		case c == '<':
			sc.Next()
			switch sc.Peek() {
			case '=':
				sc.Next()
				toks = append(toks, pqlTok{pqlOp, "<="})
			case '>':
				sc.Next()
				toks = append(toks, pqlTok{pqlOp, "<>"})
			default:
				toks = append(toks, pqlTok{pqlOp, "<"})
			}
		case c == '>':
			sc.Next()
			if sc.Peek() == '=' {
				sc.Next()
				toks = append(toks, pqlTok{pqlOp, ">="})
			} else {
				toks = append(toks, pqlTok{pqlOp, ">"})
			}
		case c == '\'':
			toks = append(toks, pqlTok{pqlString, pqlScanQuoted(sc, '\'')})
		case c == '"':
			toks = append(toks, pqlTok{pqlQuotedIdent, pqlScanQuoted(sc, '"')})
		case c == '-' || c == '+' || (c >= '0' && c <= '9'):
			toks = append(toks, pqlTok{pqlNumber, pqlScanNumber(sc)})
		default:
			if pqlIsIdentStart(c) {
				toks = append(toks, pqlTok{pqlIdent, pqlScanIdent(sc)})
			} else {
				// Consume an unknown byte so the cursor advances; the parser fails on the
				// result if it mattered.
				sc.Next()
			}
		}
		if len(toks) > partiQLMaxNodes {
			break
		}
	}
	return append(toks, pqlTok{pqlEOF, ""})
}

// PartiQL escapes the delimiter by doubling it.
func pqlScanQuoted(sc *sim.Scanner, q byte) string {
	sc.Next()
	var b strings.Builder
	for !sc.Eof() {
		ch := sc.Next()
		if ch == q {
			if sc.Peek() == q {
				sc.Next()
				b.WriteByte(q)
				continue
			}
			break
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// Keep the exact text for the {"N": …} value.
func pqlScanNumber(sc *sim.Scanner) string {
	start := sc.Pos()
	if c := sc.Peek(); c == '-' || c == '+' {
		sc.Next()
	}
	for !sc.Eof() {
		c := sc.Peek()
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			sc.Next()
			continue
		}
		break
	}
	return sc.Slice(start, sc.Pos())
}

func pqlIsIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func pqlIsIdentChar(c byte) bool {
	return pqlIsIdentStart(c) || (c >= '0' && c <= '9')
}

func pqlScanIdent(sc *sim.Scanner) string {
	start := sc.Pos()
	for !sc.Eof() && pqlIsIdentChar(sc.Peek()) {
		sc.Next()
	}
	return sc.Slice(start, sc.Pos())
}

type pqlParser struct {
	toks  []pqlTok
	pos   int
	guard *sim.ParseGuard
	// next indexes the next ? to bind.
	params []map[string]any
	pnext  int
	err    error
}

func (p *pqlParser) fail(format string, a ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, a...)
	}
}

func (p *pqlParser) peek() pqlTok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return pqlTok{pqlEOF, ""}
}

func (p *pqlParser) next() pqlTok {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}

// Quoted identifiers never match keywords.
func (p *pqlParser) kwEq(kw string) bool {
	t := p.peek()
	return t.kind == pqlIdent && strings.EqualFold(t.text, kw)
}

func (p *pqlParser) eatKw(kw string) bool {
	if p.kwEq(kw) {
		p.next()
		return true
	}
	return false
}

// Binds ? parameters positionally. ParseGuard and sim.Scanner keep it from
// panicking.
func parsePartiQL(statement string, params []map[string]any) (*partiQLStmt, error) {
	p := &pqlParser{
		toks:   pqlTokenize(statement),
		guard:  sim.NewParseGuard(partiQLMaxDepth, partiQLMaxNodes),
		params: params,
	}
	stmt := p.parseStatement()
	if p.err != nil {
		return nil, p.err
	}
	if stmt == nil {
		return nil, fmt.Errorf("could not parse PartiQL statement")
	}
	if p.peek().kind != pqlEOF {
		return nil, fmt.Errorf("unexpected trailing tokens in PartiQL statement")
	}
	return stmt, nil
}

func (p *pqlParser) parseStatement() *partiQLStmt {
	switch {
	case p.kwEq("SELECT"):
		return p.parseSelect()
	case p.kwEq("INSERT"):
		return p.parseInsert()
	case p.kwEq("UPDATE"):
		return p.parseUpdate()
	case p.kwEq("DELETE"):
		return p.parseDelete()
	}
	p.fail("unsupported PartiQL statement (expected SELECT/INSERT/UPDATE/DELETE)")
	return nil
}

// An optional `."index"` selects a secondary index.
func (p *pqlParser) parseTableRef() (table, index string) {
	t := p.next()
	switch t.kind {
	case pqlQuotedIdent, pqlIdent:
		table = t.text
	default:
		p.fail("expected table name")
		return "", ""
	}
	if p.peek().kind == pqlDot {
		p.next()
		it := p.next()
		switch it.kind {
		case pqlQuotedIdent, pqlIdent:
			index = it.text
		default:
			p.fail("expected index name after '.'")
		}
	}
	return table, index
}

func (p *pqlParser) parseSelect() *partiQLStmt {
	p.next()
	st := &partiQLStmt{Kind: pqlSelect}
	if p.peekIsStar() {
		p.next()
		st.SelectAll = true
	} else {
		for {
			c := p.next()
			if c.kind != pqlIdent && c.kind != pqlQuotedIdent {
				p.fail("expected column name in SELECT list")
				return st
			}
			st.SelectCols = append(st.SelectCols, c.text)
			if p.peek().kind == pqlComma {
				p.next()
				continue
			}
			break
		}
	}
	if !p.eatKw("FROM") {
		p.fail("expected FROM in SELECT")
		return st
	}
	st.Table, st.Index = p.parseTableRef()
	if p.eatKw("WHERE") {
		st.Where = p.parseExpr()
	}
	if p.eatKw("ORDER") {
		if !p.eatKw("BY") {
			p.fail("expected BY after ORDER")
			return st
		}
		k := p.next()
		if k.kind != pqlIdent && k.kind != pqlQuotedIdent {
			p.fail("expected key in ORDER BY")
			return st
		}
		st.OrderByKey = k.text
		switch {
		case p.eatKw("DESC"):
			st.OrderDesc = true
		case p.eatKw("ASC"):
			st.OrderDesc = false
		}
	}
	return st
}

func (p *pqlParser) peekIsStar() bool {
	return p.peek().kind == pqlStar
}

func (p *pqlParser) parseInsert() *partiQLStmt {
	p.next()
	if !p.eatKw("INTO") {
		p.fail("expected INTO after INSERT")
		return nil
	}
	st := &partiQLStmt{Kind: pqlInsert}
	st.Table, st.Index = p.parseTableRef()
	if st.Index != "" {
		p.fail("cannot INSERT into a secondary index")
		return st
	}
	if !p.eatKw("VALUE") {
		p.fail("expected VALUE after INSERT INTO <table>")
		return st
	}
	val := p.parseValue()
	m, ok := val.(map[string]any)
	if !ok {
		p.fail("INSERT VALUE must be a map literal")
		return st
	}
	st.InsertValue = m
	return st
}

func (p *pqlParser) parseUpdate() *partiQLStmt {
	p.next()
	st := &partiQLStmt{Kind: pqlUpdate}
	st.Table, st.Index = p.parseTableRef()
	if st.Index != "" {
		p.fail("cannot UPDATE a secondary index")
		return st
	}
	// One or more SET / REMOVE clauses.
	sawClause := false
	for {
		switch {
		case p.eatKw("SET"):
			sawClause = true
			for {
				col := p.next()
				if col.kind != pqlIdent && col.kind != pqlQuotedIdent {
					p.fail("expected attribute in SET")
					return st
				}
				if p.peek().kind != pqlOp || p.peek().text != "=" {
					p.fail("expected '=' in SET assignment")
					return st
				}
				p.next()
				av, ok := p.parseScalarAV()
				if !ok {
					p.fail("expected value in SET assignment")
					return st
				}
				st.Sets = append(st.Sets, partiQLSet{Path: col.text, Value: partiQLTermVal{AV: av}})
				if p.peek().kind == pqlComma {
					p.next()
					continue
				}
				break
			}
		case p.eatKw("REMOVE"):
			sawClause = true
			for {
				col := p.next()
				if col.kind != pqlIdent && col.kind != pqlQuotedIdent {
					p.fail("expected attribute in REMOVE")
					return st
				}
				st.Removes = append(st.Removes, col.text)
				if p.peek().kind == pqlComma {
					p.next()
					continue
				}
				break
			}
		default:
			goto doneClauses
		}
	}
doneClauses:
	if !sawClause {
		p.fail("UPDATE requires at least one SET or REMOVE")
		return st
	}
	if !p.eatKw("WHERE") {
		p.fail("UPDATE requires a WHERE clause")
		return st
	}
	st.Where = p.parseExpr()
	st.Returning = p.parseReturning()
	return st
}

func (p *pqlParser) parseDelete() *partiQLStmt {
	p.next()
	if !p.eatKw("FROM") {
		p.fail("expected FROM after DELETE")
		return nil
	}
	st := &partiQLStmt{Kind: pqlDelete}
	st.Table, st.Index = p.parseTableRef()
	if st.Index != "" {
		p.fail("cannot DELETE from a secondary index")
		return st
	}
	if !p.eatKw("WHERE") {
		p.fail("DELETE requires a WHERE clause")
		return st
	}
	st.Where = p.parseExpr()
	st.Returning = p.parseReturning()
	return st
}

// Returns "ALLOLD", "ALLNEW", "MODIFIEDOLD", "MODIFIEDNEW", or "" when absent.
func (p *pqlParser) parseReturning() string {
	if !p.eatKw("RETURNING") {
		return ""
	}
	var scope, when string
	switch {
	case p.eatKw("ALL"):
		scope = "ALL"
	case p.eatKw("MODIFIED"):
		scope = "MODIFIED"
	default:
		p.fail("expected ALL or MODIFIED in RETURNING")
		return ""
	}
	switch {
	case p.eatKw("OLD"):
		when = "OLD"
	case p.eatKw("NEW"):
		when = "NEW"
	default:
		p.fail("expected OLD or NEW in RETURNING")
		return ""
	}
	if p.peekIsStar() {
		p.next()
	}
	return scope + when
}

func (p *pqlParser) parseExpr() partiQLExpr { return p.parseOr() }

func (p *pqlParser) parseOr() partiQLExpr {
	left := p.parseAnd()
	for p.kwEq("OR") {
		p.next()
		left = partiQLOr{left, p.parseAnd()}
	}
	return left
}

func (p *pqlParser) parseAnd() partiQLExpr {
	left := p.parseNot()
	for p.kwEq("AND") {
		p.next()
		left = partiQLAnd{left, p.parseNot()}
	}
	return left
}

func (p *pqlParser) parseNot() partiQLExpr {
	if p.eatKw("NOT") {
		return partiQLNot{p.parseNot()}
	}
	return p.parsePrimary()
}

func (p *pqlParser) parsePrimary() partiQLExpr {
	if !p.guard.Enter() {
		p.fail("PartiQL expression too deep")
		return nil
	}
	defer p.guard.Leave()

	if p.peek().kind == pqlLParen {
		p.next()
		inner := p.parseExpr()
		if p.peek().kind == pqlRParen {
			p.next()
		} else {
			p.fail("missing ')' in WHERE")
		}
		return inner
	}

	// Boolean functions: begins_with(col, v) / contains(col, v) /
	// attribute_exists(col) / attribute_type(col, v).
	if p.peek().kind == pqlIdent {
		switch strings.ToLower(p.peek().text) {
		case "begins_with", "contains", "attribute_type":
			fn := strings.ToLower(p.next().text)
			col, arg := p.parseFuncColArg()
			return partiQLFunc{name: fn, col: col, arg: arg}
		case "attribute_exists", "attribute_not_exists":
			fn := strings.ToLower(p.next().text)
			col := p.parseFuncCol()
			return partiQLFunc{name: fn, col: col}
		}
	}

	colTok := p.next()
	if colTok.kind != pqlIdent && colTok.kind != pqlQuotedIdent {
		p.fail("expected attribute name in WHERE predicate")
		return nil
	}
	col := colTok.text

	switch {
	case p.peek().kind == pqlOp:
		op := p.next().text
		av, ok := p.parseScalarAV()
		if !ok {
			p.fail("expected value after comparison operator")
			return nil
		}
		return partiQLCompare{col: col, op: op, val: av}
	case p.kwEq("BETWEEN"):
		p.next()
		lo, ok1 := p.parseScalarAV()
		if !p.eatKw("AND") {
			p.fail("expected AND in BETWEEN")
			return nil
		}
		hi, ok2 := p.parseScalarAV()
		if !ok1 || !ok2 {
			p.fail("expected bounds in BETWEEN")
			return nil
		}
		return partiQLBetween{col: col, lo: lo, hi: hi}
	case p.kwEq("IN"):
		p.next()
		// AWS documents the IN list in brackets ("WHERE OrderID IN [100, 300, 234]")
		// and accepts parentheses too; the closing delimiter must match the opening
		// one.
		var closing pqlTokKind
		switch p.peek().kind {
		case pqlLBracket:
			closing = pqlRBracket
		case pqlLParen:
			closing = pqlRParen
		default:
			p.fail("expected '[' or '(' after IN")
			return nil
		}
		p.next()
		var vals []map[string]any
		for p.peek().kind != closing && p.peek().kind != pqlEOF {
			av, ok := p.parseScalarAV()
			if !ok {
				p.fail("expected value in IN list")
				return nil
			}
			vals = append(vals, av)
			if p.peek().kind == pqlComma {
				p.next()
			}
		}
		if p.peek().kind == closing {
			p.next()
		} else if closing == pqlRBracket {
			p.fail("missing ']' in IN list")
		} else {
			p.fail("missing ')' in IN list")
		}
		return partiQLIn{col: col, vals: vals}
	case p.kwEq("IS"):
		p.next()
		negated := p.eatKw("NOT")
		if !p.eatKw("MISSING") {
			p.fail("expected MISSING after IS [NOT]")
			return nil
		}
		// IS MISSING  → attribute_not_exists(col)
		// IS NOT MISSING → attribute_exists(col)
		if negated {
			return partiQLFunc{name: "attribute_exists", col: col}
		}
		return partiQLFunc{name: "attribute_not_exists", col: col}
	}
	p.fail("unsupported WHERE predicate for attribute %q", col)
	return nil
}

func (p *pqlParser) parseFuncCol() string {
	if p.peek().kind != pqlLParen {
		p.fail("expected '(' after function")
		return ""
	}
	p.next()
	col := p.next()
	if col.kind != pqlIdent && col.kind != pqlQuotedIdent {
		p.fail("expected attribute in function call")
	}
	if p.peek().kind == pqlRParen {
		p.next()
	} else {
		p.fail("missing ')' in function call")
	}
	return col.text
}

func (p *pqlParser) parseFuncColArg() (string, *map[string]any) {
	if p.peek().kind != pqlLParen {
		p.fail("expected '(' after function")
		return "", nil
	}
	p.next()
	col := p.next()
	if col.kind != pqlIdent && col.kind != pqlQuotedIdent {
		p.fail("expected attribute in function call")
		return "", nil
	}
	if p.peek().kind != pqlComma {
		p.fail("expected ',' in function call")
		return col.text, nil
	}
	p.next()
	av, ok := p.parseScalarAV()
	if p.peek().kind == pqlRParen {
		p.next()
	} else {
		p.fail("missing ')' in function call")
	}
	if !ok {
		return col.text, nil
	}
	return col.text, &av
}

func (p *pqlParser) parseScalarAV() (map[string]any, bool) {
	v := p.parseValue()
	if v == nil {
		return nil, false
	}
	av, ok := v.(map[string]any)
	return av, ok
}

// Returns the AttributeValue wire shape.
func (p *pqlParser) parseValue() any {
	if !p.guard.Enter() {
		p.fail("PartiQL value too deep")
		return nil
	}
	defer p.guard.Leave()

	t := p.peek()
	switch t.kind {
	case pqlParam:
		p.next()
		if p.pnext >= len(p.params) {
			p.fail("not enough Parameters for ? placeholders")
			return nil
		}
		av := p.params[p.pnext]
		p.pnext++
		return av
	case pqlString:
		p.next()
		return map[string]any{"S": t.text}
	case pqlNumber:
		p.next()
		return map[string]any{"N": t.text}
	case pqlIdent:
		switch strings.ToLower(t.text) {
		case "true":
			p.next()
			return map[string]any{"BOOL": true}
		case "false":
			p.next()
			return map[string]any{"BOOL": false}
		case "null":
			p.next()
			return map[string]any{"NULL": true}
		}
		p.fail("unexpected identifier %q where a value was expected", t.text)
		return nil
	case pqlLBrace:
		return p.parseMapLiteral()
	case pqlLBracket:
		return p.parseListLiteral()
	}
	p.fail("expected a value")
	return nil
}

func (p *pqlParser) parseMapLiteral() any {
	p.next()
	m := map[string]any{}
	for p.peek().kind != pqlRBrace && p.peek().kind != pqlEOF {
		keyTok := p.next()
		var key string
		switch keyTok.kind {
		case pqlString, pqlQuotedIdent, pqlIdent:
			key = keyTok.text
		default:
			p.fail("expected key in map literal")
			return nil
		}
		if p.peek().kind != pqlColon {
			p.fail("expected ':' in map literal")
			return nil
		}
		p.next()
		v := p.parseValue()
		if v == nil {
			return nil
		}
		m[key] = v
		if p.peek().kind == pqlComma {
			p.next()
		}
	}
	if p.peek().kind == pqlRBrace {
		p.next()
	} else {
		p.fail("missing '}' in map literal")
		return nil
	}
	return map[string]any{"M": m}
}

func (p *pqlParser) parseListLiteral() any {
	p.next()
	var lst []any
	for p.peek().kind != pqlRBracket && p.peek().kind != pqlEOF {
		v := p.parseValue()
		if v == nil {
			return nil
		}
		lst = append(lst, v)
		if p.peek().kind == pqlComma {
			p.next()
		}
	}
	if p.peek().kind == pqlRBracket {
		p.next()
	} else {
		p.fail("missing ']' in list literal")
		return nil
	}
	if lst == nil {
		lst = []any{}
	}
	return map[string]any{"L": lst}
}

// Items is set for SELECT; Item for a write's RETURNING and a batch read.
type pqlResult struct {
	Items            []map[string]any
	Item             map[string]any
	NextToken        string
	LastEvaluatedKey map[string]any
	hasLastKey       bool
}

type pqlError struct {
	Code    string // ValidationException / ResourceNotFoundException / DuplicateItemException / ConditionalCheckFailedException
	Message string
	Item    map[string]any // for ConditionalCheckFailed with ReturnValues=ALL_OLD
}

func pqlErrf(code, format string, a ...any) *pqlError {
	return &pqlError{Code: code, Message: fmt.Sprintf(format, a...)}
}

// The caller holds the statement's table stripe for read-modify-write
// atomicity. limit and nextToken apply to SELECT only.
func executePartiQL(st *partiQLStmt, limit int, nextToken string) (*pqlResult, *pqlError) {
	t, ok := ddbTables.Get(st.Table)
	if !ok {
		return nil, pqlErrf("ResourceNotFoundException",
			"Requested resource not found: Table: %s not found", st.Table)
	}
	switch st.Kind {
	case pqlSelect:
		return pqlExecSelect(t, st, limit, nextToken)
	case pqlInsert:
		return pqlExecInsert(t, st)
	case pqlUpdate:
		return pqlExecUpdate(t, st)
	case pqlDelete:
		return pqlExecDelete(t, st)
	}
	return nil, pqlErrf("ValidationException", "unsupported statement")
}

func pqlWhereToExpr(where partiQLExpr) (string, map[string]string, map[string]any) {
	if where == nil {
		return "", nil, nil
	}
	ctx := &partiQLRenderCtx{names: map[string]string{}, values: map[string]any{}}
	expr := where.render(ctx)
	return expr, ctx.names, ctx.values
}

// Only a pure conjunction of `col = literal` can resolve to a point read or a
// partition query; anything else returns ok=false.
func pqlKeyEqualities(where partiQLExpr) (map[string]map[string]any, bool) {
	out := map[string]map[string]any{}
	var walk func(e partiQLExpr) bool
	walk = func(e partiQLExpr) bool {
		switch n := e.(type) {
		case partiQLAnd:
			return walk(n.l) && walk(n.r)
		case partiQLCompare:
			if n.op != "=" {
				return false
			}
			out[n.col] = n.val
			return true
		default:
			return false
		}
	}
	if where == nil || !walk(where) {
		return nil, false
	}
	return out, true
}

// A full primary key by equality is a point read; anything else scans with the
// predicate as filter, then applies ORDER BY and pagination.
func pqlExecSelect(t DDBTable, st *partiQLStmt, limit int, nextToken string) (*pqlResult, *pqlError) {
	if st.Index != "" && !ddbHasIndex(t, st.Index) {
		return nil, pqlErrf("ValidationException", "The table does not have the specified index: %s", st.Index)
	}
	expr, names, values := pqlWhereToExpr(st.Where)

	// Only the base table's KeySchema is the primary key.
	if st.Index == "" {
		if eqs, ok := pqlKeyEqualities(st.Where); ok && pqlCoversKey(t, eqs) {
			key := map[string]any{}
			for _, ks := range t.KeySchema {
				key[ks.AttributeName] = eqs[ks.AttributeName]
			}
			res := &pqlResult{Items: []map[string]any{}}
			if it, ok := ddbItems.Get(ddbItemKey(t, key)); ok {
				res.Items = append(res.Items, pqlProject(it, st))
			}
			return res, nil
		}
	}

	filterExpr, cerr := ddbCompileExpr("WHERE clause", expr, names, values)
	if cerr != nil {
		return nil, pqlErrf("ValidationException", "%v", cerr)
	}
	prefix := st.Table + "/"
	keys := ddbTableSortedKeys(prefix)
	var matched []map[string]any
	for _, k := range keys {
		it, ok := ddbItems.Get(k)
		if !ok {
			continue
		}
		if !filterExpr.match(it, true) {
			continue
		}
		matched = append(matched, it)
	}

	if st.OrderByKey != "" {
		pqlSortItems(matched, st.OrderByKey, st.OrderDesc)
	}

	page, next, err := listq.TokenPage(pqlNextTokens, matched, nextToken, limit, 0, 0)
	if err != nil {
		return nil, pqlErrf("ValidationException", "Invalid NextToken")
	}
	res := &pqlResult{}
	if next != "" {
		res.NextToken = next
		// DynamoDB returns LastEvaluatedKey beside NextToken for a truncated page.
		if len(page) > 0 {
			res.LastEvaluatedKey = ddbExtractKey(t, page[len(page)-1])
			res.hasLastKey = true
		}
	}
	res.Items = make([]map[string]any, 0, len(page))
	for _, it := range page {
		res.Items = append(res.Items, pqlProject(it, st))
	}
	return res, nil
}

func pqlCoversKey(t DDBTable, eqs map[string]map[string]any) bool {
	if len(t.KeySchema) == 0 {
		return false
	}
	for _, ks := range t.KeySchema {
		if _, ok := eqs[ks.AttributeName]; !ok {
			return false
		}
	}
	return true
}

// SELECT * returns the item unchanged.
func pqlProject(item map[string]any, st *partiQLStmt) map[string]any {
	if st.SelectAll || len(st.SelectCols) == 0 {
		return item
	}
	out := map[string]any{}
	for _, c := range st.SelectCols {
		if v, ok := item[c]; ok {
			out[c] = v
		}
	}
	return out
}

// Numeric when both parse as numbers, lexicographic otherwise, as DynamoDB
// orders N and S sort keys.
func pqlSortItems(items []map[string]any, attr string, desc bool) {
	less := func(a, b map[string]any) bool {
		av := ddbScalarString(a[attr])
		bv := ddbScalarString(b[attr])
		if af, e1 := strconv.ParseFloat(av, 64); e1 == nil {
			if bf, e2 := strconv.ParseFloat(bv, 64); e2 == nil {
				return af < bf
			}
		}
		return av < bv
	}
	// Insertion sort keeps the order stable.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			swap := less(items[j], items[j-1])
			if desc {
				swap = less(items[j-1], items[j])
			}
			if !swap {
				break
			}
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func pqlExecInsert(t DDBTable, st *partiQLStmt) (*pqlResult, *pqlError) {
	item := pqlMapAVToItem(st.InsertValue)
	if item == nil {
		return nil, pqlErrf("ValidationException", "INSERT VALUE must be a non-empty map")
	}
	// The wording is DynamoDB Local's.
	for _, ks := range t.KeySchema {
		value, ok := item[ks.AttributeName]
		if !ok {
			return nil, pqlErrf("ValidationException",
				"Key attribute should be present in the item: Key %s", ks.AttributeName)
		}
		if message := ddbKeyValueError(t, ks.AttributeName, value); message != "" {
			if strings.Contains(message, "Type mismatch") {
				return nil, pqlErrf("ValidationException",
					"Key attribute's data type should match its data type in table's schema: Key %s", ks.AttributeName)
			}
			return nil, pqlErrf("ValidationException", "%s", message)
		}
	}
	if message := ddbNumbersError(item); message != "" {
		return nil, pqlErrf("ValidationException", "%s", message)
	}
	if message := ddbIndexKeyError(t, item); message != "" {
		return nil, pqlErrf("ValidationException", "%s", message)
	}
	if ddbItemTooDeep(item) {
		return nil, pqlErrf("ValidationException", "Item nesting exceeds the 32-level maximum")
	}
	if err := ddbValidateItemSize(item); err != nil {
		return nil, pqlErrf("ValidationException", "%v", err)
	}
	key := ddbItemKey(t, item)
	if _, exists := ddbItems.Get(key); exists {
		return nil, pqlErrf("DuplicateItemException", "Duplicate primary key exists in table")
	}
	ddbPutItem(t, key, item)
	return &pqlResult{}, nil
}

func pqlMapAVToItem(av map[string]any) map[string]any {
	m, ok := av["M"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// The WHERE must resolve to one primary key by equality.
func pqlExecUpdate(t DDBTable, st *partiQLStmt) (*pqlResult, *pqlError) {
	key, perr := pqlResolveKeyFromWhere(t, st.Where)
	if perr != nil {
		return nil, perr
	}
	itemKey := ddbItemKey(t, key)
	item, existed := ddbItems.Get(itemKey)
	// PartiQL UPDATE does not upsert.
	if !existed {
		return nil, &pqlError{Code: "ConditionalCheckFailedException",
			Message: "The conditional request failed"}
	}
	old := ddbCloneItem(item)
	// Update a copy and publish it, as UpdateItem does.
	item = ddbCloneItem(item)

	// Reuse UpdateItem's applier so path and value semantics match.
	updExpr, exprNames, exprValues := pqlBuildUpdateExpression(st)
	if updExpr != "" {
		if err := ddbApplyUpdateExpression(item, updExpr, exprNames, exprValues); err != nil {
			return nil, pqlErrf("ValidationException", "%v", err)
		}
	}
	if err := ddbValidateItemSize(item); err != nil {
		return nil, pqlErrf("ValidationException", "%v", err)
	}
	ddbPutItem(t, itemKey, item)

	res := &pqlResult{}
	switch st.Returning {
	case "ALLOLD":
		res.Item = old
	case "ALLNEW":
		res.Item = item
	case "MODIFIEDOLD":
		res.Item = pqlModifiedAttrs(old, item, true)
	case "MODIFIEDNEW":
		res.Item = pqlModifiedAttrs(old, item, false)
	}
	return res, nil
}

// wantOld returns the old values, otherwise the new.
func pqlModifiedAttrs(old, newItem map[string]any, wantOld bool) map[string]any {
	out := map[string]any{}
	for attr, nv := range newItem {
		if ov, ok := old[attr]; !ok || !ddbAttrEqual(ov, nv) {
			if wantOld {
				if ov, ok := old[attr]; ok {
					out[attr] = ov
				}
			} else {
				out[attr] = nv
			}
		}
	}
	if wantOld {
		// Removed attributes count as modified.
		for attr, ov := range old {
			if _, ok := newItem[attr]; !ok {
				out[attr] = ov
			}
		}
	}
	return out
}

func pqlBuildUpdateExpression(st *partiQLStmt) (string, map[string]string, map[string]any) {
	names := map[string]string{}
	values := map[string]any{}
	nN, nV := 0, 0
	nameRef := func(attr string) string {
		ph := fmt.Sprintf("#u%d", nN)
		nN++
		names[ph] = attr
		return ph
	}
	valRef := func(av map[string]any) string {
		ph := fmt.Sprintf(":u%d", nV)
		nV++
		values[ph] = av
		return ph
	}
	var parts []string
	if len(st.Sets) > 0 {
		var sets []string
		for _, s := range st.Sets {
			sets = append(sets, nameRef(s.Path)+" = "+valRef(s.Value.AV))
		}
		parts = append(parts, "SET "+strings.Join(sets, ", "))
	}
	if len(st.Removes) > 0 {
		var rms []string
		for _, r := range st.Removes {
			rms = append(rms, nameRef(r))
		}
		parts = append(parts, "REMOVE "+strings.Join(rms, ", "))
	}
	return strings.Join(parts, " "), names, values
}

func pqlExecDelete(t DDBTable, st *partiQLStmt) (*pqlResult, *pqlError) {
	key, perr := pqlResolveKeyFromWhere(t, st.Where)
	if perr != nil {
		return nil, perr
	}
	itemKey := ddbItemKey(t, key)
	old, existed := ddbItems.Get(itemKey)
	if !existed {
		// Deleting an absent item fails the implicit condition.
		return nil, &pqlError{Code: "ConditionalCheckFailedException",
			Message: "The conditional request failed"}
	}
	ddbDeleteItem(t, itemKey)
	res := &pqlResult{}
	if st.Returning == "ALLOLD" {
		res.Item = old
	}
	return res, nil
}

func pqlResolveKeyFromWhere(t DDBTable, where partiQLExpr) (map[string]any, *pqlError) {
	eqs, ok := pqlKeyEqualities(where)
	if !ok || !pqlCoversKey(t, eqs) {
		return nil, pqlErrf("ValidationException",
			"WHERE clause does not contain a complete primary key for table %s", t.TableName)
	}
	key := map[string]any{}
	for _, ks := range t.KeySchema {
		key[ks.AttributeName] = eqs[ks.AttributeName]
	}
	return key, nil
}

// The next page's offset into the matches, as base64 JSON.
var pqlNextTokens = listq.Tokens{
	Encode: func(offset int) string {
		raw, _ := json.Marshal(pqlNextToken{Offset: offset})
		return base64.StdEncoding.EncodeToString(raw)
	},
	Decode: func(token string) (int, bool) {
		raw, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			return 0, false
		}
		var v pqlNextToken
		if err := json.Unmarshal(raw, &v); err != nil {
			return 0, false
		}
		return v.Offset, true
	},
	Strict: true,
}

type pqlNextToken struct {
	Offset int `json:"o"`
}

func handleDDBExecuteStatement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement                           string           `json:"Statement"`
		Parameters                          []map[string]any `json:"Parameters"`
		ConsistentRead                      bool             `json:"ConsistentRead"`
		NextToken                           string           `json:"NextToken"`
		Limit                               int              `json:"Limit"`
		ReturnConsumedCapacity              string           `json:"ReturnConsumedCapacity"`
		ReturnValuesOnConditionCheckFailure string           `json:"ReturnValuesOnConditionCheckFailure"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Statement) == "" {
		AWSError(w, "ValidationException", "Statement is required", http.StatusBadRequest)
		return
	}
	st, err := parsePartiQL(req.Statement, req.Parameters)
	if err != nil {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest, "%v", err)
		return
	}
	// Take only the named table's stripe; SELECT reads, everything else writes.
	release := ddbItemLocks.Lock(st.Kind != pqlSelect, st.Table)
	res, perr := executePartiQL(st, req.Limit, req.NextToken)
	release()
	if perr != nil {
		pqlWriteError(w, perr)
		return
	}
	out := map[string]any{}
	switch st.Kind {
	case pqlSelect:
		items := res.Items
		if items == nil {
			items = []map[string]any{}
		}
		out["Items"] = items
		if res.NextToken != "" {
			out["NextToken"] = res.NextToken
		}
		if res.hasLastKey {
			out["LastEvaluatedKey"] = res.LastEvaluatedKey
		}
	default:
		// ExecuteStatementOutput has no single-item member, so RETURNING rows go in
		// Items.
		if res.Item != nil {
			out["Items"] = []map[string]any{res.Item}
		}
	}
	writeDDBJSON(w, http.StatusOK, out)
}

func handleDDBBatchExecuteStatement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statements []struct {
			Statement                           string           `json:"Statement"`
			Parameters                          []map[string]any `json:"Parameters"`
			ConsistentRead                      bool             `json:"ConsistentRead"`
			ReturnValuesOnConditionCheckFailure string           `json:"ReturnValuesOnConditionCheckFailure"`
		} `json:"Statements"`
		ReturnConsumedCapacity string `json:"ReturnConsumedCapacity"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Statements) == 0 {
		AWSError(w, "ValidationException",
			"1 validation error detected: Value at 'statements' failed to satisfy constraint: Member must have length greater than or equal to 1",
			http.StatusBadRequest)
		return
	}
	// A statement's failure goes in its Responses entry; the batch still answers
	// 200.
	responses := make([]map[string]any, len(req.Statements))
	// Parse before locking: only parsed statements name their tables, and the
	// stripes must be taken in one ordered call to avoid deadlock.
	parsed := make([]*partiQLStmt, len(req.Statements))
	parseErrs := make([]*pqlError, len(req.Statements))
	batchTables := make([]string, 0, len(req.Statements))
	for i, s := range req.Statements {
		st, perr := pqlParseForBatch(s.Statement, s.Parameters)
		parsed[i], parseErrs[i] = st, perr
		if perr == nil {
			batchTables = append(batchTables, st.Table)
		}
	}
	defer ddbItemLocks.Lock(true, batchTables...)()
	for i := range req.Statements {
		entry := map[string]any{}
		st, perr := parsed[i], parseErrs[i]
		if perr != nil {
			entry["Error"] = pqlBatchError(perr)
			responses[i] = entry
			continue
		}
		entry["TableName"] = st.Table
		res, perr := executePartiQL(st, 0, "")
		if perr != nil {
			entry["Error"] = pqlBatchError(perr)
			if perr.Item != nil {
				if be, ok := entry["Error"].(map[string]any); ok {
					be["Item"] = perr.Item
				}
			}
			responses[i] = entry
			continue
		}
		// A batch read names a full key, so it returns at most one Item.
		if st.Kind == pqlSelect && len(res.Items) > 0 {
			entry["Item"] = res.Items[0]
		} else if res.Item != nil {
			entry["Item"] = res.Item
		}
		responses[i] = entry
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"Responses": responses})
}

func pqlParseForBatch(statement string, params []map[string]any) (*partiQLStmt, *pqlError) {
	if strings.TrimSpace(statement) == "" {
		return nil, pqlErrf("ValidationException", "Statement is required")
	}
	st, err := parsePartiQL(statement, params)
	if err != nil {
		return nil, pqlErrf("ValidationException", "%v", err)
	}
	return st, nil
}

func handleDDBExecuteTransaction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransactStatements []struct {
			Statement                           string           `json:"Statement"`
			Parameters                          []map[string]any `json:"Parameters"`
			ReturnValuesOnConditionCheckFailure string           `json:"ReturnValuesOnConditionCheckFailure"`
		} `json:"TransactStatements"`
		ClientRequestToken     string `json:"ClientRequestToken"`
		ReturnConsumedCapacity string `json:"ReturnConsumedCapacity"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if n := len(req.TransactStatements); n == 0 || n > 100 {
		AWSError(w, "ValidationException",
			"1 validation error detected: Value at 'transactStatements' failed to satisfy constraint: Member must have length less than or equal to 100 and at least 1",
			http.StatusBadRequest)
		return
	}
	// All or nothing: validate every statement and implicit condition, then apply;
	// on any failure mutate nothing and return one CancellationReason per
	// statement.
	stmts := make([]*partiQLStmt, len(req.TransactStatements))
	for i, s := range req.TransactStatements {
		if strings.TrimSpace(s.Statement) == "" {
			AWSError(w, "ValidationException", "Statement is required", http.StatusBadRequest)
			return
		}
		st, err := parsePartiQL(s.Statement, s.Parameters)
		if err != nil {
			AWSErrorf(w, "ValidationException", http.StatusBadRequest, "%v", err)
			return
		}
		stmts[i] = st
	}

	// Take every table's stripe together so validate and apply are atomic.
	transactTables := make([]string, 0, len(stmts))
	for _, st := range stmts {
		transactTables = append(transactTables, st.Table)
	}
	defer ddbItemLocks.Lock(true, transactTables...)()

	reasons := make([]map[string]any, len(stmts))
	cancelled := false
	for i, st := range stmts {
		reasons[i] = map[string]any{"Code": "None"}
		if perr := pqlValidateForTransaction(st); perr != nil {
			reasons[i] = map[string]any{"Code": pqlTxCancelCode(perr.Code), "Message": perr.Message}
			cancelled = true
		}
	}
	if cancelled {
		writeDDBTransactionCancelled(w, reasons)
		return
	}

	responses := make([]map[string]any, len(stmts))
	for i, st := range stmts {
		res, perr := executePartiQL(st, 0, "")
		if perr != nil {
			// Validation passed, so this cannot fail; cancel if it does.
			reasons[i] = map[string]any{"Code": pqlTxCancelCode(perr.Code), "Message": perr.Message}
			writeDDBTransactionCancelled(w, reasons)
			return
		}
		entry := map[string]any{}
		if st.Kind == pqlSelect && len(res.Items) > 0 {
			entry["Item"] = res.Items[0]
		} else if res.Item != nil {
			entry["Item"] = res.Item
		}
		responses[i] = entry
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"Responses": responses})
}

// Mirrors the executor's checks without mutating.
func pqlValidateForTransaction(st *partiQLStmt) *pqlError {
	t, ok := ddbTables.Get(st.Table)
	if !ok {
		return pqlErrf("ResourceNotFoundException",
			"Requested resource not found: Table: %s not found", st.Table)
	}
	switch st.Kind {
	case pqlInsert:
		item := pqlMapAVToItem(st.InsertValue)
		if item == nil {
			return pqlErrf("ValidationException", "INSERT VALUE must be a non-empty map")
		}
		for _, ks := range t.KeySchema {
			if _, ok := item[ks.AttributeName]; !ok {
				return pqlErrf("ValidationException",
					"INSERT statement does not provide a value for the key attribute %s", ks.AttributeName)
			}
		}
		if ddbItemTooDeep(item) {
			return pqlErrf("ValidationException", "Item nesting exceeds the 32-level maximum")
		}
		if err := ddbValidateItemSize(item); err != nil {
			return pqlErrf("ValidationException", "%v", err)
		}
		if _, exists := ddbItems.Get(ddbItemKey(t, item)); exists {
			return pqlErrf("DuplicateItemException", "Duplicate primary key exists in table")
		}
	case pqlUpdate, pqlDelete:
		key, perr := pqlResolveKeyFromWhere(t, st.Where)
		if perr != nil {
			return perr
		}
		item, exists := ddbItems.Get(ddbItemKey(t, key))
		if !exists {
			return &pqlError{Code: "ConditionalCheckFailedException", Message: "The conditional request failed"}
		}
		if st.Kind == pqlUpdate {
			updated := ddbCloneItem(item)
			updExpr, exprNames, exprValues := pqlBuildUpdateExpression(st)
			if updExpr != "" {
				if err := ddbApplyUpdateExpression(updated, updExpr, exprNames, exprValues); err != nil {
					return pqlErrf("ValidationException", "%v", err)
				}
			}
			if err := ddbValidateItemSize(updated); err != nil {
				return pqlErrf("ValidationException", "%v", err)
			}
		}
	case pqlSelect:
		eqs, ok := pqlKeyEqualities(st.Where)
		if st.Index == "" && (!ok || !pqlCoversKey(t, eqs)) {
			return pqlErrf("ValidationException",
				"Transaction SELECT must specify the complete primary key")
		}
	}
	return nil
}

func pqlTxCancelCode(code string) string {
	switch code {
	case "ConditionalCheckFailedException":
		return "ConditionalCheckFailed"
	case "DuplicateItemException":
		return "DuplicateItem"
	case "ResourceNotFoundException":
		return "ResourceNotFound"
	default:
		return "ValidationError"
	}
}

func pqlWriteError(w http.ResponseWriter, perr *pqlError) {
	if perr.Code == "ConditionalCheckFailedException" {
		body := map[string]any{
			"__type":  "com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException",
			"Message": perr.Message,
		}
		if perr.Item != nil {
			body["Item"] = perr.Item
		}
		writeDDBJSON(w, http.StatusBadRequest, body)
		return
	}
	AWSErrorf(w, perr.Code, http.StatusBadRequest, "%s", perr.Message)
}

func pqlBatchError(perr *pqlError) map[string]any {
	code := "ValidationError"
	switch perr.Code {
	case "ConditionalCheckFailedException":
		code = "ConditionalCheckFailed"
	case "DuplicateItemException":
		code = "DuplicateItem"
	case "ResourceNotFoundException":
		code = "ResourceNotFound"
	case "ValidationException":
		code = "ValidationError"
	}
	return map[string]any{"Code": code, "Message": perr.Message}
}
