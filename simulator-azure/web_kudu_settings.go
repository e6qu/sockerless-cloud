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

// Kudu's settings API: GET /api/settings reads every setting Kudu acts on (its
// defaults, under API-written settings, under app settings), /api/settings/{key}
// reads one, POST writes and DELETE removes. Kudu keeps API-written settings in
// /home/site/deployments/settings.xml; the simulator keeps them in a store
// beside the site, which a slot swap moves with the content.

// Keyed by site resource ID.
var kuduSiteSettings sim.Store[map[string]string]

// An app setting overrides an API-written setting, which overrides Kudu's
// default.
func kuduSettings(site *Site) map[string]string {
	out := maps.Clone(kuduSettingDefaults)
	written, _ := kuduSiteSettings.Get(site.ID)
	for k, v := range written {
		kuduSetFold(out, k, v)
	}
	for k, v := range webResolvedAppSettings(site) {
		kuduSetFold(out, k, v)
	}
	return out
}

// Kudu's settings are case-insensitive.
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
