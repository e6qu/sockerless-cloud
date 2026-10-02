package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type kqlCompiled struct {
	typ  string
	eval func(row []any) any
}

// kqlBinder resolves a query's names against the schema flowing into each
// operator, types every expression before any row is read, and turns the
// expression into a function of the row.
type kqlBinder struct {
	src    string
	op     string
	schema []Column
	now    time.Time
}

func (b *kqlBinder) columnIndex(name string) int {
	for i, c := range b.schema {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func (b *kqlBinder) unresolved(name string) *kqlError {
	return kqlSemanticError("SEM0100",
		fmt.Sprintf("'%s' operator: Failed to resolve scalar expression named '%s'", b.op, name))
}

func kqlIsNumeric(t string) bool { return t == "int" || t == "long" || t == "real" }

func kqlComparable(a, b string) bool {
	if kqlIsNumeric(a) && kqlIsNumeric(b) {
		return true
	}
	return a == b
}

func kqlTypeMismatch(op, a, b string) *kqlError {
	return kqlSemanticError("", fmt.Sprintf(
		"Cannot compare values of types %s and %s. Try adding explicit casts (operator '%s')", a, b, op))
}

func (b *kqlBinder) compile(e kqlExpr) (kqlCompiled, *kqlError) {
	switch e := e.(type) {
	case kqlLiteral:
		v := e.val
		return kqlCompiled{typ: e.typ, eval: func([]any) any { return v }}, nil
	case kqlColumnRef:
		i := b.columnIndex(e.name)
		if i < 0 {
			return kqlCompiled{}, b.unresolved(e.name)
		}
		return kqlCompiled{typ: b.schema[i].Type, eval: func(row []any) any { return row[i] }}, nil
	case kqlUnaryMinus:
		x, err := b.compile(e.x)
		if err != nil {
			return kqlCompiled{}, err
		}
		switch x.typ {
		case "int", "long", "real", "timespan":
		default:
			return kqlCompiled{}, kqlSemanticError("", fmt.Sprintf("Unary minus cannot be applied to a value of type %s", x.typ))
		}
		return kqlCompiled{typ: x.typ, eval: func(row []any) any {
			switch v := x.eval(row).(type) {
			case int64:
				return -v
			case float64:
				return -v
			case time.Duration:
				return -v
			}
			return nil
		}}, nil
	case kqlBinary:
		return b.compileBinary(e)
	case kqlInList:
		return b.compileIn(e)
	case kqlBetween:
		return b.compileBetween(e)
	case kqlCall:
		return b.compileCall(e)
	}
	return kqlCompiled{}, kqlSyntaxError(b.src, e.position(), "")
}

func (b *kqlBinder) compileBinary(e kqlBinary) (kqlCompiled, *kqlError) {
	l, err := b.compile(e.l)
	if err != nil {
		return kqlCompiled{}, err
	}
	r, err := b.compile(e.r)
	if err != nil {
		return kqlCompiled{}, err
	}
	switch e.op {
	case "and", "or":
		if l.typ != "bool" || r.typ != "bool" {
			return kqlCompiled{}, kqlSemanticError("", fmt.Sprintf(
				"The operator '%s' requires operands of type bool, got %s and %s", e.op, l.typ, r.typ))
		}
		and := e.op == "and"
		return kqlCompiled{typ: "bool", eval: func(row []any) any {
			lv := l.eval(row) == true
			if and && !lv {
				return false
			}
			if !and && lv {
				return true
			}
			return r.eval(row) == true
		}}, nil
	case "==", "!=", "<", "<=", ">", ">=":
		if !kqlComparable(l.typ, r.typ) {
			return kqlCompiled{}, kqlTypeMismatch(e.op, l.typ, r.typ)
		}
		op := e.op
		return kqlCompiled{typ: "bool", eval: func(row []any) any {
			lv, rv := l.eval(row), r.eval(row)
			if lv == nil || rv == nil {
				return false
			}
			c := kqlCompare(lv, rv)
			switch op {
			case "==":
				return c == 0
			case "!=":
				return c != 0
			case "<":
				return c < 0
			case "<=":
				return c <= 0
			case ">":
				return c > 0
			}
			return c >= 0
		}}, nil
	case "=~", "!~":
		if l.typ != "string" || r.typ != "string" {
			return kqlCompiled{}, kqlTypeMismatch(e.op, l.typ, r.typ)
		}
		want := e.op == "=~"
		return kqlCompiled{typ: "bool", eval: func(row []any) any {
			return strings.EqualFold(kqlToString(l.eval(row)), kqlToString(r.eval(row))) == want
		}}, nil
	case "matches regex":
		lit, ok := e.r.(kqlLiteral)
		if !ok || lit.typ != "string" {
			return kqlCompiled{}, kqlSemanticError("", "The right side of 'matches regex' must be a constant string")
		}
		pattern, _ := lit.val.(string)
		re, rerr := regexp.Compile(pattern)
		if rerr != nil {
			return kqlCompiled{}, kqlSemanticError("", "Invalid regular expression: "+rerr.Error())
		}
		return kqlCompiled{typ: "bool", eval: func(row []any) any {
			return re.MatchString(kqlToString(l.eval(row)))
		}}, nil
	case "+", "-", "*", "/", "%":
		return kqlCompileArithmetic(e.op, l, r)
	}
	if kqlStringOperators[e.op] {
		match := kqlStringMatcher(e.op)
		negated := strings.HasPrefix(e.op, "!")
		return kqlCompiled{typ: "bool", eval: func(row []any) any {
			return match(kqlToString(l.eval(row)), kqlToString(r.eval(row))) != negated
		}}, nil
	}
	return kqlCompiled{}, kqlSyntaxError(b.src, e.pos, e.op)
}

// kqlStringMatcher returns the test a string operator applies. Operators
// without the _cs suffix ignore case, as Kusto's do.
func kqlStringMatcher(op string) func(haystack, needle string) bool {
	base := strings.TrimPrefix(op, "!")
	caseSensitive := strings.HasSuffix(base, "_cs")
	base = strings.TrimSuffix(base, "_cs")
	fold := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	return func(haystack, needle string) bool {
		h, n := fold(haystack), fold(needle)
		switch base {
		case "contains":
			return strings.Contains(h, n)
		case "startswith":
			return strings.HasPrefix(h, n)
		case "endswith":
			return strings.HasSuffix(h, n)
		case "has":
			return kqlHasTerm(h, n, true, true)
		case "hasprefix":
			return kqlHasTerm(h, n, true, false)
		case "hassuffix":
			return kqlHasTerm(h, n, false, true)
		}
		return false
	}
}

// kqlHasTerm reports whether needle occurs in haystack on term boundaries: a
// term is a run of letters and digits, so `has "error"` matches "an error:"
// but not "errors".
func kqlHasTerm(haystack, needle string, boundaryBefore, boundaryAfter bool) bool {
	if needle == "" {
		return true
	}
	isTermRune := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from := 0; from <= len(haystack)-len(needle); {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(needle)
		prev, _ := utf8.DecodeLastRuneInString(haystack[:start])
		following, _ := utf8.DecodeRuneInString(haystack[end:])
		before := start == 0 || !isTermRune(prev)
		after := end == len(haystack) || !isTermRune(following)
		if (!boundaryBefore || before) && (!boundaryAfter || after) {
			return true
		}
		from = start + 1
	}
	return false
}

func kqlCompileArithmetic(op string, l, r kqlCompiled) (kqlCompiled, *kqlError) {
	invalid := kqlSemanticError("", fmt.Sprintf(
		"Operator '%s' cannot be applied to operands of types %s and %s", op, l.typ, r.typ))
	var typ string
	switch {
	case kqlIsNumeric(l.typ) && kqlIsNumeric(r.typ):
		typ = "long"
		if l.typ == "real" || r.typ == "real" {
			typ = "real"
		}
	case l.typ == "datetime" && r.typ == "datetime" && op == "-":
		typ = "timespan"
	case l.typ == "datetime" && r.typ == "timespan" && (op == "+" || op == "-"):
		typ = "datetime"
	case l.typ == "timespan" && r.typ == "datetime" && op == "+":
		typ = "datetime"
	case l.typ == "timespan" && r.typ == "timespan" && (op == "+" || op == "-"):
		typ = "timespan"
	case l.typ == "timespan" && r.typ == "timespan" && op == "/":
		typ = "real"
	case l.typ == "timespan" && kqlIsNumeric(r.typ) && (op == "*" || op == "/"):
		typ = "timespan"
	case kqlIsNumeric(l.typ) && r.typ == "timespan" && op == "*":
		typ = "timespan"
	default:
		return kqlCompiled{}, invalid
	}
	return kqlCompiled{typ: typ, eval: func(row []any) any {
		lv, rv := l.eval(row), r.eval(row)
		if lv == nil || rv == nil {
			return nil
		}
		return kqlArithmetic(op, typ, lv, rv)
	}}, nil
}

func kqlArithmetic(op, typ string, lv, rv any) any {
	switch typ {
	case "long":
		a, aok := lv.(int64)
		b, bok := rv.(int64)
		if !aok || !bok {
			return nil
		}
		switch op {
		case "+":
			return a + b
		case "-":
			return a - b
		case "*":
			return a * b
		case "/":
			if b == 0 {
				return nil
			}
			return a / b
		case "%":
			if b == 0 {
				return nil
			}
			return a % b
		}
	case "real":
		if ld, ok := lv.(time.Duration); ok {
			rd, ok := rv.(time.Duration)
			if !ok || rd == 0 {
				return nil
			}
			return float64(ld) / float64(rd)
		}
		a, b := kqlToFloat(lv), kqlToFloat(rv)
		var v float64
		switch op {
		case "+":
			v = a + b
		case "-":
			v = a - b
		case "*":
			v = a * b
		case "/":
			v = a / b
		case "%":
			v = math.Mod(a, b)
		}
		return v
	case "timespan":
		if lt, ok := lv.(time.Time); ok {
			rt, ok := rv.(time.Time)
			if !ok {
				return nil
			}
			return lt.Sub(rt)
		}
		if ld, ok := lv.(time.Duration); ok {
			switch rv := rv.(type) {
			case time.Duration:
				if op == "+" {
					return ld + rv
				}
				return ld - rv
			default:
				f := kqlToFloat(rv)
				if op == "*" {
					return time.Duration(float64(ld) * f)
				}
				if f == 0 {
					return nil
				}
				return time.Duration(float64(ld) / f)
			}
		}
		rd, ok := rv.(time.Duration)
		if !ok {
			return nil
		}
		return time.Duration(kqlToFloat(lv) * float64(rd))
	case "datetime":
		if lt, ok := lv.(time.Time); ok {
			d, ok := rv.(time.Duration)
			if !ok {
				return nil
			}
			if op == "+" {
				return lt.Add(d)
			}
			return lt.Add(-d)
		}
		rt, tok := rv.(time.Time)
		d, dok := lv.(time.Duration)
		if !tok || !dok {
			return nil
		}
		return rt.Add(d)
	}
	return nil
}

func (b *kqlBinder) compileIn(e kqlInList) (kqlCompiled, *kqlError) {
	x, err := b.compile(e.x)
	if err != nil {
		return kqlCompiled{}, err
	}
	items := make([]kqlCompiled, 0, len(e.list))
	for _, item := range e.list {
		c, err := b.compile(item)
		if err != nil {
			return kqlCompiled{}, err
		}
		if e.ci && c.typ != "string" {
			return kqlCompiled{}, kqlTypeMismatch(e.op, "string", c.typ)
		}
		if !e.ci && !kqlComparable(x.typ, c.typ) {
			return kqlCompiled{}, kqlTypeMismatch(e.op, x.typ, c.typ)
		}
		items = append(items, c)
	}
	if e.op == "has_any" {
		return kqlCompiled{typ: "bool", eval: func(row []any) any {
			h := strings.ToLower(kqlToString(x.eval(row)))
			for _, item := range items {
				if kqlHasTerm(h, strings.ToLower(kqlToString(item.eval(row))), true, true) {
					return true
				}
			}
			return false
		}}, nil
	}
	if e.ci && x.typ != "string" {
		return kqlCompiled{}, kqlTypeMismatch(e.op, x.typ, "string")
	}
	ci, negated := e.ci, e.negated
	return kqlCompiled{typ: "bool", eval: func(row []any) any {
		v := x.eval(row)
		if v == nil {
			return false
		}
		for _, item := range items {
			iv := item.eval(row)
			if iv == nil {
				continue
			}
			if ci && strings.EqualFold(kqlToString(v), kqlToString(iv)) || !ci && kqlCompare(v, iv) == 0 {
				return !negated
			}
		}
		return negated
	}}, nil
}

func (b *kqlBinder) compileBetween(e kqlBetween) (kqlCompiled, *kqlError) {
	x, err := b.compile(e.x)
	if err != nil {
		return kqlCompiled{}, err
	}
	lo, err := b.compile(e.lo)
	if err != nil {
		return kqlCompiled{}, err
	}
	hi, err := b.compile(e.hi)
	if err != nil {
		return kqlCompiled{}, err
	}
	switch x.typ {
	case "int", "long", "real", "datetime", "timespan":
	default:
		return kqlCompiled{}, kqlSemanticError("", fmt.Sprintf("The 'between' operator cannot be applied to a value of type %s", x.typ))
	}
	if !kqlComparable(x.typ, lo.typ) {
		return kqlCompiled{}, kqlTypeMismatch("between", x.typ, lo.typ)
	}
	hiIsSpan := x.typ == "datetime" && hi.typ == "timespan"
	if !hiIsSpan && !kqlComparable(x.typ, hi.typ) {
		return kqlCompiled{}, kqlTypeMismatch("between", x.typ, hi.typ)
	}
	negated := e.negated
	return kqlCompiled{typ: "bool", eval: func(row []any) any {
		v, lv, hv := x.eval(row), lo.eval(row), hi.eval(row)
		if v == nil || lv == nil || hv == nil {
			return false
		}
		if hiIsSpan {
			start, sok := lv.(time.Time)
			span, dok := hv.(time.Duration)
			if !sok || !dok {
				return false
			}
			hv = start.Add(span)
		}
		inside := kqlCompare(v, lv) >= 0 && kqlCompare(v, hv) <= 0
		return inside != negated
	}}, nil
}

func (b *kqlBinder) compileCall(e kqlCall) (kqlCompiled, *kqlError) {
	if kqlAggregates[e.name] {
		return kqlCompiled{}, kqlSemanticError("", fmt.Sprintf(
			"'%s' operator: the aggregation function '%s' is only allowed in the context of summarize", b.op, e.name))
	}
	args := make([]kqlCompiled, 0, len(e.args))
	for _, a := range e.args {
		c, err := b.compile(a)
		if err != nil {
			return kqlCompiled{}, err
		}
		args = append(args, c)
	}
	arity := func(min, max int) *kqlError {
		if len(args) < min || len(args) > max {
			return kqlSemanticError("", fmt.Sprintf(
				"'%s' operator: function '%s' was called with %d arguments", b.op, e.name, len(args)))
		}
		return nil
	}
	wantType := func(i int, types ...string) *kqlError {
		for _, t := range types {
			if args[i].typ == t {
				return nil
			}
		}
		return kqlSemanticError("", fmt.Sprintf(
			"'%s' operator: function '%s' expects argument %d of type %s, got %s",
			b.op, e.name, i+1, strings.Join(types, " or "), args[i].typ))
	}
	unary := func(typ string, f func(any) any) (kqlCompiled, *kqlError) {
		if err := arity(1, 1); err != nil {
			return kqlCompiled{}, err
		}
		x := args[0]
		return kqlCompiled{typ: typ, eval: func(row []any) any { return f(x.eval(row)) }}, nil
	}
	now := b.now
	switch e.name {
	case "now":
		if err := arity(0, 1); err != nil {
			return kqlCompiled{}, err
		}
		if len(args) == 1 {
			if err := wantType(0, "timespan"); err != nil {
				return kqlCompiled{}, err
			}
			offset := args[0]
			return kqlCompiled{typ: "datetime", eval: func(row []any) any {
				d, ok := offset.eval(row).(time.Duration)
				if !ok {
					return nil
				}
				return now.Add(d)
			}}, nil
		}
		return kqlCompiled{typ: "datetime", eval: func([]any) any { return now }}, nil
	case "ago":
		if err := arity(1, 1); err != nil {
			return kqlCompiled{}, err
		}
		if err := wantType(0, "timespan"); err != nil {
			return kqlCompiled{}, err
		}
		return unary("datetime", func(v any) any {
			d, ok := v.(time.Duration)
			if !ok {
				return nil
			}
			return now.Add(-d)
		})
	case "todatetime":
		return unary("datetime", func(v any) any {
			switch v := v.(type) {
			case time.Time:
				return v
			case string:
				if t, ok := parseKQLDatetime(v); ok {
					return t
				}
			}
			return nil
		})
	case "tostring":
		return unary("string", func(v any) any { return kqlToString(v) })
	case "toint", "tolong":
		typ := "long"
		if e.name == "toint" {
			typ = "int"
		}
		return unary(typ, kqlToLong)
	case "todouble", "toreal":
		return unary("real", func(v any) any {
			switch v := v.(type) {
			case int64:
				return float64(v)
			case float64:
				return v
			case bool:
				if v {
					return 1.0
				}
				return 0.0
			case string:
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					return f
				}
			}
			return nil
		})
	case "tobool", "toboolean":
		return unary("bool", func(v any) any {
			switch v := v.(type) {
			case bool:
				return v
			case int64:
				return v != 0
			case float64:
				return v != 0
			case string:
				switch strings.ToLower(strings.TrimSpace(v)) {
				case "true", "1":
					return true
				case "false", "0":
					return false
				}
			}
			return nil
		})
	case "tolower", "toupper":
		upper := e.name == "toupper"
		return unary("string", func(v any) any {
			if upper {
				return strings.ToUpper(kqlToString(v))
			}
			return strings.ToLower(kqlToString(v))
		})
	case "strlen":
		return unary("long", func(v any) any { return int64(len([]rune(kqlToString(v)))) })
	case "isempty", "isnotempty":
		want := e.name == "isempty"
		return unary("bool", func(v any) any { return (v == nil || v == "") == want })
	case "isnull", "isnotnull":
		want := e.name == "isnull"
		return unary("bool", func(v any) any { return (v == nil) == want })
	case "not":
		if err := arity(1, 1); err != nil {
			return kqlCompiled{}, err
		}
		if err := wantType(0, "bool"); err != nil {
			return kqlCompiled{}, err
		}
		return unary("bool", func(v any) any { return v != true })
	case "strcat":
		if len(args) == 0 {
			return kqlCompiled{}, arity(1, 64)
		}
		return kqlCompiled{typ: "string", eval: func(row []any) any {
			var sb strings.Builder
			for _, a := range args {
				sb.WriteString(kqlToString(a.eval(row)))
			}
			return sb.String()
		}}, nil
	case "substring":
		if err := arity(2, 3); err != nil {
			return kqlCompiled{}, err
		}
		for i := 1; i < len(args); i++ {
			if err := wantType(i, "int", "long"); err != nil {
				return kqlCompiled{}, err
			}
		}
		return kqlCompiled{typ: "string", eval: func(row []any) any {
			s := []rune(kqlToString(args[0].eval(row)))
			start, ok := args[1].eval(row).(int64)
			if !ok {
				return ""
			}
			start = max(0, min(start, int64(len(s))))
			end := int64(len(s))
			if len(args) == 3 {
				n, ok := args[2].eval(row).(int64)
				if !ok {
					return ""
				}
				end = max(start, min(start+n, end))
			}
			return string(s[start:end])
		}}, nil
	case "iff", "iif":
		if err := arity(3, 3); err != nil {
			return kqlCompiled{}, err
		}
		if err := wantType(0, "bool"); err != nil {
			return kqlCompiled{}, err
		}
		typ := args[1].typ
		if !kqlComparable(args[1].typ, args[2].typ) {
			return kqlCompiled{}, kqlTypeMismatch(e.name, args[1].typ, args[2].typ)
		}
		if kqlIsNumeric(typ) && args[2].typ == "real" {
			typ = "real"
		}
		return kqlCompiled{typ: typ, eval: func(row []any) any {
			v := args[2].eval(row)
			if args[0].eval(row) == true {
				v = args[1].eval(row)
			}
			if typ == "real" {
				if i, ok := v.(int64); ok {
					return float64(i)
				}
			}
			return v
		}}, nil
	case "bin", "floor":
		if err := arity(2, 2); err != nil {
			return kqlCompiled{}, err
		}
		x, size := args[0], args[1]
		switch {
		case x.typ == "datetime" && size.typ == "timespan", x.typ == "timespan" && size.typ == "timespan":
		case kqlIsNumeric(x.typ) && kqlIsNumeric(size.typ):
		default:
			return kqlCompiled{}, kqlSemanticError("", fmt.Sprintf(
				"'%s' operator: function '%s' cannot bin a value of type %s by a value of type %s", b.op, e.name, x.typ, size.typ))
		}
		typ := x.typ
		if kqlIsNumeric(typ) && size.typ == "real" {
			typ = "real"
		}
		return kqlCompiled{typ: typ, eval: func(row []any) any {
			return kqlBin(x.eval(row), size.eval(row), typ)
		}}, nil
	}
	return kqlCompiled{}, kqlSemanticError("SEM0100",
		fmt.Sprintf("'%s' operator: Failed to resolve scalar expression named '%s'", b.op, e.name))
}

func kqlBin(v, size any, typ string) any {
	if v == nil || size == nil {
		return nil
	}
	switch v := v.(type) {
	case time.Time:
		d, ok := size.(time.Duration)
		if !ok || d <= 0 {
			return nil
		}
		ticks := v.UnixNano()
		bucket := ticks - ((ticks%int64(d))+int64(d))%int64(d)
		return time.Unix(0, bucket).UTC()
	case time.Duration:
		d, ok := size.(time.Duration)
		if !ok || d <= 0 {
			return nil
		}
		return time.Duration(math.Floor(float64(v)/float64(d))) * d
	}
	if typ == "real" {
		s := kqlToFloat(size)
		if s <= 0 {
			return nil
		}
		return math.Floor(kqlToFloat(v)/s) * s
	}
	s, sok := size.(int64)
	n, nok := v.(int64)
	if !sok || !nok || s <= 0 {
		return nil
	}
	return n - ((n%s)+s)%s
}

func kqlToFloat(v any) float64 {
	switch v := v.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

func kqlToLong(v any) any {
	switch v := v.(type) {
	case int64:
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		return int64(v)
	case bool:
		if v {
			return int64(1)
		}
		return int64(0)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return int64(f)
		}
	}
	return nil
}

// kqlCompare orders two non-null values of comparable types.
func kqlCompare(a, b any) int {
	switch a := a.(type) {
	case string:
		return strings.Compare(a, kqlToString(b))
	case bool:
		bb, _ := b.(bool)
		switch {
		case a == bb:
			return 0
		case !a:
			return -1
		}
		return 1
	case time.Time:
		bt, _ := b.(time.Time)
		return a.Compare(bt)
	case time.Duration:
		bd, _ := b.(time.Duration)
		switch {
		case a < bd:
			return -1
		case a > bd:
			return 1
		}
		return 0
	case int64:
		if bi, ok := b.(int64); ok {
			switch {
			case a < bi:
				return -1
			case a > bi:
				return 1
			}
			return 0
		}
	}
	af, bf := kqlToFloat(a), kqlToFloat(b)
	switch {
	case af < bf:
		return -1
	case af > bf:
		return 1
	}
	return 0
}

func kqlToString(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano)
	case time.Duration:
		return kqlFormatTimespan(v)
	}
	return fmt.Sprint(v)
}

// kqlFormatTimespan writes a timespan the way Kusto does: [-][d.]hh:mm:ss[.fffffff].
func kqlFormatTimespan(d time.Duration) string {
	sign := ""
	if d < 0 {
		sign = "-"
		d = -d
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	out := sign
	if days > 0 {
		out += strconv.FormatInt(int64(days), 10) + "."
	}
	out += fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if ticks := d / 100; ticks > 0 {
		out += fmt.Sprintf(".%07d", ticks)
	}
	return out
}

// kqlRenderValue converts an engine value into the JSON the query API returns
// for a cell of the given column type.
func kqlRenderValue(v any) any {
	switch v := v.(type) {
	case time.Time, time.Duration:
		return kqlToString(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
	}
	return v
}

var kqlAggregates = map[string]bool{
	"count": true, "countif": true, "dcount": true, "sum": true, "avg": true, "min": true, "max": true,
}

type kqlAggregator struct {
	name string
	typ  string
	init func() kqlAccumulator
}

type kqlAccumulator interface {
	add(row []any)
	result() any
}

type kqlCountAcc struct {
	pred *kqlCompiled
	n    int64
}

func (a *kqlCountAcc) add(row []any) {
	if a.pred == nil || a.pred.eval(row) == true {
		a.n++
	}
}
func (a *kqlCountAcc) result() any { return a.n }

type kqlDcountAcc struct {
	arg  *kqlCompiled
	seen map[string]bool
}

func (a *kqlDcountAcc) add(row []any) {
	if v := a.arg.eval(row); v != nil {
		a.seen[kqlKey(v)] = true
	}
}
func (a *kqlDcountAcc) result() any { return int64(len(a.seen)) }

type kqlSumAcc struct {
	arg   *kqlCompiled
	typ   string
	avg   bool
	sumI  int64
	sumF  float64
	sumD  time.Duration
	count int64
}

func (a *kqlSumAcc) add(row []any) {
	switch v := a.arg.eval(row).(type) {
	case int64:
		a.sumI += v
		a.sumF += float64(v)
		a.count++
	case float64:
		a.sumF += v
		a.count++
	case time.Duration:
		a.sumD += v
		a.count++
	}
}

func (a *kqlSumAcc) result() any {
	if a.avg {
		if a.count == 0 {
			return nil
		}
		if a.arg.typ == "timespan" {
			return a.sumD / time.Duration(a.count)
		}
		return a.sumF / float64(a.count)
	}
	switch a.typ {
	case "long":
		return a.sumI
	case "timespan":
		return a.sumD
	}
	return a.sumF
}

type kqlExtremeAcc struct {
	arg  *kqlCompiled
	max  bool
	best any
}

func (a *kqlExtremeAcc) add(row []any) {
	v := a.arg.eval(row)
	if v == nil {
		return
	}
	if a.best == nil {
		a.best = v
		return
	}
	c := kqlCompare(v, a.best)
	if a.max && c > 0 || !a.max && c < 0 {
		a.best = v
	}
}
func (a *kqlExtremeAcc) result() any { return a.best }

func (b *kqlBinder) compileAggregate(item kqlAssignment) (kqlAggregator, *kqlError) {
	call, ok := item.expr.(kqlCall)
	if !ok {
		return kqlAggregator{}, kqlSemanticError("",
			"'summarize' operator: an aggregate expression must be a call to an aggregation function")
	}
	if !kqlAggregates[call.name] {
		return kqlAggregator{}, kqlSemanticError("SEM0100",
			fmt.Sprintf("'summarize' operator: Failed to resolve aggregation function named '%s'", call.name))
	}
	agg := kqlAggregator{name: item.name}
	argName := ""
	if len(call.args) == 1 {
		if ref, ok := call.args[0].(kqlColumnRef); ok {
			argName = ref.name
		}
	}
	defaultName := call.name + "_" + argName
	wantArgs := func(n int) *kqlError {
		if len(call.args) != n {
			return kqlSemanticError("", fmt.Sprintf(
				"'summarize' operator: function '%s' was called with %d arguments", call.name, len(call.args)))
		}
		return nil
	}
	var arg *kqlCompiled
	if len(call.args) == 1 {
		c, err := b.compile(call.args[0])
		if err != nil {
			return kqlAggregator{}, err
		}
		arg = &c
	}
	switch call.name {
	case "count":
		if err := wantArgs(0); err != nil {
			return kqlAggregator{}, err
		}
		agg.typ = "long"
		defaultName = "count_"
		agg.init = func() kqlAccumulator { return &kqlCountAcc{} }
	case "countif":
		if err := wantArgs(1); err != nil {
			return kqlAggregator{}, err
		}
		if arg.typ != "bool" {
			return kqlAggregator{}, kqlSemanticError("", "'summarize' operator: countif expects a bool predicate")
		}
		agg.typ = "long"
		defaultName = "countif_"
		agg.init = func() kqlAccumulator { return &kqlCountAcc{pred: arg} }
	case "dcount":
		if err := wantArgs(1); err != nil {
			return kqlAggregator{}, err
		}
		agg.typ = "long"
		agg.init = func() kqlAccumulator { return &kqlDcountAcc{arg: arg, seen: map[string]bool{}} }
	case "sum", "avg":
		if err := wantArgs(1); err != nil {
			return kqlAggregator{}, err
		}
		if !kqlIsNumeric(arg.typ) && arg.typ != "timespan" {
			return kqlAggregator{}, kqlSemanticError("", fmt.Sprintf(
				"'summarize' operator: function '%s' cannot aggregate a value of type %s", call.name, arg.typ))
		}
		avg := call.name == "avg"
		switch {
		case arg.typ == "timespan":
			agg.typ = "timespan"
		case avg || arg.typ == "real":
			agg.typ = "real"
		default:
			agg.typ = "long"
		}
		typ := agg.typ
		agg.init = func() kqlAccumulator { return &kqlSumAcc{arg: arg, typ: typ, avg: avg} }
	case "min", "max":
		if err := wantArgs(1); err != nil {
			return kqlAggregator{}, err
		}
		agg.typ = arg.typ
		isMax := call.name == "max"
		agg.init = func() kqlAccumulator { return &kqlExtremeAcc{arg: arg, max: isMax} }
	}
	if agg.name == "" {
		agg.name = defaultName
	}
	return agg, nil
}

func kqlKey(v any) string {
	switch v := v.(type) {
	case time.Time:
		return "t:" + strconv.FormatInt(v.UnixNano(), 10)
	case nil:
		return "n:"
	}
	return fmt.Sprintf("%T:%v", v, v)
}

func kqlRowKey(row []any) string {
	var sb strings.Builder
	for _, v := range row {
		k := kqlKey(v)
		sb.WriteString(strconv.Itoa(len(k)))
		sb.WriteByte(':')
		sb.WriteString(k)
	}
	return sb.String()
}

// kqlColumnName names an output column: the name the query gave it, or the
// column an expression reads, or Kusto's ColumnN for anything else.
func kqlColumnName(item kqlAssignment, taken map[string]bool) string {
	if item.name != "" {
		return item.name
	}
	switch e := item.expr.(type) {
	case kqlColumnRef:
		return e.name
	case kqlCall:
		if (e.name == "bin" || e.name == "floor") && len(e.args) > 0 {
			if ref, ok := e.args[0].(kqlColumnRef); ok {
				return ref.name
			}
		}
	}
	for n := 1; ; n++ {
		name := "Column" + strconv.Itoa(n)
		if !taken[name] {
			return name
		}
	}
}

type kqlResultSet struct {
	columns []Column
	rows    [][]any
}

func (b *kqlBinder) compileItems(items []kqlAssignment, taken map[string]bool) ([]Column, []kqlCompiled, *kqlError) {
	cols := make([]Column, 0, len(items))
	fns := make([]kqlCompiled, 0, len(items))
	for _, item := range items {
		c, err := b.compile(item.expr)
		if err != nil {
			return nil, nil, err
		}
		name := kqlColumnName(item, taken)
		taken[name] = true
		cols = append(cols, Column{Name: name, Type: c.typ})
		fns = append(fns, c)
	}
	return cols, fns, nil
}

func kqlDuplicateColumn(op string, cols []Column) *kqlError {
	seen := map[string]bool{}
	for _, c := range cols {
		if seen[c.Name] {
			return kqlSemanticError("", fmt.Sprintf("'%s' operator: A column named '%s' is defined more than once", op, c.Name))
		}
		seen[c.Name] = true
	}
	return nil
}

// apply runs one tabular operator over the rows flowing into it.
func (b *kqlBinder) apply(op kqlOperator, in kqlResultSet) (kqlResultSet, *kqlError) {
	b.op = op.operatorName()
	b.schema = in.columns
	switch op := op.(type) {
	case kqlWhereOp:
		pred, err := b.compile(op.pred)
		if err != nil {
			return kqlResultSet{}, err
		}
		if pred.typ != "bool" {
			return kqlResultSet{}, kqlSemanticError("", fmt.Sprintf(
				"'where' operator: the predicate must be of type bool, got %s", pred.typ))
		}
		out := make([][]any, 0, len(in.rows))
		for _, row := range in.rows {
			if pred.eval(row) == true {
				out = append(out, row)
			}
		}
		return kqlResultSet{columns: in.columns, rows: out}, nil
	case kqlTakeOp:
		n := min(int64(len(in.rows)), max(op.n, 0))
		return kqlResultSet{columns: in.columns, rows: in.rows[:n]}, nil
	case kqlProjectOp:
		cols, fns, err := b.compileItems(op.items, map[string]bool{})
		if err != nil {
			return kqlResultSet{}, err
		}
		if err := kqlDuplicateColumn("project", cols); err != nil {
			return kqlResultSet{}, err
		}
		return kqlEvaluate(cols, fns, in.rows), nil
	case kqlProjectAwayOp:
		drop := map[string]bool{}
		for _, c := range op.cols {
			if b.columnIndex(c.name) < 0 {
				return kqlResultSet{}, b.unresolved(c.name)
			}
			drop[c.name] = true
		}
		var keep []int
		var cols []Column
		for i, c := range in.columns {
			if !drop[c.Name] {
				keep = append(keep, i)
				cols = append(cols, c)
			}
		}
		return kqlResultSet{columns: cols, rows: kqlSelect(in.rows, keep)}, nil
	case kqlProjectRenameOp:
		cols := append([]Column(nil), in.columns...)
		for _, pair := range op.pairs {
			i := b.columnIndex(pair[1].name)
			if i < 0 {
				return kqlResultSet{}, b.unresolved(pair[1].name)
			}
			cols[i].Name = pair[0].name
		}
		if err := kqlDuplicateColumn("project-rename", cols); err != nil {
			return kqlResultSet{}, err
		}
		return kqlResultSet{columns: cols, rows: in.rows}, nil
	case kqlExtendOp:
		// Each item sees the columns the items before it defined, as in
		// `extend a = 1, b = a + 1`.
		cols := append([]Column(nil), in.columns...)
		taken := map[string]bool{}
		for _, c := range cols {
			taken[c.Name] = true
		}
		target := make([]int, len(op.items))
		fns := make([]kqlCompiled, len(op.items))
		for j, item := range op.items {
			b.schema = cols
			fn, err := b.compile(item.expr)
			if err != nil {
				return kqlResultSet{}, err
			}
			c := Column{Name: kqlColumnName(item, taken), Type: fn.typ}
			taken[c.Name] = true
			fns[j] = fn
			target[j] = b.columnIndex(c.Name)
			if target[j] < 0 {
				target[j] = len(cols)
				cols = append(cols, c)
			} else {
				cols[target[j]] = c
			}
		}
		out := make([][]any, len(in.rows))
		for r, row := range in.rows {
			next := make([]any, len(cols))
			copy(next, row)
			for j, fn := range fns {
				next[target[j]] = fn.eval(next)
			}
			out[r] = next
		}
		return kqlResultSet{columns: cols, rows: out}, nil
	case kqlSortOp:
		return b.sortRows(in, op.keys)
	case kqlTopOp:
		sorted, err := b.sortRows(in, []kqlSortKey{op.key})
		if err != nil {
			return kqlResultSet{}, err
		}
		n := min(int64(len(sorted.rows)), max(op.n, 0))
		sorted.rows = sorted.rows[:n]
		return sorted, nil
	case kqlCountOp:
		return kqlResultSet{
			columns: []Column{{Name: "Count", Type: "long"}},
			rows:    [][]any{{int64(len(in.rows))}},
		}, nil
	case kqlSummarizeOp:
		return b.summarize(op, in)
	case kqlDistinctOp:
		cols, fns := in.columns, []kqlCompiled(nil)
		if op.all {
			for i, c := range in.columns {
				fns = append(fns, kqlCompiled{typ: c.Type, eval: func(row []any) any { return row[i] }})
			}
		} else {
			var err *kqlError
			cols, fns, err = b.compileItems(op.items, map[string]bool{})
			if err != nil {
				return kqlResultSet{}, err
			}
			if err := kqlDuplicateColumn("distinct", cols); err != nil {
				return kqlResultSet{}, err
			}
		}
		projected := kqlEvaluate(cols, fns, in.rows)
		seen := map[string]bool{}
		var out [][]any
		for _, row := range projected.rows {
			k := kqlRowKey(row)
			if !seen[k] {
				seen[k] = true
				out = append(out, row)
			}
		}
		return kqlResultSet{columns: cols, rows: out}, nil
	}
	return kqlResultSet{}, kqlSemanticError("", fmt.Sprintf("'%s' operator is not supported", b.op))
}

func kqlEvaluate(cols []Column, fns []kqlCompiled, rows [][]any) kqlResultSet {
	out := make([][]any, len(rows))
	for r, row := range rows {
		next := make([]any, len(fns))
		for j, fn := range fns {
			next[j] = fn.eval(row)
		}
		out[r] = next
	}
	return kqlResultSet{columns: cols, rows: out}
}

func kqlSelect(rows [][]any, keep []int) [][]any {
	out := make([][]any, len(rows))
	for r, row := range rows {
		next := make([]any, len(keep))
		for j, i := range keep {
			next[j] = row[i]
		}
		out[r] = next
	}
	return out
}

func (b *kqlBinder) sortRows(in kqlResultSet, keys []kqlSortKey) (kqlResultSet, *kqlError) {
	fns := make([]kqlCompiled, len(keys))
	for i, k := range keys {
		c, err := b.compile(k.expr)
		if err != nil {
			return kqlResultSet{}, err
		}
		fns[i] = c
	}
	type keyed struct {
		row  []any
		keys []any
	}
	items := make([]keyed, len(in.rows))
	for r, row := range in.rows {
		ks := make([]any, len(fns))
		for i, fn := range fns {
			ks[i] = fn.eval(row)
		}
		items[r] = keyed{row: row, keys: ks}
	}
	sort.SliceStable(items, func(x, y int) bool {
		for i, k := range keys {
			a, c := items[x].keys[i], items[y].keys[i]
			switch {
			case a == nil && c == nil:
				continue
			case a == nil:
				return k.nullsFirst
			case c == nil:
				return !k.nullsFirst
			}
			cmp := kqlCompare(a, c)
			if cmp == 0 {
				continue
			}
			if k.desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	out := make([][]any, len(items))
	for i, it := range items {
		out[i] = it.row
	}
	return kqlResultSet{columns: in.columns, rows: out}, nil
}

func (b *kqlBinder) summarize(op kqlSummarizeOp, in kqlResultSet) (kqlResultSet, *kqlError) {
	byCols, byFns, err := b.compileItems(op.by, map[string]bool{})
	if err != nil {
		return kqlResultSet{}, err
	}
	aggs := make([]kqlAggregator, 0, len(op.aggs))
	for _, item := range op.aggs {
		agg, err := b.compileAggregate(item)
		if err != nil {
			return kqlResultSet{}, err
		}
		aggs = append(aggs, agg)
	}
	cols := append([]Column(nil), byCols...)
	for _, agg := range aggs {
		cols = append(cols, Column{Name: agg.name, Type: agg.typ})
	}
	if err := kqlDuplicateColumn("summarize", cols); err != nil {
		return kqlResultSet{}, err
	}

	type group struct {
		keys []any
		accs []kqlAccumulator
	}
	newGroup := func(keys []any) *group {
		g := &group{keys: keys}
		for _, agg := range aggs {
			g.accs = append(g.accs, agg.init())
		}
		return g
	}
	var order []*group
	groups := map[string]*group{}
	for _, row := range in.rows {
		keys := make([]any, len(byFns))
		for i, fn := range byFns {
			keys[i] = fn.eval(row)
		}
		k := kqlRowKey(keys)
		g, ok := groups[k]
		if !ok {
			g = newGroup(keys)
			groups[k] = g
			order = append(order, g)
		}
		for _, acc := range g.accs {
			acc.add(row)
		}
	}
	// Without a by clause, summarize answers one row even over no input.
	if len(byFns) == 0 && len(order) == 0 {
		order = append(order, newGroup(nil))
	}
	out := make([][]any, 0, len(order))
	for _, g := range order {
		row := append([]any(nil), g.keys...)
		for _, acc := range g.accs {
			row = append(row, acc.result())
		}
		out = append(out, row)
	}
	return kqlResultSet{columns: cols, rows: out}, nil
}
