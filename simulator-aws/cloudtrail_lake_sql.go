package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CloudTrail Lake runs Trino SELECT statements over an event data store. The
// evaluator covers one table and the syntax the CloudTrail Lake SQL
// constraints page lists: SELECT [DISTINCT], WHERE with its condition
// operators, GROUP BY with aggregates, HAVING, ORDER BY and LIMIT.

type ctSQLKind int

const (
	ctTokIdent ctSQLKind = iota
	ctTokString
	ctTokNumber
	ctTokOp
	ctTokEOF
)

type ctSQLToken struct {
	kind ctSQLKind
	text string
}

func ctSQLTokenize(s string) ([]ctSQLToken, error) {
	var toks []ctSQLToken
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			var b strings.Builder
			j := i + 1
			for {
				if j >= len(s) {
					return nil, fmt.Errorf("unterminated string literal")
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						b.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(s[j])
				j++
			}
			toks = append(toks, ctSQLToken{ctTokString, b.String()})
			i = j + 1
		case c == '"' || c == '`':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			toks = append(toks, ctSQLToken{ctTokIdent, s[i+1 : i+1+end]})
			i += end + 2
		case ctSQLWordByte(c):
			// Event data store IDs are UUIDs, so a word keeps its hyphens.
			j := i
			for j < len(s) && (ctSQLWordByte(s[j]) || s[j] == '.' || s[j] == '-') {
				j++
			}
			word := s[i:j]
			if _, err := strconv.ParseFloat(word, 64); err == nil {
				toks = append(toks, ctSQLToken{ctTokNumber, word})
			} else {
				toks = append(toks, ctSQLToken{ctTokIdent, word})
			}
			i = j
		default:
			two := ""
			if i+1 < len(s) {
				two = s[i : i+2]
			}
			switch two {
			case "<=", ">=", "<>", "!=":
				toks = append(toks, ctSQLToken{ctTokOp, two})
				i += 2
				continue
			}
			if strings.ContainsRune("=<>(),*;", rune(c)) {
				toks = append(toks, ctSQLToken{ctTokOp, string(c)})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return append(toks, ctSQLToken{kind: ctTokEOF}), nil
}

func ctSQLWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ctSQLValue is NULL when null is set; numbers compare numerically.
type ctSQLValue struct {
	null  bool
	str   string
	num   float64
	isNum bool
	isB   bool
	b     bool
}

func (v ctSQLValue) String() string {
	switch {
	case v.null:
		return ""
	case v.isB:
		return strconv.FormatBool(v.b)
	case v.isNum:
		return strconv.FormatFloat(v.num, 'f', -1, 64)
	}
	return v.str
}

func ctSQLCompare(a, b ctSQLValue) int {
	if a.isNum && b.isNum {
		switch {
		case a.num < b.num:
			return -1
		case a.num > b.num:
			return 1
		}
		return 0
	}
	return strings.Compare(a.String(), b.String())
}

type ctSQLExpr interface {
	eval(row map[string]ctSQLValue, group []map[string]ctSQLValue) ctSQLValue
}

type ctSQLCol struct{ name string }
type ctSQLLit struct{ v ctSQLValue }
type ctSQLNot struct{ x ctSQLExpr }
type ctSQLBin struct {
	op   string
	l, r ctSQLExpr
}
type ctSQLIn struct {
	x    ctSQLExpr
	list []ctSQLExpr
	not  bool
}
type ctSQLLike struct {
	x       ctSQLExpr
	pattern ctSQLExpr
	not     bool
}
type ctSQLBetween struct {
	x, lo, hi ctSQLExpr
	not       bool
}
type ctSQLIsNull struct {
	x   ctSQLExpr
	not bool
}
type ctSQLCall struct {
	fn   string
	star bool
	args []ctSQLExpr
}

func ctBool(b bool) ctSQLValue { return ctSQLValue{isB: true, b: b} }

func ctTruthy(v ctSQLValue) bool { return v.isB && v.b }

func (c ctSQLCol) eval(row map[string]ctSQLValue, _ []map[string]ctSQLValue) ctSQLValue {
	if v, ok := row[strings.ToLower(c.name)]; ok {
		return v
	}
	return ctSQLValue{null: true}
}

func (l ctSQLLit) eval(map[string]ctSQLValue, []map[string]ctSQLValue) ctSQLValue { return l.v }

func (n ctSQLNot) eval(row map[string]ctSQLValue, g []map[string]ctSQLValue) ctSQLValue {
	v := n.x.eval(row, g)
	if v.null {
		return v
	}
	return ctBool(!ctTruthy(v))
}

func (b ctSQLBin) eval(row map[string]ctSQLValue, g []map[string]ctSQLValue) ctSQLValue {
	switch b.op {
	case "and":
		l, r := b.l.eval(row, g), b.r.eval(row, g)
		if (!l.null && !ctTruthy(l)) || (!r.null && !ctTruthy(r)) {
			return ctBool(false)
		}
		if l.null || r.null {
			return ctSQLValue{null: true}
		}
		return ctBool(true)
	case "or":
		l, r := b.l.eval(row, g), b.r.eval(row, g)
		if ctTruthy(l) || ctTruthy(r) {
			return ctBool(true)
		}
		if l.null || r.null {
			return ctSQLValue{null: true}
		}
		return ctBool(false)
	}
	l, r := b.l.eval(row, g), b.r.eval(row, g)
	switch b.op {
	case "is distinct from":
		return ctBool(l.null != r.null || !l.null && ctSQLCompare(l, r) != 0)
	case "is not distinct from":
		return ctBool(l.null == r.null && (l.null || ctSQLCompare(l, r) == 0))
	}
	if l.null || r.null {
		return ctSQLValue{null: true}
	}
	c := ctSQLCompare(l, r)
	switch b.op {
	case "=":
		return ctBool(c == 0)
	case "<>", "!=":
		return ctBool(c != 0)
	case "<":
		return ctBool(c < 0)
	case "<=":
		return ctBool(c <= 0)
	case ">":
		return ctBool(c > 0)
	case ">=":
		return ctBool(c >= 0)
	}
	return ctSQLValue{null: true}
}

func (in ctSQLIn) eval(row map[string]ctSQLValue, g []map[string]ctSQLValue) ctSQLValue {
	x := in.x.eval(row, g)
	if x.null {
		return x
	}
	for _, e := range in.list {
		if v := e.eval(row, g); !v.null && ctSQLCompare(x, v) == 0 {
			return ctBool(!in.not)
		}
	}
	return ctBool(in.not)
}

func (l ctSQLLike) eval(row map[string]ctSQLValue, g []map[string]ctSQLValue) ctSQLValue {
	x, p := l.x.eval(row, g), l.pattern.eval(row, g)
	if x.null || p.null {
		return ctSQLValue{null: true}
	}
	var re strings.Builder
	re.WriteString("(?s)^")
	for _, ch := range p.String() {
		switch ch {
		case '%':
			re.WriteString(".*")
		case '_':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	re.WriteString("$")
	return ctBool(regexp.MustCompile(re.String()).MatchString(x.String()) != l.not)
}

func (b ctSQLBetween) eval(row map[string]ctSQLValue, g []map[string]ctSQLValue) ctSQLValue {
	x, lo, hi := b.x.eval(row, g), b.lo.eval(row, g), b.hi.eval(row, g)
	if x.null || lo.null || hi.null {
		return ctSQLValue{null: true}
	}
	return ctBool((ctSQLCompare(x, lo) >= 0 && ctSQLCompare(x, hi) <= 0) != b.not)
}

func (n ctSQLIsNull) eval(row map[string]ctSQLValue, g []map[string]ctSQLValue) ctSQLValue {
	return ctBool(n.x.eval(row, g).null != n.not)
}

func ctSQLIsAggregate(fn string) bool {
	switch fn {
	case "count", "min", "max", "sum", "avg":
		return true
	}
	return false
}

func (c ctSQLCall) eval(row map[string]ctSQLValue, group []map[string]ctSQLValue) ctSQLValue {
	if ctSQLIsAggregate(c.fn) {
		var vals []ctSQLValue
		distinct := map[string]bool{}
		for _, r := range group {
			if c.star {
				vals = append(vals, ctBool(true))
				continue
			}
			if v := c.args[0].eval(r, nil); !v.null {
				vals = append(vals, v)
				distinct[v.String()] = true
			}
		}
		switch c.fn {
		case "count":
			return ctSQLValue{isNum: true, num: float64(len(vals))}
		case "min", "max":
			if len(vals) == 0 {
				return ctSQLValue{null: true}
			}
			best := vals[0]
			for _, v := range vals[1:] {
				if c.fn == "min" && ctSQLCompare(v, best) < 0 || c.fn == "max" && ctSQLCompare(v, best) > 0 {
					best = v
				}
			}
			return best
		case "sum", "avg":
			if len(vals) == 0 {
				return ctSQLValue{null: true}
			}
			total := 0.0
			for _, v := range vals {
				total += v.num
			}
			if c.fn == "avg" {
				total /= float64(len(vals))
			}
			return ctSQLValue{isNum: true, num: total}
		}
	}
	args := make([]ctSQLValue, len(c.args))
	for i, a := range c.args {
		args[i] = a.eval(row, group)
	}
	switch c.fn {
	case "lower", "upper":
		if len(args) != 1 || args[0].null {
			return ctSQLValue{null: true}
		}
		if c.fn == "lower" {
			return ctSQLValue{str: strings.ToLower(args[0].String())}
		}
		return ctSQLValue{str: strings.ToUpper(args[0].String())}
	case "greatest", "least":
		var best ctSQLValue
		for i, a := range args {
			if a.null {
				return a
			}
			if i == 0 || c.fn == "greatest" && ctSQLCompare(a, best) > 0 || c.fn == "least" && ctSQLCompare(a, best) < 0 {
				best = a
			}
		}
		return best
	}
	return ctSQLValue{null: true}
}

type ctSQLSelectItem struct {
	expr  ctSQLExpr
	star  bool
	alias string
}

type ctSQLOrder struct {
	expr ctSQLExpr
	desc bool
}

type ctSQLQuery struct {
	distinct bool
	items    []ctSQLSelectItem
	from     string
	where    ctSQLExpr
	groupBy  []ctSQLExpr
	having   ctSQLExpr
	orderBy  []ctSQLOrder
	limit    int
}

type ctSQLParser struct {
	toks []ctSQLToken
	pos  int
	err  error
}

func (p *ctSQLParser) peek() ctSQLToken { return p.toks[p.pos] }

func (p *ctSQLParser) next() ctSQLToken {
	t := p.toks[p.pos]
	if t.kind != ctTokEOF {
		p.pos++
	}
	return t
}

func (p *ctSQLParser) isKeyword(words ...string) bool {
	for i, w := range words {
		if p.pos+i >= len(p.toks) {
			return false
		}
		t := p.toks[p.pos+i]
		if t.kind != ctTokIdent || !strings.EqualFold(t.text, w) {
			return false
		}
	}
	return true
}

func (p *ctSQLParser) acceptKeyword(words ...string) bool {
	if p.isKeyword(words...) {
		p.pos += len(words)
		return true
	}
	return false
}

func (p *ctSQLParser) acceptOp(op string) bool {
	if t := p.peek(); t.kind == ctTokOp && t.text == op {
		p.pos++
		return true
	}
	return false
}

func (p *ctSQLParser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *ctSQLParser) expectOp(op string) {
	if !p.acceptOp(op) {
		p.fail("expected %q near %q", op, p.peek().text)
	}
}

var ctSQLReserved = map[string]bool{
	"select": true, "from": true, "where": true, "group": true, "by": true, "having": true,
	"order": true, "limit": true, "and": true, "or": true, "not": true, "in": true, "like": true,
	"between": true, "is": true, "null": true, "as": true, "asc": true, "desc": true, "distinct": true,
}

func ctSQLParse(statement string) (ctSQLQuery, error) {
	toks, err := ctSQLTokenize(statement)
	if err != nil {
		return ctSQLQuery{}, err
	}
	p := &ctSQLParser{toks: toks}
	var q ctSQLQuery
	if !p.acceptKeyword("select") {
		return q, fmt.Errorf("only SELECT statements are allowed")
	}
	q.distinct = p.acceptKeyword("distinct")
	for {
		if p.acceptOp("*") {
			q.items = append(q.items, ctSQLSelectItem{star: true})
		} else {
			item := ctSQLSelectItem{expr: p.parseOr()}
			if p.acceptKeyword("as") {
				item.alias = p.next().text
			} else if t := p.peek(); t.kind == ctTokIdent && !ctSQLReserved[strings.ToLower(t.text)] {
				item.alias = p.next().text
			}
			q.items = append(q.items, item)
		}
		if !p.acceptOp(",") {
			break
		}
	}
	if !p.acceptKeyword("from") {
		p.fail("the query statement does not name an event data store in its FROM clause")
	}
	q.from = p.next().text
	if t := p.peek(); t.kind == ctTokIdent && !ctSQLReserved[strings.ToLower(t.text)] {
		p.next()
	}
	if p.acceptKeyword("where") {
		q.where = p.parseOr()
	}
	if p.acceptKeyword("group", "by") {
		for {
			q.groupBy = append(q.groupBy, p.parseOr())
			if !p.acceptOp(",") {
				break
			}
		}
	}
	if p.acceptKeyword("having") {
		q.having = p.parseOr()
	}
	if p.acceptKeyword("order", "by") {
		for {
			o := ctSQLOrder{expr: p.parseOr()}
			if p.acceptKeyword("desc") {
				o.desc = true
			} else {
				p.acceptKeyword("asc")
			}
			q.orderBy = append(q.orderBy, o)
			if !p.acceptOp(",") {
				break
			}
		}
	}
	q.limit = -1
	if p.acceptKeyword("limit") {
		t := p.next()
		n, convErr := strconv.Atoi(t.text)
		if t.kind != ctTokNumber || convErr != nil {
			p.fail("LIMIT takes an integer, not %q", t.text)
		}
		q.limit = n
	}
	p.acceptOp(";")
	if t := p.peek(); t.kind != ctTokEOF {
		p.fail("unexpected %q", t.text)
	}
	return q, p.err
}

func (p *ctSQLParser) parseOr() ctSQLExpr {
	l := p.parseAnd()
	for p.acceptKeyword("or") {
		l = ctSQLBin{op: "or", l: l, r: p.parseAnd()}
	}
	return l
}

func (p *ctSQLParser) parseAnd() ctSQLExpr {
	l := p.parseNot()
	for p.acceptKeyword("and") {
		l = ctSQLBin{op: "and", l: l, r: p.parseNot()}
	}
	return l
}

func (p *ctSQLParser) parseNot() ctSQLExpr {
	if p.acceptKeyword("not") {
		return ctSQLNot{x: p.parseNot()}
	}
	return p.parsePredicate()
}

func (p *ctSQLParser) parsePredicate() ctSQLExpr {
	x := p.parsePrimary()
	if p.acceptKeyword("is") {
		switch {
		case p.acceptKeyword("not", "null"):
			return ctSQLIsNull{x: x, not: true}
		case p.acceptKeyword("null"):
			return ctSQLIsNull{x: x}
		case p.acceptKeyword("not", "distinct", "from"):
			return ctSQLBin{op: "is not distinct from", l: x, r: p.parsePrimary()}
		case p.acceptKeyword("distinct", "from"):
			return ctSQLBin{op: "is distinct from", l: x, r: p.parsePrimary()}
		}
		p.fail("unsupported IS predicate near %q", p.peek().text)
		return x
	}
	not := p.acceptKeyword("not")
	switch {
	case p.acceptKeyword("in"):
		p.expectOp("(")
		in := ctSQLIn{x: x, not: not}
		for {
			in.list = append(in.list, p.parsePrimary())
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
		return in
	case p.acceptKeyword("like"):
		return ctSQLLike{x: x, pattern: p.parsePrimary(), not: not}
	case p.acceptKeyword("between"):
		lo := p.parsePrimary()
		if !p.acceptKeyword("and") {
			p.fail("BETWEEN needs AND")
		}
		return ctSQLBetween{x: x, lo: lo, hi: p.parsePrimary(), not: not}
	}
	if not {
		p.fail("NOT must precede IN, LIKE or BETWEEN here")
	}
	if t := p.peek(); t.kind == ctTokOp {
		switch t.text {
		case "=", "<>", "!=", "<", "<=", ">", ">=":
			p.next()
			return ctSQLBin{op: t.text, l: x, r: p.parsePrimary()}
		}
	}
	return x
}

func (p *ctSQLParser) parsePrimary() ctSQLExpr {
	t := p.next()
	switch t.kind {
	case ctTokString:
		return ctSQLLit{ctSQLValue{str: t.text}}
	case ctTokNumber:
		n, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			p.fail("bad number %q", t.text)
		}
		return ctSQLLit{ctSQLValue{isNum: true, num: n}}
	case ctTokOp:
		if t.text == "(" {
			e := p.parseOr()
			p.expectOp(")")
			return e
		}
	case ctTokIdent:
		lower := strings.ToLower(t.text)
		switch lower {
		case "null":
			return ctSQLLit{ctSQLValue{null: true}}
		case "true", "false":
			return ctSQLLit{ctBool(lower == "true")}
		case "timestamp":
			if s := p.peek(); s.kind == ctTokString {
				p.next()
				return ctSQLLit{ctSQLValue{str: ctSQLTimestamp(s.text)}}
			}
		}
		if p.acceptOp("(") {
			call := ctSQLCall{fn: lower}
			if p.acceptOp("*") {
				call.star = true
			} else if !p.acceptOp(")") {
				for {
					call.args = append(call.args, p.parseOr())
					if !p.acceptOp(",") {
						break
					}
				}
			} else {
				return call
			}
			p.expectOp(")")
			switch {
			case ctSQLIsAggregate(lower) && (call.star && lower != "count" || !call.star && len(call.args) != 1):
				p.fail("%s takes one argument", lower)
			case !ctSQLIsAggregate(lower) && lower != "lower" && lower != "upper" && lower != "greatest" && lower != "least":
				p.fail("function %s is not supported", lower)
			}
			return call
		}
		return ctSQLCol{name: t.text}
	}
	p.fail("unexpected %q", t.text)
	return ctSQLLit{ctSQLValue{null: true}}
}

// Lake renders eventTime as "YYYY-MM-DD HH:MM:SS.sss", so string order is
// time order.
const ctSQLTimeLayout = "2006-01-02 15:04:05.000"

func ctSQLTimestamp(s string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(ctSQLTimeLayout)
		}
	}
	return s
}

// Columns follow the CloudTrail event record; userIdentity's fields are
// addressed with a dot.
func ctSQLEventRow(ev CloudTrailEvent) (map[string]ctSQLValue, []string) {
	str := func(s string) ctSQLValue {
		if s == "" {
			return ctSQLValue{null: true}
		}
		return ctSQLValue{str: s}
	}
	cols := []string{"eventId", "eventName", "eventSource", "eventTime", "awsRegion", "readOnly", "errorCode", "errorMessage", "userIdentity.userName", "userIdentity.accessKeyId", "userIdentity.invokedBy"}
	row := map[string]ctSQLValue{
		"eventid":                  str(ev.EventId),
		"eventname":                str(ev.EventName),
		"eventsource":              str(ev.EventSource),
		"eventtime":                str(ctSQLTimestamp(ev.EventTime)),
		"awsregion":                str(awsRegion()),
		"readonly":                 ctBool(ev.ReadOnly),
		"errorcode":                str(ev.ErrorCode),
		"errormessage":             str(ev.ErrorMessage),
		"useridentity.username":    str(ev.Username),
		"useridentity.accesskeyid": str(ev.AccessKeyId),
		"useridentity.invokedby":   str(ev.InvokedBy),
	}
	return row, cols
}

func ctSQLExprName(e ctSQLExpr, i int) string {
	if c, ok := e.(ctSQLCol); ok {
		return c.name
	}
	return fmt.Sprintf("_col%d", i)
}

// ctSQLRun returns the result rows as ordered single-entry cells and the
// count of events that passed WHERE.
func ctSQLRun(q ctSQLQuery, events []CloudTrailEvent) ([][]map[string]string, int64) {
	var rows []map[string]ctSQLValue
	var allCols []string
	for _, ev := range events {
		row, cols := ctSQLEventRow(ev)
		allCols = cols
		if q.where != nil && !ctTruthy(q.where.eval(row, nil)) {
			continue
		}
		rows = append(rows, row)
	}
	if allCols == nil {
		_, allCols = ctSQLEventRow(CloudTrailEvent{})
	}
	matched := int64(len(rows))

	aggregate := len(q.groupBy) > 0
	for _, it := range q.items {
		if c, ok := it.expr.(ctSQLCall); ok && ctSQLIsAggregate(c.fn) {
			aggregate = true
		}
	}
	type outRow struct {
		names []string
		vals  []ctSQLValue
		ctx   map[string]ctSQLValue
		group []map[string]ctSQLValue
	}
	project := func(rep map[string]ctSQLValue, group []map[string]ctSQLValue) outRow {
		o := outRow{ctx: map[string]ctSQLValue{}, group: group}
		for k, v := range rep {
			o.ctx[k] = v
		}
		for i, it := range q.items {
			if it.star {
				for _, c := range allCols {
					o.names = append(o.names, c)
					o.vals = append(o.vals, rep[strings.ToLower(c)])
				}
				continue
			}
			name := it.alias
			if name == "" {
				name = ctSQLExprName(it.expr, i)
			}
			v := it.expr.eval(rep, group)
			o.names = append(o.names, name)
			o.vals = append(o.vals, v)
			o.ctx[strings.ToLower(name)] = v
		}
		return o
	}
	var out []outRow
	if aggregate {
		order := []string{}
		groups := map[string][]map[string]ctSQLValue{}
		for _, row := range rows {
			var key strings.Builder
			for _, g := range q.groupBy {
				v := g.eval(row, nil)
				fmt.Fprintf(&key, "%t|%s\x00", v.null, v.String())
			}
			k := key.String()
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], row)
		}
		if len(q.groupBy) == 0 && len(groups) == 0 {
			order = append(order, "")
			groups[""] = nil
		}
		for _, k := range order {
			group := groups[k]
			rep := map[string]ctSQLValue{}
			if len(group) > 0 {
				rep = group[0]
			}
			if q.having != nil && !ctTruthy(q.having.eval(rep, group)) {
				continue
			}
			out = append(out, project(rep, group))
		}
	} else {
		for _, row := range rows {
			out = append(out, project(row, nil))
		}
	}
	if q.distinct {
		seen := map[string]bool{}
		kept := out[:0]
		for _, o := range out {
			var key strings.Builder
			for _, v := range o.vals {
				fmt.Fprintf(&key, "%t|%s\x00", v.null, v.String())
			}
			if !seen[key.String()] {
				seen[key.String()] = true
				kept = append(kept, o)
			}
		}
		out = kept
	}
	if len(q.orderBy) > 0 {
		sort.SliceStable(out, func(i, j int) bool {
			for _, ob := range q.orderBy {
				a, b := ob.expr.eval(out[i].ctx, out[i].group), ob.expr.eval(out[j].ctx, out[j].group)
				var c int
				switch {
				case a.null && b.null:
					c = 0
				case a.null:
					c = 1
				case b.null:
					c = -1
				default:
					c = ctSQLCompare(a, b)
					if ob.desc {
						c = -c
					}
				}
				if c != 0 {
					return c < 0
				}
			}
			return false
		})
	}
	if q.limit >= 0 && len(out) > q.limit {
		out = out[:q.limit]
	}
	result := make([][]map[string]string, 0, len(out))
	for _, o := range out {
		cells := make([]map[string]string, 0, len(o.vals))
		for i, v := range o.vals {
			if v.null {
				continue
			}
			cells = append(cells, map[string]string{o.names[i]: v.String()})
		}
		result = append(result, cells)
	}
	return result, matched
}
