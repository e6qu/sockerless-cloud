package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests pin the wire/store split for resources whose persisted
// rows carry data their Discovery schemas don't define (sockerless
// wiring, membership served by a sibling method, materialized query
// results). Two invariants per resource:
//
//  1. The wire shape never carries the store-only member.
//  2. A persisted row (the exact JSON sim.Store has always written,
//     including rows written before the wire/store split) unmarshals
//     with the store-only member intact, so SIM_* persistence restores
//     it across a simulator restart.

func assertNoJSONKeys(t *testing.T, v any, keys ...string) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range keys {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("wire shape leaks %q: %s", key, data)
		}
	}
}

func TestManagedZoneWireHasNoDockerNetworkName(t *testing.T) {
	stored := storedManagedZone{
		ManagedZone:       ManagedZone{Name: "z", DNSName: "test.local.", ID: "42", Visibility: "private"},
		DockerNetworkName: "sim-42",
	}
	assertNoJSONKeys(t, stored.ManagedZone, "dockerNetworkName")

	// Persisted row round-trip (same flattened shape as the wire struct
	// plus the wiring member).
	row, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	if !strings.Contains(string(row), `"dockerNetworkName":"sim-42"`) {
		t.Fatalf("stored row must persist dockerNetworkName: %s", row)
	}
	var got storedManagedZone
	if err := json.Unmarshal(row, &got); err != nil {
		t.Fatalf("unmarshal stored: %v", err)
	}
	if got.DockerNetworkName != "sim-42" || got.Name != "z" {
		t.Errorf("restart recovery lost data: %+v", got)
	}
}

func TestStoredFunctionRoundTrip(t *testing.T) {
	// Decode the request shape a client sends — also the exact row shape
	// sim.Store persists — and confirm wire() recovers it verbatim with
	// no sim-only fields ever appearing on the wire.
	row := `{
		"name": "projects/p/locations/l/functions/f",
		"state": "ACTIVE",
		"serviceConfig": {
			"uri": "http://sim/v2-functions-invoke/f",
			"service": "projects/p/locations/l/services/f",
			"environmentVariables": {"FOO": "bar"}
		}
	}`
	var fn storedFunction
	if err := json.Unmarshal([]byte(row), &fn); err != nil {
		t.Fatalf("unmarshal stored: %v", err)
	}
	if fn.ServiceConfig == nil || fn.ServiceConfig.EnvironmentVariables["FOO"] != "bar" ||
		fn.ServiceConfig.Service != "projects/p/locations/l/services/f" {
		t.Fatalf("decode lost serviceConfig members: %+v", fn.ServiceConfig)
	}

	assertNoJSONKeys(t, fn.wire(), "simImage", "simCommand", "simArchitecture")

	// The persisted row round-trips the real serviceConfig members.
	persisted, err := json.Marshal(fn)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	for _, key := range []string{`"FOO":"bar"`, `"service":"projects/p/locations/l/services/f"`} {
		if !strings.Contains(string(persisted), key) {
			t.Errorf("stored row must persist %s: %s", key, persisted)
		}
	}
}

func TestInstanceGroupWireHasNoInstances(t *testing.T) {
	stored := storedComputeInstanceGroup{
		ComputeInstanceGroup: ComputeInstanceGroup{Name: "g", SelfLink: "projects/p/zones/z/instanceGroups/g"},
		Instances: []ComputeInstanceGroupInstance{
			{Instance: "projects/p/zones/z/instances/i1"},
			{Instance: "projects/p/zones/z/instances/i2"},
		},
	}
	wire := wireInstanceGroup(stored)
	assertNoJSONKeys(t, wire, "instances")
	if wire.Size != 2 {
		t.Errorf("size = %d, want 2 (output-only member computed from membership)", wire.Size)
	}

	row, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	var got storedComputeInstanceGroup
	if err := json.Unmarshal(row, &got); err != nil {
		t.Fatalf("unmarshal stored: %v", err)
	}
	if len(got.Instances) != 2 {
		t.Errorf("restart recovery lost membership: %+v", got.Instances)
	}
}

func TestBQJobWireHasNoQueryOrResult(t *testing.T) {
	stored := storedBQJob{
		BQJob: BQJob{
			Kind: "bigquery#job",
			ID:   "p:job_1",
		},
		Result: BQQueryResult{TotalRows: "1", JobComplete: true},
	}
	assertNoJSONKeys(t, stored.BQJob, "result", "query")

	row, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	var got storedBQJob
	if err := json.Unmarshal(row, &got); err != nil {
		t.Fatalf("unmarshal stored: %v", err)
	}
	if got.Result.TotalRows != "1" {
		t.Errorf("restart recovery lost result: %+v", got.Result)
	}
}

func TestBuildTriggerWireHasNoLocation(t *testing.T) {
	trigger := normalizeBuildTrigger("p", "us-central1", BuildTrigger{Name: "t"})
	assertNoJSONKeys(t, trigger, "location")
	if trigger.ResourceName != "projects/p/locations/us-central1/triggers/"+trigger.ID {
		t.Errorf("resourceName must encode the location: %s", trigger.ResourceName)
	}
}

func TestRemoteRepositoryConfigSanitized(t *testing.T) {
	in := map[string]any{
		"dockerRepository":           map[string]any{"publicRepository": "DOCKER_HUB"},
		"description":                "remote",
		"enableIngestionAttestation": false,
	}
	out := sanitizeRemoteRepositoryConfig(in)
	if _, ok := out["enableIngestionAttestation"]; ok {
		t.Errorf("non-schema member must be dropped at intake: %v", out)
	}
	if _, ok := out["dockerRepository"]; !ok {
		t.Errorf("schema members must be kept: %v", out)
	}
	if out["description"] != "remote" {
		t.Errorf("schema members must be kept: %v", out)
	}
}
