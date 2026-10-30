package main

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// serveMetadataToken answers `service-accounts/{sa}/token` with an access
// token the bearer middleware verifies like any other the simulator mints.
// The token carries the account's access scopes, or — when the read sets
// `enforce_scopes` or the instance sets `enable-access-token-enforce-scopes`
// — the subset of them its `scopes` parameter names.
func serveMetadataToken(w http.ResponseWriter, r *http.Request, account string) {
	email, ok := metadataServiceAccountEmail(r, account)
	if !ok {
		metadataNotFound(w)
		return
	}
	scopes, problem := metadataTokenScopes(r)
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}
	now := time.Now()
	expires := now.Add(time.Hour)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": signScopedAccessToken(email, scopes, now, expires),
		"expires_in":   int(time.Until(expires).Seconds()),
		"token_type":   "Bearer",
	})
}

// metadataTokenScopes are the scopes a token read is granted, or the reason
// the metadata server refuses the read.
func metadataTokenScopes(r *http.Request) ([]string, string) {
	granted := metadataServiceAccountScopes(r)
	query := r.URL.Query()
	enforce := gceMetadataAttributeTrue(r, "enable-access-token-enforce-scopes")
	if query.Has("enforce_scopes") {
		switch strings.ToLower(query.Get("enforce_scopes")) {
		case "true", "1", "yes", "y":
			enforce = true
		default:
			return nil, "enforce_scopes must be true when it is set"
		}
	}
	requested := query.Get("scopes")
	if !enforce || requested == "" {
		return granted, ""
	}
	var scopes []string
	for _, scope := range strings.Split(requested, ",") {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !slices.Contains(granted, scope) {
			return nil, "scope " + scope + " is not among the scopes the instance's service account was given"
		}
		scopes = append(scopes, scope)
	}
	return scopes, ""
}

// serveMetadataIdentity answers `service-accounts/{sa}/identity` with an
// identity token for the `audience` the read names. A Compute Engine
// instance's `format=full` token carries the instance and its project in its
// `google.compute_engine` claim, and with `licenses=TRUE` the license codes of
// its boot image.
func serveMetadataIdentity(w http.ResponseWriter, r *http.Request, account string) {
	query := r.URL.Query()
	audience := query.Get("audience")
	if audience == "" {
		http.Error(w, "non-empty audience parameter required", http.StatusBadRequest)
		return
	}
	format := query.Get("format")
	switch format {
	case "", "standard", "full":
	default:
		http.Error(w, "format must be standard or full", http.StatusBadRequest)
		return
	}
	var licenses bool
	if raw := query.Get("licenses"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			http.Error(w, "licenses must be TRUE or FALSE", http.StatusBadRequest)
			return
		}
		licenses = parsed
	}
	email, ok := metadataServiceAccountEmail(r, account)
	if !ok {
		metadataNotFound(w)
		return
	}
	var extra map[string]any
	if inst, ok := gcpMetadataInstanceFor(r); ok && format == "full" {
		extra = map[string]any{"google": map[string]any{"compute_engine": gceIdentityInstanceClaims(r, inst, licenses)}}
	}
	now := time.Now()
	writeMetadataText(w, signIdentityTokenWith(email, audience, now, now.Add(time.Hour), extra))
}

// gceIdentityInstanceClaims is the `google.compute_engine` claim of a
// full-format identity token.
func gceIdentityInstanceClaims(r *http.Request, inst ComputeInstance, licenses bool) map[string]any {
	workload := gceMetadataWorkloadFor(r)
	claims := map[string]any{
		"project_id":    workload.projectID(),
		"zone":          lastSegment(inst.Zone),
		"instance_id":   inst.Id,
		"instance_name": inst.Name,
	}
	if number, ok := workload.projectNumber(); ok {
		if n, err := strconv.ParseInt(number, 10, 64); err == nil {
			claims["project_number"] = n
		}
	}
	if created, err := time.Parse(time.RFC3339, inst.CreationTimestamp); err == nil {
		claims["instance_creation_timestamp"] = created.Unix()
	}
	if licenses {
		claims["license_id"] = gceBootImageLicenseCodes(inst)
	}
	return claims
}

// gceBootImageLicenseCodes are the codes of the licences the instance's boot
// image carries, as the licences collection issued them.
func gceBootImageLicenseCodes(inst ComputeInstance) []string {
	codes := []string{}
	image, err := gcpInstanceBootImage(inst)
	if err != nil || gcpComputeLicenses == nil {
		return codes
	}
	parts := strings.Split(gcpComputeResourcePath(image.SelfLink), "/")
	if len(parts) != 5 {
		return codes
	}
	resource, ok := gcpImageResource(parts[1], parts[4])
	if !ok {
		return codes
	}
	var links []string
	switch held := resource["licenses"].(type) {
	case []string:
		links = held
	case []any:
		for _, link := range held {
			if path, ok := link.(string); ok {
				links = append(links, path)
			}
		}
	}
	for _, link := range links {
		licence, ok := gcpComputeLicenses.Get(gcpComputeResourcePath(link))
		if !ok {
			continue
		}
		if code, _ := licence["licenseCode"].(string); code != "" {
			codes = append(codes, code)
		}
	}
	return codes
}
