package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Migrating a site's in-app MySQL database to a remote one.
//
// App Service offers a site a MySQL database running beside it, in the app's
// own sandbox, and MigrateMySql moves that database out to a remote server the
// caller names. The operation is a request the platform records and then reports
// on, and both halves of that are the simulator's to hold: whether the site has
// in-app MySQL at all is the WEBSITE_MYSQL_ENABLED app setting it already
// stores, and the migration the caller started is state like any other.
//
// What the simulator does not do is move bytes — there is no MySQL process here
// whose tables could be copied. That does not make the operation unanswerable:
// what a caller asks for is that the platform accept the request and report its
// progress, and a request against a site with no in-app MySQL is refused here
// exactly as App Service refuses it, before any copying would begin.

// webMySQLMigration is a migration a caller started against a site.
type webMySQLMigration struct {
	Site        string `json:"site"`
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
}

var webMySQLMigrations sim.Store[webMySQLMigration]

// webSiteInAppMySQLEnabled reports whether the site runs the in-app MySQL
// database, which is the app setting App Service switches it on with.
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

// registerWebMigrateMySQL mounts the migration and the status beside it. The
// document declares the migration on a production site only — a deployment slot
// has no in-app database of its own to move — while the status is readable on
// both.
func registerWebMigrateMySQL(srv *sim.Server, both, site func(string, string, http.HandlerFunc)) {
	webMySQLMigrations = sim.MakeStore[webMySQLMigration](srv.DB(), "web_mysql_migrations")

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
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		// Both are required by the document, and a migration with neither a
		// destination nor a type is not a migration.
		if request.Properties.ConnectionString == "" || request.Properties.MigrationType == "" {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"connectionString and migrationType are both required to migrate a MySQL database.")
			return
		}

		id := webResourceID(r)
		if !webSiteInAppMySQLEnabled(id) {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"The app '%s' does not have in-app MySQL enabled, so it has no database to migrate.",
				sim.PathParam(r, "siteName"))
			return
		}

		// The operation the caller can now ask the status of. It is complete
		// when it is recorded: the platform has nothing left to do that the
		// caller could observe, and reporting it as running would be reporting
		// work nothing is performing.
		migration := webMySQLMigration{
			Site:        id,
			OperationID: generateUUID(),
			Status:      "Succeeded",
		}
		webMySQLMigrations.Put(id, migration)

		// The document answers this with an Operation — the operation itself,
		// not a resource with properties — and names the header a client polls
		// it through.
		started := time.Now().UTC().Format(time.RFC3339)
		// Absolute, as the header is defined and as a client's poller requires:
		// it is a URL to fetch, not a path within this response.
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

	both("GET", "/migratemysql/status", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webResourceID(r)
		properties := map[string]any{
			"localMySqlEnabled": webSiteInAppMySQLEnabled(id),
		}
		// The operation id and its status belong to a migration that was asked
		// for. A site that has never been migrated reports neither, rather than
		// an operation nobody started.
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
