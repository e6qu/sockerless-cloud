package gcp_sdk_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	functions "cloud.google.com/go/functions/apiv2"
	"cloud.google.com/go/functions/apiv2/functionspb"
	vkit "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/logging/logadmin"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// deployFunction creates a function, deploys a container to the Cloud Run
// service that serves it, the image swap a deploy pipeline makes once its
// build has run, and grants the shared invoker roles/run.invoker on that
// service. It returns the function as GetFunction reads it back.
func deployFunction(t *testing.T, client *functions.FunctionClient, functionID string, container *runpb.Container) *functionspb.Function {
	t.Helper()
	op, err := client.CreateFunction(ctx, &functionspb.CreateFunctionRequest{
		Parent:     "projects/test-project/locations/us-central1",
		FunctionId: functionID,
		Function: &functionspb.Function{
			BuildConfig: &functionspb.BuildConfig{Runtime: "go121", EntryPoint: "Handler"},
		},
	})
	require.NoError(t, err)
	fn, err := op.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		op, err := client.DeleteFunction(context.Background(), &functionspb.DeleteFunctionRequest{Name: fn.Name})
		if status.Code(err) == codes.NotFound {
			return
		}
		require.NoError(t, err)
		require.NoError(t, op.Wait(context.Background()))
	})

	require.NotEmpty(t, fn.GetServiceConfig().GetService())
	update, err := newServicesClient(t).UpdateService(ctx, &runpb.UpdateServiceRequest{
		Service: &runpb.Service{
			Name:     fn.ServiceConfig.Service,
			Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{container}},
		},
	})
	require.NoError(t, err)
	_, err = update.Wait(ctx)
	require.NoError(t, err)
	grantOnService(t, newServicesClient(t), fn.ServiceConfig.Service, "roles/run.invoker", "serviceAccount:"+sdkInvokerEmail(t), nil)

	fn, err = client.GetFunction(ctx, &functionspb.GetFunctionRequest{Name: fn.Name})
	require.NoError(t, err)
	return fn
}

// Cloud Run functions reports the run.app URL of the function's Cloud Run
// service as serviceConfig.uri and the cloudfunctions.net URL as url, and
// serves the function on both.
func TestCloudFunctions_ServedOnItsURLs(t *testing.T) {
	client := newFunctionsClient(t)
	const functionID = "sdk-served-fn"
	fn := deployFunction(t, client, functionID, &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})

	svc, err := newServicesClient(t).GetService(ctx, &runpb.GetServiceRequest{Name: fn.ServiceConfig.Service})
	require.NoError(t, err)
	assert.Equal(t, svc.Uri, fn.ServiceConfig.Uri, "serviceConfig.uri is the URL of the service that serves the function")
	u, err := url.Parse(fn.ServiceConfig.Uri)
	require.NoError(t, err)
	assert.Equal(t, "https", u.Scheme)
	assert.True(t, strings.HasSuffix(u.Host, "-us-central1.a.run.app"), "the function is served on run.app: %s", u.Host)
	assert.Equal(t, "https://us-central1-test-project.cloudfunctions.net/"+functionID, fn.Url)

	code, _, body := invokeService(t, invokerIDToken(t, fn.ServiceConfig.Uri), fn.ServiceConfig.Uri, http.MethodPut, "/items/7?source=sdk", "")
	require.Equal(t, http.StatusOK, code, "body=%q", body)
	assert.Equal(t, "PUT /items/7?source=sdk", body)

	// The cloudfunctions.net front end takes the function's name off the
	// front of the path before the function sees it.
	code, _, body = invokeService(t, invokerIDToken(t, fn.Url), fn.Url, http.MethodPost, "/"+functionID+"/orders?id=3", "{}")
	require.Equal(t, http.StatusOK, code, "body=%q", body)
	assert.Equal(t, "POST /orders?id=3", body)

	code, _, body = invokeService(t, invokerIDToken(t, fn.Url), fn.Url, http.MethodGet, "/"+functionID, "")
	require.Equal(t, http.StatusOK, code, "body=%q", body)
	assert.Equal(t, "GET /", body)

	// The container's stdout reaches Cloud Logging as the function's log.
	waitForFunctionLogMessage(t, functionID, "POST /orders?id=3")
}

// The function's caller gets whatever the container answered: its status,
// its headers and its body, on both URLs.
func TestCloudFunctions_PassesTheContainersAnswerThrough(t *testing.T) {
	client := newFunctionsClient(t)
	const functionID = "sdk-teapot-fn"
	fn := deployFunction(t, client, functionID, &runpb.Container{Image: httpProbeImageName, Args: []string{"teapot"}})

	for _, target := range []struct{ uri, path string }{
		{fn.ServiceConfig.Uri, "/"},
		{fn.Url, "/" + functionID},
	} {
		code, header, body := invokeService(t, invokerIDToken(t, target.uri), target.uri, http.MethodGet, target.path, "")
		assert.Equal(t, http.StatusTeapot, code, "%s", target.uri)
		assert.Equal(t, "teapot", header.Get("X-Workload"), "%s", target.uri)
		assert.Equal(t, "short and stout", body, "%s", target.uri)
	}
}

// Cloud Run functions authorizes an invocation on either of the function's
// URLs against the IAM policy of the Cloud Run service that serves it: a
// request without a credential is refused, an ID token whose audience is
// either URL names the caller, and the caller is admitted once it holds
// roles/run.invoker on the service. An ID token minted for another audience
// is answered 401, and granting allUsers the role makes the function public.
func TestCloudFunctions_InvokerAuthentication(t *testing.T) {
	client := newFunctionsClient(t)
	const functionID = "sdk-private-fn"
	fn := deployFunction(t, client, functionID, &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})
	services := newServicesClient(t)

	code, _, _ := invokeService(t, "", fn.ServiceConfig.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, code)
	code, _, _ = invokeService(t, "", fn.Url, http.MethodGet, "/"+functionID, "")
	assert.Equal(t, http.StatusForbidden, code)

	email := createServiceAccount(t, "test-project", uniqueName("gcf-invoker"))
	member := "serviceAccount:" + email
	code, _, body := invokeService(t, idTokenFor(t, email, fn.Url), fn.Url, http.MethodGet, "/"+functionID, "")
	assert.Equal(t, http.StatusForbidden, code, "a principal without roles/run.invoker on the service: body=%q", body)

	grantOnService(t, services, fn.ServiceConfig.Service, "roles/run.invoker", member, nil)
	for _, audience := range []string{fn.Url, fn.ServiceConfig.Uri} {
		code, _, body := invokeService(t, idTokenFor(t, email, audience), fn.Url, http.MethodGet, "/"+functionID+"/whoami", "")
		assert.Equal(t, http.StatusOK, code, "ID token for %s: body=%q", audience, body)
		assert.Equal(t, "GET /whoami", body, "ID token for %s", audience)
	}
	code, _, _ = invokeService(t, idTokenFor(t, email, "https://us-central1-test-project.cloudfunctions.net/another-fn"),
		fn.Url, http.MethodGet, "/"+functionID, "")
	assert.Equal(t, http.StatusUnauthorized, code, "an ID token minted for another function's URL")

	grantOnService(t, services, fn.ServiceConfig.Service, "roles/run.invoker", "allUsers", nil)
	code, _, body = invokeService(t, "", fn.Url, http.MethodGet, "/"+functionID+"/public", "")
	assert.Equal(t, http.StatusOK, code, "allUsers makes the function public: body=%q", body)
	assert.Equal(t, "GET /public", body)
}

// A cloudfunctions.net path that names no function is not found, and a
// deleted function's URLs, service and the service's IAM policy are gone with
// it: a function deployed again under the same name is private again.
func TestCloudFunctions_DeletedFunctionIsNoLongerServed(t *testing.T) {
	const missing = "https://us-central1-test-project.cloudfunctions.net/no-such-fn"
	code, _, _ := invokeService(t, invokerIDToken(t, missing), missing, http.MethodGet, "/no-such-fn", "")
	assert.Equal(t, http.StatusNotFound, code)

	client := newFunctionsClient(t)
	const functionID = "sdk-deleted-fn"
	fn := deployFunction(t, client, functionID, &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})
	grantOnService(t, newServicesClient(t), fn.ServiceConfig.Service, "roles/run.invoker", "allUsers", nil)
	code, _, body := invokeService(t, "", fn.Url, http.MethodGet, "/"+functionID, "")
	require.Equal(t, http.StatusOK, code, "body=%q", body)

	op, err := client.DeleteFunction(ctx, &functionspb.DeleteFunctionRequest{Name: fn.Name})
	require.NoError(t, err)
	require.NoError(t, op.Wait(ctx))

	_, err = newServicesClient(t).GetService(ctx, &runpb.GetServiceRequest{Name: fn.ServiceConfig.Service})
	assert.Equal(t, codes.NotFound, status.Code(err), "deleting a function deletes the service that served it: %v", err)
	code, _, _ = invokeService(t, invokerIDToken(t, fn.ServiceConfig.Uri), fn.ServiceConfig.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusNotFound, code)
	code, _, _ = invokeService(t, invokerIDToken(t, fn.Url), fn.Url, http.MethodGet, "/"+functionID, "")
	assert.Equal(t, http.StatusNotFound, code)

	fn = deployFunction(t, client, functionID, &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})
	code, _, body = invokeService(t, "", fn.Url, http.MethodGet, "/"+functionID, "")
	assert.Equal(t, http.StatusForbidden, code, "the deleted service's allUsers binding must not reach its successor: body=%q", body)
}

// UpdateFunction redeploys the function's service with the serviceConfig it
// sets, while the output-only uri and service stay as they were.
func TestCloudFunctions_UpdateRedeploysTheService(t *testing.T) {
	client := newFunctionsClient(t)
	fn := deployFunction(t, client, "sdk-update-fn", &runpb.Container{Image: httpProbeImageName, Args: []string{"log-request"}})

	op, err := client.UpdateFunction(ctx, &functionspb.UpdateFunctionRequest{
		Function: &functionspb.Function{
			Name: fn.Name,
			ServiceConfig: &functionspb.ServiceConfig{
				TimeoutSeconds:       120,
				EnvironmentVariables: map[string]string{"GREETING": "hello"},
			},
		},
	})
	require.NoError(t, err)
	updated, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, fn.ServiceConfig.Uri, updated.ServiceConfig.Uri)
	assert.Equal(t, fn.ServiceConfig.Service, updated.ServiceConfig.Service)

	svc, err := newServicesClient(t).GetService(ctx, &runpb.GetServiceRequest{Name: fn.ServiceConfig.Service})
	require.NoError(t, err)
	assert.Equal(t, int64(120), svc.Template.GetTimeout().GetSeconds())
	require.NotEmpty(t, svc.Template.Containers)
	env := map[string]string{}
	for _, e := range svc.Template.Containers[0].Env {
		env[e.Name] = e.GetValue()
	}
	assert.Equal(t, map[string]string{"GREETING": "hello"}, env)
}

// waitForFunctionLogMessage follows a function's Cloud Logging entries until
// one carries message.
func waitForFunctionLogMessage(t *testing.T, functionID, message string) {
	t.Helper()
	followLogMessages(t, `resource.type="cloud_run_revision" AND resource.labels.service_name="`+functionID+`"`,
		func(messages []string) bool { return slices.Contains(messages, message) })
}

// followLogMessages follows the text payloads of the Cloud Logging entries
// filter matches until done accepts them, and returns them. It opens a
// TailLogEntries stream first and then lists what was already logged, so an
// entry written between the two reads arrives on one of them.
func followLogMessages(t *testing.T, filter string, done func(messages []string) bool) []string {
	t.Helper()
	return followLogMessagesAt(t, grpcAddr, filter, done)
}

// followLogMessagesAt follows log entries as followLogMessages does, from the
// simulator whose gRPC endpoint is at address.
func followLogMessagesAt(t *testing.T, address, filter string, done func(messages []string) bool) []string {
	t.Helper()
	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	tailClient, err := vkit.NewClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	defer tailClient.Close()
	listClient, err := logadmin.NewClient(ctx, "test-project", option.WithGRPCConn(conn))
	require.NoError(t, err)
	defer listClient.Close()
	stream, err := tailClient.TailLogEntries(tailCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/test-project"},
		Filter:        filter,
		BufferWindow:  durationpb.New(0),
	}))

	var messages []string
	it := listClient.Entries(ctx, logadmin.Filter(filter))
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if text, ok := entry.Payload.(string); ok {
			messages = append(messages, text)
		}
	}
	for !done(messages) {
		resp, err := stream.Recv()
		require.NoError(t, err, "the entries %s matches never satisfied the wait: %q", filter, messages)
		for _, entry := range resp.GetEntries() {
			messages = append(messages, entry.GetTextPayload())
		}
	}
	return messages
}
