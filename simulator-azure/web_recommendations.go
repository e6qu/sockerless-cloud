package main

import (
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// App Service recommendations.
//
// Azure generates a recommendation when its advisory engine observes something
// about a running site — a plan under memory pressure, a site with too many
// apps on one instance, a hostname whose certificate is about to lapse. Three
// scopes address them: a subscription, an App Service Environment, and a single
// site.
//
// The simulator runs no advisory engine, and that decides what each operation
// answers:
//
//   - The lists and the history are empty. No recommendation has been observed
//     about anything, so there is none to report and none in the past either.
//     An empty collection is the true answer, and filling it with an advisory
//     nothing measured would be worse than reporting none.
//   - The filters are real state. Disabling a rule for a scope, disabling every
//     rule for it, and resetting the filters are the client's own decisions, so
//     they are recorded against the scope and survive until they are reset.
//   - Rule details are not served. A RecommendationRule is Microsoft's published
//     advisory copy — the display name, the message shown in the portal, the
//     blade it links to — which this simulator does not vendor and cannot
//     derive. Those two operations declare that rather than answering with
//     invented text or a 404 that would claim the rule does not exist.

// webRecommendationFilters is what a scope has suppressed.
type webRecommendationFilters struct {
	Scope       string   `json:"scope"`
	DisabledAll bool     `json:"disabledAll"`
	Disabled    []string `json:"disabled"`
}

var webRecommendationSuppressions sim.Store[webRecommendationFilters]

// registerWebRecommendations mounts the family at all three scopes.
func registerWebRecommendations(srv *sim.Server) {
	webRecommendationSuppressions = sim.MakeStore[webRecommendationFilters](
		srv.DB(), "web_recommendation_filters")

	const ruleReason = "a recommendation rule's details are Microsoft's published advisory copy — its display name, portal message and blade link — which this simulator does not vendor"

	// Subscription scope. There is no resource to look up: the subscription in
	// the path is the scope itself.
	sub := webSubscriptionProvider + "/recommendations"
	srv.HandleFunc("GET "+sub, func(w http.ResponseWriter, r *http.Request) {
		webWriteRecommendations(w)
	})
	srv.HandleFunc("POST "+sub+"/reset", func(w http.ResponseWriter, r *http.Request) {
		webResetRecommendationFilters(w, "/subscriptions/"+sim.PathParam(r, "subscriptionId"))
	})
	srv.HandleFunc("POST "+sub+"/{ruleName}/disable", func(w http.ResponseWriter, r *http.Request) {
		webDisableRecommendationRule(w, "/subscriptions/"+sim.PathParam(r, "subscriptionId"),
			sim.PathParam(r, "ruleName"))
	})

	// App Service Environment scope. The wildcard is spelled {name} to match
	// every other hostingEnvironments route in this simulator, which is what
	// aseResourceID reads.
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
	srv.HandleFunc("GET "+ase+"/recommendations/{ruleName}",
		webRecommendationRuleGap("Recommendations_GetRuleDetailsByHostingEnvironment", ruleReason))

	// Site scope. Recommendations are addressed on a production site only —
	// the document declares no slot spelling for any of them.
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
	srv.HandleFunc("GET "+site+"/recommendations/{ruleName}",
		webRecommendationRuleGap("Recommendations_GetRuleDetailsByWebApp", ruleReason))
}

// webEnvironmentMissing writes the canonical ARM 404 when the addressed App
// Service Environment does not exist; it reports whether it wrote one.
func webEnvironmentMissing(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := webHostingEnvironments.Get(aseResourceID(r)); ok {
		return false
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/hostingEnvironments/%s' was not found.",
		sim.PathParam(r, "name"))
	return true
}

// webWriteRecommendations answers a list or a history. Both are empty: the
// simulator observes nothing about a running site, so it has recommended
// nothing now and nothing before.
func webWriteRecommendations(w http.ResponseWriter) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

// webDisableRecommendationRule suppresses one rule for a scope.
func webDisableRecommendationRule(w http.ResponseWriter, scope, rule string) {
	if strings.TrimSpace(rule) == "" {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"A recommendation rule name is required to disable one.")
		return
	}
	filters := webRecommendationFiltersFor(scope)
	for _, held := range filters.Disabled {
		if strings.EqualFold(held, rule) {
			// Already suppressed; disabling it again changes nothing.
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	filters.Disabled = append(filters.Disabled, rule)
	sort.Strings(filters.Disabled)
	webRecommendationSuppressions.Put(scope, filters)
	w.WriteHeader(http.StatusOK)
}

// webDisableAllRecommendations suppresses every rule for a scope. It is
// recorded as the scope-wide flag rather than as a list of rule names, because
// it also covers the rules the advisory engine has not named yet.
func webDisableAllRecommendations(w http.ResponseWriter, scope string) {
	filters := webRecommendationFiltersFor(scope)
	filters.DisabledAll = true
	webRecommendationSuppressions.Put(scope, filters)
	w.WriteHeader(http.StatusNoContent)
}

// webResetRecommendationFilters clears what a scope has suppressed.
func webResetRecommendationFilters(w http.ResponseWriter, scope string) {
	webRecommendationSuppressions.Delete(scope)
	w.WriteHeader(http.StatusNoContent)
}

// webRecommendationFiltersFor reads a scope's filters, or the empty set it
// starts from.
func webRecommendationFiltersFor(scope string) webRecommendationFilters {
	if held, ok := webRecommendationSuppressions.Get(scope); ok {
		return held
	}
	return webRecommendationFilters{Scope: scope}
}

// webRecommendationRuleGap declares the two rule-detail reads. The gap does not
// depend on the site or the environment existing — the operation is
// unimplemented whatever it is asked about, and answering a 404 for an absent
// resource first would report an operation the simulator does not serve as one
// that it does.
func webRecommendationRuleGap(operation, reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"%s is not implemented by the simulator: %s.", operation, reason)
	}
}
