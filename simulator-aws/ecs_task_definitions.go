package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleECSRegisterTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Family                  string                   `json:"family"`
		ContainerDefinitions    []ECSContainerDefinition `json:"containerDefinitions"`
		Cpu                     string                   `json:"cpu,omitempty"`
		Memory                  string                   `json:"memory,omitempty"`
		NetworkMode             string                   `json:"networkMode,omitempty"`
		RequiresCompatibilities []string                 `json:"requiresCompatibilities,omitempty"`
		ExecutionRoleArn        string                   `json:"executionRoleArn,omitempty"`
		TaskRoleArn             string                   `json:"taskRoleArn,omitempty"`
		Volumes                 []ECSVolume              `json:"volumes,omitempty"`
		RuntimePlatform         json.RawMessage          `json:"runtimePlatform,omitempty"`
		EphemeralStorage        json.RawMessage          `json:"ephemeralStorage,omitempty"`
		ProxyConfiguration      json.RawMessage          `json:"proxyConfiguration,omitempty"`
		PlacementConstraints    json.RawMessage          `json:"placementConstraints,omitempty"`
		InferenceAccelerators   json.RawMessage          `json:"inferenceAccelerators,omitempty"`
		PidMode                 string                   `json:"pidMode,omitempty"`
		IpcMode                 string                   `json:"ipcMode,omitempty"`
		EnableFaultInjection    *bool                    `json:"enableFaultInjection,omitempty"`
		Tags                    []ECSTag                 `json:"tags,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Family == "" {
		AWSError(w, "InvalidParameterException", "Family is required", http.StatusBadRequest)
		return
	}
	if len(req.ContainerDefinitions) == 0 {
		AWSError(w, "InvalidParameterException", "At least one container definition is required", http.StatusBadRequest)
		return
	}

	// FARGATE requires task-level cpu and memory, else a ClientException.
	if hasFargate(req.RequiresCompatibilities) && (req.Cpu == "" || req.Memory == "") {
		AWSError(w, "ClientException",
			"Task definition does not support launch type FARGATE: task-level memory and cpu are required.",
			http.StatusBadRequest)
		return
	}

	if hasFargate(req.RequiresCompatibilities) && req.Cpu != "" && req.Memory != "" {
		if err := validateFargateResources(req.Cpu, req.Memory); err != nil {
			AWSError(w, "ClientException", err.Error(), http.StatusBadRequest)
			return
		}
	}

	if message := ecsContainerDependenciesInvalid(req.ContainerDefinitions); message != "" {
		AWSError(w, "ClientException", message, http.StatusBadRequest)
		return
	}

	ecsRevisionMu.Lock()
	ecsRevisions[req.Family]++
	revision := ecsRevisions[req.Family]
	ecsRevisionMu.Unlock()

	td := ECSTaskDefinition{
		TaskDefinitionArn:       fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":task-definition/%s:%d", req.Family, revision),
		Family:                  req.Family,
		Revision:                revision,
		ContainerDefinitions:    req.ContainerDefinitions,
		Cpu:                     req.Cpu,
		Memory:                  req.Memory,
		NetworkMode:             req.NetworkMode,
		RequiresCompatibilities: req.RequiresCompatibilities,
		ExecutionRoleArn:        req.ExecutionRoleArn,
		TaskRoleArn:             req.TaskRoleArn,
		Volumes:                 req.Volumes,
		RuntimePlatform:         req.RuntimePlatform,
		EphemeralStorage:        req.EphemeralStorage,
		ProxyConfiguration:      req.ProxyConfiguration,
		PlacementConstraints:    req.PlacementConstraints,
		InferenceAccelerators:   req.InferenceAccelerators,
		PidMode:                 req.PidMode,
		IpcMode:                 req.IpcMode,
		EnableFaultInjection:    req.EnableFaultInjection,
		Compatibilities:         ecsComputeCompatibilities(req.NetworkMode, req.RequiresCompatibilities),
		Tags:                    req.Tags,
		Status:                  "ACTIVE",
	}

	key := fmt.Sprintf("%s:%d", req.Family, revision)
	ecsTaskDefinitions.Put(key, td)

	// RegisterTaskDefinition echoes the tags at the response top level.
	resp := map[string]any{"taskDefinition": td}
	if len(td.Tags) > 0 {
		resp["tags"] = td.Tags
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECSDeregisterTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinition string `json:"taskDefinition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}

	key := req.TaskDefinition
	if strings.HasPrefix(key, "arn:") {
		parts := strings.Split(key, "/")
		if len(parts) > 1 {
			key = parts[len(parts)-1]
		}
	}

	found := ecsTaskDefinitions.Update(key, func(td *ECSTaskDefinition) {
		td.Status = "INACTIVE"
	})

	if !found {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Unable to describe task definition: %s", req.TaskDefinition)
		return
	}

	td, _ := ecsTaskDefinitions.Get(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"taskDefinition": td,
	})
}

func handleECSDescribeTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinition string   `json:"taskDefinition"`
		Include        []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}

	key := req.TaskDefinition
	if strings.HasPrefix(key, "arn:") {
		parts := strings.Split(key, "/")
		if len(parts) > 1 {
			key = parts[len(parts)-1]
		}
	}

	// Without a revision, take the latest active one.
	if !strings.Contains(key, ":") {
		ecsRevisionMu.RLock()
		rev, exists := ecsRevisions[key]
		ecsRevisionMu.RUnlock()
		if exists {
			key = fmt.Sprintf("%s:%d", key, rev)
		}
	}

	td, ok := ecsTaskDefinitions.Get(key)
	if !ok {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Unable to describe task definition: %s", req.TaskDefinition)
		return
	}

	// Tags appear at the top level only with include=TAGS, which
	// terraform-provider-aws reads on refresh.
	resp := map[string]any{"taskDefinition": td}
	if len(td.Tags) > 0 && ecsIncludeHasTags(req.Include) {
		resp["tags"] = td.Tags
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// Every task definition is EC2-compatible; FARGATE also requires awsvpc.
func ecsComputeCompatibilities(networkMode string, requires []string) []string {
	set := map[string]bool{"EC2": true}
	for _, c := range requires {
		set[strings.ToUpper(c)] = true
	}
	if strings.EqualFold(networkMode, "awsvpc") {
		set["FARGATE"] = true
	}
	var out []string
	for _, c := range []string{"EC2", "FARGATE", "EXTERNAL"} {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// As the NetworkMode enum spells them.
const (
	ecsNetworkModeAwsvpc = "awsvpc"
	ecsNetworkModeBridge = "bridge"
	ecsNetworkModeHost   = "host"
	ecsNetworkModeNone   = "none"
)

// An unset networkMode defaults to bridge.
func ecsEffectiveNetworkMode(td ECSTaskDefinition) string {
	mode := strings.ToLower(strings.TrimSpace(td.NetworkMode))
	if mode == "" {
		return ecsNetworkModeBridge
	}
	return mode
}

// A bare family takes its latest revision, as RunTask and StartTask do.
func ecsTaskDefinitionKey(ref string) string {
	key := ref
	if strings.HasPrefix(key, "arn:") {
		key = key[strings.LastIndexByte(key, '/')+1:]
	}
	if !strings.Contains(key, ":") {
		ecsRevisionMu.Lock()
		rev, exists := ecsRevisions[key]
		ecsRevisionMu.Unlock()
		if exists {
			key = fmt.Sprintf("%s:%d", key, rev)
		}
	}
	return key
}

func extractTDKey(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return arn
}

// Valid Fargate CPU/memory combinations: the lower tiers list values, the
// higher ones ranges.
type fargateCombo struct {
	cpu        int
	memOptions []int // nil means use the range.
	memMin     int
	memMax     int
	memInc     int
}

var fargateCombos = []fargateCombo{
	{256, []int{512, 1024, 2048}, 0, 0, 0},
	{512, []int{1024, 2048, 3072, 4096}, 0, 0, 0},
	{1024, nil, 2048, 8192, 1024},
	{2048, nil, 4096, 16384, 1024},
	{4096, nil, 8192, 30720, 1024},
	{8192, nil, 16384, 61440, 4096},
	{16384, nil, 32768, 122880, 8192},
}

func hasFargate(compatibilities []string) bool {
	for _, c := range compatibilities {
		if strings.EqualFold(c, "FARGATE") {
			return true
		}
	}
	return false
}

func validateFargateResources(cpuStr, memStr string) error {
	cpu, err := strconv.Atoi(cpuStr)
	if err != nil {
		return fmt.Errorf("invalid cpu value: %s", cpuStr)
	}
	mem, err := strconv.Atoi(memStr)
	if err != nil {
		return fmt.Errorf("invalid memory value: %s", memStr)
	}

	for _, combo := range fargateCombos {
		if combo.cpu != cpu {
			continue
		}
		if len(combo.memOptions) > 0 {
			for _, opt := range combo.memOptions {
				if opt == mem {
					return nil
				}
			}
			return fmt.Errorf("invalid memory value %d for cpu %d, valid values: %v", mem, cpu, combo.memOptions)
		}
		if mem >= combo.memMin && mem <= combo.memMax && (mem-combo.memMin)%combo.memInc == 0 {
			return nil
		}
		return fmt.Errorf("invalid memory value %d for cpu %d, valid range: %d-%d in %d increments",
			mem, cpu, combo.memMin, combo.memMax, combo.memInc)
	}
	return fmt.Errorf("invalid cpu value %d, valid values: 256, 512, 1024, 2048, 4096, 8192, 16384", cpu)
}
