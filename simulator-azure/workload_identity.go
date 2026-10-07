package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The managed identity endpoint of an App Service or Azure Functions app.
//
// App Service gives an app with a managed identity two environment variables,
// IDENTITY_ENDPOINT and IDENTITY_HEADER, and serves tokens for that app's own
// identities at the endpoint: a request presents IDENTITY_HEADER in
// X-IDENTITY-HEADER, names the token's resource, and selects a user-assigned
// identity by client_id, principal_id (object_id) or mi_res_id; without a
// selector the token is the system-assigned identity's
// (learn.microsoft.com/azure/app-service/overview-managed-identity, "REST
// endpoint reference", api-version 2019-08-01). The header is a secret per
// app, so a request authenticates as the app that holds it.

// appServiceIdentityAPIVersion is the protocol version IDENTITY_ENDPOINT
// serves; the 2017-09-01 version authenticates with MSI_SECRET, which the
// platform no longer sets.
const appServiceIdentityAPIVersion = "2019-08-01"

// workloadIdentityBinding ties an app's IDENTITY_HEADER secret to the app or
// slot it was issued to.
type workloadIdentityBinding struct {
	Header     string `json:"header"`
	ResourceID string `json:"resourceId"`
}

var (
	workloadIdentityBindings sim.Store[workloadIdentityBinding]
	workloadIdentityByHeader sim.GenerationIndex[workloadIdentityBinding]
)

func registerWorkloadIdentity(srv *sim.Server) {
	workloadIdentityBindings = sim.MakeStore[workloadIdentityBinding](srv.DB(), "workload_identity_headers")
	srv.HandleFunc("GET /msi/token", handleAppServiceIdentityToken)
}

// workloadIdentityHeader returns the IDENTITY_HEADER secret of the app or slot
// resourceID, issuing one the first time the app starts with an identity.
func workloadIdentityHeader(resourceID string) (string, error) {
	key := strings.ToLower(resourceID)
	if b, ok := workloadIdentityBindings.Get(key); ok {
		return b.Header, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("issue the identity header: %w", err)
	}
	b := workloadIdentityBinding{Header: hex.EncodeToString(raw), ResourceID: resourceID}
	workloadIdentityBindings.Put(key, b)
	return b.Header, nil
}

func workloadIdentityBindingFor(header string) (workloadIdentityBinding, bool) {
	if workloadIdentityBindings == nil || header == "" {
		return workloadIdentityBinding{}, false
	}
	return workloadIdentityByHeader.Lookup(workloadIdentityBindings, header, func(b workloadIdentityBinding) []string {
		return []string{b.Header}
	})
}

// appServiceIdentityError writes the endpoint's error body.
func appServiceIdentityError(w http.ResponseWriter, status int, message string) {
	sim.WriteJSON(w, status, map[string]any{
		"statusCode":    status,
		"message":       message,
		"correlationId": sim.NewUUID(),
	})
}

// appServiceIdentity is the identity a token request names among the app's.
type appServiceIdentity struct {
	principalID, clientID, resourceID string
}

// selectAppServiceIdentity resolves the identity a request names: the
// user-assigned identity its client_id, principal_id, object_id or mi_res_id
// selects, or the system-assigned identity when it names none. It reports
// false when the app has no such identity.
func selectAppServiceIdentity(site Site, r *http.Request) (appServiceIdentity, bool) {
	q := r.URL.Query()
	clientID := strings.TrimSpace(q.Get("client_id"))
	principalID := strings.TrimSpace(q.Get("principal_id"))
	if principalID == "" {
		principalID = strings.TrimSpace(q.Get("object_id"))
	}
	miResID := strings.TrimSpace(q.Get("mi_res_id"))
	if clientID == "" && principalID == "" && miResID == "" {
		if !site.Identity.systemAssigned() || site.Identity.PrincipalID == "" {
			return appServiceIdentity{}, false
		}
		sp, ok := entraServicePrincipalStore.Get(site.Identity.PrincipalID)
		if !ok {
			return appServiceIdentity{}, false
		}
		return appServiceIdentity{principalID: site.Identity.PrincipalID, clientID: sp.AppID, resourceID: site.ID}, true
	}
	if !site.Identity.userAssigned() {
		return appServiceIdentity{}, false
	}
	for rid, uai := range site.Identity.UserAssignedIdentities {
		if uai == nil {
			continue
		}
		switch {
		case clientID != "" && strings.EqualFold(uai.ClientID, clientID),
			principalID != "" && strings.EqualFold(uai.PrincipalID, principalID),
			miResID != "" && strings.EqualFold(rid, miResID):
			return appServiceIdentity{principalID: uai.PrincipalID, clientID: uai.ClientID, resourceID: rid}, true
		}
	}
	return appServiceIdentity{}, false
}

func handleAppServiceIdentityToken(w http.ResponseWriter, r *http.Request) {
	binding, ok := workloadIdentityBindingFor(r.Header.Get("X-IDENTITY-HEADER"))
	if !ok {
		appServiceIdentityError(w, http.StatusUnauthorized,
			"The X-IDENTITY-HEADER header is missing or is not the IDENTITY_HEADER of an app with a managed identity.")
		return
	}
	if v := r.URL.Query().Get("api-version"); v != appServiceIdentityAPIVersion {
		appServiceIdentityError(w, http.StatusBadRequest,
			fmt.Sprintf("The api-version '%s' is not supported. The supported version is %s.", v, appServiceIdentityAPIVersion))
		return
	}
	resource := strings.TrimSpace(r.URL.Query().Get("resource"))
	if resource == "" {
		appServiceIdentityError(w, http.StatusBadRequest, "The resource parameter is required.")
		return
	}
	site, ok := webSiteRecord(binding.ResourceID)
	if !ok {
		appServiceIdentityError(w, http.StatusBadRequest, "Unable to load the proper Managed Identity.")
		return
	}
	identity, ok := selectAppServiceIdentity(site, r)
	if !ok {
		appServiceIdentityError(w, http.StatusBadRequest, "Unable to load the proper Managed Identity.")
		return
	}
	now := time.Now()
	expires := now.Add(24 * time.Hour)
	token, err := mintAzureSimSignedJWT(map[string]any{
		"aud":       resource,
		"iss":       fmt.Sprintf("https://sts.windows.net/%s/", simTenantID),
		"tid":       simTenantID,
		"oid":       identity.principalID,
		"sub":       identity.principalID,
		"appid":     identity.clientID,
		"idtyp":     "app",
		"xms_mirid": identity.resourceID,
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       expires.Unix(),
		"ver":       "1.0",
	})
	if err != nil {
		appServiceIdentityError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"expires_on":   fmt.Sprintf("%d", expires.Unix()),
		"resource":     resource,
		"token_type":   "Bearer",
		"client_id":    identity.clientID,
	})
}

// siteHasManagedIdentity reports whether an app or slot has an identity, which
// is when App Service sets IDENTITY_ENDPOINT and IDENTITY_HEADER for it.
func siteHasManagedIdentity(site *Site) bool {
	return site != nil && (site.Identity.systemAssigned() || site.Identity.userAssigned())
}
