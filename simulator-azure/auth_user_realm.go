package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// userRealmPathPrefix is where Microsoft Entra serves home-realm discovery,
// GET /common/UserRealm/{username}?api-version=1.0. MSAL asks it before a
// resource-owner password grant, to learn whether the user's domain is one
// Entra manages (and takes the password itself) or one it federates.
const userRealmPathPrefix = "/common/userrealm/"

func isUserRealmPath(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), userRealmPathPrefix)
}

// handleAzureUserRealm answers home-realm discovery. A domain a directory
// user signs in under is Managed, the realm the simulator's Entra holds the
// passwords of; any other domain is Unknown to it, as to Entra.
func handleAzureUserRealm(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Path[len(userRealmPathPrefix):]
	if username == "" || strings.Contains(username, "/") {
		AzureError(w, "invalid_request", "The user realm request names no user.", http.StatusBadRequest)
		return
	}
	body := map[string]any{
		"ver":                 "1.0",
		"account_type":        "Unknown",
		"cloud_instance_name": "microsoftonline.com",
		"cloud_audience_urn":  "urn:federation:MicrosoftOnline",
	}
	if _, domain, ok := strings.Cut(username, "@"); ok && entraManagesDomain(domain) {
		body["account_type"] = "Managed"
		body["domain_name"] = strings.ToLower(domain)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// entraManagesDomain reports whether a directory user signs in under domain.
func entraManagesDomain(domain string) bool {
	inDomain := func(upn string) bool {
		_, d, ok := strings.Cut(upn, "@")
		return ok && strings.EqualFold(d, domain)
	}
	if inDomain(entraDefaultUser.PreferredUsername) {
		return true
	}
	return len(entraUsersStore.Filter(func(u EntraUser) bool { return inDomain(u.PreferredUsername) })) > 0
}
