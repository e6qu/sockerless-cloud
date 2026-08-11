package azure_cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Standard Graph provisioning helpers (endpoint-only, swappable with real cloud) ---

func createEntraGroup(t *testing.T, displayName string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"displayName":     displayName,
		"mailNickname":    displayName,
		"securityEnabled": true,
		"mailEnabled":     false,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/groups", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var grp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grp))
	require.NotEmpty(t, grp.ID)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/groups/"+grp.ID, nil)
		resp, _ := http.DefaultClient.Do(r)
		if resp != nil {
			resp.Body.Close()
		}
	})
	return grp.ID
}

func createEntraUser(t *testing.T, displayName, upn string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"displayName":       displayName,
		"userPrincipalName": upn,
		"mailNickname":      displayName,
		"accountEnabled":    true,
		"passwordProfile":   map[string]any{"password": "Test1234!", "forceChangePasswordNextSignIn": false},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/users", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var u struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&u))
	require.NotEmpty(t, u.ID)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/users/"+u.ID, nil)
		resp, _ := http.DefaultClient.Do(r)
		if resp != nil {
			resp.Body.Close()
		}
	})
	return u.ID
}

func addEntraGroupMember(t *testing.T, groupID, userID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"@odata.id": baseURL + "/v1.0/directoryObjects/" + userID,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/groups/"+groupID+"/members/$ref", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func pkceChallenge(verifier string) string {
	d := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(d[:])
}

// TestEntra_StandardProvisioningAndGraphMemberOf provisions a user and group via
// standard Graph endpoints and verifies GET /v1.0/me/memberOf returns the correct
// groups when the access token is obtained via ROPC.
func TestEntra_StandardProvisioningAndGraphMemberOf(t *testing.T) {
	groupID := createEntraGroup(t, "CLI-Alpha")
	_ = createEntraGroup(t, "CLI-Beta") // second group, user not a member
	userID := createEntraUser(t, "CLI User", "cli-user@example.com")
	addEntraGroupMember(t, groupID, userID)

	// Obtain a Graph-scoped access token via ROPC — standard non-interactive user grant.
	tokenResp, err := http.PostForm(baseURL+"/cli-entra-tenant/oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"cli-client"},
		"username":   {"cli-user@example.com"},
		"password":   {"x"},
		"scope":      {"https://graph.microsoft.com/.default"},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&tok))
	require.NotEmpty(t, tok.AccessToken)

	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1.0/me/memberOf", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	gResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer gResp.Body.Close()
	require.Equal(t, http.StatusOK, gResp.StatusCode)

	raw, err := io.ReadAll(gResp.Body)
	require.NoError(t, err)
	var body struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal(raw, &body), string(raw))
	require.Len(t, body.Value, 1, "user is member of exactly one group")
	assert.Equal(t, groupID, body.Value[0].ID)
}

// TestEntra_IDTokenGroupsViaROPC provisions a user and group via standard Graph
// endpoints and verifies the id_token returned by ROPC carries the groups claim.
func TestEntra_IDTokenGroupsViaROPC(t *testing.T) {
	groupID := createEntraGroup(t, "CLI-Viewers")
	userID := createEntraUser(t, "IDToken ROPC User", "idtoken-ropc@example.com")
	addEntraGroupMember(t, groupID, userID)

	tenant := "cli-idtoken-tenant"
	clientID := "cli-idtoken-client"

	tokenResp, err := http.PostForm(baseURL+"/"+tenant+"/oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {clientID},
		"username":   {"idtoken-ropc@example.com"},
		"password":   {"x"},
		"scope":      {"openid profile email"},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	var tokBody struct {
		IDToken string `json:"id_token"`
	}
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&tokBody))
	require.NotEmpty(t, tokBody.IDToken)

	parts := strings.Split(tokBody.IDToken, ".")
	require.Len(t, parts, 3)
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))

	assert.Equal(t, userID, payload["oid"])
	groupsRaw, ok := payload["groups"]
	require.True(t, ok, "id_token must contain groups claim")
	groups, ok := groupsRaw.([]any)
	require.True(t, ok)
	assert.Contains(t, groups, groupID)
}

func TestEntra_DuplicateUPNRejectedAndROPCClaimsStayDeterministicCLI(t *testing.T) {
	groupID := createEntraGroup(t, "CLI-Duplicate-UPN")
	userID := createEntraUser(t, "CLI Duplicate UPN User", "duplicate-upn-cli@example.com")
	addEntraGroupMember(t, groupID, userID)

	body, _ := json.Marshal(map[string]any{
		"displayName":       "CLI Duplicate UPN User Two",
		"userPrincipalName": "DUPLICATE-UPN-CLI@example.com",
		"mailNickname":      "CLI Duplicate UPN User Two",
		"accountEnabled":    true,
		"passwordProfile":   map[string]any{"password": "Test1234!", "forceChangePasswordNextSignIn": false},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/users", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var errBody struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	assert.Equal(t, "Request_BadRequest", errBody.Error.Code)
	assert.Contains(t, errBody.Error.Message, "userPrincipalName")

	tokenResp, err := http.PostForm(baseURL+"/cli-duplicate-upn-tenant/oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"cli-duplicate-upn-client"},
		"username":   {"duplicate-upn-cli@example.com"},
		"password":   {"x"},
		"scope":      {"openid profile email"},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	var tokBody struct {
		IDToken string `json:"id_token"`
	}
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&tokBody))
	require.NotEmpty(t, tokBody.IDToken)

	parts := strings.Split(tokBody.IDToken, ".")
	require.Len(t, parts, 3)
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))
	assert.Equal(t, userID, payload["oid"])
	groupsRaw, ok := payload["groups"]
	require.True(t, ok, "id_token must contain groups claim")
	groups, ok := groupsRaw.([]any)
	require.True(t, ok)
	assert.Contains(t, groups, groupID)
}

// TestEntra_ApplicationServicePrincipalAndUserPatch provisions an application +
// service principal via the standard Graph endpoints (endpoint-only, swappable
// with real cloud), then PATCHes a user — covering the new Graph surface.
func TestEntra_ApplicationServicePrincipalAndUserPatch(t *testing.T) {
	// Create an application.
	appBody, _ := json.Marshal(map[string]any{"displayName": "CLI-App", "signInAudience": "AzureADMyOrg"})
	appReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications", bytes.NewReader(appBody))
	require.NoError(t, err)
	appReq.Header.Set("Content-Type", "application/json")
	appResp, err := http.DefaultClient.Do(appReq)
	require.NoError(t, err)
	defer appResp.Body.Close()
	require.Equal(t, http.StatusCreated, appResp.StatusCode)
	var app struct {
		ID    string `json:"id"`
		AppID string `json:"appId"`
	}
	require.NoError(t, json.NewDecoder(appResp.Body).Decode(&app))
	require.NotEmpty(t, app.AppID)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/applications/"+app.ID, nil)
		cr, _ := http.DefaultClient.Do(r)
		if cr != nil {
			cr.Body.Close()
		}
	})

	// Create a service principal for the application.
	spBody, _ := json.Marshal(map[string]any{"appId": app.AppID, "displayName": "CLI-App"})
	spReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/servicePrincipals", bytes.NewReader(spBody))
	require.NoError(t, err)
	spReq.Header.Set("Content-Type", "application/json")
	spResp, err := http.DefaultClient.Do(spReq)
	require.NoError(t, err)
	defer spResp.Body.Close()
	require.Equal(t, http.StatusCreated, spResp.StatusCode)
	var sp struct {
		ID                   string `json:"id"`
		ServicePrincipalType string `json:"servicePrincipalType"`
	}
	require.NoError(t, json.NewDecoder(spResp.Body).Decode(&sp))
	require.NotEmpty(t, sp.ID)
	assert.Equal(t, "Application", sp.ServicePrincipalType)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/servicePrincipals/"+sp.ID, nil)
		cr, _ := http.DefaultClient.Do(r)
		if cr != nil {
			cr.Body.Close()
		}
	})

	// GET the SP back by object ID.
	getSP, err := http.Get(baseURL + "/v1.0/servicePrincipals/" + sp.ID)
	require.NoError(t, err)
	getSP.Body.Close()
	require.Equal(t, http.StatusOK, getSP.StatusCode)

	// PATCH a user incrementally.
	userID := createEntraUser(t, "CLI Patch User", "cli-patch@example.com")
	patch, _ := json.Marshal(map[string]any{"displayName": "CLI Patched"})
	patchReq, err := http.NewRequest(http.MethodPatch, baseURL+"/v1.0/users/"+userID, bytes.NewReader(patch))
	require.NoError(t, err)
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	require.NoError(t, err)
	patchResp.Body.Close()
	require.Equal(t, http.StatusNoContent, patchResp.StatusCode)

	getUser, err := http.Get(baseURL + "/v1.0/users/" + userID)
	require.NoError(t, err)
	defer getUser.Body.Close()
	var gotUser struct {
		DisplayName string `json:"displayName"`
	}
	require.NoError(t, json.NewDecoder(getUser.Body).Decode(&gotUser))
	assert.Equal(t, "CLI Patched", gotUser.DisplayName)
}

// TestMSI_TokenIsSignedJWT verifies the IMDS/MSI token endpoint returns a real
// signed JWT carrying the identity's oid/aud claims, not a synthetic string.
func TestMSI_TokenIsSignedJWT(t *testing.T) {
	resp, err := http.Get(baseURL + "/metadata/identity/oauth2/token?api-version=2018-02-01&resource=https%3A%2F%2Fmanagement.azure.com%2F")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &payload), string(raw))
	assert.Equal(t, "Bearer", payload.TokenType)

	parts := strings.Split(payload.AccessToken, ".")
	require.Len(t, parts, 3, "MSI access_token must be a 3-part JWT")
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	assert.Equal(t, "https://management.azure.com/", claims["aud"])
	assert.NotEmpty(t, claims["oid"])
	assert.Equal(t, claims["oid"], claims["sub"])
	assert.NotEmpty(t, claims["tid"])
}

// TestEntra_IDTokenGroupsViaAuthCodeFlow provisions a user + group via the
// standard Microsoft Graph endpoints, then runs the authorization-code flow
// with login_hint binding the grant to that user — the mechanism real Azure AD
// uses to resolve the account a silent authorize request signs in — and
// verifies the id_token carries the groups claim from the membership store.
func TestEntra_IDTokenGroupsViaAuthCodeFlow(t *testing.T) {
	groupID := createEntraGroup(t, "CLI-AuthCode-Group")
	userID := createEntraUser(t, "AuthCode User", "authcode@example.com")
	addEntraGroupMember(t, groupID, userID)

	tenant := "cli-idtoken-tenant-ac"
	clientID := "cli-idtoken-client-ac"
	redirectURI := "http://127.0.0.1/callback"
	verifier := "ThisIsntRandomButItNeedsToBe43CharactersLong"
	challenge := pkceChallenge(verifier)

	authURL := fmt.Sprintf("%s/%s/oauth2/v2.0/authorize?%s", baseURL, tenant, url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email"},
		"login_hint":            {"authcode@example.com"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode())
	noRedirect := http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	authResp, err := noRedirect.Get(authURL)
	require.NoError(t, err)
	authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)
	callback, err := url.Parse(authResp.Header.Get("Location"))
	require.NoError(t, err)
	code := callback.Query().Get("code")
	require.NotEmpty(t, code)

	tokenResp, err := http.PostForm(baseURL+"/"+tenant+"/oauth2/v2.0/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	var tokBody struct {
		IDToken string `json:"id_token"`
	}
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&tokBody))
	require.NotEmpty(t, tokBody.IDToken)

	parts := strings.Split(tokBody.IDToken, ".")
	require.Len(t, parts, 3)
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadJSON, &payload))

	assert.Equal(t, userID, payload["oid"])
	groupsRaw, ok := payload["groups"]
	require.True(t, ok, "id_token must contain groups claim")
	groups, ok := groupsRaw.([]any)
	require.True(t, ok)
	assert.Contains(t, groups, groupID)
}

// createEntraApplicationWithSP registers an application plus its service
// principal via the standard Microsoft Graph endpoints — what the Azure portal
// does when an app registration is created — and returns the application's
// object ID and appId (the OAuth2 client_id).
func createEntraApplicationWithSP(t *testing.T, displayName string) (objectID, appID string) {
	t.Helper()
	appBody, _ := json.Marshal(map[string]any{"displayName": displayName, "signInAudience": "AzureADMyOrg"})
	appReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications", bytes.NewReader(appBody))
	require.NoError(t, err)
	appReq.Header.Set("Content-Type", "application/json")
	appResp, err := http.DefaultClient.Do(appReq)
	require.NoError(t, err)
	defer appResp.Body.Close()
	require.Equal(t, http.StatusCreated, appResp.StatusCode)
	var app struct {
		ID    string `json:"id"`
		AppID string `json:"appId"`
	}
	require.NoError(t, json.NewDecoder(appResp.Body).Decode(&app))
	require.NotEmpty(t, app.AppID)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/applications/"+app.ID, nil)
		cr, _ := http.DefaultClient.Do(r)
		if cr != nil {
			cr.Body.Close()
		}
	})

	spBody, _ := json.Marshal(map[string]any{"appId": app.AppID})
	spReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/servicePrincipals", bytes.NewReader(spBody))
	require.NoError(t, err)
	spReq.Header.Set("Content-Type", "application/json")
	spResp, err := http.DefaultClient.Do(spReq)
	require.NoError(t, err)
	defer spResp.Body.Close()
	require.Equal(t, http.StatusCreated, spResp.StatusCode)
	var sp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(spResp.Body).Decode(&sp))
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/servicePrincipals/"+sp.ID, nil)
		cr, _ := http.DefaultClient.Do(r)
		if cr != nil {
			cr.Body.Close()
		}
	})
	return app.ID, app.AppID
}

// TestEntra_AppRegistrationSecretAuthenticatesAzRest mints a client secret on
// an app registration (POST /v1.0/applications/{appObjectId}/addPassword — the
// Certificates & secrets blade's call), acquires an Azure Resource Manager
// token through the OAuth2 client_credentials grant with that appId + secret,
// and drives the az CLI against the ARM control plane with it. A wrong secret
// is rejected with invalid_client, and a removed secret stops authenticating
// (POST /v1.0/applications/{appObjectId}/removePassword).
func TestEntra_AppRegistrationSecretAuthenticatesAzRest(t *testing.T) {
	objectID, appID := createEntraApplicationWithSP(t, "CLI-Secret-App")

	credBody, _ := json.Marshal(map[string]any{
		"passwordCredential": map[string]any{"displayName": "cli secret"},
	})
	credReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications/"+objectID+"/addPassword", bytes.NewReader(credBody))
	require.NoError(t, err)
	credReq.Header.Set("Content-Type", "application/json")
	credResp, err := http.DefaultClient.Do(credReq)
	require.NoError(t, err)
	defer credResp.Body.Close()
	require.Equal(t, http.StatusOK, credResp.StatusCode)
	var cred struct {
		KeyID      string `json:"keyId"`
		SecretText string `json:"secretText"`
	}
	require.NoError(t, json.NewDecoder(credResp.Body).Decode(&cred))
	require.NotEmpty(t, cred.SecretText)

	// The exact client_credentials request `az login --service-principal`
	// sends: client_id + client_secret + the ARM .default scope.
	tokenFor := func(secret string) (*http.Response, map[string]any) {
		resp, err := http.PostForm(baseURL+"/"+simTenantID+"/oauth2/v2.0/token", url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {appID},
			"client_secret": {secret},
			"scope":         {"https://management.azure.com/.default"},
		})
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return resp, body
	}

	resp, body := tokenFor(cred.SecretText)
	require.Equal(t, http.StatusOK, resp.StatusCode, "minted secret must authenticate: %v", body)
	accessToken, _ := body["access_token"].(string)
	require.NotEmpty(t, accessToken)

	// The az CLI reads the ARM control plane with the minted-credential token.
	out := runCLI(t, azRest("GET",
		fmt.Sprintf("%s/subscriptions/%s/resourcegroups?api-version=2021-04-01", baseURL, subscriptionID),
		"", "--headers", "Authorization=Bearer "+accessToken))
	var groups struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &groups)

	// A wrong secret is rejected as invalid_client.
	badResp, badBody := tokenFor("wrong-secret")
	assert.Equal(t, http.StatusUnauthorized, badResp.StatusCode)
	assert.Equal(t, "invalid_client", badBody["error"])

	// Removing the credential revokes it.
	removeBody, _ := json.Marshal(map[string]any{"keyId": cred.KeyID})
	removeReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications/"+objectID+"/removePassword", bytes.NewReader(removeBody))
	require.NoError(t, err)
	removeReq.Header.Set("Content-Type", "application/json")
	removeResp, err := http.DefaultClient.Do(removeReq)
	require.NoError(t, err)
	removeResp.Body.Close()
	require.Equal(t, http.StatusNoContent, removeResp.StatusCode)

	revokedResp, revokedBody := tokenFor(cred.SecretText)
	assert.Equal(t, http.StatusUnauthorized, revokedResp.StatusCode)
	assert.Equal(t, "invalid_client", revokedBody["error"])
}
