package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"

	"github.com/google/uuid"
	jsonata "github.com/jsonata-go/jsonata/v206"
)

func sfnCloneVariables(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func sfnJSONataExpression(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "{%") || !strings.HasSuffix(trimmed, "%}") {
		return "", false
	}
	return strings.TrimSpace(trimmed[2 : len(trimmed)-2]), true
}

func sfnEvaluateJSONata(expression string, input, result, errorOutput, context any, variables map[string]any) (any, error) {
	compiled, err := jsonata.Compile(expression, false)
	if err != nil {
		return nil, err
	}
	compiled.SetMaxDepth(200)
	compiled.SetMaxTime(10_000)
	compiled.SetMaxRange(10_000)
	for name, value := range variables {
		normalized, err := sfnNormalizeJSONataValue(value)
		if err != nil {
			return nil, err
		}
		compiled.Assign(name, normalized)
	}
	input, err = sfnNormalizeJSONataValue(input)
	if err != nil {
		return nil, err
	}
	result, err = sfnNormalizeJSONataValue(result)
	if err != nil {
		return nil, err
	}
	errorOutput, err = sfnNormalizeJSONataValue(errorOutput)
	if err != nil {
		return nil, err
	}
	context, err = sfnNormalizeJSONataValue(context)
	if err != nil {
		return nil, err
	}
	states := map[string]any{
		"input":   input,
		"context": context,
	}
	if result != nil {
		states["result"] = result
	}
	if errorOutput != nil {
		states["errorOutput"] = errorOutput
	}
	compiled.Assign("states", states)
	if err := sfnRegisterJSONataFunctions(compiled); err != nil {
		return nil, err
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	outputJSON, err := compiled.Evaluate(inputJSON, nil)
	if err != nil {
		return nil, err
	}
	if len(outputJSON) == 0 {
		return nil, fmt.Errorf("JSONata expression returned undefined")
	}
	return sfnDecodeJSON(string(outputJSON))
}

func sfnNormalizeJSONataValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func sfnRegisterJSONataFunctions(expression *jsonata.Expression) error {
	stringArgument := func(args []any, index int) (string, error) {
		if index >= len(args) {
			return "", fmt.Errorf("JSONata function argument %d is missing", index+1)
		}
		value, ok := args[index].(string)
		if !ok {
			return "", fmt.Errorf("JSONata function argument %d must be a string", index+1)
		}
		return value, nil
	}
	functions := []struct {
		name      string
		signature string
		fn        jsonata.JSONataFunc
	}{
		{name: "uuid", signature: "<:s>", fn: func([]any) (any, error) {
			return uuid.NewString(), nil
		}},
		{name: "parse", signature: "<s:x>", fn: func(args []any) (any, error) {
			source, err := stringArgument(args, 0)
			if err != nil {
				return nil, err
			}
			var value any
			if err := json.Unmarshal([]byte(source), &value); err != nil {
				return nil, err
			}
			return value, nil
		}},
		{name: "random", signature: "<n?:n>", fn: func(args []any) (any, error) {
			if len(args) == 0 {
				return rand.Float64(), nil
			}
			seed, ok := args[0].(float64)
			if !ok {
				return nil, fmt.Errorf("JSONata random seed must be a number")
			}
			return rand.New(rand.NewSource(int64(seed))).Float64(), nil
		}},
		{name: "hash", signature: "<ss:s>", fn: func(args []any) (any, error) {
			source, err := stringArgument(args, 0)
			if err != nil {
				return nil, err
			}
			algorithm, err := stringArgument(args, 1)
			if err != nil {
				return nil, err
			}
			value := []byte(source)
			switch algorithm {
			case "MD5":
				sum := md5.Sum(value)
				return hex.EncodeToString(sum[:]), nil
			case "SHA-1":
				sum := sha1.Sum(value)
				return hex.EncodeToString(sum[:]), nil
			case "SHA-256":
				sum := sha256.Sum256(value)
				return hex.EncodeToString(sum[:]), nil
			case "SHA-384":
				sum := sha512.Sum384(value)
				return hex.EncodeToString(sum[:]), nil
			case "SHA-512":
				sum := sha512.Sum512(value)
				return hex.EncodeToString(sum[:]), nil
			default:
				return nil, fmt.Errorf("unsupported hash algorithm %q", algorithm)
			}
		}},
	}
	for _, function := range functions {
		if err := expression.RegisterFunction(function.name, function.fn, function.signature); err != nil {
			return err
		}
	}
	return nil
}

func sfnResolveJSONataValue(value, input, result, errorOutput, context any, variables map[string]any) (any, error) {
	switch typed := value.(type) {
	case string:
		if expression, ok := sfnJSONataExpression(typed); ok {
			return sfnEvaluateJSONata(expression, input, result, errorOutput, context, variables)
		}
		return typed, nil
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		for name, member := range typed {
			value, err := sfnResolveJSONataValue(member, input, result, errorOutput, context, variables)
			if err != nil {
				return nil, err
			}
			resolved[name] = value
		}
		return resolved, nil
	case []any:
		resolved := make([]any, len(typed))
		for index, member := range typed {
			value, err := sfnResolveJSONataValue(member, input, result, errorOutput, context, variables)
			if err != nil {
				return nil, err
			}
			resolved[index] = value
		}
		return resolved, nil
	default:
		return value, nil
	}
}

func sfnResolveJSONataString(value string, input, result, errorOutput, context any, variables map[string]any) (string, error) {
	resolved, err := sfnResolveJSONataValue(value, input, result, errorOutput, context, variables)
	if err != nil {
		return "", err
	}
	text, ok := resolved.(string)
	if !ok {
		return "", fmt.Errorf("JSONata expression must return a string")
	}
	return text, nil
}

func sfnApplyJSONataAssignments(assignments map[string]any, input, result, errorOutput, context any, variables map[string]any) error {
	if len(assignments) == 0 {
		return nil
	}
	next := make(map[string]any, len(assignments))
	for name, expression := range assignments {
		if strings.ContainsAny(name, ".[") {
			return fmt.Errorf("invalid variable name %q", name)
		}
		value, err := sfnResolveJSONataValue(expression, input, result, errorOutput, context, variables)
		if err != nil {
			return err
		}
		next[name] = value
	}
	for name, value := range next {
		variables[name] = value
	}
	return nil
}

func sfnEvalJSONataCondition(raw json.RawMessage, input, context any, variables map[string]any) (bool, error) {
	if len(raw) == 0 {
		return false, fmt.Errorf("JSONata Choice rule requires Condition")
	}
	var condition any
	if err := json.Unmarshal(raw, &condition); err != nil {
		return false, err
	}
	value, err := sfnResolveJSONataValue(condition, input, nil, nil, context, variables)
	if err != nil {
		return false, err
	}
	matched, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("JSONata Choice Condition must return a boolean")
	}
	return matched, nil
}

func sfnValidateJSONataExpressions(value any) error {
	switch typed := value.(type) {
	case string:
		expression, ok := sfnJSONataExpression(typed)
		if !ok {
			return nil
		}
		_, err := jsonata.Compile(expression, false)
		return err
	case map[string]any:
		for _, member := range typed {
			if err := sfnValidateJSONataExpressions(member); err != nil {
				return err
			}
		}
	case []any:
		for _, member := range typed {
			if err := sfnValidateJSONataExpressions(member); err != nil {
				return err
			}
		}
	}
	return nil
}
