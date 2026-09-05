package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_function_keys.go implements the Azure Functions key surface of the
// Microsoft.Web ARM slice: host-level keys (master key, host function keys,
// system keys), per-function keys, the functions admin token, and the
// host-sync actions (ListSyncStatus / SyncFunctions / SyncFunctionTriggers /
// ListSyncFunctionTriggers). The keys are load-bearing: the POST /api/function
// invoke path enforces the real Azure Functions authLevel contract against
// them (see azureFunctionInvokeAuthorized).

// WebHostKeysRow is the durable per-site (or per-slot) Functions host key
// store — the state behind WebApps_ListHostKeys and
// WebApps_{CreateOrUpdate,Delete}HostSecret. Real Azure mints a master key and
// a "default" host function key when the function app is created; the sim does
// the same (ensureWebHostKeys, called from site/slot creation). Keyed by the
// canonical ARM resource ID of the site or slot.
type WebHostKeysRow struct {
	MasterKey    string            `json:"masterKey"`
	FunctionKeys map[string]string `json:"functionKeys"`
	SystemKeys   map[string]string `json:"systemKeys"`
}

var webHostKeys sim.Store[WebHostKeysRow]

// WebFunctionKeysRow holds one function's own keys (WebApps_ListFunctionKeys /
// CreateOrUpdateFunctionSecret / DeleteFunctionSecret). Real Azure mints a
// "default" key when the function is created. Keyed by the function's
// canonical ARM resource ID (<site>/functions/<name>), carried in ID so the
// site-deletion cleanup can find every row under a site prefix.
type WebFunctionKeysRow struct {
	ID   string            `json:"id"`
	Keys map[string]string `json:"keys"`
}

var webFunctionKeys sim.Store[WebFunctionKeysRow]

// simFunctionKey derives deterministic Functions key material. Real Azure
// Functions keys use the URL-safe base64 alphabet — they travel raw in
// `?code=` query strings — so this deliberately differs from the standard
// base64 SAS-key shape of simListKey32.
func simFunctionKey(resID, kind string) string {
	sum := sha256.Sum256([]byte("sim-function-key|" + resID + "|" + kind))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ensureWebHostKeys returns the site's host key row, minting the initial
// master key + "default" host function key on first reference — the key set
// real Azure provisions with a new function app. Minting happens at site
// creation; the ensure-on-read form keeps one code path for rows that predate
// the site's keys (a persisted database from before the site's first key
// read). A row that exists is returned untouched, so deleting an individual
// key stays deleted.
func ensureWebHostKeys(resID string) WebHostKeysRow {
	if row, ok := webHostKeys.Get(resID); ok {
		if row.FunctionKeys == nil {
			row.FunctionKeys = map[string]string{}
		}
		if row.SystemKeys == nil {
			row.SystemKeys = map[string]string{}
		}
		return row
	}
	row := WebHostKeysRow{
		MasterKey:    simFunctionKey(resID, "host-master"),
		FunctionKeys: map[string]string{"default": simFunctionKey(resID, "host-functionKeys-default")},
		SystemKeys:   map[string]string{},
	}
	webHostKeys.Put(resID, row)
	return row
}

// ensureWebFunctionKeys returns a function's key row, minting the "default"
// function key real Azure provisions with a new function.
func ensureWebFunctionKeys(fnID string) WebFunctionKeysRow {
	if row, ok := webFunctionKeys.Get(fnID); ok {
		if row.Keys == nil {
			row.Keys = map[string]string{}
		}
		return row
	}
	row := WebFunctionKeysRow{ID: fnID, Keys: map[string]string{"default": simFunctionKey(fnID, "function-default")}}
	webFunctionKeys.Put(fnID, row)
	return row
}

// webCleanupFunctionKeys removes the key rows stored under a deleted site or
// slot (host keys plus every per-function key row).
func webCleanupFunctionKeys(resID string) {
	webHostKeys.Delete(resID)
	prefix := resID + "/functions/"
	for _, row := range webFunctionKeys.Filter(func(row WebFunctionKeysRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		webFunctionKeys.Delete(row.ID)
	}
}

// webFunctionID is the canonical ARM ID of the addressed function under the
// addressed site or slot.
func webFunctionID(r *http.Request) string {
	return webResourceID(r) + "/functions/" + sim.PathParam(r, "functionName")
}

// webFunctionMissing writes the canonical 404 when the addressed function does
// not exist (the site/slot is checked first); returns true when it responded.
func webFunctionMissing(w http.ResponseWriter, r *http.Request) bool {
	if webMissing(w, r) {
		return true
	}
	if _, ok := azfFunctionConfigs.Get(webFunctionID(r)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Function %q not found.", sim.PathParam(r, "functionName"))
		return true
	}
	return false
}

func registerWebFunctionKeyHandlers(both func(string, string, http.HandlerFunc)) {
	// POST /host/default/listkeys — WebApps_ListHostKeys. The HostKeys wire
	// shape is a bare object (no ARM resource envelope).
	both("POST", "/host/default/listkeys", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row := ensureWebHostKeys(webResourceID(r))
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"masterKey":    row.MasterKey,
			"functionKeys": row.FunctionKeys,
			"systemKeys":   row.SystemKeys,
		})
	})

	// PUT /host/default/{keyType}/{keyName} — WebApps_CreateOrUpdateHostSecret.
	// keyType is the host key family exactly as the Azure CLI and SDK spell it:
	// functionKeys, systemKeys, or masterKey. An omitted value means "generate
	// one", as real Azure does.
	both("PUT", "/host/default/{keyType}/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		reqValue, ok := readKeyInfoValue(w, r)
		if !ok {
			return
		}
		resID := webResourceID(r)
		row := ensureWebHostKeys(resID)
		keyType := sim.PathParam(r, "keyType")
		keyName := sim.PathParam(r, "keyName")
		value := reqValue
		if value == "" {
			value = simFunctionKey(resID, "host-"+keyType+"-"+keyName+"-"+time.Now().UTC().Format(time.RFC3339Nano))
		}
		created := false
		switch keyType {
		case "functionKeys":
			_, existed := row.FunctionKeys[keyName]
			created = !existed
			row.FunctionKeys[keyName] = value
		case "systemKeys":
			_, existed := row.SystemKeys[keyName]
			created = !existed
			row.SystemKeys[keyName] = value
		case "masterKey":
			row.MasterKey = value
		default:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"Invalid key type %q. Valid types are 'functionKeys', 'systemKeys' and 'masterKey'.", keyType)
			return
		}
		webHostKeys.Put(resID, row)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		sim.WriteJSON(w, status, map[string]any{"name": keyName, "value": value})
	})

	// DELETE /host/default/{keyType}/{keyName} — WebApps_DeleteHostSecret.
	both("DELETE", "/host/default/{keyType}/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		resID := webResourceID(r)
		row := ensureWebHostKeys(resID)
		keyType := sim.PathParam(r, "keyType")
		keyName := sim.PathParam(r, "keyName")
		var existed bool
		switch keyType {
		case "functionKeys":
			_, existed = row.FunctionKeys[keyName]
			delete(row.FunctionKeys, keyName)
		case "systemKeys":
			_, existed = row.SystemKeys[keyName]
			delete(row.SystemKeys, keyName)
		}
		if !existed {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"Host key %q of type %q was not found.", keyName, keyType)
			return
		}
		webHostKeys.Put(resID, row)
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /functions/{functionName}/listkeys — WebApps_ListFunctionKeys
	// (StringDictionary of the function's own keys).
	both("POST", "/functions/{functionName}/listkeys", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) {
			return
		}
		row := ensureWebFunctionKeys(webFunctionID(r))
		sim.WriteJSON(w, http.StatusOK, map[string]any{"properties": row.Keys})
	})

	// POST /functions/{functionName}/listsecrets — WebApps_ListFunctionSecrets.
	// Real Azure returns the function's default key and the HTTP-trigger URL
	// carrying it.
	both("POST", "/functions/{functionName}/listsecrets", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) {
			return
		}
		row := ensureWebFunctionKeys(webFunctionID(r))
		key := row.Keys["default"]
		site, _ := webResource(r)
		fnName := sim.PathParam(r, "functionName")
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"key":         key,
			"trigger_url": fmt.Sprintf("https://%s/api/%s?code=%s", site.Properties.DefaultHostName, fnName, key),
		})
	})

	// PUT /functions/{functionName}/keys/{keyName} —
	// WebApps_CreateOrUpdateFunctionSecret.
	both("PUT", "/functions/{functionName}/keys/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) {
			return
		}
		reqValue, ok := readKeyInfoValue(w, r)
		if !ok {
			return
		}
		fnID := webFunctionID(r)
		row := ensureWebFunctionKeys(fnID)
		keyName := sim.PathParam(r, "keyName")
		value := reqValue
		if value == "" {
			value = simFunctionKey(fnID, "function-"+keyName+"-"+time.Now().UTC().Format(time.RFC3339Nano))
		}
		_, existed := row.Keys[keyName]
		row.Keys[keyName] = value
		webFunctionKeys.Put(fnID, row)
		status := http.StatusOK
		if !existed {
			status = http.StatusCreated
		}
		sim.WriteJSON(w, status, map[string]any{"name": keyName, "value": value})
	})

	// DELETE /functions/{functionName}/keys/{keyName} —
	// WebApps_DeleteFunctionSecret.
	both("DELETE", "/functions/{functionName}/keys/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) {
			return
		}
		fnID := webFunctionID(r)
		row := ensureWebFunctionKeys(fnID)
		keyName := sim.PathParam(r, "keyName")
		if _, existed := row.Keys[keyName]; !existed {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"Function key %q was not found.", keyName)
			return
		}
		delete(row.Keys, keyName)
		webFunctionKeys.Put(fnID, row)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /functions/admin/token — WebApps_GetFunctionsAdminToken: a
	// short-lived JWT for the Functions host admin API, HMAC-SHA256-signed
	// with the site's master key (the key the host itself accepts), returned
	// as a JSON string.
	both("GET", "/functions/admin/token", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		row := ensureWebHostKeys(webResourceID(r))
		sim.WriteJSON(w, http.StatusOK, webFunctionsAdminToken(site.Properties.DefaultHostName, row.MasterKey))
	})

	// The host-sync action family. The sim keeps every function's trigger
	// metadata in its own durable store, so a sync request finds nothing stale
	// to reconcile and completes immediately — 204, the documented success.
	sync204 := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	// POST /host/default/listsyncstatus — WebApps_ListSyncStatus.
	both("POST", "/host/default/listsyncstatus", sync204)
	// POST /host/default/sync — WebApps_SyncFunctions.
	both("POST", "/host/default/sync", sync204)
	// POST /syncfunctiontriggers — WebApps_SyncFunctionTriggers.
	both("POST", "/syncfunctiontriggers", sync204)

	// POST /listsyncfunctiontriggerstatus — WebApps_ListSyncFunctionTriggers.
	// Its purpose (per the ARM template deployment flow it exists for) is to
	// hand the caller the credentials for the host's synctriggers endpoint:
	// the FunctionSecrets shape carrying the master key and the trigger URL.
	both("POST", "/listsyncfunctiontriggerstatus", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		row := ensureWebHostKeys(webResourceID(r))
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"key":         row.MasterKey,
			"trigger_url": fmt.Sprintf("https://%s/admin/host/synctriggers", site.Properties.DefaultHostName),
		})
	})
}

// readKeyInfoValue reads a CreateOrUpdate{Host,Function}Secret request body's
// key value. Real Azure Resource Manager accepts both KeyInfo wire spellings
// its api-versions define — the flat {"name","value"} object of 2025-03-01
// (the Go SDK) and the ProxyOnlyResource {"properties":{"name","value"}}
// envelope of the 2018-11-01 track the Azure CLI sends — and so does the
// simulator. An empty body means "generate a value". Returns ok=false after
// writing the 400 for a malformed body.
func readKeyInfoValue(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Name       string `json:"name"`
		Value      string `json:"value"`
		Properties *struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return "", false
	}
	if req.Value == "" && req.Properties != nil {
		return req.Properties.Value, true
	}
	return req.Value, true
}

// webFunctionsAdminToken mints the Functions admin JWT: HMAC-SHA256 signed
// with the site's master key, issued by the site's SCM host for the
// /azurefunctions audience — the shape the Functions host validates.
func webFunctionsAdminToken(defaultHostName, masterKey string) string {
	scmHost := strings.Replace(defaultHostName, ".azurewebsites.net", ".scm.azurewebsites.net", 1)
	now := time.Now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": "https://" + scmHost,
		"aud": "https://" + defaultHostName + "/azurefunctions",
		"nbf": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(masterKey))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// azureFunctionInvokeAuthorized enforces the real Azure Functions authLevel
// contract on the POST /api/function invoke path. The route addresses the
// function named "function"; when the site declares that function's config
// (the ARM FunctionEnvelope carrying the function.json bindings), the
// Functions host enforces the httpTrigger binding's authLevel:
//
//   - "anonymous" — no key required;
//   - "function" (and the real host's default when the binding omits
//     authLevel) — a valid key in the x-functions-key header or the ?code=
//     query: one of the function's own keys, a host-level function key, or
//     the master key;
//   - "admin" — the master key only.
//
// A site with no declared function config is a container site (Web App for
// Containers / the sockerless bootstrap): App Service routes the request
// straight to the site's container with no function.json for the host to
// read, so no key check applies — which is why fixtures that declare no
// function stay keyless.
func azureFunctionInvokeAuthorized(site *Site, r *http.Request) bool {
	fn, ok := azfFunctionConfigs.Get(site.ID + "/functions/function")
	if !ok {
		return true
	}
	level := azureFunctionHTTPAuthLevel(fn)
	if level == "anonymous" {
		return true
	}
	key := r.Header.Get("x-functions-key")
	if key == "" {
		key = r.URL.Query().Get("code")
	}
	if key == "" {
		return false
	}
	hostRow := ensureWebHostKeys(site.ID)
	if key == hostRow.MasterKey {
		return true
	}
	if level == "admin" {
		return false
	}
	for _, v := range hostRow.FunctionKeys {
		if key == v {
			return true
		}
	}
	fnRow := ensureWebFunctionKeys(fn.ID)
	for _, v := range fnRow.Keys {
		if key == v {
			return true
		}
	}
	return false
}

// azureFunctionHTTPAuthLevel reads the httpTrigger binding's authLevel from a
// function's stored config. The real Functions host defaults to "function"
// when function.json omits the member.
func azureFunctionHTTPAuthLevel(fn FunctionEnvelope) string {
	bindings, ok := fn.Properties.Config["bindings"].([]any)
	if !ok {
		return "function"
	}
	for _, b := range bindings {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); !strings.EqualFold(t, "httpTrigger") {
			continue
		}
		if lvl, _ := m["authLevel"].(string); lvl != "" {
			return strings.ToLower(lvl)
		}
		return "function"
	}
	return "function"
}
