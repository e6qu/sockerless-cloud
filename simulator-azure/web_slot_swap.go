package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_slot_swap.go swaps two deployment slots of an app (WebApps_SwapSlot,
// WebApps_SwapSlotWithProduction). A swap exchanges what the two slots run —
// their content, their general settings and their swappable app settings,
// connection strings and storage mounts — while each slot keeps its
// hostnames, its publishing endpoints and the settings that stick to it. The
// platform restarts the destination on the source's content and waits for it
// to answer before it routes the destination's hostname there; a destination
// that does not start fails the swap and leaves both slots as they were.

// webSwapLocks serializes the swaps of one app.
var webSwapLocks = sim.NewKeyedLocks()

// webSlotSettingsNotSwapped are the siteConfig properties a swap leaves with
// the slot: scale settings, IP restrictions, Always On, diagnostic log
// settings, CORS, VNet integration and the TLS settings.
var webSlotSettingsNotSwapped = map[string]bool{
	"alwaysOn":                               true,
	"minTlsVersion":                          true,
	"scmMinTlsVersion":                       true,
	"minTlsCipherSuite":                      true,
	"ipSecurityRestrictions":                 true,
	"ipSecurityRestrictionsDefaultAction":    true,
	"scmIpSecurityRestrictions":              true,
	"scmIpSecurityRestrictionsDefaultAction": true,
	"scmIpSecurityRestrictionsUseMain":       true,
	"cors":                                   true,
	"vnetName":                               true,
	"vnetRouteAllEnabled":                    true,
	"vnetPrivatePortsCount":                  true,
	"httpLoggingEnabled":                     true,
	"detailedErrorLoggingEnabled":            true,
	"requestTracingEnabled":                  true,
	"requestTracingExpirationTime":           true,
	"logsDirectorySizeLimit":                 true,
	"numberOfWorkers":                        true,
	"functionAppScaleLimit":                  true,
	"minimumElasticInstanceCount":            true,
	"preWarmedInstanceCount":                 true,
	"elasticWebAppScaleLimit":                true,
}

// registerWebSlotSwap mounts both spellings of the swap: on the app, whose
// targetSlot swaps into production, and on a slot, which swaps into its
// targetSlot ("production" for the app itself).
func registerWebSlotSwap(both func(string, string, http.HandlerFunc)) {
	both("POST", "/slotsswap", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			TargetSlot   string `json:"targetSlot"`
			PreserveVnet *bool  `json:"preserveVnet"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.TargetSlot) == "" || req.PreserveVnet == nil {
			AzureError(w, "BadRequest", "The request must name targetSlot and preserveVnet.", http.StatusBadRequest)
			return
		}
		appName := sim.PathParam(r, "siteName")
		appID := strings.TrimSuffix(webResourceID(r), "/slots/"+sim.PathParam(r, "slot"))
		// The slot the path names swaps into targetSlot; on the app itself,
		// targetSlot swaps into production.
		source, target := sim.PathParam(r, "slot"), req.TargetSlot
		if source == "" {
			source, target = req.TargetSlot, "production"
		}
		if strings.EqualFold(source, target) {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"Cannot swap slot '%s' of site '%s' with itself.", source, appName)
			return
		}
		src, ok := webSlotOf(appID, source)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cannot find slot '%s' of site '%s'.", source, appName)
			return
		}
		dst, ok := webSlotOf(appID, target)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cannot find slot '%s' of site '%s'.", target, appName)
			return
		}
		if p, pending := webSlotPreviews.Get(appID); pending && !webSlotPreviewPair(p, src.ID, dst.ID) {
			AzureErrorf(w, "Conflict", http.StatusConflict,
				"A swap with preview between slots '%s' and '%s' of site '%s' is in progress; complete it or cancel it with resetSlotConfig first.",
				webSlotNameByID(p.SourceID), webSlotNameByID(p.TargetID), appName)
			return
		}
		opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			return webSwapSlots(appID, src.ID, dst.ID)
		})
		location := strings.ToLower(strings.ReplaceAll(src.Location, " ", ""))
		apiVersion := r.URL.Query().Get("api-version")
		sub := sim.PathParam(r, "subscriptionId")
		writeAzureAsyncCreateHeaders(w, opID,
			azureAsyncOperationHeader(r, sub, "Microsoft.Web", location, "operationStatuses", opID, apiVersion),
			azureAsyncOperationHeader(r, sub, "Microsoft.Web", location, "operationResults", opID, apiVersion))
		w.WriteHeader(http.StatusAccepted)
	})
}

// webSlotOf resolves one slot of an app by name, "production" being the app.
func webSlotOf(appID, slot string) (Site, bool) {
	if strings.EqualFold(slot, "production") {
		return azfSites.Get(appID)
	}
	return webSlots.Get(appID + "/slots/" + slot)
}

// webSlotName is the name a swap reports a slot by.
func webSlotName(site Site) string {
	if _, slot, ok := strings.Cut(site.Name, "/"); ok {
		return slot
	}
	return "production"
}

// webSaveSlotRow stores an app or slot row in its own store.
func webSaveSlotRow(site Site) {
	if strings.Contains(site.ID, "/slots/") {
		webSlots.Put(site.ID, site)
		return
	}
	azfSites.Put(site.ID, site)
}

// webSwapSlots swaps srcID into dstID and dstID into srcID, then starts the
// destination on its new content; a destination that does not start swaps
// back and fails the operation.
func webSwapSlots(appID, srcID, dstID string) *AsyncOperationError {
	defer webSwapLocks.Lock(appID)()

	// A swap between the slots of a swap with preview completes it, from the
	// settings the source held before the preview.
	if p, pending := webSlotPreviews.Get(appID); pending && webSlotPreviewPair(p, srcID, dstID) {
		webRestoreSlotPreview(p)
	}
	if err := webExchangeSlots(appID, srcID, dstID); err != nil {
		return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
	}
	dst, ok := webJobSite(dstID)
	if !ok {
		return &AsyncOperationError{Code: "ResourceNotFound", Message: "The destination slot was deleted during the swap."}
	}
	if siteRunsContainer(&dst) && !siteStopped(&dst) {
		ctx, cancel := context.WithTimeout(context.Background(), siteStartTimeLimit(&dst))
		_, err := siteContainerAddress(ctx, &dst)
		cancel()
		if err != nil {
			src, _ := webJobSite(srcID)
			if backErr := webExchangeSlots(appID, srcID, dstID); backErr != nil {
				err = fmt.Errorf("%w; swapping back failed: %v", err, backErr)
			}
			return &AsyncOperationError{
				Code: "Conflict",
				Message: fmt.Sprintf("Cannot swap slot '%s' into slot '%s': the '%s' content did not start: %v",
					webSlotName(src), webSlotName(dst), webSlotName(src), err),
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	src, _ := webJobSite(srcID)
	status := &SlotSwapStatus{TimestampUtc: now, SourceSlotName: webSlotName(src), DestinationSlotName: webSlotName(dst)}
	for _, id := range []string{srcID, dstID} {
		if row, ok := webJobSite(id); ok {
			row.Properties.SlotSwapStatus = status
			webSaveSlotRow(row)
		}
	}
	if src, ok := webJobSite(srcID); ok {
		startAlwaysOnSite(src)
	}
	return nil
}

// webExchangeSlots exchanges what two slots of an app run. Both stop first;
// each restarts on its next request.
func webExchangeSlots(appID, aID, bID string) error {
	a, ok := webJobSite(aID)
	if !ok {
		return fmt.Errorf("slot %s not found", aID)
	}
	b, ok := webJobSite(bID)
	if !ok {
		return fmt.Errorf("slot %s not found", bID)
	}
	// Requests to either slot wait for the exchange instead of starting a
	// container on half-swapped state.
	names := []string{a.Name, b.Name}
	slices.Sort(names)
	for _, name := range names {
		inst := azfInstanceFor(name)
		inst.mu.Lock()
		defer inst.mu.Unlock()
		inst.teardownLocked()
	}
	for _, site := range []Site{a, b} {
		webStopSiteWebJobs(site.ID)
	}

	appCfg, _ := siteConfigStore.Get(appID)
	sticky := SlotConfigNames{}
	if appCfg.SlotConfigNames != nil {
		sticky = *appCfg.SlotConfigNames
	}
	aCfg, _ := siteConfigStore.Get(aID)
	bCfg, _ := siteConfigStore.Get(bID)
	aSettings, bSettings := siteAppSettings(&a), siteAppSettings(&b)
	stickySetting := webStickyAppSetting(sticky, aSettings, bSettings)
	newA := webSwapStrings(aSettings, bSettings, stickySetting)
	newB := webSwapStrings(bSettings, aSettings, stickySetting)
	stickyConn := webNameSet(sticky.ConnectionStringNames)
	newAConn := webSwapConnStrings(aCfg.ConnectionStrings, bCfg.ConnectionStrings, stickyConn)
	newBConn := webSwapConnStrings(bCfg.ConnectionStrings, aCfg.ConnectionStrings, stickyConn)
	stickyStorage := webNameSet(sticky.AzureStorageConfigNames)
	newAStorage := webSwapStorage(a.AzureStorageAccounts, b.AzureStorageAccounts, stickyStorage)
	newBStorage := webSwapStorage(b.AzureStorageAccounts, a.AzureStorageAccounts, stickyStorage)
	newAConfig := webSwapSiteConfig(a.Properties.SiteConfig, b.Properties.SiteConfig)
	newBConfig := webSwapSiteConfig(b.Properties.SiteConfig, a.Properties.SiteConfig)
	newAConfig.AppSettings = nameValuePairs(newA)
	newBConfig.AppSettings = nameValuePairs(newB)

	a.Properties.SiteConfig, b.Properties.SiteConfig = newAConfig, newBConfig
	a.AzureStorageAccounts, b.AzureStorageAccounts = newAStorage, newBStorage
	a.Properties.SlotSwapStatus, b.Properties.SlotSwapStatus = b.Properties.SlotSwapStatus, a.Properties.SlotSwapStatus
	webSaveSlotRow(a)
	webSaveSlotRow(b)
	aCfg.AppSettings, bCfg.AppSettings = newA, newB
	aCfg.ConnectionStrings, bCfg.ConnectionStrings = newAConn, newBConn
	siteConfigStore.Put(aID, aCfg)
	siteConfigStore.Put(bID, bCfg)

	webSwapContent(aID, bID)
	aKudu, aHas := kuduSiteSettings.Get(aID)
	bKudu, bHas := kuduSiteSettings.Get(bID)
	kuduSiteSettings.Delete(aID)
	kuduSiteSettings.Delete(bID)
	if aHas {
		kuduSiteSettings.Put(bID, aKudu)
	}
	if bHas {
		kuduSiteSettings.Put(aID, bKudu)
	}
	if err := webSwapHomes(a.Name, b.Name); err != nil {
		return err
	}
	if err := webDiscoverWebJobs(aID); err != nil {
		return err
	}
	return webDiscoverWebJobs(bID)
}

// webStickyAppSetting reports whether an app setting stays with its slot: one
// the app's slotConfigNames names, and one ending _EXTENSION_VERSION unless
// every slot sets WEBSITE_OVERRIDE_PRESERVE_DEFAULT_STICKY_SLOT_SETTINGS to 0
// or false.
func webStickyAppSetting(sticky SlotConfigNames, a, b map[string]string) func(string) bool {
	names := webNameSet(sticky.AppSettingNames)
	overridden := func(settings map[string]string) bool {
		v := strings.ToLower(strings.TrimSpace(settings["WEBSITE_OVERRIDE_PRESERVE_DEFAULT_STICKY_SLOT_SETTINGS"]))
		return v == "0" || v == "false"
	}
	defaultSticky := !overridden(a) || !overridden(b)
	return func(name string) bool {
		if names(name) {
			return true
		}
		return defaultSticky && strings.HasSuffix(strings.ToUpper(name), "_EXTENSION_VERSION")
	}
}

func webNameSet(names []string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[strings.ToLower(n)] = true
	}
	return func(name string) bool { return set[strings.ToLower(name)] }
}

// webSwapStrings is what a slot holds after a swap: its own sticky entries and
// the other slot's swappable ones.
func webSwapStrings(own, other map[string]string, sticky func(string) bool) map[string]string {
	out := map[string]string{}
	for k, v := range own {
		if sticky(k) {
			out[k] = v
		}
	}
	for k, v := range other {
		if !sticky(k) {
			out[k] = v
		}
	}
	return out
}

func webSwapConnStrings(own, other map[string]AzureSiteConnStringValue, sticky func(string) bool) map[string]AzureSiteConnStringValue {
	out := map[string]AzureSiteConnStringValue{}
	for k, v := range own {
		if sticky(k) {
			out[k] = v
		}
	}
	for k, v := range other {
		if !sticky(k) {
			out[k] = v
		}
	}
	return out
}

func webSwapStorage(own, other map[string]*AzureStorageInfoValue, sticky func(string) bool) map[string]*AzureStorageInfoValue {
	out := map[string]*AzureStorageInfoValue{}
	for k, v := range own {
		if sticky(k) {
			out[k] = v
		}
	}
	for k, v := range other {
		if !sticky(k) {
			out[k] = v
		}
	}
	return out
}

// webSwapSiteConfig is the siteConfig a slot runs after a swap: the other
// slot's general settings with its own values of the settings that stay.
func webSwapSiteConfig(own, other *SiteConfig) *SiteConfig {
	if own == nil {
		own = &SiteConfig{}
	}
	if other == nil {
		other = &SiteConfig{}
	}
	out := *other
	out.AlwaysOn = own.AlwaysOn
	out.MinTLSVersion = own.MinTLSVersion
	out.ScmMinTLSVersion = own.ScmMinTLSVersion
	out.IPSecurityRestrictionsDefaultAction = own.IPSecurityRestrictionsDefaultAction
	out.ScmIPSecurityRestrictionsDefaultAction = own.ScmIPSecurityRestrictionsDefaultAction
	out.FunctionAppScaleLimit = own.FunctionAppScaleLimit
	out.Extra = map[string]json.RawMessage{}
	for k, v := range other.Extra {
		if !webSlotSettingsNotSwapped[k] {
			out.Extra[k] = v
		}
	}
	for k, v := range own.Extra {
		if webSlotSettingsNotSwapped[k] {
			out.Extra[k] = v
		}
	}
	return &out
}

// webSwapContent exchanges the records that live in the two slots' file
// systems: the deployment manifest and history, and the webjobs with their run
// history. webSwapHomes exchanges the file systems themselves.
func webSwapContent(aID, bID string) {
	if am, aok := webDeployManifests.Get(aID); aok {
		webDeployManifests.Delete(aID)
		if bm, bok := webDeployManifests.Get(bID); bok {
			bm.ID = aID
			webDeployManifests.Put(aID, bm)
		}
		am.ID = bID
		webDeployManifests.Put(bID, am)
	} else if bm, bok := webDeployManifests.Get(bID); bok {
		webDeployManifests.Delete(bID)
		bm.ID = aID
		webDeployManifests.Put(aID, bm)
	}
	swapRowsByPrefix(webKuduDeployments, aID+"/deployments/", bID+"/deployments/", func(rec *WebKuduDeployment, from, to string) {
		rec.ID = to + strings.TrimPrefix(rec.ID, from)
		rec.SiteID = strings.TrimSuffix(to, "/deployments/")
	})
	swapRowsByPrefix(webDeployments, aID+"/deployments/", bID+"/deployments/", func(d *WebDeployment, from, to string) {
		d.ID = to + strings.TrimPrefix(d.ID, from)
		if strings.Contains(d.ID, "/slots/") {
			d.Type = "Microsoft.Web/sites/slots/deployments"
		} else {
			d.Type = "Microsoft.Web/sites/deployments"
		}
	})
	for _, kind := range []string{"triggeredwebjobs/", "continuouswebjobs/"} {
		swapRowsByPrefix(webWebJobs, aID+"/"+kind, bID+"/"+kind, func(rec *WebJobRecord, from, to string) {
			rec.ID = to + strings.TrimPrefix(rec.ID, from)
			rec.SiteID = strings.TrimSuffix(to, "/"+kind)
		})
		swapRowsByPrefix(webJobRuns, aID+"/"+kind, bID+"/"+kind, func(run *WebJobRunRecord, from, to string) {
			run.ID = to + strings.TrimPrefix(run.ID, from)
			run.JobID = to + strings.TrimPrefix(run.JobID, from)
			run.SiteID = strings.TrimSuffix(to, "/"+kind)
		})
	}
	swapRowsByPrefix(azfSiteContainers, aID+"/sitecontainers/", bID+"/sitecontainers/", func(c *SiteContainer, from, to string) {
		c.ID = to + strings.TrimPrefix(c.ID, from)
	})
}

// swapRowsByPrefix exchanges the rows under prefix a with those under prefix
// b; move rewrites a row's keys from one prefix to the other.
func swapRowsByPrefix[T any](store sim.Store[T], a, b string, move func(row *T, from, to string)) {
	under := func(p string) []sim.Keyed[T] { return store.ListPrefix(p) }
	aRows, bRows := under(a), under(b)
	for _, kr := range aRows {
		store.Delete(kr.ID)
	}
	for _, kr := range bRows {
		store.Delete(kr.ID)
	}
	put := func(rows []sim.Keyed[T], from, to string) {
		for _, kr := range rows {
			row := kr.Item
			move(&row, from, to)
			store.Put(to+strings.TrimPrefix(kr.ID, from), row)
		}
	}
	put(aRows, a, b)
	put(bRows, b, a)
}

// webSwapHomes exchanges two slots' persistent /home storage.
func webSwapHomes(aName, bName string) error {
	aDir, bDir := siteHomeDir(aName), siteHomeDir(bName)
	tmp := aDir + ".swap-" + sim.RandomHex(4)
	aExists, bExists := webDirExists(aDir), webDirExists(bDir)
	if aExists {
		if err := os.Rename(aDir, tmp); err != nil {
			return fmt.Errorf("swap site storage: %w", err)
		}
	}
	if bExists {
		if err := os.Rename(bDir, aDir); err != nil {
			return fmt.Errorf("swap site storage: %w", err)
		}
	}
	if aExists {
		if err := os.Rename(tmp, bDir); err != nil {
			return fmt.Errorf("swap site storage: %w", err)
		}
	}
	return nil
}

func webDirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// webStopSiteWebJobs stops every webjob process of a site: its continuous
// jobs stop and its running triggered runs abort.
func webStopSiteWebJobs(siteID string) {
	for _, rec := range webWebJobs.Filter(func(rec WebJobRecord) bool { return rec.SiteID == siteID }) {
		webKillWebJobContainer(rec.ID)
		for _, run := range webJobRunsFor(rec.ID) {
			if run.Status != "Running" {
				continue
			}
			webKillWebJobContainer(run.ID)
			webJobRuns.Update(run.ID, func(row *WebJobRunRecord) {
				row.Status = "Aborted"
				row.EndTime = time.Now().UTC().Format(time.RFC3339)
			})
		}
		if rec.JobKind == "continuous" {
			webWebJobs.Update(rec.ID, func(row *WebJobRecord) {
				row.Status = "Stopped"
				row.DetailedStatus = ""
			})
		}
	}
}
