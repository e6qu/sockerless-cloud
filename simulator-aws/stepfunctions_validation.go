package main

import (
	"encoding/json"
	"fmt"
)

func sfnValidateState(definition sfnDefinition, state sfnState, location, inheritedQueryLanguage string) error {
	validTypes := map[string]bool{
		"Pass": true, "Task": true, "Choice": true, "Wait": true,
		"Succeed": true, "Fail": true, "Parallel": true, "Map": true,
	}
	if !validTypes[state.Type] {
		return fmt.Errorf("%s.Type %q is not a supported Amazon States Language state type", location, state.Type)
	}
	queryLanguage := state.QueryLanguage
	if queryLanguage == "" {
		queryLanguage = inheritedQueryLanguage
	}
	if queryLanguage != "JSONPath" && queryLanguage != "JSONata" {
		return fmt.Errorf("%s.QueryLanguage must be JSONPath or JSONata", location)
	}
	if inheritedQueryLanguage == "JSONata" && state.QueryLanguage == "JSONPath" {
		return fmt.Errorf("%s cannot change QueryLanguage from JSONata back to JSONPath", location)
	}
	if queryLanguage == "JSONata" {
		if len(state.InputPath) > 0 || state.Parameters != nil || state.ResultSelector != nil ||
			len(state.ResultPath) > 0 || len(state.OutputPath) > 0 {
			return fmt.Errorf("%s uses JSONPath-only data-flow fields in a JSONata state", location)
		}
		for field, value := range map[string]any{
			"Arguments": state.Arguments,
			"Assign":    state.Assign,
		} {
			if err := sfnValidateJSONataExpressions(value); err != nil {
				return fmt.Errorf("%s.%s: %w", location, field, err)
			}
		}
		if len(state.Output) > 0 {
			var output any
			if err := json.Unmarshal(state.Output, &output); err != nil {
				return fmt.Errorf("%s.Output: %w", location, err)
			}
			if err := sfnValidateJSONataExpressions(output); err != nil {
				return fmt.Errorf("%s.Output: %w", location, err)
			}
		}
	} else if state.Arguments != nil || state.Assign != nil || len(state.Output) > 0 {
		return fmt.Errorf("%s uses JSONata-only data-flow fields in a JSONPath state", location)
	}

	terminalState := state.Type == "Succeed" || state.Type == "Fail"
	choiceState := state.Type == "Choice"
	if terminalState {
		if state.Next != "" || state.End {
			return fmt.Errorf("%s must not declare Next or End", location)
		}
	} else if choiceState {
		if state.Next != "" || state.End {
			return fmt.Errorf("%s Choice state must not declare Next or End", location)
		}
	} else if (state.Next == "") == !state.End {
		return fmt.Errorf("%s must declare exactly one of Next or End", location)
	}
	if state.Next != "" {
		if _, ok := definition.States[state.Next]; !ok {
			return fmt.Errorf("%s.Next state %q does not exist", location, state.Next)
		}
	}
	for index, catcher := range state.Catch {
		if len(catcher.ErrorEquals) == 0 || catcher.Next == "" {
			return fmt.Errorf("%s.Catch[%d] requires ErrorEquals and Next", location, index)
		}
		if _, ok := definition.States[catcher.Next]; !ok {
			return fmt.Errorf("%s.Catch[%d].Next state %q does not exist", location, index, catcher.Next)
		}
	}
	for index, retrier := range state.Retry {
		if len(retrier.ErrorEquals) == 0 {
			return fmt.Errorf("%s.Retry[%d].ErrorEquals is required", location, index)
		}
		if retrier.MaxAttempts != nil && *retrier.MaxAttempts < 0 {
			return fmt.Errorf("%s.Retry[%d].MaxAttempts must be non-negative", location, index)
		}
		if retrier.BackoffRate < 0 || retrier.IntervalSeconds < 0 || retrier.MaxDelaySeconds < 0 {
			return fmt.Errorf("%s.Retry[%d] contains a negative retry control", location, index)
		}
	}

	switch state.Type {
	case "Pass":
		if state.Resource != "" || state.ResultSelector != nil || len(state.Retry) > 0 || len(state.Catch) > 0 {
			return fmt.Errorf("%s contains a field not supported by Pass states", location)
		}
	case "Task":
		if state.Resource == "" {
			return fmt.Errorf("%s.Resource is required", location)
		}
	case "Choice":
		if len(state.Choices) == 0 {
			return fmt.Errorf("%s.Choices is required", location)
		}
		if state.Default != "" {
			if _, ok := definition.States[state.Default]; !ok {
				return fmt.Errorf("%s.Default state %q does not exist", location, state.Default)
			}
		}
		for index, choice := range state.Choices {
			if choice.Next == "" {
				return fmt.Errorf("%s.Choices[%d].Next is required", location, index)
			}
			if _, ok := definition.States[choice.Next]; !ok {
				return fmt.Errorf("%s.Choices[%d].Next state %q does not exist", location, index, choice.Next)
			}
			if queryLanguage == "JSONata" {
				if len(choice.Condition) == 0 {
					return fmt.Errorf("%s.Choices[%d].Condition is required for JSONata", location, index)
				}
				var condition any
				if err := json.Unmarshal(choice.Condition, &condition); err != nil {
					return fmt.Errorf("%s.Choices[%d].Condition: %w", location, index, err)
				}
				if err := sfnValidateJSONataExpressions(condition); err != nil {
					return fmt.Errorf("%s.Choices[%d].Condition: %w", location, index, err)
				}
			} else if choice.Variable == "" && len(choice.And) == 0 && len(choice.Or) == 0 && choice.Not == nil {
				return fmt.Errorf("%s.Choices[%d] requires Variable or a logical operator", location, index)
			}
		}
	case "Wait":
		waitFields := 0
		if state.Seconds != nil {
			waitFields++
		}
		if state.SecondsPath != "" {
			waitFields++
		}
		if state.Timestamp != "" {
			waitFields++
		}
		if state.TimestampPath != "" {
			waitFields++
		}
		if waitFields != 1 {
			return fmt.Errorf("%s requires exactly one Wait time field", location)
		}
	case "Parallel":
		if len(state.Branches) == 0 {
			return fmt.Errorf("%s.Branches is required", location)
		}
		for index, branch := range state.Branches {
			if err := sfnValidateDefinitionObject(branch, fmt.Sprintf("%s.Branches[%d]", location, index), queryLanguage); err != nil {
				return err
			}
		}
	case "Map":
		if state.Iterator != nil && state.ItemProcessor != nil {
			return fmt.Errorf("%s cannot declare both Iterator and ItemProcessor", location)
		}
		processor := state.ItemProcessor
		if processor == nil {
			processor = state.Iterator
		}
		if processor == nil {
			return fmt.Errorf("%s requires Iterator or ItemProcessor", location)
		}
		if err := sfnValidateDefinitionObject(*processor, location+".ItemProcessor", queryLanguage); err != nil {
			return err
		}
	}
	return nil
}
