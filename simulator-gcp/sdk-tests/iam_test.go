package gcp_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

func iamService(t *testing.T) *iam.Service {
	t.Helper()
	svc, err := iam.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// requireGoogleAPIRefusal holds an error to the refusal the service makes: its
// HTTP status, and a fragment of the message that names the reason. A bare
// "an error happened" is also satisfied by a transport fault, a 500, or a
// response the client could not decode, none of which shows the service
// validated anything.
func requireGoogleAPIRefusal(t *testing.T, err error, status int, contains, what string) {
	t.Helper()
	require.Error(t, err, "%s must be refused", what)
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr, "%s must fail with a Google API error, got %v", what, err)
	require.Equal(t, status, apiErr.Code, "%s must be refused with %d, got %v", what, status, err)
	if contains != "" {
		require.Contains(t, apiErr.Message, contains, "%s must say why", what)
	}
}

func TestIAM_CreateServiceAccount(t *testing.T) {
	svc := iamService(t)

	sa, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "test-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "Test Service Account",
			},
		}).Do()
	require.NoError(t, err)
	assert.Contains(t, sa.Email, "test-sa")
	assert.Contains(t, sa.Name, "test-sa")
}

// TestIAM_CreateServiceAccountDuplicateConflict pins that creating a second
// service account under an accountId already in use within the project
// returns 409 ALREADY_EXISTS like real Cloud IAM, instead of silently
// overwriting the existing account.
func TestIAM_CreateServiceAccountDuplicateConflict(t *testing.T) {
	svc := iamService(t)

	sa, err := svc.Projects.ServiceAccounts.Create("projects/dup-sa-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "dup-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "Original",
			},
		}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.ServiceAccounts.Create("projects/dup-sa-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "dup-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "Duplicate",
			},
		}).Do()
	requireGoogleErr(t, err, 409, "ALREADY_EXISTS")

	// The rejected duplicate must not have overwritten the existing account.
	got, err := svc.Projects.ServiceAccounts.Get(sa.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Original", got.DisplayName, "duplicate create must not overwrite the existing account")
}

func TestIAM_GetServiceAccount(t *testing.T) {
	svc := iamService(t)

	created, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "get-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "Get SA",
			},
		}).Do()
	require.NoError(t, err)

	got, err := svc.Projects.ServiceAccounts.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Email, got.Email)
}

// iamAccountNames reduces a serviceAccounts.list response to its resource names.
func iamAccountNames(resp *iam.ListServiceAccountsResponse) []string {
	names := make([]string, 0, len(resp.Accounts))
	for _, a := range resp.Accounts {
		names = append(names, a.Name)
	}
	return names
}

func TestIAM_ListServiceAccounts(t *testing.T) {
	svc := iamService(t)

	created, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "list-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "List SA",
			},
		}).Do()
	require.NoError(t, err)

	resp, err := svc.Projects.ServiceAccounts.List("projects/test-project").Do()
	require.NoError(t, err)
	assert.Contains(t, iamAccountNames(resp), created.Name,
		"the service account just created must be listed")
}

// TestIAM_ListServiceAccountsPagination covers the pageSize/pageToken support
// ListServiceAccounts previously dropped (it returned the whole set, no token).
func TestIAM_ListServiceAccountsPagination(t *testing.T) {
	svc := iamService(t)
	const project = "projects/iam-page-project"
	for _, id := range []string{"sa-a", "sa-b", "sa-c"} {
		_, err := svc.Projects.ServiceAccounts.Create(project,
			&iam.CreateServiceAccountRequest{
				AccountId:      id,
				ServiceAccount: &iam.ServiceAccount{DisplayName: id},
			}).Do()
		require.NoError(t, err)
	}

	page1, err := svc.Projects.ServiceAccounts.List(project).PageSize(2).Do()
	require.NoError(t, err)
	require.Len(t, page1.Accounts, 2)
	require.NotEmpty(t, page1.NextPageToken, "pageSize=2 over 3 accounts → NextPageToken")

	page2, err := svc.Projects.ServiceAccounts.List(project).PageSize(2).PageToken(page1.NextPageToken).Do()
	require.NoError(t, err)
	assert.Len(t, page2.Accounts, 1)
}

func TestIAM_DeleteServiceAccount(t *testing.T) {
	svc := iamService(t)

	created, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "del-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "Delete SA",
			},
		}).Do()
	require.NoError(t, err)

	// The account is readable and listed before the delete, so its absence
	// afterwards is the delete's doing and not a create that never landed.
	before, err := svc.Projects.ServiceAccounts.List("projects/test-project").Do()
	require.NoError(t, err)
	require.Contains(t, iamAccountNames(before), created.Name)

	_, err = svc.Projects.ServiceAccounts.Delete(created.Name).Do()
	require.NoError(t, err)

	_, err = svc.Projects.ServiceAccounts.Get(created.Name).Do()
	requireGoogleErr(t, err, 404, "NOT_FOUND")

	after, err := svc.Projects.ServiceAccounts.List("projects/test-project").Do()
	require.NoError(t, err)
	assert.NotContains(t, iamAccountNames(after), created.Name,
		"a deleted service account must be gone from the list")
}

// iamCredentialsService points the iamcredentials SDK at the sim. The
// :generateIdToken endpoint shares the same sim handler as
// :generateAccessToken — both routed via the {emailAction} path-param
// switch in simulator-gcp/iam.go.
func iamCredentialsService(t *testing.T) *iamcredentials.Service {
	t.Helper()
	svc, err := iamcredentials.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestIAMCredentials_GenerateIdToken — Access driver's id-token category.
// Cross-Service auth chains call generateIdToken with a target audience
// (the receiving service's URL); the mint returns a JWT whose `aud`
// claim equals that audience. The sim signs an RS256 token with the same
// key its published JWKS advertises, matching a real Google ID token, so
// SDKs that pre-decode the token accept its structure and a verifier can
// check its signature.
func TestIAMCredentials_GenerateIdToken(t *testing.T) {
	iamSvc := iamService(t)
	credSvc := iamCredentialsService(t)

	created, err := iamSvc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId: "id-token-sa",
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "ID-token SA",
			},
		}).Do()
	require.NoError(t, err)

	resp, err := credSvc.Projects.ServiceAccounts.GenerateIdToken(created.Name,
		&iamcredentials.GenerateIdTokenRequest{
			Audience:     "https://target-svc.run.app",
			IncludeEmail: true,
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.Token, "expected non-empty ID token")

	// JWT has 3 base64url segments; the middle is the claims set.
	parts := strings.Split(resp.Token, ".")
	require.Len(t, parts, 3, "ID token must be a 3-segment JWT")

	// Real Google ID tokens are RS256, verifiable against Google's JWKS; the
	// sim matches, signing with the key its /.well-known/jwks.json advertises.
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var hdr struct {
		Alg string `json:"alg"`
	}
	require.NoError(t, json.Unmarshal(header, &hdr))
	assert.Equal(t, "RS256", hdr.Alg, "ID token must be RS256, not HS256")

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Equal(t, "https://target-svc.run.app", claims["aud"], "aud claim must match request audience")
	assert.Equal(t, created.Email, claims["sub"], "sub claim must match SA email")
	assert.Equal(t, created.Email, claims["email"], "email claim must be present when includeEmail=true")
}

func TestIAMCredentials_GenerateIdToken_RejectsUnknownSA(t *testing.T) {
	credSvc := iamCredentialsService(t)
	_, err := credSvc.Projects.ServiceAccounts.GenerateIdToken(
		"projects/test-project/serviceAccounts/missing@test-project.iam.gserviceaccount.com",
		&iamcredentials.GenerateIdTokenRequest{Audience: "https://x"},
	).Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotFound, "not found",
		"an identity token for a service account that does not exist")
}

func TestIAMCredentials_GenerateIdToken_RequiresAudience(t *testing.T) {
	iamSvc := iamService(t)
	credSvc := iamCredentialsService(t)

	created, err := iamSvc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "no-aud-sa",
			ServiceAccount: &iam.ServiceAccount{},
		}).Do()
	require.NoError(t, err)

	_, err = credSvc.Projects.ServiceAccounts.GenerateIdToken(created.Name,
		&iamcredentials.GenerateIdTokenRequest{}, // no audience
	).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "audience is required",
		"an identity token with no audience")
}

func TestIAM_ServiceAccountKeysCRUD(t *testing.T) {
	svc := iamService(t)

	// Create a parent service account.
	sa, err := svc.Projects.ServiceAccounts.Create("projects/sa-keys-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "key-owner-sa",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Key Owner"},
		}).Do()
	require.NoError(t, err)

	// Create a key — response must include privateKeyData.
	key, err := svc.Projects.ServiceAccounts.Keys.Create(sa.Name,
		&iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, key.Name, "key name must be set")
	require.NotEmpty(t, key.PrivateKeyData, "privateKeyData must be non-empty on create")
	assert.Equal(t, "KEY_ALG_RSA_2048", key.KeyAlgorithm)

	// Decode privateKeyData: must be base64-encoded JSON with correct fields.
	raw, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	require.NoError(t, err)
	var keyFile map[string]string
	require.NoError(t, json.Unmarshal(raw, &keyFile))
	assert.Equal(t, "service_account", keyFile["type"])
	assert.NotEmpty(t, keyFile["private_key"])
	assert.Equal(t, "sa-keys-project", keyFile["project_id"])

	// Extract key ID from the resource name (…/keys/{keyId}).
	keyID := key.Name[strings.LastIndex(key.Name, "/")+1:]

	// Get key — no privateKeyData.
	got, err := svc.Projects.ServiceAccounts.Keys.Get(key.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, key.Name, got.Name)
	assert.Empty(t, got.PrivateKeyData, "GET must not return privateKeyData")

	// List keys — must include the created key.
	list, err := svc.Projects.ServiceAccounts.Keys.List(sa.Name).Do()
	require.NoError(t, err)
	found := false
	for _, k := range list.Keys {
		if strings.HasSuffix(k.Name, "/"+keyID) {
			found = true
		}
	}
	assert.True(t, found, "created key must appear in list")

	// Delete the key.
	_, err = svc.Projects.ServiceAccounts.Keys.Delete(key.Name).Do()
	require.NoError(t, err)

	// Get after delete must fail.
	_, err = svc.Projects.ServiceAccounts.Keys.Get(key.Name).Do()
	require.Error(t, err)
}

// TestIAM_ServiceAccountUniqueIdNumeric — real GCP uniqueId is a numeric
// principal identifier (a long decimal string), not a hex UUID. A consumer
// that parses uniqueId as a number must not choke on hex digits.
func TestIAM_ServiceAccountUniqueIdNumeric(t *testing.T) {
	svc := iamService(t)

	sa, err := svc.Projects.ServiceAccounts.Create("projects/uid-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "uid-sa",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "UID SA"},
		}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, sa.UniqueId)
	for _, c := range sa.UniqueId {
		require.True(t, c >= '0' && c <= '9', "uniqueId must be all decimal digits, got %q", sa.UniqueId)
	}
	require.GreaterOrEqual(t, len(sa.UniqueId), 19, "numeric uniqueId is ~21 digits")
	require.NotEqual(t, '0', rune(sa.UniqueId[0]), "no leading zero")

	// The SA-key JSON client_id must equal the SA uniqueId, not a fresh value.
	key, err := svc.Projects.ServiceAccounts.Keys.Create(sa.Name,
		&iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	require.NoError(t, err)
	var keyFile map[string]string
	require.NoError(t, json.Unmarshal(raw, &keyFile))
	assert.Equal(t, sa.UniqueId, keyFile["client_id"], "key client_id must equal SA uniqueId")
}

// TestIAM_PredefinedRolesGetList exercises GET /v1/roles and
// GET /v1/roles/{role...}. roles.list defaults to the BASIC view (no
// includedPermissions); roles.get returns the FULL role.
func TestIAM_PredefinedRolesGetList(t *testing.T) {
	svc := iamService(t)

	list, err := svc.Roles.List().Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Roles)
	names := map[string]bool{}
	for _, role := range list.Roles {
		names[role.Name] = true
		assert.Empty(t, role.IncludedPermissions, "BASIC view omits includedPermissions")
		assert.Equal(t, "GA", role.Stage)
	}
	for _, want := range []string{"roles/viewer", "roles/editor", "roles/owner"} {
		assert.True(t, names[want], "list must include %s", want)
	}

	got, err := svc.Roles.Get("roles/viewer").Do()
	require.NoError(t, err)
	assert.Equal(t, "roles/viewer", got.Name)
	assert.Equal(t, "Viewer", got.Title)
	assert.Equal(t, "GA", got.Stage)
	assert.NotEmpty(t, got.Etag)
	assert.NotEmpty(t, got.IncludedPermissions, "roles.get returns FULL view")

	// Stable etag across reads (predefined roles are immutable).
	got2, err := svc.Roles.Get("roles/viewer").Do()
	require.NoError(t, err)
	assert.Equal(t, got.Etag, got2.Etag, "predefined-role etag is stable")

	_, err = svc.Roles.Get("roles/does.not.exist").Do()
	require.Error(t, err, "unknown role must 404")

	// Also exercise the raw GET /v1/roles and GET /v1/roles/{role...} wire paths
	// directly so the route's literal path is covered end-to-end.
	resp, err := http.Get(baseURL + "/v1/roles")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(baseURL + "/v1/roles/viewer")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func crmService(t *testing.T) *cloudresourcemanager.Service {
	t.Helper()
	svc, err := cloudresourcemanager.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// ensureV1Project creates the project a test's IAM verbs address — the real
// API answers 403 PERMISSION_DENIED for a project that does not exist.
func ensureV1Project(t *testing.T, svc *cloudresourcemanager.Service, projectID string) {
	t.Helper()
	_, err := svc.Projects.Create(&cloudresourcemanager.Project{ProjectId: projectID}).Do()
	require.NoError(t, err)
}

func TestIAM_ProjectIAMPolicy(t *testing.T) {
	svc := crmService(t)
	project := "iam-policy-project"
	ensureV1Project(t, svc, project)

	// GetIamPolicy on a fresh project returns empty bindings.
	policy, err := svc.Projects.GetIamPolicy(project,
		&cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, policy.Etag)
	assert.Empty(t, policy.Bindings, "a project nobody has granted a role on has no bindings")

	// SetIamPolicy — add a binding.
	updated, err := svc.Projects.SetIamPolicy(project,
		&cloudresourcemanager.SetIamPolicyRequest{
			Policy: &cloudresourcemanager.Policy{
				Bindings: []*cloudresourcemanager.Binding{
					{
						Role:    "roles/viewer",
						Members: []string{"serviceAccount:robot@iam-policy-project.iam.gserviceaccount.com"},
					},
				},
			},
		}).Do()
	require.NoError(t, err)
	require.Len(t, updated.Bindings, 1)
	assert.Equal(t, "roles/viewer", updated.Bindings[0].Role)

	// GetIamPolicy must reflect the set policy.
	got, err := svc.Projects.GetIamPolicy(project,
		&cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Equal(t, "roles/viewer", got.Bindings[0].Role)
	assert.Contains(t, got.Bindings[0].Members, "serviceAccount:robot@iam-policy-project.iam.gserviceaccount.com")
}

// TestIAM_ProjectIAMPolicyEtagConflict — setIamPolicy with a stale etag must be
// rejected (409 ABORTED) so the caller re-reads and retries. An empty etag is a
// blind overwrite, which is allowed.
func TestIAM_ProjectIAMPolicyEtagConflict(t *testing.T) {
	svc := crmService(t)
	project := "iam-etag-project"
	ensureV1Project(t, svc, project)

	cur, err := svc.Projects.GetIamPolicy(project,
		&cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, cur.Etag)
	// The etag must be stable across reads (default policy is persisted).
	cur2, err := svc.Projects.GetIamPolicy(project,
		&cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, cur.Etag, cur2.Etag, "default-policy etag must persist across reads")

	// Set with the current etag → succeeds, etag rotates.
	updated, err := svc.Projects.SetIamPolicy(project,
		&cloudresourcemanager.SetIamPolicyRequest{
			Policy: &cloudresourcemanager.Policy{
				Etag: cur.Etag,
				Bindings: []*cloudresourcemanager.Binding{
					{Role: "roles/viewer", Members: []string{"user:dev@example.com"}},
				},
			},
		}).Do()
	require.NoError(t, err)
	require.NotEqual(t, cur.Etag, updated.Etag, "etag must rotate on a successful set")

	// Re-set with the now-stale etag → 409 ABORTED.
	_, err = svc.Projects.SetIamPolicy(project,
		&cloudresourcemanager.SetIamPolicyRequest{
			Policy: &cloudresourcemanager.Policy{
				Etag:     cur.Etag, // stale
				Bindings: []*cloudresourcemanager.Binding{{Role: "roles/editor", Members: []string{"user:x@example.com"}}},
			},
		}).Do()
	require.Error(t, err, "stale etag must be rejected")

	// Empty etag → blind overwrite, allowed.
	_, err = svc.Projects.SetIamPolicy(project,
		&cloudresourcemanager.SetIamPolicyRequest{
			Policy: &cloudresourcemanager.Policy{
				Bindings: []*cloudresourcemanager.Binding{{Role: "roles/editor", Members: []string{"user:y@example.com"}}},
			},
		}).Do()
	require.NoError(t, err, "empty etag is a permitted blind overwrite")
}

// TestIAM_ProjectIAMPolicyInvalidMember — a member without a valid type prefix
// (or a typed email member with no domain) must be rejected with 400
// INVALID_ARGUMENT, matching real Cloud IAM.
func TestIAM_ProjectIAMPolicyInvalidMember(t *testing.T) {
	svc := crmService(t)
	project := "iam-bad-member-project"
	ensureV1Project(t, svc, project)

	for _, bad := range []string{
		"robot@example.com",     // no type prefix
		"bogus:dev@example.com", // unknown prefix
		"user:bob",              // email with no domain
		"user:",                 // empty identifier
	} {
		_, err := svc.Projects.SetIamPolicy(project,
			&cloudresourcemanager.SetIamPolicyRequest{
				Policy: &cloudresourcemanager.Policy{
					Bindings: []*cloudresourcemanager.Binding{{Role: "roles/viewer", Members: []string{bad}}},
				},
			}).Do()
		requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "invalid member",
			fmt.Sprintf("the member %q", bad))
	}

	// Legitimate members (including the bare allUsers/allAuthenticatedUsers)
	// must be accepted.
	_, err := svc.Projects.SetIamPolicy(project,
		&cloudresourcemanager.SetIamPolicyRequest{
			Policy: &cloudresourcemanager.Policy{
				Bindings: []*cloudresourcemanager.Binding{{
					Role: "roles/viewer",
					Members: []string{
						"user:dev@example.com",
						"serviceAccount:robot@iam-bad-member-project.iam.gserviceaccount.com",
						"group:admins@example.com",
						"domain:example.com",
						"allUsers",
						"allAuthenticatedUsers",
					},
				}},
			},
		}).Do()
	require.NoError(t, err, "legitimate members must be accepted")
}

// TestIAM_CustomRoleCRUD covers projects.roles create/get/list/patch(etag)/
// delete/undelete — the full custom-role lifecycle.
func TestIAM_CustomRoleCRUD(t *testing.T) {
	svc := iamService(t)
	const parent = "projects/custom-role-project"

	created, err := svc.Projects.Roles.Create(parent, &iam.CreateRoleRequest{
		RoleId: "myCustomRole",
		Role: &iam.Role{
			Title:               "My Custom Role",
			Description:         "A custom role for testing",
			IncludedPermissions: []string{"storage.objects.get", "storage.objects.list"},
			Stage:               "GA",
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, parent+"/roles/myCustomRole", created.Name)
	assert.Equal(t, "My Custom Role", created.Title)
	assert.ElementsMatch(t, []string{"storage.objects.get", "storage.objects.list"}, created.IncludedPermissions)
	assert.NotEmpty(t, created.Etag)

	// Duplicate create → 409 ALREADY_EXISTS.
	_, err = svc.Projects.Roles.Create(parent, &iam.CreateRoleRequest{
		RoleId: "myCustomRole",
		Role:   &iam.Role{Title: "dup"},
	}).Do()
	require.Error(t, err, "duplicate roleId must conflict")

	// Get.
	got, err := svc.Projects.Roles.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, got.Name)
	assert.NotEmpty(t, got.IncludedPermissions, "roles.get returns FULL view")

	// Get unknown → 404.
	_, err = svc.Projects.Roles.Get(parent + "/roles/nope").Do()
	require.Error(t, err)

	// List (BASIC view omits includedPermissions).
	list, err := svc.Projects.Roles.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, role := range list.Roles {
		if role.Name == created.Name {
			found = true
			assert.Empty(t, role.IncludedPermissions, "list BASIC view omits permissions")
		}
	}
	assert.True(t, found, "created role must appear in list")

	// Patch with the current etag → succeeds, etag rotates.
	updated, err := svc.Projects.Roles.Patch(created.Name, &iam.Role{
		Etag:        got.Etag,
		Title:       "Renamed Role",
		Description: "Updated description",
	}).UpdateMask("title,description").Do()
	require.NoError(t, err)
	assert.Equal(t, "Renamed Role", updated.Title)
	assert.Equal(t, "Updated description", updated.Description)
	require.NotEqual(t, got.Etag, updated.Etag, "etag must rotate on update")
	// Fields outside the mask are untouched.
	assert.ElementsMatch(t, []string{"storage.objects.get", "storage.objects.list"}, updated.IncludedPermissions)

	// Patch with a stale etag → 409 ABORTED.
	_, err = svc.Projects.Roles.Patch(created.Name, &iam.Role{
		Etag:  got.Etag, // stale
		Title: "should fail",
	}).UpdateMask("title").Do()
	require.Error(t, err, "stale etag must be rejected")

	// Delete (soft-delete) → role returned with deleted=true.
	deleted, err := svc.Projects.Roles.Delete(created.Name).Do()
	require.NoError(t, err)
	assert.True(t, deleted.Deleted, "delete soft-deletes the role")

	// Undelete revives the role.
	revived, err := svc.Projects.Roles.Undelete(created.Name, &iam.UndeleteRoleRequest{}).Do()
	require.NoError(t, err)
	assert.False(t, revived.Deleted, "undelete clears the deleted flag")
}

// TestIAM_OrganizationCustomRole covers the organizations.roles mirror of the
// custom-role surface.
func TestIAM_OrganizationCustomRole(t *testing.T) {
	svc := iamService(t)
	const parent = "organizations/123456"

	created, err := svc.Organizations.Roles.Create(parent, &iam.CreateRoleRequest{
		RoleId: "orgRole",
		Role: &iam.Role{
			Title:               "Org Role",
			IncludedPermissions: []string{"resourcemanager.projects.get"},
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, parent+"/roles/orgRole", created.Name)

	got, err := svc.Organizations.Roles.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, got.Name)
	assert.NotEmpty(t, got.IncludedPermissions)

	list, err := svc.Organizations.Roles.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Roles)
}

// TestIAM_ServiceAccountAsResourceIAM covers the SA-as-resource IAM triple:
// getIamPolicy / setIamPolicy / testIamPermissions on a service account.
func TestIAM_ServiceAccountAsResourceIAM(t *testing.T) {
	svc := iamService(t)

	sa, err := svc.Projects.ServiceAccounts.Create("projects/sa-iam-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "sa-iam-target",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "SA IAM Target"},
		}).Do()
	require.NoError(t, err)

	// getIamPolicy on a fresh SA → empty bindings, stable etag.
	pol, err := svc.Projects.ServiceAccounts.GetIamPolicy(sa.Name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, pol.Etag)
	require.Empty(t, pol.Bindings, "a service account nobody has granted a role on has no bindings")
	reread, err := svc.Projects.ServiceAccounts.GetIamPolicy(sa.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, pol.Etag, reread.Etag, "the default policy's etag must persist across reads")

	// setIamPolicy — grant serviceAccountUser to a member.
	set, err := svc.Projects.ServiceAccounts.SetIamPolicy(sa.Name,
		&iam.SetIamPolicyRequest{
			Policy: &iam.Policy{
				Etag: pol.Etag,
				Bindings: []*iam.Binding{{
					Role:    "roles/iam.serviceAccountUser",
					Members: []string{"user:dev@example.com"},
				}},
			},
		}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	require.NotEqual(t, pol.Etag, set.Etag, "etag rotates on set")

	// getIamPolicy reflects the binding and round-trips the etag.
	got, err := svc.Projects.ServiceAccounts.GetIamPolicy(sa.Name).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Equal(t, "roles/iam.serviceAccountUser", got.Bindings[0].Role)
	assert.Equal(t, set.Etag, got.Etag, "stored etag must round-trip")

	// Stale-etag set → 409 ABORTED.
	_, err = svc.Projects.ServiceAccounts.SetIamPolicy(sa.Name,
		&iam.SetIamPolicyRequest{
			Policy: &iam.Policy{Etag: pol.Etag}, // stale
		}).Do()
	require.Error(t, err, "stale etag must be rejected")

	// testIamPermissions — admin caller echoes the requested set.
	perms, err := svc.Projects.ServiceAccounts.TestIamPermissions(sa.Name,
		&iam.TestIamPermissionsRequest{
			Permissions: []string{"iam.serviceAccounts.actAs", "iam.serviceAccounts.get"},
		}).Do()
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"iam.serviceAccounts.actAs", "iam.serviceAccounts.get"},
		perms.Permissions)

	// getIamPolicy on a missing SA → 404.
	_, err = svc.Projects.ServiceAccounts.GetIamPolicy(
		"projects/sa-iam-project/serviceAccounts/ghost@sa-iam-project.iam.gserviceaccount.com").Do()
	require.Error(t, err)
}

// TestIAM_ServiceAccountDisableEnablePatch covers :disable, :enable, and PATCH
// (UpdateServiceAccount over displayName/description).
//
// Routes exercised here and by the custom-role tests:
//
//	PATCH /v1/projects/{project}/serviceAccounts/{email}
//	POST /v1/permissions:queryTestablePermissions
func TestIAM_ServiceAccountDisableEnablePatch(t *testing.T) {
	svc := iamService(t)

	sa, err := svc.Projects.ServiceAccounts.Create("projects/sa-toggle-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "toggle-sa",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Toggle SA"},
		}).Do()
	require.NoError(t, err)
	assert.False(t, sa.Disabled)

	_, err = svc.Projects.ServiceAccounts.Disable(sa.Name, &iam.DisableServiceAccountRequest{}).Do()
	require.NoError(t, err)
	got, err := svc.Projects.ServiceAccounts.Get(sa.Name).Do()
	require.NoError(t, err)
	assert.True(t, got.Disabled, "disable sets Disabled=true")

	_, err = svc.Projects.ServiceAccounts.Enable(sa.Name, &iam.EnableServiceAccountRequest{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.ServiceAccounts.Get(sa.Name).Do()
	require.NoError(t, err)
	assert.False(t, got.Disabled, "enable sets Disabled=false")

	// PATCH displayName + description via updateMask.
	patched, err := svc.Projects.ServiceAccounts.Patch(sa.Name,
		&iam.PatchServiceAccountRequest{
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "Renamed SA",
				Description: "patched description",
			},
			UpdateMask: "displayName,description",
		}).Do()
	require.NoError(t, err)
	assert.Equal(t, "Renamed SA", patched.DisplayName)
	assert.Equal(t, "patched description", patched.Description)

	// Patch on a missing SA → 404.
	_, err = svc.Projects.ServiceAccounts.Patch(
		"projects/sa-toggle-project/serviceAccounts/ghost@sa-toggle-project.iam.gserviceaccount.com",
		&iam.PatchServiceAccountRequest{
			ServiceAccount: &iam.ServiceAccount{DisplayName: "x"},
			UpdateMask:     "displayName",
		}).Do()
	require.Error(t, err)
}

// TestIAM_ServiceAccountUpdatePut exercises the legacy full-replace update
// (serviceAccounts.update → PUT). Real GCP replaces the mutable fields from the
// body without an updateMask.
func TestIAM_ServiceAccountUpdatePut(t *testing.T) {
	svc := iamService(t)

	sa, err := svc.Projects.ServiceAccounts.Create("projects/sa-put-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "put-sa",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Original"},
		}).Do()
	require.NoError(t, err)

	updated, err := svc.Projects.ServiceAccounts.Update(sa.Name,
		&iam.ServiceAccount{DisplayName: "Replaced", Description: "via PUT"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "Replaced", updated.DisplayName)
	assert.Equal(t, "via PUT", updated.Description)

	got, err := svc.Projects.ServiceAccounts.Get(sa.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Replaced", got.DisplayName)
}

// iamPoolFromOp decodes the WorkloadIdentityPool embedded in a settled LRO
// response. The sim settles every pool operation synchronously (done=true) with
// the resource as the protobuf-Any response, so the resource is readable
// straight off the returned Operation.
func iamPoolFromOp(t *testing.T, op *iam.Operation) iam.WorkloadIdentityPool {
	t.Helper()
	require.True(t, op.Done, "operation must settle synchronously")
	var pool iam.WorkloadIdentityPool
	require.NoError(t, json.Unmarshal(op.Response, &pool))
	return pool
}

// TestIAM_WorkloadIdentityPoolCRUD round-trips a Workload Identity Federation
// pool and a provider beneath it: create (LRO) → get → list → patch → delete →
// undelete, plus the providers sub-collection.
func TestIAM_WorkloadIdentityPoolCRUD(t *testing.T) {
	svc := iamService(t)
	parent := "projects/wif-project/locations/global"

	// Create pool → settled LRO carrying the resource.
	op, err := svc.Projects.Locations.WorkloadIdentityPools.Create(parent,
		&iam.WorkloadIdentityPool{DisplayName: "CI Pool", Description: "github actions"}).
		WorkloadIdentityPoolId("github-pool").Do()
	require.NoError(t, err)
	pool := iamPoolFromOp(t, op)
	assert.Equal(t, parent+"/workloadIdentityPools/github-pool", pool.Name)
	assert.Equal(t, "CI Pool", pool.DisplayName)
	assert.Equal(t, "ACTIVE", pool.State)

	name := parent + "/workloadIdentityPools/github-pool"

	// Get.
	got, err := svc.Projects.Locations.WorkloadIdentityPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "github actions", got.Description)

	// List — pool present.
	list, err := svc.Projects.Locations.WorkloadIdentityPools.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, p := range list.WorkloadIdentityPools {
		if p.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created pool must appear in list")

	// Patch displayName.
	pop, err := svc.Projects.Locations.WorkloadIdentityPools.Patch(name,
		&iam.WorkloadIdentityPool{DisplayName: "Renamed Pool"}).UpdateMask("displayName").Do()
	require.NoError(t, err)
	require.True(t, pop.Done)
	got, err = svc.Projects.Locations.WorkloadIdentityPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Renamed Pool", got.DisplayName)

	// Create a provider beneath the pool.
	prov, err := svc.Projects.Locations.WorkloadIdentityPools.Providers.Create(name,
		&iam.WorkloadIdentityPoolProvider{
			DisplayName:        "GitHub OIDC",
			AttributeCondition: "assertion.repository=='octo/repo'",
			Oidc:               &iam.Oidc{IssuerUri: "https://token.actions.githubusercontent.com"},
		}).WorkloadIdentityPoolProviderId("github-provider").Do()
	require.NoError(t, err)
	require.True(t, prov.Done)
	provName := name + "/providers/github-provider"
	gotProv, err := svc.Projects.Locations.WorkloadIdentityPools.Providers.Get(provName).Do()
	require.NoError(t, err)
	assert.Equal(t, provName, gotProv.Name)
	assert.Equal(t, "GitHub OIDC", gotProv.DisplayName)

	// Delete pool → soft-delete (state=DELETED).
	dop, err := svc.Projects.Locations.WorkloadIdentityPools.Delete(name).Do()
	require.NoError(t, err)
	require.True(t, dop.Done)
	got, err = svc.Projects.Locations.WorkloadIdentityPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETED", got.State, "delete soft-deletes the pool")

	// Undelete → back to ACTIVE.
	uop, err := svc.Projects.Locations.WorkloadIdentityPools.Undelete(name,
		&iam.UndeleteWorkloadIdentityPoolRequest{}).Do()
	require.NoError(t, err)
	require.True(t, uop.Done)
	got, err = svc.Projects.Locations.WorkloadIdentityPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.State, "undelete revives the pool")
}

// TestIAM_WorkforcePoolCRUD round-trips a location-scoped workforce pool and a
// provider beneath it: create (LRO) → get → list → delete → undelete.
func TestIAM_WorkforcePoolCRUD(t *testing.T) {
	svc := iamService(t)
	location := "locations/global"

	op, err := svc.Locations.WorkforcePools.Create(location,
		&iam.WorkforcePool{DisplayName: "Staff Pool", Parent: "organizations/123"}).
		WorkforcePoolId("staff-pool").Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	name := location + "/workforcePools/staff-pool"
	got, err := svc.Locations.WorkforcePools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "Staff Pool", got.DisplayName)
	assert.Equal(t, "ACTIVE", got.State)

	list, err := svc.Locations.WorkforcePools.List(location).Do()
	require.NoError(t, err)
	found := false
	for _, p := range list.WorkforcePools {
		if p.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created workforce pool must appear in list")

	// Provider beneath the pool.
	prov, err := svc.Locations.WorkforcePools.Providers.Create(name,
		&iam.WorkforcePoolProvider{
			DisplayName: "Okta",
			Oidc:        &iam.GoogleIamAdminV1WorkforcePoolProviderOidc{IssuerUri: "https://okta.example.com"},
		}).WorkforcePoolProviderId("okta").Do()
	require.NoError(t, err)
	require.True(t, prov.Done)
	provName := name + "/providers/okta"
	gotProv, err := svc.Locations.WorkforcePools.Providers.Get(provName).Do()
	require.NoError(t, err)
	assert.Equal(t, provName, gotProv.Name)

	// Delete (soft) → undelete.
	dop, err := svc.Locations.WorkforcePools.Delete(name).Do()
	require.NoError(t, err)
	require.True(t, dop.Done)
	got, err = svc.Locations.WorkforcePools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETED", got.State)

	uop, err := svc.Locations.WorkforcePools.Undelete(name, &iam.UndeleteWorkforcePoolRequest{}).Do()
	require.NoError(t, err)
	require.True(t, uop.Done)
	got, err = svc.Locations.WorkforcePools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.State)
}

// TestIAM_OAuthClientCRUD round-trips an OAuth client and a credential beneath
// it. Unlike pools, oauthClients return the resource directly (no LRO); a
// created credential carries a clientSecret.
func TestIAM_OAuthClientCRUD(t *testing.T) {
	svc := iamService(t)
	parent := "projects/oauth-project/locations/global"

	client, err := svc.Projects.Locations.OauthClients.Create(parent,
		&iam.OauthClient{
			DisplayName:         "CLI Client",
			ClientType:          "CONFIDENTIAL_CLIENT",
			AllowedGrantTypes:   []string{"AUTHORIZATION_CODE_GRANT"},
			AllowedScopes:       []string{"https://www.googleapis.com/auth/cloud-platform"},
			AllowedRedirectUris: []string{"https://example.com/callback"},
		}).OauthClientId("cli-client").Do()
	require.NoError(t, err)
	name := parent + "/oauthClients/cli-client"
	assert.Equal(t, name, client.Name)
	assert.Equal(t, "cli-client", client.ClientId)
	assert.Equal(t, "ACTIVE", client.State)

	got, err := svc.Projects.Locations.OauthClients.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "CLI Client", got.DisplayName)

	list, err := svc.Projects.Locations.OauthClients.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, c := range list.OauthClients {
		if c.Name == name {
			found = true
		}
	}
	assert.True(t, found, "created oauth client must appear in list")

	// Credential — create returns a clientSecret.
	cred, err := svc.Projects.Locations.OauthClients.Credentials.Create(name,
		&iam.OauthClientCredential{DisplayName: "primary"}).
		OauthClientCredentialId("primary").Do()
	require.NoError(t, err)
	credName := name + "/credentials/primary"
	assert.Equal(t, credName, cred.Name)
	assert.NotEmpty(t, cred.ClientSecret, "credential create must return a clientSecret")

	gotCred, err := svc.Projects.Locations.OauthClients.Credentials.Get(credName).Do()
	require.NoError(t, err)
	assert.Equal(t, "primary", gotCred.DisplayName)

	_, err = svc.Projects.Locations.OauthClients.Credentials.Delete(credName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.OauthClients.Credentials.Get(credName).Do()
	require.Error(t, err, "credential get after delete must fail")

	// Delete client → soft-delete → undelete.
	_, err = svc.Projects.Locations.OauthClients.Delete(name).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Locations.OauthClients.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETED", got.State)

	_, err = svc.Projects.Locations.OauthClients.Undelete(name, &iam.UndeleteOauthClientRequest{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Locations.OauthClients.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.State)
}

// TestIAMCredentials_GenerateAccessTokenHonoursTheRequestedLifetime drives the
// `lifetime` member of GenerateAccessTokenRequest through the official SDK.
// The rule is the iamcredentials v1 Discovery document's own, verbatim:
//
//	The desired lifetime duration of the access token in seconds. By default,
//	the maximum allowed value is 1 hour. To set a lifetime of up to 12 hours,
//	you can add the service account as an allowed value in an Organization
//	Policy that enforces the
//	`constraints/iam.allowServiceAccountCredentialLifetimeExtension`
//	constraint. […] If a value is not specified, the token's lifetime will be
//	set to a default value of 1 hour.
//
// so expireTime must track what the request asked for, the default must be an
// hour, and anything past the ceiling in force must be refused.
func TestIAMCredentials_GenerateAccessTokenHonoursTheRequestedLifetime(t *testing.T) {
	iamSvc := iamService(t)
	credSvc := iamCredentialsService(t)

	created, err := iamSvc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{AccountId: "lifetime-sa"}).Do()
	require.NoError(t, err)

	scope := []string{"https://www.googleapis.com/auth/cloud-platform"}
	for _, tc := range []struct {
		name     string
		lifetime string
		want     time.Duration
	}{
		{name: "unspecified takes the documented one-hour default", lifetime: "", want: time.Hour},
		{name: "a short lifetime is honoured", lifetime: "60s", want: time.Minute},
		{name: "a one-second lifetime is honoured", lifetime: "1s", want: time.Second},
		{name: "the default maximum is honoured", lifetime: "3600s", want: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			resp, err := credSvc.Projects.ServiceAccounts.GenerateAccessToken(created.Name,
				&iamcredentials.GenerateAccessTokenRequest{Scope: scope, Lifetime: tc.lifetime}).Do()
			require.NoError(t, err)
			require.NotEmpty(t, resp.AccessToken)

			expiry, err := time.Parse(time.RFC3339, resp.ExpireTime)
			require.NoError(t, err, "expireTime is RFC3339: %q", resp.ExpireTime)
			got := expiry.Sub(before)
			// The tolerance scales with the lifetime and stays well under half
			// the distance to the next candidate in the table, so a response
			// pinned to any other lifetime — the one-hour default above all —
			// fails. A flat 30 seconds would swallow the one-second case whole.
			// The floor of three seconds covers the request round trip plus the
			// second-precision truncation of an RFC3339 expireTime.
			tolerance := math.Max(3, tc.want.Seconds()*0.05)
			assert.InDelta(t, tc.want.Seconds(), got.Seconds(), tolerance,
				"expireTime must track the requested lifetime, not a fixed hour")
		})
	}
}

// TestIAMCredentials_GenerateAccessTokenRefusesAnOverLongLifetime covers the
// ceiling: without the Organization Policy that extends it, a service account
// may not hold a token longer than the documented one-hour default, and twelve
// hours is the ceiling in every case (google-auth-library-java's
// ImpersonatedCredentials refuses locally: "lifetime must be less than or equal
// to 43200").
func TestIAMCredentials_GenerateAccessTokenRefusesAnOverLongLifetime(t *testing.T) {
	iamSvc := iamService(t)
	credSvc := iamCredentialsService(t)

	created, err := iamSvc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{AccountId: "overlong-lifetime-sa"}).Do()
	require.NoError(t, err)

	scope := []string{"https://www.googleapis.com/auth/cloud-platform"}
	for _, lifetime := range []string{"3601s", "43200s", "43201s"} {
		t.Run(lifetime, func(t *testing.T) {
			_, err := credSvc.Projects.ServiceAccounts.GenerateAccessToken(created.Name,
				&iamcredentials.GenerateAccessTokenRequest{Scope: scope, Lifetime: lifetime}).Do()
			require.Error(t, err, "a lifetime past the ceiling must be refused")
			var apiErr *googleapi.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Code)
			assert.Contains(t, apiErr.Message, "3600")
		})
	}
}

// TestIAMCredentials_TwelveHourLifetimeNeedsTheOrganizationPolicy is the
// constraint half of the rule: the same twelve-hour request that the ceiling
// refuses above succeeds once the service account is an allowed value of
// constraints/iam.allowServiceAccountCredentialLifetimeExtension, set through
// Cloud Resource Manager the way an administrator sets it.
func TestIAMCredentials_TwelveHourLifetimeNeedsTheOrganizationPolicy(t *testing.T) {
	iamSvc := iamService(t)
	credSvc := iamCredentialsService(t)
	crm := crmV1Service(t)

	// The policy is set on the project the accounts live in, so the test owns
	// a project of its own rather than leaving a policy on a shared one.
	const projectID = "sdk-token-lifetime"
	resource := orgPolicyProject(t, projectID)

	created, err := iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID,
		&iam.CreateServiceAccountRequest{AccountId: "twelve-hour-sa"}).Do()
	require.NoError(t, err)

	scope := []string{"https://www.googleapis.com/auth/cloud-platform"}
	request := &iamcredentials.GenerateAccessTokenRequest{Scope: scope, Lifetime: "43200s"}

	_, err = credSvc.Projects.ServiceAccounts.GenerateAccessToken(created.Name, request).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "3600",
		"twelve hours before the policy lists the account")

	_, err = crm.Projects.SetOrgPolicy(resource, &cloudresourcemanager.SetOrgPolicyRequest{
		Policy: &cloudresourcemanager.OrgPolicy{
			Constraint: "constraints/iam.allowServiceAccountCredentialLifetimeExtension",
			ListPolicy: &cloudresourcemanager.ListPolicy{AllowedValues: []string{created.Email}},
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = crm.Projects.ClearOrgPolicy(resource, &cloudresourcemanager.ClearOrgPolicyRequest{
			Constraint: "constraints/iam.allowServiceAccountCredentialLifetimeExtension",
		}).Do()
	})

	before := time.Now()
	resp, err := credSvc.Projects.ServiceAccounts.GenerateAccessToken(created.Name, request).Do()
	require.NoError(t, err, "the listed account may hold a twelve-hour token")
	expiry, err := time.Parse(time.RFC3339, resp.ExpireTime)
	require.NoError(t, err)
	assert.InDelta(t, (12 * time.Hour).Seconds(), expiry.Sub(before).Seconds(), 30)

	// An account the policy does not list is still held to the default hour.
	other, err := iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID,
		&iam.CreateServiceAccountRequest{AccountId: "unlisted-lifetime-sa"}).Do()
	require.NoError(t, err)
	_, err = credSvc.Projects.ServiceAccounts.GenerateAccessToken(other.Name, request).Do()
	requireGoogleAPIRefusal(t, err, http.StatusBadRequest, "3600",
		"an account the policy does not list, which keeps the one-hour ceiling")
}
