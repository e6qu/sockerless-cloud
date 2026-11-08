package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Migrating a site's in-app MySQL database to a remote server, and its content
// into an Azure Files share. Whether a site has in-app MySQL is its
// WEBSITE_MYSQL_ENABLED setting. No site here runs an in-app MySQL server, and
// no vendored page states what a share migration moves or what switching the
// site to the share changes, so both migrations answer a declared 501 rather
// than report a migration that moved nothing.

// App Service switches in-app MySQL on with this app setting.
func webSiteInAppMySQLEnabled(resourceID string) bool {
	config, ok := siteConfigStore.Get(resourceID)
	if !ok {
		return false
	}
	for name, value := range config.AppSettings {
		if strings.EqualFold(name, "WEBSITE_MYSQL_ENABLED") {
			return value == "1" || strings.EqualFold(value, "true")
		}
	}
	return false
}

// The document declares the migration on a production site only — a slot has
// no in-app database — and the status on both.
func registerWebMigrateMySQL(both, site func(string, string, http.HandlerFunc)) {
	site("POST", "/migratemysql", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		AzureError(w, "NotImplemented",
			"The simulator runs no in-app MySQL server, so it has no database to migrate.", http.StatusNotImplemented)
	})

	// WebApps_MigrateStorage.
	site("PUT", "/migrate", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		AzureError(w, "NotImplemented",
			"The simulator does not migrate a site's content into an Azure Files share.", http.StatusNotImplemented)
	})

	// No migration ever runs, so the status names no operation.
	both("GET", "/migratemysql/status", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webResourceID(r)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   id + "/migratemysql/status",
			"name": "status",
			"type": "Microsoft.Web/sites/migratemysql",
			"properties": map[string]any{
				"localMySqlEnabled": webSiteInAppMySQLEnabled(id),
			},
		})
	})
}
