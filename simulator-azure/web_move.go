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
// just addressed under the new resource group. The Microsoft.Web hooks in
// resourceMoveHooks (resource_move.go) dispatch to the functions here.

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

	rekeyRowsByPrefix(webSlots, oldID+"/slots/", newID+"/slots/", func(s *Site) *string {
		s.Properties.ResourceGroup = targetRG
		return &s.ID
	})

	// Child stores keyed by the site (or slot) resource-ID prefix. The
	// prefixes below cover both the production site's children and, because
	// the slot IDs share the site's prefix, every slot child too.
	oldSub, newSub := oldID+"/", newID+"/"
	rekeyRowsByPrefix(webDeployments, oldSub, newSub, func(d *WebDeployment) *string { return &d.ID })
	rekeyRowsByPrefix(webHostNameBindings, oldSub, newSub, func(b *WebHostNameBinding) *string { return &b.ID })
	rekeyRowsByPrefix(webSourceControls, oldSub, newSub, func(sc *WebSourceControl) *string { return &sc.ID })
	rekeyRowsByPrefix(webSiteExtensions, oldSub, newSub, func(e *WebSiteExtension) *string { return &e.ID })
	rekeyRowsByPrefix(azfSiteContainers, oldSub, newSub, func(c *SiteContainer) *string { return &c.ID })
	rekeyRowsByPrefix(webPublicCertificates, oldSub, newSub, func(c *WebPublicCertificate) *string { return &c.ID })
	rekeyRowsByPrefix(webSiteCertificates, oldSub, newSub, func(c *WebCertificate) *string { return &c.ID })
	rekeyRowsByPrefix(webDomainOwnershipIdentifiers, oldSub, newSub, func(d *WebDomainOwnershipIdentifier) *string { return &d.ID })
	rekeyRowsByPrefix(webPremierAddOns, oldSub, newSub, func(a *WebPremierAddOn) *string { return &a.ID })
	rekeyRowsByPrefix(webPushSettings, oldSub, newSub, func(p *WebPushSettings) *string { return &p.ID })
	rekeyRowsByPrefix(webFunctionKeys, oldSub, newSub, func(k *WebFunctionKeysRow) *string { return &k.ID })
	rekeyRowsByPrefix(webWebJobs, oldSub, newSub, func(rec *WebJobRecord) *string { return &rec.ID })
	rekeyRowsByPrefix(webJobRuns, oldSub, newSub, func(run *WebJobRunRecord) *string { return &run.ID })
	rekeyRowsByPrefix(webMSDeployOps, oldSub, newSub, func(rec *WebMSDeployRecord) *string { return &rec.ID })
	rekeyRowsByPrefix(webOneDeployOps, oldSub, newSub, func(rec *WebOneDeployRecord) *string { return &rec.ID })
	rekeyRowsByPrefix(webDeploymentStatuses, oldSub, newSub, func(rec *WebDeploymentStatusRecord) *string { return &rec.ID })
	rekeyRowsByPrefix(webSiteContent, oldID+"|", newID+"|", func(f *WebSiteContentFile) *string { return &f.ID })
	rekeyRowsByPrefix(logicWorkflows, oldSub, newSub, func(wf *LogicWorkflow) *string { return &wf.ID })
	rekeyRowsByPrefix(webConfigSnapshots, oldSub, newSub, func(row *webConfigSnapshotRow) *string {
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
		rekeyEntry(webHostKeys, pair[0], pair[1])
		rekeyEntry(webConfigExtras, pair[0], pair[1])
		rekeyEntry(siteConfigStore, pair[0], pair[1])
		rekeyEntry(webWorkflowFiles, pair[0], pair[1])
		// The publishing password is derived from the site's resource ID, so
		// it is pinned across the move the way every other resource-ID-derived
		// credential is: a move never rotates a site's deployment credential.
		pinAzureKeySlots(pair[0], pair[1], azureKeyMaterial32, "publishingPassword")
	}

	// A workflow hosted by the site signs its callback URLs with an access key
	// derived from the workflow's resource ID, which the site's move rewrote.
	for _, wf := range logicWorkflows.Filter(func(wf LogicWorkflow) bool { return strings.HasPrefix(wf.ID, newSub) }) {
		pinAzureKeySlots(oldID+strings.TrimPrefix(wf.ID, newID), wf.ID, azureKeyMaterial32,
			"logic-access-primary", "logic-access-secondary")
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

// webMoveCertificate moves a resource-group-scoped Microsoft.Web certificate
// row onto its new resource ID. Certificates carry no child records.
func webMoveCertificate(oldID, newID string) {
	if cert, ok := webCertificates.Get(oldID); ok {
		webCertificates.Delete(oldID)
		cert.ID = newID
		webCertificates.Put(cert.ID, cert)
	}
}
