package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGCPConditionHolds(t *testing.T) {
	key := gcpIAMResourceNamed("projects/p/locations/us-central1/keyRings/r/cryptoKeys/k")
	instance := gcpIAMResourceNamed("compute/projects/p/zones/us-central1-a/instances/vm")
	table := gcpIAMResourceNamed("bigquery:projects/p/datasets/d/tables/t")
	bucket := gcpIAMResource{Name: "projects/_/buckets/b", Type: "storage.googleapis.com/Bucket", Service: "storage.googleapis.com"}
	// Cloud Run services are not in the resource attribute reference, so
	// IAM supplies them no resource attributes.
	service := gcpIAMResourceNamed("projects/p/locations/us-central1/services/api")
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
		{"key type", `resource.type == "cloudkms.googleapis.com/CryptoKey" && resource.service == "cloudkms.googleapis.com"`, key, true},
		{"key name", `resource.name.startsWith("projects/p/locations/us-central1/keyRings/r/")`, key, true},
		{"other key", `resource.name == "projects/p/locations/us-central1/keyRings/r/cryptoKeys/other"`, key, false},
		{"instance under its compute prefix", `resource.type == "compute.googleapis.com/Instance" && resource.name == "projects/p/zones/us-central1-a/instances/vm"`, instance, true},
		{"table under its bigquery prefix", `resource.type == "bigquery.googleapis.com/Table" && resource.name.endsWith("/tables/t")`, table, true},
		{"bucket type", `resource.type == "storage.googleapis.com/Bucket"`, bucket, true},
		{"type a service outside the reference does not supply", `resource.type == "run.googleapis.com/Service"`, service, false},
		{"name a service outside the reference does not supply", `resource.name.startsWith("projects/p/")`, service, false},
		{"tag no binding carries", `resource.matchTag("123456789012/env", "prod")`, key, false},
		{"tag key no binding carries", `resource.hasTagKeyId("tagKeys/123")`, key, false},
		{"tag function on a non-string", `resource.matchTag(1, "prod")`, key, false},
		{"not a boolean", `resource.name`, key, false},
		{"does not compile", `resource.name ==`, key, false},
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

func TestGCPIAMResourceNamed(t *testing.T) {
	for key, want := range map[string]gcpIAMResource{
		"compute/projects/p/global/images/img":                               {Name: "projects/p/global/images/img", Type: "compute.googleapis.com/Image", Service: "compute.googleapis.com", project: "p"},
		"compute/projects/p/regions/us-east1/disks/d":                        {Name: "projects/p/regions/us-east1/disks/d", Type: "compute.googleapis.com/Disk", Service: "compute.googleapis.com", project: "p"},
		"projects/p/instances/i/databases/db":                                {Name: "projects/p/instances/i/databases/db", Type: "spanner.googleapis.com/Database", Service: "spanner.googleapis.com", project: "p"},
		"projects/p/instances/i/clusters/c":                                  {Name: "projects/p/instances/i/clusters/c", Type: "bigtableadmin.googleapis.com/Cluster", Service: "bigtableadmin.googleapis.com", project: "p"},
		"project/p":                                                          {Type: "cloudresourcemanager.googleapis.com/Project", Service: "cloudresourcemanager.googleapis.com", project: "p"},
		"organization/123":                                                   {Name: "organizations/123", Service: "cloudresourcemanager.googleapis.com"},
		"serviceAccount/sa@p.iam.gserviceaccount.com":                        {project: "p"},
		"dnsManagedZone/p/zone":                                              {project: "p"},
		"projects/p/locations/us-central1/gateways/gw":                       {project: "p"},
		"projects/p/topics/t":                                                {project: "p"},
		"billingAccounts/000000-AAAAAA-BBBBBB":                               {},
		"projects/p/locations/us-central1/services/api":                      {project: "p"},
		"projects/p/locations/global/buckets/_Default":                       {Name: "projects/p/locations/global/buckets/_Default", Type: "logging.googleapis.com/LogBucket", Service: "logging.googleapis.com", project: "p"},
		"projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1": {Name: "projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1", Type: "cloudkms.googleapis.com/CryptoKeyVersion", Service: "cloudkms.googleapis.com", project: "p"},
	} {
		if got := gcpIAMResourceNamed(key); got != want {
			t.Errorf("gcpIAMResourceNamed(%q) = %+v, want %+v", key, got, want)
		}
	}
}

func TestGCPPermissionsHeldByHonoursConditions(t *testing.T) {
	resource := gcpIAMResourceNamed("projects/p/locations/us-central1/keyRings/r")
	at := time.Now()
	policy := IAMPolicy{Bindings: []IAMBinding{
		{Role: "roles/run.invoker", Members: []string{"serviceAccount:a@p.iam.gserviceaccount.com"},
			Condition: json.RawMessage(`{"expression":"request.time < timestamp(\"2001-01-01T00:00:00Z\")"}`)},
		{Role: "roles/run.invoker", Members: []string{"serviceAccount:b@p.iam.gserviceaccount.com"},
			Condition: json.RawMessage(`{"expression":"resource.type == \"cloudkms.googleapis.com/KeyRing\""}`)},
		{Role: "roles/run.invoker", Members: []string{"allAuthenticatedUsers"},
			Condition: json.RawMessage(`{"expression":"resource.name.endsWith(\"/other\")"}`)},
	}}
	get := []string{"run.routes.invoke"}
	if held := gcpPermissionsHeldBy(policy, "serviceAccount:a@p.iam.gserviceaccount.com", get, resource, at); len(held) != 0 {
		t.Fatalf("an expired conditional binding granted %v", held)
	}
	if held := gcpPermissionsHeldBy(policy, "serviceAccount:b@p.iam.gserviceaccount.com", get, resource, at); len(held) != 1 {
		t.Fatal("a conditional binding whose condition holds must grant its role")
	}
	if held := gcpPermissionsHeldBy(policy, "", get, resource, at); len(held) != 0 {
		t.Fatal("allAuthenticatedUsers must not cover a caller without a credential")
	}
}
