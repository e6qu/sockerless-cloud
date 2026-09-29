package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/listq"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch Logs metric-filter pattern language (FilterLogEvents filterPattern),
// replacing the naive substring match. Two grammars, per the AWS docs:
//
//   - Unstructured (plain-text events): space-separated terms — all required
//     (AND); `?term` makes a term optional (OR group); `-term` excludes; quoted
//     "phrases" match as a substring of the raw message.
//   - Structured (pattern wrapped in {…}, JSON events): a boolean expression over
//     JSON selectors — `$.field`, nested `$.a.b`, array `$.a[0]` — with the
//     comparison operators = != < <= > >= (string equality supports a trailing
//     `*` wildcard) and the tests IS NULL, NOT EXISTS, IS TRUE and IS FALSE,
//     combined with && / || and parentheses.
//
// A malformed structured pattern is a loud error (the FilterLogEvents handler
// surfaces it as InvalidParameterException, exactly as real CloudWatch Logs),
// never a silent "matches nothing".
//
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html

// cwCompiledPattern is a parsed, validated filter pattern ready to test against
// many log events. A nil *cwCompiledPattern matches every event (empty pattern).
type cwCompiledPattern struct {
	structured listq.Node // non-nil for a {…} JSON pattern
	terms      []string   // unstructured terms (raw, incl. -/? prefixes & quotes)
}

// cwCompileLogPattern parses a filterPattern. A malformed structured pattern
// returns an error; an unstructured pattern (space-separated terms) is always
// well-formed.
func cwCompileLogPattern(pattern string) (*cwCompiledPattern, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	if strings.HasPrefix(pattern, "{") {
		if !strings.HasSuffix(pattern, "}") {
			return nil, fmt.Errorf("invalid filter pattern: unbalanced braces")
		}
		node, err := cwParseStructuredPattern(pattern[1 : len(pattern)-1])
		if err != nil {
			return nil, err
		}
		return &cwCompiledPattern{structured: node}, nil
	}
	return &cwCompiledPattern{terms: cwSplitPatternTerms(pattern)}, nil
}

// match reports whether the compiled pattern matches the event message. A nil
// receiver (empty pattern) matches every event.
func (c *cwCompiledPattern) match(message string) bool {
	if c == nil {
		return true
	}
	if c.structured != nil {
		var doc any
		if err := json.Unmarshal([]byte(message), &doc); err != nil {
			return false // a structured pattern only matches JSON events
		}
		return c.structured.Eval(listq.Doc{cwPatRoot: doc})
	}
	return cwMatchUnstructuredTerms(message, c.terms)
}

func cwMatchUnstructuredTerms(message string, terms []string) bool {
	anyOptional, optionalMatched := false, false
	for _, raw := range terms {
		neg, opt := false, false
		t := raw
		if strings.HasPrefix(t, "-") {
			neg, t = true, t[1:]
		} else if strings.HasPrefix(t, "?") {
			opt, t = true, t[1:]
		}
		t = strings.Trim(t, `"`)
		if t == "" {
			continue
		}
		contains := strings.Contains(message, t)
		switch {
		case neg:
			if contains {
				return false
			}
		case opt:
			anyOptional = true
			if contains {
				optionalMatched = true
			}
		default:
			if !contains {
				return false
			}
		}
	}
	if anyOptional && !optionalMatched {
		return false
	}
	return true
}

// cwSplitPatternTerms splits on whitespace, keeping "quoted phrases" together.
func cwSplitPatternTerms(s string) []string {
	var terms []string
	var cur strings.Builder
	inQuote := false
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Next()
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case (c == ' ' || c == '\t') && !inQuote:
			if cur.Len() > 0 {
				terms = append(terms, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		terms = append(terms, cur.String())
	}
	return terms
}

// cwParseStructuredPattern parses the body of a {…} structured pattern into an
// evaluable node, returning an error for a malformed pattern (unbalanced
// parentheses, a comparison missing its operator/value, trailing garbage, or
// nesting too deep) instead of silently matching nothing.
func cwParseStructuredPattern(expr string) (listq.Node, error) {
	p := &cwPatParser{toks: cwPatTokenize(expr), guard: sim.NewParseGuard(maxExprParseDepth, 1<<62)}
	node := p.parseOr()
	if p.err == nil && p.peek().kind != cwPatEOF {
		p.fail("invalid filter pattern: unexpected token %q", p.peek().text)
	}
	if p.err != nil {
		return nil, p.err
	}
	return node, nil
}

// cwPatRoot keys the decoded event in the document a structured pattern
// evaluates, so a selector can address a top-level array as well as an object.
const cwPatRoot = "$"

type cwPatCmp struct{ selector, op, value string }

func (n cwPatCmp) Eval(d listq.Doc) bool {
	actual, present := cwSelectJSON(d[cwPatRoot], n.selector)
	value := listq.ScalarString(actual)
	switch n.op {
	case "=":
		if strings.HasSuffix(n.value, "*") {
			return present && strings.HasPrefix(value, strings.TrimSuffix(n.value, "*"))
		}
		return present && value == n.value
	case "!=":
		return !present || value != n.value
	case "<":
		return present && listq.CompareOrdered(value, n.value) < 0
	case "<=":
		return present && listq.CompareOrdered(value, n.value) <= 0
	case ">":
		return present && listq.CompareOrdered(value, n.value) > 0
	case ">=":
		return present && listq.CompareOrdered(value, n.value) >= 0
	}
	return false
}

// cwPatTest is a structured pattern's keyword test on a selector: IS NULL
// matches a property whose value is null, NOT EXISTS one the event lacks, and
// IS TRUE / IS FALSE a Boolean property.
type cwPatTest struct{ selector, test string }

func (n cwPatTest) Eval(d listq.Doc) bool {
	actual, present := cwSelectJSON(d[cwPatRoot], n.selector)
	switch n.test {
	case "IS NULL":
		return present && actual == nil
	case "NOT EXISTS":
		return !present
	case "IS TRUE":
		return present && actual == true
	case "IS FALSE":
		return present && actual == false
	}
	return false
}

type cwPatKind int

const (
	cwPatEOF cwPatKind = iota
	cwPatLParen
	cwPatRParen
	cwPatAndOp
	cwPatOrOp
	cwPatOp
	cwPatWord
)

type cwPatTok struct {
	kind cwPatKind
	text string
}

func cwPatTokenize(s string) []cwPatTok {
	var toks []cwPatTok
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			sc.Next()
		case c == '(':
			toks = append(toks, cwPatTok{cwPatLParen, "("})
			sc.Next()
		case c == ')':
			toks = append(toks, cwPatTok{cwPatRParen, ")"})
			sc.Next()
		case c == '&' && sc.PeekAt(1) == '&':
			toks = append(toks, cwPatTok{cwPatAndOp, "&&"})
			sc.Next()
			sc.Next()
		case c == '|' && sc.PeekAt(1) == '|':
			toks = append(toks, cwPatTok{cwPatOrOp, "||"})
			sc.Next()
			sc.Next()
		case c == '=' || c == '!' || c == '<' || c == '>':
			op := string(c)
			sc.Next()
			if sc.Peek() == '=' {
				op += "="
				sc.Next()
			}
			toks = append(toks, cwPatTok{cwPatOp, op})
		case c == '"':
			sc.Next()
			start := sc.Pos()
			for !sc.Eof() && sc.Peek() != '"' {
				sc.Next()
			}
			toks = append(toks, cwPatTok{cwPatWord, sc.Slice(start, sc.Pos())})
			if !sc.Eof() {
				sc.Next()
			}
		default:
			start := sc.Pos()
			for !sc.Eof() {
				ch := sc.Peek()
				if ch == ' ' || ch == '\t' || ch == '\n' || ch == '(' || ch == ')' ||
					ch == '=' || ch == '!' || ch == '<' || ch == '>' || ch == '&' || ch == '|' {
					break
				}
				sc.Next()
			}
			if sc.Pos() == start {
				// A lone '&' / '|' (not doubled into && / ||) reaches here: it is
				// a delimiter for the word scanner but matches no operator case, so
				// it would be consumed zero times — an infinite loop. Emit the stray
				// byte as a word token and advance to guarantee forward progress.
				toks = append(toks, cwPatTok{cwPatWord, sc.Slice(start, start+1)})
				sc.Next()
				continue
			}
			toks = append(toks, cwPatTok{cwPatWord, sc.Slice(start, sc.Pos())})
		}
	}
	return append(toks, cwPatTok{cwPatEOF, ""})
}

type cwPatParser struct {
	toks  []cwPatTok
	pos   int
	guard *sim.ParseGuard
	err   error
}

func (p *cwPatParser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *cwPatParser) peek() cwPatTok { return p.toks[p.pos] }
func (p *cwPatParser) next() cwPatTok { t := p.toks[p.pos]; p.pos++; return t }

func (p *cwPatParser) parseOr() listq.Node {
	left := p.parseAnd()
	for p.err == nil && p.peek().kind == cwPatOrOp {
		p.next()
		left = listq.Or{L: left, R: p.parseAnd()}
	}
	return left
}

func (p *cwPatParser) parseAnd() listq.Node {
	left := p.parseTerm()
	for p.err == nil && p.peek().kind == cwPatAndOp {
		p.next()
		left = listq.And{L: left, R: p.parseTerm()}
	}
	return left
}

func (p *cwPatParser) parseTerm() listq.Node {
	if p.peek().kind == cwPatLParen {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			p.fail("invalid filter pattern: nesting too deep")
			return listq.True{}
		}
		inner := p.parseOr()
		p.guard.Leave()
		if p.peek().kind == cwPatRParen {
			p.next()
		} else {
			p.fail("invalid filter pattern: expected ')'")
		}
		return inner
	}
	if p.peek().kind != cwPatWord {
		if p.peek().kind == cwPatEOF {
			p.fail("invalid filter pattern: unexpected end of pattern")
		} else {
			p.fail("invalid filter pattern: unexpected token %q", p.peek().text)
			p.next()
		}
		return cwPatCmp{op: "="}
	}
	selector := p.next().text
	if keyword := p.peek(); keyword.kind == cwPatWord && (keyword.text == "IS" || keyword.text == "NOT") {
		p.next()
		operand := p.peek()
		test := keyword.text + " " + operand.text
		switch {
		case operand.kind == cwPatWord && (test == "IS NULL" || test == "IS TRUE" || test == "IS FALSE" || test == "NOT EXISTS"):
			p.next()
			return cwPatTest{selector: selector, test: test}
		case keyword.text == "IS":
			p.fail("invalid filter pattern: IS on %q must be followed by NULL, TRUE or FALSE", selector)
		default:
			p.fail("invalid filter pattern: NOT on %q must be followed by EXISTS", selector)
		}
		return listq.True{}
	}
	if p.peek().kind != cwPatOp {
		p.fail("invalid filter pattern: %q is missing a comparison", selector)
		return listq.True{}
	}
	op := p.next().text
	if p.peek().kind != cwPatWord {
		p.fail("invalid filter pattern: comparison on %q is missing its value", selector)
		return listq.True{}
	}
	return cwPatCmp{selector: selector, op: op, value: p.next().text}
}

// cwSelectJSON resolves a "$.a.b[0]" selector into a decoded JSON document.
func cwSelectJSON(doc any, selector string) (any, bool) {
	sel := strings.TrimPrefix(selector, "$")
	sel = strings.TrimPrefix(sel, ".")
	if sel == "" {
		return doc, true
	}
	cur := doc
	for _, seg := range cwSplitSelectorPath(sel) {
		if strings.HasPrefix(seg, "[") {
			idx, err := strconv.Atoi(strings.Trim(seg, "[]"))
			arr, ok := cur.([]any)
			if err != nil || !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func cwSplitSelectorPath(s string) []string {
	var segs []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			segs = append(segs, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.':
			flush()
		case '[':
			flush()
			if j := strings.IndexByte(s[i:], ']'); j >= 0 {
				segs = append(segs, s[i:i+j+1])
				i += j
			}
		default:
			cur.WriteByte(s[i])
		}
	}
	flush()
	return segs
}
