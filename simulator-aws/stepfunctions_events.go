package main

import (
	"encoding/json"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Step Functions sends a standard workflow execution's every status change to
// the default event bus as a "Step Functions Execution Status Change" event
// (sfn-eventbridge-integration.md.gz); express workflows send none.

func sfnStatusEvents(store sim.Store[SFNExecution]) sim.Store[SFNExecution] {
	return sfnStatusEventStore{Store: store}
}

type sfnStatusEventStore struct {
	sim.Store[SFNExecution]
}

func (s sfnStatusEventStore) status(id string) string {
	prior, _ := s.Get(id)
	return prior.Status
}

func (s sfnStatusEventStore) announce(id, before string) {
	after, ok := s.Get(id)
	if ok && after.Status != before {
		sfnEmitStatusChange(after)
	}
}

func (s sfnStatusEventStore) Put(id string, item SFNExecution) {
	before := s.status(id)
	s.Store.Put(id, item)
	s.announce(id, before)
}

func (s sfnStatusEventStore) Update(id string, fn func(*SFNExecution)) bool {
	before := s.status(id)
	updated := s.Store.Update(id, fn)
	if updated {
		s.announce(id, before)
	}
	return updated
}

func (s sfnStatusEventStore) Upsert(id string, fn func(*SFNExecution)) {
	before := s.status(id)
	s.Store.Upsert(id, fn)
	s.announce(id, before)
}

func sfnEmitStatusChange(exec SFNExecution) {
	machine, ok := sfnStateMachines.Get(exec.StateMachineArn)
	if ok && machine.Type == "EXPRESS" {
		return
	}
	millis := func(seconds float64) int64 { return int64(seconds * 1000) }
	detail := map[string]any{
		"executionArn":           exec.ExecutionArn,
		"stateMachineArn":        exec.StateMachineArn,
		"name":                   exec.Name,
		"status":                 exec.Status,
		"startDate":              millis(exec.StartDate),
		"stopDate":               nil,
		"input":                  exec.Input,
		"inputDetails":           map[string]any{"included": true},
		"output":                 nil,
		"outputDetails":          nil,
		"stateMachineVersionArn": nil,
		"stateMachineAliasArn":   nil,
		"redriveCount":           exec.RedriveCount,
		"redriveDate":            nil,
		"error":                  nil,
		"cause":                  nil,
	}
	if exec.StopDate != nil {
		detail["stopDate"] = millis(*exec.StopDate)
	}
	if exec.Status == "SUCCEEDED" {
		detail["output"] = exec.Output
		detail["outputDetails"] = map[string]any{"included": true}
	}
	if exec.StateMachineVersionArn != "" {
		detail["stateMachineVersionArn"] = exec.StateMachineVersionArn
	}
	if exec.StateMachineAliasArn != "" {
		detail["stateMachineAliasArn"] = exec.StateMachineAliasArn
	}
	if exec.RedriveDate != nil {
		detail["redriveDate"] = millis(*exec.RedriveDate)
	}
	if exec.Error != "" {
		detail["error"] = exec.Error
	}
	if exec.Cause != "" {
		detail["cause"] = exec.Cause
	}
	switch exec.Status {
	case "FAILED", "TIMED_OUT", "ABORTED":
		detail["redriveStatus"] = "REDRIVABLE"
	default:
		detail["redriveStatus"] = "NOT_REDRIVABLE"
		detail["redriveStatusReason"] = "Execution is " + exec.Status + " and cannot be redriven"
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return
	}
	ebPutServiceEvent("aws.states", "Step Functions Execution Status Change", string(body), []string{exec.ExecutionArn})
}
