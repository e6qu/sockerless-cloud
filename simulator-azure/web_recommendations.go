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
	srv.HandleFunc("GET "+ase+"/recommendations/{ruleName}", func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		webWriteRecommendationRule(w, sim.PathParam(r, "ruleName"))
	})

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
	srv.HandleFunc("GET "+site+"/recommendations/{ruleName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webWriteRecommendationRule(w, sim.PathParam(r, "ruleName"))
	})
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
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": webRecommendations()})
}

// webRecommendations is the recommendations this simulator has raised. It
// raises none: a recommendation is Azure Advisor's judgement about a resource,
// formed from telemetry this simulator does not collect, so there are none to
// report and the empty collection says exactly that. Every read of a
// recommendation — the listings and the rule reads alike — answers from here,
// so none of them can contradict another.
func webRecommendations() []map[string]any {
	return []map[string]any{}
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
// webWriteRecommendationRule answers a read of one recommendation rule out of
// the same collection the listings beside it return.
//
// This used to decline, on the grounds that a rule's details are Microsoft's
// published advisory copy. The listing next to it does not decline: it answers
// an empty collection, which states that this scope has no recommendations —
// and it is right, because the simulator raises none. Those two cannot both be
// true. A scope with no recommendations has no rule to read, and a read of
// something that does not exist is not found; declining says the simulator
// cannot answer a question it has already answered.
//
// Reading from the same collection is what keeps them agreeing: a rule the
// listing does not carry is not found, whatever the listing comes to carry.
func webWriteRecommendationRule(w http.ResponseWriter, rule string) {
	if strings.TrimSpace(rule) == "" {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"A recommendation rule name is required.")
		return
	}
	for _, recommendation := range webRecommendations() {
		if name, _ := recommendation["name"].(string); strings.EqualFold(name, rule) {
			sim.WriteJSON(w, http.StatusOK, recommendation)
			return
		}
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"No recommendation named %q has been raised for this resource.", rule)
}
