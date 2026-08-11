package gcp_sdk_test

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	functions "cloud.google.com/go/functions/apiv2"
	"cloud.google.com/go/functions/apiv2/functionspb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// startQuotaIsolatedSim spawns a dedicated simulator instance with the
// regional CPU quota set to `budget`. The default sim (helpers_test.go)
// has quota disabled — this isolated instance lets quota tests run
// alongside everything else without bleeding rejections into unrelated
// SDK tests.
//
// Returns the test-scoped baseURL; t.Cleanup tears down the process.
func startQuotaIsolatedSim(t *testing.T, budget float64) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	grpcPort := ln2.Addr().(*net.TCPAddr).Port
	ln.Close()
	ln2.Close()

	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", grpcPort),
		fmt.Sprintf("SIM_GCP_CPU_QUOTA_PER_REGION=%g", budget),
		"SIM_GCP_CPU_QUOTA_WINDOW=1m",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHealth(url + "/health"); err != nil {
		_ = cmd.Process.Kill()
		log.Fatalf("isolated sim did not become healthy: %v", err)
	}
	return url
}

// TestSDK_RegionalCPUQuota_RejectsCloudRunDeployOverBudget exercises
// the live-cloud quota error path through the same Cloud Run v2 REST
// client the cloudrun backend uses. With the simulator's
// SIM_GCP_CPU_QUOTA_PER_REGION set to 2 vCPU-min and the test
// deploying 1 vCPU services, the third deploy must be rejected with
// the canonical "Quota exceeded for total allowable CPU per project
// per region" error.
func TestSDK_RegionalCPUQuota_RejectsCloudRunDeployOverBudget(t *testing.T) {
	url := startQuotaIsolatedSim(t, 2)

	client, err := run.NewServicesRESTClient(ctx,
		option.WithEndpoint(url),
		option.WithTokenSource(simTokenSourceFor(url)),
	)
	require.NoError(t, err)
	defer client.Close()

	mkSvc := func(id string) *runpb.CreateServiceRequest {
		return &runpb.CreateServiceRequest{
			Parent:    "projects/quota-test/locations/us-central1",
			ServiceId: id,
			Service: &runpb.Service{
				Template: &runpb.RevisionTemplate{
					Containers: []*runpb.Container{{
						Image: "gcr.io/quota-test/hello",
						Resources: &runpb.ResourceRequirements{
							Limits: map[string]string{"cpu": "1", "memory": "512Mi"},
						},
					}},
				},
			},
		}
	}

	// First two deploys debit 1 + 1 = 2 vCPU-min — at budget, both succeed.
	for i, id := range []string{"svc-a", "svc-b"} {
		op, err := client.CreateService(ctx, mkSvc(id))
		require.NoError(t, err, "service %d (%s) CreateService should succeed under budget", i, id)
		_, err = op.Wait(ctx)
		require.NoError(t, err, "service %d (%s) LRO should complete", i, id)
	}

	// Third deploy would push to 3 vCPU-min, over the budget=2 threshold.
	// Expect the canonical InvalidArgument with the quota message.
	_, err = client.CreateService(ctx, mkSvc("svc-c"))
	require.Error(t, err, "third deploy should be rejected; over budget=2")
	if !strings.Contains(err.Error(), "Quota exceeded for total allowable CPU per project per region") {
		t.Errorf("expected canonical quota message in error; got %q", err.Error())
	}
}

// TestSDK_RegionalCPUQuota_PartitionedByRegion verifies that
// per-region budgets are independent — exhausting us-central1 does
// not block europe-west1 deploys. Real Cloud Run regional quotas are
// per-(project, region) so simulator parity matters for tests that
// exercise multi-region deployment patterns.
func TestSDK_RegionalCPUQuota_PartitionedByRegion(t *testing.T) {
	url := startQuotaIsolatedSim(t, 1)

	client, err := run.NewServicesRESTClient(ctx,
		option.WithEndpoint(url),
		option.WithTokenSource(simTokenSourceFor(url)),
	)
	require.NoError(t, err)
	defer client.Close()

	mkSvc := func(parent, id string) *runpb.CreateServiceRequest {
		return &runpb.CreateServiceRequest{
			Parent:    parent,
			ServiceId: id,
			Service: &runpb.Service{
				Template: &runpb.RevisionTemplate{
					Containers: []*runpb.Container{{
						Image:     "gcr.io/quota-test/hello",
						Resources: &runpb.ResourceRequirements{Limits: map[string]string{"cpu": "1"}},
					}},
				},
			},
		}
	}

	op, err := client.CreateService(ctx, mkSvc("projects/qt/locations/us-central1", "svc-a"))
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)

	_, err = client.CreateService(ctx, mkSvc("projects/qt/locations/us-central1", "svc-b"))
	require.Error(t, err, "us-central1 second deploy should be rejected")

	// europe-west1 has its own bucket; deploy there must succeed.
	op, err = client.CreateService(ctx, mkSvc("projects/qt/locations/europe-west1", "svc-c"))
	require.NoError(t, err, "europe-west1 deploy should not be blocked by us-central1's exhaustion")
	_, err = op.Wait(ctx)
	require.NoError(t, err)
}

// TestSDK_RegionalCPUQuota_RejectsCloudFunctionsDeploy walks the
// gcf path: CreateFunction triggers the simulator to also create the
// underlying Cloud Run service (cloudfunctions.go:registerCloudFunctions
// stamps a backing ServiceV2). The function's AvailableCpu is the
// container CPU limit on that backing service — so the same regional
// quota check fires, just one layer deeper.
func TestSDK_RegionalCPUQuota_RejectsCloudFunctionsDeploy(t *testing.T) {
	url := startQuotaIsolatedSim(t, 1)

	fnClient, err := functions.NewFunctionRESTClient(ctx,
		option.WithEndpoint(url),
		option.WithTokenSource(simTokenSourceFor(url)),
	)
	require.NoError(t, err)
	defer fnClient.Close()

	mkFn := func(id string) *functionspb.CreateFunctionRequest {
		return &functionspb.CreateFunctionRequest{
			Parent:     "projects/qt/locations/us-central1",
			FunctionId: id,
			Function: &functionspb.Function{
				Environment: functionspb.Environment_GEN_2,
				BuildConfig: &functionspb.BuildConfig{
					Runtime:    "go124",
					EntryPoint: "Stub",
				},
				ServiceConfig: &functionspb.ServiceConfig{
					AvailableCpu:    "1",
					AvailableMemory: "512Mi",
				},
			},
		}
	}

	// First function: 1 vCPU; at budget. Succeeds.
	op, err := fnClient.CreateFunction(ctx, mkFn("fn-a"))
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)

	// Second function: would push us over budget=1. Rejected with the
	// canonical quota message — same wire shape the gcf backend matches on.
	_, err = fnClient.CreateFunction(ctx, mkFn("fn-b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Quota exceeded for total allowable CPU per project per region",
		"gcf CreateFunction over-quota must surface the canonical message; gcf backend matches on it")
}
