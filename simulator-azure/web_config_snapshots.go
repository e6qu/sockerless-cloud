package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The SiteConfig snapshot history: List/GetConfigurationSnapshotInfo and
// RecoverSiteConfigurationSnapshot. Every config/web update, site or slot,
// records a snapshot through webRecordConfigSnapshot, and recovery restores the
// exact stored configuration, as App Service does.

// Keyed by "<site resource ID>/config/web/snapshots/<snapshotId>".
type webConfigSnapshotRow struct {
	ID         string     `json:"id"`
	SiteID     string     `json:"siteId"`
	SnapshotID int        `json:"snapshotId"`
	Time       string     `json:"time"`
	Config     SiteConfig `json:"config"`
}

var webConfigSnapshots sim.Store[webConfigSnapshotRow]

// Snapshot IDs increase per site.
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
	// The configuration changed, so the automatic-backup snapshot of this state
	// exists from here on.
	webCaptureAppSnapshot(siteID)
}

func registerWebConfigSnapshots(srv *sim.Server, both func(string, string, http.HandlerFunc)) {
	webConfigSnapshots = sim.MakeStore[webConfigSnapshotRow](srv.DB(), "web_config_snapshots")

	snapID := func(r *http.Request) string {
		return webResourceID(r) + "/config/web/snapshots/" + sim.PathParam(r, "snapshotId")
	}

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

	both("GET", "/config/web/snapshots/{snapshotId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webConfigSnapshots.Get(snapID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Configuration snapshot %q not found.", sim.PathParam(r, "snapshotId"))
			return
		}
		cfg := row.Config
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "web", &cfg))
	})

	both("POST", "/config/web/snapshots/{snapshotId}/recover", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webConfigSnapshots.Get(snapID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
