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

// The Azure Functions key surface: host keys (master, function, system),
// per-function keys, the admin token, and the host-sync actions. The POST
// /api/function invoke path enforces authLevel against these keys
// (azureFunctionInvokeAuthorized).

// Azure mints a master key and a "default" host function key with a new
// function app, as ensureWebHostKeys does. Keyed by canonical site or slot ID.
// EncryptionKey is the site's WEBSITE_AUTH_ENCRYPTION_KEY, which no ARM
// operation returns.
type WebHostKeysRow struct {
	MasterKey     string            `json:"masterKey"`
	FunctionKeys  map[string]string `json:"functionKeys"`
	SystemKeys    map[string]string `json:"systemKeys"`
	EncryptionKey string            `json:"encryptionKey"`
}

var webHostKeys sim.Store[WebHostKeysRow]

// Azure mints a "default" key with a new function. Keyed by
// <site>/functions/<name>, carried in ID so site deletion finds every row by
// prefix.
type WebFunctionKeysRow struct {
	ID   string            `json:"id"`
	Keys map[string]string `json:"keys"`
}

var webFunctionKeys sim.Store[WebFunctionKeysRow]

// A row that exists keeps its keys, so a deleted key stays deleted.
func ensureWebHostKeys(resID string) WebHostKeysRow {
	row, ok := webHostKeys.Get(resID)
	if !ok {
		row = WebHostKeysRow{
			MasterKey:    newFunctionsKey(functionsMasterKeySeed),
			FunctionKeys: map[string]string{"default": newFunctionsKey(functionsFunctionKeySeed)},
		}
	}
	if row.FunctionKeys == nil {
		row.FunctionKeys = map[string]string{}
	}
	if row.SystemKeys == nil {
		row.SystemKeys = map[string]string{}
	}
	if !ok || row.EncryptionKey == "" {
		if row.EncryptionKey == "" {
			row.EncryptionKey = newSiteEncryptionKey()
		}
		webHostKeys.Put(resID, row)
	}
	return row
}

func ensureWebFunctionKeys(fnID string) WebFunctionKeysRow {
	if row, ok := webFunctionKeys.Get(fnID); ok {
		if row.Keys == nil {
			row.Keys = map[string]string{}
		}
		return row
	}
	row := WebFunctionKeysRow{ID: fnID, Keys: map[string]string{"default": newFunctionsKey(functionsFunctionKeySeed)}}
	webFunctionKeys.Put(fnID, row)
	return row
}

func webCleanupFunctionKeys(resID string) {
	webHostKeys.Delete(resID)
	prefix := resID + "/functions/"
	for _, row := range webFunctionKeys.Filter(func(row WebFunctionKeysRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		webFunctionKeys.Delete(row.ID)
	}
}

func webFunctionID(r *http.Request) string {
	resID := webResourceID(r)
	if site, ok := hostRunFunctionApp(resID); ok {
		return siteFunctionID(site, sim.PathParam(r, "functionName"))
	}
	return resID + "/functions/" + sim.PathParam(r, "functionName")
}

// The site or slot is checked first. Returns true when it responded.
func webFunctionMissing(w http.ResponseWriter, r *http.Request) bool {
	if webMissing(w, r) {
		return true
	}
	var exists bool
	if site, ok := hostRunFunctionApp(webResourceID(r)); ok {
		_, exists = siteFunction(site, sim.PathParam(r, "functionName"))
	} else {
		_, exists = azfFunctionConfigs.Get(webFunctionID(r))
	}
	if !exists {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Function %q not found.", sim.PathParam(r, "functionName"))
		return true
	}
	return false
}

// Answers 500 and returns false when the store cannot be read.
func functionsHostSecretsIn(w http.ResponseWriter, r *http.Request) bool {
	site, ok := hostRunFunctionApp(webResourceID(r))
	if !ok {
		return true
	}
	if err := importFunctionsHostSecrets(site); err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "read the function app's keys: %v", err)
		return false
	}
	return true
}

// The running host picks them up from the store.
func functionsHostSecretsOut(w http.ResponseWriter, r *http.Request) bool {
	site, ok := hostRunFunctionApp(webResourceID(r))
	if !ok {
		return true
	}
	if err := exportFunctionsHostSecrets(site); err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "write the function app's keys: %v", err)
		return false
	}
	return true
}

func registerWebFunctionKeyHandlers(both func(string, string, http.HandlerFunc)) {
	// The HostKeys shape is a bare object, no resource envelope.
	both("POST", "/host/default/listkeys", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !functionsHostSecretsIn(w, r) {
			return
		}
		row := ensureWebHostKeys(webResourceID(r))
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"masterKey":    row.MasterKey,
			"functionKeys": row.FunctionKeys,
			"systemKeys":   row.SystemKeys,
		})
	})

	// keyType is functionKeys, systemKeys or masterKey, as the Azure CLI and SDK
	// spell it. An omitted value means "generate one", as in Azure.
	both("PUT", "/host/default/{keyType}/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		reqValue, ok := readKeyInfoValue(w, r)
		if !ok || !functionsHostSecretsIn(w, r) {
			return
		}
		resID := webResourceID(r)
		row := ensureWebHostKeys(resID)
		keyType := sim.PathParam(r, "keyType")
		keyName := sim.PathParam(r, "keyName")
		value := reqValue
		if value == "" {
			value = newFunctionsKey(functionsHostKeySeed(keyType))
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
		if !functionsHostSecretsOut(w, r) {
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		sim.WriteJSON(w, status, map[string]any{"name": keyName, "value": value})
	})

	both("DELETE", "/host/default/{keyType}/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !functionsHostSecretsIn(w, r) {
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
		if !functionsHostSecretsOut(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	both("POST", "/functions/{functionName}/listkeys", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) || !functionsHostSecretsIn(w, r) {
			return
		}
		row := ensureWebFunctionKeys(webFunctionID(r))
		sim.WriteJSON(w, http.StatusOK, map[string]any{"properties": row.Keys})
	})

	// Azure returns the function's default key and the HTTP-trigger URL carrying
	// it.
	both("POST", "/functions/{functionName}/listsecrets", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) || !functionsHostSecretsIn(w, r) {
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

	both("PUT", "/functions/{functionName}/keys/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) {
			return
		}
		reqValue, ok := readKeyInfoValue(w, r)
		if !ok || !functionsHostSecretsIn(w, r) {
			return
		}
		fnID := webFunctionID(r)
		row := ensureWebFunctionKeys(fnID)
		keyName := sim.PathParam(r, "keyName")
		value := reqValue
		if value == "" {
			value = newFunctionsKey(functionsFunctionKeySeed)
		}
		_, existed := row.Keys[keyName]
		row.Keys[keyName] = value
		webFunctionKeys.Put(fnID, row)
		if !functionsHostSecretsOut(w, r) {
			return
		}
		status := http.StatusOK
		if !existed {
			status = http.StatusCreated
		}
		sim.WriteJSON(w, status, map[string]any{"name": keyName, "value": value})
	})

	both("DELETE", "/functions/{functionName}/keys/{keyName}", func(w http.ResponseWriter, r *http.Request) {
		if webFunctionMissing(w, r) || !functionsHostSecretsIn(w, r) {
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
		if !functionsHostSecretsOut(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// A short-lived JWT for the host admin API, HMAC-SHA256-signed with the
	// site's WEBSITE_AUTH_ENCRYPTION_KEY, which the host validates platform tokens
	// with, returned as a JSON string.
	both("GET", "/functions/admin/token", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		row := ensureWebHostKeys(webResourceID(r))
		token, err := webFunctionsAdminToken(site.Properties.DefaultHostName, row.EncryptionKey)
		if err != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "sign the admin token: %v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, token)
	})

	// Every function's trigger metadata lives in a durable store, so a sync finds
	// nothing stale and completes at once with 204, the documented success.
	sync204 := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	both("POST", "/host/default/listsyncstatus", sync204)
	both("POST", "/host/default/sync", sync204)
	both("POST", "/syncfunctiontriggers", sync204)

	// Exists for the ARM template deployment flow: hands the caller the
	// credentials for the host's synctriggers endpoint, the master key and the
	// trigger URL.
	both("POST", "/listsyncfunctiontriggerstatus", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) || !functionsHostSecretsIn(w, r) {
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

// Azure Resource Manager accepts both KeyInfo spellings: the flat
// {"name","value"} of 2025-03-01 (the Go SDK) and the {"properties":{...}}
// envelope of 2018-11-01 (the Azure CLI). An empty body means "generate a
// value". Returns ok=false after writing the 400.
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

// Issued by the site's SCM host for its /azurefunctions audience and signed
// with the site's encryption key.
func webFunctionsAdminToken(defaultHostName, encryptionKey string) (string, error) {
	key, err := functionsKeyBytes(encryptionKey)
	if err != nil {
		return "", err
	}
	scmHost := strings.Replace(defaultHostName, ".azurewebsites.net", ".scm.azurewebsites.net", 1)
	now := time.Now().UTC()
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": "https://" + scmHost,
		"aud": "https://" + defaultHostName + "/azurefunctions",
		"nbf": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func functionsHostKeySeed(keyType string) uint64 {
	switch keyType {
	case "masterKey":
		return functionsMasterKeySeed
	case "systemKeys":
		return functionsSystemKeySeed
	}
	return functionsFunctionKeySeed
}

// The route addresses the function named "function". When the site declares
// its config (the FunctionEnvelope carrying function.json), the host enforces
// the httpTrigger's authLevel:
//
//   - "anonymous" — no key required;
//   - "function" (the host's default) — a key in x-functions-key or ?code=:
//     the function's own, a host function key, or the master key;
//   - "admin" — the master key only.
//
// With no declared config there is no function.json, so no key check applies.
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

// The host defaults to "function" when function.json omits the member.
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
