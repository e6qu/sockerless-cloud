package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_slot_preview.go implements swap with preview (multi-phase swap) and the
// slot differences read. applySlotConfig is the first phase: the slot the
// request addresses takes the target slot's slot-specific app settings and
// connection strings and restarts on them, so it runs as it will after the
// swap. The swap that follows completes the phase; resetSlotConfig cancels it
// and gives the slot its own settings back.

// WebSlotPreview is an app's pending swap with preview: the slot whose
// configuration took the target's sticky settings, the target, and the
// settings the source held before. Keyed by the app's resource ID.
type WebSlotPreview struct {
	ID                string                              `json:"id"`
	SourceID          string                              `json:"sourceId"`
	TargetID          string                              `json:"targetId"`
	AppSettings       map[string]string                   `json:"appSettings"`
	ConnectionStrings map[string]AzureSiteConnStringValue `json:"connectionStrings"`
}

var webSlotPreviews sim.Store[WebSlotPreview]

func initWebSlotPreviewStore(srv *sim.Server) {
	webSlotPreviews = sim.MakeStore[WebSlotPreview](srv.DB(), "web_slot_previews")
}

// webSlotPair resolves the slot a slot-entity request addresses and its
// targetSlot, writing the error response when either is missing.
func webSlotPair(w http.ResponseWriter, r *http.Request) (appID string, current, target Site, ok bool) {
	if webMissing(w, r) {
		return "", Site{}, Site{}, false
	}
	var req struct {
		TargetSlot   string `json:"targetSlot"`
		PreserveVnet *bool  `json:"preserveVnet"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return "", Site{}, Site{}, false
	}
	if strings.TrimSpace(req.TargetSlot) == "" || req.PreserveVnet == nil {
		AzureError(w, "BadRequest", "The request must name targetSlot and preserveVnet.", http.StatusBadRequest)
		return "", Site{}, Site{}, false
	}
	appName := sim.PathParam(r, "siteName")
	slot := sim.PathParam(r, "slot")
	appID = strings.TrimSuffix(webResourceID(r), "/slots/"+slot)
	if slot == "" {
		slot = "production"
	}
	if strings.EqualFold(slot, req.TargetSlot) {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"The target slot '%s' of site '%s' is the slot the request addresses.", req.TargetSlot, appName)
		return "", Site{}, Site{}, false
	}
	current, _ = webSlotOf(appID, slot)
	target, found := webSlotOf(appID, req.TargetSlot)
	if !found {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cannot find slot '%s' of site '%s'.", req.TargetSlot, appName)
		return "", Site{}, Site{}, false
	}
	return appID, current, target, true
}

// webSlotSticky is the app's slot-setting rule for app settings and for
// connection strings, given the two slots' app settings.
func webSlotSticky(appID string, a, b map[string]string) (appSetting, connString func(string) bool) {
	appCfg, _ := siteConfigStore.Get(appID)
	sticky := SlotConfigNames{}
	if appCfg.SlotConfigNames != nil {
		sticky = *appCfg.SlotConfigNames
	}
	return webStickyAppSetting(sticky, a, b), webNameSet(sticky.ConnectionStringNames)
}

// webSetSlotSettings stores a slot's app settings and connection strings.
func webSetSlotSettings(id string, settings map[string]string, conns map[string]AzureSiteConnStringValue) {
	if row, ok := webJobSite(id); ok {
		if row.Properties.SiteConfig == nil {
			row.Properties.SiteConfig = &SiteConfig{}
		}
		row.Properties.SiteConfig.AppSettings = nameValuePairs(settings)
		webSaveSlotRow(row)
	}
	cfg, _ := siteConfigStore.Get(id)
	cfg.AppSettings = settings
	cfg.ConnectionStrings = conns
	siteConfigStore.Put(id, cfg)
}

// webRestoreSlotPreview gives a preview's source slot back the settings it
// held before the preview and forgets the preview.
func webRestoreSlotPreview(p WebSlotPreview) {
	webSetSlotSettings(p.SourceID, p.AppSettings, p.ConnectionStrings)
	webSlotPreviews.Delete(p.ID)
}

// webSlotPreviewPair reports whether a pending preview is between slots a and
// b, in either order.
func webSlotPreviewPair(p WebSlotPreview, a, b string) bool {
	return (p.SourceID == a && p.TargetID == b) || (p.SourceID == b && p.TargetID == a)
}

// webApplySlotConfig is the first phase of a swap with preview.
func webApplySlotConfig(w http.ResponseWriter, r *http.Request) {
	appID, source, target, ok := webSlotPair(w, r)
	if !ok {
		return
	}
	defer webSwapLocks.Lock(appID)()
	if p, pending := webSlotPreviews.Get(appID); pending {
		if p.SourceID != source.ID || p.TargetID != target.ID {
			AzureErrorf(w, "Conflict", http.StatusConflict,
				"A swap with preview between slots '%s' and '%s' of site '%s' is in progress; complete it with a swap or cancel it with resetSlotConfig first.",
				webSlotNameByID(p.SourceID), webSlotNameByID(p.TargetID), sim.PathParam(r, "siteName"))
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	srcCfg, _ := siteConfigStore.Get(source.ID)
	dstCfg, _ := siteConfigStore.Get(target.ID)
	srcSettings, dstSettings := siteAppSettings(&source), siteAppSettings(&target)
	stickySetting, stickyConn := webSlotSticky(appID, srcSettings, dstSettings)
	preview := WebSlotPreview{
		ID:                appID,
		SourceID:          source.ID,
		TargetID:          target.ID,
		AppSettings:       maps.Clone(srcSettings),
		ConnectionStrings: maps.Clone(srcCfg.ConnectionStrings),
	}
	webSlotPreviews.Put(appID, preview)
	webSetSlotSettings(source.ID,
		webSwapStrings(dstSettings, srcSettings, stickySetting),
		webSwapConnStrings(dstCfg.ConnectionStrings, srcCfg.ConnectionStrings, stickyConn))
	updated, _ := webJobSite(source.ID)
	restartAzureFunctionInstance(updated)
	// The platform waits for the source's instances to restart on the target's
	// settings, and reverts the source when one does not.
	if siteRunsContainer(&updated) && !siteStopped(&updated) {
		ctx, cancel := context.WithTimeout(r.Context(), siteStartTimeLimit(&updated))
		_, err := siteContainerAddress(ctx, &updated)
		cancel()
		if err != nil {
			webRestoreSlotPreview(preview)
			if restored, ok := webJobSite(source.ID); ok {
				restartAzureFunctionInstance(restored)
			}
			AzureErrorf(w, "Conflict", http.StatusConflict,
				"Cannot apply the configuration of slot '%s' to slot '%s': the slot did not restart: %v",
				webSlotName(target), webSlotName(source), err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// webResetSlotConfig cancels the app's swap with preview when the slot the
// request addresses takes part in it; otherwise there is nothing to reset.
func webResetSlotConfig(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	id := webResourceID(r)
	appID := strings.TrimSuffix(id, "/slots/"+sim.PathParam(r, "slot"))
	defer webSwapLocks.Lock(appID)()
	p, pending := webSlotPreviews.Get(appID)
	if pending && (p.SourceID == id || p.TargetID == id) {
		webRestoreSlotPreview(p)
		if source, ok := webJobSite(p.SourceID); ok {
			restartAzureFunctionInstance(source)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func webSlotNameByID(id string) string {
	if _, slot, ok := strings.Cut(id, "/slots/"); ok {
		return slot
	}
	return "production"
}

// webSlotDifference is one SlotDifference.
type webSlotDifference struct {
	Level              string `json:"level"`
	SettingType        string `json:"settingType"`
	DiffRule           string `json:"diffRule"`
	SettingName        string `json:"settingName"`
	ValueInCurrentSlot string `json:"valueInCurrentSlot"`
	ValueInTargetSlot  string `json:"valueInTargetSlot"`
	Description        string `json:"description"`
}

// webSlotDiff describes how a swap treats a setting the two slots hold
// differently: a swappable one moves with the content, a slot setting stays.
func webSlotDiff(settingType, kind, name, current, target string, sticky bool) webSlotDifference {
	d := webSlotDifference{
		Level:              "Information",
		SettingType:        settingType,
		DiffRule:           "SettingsWillBeSwapped",
		SettingName:        name,
		ValueInCurrentSlot: current,
		ValueInTargetSlot:  target,
		Description:        fmt.Sprintf("The %s '%s' differs between the slots and is swapped with the content.", kind, name),
	}
	if sticky {
		d.Level = "Warning"
		d.DiffRule = "SettingsWillNotBeSwapped"
		d.Description = fmt.Sprintf("The %s '%s' differs between the slots and stays with its slot: after the swap the current slot's content runs with the target slot's value.", kind, name)
	}
	return d
}

// webListSlotDifferences answers the settings the slot the request addresses
// holds differently from its targetSlot.
func webListSlotDifferences(w http.ResponseWriter, r *http.Request) {
	appID, current, target, ok := webSlotPair(w, r)
	if !ok {
		return
	}
	curSettings, tgtSettings := siteAppSettings(&current), siteAppSettings(&target)
	curCfg, _ := siteConfigStore.Get(current.ID)
	tgtCfg, _ := siteConfigStore.Get(target.ID)
	stickySetting, stickyConn := webSlotSticky(appID, curSettings, tgtSettings)
	var out []webSlotDifference
	for _, name := range webUnionKeys(curSettings, tgtSettings) {
		cv, cok := curSettings[name]
		tv, tok := tgtSettings[name]
		if cok == tok && cv == tv {
			continue
		}
		out = append(out, webSlotDiff("AppSetting", "app setting", name, cv, tv, stickySetting(name)))
	}
	for _, name := range webUnionKeys(curCfg.ConnectionStrings, tgtCfg.ConnectionStrings) {
		cv, cok := curCfg.ConnectionStrings[name]
		tv, tok := tgtCfg.ConnectionStrings[name]
		if cok == tok && cv == tv {
			continue
		}
		out = append(out, webSlotDiff("ConnectionString", "connection string", name, cv.Value, tv.Value, stickyConn(name)))
	}
	general, err := webGeneralSettingDiffs(current.Properties.SiteConfig, target.Properties.SiteConfig)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	out = append(out, general...)
	value := make([]map[string]any, 0, len(out))
	for _, d := range out {
		value = append(value, map[string]any{"properties": d})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

// webGeneralSettingDiffs lists the siteConfig general settings the two slots
// hold differently.
func webGeneralSettingDiffs(current, target *SiteConfig) ([]webSlotDifference, error) {
	flat := func(c *SiteConfig) (map[string]json.RawMessage, error) {
		out := map[string]json.RawMessage{}
		if c == nil {
			return out, nil
		}
		data, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		delete(out, "appSettings")
		delete(out, "connectionStrings")
		for k, v := range out {
			if string(v) == "null" {
				delete(out, k)
			}
		}
		return out, nil
	}
	cur, err := flat(current)
	if err != nil {
		return nil, err
	}
	tgt, err := flat(target)
	if err != nil {
		return nil, err
	}
	text := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		var buf bytes.Buffer
		if json.Compact(&buf, raw) == nil {
			return buf.String()
		}
		return string(raw)
	}
	var out []webSlotDifference
	for _, name := range webUnionKeys(cur, tgt) {
		cv, tv := text(cur[name]), text(tgt[name])
		if cv == tv {
			continue
		}
		out = append(out, webSlotDiff("General", "general setting", name, cv, tv, webSlotSettingsNotSwapped[name]))
	}
	return out, nil
}

func webUnionKeys[V any](a, b map[string]V) []string {
	keys := slices.Collect(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// webCleanupSlotPreview ends the swap with preview a deleted app or slot took
// part in: a source that outlives its target gets its own settings back.
func webCleanupSlotPreview(resID string) {
	appID, _, isSlot := strings.Cut(resID, "/slots/")
	p, pending := webSlotPreviews.Get(appID)
	switch {
	case !pending:
	case !isSlot || p.SourceID == resID:
		webSlotPreviews.Delete(appID)
	case p.TargetID == resID:
		webRestoreSlotPreview(p)
	}
}
