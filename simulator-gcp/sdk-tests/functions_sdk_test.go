package gcp_sdk_test

import (
	"testing"

	functions "cloud.google.com/go/functions/apiv2"
	"cloud.google.com/go/functions/apiv2/functionspb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func newFunctionsClient(t *testing.T) *functions.FunctionClient {
	t.Helper()
	client, err := functions.NewFunctionRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSDK_CloudFunctions_CreateFunction(t *testing.T) {
	client := newFunctionsClient(t)

	op, err := client.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
		Parent:     "projects/test-project/locations/us-central1",
		FunctionId: "sdk-create-fn",
		Function: &functionspb.Function{
			BuildConfig: &functionspb.BuildConfig{
				Runtime:    "go121",
				EntryPoint: "Handler",
			},
		},
	})
	require.NoError(t, err)

	fn, err := op.Wait(ctx)
	require.NoError(t, err)

	assert.Contains(t, fn.Name, "sdk-create-fn")
	assert.Equal(t, functionspb.Function_ACTIVE, fn.State)
	assert.Equal(t, functionspb.Environment_GEN_2, fn.Environment)
}

func TestSDK_CloudFunctions_GetFunction(t *testing.T) {
	client := newFunctionsClient(t)

	// Create first
	op, err := client.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
		Parent:     "projects/test-project/locations/us-central1",
		FunctionId: "sdk-get-fn",
		Function: &functionspb.Function{
			BuildConfig: &functionspb.BuildConfig{
				Runtime:    "go121",
				EntryPoint: "Handler",
			},
		},
	})
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)

	// Get
	fn, err := client.GetFunction(ctx, &functionspb.GetFunctionRequest{
		Name: "projects/test-project/locations/us-central1/functions/sdk-get-fn",
	})
	require.NoError(t, err)

	assert.Equal(t, "projects/test-project/locations/us-central1/functions/sdk-get-fn", fn.Name)
	assert.Equal(t, functionspb.Function_ACTIVE, fn.State)
}

func TestSDK_CloudFunctions_ListFunctions(t *testing.T) {
	client := newFunctionsClient(t)

	// Create two functions
	for _, id := range []string{"sdk-list-fn-a", "sdk-list-fn-b"} {
		op, err := client.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
			Parent:     "projects/test-project/locations/us-central1",
			FunctionId: id,
			Function: &functionspb.Function{
				BuildConfig: &functionspb.BuildConfig{
					Runtime:    "go121",
					EntryPoint: "Handler",
				},
			},
		})
		require.NoError(t, err)
		_, err = op.Wait(ctx)
		require.NoError(t, err)
	}

	// List
	it := client.ListFunctions(ctx, &functionspb.ListFunctionsRequest{
		Parent: "projects/test-project/locations/us-central1",
	})

	var names []string
	for {
		fn, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, fn.Name)
	}

	foundA, foundB := false, false
	for _, n := range names {
		if n == "projects/test-project/locations/us-central1/functions/sdk-list-fn-a" {
			foundA = true
		}
		if n == "projects/test-project/locations/us-central1/functions/sdk-list-fn-b" {
			foundB = true
		}
	}
	assert.True(t, foundA, "sdk-list-fn-a not found in list")
	assert.True(t, foundB, "sdk-list-fn-b not found in list")
}

func TestSDK_CloudFunctions_DeleteFunction(t *testing.T) {
	client := newFunctionsClient(t)

	// Create
	createOp, err := client.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
		Parent:     "projects/test-project/locations/us-central1",
		FunctionId: "sdk-delete-fn",
		Function: &functionspb.Function{
			BuildConfig: &functionspb.BuildConfig{
				Runtime:    "go121",
				EntryPoint: "Handler",
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	// Delete
	deleteOp, err := client.DeleteFunction(ctx, &functionspb.DeleteFunctionRequest{
		Name: "projects/test-project/locations/us-central1/functions/sdk-delete-fn",
	})
	require.NoError(t, err)
	err = deleteOp.Wait(ctx)
	require.NoError(t, err)

	// Verify gone
	_, err = client.GetFunction(ctx, &functionspb.GetFunctionRequest{
		Name: "projects/test-project/locations/us-central1/functions/sdk-delete-fn",
	})
	assert.Error(t, err, "function should not exist after deletion")
}
