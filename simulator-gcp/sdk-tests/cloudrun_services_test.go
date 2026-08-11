package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v1"
)

// Cloud Run v1 services SDK tests (Knative-style API). Sockerless
// doesn't use this API on the runner path today, but the simulator
// ships the slice so `gcloud run services *` and the run/v1 client
// round-trip. The direct-REST tests assert wire-level member shapes;
// TestCloudRunServices_RunV1ClientRoundTrip drives the canonical
// google.golang.org/api/run/v1 client, which builds the real
// /apis/serving.knative.dev/v1/... method paths and so pins the
// simulator's path shape to the Discovery document's.

// TestCloudRunServices_RunV1ClientRoundTrip exercises the knative
// namespaces.services surface through the official run/v1 client.
func TestCloudRunServices_RunV1ClientRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, err := run.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	const namespace = "test-project-runv1"
	created, err := svc.Namespaces.Services.Create("namespaces/"+namespace, &run.Service{
		ApiVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata:   &run.ObjectMeta{Name: "svc-runv1"},
		Spec: &run.ServiceSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "gcr.io/x/hello"}}},
		}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "svc-runv1", created.Metadata.Name)

	got, err := svc.Namespaces.Services.Get("namespaces/" + namespace + "/services/svc-runv1").Do()
	require.NoError(t, err)
	assert.Equal(t, namespace, got.Metadata.Namespace)
	require.NotNil(t, got.Status)
	assert.NotEmpty(t, got.Status.Url)

	list, err := svc.Namespaces.Services.List("namespaces/" + namespace).Do()
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "svc-runv1", list.Items[0].Metadata.Name)

	_, err = svc.Namespaces.Services.Delete("namespaces/" + namespace + "/services/svc-runv1").Do()
	require.NoError(t, err)

	_, err = svc.Namespaces.Services.Get("namespaces/" + namespace + "/services/svc-runv1").Do()
	require.Error(t, err, "service must be gone after delete")
}

func TestCloudRunServices_CreateGetListDelete(t *testing.T) {
	namespace := "test-project"

	// Create.
	createBody := `{
		"apiVersion":"serving.knative.dev/v1",
		"kind":"Service",
		"metadata":{"name":"svc-roundtrip","labels":{"env":"test"}},
		"spec":{"template":{"spec":{"containers":[{"image":"gcr.io/x/hello","env":[{"name":"FOO","value":"bar"}]}]}}}
	}`
	createResp := doKnativePOST(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services", createBody)

	var created struct {
		Metadata CRServiceMetaResp `json:"metadata"`
		Status   struct {
			URL        string                          `json:"url"`
			Conditions []struct{ Type, Status string } `json:"conditions"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(createResp), &created))
	assert.Equal(t, "svc-roundtrip", created.Metadata.Name)
	assert.Equal(t, namespace, created.Metadata.Namespace)
	assert.NotEmpty(t, created.Metadata.UID)
	assert.NotEmpty(t, created.Status.URL)
	require.NotEmpty(t, created.Status.Conditions)
	assert.Equal(t, "Ready", created.Status.Conditions[0].Type)
	assert.Equal(t, "True", created.Status.Conditions[0].Status)

	// Get.
	getResp := doKnativeGET(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services/svc-roundtrip")
	var got struct{ Metadata CRServiceMetaResp }
	require.NoError(t, json.Unmarshal([]byte(getResp), &got))
	assert.Equal(t, "svc-roundtrip", got.Metadata.Name)

	// List — should contain the service we created.
	listResp := doKnativeGET(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services")
	var list struct {
		Items []struct{ Metadata CRServiceMetaResp } `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(listResp), &list))
	names := []string{}
	for _, s := range list.Items {
		names = append(names, s.Metadata.Name)
	}
	assert.Contains(t, names, "svc-roundtrip")

	// Delete.
	doKnativeDELETE(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services/svc-roundtrip")

	// Verify gone.
	req, _ := http.NewRequest("GET", baseURL+"/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services/svc-roundtrip", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCloudRunServices_ReplaceBumpsGeneration(t *testing.T) {
	namespace := "test-project-replace"

	createBody := `{
		"metadata":{"name":"svc-rev"},
		"spec":{"template":{"spec":{"containers":[{"image":"gcr.io/x/v1"}]}}}
	}`
	doKnativePOST(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services", createBody)

	replaceBody := `{
		"metadata":{"name":"svc-rev","labels":{"version":"v2"}},
		"spec":{"template":{"spec":{"containers":[{"image":"gcr.io/x/v2"}]}}}
	}`
	updResp := doKnativePUT(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services/svc-rev", replaceBody)

	var updated struct {
		Metadata CRServiceMetaResp `json:"metadata"`
		Status   struct {
			LatestReadyRevisionName string `json:"latestReadyRevisionName"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(updResp), &updated))
	assert.Equal(t, int64(2), updated.Metadata.Generation, "replace should bump generation")
	assert.Equal(t, "svc-rev-00002", updated.Status.LatestReadyRevisionName,
		"each revision should get a new name")
	assert.Equal(t, "v2", updated.Metadata.Labels["version"])

	// Cleanup.
	doKnativeDELETE(t, "/apis/serving.knative.dev/v1/namespaces/"+namespace+"/services/svc-rev")
}

// ---- helpers ----

type CRServiceMetaResp struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	UID        string            `json:"uid"`
	Generation int64             `json:"generation"`
	Labels     map[string]string `json:"labels"`
}

func doKnativePOST(t *testing.T, path, body string) string {
	t.Helper()
	return doKnative(t, "POST", path, body)
}

func doKnativeGET(t *testing.T, path string) string {
	t.Helper()
	return doKnative(t, "GET", path, "")
}

func doKnativePUT(t *testing.T, path, body string) string {
	t.Helper()
	return doKnative(t, "PUT", path, body)
}

func doKnativeDELETE(t *testing.T, path string) {
	t.Helper()
	_ = doKnative(t, "DELETE", path, "")
}

func doKnative(t *testing.T, method, path, body string) string {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, baseURL+path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "%s %s failed: %d %s", method, path, resp.StatusCode, string(data))
	return string(data)
}

func TestCloudRun_GetService_NotFound_ErrorClassification(t *testing.T) {
	const project = "test-project"
	const region = "us-central1"
	resp, err := http.Get(baseURL + "/v2/projects/" + project + "/locations/" + region + "/services/nonexistent-service")
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("expected HTTP 404 for missing Cloud Run service, got %d", resp.StatusCode)
	}
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Status  string `json:"status"`
			Details []any  `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	if body.Error.Code != 404 {
		t.Errorf("error.code = %d, want 404", body.Error.Code)
	}
	if body.Error.Status == "" {
		t.Errorf("error.status must be present (e.g. NOT_FOUND), got empty")
	}
	if body.Error.Details == nil {
		t.Errorf("error.details must be present (even if empty)")
	}
}
