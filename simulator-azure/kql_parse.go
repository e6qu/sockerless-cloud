package main

import (
	"time"
)

type kqlExpr interface{ position() int }

type kqlLiteral struct {
	pos int
	typ string
	val any
}

type kqlColumnRef struct {
	pos  int
	name string
}

type kqlBinary struct {
	pos  int
	op   string
	l, r kqlExpr
}

type kqlUnaryMinus struct {
	pos int
	x   kqlExpr
}

type kqlInList struct {
	pos         int
	op          string // in, !in, in~, !in~, has_any
	x           kqlExpr
	list        []kqlExpr
	negated, ci bool
}

type kqlBetween struct {
	pos     int
	negated bool
	x       kqlExpr
	lo, hi  kqlExpr
}

type kqlCall struct {
	pos  int
	name string
	args []kqlExpr
}

func (e kqlLiteral) position() int    { return e.pos }
func (e kqlColumnRef) position() int  { return e.pos }
func (e kqlBinary) position() int     { return e.pos }
func (e kqlUnaryMinus) position() int { return e.pos }
func (e kqlInList) position() int     { return e.pos }
func (e kqlBetween) position() int    { return e.pos }
func (e kqlCall) position() int       { return e.pos }

// kqlAssignment is one `Name = expr` (or bare `expr`) item of project, extend,
// summarize or distinct.
type kqlAssignment struct {
	name string // empty when the item names no column
	expr kqlExpr
}

type kqlSortKey struct {
	expr       kqlExpr
	desc       bool
	nullsFirst bool
}

type kqlOperator interface{ operatorName() string }

type kqlWhereOp struct{ pred kqlExpr }
type kqlTakeOp struct {
	name string
	n    int64
}
type kqlProjectOp struct{ items []kqlAssignment }
type kqlProjectAwayOp struct {
	cols []kqlColumnRef
}
type kqlProjectRenameOp struct {
	pairs [][2]kqlColumnRef // new, old
}
type kqlExtendOp struct{ items []kqlAssignment }
type kqlSortOp struct {
	name string
	keys []kqlSortKey
}
type kqlTopOp struct {
	n   int64
	key kqlSortKey
}
type kqlCountOp struct{}
type kqlSummarizeOp struct {
	aggs []kqlAssignment
	by   []kqlAssignment
}
type kqlDistinctOp struct {
	all   bool
	items []kqlAssignment
}

func (kqlWhereOp) operatorName() string         { return "where" }
func (o kqlTakeOp) operatorName() string        { return o.name }
func (kqlProjectOp) operatorName() string       { return "project" }
func (kqlProjectAwayOp) operatorName() string   { return "project-away" }
func (kqlProjectRenameOp) operatorName() string { return "project-rename" }
func (kqlExtendOp) operatorName() string        { return "extend" }
func (o kqlSortOp) operatorName() string        { return o.name }
func (kqlTopOp) operatorName() string           { return "top" }
func (kqlCountOp) operatorName() string         { return "count" }
func (kqlSummarizeOp) operatorName() string     { return "summarize" }
func (kqlDistinctOp) operatorName() string      { return "distinct" }

// kqlQuery is a tabular expression: a table and the operators piped after it.
type kqlQuery struct {
	table string
	ops   []kqlOperator
}

type kqlParser struct {
	src  string
	toks []kqlToken
	i    int
}

// parseKQL parses a query into its source table and the tabular operators
// applied to it. An operator, function or token the engine does not know is a
// syntax error rather than something skipped.
func parseKQL(src string) (kqlQuery, *kqlError) {
	toks, err := lexKQL(src)
	if err != nil {
		return kqlQuery{}, err
	}
	p := &kqlParser{src: src, toks: toks}
	return p.parseQuery()
}

func (p *kqlParser) peek() kqlToken { return p.toks[p.i] }
func (p *kqlParser) next() kqlToken {
	t := p.toks[p.i]
	if t.kind != kqlEOF {
		p.i++
	}
	return t
}

func (p *kqlParser) fail(t kqlToken) *kqlError {
	return kqlSyntaxError(p.src, t.pos, t.text)
}

func (p *kqlParser) isPunct(text string) bool {
	t := p.peek()
	return t.kind == kqlPunct && t.text == text
}

func (p *kqlParser) isWord(text string) bool {
	t := p.peek()
	return t.kind == kqlIdent && t.text == text
}

func (p *kqlParser) expectPunct(text string) *kqlError {
	if !p.isPunct(text) {
		return p.fail(p.peek())
	}
	p.next()
	return nil
}

func (p *kqlParser) expectWord(text string) *kqlError {
	if !p.isWord(text) {
		return p.fail(p.peek())
	}
	p.next()
	return nil
}

func (p *kqlParser) ident() (kqlColumnRef, *kqlError) {
	t := p.peek()
	if t.kind != kqlIdent {
		return kqlColumnRef{}, p.fail(t)
	}
	p.next()
	return kqlColumnRef{pos: t.pos, name: t.text}, nil
}

func (p *kqlParser) parseQuery() (kqlQuery, *kqlError) {
	table, err := p.ident()
	if err != nil {
		return kqlQuery{}, err
	}
	q := kqlQuery{table: table.name}
	for p.isPunct("|") {
		p.next()
		op, err := p.parseOperator()
		if err != nil {
			return kqlQuery{}, err
		}
		q.ops = append(q.ops, op)
	}
	if p.isPunct(";") {
		p.next()
	}
	if t := p.peek(); t.kind != kqlEOF {
		return kqlQuery{}, p.fail(t)
	}
	return q, nil
}

func (p *kqlParser) parseOperator() (kqlOperator, *kqlError) {
	t := p.peek()
	if t.kind != kqlIdent {
		return nil, p.fail(t)
	}
	switch t.text {
	case "where", "filter":
		p.next()
		pred, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		return kqlWhereOp{pred: pred}, nil
	case "take", "limit":
		p.next()
		n, err := p.parseCount()
		if err != nil {
			return nil, err
		}
		return kqlTakeOp{name: t.text, n: n}, nil
	case "project":
		p.next()
		items, err := p.parseAssignments()
		if err != nil {
			return nil, err
		}
		return kqlProjectOp{items: items}, nil
	case "project-away":
		p.next()
		var cols []kqlColumnRef
		for {
			c, err := p.ident()
			if err != nil {
				return nil, err
			}
			cols = append(cols, c)
			if !p.isPunct(",") {
				break
			}
			p.next()
		}
		return kqlProjectAwayOp{cols: cols}, nil
	case "project-rename":
		p.next()
		var pairs [][2]kqlColumnRef
		for {
			newName, err := p.ident()
			if err != nil {
				return nil, err
			}
			if err := p.expectPunct("="); err != nil {
				return nil, err
			}
			old, err := p.ident()
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, [2]kqlColumnRef{newName, old})
			if !p.isPunct(",") {
				break
			}
			p.next()
		}
		return kqlProjectRenameOp{pairs: pairs}, nil
	case "extend":
		p.next()
		items, err := p.parseAssignments()
		if err != nil {
			return nil, err
		}
		return kqlExtendOp{items: items}, nil
	case "order", "sort":
		p.next()
		if err := p.expectWord("by"); err != nil {
			return nil, err
		}
		var keys []kqlSortKey
		for {
			key, err := p.parseSortKey()
			if err != nil {
				return nil, err
			}
			keys = append(keys, key)
			if !p.isPunct(",") {
				break
			}
			p.next()
		}
		return kqlSortOp{name: t.text, keys: keys}, nil
	case "top":
		p.next()
		n, err := p.parseCount()
		if err != nil {
			return nil, err
		}
		if err := p.expectWord("by"); err != nil {
			return nil, err
		}
		key, err := p.parseSortKey()
		if err != nil {
			return nil, err
		}
		return kqlTopOp{n: n, key: key}, nil
	case "count":
		p.next()
		return kqlCountOp{}, nil
	case "summarize":
		p.next()
		var op kqlSummarizeOp
		if !p.isWord("by") {
			aggs, err := p.parseAssignments()
			if err != nil {
				return nil, err
			}
			op.aggs = aggs
		}
		if p.isWord("by") {
			p.next()
			by, err := p.parseAssignments()
			if err != nil {
				return nil, err
			}
			op.by = by
		}
		if len(op.aggs) == 0 && len(op.by) == 0 {
			return nil, p.fail(p.peek())
		}
		return op, nil
	case "distinct":
		p.next()
		if p.isPunct("*") {
			p.next()
			return kqlDistinctOp{all: true}, nil
		}
		items, err := p.parseAssignments()
		if err != nil {
			return nil, err
		}
		return kqlDistinctOp{items: items}, nil
	}
	return nil, p.fail(t)
}

func (p *kqlParser) parseCount() (int64, *kqlError) {
	t := p.peek()
	if t.kind != kqlLong {
		return 0, p.fail(t)
	}
	p.next()
	return t.num, nil
}

func (p *kqlParser) parseSortKey() (kqlSortKey, *kqlError) {
	expr, err := p.parseExpr()
	if err != nil {
		return kqlSortKey{}, err
	}
	key := kqlSortKey{expr: expr, desc: true}
	switch {
	case p.isWord("asc"):
		p.next()
		key.desc = false
	case p.isWord("desc"):
		p.next()
	}
	key.nullsFirst = !key.desc
	if p.isWord("nulls") {
		p.next()
		switch {
		case p.isWord("first"):
			key.nullsFirst = true
		case p.isWord("last"):
			key.nullsFirst = false
		default:
			return kqlSortKey{}, p.fail(p.peek())
		}
		p.next()
	}
	return key, nil
}

// parseAssignments reads a comma-separated list of `Name = expr` or `expr`.
func (p *kqlParser) parseAssignments() ([]kqlAssignment, *kqlError) {
	var items []kqlAssignment
	for {
		var item kqlAssignment
		if t := p.peek(); t.kind == kqlIdent && p.toks[p.i+1].kind == kqlPunct && p.toks[p.i+1].text == "=" {
			p.next()
			p.next()
			item.name = t.text
		}
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		item.expr = expr
		items = append(items, item)
		if !p.isPunct(",") {
			return items, nil
		}
		p.next()
	}
}

func (p *kqlParser) parseExpr() (kqlExpr, *kqlError) { return p.parseOr() }

func (p *kqlParser) parseOr() (kqlExpr, *kqlError) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isWord("or") {
		t := p.next()
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = kqlBinary{pos: t.pos, op: "or", l: l, r: r}
	}
	return l, nil
}

func (p *kqlParser) parseAnd() (kqlExpr, *kqlError) {
	l, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for p.isWord("and") {
		t := p.next()
		r, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		l = kqlBinary{pos: t.pos, op: "and", l: l, r: r}
	}
	return l, nil
}

var kqlComparisonPuncts = map[string]bool{
	"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true, "=~": true, "!~": true,
}

var kqlStringOperators = map[string]bool{
	"contains": true, "!contains": true, "contains_cs": true, "!contains_cs": true,
	"has": true, "!has": true, "has_cs": true, "!has_cs": true,
	"hasprefix": true, "!hasprefix": true, "hassuffix": true, "!hassuffix": true,
	"startswith": true, "!startswith": true, "startswith_cs": true, "!startswith_cs": true,
	"endswith": true, "!endswith": true, "endswith_cs": true, "!endswith_cs": true,
}

func (p *kqlParser) parseComparison() (kqlExpr, *kqlError) {
	l, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	switch {
	case t.kind == kqlPunct && kqlComparisonPuncts[t.text]:
		p.next()
		r, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return kqlBinary{pos: t.pos, op: t.text, l: l, r: r}, nil
	case t.kind == kqlIdent && kqlStringOperators[t.text]:
		p.next()
		r, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return kqlBinary{pos: t.pos, op: t.text, l: l, r: r}, nil
	case t.kind == kqlIdent && t.text == "matches":
		p.next()
		if err := p.expectWord("regex"); err != nil {
			return nil, err
		}
		r, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return kqlBinary{pos: t.pos, op: "matches regex", l: l, r: r}, nil
	case t.kind == kqlIdent && (t.text == "in" || t.text == "!in" || t.text == "in~" || t.text == "!in~" || t.text == "has_any"):
		p.next()
		if err := p.expectPunct("("); err != nil {
			return nil, err
		}
		var list []kqlExpr
		for {
			item, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			list = append(list, item)
			if !p.isPunct(",") {
				break
			}
			p.next()
		}
		if err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		return kqlInList{
			pos: t.pos, op: t.text, x: l, list: list,
			negated: t.text[0] == '!', ci: t.text == "in~" || t.text == "!in~" || t.text == "has_any",
		}, nil
	case t.kind == kqlIdent && (t.text == "between" || t.text == "!between"):
		p.next()
		if err := p.expectPunct("("); err != nil {
			return nil, err
		}
		lo, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(".."); err != nil {
			return nil, err
		}
		hi, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		return kqlBetween{pos: t.pos, negated: t.text == "!between", x: l, lo: lo, hi: hi}, nil
	}
	return l, nil
}

func (p *kqlParser) parseAdditive() (kqlExpr, *kqlError) {
	l, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for p.isPunct("+") || p.isPunct("-") {
		t := p.next()
		r, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		l = kqlBinary{pos: t.pos, op: t.text, l: l, r: r}
	}
	return l, nil
}

func (p *kqlParser) parseMultiplicative() (kqlExpr, *kqlError) {
	l, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.isPunct("*") || p.isPunct("/") || p.isPunct("%") {
		t := p.next()
		r, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		l = kqlBinary{pos: t.pos, op: t.text, l: l, r: r}
	}
	return l, nil
}

func (p *kqlParser) parseUnary() (kqlExpr, *kqlError) {
	if p.isPunct("-") {
		t := p.next()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return kqlUnaryMinus{pos: t.pos, x: x}, nil
	}
	if p.isPunct("+") {
		p.next()
		return p.parseUnary()
	}
	return p.parsePrimary()
}

func (p *kqlParser) parsePrimary() (kqlExpr, *kqlError) {
	t := p.peek()
	switch t.kind {
	case kqlString:
		p.next()
		return kqlLiteral{pos: t.pos, typ: "string", val: t.str}, nil
	case kqlLong:
		p.next()
		return kqlLiteral{pos: t.pos, typ: "long", val: t.num}, nil
	case kqlReal:
		p.next()
		return kqlLiteral{pos: t.pos, typ: "real", val: t.real}, nil
	case kqlTimespan:
		p.next()
		return kqlLiteral{pos: t.pos, typ: "timespan", val: t.span}, nil
	case kqlDatetime:
		p.next()
		if t.str == "null" {
			return kqlLiteral{pos: t.pos, typ: "datetime", val: nil}, nil
		}
		ts, ok := parseKQLDatetime(t.str)
		if !ok {
			return nil, p.fail(t)
		}
		return kqlLiteral{pos: t.pos, typ: "datetime", val: ts}, nil
	case kqlPunct:
		if t.text == "(" {
			p.next()
			x, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			return x, nil
		}
		return nil, p.fail(t)
	case kqlIdent:
		p.next()
		switch t.text {
		case "true":
			return kqlLiteral{pos: t.pos, typ: "bool", val: true}, nil
		case "false":
			return kqlLiteral{pos: t.pos, typ: "bool", val: false}, nil
		}
		if p.isPunct("(") {
			p.next()
			var args []kqlExpr
			if !p.isPunct(")") {
				for {
					if p.isPunct("*") && t.text == "count" {
						p.next()
					} else {
						arg, err := p.parseExpr()
						if err != nil {
							return nil, err
						}
						args = append(args, arg)
					}
					if !p.isPunct(",") {
						break
					}
					p.next()
				}
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			return kqlCall{pos: t.pos, name: t.text, args: args}, nil
		}
		if kqlReservedWords[t.text] {
			return nil, p.fail(t)
		}
		return kqlColumnRef{pos: t.pos, name: t.text}, nil
	}
	return nil, p.fail(t)
}

// kqlReservedWords cannot name a column without bracket quoting, so meeting
// one where an expression belongs is a syntax error.
var kqlReservedWords = map[string]bool{
	"and": true, "or": true, "by": true, "asc": true, "desc": true, "nulls": true,
	"where": true, "project": true, "extend": true, "summarize": true, "take": true,
	"limit": true, "order": true, "sort": true, "top": true, "distinct": true,
	"let": true, "in": true, "between": true, "has": true, "contains": true,
}

// parseKQLDatetime reads the content of a datetime() literal in the forms
// Kusto accepts: ISO 8601 with or without a zone, a date alone, and a date and
// time separated by a space.
func parseKQLDatetime(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
