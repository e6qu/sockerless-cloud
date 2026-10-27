package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Migrating a site's in-app MySQL database to a remote server, and its content
// into an Azure Files share. Whether a site has in-app MySQL is its
// WEBSITE_MYSQL_ENABLED setting, and a request against one without is refused as
// App Service refuses it. The simulator records the migration and moves no
// data.

var webStorageMigrations sim.Store[webMySQLMigration]

type webMySQLMigration struct {
	Site        string `json:"site"`
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
}

var webMySQLMigrations sim.Store[webMySQLMigration]

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
func registerWebMigrateMySQL(srv *sim.Server, both, site func(string, string, http.HandlerFunc)) {
	webMySQLMigrations = sim.MakeStore[webMySQLMigration](srv.DB(), "web_mysql_migrations")
	webStorageMigrations = sim.MakeStore[webMySQLMigration](srv.DB(), "web_storage_migrations")

	site("POST", "/migratemysql", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var request struct {
			Properties struct {
				ConnectionString string `json:"connectionString"`
				MigrationType    string `json:"migrationType"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &request); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		// The document requires both.
		if request.Properties.ConnectionString == "" || request.Properties.MigrationType == "" {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"connectionString and migrationType are both required to migrate a MySQL database.")
			return
		}

		id := webResourceID(r)
		if !webSiteInAppMySQLEnabled(id) {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"The app '%s' does not have in-app MySQL enabled, so it has no database to migrate.",
				sim.PathParam(r, "siteName"))
			return
		}

		// Recorded as complete: no data moves.
		migration := webMySQLMigration{
			Site:        id,
			OperationID: sim.NewUUID(),
			Status:      "Succeeded",
		}
		webMySQLMigrations.Put(id, migration)

		// The document answers with an Operation, not a resource, and names the
		// header a client polls it through.
		started := time.Now().UTC().Format(time.RFC3339)
		// Absolute, as the header is defined and a poller requires.
		w.Header().Set("Location", fmt.Sprintf("%s://%s%s/migratemysql/status?api-version=%s",
			azureRequestScheme(r), r.Host, id, r.URL.Query().Get("api-version")))
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":           migration.OperationID,
			"name":         migration.OperationID,
			"status":       migration.Status,
			"createdTime":  started,
			"modifiedTime": started,
		})
	})

	// WebApps_MigrateStorage moves content into the named Azure Files share and
	// optionally switches the site to it. The simulator records the operation and
	// moves no data: these sites serve from a container image, not a share.
	site("PUT", "/migrate", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var request struct {
			Properties struct {
				AzurefilesConnectionString string `json:"azurefilesConnectionString"`
				AzurefilesShare            string `json:"azurefilesShare"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &request); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		// The document requires both.
		if request.Properties.AzurefilesConnectionString == "" || request.Properties.AzurefilesShare == "" {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"azurefilesConnectionString and azurefilesShare are both required to migrate a site's content.")
			return
		}

		id := webResourceID(r)
		migration := webMySQLMigration{Site: id, OperationID: sim.NewUUID(), Status: "Succeeded"}
		webStorageMigrations.Put(id, migration)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   id + "/migrate",
			"name": sim.PathParam(r, "siteName"),
			"type": "Microsoft.Web/sites/migrate",
			"properties": map[string]any{
				"operationId": migration.OperationID,
			},
		})
	})

	both("GET", "/migratemysql/status", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webResourceID(r)
		properties := map[string]any{
			"localMySqlEnabled": webSiteInAppMySQLEnabled(id),
		}
		// A site never migrated reports no operation id or status.
		if migration, ok := webMySQLMigrations.Get(id); ok {
			properties["operationId"] = migration.OperationID
			properties["migrationOperationStatus"] = migration.Status
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         id + "/migratemysql/status",
			"name":       "status",
			"type":       "Microsoft.Web/sites/migratemysql",
			"properties": properties,
		})
	})
}
