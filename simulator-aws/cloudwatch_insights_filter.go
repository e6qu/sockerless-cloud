package main

import (
	"fmt"
	"regexp"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudWatch Logs Insights `filter` expression grammar:
//
//	expr   = or
//	or     = and { "or" and }
//	and    = not { "and" not }
//	not    = ["not"] term
//	term   = "(" expr ")" | comparison | restriction
//	comparison = field (= | != | < | <= | > | >= | like | in) value
//	restriction = field                       (bare field → present & truthy)
//	value  = "string" | number | /regex/ | [ v, v, … ]   (in)
//
// Evaluated against a flattened record (field → string).

type cwInsightsNode interface {
	eval(rec cwInsightsRecord) bool
}

type cwInsTrue struct{}

func (cwInsTrue) eval(cwInsightsRecord) bool { return true }

type cwInsOr struct{ l, r cwInsightsNode }

func (n cwInsOr) eval(rec cwInsightsRecord) bool { return n.l.eval(rec) || n.r.eval(rec) }

type cwInsAnd struct{ l, r cwInsightsNode }

func (n cwInsAnd) eval(rec cwInsightsRecord) bool { return n.l.eval(rec) && n.r.eval(rec) }

type cwInsNot struct{ inner cwInsightsNode }

func (n cwInsNot) eval(rec cwInsightsRecord) bool { return !n.inner.eval(rec) }

type cwInsCmp struct {
	field string
	op    string
	value string
	list  []string
	re    *regexp.Regexp
}

func (n cwInsCmp) eval(rec cwInsightsRecord) bool {
	actual, present := rec[n.field]
	switch n.op {
	case "":
		return present && actual != "" && actual != "false" && actual != "0"
	case "=", "==":
		return actual == n.value
	case "!=":
		return actual != n.value
	case "<", "<=", ">", ">=":
		return cwNumCompare(actual, n.op, n.value)
	case "like":
		if n.re != nil {
			return n.re.MatchString(actual)
		}
		return strings.Contains(actual, n.value)
	case "in":
		for _, v := range n.list {
			if actual == v {
				return true
			}
		}
		return false
	}
	return false
}

// ── tokenizer ──────────────────────────────────────────────────────────────

type cwInsTokKind int

const (
	cwInsEOF cwInsTokKind = iota
	cwInsLParen
	cwInsRParen
	cwInsLBracket
	cwInsRBracket
	cwInsComma
	cwInsOp
	cwInsAndKw
	cwInsOrKw
	cwInsNotKw
	cwInsLikeKw
	cwInsInKw
	cwInsWord
	cwInsString
	cwInsRegex
)

type cwInsTok struct {
	kind cwInsTokKind
	text string
}

func cwInsTokenize(s string) []cwInsTok {
	var toks []cwInsTok
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Peek()
		switch c {
		case ' ', '\t', '\n':
			sc.Next()
		case '(':
			toks = append(toks, cwInsTok{cwInsLParen, "("})
			sc.Next()
		case ')':
			toks = append(toks, cwInsTok{cwInsRParen, ")"})
			sc.Next()
		case '[':
			toks = append(toks, cwInsTok{cwInsLBracket, "["})
			sc.Next()
		case ']':
			toks = append(toks, cwInsTok{cwInsRBracket, "]"})
			sc.Next()
		case ',':
			toks = append(toks, cwInsTok{cwInsComma, ","})
			sc.Next()
		case '=', '!', '<', '>':
			op := string(c)
			sc.Next()
			if sc.Peek() == '=' {
				op += "="
				sc.Next()
			}
			toks = append(toks, cwInsTok{cwInsOp, op})
		case '"', '\'':
			q := c
			sc.Next()
			start := sc.Pos()
			for !sc.Eof() && sc.Peek() != q {
				sc.Next()
			}
			toks = append(toks, cwInsTok{cwInsString, sc.Slice(start, sc.Pos())})
			if !sc.Eof() {
				sc.Next()
			}
		case '/':
			sc.Next()
			start := sc.Pos()
			for !sc.Eof() && sc.Peek() != '/' {
				sc.Next()
			}
			toks = append(toks, cwInsTok{cwInsRegex, sc.Slice(start, sc.Pos())})
			if !sc.Eof() {
				sc.Next()
			}
		default:
			start := sc.Pos()
			for !sc.Eof() {
				ch := sc.Peek()
				if ch == ' ' || ch == '\t' || ch == '\n' || ch == '(' || ch == ')' ||
					ch == '[' || ch == ']' || ch == ',' || ch == '=' || ch == '!' || ch == '<' || ch == '>' || ch == '/' {
					break
				}
				sc.Next()
			}
			word := sc.Slice(start, sc.Pos())
			switch strings.ToLower(word) {
			case "and":
				toks = append(toks, cwInsTok{cwInsAndKw, word})
			case "or":
				toks = append(toks, cwInsTok{cwInsOrKw, word})
			case "not":
				toks = append(toks, cwInsTok{cwInsNotKw, word})
			case "like":
				toks = append(toks, cwInsTok{cwInsLikeKw, word})
			case "in":
				toks = append(toks, cwInsTok{cwInsInKw, word})
			default:
				toks = append(toks, cwInsTok{cwInsWord, word})
			}
		}
	}
	return append(toks, cwInsTok{cwInsEOF, ""})
}

// ── parser ─────────────────────────────────────────────────────────────────

type cwInsParser struct {
	toks  []cwInsTok
	pos   int
	guard *sim.ParseGuard
	err   error
}

func (p *cwInsParser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func cwParseInsightsFilter(s string) (cwInsightsNode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return cwInsTrue{}, nil
	}
	p := &cwInsParser{toks: cwInsTokenize(s), guard: sim.NewParseGuard(maxExprParseDepth, 1<<62)}
	node := p.parseOr()
	if p.err == nil && p.peek().kind != cwInsEOF {
		p.fail("malformed query: unexpected token %q in filter", p.peek().text)
	}
	if p.err != nil {
		return nil, p.err
	}
	return node, nil
}

func (p *cwInsParser) peek() cwInsTok { return p.toks[p.pos] }
func (p *cwInsParser) next() cwInsTok { t := p.toks[p.pos]; p.pos++; return t }

func (p *cwInsParser) parseOr() cwInsightsNode {
	left := p.parseAnd()
	for p.peek().kind == cwInsOrKw {
		p.next()
		left = cwInsOr{left, p.parseAnd()}
	}
	return left
}

func (p *cwInsParser) parseAnd() cwInsightsNode {
	left := p.parseNot()
	for p.peek().kind == cwInsAndKw {
		p.next()
		left = cwInsAnd{left, p.parseNot()}
	}
	return left
}

func (p *cwInsParser) parseNot() cwInsightsNode {
	if p.peek().kind == cwInsNotKw {
		p.next()
		return cwInsNot{p.parseNot()}
	}
	return p.parseTerm()
}

func (p *cwInsParser) parseTerm() cwInsightsNode {
	if p.peek().kind == cwInsLParen {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			p.fail("malformed query: filter nesting too deep")
			return cwInsTrue{}
		}
		inner := p.parseOr()
		p.guard.Leave()
		if p.peek().kind == cwInsRParen {
			p.next()
		} else {
			p.fail("malformed query: expected ')'")
		}
		return inner
	}
	if p.peek().kind != cwInsWord {
		if p.peek().kind == cwInsEOF {
			p.fail("malformed query: unexpected end of filter")
		} else {
			p.fail("malformed query: unexpected token %q in filter", p.peek().text)
			p.next()
		}
		return cwInsTrue{}
	}
	field := p.next().text
	switch p.peek().kind {
	case cwInsOp:
		op := p.next().text
		return cwInsCmp{field: field, op: op, value: p.parseValue()}
	case cwInsLikeKw:
		p.next()
		if p.peek().kind == cwInsRegex {
			re, err := regexp.Compile(p.next().text)
			if err != nil {
				p.fail("malformed query: invalid regex in like: %v", err)
				return cwInsCmp{field: field, op: "like", value: ""}
			}
			return cwInsCmp{field: field, op: "like", re: re}
		}
		return cwInsCmp{field: field, op: "like", value: p.parseValue()}
	case cwInsInKw:
		p.next()
		var list []string
		if p.peek().kind == cwInsLBracket {
			p.next()
			for p.peek().kind != cwInsRBracket && p.peek().kind != cwInsEOF {
				list = append(list, p.parseValue())
				if p.peek().kind == cwInsComma {
					p.next()
				}
			}
			if p.peek().kind == cwInsRBracket {
				p.next()
			} else {
				p.fail("malformed query: expected ']' to close in [...]")
			}
		} else {
			p.fail("malformed query: expected '[' after in")
		}
		return cwInsCmp{field: field, op: "in", list: list}
	}
	// A bare field with no operator is a valid Insights filter (presence/truthy
	// test), so this is not an error.
	return cwInsCmp{field: field, op: ""}
}

func (p *cwInsParser) parseValue() string {
	switch p.peek().kind {
	case cwInsString, cwInsWord:
		return p.next().text
	case cwInsRegex:
		return p.next().text
	}
	if p.peek().kind != cwInsEOF {
		return p.next().text
	}
	return ""
}
