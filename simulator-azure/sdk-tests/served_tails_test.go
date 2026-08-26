package azure_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Microsoft.Resources generic surface and the Microsoft.Authorization
// permission listings, which used to be mux misses.

// Every resource ARM addresses by its typed route is addressable by its
// full resource ID: the generic methods read, write, patch and delete the
// same resource the typed provider owns, so a write through one door is
// visible through the other.
func TestResources_ByResourceIDAddressesTheSameResource(t *testing.T) {
	const group = "generic-id-rg"
	createResourceGroup(t, group)

	const (
		version = "?api-version=2024-01-01"
		account = "genericidsa"
	)
	resourceID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + group +
		"/providers/Microsoft.Storage/storageAccounts/" + account

	// Created through the generic by-id door.
	resp := armReq(t, http.MethodPut, resourceID+version, `{
		"location": "eastus",
		"sku": {"name": "Standard_LRS"},
		"kind": "StorageV2",
		"properties": {}
	}`)
	defer resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated, http.StatusAccepted}, resp.StatusCode,
		"the generic create must reach the typed provider")

	// Read back through the typed route: the same resource, one store.
	typed := armReq(t, http.MethodGet, resourceID+version, "")
	defer typed.Body.Close()
	require.Equal(t, http.StatusOK, typed.StatusCode)
	var read map[string]any
	require.NoError(t, json.NewDecoder(typed.Body).Decode(&read))
	assert.Equal(t, account, read["name"])

	// Existence is a HEAD on the same identifier.
	head := armReq(t, http.MethodHead, resourceID+version, "")
	defer head.Body.Close()
	assert.Equal(t, http.StatusNoContent, head.StatusCode,
		"checkExistenceById answers no-content for a resource that exists")

	// A tag patch through the generic door lands on the typed resource.
	patch := armReq(t, http.MethodPatch, resourceID+version, `{"tags":{"owner":"generic"}}`)
	defer patch.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, patch.StatusCode)
	after := armReq(t, http.MethodGet, resourceID+version, "")
	defer after.Body.Close()
	require.Equal(t, http.StatusOK, after.StatusCode)
	var patched map[string]any
	require.NoError(t, json.NewDecoder(after.Body).Decode(&patched))
	tags, _ := patched["tags"].(map[string]any)
	assert.Equal(t, "generic", tags["owner"], "the patch through the generic door is the one the read returns")

	// Deleting by id removes it, and the existence check then says so.
	del := armReq(t, http.MethodDelete, resourceID+version, "")
	defer del.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent}, del.StatusCode)
	gone := armReq(t, http.MethodHead, resourceID+version, "")
	defer gone.Body.Close()
	assert.Equal(t, http.StatusNotFound, gone.StatusCode,
		"a deleted resource must not answer the existence check")
}

// The permission listings report the actions the role definitions the
// caller's assignments name actually carry, at the resource-group scope and
// at a resource's own scope.
func TestAuthorization_PermissionListings(t *testing.T) {
	const group = "permissions-rg"
	createResourceGroup(t, group)

	// The listing reports what the CALLER may do, so the assignment names
	// the caller's own principal — the object id its bearer carries.
	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + group
	assignment := scope + "/providers/Microsoft.Authorization/roleAssignments/" + azurePermissionsAssignmentGUID
	resp := armReq(t, http.MethodPut, assignment+"?api-version=2022-04-01", `{
		"properties": {
			"roleDefinitionId": "/subscriptions/`+subscriptionID+`/providers/Microsoft.Authorization/roleDefinitions/b24988ac-6180-42a0-ab88-20f7382dd24c",
			"principalId": "`+azureCallerPrincipalID(t)+`"
		}
	}`)
	defer resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)

	groupScoped := armReq(t, http.MethodGet,
		scope+"/providers/Microsoft.Authorization/permissions?api-version=2022-04-01", "")
	defer groupScoped.Body.Close()
	require.Equal(t, http.StatusOK, groupScoped.StatusCode)
	assert.NotEmpty(t, azurePermissionActions(t, groupScoped.Body),
		"the resource group's permissions come from the role its assignment names")

	resourceScoped := armReq(t, http.MethodGet,
		scope+"/providers/Microsoft.Sql/servers/databases/permdb/providers/Microsoft.Authorization/permissions?api-version=2022-04-01", "")
	defer resourceScoped.Body.Close()
	require.Equal(t, http.StatusOK, resourceScoped.StatusCode)
	assert.NotEmpty(t, azurePermissionActions(t, resourceScoped.Body),
		"a resource inherits the permissions its resource group's assignments grant")
}

const azurePermissionsAssignmentGUID = "7c3a1e5d-9b42-4f08-a6d1-2e5b8c740f93"

// azurePermissionActions reads the actions out of a permission listing.
func azurePermissionActions(t *testing.T, body io.Reader) []string {
	t.Helper()
	var listing struct {
		Value []struct {
			Actions []string `json:"actions"`
		} `json:"value"`
	}
	require.NoError(t, json.NewDecoder(body).Decode(&listing))
	var actions []string
	for _, permission := range listing.Value {
		actions = append(actions, permission.Actions...)
	}
	return actions
}

// azureCallerPrincipalID reads the object id out of the bearer the tests
// carry — the principal Azure Resource Manager attributes their calls to.
func azureCallerPrincipalID(t *testing.T) string {
	t.Helper()
	token := strings.TrimPrefix(simARMBearer, "Bearer ")
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "the ARM bearer must be a JWT")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims struct {
		OID string `json:"oid"`
	}
	require.NoError(t, json.Unmarshal(payload, &claims))
	require.NotEmpty(t, claims.OID, "the bearer must carry an object id")
	return claims.OID
}
