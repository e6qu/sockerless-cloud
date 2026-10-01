package gcp_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/iam/apiv1/iampb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/type/expr"
)

// sdkInvokerAccountID names the service account createInvokableService grants
// roles/run.invoker on each service it creates, so a test that is not about
// authorization invokes the service the way a granted caller does.
const sdkInvokerAccountID = "sdk-run-invoker"

var (
	sdkInvokerOnce sync.Once
	sdkInvokerErr  error
)

func sdkInvokerEmail(t *testing.T) string {
	t.Helper()
	sdkInvokerOnce.Do(func() {
		_, sdkInvokerErr = iamService(t).Projects.ServiceAccounts.Create("projects/test-project",
			&iam.CreateServiceAccountRequest{AccountId: sdkInvokerAccountID}).Do()
	})
	require.NoError(t, sdkInvokerErr)
	return sdkInvokerAccountID + "@test-project.iam.gserviceaccount.com"
}

// invokerIDToken is an ID token for the shared invoker, minted for the
// service URL it is presented to.
func invokerIDToken(t *testing.T, serviceURI string) string {
	t.Helper()
	return idTokenFor(t, sdkInvokerEmail(t), serviceURI)
}

// createServiceAccount creates a service account in the project and deletes it
// when the test ends.
func createServiceAccount(t *testing.T, project, accountID string) string {
	t.Helper()
	svc := iamService(t)
	sa, err := svc.Projects.ServiceAccounts.Create("projects/"+project,
		&iam.CreateServiceAccountRequest{AccountId: accountID}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := svc.Projects.ServiceAccounts.Delete(sa.Name).Do()
		require.NoError(t, err)
	})
	return sa.Email
}

// idTokenFor mints a Google-signed ID token for a service account through the
// IAM Service Account Credentials API, the way a caller impersonating it does.
func idTokenFor(t *testing.T, email, audience string) string {
	t.Helper()
	resp, err := iamCredentialsService(t).Projects.ServiceAccounts.GenerateIdToken(
		"projects/-/serviceAccounts/"+email,
		&iamcredentials.GenerateIdTokenRequest{Audience: audience, IncludeEmail: true}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.Token)
	return resp.Token
}

// editServicePolicy applies edit to the service's IAM policy through
// getIamPolicy and setIamPolicy.
func editServicePolicy(t *testing.T, client *run.ServicesClient, service string, edit func(*iampb.Policy)) {
	t.Helper()
	policy, err := client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: service})
	require.NoError(t, err)
	edit(policy)
	_, err = client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: service, Policy: policy})
	require.NoError(t, err)
}

func grantOnService(t *testing.T, client *run.ServicesClient, service, role, member string, condition *expr.Expr) {
	t.Helper()
	editServicePolicy(t, client, service, func(policy *iampb.Policy) {
		if condition != nil {
			policy.Version = 3
		}
		policy.Bindings = append(policy.Bindings, &iampb.Binding{Role: role, Members: []string{member}, Condition: condition})
	})
}

func revokeOnService(t *testing.T, client *run.ServicesClient, service, role, member string) {
	t.Helper()
	editServicePolicy(t, client, service, func(policy *iampb.Policy) {
		var kept []*iampb.Binding
		for _, binding := range policy.Bindings {
			if binding.Role == role {
				binding.Members = slices.DeleteFunc(binding.Members, func(m string) bool { return m == member })
				if len(binding.Members) == 0 {
					continue
				}
			}
			kept = append(kept, binding)
		}
		policy.Bindings = kept
	})
}

// editProjectPolicy applies edit to the project's IAM policy through Cloud
// Resource Manager.
func editProjectPolicy(t *testing.T, project string, edit func(*cloudresourcemanager.Policy)) {
	t.Helper()
	svc := crmService(t)
	policy, err := svc.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	edit(policy)
	_, err = svc.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Do()
	require.NoError(t, err)
}

// createInvokableServiceIn creates a service in parent and deletes it when the
// test ends, granting nobody the right to invoke it.
func createInvokableServiceIn(t *testing.T, client *run.ServicesClient, parent, prefix string, service *runpb.Service) *runpb.Service {
	t.Helper()
	op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    parent,
		ServiceId: uniqueName(prefix),
		Service:   service,
	})
	require.NoError(t, err)
	svc, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupService(t, client, svc.Name)
	return svc
}

func echoService() *runpb.Service {
	return &runpb.Service{Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
		Image: httpProbeImageName, Args: []string{"echo-request"},
	}}}}
}

const cloudRunForbiddenRoot = "Your client does not have permission to get URL <code>/</code> from this server."

// Cloud Run admits a caller to a private service only when the ID token it
// presents names a principal holding run.routes.invoke on the service, and
// answers 403 to one that does not, 401 to a bearer that is not an ID token
// for the service, and 403 to a request without a credential.
func TestSDK_CloudRunV2Services_InvokerIAMOnTheService(t *testing.T) {
	client := newServicesClient(t)
	svc := createInvokableServiceIn(t, client, "projects/test-project/locations/us-central1", "v2-svc-invoker", echoService())
	email := createServiceAccount(t, "test-project", uniqueName("sdk-inv"))
	member := "serviceAccount:" + email
	token := idTokenFor(t, email, svc.Uri)

	status, _, body := invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status, "a principal bound to nothing must not invoke the service")
	assert.Contains(t, body, cloudRunForbiddenRoot)
	assert.Contains(t, body, "<title>403 Forbidden</title>")

	status, _, body = invokeService(t, "", svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status, "a request without a credential must not invoke the service")
	assert.Contains(t, body, cloudRunForbiddenRoot)

	grantOnService(t, client, svc.Name, "roles/run.invoker", member, nil)

	status, _, body = invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, status, "roles/run.invoker on the service admits its holder: %q", body)
	assert.Equal(t, "GET /", body)

	// The holder's OAuth access token is not an ID token, and an ID token
	// minted for another audience is not one for this service.
	accessToken, err := iamCredentialsService(t).Projects.ServiceAccounts.GenerateAccessToken(
		"projects/-/serviceAccounts/"+email,
		&iamcredentials.GenerateAccessTokenRequest{Scope: []string{"https://www.googleapis.com/auth/cloud-platform"}}).Do()
	require.NoError(t, err)
	for name, bearer := range map[string]string{
		"access token":   accessToken.AccessToken,
		"other audience": idTokenFor(t, email, "https://other-service-abcdefghij-us-central1.a.run.app"),
	} {
		resp := invokeServiceRaw(t, svc.Uri, "/", http.Header{"Authorization": {"Bearer " + bearer}})
		assert.Equal(t, http.StatusUnauthorized, resp.status, "%s: %q", name, resp.body)
		assert.Contains(t, resp.body, "Your client does not have permission to the requested URL <code>/</code>.", name)
		assert.Equal(t, `Bearer error="invalid_token" error_description="The access token could not be verified"`,
			resp.header.Get("WWW-Authenticate"), name)
	}

	// X-Serverless-Authorization carries the credential and leaves
	// Authorization to the application.
	resp := invokeServiceRaw(t, svc.Uri, "/via-serverless", http.Header{
		"X-Serverless-Authorization": {"Bearer " + token},
		"Authorization":              {"Bearer application-token"},
	})
	assert.Equal(t, http.StatusOK, resp.status, "body=%q", resp.body)
	assert.Equal(t, "GET /via-serverless", resp.body)

	revokeOnService(t, client, svc.Name, "roles/run.invoker", member)
	status, _, body = invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status, "revoking the binding must refuse the principal again: %q", body)

	// allAuthenticatedUsers admits any caller with a valid ID token and still
	// refuses one without a credential.
	grantOnService(t, client, svc.Name, "roles/run.invoker", "allAuthenticatedUsers", nil)
	status, _, body = invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusOK, status, "body=%q", body)
	status, _, _ = invokeService(t, "", svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status)
	revokeOnService(t, client, svc.Name, "roles/run.invoker", "allAuthenticatedUsers")

	grantOnService(t, client, svc.Name, "roles/run.invoker", "allUsers", nil)
	status, _, body = invokeService(t, "", svc.Uri, http.MethodGet, "/public", "")
	assert.Equal(t, http.StatusOK, status, "allUsers makes the service public: %q", body)
	assert.Equal(t, "GET /public", body)
}

// A role granted on the project reaches every service in it.
func TestSDK_CloudRunV2Services_InvokerInheritedFromTheProject(t *testing.T) {
	project := uniqueName("run-inv")
	ensureV1Project(t, crmService(t), project)
	client := newServicesClient(t)
	svc := createInvokableServiceIn(t, client, "projects/"+project+"/locations/us-central1", "v2-svc-inherit", echoService())
	email := createServiceAccount(t, project, uniqueName("sdk-proj-inv"))
	member := "serviceAccount:" + email
	token := idTokenFor(t, email, svc.Uri)

	status, _, _ := invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	require.Equal(t, http.StatusForbidden, status)

	editProjectPolicy(t, project, func(policy *cloudresourcemanager.Policy) {
		policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{Role: "roles/run.invoker", Members: []string{member}})
	})
	status, _, body := invokeService(t, token, svc.Uri, http.MethodGet, "/inherited", "")
	require.Equal(t, http.StatusOK, status, "roles/run.invoker on the project admits its holder: %q", body)
	assert.Equal(t, "GET /inherited", body)

	editProjectPolicy(t, project, func(policy *cloudresourcemanager.Policy) {
		policy.Bindings = slices.DeleteFunc(policy.Bindings, func(b *cloudresourcemanager.Binding) bool {
			return b.Role == "roles/run.invoker"
		})
	})
	status, _, _ = invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status, "removing the project binding must refuse the principal again")
}

// A conditional binding grants the role only while its condition holds,
// evaluated against the service and the time of the request.
func TestSDK_CloudRunV2Services_InvokerBindingConditions(t *testing.T) {
	client := newServicesClient(t)
	svc := createInvokableServiceIn(t, client, "projects/test-project/locations/us-central1", "v2-svc-cond", echoService())
	other := createInvokableServiceIn(t, client, "projects/test-project/locations/us-central1", "v2-svc-cond-other", echoService())
	email := createServiceAccount(t, "test-project", uniqueName("sdk-cond"))
	member := "serviceAccount:" + email
	token := idTokenFor(t, email, svc.Uri)

	grantOnService(t, client, svc.Name, "roles/run.invoker", member, &expr.Expr{
		Title:      "expired",
		Expression: `request.time < timestamp("2001-01-01T00:00:00Z")`,
	})
	status, _, _ := invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status, "an expired grant must not admit the principal")
	revokeOnService(t, client, svc.Name, "roles/run.invoker", member)

	grantOnService(t, client, svc.Name, "roles/run.invoker", member, &expr.Expr{
		Title:      "this service",
		Expression: fmt.Sprintf(`resource.type == "run.googleapis.com/Service" && resource.name == %q`, svc.Name),
	})
	status, _, body := invokeService(t, token, svc.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusOK, status, "a condition naming the service admits the principal: %q", body)

	// The same condition on the project grants nothing on another service.
	editProjectPolicy(t, "test-project", func(policy *cloudresourcemanager.Policy) {
		policy.Version = 3
		policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{
			Role:    "roles/run.invoker",
			Members: []string{member},
			Condition: &cloudresourcemanager.Expr{
				Title:      "only " + svc.Name,
				Expression: fmt.Sprintf(`resource.name.startsWith(%q)`, svc.Name),
			},
		})
	})
	t.Cleanup(func() {
		editProjectPolicy(t, "test-project", func(policy *cloudresourcemanager.Policy) {
			policy.Bindings = slices.DeleteFunc(policy.Bindings, func(b *cloudresourcemanager.Binding) bool {
				return slices.Contains(b.Members, member)
			})
		})
	})
	status, _, _ = invokeService(t, idTokenFor(t, email, other.Uri), other.Uri, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusForbidden, status, "a project grant conditioned on one service must not reach another")
}

// The google.golang.org/api/idtoken service-account flow — a JWT-bearer grant
// with a target_audience at the token endpoint — mints the ID token a backend
// presents to the service it invokes.
func TestSDK_CloudRunV2Services_IDTokenClientInvokes(t *testing.T) {
	saPath, email := writeServiceAccountJSON(t)
	client := newServicesClient(t)
	svc := createInvokableServiceIn(t, client, "projects/test-project/locations/us-central1", "v2-svc-idtoken", echoService())
	grantOnService(t, client, svc.Name, "roles/run.invoker", "serviceAccount:"+email, nil)

	invoker, err := idtoken.NewClient(ctx, svc.Uri, option.WithCredentialsFile(saPath))
	require.NoError(t, err)
	u, err := url.Parse(svc.Uri)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/from-backend", nil)
	require.NoError(t, err)
	req.Host = u.Host
	resp, err := invoker.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

type serviceAnswer struct {
	status int
	header http.Header
	body   string
}

// invokeServiceRaw sends a GET with the given headers to a service's URL.
func invokeServiceRaw(t *testing.T, uri, path string, header http.Header) serviceAnswer {
	t.Helper()
	u, err := url.Parse(uri)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	require.NoError(t, err)
	req.Host = u.Host
	for name, values := range header {
		req.Header[name] = values
	}
	resp, err := rawClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body strings.Builder
	_, err = io.Copy(&body, resp.Body)
	require.NoError(t, err)
	return serviceAnswer{status: resp.StatusCode, header: resp.Header, body: body.String()}
}
