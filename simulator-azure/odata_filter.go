package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// azureApplyListQuery applies `$filter` and `$orderby` to items. The $filter
// grammar is the Azure Resource Manager / OData subset clients use:
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
	var node listq.Node
	if filter := strings.TrimSpace(r.URL.Query().Get("$filter")); filter != "" {
		parsed, err := azureParseODataFilter(filter)
		if err != nil {
			return nil, err
		}
		node = parsed
	}
	order, err := listq.ParseOrderBy(r.URL.Query().Get("$orderby"), true)
	if err != nil {
		return nil, fmt.Errorf("invalid $orderby: %w", err)
	}
	return listq.ApplyList(items, node, order, "/")
}

func odataCmp(field, op, value string) listq.Node {
	var test listq.Test
	switch op {
	case "eq":
		test = func(v string, present bool) bool { return present && v == value }
	case "ne":
		test = func(v string, present bool) bool { return !present || v != value }
	case "gt":
		test = func(v string, present bool) bool { return present && listq.CompareOrdered(v, value) > 0 }
	case "ge":
		test = func(v string, present bool) bool { return present && listq.CompareOrdered(v, value) >= 0 }
	case "lt":
		test = func(v string, present bool) bool { return present && listq.CompareOrdered(v, value) < 0 }
	case "le":
		test = func(v string, present bool) bool { return present && listq.CompareOrdered(v, value) <= 0 }
	}
	return listq.Cmp{Path: field, Sep: "/", Test: test}
}

func odataFunc(name, field, value string) listq.Node {
	var test listq.Test
	switch name {
	case "startswith":
		test = func(v string, present bool) bool { return present && strings.HasPrefix(v, value) }
	case "endswith":
		test = func(v string, present bool) bool { return present && strings.HasSuffix(v, value) }
	default:
		test = func(v string, present bool) bool { return present && strings.Contains(v, value) }
	}
	return listq.Cmp{Path: field, Sep: "/", Test: test}
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
func azureParseODataFilter(s string) (listq.Node, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return listq.True{}, nil
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

func (p *odataParser) parseOr() listq.Node {
	left := p.parseAnd()
	for p.err == nil && p.isKeyword("or") {
		p.next()
		left = listq.Or{L: left, R: p.parseAnd()}
	}
	return left
}

func (p *odataParser) parseAnd() listq.Node {
	left := p.parseNot()
	for p.err == nil && p.isKeyword("and") {
		p.next()
		left = listq.And{L: left, R: p.parseNot()}
	}
	return left
}

func (p *odataParser) parseNot() listq.Node {
	if p.err == nil && p.isKeyword("not") {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			p.fail("Invalid syntax in $filter: expression nesting too deep")
			return listq.True{}
		}
		inner := p.parseNot()
		p.guard.Leave()
		return listq.Not{Inner: inner}
	}
	return p.parseTerm()
}

func (p *odataParser) parseTerm() listq.Node {
	if p.err != nil {
		return listq.True{}
	}
	if p.peek().kind == odataLParen {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			p.fail("Invalid syntax in $filter: expression nesting too deep")
			return listq.True{}
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
			return odataFunc(name, field, value)
		case "substringof":
			p.next()
			// substringof('value', field)
			value, field := p.parseFuncValueField()
			return odataFunc("substringof", field, value)
		}
	}
	// comparison: field op value
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a field name, got %q", p.peek().text)
		return listq.True{}
	}
	field := p.next().text
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a comparison operator after %q", field)
		return listq.True{}
	}
	op := strings.ToLower(p.next().text)
	if !odataIsComparisonOp(op) {
		p.fail("Invalid syntax in $filter: unknown operator %q", op)
		return listq.True{}
	}
	if p.peek().kind != odataString && p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a value after %q %s", field, op)
		return listq.True{}
	}
	value := p.next().text
	return odataCmp(field, op, value)
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
