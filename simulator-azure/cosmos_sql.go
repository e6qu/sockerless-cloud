package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cosmos DB SQL-query subset interpreter.
//
// The Cosmos data plane accepts SQL queries (the `application/query+json` body
// with a `query` string + optional `parameters`). This file implements a real
// recursive-descent parser + evaluator for the subset the azcosmos SDK and the
// runner workloads issue, replacing the previous single-`=` string split that
// silently returned the whole collection for anything non-trivial.
//
// Supported grammar:
//
//	query     = "SELECT" projection "FROM" ident
//	            [ "WHERE" expr ]
//	            [ "ORDER" "BY" orderItem { "," orderItem } ]
//	            [ "OFFSET" int "LIMIT" int ]
//	projection= "*" | "VALUE" aggregate | "VALUE" path | path { "," path }
//	aggregate = "COUNT" "(" ("1" | "*" | path) ")"
//	orderItem = path [ "ASC" | "DESC" ]
//	expr      = or
//	or        = and { "OR" and }
//	and       = not { "AND" not }
//	not       = "NOT" not | cmp
//	cmp       = primary [ ("=" | "!=" | "<>" | "<" | "<=" | ">" | ">=") primary
//	                    | "IN" "(" primary { "," primary } ")" ]
//	primary   = "(" expr ")" | func | path | literal | param
//	func      = ("CONTAINS"|"STARTSWITH"|"ENDSWITH") "(" primary "," primary ")"
//	path      = ident { "." ident }              ("c", "c.a", "c.a.b")
//	literal   = string | number | "true" | "false" | "null"
//	param     = "@name"
//
// `TOP n` (SELECT TOP n ...) is also accepted as an alternative to OFFSET/LIMIT.
//
// Out of scope (returns a Cosmos BadRequest, never the whole collection): JOIN,
// subqueries, GROUP BY, ARRAY/object literals, non-COUNT aggregates (SUM/AVG/…),
// the full built-in function library, and EXISTS. These are not issued by the
// SDK traffic sockerless serves; a query that uses them fails loudly.

const cosmosMaxQueryDepth = 256

// cosmosQuery is the parsed query plan.
type cosmosQuery struct {
	projectAll bool
	// when countAll, the result is a single scalar = number of matched docs
	// (SELECT VALUE COUNT(1) FROM c).
	countAll bool
	// projection paths (each "a.b.c"); empty means whole doc. valueOnly emits
	// the bare scalar (SELECT VALUE c.x) instead of an object.
	projection []cosmosPath
	valueOnly  bool
	where      cosmosExpr // nil = match all
	orderBy    []cosmosOrderItem
	offset     int // -1 = none
	limit      int // -1 = none
}

type cosmosOrderItem struct {
	path cosmosPath
	desc bool
}

// cosmosPath is a doc traversal like c.a.b → [a b] (the root alias is stripped).
type cosmosPath struct {
	root string   // the FROM alias, e.g. "c"
	segs []string // segments after the alias
}

// ── parser ──────────────────────────────────────────────────────────────────

type cosmosSQLParser struct {
	toks  []cosmosTok
	pos   int
	guard *sim.ParseGuard
	err   error
}

type cosmosTokKind int

const (
	cosmosTokEOF cosmosTokKind = iota
	cosmosTokIdent
	cosmosTokString
	cosmosTokNumber
	cosmosTokParam
	cosmosTokPunct // ( ) , .
	cosmosTokOp    // = != <> < <= > >=
)

type cosmosTok struct {
	kind cosmosTokKind
	text string
}

func cosmosTokenize(s string) []cosmosTok {
	var toks []cosmosTok
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			sc.Next()
		case c == '(' || c == ')' || c == ',' || c == '.':
			toks = append(toks, cosmosTok{cosmosTokPunct, string(sc.Next())})
		case c == '*':
			// "*" (SELECT *, COUNT(*)) is emitted as an ident so the projection
			// parser can detect it with peekStar.
			toks = append(toks, cosmosTok{cosmosTokIdent, string(sc.Next())})
		case c == '\'' || c == '"':
			quote := sc.Next()
			var b strings.Builder
			for !sc.Eof() {
				ch := sc.Peek()
				if ch == quote {
					if sc.PeekAt(1) == quote { // doubled-quote escape
						b.WriteByte(quote)
						sc.Next()
						sc.Next()
						continue
					}
					break
				}
				if ch == '\\' && (sc.PeekAt(1) == quote || sc.PeekAt(1) == '\\') {
					sc.Next()
					b.WriteByte(sc.Next())
					continue
				}
				b.WriteByte(sc.Next())
			}
			if !sc.Eof() {
				sc.Next() // closing quote
			}
			toks = append(toks, cosmosTok{cosmosTokString, b.String()})
		case c == '@':
			start := sc.Pos()
			sc.Next()
			for !sc.Eof() && cosmosIsIdentByte(sc.Peek()) {
				sc.Next()
			}
			toks = append(toks, cosmosTok{cosmosTokParam, sc.Slice(start, sc.Pos())})
		case c == '=' || c == '!' || c == '<' || c == '>':
			start := sc.Pos()
			sc.Next()
			if !sc.Eof() && (sc.Peek() == '=' || sc.Peek() == '>') {
				sc.Next()
			}
			toks = append(toks, cosmosTok{cosmosTokOp, sc.Slice(start, sc.Pos())})
		case c >= '0' && c <= '9' || (c == '-' && cosmosIsDigit(sc.PeekAt(1))):
			start := sc.Pos()
			sc.Next()
			for !sc.Eof() {
				ch := sc.Peek()
				if cosmosIsDigit(ch) || ch == '.' || ch == 'e' || ch == 'E' || ch == '+' || ch == '-' {
					sc.Next()
					continue
				}
				break
			}
			toks = append(toks, cosmosTok{cosmosTokNumber, sc.Slice(start, sc.Pos())})
		case cosmosIsIdentByte(c):
			start := sc.Pos()
			for !sc.Eof() && cosmosIsIdentByte(sc.Peek()) {
				sc.Next()
			}
			toks = append(toks, cosmosTok{cosmosTokIdent, sc.Slice(start, sc.Pos())})
		default:
			sc.Next() // skip unknown byte
		}
	}
	return append(toks, cosmosTok{cosmosTokEOF, ""})
}

func cosmosIsDigit(b byte) bool { return b >= '0' && b <= '9' }
func cosmosIsIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// cosmosParseQuery parses a SQL query string into a plan, or returns an error
// (translated to a Cosmos BadRequest by the caller).
func cosmosParseQuery(query string) (*cosmosQuery, error) {
	p := &cosmosSQLParser{
		toks:  cosmosTokenize(query),
		guard: sim.NewParseGuard(cosmosMaxQueryDepth, 0),
	}
	q := &cosmosQuery{offset: -1, limit: -1}
	if !p.expectKeyword("SELECT") {
		return nil, fmt.Errorf("query must start with SELECT")
	}
	// optional TOP n
	if p.peekKeyword("TOP") {
		p.next()
		n, ok := p.parseInt()
		if !ok {
			return nil, fmt.Errorf("TOP expects an integer")
		}
		q.limit = n
	}
	if err := p.parseProjection(q); err != nil {
		return nil, err
	}
	if !p.expectKeyword("FROM") {
		return nil, fmt.Errorf("expected FROM")
	}
	if p.peek().kind != cosmosTokIdent {
		return nil, fmt.Errorf("expected source alias after FROM")
	}
	p.next() // FROM <alias> (and an optional sub-path which we don't model)
	for p.peekPunct(".") {
		p.next()
		p.next()
	}
	if p.peekKeyword("WHERE") {
		p.next()
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		q.where = node
	}
	if p.peekKeyword("ORDER") {
		p.next()
		if !p.expectKeyword("BY") {
			return nil, fmt.Errorf("expected BY after ORDER")
		}
		for {
			path, ok := p.parsePath()
			if !ok {
				return nil, fmt.Errorf("expected field in ORDER BY")
			}
			item := cosmosOrderItem{path: path}
			if p.peekKeyword("DESC") {
				p.next()
				item.desc = true
			} else if p.peekKeyword("ASC") {
				p.next()
			}
			q.orderBy = append(q.orderBy, item)
			if p.peekPunct(",") {
				p.next()
				continue
			}
			break
		}
	}
	if p.peekKeyword("OFFSET") {
		p.next()
		n, ok := p.parseInt()
		if !ok {
			return nil, fmt.Errorf("OFFSET expects an integer")
		}
		q.offset = n
		if !p.expectKeyword("LIMIT") {
			return nil, fmt.Errorf("OFFSET must be followed by LIMIT")
		}
		n, ok = p.parseInt()
		if !ok {
			return nil, fmt.Errorf("LIMIT expects an integer")
		}
		q.limit = n
	}
	if p.peek().kind != cosmosTokEOF {
		return nil, fmt.Errorf("unexpected token %q in query", p.peek().text)
	}
	if p.err != nil {
		return nil, p.err
	}
	return q, nil
}

func (p *cosmosSQLParser) parseProjection(q *cosmosQuery) error {
	if p.peekStar() {
		p.next()
		q.projectAll = true
		return nil
	}
	if p.peekKeyword("VALUE") {
		p.next()
		// VALUE COUNT(1) | VALUE COUNT(*) | VALUE <path>
		if p.peekKeyword("COUNT") {
			p.next()
			if !p.expectPunct("(") {
				return fmt.Errorf("COUNT expects (")
			}
			// consume the count argument (1, *, or a path) — value irrelevant
			if !p.peekStar() && p.peek().kind != cosmosTokNumber && p.peek().kind != cosmosTokIdent {
				return fmt.Errorf("COUNT expects an argument")
			}
			p.next()
			for p.peekPunct(".") {
				p.next()
				p.next()
			}
			if !p.expectPunct(")") {
				return fmt.Errorf("COUNT expects )")
			}
			q.countAll = true
			return nil
		}
		path, ok := p.parsePath()
		if !ok {
			return fmt.Errorf("VALUE expects a field")
		}
		q.valueOnly = true
		q.projection = []cosmosPath{path}
		return nil
	}
	// Projection list: path { , path }.
	for {
		path, ok := p.parsePath()
		if !ok {
			return fmt.Errorf("expected a projection field")
		}
		q.projection = append(q.projection, path)
		if p.peekPunct(",") {
			p.next()
			continue
		}
		break
	}
	return nil
}

// peekStar reports whether the current token is a bare "*" (cosmosTokenize emits
// "*" as an ident so SELECT * and COUNT(*) parse).
func (p *cosmosSQLParser) peekStar() bool {
	return p.peek().kind == cosmosTokIdent && p.peek().text == "*"
}

func (p *cosmosSQLParser) parsePath() (cosmosPath, bool) {
	if p.peek().kind != cosmosTokIdent {
		return cosmosPath{}, false
	}
	root := p.next().text
	var segs []string
	for p.peekPunct(".") {
		p.next()
		if p.peek().kind != cosmosTokIdent {
			return cosmosPath{}, false
		}
		segs = append(segs, p.next().text)
	}
	return cosmosPath{root: root, segs: segs}, true
}

func (p *cosmosSQLParser) parseInt() (int, bool) {
	if p.peek().kind != cosmosTokNumber {
		return 0, false
	}
	n, err := strconv.Atoi(p.next().text)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ── expression parsing ───────────────────────────────────────────────────────

func (p *cosmosSQLParser) parseExpr() (cosmosExpr, error) { return p.parseOr() }

func (p *cosmosSQLParser) parseOr() (cosmosExpr, error) {
	if !p.guard.Enter() {
		return nil, fmt.Errorf("query too complex")
	}
	defer p.guard.Leave()
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peekKeyword("OR") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = cosmosOrExpr{left, right}
	}
	return left, nil
}

func (p *cosmosSQLParser) parseAnd() (cosmosExpr, error) {
	if !p.guard.Enter() {
		return nil, fmt.Errorf("query too complex")
	}
	defer p.guard.Leave()
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peekKeyword("AND") {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = cosmosAndExpr{left, right}
	}
	return left, nil
}

func (p *cosmosSQLParser) parseNot() (cosmosExpr, error) {
	if p.peekKeyword("NOT") {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return cosmosNotExpr{inner}, nil
	}
	return p.parseCmp()
}

func (p *cosmosSQLParser) parseCmp() (cosmosExpr, error) {
	left, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	if p.peekKeyword("IN") {
		p.next()
		if !p.expectPunct("(") {
			return nil, fmt.Errorf("IN expects (")
		}
		var set []cosmosOperand
		for {
			op, err := p.parseOperand()
			if err != nil {
				return nil, err
			}
			set = append(set, op)
			if p.peekPunct(",") {
				p.next()
				continue
			}
			break
		}
		if !p.expectPunct(")") {
			return nil, fmt.Errorf("IN expects )")
		}
		return cosmosInExpr{left, set}, nil
	}
	if p.peek().kind == cosmosTokOp {
		op := p.next().text
		right, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return cosmosCmpExpr{left: left, op: op, right: right}, nil
	}
	// A bare boolean operand (e.g. a function call or a boolean field).
	return cosmosBoolExpr{left}, nil
}

func (p *cosmosSQLParser) parseOperand() (cosmosOperand, error) {
	tok := p.peek()
	switch tok.kind {
	case cosmosTokPunct:
		if tok.text == "(" {
			p.next()
			if !p.guard.Enter() {
				return cosmosOperand{}, fmt.Errorf("query too complex")
			}
			inner, err := p.parseExpr()
			p.guard.Leave()
			if err != nil {
				return cosmosOperand{}, err
			}
			if !p.expectPunct(")") {
				return cosmosOperand{}, fmt.Errorf("missing )")
			}
			return cosmosOperand{expr: inner}, nil
		}
	case cosmosTokString:
		p.next()
		return cosmosOperand{lit: tok.text, hasLit: true}, nil
	case cosmosTokNumber:
		p.next()
		if f, ok := new(big.Float).SetString(tok.text); ok {
			return cosmosOperand{num: f, hasNum: true}, nil
		}
		return cosmosOperand{lit: tok.text, hasLit: true}, nil
	case cosmosTokParam:
		p.next()
		return cosmosOperand{param: tok.text, hasParam: true}, nil
	case cosmosTokIdent:
		switch strings.ToUpper(tok.text) {
		case "TRUE":
			p.next()
			return cosmosOperand{boolVal: true, hasBool: true}, nil
		case "FALSE":
			p.next()
			return cosmosOperand{boolVal: false, hasBool: true}, nil
		case "NULL":
			p.next()
			return cosmosOperand{isNull: true}, nil
		case "CONTAINS", "STARTSWITH", "ENDSWITH":
			name := strings.ToUpper(tok.text)
			p.next()
			if !p.expectPunct("(") {
				return cosmosOperand{}, fmt.Errorf("%s expects (", name)
			}
			a, err := p.parseOperand()
			if err != nil {
				return cosmosOperand{}, err
			}
			if !p.expectPunct(",") {
				return cosmosOperand{}, fmt.Errorf("%s expects two arguments", name)
			}
			b, err := p.parseOperand()
			if err != nil {
				return cosmosOperand{}, err
			}
			// optional 3rd boolean arg (case-insensitivity flag) — consume.
			if p.peekPunct(",") {
				p.next()
				if _, err := p.parseOperand(); err != nil {
					return cosmosOperand{}, err
				}
			}
			if !p.expectPunct(")") {
				return cosmosOperand{}, fmt.Errorf("%s expects )", name)
			}
			return cosmosOperand{fn: &cosmosFnOperand{name: name, a: a, b: b}}, nil
		}
		// a path
		path, ok := p.parsePath()
		if !ok {
			return cosmosOperand{}, fmt.Errorf("invalid path")
		}
		return cosmosOperand{path: &path}, nil
	}
	return cosmosOperand{}, fmt.Errorf("unexpected token %q", tok.text)
}

// ── token helpers ────────────────────────────────────────────────────────────

func (p *cosmosSQLParser) peek() cosmosTok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return cosmosTok{cosmosTokEOF, ""}
}
func (p *cosmosSQLParser) next() cosmosTok {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}
func (p *cosmosSQLParser) peekKeyword(kw string) bool {
	return p.peek().kind == cosmosTokIdent && strings.EqualFold(p.peek().text, kw)
}
func (p *cosmosSQLParser) expectKeyword(kw string) bool {
	if p.peekKeyword(kw) {
		p.next()
		return true
	}
	return false
}
func (p *cosmosSQLParser) peekPunct(s string) bool {
	return p.peek().kind == cosmosTokPunct && p.peek().text == s
}
func (p *cosmosSQLParser) expectPunct(s string) bool {
	if p.peekPunct(s) {
		p.next()
		return true
	}
	return false
}

// ── AST evaluation ───────────────────────────────────────────────────────────

type cosmosExpr interface {
	eval(doc map[string]any, params map[string]any) bool
}

type cosmosOrExpr struct{ l, r cosmosExpr }

func (e cosmosOrExpr) eval(d map[string]any, p map[string]any) bool {
	return e.l.eval(d, p) || e.r.eval(d, p)
}

type cosmosAndExpr struct{ l, r cosmosExpr }

func (e cosmosAndExpr) eval(d map[string]any, p map[string]any) bool {
	return e.l.eval(d, p) && e.r.eval(d, p)
}

type cosmosNotExpr struct{ inner cosmosExpr }

func (e cosmosNotExpr) eval(d map[string]any, p map[string]any) bool { return !e.inner.eval(d, p) }

type cosmosBoolExpr struct{ op cosmosOperand }

func (e cosmosBoolExpr) eval(d map[string]any, p map[string]any) bool {
	v, ok := e.op.value(d, p)
	if !ok {
		return false
	}
	b, isBool := v.(bool)
	return isBool && b
}

type cosmosCmpExpr struct {
	left  cosmosOperand
	op    string
	right cosmosOperand
}

func (e cosmosCmpExpr) eval(d map[string]any, p map[string]any) bool {
	lv, lok := e.left.value(d, p)
	rv, rok := e.right.value(d, p)
	switch e.op {
	case "=":
		return lok && rok && cosmosValuesEqual(lv, rv)
	case "!=", "<>":
		// Cosmos: a missing field never satisfies != (the SQL NULL-ish rule);
		// match real behaviour by requiring both present and unequal.
		if !lok || !rok {
			return false
		}
		return !cosmosValuesEqual(lv, rv)
	case "<", "<=", ">", ">=":
		if !lok || !rok {
			return false
		}
		return cosmosCompareOrdered(lv, rv, e.op)
	}
	return false
}

type cosmosInExpr struct {
	left cosmosOperand
	set  []cosmosOperand
}

func (e cosmosInExpr) eval(d map[string]any, p map[string]any) bool {
	lv, lok := e.left.value(d, p)
	if !lok {
		return false
	}
	for _, op := range e.set {
		rv, rok := op.value(d, p)
		if rok && cosmosValuesEqual(lv, rv) {
			return true
		}
	}
	return false
}

// cosmosOperand is a value-producing node: a literal, a parameter, a doc path,
// a nested boolean expression (parenthesised), or a string function.
type cosmosOperand struct {
	path     *cosmosPath
	lit      string
	hasLit   bool
	num      *big.Float
	hasNum   bool
	param    string
	hasParam bool
	boolVal  bool
	hasBool  bool
	isNull   bool
	expr     cosmosExpr
	fn       *cosmosFnOperand
}

type cosmosFnOperand struct {
	name string // CONTAINS / STARTSWITH / ENDSWITH
	a, b cosmosOperand
}

// value resolves the operand to a concrete Go value (string / *big.Float for
// numbers / bool / nil), reporting ok=false when a path is absent.
func (o cosmosOperand) value(doc map[string]any, params map[string]any) (any, bool) {
	switch {
	case o.path != nil:
		return cosmosLookupPath(doc, o.path.segs)
	case o.hasLit:
		return o.lit, true
	case o.hasNum:
		return o.num, true
	case o.hasBool:
		return o.boolVal, true
	case o.isNull:
		return nil, true
	case o.hasParam:
		v, ok := params[o.param]
		if !ok {
			return nil, false
		}
		return cosmosNormalizeValue(v), true
	case o.expr != nil:
		return o.expr.eval(doc, params), true
	case o.fn != nil:
		av, aok := o.fn.a.value(doc, params)
		bv, bok := o.fn.b.value(doc, params)
		if !aok || !bok {
			return false, true
		}
		as, aIsStr := av.(string)
		bs, bIsStr := bv.(string)
		if !aIsStr || !bIsStr {
			return false, true
		}
		switch o.fn.name {
		case "CONTAINS":
			return strings.Contains(as, bs), true
		case "STARTSWITH":
			return strings.HasPrefix(as, bs), true
		case "ENDSWITH":
			return strings.HasSuffix(as, bs), true
		}
		return false, true
	}
	return nil, false
}

// cosmosLookupPath traverses doc by the path segments. Empty segs returns the
// whole doc (the root alias `c` with no sub-path).
func cosmosLookupPath(doc map[string]any, segs []string) (any, bool) {
	var cur any = doc
	if len(segs) == 0 {
		return doc, true
	}
	for _, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// cosmosNormalizeValue coerces a JSON-decoded parameter value into the same
// representation the operand evaluator compares against (numbers → *big.Float).
func cosmosNormalizeValue(v any) any {
	switch t := v.(type) {
	case float64:
		return new(big.Float).SetFloat64(t)
	case int:
		return new(big.Float).SetInt64(int64(t))
	case int64:
		return new(big.Float).SetInt64(t)
	case json.Number:
		if f, ok := new(big.Float).SetString(string(t)); ok {
			return f
		}
		return string(t)
	}
	return v
}

// cosmosValuesEqual compares two values by VALUE with numeric unification: an
// int 5 from a doc equals a 5.0 literal, a typed bool/null compares typed.
func cosmosValuesEqual(a, b any) bool {
	an, aIsNum := cosmosAsBigFloat(a)
	bn, bIsNum := cosmosAsBigFloat(b)
	if aIsNum && bIsNum {
		return an.Cmp(bn) == 0
	}
	if aIsNum != bIsNum {
		return false
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ab, aIsBool := a.(bool)
	bb, bIsBool := b.(bool)
	if aIsBool || bIsBool {
		return aIsBool && bIsBool && ab == bb
	}
	as, aIsStr := a.(string)
	bs, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return as == bs
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// cosmosCompareOrdered evaluates <,<=,>,>= numerically when both sides are
// numbers, else lexicographically over their string form.
func cosmosCompareOrdered(a, b any, op string) bool {
	an, aIsNum := cosmosAsBigFloat(a)
	bn, bIsNum := cosmosAsBigFloat(b)
	var cmp int
	if aIsNum && bIsNum {
		cmp = an.Cmp(bn)
	} else {
		as, bs := cosmosScalarToString(a), cosmosScalarToString(b)
		switch {
		case as < bs:
			cmp = -1
		case as > bs:
			cmp = 1
		}
	}
	switch op {
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	}
	return false
}

func cosmosAsBigFloat(v any) (*big.Float, bool) {
	switch t := v.(type) {
	case *big.Float:
		return t, true
	case float64:
		return new(big.Float).SetFloat64(t), true
	case int:
		return new(big.Float).SetInt64(int64(t)), true
	case int64:
		return new(big.Float).SetInt64(t), true
	case json.Number:
		if f, ok := new(big.Float).SetString(string(t)); ok {
			return f, true
		}
	}
	return nil, false
}

func cosmosScalarToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case *big.Float:
		return t.Text('f', -1)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// ── execution ────────────────────────────────────────────────────────────────

// cosmosBindParams flattens the request `parameters` array into a name→value
// map keyed by the `@name`.
func cosmosBindParams(params []map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for _, p := range params {
		name, _ := p["name"].(string)
		if name == "" {
			continue
		}
		out[name] = p["value"]
	}
	return out
}

// cosmosRunQuery evaluates a parsed plan against a collection's documents and
// returns the result rows (each already projected) plus whether the result is a
// scalar (COUNT). Documents are evaluated against their stored Body map.
func cosmosRunQuery(q *cosmosQuery, docs []CosmosDocument, params map[string]any) []map[string]any {
	type row struct {
		doc  CosmosDocument
		body map[string]any
	}
	var matched []row
	for _, d := range docs {
		if q.where != nil && !q.where.eval(d.Body, params) {
			continue
		}
		matched = append(matched, row{doc: d, body: d.Body})
	}

	// ORDER BY (stable, multi-key).
	if len(q.orderBy) > 0 {
		sort.SliceStable(matched, func(i, j int) bool {
			for _, item := range q.orderBy {
				av, aok := cosmosLookupPath(matched[i].body, item.path.segs)
				bv, bok := cosmosLookupPath(matched[j].body, item.path.segs)
				if !aok && !bok {
					continue
				}
				if cosmosValuesEqual(av, bv) {
					continue
				}
				less := cosmosCompareOrdered(av, bv, "<")
				if item.desc {
					return !less && !cosmosValuesEqual(av, bv)
				}
				return less
			}
			return false
		})
	}

	// COUNT aggregate → single scalar row. Apply OFFSET/LIMIT first? Real
	// Cosmos applies COUNT over the whole filtered set; TOP/OFFSET with an
	// aggregate is rejected, so count the matched rows directly.
	if q.countAll {
		return []map[string]any{{"$1": len(matched)}}
	}

	// OFFSET / LIMIT (TOP folds into limit).
	start := 0
	if q.offset > 0 {
		start = q.offset
	}
	if start > len(matched) {
		start = len(matched)
	}
	matched = matched[start:]
	if q.limit >= 0 && q.limit < len(matched) {
		matched = matched[:q.limit]
	}

	out := make([]map[string]any, 0, len(matched))
	for _, m := range matched {
		out = append(out, cosmosProjectRow(q, m.doc))
	}
	return out
}

// cosmosProjectRow renders one result row honoring the SELECT projection.
func cosmosProjectRow(q *cosmosQuery, doc CosmosDocument) map[string]any {
	if q.projectAll || len(q.projection) == 0 {
		return cosmosDocBody(doc)
	}
	if q.valueOnly {
		// SELECT VALUE c.x — emit the scalar under "$1" (azcosmos unwraps a
		// single-property row; the value query path reads the property).
		v, ok := cosmosLookupPath(doc.Body, q.projection[0].segs)
		if !ok {
			return map[string]any{}
		}
		return map[string]any{"$1": cosmosRenderValue(v)}
	}
	row := make(map[string]any, len(q.projection))
	for _, path := range q.projection {
		v, ok := cosmosLookupPath(doc.Body, path.segs)
		if !ok {
			continue
		}
		// the result key is the last path segment (Cosmos default alias).
		key := path.root
		if len(path.segs) > 0 {
			key = path.segs[len(path.segs)-1]
		}
		row[key] = cosmosRenderValue(v)
	}
	return row
}

// cosmosRenderValue converts an internal value (e.g. *big.Float from a param)
// back to a JSON-friendly form for the response body.
func cosmosRenderValue(v any) any {
	if f, ok := v.(*big.Float); ok {
		if i, acc := f.Int64(); acc == big.Exact {
			return i
		}
		out, _ := f.Float64()
		return out
	}
	return v
}
