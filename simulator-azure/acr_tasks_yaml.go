package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// An ACR Tasks task file, as the ACR Tasks YAML reference documents it and as
// the service's open-source run engine (Azure/acr-builder) reads it: Go
// templates over the run's variables and values, then aliases, then the steps.

const (
	acrTaskDefaultVersion     = "v1.0.0"
	acrTaskDefaultStepTimeout = 600
	acrTaskWorkspace          = "/workspace"
	acrTaskImmediate          = "-"
	acrTaskAliasVersion       = "v1.1.0"
	acrTaskDefaultNetwork     = "acb_default_network"
)

// acrTaskImageAliases are the image aliases a cmd step names without a
// directive, from the ACR Tasks YAML reference.
var acrTaskImageAliases = map[string]string{
	"acr":  "mcr.microsoft.com/acr/acr-cli:0.14",
	"az":   "mcr.microsoft.com/acr/azure-cli:9fb281c",
	"bash": "mcr.microsoft.com/acr/bash:9fb281c",
	"curl": "mcr.microsoft.com/acr/curl:9fb281c",
	"cssc": "mcr.microsoft.com/acr/cssc:9fb281c",
}

type acrSetValue struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

// acrRunVariables are the `Run` template variables, named as the run engine
// names them.
type acrRunVariables struct {
	ID           string
	SharedVolume string
	Registry     string
	RegistryName string
	Date         time.Time
	OS           string
	Architecture string
	TaskName     string
}

func (v acrRunVariables) template() map[string]any {
	return map[string]any{
		"ID":           v.ID,
		"Commit":       "",
		"Repository":   "",
		"Branch":       "",
		"GitTag":       "",
		"TriggeredBy":  "",
		"Registry":     v.Registry,
		"RegistryName": v.RegistryName,
		"Date":         v.Date.UTC().Format("20060102-150405z"),
		"SharedVolume": v.SharedVolume,
		"OS":           v.OS,
		"OSVersion":    "",
		"Architecture": v.Architecture,
		"TaskName":     v.TaskName,
	}
}

// aliases are the predefined variable aliases a `$` directive expands.
func (v acrRunVariables) aliases() map[string]string {
	t := v.template()
	out := map[string]string{}
	for _, name := range []string{"ID", "SharedVolume", "Registry", "RegistryName", "Date", "OS", "Architecture", "Commit", "Branch"} {
		out[name], _ = t[name].(string)
	}
	return out
}

type acrTaskFile struct {
	Version          string         `yaml:"version"`
	StepTimeout      int            `yaml:"stepTimeout"`
	WorkingDirectory string         `yaml:"workingDirectory"`
	Env              []string       `yaml:"env"`
	Secrets          []yaml.Node    `yaml:"secrets"`
	Networks         []yaml.Node    `yaml:"networks"`
	Volumes          []yaml.Node    `yaml:"volumes"`
	Alias            *acrTaskAlias  `yaml:"alias"`
	Steps            []*acrTaskStep `yaml:"steps"`
	byID             map[string]*acrTaskStep
}

type acrTaskAlias struct {
	Src       []string          `yaml:"src"`
	Values    map[string]string `yaml:"values"`
	Directive string            `yaml:"directive"`
}

type acrTaskStep struct {
	ID                              string        `yaml:"id"`
	Cmd                             string        `yaml:"cmd"`
	Build                           string        `yaml:"build"`
	Push                            acrStringList `yaml:"push"`
	WorkingDirectory                string        `yaml:"workingDirectory"`
	EntryPoint                      string        `yaml:"entryPoint"`
	User                            string        `yaml:"user"`
	Network                         *yaml.Node    `yaml:"network"`
	Isolation                       string        `yaml:"isolation"`
	CPUs                            string        `yaml:"cpus"`
	Cache                           string        `yaml:"cache"`
	VolumeMounts                    []yaml.Node   `yaml:"volumeMounts"`
	Env                             []string      `yaml:"env"`
	Expose                          []string      `yaml:"expose"`
	Ports                           []string      `yaml:"ports"`
	When                            []string      `yaml:"when"`
	ExitedWith                      []int         `yaml:"exitedWith"`
	ExitedWithout                   []int         `yaml:"exitedWithout"`
	Timeout                         int           `yaml:"timeout"`
	CmdDownloadRetries              int           `yaml:"cmdDownloadRetries"`
	CmdDownloadRetryDelay           int           `yaml:"cmdDownloadRetryDelay"`
	StartDelay                      int           `yaml:"startDelay"`
	RetryDelay                      int           `yaml:"retryDelay"`
	Retries                         int           `yaml:"retries"`
	RetryOnErrors                   []string      `yaml:"retryOnErrors"`
	Repeat                          int           `yaml:"repeat"`
	Keep                            bool          `yaml:"keep"`
	Detach                          bool          `yaml:"detach"`
	Privileged                      bool          `yaml:"privileged"`
	IgnoreErrors                    bool          `yaml:"ignoreErrors"`
	DisableWorkingDirectoryOverride bool          `yaml:"disableWorkingDirectoryOverride"`
	Pull                            bool          `yaml:"pull"`

	deps []string
}

// acrStringList reads a push step written inline as one image or as a list.
type acrStringList []string

func (l *acrStringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = acrStringList{n.Value}
		return nil
	}
	var items []string
	if err := n.Decode(&items); err != nil {
		return err
	}
	*l = items
	return nil
}

func (s *acrTaskStep) kind() string {
	switch {
	case s.Cmd != "":
		return "cmd"
	case s.Build != "":
		return "build"
	default:
		return "push"
	}
}

// acrRenderTemplate renders a task or values file the way the run engine's
// default mode does: a missing value renders empty.
func acrRenderTemplate(name, text string, data map[string]any) (string, error) {
	t, err := template.New(name).Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return strings.ReplaceAll(buf.String(), "<no value>", ""), nil
}

// acrTaskValues merges the values file under the values a request sets.
func acrTaskValues(valuesFile []byte, set []acrSetValue) (map[string]any, error) {
	values := map[string]any{}
	if len(bytes.TrimSpace(valuesFile)) > 0 {
		if err := yaml.Unmarshal(valuesFile, &values); err != nil {
			return nil, fmt.Errorf("parse the values file: %w", err)
		}
		if values == nil {
			values = map[string]any{}
		}
	}
	for _, v := range set {
		values[v.Name] = v.Value
	}
	return values, nil
}

func acrVersionAtLeast(version, floor string) bool {
	parse := func(v string) []int {
		var out []int
		for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".") {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parse(version), parse(floor)
	if a == nil {
		return false
	}
	for i := 0; i < len(b); i++ {
		x := 0
		if i < len(a) {
			x = a[i]
		}
		if x != b[i] {
			return x > b[i]
		}
	}
	return true
}

// acrExpandAliases replaces each directive-prefixed alias the table knows and
// leaves every other directive alone, so a shell's own `$HOME` survives. A
// doubled directive is a literal one.
func acrExpandAliases(text string, directive rune, aliases map[string]string) string {
	var out strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != directive {
			out.WriteRune(runes[i])
			continue
		}
		if i+1 < len(runes) && runes[i+1] == directive {
			out.WriteRune(directive)
			i++
			continue
		}
		j := i + 1
		for j < len(runes) && (runes[j] == '_' || unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j])) {
			j++
		}
		if value, ok := aliases[string(runes[i+1:j])]; ok && j > i+1 {
			out.WriteString(value)
			i = j - 1
			continue
		}
		out.WriteRune(directive)
	}
	return out.String()
}

// acrLoadTaskFile renders, expands and validates a task file.
func acrLoadTaskFile(content, valuesFile []byte, set []acrSetValue, run acrRunVariables) (*acrTaskFile, error) {
	values, err := acrTaskValues(valuesFile, set)
	if err != nil {
		return nil, err
	}
	rendered, err := acrRenderTemplate("task", string(content), map[string]any{
		"Run":    run.template(),
		"Values": values,
	})
	if err != nil {
		return nil, err
	}

	var head struct {
		Version string        `yaml:"version"`
		Alias   *acrTaskAlias `yaml:"alias"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &head); err != nil {
		return nil, fmt.Errorf("parse the task file: %w", err)
	}
	aliasing := acrVersionAtLeast(head.Version, acrTaskAliasVersion)
	if aliasing {
		directive := '$'
		aliases := run.aliases()
		if head.Alias != nil {
			if len(head.Alias.Src) > 0 {
				return nil, errors.New("alias src files are not supported: define the aliases under alias.values")
			}
			if d := []rune(head.Alias.Directive); len(d) == 1 {
				directive = d[0]
			} else if len(d) > 1 {
				return nil, fmt.Errorf("alias directive %q must be a single character", head.Alias.Directive)
			}
			for name, value := range head.Alias.Values {
				aliases[name] = value
			}
		}
		rendered = acrExpandAliases(rendered, directive, aliases)
	} else if head.Alias != nil {
		return nil, fmt.Errorf("aliases need task file version %s or later", acrTaskAliasVersion)
	}

	var task acrTaskFile
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	dec.KnownFields(true)
	if err := dec.Decode(&task); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse the task file: %w", err)
	}
	if aliasing {
		for _, s := range task.Steps {
			if s.Cmd == "" {
				continue
			}
			image, rest, _ := strings.Cut(strings.TrimLeft(s.Cmd, " "), " ")
			if full, ok := acrTaskImageAliases[image]; ok {
				s.Cmd = strings.TrimSpace(full + " " + rest)
			}
		}
	}
	if err := task.initialize(); err != nil {
		return nil, err
	}
	return &task, nil
}

func (t *acrTaskFile) initialize() error {
	if t.Version == "" {
		t.Version = acrTaskDefaultVersion
	}
	if t.StepTimeout <= 0 {
		t.StepTimeout = acrTaskDefaultStepTimeout
	}
	if len(t.Secrets) > 0 {
		return errors.New("task secrets (Azure Key Vault references) are not supported")
	}
	if len(t.Networks) > 0 {
		return errors.New("task networks are not supported: steps run on the default network")
	}
	if len(t.Volumes) > 0 {
		return errors.New("task volumes are not supported")
	}
	if len(t.Steps) == 0 {
		return errors.New("the task file defines no steps")
	}
	t.byID = map[string]*acrTaskStep{}
	for i, s := range t.Steps {
		if s == nil {
			return fmt.Errorf("step %d is empty", i)
		}
		if s.ID == "" {
			s.ID = fmt.Sprintf("acb_step_%d", i)
		}
		if strings.ContainsFunc(s.ID, unicode.IsSpace) {
			return fmt.Errorf("step ID %q must not contain spaces", s.ID)
		}
		if _, dup := t.byID[s.ID]; dup {
			return fmt.Errorf("step ID %q is defined more than once", s.ID)
		}
		t.byID[s.ID] = s
		kinds := 0
		for _, set := range []bool{s.Cmd != "", s.Build != "", len(s.Push) > 0} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			return fmt.Errorf("step %s must define exactly one of cmd, build or push", s.ID)
		}
		if s.Timeout <= 0 {
			s.Timeout = t.StepTimeout
		}
		if s.WorkingDirectory == "" {
			s.WorkingDirectory = t.WorkingDirectory
		}
		s.Env = acrMergeEnv(t.Env, s.Env)
		if s.Retries < 0 || s.Repeat < 0 || s.RetryDelay < 0 || s.StartDelay < 0 {
			return fmt.Errorf("step %s: retries, repeat, retryDelay and startDelay must not be negative", s.ID)
		}
		switch s.Cache {
		case "", "enabled", "disabled":
		default:
			return fmt.Errorf("step %s: cache must be enabled or disabled, not %q", s.ID, s.Cache)
		}
		if err := s.checkSupported(); err != nil {
			return err
		}
	}
	for i, s := range t.Steps {
		switch {
		case len(s.When) == 1 && s.When[0] == acrTaskImmediate:
		case len(s.When) > 0:
			for _, dep := range s.When {
				if dep == acrTaskImmediate {
					return fmt.Errorf("step %s: %q cannot be combined with other dependencies", s.ID, acrTaskImmediate)
				}
				if _, ok := t.byID[dep]; !ok {
					return fmt.Errorf("step %s depends on step %q, which the task does not define", s.ID, dep)
				}
			}
			s.deps = s.When
		case i > 0:
			s.deps = []string{t.Steps[i-1].ID}
		}
	}
	return t.checkAcyclic()
}

// checkAcyclic refuses a task whose `when` dependencies form a cycle, which
// no order of execution satisfies.
func (t *acrTaskFile) checkAcyclic() error {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("step %s depends on itself through its when dependencies", id)
		case done:
			return nil
		}
		state[id] = visiting
		for _, dep := range t.byID[id].deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	for _, s := range t.Steps {
		if err := visit(s.ID); err != nil {
			return err
		}
	}
	return nil
}

// checkSupported refuses the step properties this run engine does not carry
// out, rather than running the step without them.
func (s *acrTaskStep) checkSupported() error {
	unsupported := []struct {
		name string
		set  bool
	}{
		{"network", s.Network != nil},
		{"volumeMounts", len(s.VolumeMounts) > 0},
		{"exitedWith", len(s.ExitedWith) > 0},
		{"exitedWithout", len(s.ExitedWithout) > 0},
		{"retryOnErrors", len(s.RetryOnErrors) > 0},
		{"cmdDownloadRetries", s.CmdDownloadRetries != 0},
		{"cmdDownloadRetryDelay", s.CmdDownloadRetryDelay != 0},
	}
	if s.kind() != "cmd" {
		unsupported = append(unsupported, []struct {
			name string
			set  bool
		}{
			{"detach", s.Detach}, {"keep", s.Keep}, {"entryPoint", s.EntryPoint != ""},
			{"user", s.User != ""}, {"privileged", s.Privileged}, {"ports", len(s.Ports) > 0},
			{"expose", len(s.Expose) > 0}, {"isolation", s.Isolation != ""}, {"cpus", s.CPUs != ""},
		}...)
	}
	if s.kind() == "push" {
		unsupported = append(unsupported, []struct {
			name string
			set  bool
		}{
			{"workingDirectory", s.WorkingDirectory != ""}, {"retries", s.Retries != 0}, {"repeat", s.Repeat != 0},
			{"pull", s.Pull}, {"cache", s.Cache != ""},
		}...)
	}
	for _, u := range unsupported {
		if u.set {
			return fmt.Errorf("step %s: the %s property is not supported on a %s step", s.ID, u.name, s.kind())
		}
	}
	return nil
}

// acrMergeEnv overlays a step's environment on the task's, keyed by name.
func acrMergeEnv(task, step []string) []string {
	if len(task) == 0 {
		return step
	}
	set := map[string]bool{}
	for _, e := range step {
		name, _, _ := strings.Cut(e, "=")
		set[name] = true
	}
	out := append([]string(nil), step...)
	for _, e := range task {
		name, _, _ := strings.Cut(e, "=")
		if !set[name] {
			out = append(out, e)
		}
	}
	return out
}

// acrSplitArgs splits a step's command line into words the way a POSIX shell
// does for quotes and backslashes, without expanding anything.
func acrSplitArgs(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(runes) && strings.ContainsRune(`"\$`+"`", runes[i+1]):
				i++
				cur.WriteRune(runes[i])
			default:
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == '\\' && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			inWord = true
		case unicode.IsSpace(c):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in %q", line)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
