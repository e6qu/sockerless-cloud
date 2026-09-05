package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Dataflow's config store settings: named values a project, folder or
// organization sets, which a job then resolves.
//
// The same collection hangs off all three parents, and they are one store keyed
// by the full resource name — a setting is identified by where it was set, so
// nothing is gained by keeping three.
//
// resolve answers which setting applies at the resource it is asked about. It
// does not walk up the hierarchy, because the request does not say what the
// hierarchy is: a resolve addressed at a project names the project and nothing
// else, and the folder or organization above it is not something this API
// carries. Answering with an inherited value would mean inventing a parentage
// the caller never stated.
func registerDataflowConfigStore(srv *sim.Server) {
	settings := sim.MakeStore[map[string]any](srv.DB(), "dataflow_config_store_settings")

	// Every parent shape the document declares, in the order a setting
	// resolves: the nearest ancestor wins.
	parents := []struct{ collection, id string }{
		{"projects", "projectsId"},
		{"folders", "foldersId"},
		{"organizations", "organizationsId"},
	}

	for _, parent := range parents {
		parent := parent
		base := "/v1b3/" + parent.collection + "/{" + parent.id + "}/locations/{locationsId}/configStoreSettings"

		parentName := func(r *http.Request) string {
			return parent.collection + "/" + sim.PathParam(r, parent.id) +
				"/locations/" + sim.PathParam(r, "locationsId")
		}
		settingName := func(r *http.Request) string {
			return parentName(r) + "/configStoreSettings/" + sim.PathParam(r, "configStoreSettingsId")
		}

		srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
			var setting map[string]any
			if err := sim.ReadJSON(r, &setting); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			id := r.URL.Query().Get("configStoreSettingId")
			if id == "" {
				// The id may also ride in the body's name, which is what a
				// client that built the resource first sends.
				if name, _ := setting["name"].(string); name != "" {
					id = name[strings.LastIndex(name, "/")+1:]
				}
			}
			if id == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"a config store setting needs an id to be addressed by")
				return
			}
			name := parentName(r) + "/configStoreSettings/" + id
			if _, taken := settings.Get(name); taken {
				GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
					"config store setting %q already exists", name)
				return
			}
			setting["name"] = name
			settings.Put(name, setting)
			sim.WriteJSON(w, http.StatusOK, setting)
		})

		srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
			prefix := parentName(r) + "/configStoreSettings/"
			held := settings.Filter(func(m map[string]any) bool {
				name, _ := m["name"].(string)
				return strings.HasPrefix(name, prefix)
			})
			sort.Slice(held, func(i, j int) bool {
				a, _ := held[i]["name"].(string)
				b, _ := held[j]["name"].(string)
				return a < b
			})
			items := []any{}
			for _, setting := range held {
				items = append(items, setting)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"configStoreSettings": items})
		})

		srv.HandleFunc("GET "+base+"/{configStoreSettingsId}", func(w http.ResponseWriter, r *http.Request) {
			held, ok := settings.Get(settingName(r))
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"config store setting %q not found", settingName(r))
				return
			}
			sim.WriteJSON(w, http.StatusOK, held)
		})

		srv.HandleFunc("DELETE "+base+"/{configStoreSettingsId}", func(w http.ResponseWriter, r *http.Request) {
			if !settings.Delete(settingName(r)) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"config store setting %q not found", settingName(r))
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
		})

		// Go's router has no way to spell a colon verb — a wildcard segment has
		// to end at its brace — so the id and the verb arrive together and are
		// split here, which is how every other colon verb in this simulator is
		// mounted.
		srv.HandleFunc("POST "+base+"/{configStoreSettingsId}", func(w http.ResponseWriter, r *http.Request) {
			id, verb, _ := strings.Cut(sim.PathParam(r, "configStoreSettingsId"), ":")
			if verb != "resolve" {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no config store setting method named %q", verb)
				return
			}
			resolved, ok := settings.Get(parentName(r) + "/configStoreSettings/" + id)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no config store setting named %q applies here", id)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"setting": resolved, "choices": []any{resolved},
			})
		})
	}
}
