package main

import (
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// The Pub/Sub subscription filter language
// (https://cloud.google.com/pubsub/docs/subscription-message-filter):
//
//	filter  = term { ("AND" | "OR") term }   one operator per level; mix needs parentheses
//	term    = [ "NOT" | "-" ] ( "(" filter ")" | attributes:KEY
//	        | attributes.KEY ("=" | "!=") VALUE
//	        | hasPrefix(attributes.KEY, VALUE) )
//	KEY     = name | quoted string
//	VALUE   = quoted string
//
// A filter selects on attributes only and is at most 256 bytes.

const psMaxFilterBytes = 256

// psAttributeSep joins the path Cmp walks; attribute names may hold dots.
const psAttributeSep = "\x00"

func psFilterDoc(attrs map[string]string) listq.Doc {
	m := make(map[string]any, len(attrs))
	for k, v := range attrs {
		m[k] = v
	}
	return listq.Doc{"attributes": m}
}

func psParseFilter(s string) (listq.Node, error) {
	if len(s) > psMaxFilterBytes {
		return nil, fmt.Errorf("filter is longer than %d bytes", psMaxFilterBytes)
	}
	if strings.TrimSpace(s) == "" {
		return listq.True{}, nil
	}
	p := &psFilterParser{src: s, guard: sim.NewParseGuard(maxFilterParseDepth, -1)}
	n, err := p.expr()
	if err == nil {
		p.space()
		if p.pos < len(p.src) {
			err = fmt.Errorf("unexpected %q", p.src[p.pos:])
		}
	}
	if err != nil {
		return nil, fmt.Errorf("invalid filter %q: %w", s, err)
	}
	return n, nil
}

type psFilterParser struct {
	src   string
	pos   int
	guard *sim.ParseGuard
}

func (p *psFilterParser) space() {
	for p.pos < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])) {
		p.pos++
	}
}

// keyword consumes word when it stands alone at the cursor.
func (p *psFilterParser) keyword(word string) bool {
	p.space()
	end := p.pos + len(word)
	if end > len(p.src) || p.src[p.pos:end] != word {
		return false
	}
	if end < len(p.src) && psNameByte(p.src[end]) {
		return false
	}
	p.pos = end
	return true
}

func (p *psFilterParser) lit(tok string) bool {
	p.space()
	if strings.HasPrefix(p.src[p.pos:], tok) {
		p.pos += len(tok)
		return true
	}
	return false
}

func psNameByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (p *psFilterParser) expr() (listq.Node, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	op := ""
	for {
		var next string
		switch {
		case p.keyword("AND"):
			next = "AND"
		case p.keyword("OR"):
			next = "OR"
		default:
			return left, nil
		}
		if op != "" && op != next {
			return nil, fmt.Errorf("AND and OR mixed without parentheses")
		}
		op = next
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		if op == "AND" {
			left = listq.And{L: left, R: right}
		} else {
			left = listq.Or{L: left, R: right}
		}
	}
}

func (p *psFilterParser) term() (listq.Node, error) {
	if !p.guard.Enter() {
		p.guard.Leave()
		return nil, fmt.Errorf("expression nesting too deep")
	}
	defer p.guard.Leave()
	if p.keyword("NOT") || p.lit("-") {
		inner, err := p.term()
		if err != nil {
			return nil, err
		}
		return listq.Not{Inner: inner}, nil
	}
	if p.lit("(") {
		inner, err := p.expr()
		if err != nil {
			return nil, err
		}
		if !p.lit(")") {
			return nil, fmt.Errorf("missing ')'")
		}
		return inner, nil
	}
	if p.keyword("hasPrefix") {
		if !p.lit("(") {
			return nil, fmt.Errorf("expected '(' after hasPrefix")
		}
		key, err := p.attribute('.')
		if err != nil {
			return nil, err
		}
		if !p.lit(",") {
			return nil, fmt.Errorf("expected ',' in hasPrefix")
		}
		prefix, err := p.quoted()
		if err != nil {
			return nil, err
		}
		if !p.lit(")") {
			return nil, fmt.Errorf("missing ')' after hasPrefix")
		}
		return psAttrCmp(key, func(v string, ok bool) bool { return ok && strings.HasPrefix(v, prefix) }), nil
	}
	p.space()
	if !strings.HasPrefix(p.src[p.pos:], "attributes") {
		return nil, fmt.Errorf("expected an attributes comparison at %q", p.src[p.pos:])
	}
	if next := p.pos + len("attributes"); next < len(p.src) && p.src[next] == ':' {
		key, err := p.attribute(':')
		if err != nil {
			return nil, err
		}
		return psAttrCmp(key, func(_ string, ok bool) bool { return ok }), nil
	}
	key, err := p.attribute('.')
	if err != nil {
		return nil, err
	}
	var negate bool
	switch {
	case p.lit("!="):
		negate = true
	case p.lit("="):
	default:
		return nil, fmt.Errorf("expected '=' or '!=' after attributes.%s", key)
	}
	value, err := p.quoted()
	if err != nil {
		return nil, err
	}
	if negate {
		return psAttrCmp(key, func(v string, ok bool) bool { return !ok || v != value }), nil
	}
	return psAttrCmp(key, func(v string, ok bool) bool { return ok && v == value }), nil
}

func psAttrCmp(key string, test listq.Test) listq.Node {
	return listq.Cmp{Path: "attributes" + psAttributeSep + key, Sep: psAttributeSep, Test: test}
}

// attribute reads `attributes<sep>KEY`.
func (p *psFilterParser) attribute(sep byte) (string, error) {
	p.space()
	if !strings.HasPrefix(p.src[p.pos:], "attributes") || p.pos+len("attributes") >= len(p.src) ||
		p.src[p.pos+len("attributes")] != sep {
		return "", fmt.Errorf("expected attributes%c<key> at %q", sep, p.src[p.pos:])
	}
	p.pos += len("attributes") + 1
	if p.pos < len(p.src) && (p.src[p.pos] == '"' || p.src[p.pos] == '\'') {
		return p.quoted()
	}
	start := p.pos
	for p.pos < len(p.src) && psNameByte(p.src[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		return "", fmt.Errorf("missing attribute key")
	}
	return p.src[start:p.pos], nil
}

func (p *psFilterParser) quoted() (string, error) {
	p.space()
	if p.pos >= len(p.src) || (p.src[p.pos] != '"' && p.src[p.pos] != '\'') {
		return "", fmt.Errorf("expected a quoted string at %q", p.src[p.pos:])
	}
	quote := p.src[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) && p.src[p.pos] != quote {
		if p.src[p.pos] == '\\' && p.pos+1 < len(p.src) {
			p.pos++
		}
		b.WriteByte(p.src[p.pos])
		p.pos++
	}
	if p.pos >= len(p.src) {
		return "", fmt.Errorf("unterminated string literal")
	}
	p.pos++
	return b.String(), nil
}
