package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Workflow Definition Language expressions, as the Logic Apps expression
// functions reference defines them. A string that starts with @ is one
// expression, @@ escapes a literal @, and @{...} interpolates an expression's
// value into a string. A function the evaluator does not implement fails the
// evaluation with InvalidTemplate, naming it.

// evaluate resolves every expression in a definition value.
func (run *logicRun) evaluate(v any) (any, error) {
	switch t := v.(type) {
	case string:
		return run.evaluateString(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			value, err := run.evaluate(e)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			value, err := run.evaluate(e)
			if err != nil {
				return nil, err
			}
			out[k] = value
		}
		return out, nil
	}
	return v, nil
}

type logicPredicateSpec struct {
	op   string
	args []any
}

// logicPredicate recognizes the designer's condition object — a single key
// naming a logical or comparison function, whose value is its arguments —
// such as {"and": [{"equals": ["@variables('x')", 1]}]}.
func logicPredicate(m map[string]any) (logicPredicateSpec, bool) {
	if len(m) != 1 {
		return logicPredicateSpec{}, false
	}
	for k, v := range m {
		switch k {
		case "and", "or", "not", "equals", "greater", "greaterOrEquals", "less", "lessOrEquals", "contains", "startsWith", "endsWith":
			if k == "not" {
				return logicPredicateSpec{op: k, args: []any{v}}, true
			}
			args, ok := v.([]any)
			return logicPredicateSpec{op: k, args: args}, ok
		}
	}
	return logicPredicateSpec{}, false
}

// evaluateCondition evaluates an If action's expression: an expression
// string, or the designer's condition object, nested to any depth.
func (run *logicRun) evaluateCondition(v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return run.evaluate(v)
	}
	pred, ok := logicPredicate(m)
	if !ok {
		return nil, logicErrorf("The condition expression is not valid: expected an expression or a single logical function, got %s.", logicToString(m))
	}
	args := make([]any, len(pred.args))
	for i, a := range pred.args {
		value, err := run.evaluateCondition(a)
		if err != nil {
			return nil, err
		}
		args[i] = value
	}
	return logicCall(run, pred.op, args)
}

func (run *logicRun) evaluateString(s string) (any, error) {
	if strings.HasPrefix(s, "@@") {
		return s[1:], nil
	}
	if strings.HasPrefix(s, "@") && !strings.HasPrefix(s, "@{") {
		return run.evaluateExpression(s[1:])
	}
	if !strings.Contains(s, "@{") {
		return s, nil
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "@{")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		b.WriteString(s[:i])
		end := logicMatchingBrace(s, i+2)
		if end < 0 {
			return nil, logicErrorf("The template language expression '%s' is not valid: the string interpolation is not closed.", s)
		}
		value, err := run.evaluateExpression(s[i+2 : end])
		if err != nil {
			return nil, err
		}
		b.WriteString(logicToString(value))
		s = s[end+1:]
	}
}

// logicMatchingBrace finds the brace closing an interpolation, skipping
// quoted strings.
func logicMatchingBrace(s string, from int) int {
	depth, quoted := 1, false
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			quoted = !quoted
		case quoted:
		case s[i] == '{':
			depth++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func (run *logicRun) evaluateExpression(expr string) (any, error) {
	p := &logicParser{src: expr, run: run}
	value, err := p.expression()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return nil, logicErrorf("The template language expression '%s' is not valid: unexpected '%s' at position %d.", expr, p.src[p.pos:], p.pos)
	}
	return value, nil
}

type logicParser struct {
	src string
	pos int
	run *logicRun
}

func (p *logicParser) skipSpace() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *logicParser) fail(format string, args ...any) error {
	return logicErrorf("The template language expression '%s' is not valid: %s", p.src, fmt.Sprintf(format, args...))
}

func (p *logicParser) expression() (any, error) {
	value, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		optional := false
		if strings.HasPrefix(p.src[p.pos:], "?") {
			optional = true
			p.pos++
		}
		switch {
		case strings.HasPrefix(p.src[p.pos:], "["):
			p.pos++
			index, err := p.expression()
			if err != nil {
				return nil, err
			}
			p.skipSpace()
			if !strings.HasPrefix(p.src[p.pos:], "]") {
				return nil, p.fail("expected ']'")
			}
			p.pos++
			if value, err = logicIndex(value, index, optional); err != nil {
				return nil, err
			}
		case strings.HasPrefix(p.src[p.pos:], "."):
			p.pos++
			name := p.identifier()
			if name == "" {
				return nil, p.fail("expected a property name after '.'")
			}
			if value, err = logicIndex(value, name, optional); err != nil {
				return nil, err
			}
		default:
			if optional {
				return nil, p.fail("expected '[' or '.' after '?'")
			}
			return value, nil
		}
	}
}

func (p *logicParser) identifier() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := rune(p.src[p.pos])
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			break
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *logicParser) primary() (any, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, p.fail("the expression ends early")
	}
	c := p.src[p.pos]
	switch {
	case c == '\'':
		p.pos++
		var b strings.Builder
		for p.pos < len(p.src) {
			if p.src[p.pos] == '\'' {
				if p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'' {
					b.WriteByte('\'')
					p.pos += 2
					continue
				}
				p.pos++
				return b.String(), nil
			}
			b.WriteByte(p.src[p.pos])
			p.pos++
		}
		return nil, p.fail("a string literal is not closed")
	case c == '-' || (c >= '0' && c <= '9'):
		start := p.pos
		p.pos++
		for p.pos < len(p.src) && (p.src[p.pos] == '.' || (p.src[p.pos] >= '0' && p.src[p.pos] <= '9')) {
			p.pos++
		}
		n, err := strconv.ParseFloat(p.src[start:p.pos], 64)
		if err != nil {
			return nil, p.fail("'%s' is not a number", p.src[start:p.pos])
		}
		return n, nil
	}
	name := p.identifier()
	if name == "" {
		return nil, p.fail("unexpected '%c'", c)
	}
	p.skipSpace()
	if !strings.HasPrefix(p.src[p.pos:], "(") {
		switch name {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		return nil, p.fail("'%s' is not a function call", name)
	}
	p.pos++
	var args []any
	p.skipSpace()
	if strings.HasPrefix(p.src[p.pos:], ")") {
		p.pos++
		return logicCall(p.run, name, args)
	}
	for {
		arg, err := p.expression()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		p.skipSpace()
		switch {
		case strings.HasPrefix(p.src[p.pos:], ","):
			p.pos++
		case strings.HasPrefix(p.src[p.pos:], ")"):
			p.pos++
			return logicCall(p.run, name, args)
		default:
			return nil, p.fail("expected ',' or ')' in the arguments of '%s'", name)
		}
	}
}

// logicIndex reads a property or element. The ? operator makes a missing
// property null instead of an error.
func logicIndex(value, index any, optional bool) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		key := logicToString(index)
		if got, ok := v[key]; ok {
			return got, nil
		}
		for k, got := range v {
			if strings.EqualFold(k, key) {
				return got, nil
			}
		}
		if optional {
			return nil, nil
		}
		return nil, logicErrorf("The template language expression cannot be evaluated because property '%s' doesn't exist, available properties are '%s'.", key, strings.Join(logicKeys(v), ", "))
	case []any:
		n, ok := logicNumber(index)
		if !ok || n < 0 || int(n) >= len(v) {
			if optional {
				return nil, nil
			}
			return nil, logicErrorf("The template language expression cannot be evaluated because array index '%v' is outside bounds (0 - %d) of array.", index, len(v)-1)
		}
		return v[int(n)], nil
	case nil:
		if optional {
			return nil, nil
		}
		return nil, logicErrorf("The template language expression cannot be evaluated because property '%v' cannot be selected from a null value.", index)
	}
	return nil, logicErrorf("The template language expression cannot be evaluated because property '%v' cannot be selected from a value of type '%s'.", index, logicTypeName(value))
}

func logicKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func logicToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(encoded)
}

func logicTruthy(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		return strconv.ParseBool(t)
	}
	return false, logicErrorf("The template language expression cannot be evaluated: expected a Boolean, got a value of type '%s'.", logicTypeName(v))
}

func logicEquals(a, b any) bool {
	if x, ok := a.(bool); ok {
		if n, ok := logicNumber(b); ok {
			return (x && n == 1) || (!x && n == 0)
		}
	}
	if x, ok := b.(bool); ok {
		if n, ok := logicNumber(a); ok {
			return (x && n == 1) || (!x && n == 0)
		}
	}
	if x, ok := a.(float64); ok {
		if y, ok := b.(float64); ok {
			return x == y
		}
	}
	return reflect.DeepEqual(a, b)
}

func logicCompare(a, b any) (int, error) {
	if x, ok := logicNumber(a); ok {
		if y, ok := logicNumber(b); ok {
			switch {
			case x < y:
				return -1, nil
			case x > y:
				return 1, nil
			}
			return 0, nil
		}
	}
	x, xs := a.(string)
	y, ys := b.(string)
	if xs && ys {
		return strings.Compare(x, y), nil
	}
	return 0, logicErrorf("The template language function cannot compare values of type '%s' and '%s'.", logicTypeName(a), logicTypeName(b))
}

func logicArgs(name string, args []any, n int) error {
	if len(args) != n {
		return logicErrorf("The template language function '%s' expects %d parameter(s); it was given %d.", name, n, len(args))
	}
	return nil
}

func logicStringArg(name string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", logicErrorf("The template language function '%s' expects a string parameter; it was given a value of type '%s'.", name, logicTypeName(v))
	}
	return s, nil
}

// logicCall runs one expression function.
func logicCall(run *logicRun, name string, args []any) (any, error) {
	switch name {
	case "equals":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		return logicEquals(args[0], args[1]), nil
	case "not":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		b, err := logicTruthy(args[0])
		return !b, err
	case "and", "or":
		if len(args) < 1 {
			return nil, logicArgs(name, args, 2)
		}
		result := name == "and"
		for _, a := range args {
			b, err := logicTruthy(a)
			if err != nil {
				return nil, err
			}
			if name == "and" {
				result = result && b
			} else {
				result = result || b
			}
		}
		return result, nil
	case "greater", "greaterOrEquals", "less", "lessOrEquals":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		c, err := logicCompare(args[0], args[1])
		if err != nil {
			return nil, err
		}
		switch name {
		case "greater":
			return c > 0, nil
		case "greaterOrEquals":
			return c >= 0, nil
		case "less":
			return c < 0, nil
		}
		return c <= 0, nil
	case "if":
		if err := logicArgs(name, args, 3); err != nil {
			return nil, err
		}
		b, err := logicTruthy(args[0])
		if err != nil {
			return nil, err
		}
		if b {
			return args[1], nil
		}
		return args[2], nil
	case "concat":
		var b strings.Builder
		for _, a := range args {
			b.WriteString(logicToString(a))
		}
		return b.String(), nil
	case "add", "sub", "mul", "div", "mod":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		x, xok := logicNumber(args[0])
		y, yok := logicNumber(args[1])
		if !xok || !yok {
			return nil, logicErrorf("The template language function '%s' expects numeric parameters.", name)
		}
		switch name {
		case "add":
			return x + y, nil
		case "sub":
			return x - y, nil
		case "mul":
			return x * y, nil
		}
		if y == 0 {
			return nil, logicErrorf("The template language function '%s' cannot divide by zero.", name)
		}
		if name == "div" {
			if x == math.Trunc(x) && y == math.Trunc(y) {
				return math.Trunc(x / y), nil
			}
			return x / y, nil
		}
		return math.Mod(x, y), nil
	case "string":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		return logicToString(args[0]), nil
	case "int", "float":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		n, ok := logicNumber(args[0])
		if !ok || (name == "int" && n != math.Trunc(n)) {
			return nil, logicErrorf("The template language function '%s' was invoked with a parameter that is not valid. The value cannot be converted to the target type.", name)
		}
		return n, nil
	case "bool":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		if n, ok := args[0].(float64); ok {
			return n != 0, nil
		}
		return logicTruthy(args[0])
	case "json":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		s, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		var out any
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, logicErrorf("The template language function 'json' parameter is not valid. The provided value '%s' cannot be parsed: %v", s, err)
		}
		return out, nil
	case "length":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		switch v := args[0].(type) {
		case string:
			return float64(len([]rune(v))), nil
		case []any:
			return float64(len(v)), nil
		}
		return nil, logicErrorf("The template language function 'length' expects its parameter to be an array or a string.")
	case "empty":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		switch v := args[0].(type) {
		case nil:
			return true, nil
		case string:
			return v == "", nil
		case []any:
			return len(v) == 0, nil
		case map[string]any:
			return len(v) == 0, nil
		}
		return false, nil
	case "contains":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		switch v := args[0].(type) {
		case string:
			return strings.Contains(v, logicToString(args[1])), nil
		case []any:
			for _, e := range v {
				if logicEquals(e, args[1]) {
					return true, nil
				}
			}
			return false, nil
		case map[string]any:
			_, ok := v[logicToString(args[1])]
			return ok, nil
		}
		return nil, logicErrorf("The template language function 'contains' expects its first parameter to be a dictionary, an array or a string.")
	case "startsWith", "endsWith":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		text, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		search, err := logicStringArg(name, args[1])
		if err != nil {
			return nil, err
		}
		if name == "startsWith" {
			return strings.HasPrefix(strings.ToLower(text), strings.ToLower(search)), nil
		}
		return strings.HasSuffix(strings.ToLower(text), strings.ToLower(search)), nil
	case "toLower", "toUpper", "trim":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		s, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		switch name {
		case "toLower":
			return strings.ToLower(s), nil
		case "toUpper":
			return strings.ToUpper(s), nil
		}
		return strings.TrimSpace(s), nil
	case "indexOf":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		text, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		search, err := logicStringArg(name, args[1])
		if err != nil {
			return nil, err
		}
		return float64(sim.CaseInsensitiveIndex(text, search)), nil
	case "replace":
		if err := logicArgs(name, args, 3); err != nil {
			return nil, err
		}
		text, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		return strings.ReplaceAll(text, logicToString(args[1]), logicToString(args[2])), nil
	case "split":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		text, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		parts := strings.Split(text, logicToString(args[1]))
		out := make([]any, len(parts))
		for i, part := range parts {
			out[i] = part
		}
		return out, nil
	case "join":
		if err := logicArgs(name, args, 2); err != nil {
			return nil, err
		}
		items, ok := args[0].([]any)
		if !ok {
			return nil, logicErrorf("The template language function 'join' expects its first parameter to be an array.")
		}
		parts := make([]string, len(items))
		for i, item := range items {
			parts[i] = logicToString(item)
		}
		return strings.Join(parts, logicToString(args[1])), nil
	case "substring":
		if len(args) != 2 && len(args) != 3 {
			return nil, logicArgs(name, args, 3)
		}
		text, err := logicStringArg(name, args[0])
		if err != nil {
			return nil, err
		}
		start, ok := logicNumber(args[1])
		length := float64(len(text)) - start
		if len(args) == 3 {
			length, ok = logicNumber(args[2])
		}
		if !ok || start < 0 || length < 0 || int(start+length) > len(text) {
			return nil, logicErrorf("The template language function 'substring' parameters are out of range: 'start index' and 'length' must be non-negative and within the string.")
		}
		return text[int(start):int(start+length)], nil
	case "first", "last":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		switch v := args[0].(type) {
		case string:
			if v == "" {
				return nil, nil
			}
			r := []rune(v)
			if name == "first" {
				return string(r[0]), nil
			}
			return string(r[len(r)-1]), nil
		case []any:
			if len(v) == 0 {
				return nil, nil
			}
			if name == "first" {
				return v[0], nil
			}
			return v[len(v)-1], nil
		}
		return nil, logicErrorf("The template language function '%s' expects its parameter to be an array or a string.", name)
	case "coalesce":
		for _, a := range args {
			if a != nil {
				return a, nil
			}
		}
		return nil, nil
	case "createArray":
		return append([]any{}, args...), nil
	case "utcNow":
		now := time.Now().UTC()
		if len(args) == 1 {
			return now.Format(logicDotNetLayout(logicToString(args[0]))), nil
		}
		return now.Format("2006-01-02T15:04:05.0000000Z"), nil
	case "variables":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		key := logicToString(args[0])
		value, ok := run.variables[key]
		if !ok {
			return nil, logicErrorf("The variable '%s' has not been initialized.", key)
		}
		return value, nil
	case "parameters":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		key := logicToString(args[0])
		value, ok := run.parameters[key]
		if !ok {
			return nil, logicErrorf("The workflow parameter '%s' is not found.", key)
		}
		return value, nil
	case "triggerBody":
		return run.trigger["body"], nil
	case "triggerOutputs":
		return run.trigger, nil
	case "outputs", "body", "actions":
		if err := logicArgs(name, args, 1); err != nil {
			return nil, err
		}
		action := logicToString(args[0])
		outputs, err := run.logicActionOutputs(action)
		if err != nil {
			return nil, err
		}
		switch name {
		case "body":
			return outputs["body"], nil
		case "outputs":
			return outputs, nil
		}
		result := run.results[action]
		return map[string]any{"name": action, "status": result.Status, "code": result.Code, "outputs": outputs,
			"inputs": result.Inputs, "error": result.Error}, nil
	case "item":
		if len(run.items) == 0 {
			return nil, logicErrorf("The template language function 'item' must be used inside a Foreach loop.")
		}
		return run.items[len(run.items)-1], nil
	}
	return nil, logicErrorf("The template function '%s' is not defined or not valid.", name)
}

// logicDotNetLayout translates the .NET format strings utcNow accepts for
// its common specifiers.
func logicDotNetLayout(format string) string {
	switch format {
	case "", "o":
		return "2006-01-02T15:04:05.0000000Z"
	case "s":
		return "2006-01-02T15:04:05"
	case "yyyy-MM-dd":
		return "2006-01-02"
	case "yyyyMMdd":
		return "20060102"
	case "HH:mm:ss":
		return "15:04:05"
	}
	r := strings.NewReplacer("yyyy", "2006", "MM", "01", "dd", "02", "HH", "15", "mm", "04", "ss", "05")
	return r.Replace(format)
}
