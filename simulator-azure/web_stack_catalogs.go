package main

import (
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// App Service's runtime-stack catalogs.
//
// These list the built-in language runtimes an App Service offers — the
// platform images it supplies and the versions of each it will run. This one
// supplies none. A site here runs the container image its linuxFxVersion names,
// and a site configured with a built-in stack instead ("PHP|8.2", "NODE|20-lts")
// cannot start: the platform image that stack names is Microsoft's, and
// `startRawServiceLocked` refuses the site saying exactly that.
//
// So the catalogs answer an empty collection, which states that: this App
// Service offers no built-in runtime stack. That is the same answer the site
// path gives, from the same fact, and the two cannot come to disagree. The
// alternative a caller would face — a partial list of runtimes assembled from
// Microsoft's published lifecycle data — would have them deploy against a stack
// no site here can run.
//
// Every lifecycle field in these schemas (isPreview, isDeprecated, isHidden,
// endOfLifeDate) hangs off a stack entry, and there are no entries; the
// collections require only `value`.

// webStackCollection is the shape all four collections share: a required value
// array and an optional nextLink, which a single page omits.
type webStackCollection struct {
	Value []struct{} `json:"value"`
}

// registerWebStackCatalogs mounts the six spellings of the four catalog reads.
func registerWebStackCatalogs(srv *sim.Server) {
	empty := func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, webStackCollection{Value: []struct{}{}})
	}

	srv.HandleFunc("GET /providers/Microsoft.Web/availableStacks", empty)
	srv.HandleFunc("GET /providers/Microsoft.Web/webAppStacks", empty)
	srv.HandleFunc("GET /providers/Microsoft.Web/functionAppStacks", empty)
	srv.HandleFunc("GET /providers/Microsoft.Web/locations/{location}/webAppStacks", empty)
	srv.HandleFunc("GET /providers/Microsoft.Web/locations/{location}/functionAppStacks", empty)
	srv.HandleFunc("GET "+webSubscriptionProvider+"/availableStacks", empty)
}
