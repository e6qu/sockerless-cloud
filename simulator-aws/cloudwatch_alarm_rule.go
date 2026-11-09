package main

import (
	"fmt"
	"slices"
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

// A cwRuleError is CloudWatch's refusal of an AlarmRule it cannot parse:
// ValidationError "Error in AlarmRule [Unsupported token 'XOR' at char 20,
// must be: <EOF>, 'AND', 'OR']". The offset counts from 0 and <EOF> sits at
// the rule's length. CloudWatch omits the list when the rule ends where exactly
// one token would complete it. Where no capture shows how CloudWatch names an
// expected token (cwRuleUnnamed), the message carries no list.
type cwRuleError struct {
	token    string
	offset   int
	expected []string
}

func (e *cwRuleError) Error() string {
	msg := fmt.Sprintf("Error in AlarmRule [Unsupported token '%s' at char %d", e.token, e.offset)
	if len(e.expected) > 0 && !slices.Contains(e.expected, cwRuleUnnamed) &&
		(e.token != cwRuleEOF || len(e.expected) > 1) {
		msg += ", must be: " + strings.Join(e.expected, ", ")
	}
	return msg + "]"
}

const (
	cwRuleEOF     = "<EOF>"
	cwRuleUnnamed = ""
)

var (
	cwRuleOperand  = []string{"'('", "'NOT'", "AT_LEAST", "TRUE or FALSE", "ALARM, OK, or INSUFFICIENT_DATA"}
	cwRuleStateArg = []string{"'NOT'", "ALARM, OK, or INSUFFICIENT_DATA"}
)

type cwRuleToken struct {
	text   string
	offset int
	quoted bool
}

// cwRuleTokens splits a rule into parentheses, commas, double-quoted
// references and words, a word running to the next space, parenthesis, comma
// or double quote.
func cwRuleTokens(src string) ([]cwRuleToken, *cwRuleError) {
	var out []cwRuleToken
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case strings.IndexByte(" \t\r\n", c) >= 0:
			i++
		case c == '(' || c == ')' || c == ',':
			out = append(out, cwRuleToken{text: string(c), offset: i})
			i++
		case c == '"':
			start := i
			var b strings.Builder
			for i++; ; i++ {
				if i >= len(src) {
					return nil, &cwRuleError{token: src[start:], offset: start, expected: []string{cwRuleUnnamed}}
				}
				if src[i] == '\\' && i+1 < len(src) {
					i++
					b.WriteByte(src[i])
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				b.WriteByte(src[i])
			}
			out = append(out, cwRuleToken{text: b.String(), offset: start, quoted: true})
		default:
			start := i
			for i < len(src) && strings.IndexByte(" \t\r\n(),\"", src[i]) < 0 {
				i++
			}
			out = append(out, cwRuleToken{text: src[start:i], offset: start})
		}
	}
	return out, nil
}

type cwRuleParser struct {
	src    string
	tokens []cwRuleToken
	pos    int
	depth  int
}

func cwParseAlarmRule(rule string) (*cwRuleNode, error) {
	tokens, lexErr := cwRuleTokens(rule)
	if lexErr != nil {
		return nil, lexErr
	}
	p := &cwRuleParser{src: rule, tokens: tokens}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.tokens) {
		return nil, p.fail(p.afterOperand())
	}
	return n, nil
}

// fail refuses the token at the cursor.
func (p *cwRuleParser) fail(expected []string) *cwRuleError {
	if p.pos >= len(p.tokens) {
		return &cwRuleError{token: cwRuleEOF, offset: len(p.src), expected: expected}
	}
	t := p.tokens[p.pos]
	text := t.text
	if t.quoted {
		text = p.src[t.offset:p.tokenEnd(p.pos)]
	}
	return &cwRuleError{token: text, offset: t.offset, expected: expected}
}

func (p *cwRuleParser) tokenEnd(i int) int {
	t := p.tokens[i]
	if !t.quoted {
		return t.offset + len(t.text)
	}
	for j := t.offset + 1; j < len(p.src); j++ {
		if p.src[j] == '\\' {
			j++
			continue
		}
		if p.src[j] == '"' {
			return j + 1
		}
	}
	return len(p.src)
}

// afterOperand lists what may follow a complete operand.
func (p *cwRuleParser) afterOperand() []string {
	if p.depth > 0 {
		return []string{"')'", "'AND'", "'OR'"}
	}
	return []string{cwRuleEOF, "'AND'", "'OR'"}
}

// keyword consumes the word at the cursor when it is word; keywords are case
// sensitive.
func (p *cwRuleParser) keyword(word string) bool {
	if p.pos < len(p.tokens) && !p.tokens[p.pos].quoted && p.tokens[p.pos].text == word {
		p.pos++
		return true
	}
	return false
}

func (p *cwRuleParser) expect(c string, expected []string) error {
	if !p.keyword(c) {
		return p.fail(expected)
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
	if p.keyword("(") {
		p.depth++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.keyword(")") {
			return nil, p.fail(p.afterOperand())
		}
		p.depth--
		return n, nil
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
		if err := p.expect("(", []string{"'('"}); err != nil {
			return nil, err
		}
		ref, err := p.ref()
		if err != nil {
			return nil, err
		}
		return &cwRuleNode{op: "state", state: state, ref: ref}, p.expect(")", []string{"')'"})
	}
	return nil, p.fail(cwRuleOperand)
}

func (p *cwRuleParser) atLeast() (*cwRuleNode, error) {
	if err := p.expect("(", []string{"'('"}); err != nil {
		return nil, err
	}
	if p.pos >= len(p.tokens) || p.tokens[p.pos].quoted {
		return nil, p.fail([]string{cwRuleUnnamed})
	}
	m := p.tokens[p.pos].text
	n := &cwRuleNode{op: "atLeast"}
	if digits, ok := strings.CutSuffix(m, "%"); ok {
		m, n.percent = digits, true
	}
	count, err := strconv.Atoi(m)
	if err != nil || strings.ContainsAny(m, "+-") {
		return nil, p.fail([]string{cwRuleUnnamed})
	}
	n.count = count
	p.pos++
	if err := p.expect(",", []string{cwRuleUnnamed}); err != nil {
		return nil, err
	}
	n.negate = p.keyword("NOT")
	state, ok := p.stateWord()
	if !ok {
		if n.negate {
			return nil, p.fail([]string{"ALARM, OK, or INSUFFICIENT_DATA"})
		}
		return nil, p.fail(cwRuleStateArg)
	}
	n.state = state
	if err := p.expect(",", []string{cwRuleUnnamed}); err != nil {
		return nil, err
	}
	if err := p.expect("(", []string{"'('"}); err != nil {
		return nil, err
	}
	for {
		ref, err := p.ref()
		if err != nil {
			return nil, err
		}
		n.refs = append(n.refs, ref)
		if !p.keyword(",") {
			break
		}
	}
	if err := p.expect(")", []string{cwRuleUnnamed}); err != nil {
		return nil, err
	}
	return n, p.expect(")", []string{"')'"})
}

// A quoted reference escapes a double quote with a backslash; a bare one holds
// no space, parenthesis or comma.
func (p *cwRuleParser) ref() (string, error) {
	if p.pos >= len(p.tokens) {
		return "", p.fail([]string{cwRuleUnnamed})
	}
	t := p.tokens[p.pos]
	if !t.quoted && (t.text == "(" || t.text == ")" || t.text == ",") {
		return "", p.fail([]string{cwRuleUnnamed})
	}
	p.pos++
	return t.text, nil
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
