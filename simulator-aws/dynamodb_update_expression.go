package main

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// DynamoDB UpdateExpression evaluator.
//
// Modern clients (aws CLI / SDK / terraform-provider-aws) drive UpdateItem with
// an UpdateExpression, not the legacy AttributeUpdates parameter. This applies
// the common subset, in place, against the stored item (attribute values are
// the wire shape, e.g. {"N":"5"} / {"S":"x"} / {"SS":[...]}):
//
//	SET  path = operand                 (assignment)
//	SET  path = operand +|- operand     (numeric arithmetic)
//	SET  path = if_not_exists(path, op)  (default when absent)
//	REMOVE path[, path...]
//	ADD  path operand                   (number increment, or string/number-set union)
//	DELETE path operand                 (set-element removal)
//
// Placeholders #name (ExpressionAttributeNames) and :val
// (ExpressionAttributeValues) are resolved. Top-level attribute paths only —
// nested document paths are not modelled.
func ddbApplyUpdateExpression(item map[string]any, expr string, names map[string]string, values map[string]any) error {
	for kw, body := range ddbSplitUpdateClauses(expr) {
		switch kw {
		case "SET":
			for _, part := range ddbSplitTopLevel(body, ',') {
				eq := strings.Index(part, "=")
				if eq < 0 {
					return fmt.Errorf("invalid SET action %q", strings.TrimSpace(part))
				}
				val, err := ddbEvalSetRHS(strings.TrimSpace(part[eq+1:]), item, names, values)
				if err != nil {
					return err
				}
				if err := ddbSetByPath(item, strings.TrimSpace(part[:eq]), names, val); err != nil {
					return err
				}
			}
		case "REMOVE":
			for _, p := range ddbSplitTopLevel(body, ',') {
				ddbRemoveByPath(item, strings.TrimSpace(p), names)
			}
		case "ADD":
			for _, p := range ddbSplitTopLevel(body, ',') {
				path, operand, err := ddbPathOperand(p, item, names, values)
				if err != nil {
					return err
				}
				cur, _ := ddbResolvePath(item, path, names)
				if err := ddbSetByPath(item, path, names, ddbAddValues(cur, operand)); err != nil {
					return err
				}
			}
		case "DELETE":
			for _, p := range ddbSplitTopLevel(body, ',') {
				path, operand, err := ddbPathOperand(p, item, names, values)
				if err != nil {
					return err
				}
				if cur, ok := ddbResolvePath(item, path, names); ok {
					if err := ddbSetByPath(item, path, names, ddbDeleteSetElems(cur, operand)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// ddbSetByPath assigns value at a (possibly nested) document path — a top-level
// attribute or `a.b[0].c` — creating intermediate map (M) / list (L) containers
// as DynamoDB does. Setting list index == len appends; > len is an error.
func ddbSetByPath(item map[string]any, path string, names map[string]string, value any) error {
	segs := ddbSplitPath(path)
	if len(segs) == 0 {
		return fmt.Errorf("invalid path %q", path)
	}
	top := ddbResolveAttrName(segs[0], names)
	if len(segs) == 1 {
		item[top] = value
		return nil
	}
	nc, err := ddbAssignNested(item[top], segs[1:], names, value)
	if err != nil {
		return err
	}
	item[top] = nc
	return nil
}

// ddbAssignNested returns container (an AttributeValue) with value assigned at
// the nested segs, creating M/L containers as needed.
func ddbAssignNested(container any, segs []string, names map[string]string, value any) (any, error) {
	seg := segs[0]
	last := len(segs) == 1
	if strings.HasPrefix(seg, "[") {
		idx, err := strconv.Atoi(strings.Trim(seg, "[]"))
		if err != nil || idx < 0 {
			return nil, fmt.Errorf("invalid list index %q", seg)
		}
		m, _ := container.(map[string]any)
		lst, _ := m["L"].([]any)
		if idx > len(lst) {
			return nil, fmt.Errorf("list index %d out of range (len %d)", idx, len(lst))
		}
		var child any
		if idx < len(lst) {
			child = lst[idx]
		}
		if last {
			child = value
		} else if child, err = ddbAssignNested(child, segs[1:], names, value); err != nil {
			return nil, err
		}
		if idx == len(lst) {
			lst = append(lst, child)
		} else {
			lst[idx] = child
		}
		return map[string]any{"L": lst}, nil
	}
	m, _ := container.(map[string]any)
	mm, _ := m["M"].(map[string]any)
	if mm == nil {
		mm = map[string]any{}
	}
	name := ddbResolveAttrName(seg, names)
	if last {
		mm[name] = value
	} else {
		nc, err := ddbAssignNested(mm[name], segs[1:], names, value)
		if err != nil {
			return nil, err
		}
		mm[name] = nc
	}
	return map[string]any{"M": mm}, nil
}

// ddbRemoveByPath deletes the attribute at a (possibly nested) document path.
func ddbRemoveByPath(item map[string]any, path string, names map[string]string) {
	segs := ddbSplitPath(path)
	if len(segs) == 0 {
		return
	}
	top := ddbResolveAttrName(segs[0], names)
	if len(segs) == 1 {
		delete(item, top)
		return
	}
	if nc, ok := ddbDeleteNested(item[top], segs[1:], names); ok {
		item[top] = nc
	}
}

// ddbDeleteNested returns container with the attribute at segs removed; ok is
// false when the path doesn't exist (container left unchanged).
func ddbDeleteNested(container any, segs []string, names map[string]string) (any, bool) {
	seg := segs[0]
	last := len(segs) == 1
	m, isMap := container.(map[string]any)
	if !isMap {
		return container, false
	}
	if strings.HasPrefix(seg, "[") {
		idx, err := strconv.Atoi(strings.Trim(seg, "[]"))
		lst, _ := m["L"].([]any)
		if err != nil || idx < 0 || idx >= len(lst) {
			return container, false
		}
		if last {
			lst = append(lst[:idx:idx], lst[idx+1:]...) // remove + shift
			return map[string]any{"L": lst}, true
		}
		nc, ok := ddbDeleteNested(lst[idx], segs[1:], names)
		if !ok {
			return container, false
		}
		lst = append([]any(nil), lst...)
		lst[idx] = nc
		return map[string]any{"L": lst}, true
	}
	mm, _ := m["M"].(map[string]any)
	name := ddbResolveAttrName(seg, names)
	if _, present := mm[name]; !present {
		return container, false
	}
	if last {
		delete(mm, name)
		return map[string]any{"M": mm}, true
	}
	nc, ok := ddbDeleteNested(mm[name], segs[1:], names)
	if !ok {
		return container, false
	}
	mm[name] = nc
	return map[string]any{"M": mm}, true
}

// ddbSplitUpdateClauses splits an UpdateExpression into {KEYWORD: body}. The
// four action keywords each appear at most once and introduce a clause.
func ddbSplitUpdateClauses(expr string) map[string]string {
	keywords := []string{"SET", "REMOVE", "ADD", "DELETE"}
	type mark struct {
		idx, end int
		kw       string
	}
	var marks []mark
	// ASCII-only uppercasing that preserves byte length, so the keyword indices
	// computed against `upper` remain valid offsets into the original `expr`.
	// strings.ToUpper can change the byte length of non-ASCII / invalid-UTF-8
	// input, which would make the slice offsets below out-of-range for expr.
	upper := sim.ASCIIFoldUpper(expr)
	for _, kw := range keywords {
		for i := 0; i+len(kw) <= len(upper); i++ {
			if upper[i:i+len(kw)] != kw {
				continue
			}
			if i > 0 && isWordChar(upper[i-1]) {
				continue
			}
			if i+len(kw) < len(upper) && isWordChar(upper[i+len(kw)]) {
				continue
			}
			marks = append(marks, mark{idx: i, end: i + len(kw), kw: kw})
			break
		}
	}
	// Order clause starts so each body runs to the next clause.
	for i := 0; i < len(marks); i++ {
		for j := i + 1; j < len(marks); j++ {
			if marks[j].idx < marks[i].idx {
				marks[i], marks[j] = marks[j], marks[i]
			}
		}
	}
	out := map[string]string{}
	for i, m := range marks {
		bodyEnd := len(expr)
		if i+1 < len(marks) {
			bodyEnd = marks[i+1].idx
		}
		out[m.kw] = strings.TrimSpace(expr[m.end:bodyEnd])
	}
	return out
}

func isWordChar(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// ddbSplitTopLevel splits on sep at paren depth 0 (so if_not_exists(a, b)
// commas are not split).
func ddbSplitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

func ddbResolveName(token string, names map[string]string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "#") {
		if n, ok := names[token]; ok {
			return n
		}
	}
	return token
}

func ddbPathOperand(part string, item map[string]any, names map[string]string, values map[string]any) (string, any, error) {
	fields := strings.Fields(strings.TrimSpace(part))
	if len(fields) != 2 {
		return "", nil, fmt.Errorf("invalid action %q", strings.TrimSpace(part))
	}
	operand, err := ddbResolveOperand(fields[1], item, names, values)
	if err != nil {
		return "", nil, err
	}
	return ddbResolveName(fields[0], names), operand, nil
}

// ddbResolveOperand resolves a single operand to its attribute-value: a :value
// placeholder, or a #name / literal attribute's current value.
func ddbResolveOperand(token string, item map[string]any, names map[string]string, values map[string]any) (any, error) {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, ":") {
		v, ok := values[token]
		if !ok {
			return nil, fmt.Errorf("ExpressionAttributeValues missing %q", token)
		}
		return v, nil
	}
	// A bare path operand (copy-attribute) may be a nested document path
	// (a.b[0].c), not just a top-level attribute.
	if v, ok := ddbResolvePath(item, token, names); ok {
		return v, nil
	}
	return nil, nil
}

// ddbStripParens removes fully-enclosing balanced parentheses from a SET value
// (repeatedly), so `(if_not_exists(c,:0) - :v)` and `(:z)` evaluate the same as
// the unparenthesized form — the shape ElectroDB emits for `.subtract()`. Only a
// pair that wraps the WHOLE string is stripped: `(a) - (b)` is left intact
// because the first '(' closes before the end.
func ddbStripParens(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth, enclosing := 0, true
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					enclosing = false
				}
			}
		}
		if !enclosing || depth != 0 {
			break
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func ddbEvalSetRHS(rhs string, item map[string]any, names map[string]string, values map[string]any) (any, error) {
	rhs = ddbStripParens(rhs)
	// Binary +/- on numbers, split at the TOP level FIRST so an operand that is
	// itself an if_not_exists(...) / list_append(...) call (with its own commas
	// and parens) resolves as a whole — e.g. `if_not_exists(#c,:0) - :1`. Doing
	// this before the function-call branch is what makes the arithmetic forms
	// ElectroDB emits for .add()/.subtract() compute instead of storing null.
	for _, op := range []byte{'+', '-'} {
		if parts := ddbSplitTopLevel(rhs, op); len(parts) == 2 {
			a, err := ddbEvalSetOperand(parts[0], item, names, values)
			if err != nil {
				return nil, err
			}
			b, err := ddbEvalSetOperand(parts[1], item, names, values)
			if err != nil {
				return nil, err
			}
			an, aok := ddbToNumber(a)
			bn, bok := ddbToNumber(b)
			if !aok || !bok {
				return nil, fmt.Errorf("incorrect operand type for operator or function %c; expected a number", op)
			}
			if op == '+' {
				return ddbNumberValue(new(big.Rat).Add(an, bn)), nil
			}
			return ddbNumberValue(new(big.Rat).Sub(an, bn)), nil
		}
	}
	return ddbEvalSetOperand(rhs, item, names, values)
}

// ddbEvalSetOperand resolves a single SET operand: an if_not_exists(path,
// operand) call, a list_append(a, b) call, or a bare :value / #name / path.
func ddbEvalSetOperand(operand string, item map[string]any, names map[string]string, values map[string]any) (any, error) {
	operand = ddbStripParens(operand)
	// An operand beginning with a known function name MUST be a well-formed,
	// balanced whole call — `if_not_exists(` with no close paren is malformed and
	// rejected, not silently treated as a path.
	for _, fn := range []string{"if_not_exists", "list_append"} {
		if !ddbHasFuncPrefix(operand, fn) {
			continue
		}
		inner, ok := ddbWholeFuncCall(operand, fn)
		if !ok {
			return nil, fmt.Errorf("malformed %s call: %q", fn, operand)
		}
		args := ddbSplitTopLevel(inner, ',')
		if len(args) != 2 {
			return nil, fmt.Errorf("%s expects 2 args: %q", fn, operand)
		}
		if fn == "if_not_exists" {
			if cur, ok := ddbResolvePath(item, strings.TrimSpace(args[0]), names); ok {
				return cur, nil
			}
			return ddbEvalSetOperand(args[1], item, names, values)
		}
		a, err := ddbEvalSetOperand(args[0], item, names, values)
		if err != nil {
			return nil, err
		}
		b, err := ddbEvalSetOperand(args[1], item, names, values)
		if err != nil {
			return nil, err
		}
		return ddbListAppend(a, b), nil
	}
	return ddbResolveOperand(operand, item, names, values)
}

// ddbHasFuncPrefix reports whether operand begins with `name(` (case-insensitive).
func ddbHasFuncPrefix(operand, name string) bool {
	pfx := name + "("
	return len(operand) >= len(pfx) && strings.EqualFold(operand[:len(pfx)], pfx)
}

// ddbWholeFuncCall reports whether operand is exactly a balanced `name(...)`
// call (case-insensitive name, no trailing top-level operator) and returns the
// argument text between the outer parens.
func ddbWholeFuncCall(operand, name string) (string, bool) {
	pfx := name + "("
	if len(operand) < len(pfx)+1 || !strings.HasSuffix(operand, ")") {
		return "", false
	}
	if !strings.EqualFold(operand[:len(pfx)], pfx) {
		return "", false
	}
	depth := 0
	for i := len(pfx) - 1; i < len(operand); i++ {
		switch operand[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				// Whole operand is this single call only when its matching
				// close paren is the final byte (no trailing top-level op).
				return operand[len(pfx) : len(operand)-1], i == len(operand)-1
			}
		}
	}
	return "", false
}

// ddbListAppend concatenates two L-typed AttributeValues for the list_append()
// function; a non-list operand contributes nothing (treated as an empty list).
func ddbListAppend(a, b any) any {
	out := []any{}
	for _, v := range []any{a, b} {
		if m, ok := v.(map[string]any); ok {
			if l, ok := m["L"].([]any); ok {
				out = append(out, l...)
			}
		}
	}
	return map[string]any{"L": out}
}

// ddbToNumber parses a numeric AttributeValue into an exact rational (DynamoDB
// carries up to 38 significant digits — float64 would corrupt large/precise
// numbers). ok is false when v isn't a number.
func ddbToNumber(v any) (*big.Rat, bool) {
	if m, ok := v.(map[string]any); ok {
		if n, ok := m["N"].(string); ok {
			if r, ok := new(big.Rat).SetString(n); ok {
				return r, true
			}
		}
	}
	return nil, false
}

// ddbNumberValue formats an exact rational back into a DynamoDB number string:
// integers print exactly at any magnitude; fractional results print as a decimal
// trimmed to DynamoDB's 38-digit precision.
func ddbNumberValue(r *big.Rat) map[string]any {
	var s string
	if r.IsInt() {
		s = r.Num().String()
	} else {
		s = strings.TrimRight(r.FloatString(38), "0")
		s = strings.TrimRight(s, ".")
	}
	return map[string]any{"N": s}
}

// ddbAddValues implements ADD: numeric increment, or string/number-set union.
func ddbAddValues(cur, operand any) any {
	om, _ := operand.(map[string]any)
	if om == nil {
		return cur
	}
	for _, st := range []string{"SS", "NS", "BS"} {
		if add, ok := om[st].([]any); ok {
			existing := map[string]bool{}
			var union []any
			if cm, ok := cur.(map[string]any); ok {
				if curSet, ok := cm[st].([]any); ok {
					for _, e := range curSet {
						existing[fmt.Sprintf("%v", e)] = true
						union = append(union, e)
					}
				}
			}
			for _, e := range add {
				if !existing[fmt.Sprintf("%v", e)] {
					existing[fmt.Sprintf("%v", e)] = true
					union = append(union, e)
				}
			}
			return map[string]any{st: union}
		}
	}
	// number increment (ADD on a missing attribute starts from 0)
	cn, _ := ddbToNumber(cur)
	if cn == nil {
		cn = new(big.Rat)
	}
	on, _ := ddbToNumber(operand)
	if on == nil {
		on = new(big.Rat)
	}
	return ddbNumberValue(new(big.Rat).Add(cn, on))
}

// ddbDeleteSetElems implements DELETE: remove the operand's elements from a set.
func ddbDeleteSetElems(cur, operand any) any {
	cm, _ := cur.(map[string]any)
	om, _ := operand.(map[string]any)
	if cm == nil || om == nil {
		return cur
	}
	for _, st := range []string{"SS", "NS", "BS"} {
		curSet, ok1 := cm[st].([]any)
		delSet, ok2 := om[st].([]any)
		if !ok1 || !ok2 {
			continue
		}
		remove := map[string]bool{}
		for _, e := range delSet {
			remove[fmt.Sprintf("%v", e)] = true
		}
		var kept []any
		for _, e := range curSet {
			if !remove[fmt.Sprintf("%v", e)] {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			return nil // emptying a set removes the attribute
		}
		return map[string]any{st: kept}
	}
	return cur
}
