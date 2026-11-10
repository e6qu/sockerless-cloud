package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// SEARCH('{Namespace, DimensionName, ...} SearchTerm', 'Statistic') follows
// the User Guide's "CloudWatch search expression syntax": an optional metric
// schema, then partial-match tokens, "exact" strings and Property=value
// designators joined by AND (implicit between terms), OR and NOT, grouped by
// parentheses. It finds metrics that reported data within the past two
// weeks.

const (
	cwSearchMaxLength = 1024
	cwSearchMaxSeries = 500
	cwSearchLookback  = 14 * 24 * time.Hour
)

type cwSearchNode struct {
	kind     string // and, or, not, word, exact, property
	children []*cwSearchNode
	text     string
	property string
	exact    bool
}

type cwSearchQuery struct {
	namespace  string
	dimensions []string
	term       *cwSearchNode
}

type cwSearchParser struct {
	src []rune
	pos int
}

func cwParseSearch(src string) (cwSearchQuery, error) {
	if len(src) > cwSearchMaxLength {
		return cwSearchQuery{}, fmt.Errorf("the search expression is longer than %d characters", cwSearchMaxLength)
	}
	p := &cwSearchParser{src: []rune(src)}
	var q cwSearchQuery
	p.skip()
	if p.peek() == '{' {
		p.pos++
		var names []string
		for {
			p.skip()
			if p.peek() == '}' {
				p.pos++
				break
			}
			if p.pos >= len(p.src) {
				return q, fmt.Errorf("the metric schema is missing its closing brace")
			}
			name, err := p.schemaName()
			if err != nil {
				return q, err
			}
			names = append(names, name)
			p.skip()
			if p.peek() == ',' {
				p.pos++
			}
		}
		if len(names) == 0 {
			return q, fmt.Errorf("the metric schema must name a namespace")
		}
		q.namespace, q.dimensions = names[0], names[1:]
	}
	p.skip()
	if p.pos < len(p.src) {
		term, err := p.or()
		if err != nil {
			return q, err
		}
		p.skip()
		if p.pos < len(p.src) {
			return q, fmt.Errorf("unexpected %q in the search term", string(p.src[p.pos:]))
		}
		q.term = term
	}
	if q.namespace == "" && q.term == nil {
		return q, fmt.Errorf("a search needs a metric schema, a search term or both")
	}
	return q, nil
}

func (p *cwSearchParser) peek() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

// skip passes whitespace and the commas that may separate terms.
func (p *cwSearchParser) skip() {
	for p.pos < len(p.src) && (unicode.IsSpace(p.src[p.pos]) || p.src[p.pos] == ',') {
		p.pos++
	}
}

func (p *cwSearchParser) schemaName() (string, error) {
	if p.peek() == '"' {
		return p.quoted()
	}
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != ',' && p.src[p.pos] != '}' && !unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
	return string(p.src[start:p.pos]), nil
}

// quoted reads a "string" in which \" \\ \( and \) stand for the character.
func (p *cwSearchParser) quoted() (string, error) {
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\\' && p.pos+1 < len(p.src):
			b.WriteRune(p.src[p.pos+1])
			p.pos += 2
		case c == '"':
			p.pos++
			return b.String(), nil
		default:
			b.WriteRune(c)
			p.pos++
		}
	}
	return "", fmt.Errorf("an exact match string is missing its closing quote")
}

func (p *cwSearchParser) keyword(word string) bool {
	p.skip()
	end := p.pos + len(word)
	if end > len(p.src) || string(p.src[p.pos:end]) != word {
		return false
	}
	if end < len(p.src) && !unicode.IsSpace(p.src[end]) && p.src[end] != '(' && p.src[end] != '"' {
		return false
	}
	p.pos = end
	return true
}

func (p *cwSearchParser) or() (*cwSearchNode, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.keyword("OR") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &cwSearchNode{kind: "or", children: []*cwSearchNode{left, right}}
	}
	return left, nil
}

func (p *cwSearchParser) and() (*cwSearchNode, error) {
	left, err := p.not()
	if err != nil {
		return nil, err
	}
	for {
		p.skip()
		save := p.pos
		if p.pos >= len(p.src) || p.peek() == ')' {
			return left, nil
		}
		if p.keyword("OR") {
			p.pos = save
			return left, nil
		}
		p.keyword("AND")
		right, err := p.not()
		if err != nil {
			return nil, err
		}
		left = &cwSearchNode{kind: "and", children: []*cwSearchNode{left, right}}
	}
}

func (p *cwSearchParser) not() (*cwSearchNode, error) {
	if p.keyword("NOT") {
		inner, err := p.not()
		if err != nil {
			return nil, err
		}
		return &cwSearchNode{kind: "not", children: []*cwSearchNode{inner}}, nil
	}
	return p.primary()
}

func (p *cwSearchParser) primary() (*cwSearchNode, error) {
	p.skip()
	switch p.peek() {
	case 0:
		return nil, fmt.Errorf("the search term ends where a term belongs")
	case '(':
		p.pos++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.peek() != ')' {
			return nil, fmt.Errorf("the search term is missing a closing parenthesis")
		}
		p.pos++
		return n, nil
	case '"':
		s, err := p.quoted()
		if err != nil {
			return nil, err
		}
		return &cwSearchNode{kind: "exact", text: s, exact: true}, nil
	}
	start := p.pos
	for p.pos < len(p.src) && !unicode.IsSpace(p.src[p.pos]) && !strings.ContainsRune(`()"=,`, p.src[p.pos]) {
		p.pos++
	}
	word := string(p.src[start:p.pos])
	save := p.pos
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
	if p.peek() != '=' {
		p.pos = save
		if word == "" {
			return nil, fmt.Errorf("unexpected %q in the search term", string(p.peek()))
		}
		return &cwSearchNode{kind: "word", text: word}, nil
	}
	p.pos++
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
	if p.peek() == '"' {
		s, err := p.quoted()
		if err != nil {
			return nil, err
		}
		return &cwSearchNode{kind: "property", property: word, text: s, exact: true}, nil
	}
	if p.peek() == '\'' {
		end := strings.IndexRune(string(p.src[p.pos+1:]), '\'')
		if end < 0 {
			return nil, fmt.Errorf("a quoted value is missing its closing quote")
		}
		s := string(p.src[p.pos+1 : p.pos+1+end])
		p.pos += end + 2
		return &cwSearchNode{kind: "property", property: word, text: s, exact: true}, nil
	}
	vstart := p.pos
	for p.pos < len(p.src) && !unicode.IsSpace(p.src[p.pos]) && !strings.ContainsRune(`()"=,`, p.src[p.pos]) {
		p.pos++
	}
	return &cwSearchNode{kind: "property", property: word, text: string(p.src[vstart:p.pos])}, nil
}

type cwSearchToken struct {
	text      string
	sensitive bool
}

// cwCamelTokens splits a string the way search tokenization does: at every
// non-alphanumeric run, at a lowercase-to-uppercase change, before the last
// capital of a capital run that a lowercase letter follows (SDBFailure is
// SDB, Failure), and where digits start or end.
func cwCamelTokens(s string) [][]string {
	var pieces [][]string
	for _, piece := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		runes := []rune(piece)
		var tokens []string
		start := 0
		for i := 1; i < len(runes); i++ {
			prev, cur := runes[i-1], runes[i]
			split := unicode.IsDigit(prev) != unicode.IsDigit(cur) ||
				unicode.IsLower(prev) && unicode.IsUpper(cur) ||
				unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if split {
				tokens = append(tokens, string(runes[start:i]))
				start = i
			}
		}
		tokens = append(tokens, string(runes[start:]))
		pieces = append(pieces, tokens)
	}
	return pieces
}

// cwSearchPartial matches a partial term against one name: each delimited
// piece of the term that is a single token matches case-insensitively, a
// piece of several camel-case tokens matches them case-sensitively, and the
// pieces match consecutive tokens of the name. The name's whole token, its
// alphanumerics lowercased, also matches.
func cwSearchPartial(term, name string) bool {
	var want []cwSearchToken
	for _, piece := range cwCamelTokens(term) {
		for _, tok := range piece {
			want = append(want, cwSearchToken{text: tok, sensitive: len(piece) > 1})
		}
	}
	if len(want) == 0 {
		return false
	}
	var have []string
	var whole strings.Builder
	for _, piece := range cwCamelTokens(name) {
		have = append(have, piece...)
		for _, tok := range piece {
			whole.WriteString(strings.ToLower(tok))
		}
	}
	var joined strings.Builder
	for _, w := range want {
		joined.WriteString(strings.ToLower(w.text))
	}
	if joined.String() == whole.String() {
		return true
	}
	for i := 0; i+len(want) <= len(have); i++ {
		match := true
		for k, w := range want {
			h := have[i+k]
			if w.sensitive && h != w.text || !w.sensitive && !strings.EqualFold(h, w.text) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

type cwSearchMetric struct {
	namespace  string
	name       string
	dimensions []CWDimension
}

func (m cwSearchMetric) names() []string {
	out := []string{m.namespace, m.name}
	for _, d := range m.dimensions {
		out = append(out, d.Name, d.Value)
	}
	return out
}

func (n *cwSearchNode) matches(m cwSearchMetric) bool {
	switch n.kind {
	case "and":
		return n.children[0].matches(m) && n.children[1].matches(m)
	case "or":
		return n.children[0].matches(m) || n.children[1].matches(m)
	case "not":
		return !n.children[0].matches(m)
	case "word":
		for _, s := range m.names() {
			if cwSearchPartial(n.text, s) {
				return true
			}
		}
		return false
	case "exact":
		for _, s := range m.names() {
			if s == n.text {
				return true
			}
		}
		return false
	}
	var fields []string
	switch n.property {
	case "MetricName":
		fields = []string{m.name}
	case "Namespace":
		fields = []string{m.namespace}
	case ":aws.AccountId":
		return n.text == awsAccountID() || n.text == "LOCAL"
	default:
		for _, d := range m.dimensions {
			if d.Name == n.property {
				fields = append(fields, d.Value)
			}
		}
	}
	for _, f := range fields {
		if n.exact && f == n.text || !n.exact && cwSearchPartial(n.text, f) {
			return true
		}
	}
	return false
}

func (q cwSearchQuery) admits(m cwSearchMetric) bool {
	if q.namespace != "" {
		if m.namespace != q.namespace {
			return false
		}
		for _, want := range q.dimensions {
			found := false
			for _, d := range m.dimensions {
				if d.Name == want {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return q.term == nil || q.term.matches(m)
}

func (ev *cwMetricEvaluator) search(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) != 2 || n.args[0].kind != "str" || n.args[1].kind != "str" {
		return cwMathValue{}, fmt.Errorf("SEARCH takes a search expression and a statistic, each in single quotes")
	}
	query, err := cwParseSearch(n.args[0].name)
	if err != nil {
		return cwMathValue{}, err
	}
	stat := n.args[1].name
	if !cwIsStatistic(stat) {
		return cwMathValue{}, fmt.Errorf("%s is not a CloudWatch statistic", stat)
	}
	recent := float64(ev.opts.now.Add(-cwSearchLookback).Unix())
	var found []cwSearchMetric
	for _, data := range cwMetrics.List() {
		if len(data) == 0 {
			continue
		}
		latest := data[0].Timestamp
		for _, d := range data {
			latest = max(latest, d.Timestamp)
		}
		m := cwSearchMetric{namespace: data[0].Namespace, name: data[0].MetricName, dimensions: data[0].Dimensions}
		if latest >= recent && query.admits(m) {
			found = append(found, m)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		return metricsKey(found[i].namespace, found[i].name, found[i].dimensions) < metricsKey(found[j].namespace, found[j].name, found[j].dimensions)
	})
	if len(found) > cwSearchMaxSeries {
		found = found[:cwSearchMaxSeries]
	}
	period := ev.periodOf(q)
	series := make([]cwMetricSeries, 0, len(found))
	for _, m := range found {
		s := cwStatSeries(CWMetricRef{Namespace: m.namespace, MetricName: m.name, Dimensions: m.dimensions}, period, stat, ev.start, ev.end)
		s.Label = cwDimensionedLabel(m.name, m.dimensions)
		series = append(series, s)
	}
	return cwArrayValue(series), nil
}

// cwDimensionedLabel labels a found metric by its dimension values and name.
func cwDimensionedLabel(name string, dims []CWDimension) string {
	parts := make([]string, 0, len(dims)+1)
	for _, d := range dims {
		parts = append(parts, d.Value)
	}
	return strings.Join(append(parts, name), " ")
}

// cwIsStatistic accepts the statistics the Statistics concept lists:
// SampleCount, Average, Sum, Minimum, Maximum and a percentile pNN.NN.
func cwIsStatistic(stat string) bool {
	switch stat {
	case "SampleCount", "Average", "Sum", "Minimum", "Maximum":
		return true
	}
	p, ok := cwParsePercentile(stat)
	return ok && p >= 0 && p <= 100
}

// cwSearchUsed reports whether an expression calls SEARCH.
func cwSearchUsed(expression string) bool {
	node, err := cwParseMetricExpression(expression)
	return err == nil && cwCallsFunction(node, "SEARCH")
}

func cwCallsFunction(n *cwMathNode, name string) bool {
	if n == nil {
		return false
	}
	if n.kind == "call" && n.name == name {
		return true
	}
	if cwCallsFunction(n.left, name) || cwCallsFunction(n.right, name) {
		return true
	}
	for _, a := range n.args {
		if cwCallsFunction(a, name) {
			return true
		}
	}
	return false
}
