package gcp_sdk_test

import (
	"net/http"
	"testing"

	cloudquotas "cloud.google.com/go/cloudquotas/apiv1"
	"cloud.google.com/go/cloudquotas/apiv1/cloudquotaspb"
	functions "cloud.google.com/go/functions/apiv2"
	"cloud.google.com/go/functions/apiv2/functionspb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const cpuQuotaMessage = "Quota exceeded for total allowable CPU per project per region"

func newQuotasClient(t *testing.T) *cloudquotas.Client {
	t.Helper()
	client, err := cloudquotas.NewRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// setRunCPUQuota sets a project's Cloud Run CPU quota in a region through the
// quota preference id, creating it when it does not exist.
func setRunCPUQuota(t *testing.T, client *cloudquotas.Client, project, id, region string, milliCPU int64, ignore ...cloudquotaspb.QuotaSafetyCheck) *cloudquotaspb.QuotaPreference {
	t.Helper()
	pref, err := client.UpdateQuotaPreference(ctx, &cloudquotaspb.UpdateQuotaPreferenceRequest{
		QuotaPreference: &cloudquotaspb.QuotaPreference{
			Name:         "projects/" + project + "/locations/global/quotaPreferences/" + id,
			Service:      "run.googleapis.com",
			QuotaId:      "CpuAllocPerProjectRegion",
			Dimensions:   map[string]string{"region": region},
			QuotaConfig:  &cloudquotaspb.QuotaConfig{PreferredValue: milliCPU},
			ContactEmail: "quota-owner@example.com",
		},
		AllowMissing:       true,
		IgnoreSafetyChecks: ignore,
	})
	require.NoError(t, err)
	require.Equal(t, milliCPU, pref.GetQuotaConfig().GetGrantedValue().GetValue())
	return pref
}

func oneCPUService() *runpb.Service {
	tmpl := instanceTemplate(nil, 0)
	tmpl.Containers[0].Resources = &runpb.ResourceRequirements{Limits: map[string]string{"cpu": "1", "memory": "512Mi"}}
	return &runpb.Service{Template: tmpl}
}

func deployOneCPU(client *run.ServicesClient, parent, id string) (*runpb.Service, error) {
	op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{Parent: parent, ServiceId: id, Service: oneCPUService()})
	if err != nil {
		return nil, err
	}
	return op.Wait(ctx)
}

// runInstance sends the service one request, which starts the instance that
// serves it and keeps holding its CPU.
func runInstance(t *testing.T, client *run.ServicesClient, svc *runpb.Service) {
	t.Helper()
	grantOnService(t, client, svc.Name, "roles/run.invoker", "serviceAccount:"+sdkInvokerEmail(t), nil)
	code, _, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, code, body)
}

func requireCPUQuotaRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), cpuQuotaMessage)
}

// A project's running Cloud Run instances hold their CPU against the regional
// quota a Cloud Quotas preference sets: a deploy whose first instance does not
// fit is refused, and fits again once the instances holding the CPU go away
// or the preference rises.
func TestSDK_RegionalCPUQuota_CloudRunDeployHonoursQuotaPreference(t *testing.T) {
	t.Parallel()
	const project = "sdk-run-cpu-quota"
	ensureProject(t, project)
	quotas := newQuotasClient(t)
	setRunCPUQuota(t, quotas, project, "run-cpu-us-central1", "us-central1", 1500,
		cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_PERCENTAGE_TOO_HIGH)

	client := newServicesClient(t)
	parent := "projects/" + project + "/locations/us-central1"
	svcA, err := deployOneCPU(client, parent, "quota-a")
	require.NoError(t, err)
	runInstance(t, client, svcA)

	_, err = deployOneCPU(client, parent, "quota-b")
	requireCPUQuotaRefusal(t, err)

	other, err := deployOneCPU(client, "projects/"+project+"/locations/europe-west1", "quota-eu")
	require.NoError(t, err, "europe-west1 keeps its own quota")
	cleanupService(t, client, other.Name)

	del, err := client.DeleteService(ctx, &runpb.DeleteServiceRequest{Name: svcA.Name})
	require.NoError(t, err)
	_, err = del.Wait(ctx)
	require.NoError(t, err)

	svcB, err := deployOneCPU(client, parent, "quota-b")
	require.NoError(t, err, "the deleted service's instance no longer holds CPU")
	cleanupService(t, client, svcB.Name)
	runInstance(t, client, svcB)

	_, err = deployOneCPU(client, parent, "quota-c")
	requireCPUQuotaRefusal(t, err)

	setRunCPUQuota(t, quotas, project, "run-cpu-us-central1", "us-central1", 2500)
	svcC, err := deployOneCPU(client, parent, "quota-c")
	require.NoError(t, err, "the raised preference has room for a second instance")
	cleanupService(t, client, svcC.Name)
}

// CreateFunction deploys the function's Cloud Run service, whose container
// CPU is the function's availableCpu, under the same regional quota.
func TestSDK_RegionalCPUQuota_CloudFunctionsDeployHonoursQuotaPreference(t *testing.T) {
	t.Parallel()
	const project = "sdk-gcf-cpu-quota"
	ensureProject(t, project)
	quotas := newQuotasClient(t)
	setRunCPUQuota(t, quotas, project, "run-cpu-us-central1", "us-central1", 500,
		cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_PERCENTAGE_TOO_HIGH)

	fnClient, err := functions.NewFunctionRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	defer fnClient.Close()
	create := func() error {
		op, err := fnClient.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
			Parent:     "projects/" + project + "/locations/us-central1",
			FunctionId: "quota-fn",
			Function: &functionspb.Function{
				Environment:   functionspb.Environment_GEN_2,
				BuildConfig:   &functionspb.BuildConfig{Runtime: "go124", EntryPoint: "Handle"},
				ServiceConfig: &functionspb.ServiceConfig{AvailableCpu: "1", AvailableMemory: "512Mi"},
			},
		})
		if err != nil {
			return err
		}
		_, err = op.Wait(ctx)
		return err
	}
	requireCPUQuotaRefusal(t, create())

	setRunCPUQuota(t, quotas, project, "run-cpu-us-central1", "us-central1", 1000)
	require.NoError(t, create())
}
