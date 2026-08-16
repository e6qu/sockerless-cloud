package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Azure CLI reaches Microsoft Graph with `az rest`, which is how an
// operator drives the directory from a shell against any Graph endpoint. Graph
// serves the same directory under two versions — v1.0 and beta — and clients
// mix them, so this walks the directory family on both.
//
// Each case below names the route by the literal pattern the simulator mounts
// it under, so the command and the mount are checkable against each other.

// azGraph issues an `az rest` call against a Graph path and decodes the JSON
// response, failing the test if the CLI does.
func azGraph(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	out := runCLI(t, azGraphCmd(t, method, path, body))
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var decoded map[string]any
	parseJSON(t, out, &decoded)
	return decoded
}

// azGraphExpectFailure runs an `az rest` call that the service is expected to
// reject, returning the combined CLI output so the caller can assert on the
// error the service produced.
func azGraphExpectFailure(t *testing.T, method, path string, body any) string {
	t.Helper()
	out, err := azGraphCmd(t, method, path, body).CombinedOutput()
	require.Errorf(t, err, "az rest %s %s was expected to fail; output: %s", method, path, out)
	return string(out)
}

func azGraphCmd(t *testing.T, method, path string, body any) *exec.Cmd {
	t.Helper()
	encoded := ""
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		encoded = string(raw)
	}
	return azRest(method, baseURL+path, encoded)
}

// azGraphIDs reads the object IDs out of a Graph collection response.
func azGraphIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	values, ok := body["value"].([]any)
	require.Truef(t, ok, "collection response has no value array: %v", body)
	out := make([]string, 0, len(values))
	for _, v := range values {
		item, ok := v.(map[string]any)
		require.Truef(t, ok, "collection item is not an object: %v", v)
		id, _ := item["id"].(string)
		out = append(out, id)
	}
	return out
}

func TestEntraGraphDirectoryCLI(t *testing.T) {
	for _, version := range []string{"v1.0", "beta"} {
		t.Run(version, func(t *testing.T) {
			runEntraGraphDirectoryCLI(t, version)
		})
	}
}

func runEntraGraphDirectoryCLI(t *testing.T, version string) {
	suffix := "-cli-" + version

	// --- users -------------------------------------------------------------
	// POST /v1.0/users, POST /beta/users
	managerUPN := "graph-cli-manager" + suffix + "@example.com"
	manager := azGraph(t, "POST", "/"+version+"/users", map[string]any{
		"displayName":       "Graph CLI Manager" + suffix,
		"userPrincipalName": managerUPN,
		"mailNickname":      "graph-cli-manager" + suffix,
		"accountEnabled":    true,
		"usageLocation":     "GB",
	})
	managerID, _ := manager["id"].(string)
	require.NotEmpty(t, managerID)

	memberUPN := "graph-cli-member" + suffix + "@example.com"
	member := azGraph(t, "POST", "/"+version+"/users", map[string]any{
		"displayName":       "Graph CLI Member" + suffix,
		"userPrincipalName": memberUPN,
		"mailNickname":      "graph-cli-member" + suffix,
		"accountEnabled":    true,
	})
	memberID, _ := member["id"].(string)
	require.NotEmpty(t, memberID)

	// GET /v1.0/users/{userId}, GET /beta/users/{userId} — every property the
	// client wrote is read back, and $select narrows the response.
	read := azGraph(t, "GET", "/"+version+"/users/"+managerID, nil)
	assert.Equal(t, "GB", read["usageLocation"])
	selected := azGraph(t, "GET", "/"+version+"/users/"+managerID+"?$select=displayName", nil)
	assert.Contains(t, selected, "id")
	assert.NotContains(t, selected, "usageLocation")

	// PATCH /v1.0/users/{userId}, PATCH /beta/users/{userId}
	azGraph(t, "PATCH", "/"+version+"/users/"+memberID, map[string]any{"jobTitle": "Operator"})
	patched := azGraph(t, "GET", "/"+version+"/users/"+memberID, nil)
	assert.Equal(t, "Operator", patched["jobTitle"])

	// GET /v1.0/users, GET /beta/users — with $filter, and with $count, which
	// Graph serves only for a request carrying ConsistencyLevel: eventual.
	filtered := azGraph(t, "GET", "/"+version+"/users?$filter="+
		url.QueryEscape("userPrincipalName eq '"+memberUPN+"'"), nil)
	assert.Equal(t, []string{memberID}, azGraphIDs(t, filtered))

	counted := runCLI(t, azRest("GET", baseURL+"/"+version+"/users?$count=true", "",
		"--headers", "Authorization=Bearer "+armBearer, "ConsistencyLevel=eventual"))
	var countedBody map[string]any
	parseJSON(t, counted, &countedBody)
	assert.Contains(t, countedBody, "@odata.count")

	rejected := azGraphExpectFailure(t, "GET", "/"+version+"/users?$count=true", nil)
	assert.Contains(t, rejected, "Request_UnsupportedQuery",
		"$count without ConsistencyLevel:eventual must be rejected")

	// --- manager navigation property ---------------------------------------
	// PUT /v1.0/users/{userId}/manager/$ref, PUT /beta/users/{userId}/manager/$ref
	azGraph(t, "PUT", "/"+version+"/users/"+memberID+"/manager/$ref", map[string]any{
		"@odata.id": baseURL + "/" + version + "/directoryObjects/" + managerID,
	})
	// GET /v1.0/users/{userId}/manager, GET /beta/users/{userId}/manager
	managerRead := azGraph(t, "GET", "/"+version+"/users/"+memberID+"/manager", nil)
	assert.Equal(t, managerID, managerRead["id"])
	assert.Equal(t, "#microsoft.graph.user", managerRead["@odata.type"])

	// GET /v1.0/directoryObjects/{objectId}, GET /beta/directoryObjects/{objectId}
	directoryObject := azGraph(t, "GET", "/"+version+"/directoryObjects/"+managerID, nil)
	assert.Equal(t, "#microsoft.graph.user", directoryObject["@odata.type"])

	// --- applications --------------------------------------------------------
	// POST /v1.0/applications, POST /beta/applications
	app := azGraph(t, "POST", "/"+version+"/applications", map[string]any{
		"displayName":    "graph-cli-app" + suffix,
		"signInAudience": "AzureADMyOrg",
		"web":            map[string]any{"redirectUris": []string{"https://example.invalid/cb"}},
		"owners@odata.bind": []string{
			baseURL + "/" + version + "/directoryObjects/" + managerID,
		},
	})
	appObjectID, _ := app["id"].(string)
	appClientID, _ := app["appId"].(string)
	require.NotEmpty(t, appObjectID)
	require.NotEmpty(t, appClientID)

	// GET /v1.0/applications/{appObjectId}, GET /beta/applications/{appObjectId}
	appRead := azGraph(t, "GET", "/"+version+"/applications/"+appObjectID, nil)
	web, ok := appRead["web"].(map[string]any)
	require.Truef(t, ok, "the application read must carry the web member: %v", appRead)
	assert.Equal(t, []any{"https://example.invalid/cb"}, web["redirectUris"])
	if version == "beta" {
		assert.Equal(t, false, appRead["oauth2RequirePostResponse"])
	} else {
		assert.NotContains(t, appRead, "oauth2RequirePostResponse")
	}

	// PATCH /v1.0/applications/{appObjectId}, PATCH /beta/applications/{appObjectId}
	azGraph(t, "PATCH", "/"+version+"/applications/"+appObjectID, map[string]any{"notes": "cli"})

	// GET /v1.0/applications, GET /beta/applications
	appList := azGraph(t, "GET", "/"+version+"/applications?$filter="+
		url.QueryEscape("appId eq '"+appClientID+"'"), nil)
	assert.Equal(t, []string{appObjectID}, azGraphIDs(t, appList))

	// GET /v1.0/applications/{appObjectId}/owners, GET /beta/applications/{appObjectId}/owners
	owners := azGraph(t, "GET", "/"+version+"/applications/"+appObjectID+"/owners", nil)
	assert.Equal(t, []string{managerID}, azGraphIDs(t, owners))
	// POST /v1.0/applications/{appObjectId}/owners/$ref, POST /beta/applications/{appObjectId}/owners/$ref
	azGraph(t, "POST", "/"+version+"/applications/"+appObjectID+"/owners/$ref",
		map[string]any{"@odata.id": baseURL + "/" + version + "/directoryObjects/" + memberID})
	// DELETE /v1.0/applications/{appObjectId}/owners/{ownerId}/$ref,
	// DELETE /beta/applications/{appObjectId}/owners/{ownerId}/$ref
	azGraph(t, "DELETE", "/"+version+"/applications/"+appObjectID+"/owners/"+memberID+"/$ref", nil)
	owners = azGraph(t, "GET", "/"+version+"/applications/"+appObjectID+"/owners", nil)
	assert.Equal(t, []string{managerID}, azGraphIDs(t, owners))

	// POST /v1.0/applications/{appObjectId}/addPassword, POST /beta/applications/{appObjectId}/addPassword
	cred := azGraph(t, "POST", "/"+version+"/applications/"+appObjectID+"/addPassword",
		map[string]any{"passwordCredential": map[string]any{"displayName": "cli"}})
	keyID, _ := cred["keyId"].(string)
	require.NotEmpty(t, keyID)
	// POST /v1.0/applications/{appObjectId}/removePassword, POST /beta/applications/{appObjectId}/removePassword
	azGraph(t, "POST", "/"+version+"/applications/"+appObjectID+"/removePassword",
		map[string]any{"keyId": keyID})

	// --- service principals ----------------------------------------------------
	// POST /v1.0/servicePrincipals, POST /beta/servicePrincipals
	sp := azGraph(t, "POST", "/"+version+"/servicePrincipals", map[string]any{
		"appId":                     appClientID,
		"appRoleAssignmentRequired": true,
		"owners@odata.bind": []string{
			baseURL + "/" + version + "/directoryObjects/" + managerID,
		},
	})
	spID, _ := sp["id"].(string)
	require.NotEmpty(t, spID)

	// GET /v1.0/servicePrincipals/{spId}, GET /beta/servicePrincipals/{spId}
	spRead := azGraph(t, "GET", "/"+version+"/servicePrincipals/"+spID, nil)
	assert.Equal(t, true, spRead["appRoleAssignmentRequired"])
	if version == "beta" {
		assert.Contains(t, spRead, "samlMetadataUrl")
	} else {
		assert.NotContains(t, spRead, "samlMetadataUrl")
	}
	// PATCH /v1.0/servicePrincipals/{spId}, PATCH /beta/servicePrincipals/{spId}
	azGraph(t, "PATCH", "/"+version+"/servicePrincipals/"+spID,
		map[string]any{"description": "cli principal"})
	// GET /v1.0/servicePrincipals, GET /beta/servicePrincipals
	spList := azGraph(t, "GET", "/"+version+"/servicePrincipals?$filter="+
		url.QueryEscape("appId eq '"+appClientID+"'"), nil)
	assert.Equal(t, []string{spID}, azGraphIDs(t, spList))
	// GET /v1.0/servicePrincipals/{spId}/owners, GET /beta/servicePrincipals/{spId}/owners
	spOwners := azGraph(t, "GET", "/"+version+"/servicePrincipals/"+spID+"/owners", nil)
	assert.Equal(t, []string{managerID}, azGraphIDs(t, spOwners))
	// POST /v1.0/servicePrincipals/{spId}/owners/$ref, POST /beta/servicePrincipals/{spId}/owners/$ref
	azGraph(t, "POST", "/"+version+"/servicePrincipals/"+spID+"/owners/$ref",
		map[string]any{"@odata.id": baseURL + "/" + version + "/directoryObjects/" + memberID})
	// DELETE /v1.0/servicePrincipals/{spId}/owners/{ownerId}/$ref,
	// DELETE /beta/servicePrincipals/{spId}/owners/{ownerId}/$ref
	azGraph(t, "DELETE", "/"+version+"/servicePrincipals/"+spID+"/owners/"+memberID+"/$ref", nil)
	// POST /v1.0/servicePrincipals/{spId}/addPassword, POST /beta/servicePrincipals/{spId}/addPassword
	spCred := azGraph(t, "POST", "/"+version+"/servicePrincipals/"+spID+"/addPassword", nil)
	spKeyID, _ := spCred["keyId"].(string)
	require.NotEmpty(t, spKeyID)
	// POST /v1.0/servicePrincipals/{spId}/removePassword, POST /beta/servicePrincipals/{spId}/removePassword
	azGraph(t, "POST", "/"+version+"/servicePrincipals/"+spID+"/removePassword",
		map[string]any{"keyId": spKeyID})

	// --- groups ------------------------------------------------------------------
	// POST /v1.0/groups, POST /beta/groups
	group := azGraph(t, "POST", "/"+version+"/groups", map[string]any{
		"displayName":     "graph-cli-group" + suffix,
		"mailNickname":    "graph-cli-group" + suffix,
		"securityEnabled": true,
		"mailEnabled":     false,
		"visibility":      "Private",
		"owners@odata.bind": []string{
			baseURL + "/" + version + "/directoryObjects/" + managerID,
		},
	})
	groupID, _ := group["id"].(string)
	require.NotEmpty(t, groupID)

	// GET /v1.0/groups/{groupId}, GET /beta/groups/{groupId}
	groupRead := azGraph(t, "GET", "/"+version+"/groups/"+groupID, nil)
	assert.Equal(t, "Private", groupRead["visibility"])
	// PATCH /v1.0/groups/{groupId}, PATCH /beta/groups/{groupId}
	azGraph(t, "PATCH", "/"+version+"/groups/"+groupID, map[string]any{"description": "cli group"})
	// GET /v1.0/groups, GET /beta/groups
	groupList := azGraph(t, "GET", "/"+version+"/groups?$filter="+
		url.QueryEscape("displayName eq 'graph-cli-group"+suffix+"'"), nil)
	assert.Equal(t, []string{groupID}, azGraphIDs(t, groupList))

	// GET /v1.0/groups/{groupId}/owners, GET /beta/groups/{groupId}/owners
	groupOwners := azGraph(t, "GET", "/"+version+"/groups/"+groupID+"/owners", nil)
	assert.Equal(t, []string{managerID}, azGraphIDs(t, groupOwners))
	// POST /v1.0/groups/{groupId}/owners/$ref, POST /beta/groups/{groupId}/owners/$ref
	azGraph(t, "POST", "/"+version+"/groups/"+groupID+"/owners/$ref",
		map[string]any{"@odata.id": baseURL + "/" + version + "/directoryObjects/" + memberID})
	// DELETE /v1.0/groups/{groupId}/owners/{ownerId}/$ref,
	// DELETE /beta/groups/{groupId}/owners/{ownerId}/$ref
	azGraph(t, "DELETE", "/"+version+"/groups/"+groupID+"/owners/"+memberID+"/$ref", nil)

	// POST /v1.0/groups/{groupId}/members/$ref, POST /beta/groups/{groupId}/members/$ref
	azGraph(t, "POST", "/"+version+"/groups/"+groupID+"/members/$ref",
		map[string]any{"@odata.id": baseURL + "/" + version + "/directoryObjects/" + memberID})
	// GET /v1.0/groups/{groupId}/members, GET /beta/groups/{groupId}/members
	members := azGraph(t, "GET", "/"+version+"/groups/"+groupID+"/members", nil)
	assert.Equal(t, []string{memberID}, azGraphIDs(t, members))
	// GET /v1.0/users/{userId}/memberOf, GET /beta/users/{userId}/memberOf
	memberOf := azGraph(t, "GET", "/"+version+"/users/"+memberID+"/memberOf", nil)
	assert.Equal(t, []string{groupID}, azGraphIDs(t, memberOf))

	// A nested group reads its parent back through the group memberOf
	// collection.
	parent := azGraph(t, "POST", "/"+version+"/groups", map[string]any{
		"displayName":     "graph-cli-parent" + suffix,
		"mailNickname":    "graph-cli-parent" + suffix,
		"securityEnabled": true,
		"mailEnabled":     false,
	})
	parentID, _ := parent["id"].(string)
	require.NotEmpty(t, parentID)
	azGraph(t, "POST", "/"+version+"/groups/"+parentID+"/members/$ref",
		map[string]any{"@odata.id": baseURL + "/" + version + "/directoryObjects/" + groupID})
	// GET /v1.0/groups/{groupId}/memberOf, GET /beta/groups/{groupId}/memberOf
	childMemberOf := azGraph(t, "GET", "/"+version+"/groups/"+groupID+"/memberOf", nil)
	assert.Equal(t, []string{parentID}, azGraphIDs(t, childMemberOf))

	// --- delegated /me reads -------------------------------------------------------
	memberBearer := graphUserBearer(t, memberUPN)
	// GET /v1.0/me/memberOf, GET /beta/me/memberOf
	meMemberOf := azGraph2(t, "GET", "/"+version+"/me/memberOf", memberBearer)
	assert.Contains(t, azGraphIDs(t, meMemberOf), groupID)
	// GET /v1.0/me/transitiveMemberOf, GET /beta/me/transitiveMemberOf
	meTransitive := azGraph2(t, "GET", "/"+version+"/me/transitiveMemberOf", memberBearer)
	assert.Contains(t, azGraphIDs(t, meTransitive), groupID)

	// --- teardown ---------------------------------------------------------------------
	// DELETE /v1.0/groups/{groupId}/members/{memberId}/$ref,
	// DELETE /beta/groups/{groupId}/members/{memberId}/$ref
	azGraph(t, "DELETE", "/"+version+"/groups/"+groupID+"/members/"+memberID+"/$ref", nil)
	// DELETE /v1.0/groups/{groupId}, DELETE /beta/groups/{groupId}
	azGraph(t, "DELETE", "/"+version+"/groups/"+groupID, nil)
	azGraph(t, "DELETE", "/"+version+"/groups/"+parentID, nil)
	// DELETE /v1.0/servicePrincipals/{spId}, DELETE /beta/servicePrincipals/{spId}
	azGraph(t, "DELETE", "/"+version+"/servicePrincipals/"+spID, nil)
	// DELETE /v1.0/applications/{appObjectId}, DELETE /beta/applications/{appObjectId}
	azGraph(t, "DELETE", "/"+version+"/applications/"+appObjectID, nil)
	// DELETE /v1.0/users/{userId}/manager/$ref, DELETE /beta/users/{userId}/manager/$ref
	azGraph(t, "DELETE", "/"+version+"/users/"+memberID+"/manager/$ref", nil)
	// DELETE /v1.0/users/{userId}, DELETE /beta/users/{userId}
	azGraph(t, "DELETE", "/"+version+"/users/"+memberID, nil)
	azGraph(t, "DELETE", "/"+version+"/users/"+managerID, nil)
}

// azGraph2 issues an `az rest` call carrying a caller-supplied bearer, which is
// what the delegated /me reads need: they resolve the signed-in principal from
// the token's oid claim rather than from the request path.
func azGraph2(t *testing.T, method, path, bearer string) map[string]any {
	t.Helper()
	out := runCLI(t, azRest(method, baseURL+path, "", "--headers", "Authorization=Bearer "+bearer))
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var decoded map[string]any
	parseJSON(t, out, &decoded)
	return decoded
}

// graphUserBearer acquires a delegated access token for a directory user
// through the resource-owner password grant, the same grant `az login -u/-p`
// uses against a tenant that permits it.
func graphUserBearer(t *testing.T, upn string) string {
	t.Helper()
	resp, err := http.PostForm(fmt.Sprintf("%s/%s/oauth2/v2.0/token", baseURL, simTenantID), url.Values{
		"grant_type": {"password"},
		"client_id":  {"graph-cli-directory"},
		"username":   {upn},
		"password":   {"irrelevant"},
		"scope":      {"https://graph.microsoft.com/.default"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.AccessToken)
	return body.AccessToken
}
