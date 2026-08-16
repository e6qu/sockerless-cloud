package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_config_snapshots.go implements the SiteConfig snapshot history:
// WebApps_{List,Get}ConfigurationSnapshotInfo[Slot] and
// WebApps_RecoverSiteConfigurationSnapshot[Slot]. A snapshot record is
// written on every SiteConfig (config/web) update — both the production-site
// PUT and the slot PUT/PATCH paths call webRecordConfigSnapshot — and
// recovery restores the exact stored configuration into the site row, as
// real App Service does.

// webConfigSnapshotRow is one point-in-time capture of a site's SiteConfig.
// Keyed by "<site resource ID>/config/web/snapshots/<snapshotId>".
type webConfigSnapshotRow struct {
	ID         string     `json:"id"`
	SiteID     string     `json:"siteId"`
	SnapshotID int        `json:"snapshotId"`
	Time       string     `json:"time"`
	Config     SiteConfig `json:"config"`
}

var webConfigSnapshots sim.Store[webConfigSnapshotRow]

// webRecordConfigSnapshot captures the site's just-written SiteConfig as the
// next snapshot in its history. Snapshot IDs increase monotonically per site.
func webRecordConfigSnapshot(siteID string, cfg *SiteConfig) {
	if cfg == nil {
		cfg = &SiteConfig{}
	}
	prefix := siteID + "/config/web/snapshots/"
	next := 1
	for _, row := range webConfigSnapshots.Filter(func(row webConfigSnapshotRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		if row.SnapshotID >= next {
			next = row.SnapshotID + 1
		}
	}
	row := webConfigSnapshotRow{
		ID:         fmt.Sprintf("%s%d", prefix, next),
		SiteID:     siteID,
		SnapshotID: next,
		Time:       time.Now().UTC().Format(time.RFC3339),
		Config:     *cfg,
	}
	webConfigSnapshots.Put(row.ID, row)
	// The app's configuration just changed, so the platform's automatic-backup
	// snapshot of this app state exists from here on.
	webCaptureAppSnapshot(siteID)
}

func registerWebConfigSnapshots(srv *sim.Server, both func(string, string, http.HandlerFunc)) {
	webConfigSnapshots = sim.MakeStore[webConfigSnapshotRow](srv.DB(), "web_config_snapshots")

	snapID := func(r *http.Request) string {
		return webResourceID(r) + "/config/web/snapshots/" + sim.PathParam(r, "snapshotId")
	}

	// WebApps_ListConfigurationSnapshotInfo[Slot].
	both("GET", "/config/web/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/config/web/snapshots/"
		rows := webConfigSnapshots.Filter(func(row webConfigSnapshotRow) bool { return strings.HasPrefix(row.ID, prefix) })
		sort.Slice(rows, func(i, j int) bool { return rows[i].SnapshotID < rows[j].SnapshotID })
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"id":   row.ID,
				"name": fmt.Sprintf("%d", row.SnapshotID),
				"type": webChildType(r, "config/web/snapshots"),
				"properties": map[string]any{
					"time":       row.Time,
					"snapshotId": row.SnapshotID,
				},
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// WebApps_GetConfigurationSnapshot[Slot] — the full SiteConfig captured
	// in that snapshot.
	both("GET", "/config/web/snapshots/{snapshotId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webConfigSnapshots.Get(snapID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Configuration snapshot %q not found.", sim.PathParam(r, "snapshotId"))
			return
		}
		cfg := row.Config
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "web", &cfg))
	})

	// WebApps_RecoverSiteConfigurationSnapshot[Slot] — restore the exact
	// stored configuration into the site row.
	both("POST", "/config/web/snapshots/{snapshotId}/recover", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webConfigSnapshots.Get(snapID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Configuration snapshot %q not found.", sim.PathParam(r, "snapshotId"))
			return
		}
		store := webResourceStore(r)
		site, _ := store.Get(webResourceID(r))
		restored := row.Config
		site.Properties.SiteConfig = &restored
		store.Put(webResourceID(r), site)
		w.WriteHeader(http.StatusNoContent)
	})
}
