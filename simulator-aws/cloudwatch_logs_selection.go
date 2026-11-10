package main

import (
	"fmt"
	"strings"
	"unicode"
)

// CloudWatch Logs spells two filters in one syntax: an account policy's
// selectionCriteria over log group names and data sources, and a filter's
// fieldSelectionCriteria over the system fields of centralized log events.
// The API reference documents the operators =, !=, IN and NOT IN, joined by
// AND and OR.

type cwSelection struct {
	op          string // "AND", "OR" or "" for a single condition
	left, right *cwSelection
	field       string
	cmp         string // "=", "!=", "IN" or "NOT IN"
	values      []string
}

type cwSelToken struct {
	kind string // "ident", "string", "punct"
	text string
}

func cwTokenizeSelection(s string) ([]cwSelToken, error) {
	var toks []cwSelToken
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '"':
			var b strings.Builder
			i++
			closed := false
			for i < len(rs) {
				if rs[i] == '\\' && i+1 < len(rs) {
					b.WriteRune(rs[i+1])
					i += 2
					continue
				}
				if rs[i] == '"' {
					closed = true
					i++
					break
				}
				b.WriteRune(rs[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string")
			}
			toks = append(toks, cwSelToken{"string", b.String()})
		case r == '!' && i+1 < len(rs) && rs[i+1] == '=':
			toks = append(toks, cwSelToken{"punct", "!="})
			i += 2
		case strings.ContainsRune("=[](),", r):
			toks = append(toks, cwSelToken{"punct", string(r)})
			i++
		case r == '@' || r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r):
			start := i
			for i < len(rs) && (rs[i] == '@' || rs[i] == '_' || rs[i] == '.' || unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i])) {
				i++
			}
			toks = append(toks, cwSelToken{"ident", string(rs[start:i])})
		default:
			return nil, fmt.Errorf("unexpected character %q", r)
		}
	}
	return toks, nil
}

type cwSelParser struct {
	toks   []cwSelToken
	pos    int
	fields map[string]bool
}

func (p *cwSelParser) peek() (cwSelToken, bool) {
	if p.pos >= len(p.toks) {
		return cwSelToken{}, false
	}
	return p.toks[p.pos], true
}

func (p *cwSelParser) keyword(word string) bool {
	if t, ok := p.peek(); ok && t.kind == "ident" && strings.EqualFold(t.text, word) {
		p.pos++
		return true
	}
	return false
}

func (p *cwSelParser) punct(text string) bool {
	if t, ok := p.peek(); ok && t.kind == "punct" && t.text == text {
		p.pos++
		return true
	}
	return false
}

func (p *cwSelParser) or() (*cwSelection, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.keyword("OR") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &cwSelection{op: "OR", left: left, right: right}
	}
	return left, nil
}

func (p *cwSelParser) and() (*cwSelection, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.keyword("AND") {
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		left = &cwSelection{op: "AND", left: left, right: right}
	}
	return left, nil
}

func (p *cwSelParser) term() (*cwSelection, error) {
	if p.punct("(") {
		inner, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.punct(")") {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		return inner, nil
	}
	t, ok := p.peek()
	if !ok || t.kind != "ident" {
		return nil, fmt.Errorf("expected a field name")
	}
	if !p.fields[t.text] {
		return nil, fmt.Errorf("unsupported field %s", t.text)
	}
	p.pos++
	cond := &cwSelection{field: t.text}
	switch {
	case p.punct("="):
		cond.cmp = "="
	case p.punct("!="):
		cond.cmp = "!="
	case p.keyword("IN"):
		cond.cmp = "IN"
	case p.keyword("NOT"):
		if !p.keyword("IN") {
			return nil, fmt.Errorf("expected IN after NOT")
		}
		cond.cmp = "NOT IN"
	default:
		return nil, fmt.Errorf("expected an operator after %s", t.text)
	}
	if cond.cmp == "IN" || cond.cmp == "NOT IN" {
		if !p.punct("[") {
			return nil, fmt.Errorf("expected a list after %s", cond.cmp)
		}
		for !p.punct("]") {
			v, ok := p.peek()
			if !ok || v.kind != "string" {
				return nil, fmt.Errorf("expected a quoted value in the list")
			}
			p.pos++
			cond.values = append(cond.values, v.text)
			if !p.punct(",") {
				if !p.punct("]") {
					return nil, fmt.Errorf("expected , or ] in the list")
				}
				break
			}
		}
		return cond, nil
	}
	v, ok := p.peek()
	if !ok || v.kind != "string" {
		return nil, fmt.Errorf("expected a quoted value after %s", cond.cmp)
	}
	p.pos++
	cond.values = []string{v.text}
	return cond, nil
}

func cwParseSelection(s string, fields ...string) (*cwSelection, error) {
	toks, err := cwTokenizeSelection(s)
	if err != nil {
		return nil, err
	}
	p := &cwSelParser{toks: toks, fields: map[string]bool{}}
	for _, f := range fields {
		p.fields[f] = true
	}
	sel, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.pos].text)
	}
	return sel, nil
}

// match evaluates the selection against a value per field; a field ending in
// "Prefix" compares by prefix.
func (s *cwSelection) match(values map[string]string) bool {
	switch s.op {
	case "AND":
		return s.left.match(values) && s.right.match(values)
	case "OR":
		return s.left.match(values) || s.right.match(values)
	}
	field := s.field
	prefix := strings.HasSuffix(field, "Prefix")
	if prefix {
		field = strings.TrimSuffix(field, "Prefix")
	}
	have := values[field]
	hit := false
	for _, v := range s.values {
		if have == v || (prefix && strings.HasPrefix(have, v)) {
			hit = true
			break
		}
	}
	if s.cmp == "!=" || s.cmp == "NOT IN" {
		return !hit
	}
	return hit
}

// conditions lists the selection's conditions, outermost first.
func (s *cwSelection) conditions() []*cwSelection {
	if s == nil {
		return nil
	}
	if s.op == "" {
		return []*cwSelection{s}
	}
	return append(s.left.conditions(), s.right.conditions()...)
}

// The API reference names three system fields.
var cwSystemFields = []string{"@aws.account", "@aws.region", "@source.log"}

const cwFieldSelectionMaxLen = 2000

// cwParseFieldSelection parses a filter's fieldSelectionCriteria; an empty
// one selects every event.
func cwParseFieldSelection(s string) (*cwSelection, error) {
	if s == "" {
		return nil, nil
	}
	if len([]rune(s)) > cwFieldSelectionMaxLen {
		return nil, fmt.Errorf("fieldSelectionCriteria must be at most %d characters", cwFieldSelectionMaxLen)
	}
	sel, err := cwParseSelection(s, cwSystemFields...)
	if err != nil {
		return nil, cwtInvalid("Invalid fieldSelectionCriteria: %v", err)
	}
	return sel, nil
}

func cwCheckSystemFields(fields []string, allowed ...string) error {
	for _, f := range fields {
		ok := false
		for _, a := range allowed {
			ok = ok || f == a
		}
		if !ok {
			return cwtInvalid("Invalid emitSystemFields value %s: valid values are %s", f, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// A log group's own events carry its account, its Region and itself as their
// source; only centralization copies events whose source differs.
func cwSystemFieldValues(group string) map[string]string {
	return map[string]string{"@aws.account": awsAccountID(), "@aws.region": awsRegion(), "@source.log": group}
}
