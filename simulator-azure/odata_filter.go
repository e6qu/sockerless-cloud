package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure ARM `$filter` (OData) support + the `$top`/`$skiptoken` and `$orderby`
// query options. azureApplyListQuery evaluates `$filter` against each resource's
// JSON, sorts by `$orderby`, then pages via armPage — so a list handler gets the
// full documented query-option surface from one call. Previously $filter was
// ignored (and most lists ignored $top too).
//
// $filter grammar (the ARM/OData subset clients use):
//
//	expr       = or
//	or         = and { "or" and }
//	and        = not { "and" not }
//	not        = ["not"] term
//	term       = "(" expr ")" | function | comparison
//	function   = startswith(field,'v') | endswith(field,'v') | contains(field,'v')
//	           | substringof('v',field)
//	comparison = field (eq|ne|gt|ge|lt|le) value
//	field      = name { "/" name }                 (nested via '/')
//	value      = 'string' | number | true | false | null
func azureApplyListQuery[T any](items []T, r *http.Request) ([]T, error) {
	filter := strings.TrimSpace(r.URL.Query().Get("$filter"))
	orderby := strings.TrimSpace(r.URL.Query().Get("$orderby"))

	if filter != "" {
		node, err := azureParseODataFilter(filter)
		if err != nil {
			return nil, err
		}
		kept := make([]T, 0, len(items))
		for _, it := range items {
			m, err := azureItemToMap(it)
			if err != nil {
				return nil, err
			}
			if node.eval(m) {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	if orderby != "" {
		field, desc := azureParseOrderBy(orderby)
		maps := make([]map[string]any, len(items))
		for i, it := range items {
			m, err := azureItemToMap(it)
			if err != nil {
				return nil, err
			}
			maps[i] = m
		}
		idx := make([]int, len(items))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			x, y := azureFieldString(maps[idx[a]], field), azureFieldString(maps[idx[b]], field)
			if desc {
				return x > y
			}
			return x < y
		})
		out := make([]T, len(items))
		for i, j := range idx {
			out[i] = items[j]
		}
		items = out
	}
	return items, nil
}

// azureItemToMap round-trips a list item through JSON into a generic map so the
// OData filter/orderby evaluator can read its fields. A round-trip failure means
// the sim's own resource value is corrupt — surface it loudly rather than
// evaluating the filter against an empty map (which would silently match or
// mis-sort).
func azureItemToMap[T any](it T) (map[string]any, error) {
	b, err := json.Marshal(it)
	if err != nil {
		return nil, fmt.Errorf("marshal list item for $filter/$orderby: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal list item for $filter/$orderby: %w", err)
	}
	return m, nil
}

func azureParseOrderBy(s string) (field string, desc bool) {
	s = strings.TrimSpace(strings.Split(s, ",")[0])
	if strings.HasSuffix(strings.ToLower(s), " desc") {
		return strings.TrimSpace(s[:len(s)-5]), true
	}
	if strings.HasSuffix(strings.ToLower(s), " asc") {
		return strings.TrimSpace(s[:len(s)-4]), false
	}
	return s, false
}

// ── AST ────────────────────────────────────────────────────────────────────

type odataNode interface{ eval(m map[string]any) bool }

type odataTrue struct{}

func (odataTrue) eval(map[string]any) bool { return true }

type odataOr struct{ l, r odataNode }

func (n odataOr) eval(m map[string]any) bool { return n.l.eval(m) || n.r.eval(m) }

type odataAnd struct{ l, r odataNode }

func (n odataAnd) eval(m map[string]any) bool { return n.l.eval(m) && n.r.eval(m) }

type odataNot struct{ inner odataNode }

func (n odataNot) eval(m map[string]any) bool { return !n.inner.eval(m) }

type odataCmp struct{ field, op, value string }

func (n odataCmp) eval(m map[string]any) bool {
	actual, present := azureFieldLookup(m, n.field)
	switch n.op {
	case "eq":
		return present && actual == n.value
	case "ne":
		return !present || actual != n.value
	case "gt", "ge", "lt", "le":
		return present && azureNumCompare(actual, n.op, n.value)
	}
	return false
}

type odataFunc struct{ name, field, value string }

func (n odataFunc) eval(m map[string]any) bool {
	actual, present := azureFieldLookup(m, n.field)
	if !present {
		return false
	}
	switch n.name {
	case "startswith":
		return strings.HasPrefix(actual, n.value)
	case "endswith":
		return strings.HasSuffix(actual, n.value)
	case "contains", "substringof":
		return strings.Contains(actual, n.value)
	}
	return false
}

// ── tokenizer ──────────────────────────────────────────────────────────────

type odataTokKind int

const (
	odataEOF odataTokKind = iota
	odataLParen
	odataRParen
	odataComma
	odataWord
	odataString
)

type odataTok struct {
	kind odataTokKind
	text string
}

func azureODataTokenize(s string) []odataTok {
	var toks []odataTok
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Peek()
		switch c {
		case ' ', '\t', '\n':
			sc.Next()
		case '(':
			toks = append(toks, odataTok{odataLParen, "("})
			sc.Next()
		case ')':
			toks = append(toks, odataTok{odataRParen, ")"})
			sc.Next()
		case ',':
			toks = append(toks, odataTok{odataComma, ","})
			sc.Next()
		case '\'':
			sc.Next()
			var b strings.Builder
			for !sc.Eof() {
				if sc.Peek() == '\'' {
					if sc.PeekAt(1) == '\'' { // '' escape
						b.WriteByte('\'')
						sc.Next()
						sc.Next()
						continue
					}
					break
				}
				b.WriteByte(sc.Next())
			}
			if !sc.Eof() {
				sc.Next()
			}
			toks = append(toks, odataTok{odataString, b.String()})
		default:
			start := sc.Pos()
			for !sc.Eof() {
				ch := sc.Peek()
				if ch == ' ' || ch == '\t' || ch == '\n' || ch == '(' || ch == ')' || ch == ',' || ch == '\'' {
					break
				}
				sc.Next()
			}
			word := sc.Slice(start, sc.Pos())
			// Typed-literal prefix: datetime'…' / guid'…' / X'…' / binary'…'.
			// Real OData wraps a typed value in `<type>'<value>'`; the inner
			// value is what the filter compares against. Recognise the prefix
			// (case-insensitive) immediately followed by a quote and emit a
			// single string token carrying the unwrapped value.
			if sc.Peek() == '\'' && odataIsTypedLiteralPrefix(word) {
				sc.Next() // opening quote
				var b strings.Builder
				for !sc.Eof() {
					if sc.Peek() == '\'' {
						if sc.PeekAt(1) == '\'' {
							b.WriteByte('\'')
							sc.Next()
							sc.Next()
							continue
						}
						break
					}
					b.WriteByte(sc.Next())
				}
				if !sc.Eof() {
					sc.Next() // closing quote
				}
				toks = append(toks, odataTok{odataString, b.String()})
				continue
			}
			// Numeric type suffix: 123L / 1.5f / 2.0d / 9.99m. Strip the suffix
			// and emit the bare number as a word (numeric comparison unwraps it).
			if stripped, ok := odataStripNumericSuffix(word); ok {
				word = stripped
			}
			toks = append(toks, odataTok{odataWord, word})
		}
	}
	return append(toks, odataTok{odataEOF, ""})
}

// odataIsTypedLiteralPrefix reports whether word is one of the OData typed-value
// prefixes that wrap their payload in quotes.
func odataIsTypedLiteralPrefix(word string) bool {
	switch strings.ToLower(word) {
	case "datetime", "datetimeoffset", "guid", "binary", "x", "time", "duration":
		return true
	}
	return false
}

// odataStripNumericSuffix removes a single OData numeric type suffix (L/f/d/m,
// case-insensitive) from an otherwise-numeric literal, returning the bare number.
func odataStripNumericSuffix(word string) (string, bool) {
	if len(word) < 2 {
		return word, false
	}
	last := word[len(word)-1]
	switch last {
	case 'L', 'l', 'f', 'F', 'd', 'D', 'm', 'M':
	default:
		return word, false
	}
	bare := word[:len(word)-1]
	if _, err := strconv.ParseFloat(bare, 64); err != nil {
		return word, false
	}
	return bare, true
}

// ── parser ─────────────────────────────────────────────────────────────────

type odataParser struct {
	toks  []odataTok
	pos   int
	guard *sim.ParseGuard
	err   error
}

// fail records the first parse error. Subsequent calls are no-ops so the
// earliest, most specific diagnostic survives.
func (p *odataParser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

// maxODataParseDepth bounds parenthesis nesting so a pathological $filter can't
// overflow the goroutine stack and crash the sim process (a Go stack-overflow
// fatal error is not recoverable).
const maxODataParseDepth = 1000

// azureParseODataFilter parses an ARM/OData `$filter` expression. A malformed
// filter is a client error: real Azure rejects it with HTTP 400 ("Invalid
// $filter") rather than matching every item, so this returns an error the
// callers surface as 400 BadRequest. An empty filter is the documented
// "no filter" case and matches everything.
func azureParseODataFilter(s string) (odataNode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return odataTrue{}, nil
	}
	p := &odataParser{toks: azureODataTokenize(s), guard: sim.NewParseGuard(maxODataParseDepth, -1)}
	node := p.parseOr()
	if p.err == nil && p.peek().kind != odataEOF {
		p.fail("Invalid syntax in $filter: unexpected token %q", p.peek().text)
	}
	if p.err != nil {
		return nil, p.err
	}
	return node, nil
}

func (p *odataParser) peek() odataTok { return p.toks[p.pos] }
func (p *odataParser) next() odataTok { t := p.toks[p.pos]; p.pos++; return t }

func (p *odataParser) isKeyword(kw string) bool {
	return p.peek().kind == odataWord && strings.EqualFold(p.peek().text, kw)
}

func (p *odataParser) parseOr() odataNode {
	left := p.parseAnd()
	for p.isKeyword("or") {
		p.next()
		left = odataOr{left, p.parseAnd()}
	}
	return left
}

func (p *odataParser) parseAnd() odataNode {
	left := p.parseNot()
	for p.isKeyword("and") {
		p.next()
		left = odataAnd{left, p.parseNot()}
	}
	return left
}

func (p *odataParser) parseNot() odataNode {
	if p.isKeyword("not") {
		p.next()
		return odataNot{p.parseNot()}
	}
	return p.parseTerm()
}

func (p *odataParser) parseTerm() odataNode {
	if p.err != nil {
		return odataTrue{}
	}
	if p.peek().kind == odataLParen {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			p.fail("Invalid syntax in $filter: expression nesting too deep")
			return odataTrue{}
		}
		inner := p.parseOr()
		p.guard.Leave()
		if p.peek().kind == odataRParen {
			p.next()
		} else {
			p.fail("Invalid syntax in $filter: missing ')'")
		}
		return inner
	}
	// Function call: name ( args )
	if p.peek().kind == odataWord {
		switch strings.ToLower(p.peek().text) {
		case "startswith", "endswith", "contains":
			name := strings.ToLower(p.next().text)
			field, value := p.parseFuncFieldValue()
			return odataFunc{name: name, field: field, value: value}
		case "substringof":
			p.next()
			// substringof('value', field)
			value, field := p.parseFuncValueField()
			return odataFunc{name: "substringof", field: field, value: value}
		}
	}
	// comparison: field op value
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a field name, got %q", p.peek().text)
		return odataTrue{}
	}
	field := p.next().text
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a comparison operator after %q", field)
		return odataTrue{}
	}
	op := strings.ToLower(p.next().text)
	if !odataIsComparisonOp(op) {
		p.fail("Invalid syntax in $filter: unknown operator %q", op)
		return odataTrue{}
	}
	if p.peek().kind != odataString && p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a value after %q %s", field, op)
		return odataTrue{}
	}
	value := p.next().text
	return odataCmp{field: field, op: op, value: value}
}

// odataIsComparisonOp reports whether op is one of the OData scalar comparison
// operators the filter grammar accepts.
func odataIsComparisonOp(op string) bool {
	switch op {
	case "eq", "ne", "gt", "ge", "lt", "le":
		return true
	}
	return false
}

func (p *odataParser) parseFuncFieldValue() (field, value string) {
	if p.peek().kind != odataLParen {
		p.fail("Invalid syntax in $filter: expected '(' after function name")
		return "", ""
	}
	p.next()
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a field name in function argument")
		return "", ""
	}
	field = p.next().text
	if p.peek().kind != odataComma {
		p.fail("Invalid syntax in $filter: expected ',' in function arguments")
		return field, ""
	}
	p.next()
	if p.peek().kind != odataString && p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a value in function argument")
		return field, ""
	}
	value = p.next().text
	if p.peek().kind != odataRParen {
		p.fail("Invalid syntax in $filter: missing ')' in function call")
		return field, value
	}
	p.next()
	return field, value
}

func (p *odataParser) parseFuncValueField() (value, field string) {
	if p.peek().kind != odataLParen {
		p.fail("Invalid syntax in $filter: expected '(' after function name")
		return "", ""
	}
	p.next()
	if p.peek().kind != odataString && p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a value in function argument")
		return "", ""
	}
	value = p.next().text
	if p.peek().kind != odataComma {
		p.fail("Invalid syntax in $filter: expected ',' in function arguments")
		return value, ""
	}
	p.next()
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a field name in function argument")
		return value, ""
	}
	field = p.next().text
	if p.peek().kind != odataRParen {
		p.fail("Invalid syntax in $filter: missing ')' in function call")
		return value, field
	}
	p.next()
	return value, field
}

// ── helpers ────────────────────────────────────────────────────────────────

func azureFieldLookup(m map[string]any, path string) (string, bool) {
	// OData paths nest via '/'.
	var cur any = m
	for _, seg := range strings.Split(path, "/") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		v, ok := mm[seg]
		if !ok {
			return "", false
		}
		cur = v
	}
	return azureScalarString(cur), true
}

func azureFieldString(m map[string]any, path string) string {
	v, _ := azureFieldLookup(m, path)
	return v
}

func azureScalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func azureNumCompare(a, op, b string) bool {
	af, aerr := strconv.ParseFloat(a, 64)
	bf, berr := strconv.ParseFloat(b, 64)
	if aerr != nil || berr != nil {
		switch op {
		case "gt":
			return a > b
		case "lt":
			return a < b
		case "ge":
			return a >= b
		case "le":
			return a <= b
		}
		return false
	}
	switch op {
	case "gt":
		return af > bf
	case "lt":
		return af < bf
	case "ge":
		return af >= bf
	case "le":
		return af <= bf
	}
	return false
}
