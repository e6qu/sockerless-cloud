package gcp_sdk_test

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/simready"

	functions "cloud.google.com/go/functions/apiv2"
	"cloud.google.com/go/functions/apiv2/functionspb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// startQuotaIsolatedSim spawns a dedicated simulator with the regional CPU
// quota set to budget, so quota rejections cannot reach other tests; the
// shared simulator runs with quota off.
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
	startErr := simready.Start(cmd, os.Stderr)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if startErr != nil {
		log.Fatalf("isolated sim did not start listening: %v", startErr)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// TestSDK_RegionalCPUQuota_RejectsCloudRunDeployOverBudget: with
// SIM_GCP_CPU_QUOTA_PER_REGION at 2 vCPU and 1-vCPU services, the third deploy
// fails with "Quota exceeded for total allowable CPU per project per region".
func TestSDK_RegionalCPUQuota_RejectsCloudRunDeployOverBudget(t *testing.T) {
	t.Parallel()
	url := startQuotaIsolatedSim(t, 2)
	ensureProjectAt(t, url, "quota-test")

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

	for i, id := range []string{"svc-a", "svc-b"} {
		op, err := client.CreateService(ctx, mkSvc(id))
		require.NoError(t, err, "service %d (%s) CreateService should succeed under budget", i, id)
		_, err = op.Wait(ctx)
		require.NoError(t, err, "service %d (%s) LRO should complete", i, id)
	}

	_, err = client.CreateService(ctx, mkSvc("svc-c"))
	require.Error(t, err, "third deploy should be rejected; over budget=2")
	if !strings.Contains(err.Error(), "Quota exceeded for total allowable CPU per project per region") {
		t.Errorf("expected canonical quota message in error; got %q", err.Error())
	}
}

// TestSDK_RegionalCPUQuota_PartitionedByRegion: Cloud Run quotas are per
// project and region, so exhausting us-central1 leaves europe-west1 free.
func TestSDK_RegionalCPUQuota_PartitionedByRegion(t *testing.T) {
	t.Parallel()
	url := startQuotaIsolatedSim(t, 1)
	ensureProjectAt(t, url, "quota-qt")

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

	op, err := client.CreateService(ctx, mkSvc("projects/quota-qt/locations/us-central1", "svc-a"))
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)

	_, err = client.CreateService(ctx, mkSvc("projects/quota-qt/locations/us-central1", "svc-b"))
	require.Error(t, err, "us-central1 second deploy should be rejected")
	assert.Contains(t, err.Error(), "Quota exceeded for total allowable CPU per project per region",
		"the rejection must be the quota rejection, not some other failure")

	// europe-west1 has its own bucket; deploy there must succeed.
	op, err = client.CreateService(ctx, mkSvc("projects/quota-qt/locations/europe-west1", "svc-c"))
	require.NoError(t, err, "europe-west1 deploy should not be blocked by us-central1's exhaustion")
	_, err = op.Wait(ctx)
	require.NoError(t, err)
}

// TestSDK_RegionalCPUQuota_RejectsCloudFunctionsDeploy: CreateFunction creates
// the function's Cloud Run service, whose container CPU limit is the function's
// AvailableCpu, so the same regional quota applies.
func TestSDK_RegionalCPUQuota_RejectsCloudFunctionsDeploy(t *testing.T) {
	t.Parallel()
	url := startQuotaIsolatedSim(t, 1)
	ensureProjectAt(t, url, "quota-qt")

	fnClient, err := functions.NewFunctionRESTClient(ctx,
		option.WithEndpoint(url),
		option.WithTokenSource(simTokenSourceFor(url)),
	)
	require.NoError(t, err)
	defer fnClient.Close()

	mkFn := func(id string) *functionspb.CreateFunctionRequest {
		return &functionspb.CreateFunctionRequest{
			Parent:     "projects/quota-qt/locations/us-central1",
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

	op, err := fnClient.CreateFunction(ctx, mkFn("fn-a"))
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)

	_, err = fnClient.CreateFunction(ctx, mkFn("fn-b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Quota exceeded for total allowable CPU per project per region",
		"gcf CreateFunction over-quota must surface the canonical message; gcf backend matches on it")
}
