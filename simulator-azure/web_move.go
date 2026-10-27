package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Re-homes Microsoft.Web resources for Resources_MoveResources (through
// resource_move.go's hooks): the site, plan or certificate and the site's whole
// child subtree, so a moved site keeps working under the new group.

// Including the deployment slots and their subtrees.
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

	// Slot IDs share the site's prefix, so these cover slot children too.
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
	rekeyRowsByPrefix(logicWorkflows, oldSub, newSub, func(wf *LogicWorkflow) *string { return &wf.ID })
	rekeyRowsByPrefix(webConfigSnapshots, oldSub, newSub, func(row *webConfigSnapshotRow) *string {
		row.SiteID = strings.Replace(row.SiteID, oldID, newID, 1)
		return &row.ID
	})

	ids := [][2]string{{oldID, newID}}
	for _, s := range webSlots.Filter(func(row Site) bool { return strings.HasPrefix(row.ID, newID+"/slots/") }) {
		ids = append(ids, [2]string{oldID + strings.TrimPrefix(s.ID, newID), s.ID})
	}
	for _, pair := range ids {
		rekeyEntry(webHostKeys, pair[0], pair[1])
		rekeyEntry(webConfigExtras, pair[0], pair[1])
		rekeyEntry(siteConfigStore, pair[0], pair[1])
		rekeyEntry(webWorkflowFiles, pair[0], pair[1])
		rekeyEntry(kuduSiteSettings, pair[0], pair[1])
		// The publishing password derives from the resource ID, so pin it: a move
		// never rotates a site's deployment credential.
		pinAzureKeySlots(pair[0], pair[1], azureKeyMaterial32, "publishingPassword")
	}

	// A hosted workflow signs callback URLs with a key derived from its resource
	// ID, which the move rewrote.
	for _, wf := range logicWorkflows.Filter(func(wf LogicWorkflow) bool { return strings.HasPrefix(wf.ID, newSub) }) {
		pinAzureKeySlots(oldID+strings.TrimPrefix(wf.ID, newID), wf.ID, azureKeyMaterial32,
			"logic-access-primary", "logic-access-secondary")
	}
}

// A plan and its apps reference each other by resource ID, so repoint every
// site's serverFarmId.
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

// Certificates carry no child records.
func webMoveCertificate(oldID, newID string) {
	if cert, ok := webCertificates.Get(oldID); ok {
		webCertificates.Delete(oldID)
		cert.ID = newID
		webCertificates.Put(cert.ID, cert)
	}
}
