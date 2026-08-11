package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// v2 Cloud Run Service Revisions + IAM + Operations contract. These drive
// the canonical cloud.google.com/go/run/apiv2 REST clients (the same the
// cloudrun backend uses) against the new endpoints so their path shapes
// and wire bodies stay pinned to the Discovery document.

func newRevisionsClient(t *testing.T) *run.RevisionsClient {
	t.Helper()
	client, err := run.NewRevisionsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func createV2Service(t *testing.T, client *run.ServicesClient, parent, id string) {
	t.Helper()
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
	t.Cleanup(func() {
		delOp, err := client.DeleteService(ctx, &runpb.DeleteServiceRequest{Name: parent + "/services/" + id})
		if err == nil {
			_, _ = delOp.Wait(ctx)
		}
	})
}

func TestSDK_CloudRunV2_ServiceRevisions_RoundTrip(t *testing.T) {
	services := newServicesClient(t)
	revisions := newRevisionsClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-svc-revs"
	createV2Service(t, services, parent, id)

	servicePath := parent + "/services/" + id

	// List revisions — the create materialized exactly one.
	it := revisions.ListRevisions(ctx, &runpb.ListRevisionsRequest{Parent: servicePath})
	var names []string
	for {
		rev, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, rev.GetName())
	}
	require.Len(t, names, 1, "one revision per service deploy")

	// Get the revision.
	got, err := revisions.GetRevision(ctx, &runpb.GetRevisionRequest{Name: names[0]})
	require.NoError(t, err)
	assert.Equal(t, names[0], got.GetName())
	assert.Equal(t, servicePath, got.GetService())
	require.Len(t, got.GetContainers(), 1)
	assert.Equal(t, "gcr.io/test-project/"+id, got.GetContainers()[0].GetImage())

	// Delete the revision.
	delOp, err := revisions.DeleteRevision(ctx, &runpb.DeleteRevisionRequest{Name: names[0]})
	require.NoError(t, err)
	_, err = delOp.Wait(ctx)
	require.NoError(t, err)

	_, err = revisions.GetRevision(ctx, &runpb.GetRevisionRequest{Name: names[0]})
	require.Error(t, err, "deleted revision must be gone")
}

func TestSDK_CloudRunV2_ServiceIAM_RoundTrip(t *testing.T) {
	services := newServicesClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-svc-iam"
	createV2Service(t, services, parent, id)
	resource := parent + "/services/" + id

	// Default empty policy.
	pol, err := services.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: resource})
	require.NoError(t, err)
	require.NotNil(t, pol)

	// Set a binding.
	set, err := services.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: resource,
		Policy: &iampb.Policy{
			Bindings: []*iampb.Binding{{
				Role:    "roles/run.invoker",
				Members: []string{"user:dev@example.com"},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, set.GetBindings(), 1)
	assert.Equal(t, "roles/run.invoker", set.GetBindings()[0].GetRole())

	// Read back.
	pol2, err := services.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: resource})
	require.NoError(t, err)
	require.Len(t, pol2.GetBindings(), 1)
	assert.Contains(t, pol2.GetBindings()[0].GetMembers(), "user:dev@example.com")

	// TestIamPermissions echoes the requested set (sim admin model).
	perms, err := services.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    resource,
		Permissions: []string{"run.services.get", "run.services.invoke"},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"run.services.get", "run.services.invoke"}, perms.GetPermissions())
}

// TestSDK_CloudRunV2_Operations_DeleteAndWait drives the operations
// delete + wait endpoints via raw REST (the LRO completes synchronously
// in the sim, so wait returns the done operation immediately).
func TestSDK_CloudRunV2_Operations_DeleteAndWait(t *testing.T) {
	services := newServicesClient(t)
	parent := "projects/test-project-ops/locations/us-central1"
	const id = "v2-svc-ops"
	createV2Service(t, services, parent, id)

	// The create wrote an LRO under crOperations; list it (served by the
	// shared operations list handler) to find a real operation name.
	resp, err := http.Get(baseURL + "/v2/" + parent + "/operations")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct {
		Operations []struct {
			Name string `json:"name"`
			Done bool   `json:"done"`
		} `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	require.NotEmpty(t, list.Operations)
	opName := list.Operations[0].Name

	// Wait — already done, returns immediately.
	waitResp, err := http.Post(baseURL+"/v2/"+opName+":wait", "application/json", nil)
	require.NoError(t, err)
	defer waitResp.Body.Close()
	require.Equal(t, http.StatusOK, waitResp.StatusCode)
	var waited struct {
		Name string `json:"name"`
		Done bool   `json:"done"`
	}
	require.NoError(t, json.NewDecoder(waitResp.Body).Decode(&waited))
	assert.Equal(t, opName, waited.Name)
	assert.True(t, waited.Done)

	// Delete the operation.
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/v2/"+opName, nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	// Gone.
	getResp, err := http.Get(baseURL + "/v2/" + opName)
	require.NoError(t, err)
	getResp.Body.Close()
	require.Equal(t, http.StatusNotFound, getResp.StatusCode)
}
