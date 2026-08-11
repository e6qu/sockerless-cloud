package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const authAPIVersion = "2022-04-01"

// TestRoleDefinition_CustomCreateGetDelete exercises the custom-role-definition
// lifecycle (RoleDefinitions_CreateOrUpdate / Get / Delete) via az-rest.
func TestRoleDefinition_CustomCreateGetDelete(t *testing.T) {
	roleDefID := "77777777-8888-9999-aaaa-bbbbbbbbbbbb"
	url := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/%s?api-version=%s",
		baseURL, subscriptionID, roleDefID, authAPIVersion)
	body := fmt.Sprintf(`{
		"properties": {
			"roleName": "Sockerless CLI Custom Role",
			"type": "CustomRole",
			"description": "Created via az rest.",
			"permissions": [{"actions": ["*/read"], "notActions": []}],
			"assignableScopes": ["/subscriptions/%s"]
		}
	}`, subscriptionID)

	out := runCLI(t, azRest("PUT", url, body))
	var def struct {
		Name       string `json:"name"`
		Properties struct {
			RoleName string `json:"roleName"`
			Type     string `json:"type"`
		} `json:"properties"`
	}
	parseJSON(t, out, &def)
	assert.Equal(t, roleDefID, def.Name)
	assert.Equal(t, "Sockerless CLI Custom Role", def.Properties.RoleName)
	assert.Equal(t, "CustomRole", def.Properties.Type)

	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &def)
	assert.Equal(t, "Sockerless CLI Custom Role", def.Properties.RoleName)

	runCLI(t, azRest("DELETE", url, ""))

	cmd := azRest("GET", url, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "custom role definition GET must fail after delete")
}

func TestRoleAssignment_CreateAndShow(t *testing.T) {
	raName := "00000000-0000-0000-0000-000000000099"
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Authorization/roleAssignments/%s?api-version=%s",
		baseURL, subscriptionID, resourceGroup, raName, authAPIVersion)

	principalID := "00000000-0000-0000-0000-0000000000aa"
	body := fmt.Sprintf(`{
		"properties": {
			"roleDefinitionId": "/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7",
			"principalId": "%s"
		}
	}`, subscriptionID, principalID)

	out := runCLI(t, azRest("PUT", url, body))

	var result struct {
		Name       string `json:"name"`
		Properties struct {
			RoleDefinitionId string `json:"roleDefinitionId"`
			PrincipalId      string `json:"principalId"`
		} `json:"properties"`
	}
	parseJSON(t, out, &result)
	assert.Equal(t, raName, result.Name)
	assert.Equal(t, principalID, result.Properties.PrincipalId)

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &result)
	assert.Equal(t, raName, result.Name)

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

func TestRoleAssignment_Delete(t *testing.T) {
	raName := "00000000-0000-0000-0000-000000000098"
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Authorization/roleAssignments/%s?api-version=%s",
		baseURL, subscriptionID, resourceGroup, raName, authAPIVersion)

	body := fmt.Sprintf(`{
		"properties": {
			"roleDefinitionId": "/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7",
			"principalId": "00000000-0000-0000-0000-0000000000bb"
		}
	}`, subscriptionID)

	runCLI(t, azRest("PUT", url, body))
	runCLI(t, azRest("DELETE", url, ""))

	// Verify deletion
	cmd := azRest("GET", url, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "Expected GET to fail after deletion")
}

// TestRoleAssignment_RejectsUnknownRoleDefinition pins that a role assignment
// referencing a role definition GUID that isn't a known built-in is rejected
// with 400, as real Azure does (RoleDefinitionDoesNotExist).
func TestRoleAssignment_RejectsUnknownRoleDefinition(t *testing.T) {
	raName := "00000000-0000-0000-0000-000000000097"
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Authorization/roleAssignments/%s?api-version=%s",
		baseURL, subscriptionID, resourceGroup, raName, authAPIVersion)

	body := fmt.Sprintf(`{
		"properties": {
			"roleDefinitionId": "/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/deadbeef-0000-0000-0000-000000000000",
			"principalId": "00000000-0000-0000-0000-0000000000cc"
		}
	}`, subscriptionID)

	out, err := azRest("PUT", url, body).CombinedOutput()
	assert.Error(t, err, "PUT with unknown roleDefinitionId must fail")
	assert.Contains(t, string(out), "RoleDefinitionDoesNotExist")
}

// TestRoleAssignment_RejectsNonGUIDName pins that a role assignment whose name
// is not a GUID is rejected with 400 (real Azure requires the name be a GUID).
func TestRoleAssignment_RejectsNonGUIDName(t *testing.T) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Authorization/roleAssignments/not-a-guid?api-version=%s",
		baseURL, subscriptionID, resourceGroup, authAPIVersion)

	body := fmt.Sprintf(`{
		"properties": {
			"roleDefinitionId": "/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7",
			"principalId": "00000000-0000-0000-0000-0000000000dd"
		}
	}`, subscriptionID)

	out, err := azRest("PUT", url, body).CombinedOutput()
	assert.Error(t, err, "PUT with non-GUID name must fail")
	assert.Contains(t, string(out), "InvalidRoleAssignmentName")
}

// TestRoleAssignment_List exercises the collection list endpoint, including the
// $filter=principalId eq 'X' OData filter.
func TestRoleAssignment_List(t *testing.T) {
	principalID := "00000000-0000-0000-0000-0000000000ee"
	raName := "00000000-0000-0000-0000-000000000096"
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Authorization/roleAssignments/%s?api-version=%s",
		baseURL, subscriptionID, resourceGroup, raName, authAPIVersion)
	body := fmt.Sprintf(`{
		"properties": {
			"roleDefinitionId": "/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7",
			"principalId": "%s"
		}
	}`, subscriptionID, principalID)
	runCLI(t, azRest("PUT", url, body))
	defer runCLI(t, azRest("DELETE", url, ""))

	listURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Authorization/roleAssignments?api-version=%s&$filter=principalId+eq+'%s'",
		baseURL, subscriptionID, resourceGroup, authAPIVersion, principalID)
	out := runCLI(t, azRest("GET", listURL, ""))

	var list struct {
		Value []struct {
			Name       string `json:"name"`
			Properties struct {
				PrincipalId string `json:"principalId"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	require.NotEmpty(t, list.Value, "list must return the created assignment")
	found := false
	for _, ra := range list.Value {
		assert.Equal(t, principalID, ra.Properties.PrincipalId, "$filter=principalId must only return matching assignments")
		if ra.Name == raName {
			found = true
		}
	}
	assert.True(t, found, "created assignment must appear in the filtered list")
}

// TestRoleDefinition_ReaderHasReadOnlyPermissions pins that the Reader built-in
// role returns its real granular permission (*/read), not a wildcard.
func TestRoleDefinition_ReaderHasReadOnlyPermissions(t *testing.T) {
	url := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7?api-version=%s",
		baseURL, subscriptionID, authAPIVersion)
	out := runCLI(t, azRest("GET", url, ""))

	var def struct {
		Properties struct {
			RoleName    string `json:"roleName"`
			Permissions []struct {
				Actions []string `json:"actions"`
			} `json:"permissions"`
		} `json:"properties"`
	}
	parseJSON(t, out, &def)
	assert.Equal(t, "Reader", def.Properties.RoleName)
	require.NotEmpty(t, def.Properties.Permissions)
	assert.Equal(t, []string{"*/read"}, def.Properties.Permissions[0].Actions,
		"Reader must carry */read, not the fake wildcard *")
}
