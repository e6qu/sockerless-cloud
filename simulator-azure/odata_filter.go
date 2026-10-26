package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// azureApplyListQuery applies `$filter` and `$orderby`, keying each match by
// its $orderby values and then its id. The $filter grammar is the OData subset
// Azure Resource Manager clients use:
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
func azureApplyListQuery[T any](items []T, r *http.Request, id func(T) string) ([]azureKeyed[T], error) {
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
	keyed := make([]azureKeyed[T], 0, len(items))
	for _, item := range items {
		if node == nil && len(order) == 0 {
			keyed = append(keyed, azureKeyed[T]{id(item), item})
			continue
		}
		doc, err := listq.ToDoc(item)
		if err != nil {
			return nil, err
		}
		if node != nil && !node.Eval(doc) {
			continue
		}
		var key []byte
		for _, term := range order {
			value, _ := listq.Field(doc, term.Path, "/")
			key = append(key, azureOrderTermKey(value, term.Desc)...)
		}
		keyed = append(keyed, azureKeyed[T]{string(key) + id(item), item})
	}
	sort.Slice(keyed, func(i, j int) bool { return keyed[i].key < keyed[j].key })
	return keyed, nil
}

// Ties break by id, which must be unique within items.
func armQueryPage[T any](w http.ResponseWriter, r *http.Request, items []T, id func(T) string) ([]T, string, bool) {
	keyed, err := azureApplyListQuery(items, r, id)
	if err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return nil, "", false
	}
	return armKeyedPage(w, r, keyed)
}

// Byte order matches listq.CompareOrdered: a number orders numerically and
// ahead of any string. Every spelling is prefix-free, so a descending term can
// invert its bytes and terms can concatenate.
func azureOrderTermKey(value string, desc bool) []byte {
	var key []byte
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		b := math.Float64bits(f)
		if b>>63 == 1 {
			b = ^b
		} else {
			b |= 1 << 63
		}
		key = append([]byte{1}, fmt.Sprintf("%020d", b)...)
	} else {
		key = []byte{2}
		for i := 0; i < len(value); i++ {
			if value[i] == 0 {
				key = append(key, 0, 0xff)
				continue
			}
			key = append(key, value[i])
		}
		key = append(key, 0, 1)
	}
	if desc {
		for i := range key {
			key[i] = ^key[i]
		}
	}
	return key
}

func odataCmp(field, op, value string) listq.Node {
	return listq.Cmp{Path: field, Sep: "/", Test: odataTest(op, value)}
}

func odataTest(op, value string) listq.Test {
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
	return test
}

// Each element, or its member Sub, is compared with Test.
type odataLambda struct {
	Path, Sub string
	All       bool
	Test      listq.Test
}

func (n odataLambda) Eval(d listq.Doc) bool {
	v, ok := listq.Lookup(d, n.Path, "/")
	if !ok {
		return n.All
	}
	items, _ := v.([]any)
	for _, item := range items {
		value, present := item, true
		if n.Sub != "" {
			m, isObject := item.(map[string]any)
			if isObject {
				value, present = listq.Lookup(m, n.Sub, "/")
			} else {
				present = false
			}
		}
		matched := n.Test(listq.ScalarString(value), present)
		if matched && !n.All {
			return true
		}
		if !matched && n.All {
			return false
		}
	}
	return n.All
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
			// OData wraps a typed value as `<type>'<value>'` (datetime'…', guid'…', X'…',
			// binary'…'); emit the unwrapped value as one string token.
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
			// Strip a numeric type suffix (123L, 1.5f, 2.0d, 9.99m).
			if stripped, ok := odataStripNumericSuffix(word); ok {
				word = stripped
			}
			toks = append(toks, odataTok{odataWord, word})
		}
	}
	return append(toks, odataTok{odataEOF, ""})
}

func odataIsTypedLiteralPrefix(word string) bool {
	switch strings.ToLower(word) {
	case "datetime", "datetimeoffset", "guid", "binary", "x", "time", "duration":
		return true
	}
	return false
}

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

type odataParser struct {
	toks  []odataTok
	pos   int
	guard *sim.ParseGuard
	err   error
}

// Keep the earliest, most specific error.
func (p *odataParser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

// A Go stack overflow is fatal and unrecoverable, so bound parenthesis nesting.
const maxODataParseDepth = 1000

// Azure rejects a malformed filter with 400 ("Invalid $filter") rather than
// matching everything; an empty filter matches everything.
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
	if p.peek().kind == odataWord {
		switch strings.ToLower(p.peek().text) {
		case "startswith", "endswith", "contains":
			name := strings.ToLower(p.next().text)
			field, value := p.parseFuncFieldValue()
			return odataFunc(name, field, value)
		case "substringof":
			p.next()
			value, field := p.parseFuncValueField()
			return odataFunc("substringof", field, value)
		}
	}
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a field name, got %q", p.peek().text)
		return listq.True{}
	}
	field := p.next().text
	if p.peek().kind == odataLParen {
		if i := strings.LastIndex(field, "/"); i > 0 && odataIsLambda(field[i+1:]) {
			return p.parseLambda(field[:i], strings.EqualFold(field[i+1:], "all"))
		}
	}
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

func odataIsLambda(word string) bool {
	return strings.EqualFold(word, "any") || strings.EqualFold(word, "all")
}

// collection/any(x:x op value) or collection/all(x:x/member op value).
func (p *odataParser) parseLambda(collection string, all bool) listq.Node {
	p.next()
	if p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a lambda variable after '('")
		return listq.True{}
	}
	variable, operand, ok := strings.Cut(p.next().text, ":")
	if !ok || variable == "" {
		p.fail("Invalid syntax in $filter: a lambda must read variable:expression")
		return listq.True{}
	}
	sub := ""
	if operand != variable {
		rest, found := strings.CutPrefix(operand, variable+"/")
		if !found {
			p.fail("Invalid syntax in $filter: the lambda expression must use its variable %q", variable)
			return listq.True{}
		}
		sub = rest
	}
	if p.peek().kind != odataWord || !odataIsComparisonOp(strings.ToLower(p.peek().text)) {
		p.fail("Invalid syntax in $filter: expected a comparison operator in the lambda")
		return listq.True{}
	}
	op := strings.ToLower(p.next().text)
	if p.peek().kind != odataString && p.peek().kind != odataWord {
		p.fail("Invalid syntax in $filter: expected a value in the lambda")
		return listq.True{}
	}
	value := p.next().text
	if p.peek().kind != odataRParen {
		p.fail("Invalid syntax in $filter: missing ')' after the lambda")
		return listq.True{}
	}
	p.next()
	return odataLambda{Path: collection, Sub: sub, All: all, Test: odataTest(op, value)}
}

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
