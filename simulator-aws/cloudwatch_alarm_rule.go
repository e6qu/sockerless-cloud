package main

import (
	"fmt"
	"strconv"
	"strings"
)

// A composite alarm's AlarmRule, parsed as the CloudWatch User Guide's
// "Composite alarms" page lays it out: ALARM, OK and INSUFFICIENT_DATA of an
// alarm name or ARN, quoted or bare; TRUE and FALSE; AND, OR and NOT;
// parentheses; and AT_LEAST(M, [NOT] STATE, (alarm, ...)) with M a count or a
// percentage.
type cwRuleNode struct {
	op      string
	kids    []*cwRuleNode
	state   string
	negate  bool
	ref     string
	refs    []string
	value   bool
	count   int
	percent bool
}

type cwRuleParser struct {
	src string
	pos int
}

func cwParseAlarmRule(rule string) (*cwRuleNode, error) {
	p := &cwRuleParser{src: rule}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.src) {
		return nil, fmt.Errorf("unexpected %q at offset %d", p.src[p.pos:], p.pos)
	}
	return n, nil
}

func (p *cwRuleParser) space() {
	for p.pos < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])) {
		p.pos++
	}
}

// keyword consumes word when it stands alone at the cursor.
func (p *cwRuleParser) keyword(word string) bool {
	p.space()
	if !strings.HasPrefix(p.src[p.pos:], word) {
		return false
	}
	end := p.pos + len(word)
	if end < len(p.src) && cwRuleWordByte(p.src[end]) {
		return false
	}
	p.pos = end
	return true
}

func cwRuleWordByte(b byte) bool {
	return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

func (p *cwRuleParser) punct(c byte) bool {
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *cwRuleParser) expect(c byte) error {
	if !p.punct(c) {
		return fmt.Errorf("expected %q at offset %d", c, p.pos)
	}
	return nil
}

func (p *cwRuleParser) or() (*cwRuleNode, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.keyword("OR") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &cwRuleNode{op: "or", kids: []*cwRuleNode{left, right}}
	}
	return left, nil
}

func (p *cwRuleParser) and() (*cwRuleNode, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.keyword("AND") {
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		left = &cwRuleNode{op: "and", kids: []*cwRuleNode{left, right}}
	}
	return left, nil
}

func (p *cwRuleParser) unary() (*cwRuleNode, error) {
	if p.keyword("NOT") {
		kid, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &cwRuleNode{op: "not", kids: []*cwRuleNode{kid}}, nil
	}
	return p.primary()
}

func (p *cwRuleParser) stateWord() (string, bool) {
	for _, s := range []string{"INSUFFICIENT_DATA", "ALARM", "OK"} {
		if p.keyword(s) {
			return s, true
		}
	}
	return "", false
}

func (p *cwRuleParser) primary() (*cwRuleNode, error) {
	if p.punct('(') {
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		return n, p.expect(')')
	}
	if p.keyword("TRUE") {
		return &cwRuleNode{op: "const", value: true}, nil
	}
	if p.keyword("FALSE") {
		return &cwRuleNode{op: "const", value: false}, nil
	}
	if p.keyword("AT_LEAST") {
		return p.atLeast()
	}
	if state, ok := p.stateWord(); ok {
		if err := p.expect('('); err != nil {
			return nil, err
		}
		ref, err := p.ref()
		if err != nil {
			return nil, err
		}
		return &cwRuleNode{op: "state", state: state, ref: ref}, p.expect(')')
	}
	p.space()
	return nil, fmt.Errorf("expected a function, TRUE, FALSE or '(' at offset %d", p.pos)
}

func (p *cwRuleParser) atLeast() (*cwRuleNode, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	p.space()
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	count, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil {
		return nil, fmt.Errorf("AT_LEAST needs a count or percentage at offset %d", start)
	}
	n := &cwRuleNode{op: "atLeast", count: count}
	if p.punct('%') {
		n.percent = true
	}
	if err := p.expect(','); err != nil {
		return nil, err
	}
	n.negate = p.keyword("NOT")
	state, ok := p.stateWord()
	if !ok {
		return nil, fmt.Errorf("AT_LEAST needs ALARM, OK or INSUFFICIENT_DATA at offset %d", p.pos)
	}
	n.state = state
	if err := p.expect(','); err != nil {
		return nil, err
	}
	if err := p.expect('('); err != nil {
		return nil, err
	}
	for {
		ref, err := p.ref()
		if err != nil {
			return nil, err
		}
		n.refs = append(n.refs, ref)
		if !p.punct(',') {
			break
		}
	}
	if err := p.expect(')'); err != nil {
		return nil, err
	}
	return n, p.expect(')')
}

// A quoted reference escapes a double quote with a backslash; a bare one holds
// no space, parenthesis or comma.
func (p *cwRuleParser) ref() (string, error) {
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == '"' {
		p.pos++
		var b strings.Builder
		for p.pos < len(p.src) {
			c := p.src[p.pos]
			switch {
			case c == '\\' && p.pos+1 < len(p.src):
				b.WriteByte(p.src[p.pos+1])
				p.pos += 2
			case c == '"':
				p.pos++
				return b.String(), nil
			default:
				b.WriteByte(c)
				p.pos++
			}
		}
		return "", fmt.Errorf("unterminated quoted alarm reference")
	}
	start := p.pos
	for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n(),", rune(p.src[p.pos])) {
		p.pos++
	}
	if p.pos == start {
		return "", fmt.Errorf("expected an alarm name or ARN at offset %d", start)
	}
	return p.src[start:p.pos], nil
}

// eval answers the rule's truth, with stateOf giving each referenced alarm's
// state.
func (n *cwRuleNode) eval(stateOf func(ref string) string) bool {
	switch n.op {
	case "const":
		return n.value
	case "not":
		return !n.kids[0].eval(stateOf)
	case "and":
		return n.kids[0].eval(stateOf) && n.kids[1].eval(stateOf)
	case "or":
		return n.kids[0].eval(stateOf) || n.kids[1].eval(stateOf)
	case "state":
		return stateOf(n.ref) == n.state
	case "atLeast":
		matched := 0
		for _, ref := range n.refs {
			if (stateOf(ref) == n.state) != n.negate {
				matched++
			}
		}
		if n.percent {
			return matched*100 >= n.count*len(n.refs)
		}
		return matched >= n.count
	}
	return false
}

// refsOf lists every alarm the rule references, in order of appearance.
func (n *cwRuleNode) refsOf() []string {
	var out []string
	var walk func(*cwRuleNode)
	walk = func(x *cwRuleNode) {
		switch x.op {
		case "state":
			out = append(out, x.ref)
		case "atLeast":
			out = append(out, x.refs...)
		}
		for _, k := range x.kids {
			walk(k)
		}
	}
	walk(n)
	return out
}
