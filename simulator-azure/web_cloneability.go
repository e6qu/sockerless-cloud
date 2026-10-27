package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// WebApps_IsCloneable answers from the site: App Service clones only from a
// Premium or Isolated plan, so the site's plan decides, and the deployment
// slots a clone would leave behind make it partial.

// Cloning copies an app's content and configuration onto a new app, offered on
// the Premium and Isolated tiers only.
var webCloneablePlanTiers = []string{
	"Premium", "PremiumV2", "PremiumV3", "PremiumContainer",
	"Isolated", "IsolatedV2",
}

func registerWebCloneability(srv *sim.Server) {
	srv.HandleFunc("POST "+webProvider+"/sites/{siteName}/iscloneable", webIsCloneable)
	srv.HandleFunc("POST "+webProvider+"/sites/{siteName}/slots/{slot}/iscloneable", webIsCloneable)

}

func webIsCloneable(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	site, _ := webResource(r)

	// Read the plan the site is placed on, so a plan scaled after the site was
	// created decides.
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

	// A clone copies one app, not its slots, so a site with slots clones only in
	// part.
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

// A slot runs on its production site's plan, which would host the clone.
func webCloneabilityTier(r *http.Request, site Site) string {
	if tier := webPlanSKUFor(site.Properties.ServerFarmID); tier != "" {
		return tier
	}
	if sim.PathParam(r, "slot") != "" {
		production := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "sites", sim.PathParam(r, "siteName"))
		if parent, ok := azfSites.Get(production); ok {
			if tier := webPlanSKUFor(parent.Properties.ServerFarmID); tier != "" {
				return tier
			}
			return parent.Properties.SKU
		}
	}
	return site.Properties.SKU
}

// The criterion list is present even when nothing blocks the clone.
func webCriteria(items []map[string]any) []any {
	rendered := make([]any, 0, len(items))
	for _, item := range items {
		rendered = append(rendered, item)
	}
	return rendered
}

func webTierIsCloneable(tier string) bool {
	for _, allowed := range webCloneablePlanTiers {
		if strings.EqualFold(tier, allowed) {
			return true
		}
	}
	return false
}

// A site on no plan has no tier to name.
func webTierLabel(tier string) string {
	if tier == "" {
		return "no App Service plan"
	}
	return tier
}

// A slot addressed directly has none of its own.
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
