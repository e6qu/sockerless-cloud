package main

import (
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// App Service's runtime-stack catalogs.
//
// These list the language runtimes App Service offers and the versions of each
// that are current, deprecated or retired, per region — Microsoft's published
// catalog of its own platform images, revised as runtimes ship and reach end of
// life. It is not derivable from anything this simulator holds, and a partial
// or invented list is worse than none: a client would deploy against a runtime
// version that does not exist, or refuse one that does.
//
// So each spelling declares that. They used to miss the mux entirely and answer
// a bare routing 404, which reads as "no such API" rather than "this API exists
// and the simulator does not vendor the data behind it".

// registerWebStackCatalogs mounts the six spellings as declared gaps.
func registerWebStackCatalogs(srv *sim.Server) {
	const reason = "the runtime-stack catalog is Microsoft's published list of App Service platform images and their support lifecycle, which this simulator does not vendor and cannot derive"

	gap := func(operation string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"%s is not implemented by the simulator: %s.", operation, reason)
		}
	}

	srv.HandleFunc("GET /providers/Microsoft.Web/availableStacks",
		gap("Provider_GetAvailableStacks"))
	srv.HandleFunc("GET /providers/Microsoft.Web/webAppStacks",
		gap("Provider_GetWebAppStacks"))
	srv.HandleFunc("GET /providers/Microsoft.Web/functionAppStacks",
		gap("Provider_GetFunctionAppStacks"))
	srv.HandleFunc("GET /providers/Microsoft.Web/locations/{location}/webAppStacks",
		gap("Provider_GetWebAppStacksForLocation"))
	srv.HandleFunc("GET /providers/Microsoft.Web/locations/{location}/functionAppStacks",
		gap("Provider_GetFunctionAppStacksForLocation"))
	srv.HandleFunc("GET "+webSubscriptionProvider+"/availableStacks",
		gap("Provider_GetAvailableStacksOnPrem"))
}
