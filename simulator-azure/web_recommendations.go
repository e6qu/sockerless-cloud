package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The simulator runs no advisory engine, so the lists and the history are
// empty and a rule read finds nothing. The filters are the client's own
// decisions, recorded against the scope until they are reset.

type webRecommendationFilters struct {
	Scope       string   `json:"scope"`
	DisabledAll bool     `json:"disabledAll"`
	Disabled    []string `json:"disabled"`
}

var webRecommendationSuppressions sim.Store[webRecommendationFilters]

func registerWebRecommendations(srv *sim.Server) {
	webRecommendationSuppressions = sim.MakeStore[webRecommendationFilters](
		srv.DB(), "web_recommendation_filters")

	sub := webSubscriptionProvider + "/recommendations"
	srv.HandleFunc("GET "+sub, func(w http.ResponseWriter, r *http.Request) {
		webWriteRecommendations(w)
	})
	srv.HandleFunc("POST "+sub+"/reset", func(w http.ResponseWriter, r *http.Request) {
		webResetRecommendationFilters(w, armSubscriptionID(sim.PathParam(r, "subscriptionId")))
	})
	srv.HandleFunc("POST "+sub+"/{ruleName}/disable", func(w http.ResponseWriter, r *http.Request) {
		webDisableRecommendationRule(w, armSubscriptionID(sim.PathParam(r, "subscriptionId")),
			sim.PathParam(r, "ruleName"))
	})

	// Spell the wildcard {name} to match every other hostingEnvironments route,
	// which is what aseResourceID reads.
	ase := webProvider + "/hostingEnvironments/{name}"
	srv.HandleFunc("GET "+ase+"/recommendations", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webWriteRecommendations(w)
	})
	srv.HandleFunc("GET "+ase+"/recommendationHistory", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webWriteRecommendations(w)
	})
	srv.HandleFunc("POST "+ase+"/recommendations/disable", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webDisableAllRecommendations(w, aseResourceID(r))
	})
	srv.HandleFunc("POST "+ase+"/recommendations/reset", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webResetRecommendationFilters(w, aseResourceID(r))
	})
	srv.HandleFunc("POST "+ase+"/recommendations/{ruleName}/disable", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webDisableRecommendationRule(w, aseResourceID(r), sim.PathParam(r, "ruleName"))
	})
	srv.HandleFunc("GET "+ase+"/recommendations/{ruleName}", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webWriteRecommendationRule(w, sim.PathParam(r, "ruleName"))
	})

	// The document declares no slot spelling for any of these.
	site := webProvider + "/sites/{siteName}"
	srv.HandleFunc("GET "+site+"/recommendations", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webWriteRecommendations(w)
	})
	srv.HandleFunc("GET "+site+"/recommendationHistory", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webWriteRecommendations(w)
	})
	srv.HandleFunc("POST "+site+"/recommendations/disable", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webDisableAllRecommendations(w, webResourceID(r))
	})
	srv.HandleFunc("POST "+site+"/recommendations/reset", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webResetRecommendationFilters(w, webResourceID(r))
	})
	srv.HandleFunc("POST "+site+"/recommendations/{ruleName}/disable", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webDisableRecommendationRule(w, webResourceID(r), sim.PathParam(r, "ruleName"))
	})
	srv.HandleFunc("GET "+site+"/recommendations/{ruleName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webWriteRecommendationRule(w, sim.PathParam(r, "ruleName"))
	})
}

func webEnvironmentMissing(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := webHostingEnvironments.Get(aseResourceID(r)); ok {
		return false
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/hostingEnvironments/%s' was not found.",
		sim.PathParam(r, "name"))
	return true
}

func webWriteRecommendations(w http.ResponseWriter) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": webRecommendations()})
}

// webRecommendations is empty: a recommendation is Azure Advisor's judgement
// formed from telemetry the simulator does not collect. Every read answers
// from here, so a listing and a rule read never contradict each other.
func webRecommendations() []map[string]any {
	return []map[string]any{}
}

func webDisableRecommendationRule(w http.ResponseWriter, scope, rule string) {
	if strings.TrimSpace(rule) == "" {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"A recommendation rule name is required to disable one.")
		return
	}
	filters := webRecommendationFiltersFor(scope)
	for _, held := range filters.Disabled {
		if strings.EqualFold(held, rule) {
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	filters.Disabled = append(filters.Disabled, rule)
	sort.Strings(filters.Disabled)
	webRecommendationSuppressions.Put(scope, filters)
	w.WriteHeader(http.StatusOK)
}

// Record a scope-wide flag rather than a list of rule names, because it also
// covers rules the advisory engine has not named yet.
func webDisableAllRecommendations(w http.ResponseWriter, scope string) {
	filters := webRecommendationFiltersFor(scope)
	filters.DisabledAll = true
	webRecommendationSuppressions.Put(scope, filters)
	w.WriteHeader(http.StatusNoContent)
}

func webResetRecommendationFilters(w http.ResponseWriter, scope string) {
	webRecommendationSuppressions.Delete(scope)
	w.WriteHeader(http.StatusNoContent)
}

func webRecommendationFiltersFor(scope string) webRecommendationFilters {
	if held, ok := webRecommendationSuppressions.Get(scope); ok {
		return held
	}
	return webRecommendationFilters{Scope: scope}
}

func webWriteRecommendationRule(w http.ResponseWriter, rule string) {
	if strings.TrimSpace(rule) == "" {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"A recommendation rule name is required.")
		return
	}
	for _, recommendation := range webRecommendations() {
		if name, _ := recommendation["name"].(string); strings.EqualFold(name, rule) {
			sim.WriteJSON(w, http.StatusOK, recommendation)
			return
		}
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"No recommendation named %q has been raised for this resource.", rule)
}
