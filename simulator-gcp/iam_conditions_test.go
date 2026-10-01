package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGCPConditionHolds(t *testing.T) {
	service := gcpIAMResourceNamed("projects/p/locations/us-central1/services/api")
	bucket := gcpIAMResource{Name: "projects/_/buckets/b", Type: "storage.googleapis.com/Bucket", Service: "storage.googleapis.com"}
	unknown := gcpIAMResourceNamed("projects/p/widgets/w")
	at := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)

	cases := []struct {
		name       string
		expression string
		resource   gcpIAMResource
		want       bool
	}{
		{"time before", `request.time < timestamp("2027-01-01T00:00:00Z")`, service, true},
		{"time after", `request.time < timestamp("2001-01-01T00:00:00Z")`, service, false},
		{"hour of day", `request.time.getHours("UTC") == 15`, service, true},
		{"name prefix", `resource.name.startsWith("projects/p/locations/us-central1/services/")`, service, true},
		{"other name", `resource.name == "projects/p/locations/us-central1/services/other"`, service, false},
		{"service type", `resource.type == "run.googleapis.com/Service" && resource.service == "run.googleapis.com"`, service, true},
		{"bucket type", `resource.type == "storage.googleapis.com/Bucket"`, bucket, true},
		{"type the resource does not supply", `resource.type == "run.googleapis.com/Service"`, unknown, false},
		{"function IAM does not offer", `resource.matchTag("123/env", "prod")`, service, false},
		{"not a boolean", `resource.name`, service, false},
		{"does not compile", `resource.name ==`, service, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(gcpIAMCondition{Expression: c.expression})
			if err != nil {
				t.Fatal(err)
			}
			if got := gcpConditionHolds(raw, c.resource, at); got != c.want {
				t.Fatalf("gcpConditionHolds(%q) = %v, want %v", c.expression, got, c.want)
			}
		})
	}

	if !gcpConditionHolds(nil, service, at) {
		t.Fatal("a binding without a condition must apply")
	}
	if gcpConditionHolds(json.RawMessage(`{"title":"no expression"}`), service, at) {
		t.Fatal("a condition without an expression must not apply")
	}
}

func TestGCPPermissionsHeldByHonoursConditions(t *testing.T) {
	resource := gcpIAMResourceNamed("projects/p/locations/us-central1/services/api")
	at := time.Now()
	policy := IAMPolicy{Bindings: []IAMBinding{
		{Role: "roles/run.invoker", Members: []string{"serviceAccount:a@p.iam.gserviceaccount.com"},
			Condition: json.RawMessage(`{"expression":"request.time < timestamp(\"2001-01-01T00:00:00Z\")"}`)},
		{Role: "roles/run.invoker", Members: []string{"serviceAccount:b@p.iam.gserviceaccount.com"},
			Condition: json.RawMessage(`{"expression":"resource.type == \"run.googleapis.com/Service\""}`)},
		{Role: "roles/run.invoker", Members: []string{"allAuthenticatedUsers"},
			Condition: json.RawMessage(`{"expression":"resource.name.endsWith(\"/other\")"}`)},
	}}
	invoke := []string{"run.routes.invoke"}
	if held := gcpPermissionsHeldBy(policy, "serviceAccount:a@p.iam.gserviceaccount.com", invoke, resource, at); len(held) != 0 {
		t.Fatalf("an expired conditional binding granted %v", held)
	}
	if held := gcpPermissionsHeldBy(policy, "serviceAccount:b@p.iam.gserviceaccount.com", invoke, resource, at); len(held) != 1 {
		t.Fatal("a conditional binding whose condition holds must grant its role")
	}
	if held := gcpPermissionsHeldBy(policy, "", invoke, resource, at); len(held) != 0 {
		t.Fatal("allAuthenticatedUsers must not cover a caller without a credential")
	}
}
