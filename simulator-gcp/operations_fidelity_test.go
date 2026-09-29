package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/apigateway/apiv1/apigatewaypb"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"cloud.google.com/go/redis/apiv1/redispb"
	"cloud.google.com/go/redis/cluster/apiv1/clusterpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func buildOperationsTestSimulator(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	return srv
}

// gcpHostCall sends one request to the simulator as the named service host and
// returns the status and decoded body.
func gcpHostCall(t *testing.T, srv *sim.Server, host, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s answered %d with a body that is not JSON: %s", method, path, rec.Code, rec.Body)
		}
	}
	return rec.Code, out
}

func gcpHostOK(t *testing.T, srv *sim.Server, host, method, path, body string) map[string]any {
	t.Helper()
	code, out := gcpHostCall(t, srv, host, method, path, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s → %d: %v", method, path, code, out)
	}
	return out
}

// grpcReadOperation reads a stored operation over the gRPC Operations service
// into the response and metadata messages its method declares.
func grpcReadOperation(t *testing.T, name string, response, metadata proto.Message) {
	t.Helper()
	op, err := (&grpcOperationsService{}).GetOperation(context.Background(), &longrunningpb.GetOperationRequest{Name: name})
	if err != nil {
		t.Fatalf("operations.get %s over gRPC: %v", name, err)
	}
	if err := op.GetResponse().UnmarshalTo(response); err != nil {
		t.Fatalf("operation %s response is %s: %v", name, op.GetResponse().GetTypeUrl(), err)
	}
	if err := op.GetMetadata().UnmarshalTo(metadata); err != nil {
		t.Fatalf("operation %s metadata is %s: %v", name, op.GetMetadata().GetTypeUrl(), err)
	}
}

// Every standard OperationMetadata names the method its operation ran and the
// resource that method acted on — a delete, whose response is Empty, too.
func TestGCPOperationMetadataNamesVerbAndTarget(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const redisHost = "redis.googleapis.com"
	const base = "/v1/projects/p/locations/us-central1"
	instance := "projects/p/locations/us-central1/instances/verb-cache"
	cluster := "projects/p/locations/us-central1/clusters/verb-cluster"
	api := "projects/p/locations/global/apis/verb-api"

	for _, tc := range []struct {
		name, host, method, path, body string
		verb, target, response         string
	}{
		{"instance create", redisHost, http.MethodPost, base + "/instances?instanceId=verb-cache", `{"tier":"BASIC","memorySizeGb":1}`, "create", instance, "google.cloud.redis.v1.Instance"},
		{"instance update", redisHost, http.MethodPatch, "/v1/" + instance + "?updateMask=memorySizeGb", `{"memorySizeGb":2}`, "update", instance, "google.cloud.redis.v1.Instance"},
		{"instance delete", redisHost, http.MethodDelete, "/v1/" + instance, ``, "delete", instance, "google.protobuf.Empty"},
		{"cluster create", redisHost, http.MethodPost, base + "/clusters?clusterId=verb-cluster", `{"shardCount":1}`, "create", cluster, "google.cloud.redis.cluster.v1.Cluster"},
		{"cluster backup", redisHost, http.MethodPost, "/v1/" + cluster + ":backup", `{"backupId":"b1"}`, "backup", cluster, "google.cloud.redis.cluster.v1.Cluster"},
		{"cluster delete", redisHost, http.MethodDelete, "/v1/" + cluster, ``, "delete", cluster, "google.protobuf.Empty"},
		{"api create", "apigateway.googleapis.com", http.MethodPost, "/v1/projects/p/locations/global/apis?apiId=verb-api", `{}`, "create", api, "google.cloud.apigateway.v1.Api"},
		{"api delete", "apigateway.googleapis.com", http.MethodDelete, "/v1/" + api, ``, "delete", api, "google.protobuf.Empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := gcpHostOK(t, srv, tc.host, tc.method, tc.path, tc.body)
			metadata, _ := op["metadata"].(map[string]any)
			if metadata["verb"] != tc.verb || metadata["target"] != tc.target {
				t.Fatalf("metadata = %v, want verb %q and target %q", metadata, tc.verb, tc.target)
			}
			response, _ := op["response"].(map[string]any)
			if response["@type"] != "type.googleapis.com/"+tc.response {
				t.Fatalf("response = %v, want %s", response, tc.response)
			}
		})
	}
}

// Memorystore for Redis, Memorystore for Redis Cluster, Artifact Registry and
// API Gateway record their operations through REST, and the gRPC Operations
// service reads them back as the messages their protos declare.
func TestGRPCOperationsReadRESTServiceOperations(t *testing.T) {
	srv := buildOperationsTestSimulator(t)

	op := gcpHostOK(t, srv, "redis.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/us-central1/instances?instanceId=grpc-cache", `{"tier":"BASIC","memorySizeGb":1}`)
	instance, instanceMeta := &redispb.Instance{}, &redispb.OperationMetadata{}
	grpcReadOperation(t, op["name"].(string), instance, instanceMeta)
	if instance.GetName() != "projects/p/locations/us-central1/instances/grpc-cache" || instance.GetMemorySizeGb() != 1 || instanceMeta.GetVerb() != "create" {
		t.Fatalf("instance %v, metadata %v", instance, instanceMeta)
	}

	op = gcpHostOK(t, srv, "redis.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/us-central1/clusters?clusterId=grpc-cluster", `{"shardCount":2}`)
	cluster, clusterMeta := &clusterpb.Cluster{}, &clusterpb.OperationMetadata{}
	grpcReadOperation(t, op["name"].(string), cluster, clusterMeta)
	if cluster.GetShardCount() != 2 || clusterMeta.GetTarget() != cluster.GetName() {
		t.Fatalf("cluster %v, metadata %v", cluster, clusterMeta)
	}
	gcpHostOK(t, srv, "redis.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/us-central1/clusters/grpc-cluster:backup", `{"backupId":"b1"}`)
	op = gcpHostOK(t, srv, "redis.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/us-central1/backupCollections/grpc-cluster/backups/b1:export", `{"gcsBucket":"any"}`)
	backup := &clusterpb.Backup{}
	grpcReadOperation(t, op["name"].(string), backup, &clusterpb.OperationMetadata{})
	if backup.GetCluster() != cluster.GetName() {
		t.Fatalf("backup %v does not name its cluster", backup)
	}

	op = gcpHostOK(t, srv, "artifactregistry.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/us-central1/repositories?repositoryId=grpc-repo", `{"format":"DOCKER"}`)
	repo := &artifactregistrypb.Repository{}
	grpcReadOperation(t, op["name"].(string), repo, &artifactregistrypb.OperationMetadata{})
	if repo.GetFormat() != artifactregistrypb.Repository_DOCKER {
		t.Fatalf("repository %v", repo)
	}

	op = gcpHostOK(t, srv, "apigateway.googleapis.com", http.MethodPost,
		"/v1/projects/p/locations/global/apis?apiId=grpc-api", `{"displayName":"over grpc"}`)
	api, apiMeta := &apigatewaypb.Api{}, &apigatewaypb.OperationMetadata{}
	grpcReadOperation(t, op["name"].(string), api, apiMeta)
	if api.GetDisplayName() != "over grpc" || apiMeta.GetTarget() != api.GetName() {
		t.Fatalf("api %v, metadata %v", api, apiMeta)
	}
}

// Artifact Registry's deletes answer google.protobuf.Empty, as each delete
// method's operation_info declares, and batchDelete reports the versions it
// could not delete in its BatchDeleteVersionsMetadata.
func TestArtifactRegistryDeletesAnswerEmpty(t *testing.T) {
	srv, baseURL := arTestServer(t)
	const host = "artifactregistry.googleapis.com"
	arTestCreateRepository(t, baseURL, "test-project", "us-central1", "del-repo")
	repo := "projects/test-project/locations/us-central1/repositories/del-repo"

	// A Docker push is what creates a package, its versions and its tags.
	now := time.Now()
	authorization := "Bearer " + arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now, now.Add(time.Hour))
	push := func(image, ref, manifest string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, baseURL+"/v2/test-project/del-repo/"+image+"/manifests/"+ref, strings.NewReader(manifest))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", authorization)
		req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("push %s:%s answered %d", image, ref, resp.StatusCode)
		}
		return resp.Header.Get("Docker-Content-Digest")
	}
	first := push("team/app", "v1", `{"schemaVersion":2,"layers":[{"digest":"sha256:01"}]}`)
	second := push("team/app", "v2", `{"schemaVersion":2,"layers":[{"digest":"sha256:02"}]}`)
	third := push("lib", "v1", `{"schemaVersion":2,"layers":[{"digest":"sha256:03"}]}`)

	pkg := repo + "/packages/team%2Fapp"
	listed := gcpHostOK(t, srv, host, http.MethodGet, "/v1/"+pkg+"/versions", ``)
	if versions, _ := listed["versions"].([]any); len(versions) != 2 {
		t.Fatalf("the pushed package lists %v", listed)
	}
	if tag := gcpHostOK(t, srv, host, http.MethodGet, "/v1/"+pkg+"/tags/v2", ``); tag["version"] != pkg+"/versions/"+second {
		t.Fatalf("tag v2 = %v, want version %s", tag, second)
	}

	for _, path := range []string{"/v1/" + pkg + "/versions/" + first, "/v1/" + pkg} {
		op := gcpHostOK(t, srv, host, http.MethodDelete, path, ``)
		grpcReadOperation(t, op["name"].(string), &emptypb.Empty{}, &artifactregistrypb.OperationMetadata{})
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodGet, "/v1/"+pkg+"/versions/"+second, ``); code != http.StatusNotFound {
		t.Fatalf("deleting the package left its versions behind (%d)", code)
	}

	lib := repo + "/packages/lib"
	op := gcpHostOK(t, srv, host, http.MethodPost, "/v1/"+lib+"/versions:batchDelete",
		`{"names":["`+lib+`/versions/`+third+`","`+lib+`/versions/sha256:missing"]}`)
	metadata := &artifactregistrypb.BatchDeleteVersionsMetadata{}
	grpcReadOperation(t, op["name"].(string), &emptypb.Empty{}, metadata)
	if got := metadata.GetFailedVersions(); len(got) != 1 || got[0] != lib+"/versions/sha256:missing" {
		t.Fatalf("failedVersions = %v", got)
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodGet, "/v1/"+lib+"/versions/"+third, ``); code != http.StatusNotFound {
		t.Fatalf("batchDelete left the version it deleted (%d)", code)
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodPost, "/v1/"+lib+"/versions:batchDelete",
		`{"names":["`+pkg+`/versions/`+second+`"]}`); code != http.StatusBadRequest {
		t.Fatalf("a batchDelete naming another package's version answered %d", code)
	}

	op = gcpHostOK(t, srv, host, http.MethodDelete, "/v1/"+repo, ``)
	grpcReadOperation(t, op["name"].(string), &emptypb.Empty{}, &artifactregistrypb.OperationMetadata{})
}

// Artifact Registry prewarm and export and Cloud Build's Bitbucket Server
// batchCreate each mint an operation of their own in the regional operations
// collection, which operations.get then reads.
func TestCustomMethodOperationsAreRecorded(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const arHost = "artifactregistry.googleapis.com"
	repo := "projects/p/locations/us-central1/repositories/rec-repo"
	gcpHostOK(t, srv, arHost, http.MethodPost, "/v1/projects/p/locations/us-central1/repositories?repositoryId=rec-repo", `{"format":"DOCKER"}`)
	version := repo + "/packages/app/versions/sha256:0123"
	arVersions.Put(version, ARVersion{Name: version})
	arRegistry.Blobs.Put("rec-repo@sha256:0123", sim.OCIBlob{Digest: "sha256:0123", ContentType: "application/octet-stream", Data: []byte("layer bytes")})
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=p", `{"name":"export-bucket"}`)

	readBack := func(op map[string]any) map[string]any {
		t.Helper()
		name, _ := op["name"].(string)
		if !strings.HasPrefix(name, "projects/p/locations/us-central1/operations/") {
			t.Fatalf("operation name %q is not in the regional operations collection", name)
		}
		fetched := gcpHostOK(t, srv, arHost, http.MethodGet, "/v1/"+name, ``)
		if fetched["name"] != name || fetched["done"] != true {
			t.Fatalf("operations.get answered %v", fetched)
		}
		return fetched
	}

	prewarm := readBack(gcpHostOK(t, srv, arHost, http.MethodPost, "/v1/"+repo+":prewarmArtifact", `{"version":"`+version+`"}`))
	response, _ := prewarm["response"].(map[string]any)
	if artifact, _ := response["prewarmedArtifact"].(map[string]any); artifact["location"] != "us-central1" {
		t.Fatalf("prewarm response = %v", response)
	}

	export := readBack(gcpHostOK(t, srv, arHost, http.MethodPost, "/v1/"+repo+":exportArtifact",
		`{"sourceVersion":"`+version+`","gcsPath":"export-bucket/out"}`))
	metadata, _ := export["metadata"].(map[string]any)
	if files, _ := metadata["exportedFiles"].([]any); len(files) != 1 {
		t.Fatalf("export metadata = %v", metadata)
	}
	if data, err := GCSObjectBytes("export-bucket", "out/sha256:0123"); err != nil || string(data) != "layer bytes" {
		t.Fatalf("exported object = %q, %v", data, err)
	}

	const cbHost = "cloudbuild.googleapis.com"
	gcpHostOK(t, srv, cbHost, http.MethodPost, "/v1/projects/p/locations/us-central1/bitbucketServerConfigs?bitbucketServerConfigId=bb",
		`{"hostUri":"https://bitbucket.example.com","username":"builder"}`)
	batch := readBack(gcpHostOK(t, srv, cbHost, http.MethodPost,
		"/v1/projects/p/locations/us-central1/bitbucketServerConfigs/bb/connectedRepositories:batchCreate",
		`{"requests":[{"parent":"projects/p/locations/us-central1/bitbucketServerConfigs/bb","bitbucketServerConnectedRepository":{"repo":{"projectKey":"TEAM","repoSlug":"app"}}}]}`))
	metadata, _ = batch["metadata"].(map[string]any)
	if metadata["config"] != "projects/p/locations/us-central1/bitbucketServerConfigs/bb" {
		t.Fatalf("batchCreate metadata = %v", metadata)
	}

	del := readBack(gcpHostOK(t, srv, cbHost, http.MethodDelete, "/v1/projects/p/locations/us-central1/bitbucketServerConfigs/bb", ``))
	metadata, _ = del["metadata"].(map[string]any)
	if metadata["@type"] != "type.googleapis.com/google.devtools.cloudbuild.v1.DeleteBitbucketServerConfigOperationMetadata" {
		t.Fatalf("delete metadata = %v", metadata)
	}
	if code, _ := gcpHostCall(t, srv, cbHost, http.MethodDelete, "/v1/projects/p/locations/us-central1/bitbucketServerConfigs/bb", ``); code != http.StatusNotFound {
		t.Fatalf("deleting a config twice answered %d", code)
	}
}

// IAM's workload identity pool operations carry the empty
// WorkloadIdentityPoolOperationMetadata; the other IAM operations carry the
// standard google.iam.v1.OperationMetadata, whose members are the iam v1
// document's own.
func TestIAMOperationMetadataTypes(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "iam.googleapis.com"
	const pools = "/v1/projects/p/locations/global/workloadIdentityPools"

	op := gcpHostOK(t, srv, host, http.MethodPost, pools+"?workloadIdentityPoolId=meta-pool", `{}`)
	metadata, _ := op["metadata"].(map[string]any)
	if len(metadata) != 1 || metadata["@type"] != "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolOperationMetadata" {
		t.Fatalf("pool metadata = %v", metadata)
	}

	op = gcpHostOK(t, srv, host, http.MethodPost, pools+"/meta-pool/providers?workloadIdentityPoolProviderId=meta-provider",
		`{"oidc":{"issuerUri":"https://issuer.example.com"}}`)
	metadata, _ = op["metadata"].(map[string]any)
	provider := "projects/p/locations/global/workloadIdentityPools/meta-pool/providers/meta-provider"
	if metadata["@type"] != "type.googleapis.com/google.iam.v1.OperationMetadata" || metadata["verb"] != "create" || metadata["target"] != provider {
		t.Fatalf("provider metadata = %v", metadata)
	}
	declared := discoverySchemaProperties(t, "iam-v1", "OperationMetadata")
	for member := range metadata {
		if member != "@type" && !declared[member] {
			t.Errorf("metadata member %s is not a member of iam-v1.OperationMetadata", member)
		}
	}

	op = gcpHostOK(t, srv, host, http.MethodDelete, pools+"/meta-pool/providers/meta-provider", ``)
	if metadata, _ = op["metadata"].(map[string]any); metadata["verb"] != "delete" || metadata["target"] != provider {
		t.Fatalf("provider delete metadata = %v", metadata)
	}
}

// Cloud Resource Manager v2's folder create and move carry the FolderOperation
// the v2 protos' operation_info declares.
func TestCRMV2FolderOperationMetadata(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "cloudresourcemanager.googleapis.com"
	op := gcpHostOK(t, srv, host, http.MethodPost, "/v2/folders?parent=organizations/123", `{"displayName":"moved"}`)
	metadata, _ := op["metadata"].(map[string]any)
	if metadata["@type"] != "type.googleapis.com/google.cloud.resourcemanager.v2.FolderOperation" ||
		metadata["operationType"] != "CREATE" || metadata["displayName"] != "moved" || metadata["destinationParent"] != "organizations/123" {
		t.Fatalf("create metadata = %v", metadata)
	}
	response, _ := op["response"].(map[string]any)
	folder, _ := response["name"].(string)
	op = gcpHostOK(t, srv, host, http.MethodPost, "/v2/"+folder+":move", `{"destinationParent":"organizations/456"}`)
	metadata, _ = op["metadata"].(map[string]any)
	if metadata["operationType"] != "MOVE" || metadata["sourceParent"] != "organizations/123" || metadata["destinationParent"] != "organizations/456" {
		t.Fatalf("move metadata = %v", metadata)
	}
	declared := discoverySchemaProperties(t, "cloudresourcemanager-v2", "FolderOperation")
	for member := range metadata {
		if member != "@type" && !declared[member] {
			t.Errorf("metadata member %s is not a member of FolderOperation", member)
		}
	}
}
