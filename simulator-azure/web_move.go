package main

import (
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_move.go re-homes Microsoft.Web resources between resource groups for
// the Resources_MoveResources operation: the site (or plan / certificate)
// row itself plus the site's entire child subtree — slots, deployments,
// host-name bindings, source controls, site extensions, sitecontainers,
// public and private certificates, domain ownership identifiers, premier
// add-ons, push settings, function keys, webjobs and run history, deployed
// content and deployment operation records, config sections and
// configuration snapshots — so a moved site keeps working exactly as it did,
// just addressed under the new resource group.

// webRekeyRows re-keys every row whose ID starts with oldPrefix onto
// newPrefix. The id accessor returns a pointer to the row's ID member so the
// helper both filters on it and rewrites it in place.
func webRekeyRows[T any](store sim.Store[T], oldPrefix, newPrefix string, id func(*T) *string) {
	rows := store.Filter(func(row T) bool { return strings.HasPrefix(*id(&row), oldPrefix) })
	for _, row := range rows {
		old := *id(&row)
		store.Delete(old)
		*id(&row) = newPrefix + strings.TrimPrefix(old, oldPrefix)
		store.Put(*id(&row), row)
	}
}

// webRekeyEntry re-homes one key-addressed record (a store whose rows carry
// no ID member of their own).
func webRekeyEntry[T any](store sim.Store[T], oldKey, newKey string) {
	if row, ok := store.Get(oldKey); ok {
		store.Delete(oldKey)
		store.Put(newKey, row)
	}
}

// webMoveSiteTree moves a production site and everything stored beneath it
// (including its deployment slots and their subtrees) onto a new resource ID.
func webMoveSiteTree(oldID, newID, targetRG string) {
	site, ok := azfSites.Get(oldID)
	if !ok {
		return
	}
	azfSites.Delete(oldID)
	site.ID = newID
	site.Properties.ResourceGroup = targetRG
	azfSites.Put(site.ID, site)

	webRekeyRows(webSlots, oldID+"/slots/", newID+"/slots/", func(s *Site) *string {
		s.Properties.ResourceGroup = targetRG
		return &s.ID
	})

	// Child stores keyed by the site (or slot) resource-ID prefix. The
	// prefixes below cover both the production site's children and, because
	// the slot IDs share the site's prefix, every slot child too.
	oldSub, newSub := oldID+"/", newID+"/"
	webRekeyRows(webDeployments, oldSub, newSub, func(d *WebDeployment) *string { return &d.ID })
	webRekeyRows(webHostNameBindings, oldSub, newSub, func(b *WebHostNameBinding) *string { return &b.ID })
	webRekeyRows(webSourceControls, oldSub, newSub, func(sc *WebSourceControl) *string { return &sc.ID })
	webRekeyRows(webSiteExtensions, oldSub, newSub, func(e *WebSiteExtension) *string { return &e.ID })
	webRekeyRows(azfSiteContainers, oldSub, newSub, func(c *SiteContainer) *string { return &c.ID })
	webRekeyRows(webPublicCertificates, oldSub, newSub, func(c *WebPublicCertificate) *string { return &c.ID })
	webRekeyRows(webSiteCertificates, oldSub, newSub, func(c *WebCertificate) *string { return &c.ID })
	webRekeyRows(webDomainOwnershipIdentifiers, oldSub, newSub, func(d *WebDomainOwnershipIdentifier) *string { return &d.ID })
	webRekeyRows(webPremierAddOns, oldSub, newSub, func(a *WebPremierAddOn) *string { return &a.ID })
	webRekeyRows(webPushSettings, oldSub, newSub, func(p *WebPushSettings) *string { return &p.ID })
	webRekeyRows(webFunctionKeys, oldSub, newSub, func(k *WebFunctionKeysRow) *string { return &k.ID })
	webRekeyRows(webWebJobs, oldSub, newSub, func(rec *WebJobRecord) *string { return &rec.ID })
	webRekeyRows(webJobRuns, oldSub, newSub, func(run *WebJobRunRecord) *string { return &run.ID })
	webRekeyRows(webMSDeployOps, oldSub, newSub, func(rec *WebMSDeployRecord) *string { return &rec.ID })
	webRekeyRows(webOneDeployOps, oldSub, newSub, func(rec *WebOneDeployRecord) *string { return &rec.ID })
	webRekeyRows(webDeploymentStatuses, oldSub, newSub, func(rec *WebDeploymentStatusRecord) *string { return &rec.ID })
	webRekeyRows(webSiteContent, oldID+"|", newID+"|", func(f *WebSiteContentFile) *string { return &f.ID })
	webRekeyRows(logicWorkflows, oldSub, newSub, func(wf *LogicWorkflow) *string { return &wf.ID })
	webRekeyRows(webConfigSnapshots, oldSub, newSub, func(row *webConfigSnapshotRow) *string {
		row.SiteID = strings.Replace(row.SiteID, oldID, newID, 1)
		return &row.ID
	})

	// Key-addressed per-resource records, for the site itself and each of
	// its slots.
	ids := [][2]string{{oldID, newID}}
	for _, s := range webSlots.Filter(func(row Site) bool { return strings.HasPrefix(row.ID, newID+"/slots/") }) {
		ids = append(ids, [2]string{oldID + strings.TrimPrefix(s.ID, newID), s.ID})
	}
	for _, pair := range ids {
		webRekeyEntry(webHostKeys, pair[0], pair[1])
		webRekeyEntry(webConfigExtras, pair[0], pair[1])
		webRekeyEntry(siteConfigStore, pair[0], pair[1])
		webRekeyEntry(webWorkflowFiles, pair[0], pair[1])
	}
}

// webMoveAppServicePlan moves an App Service plan row and repoints every
// site whose serverFarmId referenced it — a plan and its apps address each
// other by resource ID, and the move rewrites that ID.
func webMoveAppServicePlan(oldID, newID string) {
	plan, ok := azureAppServicePlans.Get(oldID)
	if !ok {
		return
	}
	azureAppServicePlans.Delete(oldID)
	plan.ID = newID
	azureAppServicePlans.Put(plan.ID, plan)

	repoint := func(store sim.Store[Site]) {
		for _, s := range store.Filter(func(s Site) bool { return strings.EqualFold(s.Properties.ServerFarmID, oldID) }) {
			store.Update(s.ID, func(row *Site) { row.Properties.ServerFarmID = newID })
		}
	}
	repoint(azfSites)
	repoint(webSlots)
}
