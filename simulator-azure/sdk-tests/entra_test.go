package azure_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Graph provisioning helpers ---

type graphGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type graphUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
}

func createGraphGroup(t *testing.T, displayName string) graphGroup {
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
	var grp graphGroup
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grp))
	require.NotEmpty(t, grp.ID)
	t.Cleanup(func() { deleteGraphGroup(t, grp.ID) })
	return grp
}

func deleteGraphGroup(t *testing.T, id string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/groups/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
}

func createGraphUser(t *testing.T, displayName, upn string) graphUser {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"displayName":       displayName,
		"userPrincipalName": upn,
		"mailNickname":      displayName,
		"accountEnabled":    true,
		"passwordProfile": map[string]any{
			"password":                      "Test1234!",
			"forceChangePasswordNextSignIn": false,
		},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/users", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var u graphUser
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&u))
	require.NotEmpty(t, u.ID)
	t.Cleanup(func() { deleteGraphUser(t, u.ID) })
	return u
}

func deleteGraphUser(t *testing.T, id string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/users/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
}

func addGroupMember(t *testing.T, groupID, userID string) {
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

// doROPC exchanges username+password for tokens and returns the decoded id_token payload.
func doROPC(t *testing.T, tenant, clientID, username string) map[string]any {
	t.Helper()
	tokenBody := requestAzureToken(t, tenant, "oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {clientID},
		"username":   {username},
		"password":   {"irrelevant"},
		"scope":      {"openid profile email"},
	})
	require.NotEmpty(t, tokenBody.IDToken, "ROPC must return id_token when scope includes openid")
	return azureTokenPayload(t, tokenBody.IDToken)
}

// --- Standard provisioning + ROPC tests ---

func TestEntra_GraphGroupCRUD(t *testing.T) {
	grp := createGraphGroup(t, "SDK-Test-Group")

	// GET by ID
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1.0/groups/"+grp.ID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got graphGroup
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, grp.ID, got.ID)
	assert.Equal(t, "SDK-Test-Group", got.DisplayName)
}

func TestEntra_GraphUserCRUD(t *testing.T) {
	u := createGraphUser(t, "SDK Test User", "sdktest@example.com")

	// GET by ID
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1.0/users/"+u.ID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got graphUser
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, u.ID, got.ID)
	assert.Equal(t, "sdktest@example.com", got.UserPrincipalName)
}

func TestEntra_GraphGroupMembership(t *testing.T) {
	grp := createGraphGroup(t, "SDK-Membership-Group")
	u := createGraphUser(t, "Member User", "member@example.com")
	addGroupMember(t, grp.ID, u.ID)

	// GET members
	resp, err := http.Get(baseURL + "/v1.0/groups/" + grp.ID + "/members")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		Value []graphUser `json:"value"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Value, 1)
	assert.Equal(t, u.ID, body.Value[0].ID)

	// DELETE member
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/groups/"+grp.ID+"/members/"+u.ID+"/$ref", nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)
}

func TestEntra_ROPCIDTokenGroupsClaim(t *testing.T) {
	grp := createGraphGroup(t, "ROPC-Admins")
	u := createGraphUser(t, "ROPC User", "ropc-user@example.com")
	addGroupMember(t, grp.ID, u.ID)

	payload := doROPC(t, "tenant-ropc", "client-ropc", "ropc-user@example.com")

	assert.Equal(t, u.ID, payload["oid"])
	assert.Equal(t, "ROPC User", payload["name"])
	groupsRaw, ok := payload["groups"]
	require.True(t, ok, "id_token must contain groups claim")
	groups, ok := groupsRaw.([]any)
	require.True(t, ok)
	assert.Contains(t, groups, grp.ID)
}

func TestEntra_DuplicateUPNRejectedAndROPCClaimsStayDeterministic(t *testing.T) {
	grp := createGraphGroup(t, "ROPC-Duplicate-UPN")
	u := createGraphUser(t, "Duplicate UPN User", "duplicate-upn-sdk@example.com")
	addGroupMember(t, grp.ID, u.ID)

	body, _ := json.Marshal(map[string]any{
		"displayName":       "Duplicate UPN User Two",
		"userPrincipalName": "DUPLICATE-UPN-SDK@example.com",
		"mailNickname":      "Duplicate UPN User Two",
		"accountEnabled":    true,
		"passwordProfile": map[string]any{
			"password":                      "Test1234!",
			"forceChangePasswordNextSignIn": false,
		},
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

	payload := doROPC(t, "tenant-ropc-duplicate", "client-ropc-duplicate", "duplicate-upn-sdk@example.com")
	assert.Equal(t, u.ID, payload["oid"])
	groupsRaw, ok := payload["groups"]
	require.True(t, ok, "id_token must contain groups claim")
	groups, ok := groupsRaw.([]any)
	require.True(t, ok)
	assert.Contains(t, groups, grp.ID)
}

func TestEntra_ROPCUnknownUserReturns400(t *testing.T) {
	tokenResp, err := http.PostForm(baseURL+"/tenant-ropc-unknown/oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"client-ropc"},
		"username":   {"nobody@example.com"},
		"password":   {"x"},
		"scope":      {"openid"},
	})
	require.NoError(t, err)
	defer tokenResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, tokenResp.StatusCode)
}

func TestEntra_IDTokenGroupsClaim(t *testing.T) {
	grp := createGraphGroup(t, "Auth-Admins")
	u := createGraphUser(t, "Auth Groups User", "authgroups@example.com")
	addGroupMember(t, grp.ID, u.ID)

	// Auth code flow uses the sim-active user (default). Provision via ROPC
	// which targets the specific user.
	payload := doROPC(t, "tenant-entra-groups", "client-entra-groups", "authgroups@example.com")

	assert.Equal(t, u.ID, payload["oid"])
	groupsRaw, ok := payload["groups"]
	require.True(t, ok, "id_token must contain groups claim")
	groups, ok := groupsRaw.([]any)
	require.True(t, ok)
	assert.Contains(t, groups, grp.ID)
}

func TestEntra_IDTokenNoGroupsClaimWhenUserHasNoGroups(t *testing.T) {
	u := createGraphUser(t, "No Groups User", "nogroups@example.com")
	payload := doROPC(t, "tenant-entra-nogroups", "client-entra-nogroups", "nogroups@example.com")
	assert.Equal(t, u.ID, payload["oid"])
	_, hasGroups := payload["groups"]
	assert.False(t, hasGroups, "id_token must not contain groups claim when user has no groups")
}

func TestEntra_GraphMemberOf(t *testing.T) {
	grp1 := createGraphGroup(t, "Engineering")
	grp2 := createGraphGroup(t, "Platform")
	u := createGraphUser(t, "Graph User", "graph-user@example.com")
	addGroupMember(t, grp1.ID, u.ID)
	addGroupMember(t, grp2.ID, u.ID)

	// Get an access token via ROPC (oid in the token will be the created user's oid).
	tokenBody := requestAzureToken(t, "tenant-entra-graph", "oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"client-entra-graph"},
		"username":   {"graph-user@example.com"},
		"password":   {"x"},
		"scope":      {"https://graph.microsoft.com/.default"},
	})

	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1.0/me/memberOf", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tokenBody.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Value []struct {
			ODataType   string `json:"@odata.type"`
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"value"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Value, 2)
	ids := make(map[string]string)
	for _, g := range body.Value {
		assert.Equal(t, "#microsoft.graph.group", g.ODataType)
		ids[g.ID] = g.DisplayName
	}
	assert.Equal(t, "Engineering", ids[grp1.ID])
	assert.Equal(t, "Platform", ids[grp2.ID])
}

func TestEntra_GraphTransitiveMemberOf(t *testing.T) {
	grp := createGraphGroup(t, "Infra")
	u := createGraphUser(t, "Transitive User", "transitive@example.com")
	addGroupMember(t, grp.ID, u.ID)

	tokenBody := requestAzureToken(t, "tenant-entra-transitive", "oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"client-entra-transitive"},
		"username":   {"transitive@example.com"},
		"password":   {"x"},
		"scope":      {"https://graph.microsoft.com/.default"},
	})

	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1.0/me/transitiveMemberOf", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tokenBody.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Value, 1)
	assert.Equal(t, grp.ID, body.Value[0].ID)
}

// --- Application + service principal provisioning ---
//
// These tests exercise the Microsoft Graph application + service-principal
// surface end-to-end. The routes covered (placeholders resolve to runtime
// object IDs at request time):
//   GET /v1.0/applications/{appObjectId}
//   PATCH /v1.0/applications/{appObjectId}
//   DELETE /v1.0/applications/{appObjectId}
//   GET /v1.0/servicePrincipals/{spId}
//   PATCH /v1.0/servicePrincipals/{spId}
//   DELETE /v1.0/servicePrincipals/{spId}
//   POST /v1.0/servicePrincipals/{spId}/addPassword
//   POST /v1.0/servicePrincipals/{spId}/removePassword
//   POST /v1.0/applications/{appObjectId}/addPassword
//   POST /v1.0/applications/{appObjectId}/removePassword
//   PATCH /v1.0/users/{userId}

func createGraphApplication(t *testing.T, displayName string) (objectID, appID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"displayName":    displayName,
		"signInAudience": "AzureADMyOrg",
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var app struct {
		ID    string `json:"id"`
		AppID string `json:"appId"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&app))
	require.NotEmpty(t, app.ID)
	require.NotEmpty(t, app.AppID)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/applications/"+app.ID, nil)
		cr, _ := http.DefaultClient.Do(r)
		if cr != nil {
			cr.Body.Close()
		}
	})
	return app.ID, app.AppID
}

func createGraphServicePrincipal(t *testing.T, appID, displayName string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"appId":       appID,
		"displayName": displayName,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/servicePrincipals", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var sp struct {
		ID                   string `json:"id"`
		AppID                string `json:"appId"`
		ServicePrincipalType string `json:"servicePrincipalType"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sp))
	require.NotEmpty(t, sp.ID)
	assert.Equal(t, appID, sp.AppID)
	assert.Equal(t, "Application", sp.ServicePrincipalType)
	t.Cleanup(func() {
		r, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1.0/servicePrincipals/"+sp.ID, nil)
		cr, _ := http.DefaultClient.Do(r)
		if cr != nil {
			cr.Body.Close()
		}
	})
	return sp.ID
}

func TestEntra_ApplicationAndServicePrincipal(t *testing.T) {
	objectID, appID := createGraphApplication(t, "SDK-App")

	// GET the application by object ID.
	resp, err := http.Get(baseURL + "/v1.0/applications/" + objectID)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var gotApp struct {
		ID          string `json:"id"`
		AppID       string `json:"appId"`
		DisplayName string `json:"displayName"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&gotApp))
	assert.Equal(t, appID, gotApp.AppID)
	assert.Equal(t, "SDK-App", gotApp.DisplayName)

	spID := createGraphServicePrincipal(t, appID, "SDK-App")

	// GET the service principal by object ID.
	spResp, err := http.Get(baseURL + "/v1.0/servicePrincipals/" + spID)
	require.NoError(t, err)
	defer spResp.Body.Close()
	require.Equal(t, http.StatusOK, spResp.StatusCode)

	// LIST servicePrincipals filtered by appId.
	listResp, err := http.Get(baseURL + "/v1.0/servicePrincipals?$filter=appId+eq+'" + url.QueryEscape(appID) + "'")
	require.NoError(t, err)
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	var list struct {
		Value []struct {
			ID    string `json:"id"`
			AppID string `json:"appId"`
		} `json:"value"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&list))
	require.Len(t, list.Value, 1, "$filter=appId must return exactly the matching SP")
	assert.Equal(t, spID, list.Value[0].ID)

	// PATCH the application's displayName (204), then read it back.
	patchApp, _ := json.Marshal(map[string]any{"displayName": "SDK-App-Renamed"})
	patchAppReq, err := http.NewRequest(http.MethodPatch, baseURL+"/v1.0/applications/"+objectID, bytes.NewReader(patchApp))
	require.NoError(t, err)
	patchAppReq.Header.Set("Content-Type", "application/json")
	patchAppResp, err := http.DefaultClient.Do(patchAppReq)
	require.NoError(t, err)
	patchAppResp.Body.Close()
	require.Equal(t, http.StatusNoContent, patchAppResp.StatusCode)

	// PATCH the service principal's displayName (204).
	patchSP, _ := json.Marshal(map[string]any{"displayName": "SDK-SP-Renamed"})
	patchSPReq, err := http.NewRequest(http.MethodPatch, baseURL+"/v1.0/servicePrincipals/"+spID, bytes.NewReader(patchSP))
	require.NoError(t, err)
	patchSPReq.Header.Set("Content-Type", "application/json")
	patchSPResp, err := http.DefaultClient.Do(patchSPReq)
	require.NoError(t, err)
	patchSPResp.Body.Close()
	require.Equal(t, http.StatusNoContent, patchSPResp.StatusCode)

	getRenamed, err := http.Get(baseURL + "/v1.0/applications/" + objectID)
	require.NoError(t, err)
	defer getRenamed.Body.Close()
	var renamed struct {
		DisplayName string `json:"displayName"`
	}
	require.NoError(t, json.NewDecoder(getRenamed.Body).Decode(&renamed))
	assert.Equal(t, "SDK-App-Renamed", renamed.DisplayName)
}

func TestEntra_ServicePrincipalAddPassword(t *testing.T) {
	_, appID := createGraphApplication(t, "SDK-Secret-App")
	spID := createGraphServicePrincipal(t, appID, "SDK-Secret-App")

	body, _ := json.Marshal(map[string]any{
		"passwordCredential": map[string]any{"displayName": "rbac-secret"},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/servicePrincipals/"+spID+"/addPassword", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var cred struct {
		KeyID      string `json:"keyId"`
		SecretText string `json:"secretText"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cred))
	assert.NotEmpty(t, cred.KeyID)
	assert.NotEmpty(t, cred.SecretText, "addPassword must return the generated secretText")

	// A subsequent GET must list the credential but never echo the secretText.
	getResp, err := http.Get(baseURL + "/v1.0/servicePrincipals/" + spID)
	require.NoError(t, err)
	defer getResp.Body.Close()
	var got struct {
		PasswordCredentials []struct {
			KeyID      string `json:"keyId"`
			SecretText string `json:"secretText"`
		} `json:"passwordCredentials"`
	}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&got))
	require.Len(t, got.PasswordCredentials, 1)
	assert.Equal(t, cred.KeyID, got.PasswordCredentials[0].KeyID)
	assert.Empty(t, got.PasswordCredentials[0].SecretText, "secretText must not be returned on read")

	// removePassword deletes the credential; a second removal of the same
	// keyId is a 404, matching real Graph.
	remove := func() *http.Response {
		body, _ := json.Marshal(map[string]any{"keyId": cred.KeyID})
		req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/servicePrincipals/"+spID+"/removePassword", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}
	require.Equal(t, http.StatusNoContent, remove().StatusCode)
	require.Equal(t, http.StatusNotFound, remove().StatusCode)
}

// addApplicationPassword mints a client secret on the application object —
// POST /v1.0/applications/{appObjectId}/addPassword, the request behind the
// real portal's Certificates & secrets blade.
func addApplicationPassword(t *testing.T, appObjectID, displayName string) (keyID, secretText, hint string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"passwordCredential": map[string]any{"displayName": displayName},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications/"+appObjectID+"/addPassword", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var cred struct {
		KeyID      string `json:"keyId"`
		SecretText string `json:"secretText"`
		Hint       string `json:"hint"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cred))
	require.NotEmpty(t, cred.KeyID)
	require.NotEmpty(t, cred.SecretText, "addPassword must return the generated secretText exactly once")
	return cred.KeyID, cred.SecretText, cred.Hint
}

// TestEntra_ApplicationClientSecretEndToEnd walks the credential-minting path
// the portal's App registrations blade drives: register an application and its
// service principal, mint a client secret on the application object, then
// authenticate the OAuth2 client_credentials grant with appId + secretText and
// read the ARM control plane through the Azure SDK with the issued token. A
// wrong secret and a removed secret are rejected with invalid_client.
func TestEntra_ApplicationClientSecretEndToEnd(t *testing.T) {
	objectID, appID := createGraphApplication(t, "SDK-CC-App")
	spID := createGraphServicePrincipal(t, appID, "SDK-CC-App")

	keyID, secretText, hint := addApplicationPassword(t, objectID, "sdk secret")
	assert.Equal(t, secretText[:3], hint, "hint must be the first three characters of the secret")

	// The application read lists the credential's metadata with the hint, and
	// never the secretText.
	getResp, err := http.Get(baseURL + "/v1.0/applications/" + objectID)
	require.NoError(t, err)
	defer getResp.Body.Close()
	var gotApp struct {
		PasswordCredentials []struct {
			KeyID      string `json:"keyId"`
			Hint       string `json:"hint"`
			SecretText string `json:"secretText"`
		} `json:"passwordCredentials"`
	}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&gotApp))
	require.Len(t, gotApp.PasswordCredentials, 1)
	assert.Equal(t, keyID, gotApp.PasswordCredentials[0].KeyID)
	assert.Equal(t, hint, gotApp.PasswordCredentials[0].Hint)
	assert.Empty(t, gotApp.PasswordCredentials[0].SecretText, "secretText must not be returned on read")

	// The v2.0 client_credentials grant — the request an SDK or
	// `az login --service-principal` sends — validates the minted secret and
	// issues an app-only token for the service principal.
	tokenFor := func(secret string) (int, map[string]any) {
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
		return resp.StatusCode, body
	}
	status, body := tokenFor(secretText)
	require.Equal(t, http.StatusOK, status, "minted secret must authenticate: %v", body)
	accessToken, _ := body["access_token"].(string)
	require.NotEmpty(t, accessToken)
	claims := decodeJWTClaims(t, accessToken)
	assert.Equal(t, appID, claims["appid"])
	assert.Equal(t, spID, claims["oid"], "app-only token must speak for the service principal")
	assert.Equal(t, "app", claims["idtyp"])

	// The Azure SDK's client-credentials flow end-to-end: the credential runs
	// the same client_credentials grant azidentity's ClientSecretCredential
	// sends (issued directly because azidentity refuses a non-https authority
	// host and the simulator listens on plain http), and the ARM client reads
	// the control plane with the issued token — the enforcing bearer
	// middleware accepts the credential.
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID,
		&appSecretCredential{clientID: appID, clientSecret: secretText}, clientOpts())
	require.NoError(t, err)
	pager := rgClient.NewListPager(nil)
	_, err = pager.NextPage(ctx)
	require.NoError(t, err, "ARM must accept the token minted from the app registration's client secret")

	// A wrong secret is rejected as invalid_client.
	badStatus, badBody := tokenFor("wrong-secret")
	assert.Equal(t, http.StatusUnauthorized, badStatus)
	assert.Equal(t, "invalid_client", badBody["error"])

	// removePassword revokes the credential.
	removeBody, _ := json.Marshal(map[string]any{"keyId": keyID})
	removeReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1.0/applications/"+objectID+"/removePassword", bytes.NewReader(removeBody))
	require.NoError(t, err)
	removeReq.Header.Set("Content-Type", "application/json")
	removeResp, err := http.DefaultClient.Do(removeReq)
	require.NoError(t, err)
	removeResp.Body.Close()
	require.Equal(t, http.StatusNoContent, removeResp.StatusCode)

	revokedStatus, revokedBody := tokenFor(secretText)
	assert.Equal(t, http.StatusUnauthorized, revokedStatus)
	assert.Equal(t, "invalid_client", revokedBody["error"])
}

// TestEntra_UnregisteredClientCredentialsRejected proves the v2.0
// client_credentials grant rejects a client_id the directory holds no
// application registration for — exactly as real Microsoft Entra rejects an
// unknown client with AADSTS700016 unauthorized_client, before ever
// inspecting a client_secret. A client_secret that happens to match the
// seeded bootstrap application's secret must not authenticate a different,
// unregistered client_id.
func TestEntra_UnregisteredClientCredentialsRejected(t *testing.T) {
	resp, err := http.PostForm(baseURL+"/"+simTenantID+"/oauth2/v2.0/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"no-such-registered-app"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "unauthorized_client", body["error"])
	assert.Contains(t, body["error_description"], "AADSTS700016")
	assert.Contains(t, body["error_description"], "no-such-registered-app")
}

// appSecretCredential implements azcore.TokenCredential by running the OAuth2
// client_credentials grant with an app registration's appId + client secret —
// the same wire request azidentity's ClientSecretCredential sends, issued
// directly because azidentity refuses a non-https authority host and the
// simulator listens on plain http.
type appSecretCredential struct {
	clientID     string
	clientSecret string
}

func (c *appSecretCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	scope := strings.Join(opts.Scopes, " ")
	resp, err := http.PostForm(baseURL+"/"+simTenantID+"/oauth2/v2.0/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"scope":         {scope},
	})
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("acquire token with client secret: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return azcore.AccessToken{}, fmt.Errorf("parse token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return azcore.AccessToken{}, fmt.Errorf("token endpoint returned %d: %s %s", resp.StatusCode, body.Error, body.Description)
	}
	return azcore.AccessToken{Token: body.AccessToken, ExpiresOn: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)}, nil
}

func TestEntra_UserPatch(t *testing.T) {
	u := createGraphUser(t, "Patch Me", "patch-me@example.com")

	patch, _ := json.Marshal(map[string]any{
		"displayName": "Patched Name",
		"mail":        "patched@example.com",
	})
	req, err := http.NewRequest(http.MethodPatch, baseURL+"/v1.0/users/"+u.ID, bytes.NewReader(patch))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "Graph user PATCH returns 204 No Content")

	getResp, err := http.Get(baseURL + "/v1.0/users/" + u.ID)
	require.NoError(t, err)
	defer getResp.Body.Close()
	var got struct {
		DisplayName string `json:"displayName"`
		Mail        string `json:"mail"`
	}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&got))
	assert.Equal(t, "Patched Name", got.DisplayName)
	assert.Equal(t, "patched@example.com", got.Mail)
}

func TestEntra_GroupMemberODataID(t *testing.T) {
	grp := createGraphGroup(t, "OData-Group")
	u := createGraphUser(t, "OData Member", "odata-member@example.com")
	addGroupMember(t, grp.ID, u.ID)

	resp, err := http.Get(baseURL + "/v1.0/groups/" + grp.ID + "/members")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		Value []struct {
			ODataID string `json:"@odata.id"`
			ID      string `json:"id"`
		} `json:"value"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Value, 1)
	assert.Contains(t, body.Value[0].ODataID, "/v1.0/directoryObjects/"+u.ID,
		"group members must carry an @odata.id binding")
}

func TestEntra_DiscoveryAdvertisesGroupsClaimAndROPC(t *testing.T) {
	resp, err := http.Get(baseURL + "/tenant-groups-discovery/.well-known/openid-configuration")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		ClaimsSupported     []string `json:"claims_supported"`
		GrantTypesSupported []string `json:"grant_types_supported"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body.ClaimsSupported, "groups")
	assert.Contains(t, body.GrantTypesSupported, "password")
}
