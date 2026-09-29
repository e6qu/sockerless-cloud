package main

import (
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// The Google Cloud list `filter` grammar (AIP-160):
//
//	filter      = expression
//	expression  = sequence { OR sequence }            (OR lowest precedence)
//	sequence    = factor { [AND] factor }             (AND explicit OR implicit/adjacent)
//	factor      = [ NOT | "-" ] term
//	term        = "(" expression ")" | comparison | restriction
//	comparison  = member operator value               operator ∈ = != < <= > >= :
//	restriction = member                              (bare member → truthy/has)
//	member      = name { "." name }                   (dotted field path)
//	value       = STRING | NUMBER | BOOL | name
//
// A filter the grammar does not admit is an error; Google Cloud answers it with
// INVALID_ARGUMENT.

func gcpFilterTest(op, value string) listq.Test {
	switch op {
	case "":
		return func(v string, present bool) bool {
			return present && v != "" && v != "false" && v != "0"
		}
	case "=":
		return func(v string, _ bool) bool { return v == value }
	case "!=":
		return func(v string, _ bool) bool { return v != value }
	case ":":
		if value == "*" {
			return func(_ string, present bool) bool { return present }
		}
		return func(v string, _ bool) bool { return strings.Contains(v, value) }
	case ">":
		return func(v string, _ bool) bool { return listq.CompareOrdered(v, value) > 0 }
	case ">=":
		return func(v string, _ bool) bool { return listq.CompareOrdered(v, value) >= 0 }
	case "<":
		return func(v string, _ bool) bool { return listq.CompareOrdered(v, value) < 0 }
	case "<=":
		return func(v string, _ bool) bool { return listq.CompareOrdered(v, value) <= 0 }
	}
	return nil
}

type gcpTokKind int

const (
	tokEOF gcpTokKind = iota
	tokLParen
	tokRParen
	tokAnd
	tokOr
	tokNot
	tokOp     // = != < <= > >= :
	tokWord   // identifier / field path / bare value
	tokString // quoted value
)

type gcpTok struct {
	kind gcpTokKind
	text string
}

func gcpTokenize(s string) ([]gcpTok, error) {
	var toks []gcpTok
	sc := sim.NewScanner(s)
	for !sc.Eof() {
		c := sc.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			sc.Next()
		case c == '(':
			toks = append(toks, gcpTok{tokLParen, "("})
			sc.Next()
		case c == ')':
			toks = append(toks, gcpTok{tokRParen, ")"})
			sc.Next()
		case c == '-' && (len(toks) == 0 || toks[len(toks)-1].kind == tokLParen || toks[len(toks)-1].kind == tokAnd || toks[len(toks)-1].kind == tokOr || toks[len(toks)-1].kind == tokNot):
			toks = append(toks, gcpTok{tokNot, "-"})
			sc.Next()
		case c == '=' || c == '!' || c == '<' || c == '>' || c == ':':
			sc.Next()
			op := string(c)
			if c != ':' && sc.Peek() == '=' {
				op += "="
				sc.Next()
			}
			if (op == "=" || op == "!") && sc.Peek() == '~' {
				op += "~"
				sc.Next()
			}
			if op == "!" {
				return nil, fmt.Errorf("invalid operator %q", op)
			}
			toks = append(toks, gcpTok{tokOp, op})
		case c == '"' || c == '\'':
			quote := c
			sc.Next()
			var b strings.Builder
			for !sc.Eof() && sc.Peek() != quote {
				if sc.Peek() == '\\' && sc.Pos()+1 < sc.Len() {
					sc.Next()
				}
				b.WriteByte(sc.Next())
			}
			if sc.Eof() {
				return nil, fmt.Errorf("unterminated string literal")
			}
			sc.Next()
			toks = append(toks, gcpTok{tokString, b.String()})
		default:
			start := sc.Pos()
			for !sc.Eof() {
				ch := sc.Peek()
				if ch == ' ' || ch == '\t' || ch == '\n' || ch == '(' || ch == ')' ||
					ch == '=' || ch == '!' || ch == '<' || ch == '>' || ch == ':' {
					break
				}
				sc.Next()
			}
			word := sc.Slice(start, sc.Pos())
			switch strings.ToUpper(word) {
			case "AND":
				toks = append(toks, gcpTok{tokAnd, word})
			case "OR":
				toks = append(toks, gcpTok{tokOr, word})
			case "NOT":
				toks = append(toks, gcpTok{tokNot, word})
			default:
				toks = append(toks, gcpTok{tokWord, word})
			}
		}
	}
	return append(toks, gcpTok{tokEOF, ""}), nil
}

// gcpFilterLeaf builds the node for one comparison; op is empty for a bare
// restriction. Services whose query language gives some fields their own
// comparison rules supply their own.
type gcpFilterLeaf func(field, op, value string) (listq.Node, error)

func gcpDefaultLeaf(field, op, value string) (listq.Node, error) {
	test := gcpFilterTest(op, value)
	if test == nil {
		return nil, fmt.Errorf("unsupported operator %q", op)
	}
	return listq.Cmp{Path: field, Sep: ".", Test: test}, nil
}

type gcpFilterParser struct {
	toks  []gcpTok
	pos   int
	guard *sim.ParseGuard
	leaf  gcpFilterLeaf
	err   error
}

// maxFilterParseDepth bounds nesting so a pathological filter cannot overflow
// the goroutine stack, a fatal error Go cannot recover from.
const maxFilterParseDepth = 1000

func (p *gcpFilterParser) fail(format string, args ...any) listq.Node {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
	return listq.True{}
}

func gcpParseFilterExpr(s string) (listq.Node, error) {
	return gcpParseFilter(s, gcpDefaultLeaf)
}

func gcpParseFilter(s string, leaf gcpFilterLeaf) (listq.Node, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return listq.True{}, nil
	}
	toks, err := gcpTokenize(s)
	if err != nil {
		return nil, fmt.Errorf("invalid filter %q: %w", s, err)
	}
	p := &gcpFilterParser{toks: toks, guard: sim.NewParseGuard(maxFilterParseDepth, -1), leaf: leaf}
	node := p.parseOr()
	if p.err == nil && p.peek().kind != tokEOF {
		p.fail("unexpected %q", p.peek().text)
	}
	if p.err != nil {
		return nil, fmt.Errorf("invalid filter %q: %w", s, p.err)
	}
	return node, nil
}

func (p *gcpFilterParser) peek() gcpTok { return p.toks[p.pos] }
func (p *gcpFilterParser) next() gcpTok { t := p.toks[p.pos]; p.pos++; return t }

func (p *gcpFilterParser) parseOr() listq.Node {
	left := p.parseAnd()
	for p.err == nil && p.peek().kind == tokOr {
		p.next()
		left = listq.Or{L: left, R: p.parseAnd()}
	}
	return left
}

func (p *gcpFilterParser) parseAnd() listq.Node {
	left := p.parseFactor()
	for p.err == nil {
		k := p.peek().kind
		if k == tokAnd {
			p.next()
			left = listq.And{L: left, R: p.parseFactor()}
			continue
		}
		if k == tokWord || k == tokString || k == tokLParen || k == tokNot {
			left = listq.And{L: left, R: p.parseFactor()}
			continue
		}
		break
	}
	return left
}

func (p *gcpFilterParser) parseFactor() listq.Node {
	if p.err != nil {
		return listq.True{}
	}
	if p.peek().kind == tokNot {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			return p.fail("expression nesting too deep")
		}
		inner := p.parseFactor()
		p.guard.Leave()
		return listq.Not{Inner: inner}
	}
	if p.peek().kind == tokLParen {
		p.next()
		if !p.guard.Enter() {
			p.guard.Leave()
			return p.fail("expression nesting too deep")
		}
		inner := p.parseOr()
		p.guard.Leave()
		if p.err != nil {
			return inner
		}
		if p.peek().kind != tokRParen {
			return p.fail("missing ')'")
		}
		p.next()
		return inner
	}
	return p.parseComparison()
}

func (p *gcpFilterParser) parseComparison() listq.Node {
	if p.peek().kind != tokWord && p.peek().kind != tokString {
		if p.peek().kind == tokEOF {
			return p.fail("unexpected end of filter")
		}
		return p.fail("unexpected %q", p.peek().text)
	}
	field := p.next().text
	if p.peek().kind != tokOp {
		return p.makeLeaf(field, "", "")
	}
	op := p.next().text
	if p.peek().kind != tokWord && p.peek().kind != tokString {
		return p.fail("expected a value after %q %s", field, op)
	}
	return p.makeLeaf(field, op, p.next().text)
}

func (p *gcpFilterParser) makeLeaf(field, op, value string) listq.Node {
	node, err := p.leaf(field, op, value)
	if err != nil {
		return p.fail("%v", err)
	}
	return node
}
