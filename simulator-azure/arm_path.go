package main

import (
	"net/http"
	"strings"
)

// Azure Resource Manager matches URL paths case-insensitively, and its clients
// disagree: the azurerm Terraform provider sends `microsoft.cache/redis` where
// SDK clients send `Microsoft.Cache/Redis`. Both reach real Azure, so both must
// reach the simulator. Canonicalize to the casing the routes register; the
// server runs this on every request before routing (sim.Config.RewriteRequest).
func azureNormalizeRequestPath(r *http.Request) {
	// Resource-type and provider segments map to canonical mixed case.
	// Action and sub-resource verbs map to lowercase, because clients vary
	// (`appSettings` from terraform-provider-azurerm, `appsettings` from
	// azurestack) and the handlers register one casing.
	replacements := map[string]string{
		// No trailing slash: the segment also ends the SDK's
		// list-resource-groups URL.
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
		"/deletedvaults":                      "/deletedVaults",
		"/deletedworkspaces":                  "/deletedWorkspaces",
	}
	path := r.URL.Path
	lower := strings.ToLower(path)
	for lowerSeg, canonical := range replacements {
		if idx := strings.Index(lower, lowerSeg); idx >= 0 {
			path = path[:idx] + canonical + path[idx+len(lowerSeg):]
			lower = strings.ToLower(path)
		}
	}
	r.URL.Path = path
}
