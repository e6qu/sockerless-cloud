package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	runv1 "google.golang.org/api/run/v1"
)

// v2 Cloud Run Services routes contract. The cloudrun backend uses
// run.NewServicesRESTClient (v2 REST) when Config.UseService=true.
// These tests pin the v2 contract using the same client the backend
// uses.

func newServicesClient(t *testing.T) *run.ServicesClient {
	t.Helper()
	client, err := run.NewServicesRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// cleanupService deletes a Cloud Run service when the test ends and waits for
// the delete to complete.
func cleanupService(t *testing.T, client *run.ServicesClient, name string) {
	t.Helper()
	t.Cleanup(func() {
		op, err := client.DeleteService(ctx, &runpb.DeleteServiceRequest{Name: name})
		require.NoError(t, err, "delete service %s", name)
		_, err = op.Wait(ctx)
		require.NoError(t, err, "delete service %s", name)
	})
}

func TestSDK_CloudRunV2Services_ListPaginationAndWireShape(t *testing.T) {
	client := newServicesClient(t)
	parent := "projects/test-project/locations/us-central1"

	for _, id := range []string{uniqueName("v2-svc-page-a"), uniqueName("v2-svc-page-b")} {
		op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
			Parent:    parent,
			ServiceId: id,
			Service: &runpb.Service{
				Template: &runpb.RevisionTemplate{
					Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + id}},
				},
			},
		})
		require.NoError(t, err)
		_, err = op.Wait(ctx)
		require.NoError(t, err)
		cleanupService(t, client, parent+"/services/"+id)
	}

	resp, err := http.Get(baseURL + "/v2/" + parent + "/services?pageSize=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var first struct {
		Services      []map[string]any `json:"services"`
		NextPageToken string           `json:"nextPageToken"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&first))
	require.Len(t, first.Services, 1)
	require.NotEmpty(t, first.NextPageToken)

	resp2, err := http.Get(baseURL + "/v2/" + parent + "/services?pageSize=1&pageToken=" + first.NextPageToken)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var second struct {
		Services []map[string]any `json:"services"`
	}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&second))
	require.Len(t, second.Services, 1)
	require.NotEqual(t, first.Services[0]["name"], second.Services[0]["name"])

	resp3, err := http.Get(baseURL + "/v2/projects/test-project/locations/europe-west1/services")
	require.NoError(t, err)
	defer resp3.Body.Close()
	var empty map[string]any
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&empty))
	require.IsType(t, []any{}, empty["services"], "empty service list must serialize as [] not null")
}

func TestSDK_CloudRunServiceV1V2AreOneResource(t *testing.T) {
	const (
		project  = "cross-version-project"
		location = "us-central1"
		fromV2   = "created-through-v2"
		fromV1   = "created-through-v1"
	)
	v2 := newServicesClient(t)
	parent := "projects/" + project + "/locations/" + location
	createV2, err := v2.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent: parent, ServiceId: fromV2,
		Service: &runpb.Service{Template: &runpb.RevisionTemplate{
			Containers: []*runpb.Container{{Image: "gcr.io/cross/version"}},
		}},
	})
	require.NoError(t, err)
	_, err = createV2.Wait(ctx)
	require.NoError(t, err)

	v1, err := runv1.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	v1List, err := v1.Namespaces.Services.List("namespaces/" + project).Do()
	require.NoError(t, err)
	require.Len(t, v1List.Items, 1)
	assert.Equal(t, fromV2, v1List.Items[0].Metadata.Name)

	_, err = v1.Namespaces.Services.Create("namespaces/"+project, &runv1.Service{
		ApiVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata:   &runv1.ObjectMeta{Name: fromV1},
		Spec: &runv1.ServiceSpec{Template: &runv1.RevisionTemplate{
			Spec: &runv1.RevisionSpec{Containers: []*runv1.Container{{Image: "gcr.io/cross/reverse"}}},
		}},
	}).Do()
	require.NoError(t, err)
	gotV2, err := v2.GetService(ctx, &runpb.GetServiceRequest{Name: parent + "/services/" + fromV1})
	require.NoError(t, err)
	assert.Equal(t, parent+"/services/"+fromV1, gotV2.Name)
	require.Len(t, gotV2.Template.Containers, 1)
	assert.Equal(t, "gcr.io/cross/reverse", gotV2.Template.Containers[0].Image)

	deleteV2, err := v2.DeleteService(ctx, &runpb.DeleteServiceRequest{Name: parent + "/services/" + fromV2})
	require.NoError(t, err)
	_, err = deleteV2.Wait(ctx)
	require.NoError(t, err)
	_, err = v1.Namespaces.Services.Get("namespaces/" + project + "/services/" + fromV2).Do()
	require.Error(t, err, "deleting the v2 representation must remove the v1 representation")

	_, err = v1.Namespaces.Services.Delete("namespaces/" + project + "/services/" + fromV1).Do()
	require.NoError(t, err)
	_, err = v2.GetService(ctx, &runpb.GetServiceRequest{Name: parent + "/services/" + fromV1})
	require.Error(t, err, "deleting the v1 representation must remove the v2 representation")
}

func TestSDK_CloudRunV2Service_OperationMetadataAndTimestampShape(t *testing.T) {
	serviceID := uniqueName("v2-svc-lro-shape")
	cleanupService(t, newServicesClient(t), "projects/test-project/locations/us-central1/services/"+serviceID)
	body := strings.NewReader(`{"template":{"containers":[{"image":"gcr.io/test-project/raw"}]}}`)
	resp, err := http.Post(baseURL+"/v2/projects/test-project/locations/us-central1/services?serviceId="+serviceID, "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var op map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&op))
	metadata, ok := op["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "type.googleapis.com/google.cloud.run.v2.Service", metadata["@type"])
	response, ok := op["response"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "type.googleapis.com/google.cloud.run.v2.Service", response["@type"])
	createTime, _ := response["createTime"].(string)
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`, createTime)
}

func TestSDK_CloudRunV2Services_CreateGetListDelete(t *testing.T) {
	client := newServicesClient(t)
	serviceID := uniqueName("v2-svc-roundtrip")

	createOp, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    "projects/test-project/locations/us-central1",
		ServiceId: serviceID,
		Service: &runpb.Service{
			Labels: map[string]string{
				"sockerless_managed":      "true",
				"sockerless_container_id": "abc123",
			},
			Annotations: map[string]string{
				"sockerless_name": "my-svc",
			},
			Ingress: runpb.IngressTraffic_INGRESS_TRAFFIC_INTERNAL_ONLY,
			Template: &runpb.RevisionTemplate{
				Containers: []*runpb.Container{
					{
						Image: "gcr.io/test-project/hello",
						Env: []*runpb.EnvVar{
							{Name: "SOCKERLESS_CALLBACK_URL", Values: &runpb.EnvVar_Value{Value: "ws://host.docker.internal:3375/v1/cloudrun/reverse"}},
							{Name: "SOCKERLESS_CONTAINER_ID", Values: &runpb.EnvVar_Value{Value: "abc123"}},
						},
					},
				},
				Scaling: &runpb.RevisionScaling{
					MinInstanceCount: 1,
					MaxInstanceCount: 1,
				},
				VpcAccess: &runpb.VpcAccess{
					Connector: "projects/test-project/locations/us-central1/connectors/test-connector",
					Egress:    runpb.VpcAccess_ALL_TRAFFIC,
				},
			},
		},
	})
	require.NoError(t, err)

	svc, err := createOp.Wait(ctx)
	require.NoError(t, err, "CreateService LRO must complete")
	require.NotNil(t, svc)
	assert.Contains(t, svc.Name, serviceID)
	assert.NotEmpty(t, svc.Uid)
	assert.Equal(t, int64(1), svc.Generation)
	assert.Equal(t, "true", svc.Labels["sockerless_managed"])
	assert.Equal(t, "my-svc", svc.Annotations["sockerless_name"])
	require.NotNil(t, svc.TerminalCondition)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, svc.TerminalCondition.State)
	assert.NotEmpty(t, svc.LatestReadyRevision, "LatestReadyRevision must be set so backend's serviceContainerState reads 'running'")
	require.NotNil(t, svc.Template)
	require.NotNil(t, svc.Template.VpcAccess)
	assert.Equal(t, "projects/test-project/locations/us-central1/connectors/test-connector", svc.Template.VpcAccess.Connector)
	assert.Equal(t, runpb.VpcAccess_ALL_TRAFFIC, svc.Template.VpcAccess.Egress)
	require.Len(t, svc.Template.Containers, 1)
	require.Len(t, svc.Template.Containers[0].Env, 2)
	assert.Equal(t, "SOCKERLESS_CALLBACK_URL", svc.Template.Containers[0].Env[0].Name)
	assert.Equal(t, "ws://host.docker.internal:3375/v1/cloudrun/reverse", svc.Template.Containers[0].Env[0].GetValue())
	assert.Equal(t, "SOCKERLESS_CONTAINER_ID", svc.Template.Containers[0].Env[1].Name)
	assert.Equal(t, "abc123", svc.Template.Containers[0].Env[1].GetValue())

	got, err := client.GetService(ctx, &runpb.GetServiceRequest{Name: svc.Name})
	require.NoError(t, err)
	assert.Equal(t, svc.Name, got.Name)
	assert.Equal(t, "true", got.Labels["sockerless_managed"])
	require.Len(t, got.Template.Containers, 1)
	require.Len(t, got.Template.Containers[0].Env, 2)
	assert.Equal(t, "ws://host.docker.internal:3375/v1/cloudrun/reverse", got.Template.Containers[0].Env[0].GetValue())
	assert.Equal(t, "abc123", got.Template.Containers[0].Env[1].GetValue())

	it := client.ListServices(ctx, &runpb.ListServicesRequest{
		Parent: "projects/test-project/locations/us-central1",
	})
	found := false
	for {
		s, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if s.Name == svc.Name {
			found = true
			break
		}
	}
	assert.True(t, found, "ListServices must return the service we just created")

	deleteOp, err := client.DeleteService(ctx, &runpb.DeleteServiceRequest{Name: svc.Name})
	require.NoError(t, err)
	_, err = deleteOp.Wait(ctx)
	require.NoError(t, err)

	_, err = client.GetService(ctx, &runpb.GetServiceRequest{Name: svc.Name})
	assert.Error(t, err, "GetService after delete must 404")
}

func TestSDK_CloudRunV2Services_MultiContainerSharesLocalhost(t *testing.T) {
	client := newServicesClient(t)

	svc := createInvokableService(t, client, "v2-svc-pod-localhost", &runpb.Service{
		Template: &runpb.RevisionTemplate{
			Containers: []*runpb.Container{
				{
					// The containers start together, so main gives its
					// sidecar time to listen before it answers.
					Name:  "main",
					Image: httpProbeImageName,
					Args:  []string{"probe-retry", "cloudrun-sidecar-ok"},
				},
				{
					Name:  "sidecar",
					Image: httpProbeImageName,
					Args:  []string{"server"},
				},
			},
		},
	})
	require.NotEmpty(t, svc.Uri)

	status, _, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodPost, "/", "{}")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "cloudrun-sidecar-ok", body, "Cloud Run Service main must reach sidecar on localhost")
}

func TestSDK_CloudRunV2Services_ForwardsRequestPath(t *testing.T) {
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-forward-path", echoService())

	u, err := url.Parse(svc.Uri)
	require.NoError(t, err)
	assert.Equal(t, "https", u.Scheme)
	assert.True(t, strings.HasPrefix(u.Host, svc.Name[strings.LastIndex(svc.Name, "/")+1:]+"-"), "the run.app host starts with the service name: %s", u.Host)
	assert.True(t, strings.HasSuffix(u.Host, "-us-central1.a.run.app"), "the service is served on run.app: %s", u.Host)

	status, _, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodPut, "/items/7?source=sdk", "")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "PUT /items/7?source=sdk", body)
}

// invokeService sends a request to a Cloud Run service's URL, presenting
// bearer — an ID token — when it is not empty. The request is addressed to
// the service's run.app host; the simulator's endpoint is where that host
// resolves to.
func invokeService(t *testing.T, bearer, uri, method, path, body string) (int, http.Header, string) {
	t.Helper()
	u, err := url.Parse(uri)
	require.NoError(t, err)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	require.NoError(t, err)
	req.Host = u.Host
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := rawClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(data)
}

// createInvokableService creates a service in test-project, deletes it when
// the test ends, and grants the shared invoker roles/run.invoker on it.
func createInvokableService(t *testing.T, client *run.ServicesClient, prefix string, service *runpb.Service) *runpb.Service {
	t.Helper()
	svc := createInvokableServiceIn(t, client, "projects/test-project/locations/us-central1", prefix, service)
	grantOnService(t, client, svc.Name, "roles/run.invoker", "serviceAccount:"+sdkInvokerEmail(t), nil)
	return svc
}

// Cloud Run hands the caller whatever the container answered: its status,
// its headers and its body.
func TestSDK_CloudRunV2Services_PassesTheContainersAnswerThrough(t *testing.T) {
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-teapot", &runpb.Service{
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName,
			Args:  []string{"teapot"},
		}}},
	})

	status, header, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusTeapot, status)
	assert.Equal(t, "teapot", header.Get("X-Workload"))
	assert.Equal(t, "short and stout", body)
}

// A configured HTTP startup probe gates the instance: traffic reaches the
// container once the probe's GET answers 2xx.
func TestSDK_CloudRunV2Services_HTTPStartupProbeAdmitsTraffic(t *testing.T) {
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-http-probe", &runpb.Service{
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName,
			Args:  []string{"echo-request"},
			Ports: []*runpb.ContainerPort{{ContainerPort: 8080}},
			StartupProbe: &runpb.Probe{
				PeriodSeconds:    1,
				TimeoutSeconds:   1,
				FailureThreshold: 10,
				ProbeType: &runpb.Probe_HttpGet{HttpGet: &runpb.HTTPGetAction{
					Path: "/healthz",
				}},
			},
		}}},
	})

	status, _, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodGet, "/hello", "")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /hello", body)
}

// A startup probe that keeps failing fails the instance, and the request it
// was started for gets 503 instead of reaching the container.
func TestSDK_CloudRunV2Services_FailingStartupProbeAnswers503(t *testing.T) {
	client := newServicesClient(t)
	cases := map[string]*runpb.Probe{
		// The teapot answers every GET with 418, which is not a success.
		"http": {
			PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 2,
			ProbeType: &runpb.Probe_HttpGet{HttpGet: &runpb.HTTPGetAction{Path: "/ready"}},
		},
		// Nothing in the container listens on 9999.
		"tcp": {
			PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 2,
			ProbeType: &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: 9999}},
		},
	}
	for name, probe := range cases {
		t.Run(name, func(t *testing.T) {
			svc := createInvokableService(t, client, "v2-svc-bad-probe-"+name, &runpb.Service{
				Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
					Image:        httpProbeImageName,
					Args:         []string{"teapot"},
					StartupProbe: probe,
				}}},
			})
			status, _, body := invokeService(t, invokerIDToken(t, svc.Uri), svc.Uri, http.MethodGet, "/", "")
			assert.Equal(t, http.StatusServiceUnavailable, status, "body=%q", body)
			assert.NotContains(t, body, "short and stout", "the request must not reach a container whose startup probe failed")
		})
	}
}

// A private service refuses a request without a credential; one whose invoker
// IAM check is disabled admits it.
func TestSDK_CloudRunV2Services_InvokerAuthentication(t *testing.T) {
	client := newServicesClient(t)

	private := createInvokableService(t, client, "v2-svc-private", &runpb.Service{
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName, Args: []string{"echo-request"},
		}}},
	})
	status, _, _ := invokeService(t, "", private.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status)

	public := createInvokableService(t, client, "v2-svc-public", &runpb.Service{
		InvokerIamDisabled: true,
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName, Args: []string{"echo-request"},
		}}},
	})
	status, _, body := invokeService(t, "", public.Uri, http.MethodGet, "/open", "")
	assert.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /open", body)
}

// A run.app host that names no service is not found.
func TestSDK_CloudRunV2Services_UnknownHostIsNotFound(t *testing.T) {
	status, _, _ := invokeService(t, "", "https://no-such-service-abcdefghij-us-central1.a.run.app", http.MethodGet, "/", "")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestSDK_CloudRunV2Services_UpdateBumpsGeneration(t *testing.T) {
	client := newServicesClient(t)

	createOp, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    "projects/test-project/locations/us-central1",
		ServiceId: uniqueName("v2-svc-update"),
		Service: &runpb.Service{
			Template: &runpb.RevisionTemplate{
				Containers: []*runpb.Container{{Image: "gcr.io/test-project/v1"}},
			},
		},
	})
	require.NoError(t, err)
	created, err := createOp.Wait(ctx)
	require.NoError(t, err)
	cleanupService(t, client, created.Name)
	require.Equal(t, int64(1), created.Generation)

	updateOp, err := client.UpdateService(ctx, &runpb.UpdateServiceRequest{
		Service: &runpb.Service{
			Name: created.Name,
			Template: &runpb.RevisionTemplate{
				Containers: []*runpb.Container{{Image: "gcr.io/test-project/v2"}},
			},
		},
	})
	require.NoError(t, err)
	updated, err := updateOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Generation, "Update should bump generation")
	require.NotNil(t, updated.TerminalCondition)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, updated.TerminalCondition.State)
}
