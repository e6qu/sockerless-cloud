package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// App Service's runtime-stack catalogs list the built-in runtime stacks the
// App Service runs, read from the same tables (appServiceLinuxStacks and
// functionsHostStacks) the site path starts a stack's image from, so the two
// cannot disagree. Every stack here is a Linux stack; a Windows selection
// lists none.

// registerWebStackCatalogs mounts the six spellings of the four catalog reads.
func registerWebStackCatalogs(srv *sim.Server) {
	webAppStacks := func(location string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			value := []any{}
			if sel := r.URL.Query().Get("stackOsType"); sel == "" || strings.EqualFold(sel, "Linux") || strings.EqualFold(sel, "All") {
				value = webAppStackEntries(location)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
		}
	}
	availableStacks := func(w http.ResponseWriter, r *http.Request) {
		value := []any{}
		if sel := r.URL.Query().Get("osTypeSelected"); sel == "" || strings.EqualFold(sel, "Linux") || strings.EqualFold(sel, "All") {
			value = availableStackEntries()
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
	}
	functionAppStacks := func(location string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			value := []any{}
			if sel := r.URL.Query().Get("stackOsType"); sel == "" || strings.EqualFold(sel, "Linux") || strings.EqualFold(sel, "All") {
				value = functionAppStackEntries(location)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
		}
	}

	srv.HandleFunc("GET /providers/Microsoft.Web/availableStacks", availableStacks)
	srv.HandleFunc("GET /providers/Microsoft.Web/webAppStacks", webAppStacks(""))
	srv.HandleFunc("GET /providers/Microsoft.Web/functionAppStacks", functionAppStacks(""))
	srv.HandleFunc("GET /providers/Microsoft.Web/locations/{location}/webAppStacks", func(w http.ResponseWriter, r *http.Request) {
		webAppStacks(sim.PathParam(r, "location"))(w, r)
	})
	srv.HandleFunc("GET /providers/Microsoft.Web/locations/{location}/functionAppStacks", func(w http.ResponseWriter, r *http.Request) {
		functionAppStacks(sim.PathParam(r, "location"))(w, r)
	})
	srv.HandleFunc("GET "+webSubscriptionProvider+"/availableStacks", availableStacks)
}

// webAppStackEntries renders the stack table as WebAppStack resources: one per
// stack, its major versions in table order, each holding its minor versions.
func webAppStackEntries(location string) []any {
	type major struct {
		value, display string
		minors         []any
	}
	type stack struct {
		value, display string
		majors         []*major
	}
	var stacks []*stack
	for _, s := range appServiceLinuxStacks {
		var st *stack
		for _, existing := range stacks {
			if existing.value == s.Stack {
				st = existing
			}
		}
		if st == nil {
			st = &stack{value: s.Stack, display: s.StackDisplay}
			stacks = append(stacks, st)
		}
		var mj *major
		for _, existing := range st.majors {
			if existing.value == s.Major {
				mj = existing
			}
		}
		if mj == nil {
			mj = &major{value: s.Major, display: s.MajorDisplay}
			st.majors = append(st.majors, mj)
		}
		mj.minors = append(mj.minors, map[string]any{
			"displayText": s.MinorDisplay,
			"value":       s.Minor,
			"stackSettings": map[string]any{
				"linuxRuntimeSettings": map[string]any{
					"runtimeVersion":           s.LinuxFxVersion(),
					"remoteDebuggingSupported": false,
					"appInsightsSettings":      map[string]any{"isSupported": false},
					"gitHubActionSettings":     map[string]any{"isSupported": false},
				},
			},
		})
	}
	out := make([]any, 0, len(stacks))
	for _, st := range stacks {
		majors := make([]any, 0, len(st.majors))
		for _, mj := range st.majors {
			majors = append(majors, map[string]any{
				"displayText":   mj.display,
				"value":         mj.value,
				"minorVersions": mj.minors,
			})
		}
		id := "/providers/Microsoft.Web/webAppStacks/" + st.value
		if location != "" {
			id = "/providers/Microsoft.Web/locations/" + location + "/webAppStacks/" + st.value
		}
		entry := map[string]any{
			"id":   id,
			"name": st.value,
			"type": "Microsoft.Web/webAppStacks",
			"properties": map[string]any{
				"displayText":   st.display,
				"value":         st.value,
				"preferredOs":   "Linux",
				"majorVersions": majors,
			},
		}
		if location != "" {
			entry["location"] = location
		}
		out = append(out, entry)
	}
	return out
}

// availableStackEntries renders the stack table as the ApplicationStack
// resources of the availableStacks read: one per stack, one major version per
// runnable version.
func availableStackEntries() []any {
	var order []string
	byStack := map[string][]any{}
	display := map[string]string{}
	for _, s := range appServiceLinuxStacks {
		if _, seen := byStack[s.Stack]; !seen {
			order = append(order, s.Stack)
			display[s.Stack] = s.StackDisplay
		}
		byStack[s.Stack] = append(byStack[s.Stack], map[string]any{
			"displayVersion": s.MinorDisplay,
			"runtimeVersion": s.LinuxFxVersion(),
			"isDefault":      false,
			"minorVersions":  []any{},
		})
	}
	out := make([]any, 0, len(order))
	for _, name := range order {
		out = append(out, map[string]any{
			"id":   "/providers/Microsoft.Web/availableStacks/" + name,
			"name": name,
			"type": "Microsoft.Web/availableStacks",
			"properties": map[string]any{
				"name":          name,
				"display":       display[name],
				"majorVersions": byStack[name],
				"frameworks":    []any{},
			},
		})
	}
	return out
}

// functionAppStackEntries renders the Functions host stacks as FunctionAppStack
// resources: one per runtime, one major version per runtime version, each
// carrying the app settings and site configuration a function app on it is
// created with.
func functionAppStackEntries(location string) []any {
	var order []string
	majors := map[string][]any{}
	display := map[string]string{}
	for _, s := range functionsHostStacks {
		if _, seen := majors[s.Runtime]; !seen {
			order = append(order, s.Runtime)
			display[s.Runtime] = s.RuntimeDisplay
		}
		versionDisplay := s.RuntimeDisplay + " " + s.Version
		majors[s.Runtime] = append(majors[s.Runtime], map[string]any{
			"displayText": versionDisplay,
			"value":       s.Version,
			"minorVersions": []any{map[string]any{
				"displayText": versionDisplay,
				"value":       s.Version,
				"stackSettings": map[string]any{
					"linuxRuntimeSettings": map[string]any{
						"runtimeVersion":           s.LinuxFx,
						"isPreview":                false,
						"isDeprecated":             false,
						"isHidden":                 false,
						"remoteDebuggingSupported": false,
						"appInsightsSettings":      map[string]any{"isSupported": false},
						"gitHubActionSettings":     map[string]any{"isSupported": false},
						"appSettingsDictionary":    map[string]any{"FUNCTIONS_WORKER_RUNTIME": s.Runtime},
						"siteConfigPropertiesDictionary": map[string]any{
							"use32BitWorkerProcess": false,
							"linuxFxVersion":        s.LinuxFx,
						},
						"supportedFunctionsExtensionVersions": []any{"~4"},
						"endOfLifeDate":                       s.EndOfLife,
					},
				},
			}},
		})
	}
	out := make([]any, 0, len(order))
	for _, runtime := range order {
		id := "/providers/Microsoft.Web/functionAppStacks/" + runtime
		if location != "" {
			id = "/providers/Microsoft.Web/locations/" + location + "/functionAppStacks/" + runtime
		}
		entry := map[string]any{
			"id":   id,
			"name": runtime,
			"type": "Microsoft.Web/functionAppStacks",
			"properties": map[string]any{
				"displayText":   display[runtime],
				"value":         runtime,
				"preferredOs":   "Linux",
				"majorVersions": majors[runtime],
			},
		}
		if location != "" {
			entry["location"] = location
		}
		out = append(out, entry)
	}
	return out
}
