package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the wire/store split for resources whose stored rows carry
// data their Discovery schemas do not define (membership a sibling method
// serves, materialized results): the wire shape never carries the store-only
// member, and a stored row, including one written before the split, decodes
// with it intact, so persistence restores it across a restart.

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
	// Marshalling one instance only proves the member was empty on that
	// instance. The invariant is about the type: a member added to the wire
	// struct leaks on the first request that populates it, long before a test
	// happens to fill it in. So the type's own JSON tag set is checked too.
	assertTypeHasNoJSONKeys(t, reflect.TypeOf(v), keys...)
}

// assertTypeHasNoJSONKeys walks every JSON member reachable from a type —
// through pointers, slices, arrays, map values and embedded structs — and fails
// if any of the named keys is declared anywhere in it. Unlike marshalling an
// instance, this cannot be satisfied by a zero value.
func assertTypeHasNoJSONKeys(t *testing.T, typ reflect.Type, keys ...string) {
	t.Helper()
	forbidden := map[string]bool{}
	for _, key := range keys {
		forbidden[key] = true
	}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ != nil && (typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice ||
			typ.Kind() == reflect.Array || typ.Kind() == reflect.Map) {
			typ = typ.Elem()
		}
		if typ == nil || typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if forbidden[name] {
				t.Errorf("wire type %s declares the store-only member %q", typ, name)
			}
			walk(field.Type)
		}
	}
	walk(typ)
}

func TestStoredFunctionRoundTrip(t *testing.T) {
	// Decode the request shape a client sends, which is also the stored row, and
	// check wire() recovers it with no store-only member on the wire.
	row := `{
		"name": "projects/p/locations/l/functions/f",
		"state": "ACTIVE",
		"serviceConfig": {
			"uri": "https://f-abcdefghij-l.a.run.app",
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

	// wire() narrows the stored serviceConfig, which shadows the wire Function's,
	// so compare the emitted member set exactly: a lost serviceConfig, a dropped
	// member and an invented one all fail.
	wireJSON, err := json.Marshal(fn.wire())
	if err != nil {
		t.Fatalf("marshal wire function: %v", err)
	}
	var emitted struct {
		ServiceConfig map[string]any `json:"serviceConfig"`
	}
	if err := json.Unmarshal(wireJSON, &emitted); err != nil {
		t.Fatalf("unmarshal wire function: %v", err)
	}
	wantMembers := map[string]any{
		"uri":                  "https://f-abcdefghij-l.a.run.app",
		"service":              "projects/p/locations/l/services/f",
		"environmentVariables": map[string]any{"FOO": "bar"},
	}
	if !reflect.DeepEqual(emitted.ServiceConfig, wantMembers) {
		t.Errorf("wire serviceConfig = %#v, want %#v", emitted.ServiceConfig, wantMembers)
	}

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
