package azure_sdk_test

import (
	"bytes"
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

// Microsoft Graph serves one directory under two endpoints — v1.0 at
// https://graph.microsoft.com/v1.0 and beta at
// https://graph.microsoft.com/beta — and clients mix them freely.
// terraform-provider-azuread reads `oauth2RequirePostResponse` on an
// application, `samlMetadataUrl` on a service principal, and
// `showInAddressList` on a user from beta because v1.0 models none of them,
// and drives the entire group family through beta.
//
// graphRouteTemplates is the whole directory surface the simulator mounts,
// written as the literal patterns it is registered under: the v1.0 spelling
// first, the beta spelling second. Every route in the table is driven for real
// by TestEntraGraphDirectorySurface below, on both versions, and the test fails
// if any row goes unexercised — a table nobody calls proves nothing.
var graphRouteTemplates = map[string][2]string{
	"applications.create":      {"POST /v1.0/applications", "POST /beta/applications"},
	"applications.list":        {"GET /v1.0/applications", "GET /beta/applications"},
	"applications.get":         {"GET /v1.0/applications/{appObjectId}", "GET /beta/applications/{appObjectId}"},
	"applications.patch":       {"PATCH /v1.0/applications/{appObjectId}", "PATCH /beta/applications/{appObjectId}"},
	"applications.delete":      {"DELETE /v1.0/applications/{appObjectId}", "DELETE /beta/applications/{appObjectId}"},
	"applications.addPassword": {"POST /v1.0/applications/{appObjectId}/addPassword", "POST /beta/applications/{appObjectId}/addPassword"},
	"applications.rmPassword":  {"POST /v1.0/applications/{appObjectId}/removePassword", "POST /beta/applications/{appObjectId}/removePassword"},
	"applications.owners":      {"GET /v1.0/applications/{appObjectId}/owners", "GET /beta/applications/{appObjectId}/owners"},
	"applications.addOwner":    {"POST /v1.0/applications/{appObjectId}/owners/$ref", "POST /beta/applications/{appObjectId}/owners/$ref"},
	"applications.rmOwner":     {"DELETE /v1.0/applications/{appObjectId}/owners/{ownerId}/$ref", "DELETE /beta/applications/{appObjectId}/owners/{ownerId}/$ref"},

	"servicePrincipals.create":      {"POST /v1.0/servicePrincipals", "POST /beta/servicePrincipals"},
	"servicePrincipals.list":        {"GET /v1.0/servicePrincipals", "GET /beta/servicePrincipals"},
	"servicePrincipals.get":         {"GET /v1.0/servicePrincipals/{spId}", "GET /beta/servicePrincipals/{spId}"},
	"servicePrincipals.patch":       {"PATCH /v1.0/servicePrincipals/{spId}", "PATCH /beta/servicePrincipals/{spId}"},
	"servicePrincipals.delete":      {"DELETE /v1.0/servicePrincipals/{spId}", "DELETE /beta/servicePrincipals/{spId}"},
	"servicePrincipals.addPassword": {"POST /v1.0/servicePrincipals/{spId}/addPassword", "POST /beta/servicePrincipals/{spId}/addPassword"},
	"servicePrincipals.rmPassword":  {"POST /v1.0/servicePrincipals/{spId}/removePassword", "POST /beta/servicePrincipals/{spId}/removePassword"},
	"servicePrincipals.owners":      {"GET /v1.0/servicePrincipals/{spId}/owners", "GET /beta/servicePrincipals/{spId}/owners"},
	"servicePrincipals.addOwner":    {"POST /v1.0/servicePrincipals/{spId}/owners/$ref", "POST /beta/servicePrincipals/{spId}/owners/$ref"},
	"servicePrincipals.rmOwner":     {"DELETE /v1.0/servicePrincipals/{spId}/owners/{ownerId}/$ref", "DELETE /beta/servicePrincipals/{spId}/owners/{ownerId}/$ref"},

	"groups.create":   {"POST /v1.0/groups", "POST /beta/groups"},
	"groups.list":     {"GET /v1.0/groups", "GET /beta/groups"},
	"groups.get":      {"GET /v1.0/groups/{groupId}", "GET /beta/groups/{groupId}"},
	"groups.patch":    {"PATCH /v1.0/groups/{groupId}", "PATCH /beta/groups/{groupId}"},
	"groups.delete":   {"DELETE /v1.0/groups/{groupId}", "DELETE /beta/groups/{groupId}"},
	"groups.members":  {"GET /v1.0/groups/{groupId}/members", "GET /beta/groups/{groupId}/members"},
	"groups.addMbr":   {"POST /v1.0/groups/{groupId}/members/$ref", "POST /beta/groups/{groupId}/members/$ref"},
	"groups.rmMbr":    {"DELETE /v1.0/groups/{groupId}/members/{memberId}/$ref", "DELETE /beta/groups/{groupId}/members/{memberId}/$ref"},
	"groups.owners":   {"GET /v1.0/groups/{groupId}/owners", "GET /beta/groups/{groupId}/owners"},
	"groups.addOwner": {"POST /v1.0/groups/{groupId}/owners/$ref", "POST /beta/groups/{groupId}/owners/$ref"},
	"groups.rmOwner":  {"DELETE /v1.0/groups/{groupId}/owners/{ownerId}/$ref", "DELETE /beta/groups/{groupId}/owners/{ownerId}/$ref"},
	"groups.memberOf": {"GET /v1.0/groups/{groupId}/memberOf", "GET /beta/groups/{groupId}/memberOf"},

	"users.create":   {"POST /v1.0/users", "POST /beta/users"},
	"users.list":     {"GET /v1.0/users", "GET /beta/users"},
	"users.get":      {"GET /v1.0/users/{userId}", "GET /beta/users/{userId}"},
	"users.patch":    {"PATCH /v1.0/users/{userId}", "PATCH /beta/users/{userId}"},
	"users.delete":   {"DELETE /v1.0/users/{userId}", "DELETE /beta/users/{userId}"},
	"users.memberOf": {"GET /v1.0/users/{userId}/memberOf", "GET /beta/users/{userId}/memberOf"},

	"users.getManager": {"GET /v1.0/users/{userId}/manager", "GET /beta/users/{userId}/manager"},
	"users.setManager": {"PUT /v1.0/users/{userId}/manager/$ref", "PUT /beta/users/{userId}/manager/$ref"},
	"users.rmManager":  {"DELETE /v1.0/users/{userId}/manager/$ref", "DELETE /beta/users/{userId}/manager/$ref"},

	"directoryObjects.get": {"GET /v1.0/directoryObjects/{objectId}", "GET /beta/directoryObjects/{objectId}"},

	"me.memberOf":           {"GET /v1.0/me/memberOf", "GET /beta/me/memberOf"},
	"me.transitiveMemberOf": {"GET /v1.0/me/transitiveMemberOf", "GET /beta/me/transitiveMemberOf"},
}

// graphCall is one request against a route in the table.
type graphCall struct {
	t       *testing.T
	version int
	params  map[string]string
	visited map[string]bool
}

// do issues the request the named route describes, filling its path parameters
// from the run's captured object IDs, and records the route as exercised.
func (c *graphCall) do(route string, body any, query string, headers map[string]string) (int, map[string]any) {
	c.t.Helper()
	tmpl, ok := graphRouteTemplates[route]
	require.Truef(c.t, ok, "unknown Microsoft Graph route %q", route)
	c.visited[route] = true

	method, path, found := strings.Cut(tmpl[c.version], " ")
	require.Truef(c.t, found, "route template %q must be %q", tmpl[c.version], "METHOD /path")
	for name, value := range c.params {
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	require.NotContainsf(c.t, path, "{", "unfilled path parameter in %s", path)

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(c.t, err)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, baseURL+path+query, reader)
	require.NoError(c.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	if len(bytes.TrimSpace(raw)) == 0 {
		return resp.StatusCode, nil
	}
	var decoded map[string]any
	require.NoErrorf(c.t, json.Unmarshal(raw, &decoded), "%s %s: decode %q", method, path, raw)
	return resp.StatusCode, decoded
}

// graphRef is the reference-URL body Graph's `$ref` routes take.
func graphRef(objectID string) map[string]any {
	return map[string]any{"@odata.id": baseURL + "/v1.0/directoryObjects/" + objectID}
}

// graphIDs reads the object IDs out of a Graph collection response.
func graphIDs(t *testing.T, body map[string]any) []string {
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

// TestEntraGraphDirectorySurface drives the whole Microsoft Graph directory
// surface — applications, service principals, groups, users, their owner and
// member reference collections, the manager navigation property, and the
// polymorphic directoryObjects read — against both the v1.0 and the beta
// endpoint, over the same directory.
func TestEntraGraphDirectorySurface(t *testing.T) {
	visited := map[string]bool{}
	for version, label := range []string{"v1.0", "beta"} {
		t.Run(label, func(t *testing.T) {
			runGraphDirectorySurface(t, version, label, visited)
		})
	}

	// A route table nobody calls proves nothing: every row must have been
	// driven for real above.
	for route := range graphRouteTemplates {
		assert.Truef(t, visited[route], "Microsoft Graph route %q was never exercised", route)
	}
}

func runGraphDirectorySurface(t *testing.T, version int, label string, visited map[string]bool) {
	c := &graphCall{t: t, version: version, params: map[string]string{}, visited: visited}
	suffix := "-" + label

	// --- users -------------------------------------------------------------
	managerUPN := "graph-surface-manager" + suffix + "@example.com"
	status, manager := c.do("users.create", map[string]any{
		"displayName":       "Graph Surface Manager" + suffix,
		"userPrincipalName": managerUPN,
		"mailNickname":      "graph-surface-manager" + suffix,
		"accountEnabled":    true,
		"jobTitle":          "Directory Manager",
	}, "", nil)
	require.Equal(t, http.StatusCreated, status)
	managerID, _ := manager["id"].(string)
	require.NotEmpty(t, managerID)
	c.params["objectId"] = managerID

	memberUPN := "graph-surface-member" + suffix + "@example.com"
	status, member := c.do("users.create", map[string]any{
		"displayName":       "Graph Surface Member" + suffix,
		"userPrincipalName": memberUPN,
		"mailNickname":      "graph-surface-member" + suffix,
		"accountEnabled":    true,
	}, "", nil)
	require.Equal(t, http.StatusCreated, status)
	memberID, _ := member["id"].(string)
	require.NotEmpty(t, memberID)
	c.params["userId"] = memberID
	c.params["memberId"] = memberID
	c.params["ownerId"] = memberID

	// Every property the client wrote comes back on the read, not just the
	// handful the simulator models on its own record.
	status, managerDoc := c.do("users.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "Graph Surface Member"+suffix, managerDoc["displayName"])
	assert.Equal(t, true, managerDoc["accountEnabled"])

	// $select narrows the response to the selected members plus the id.
	status, selected := c.do("users.get", nil, "?$select=displayName", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "Graph Surface Member"+suffix, selected["displayName"])
	assert.NotContains(t, selected, "accountEnabled", "$select must drop unselected members")
	assert.Contains(t, selected, "id", "Graph always returns the object id alongside a $select")

	// The beta endpoint models members v1.0 does not, and the v1.0 endpoint
	// must not leak them.
	if label == "beta" {
		assert.Contains(t, managerDoc, "showInAddressList",
			"beta models showInAddressList on a user")
		status, _ = c.do("users.patch", map[string]any{"showInAddressList": true}, "", nil)
		require.Equal(t, http.StatusNoContent, status)
	} else {
		assert.NotContains(t, managerDoc, "showInAddressList",
			"v1.0 does not model showInAddressList on a user")
	}

	status, _ = c.do("users.patch", map[string]any{"jobTitle": "Platform Engineer"}, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, patched := c.do("users.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "Platform Engineer", patched["jobTitle"], "PATCH must merge into the stored object")

	status, listed := c.do("users.list", nil,
		"?$filter="+url.QueryEscape("userPrincipalName eq '"+memberUPN+"'"), nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{memberID}, graphIDs(t, listed), "$filter must narrow the collection")

	// $count is served only for a request that opts into the eventually
	// consistent index with ConsistencyLevel: eventual.
	status, counted := c.do("users.list", nil, "?$count=true",
		map[string]string{"ConsistencyLevel": "eventual"})
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, counted, "@odata.count", "$count with ConsistencyLevel:eventual must return @odata.count")
	status, rejected := c.do("users.list", nil, "?$count=true", nil)
	assert.Equal(t, http.StatusBadRequest, status,
		"$count without ConsistencyLevel:eventual must be rejected")
	assert.Equal(t, "Request_UnsupportedQuery", graphErrorCode(rejected))

	// --- manager navigation property ---------------------------------------
	status, _ = c.do("users.getManager", nil, "", nil)
	assert.Equal(t, http.StatusNotFound, status, "a user with no manager answers 404")

	status, _ = c.do("users.setManager", map[string]any{
		"@odata.id": baseURL + "/v1.0/users/" + managerID,
	}, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, managerRead := c.do("users.getManager", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, managerID, managerRead["id"])
	assert.Equal(t, "#microsoft.graph.user", managerRead["@odata.type"])

	// --- directoryObjects ---------------------------------------------------
	status, directoryObject := c.do("directoryObjects.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, managerID, directoryObject["id"])
	assert.Equal(t, "#microsoft.graph.user", directoryObject["@odata.type"],
		"the polymorphic read must name the concrete directory type")

	// --- applications -------------------------------------------------------
	status, app := c.do("applications.create", map[string]any{
		"displayName":    "graph-surface-app" + suffix,
		"signInAudience": "AzureADMyOrg",
		"identifierUris": []string{},
		"web":            map[string]any{"redirectUris": []string{"https://example.invalid/callback"}},
		"owners@odata.bind": []string{
			baseURL + "/v1.0/directoryObjects/" + managerID,
		},
	}, "", nil)
	require.Equal(t, http.StatusCreated, status)
	appObjectID, _ := app["id"].(string)
	appClientID, _ := app["appId"].(string)
	require.NotEmpty(t, appObjectID)
	require.NotEmpty(t, appClientID)
	c.params["appObjectId"] = appObjectID

	status, appRead := c.do("applications.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	web, ok := appRead["web"].(map[string]any)
	require.Truef(t, ok, "the application read must carry the web member the create wrote: %v", appRead)
	assert.Equal(t, []any{"https://example.invalid/callback"}, web["redirectUris"])
	if label == "beta" {
		assert.Equal(t, false, appRead["oauth2RequirePostResponse"],
			"beta models oauth2RequirePostResponse on an application")
	} else {
		assert.NotContains(t, appRead, "oauth2RequirePostResponse",
			"v1.0 does not model oauth2RequirePostResponse")
	}

	status, _ = c.do("applications.patch", map[string]any{"notes": "surface test"}, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, appPatched := c.do("applications.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "surface test", appPatched["notes"])

	status, appList := c.do("applications.list", nil,
		"?$filter="+url.QueryEscape("appId eq '"+appClientID+"'"), nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{appObjectID}, graphIDs(t, appList))

	// The create-time owners@odata.bind populated the reference collection.
	status, appOwners := c.do("applications.owners", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{managerID}, graphIDs(t, appOwners))

	status, _ = c.do("applications.addOwner", graphRef(memberID), "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, appOwners = c.do("applications.owners", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.ElementsMatch(t, []string{managerID, memberID}, graphIDs(t, appOwners))

	status, _ = c.do("applications.rmOwner", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, appOwners = c.do("applications.owners", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{managerID}, graphIDs(t, appOwners))

	status, cred := c.do("applications.addPassword", map[string]any{
		"passwordCredential": map[string]any{"displayName": "surface"},
	}, "", nil)
	require.Equal(t, http.StatusOK, status)
	keyID, _ := cred["keyId"].(string)
	require.NotEmpty(t, keyID)
	require.NotEmpty(t, cred["secretText"])

	status, _ = c.do("applications.rmPassword", map[string]any{"keyId": keyID}, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	// --- service principals --------------------------------------------------
	status, sp := c.do("servicePrincipals.create", map[string]any{
		"appId":                     appClientID,
		"appRoleAssignmentRequired": true,
		"owners@odata.bind": []string{
			baseURL + "/v1.0/directoryObjects/" + managerID,
		},
	}, "", nil)
	require.Equal(t, http.StatusCreated, status)
	spID, _ := sp["id"].(string)
	require.NotEmpty(t, spID)
	c.params["spId"] = spID

	status, spRead := c.do("servicePrincipals.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, appClientID, spRead["appId"])
	assert.Equal(t, true, spRead["appRoleAssignmentRequired"])
	if label == "beta" {
		assert.Contains(t, spRead, "samlMetadataUrl", "beta models samlMetadataUrl on a service principal")
	} else {
		assert.NotContains(t, spRead, "samlMetadataUrl", "v1.0 does not model samlMetadataUrl")
	}

	status, _ = c.do("servicePrincipals.patch", map[string]any{"description": "surface principal"}, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	status, spList := c.do("servicePrincipals.list", nil,
		"?$filter="+url.QueryEscape("appId eq '"+appClientID+"'"), nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{spID}, graphIDs(t, spList))

	status, spOwners := c.do("servicePrincipals.owners", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{managerID}, graphIDs(t, spOwners))
	status, _ = c.do("servicePrincipals.addOwner", graphRef(memberID), "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = c.do("servicePrincipals.rmOwner", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	status, spCred := c.do("servicePrincipals.addPassword", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	spKeyID, _ := spCred["keyId"].(string)
	require.NotEmpty(t, spKeyID)
	status, _ = c.do("servicePrincipals.rmPassword", map[string]any{"keyId": spKeyID}, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	// --- groups ---------------------------------------------------------------
	status, group := c.do("groups.create", map[string]any{
		"displayName":     "graph-surface-group" + suffix,
		"mailNickname":    "graph-surface-group" + suffix,
		"securityEnabled": true,
		"mailEnabled":     false,
		"visibility":      "Private",
		"owners@odata.bind": []string{
			baseURL + "/v1.0/directoryObjects/" + managerID,
		},
		"members@odata.bind": []string{
			baseURL + "/v1.0/directoryObjects/" + memberID,
		},
	}, "", nil)
	require.Equal(t, http.StatusCreated, status)
	groupID, _ := group["id"].(string)
	require.NotEmpty(t, groupID)
	c.params["groupId"] = groupID

	status, groupRead := c.do("groups.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "Private", groupRead["visibility"])

	status, _ = c.do("groups.patch", map[string]any{"description": "surface group"}, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, groupPatched := c.do("groups.get", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "surface group", groupPatched["description"])

	status, groupList := c.do("groups.list", nil,
		"?$filter="+url.QueryEscape("displayName eq 'graph-surface-group"+suffix+"'"), nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{groupID}, graphIDs(t, groupList))

	status, groupOwners := c.do("groups.owners", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{managerID}, graphIDs(t, groupOwners))
	status, _ = c.do("groups.addOwner", graphRef(memberID), "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = c.do("groups.rmOwner", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	// The create-time members@odata.bind populated the member collection; the
	// $ref routes drive it afterwards.
	status, groupMembers := c.do("groups.members", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{memberID}, graphIDs(t, groupMembers))

	status, _ = c.do("groups.rmMbr", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = c.do("groups.addMbr", graphRef(memberID), "", nil)
	require.Equal(t, http.StatusNoContent, status)

	status, userMemberOf := c.do("users.memberOf", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{groupID}, graphIDs(t, userMemberOf))

	// A group nested inside another group reads its parent back through
	// memberOf, which is the same reference collection with a group as the
	// member rather than a user.
	status, parent := c.do("groups.create", map[string]any{
		"displayName":     "graph-surface-parent" + suffix,
		"mailNickname":    "graph-surface-parent" + suffix,
		"securityEnabled": true,
		"mailEnabled":     false,
	}, "", nil)
	require.Equal(t, http.StatusCreated, status)
	parentID, _ := parent["id"].(string)
	require.NotEmpty(t, parentID)

	childID := groupID
	c.params["groupId"] = parentID
	status, _ = c.do("groups.addMbr", graphRef(childID), "", nil)
	require.Equal(t, http.StatusNoContent, status)
	c.params["groupId"] = childID
	status, childMemberOf := c.do("groups.memberOf", nil, "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{parentID}, graphIDs(t, childMemberOf))

	// --- delegated /me reads --------------------------------------------------
	memberToken := requestAzureToken(t, "tenant-graph-surface"+suffix, "oauth2/v2.0/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"client-graph-surface"},
		"username":   {memberUPN},
		"password":   {"irrelevant"},
		"scope":      {"https://graph.microsoft.com/.default"},
	})
	bearer := map[string]string{"Authorization": "Bearer " + memberToken.AccessToken}
	status, meMemberOf := c.do("me.memberOf", nil, "", bearer)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, graphIDs(t, meMemberOf), childID)
	status, meTransitive := c.do("me.transitiveMemberOf", nil, "", bearer)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, graphIDs(t, meTransitive), childID)

	// --- teardown --------------------------------------------------------------
	status, _ = c.do("users.rmManager", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = c.do("users.getManager", nil, "", nil)
	assert.Equal(t, http.StatusNotFound, status, "the manager reference is gone after DELETE")

	status, _ = c.do("groups.delete", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	c.params["groupId"] = parentID
	status, _ = c.do("groups.delete", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	status, _ = c.do("servicePrincipals.delete", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = c.do("applications.delete", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	status, _ = c.do("users.delete", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)
	c.params["userId"] = managerID
	status, _ = c.do("users.delete", nil, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	// The owner references the deleted objects held are gone with them.
	c.params["userId"] = memberID
	status, _ = c.do("users.get", nil, "", nil)
	assert.Equal(t, http.StatusNotFound, status)
	status, gone := c.do("applications.owners", nil, "", nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "Request_ResourceNotFound", graphErrorCode(gone))
}

// graphErrorCode reads the OData error code out of a Graph error response.
func graphErrorCode(body map[string]any) string {
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		return fmt.Sprintf("%v", body)
	}
	code, _ := errObj["code"].(string)
	return code
}
