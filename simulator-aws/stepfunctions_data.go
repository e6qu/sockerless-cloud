package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mathrand "math/rand"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PaesslerAG/jsonpath"
	"github.com/google/uuid"
)

type sfnPathToken struct {
	key   string
	index *int
}

func sfnDecodeJSON(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func sfnEncodeJSON(value any) (string, error) {
	b, err := json.Marshal(value)
	return string(b), err
}

func sfnParseReferencePath(expr string) ([]sfnPathToken, error) {
	if expr == "$" || expr == "$$" {
		return nil, nil
	}
	if !strings.HasPrefix(expr, "$") {
		return nil, fmt.Errorf("reference path %q must start with $", expr)
	}
	i := 1
	if len(expr) > 1 && expr[1] == '$' {
		i = 2
	}
	var tokens []sfnPathToken
	for i < len(expr) {
		switch expr[i] {
		case '.':
			i++
			start := i
			for i < len(expr) && expr[i] != '.' && expr[i] != '[' {
				i++
			}
			if start == i {
				return nil, fmt.Errorf("empty reference-path member in %q", expr)
			}
			tokens = append(tokens, sfnPathToken{key: expr[start:i]})
		case '[':
			end := strings.IndexByte(expr[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated reference-path bracket in %q", expr)
			}
			end += i
			part := strings.TrimSpace(expr[i+1 : end])
			if len(part) >= 2 && ((part[0] == '\'' && part[len(part)-1] == '\'') ||
				(part[0] == '"' && part[len(part)-1] == '"')) {
				key := part[1 : len(part)-1]
				tokens = append(tokens, sfnPathToken{key: key})
			} else {
				index, err := strconv.Atoi(part)
				if err != nil || index < 0 {
					return nil, fmt.Errorf("invalid reference-path index %q in %q", part, expr)
				}
				tokens = append(tokens, sfnPathToken{index: &index})
			}
			i = end + 1
		default:
			return nil, fmt.Errorf("invalid reference-path character %q in %q", expr[i], expr)
		}
	}
	return tokens, nil
}

func sfnPathValue(input, context any, expr string) (any, bool) {
	root := input
	if strings.HasPrefix(expr, "$$") {
		root = context
	}
	tokens, err := sfnParseReferencePath(expr)
	if err != nil {
		selection := expr
		if strings.HasPrefix(selection, "$$") {
			selection = selection[1:]
		}
		value, selectionErr := jsonpath.Get(selection, root)
		return value, selectionErr == nil
	}
	current := root
	for _, token := range tokens {
		if token.index != nil {
			values, ok := current.([]any)
			if !ok || *token.index >= len(values) {
				return nil, false
			}
			current = values[*token.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[token.key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func sfnRawPath(raw json.RawMessage, defaultPath string) (string, bool, error) {
	if len(raw) == 0 {
		return defaultPath, false, nil
	}
	if string(raw) == "null" {
		return "", true, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, fmt.Errorf("path must be a string or null: %w", err)
	}
	return value, false, nil
}

func sfnApplyInputPath(input, context any, raw json.RawMessage) (any, error) {
	expr, discard, err := sfnRawPath(raw, "$")
	if err != nil {
		return nil, err
	}
	if discard {
		return map[string]any{}, nil
	}
	value, ok := sfnPathValue(input, context, expr)
	if !ok {
		return nil, fmt.Errorf("States.Runtime: InputPath %q could not be applied", expr)
	}
	return value, nil
}

func sfnApplyOutputPath(output, context any, raw json.RawMessage) (any, error) {
	expr, discard, err := sfnRawPath(raw, "$")
	if err != nil {
		return nil, err
	}
	if discard {
		return map[string]any{}, nil
	}
	value, ok := sfnPathValue(output, context, expr)
	if !ok {
		return nil, fmt.Errorf("States.Runtime: OutputPath %q could not be applied", expr)
	}
	return value, nil
}

func sfnSetResultPath(input, result any, raw json.RawMessage) (any, error) {
	expr, discard, err := sfnRawPath(raw, "$")
	if err != nil {
		return nil, err
	}
	if discard {
		return input, nil
	}
	if expr == "$" {
		return result, nil
	}
	tokens, err := sfnParseReferencePath(expr)
	if err != nil || len(tokens) == 0 {
		return nil, fmt.Errorf("States.ReferencePathConflict: invalid ResultPath %q", expr)
	}
	cloned, err := sfnCloneJSON(input)
	if err != nil {
		return nil, err
	}
	current := cloned
	for i, token := range tokens {
		last := i == len(tokens)-1
		if token.index != nil {
			values, ok := current.([]any)
			if !ok || *token.index >= len(values) {
				return nil, fmt.Errorf("States.ReferencePathConflict: ResultPath %q does not identify an existing array element", expr)
			}
			if last {
				values[*token.index] = result
				return cloned, nil
			}
			current = values[*token.index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("States.ReferencePathConflict: ResultPath %q cannot be applied", expr)
		}
		if last {
			object[token.key] = result
			return cloned, nil
		}
		next, exists := object[token.key]
		if !exists {
			next = map[string]any{}
			object[token.key] = next
		}
		current = next
	}
	return cloned, nil
}

func sfnCloneJSON(value any) (any, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return sfnDecodeJSON(string(b))
}

func sfnResolvePayload(value any, input, context any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, member := range typed {
			if strings.HasSuffix(key, ".$") {
				resolved, err := sfnEvaluatePayloadExpression(member, input, context)
				if err != nil {
					return nil, err
				}
				out[strings.TrimSuffix(key, ".$")] = resolved
				continue
			}
			resolved, err := sfnResolvePayload(member, input, context)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, member := range typed {
			resolved, err := sfnResolvePayload(member, input, context)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}

func sfnEvaluatePayloadExpression(value, input, context any) (any, error) {
	expr, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("States.Runtime: '.$' payload values must be strings")
	}
	if strings.HasPrefix(expr, "States.") {
		return sfnEvaluateIntrinsic(expr, input, context)
	}
	resolved, ok := sfnPathValue(input, context, expr)
	if !ok {
		return nil, fmt.Errorf("States.Runtime: path %q could not be found", expr)
	}
	return resolved, nil
}

func sfnEvaluateIntrinsic(expr string, input, context any) (any, error) {
	open := strings.IndexByte(expr, '(')
	if open < 0 || !strings.HasSuffix(expr, ")") {
		return nil, fmt.Errorf("States.IntrinsicFailure: malformed intrinsic %q", expr)
	}
	name := expr[:open]
	rawArgs := strings.TrimSpace(expr[open+1 : len(expr)-1])
	parts, err := sfnSplitIntrinsicArgs(rawArgs)
	if err != nil {
		return nil, err
	}
	args := make([]any, len(parts))
	for i, part := range parts {
		args[i], err = sfnIntrinsicArgument(part, input, context)
		if err != nil {
			return nil, err
		}
	}
	argCount := func(want ...int) error {
		for _, count := range want {
			if len(args) == count {
				return nil
			}
		}
		return fmt.Errorf("States.IntrinsicFailure: %s received %d arguments", name, len(args))
	}
	switch name {
	case "States.Array":
		return args, nil
	case "States.ArrayPartition":
		if err := argCount(2); err != nil {
			return nil, err
		}
		values, ok := args[0].([]any)
		size, sizeOK := sfnInteger(args[1])
		if !ok || !sizeOK || size <= 0 {
			return nil, fmt.Errorf("States.IntrinsicFailure: ArrayPartition requires an array and positive chunk size")
		}
		var result []any
		for start := 0; start < len(values); start += size {
			end := start + size
			if end > len(values) {
				end = len(values)
			}
			result = append(result, append([]any(nil), values[start:end]...))
		}
		return result, nil
	case "States.ArrayContains":
		if err := argCount(2); err != nil {
			return nil, err
		}
		values, ok := args[0].([]any)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: ArrayContains requires an array")
		}
		needle, _ := json.Marshal(args[1])
		for _, value := range values {
			candidate, _ := json.Marshal(value)
			if string(candidate) == string(needle) {
				return true, nil
			}
		}
		return false, nil
	case "States.ArrayRange":
		if err := argCount(3); err != nil {
			return nil, err
		}
		start, okStart := sfnInteger(args[0])
		end, okEnd := sfnInteger(args[1])
		step, okStep := sfnInteger(args[2])
		if !okStart || !okEnd || !okStep || step == 0 {
			return nil, fmt.Errorf("States.IntrinsicFailure: ArrayRange requires three integers and a non-zero step")
		}
		var result []any
		for value := start; (step > 0 && value <= end) || (step < 0 && value >= end); value += step {
			if len(result) == 1000 {
				return nil, fmt.Errorf("States.IntrinsicFailure: ArrayRange cannot produce more than 1000 items")
			}
			result = append(result, value)
		}
		return result, nil
	case "States.ArrayGetItem":
		if err := argCount(2); err != nil {
			return nil, err
		}
		values, ok := args[0].([]any)
		index, indexOK := sfnInteger(args[1])
		if !ok || !indexOK || index < 0 || index >= len(values) {
			return nil, fmt.Errorf("States.IntrinsicFailure: ArrayGetItem index is out of range")
		}
		return values[index], nil
	case "States.ArrayLength":
		if err := argCount(1); err != nil {
			return nil, err
		}
		values, ok := args[0].([]any)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: ArrayLength requires an array")
		}
		return len(values), nil
	case "States.ArrayUnique":
		if err := argCount(1); err != nil {
			return nil, err
		}
		values, ok := args[0].([]any)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: ArrayUnique requires an array")
		}
		seen := map[string]bool{}
		result := make([]any, 0, len(values))
		for _, value := range values {
			key, _ := json.Marshal(value)
			if !seen[string(key)] {
				seen[string(key)] = true
				result = append(result, value)
			}
		}
		return result, nil
	case "States.Base64Encode":
		if err := argCount(1); err != nil {
			return nil, err
		}
		value, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: Base64Encode requires a string")
		}
		return base64.StdEncoding.EncodeToString([]byte(value)), nil
	case "States.Base64Decode":
		if err := argCount(1); err != nil {
			return nil, err
		}
		value, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: Base64Decode requires a string")
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("States.IntrinsicFailure: %w", err)
		}
		return string(decoded), nil
	case "States.Hash":
		if err := argCount(2); err != nil {
			return nil, err
		}
		value, okValue := args[0].(string)
		algorithm, okAlgorithm := args[1].(string)
		if !okValue || !okAlgorithm {
			return nil, fmt.Errorf("States.IntrinsicFailure: Hash requires string arguments")
		}
		switch algorithm {
		case "MD5":
			sum := md5.Sum([]byte(value))
			return hex.EncodeToString(sum[:]), nil
		case "SHA-1":
			sum := sha1.Sum([]byte(value))
			return hex.EncodeToString(sum[:]), nil
		case "SHA-256":
			sum := sha256.Sum256([]byte(value))
			return hex.EncodeToString(sum[:]), nil
		case "SHA-384":
			sum := sha512.Sum384([]byte(value))
			return hex.EncodeToString(sum[:]), nil
		case "SHA-512":
			sum := sha512.Sum512([]byte(value))
			return hex.EncodeToString(sum[:]), nil
		default:
			return nil, fmt.Errorf("States.IntrinsicFailure: unsupported hash algorithm %q", algorithm)
		}
	case "States.JsonMerge":
		if err := argCount(3); err != nil {
			return nil, err
		}
		left, leftOK := args[0].(map[string]any)
		right, rightOK := args[1].(map[string]any)
		deep, deepOK := args[2].(bool)
		if !leftOK || !rightOK || !deepOK || deep {
			return nil, fmt.Errorf("States.IntrinsicFailure: JsonMerge requires two objects and false")
		}
		result := make(map[string]any, len(left)+len(right))
		for key, value := range left {
			result[key] = value
		}
		for key, value := range right {
			result[key] = value
		}
		return result, nil
	case "States.StringToJson":
		if err := argCount(1); err != nil {
			return nil, err
		}
		value, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: StringToJson requires a string")
		}
		return sfnDecodeJSON(value)
	case "States.JsonToString":
		if err := argCount(1); err != nil {
			return nil, err
		}
		b, err := json.Marshal(args[0])
		return string(b), err
	case "States.MathAdd":
		if err := argCount(2); err != nil {
			return nil, err
		}
		left, leftOK := sfnInteger(args[0])
		right, rightOK := sfnInteger(args[1])
		if !leftOK || !rightOK {
			return nil, fmt.Errorf("States.IntrinsicFailure: MathAdd requires integer arguments")
		}
		result := int64(left) + int64(right)
		if result < math.MinInt32 || result > math.MaxInt32 {
			return nil, fmt.Errorf("States.IntrinsicFailure: MathAdd result is outside the 32-bit integer range")
		}
		return int(result), nil
	case "States.MathRandom":
		if err := argCount(2, 3); err != nil {
			return nil, err
		}
		start, startOK := sfnInteger(args[0])
		end, endOK := sfnInteger(args[1])
		if !startOK || !endOK || start >= end {
			return nil, fmt.Errorf("States.IntrinsicFailure: MathRandom requires a valid integer range")
		}
		source := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
		if len(args) == 3 {
			seed, ok := sfnInteger(args[2])
			if !ok {
				return nil, fmt.Errorf("States.IntrinsicFailure: MathRandom seed must be an integer")
			}
			source = mathrand.New(mathrand.NewSource(int64(seed)))
		}
		return start + source.Intn(end-start), nil
	case "States.StringSplit":
		if err := argCount(2); err != nil {
			return nil, err
		}
		value, valueOK := args[0].(string)
		separators, separatorsOK := args[1].(string)
		if !valueOK || !separatorsOK {
			return nil, fmt.Errorf("States.IntrinsicFailure: StringSplit requires string arguments")
		}
		return strings.FieldsFunc(value, func(r rune) bool { return strings.ContainsRune(separators, r) }), nil
	case "States.UUID":
		if err := argCount(0); err != nil {
			return nil, err
		}
		return uuid.NewString(), nil
	case "States.Format":
		if len(args) == 0 {
			return nil, fmt.Errorf("States.IntrinsicFailure: Format requires a template")
		}
		format, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: Format template must be a string")
		}
		for _, value := range args[1:] {
			replacement := fmt.Sprint(value)
			if value == nil {
				replacement = "null"
			} else if _, ok := value.(map[string]any); ok {
				b, _ := json.Marshal(value)
				replacement = string(b)
			} else if _, ok := value.([]any); ok {
				b, _ := json.Marshal(value)
				replacement = string(b)
			}
			if !strings.Contains(format, "{}") {
				return nil, fmt.Errorf("States.IntrinsicFailure: Format has fewer placeholders than values")
			}
			format = strings.Replace(format, "{}", replacement, 1)
		}
		return format, nil
	default:
		return nil, fmt.Errorf("States.IntrinsicFailure: unsupported intrinsic %s", name)
	}
}

func sfnSplitIntrinsicArgs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var (
		parts     []string
		start     int
		depth     int
		quote     byte
		backslash bool
	)
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if quote != 0 {
			if backslash {
				backslash = false
				continue
			}
			if ch == '\\' {
				backslash = true
			} else if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("States.IntrinsicFailure: unbalanced parentheses")
			}
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(raw[start:i]))
				start = i + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("States.IntrinsicFailure: unterminated intrinsic argument")
	}
	parts = append(parts, strings.TrimSpace(raw[start:]))
	return parts, nil
}

func sfnIntrinsicArgument(raw string, input, context any) (any, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "States.") {
		return sfnEvaluateIntrinsic(raw, input, context)
	}
	if strings.HasPrefix(raw, "$") {
		value, ok := sfnPathValue(input, context, raw)
		if !ok {
			return nil, fmt.Errorf("States.IntrinsicFailure: path %q could not be found", raw)
		}
		return value, nil
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		value := strings.ReplaceAll(raw[1:len(raw)-1], `\'`, `'`)
		value = strings.ReplaceAll(value, `\\`, `\`)
		return value, nil
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("States.IntrinsicFailure: invalid argument %q", raw)
	}
	return value, nil
}

func sfnInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(math.Round(typed)), true
	case json.Number:
		number, err := typed.Float64()
		return int(math.Round(number)), err == nil
	default:
		return 0, false
	}
}

func sfnStringMatches(value, pattern string) bool {
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}
