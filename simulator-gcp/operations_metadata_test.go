package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/e6qu/sockerless-cloud/sim"
)

// discoverySchemaProperties reads the property names of one schema in a
// vendored Discovery document.
func discoverySchemaProperties(t *testing.T, document, schema string) map[string]bool {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "specs", "cloud-api", "gcp", document+".discovery.json.gz"))
	if err != nil {
		t.Fatalf("open %s: %v", document, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("read %s: %v", document, err)
	}
	var doc struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.NewDecoder(zr).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", document, err)
	}
	s, ok := doc.Schemas[schema]
	if !ok {
		t.Fatalf("%s declares no schema %s", document, schema)
	}
	props := map[string]bool{}
	for name := range s.Properties {
		props[name] = true
	}
	return props
}

// Each service's operations carry the metadata message its own API declares —
// named by the @type the service uses and holding only members that message
// defines — never a type no API declares.
func TestGCPOperationsCarryTheirServicesMetadata(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	do := func(host, method, path, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return out
	}

	for _, tc := range []struct {
		name, host, method, path, body string
		// operation reads the operation out of the method's response.
		operation        func(map[string]any) map[string]any
		wantType         string
		document, schema string
		wantMembers      []string
	}{
		{
			name: "Memorystore for Redis instance", host: "redis.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/instances?instanceId=meta-redis", body: `{"tier":"BASIC","memorySizeGb":1}`,
			wantType: "type.googleapis.com/google.cloud.redis.v1.OperationMetadata",
			document: "redis-v1", schema: "GoogleCloudRedisV1OperationMetadata", wantMembers: []string{"createTime", "endTime", "target"},
		},
		{
			name: "Memorystore for Redis Cluster", host: "redis.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/clusters?clusterId=meta-cluster", body: `{"shardCount":1}`,
			wantType: "type.googleapis.com/google.cloud.redis.cluster.v1.OperationMetadata",
			document: "redis-v1", schema: "OperationMetadata", wantMembers: []string{"createTime", "endTime", "target"},
		},
		{
			name: "Artifact Registry repository", host: "artifactregistry.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/repositories?repositoryId=meta-repo", body: `{"format":"DOCKER"}`,
			wantType: "type.googleapis.com/google.devtools.artifactregistry.v1.OperationMetadata",
			document: "artifactregistry-v1", schema: "OperationMetadata",
		},
		{
			name: "Artifact Registry apt import", host: "artifactregistry.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/repositories/meta-repo/aptArtifacts:import", body: `{}`,
			wantType: "type.googleapis.com/google.devtools.artifactregistry.v1.ImportAptArtifactsMetadata",
			document: "artifactregistry-v1", schema: "ImportAptArtifactsMetadata",
		},
		{
			name: "Artifact Registry Go module upload", host: "artifactregistry.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/repositories/meta-repo/goModules:create", body: `{}`,
			operation: func(m map[string]any) map[string]any { op, _ := m["operation"].(map[string]any); return op },
			wantType:  "type.googleapis.com/google.devtools.artifactregistry.v1.UploadGoModuleMetadata",
			document:  "artifactregistry-v1", schema: "UploadGoModuleMetadata",
		},
		{
			name: "Serverless VPC Access connector", host: "vpcaccess.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/connectors?connectorId=meta-conn", body: `{"network":"default","ipCidrRange":"10.8.0.0/28"}`,
			wantType: "type.googleapis.com/google.cloud.vpcaccess.v1.OperationMetadata",
			document: "vpcaccess-v1", schema: "OperationMetadata", wantMembers: []string{"method", "createTime", "endTime", "target"},
		},
		{
			name: "Service Usage enable", host: "serviceusage.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/services/pubsub.googleapis.com:enable", body: `{}`,
			wantType: "type.googleapis.com/google.api.serviceusage.v1.OperationMetadata",
			document: "serviceusage-v1", schema: "OperationMetadata", wantMembers: []string{"resourceNames"},
		},
		{
			name: "Cloud KMS KeyHandle", host: "cloudkms.googleapis.com", method: http.MethodPost,
			path: "/v1/projects/p/locations/us-central1/keyHandles?keyHandleId=meta-kh", body: `{"resourceTypeSelector":"storage.googleapis.com/Bucket"}`,
			wantType: "type.googleapis.com/google.cloud.kms.v1.CreateKeyHandleMetadata",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := do(tc.host, tc.method, tc.path, tc.body)
			if tc.operation != nil {
				op = tc.operation(op)
			}
			metadata, _ := op["metadata"].(map[string]any)
			if metadata["@type"] != tc.wantType {
				t.Fatalf("metadata = %v, want @type %s", metadata, tc.wantType)
			}
			for _, member := range tc.wantMembers {
				if _, ok := metadata[member]; !ok {
					t.Errorf("metadata %v lacks %s", metadata, member)
				}
			}
			if tc.document == "" {
				return
			}
			declared := discoverySchemaProperties(t, tc.document, tc.schema)
			for member := range metadata {
				if member != "@type" && !declared[member] {
					t.Errorf("metadata member %s is not a member of %s.%s", member, tc.document, tc.schema)
				}
			}
		})
	}
}

// A Cloud KMS operation a REST method started reads back over the gRPC
// google.longrunning.Operations service with its declared metadata and result.
func TestGCPKeyHandleOperationReadsBackOverGRPC(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	req := httptest.NewRequest(http.MethodPost,
		"http://cloudkms.googleapis.com/v1/projects/p/locations/us-central1/keyHandles?keyHandleId=grpc-kh",
		strings.NewReader(`{"resourceTypeSelector":"storage.googleapis.com/Bucket"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create key handle: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Name == "" {
		t.Fatalf("create key handle answered %s", rec.Body)
	}

	op, err := (&grpcOperationsService{}).GetOperation(context.Background(), &longrunningpb.GetOperationRequest{Name: created.Name})
	if err != nil {
		t.Fatalf("operations.get over gRPC: %v", err)
	}
	if err := op.GetMetadata().UnmarshalTo(&kmspb.CreateKeyHandleMetadata{}); err != nil {
		t.Fatalf("metadata %s is not CreateKeyHandleMetadata: %v", op.GetMetadata().GetTypeUrl(), err)
	}
	handle := &kmspb.KeyHandle{}
	if err := op.GetResponse().UnmarshalTo(handle); err != nil {
		t.Fatalf("response is not a KeyHandle: %v", err)
	}
	if handle.GetName() != "projects/p/locations/us-central1/keyHandles/grpc-kh" ||
		handle.GetResourceTypeSelector() != "storage.googleapis.com/Bucket" {
		t.Fatalf("KeyHandle = %v", handle)
	}
}
