package main

import (
	"net/http"
	"strings"
)

// Azure Resource Manager matches paths case-insensitively, and its clients
// disagree: the azurerm provider sends `microsoft.cache/redis` where SDKs send
// `Microsoft.Cache/Redis`. Canonicalize to the casing the routes register
// (sim.Config.RewriteRequest runs this before routing).
func azureNormalizeRequestPath(r *http.Request) {
	// Only Azure Resource Manager is case-insensitive; a blob name, a console route
	// or a Graph path keeps its casing.
	if lower := strings.ToLower(r.URL.Path); !strings.HasPrefix(lower, "/subscriptions/") && !strings.HasPrefix(lower, "/providers/") {
		return
	}
	// Resource-type and provider segments map to canonical mixed case; action and
	// sub-resource verbs to lowercase, because clients vary (`appSettings` from
	// terraform-provider-azurerm, `appsettings` from azurestack).
	replacements := map[string]string{
		// No trailing slash: the segment also ends the SDK's list-resource-groups URL.
		"/resourcegroups":            "/resourceGroups",
		"/microsoft.cache/redis":     "/Microsoft.Cache/Redis",
		"/microsoft.cache":           "/Microsoft.Cache",
		"/microsoft.servicebus":      "/Microsoft.ServiceBus",
		"/microsoft.apimanagement":   "/Microsoft.ApiManagement",
		"/microsoft.dbforpostgresql": "/Microsoft.DBforPostgreSQL",
		"/microsoft.keyvault":        "/Microsoft.KeyVault",
		"/microsoft.storage":         "/Microsoft.Storage",
		// azure-mgmt-web spells the namespace "microsoft.Web" in several
		// StaticSites URL templates.
		"/microsoft.web": "/Microsoft.Web",

		"/appsettings":                        "/appsettings",
		"/connectionstrings":                  "/connectionstrings",
		"/slotconfignames":                    "/slotconfignames",
		"/listsecrets":                        "/listsecrets",
		"/listcredentials":                    "/listcredentials",
		"/checknameavailability":              "/checknameavailability",
		"/authsettings":                       "/authsettings",
		"/authsettingsv2":                     "/authsettingsv2",
		"/publishingcredentials":              "/publishingcredentials",
		"/azurestorageaccounts":               "/azurestorageaccounts",
		"/basicpublishingcredentialspolicies": "/basicpublishingcredentialspolicies",
		"/sourcecontrols":                     "/sourcecontrols",
		// terraform-provider-azurerm spells these artifactTypes/vmImage; the handlers
		// register the REST specification's lowercase.
		"/artifacttypes/vmimage": "/artifacttypes/vmimage",
		"/deletedvaults":         "/deletedVaults",
		"/deletedworkspaces":     "/deletedWorkspaces",
		"/getauthtoken":          "/getAuthtoken",
	}
	path := r.URL.Path
	lower := strings.ToLower(path)
	for lowerSeg, canonical := range replacements {
		if idx := strings.Index(lower, lowerSeg); idx >= 0 {
			path = path[:idx] + canonical + path[idx+len(lowerSeg):]
			lower = strings.ToLower(path)
		}
	}
	// A site's host and function key listings register lowercase, as the Azure CLI
	// sends them; terraform-provider-azurerm sends `listKeys`. Other `listKeys`
	// actions register mixed case, so only a site's fold.
	if strings.Contains(lower, "/providers/microsoft.web/sites/") && strings.HasSuffix(lower, "/listkeys") {
		path = path[:len(path)-len("/listkeys")] + "/listkeys"
	}
	r.URL.Path = path
}
