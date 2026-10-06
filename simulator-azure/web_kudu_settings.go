package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_kudu_settings.go serves Kudu's settings API on a site's SCM host: GET
// /api/settings reads every setting Kudu acts on — its defaults, under the
// settings written through this API, under the site's app settings — and
// /api/settings/{key} reads one; POST /api/settings writes settings and
// DELETE /api/settings/{key} removes one. The settings written here live in
// the site's file system, so a slot swap carries them with the content.

// kuduSiteSettings holds the settings written through the API, keyed by the
// site's resource ID.
var kuduSiteSettings sim.Store[map[string]string]

// kuduSettings is the merged view: an app setting overrides a setting written
// through the API, which overrides Kudu's default.
func kuduSettings(site *Site) map[string]string {
	out := maps.Clone(kuduSettingDefaults)
	written, _ := kuduSiteSettings.Get(site.ID)
	for k, v := range written {
		kuduSetFold(out, k, v)
	}
	for k, v := range siteAppSettings(site) {
		kuduSetFold(out, k, v)
	}
	return out
}

// kuduSetFold sets k in m, replacing a key that differs from it only in case,
// as Kudu's settings are case-insensitive.
func kuduSetFold(m map[string]string, k, v string) {
	for existing := range m {
		if strings.EqualFold(existing, k) && existing != k {
			delete(m, existing)
		}
	}
	m[k] = v
}

func kuduSettingsAPI(w http.ResponseWriter, r *http.Request, site *Site, key string) {
	switch {
	case key == "" && r.Method == http.MethodGet:
		sim.WriteJSON(w, http.StatusOK, kuduSettings(site))
	case key == "" && r.Method == http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			kuduWebAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
			kuduWebAPIError(w, http.StatusBadRequest, "The settings must be a JSON object.")
			return
		}
		updates := map[string]string{}
		for k, v := range raw {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				s = strings.TrimSpace(string(v))
			}
			updates[k] = s
		}
		kuduSiteSettings.Upsert(site.ID, func(m *map[string]string) {
			if *m == nil {
				*m = map[string]string{}
			}
			for k, v := range updates {
				kuduSetFold(*m, k, v)
			}
		})
		w.WriteHeader(http.StatusNoContent)
	case key == "":
		kuduMethod(w, r, http.MethodGet, http.MethodPost)
	case r.Method == http.MethodGet:
		for k, v := range kuduSettings(site) {
			if strings.EqualFold(k, key) {
				sim.WriteJSON(w, http.StatusOK, v)
				return
			}
		}
		kuduWebAPIError(w, http.StatusNotFound, fmt.Sprintf("'%s' is not a valid setting.", key))
	case r.Method == http.MethodDelete:
		kuduSiteSettings.Update(site.ID, func(m *map[string]string) {
			for k := range *m {
				if strings.EqualFold(k, key) {
					delete(*m, k)
				}
			}
		})
		w.WriteHeader(http.StatusNoContent)
	default:
		kuduMethod(w, r, http.MethodGet, http.MethodDelete)
	}
}
