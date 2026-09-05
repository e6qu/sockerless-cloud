package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// EventBridge uses the AWS JSON protocol with X-Amz-Target:
// AWSEvents.<Operation>. The simulator implements the foundational
// rule/target/event slice used by SDK, CLI, and Terraform consumers.

type EBRule struct {
	Name               string            `json:"Name"`
	Arn                string            `json:"Arn"`
	EventBusName       string            `json:"EventBusName,omitempty"`
	EventPattern       string            `json:"EventPattern,omitempty"`
	ScheduleExpression string            `json:"ScheduleExpression,omitempty"`
	State              string            `json:"State"`
	Description        string            `json:"Description,omitempty"`
	RoleArn            string            `json:"RoleArn,omitempty"`
	Tags               map[string]string `json:"-"`
	CreatedAt          int64             `json:"-"`
}

type EBEventBus struct {
	Name             string            `json:"Name"`
	Arn              string            `json:"Arn"`
	Description      string            `json:"Description,omitempty"`
	KmsKeyIdentifier string            `json:"KmsKeyIdentifier,omitempty"`
	DeadLetterConfig json.RawMessage   `json:"DeadLetterConfig,omitempty"`
	Policy           string            `json:"Policy,omitempty"`
	CreationTime     int64             `json:"CreationTime,omitempty"`
	LastModifiedTime int64             `json:"LastModifiedTime,omitempty"`
	Tags             map[string]string `json:"-"`
}

type EBTarget struct {
	ID        string `json:"Id"`
	Arn       string `json:"Arn"`
	RoleArn   string `json:"RoleArn,omitempty"`
	Input     string `json:"Input,omitempty"`
	InputPath string `json:"InputPath,omitempty"`
	// Structured target parameters round-trip byte-exact: storing and
	// re-emitting the raw JSON preserves every sub-shape so ListTargetsByRule
	// returns what PutTargets received (terraform aws_cloudwatch_event_target
	// reads these back).
	EcsParameters        json.RawMessage `json:"EcsParameters,omitempty"`
	InputTransformer     json.RawMessage `json:"InputTransformer,omitempty"`
	RetryPolicy          json.RawMessage `json:"RetryPolicy,omitempty"`
	DeadLetterConfig     json.RawMessage `json:"DeadLetterConfig,omitempty"`
	SqsParameters        json.RawMessage `json:"SqsParameters,omitempty"`
	HttpParameters       json.RawMessage `json:"HttpParameters,omitempty"`
	BatchParameters      json.RawMessage `json:"BatchParameters,omitempty"`
	RunCommandParameters json.RawMessage `json:"RunCommandParameters,omitempty"`
	KinesisParameters    json.RawMessage `json:"KinesisParameters,omitempty"`
}

type EBEventRecord struct {
	ID         string   `json:"id"`
	Source     string   `json:"source"`
	DetailType string   `json:"detail-type"`
	Detail     string   `json:"detail,omitempty"`
	Time       int64    `json:"time"`
	Resources  []string `json:"resources,omitempty"`
}

type EBArchive struct {
	ArchiveName      string          `json:"ArchiveName"`
	ArchiveArn       string          `json:"ArchiveArn"`
	EventSourceArn   string          `json:"EventSourceArn"`
	Description      string          `json:"Description,omitempty"`
	EventPattern     string          `json:"EventPattern,omitempty"`
	RetentionDays    *int32          `json:"RetentionDays,omitempty"`
	State            string          `json:"State"`
	StateReason      string          `json:"StateReason,omitempty"`
	CreationTime     int64           `json:"CreationTime,omitempty"`
	EventCount       int64           `json:"EventCount"`
	SizeBytes        int64           `json:"SizeBytes"`
	ArchivedEvents   []EBEventRecord `json:"-"`
	KmsKeyIdentifier string          `json:"KmsKeyIdentifier,omitempty"`
}

type EBReplay struct {
	ReplayName              string         `json:"ReplayName"`
	ReplayArn               string         `json:"ReplayArn"`
	Description             string         `json:"Description,omitempty"`
	EventSourceArn          string         `json:"EventSourceArn"`
	EventStartTime          int64          `json:"EventStartTime,omitempty"`
	EventEndTime            int64          `json:"EventEndTime,omitempty"`
	EventLastReplayedTime   int64          `json:"EventLastReplayedTime,omitempty"`
	ReplayStartTime         int64          `json:"ReplayStartTime,omitempty"`
	ReplayEndTime           int64          `json:"ReplayEndTime,omitempty"`
	State                   string         `json:"State"`
	StateReason             string         `json:"StateReason,omitempty"`
	Destination             map[string]any `json:"-"`
	ReplayDestinationString string         `json:"-"`
}

var (
	ebBuses    sim.Store[EBEventBus]
	ebRules    sim.Store[EBRule]
	ebTargets  sim.Store[[]EBTarget]
	ebEvents   sim.Store[[]EBEventRecord]
	ebArchives sim.Store[EBArchive]
	ebReplays  sim.Store[EBReplay]
)

func registerEventBridge(r *AWSRouter, srv *sim.Server) {
	ebBuses = sim.MakeStore[EBEventBus](srv.DB(), "eventbridge_buses")
	ebRules = sim.MakeStore[EBRule](srv.DB(), "eventbridge_rules")
	ebTargets = sim.MakeStore[[]EBTarget](srv.DB(), "eventbridge_targets")
	ebEvents = sim.MakeStore[[]EBEventRecord](srv.DB(), "eventbridge_events")
	ebArchives = sim.MakeStore[EBArchive](srv.DB(), "eventbridge_archives")
	ebReplays = sim.MakeStore[EBReplay](srv.DB(), "eventbridge_replays")

	r.Register("AWSEvents.CreateEventBus", handleEBCreateEventBus)
	r.Register("AWSEvents.DescribeEventBus", handleEBDescribeEventBus)
	r.Register("AWSEvents.ListEventBuses", handleEBListEventBuses)
	r.Register("AWSEvents.DeleteEventBus", handleEBDeleteEventBus)
	r.Register("AWSEvents.PutPermission", handleEBPutPermission)
	r.Register("AWSEvents.RemovePermission", handleEBRemovePermission)
	r.Register("AWSEvents.PutRule", handleEBPutRule)
	r.Register("AWSEvents.DescribeRule", handleEBDescribeRule)
	r.Register("AWSEvents.ListRules", handleEBListRules)
	r.Register("AWSEvents.ListRuleNamesByTarget", handleEBListRuleNamesByTarget)
	r.Register("AWSEvents.TestEventPattern", handleEBTestEventPattern)
	r.Register("AWSEvents.UpdateEventBus", handleEBUpdateEventBus)
	r.Register("AWSEvents.DeleteRule", handleEBDeleteRule)
	r.Register("AWSEvents.EnableRule", handleEBEnableRule)
	r.Register("AWSEvents.DisableRule", handleEBDisableRule)
	r.Register("AWSEvents.PutTargets", handleEBPutTargets)
	r.Register("AWSEvents.ListTargetsByRule", handleEBListTargetsByRule)
	r.Register("AWSEvents.RemoveTargets", handleEBRemoveTargets)
	r.Register("AWSEvents.PutEvents", handleEBPutEvents)
	r.Register("AWSEvents.TagResource", handleEBTagResource)
	r.Register("AWSEvents.UntagResource", handleEBUntagResource)
	r.Register("AWSEvents.ListTagsForResource", handleEBListTagsForResource)
	r.Register("AWSEvents.CreateArchive", handleEBCreateArchive)
	r.Register("AWSEvents.DescribeArchive", handleEBDescribeArchive)
	r.Register("AWSEvents.ListArchives", handleEBListArchives)
	r.Register("AWSEvents.DeleteArchive", handleEBDeleteArchive)
	r.Register("AWSEvents.StartReplay", handleEBStartReplay)
	r.Register("AWSEvents.DescribeReplay", handleEBDescribeReplay)
	r.Register("AWSEvents.ListReplays", handleEBListReplays)
	r.Register("AWSEvents.UpdateArchive", handleEBUpdateArchive)
	r.Register("AWSEvents.CancelReplay", handleEBCancelReplay)

	registerEventBridgeConnectivity(r, srv)
}

func ebRuleArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:rule/%s", awsRegion(), awsAccountID(), name)
}

func ebRuleArnForBus(bus, name string) string {
	if ebBusName(bus) == "default" {
		return ebRuleArn(name)
	}
	return fmt.Sprintf("arn:aws:events:%s:%s:rule/%s/%s", awsRegion(), awsAccountID(), ebBusName(bus), name)
}

func ebBusArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:event-bus/%s", awsRegion(), awsAccountID(), ebBusName(name))
}

func ebArchiveArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:archive/%s", awsRegion(), awsAccountID(), name)
}

func ebReplayArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:replay/%s", awsRegion(), awsAccountID(), name)
}

func ebBusName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

func ebRuleKey(bus, name string) string {
	return ebBusName(bus) + "/" + name
}

func ebDefaultBus() EBEventBus {
	now := time.Now().Unix()
	return EBEventBus{
		Name:             "default",
		Arn:              ebBusArn("default"),
		CreationTime:     now,
		LastModifiedTime: now,
	}
}

func ebGetBus(name string) (EBEventBus, bool) {
	busName := ebBusName(name)
	if busName == "default" {
		if bus, ok := ebBuses.Get("default"); ok {
			return bus, true
		}
		bus := ebDefaultBus()
		ebBuses.Put("default", bus)
		return bus, true
	}
	return ebBuses.Get(busName)
}

func ebPutBus(bus EBEventBus) {
	if bus.Name == "" {
		bus.Name = "default"
	}
	if bus.Arn == "" {
		bus.Arn = ebBusArn(bus.Name)
	}
	now := time.Now().Unix()
	if bus.CreationTime == 0 {
		bus.CreationTime = now
	}
	bus.LastModifiedTime = now
	ebBuses.Put(bus.Name, bus)
}

func writeEBJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func handleEBCreateEventBus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string                        `json:"Name"`
		Description      string                        `json:"Description"`
		KmsKeyIdentifier string                        `json:"KmsKeyIdentifier"`
		Tags             []struct{ Key, Value string } `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Name == "default" {
		AWSError(w, "ValidationException", "custom event bus name is required", http.StatusBadRequest)
		return
	}
	if _, ok := ebBuses.Get(req.Name); ok {
		AWSError(w, "ResourceAlreadyExistsException", "Event bus already exists", http.StatusConflict)
		return
	}
	tags := map[string]string{}
	for _, tag := range req.Tags {
		tags[tag.Key] = tag.Value
	}
	bus := EBEventBus{
		Name:             req.Name,
		Arn:              ebBusArn(req.Name),
		Description:      req.Description,
		KmsKeyIdentifier: req.KmsKeyIdentifier,
		Tags:             tags,
	}
	ebPutBus(bus)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"EventBusArn":      bus.Arn,
		"Description":      req.Description,
		"KmsKeyIdentifier": req.KmsKeyIdentifier,
	})
}

func handleEBDescribeEventBus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	_ = sim.ReadJSON(r, &req)
	bus, ok := ebGetBus(req.Name)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Event bus does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, bus)
}

func handleEBListEventBuses(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix string `json:"NamePrefix"`
		Limit      int    `json:"Limit"`
		NextToken  string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	_, _ = ebGetBus("default")
	buses := make([]EBEventBus, 0)
	for _, bus := range ebBuses.List() {
		if req.NamePrefix != "" && !strings.HasPrefix(bus.Name, req.NamePrefix) {
			continue
		}
		buses = append(buses, bus)
	}
	sort.Slice(buses, func(i, j int) bool { return buses[i].Name < buses[j].Name })
	page, next := awsPageExplicit(buses, req.NextToken, req.Limit)
	out := map[string]any{"EventBuses": page}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBDeleteEventBus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Name == "default" {
		AWSError(w, "ValidationException", "default event bus cannot be deleted", http.StatusBadRequest)
		return
	}
	if !ebBuses.Delete(req.Name) {
		AWSError(w, "ResourceNotFoundException", "Event bus does not exist", http.StatusNotFound)
		return
	}
	for _, rule := range ebRules.List() {
		if rule.EventBusName == req.Name {
			key := ebRuleKey(rule.EventBusName, rule.Name)
			ebRules.Delete(key)
			ebTargets.Delete(key)
		}
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBPutPermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventBusName string         `json:"EventBusName"`
		StatementID  string         `json:"StatementId"`
		Action       string         `json:"Action"`
		Principal    string         `json:"Principal"`
		Policy       string         `json:"Policy"`
		Condition    map[string]any `json:"Condition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	bus, ok := ebGetBus(req.EventBusName)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Event bus does not exist", http.StatusNotFound)
		return
	}
	if req.Policy != "" {
		var doc map[string]any
		if err := json.Unmarshal([]byte(req.Policy), &doc); err != nil {
			AWSError(w, "ValidationException", "Policy is not valid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		bus.Policy = req.Policy
		ebPutBus(bus)
		writeEBJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if req.StatementID == "" || req.Action == "" || req.Principal == "" {
		AWSError(w, "ValidationException", "StatementId, Action, and Principal are required", http.StatusBadRequest)
		return
	}
	policy, err := ebPolicyDocument(bus.Policy)
	if err != nil {
		AWSError(w, "InternalException", "Stored resource policy is not valid JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}
	statements := ebPolicyStatements(policy)
	statement := map[string]any{
		"Sid":       req.StatementID,
		"Effect":    "Allow",
		"Principal": map[string]any{"AWS": req.Principal},
		"Action":    req.Action,
		"Resource":  bus.Arn,
	}
	if req.Condition != nil {
		statement["Condition"] = req.Condition
	}
	replaced := false
	for i, existing := range statements {
		if existing["Sid"] == req.StatementID {
			statements[i] = statement
			replaced = true
			break
		}
	}
	if !replaced {
		statements = append(statements, statement)
	}
	policy["Statement"] = statements
	body, _ := json.Marshal(policy)
	bus.Policy = string(body)
	ebPutBus(bus)
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBRemovePermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventBusName         string `json:"EventBusName"`
		StatementID          string `json:"StatementId"`
		RemoveAllPermissions bool   `json:"RemoveAllPermissions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	bus, ok := ebGetBus(req.EventBusName)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Event bus does not exist", http.StatusNotFound)
		return
	}
	if req.RemoveAllPermissions {
		bus.Policy = ""
		ebPutBus(bus)
		writeEBJSON(w, http.StatusOK, map[string]any{})
		return
	}
	policy, err := ebPolicyDocument(bus.Policy)
	if err != nil {
		AWSError(w, "InternalException", "Stored resource policy is not valid JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filtered := make([]map[string]any, 0)
	for _, statement := range ebPolicyStatements(policy) {
		if statement["Sid"] != req.StatementID {
			filtered = append(filtered, statement)
		}
	}
	if len(filtered) == 0 {
		bus.Policy = ""
	} else {
		policy["Statement"] = filtered
		body, _ := json.Marshal(policy)
		bus.Policy = string(body)
	}
	ebPutBus(bus)
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func ebPolicyDocument(raw string) (map[string]any, error) {
	policy := map[string]any{
		"Version":   "2012-10-17",
		"Statement": []map[string]any{},
	}
	if raw == "" {
		return policy, nil
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil, err
	}
	if _, ok := policy["Statement"]; !ok {
		policy["Statement"] = []map[string]any{}
	}
	return policy, nil
}

func ebPolicyStatements(policy map[string]any) []map[string]any {
	raw, ok := policy["Statement"]
	if !ok {
		return []map[string]any{}
	}
	switch v := raw.(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{v}
	default:
		return []map[string]any{}
	}
}

func handleEBPutRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name               string                        `json:"Name"`
		EventBusName       string                        `json:"EventBusName"`
		EventPattern       string                        `json:"EventPattern"`
		ScheduleExpression string                        `json:"ScheduleExpression"`
		State              string                        `json:"State"`
		Description        string                        `json:"Description"`
		RoleArn            string                        `json:"RoleArn"`
		Tags               []struct{ Key, Value string } `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "ValidationException", "Name is required", http.StatusBadRequest)
		return
	}
	if _, ok := ebGetBus(req.EventBusName); !ok {
		AWSError(w, "ResourceNotFoundException", "Event bus does not exist", http.StatusNotFound)
		return
	}
	state := req.State
	if state == "" {
		state = "ENABLED"
	}
	key := ebRuleKey(req.EventBusName, req.Name)
	rule := EBRule{
		Name:               req.Name,
		Arn:                ebRuleArnForBus(req.EventBusName, req.Name),
		EventBusName:       ebBusName(req.EventBusName),
		EventPattern:       req.EventPattern,
		ScheduleExpression: req.ScheduleExpression,
		State:              state,
		Description:        req.Description,
		RoleArn:            req.RoleArn,
		CreatedAt:          time.Now().Unix(),
	}
	if existing, ok := ebRules.Get(key); ok {
		rule.Tags = existing.Tags
		rule.CreatedAt = existing.CreatedAt
	}
	if len(req.Tags) > 0 {
		rule.Tags = map[string]string{}
		for _, tag := range req.Tags {
			rule.Tags[tag.Key] = tag.Value
		}
	}
	ebRules.Put(key, rule)
	writeEBJSON(w, http.StatusOK, map[string]string{"RuleArn": rule.Arn})
}

func handleEBDescribeRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"Name"`
		EventBusName string `json:"EventBusName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	rule, ok := ebRules.Get(ebRuleKey(req.EventBusName, req.Name))
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Rule does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, rule)
}

func handleEBListRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamePrefix   string `json:"NamePrefix"`
		EventBusName string `json:"EventBusName"`
		Limit        int    `json:"Limit"`
		NextToken    string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	bus := ebBusName(req.EventBusName)
	rules := make([]EBRule, 0)
	for _, rule := range ebRules.List() {
		if rule.EventBusName != bus {
			continue
		}
		if req.NamePrefix != "" && !strings.HasPrefix(rule.Name, req.NamePrefix) {
			continue
		}
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	page, next := awsPageExplicit(rules, req.NextToken, req.Limit)
	out := map[string]any{"Rules": page}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

// handleEBListRuleNamesByTarget returns the names of rules on the given bus that
// have a target with the supplied ARN. EventBridge scans every rule's target
// list for the ARN; a rule with multiple targets sharing the ARN is reported
// once.
func handleEBListRuleNamesByTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetArn    string `json:"TargetArn"`
		EventBusName string `json:"EventBusName"`
		Limit        int    `json:"Limit"`
		NextToken    string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetArn == "" {
		AWSError(w, "ValidationException", "TargetArn is required", http.StatusBadRequest)
		return
	}
	bus := ebBusName(req.EventBusName)
	names := make([]string, 0)
	for _, rule := range ebRules.List() {
		if rule.EventBusName != bus {
			continue
		}
		targets, _ := ebTargets.Get(ebRuleKey(rule.EventBusName, rule.Name))
		for _, target := range targets {
			if target.Arn == req.TargetArn {
				names = append(names, rule.Name)
				break
			}
		}
	}
	sort.Strings(names)
	page, next := awsPageExplicit(names, req.NextToken, req.Limit)
	out := map[string]any{"RuleNames": page}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

// handleEBTestEventPattern reports whether the supplied Event matches the
// supplied EventPattern. It reuses ebEventPatternMatches — the same evaluator
// PutEvents delivery uses — so a "Result":true here is exactly a rule that
// would fire on the event. A malformed pattern yields InvalidEventPatternException.
func handleEBTestEventPattern(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventPattern string `json:"EventPattern"`
		Event        string `json:"Event"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EventPattern == "" {
		AWSError(w, "ValidationException", "EventPattern is required", http.StatusBadRequest)
		return
	}
	if req.Event == "" {
		AWSError(w, "ValidationException", "Event is required", http.StatusBadRequest)
		return
	}
	if err := ebValidateEventPattern(req.EventPattern); err != nil {
		AWSError(w, "InvalidEventPatternException", "Event pattern is not valid. Reason: "+err.Error(), http.StatusBadRequest)
		return
	}
	var event struct {
		Source     string          `json:"source"`
		DetailType string          `json:"detail-type"`
		Detail     json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal([]byte(req.Event), &event); err != nil {
		AWSError(w, "InvalidEventPatternException", "Event is not valid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	result := ebEventPatternMatches(req.EventPattern, event.Source, event.DetailType, string(event.Detail))
	writeEBJSON(w, http.StatusOK, map[string]any{"Result": result})
}

// ebValidateEventPattern reports whether a pattern is structurally valid per
// EventBridge's content-filtering grammar: the pattern is a JSON object whose
// every value is either an OR array of leaf matchers or a nested pattern object
// (which recurses). A scalar pattern value (e.g. {"source":"x"} instead of
// {"source":["x"]}) is invalid, matching real EventBridge.
func ebValidateEventPattern(patternJSON string) error {
	var pattern map[string]any
	if err := json.Unmarshal([]byte(patternJSON), &pattern); err != nil {
		return fmt.Errorf("event pattern is not valid JSON: %w", err)
	}
	return ebValidatePatternObject(pattern)
}

func ebValidatePatternObject(pattern map[string]any) error {
	if len(pattern) == 0 {
		return fmt.Errorf(`"%s" must be an object or an array`, "pattern")
	}
	for key, val := range pattern {
		switch v := val.(type) {
		case map[string]any:
			if err := ebValidatePatternObject(v); err != nil {
				return err
			}
		case []any:
			if len(v) == 0 {
				return fmt.Errorf(`"%s" must be a non-empty array`, key)
			}
		default:
			return fmt.Errorf(`"%s" must be an object or an array`, key)
		}
	}
	return nil
}

// handleEBUpdateEventBus updates a named (or default) event bus's mutable
// fields — Description, KmsKeyIdentifier, and DeadLetterConfig — and returns the
// bus's identity plus the updated fields, matching the UpdateEventBus response.
func handleEBUpdateEventBus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string          `json:"Name"`
		Description      *string         `json:"Description"`
		KmsKeyIdentifier *string         `json:"KmsKeyIdentifier"`
		DeadLetterConfig json.RawMessage `json:"DeadLetterConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	bus, ok := ebGetBus(req.Name)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Event bus does not exist", http.StatusNotFound)
		return
	}
	if req.Description != nil {
		bus.Description = *req.Description
	}
	if req.KmsKeyIdentifier != nil {
		bus.KmsKeyIdentifier = *req.KmsKeyIdentifier
	}
	if len(req.DeadLetterConfig) > 0 {
		bus.DeadLetterConfig = req.DeadLetterConfig
	}
	ebPutBus(bus)
	out := map[string]any{
		"Arn":  bus.Arn,
		"Name": bus.Name,
	}
	if bus.Description != "" {
		out["Description"] = bus.Description
	}
	if bus.KmsKeyIdentifier != "" {
		out["KmsKeyIdentifier"] = bus.KmsKeyIdentifier
	}
	if len(bus.DeadLetterConfig) > 0 {
		out["DeadLetterConfig"] = bus.DeadLetterConfig
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBDeleteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"Name"`
		EventBusName string `json:"EventBusName"`
		Force        bool   `json:"Force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ebRuleKey(req.EventBusName, req.Name)
	if _, ok := ebRules.Get(key); !ok {
		AWSError(w, "ResourceNotFoundException", "Rule does not exist", http.StatusNotFound)
		return
	}
	if targets, ok := ebTargets.Get(key); ok && len(targets) > 0 && !req.Force {
		AWSError(w, "ConcurrentModificationException", "Rule has targets", http.StatusConflict)
		return
	}
	ebRules.Delete(key)
	ebTargets.Delete(key)
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBEnableRule(w http.ResponseWriter, r *http.Request)  { ebSetRuleState(w, r, "ENABLED") }
func handleEBDisableRule(w http.ResponseWriter, r *http.Request) { ebSetRuleState(w, r, "DISABLED") }

func ebSetRuleState(w http.ResponseWriter, r *http.Request, state string) {
	var req struct {
		Name         string `json:"Name"`
		EventBusName string `json:"EventBusName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ebRuleKey(req.EventBusName, req.Name)
	if !ebRules.Update(key, func(rule *EBRule) { rule.State = state }) {
		AWSError(w, "ResourceNotFoundException", "Rule does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBPutTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule         string     `json:"Rule"`
		EventBusName string     `json:"EventBusName"`
		Targets      []EBTarget `json:"Targets"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ebRuleKey(req.EventBusName, req.Rule)
	if _, ok := ebRules.Get(key); !ok {
		AWSError(w, "ResourceNotFoundException", "Rule does not exist", http.StatusNotFound)
		return
	}
	existing, _ := ebTargets.Get(key)
	byID := map[string]EBTarget{}
	for _, target := range existing {
		byID[target.ID] = target
	}
	for _, target := range req.Targets {
		if target.ID == "" || target.Arn == "" {
			writeEBJSON(w, http.StatusOK, map[string]any{
				"FailedEntryCount": 1,
				"FailedEntries": []map[string]string{{
					"TargetId":     target.ID,
					"ErrorCode":    "ValidationException",
					"ErrorMessage": "Target Id and Arn are required",
				}},
			})
			return
		}
		byID[target.ID] = target
	}
	out := make([]EBTarget, 0, len(byID))
	for _, target := range byID {
		out = append(out, target)
	}
	ebTargets.Put(key, out)
	writeEBJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": 0, "FailedEntries": []any{}})
}

func handleEBListTargetsByRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule         string `json:"Rule"`
		EventBusName string `json:"EventBusName"`
		Limit        int    `json:"Limit"`
		NextToken    string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ebRuleKey(req.EventBusName, req.Rule)
	if _, ok := ebRules.Get(key); !ok {
		AWSError(w, "ResourceNotFoundException", "Rule does not exist", http.StatusNotFound)
		return
	}
	targets, _ := ebTargets.Get(key)
	if targets == nil {
		targets = []EBTarget{}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	page, next := awsPageExplicit(targets, req.NextToken, req.Limit)
	out := map[string]any{"Targets": page}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBRemoveTargets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule         string   `json:"Rule"`
		EventBusName string   `json:"EventBusName"`
		Ids          []string `json:"Ids"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ebRuleKey(req.EventBusName, req.Rule)
	targets, _ := ebTargets.Get(key)
	remove := map[string]bool{}
	for _, id := range req.Ids {
		remove[id] = true
	}
	out := targets[:0]
	for _, target := range targets {
		if !remove[target.ID] {
			out = append(out, target)
		}
	}
	ebTargets.Put(key, out)
	writeEBJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": 0, "FailedEntries": []any{}})
}

func handleEBPutEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []struct {
			Source       string   `json:"Source"`
			DetailType   string   `json:"DetailType"`
			Detail       string   `json:"Detail"`
			EventBusName string   `json:"EventBusName"`
			Resources    []string `json:"Resources"`
			Time         float64  `json:"Time"`
		} `json:"Entries"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	entries := make([]map[string]string, 0, len(req.Entries))
	failed := 0
	for _, entry := range req.Entries {
		// Real EventBridge requires each entry's Detail to be a valid JSON
		// object; a malformed Detail fails that single entry (the rest of the
		// batch still succeeds) with a MalformedDetail per-entry error.
		if entry.Detail != "" {
			var detailObj any
			if err := json.Unmarshal([]byte(entry.Detail), &detailObj); err != nil {
				failed++
				entries = append(entries, map[string]string{
					"ErrorCode":    "MalformedDetail",
					"ErrorMessage": "Detail is malformed.",
				})
				continue
			}
		}
		eventID := generateUUID()
		now := time.Now().Unix()
		record := EBEventRecord{
			ID:         eventID,
			Source:     entry.Source,
			DetailType: entry.DetailType,
			Detail:     entry.Detail,
			Time:       now,
			Resources:  entry.Resources,
		}
		bus := ebBusName(entry.EventBusName)
		if _, ok := ebGetBus(bus); !ok {
			failed++
			entries = append(entries, map[string]string{
				"ErrorCode":    "ResourceNotFoundException",
				"ErrorMessage": "Event bus does not exist",
			})
			continue
		}
		events, _ := ebEvents.Get(bus)
		events = append(events, record)
		ebEvents.Put(bus, events)
		archiveEBEvent(bus, record)
		deliverEBEvent(bus, entry.Source, entry.DetailType, entry.Detail, eventID)
		entries = append(entries, map[string]string{"EventId": eventID})
	}
	writeEBJSON(w, http.StatusOK, map[string]any{"FailedEntryCount": failed, "Entries": entries})
}

// ebMaxArchivedEvents bounds an archive's retained-event slice when the archive
// has no finite RetentionDays (0 = indefinite in real EventBridge). Without a
// bound a long-lived sim leaks memory as ArchivedEvents grows unboundedly. A
// real indefinite archive is effectively bounded by storage; the sim models that
// as a generous in-memory window, dropping the oldest events past the cap.
// replayArchivedEvents iterates the retained slice, so only the retained window
// replays — faithful to a retention period.
const ebMaxArchivedEvents = 10000

func archiveEBEvent(bus string, record EBEventRecord) {
	sourceArn := ebBusArn(bus)
	for _, archive := range ebArchives.List() {
		if archive.EventSourceArn != sourceArn || archive.State != "ENABLED" {
			continue
		}
		if archive.EventPattern != "" && !ebEventPatternMatches(archive.EventPattern, record.Source, record.DetailType, record.Detail) {
			continue
		}
		archive.ArchivedEvents = append(archive.ArchivedEvents, record)
		archive.ArchivedEvents = ebApplyRetention(archive.ArchivedEvents, archive.RetentionDays)
		// EventCount and SizeBytes describe the retained window, recomputed from
		// the (possibly trimmed) slice so they stay consistent after aging-out.
		archive.EventCount = int64(len(archive.ArchivedEvents))
		archive.SizeBytes = 0
		for _, e := range archive.ArchivedEvents {
			archive.SizeBytes += int64(len(e.Detail))
		}
		ebArchives.Put(archive.ArchiveName, archive)
	}
}

// ebApplyRetention bounds an archive's retained events. When RetentionDays is set
// (> 0), events older than that window (by their epoch Time) age out, matching
// EventBridge's retention period. When it is unset/0 (indefinite), the slice is
// capped to the newest ebMaxArchivedEvents so memory stays bounded. The newest
// events are always retained, so a just-archived event survives both paths.
func ebApplyRetention(events []EBEventRecord, retentionDays *int32) []EBEventRecord {
	if retentionDays != nil && *retentionDays > 0 {
		cutoff := time.Now().Unix() - int64(*retentionDays)*86400
		kept := events[:0]
		for _, e := range events {
			if e.Time >= cutoff {
				kept = append(kept, e)
			}
		}
		return kept
	}
	if len(events) > ebMaxArchivedEvents {
		return events[len(events)-ebMaxArchivedEvents:]
	}
	return events
}

func deliverEBEvent(bus, source, detailType, detail, eventID string) {
	for _, rule := range ebRules.List() {
		if rule.EventBusName != bus || rule.State == "DISABLED" {
			continue
		}
		if !ebRuleMatches(rule, source, detailType, detail) {
			continue
		}
		targets, _ := ebTargets.Get(ebRuleKey(rule.EventBusName, rule.Name))
		for _, target := range targets {
			body := ebApplyInput(target, source, detailType, detail, eventID)
			deliverEBTarget(rule.Arn, target, body, source, detailType, eventID)
		}
	}
}

// ebBuildEvent assembles the full EventBridge event object that a target's
// InputPath / InputTransformer JSONPaths resolve against.
func ebBuildEvent(source, detailType, detail, eventID string) map[string]any {
	var detailObj any
	if detail != "" {
		_ = json.Unmarshal([]byte(detail), &detailObj)
	}
	return map[string]any{
		"version":     "0",
		"id":          eventID,
		"detail-type": detailType,
		"source":      source,
		"account":     awsAccountID(),
		"time":        time.Now().UTC().Format(time.RFC3339),
		"region":      awsRegion(),
		"resources":   []any{},
		"detail":      detailObj,
	}
}

// ebJSONPath resolves a simple EventBridge input-transformer JSONPath
// ($.a.b, $.a[0].b) against the event object, returning the value + found.
func ebJSONPath(root any, path string) (any, bool) {
	if !strings.HasPrefix(path, "$") {
		return nil, false
	}
	cur := root
	rest := strings.TrimPrefix(path, "$")
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			i := strings.IndexAny(rest, ".[")
			key := rest
			if i >= 0 {
				key, rest = rest[:i], rest[i:]
			} else {
				rest = ""
			}
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[key]; !ok {
				return nil, false
			}
		case strings.HasPrefix(rest, "["):
			j := strings.Index(rest, "]")
			if j < 0 {
				return nil, false
			}
			idx, err := strconv.Atoi(rest[1:j])
			rest = rest[j+1:]
			arr, ok := cur.([]any)
			if err != nil || !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// ebApplyInput computes the body delivered to a target per its mutually
// exclusive Input / InputPath / InputTransformer, in real-EventBridge priority.
func ebApplyInput(target EBTarget, source, detailType, detail, eventID string) string {
	if target.Input != "" {
		return target.Input
	}
	if target.InputPath == "" && len(target.InputTransformer) == 0 {
		event, _ := json.Marshal(ebBuildEvent(source, detailType, detail, eventID))
		return string(event)
	}
	event := ebBuildEvent(source, detailType, detail, eventID)
	if target.InputPath != "" {
		if v, ok := ebJSONPath(event, target.InputPath); ok {
			raw, _ := json.Marshal(v)
			return string(raw)
		}
		return "null"
	}
	var it struct {
		InputPathsMap map[string]string `json:"InputPathsMap"`
		InputTemplate string            `json:"InputTemplate"`
	}
	if json.Unmarshal(target.InputTransformer, &it) != nil || it.InputTemplate == "" {
		return detail
	}
	out := it.InputTemplate
	for varName, jp := range it.InputPathsMap {
		val := ""
		if v, ok := ebJSONPath(event, jp); ok {
			if s, isStr := v.(string); isStr {
				val = s // unquoted: matches AWS inserting a string into "<var>"
			} else {
				raw, _ := json.Marshal(v)
				val = string(raw)
			}
		}
		out = strings.ReplaceAll(out, "<"+varName+">", val)
	}
	return out
}

func ebRuleMatches(rule EBRule, source, detailType, detail string) bool {
	if rule.EventPattern == "" {
		return true
	}
	return ebEventPatternMatches(rule.EventPattern, source, detailType, detail)
}

// ebEventPatternMatches evaluates an EventBridge event pattern against an event.
// It builds the event as the same nested JSON object EventBridge matches against
// (top-level "source"/"detail-type"/"resources" plus the parsed "detail" object)
// and recurses through the pattern. Per the EventBridge content-filtering rules
// (https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-event-patterns.html):
//   - a pattern value array is an OR — the event value matches if it satisfies
//     any element (an exact string or a content-matcher object);
//   - sibling keys (including nested keys inside "detail") are ANDed;
//   - a pattern value that is an object recurses into the event's matching key.
func ebEventPatternMatches(patternJSON, source, detailType, detail string) bool {
	var pattern map[string]any
	if err := json.Unmarshal([]byte(patternJSON), &pattern); err != nil {
		return false
	}
	event := map[string]any{
		"source":      source,
		"detail-type": detailType,
	}
	if detail != "" {
		var d any
		if err := json.Unmarshal([]byte(detail), &d); err == nil {
			event["detail"] = d
		}
	}
	return ebMatchObject(pattern, event)
}

// ebMatchObject ANDs every key in the pattern against the event object.
func ebMatchObject(pattern map[string]any, event any) bool {
	obj, ok := event.(map[string]any)
	if !ok {
		return false
	}
	for key, patVal := range pattern {
		eventVal, present := obj[key]
		if !ebMatchValue(patVal, eventVal, present) {
			return false
		}
	}
	return true
}

// ebMatchValue dispatches on the pattern node shape: a nested object recurses,
// an array is an OR of leaf matchers, anything else is invalid.
func ebMatchValue(patVal, eventVal any, present bool) bool {
	switch pv := patVal.(type) {
	case map[string]any:
		// Nested key path: recurse into the event's sub-object.
		return ebMatchObject(pv, eventVal)
	case []any:
		// OR list: the event value matches if it satisfies any element.
		for _, candidate := range pv {
			if ebMatchLeaf(candidate, eventVal, present) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// ebMatchLeaf evaluates one element of a pattern's value array against the event
// value: either an exact value (string/number/bool/null) or a content-matcher
// object ({"prefix":...}, {"suffix":...}, {"anything-but":...}, {"numeric":...},
// {"exists":...}, {"cidr":...}, {"equals-ignore-case":...}).
func ebMatchLeaf(candidate, eventVal any, present bool) bool {
	if m, ok := candidate.(map[string]any); ok {
		return ebMatchContentFilter(m, eventVal, present)
	}
	// Exact match. EventBridge compares decoded JSON values, so a string
	// pattern matches a string event value, a number matches a number, etc.
	if !present {
		return false
	}
	return ebValuesEqual(candidate, eventVal)
}

func ebValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case float64:
		bv, ok := ebToFloat(b)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return false
	}
}

// ebMatchContentFilter implements EventBridge's content-matcher objects.
func ebMatchContentFilter(filter map[string]any, eventVal any, present bool) bool {
	// exists is evaluated on presence/absence, not on the value.
	if want, ok := filter["exists"]; ok {
		wantBool, _ := want.(bool)
		return present == wantBool
	}
	if prefix, ok := filter["prefix"]; ok {
		s, sok := eventVal.(string)
		p, pok := prefix.(string)
		return present && sok && pok && strings.HasPrefix(s, p)
	}
	if suffix, ok := filter["suffix"]; ok {
		s, sok := eventVal.(string)
		p, pok := suffix.(string)
		return present && sok && pok && strings.HasSuffix(s, p)
	}
	if ci, ok := filter["equals-ignore-case"]; ok {
		s, sok := eventVal.(string)
		p, pok := ci.(string)
		return present && sok && pok && strings.EqualFold(s, p)
	}
	if ab, ok := filter["anything-but"]; ok {
		return present && ebMatchAnythingBut(ab, eventVal)
	}
	if num, ok := filter["numeric"]; ok {
		return present && ebMatchNumeric(num, eventVal)
	}
	if c, ok := filter["cidr"]; ok {
		s, sok := eventVal.(string)
		cidr, cok := c.(string)
		return present && sok && cok && ebMatchCIDR(cidr, s)
	}
	return false
}

// ebMatchAnythingBut matches when the event value differs from every excluded
// value. The exclusion can be a single scalar or a list of scalars.
func ebMatchAnythingBut(exclude, eventVal any) bool {
	switch ex := exclude.(type) {
	case []any:
		for _, e := range ex {
			if ebValuesEqual(e, eventVal) {
				return false
			}
		}
		return true
	default:
		return !ebValuesEqual(exclude, eventVal)
	}
}

// ebMatchNumeric implements {"numeric": [op, value, ...]} where op is one of
// "=", "!=", "<", "<=", ">", ">=" and pairs can be chained (e.g.
// [">", 0, "<=", 5]). All chained conditions are ANDed.
func ebMatchNumeric(spec, eventVal any) bool {
	terms, ok := spec.([]any)
	if !ok || len(terms)%2 != 0 {
		return false
	}
	val, ok := ebToFloat(eventVal)
	if !ok {
		return false
	}
	for i := 0; i < len(terms); i += 2 {
		op, ok := terms[i].(string)
		if !ok {
			return false
		}
		bound, ok := ebToFloat(terms[i+1])
		if !ok {
			return false
		}
		switch op {
		case "=":
			if val != bound {
				return false
			}
		case "!=":
			if val == bound {
				return false
			}
		case "<":
			if !(val < bound) {
				return false
			}
		case "<=":
			if !(val <= bound) {
				return false
			}
		case ">":
			if !(val > bound) {
				return false
			}
		case ">=":
			if !(val >= bound) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func ebToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// ebMatchCIDR reports whether ip falls inside the cidr block.
func ebMatchCIDR(cidr, ip string) bool {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	return network.Contains(addr)
}

// ebLambdaNameFromARN extracts the function name from a Lambda function ARN
// (arn:aws:lambda:<region>:<acct>:function:<name>[:<qualifier>]). The sim keys
// lambdaFunctions by unqualified name, so any qualifier is dropped.
func ebLambdaNameFromARN(arn string) string {
	const marker = ":function:"
	i := strings.Index(arn, marker)
	if i < 0 {
		return ""
	}
	name := arn[i+len(marker):]
	if j := strings.Index(name, ":"); j >= 0 {
		name = name[:j]
	}
	return name
}

// deliverEBTarget delivers one matched event to one rule target. EventBridge
// delivers as the events.amazonaws.com service on the rule's behalf, so each
// delivery is authorized against the TARGET's resource-based policy with the
// service-initiation condition context (aws:SourceArn = the matched rule's ARN,
// aws:SourceAccount = this account). A target whose resource policy does not
// admit events.amazonaws.com for that source rule receives nothing — exactly as
// real AWS, which silently drops the delivery rather than enqueuing it.
func deliverEBTarget(ruleArn string, target EBTarget, body, source, detailType, eventID string) {
	src := iamServiceSource{
		Service:       "events.amazonaws.com",
		SourceArn:     ruleArn,
		SourceAccount: awsAccountID(),
	}
	if strings.HasPrefix(target.Arn, "arn:aws:sqs:") {
		if !iamAuthorizeServiceDelivery(target.Arn, "sqs:SendMessage", src) {
			return
		}
		queue := snsTopicNameFromARN(target.Arn)
		sqsEnqueue(queue, sqsSendEntry{MessageBody: body})
		return
	}
	if strings.HasPrefix(target.Arn, "arn:aws:lambda:") {
		if !iamAuthorizeServiceDelivery(target.Arn, "lambda:InvokeFunction", src) {
			return
		}
		name := ebLambdaNameFromARN(target.Arn)
		fn, ok := lambdaFunctions.Get(name)
		if !ok {
			return
		}
		// Real in-process invoke. EventBridge invokes asynchronously (an
		// "Event" invocation): the rule delivery does not wait on the function
		// result, so run the invoke in the background exactly as the async
		// Lambda Invoke path does.
		go func() { _, _, _ = invokeLambdaViaRuntimeAPI(fn, []byte(body)) }()
		return
	}
	if strings.HasPrefix(target.Arn, "arn:aws:sns:") {
		if !iamAuthorizeServiceDelivery(target.Arn, "sns:Publish", src) {
			return
		}
		if _, ok := snsTopics.Get(snsTopicNameFromARN(target.Arn)); !ok {
			return
		}
		snsFanout(target.Arn, eventID, detailType, body, nil)
		return
	}
	if strings.HasPrefix(target.Arn, "arn:aws:states:") {
		_, _ = sfnStartNestedExecution(target.Arn, eventID, body)
		return
	}
	if strings.HasPrefix(target.Arn, "arn:aws:logs:") && strings.Contains(target.Arn, ":log-group:") {
		group := strings.SplitN(target.Arn, ":log-group:", 2)[1]
		group = strings.TrimSuffix(group, ":*")
		if _, ok := cwLogGroups.Get(group); !ok {
			return
		}
		stream := "eventbridge/" + cloudTrailShortName(ruleArn)
		key := cwEventsKey(group, stream)
		now := time.Now().UnixMilli()
		if _, ok := cwLogStreams.Get(key); !ok {
			cwLogStreams.Put(key, CWLogStream{
				LogStreamName: stream,
				LogGroupName:  group,
				CreationTime:  now,
				Arn:           cwLogStreamArn(group, stream),
			})
			cwLogEvents.Put(key, []CWLogEvent{})
		}
		cwLogEvents.Update(key, func(events *[]CWLogEvent) {
			*events = append(*events, CWLogEvent{Timestamp: now, IngestionTime: now, Message: body})
		})
		cwLogStreams.Update(key, func(logStream *CWLogStream) {
			logStream.LastEventTimestamp = now
			logStream.LastIngestionTime = now
		})
		return
	}
	_, _, _ = source, detailType, eventID
}

func handleEBCreateArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArchiveName      string `json:"ArchiveName"`
		EventSourceArn   string `json:"EventSourceArn"`
		Description      string `json:"Description"`
		EventPattern     string `json:"EventPattern"`
		RetentionDays    *int32 `json:"RetentionDays"`
		KmsKeyIdentifier string `json:"KmsKeyIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ArchiveName == "" || req.EventSourceArn == "" {
		AWSError(w, "ValidationException", "ArchiveName and EventSourceArn are required", http.StatusBadRequest)
		return
	}
	if _, ok := ebArchives.Get(req.ArchiveName); ok {
		AWSError(w, "ResourceAlreadyExistsException", "Archive already exists", http.StatusConflict)
		return
	}
	if _, ok := ebBusByARN(req.EventSourceArn); !ok {
		AWSError(w, "ResourceNotFoundException", "Event source bus does not exist", http.StatusNotFound)
		return
	}
	now := time.Now().Unix()
	archive := EBArchive{
		ArchiveName:      req.ArchiveName,
		ArchiveArn:       ebArchiveArn(req.ArchiveName),
		EventSourceArn:   req.EventSourceArn,
		Description:      req.Description,
		EventPattern:     req.EventPattern,
		RetentionDays:    req.RetentionDays,
		State:            "ENABLED",
		CreationTime:     now,
		KmsKeyIdentifier: req.KmsKeyIdentifier,
	}
	ebArchives.Put(archive.ArchiveName, archive)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ArchiveArn":   archive.ArchiveArn,
		"CreationTime": archive.CreationTime,
		"State":        archive.State,
	})
}

func handleEBDescribeArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArchiveName string `json:"ArchiveName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	archive, ok := ebArchives.Get(req.ArchiveName)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Archive does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, archive)
}

func handleEBListArchives(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventSourceArn string `json:"EventSourceArn"`
		NamePrefix     string `json:"NamePrefix"`
		Limit          int    `json:"Limit"`
		NextToken      string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	archives := make([]EBArchive, 0)
	for _, archive := range ebArchives.List() {
		if req.EventSourceArn != "" && archive.EventSourceArn != req.EventSourceArn {
			continue
		}
		if req.NamePrefix != "" && !strings.HasPrefix(archive.ArchiveName, req.NamePrefix) {
			continue
		}
		archives = append(archives, archive)
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].ArchiveName < archives[j].ArchiveName })
	page, next := awsPageExplicit(archives, req.NextToken, req.Limit)
	summaries := make([]map[string]any, 0, len(page))
	for _, archive := range page {
		summaries = append(summaries, ebArchiveSummary(archive))
	}
	out := map[string]any{"Archives": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

// ebArchiveSummary projects an archive onto the list-shape Archive
// members; ArchiveArn / Description / EventPattern / KmsKeyIdentifier
// are describe-only and must not appear in ListArchives entries.
func ebArchiveSummary(a EBArchive) map[string]any {
	out := map[string]any{
		"ArchiveName":    a.ArchiveName,
		"EventSourceArn": a.EventSourceArn,
		"State":          a.State,
		"EventCount":     a.EventCount,
		"SizeBytes":      a.SizeBytes,
	}
	if a.StateReason != "" {
		out["StateReason"] = a.StateReason
	}
	if a.RetentionDays != nil {
		out["RetentionDays"] = *a.RetentionDays
	}
	if a.CreationTime != 0 {
		out["CreationTime"] = a.CreationTime
	}
	return out
}

func handleEBDeleteArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArchiveName string `json:"ArchiveName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ebArchives.Delete(req.ArchiveName) {
		AWSError(w, "ResourceNotFoundException", "Archive does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

// handleEBUpdateArchive mutates an existing archive's Description, EventPattern,
// RetentionDays, and KmsKeyIdentifier. Only the fields supplied in the request
// are changed (each is a pointer so the absent/null case leaves the stored value
// intact), matching UpdateArchive. A malformed EventPattern is rejected with
// InvalidEventPatternException, the same way CreateArchive validates patterns.
func handleEBUpdateArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArchiveName      string  `json:"ArchiveName"`
		Description      *string `json:"Description"`
		EventPattern     *string `json:"EventPattern"`
		RetentionDays    *int32  `json:"RetentionDays"`
		KmsKeyIdentifier *string `json:"KmsKeyIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ArchiveName == "" {
		AWSError(w, "ValidationException", "ArchiveName is required", http.StatusBadRequest)
		return
	}
	if req.EventPattern != nil && *req.EventPattern != "" {
		if err := ebValidateEventPattern(*req.EventPattern); err != nil {
			AWSError(w, "InvalidEventPatternException", "Event pattern is not valid. Reason: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	archive, ok := ebArchives.Get(req.ArchiveName)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Archive does not exist", http.StatusNotFound)
		return
	}
	if req.Description != nil {
		archive.Description = *req.Description
	}
	if req.EventPattern != nil {
		archive.EventPattern = *req.EventPattern
	}
	if req.RetentionDays != nil {
		archive.RetentionDays = req.RetentionDays
	}
	if req.KmsKeyIdentifier != nil {
		archive.KmsKeyIdentifier = *req.KmsKeyIdentifier
	}
	ebArchives.Put(archive.ArchiveName, archive)
	out := map[string]any{
		"ArchiveArn":   archive.ArchiveArn,
		"CreationTime": archive.CreationTime,
		"State":        archive.State,
	}
	if archive.StateReason != "" {
		out["StateReason"] = archive.StateReason
	}
	writeEBJSON(w, http.StatusOK, out)
}

func handleEBStartReplay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReplayName     string          `json:"ReplayName"`
		Description    string          `json:"Description"`
		EventSourceArn string          `json:"EventSourceArn"`
		EventStartTime json.RawMessage `json:"EventStartTime"`
		EventEndTime   json.RawMessage `json:"EventEndTime"`
		Destination    map[string]any  `json:"Destination"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ReplayName == "" || req.EventSourceArn == "" || req.Destination == nil {
		AWSError(w, "ValidationException", "ReplayName, EventSourceArn, and Destination are required", http.StatusBadRequest)
		return
	}
	archive, ok := ebArchiveByARN(req.EventSourceArn)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Archive does not exist", http.StatusNotFound)
		return
	}
	startTime, err := ebParseJSONTime(req.EventStartTime)
	if err != nil {
		AWSError(w, "ValidationException", "EventStartTime is invalid", http.StatusBadRequest)
		return
	}
	endTime, err := ebParseJSONTime(req.EventEndTime)
	if err != nil {
		AWSError(w, "ValidationException", "EventEndTime is invalid", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	replay := EBReplay{
		ReplayName:            req.ReplayName,
		ReplayArn:             ebReplayArn(req.ReplayName),
		Description:           req.Description,
		EventSourceArn:        req.EventSourceArn,
		EventStartTime:        startTime,
		EventEndTime:          endTime,
		ReplayStartTime:       now,
		ReplayEndTime:         now,
		EventLastReplayedTime: endTime,
		State:                 "COMPLETED",
		Destination:           req.Destination,
	}
	if arn, _ := req.Destination["Arn"].(string); arn != "" {
		replayArchivedEvents(archive, arn)
	}
	ebReplays.Put(replay.ReplayName, replay)
	writeEBJSON(w, http.StatusOK, map[string]any{
		"ReplayArn":       replay.ReplayArn,
		"ReplayStartTime": replay.ReplayStartTime,
		"State":           replay.State,
	})
}

func handleEBDescribeReplay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReplayName string `json:"ReplayName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	replay, ok := ebReplays.Get(req.ReplayName)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Replay does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, replay)
}

func handleEBListReplays(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventSourceArn string `json:"EventSourceArn"`
		NamePrefix     string `json:"NamePrefix"`
		Limit          int    `json:"Limit"`
		NextToken      string `json:"NextToken"`
	}
	_ = sim.ReadJSON(r, &req)
	replays := make([]EBReplay, 0)
	for _, replay := range ebReplays.List() {
		if req.EventSourceArn != "" && replay.EventSourceArn != req.EventSourceArn {
			continue
		}
		if req.NamePrefix != "" && !strings.HasPrefix(replay.ReplayName, req.NamePrefix) {
			continue
		}
		replays = append(replays, replay)
	}
	sort.Slice(replays, func(i, j int) bool { return replays[i].ReplayName < replays[j].ReplayName })
	page, next := awsPageExplicit(replays, req.NextToken, req.Limit)
	summaries := make([]map[string]any, 0, len(page))
	for _, replay := range page {
		summaries = append(summaries, ebReplaySummary(replay))
	}
	out := map[string]any{"Replays": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	writeEBJSON(w, http.StatusOK, out)
}

// handleEBCancelReplay transitions a replay out of an in-progress state. Per the
// real CancelReplay state machine, a replay that is STARTING or RUNNING moves to
// CANCELLED (real AWS reports the intermediate CANCELLING state while it drains;
// the sim's in-process replay leaves nothing to drain, so it lands directly on
// CANCELLED). A replay already in a terminal state (COMPLETED, CANCELLED, FAILED)
// cannot be cancelled and yields IllegalStatusException, exactly as real AWS.
func handleEBCancelReplay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReplayName string `json:"ReplayName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	replay, ok := ebReplays.Get(req.ReplayName)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "Replay does not exist", http.StatusNotFound)
		return
	}
	switch replay.State {
	case "STARTING", "RUNNING":
		replay.State = "CANCELLED"
		replay.StateReason = "Replay was cancelled."
		replay.ReplayEndTime = time.Now().Unix()
		ebReplays.Put(replay.ReplayName, replay)
		writeEBJSON(w, http.StatusOK, map[string]any{
			"ReplayArn":   replay.ReplayArn,
			"State":       replay.State,
			"StateReason": replay.StateReason,
		})
	default:
		AWSError(w, "IllegalStatusException",
			fmt.Sprintf("Replay %s is not in a state from which it can be cancelled.", replay.ReplayName),
			http.StatusBadRequest)
	}
}

// ebReplaySummary projects a replay onto the list-shape Replay members;
// ReplayArn / Description are describe-only and must not appear in
// ListReplays entries.
func ebReplaySummary(rp EBReplay) map[string]any {
	out := map[string]any{
		"ReplayName":     rp.ReplayName,
		"EventSourceArn": rp.EventSourceArn,
		"State":          rp.State,
	}
	if rp.StateReason != "" {
		out["StateReason"] = rp.StateReason
	}
	for k, v := range map[string]int64{
		"EventStartTime":        rp.EventStartTime,
		"EventEndTime":          rp.EventEndTime,
		"EventLastReplayedTime": rp.EventLastReplayedTime,
		"ReplayStartTime":       rp.ReplayStartTime,
		"ReplayEndTime":         rp.ReplayEndTime,
	} {
		if v != 0 {
			out[k] = v
		}
	}
	return out
}

func ebParseJSONTime(raw json.RawMessage) (int64, error) {
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		return int64(seconds), nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return 0, err
	}
	return parsed.Unix(), nil
}

func ebBusByARN(arn string) (EBEventBus, bool) {
	for _, bus := range ebBuses.List() {
		if bus.Arn == arn {
			return bus, true
		}
	}
	if arn == ebBusArn("default") {
		return ebGetBus("default")
	}
	return EBEventBus{}, false
}

func ebArchiveByARN(arn string) (EBArchive, bool) {
	for _, archive := range ebArchives.List() {
		if archive.ArchiveArn == arn {
			return archive, true
		}
	}
	return EBArchive{}, false
}

func replayArchivedEvents(archive EBArchive, destinationBusArn string) {
	bus, ok := ebBusByARN(destinationBusArn)
	if !ok {
		return
	}
	for _, event := range archive.ArchivedEvents {
		deliverEBEvent(bus.Name, event.Source, event.DetailType, event.Detail, event.ID)
	}
}

func handleEBTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string                        `json:"ResourceARN"`
		Tags        []struct{ Key, Value string } `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if ebBusByARNUpdate(req.ResourceARN, func(bus *EBEventBus) {
		if bus.Tags == nil {
			bus.Tags = map[string]string{}
		}
		for _, tag := range req.Tags {
			bus.Tags[tag.Key] = tag.Value
		}
	}) {
		writeEBJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if !ebRuleByARNUpdate(req.ResourceARN, func(rule *EBRule) {
		if rule.Tags == nil {
			rule.Tags = map[string]string{}
		}
		for _, tag := range req.Tags {
			rule.Tags[tag.Key] = tag.Value
		}
	}) {
		AWSError(w, "ResourceNotFoundException", "Resource does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if ebBusByARNUpdate(req.ResourceARN, func(bus *EBEventBus) {
		for _, key := range req.TagKeys {
			delete(bus.Tags, key)
		}
	}) {
		writeEBJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if !ebRuleByARNUpdate(req.ResourceARN, func(rule *EBRule) {
		for _, key := range req.TagKeys {
			delete(rule.Tags, key)
		}
	}) {
		AWSError(w, "ResourceNotFoundException", "Resource does not exist", http.StatusNotFound)
		return
	}
	writeEBJSON(w, http.StatusOK, map[string]any{})
}

func handleEBListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	for _, bus := range ebBuses.List() {
		if bus.Arn != req.ResourceARN {
			continue
		}
		writeEBJSON(w, http.StatusOK, map[string]any{"Tags": ebTagsList(bus.Tags)})
		return
	}
	for _, rule := range ebRules.List() {
		if rule.Arn != req.ResourceARN {
			continue
		}
		writeEBJSON(w, http.StatusOK, map[string]any{"Tags": ebTagsList(rule.Tags)})
		return
	}
	AWSError(w, "ResourceNotFoundException", "Resource does not exist", http.StatusNotFound)
}

func ebTagsList(tags map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for k, v := range tags {
		out = append(out, map[string]string{"Key": k, "Value": v})
	}
	return out
}

func ebRuleByARNUpdate(arn string, fn func(*EBRule)) bool {
	for _, rule := range ebRules.List() {
		if rule.Arn == arn {
			return ebRules.Update(ebRuleKey(rule.EventBusName, rule.Name), fn)
		}
	}
	return false
}

func ebBusByARNUpdate(arn string, fn func(*EBEventBus)) bool {
	for _, bus := range ebBuses.List() {
		if bus.Arn == arn {
			fn(&bus)
			ebPutBus(bus)
			return true
		}
	}
	return false
}
