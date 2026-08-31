package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Whether a site can be cloned, and the resource-health metadata that cannot
// be derived beside it.
//
// WebApps_IsCloneable answers from the site itself. App Service clones an app
// only from a Premium or Isolated App Service plan, so the plan the site is
// placed on decides the result, and the deployment slots a clone would leave
// behind decide whether the result is partial. Both are facts this simulator
// already holds, so the answer is computed from them rather than declared.
//
// ResourceHealthMetadata is not derivable in the same way. Its category is
// defined by the operation as "the category that the resource matches in the
// RHC Policy File" — Microsoft's own Resource Health Check policy, which this
// project does not vendor and cannot infer from a site. Its six spellings
// declare that instead of matching a site against a policy nobody here has.

// webCloneablePlanTiers are the App Service plan tiers a site can be cloned
// from. Cloning copies an app's content and configuration onto a new app, and
// App Service offers it on the Premium and Isolated tiers only.
var webCloneablePlanTiers = []string{
	"Premium", "PremiumV2", "PremiumV3", "PremiumContainer",
	"Isolated", "IsolatedV2",
}

// registerWebCloneability mounts both families.
func registerWebCloneability(srv *sim.Server) {
	srv.HandleFunc("POST "+webProvider+"/sites/{siteName}/iscloneable", webIsCloneable)
	srv.HandleFunc("POST "+webProvider+"/sites/{siteName}/slots/{slot}/iscloneable", webIsCloneable)

	const rhcReason = "a resource's health category is the one it matches in Microsoft's Resource Health Check policy file, which this simulator does not vendor and cannot infer from a site"
	gap := func(operation string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"%s is not implemented by the simulator: %s.", operation, rhcReason)
		}
	}
	srv.HandleFunc("GET "+webSubscriptionProvider+"/resourceHealthMetadata",
		gap("ResourceHealthMetadata_List"))
	srv.HandleFunc("GET "+webProvider+"/resourceHealthMetadata",
		gap("ResourceHealthMetadata_ListByResourceGroup"))
	srv.HandleFunc("GET "+webProvider+"/sites/{siteName}/resourceHealthMetadata",
		gap("ResourceHealthMetadata_ListBySite"))
	srv.HandleFunc("GET "+webProvider+"/sites/{siteName}/resourceHealthMetadata/default",
		gap("ResourceHealthMetadata_GetBySite"))
	srv.HandleFunc("GET "+webProvider+"/sites/{siteName}/slots/{slot}/resourceHealthMetadata",
		gap("ResourceHealthMetadata_ListBySiteSlot"))
	srv.HandleFunc("GET "+webProvider+"/sites/{siteName}/slots/{slot}/resourceHealthMetadata/default",
		gap("ResourceHealthMetadata_GetBySiteSlot"))
}

// webIsCloneable reports whether the addressed site or slot can be cloned.
func webIsCloneable(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	site, _ := webResource(r)

	// The tier is read from the plan the site is placed on rather than from the
	// site's stored copy, so a plan scaled after the site was created decides
	// the answer.
	tier := webCloneabilityTier(r, site)

	var blocking, unsupported []map[string]any
	if !webTierIsCloneable(tier) {
		blocking = append(blocking, map[string]any{
			"name": "AppServicePlanTier",
			"description": fmt.Sprintf(
				"The app is on the %s tier. Cloning an app requires a Premium or Isolated App Service plan.",
				webTierLabel(tier)),
		})
	}

	// A clone copies one app. The source's deployment slots are not cloned with
	// it, so a site that has them can be cloned only in part.
	if slots := webSlotNamesOf(r, site.ID); len(slots) > 0 {
		unsupported = append(unsupported, map[string]any{
			"name": "DeploymentSlots",
			"description": fmt.Sprintf(
				"The app has the deployment slot(s) %s. Slots are not copied to the clone.",
				strings.Join(slots, ", ")),
		})
	}

	result := "Cloneable"
	switch {
	case len(blocking) > 0:
		result = "NotCloneable"
	case len(unsupported) > 0:
		result = "PartiallyCloneable"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"result":                  result,
		"blockingFeatures":        []any{},
		"unsupportedFeatures":     webCriteria(unsupported),
		"blockingCharacteristics": webCriteria(blocking),
	})
}

// webCloneabilityTier resolves the App Service plan tier the addressed
// resource runs on. A deployment slot runs on its production site's plan and
// carries no plan of its own, so a slot that names none is answered for by the
// site it belongs to — which is the plan that would host the clone.
func webCloneabilityTier(r *http.Request, site Site) string {
	if tier := webPlanSKUFor(site.Properties.ServerFarmID); tier != "" {
		return tier
	}
	if sim.PathParam(r, "slot") != "" {
		production := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
			sim.PathParam(r, "siteName"))
		if parent, ok := azfSites.Get(production); ok {
			if tier := webPlanSKUFor(parent.Properties.ServerFarmID); tier != "" {
				return tier
			}
			return parent.Properties.SKU
		}
	}
	return site.Properties.SKU
}

// webCriteria renders a criterion list, which is always present even when
// nothing blocks the clone.
func webCriteria(items []map[string]any) []any {
	rendered := make([]any, 0, len(items))
	for _, item := range items {
		rendered = append(rendered, item)
	}
	return rendered
}

// webTierIsCloneable reports whether an App Service plan tier allows cloning.
func webTierIsCloneable(tier string) bool {
	for _, allowed := range webCloneablePlanTiers {
		if strings.EqualFold(tier, allowed) {
			return true
		}
	}
	return false
}

// webTierLabel names the tier in the refusal. A site placed on no plan has no
// tier to name.
func webTierLabel(tier string) string {
	if tier == "" {
		return "no App Service plan"
	}
	return tier
}

// webSlotNamesOf lists the deployment slots of the addressed resource. A slot
// addressed directly has none of its own: the request is already inside one.
func webSlotNamesOf(r *http.Request, siteID string) []string {
	if sim.PathParam(r, "slot") != "" {
		return nil
	}
	prefix := strings.ToLower(siteID) + "/slots/"
	var names []string
	for _, slot := range webSlots.List() {
		if !strings.HasPrefix(strings.ToLower(slot.ID), prefix) {
			continue
		}
		names = append(names, slot.ID[strings.LastIndex(slot.ID, "/")+1:])
	}
	sort.Strings(names)
	return names
}
