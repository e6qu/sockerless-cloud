package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The run-request union Registries_ScheduleRun accepts, discriminated by
// `type`: a docker build, a task file in the source, an inline task file, or
// a run of an existing Task resource.

type acrRunRequestBase struct {
	Type             string `json:"type"`
	IsArchiveEnabled bool   `json:"isArchiveEnabled"`
	AgentPoolName    string `json:"agentPoolName"`
	LogTemplate      string `json:"logTemplate"`
}

// acrDockerBuildRequest is a DockerBuildRequest.
type acrDockerBuildRequest struct {
	acrRunRequestBase
	DockerFilePath string        `json:"dockerFilePath"`
	ImageNames     []string      `json:"imageNames"`
	SourceLocation string        `json:"sourceLocation"`
	IsPushEnabled  *bool         `json:"isPushEnabled"`
	NoCache        bool          `json:"noCache"`
	Arguments      []acrArgument `json:"arguments"`
	Target         string        `json:"target"`
	Platform       *acrPlatform  `json:"platform"`
	Timeout        *int32        `json:"timeout"`
}

// acrTaskFileRunRequest is a FileTaskRunRequest or an EncodedTaskRunRequest.
type acrTaskFileRunRequest struct {
	acrRunRequestBase
	TaskFilePath         string        `json:"taskFilePath"`
	ValuesFilePath       string        `json:"valuesFilePath"`
	EncodedTaskContent   string        `json:"encodedTaskContent"`
	EncodedValuesContent string        `json:"encodedValuesContent"`
	Values               []acrSetValue `json:"values"`
	SourceLocation       string        `json:"sourceLocation"`
	Platform             *acrPlatform  `json:"platform"`
	Timeout              *int32        `json:"timeout"`
}

type acrTaskRunRequest struct {
	acrRunRequestBase
	TaskID                     string                         `json:"taskId"`
	OverrideTaskStepProperties *acrOverrideTaskStepProperties `json:"overrideTaskStepProperties"`
}

type acrOverrideTaskStepProperties struct {
	ContextPath        string        `json:"contextPath"`
	File               string        `json:"file"`
	Arguments          []acrArgument `json:"arguments"`
	Target             string        `json:"target"`
	Values             []acrSetValue `json:"values"`
	UpdateTriggerToken string        `json:"updateTriggerToken"`
}

// acrTaskProperties is the part of a Task resource a run of it reads.
type acrTaskProperties struct {
	Status        string           `json:"status"`
	Platform      *acrPlatform     `json:"platform"`
	AgentPoolName string           `json:"agentPoolName"`
	Timeout       *int32           `json:"timeout"`
	Step          *acrTaskStepSpec `json:"step"`
}

// acrTaskStepSpec is a Task's step: a DockerBuildStep, FileTaskStep or
// EncodedTaskStep, discriminated by `type`.
type acrTaskStepSpec struct {
	Type                 string        `json:"type"`
	ContextPath          string        `json:"contextPath"`
	DockerFilePath       string        `json:"dockerFilePath"`
	ImageNames           []string      `json:"imageNames"`
	IsPushEnabled        *bool         `json:"isPushEnabled"`
	NoCache              bool          `json:"noCache"`
	Arguments            []acrArgument `json:"arguments"`
	Target               string        `json:"target"`
	TaskFilePath         string        `json:"taskFilePath"`
	ValuesFilePath       string        `json:"valuesFilePath"`
	EncodedTaskContent   string        `json:"encodedTaskContent"`
	EncodedValuesContent string        `json:"encodedValuesContent"`
	Values               []acrSetValue `json:"values"`
}

// acrRunSpec is what a run executes, whichever request scheduled it.
type acrRunSpec struct {
	runType          string
	taskName         string
	agentPool        string
	isArchiveEnabled bool
	platform         *acrPlatform
	timeout          time.Duration
	source           string

	// A docker build run.
	docker *acrDockerBuildSpec

	// A task-file run: the file in the source, or its content inline.
	taskFile      string
	valuesFile    string
	encodedTask   []byte
	encodedValues []byte
	values        []acrSetValue
}

type acrDockerBuildSpec struct {
	DockerFilePath string
	ImageNames     []string
	IsPushEnabled  bool
	NoCache        bool
	Arguments      []acrArgument
	Target         string
}

// acrRequestError is a run request the service refuses before it queues a run.
type acrRequestError struct {
	status  int
	code    string
	message string
}

func (e *acrRequestError) Error() string { return e.message }

func acrBadRequest(format string, args ...any) *acrRequestError {
	return &acrRequestError{status: http.StatusBadRequest, code: "InvalidRequestContent", message: fmt.Sprintf(format, args...)}
}

func acrRunTimeout(timeout *int32) (time.Duration, *acrRequestError) {
	if timeout == nil {
		return acrRunDefaultTimeoutSeconds * time.Second, nil
	}
	if *timeout < acrRunMinTimeoutSeconds || *timeout > acrRunMaxTimeoutSeconds {
		return 0, acrBadRequest("The run timeout %d is out of range: it must be between %d and %d seconds.",
			*timeout, acrRunMinTimeoutSeconds, acrRunMaxTimeoutSeconds)
	}
	return time.Duration(*timeout) * time.Second, nil
}

func acrDecodeBase64(field, value string) ([]byte, *acrRequestError) {
	if value == "" {
		return nil, nil
	}
	out, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, acrBadRequest("%s is not valid base64: %v", field, err)
	}
	return out, nil
}

// acrDecodeRunRequest reads a Registries_ScheduleRun body into the run it
// schedules.
func acrDecodeRunRequest(body []byte, reg Registry) (acrRunSpec, *acrRequestError) {
	var base acrRunRequestBase
	if err := json.Unmarshal(body, &base); err != nil {
		return acrRunSpec{}, acrBadRequest("failed to parse run request: %v", err)
	}
	switch base.Type {
	case "DockerBuildRequest":
		var req acrDockerBuildRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return acrRunSpec{}, acrBadRequest("failed to parse run request: %v", err)
		}
		timeout, rerr := acrRunTimeout(req.Timeout)
		if rerr != nil {
			return acrRunSpec{}, rerr
		}
		return acrRunSpec{
			runType: "QuickBuild", agentPool: req.AgentPoolName, isArchiveEnabled: req.IsArchiveEnabled,
			platform: req.Platform, timeout: timeout, source: req.SourceLocation,
			docker: &acrDockerBuildSpec{
				DockerFilePath: req.DockerFilePath, ImageNames: req.ImageNames,
				IsPushEnabled: req.IsPushEnabled == nil || *req.IsPushEnabled,
				NoCache:       req.NoCache, Arguments: req.Arguments, Target: req.Target,
			},
		}, nil

	case "FileTaskRunRequest", "EncodedTaskRunRequest":
		var req acrTaskFileRunRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return acrRunSpec{}, acrBadRequest("failed to parse run request: %v", err)
		}
		timeout, rerr := acrRunTimeout(req.Timeout)
		if rerr != nil {
			return acrRunSpec{}, rerr
		}
		spec := acrRunSpec{
			runType: "QuickRun", agentPool: req.AgentPoolName, isArchiveEnabled: req.IsArchiveEnabled,
			platform: req.Platform, timeout: timeout, source: req.SourceLocation, values: req.Values,
		}
		if base.Type == "FileTaskRunRequest" {
			if req.TaskFilePath == "" {
				return acrRunSpec{}, acrBadRequest("The FileTaskRunRequest requires taskFilePath.")
			}
			if req.SourceLocation == "" {
				return acrRunSpec{}, acrBadRequest("The FileTaskRunRequest requires sourceLocation, which holds taskFilePath.")
			}
			spec.taskFile, spec.valuesFile = req.TaskFilePath, req.ValuesFilePath
			return spec, nil
		}
		if req.EncodedTaskContent == "" {
			return acrRunSpec{}, acrBadRequest("The EncodedTaskRunRequest requires encodedTaskContent.")
		}
		if spec.encodedTask, rerr = acrDecodeBase64("encodedTaskContent", req.EncodedTaskContent); rerr != nil {
			return acrRunSpec{}, rerr
		}
		if spec.encodedValues, rerr = acrDecodeBase64("encodedValuesContent", req.EncodedValuesContent); rerr != nil {
			return acrRunSpec{}, rerr
		}
		return spec, nil

	case "TaskRunRequest":
		var req acrTaskRunRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return acrRunSpec{}, acrBadRequest("failed to parse run request: %v", err)
		}
		return acrTaskRunSpec(req, reg)

	case "":
		return acrRunSpec{}, acrBadRequest("The run request requires a type.")
	}
	return acrRunSpec{}, acrBadRequest("The run request type %q is not one of DockerBuildRequest, FileTaskRunRequest, EncodedTaskRunRequest or TaskRunRequest.", base.Type)
}

// acrTaskRunSpec resolves a TaskRunRequest against the Task it names, with the
// request's step overrides applied.
func acrTaskRunSpec(req acrTaskRunRequest, reg Registry) (acrRunSpec, *acrRequestError) {
	if req.TaskID == "" {
		return acrRunSpec{}, acrBadRequest("The TaskRunRequest requires taskId.")
	}
	prefix := strings.ToLower(reg.ID + "/tasks/")
	if !strings.HasPrefix(strings.ToLower(req.TaskID), prefix) {
		return acrRunSpec{}, acrBadRequest("The task %q does not belong to registry %q.", req.TaskID, reg.Name)
	}
	taskName := req.TaskID[len(prefix):]
	stored, ok := acrTasks.Get(reg.ID + "/tasks/" + taskName)
	if !ok {
		return acrRunSpec{}, &acrRequestError{status: http.StatusNotFound, code: "ResourceNotFound",
			message: fmt.Sprintf("The Resource 'Microsoft.ContainerRegistry/registries/%s/tasks/%s' was not found.", reg.Name, taskName)}
	}
	raw, err := json.Marshal(stored.Properties)
	if err != nil {
		return acrRunSpec{}, acrBadRequest("read task %s: %v", taskName, err)
	}
	var props acrTaskProperties
	if err := json.Unmarshal(raw, &props); err != nil {
		return acrRunSpec{}, acrBadRequest("read task %s: %v", taskName, err)
	}
	if strings.EqualFold(props.Status, "Disabled") {
		return acrRunSpec{}, acrBadRequest("The task %s is disabled.", taskName)
	}
	if props.Step == nil {
		return acrRunSpec{}, acrBadRequest("The task %s has no step.", taskName)
	}
	timeout, rerr := acrRunTimeout(props.Timeout)
	if rerr != nil {
		return acrRunSpec{}, rerr
	}
	step := *props.Step
	o := req.OverrideTaskStepProperties
	if o == nil {
		o = &acrOverrideTaskStepProperties{}
	}
	if o.ContextPath != "" {
		step.ContextPath = o.ContextPath
	}
	spec := acrRunSpec{
		taskName: stored.Name, agentPool: props.AgentPoolName, isArchiveEnabled: req.IsArchiveEnabled,
		platform: props.Platform, timeout: timeout, source: step.ContextPath,
	}
	if req.AgentPoolName != "" {
		spec.agentPool = req.AgentPoolName
	}
	switch step.Type {
	case "Docker":
		if o.File != "" {
			step.DockerFilePath = o.File
		}
		if o.Target != "" {
			step.Target = o.Target
		}
		spec.runType = "QuickBuild"
		spec.docker = &acrDockerBuildSpec{
			DockerFilePath: step.DockerFilePath, ImageNames: step.ImageNames,
			IsPushEnabled: step.IsPushEnabled == nil || *step.IsPushEnabled,
			NoCache:       step.NoCache, Arguments: acrMergeArguments(step.Arguments, o.Arguments), Target: step.Target,
		}
	case "FileTask":
		if o.File != "" {
			step.TaskFilePath = o.File
		}
		spec.runType = "QuickRun"
		spec.taskFile, spec.valuesFile = step.TaskFilePath, step.ValuesFilePath
		spec.values = acrMergeValues(step.Values, o.Values)
		if spec.source == "" {
			return acrRunSpec{}, acrBadRequest("The task %s runs a task file but has no context path.", taskName)
		}
	case "EncodedTask":
		spec.runType = "QuickRun"
		if spec.encodedTask, rerr = acrDecodeBase64("encodedTaskContent", step.EncodedTaskContent); rerr != nil {
			return acrRunSpec{}, rerr
		}
		if spec.encodedValues, rerr = acrDecodeBase64("encodedValuesContent", step.EncodedValuesContent); rerr != nil {
			return acrRunSpec{}, rerr
		}
		spec.values = acrMergeValues(step.Values, o.Values)
	default:
		return acrRunSpec{}, acrBadRequest("The task %s has a step of unknown type %q.", taskName, step.Type)
	}
	return spec, nil
}

// acrMergeArguments overlays override arguments on a step's, by name.
func acrMergeArguments(base, override []acrArgument) []acrArgument {
	out := append([]acrArgument(nil), base...)
	for _, o := range override {
		replaced := false
		for i := range out {
			if out[i].Name == o.Name {
				out[i] = o
				replaced = true
			}
		}
		if !replaced {
			out = append(out, o)
		}
	}
	return out
}

func acrMergeValues(base, override []acrSetValue) []acrSetValue {
	out := append([]acrSetValue(nil), base...)
	for _, o := range override {
		replaced := false
		for i := range out {
			if out[i].Name == o.Name {
				out[i] = o
				replaced = true
			}
		}
		if !replaced {
			out = append(out, o)
		}
	}
	return out
}
