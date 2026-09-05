package main

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_configrefs.go serves /config/configreferences: the Key Vault reference
// status of a site's (or slot's) application settings and connection strings.
// The surface keeps NO state of its own — every response is derived on read
// by parsing the stored app-setting / connection-string values for the
// @Microsoft.KeyVault(...) reference syntax and resolving the named vault and
// secret against the simulator's own Key Vault slice, exactly as the real
// service resolves references against real Key Vault.

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

// webResolveKVRef derives the ApiKVReference properties for one reference
// value: the reference string, the vault/secret coordinates it names, and the
// resolution status against the simulator's Key Vault stores (the vault
// resource, the secret, and — when the reference pins one — the exact secret
// version must exist).
func webResolveKVRef(raw string) map[string]any {
	props := map[string]any{
		"reference": raw,
		"source":    "KeyVault",
	}
	ref, _ := webParseKeyVaultRef(raw)
	if !ref.Valid {
		props["status"] = "InvalidSyntax"
		return props
	}
	props["vaultName"] = ref.VaultName
	props["secretName"] = ref.SecretName
	if ref.SecretVersion != "" {
		props["secretVersion"] = ref.SecretVersion
	}
	vaults := keyVaults.Filter(func(v KeyVault) bool { return strings.EqualFold(v.Name, ref.VaultName) })
	if len(vaults) == 0 {
		props["status"] = "VaultNotFound"
		return props
	}
	// The data plane keys secrets by the vault's host label, which is the
	// vault name as created; resolve through the stored vault's own name so
	// a reference written in a different casing still finds it.
	stored, ok := keyVaultData.Get(keyVaultSecretKey(vaults[0].Name, ref.SecretName))
	if !ok || stored.isDeleted() || len(stored.Versions) == 0 {
		props["status"] = "SecretNotFound"
		return props
	}
	version := stored.latest().Version
	if ref.SecretVersion != "" {
		if _, ok := stored.findVersion(ref.SecretVersion); !ok {
			props["status"] = "SecretVersionNotFound"
			return props
		}
		version = ref.SecretVersion
	}
	props["status"] = "Resolved"
	props["activeVersion"] = version
	return props
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
		cfg, _ := siteConfigStore.Get(webResourceID(r))
		keys := make([]string, 0, len(cfg.AppSettings))
		for k := range cfg.AppSettings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		refs := []any{}
		for _, k := range keys {
			if _, isRef := webParseKeyVaultRef(cfg.AppSettings[k]); isRef {
				refs = append(refs, webKVRefResource(webResourceID(r), "appsettings", k, webResolveKVRef(cfg.AppSettings[k])))
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
		sim.WriteJSON(w, http.StatusOK, webKVRefResource(webResourceID(r), "appsettings", key, webResolveKVRef(value)))
	})

	// Connection-string references, over the stored connection-string values.
	both("GET", "/config/configreferences/connectionstrings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		cfg, _ := siteConfigStore.Get(webResourceID(r))
		keys := make([]string, 0, len(cfg.ConnectionStrings))
		for k := range cfg.ConnectionStrings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		refs := []any{}
		for _, k := range keys {
			if _, isRef := webParseKeyVaultRef(cfg.ConnectionStrings[k].Value); isRef {
				refs = append(refs, webKVRefResource(webResourceID(r), "connectionstrings", k, webResolveKVRef(cfg.ConnectionStrings[k].Value)))
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
		sim.WriteJSON(w, http.StatusOK, webKVRefResource(webResourceID(r), "connectionstrings", key, webResolveKVRef(entry.Value)))
	})
}
