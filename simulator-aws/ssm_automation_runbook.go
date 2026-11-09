package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// An Automation runbook: its parameters, assume role, mainSteps and outputs
// (ssm-documents-syntax-data-elements-parameters.md.gz), each step with the
// properties every action shares (ssm-automation-actions.md.gz).

type ssmRunbookParameter struct {
	Type           string
	Default        any
	HasDefault     bool
	AllowedValues  []string
	AllowedPattern string
}

type ssmStepOutput struct {
	Name     string
	Selector string
	Type     string
}

type ssmRunbookStep struct {
	Name           string
	Action         string
	Inputs         map[string]any
	Outputs        []ssmStepOutput
	MaxAttempts    int
	TimeoutSeconds int64
	OnFailure      string
	OnCancel       string
	NextStep       string
	IsEnd          bool
	IsCritical     *bool
}

type ssmRunbook struct {
	AssumeRole string
	Parameters map[string]ssmRunbookParameter
	MainSteps  []ssmRunbookStep
	Outputs    []string
}

func (rb ssmRunbook) step(name string) *ssmRunbookStep {
	for i := range rb.MainSteps {
		if rb.MainSteps[i].Name == name {
			return &rb.MainSteps[i]
		}
	}
	return nil
}

// following is the step after name in mainSteps, or "" after the last.
func (rb ssmRunbook) following(name string) string {
	for i := range rb.MainSteps {
		if rb.MainSteps[i].Name == name && i+1 < len(rb.MainSteps) {
			return rb.MainSteps[i+1].Name
		}
	}
	return ""
}

// critical applies isCritical's default: false for a step that continues on
// failure, true otherwise.
func (s ssmRunbookStep) critical() bool {
	if s.IsCritical != nil {
		return *s.IsCritical
	}
	return s.OnFailure != "Continue"
}

func (s ssmRunbookStep) branchTargets() []string {
	var out []string
	choices, _ := s.Inputs["Choices"].([]any)
	for _, c := range choices {
		if m, ok := c.(map[string]any); ok {
			if next, ok := m["NextStep"].(string); ok {
				out = append(out, next)
			}
		}
	}
	if d, ok := s.Inputs["Default"].(string); ok && d != "" {
		out = append(out, d)
	}
	return out
}

func ssmDecodeDocument(content, format string) (map[string]any, error) {
	var doc map[string]any
	var err error
	if strings.EqualFold(format, "YAML") {
		err = yaml.Unmarshal([]byte(content), &doc)
	} else {
		err = json.Unmarshal([]byte(content), &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("the runbook content can't be read: %w", err)
	}
	return doc, nil
}

func ssmAnyInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == float64(int64(n))
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i, err == nil
	}
	return 0, false
}

func ssmAnyBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		return parsed, err == nil
	}
	return false, false
}

func ssmParseRunbook(content, format string) (ssmRunbook, error) {
	doc, err := ssmDecodeDocument(content, format)
	if err != nil {
		return ssmRunbook{}, err
	}
	rb := ssmRunbook{Parameters: map[string]ssmRunbookParameter{}}
	if role, ok := doc["assumeRole"].(string); ok {
		rb.AssumeRole = role
	}
	params, _ := doc["parameters"].(map[string]any)
	for name, raw := range params {
		spec, _ := raw.(map[string]any)
		p := ssmRunbookParameter{Type: "String"}
		if t, ok := spec["type"].(string); ok {
			p.Type = t
		}
		p.Default, p.HasDefault = spec["default"]
		if allowed, ok := spec["allowedValues"].([]any); ok {
			for _, v := range allowed {
				p.AllowedValues = append(p.AllowedValues, ssmValueString(v))
			}
		}
		if pattern, ok := spec["allowedPattern"].(string); ok {
			p.AllowedPattern = pattern
		}
		rb.Parameters[name] = p
	}
	steps, _ := doc["mainSteps"].([]any)
	seen := map[string]bool{}
	for i, raw := range steps {
		m, ok := raw.(map[string]any)
		if !ok {
			return ssmRunbook{}, fmt.Errorf("mainSteps[%d] isn't a step", i)
		}
		step := ssmRunbookStep{Inputs: map[string]any{}}
		step.Name, _ = m["name"].(string)
		step.Action, _ = m["action"].(string)
		if step.Name == "" || step.Action == "" {
			return ssmRunbook{}, fmt.Errorf("mainSteps[%d] needs a name and an action", i)
		}
		if seen[step.Name] {
			return ssmRunbook{}, fmt.Errorf("the step name %s isn't unique", step.Name)
		}
		seen[step.Name] = true
		if inputs, ok := m["inputs"].(map[string]any); ok {
			step.Inputs = inputs
		}
		if n, ok := ssmAnyInt(m["maxAttempts"]); ok {
			step.MaxAttempts = int(n)
		}
		if n, ok := ssmAnyInt(m["timeoutSeconds"]); ok {
			step.TimeoutSeconds = n
		}
		step.OnFailure, _ = m["onFailure"].(string)
		step.OnCancel, _ = m["onCancel"].(string)
		step.NextStep, _ = m["nextStep"].(string)
		step.IsEnd, _ = ssmAnyBool(m["isEnd"])
		if critical, ok := ssmAnyBool(m["isCritical"]); ok {
			step.IsCritical = &critical
		}
		outputs, _ := m["outputs"].([]any)
		for _, o := range outputs {
			om, _ := o.(map[string]any)
			out := ssmStepOutput{Type: "String"}
			out.Name, _ = om["Name"].(string)
			out.Selector, _ = om["Selector"].(string)
			if t, ok := om["Type"].(string); ok && t != "" {
				out.Type = t
			}
			step.Outputs = append(step.Outputs, out)
		}
		rb.MainSteps = append(rb.MainSteps, step)
	}
	outputs, _ := doc["outputs"].([]any)
	for _, o := range outputs {
		if s, ok := o.(string); ok {
			rb.Outputs = append(rb.Outputs, s)
		}
	}
	return rb, nil
}

var ssmSSMParameterReference = regexp.MustCompile(`^\{\{\s*ssm:([^{}\s]+)\s*\}\}$`)

// ssmResolveRunbookParameters types each parameter the runbook declares from
// the request or its default, and refuses a value the runbook does not
// declare, lacks or does not allow.
func ssmResolveRunbookParameters(ctx context.Context, rb ssmRunbook, provided map[string][]string, identity ssmAutomationIdentity) (map[string]any, string) {
	for name := range provided {
		if _, declared := rb.Parameters[name]; !declared {
			return nil, fmt.Sprintf("Parameter %q isn't declared in the runbook.", name)
		}
	}
	values := map[string]any{}
	for _, name := range ssmSortedKeys(rb.Parameters) {
		p := rb.Parameters[name]
		var (
			value any
			err   error
		)
		if raw, given := provided[name]; given {
			value, err = ssmTypedFromStrings(p.Type, raw)
		} else if p.HasDefault {
			def := p.Default
			if s, ok := def.(string); ok {
				if m := ssmSSMParameterReference.FindStringSubmatch(s); m != nil {
					resolved, failure := ssmReadParameterStore(ctx, identity, m[1])
					if failure != "" {
						return nil, fmt.Sprintf("Parameter %q: %s", name, failure)
					}
					def = resolved
				}
			}
			value, err = ssmTypedFromDefault(p.Type, def)
		} else {
			return nil, fmt.Sprintf("Parameter %q is required but no value was provided.", name)
		}
		if err != nil {
			return nil, fmt.Sprintf("Parameter %q: %v", name, err)
		}
		if failure := ssmCheckAllowed(name, p, value); failure != "" {
			return nil, failure
		}
		values[name] = value
	}
	return values, ""
}

func ssmCheckAllowed(name string, p ssmRunbookParameter, value any) string {
	strs := ssmValueStrings(value)
	if len(p.AllowedValues) > 0 {
		for _, s := range strs {
			allowed := false
			for _, a := range p.AllowedValues {
				allowed = allowed || a == s
			}
			if !allowed {
				return fmt.Sprintf("Parameter %q value %q isn't one of its allowed values.", name, s)
			}
		}
	}
	if p.AllowedPattern != "" {
		re, err := regexp.Compile(p.AllowedPattern)
		if err != nil {
			return fmt.Sprintf("Parameter %q has an allowedPattern that isn't a regular expression.", name)
		}
		for _, s := range strs {
			if !re.MatchString(s) {
				return fmt.Sprintf("Parameter %q value %q doesn't match its allowedPattern %s.", name, s, p.AllowedPattern)
			}
		}
	}
	return ""
}

func ssmListType(t string) bool {
	return t == "StringList" || strings.HasPrefix(t, "List<")
}

// ssmTypedFromStrings types a parameter value the API carries as strings: a
// StringMap and each MapList item arrive as JSON.
func ssmTypedFromStrings(t string, raw []string) (any, error) {
	switch {
	case ssmListType(t):
		return append([]string{}, raw...), nil
	case t == "MapList":
		out := make([]any, 0, len(raw))
		for _, s := range raw {
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err != nil {
				return nil, fmt.Errorf("a MapList item must be a JSON object")
			}
			out = append(out, m)
		}
		return out, nil
	}
	if len(raw) != 1 {
		return nil, fmt.Errorf("a %s parameter takes one value", t)
	}
	return ssmConvertScalar(t, raw[0])
}

func ssmTypedFromDefault(t string, def any) (any, error) {
	switch {
	case ssmListType(t):
		switch v := def.(type) {
		case []any:
			return ssmValueStrings(v), nil
		case string:
			if v == "" {
				return []string{}, nil
			}
			return []string{v}, nil
		}
		return nil, fmt.Errorf("the default isn't a StringList")
	case t == "MapList":
		if v, ok := def.([]any); ok {
			return v, nil
		}
		return nil, fmt.Errorf("the default isn't a MapList")
	case t == "StringMap":
		if v, ok := def.(map[string]any); ok {
			return v, nil
		}
		if s, ok := def.(string); ok {
			return ssmConvertScalar(t, s)
		}
		return nil, fmt.Errorf("the default isn't a StringMap")
	}
	if s, ok := def.(string); ok {
		return ssmConvertScalar(t, s)
	}
	return ssmConvertValue(t, def)
}

func ssmConvertScalar(t, s string) (any, error) {
	switch t {
	case "Integer":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q isn't an Integer", s)
		}
		return n, nil
	case "Boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%q isn't a Boolean", s)
		}
		return b, nil
	case "StringMap":
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, fmt.Errorf("a StringMap value must be a JSON object")
		}
		return m, nil
	}
	return s, nil
}

// ssmConvertValue converts a selected or defaulted value to an output type,
// as the outputs reference describes (ssm-automation-action-outputs-inputs.md.gz):
// a String to StringList, Integer or Boolean; an Integer or Boolean to String
// or StringList; a one-element list to its element's type.
func ssmConvertValue(t string, v any) (any, error) {
	if list, ok := v.([]any); ok && len(list) == 1 && !ssmListType(t) && t != "MapList" {
		v = list[0]
	}
	if list, ok := v.([]string); ok && len(list) == 1 && !ssmListType(t) && t != "MapList" {
		v = list[0]
	}
	switch {
	case ssmListType(t):
		switch list := v.(type) {
		case []any:
			return ssmValueStrings(list), nil
		case []string:
			return list, nil
		}
		return []string{ssmValueString(v)}, nil
	case t == "MapList":
		if list, ok := v.([]any); ok {
			return list, nil
		}
		return nil, fmt.Errorf("the value isn't a MapList")
	case t == "StringMap":
		if m, ok := v.(map[string]any); ok {
			return m, nil
		}
		return nil, fmt.Errorf("the value isn't a StringMap")
	case t == "Integer":
		switch n := v.(type) {
		case json.Number:
			return n.Int64()
		case string:
			return ssmConvertScalar(t, n)
		}
		if n, ok := ssmAnyInt(v); ok {
			return n, nil
		}
		return nil, fmt.Errorf("the value isn't an Integer")
	case t == "Boolean":
		if b, ok := ssmAnyBool(v); ok {
			return b, nil
		}
		return nil, fmt.Errorf("the value isn't a Boolean")
	}
	return ssmValueString(v), nil
}

var ssmReferencePattern = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

// ssmInterpolator resolves {{ }} references in step inputs: runbook
// parameters, earlier steps' outputs, and the system and automation
// variables (ssm-automation-variables.md.gz).
type ssmInterpolator struct {
	exec SSMAutomationExecution
}

func (ip ssmInterpolator) lookup(name string) (any, error) {
	now := time.Now().UTC()
	switch name {
	case "global:ACCOUNT_ID":
		return awsAccountID(), nil
	case "global:REGION":
		return awsRegion(), nil
	case "global:AWS_PARTITION":
		return "aws", nil
	case "global:DATE":
		return now.Format("2006-01-02"), nil
	case "global:DATE_TIME":
		return now.Format("2006-01-02_15.04.05"), nil
	case "automation:EXECUTION_ID":
		return ip.exec.AutomationExecutionId, nil
	}
	if stepName, output, ok := strings.Cut(name, "."); ok {
		for i := len(ip.exec.Steps) - 1; i >= 0; i-- {
			s := ip.exec.Steps[i]
			if s.StepName != stepName {
				continue
			}
			if v, ok := s.Values[output]; ok {
				return v, nil
			}
		}
		return nil, fmt.Errorf("the output %s isn't available", name)
	}
	if v, ok := ip.exec.Values[name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("the variable %s isn't a parameter of the runbook", name)
}

func (ip ssmInterpolator) resolve(v any) (any, error) {
	switch value := v.(type) {
	case string:
		if m := ssmReferencePattern.FindStringSubmatch(value); m != nil && strings.TrimSpace(value) == m[0] {
			return ip.lookup(m[1])
		}
		var failure error
		out := ssmReferencePattern.ReplaceAllStringFunc(value, func(ref string) string {
			name := ssmReferencePattern.FindStringSubmatch(ref)[1]
			resolved, err := ip.lookup(name)
			if err != nil {
				failure = err
				return ref
			}
			if list, ok := resolved.([]string); ok {
				return strings.Join(list, ",")
			}
			return ssmValueString(resolved)
		})
		return out, failure
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			resolved, err := ip.resolve(item)
			if err != nil {
				return nil, err
			}
			// A list item that is a whole list reference stands for that list's items.
			if s, ok := item.(string); ok && ssmIsWholeReference(s) {
				switch list := resolved.(type) {
				case []string:
					for _, x := range list {
						out = append(out, x)
					}
					continue
				case []any:
					out = append(out, list...)
					continue
				}
			}
			out = append(out, resolved)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			resolved, err := ip.resolve(item)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	}
	return v, nil
}

func ssmIsWholeReference(s string) bool {
	m := ssmReferencePattern.FindStringSubmatch(s)
	return m != nil && strings.TrimSpace(s) == m[0]
}

// ssmISODuration reads an ISO 8601 duration of weeks, days, hours, minutes and
// seconds, the units aws:sleep's maximum of seven days leaves meaningful.
var ssmISODurationPattern = regexp.MustCompile(`^P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

func ssmISODuration(s string) (time.Duration, error) {
	m := ssmISODurationPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || s == "P" || strings.HasSuffix(s, "T") {
		return 0, fmt.Errorf("%q isn't an ISO 8601 duration", s)
	}
	var d time.Duration
	units := []time.Duration{7 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute}
	for i, unit := range units {
		if m[i+1] != "" {
			n, _ := strconv.Atoi(m[i+1])
			d += time.Duration(n) * unit
		}
	}
	if m[5] != "" {
		secs, _ := strconv.ParseFloat(m[5], 64)
		d += time.Duration(secs * float64(time.Second))
	}
	return d, nil
}
