package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Resolves an app's @Microsoft.KeyVault(...) app settings and connection
// strings through the identity keyVaultReferenceIdentity names, with that
// identity's vault access, and serves /config/configreferences, each
// reference's status. The workload gets the secret's value where a reference
// resolves and the reference text where it does not.

type webKVRef struct {
	VaultName     string
	SecretName    string
	SecretVersion string
	Valid         bool // syntactically complete (vault + secret identified)
}

// The second result reports whether the value is a reference at all. Both
// documented spellings parse: SecretUri=..., and
// VaultName=...;SecretName=...[;SecretVersion=...].
func webParseKeyVaultRef(value string) (webKVRef, bool) {
	const prefix = "@Microsoft.KeyVault("
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, ")") {
		return webKVRef{}, false
	}
	inner := value[len(prefix) : len(value)-1]
	var ref webKVRef
	for _, part := range strings.Split(inner, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch {
		case strings.EqualFold(key, "SecretUri"):
			u, err := url.Parse(val)
			if err != nil || u.Host == "" {
				return webKVRef{}, true
			}
			ref.VaultName, _, _ = strings.Cut(u.Host, ".")
			segs := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(segs) < 2 || !strings.EqualFold(segs[0], "secrets") {
				return webKVRef{}, true
			}
			ref.SecretName = segs[1]
			if len(segs) > 2 {
				ref.SecretVersion = segs[2]
			}
		case strings.EqualFold(key, "VaultName"):
			ref.VaultName = val
		case strings.EqualFold(key, "SecretName"):
			ref.SecretName = val
		case strings.EqualFold(key, "SecretVersion"):
			ref.SecretVersion = val
		}
	}
	ref.Valid = ref.VaultName != "" && ref.SecretName != ""
	return ref, true
}

// Details App Service reports with an unresolved reference's status.
const (
	webKVRefDetailMSINotEnabled = "Key Vault reference was not able to be resolved because site Managed Identity not enabled"
	webKVRefDetailDenied        = "Key Vault reference was not able to be resolved because site was denied access to Key Vault reference's vault."
)

// The site reaches the vault as the attached identity keyVaultReferenceIdentity
// names; the vault must grant it secret get (an access policy, or an Azure RBAC
// assignment with the getSecret data action), and the secret and pinned version
// must exist, be enabled and be current.
func webResolveKVRef(site *Site, raw string) (map[string]any, string, bool) {
	props := map[string]any{
		"reference": raw,
		"source":    "KeyVault",
	}
	ref, _ := webParseKeyVaultRef(raw)
	if !ref.Valid {
		props["status"] = "InvalidSyntax"
		props["details"] = "Key Vault reference was not able to be resolved because the reference syntax is invalid."
		return props, "", false
	}
	props["vaultName"] = ref.VaultName
	props["secretName"] = ref.SecretName
	if ref.SecretVersion != "" {
		props["secretVersion"] = ref.SecretVersion
	}
	principalID, identityType, ok := webKVRefPrincipal(site)
	props["identityType"] = identityType
	if !ok {
		props["status"] = "MSINotEnabled"
		props["details"] = webKVRefDetailMSINotEnabled
		return props, "", false
	}
	vault, found := keyVaultByName(ref.VaultName)
	if !found {
		props["status"] = "VaultNotFound"
		props["details"] = fmt.Sprintf("Key Vault reference was not able to be resolved because vault '%s' could not be found.", ref.VaultName)
		return props, "", false
	}
	if !keyVaultGrantsSecretGet(vault, principalID, ref.SecretName) {
		props["status"] = "AccessToKeyVaultDenied"
		props["details"] = webKVRefDetailDenied
		return props, "", false
	}
	// The data plane keys secrets by the vault's name as created, so resolve
	// through the stored vault to tolerate other casings.
	stored, found := keyVaultData.Get(keyVaultSecretKey(vault.Name, ref.SecretName))
	if !found || stored.isDeleted() || len(stored.Versions) == 0 {
		props["status"] = "SecretNotFound"
		props["details"] = fmt.Sprintf("Key Vault reference was not able to be resolved because secret '%s' could not be found in vault '%s'.", ref.SecretName, ref.VaultName)
		return props, "", false
	}
	version := stored.latest()
	if ref.SecretVersion != "" {
		if version, found = stored.findVersion(ref.SecretVersion); !found {
			props["status"] = "SecretVersionNotFound"
			props["details"] = fmt.Sprintf("Key Vault reference was not able to be resolved because version '%s' of secret '%s' could not be found in vault '%s'.", ref.SecretVersion, ref.SecretName, ref.VaultName)
			return props, "", false
		}
	}
	if reason := keyVaultSecretVersionUnusable(version.Attributes, time.Now()); reason != "" {
		props["status"] = "OtherReasons"
		props["details"] = "Key Vault reference was not able to be resolved because the secret " + reason + "."
		return props, "", false
	}
	props["status"] = "Resolved"
	props["activeVersion"] = version.Version
	return props, version.Value, true
}

// The system-assigned identity, or the attached user-assigned identity
// keyVaultReferenceIdentity names.
func webKVRefPrincipal(site *Site) (string, map[string]any, bool) {
	want := webKeyVaultReferenceIdentity(site)
	if strings.EqualFold(want, "SystemAssigned") {
		identityType := map[string]any{"type": "SystemAssigned"}
		if !site.Identity.systemAssigned() {
			return "", identityType, false
		}
		return site.Identity.PrincipalID, identityType, true
	}
	identityType := map[string]any{
		"type":                   "UserAssigned",
		"userAssignedIdentities": map[string]any{want: map[string]any{}},
	}
	if !site.Identity.userAssigned() {
		return "", identityType, false
	}
	for rid, uai := range site.Identity.UserAssignedIdentities {
		if strings.EqualFold(rid, want) && uai != nil {
			identityType["userAssignedIdentities"] = map[string]any{rid: map[string]any{
				"principalId": uai.PrincipalID,
				"clientId":    uai.ClientID,
			}}
			return uai.PrincipalID, identityType, true
		}
	}
	return "", identityType, false
}

// A resolving reference is replaced by the secret's value; one that does not
// stays as written.
func webResolvedAppSettings(site *Site) map[string]string {
	settings := siteAppSettings(site)
	for k, v := range settings {
		if _, isRef := webParseKeyVaultRef(v); isRef {
			if _, value, ok := webResolveKVRef(site, v); ok {
				settings[k] = value
			}
		}
	}
	return settings
}

// What the site's containers start with.
type webReferenceValues struct {
	appSettings       map[string]string
	connectionStrings map[string]string
}

func webSiteReferenceValues(site *Site) webReferenceValues {
	return webReferenceValues{appSettings: webResolvedAppSettings(site), connectionStrings: siteConnectionStringEnv(site)}
}

func (v webReferenceValues) equal(other webReferenceValues) bool {
	return maps.Equal(v.appSettings, other.appSettings) && maps.Equal(v.connectionStrings, other.connectionStrings)
}

// "App Service caches the values of the key vault references and refetches it
// every 24 hours."
const webKeyVaultReferenceRefresh = 24 * time.Hour

// Caller holds inst.mu.
func (inst *azureFunctionInstance) cacheReferencesLocked(siteID, containerID string, references webReferenceValues) {
	inst.references = references
	inst.referenceRefresh = bg.AfterFunc(webKeyVaultReferenceRefresh, func() {
		inst.refreshReferences(siteID, containerID)
	})
}

// A versionless reference resolves to the latest version, so a rotated secret
// changes its value; the app then restarts, at once when Always On and on its
// next request otherwise. Unchanged values stay cached another period.
func (inst *azureFunctionInstance) refreshReferences(siteID, containerID string) {
	site, ok := webJobSite(siteID)
	if !ok || !inst.reloadReferences(&site, siteID, containerID) {
		return
	}
	startAlwaysOnSite(site)
}

// Reports whether it stopped the containers.
func (inst *azureFunctionInstance) reloadReferences(site *Site, siteID, containerID string) bool {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.containerID != containerID {
		return false
	}
	references := webSiteReferenceValues(site)
	if references.equal(inst.references) {
		inst.cacheReferencesLocked(siteID, containerID, references)
		return false
	}
	inst.teardownLocked()
	return true
}

func webKVRefResource(resID, section, key string, props map[string]any) map[string]any {
	return map[string]any{
		"id":         resID + "/config/configreferences/" + section + "/" + key,
		"name":       key,
		"type":       "Microsoft.Web/sites/config",
		"properties": props,
	}
}

func registerWebConfigReferences(srv *sim.Server) {
	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	// Only app settings whose value is a reference belong here; a non-reference
	// key and an absent key both answer 404.
	both("GET", "/config/configreferences/appsettings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		cfg, _ := siteConfigStore.Get(webResourceID(r))
		keys := make([]string, 0, len(cfg.AppSettings))
		for k := range cfg.AppSettings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		refs := []any{}
		for _, k := range keys {
			if _, isRef := webParseKeyVaultRef(cfg.AppSettings[k]); isRef {
				props, _, _ := webResolveKVRef(&site, cfg.AppSettings[k])
				refs = append(refs, webKVRefResource(webResourceID(r), "appsettings", k, props))
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": refs})
	})
	both("GET", "/config/configreferences/appsettings/{appSettingKey}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		key := sim.PathParam(r, "appSettingKey")
		cfg, _ := siteConfigStore.Get(webResourceID(r))
		value, ok := cfg.AppSettings[key]
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"App setting %q not found.", key)
			return
		}
		if _, isRef := webParseKeyVaultRef(value); !isRef {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"App setting %q is not a Key Vault reference.", key)
			return
		}
		site, _ := webResource(r)
		props, _, _ := webResolveKVRef(&site, value)
		sim.WriteJSON(w, http.StatusOK, webKVRefResource(webResourceID(r), "appsettings", key, props))
	})

	both("GET", "/config/configreferences/connectionstrings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		cfg, _ := siteConfigStore.Get(webResourceID(r))
		keys := make([]string, 0, len(cfg.ConnectionStrings))
		for k := range cfg.ConnectionStrings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		refs := []any{}
		for _, k := range keys {
			if _, isRef := webParseKeyVaultRef(cfg.ConnectionStrings[k].Value); isRef {
				props, _, _ := webResolveKVRef(&site, cfg.ConnectionStrings[k].Value)
				refs = append(refs, webKVRefResource(webResourceID(r), "connectionstrings", k, props))
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": refs})
	})
	both("GET", "/config/configreferences/connectionstrings/{connectionStringKey}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		key := sim.PathParam(r, "connectionStringKey")
		cfg, _ := siteConfigStore.Get(webResourceID(r))
		entry, ok := cfg.ConnectionStrings[key]
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Connection string %q not found.", key)
			return
		}
		if _, isRef := webParseKeyVaultRef(entry.Value); !isRef {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Connection string %q is not a Key Vault reference.", key)
			return
		}
		site, _ := webResource(r)
		props, _, _ := webResolveKVRef(&site, entry.Value)
		sim.WriteJSON(w, http.StatusOK, webKVRefResource(webResourceID(r), "connectionstrings", key, props))
	})
}
