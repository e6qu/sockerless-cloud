package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_configrefs.go resolves an App Service app's Key Vault references — app
// settings and connection strings whose value is @Microsoft.KeyVault(...) —
// against the simulator's Key Vault slice through the identity the app's
// keyVaultReferenceIdentity names, with that identity's access to the vault,
// and serves /config/configreferences: the status of each reference. The
// workload's environment carries the secret's value where a reference
// resolves and the reference text where it does not.

// webKVRef is one parsed @Microsoft.KeyVault(...) reference.
type webKVRef struct {
	VaultName     string
	SecretName    string
	SecretVersion string
	Valid         bool // syntactically complete (vault + secret identified)
}

// webParseKeyVaultRef parses an app-setting value. The second result reports
// whether the value is a Key Vault reference at all (the @Microsoft.KeyVault(
// prefix); a non-reference value is not part of the configreferences surface.
// Both documented spellings are handled: SecretUri=..., and
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

// webResolveKVRef resolves one reference value for a site: the ApiKVReference
// properties that report it, and the secret's value when it resolved. The
// site reaches the vault as the identity its keyVaultReferenceIdentity names,
// which must be attached to it; the vault must exist and grant that identity
// the secret get permission (an access policy, or an Azure RBAC role
// assignment with the getSecret data action when the vault uses Azure RBAC);
// and the secret, and the version a reference pins, must exist and be
// enabled and current.
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
	vault, found := keyVaultsByName.Lookup(keyVaults, strings.ToLower(ref.VaultName),
		func(v KeyVault) []string { return []string{strings.ToLower(v.Name)} })
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
	// The data plane keys secrets by the vault's host label, which is the
	// vault name as created; resolve through the stored vault's own name so
	// a reference written in a different casing still finds it.
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

// keyVaultsByName finds a vault by its globally unique name on every
// reference a workload resolves.
var keyVaultsByName sim.GenerationIndex[KeyVault]

// webKVRefPrincipal is the principal a site reaches Key Vault as, and the
// ManagedServiceIdentity a reference status reports: the site's
// system-assigned identity, or the user-assigned identity its
// keyVaultReferenceIdentity names, which must be attached to the site.
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

// webResolvedAppSettings is the site's app settings as its workload sees
// them: each Key Vault reference that resolves replaced by the secret's
// value, one that does not left as the reference.
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

// webKVRefResource wraps one reference's properties in the ApiKVReference
// ARM envelope.
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

	// App-setting references: the collection carries every app setting whose
	// value is a Key Vault reference; a single key that exists but is not a
	// reference is not part of this surface and reports 404, as does an
	// absent key.
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

	// Connection-string references, over the stored connection-string values.
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
