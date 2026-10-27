package main

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Applies an UpdateExpression to the stored item:
//
//	SET  path = operand                 (assignment)
//	SET  path = operand +|- operand     (numeric arithmetic)
//	SET  path = if_not_exists(path, op)  (default when absent)
//	REMOVE path[, path...]
//	ADD  path operand                   (number increment, or string/number-set union)
//	DELETE path operand                 (set-element removal)
//
// #name and :val placeholders resolve against the expression attribute maps.
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

// Create intermediate M and L containers as DynamoDB does. A list index equal
// to the length appends; beyond it is an error.
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

// ok is false when the path does not exist.
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
			lst = append(lst[:idx:idx], lst[idx+1:]...)
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

// Each of the four action keywords appears at most once.
func ddbSplitUpdateClauses(expr string) map[string]string {
	keywords := []string{"SET", "REMOVE", "ADD", "DELETE"}
	type mark struct {
		idx, end int
		kw       string
	}
	var marks []mark
	// strings.ToUpper can change the byte length of non-ASCII input, which would
	// misalign the keyword offsets into expr.
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

// Depth 0 only, so if_not_exists(a, b) commas do not split.
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

func ddbResolveOperand(token string, item map[string]any, names map[string]string, values map[string]any) (any, error) {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, ":") {
		v, ok := values[token]
		if !ok {
			return nil, fmt.Errorf("ExpressionAttributeValues missing %q", token)
		}
		return v, nil
	}
	// A copied attribute may be a nested document path.
	if v, ok := ddbResolvePath(item, token, names); ok {
		return v, nil
	}
	return nil, nil
}

// Strip parentheses wrapping the whole string, so `(if_not_exists(c,:0) - :v)`,
// ElectroDB's `.subtract()` shape, evaluates like the bare form; `(a) - (b)` is
// left intact.
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
	// Split +/- at the top level before the function-call branch, so an
	// if_not_exists(...) or list_append(...) operand resolves whole, as in
	// ElectroDB's `if_not_exists(#c,:0) - :1`.
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

func ddbEvalSetOperand(operand string, item map[string]any, names map[string]string, values map[string]any) (any, error) {
	operand = ddbStripParens(operand)
	// A known function prefix must be a balanced whole call; a malformed one is
	// rejected, not read as a path.
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

func ddbHasFuncPrefix(operand, name string) bool {
	pfx := name + "("
	return len(operand) >= len(pfx) && strings.EqualFold(operand[:len(pfx)], pfx)
}

// Returns the arguments when operand is exactly one balanced call with nothing
// after it.
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
				return operand[len(pfx) : len(operand)-1], i == len(operand)-1
			}
		}
	}
	return "", false
}

// A non-list operand contributes nothing.
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

// DynamoDB numbers carry up to 38 significant digits, beyond float64.
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

// Integers print exactly; fractions trim to 38 digits.
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
	// ADD on a missing attribute starts from 0.
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
			return nil // Emptying a set removes the attribute.
		}
		return map[string]any{st: kept}
	}
	return cur
}
