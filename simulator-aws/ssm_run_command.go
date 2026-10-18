package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// ssmRunShellScriptDocument is the content of the AWS-RunShellScript managed
// document, as AWS publishes it.
const ssmRunShellScriptDocument = `{"schemaVersion":"2.2","description":"Run a shell script or specify the commands to run.","parameters":{"commands":{"type":"StringList","description":"(Required) Specify a shell script or a command to run.","minItems":1,"displayType":"textarea"},"workingDirectory":{"type":"String","default":"","description":"(Optional) The path to the working directory on your instance.","maxChars":4096},"executionTimeout":{"type":"String","default":"3600","description":"(Optional) The time in seconds for a command to complete before it is considered to have failed. Default is 3600 (1 hour). Maximum is 172800 (48 hours).","allowedPattern":"([1-9][0-9]{0,4})|(1[0-6][0-9]{4})|(17[0-1][0-9]{3})|(172[0-7][0-9]{2})|(172800)"}},"mainSteps":[{"action":"aws:runShellScript","name":"runShellScript","inputs":{"timeoutSeconds":"{{ executionTimeout }}","runCommand":"{{ commands }}","workingDirectory":"{{ workingDirectory }}"}}]}`

// ssmManagedDocuments are the AWS-owned documents Run Command accepts beside
// the account's own.
var ssmManagedDocuments = map[string]string{
	"AWS-RunShellScript": ssmRunShellScriptDocument,
}

// ssmCommandDocumentContent is the content a command runs: the account's
// document at its default version, or a managed one.
func ssmCommandDocumentContent(name string) (content, format string, ok bool) {
	if doc, ok := ssmDocuments.Get(name); ok {
		for _, version := range doc.Versions {
			if version.DocumentVersion == doc.DefaultVersion || len(doc.Versions) == 1 {
				return version.Content, version.DocumentFormat, true
			}
		}
		if len(doc.Versions) > 0 {
			latest := doc.Versions[len(doc.Versions)-1]
			return latest.Content, latest.DocumentFormat, true
		}
		return "", "", false
	}
	if content, ok := ssmManagedDocuments[name]; ok {
		return content, "JSON", true
	}
	return "", "", false
}

// ssmShellStep is one aws:runShellScript step of a command document, with
// its parameters substituted.
type ssmShellStep struct {
	Commands         []string
	WorkingDirectory string
	Timeout          time.Duration
}

// ssmShellSteps reads the aws:runShellScript steps of a document's content.
func ssmShellSteps(content, format string, params map[string][]string) ([]ssmShellStep, error) {
	var doc struct {
		Parameters map[string]struct {
			Default any `json:"default" yaml:"default"`
		} `json:"parameters" yaml:"parameters"`
		MainSteps []struct {
			Action string         `json:"action" yaml:"action"`
			Inputs map[string]any `json:"inputs" yaml:"inputs"`
		} `json:"mainSteps" yaml:"mainSteps"`
	}
	var err error
	if strings.EqualFold(format, "YAML") {
		err = yaml.Unmarshal([]byte(content), &doc)
	} else {
		err = json.Unmarshal([]byte(content), &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("document content: %w", err)
	}
	values := map[string][]string{}
	for name, p := range doc.Parameters {
		switch v := p.Default.(type) {
		case string:
			values[name] = []string{v}
		case []any:
			for _, item := range v {
				values[name] = append(values[name], fmt.Sprint(item))
			}
		}
	}
	for name, v := range params {
		values[name] = v
	}
	var steps []ssmShellStep
	for _, step := range doc.MainSteps {
		if step.Action != "aws:runShellScript" {
			continue
		}
		commands, err := ssmStringList(step.Inputs["runCommand"], values)
		if err != nil {
			return nil, fmt.Errorf("step runCommand: %w", err)
		}
		workingDirectory := ssmSubstitute(fmt.Sprint(ssmInput(step.Inputs, "workingDirectory")), values)
		timeout := time.Hour
		if raw := ssmSubstitute(fmt.Sprint(ssmInput(step.Inputs, "timeoutSeconds")), values); raw != "" && raw != "<nil>" {
			seconds, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || seconds <= 0 {
				return nil, fmt.Errorf("step timeoutSeconds %q is not a positive number of seconds", raw)
			}
			timeout = time.Duration(seconds) * time.Second
		}
		steps = append(steps, ssmShellStep{Commands: commands, WorkingDirectory: workingDirectory, Timeout: timeout})
	}
	return steps, nil
}

func ssmInput(inputs map[string]any, name string) any {
	if v, ok := inputs[name]; ok {
		return v
	}
	return ""
}

// ssmStringList reads a StringList input: a list, or a parameter reference
// standing for the whole list.
func ssmStringList(raw any, values map[string][]string) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, fmt.Errorf("missing")
	case string:
		if name, ok := ssmParameterReference(v); ok {
			return values[name], nil
		}
		return []string{ssmSubstitute(v, values)}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, ssmSubstitute(fmt.Sprint(item), values))
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, ssmSubstitute(item, values))
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected shape %T", raw)
}

// ssmParameterReference reads "{{ name }}" as name.
func ssmParameterReference(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return "", false
	}
	name := strings.TrimSpace(s[2 : len(s)-2])
	if name == "" || strings.ContainsAny(name, " {}") {
		return "", false
	}
	return name, true
}

// ssmSubstitute replaces every {{ name }} in s with the parameter's values
// joined by newlines, as the agent renders a StringList into a string.
func ssmSubstitute(s string, values map[string][]string) string {
	for name, v := range values {
		for _, form := range []string{"{{ " + name + " }}", "{{" + name + "}}"} {
			s = strings.ReplaceAll(s, form, strings.Join(v, "\n"))
		}
	}
	return s
}

// ssmOutputLimit is how much of an invocation's output GetCommandInvocation
// carries; the rest goes to the command's Amazon S3 or CloudWatch Logs
// destinations.
const ssmOutputLimit = 24000

const ssmOutputTruncated = "\n---Output truncated---"

func ssmTruncateOutput(s string) string {
	if len(s) <= ssmOutputLimit {
		return s
	}
	return s[:ssmOutputLimit-len(ssmOutputTruncated)] + ssmOutputTruncated
}

// ssmRunCommandOnInstances runs the command's shell steps on every target in
// the background and records each invocation's result as the agent reports
// it.
func ssmRunCommandOnInstances(commandID string, steps []ssmShellStep) {
	cmd, ok := ssmCommands.Get(commandID)
	if !ok {
		return
	}
	for _, invocation := range cmd.Invocations {
		instanceID := invocation.InstanceId
		bg.Go(func() { ssmRunInvocation(commandID, instanceID, steps) })
	}
}

func ssmRunInvocation(commandID, instanceID string, steps []ssmShellStep) {
	start := time.Now().UTC()
	ssmUpdateInvocation(commandID, instanceID, func(inv *SSMCommandInvocation) {
		inv.Status, inv.StatusDetail = "InProgress", "InProgress"
		inv.ExecutionStart = start.Format(time.RFC3339)
	})
	var stdout, stderr strings.Builder
	status, detail, code := "Success", "Success", 0
	for _, step := range steps {
		handle, ok := ec2WorkloadHandle(instanceID)
		if !ok {
			status, detail, code = "Failed", "Failed", -1
			stderr.WriteString("the instance is not running\n")
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), step.Timeout)
		script := strings.Join(step.Commands, "\n")
		if step.WorkingDirectory != "" {
			script = "cd " + shellQuoteSSM(step.WorkingDirectory) + " || exit 1\n" + script
		}
		result, err := handle.Exec(ctx, []string{"/bin/sh", "-c", script})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				status, detail, code = "TimedOut", "ExecutionTimedOut", -1
			} else {
				status, detail, code = "Failed", "Failed", -1
				stderr.WriteString(err.Error() + "\n")
			}
			break
		}
		stdout.WriteString(result.Stdout)
		stderr.WriteString(result.Stderr)
		if result.ExitCode != 0 {
			status, detail, code = "Failed", "Failed", result.ExitCode
			break
		}
	}
	end := time.Now().UTC()
	ssmUpdateInvocation(commandID, instanceID, func(inv *SSMCommandInvocation) {
		inv.Status, inv.StatusDetail = status, detail
		inv.ResponseCode = code
		inv.StandardOutput = ssmTruncateOutput(stdout.String())
		inv.StandardError = ssmTruncateOutput(stderr.String())
		inv.ExecutionEnd = end.Format(time.RFC3339)
		inv.ExecutionElapsed = "PT" + strconv.FormatFloat(end.Sub(start).Seconds(), 'f', 3, 64) + "S"
	})
}

func shellQuoteSSM(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ssmUpdateInvocation edits one invocation and folds the invocations' states
// into the command's: a command is in progress until every invocation
// finished, then Success when all succeeded, else Failed.
func ssmUpdateInvocation(commandID, instanceID string, edit func(*SSMCommandInvocation)) {
	ssmCommands.Update(commandID, func(cmd *SSMCommand) {
		for i := range cmd.Invocations {
			if cmd.Invocations[i].InstanceId == instanceID {
				edit(&cmd.Invocations[i])
			}
		}
		if cmd.Status == "Cancelled" {
			return
		}
		pending, failed, timedOut := 0, 0, 0
		for _, inv := range cmd.Invocations {
			switch inv.Status {
			case "Pending", "InProgress":
				pending++
			case "Failed":
				failed++
			case "TimedOut":
				timedOut++
			}
		}
		switch {
		case pending > 0:
			cmd.Status, cmd.StatusDetail = "InProgress", "InProgress"
		case timedOut > 0:
			cmd.Status, cmd.StatusDetail = "TimedOut", "ExecutionTimedOut"
		case failed > 0:
			cmd.Status, cmd.StatusDetail = "Failed", "Failed"
		default:
			cmd.Status, cmd.StatusDetail = "Success", "Success"
		}
	})
}

// ssmCommandCounts are the finished and failed invocations of a command.
func ssmCommandCounts(cmd SSMCommand) (completed, errored int) {
	for _, inv := range cmd.Invocations {
		switch inv.Status {
		case "Pending", "InProgress":
		case "Success":
			completed++
		default:
			completed++
			errored++
		}
	}
	return completed, errored
}
