package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

type Registry struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Location   string             `json:"location"`
	Sku        *RegistrySku       `json:"sku,omitempty"`
	Tags       map[string]string  `json:"tags,omitempty"`
	Properties RegistryProperties `json:"properties"`
}

type RegistrySku struct {
	Name string `json:"name"`
	Tier string `json:"tier,omitempty"`
}

type RegistryProperties struct {
	LoginServer              string          `json:"loginServer"`
	ProvisioningState        string          `json:"provisioningState"`
	AdminUserEnabled         bool            `json:"adminUserEnabled"`
	PublicNetworkAccess      string          `json:"publicNetworkAccess,omitempty"`
	NetworkRuleBypassOptions string          `json:"networkRuleBypassOptions,omitempty"`
	ZoneRedundancy           string          `json:"zoneRedundancy,omitempty"`
	AnonymousPullEnabled     *bool           `json:"anonymousPullEnabled,omitempty"`
	DataEndpointEnabled      *bool           `json:"dataEndpointEnabled,omitempty"`
	RoleAssignmentMode       string          `json:"roleAssignmentMode,omitempty"`
	Policies                 json.RawMessage `json:"policies,omitempty"`
	Encryption               json.RawMessage `json:"encryption,omitempty"`
}

// ACRCacheRule is a pull-through cache rule, the registry's cacheRules
// sub-resource.
type ACRCacheRule struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type,omitempty"`
	Properties ACRCacheRuleProperties `json:"properties"`
}

// SourceRepository is the upstream reference (`docker.io/library/alpine`);
// TargetRepository is the local path (`docker-hub/library/alpine`).
type ACRCacheRuleProperties struct {
	CredentialSetResourceID string `json:"credentialSetResourceId,omitempty"`
	SourceRepository        string `json:"sourceRepository,omitempty"`
	TargetRepository        string `json:"targetRepository,omitempty"`
	CreationDate            string `json:"creationDate,omitempty"`
	ProvisioningState       string `json:"provisioningState,omitempty"`
}

var acrRegistries sim.Store[Registry]

// A registry name is global in Azure Container Registry while the store keys
// registries by resource ID, so the name check reads this index.
var acrRegistriesByName sim.GenerationIndex[Registry]

// The vendored document's constraint on a checked name: 5 to 50 alphanumerics.
var acrNamePattern = regexp.MustCompile(`^[a-zA-Z0-9]{5,50}$`)

func acrRegistryNameKeys(reg Registry) []string {
	if reg.Name == "" {
		return nil
	}
	return []string{strings.ToLower(reg.Name)}
}

func registerACR(srv *sim.Server) {
	makeAzureKeyGens(srv)
	// A second buildSimulator in the same process re-collects its own child stores.
	acrMovableChildStores = nil
	registries := sim.MakeStore[Registry](srv.DB(), "acr_registries")
	acrRegistries = registries
	// Every /v2/ request authenticates against the registry its Host addresses
	// (acr_dataplane_auth.go).
	reg := &sim.OCIRegistry{
		Manifests: sim.MakeStore[sim.OCIManifest](srv.DB(), "acr_manifests"),
		Blobs:     sim.MakeStore[sim.OCIBlob](srv.DB(), "acr_blobs"),
		Uploads:   sim.MakeStore[sim.OCIUpload](srv.DB(), "acr_uploads"),
		Authorize: acrAuthorizeV2,
		Scope:     acrDataPlaneScope,
		// Azure Container Registry answers the authorized ping with the literal
		// two-byte body `{}` as `application/json; charset=utf-8`, as captured from
		// live registries and mcr.microsoft.com; JSON encoding would add a newline
		// and drop the charset.
		BaseResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		},
	}
	cacheRules := sim.MakeStore[ACRCacheRule](srv.DB(), "acr_cache_rules")
	acrCacheRules = cacheRules

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerRegistry"

	// azurerm v3 checks the name before creating a registry. The middleware
	// lowercases the camelCase action before dispatch.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/checknameavailability", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// A client checks precisely to avoid a conflict, so answer from the names
		// held.
		switch {
		case !acrNamePattern.MatchString(req.Name):
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"nameAvailable": false,
				"reason":        "Invalid",
				"message": "The registry name must be between 5 and 50 characters long " +
					"and use alphanumeric characters only.",
			})
		default:
			if _, taken := acrRegistriesByName.Lookup(
				acrRegistries, strings.ToLower(req.Name), acrRegistryNameKeys); taken {
				sim.WriteJSON(w, http.StatusOK, map[string]any{
					"nameAvailable": false,
					"reason":        "AlreadyExists",
					"message":       "The registry " + req.Name + " is already in use.",
				})
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"nameAvailable": true})
		}
	})

	srv.HandleFunc("PUT "+armBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")

		var req Registry
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", name)

		// A registry's name is its login server's host name, unique across every
		// subscription.
		if held, taken := acrRegistriesByName.Lookup(acrRegistries, strings.ToLower(name), acrRegistryNameKeys); taken && !strings.EqualFold(held.ID, resourceID) {
			AzureError(w, "AlreadyInUse", "The registry DNS name "+strings.ToLower(name)+".azurecr.io is already in use. "+
				"You can check if the name is already claimed using following api: "+
				"'https://docs.microsoft.com/en-us/rest/api/containerregistry/registries/checknameavailability'", http.StatusConflict)
			return
		}

		sku := req.Sku
		if sku == nil {
			sku = &RegistrySku{Name: "Basic"}
		}
		// Azure echoes sku.tier equal to sku.name when the client sends only the name.
		if sku.Tier == "" {
			sku.Tier = sku.Name
		}

		bypass := req.Properties.NetworkRuleBypassOptions
		if bypass == "" {
			bypass = "AzureServices"
		}
		publicNetworkAccess := req.Properties.PublicNetworkAccess
		if publicNetworkAccess == "" {
			publicNetworkAccess = "Enabled"
		}
		zoneRedundancy := req.Properties.ZoneRedundancy
		if zoneRedundancy == "" {
			zoneRedundancy = "Disabled"
		}
		// Microsoft.ContainerRegistry's default when the request does not opt into
		// repository-level Azure ABAC permissions.
		roleAssignmentMode := req.Properties.RoleAssignmentMode
		if roleAssignmentMode == "" {
			roleAssignmentMode = "LegacyRegistryPermissions"
		}

		reg := Registry{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.ContainerRegistry/registries",
			Location: req.Location,
			Sku:      sku,
			Tags:     req.Tags,
			Properties: RegistryProperties{
				LoginServer:              azureACRLoginServer(r, strings.ToLower(name)),
				ProvisioningState:        "Succeeded",
				AdminUserEnabled:         req.Properties.AdminUserEnabled,
				PublicNetworkAccess:      publicNetworkAccess,
				NetworkRuleBypassOptions: bypass,
				ZoneRedundancy:           zoneRedundancy,
				AnonymousPullEnabled:     req.Properties.AnonymousPullEnabled,
				DataEndpointEnabled:      req.Properties.DataEndpointEnabled,
				RoleAssignmentMode:       roleAssignmentMode,
				Policies:                 req.Properties.Policies,
				Encryption:               req.Properties.Encryption,
			},
		}

		registries.Put(resourceID, reg)

		// go-azure-sdk expects 200 for a synchronous create.
		sim.WriteJSON(w, http.StatusOK, reg)
	})

	srv.HandleFunc("GET "+armBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")

		resourceID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", name)

		reg, ok := registries.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, reg)
	})

	srv.HandleFunc("GET "+armBase+"/registries", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.ContainerRegistry", "registries") + "/"
		writeACRRegistryList(w, r, registries, func(reg Registry) bool {
			return strings.HasPrefix(reg.ID, prefix)
		})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/registries", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/resourceGroups/"
		writeACRRegistryList(w, r, registries, func(reg Registry) bool {
			return strings.HasPrefix(reg.ID, prefix)
		})
	})

	// The middleware lowercases the action verb before dispatch.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/listcredentials", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")
		resourceID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", name)
		reg, ok := registries.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		if !reg.Properties.AdminUserEnabled {
			AzureError(w, "BadRequest",
				"The admin user is not enabled for the registry. Enable it before requesting credentials.",
				http.StatusBadRequest)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrAdminCredentialsBody(resourceID, name))
	})

	srv.HandleFunc("DELETE "+armBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")

		resourceID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", name)

		if registries.Delete(resourceID) {
			acrDropCredentialKeyGens(resourceID)
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// CacheRulesClient's BeginCreate accepts 200 and BeginDelete 204, so both
	// complete synchronously.

	srv.HandleFunc("PUT "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")

			regID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", regName)
			if _, ok := registries.Get(regID); !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Registry '%s' under resource group '%s' was not found.", regName, rg)
				return
			}

			var req ACRCacheRule
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent",
					"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if req.Properties.SourceRepository == "" || req.Properties.TargetRepository == "" {
				AzureError(w, "InvalidRequestContent",
					"properties.sourceRepository and properties.targetRepository are required",
					http.StatusBadRequest)
				return
			}

			ruleID := fmt.Sprintf("%s/cacheRules/%s", regID, ruleName)
			rule := ACRCacheRule{
				ID:   ruleID,
				Name: ruleName,
				Type: "Microsoft.ContainerRegistry/registries/cacheRules",
				Properties: ACRCacheRuleProperties{
					CredentialSetResourceID: req.Properties.CredentialSetResourceID,
					SourceRepository:        req.Properties.SourceRepository,
					TargetRepository:        req.Properties.TargetRepository,
					CreationDate:            req.Properties.CreationDate,
					ProvisioningState:       "Succeeded",
				},
			}
			cacheRules.Put(ruleID, rule)

			sim.WriteJSON(w, http.StatusOK, rule)
		})

	srv.HandleFunc("GET "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")

			ruleID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", regName, "cacheRules", ruleName)
			rule, ok := cacheRules.Get(ruleID)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Cache rule '%s' under registry '%s' was not found.", ruleName, regName)
				return
			}
			sim.WriteJSON(w, http.StatusOK, rule)
		})

	srv.HandleFunc("GET "+armBase+"/registries/{registryName}/cacheRules",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")

			regPrefix := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", regName, "cacheRules") + "/"
			matched := cacheRules.Filter(func(cr ACRCacheRule) bool {
				return strings.HasPrefix(cr.ID, regPrefix)
			})
			if matched == nil {
				matched = []ACRCacheRule{}
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"value": matched,
			})
		})

	srv.HandleFunc("DELETE "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")

			ruleID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", regName, "cacheRules", ruleName)
			cacheRules.Delete(ruleID)
			w.WriteHeader(http.StatusNoContent)
		})

	// Only credentialSetResourceId is mutable.
	srv.HandleFunc("PATCH "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")
			ruleID := armResourceID(sub, rg, "Microsoft.ContainerRegistry", "registries", regName, "cacheRules", ruleName)
			if _, ok := cacheRules.Get(ruleID); !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Cache rule '%s' under registry '%s' was not found.", ruleName, regName)
				return
			}
			var req ACRCacheRule
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent",
					"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			cacheRules.Update(ruleID, func(cr *ACRCacheRule) {
				if req.Properties.CredentialSetResourceID != "" {
					cr.Properties.CredentialSetResourceID = req.Properties.CredentialSetResourceID
				}
				cr.Properties.ProvisioningState = "Succeeded"
			})
			rule, _ := cacheRules.Get(ruleID)
			sim.WriteJSON(w, http.StatusOK, rule)
		})

	registerACRRegistryActions(srv, registries)
	registerACRChildResources(srv)
	registerACRWebhooks(srv)

	reg.Register(srv)

	// Azure Container Registry serves the catalog on both `/v2/_catalog` and
	// `/acr/v1/_catalog`, with the same `registry:catalog:*` access and paging.
	catalog := func(path string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !acrAuthorize(w, r, acrRegistryCatalogResource(acrActionAll)) {
				return
			}
			page, last := acrCatalogPage(r, acrRepositoriesOf(reg, acrDataPlaneScope(r)))
			if page == nil {
				page = []string{}
			}
			if last != "" {
				q := r.URL.Query()
				q.Set("last", last)
				w.Header().Set("Link", fmt.Sprintf("<%s?%s>; rel=\"next\"", path, q.Encode()))
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"repositories": page,
			})
		}
	}
	srv.HandleFunc("GET /v2/_catalog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		catalog("/v2/_catalog")(w, r)
	})
	srv.HandleFunc("GET /acr/v1/_catalog", catalog("/acr/v1/_catalog"))

	// A repository name can contain slashes, hence {path...}.
	srv.HandleFunc("GET /acr/v1/{path...}", func(w http.ResponseWriter, r *http.Request) {
		target, ok := acrParsePropertiesPath(sim.PathParam(r, "path"))
		if !ok {
			acrPropertiesNotFound(w, "the path does not address a repository, manifest or tag")
			return
		}
		// The repository, manifest and tag reads sharing this path are served by
		// acrReadProperties.
		if target.kind != "tags" {
			acrReadProperties(w, r, reg, target)
			return
		}
		repoName := target.repo
		// metadata_read is the action Azure Container Registry names in the challenge
		// for this API.
		if !acrAuthorize(w, r, acrRepositoryResource(repoName, acrActionMetadataRead, acrActionMetadataRead)) {
			return
		}
		scope := acrDataPlaneScope(r)
		tags := reg.Manifests.Filter(func(m sim.OCIManifest) bool {
			return m.Scope == scope && m.Repo == repoName && m.Ref != "" && !strings.HasPrefix(m.Ref, "sha256:")
		})
		sort.Slice(tags, func(i, j int) bool { return tags[i].Ref < tags[j].Ref })
		tagList := make([]map[string]any, 0, len(tags))
		for _, m := range tags {
			tagList = append(tagList, map[string]any{
				"name":                 m.Ref,
				"digest":               m.Digest,
				"createdTime":          acrStamp(m.Pushed),
				"lastUpdateTime":       acrStamp(m.Pushed),
				"signed":               false,
				"changeableAttributes": acrAttributesDoc(acrAttributesFor(acrAttributeKey(scope, repoName, ":"+m.Ref))),
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"registry":  acrRegistryName(r),
			"imageName": repoName,
			"tags":      tagList,
		})
	})

	registerACRDataPlaneProperties(srv, reg)
	registerACROAuth2(srv)
}

// registerACROAuth2 mounts a registry's token service, the realm of the Docker
// Registry v2 Bearer challenge:
//
//   - Docker-protocol clients present the admin credential as HTTP Basic on
//     `GET /oauth2/token?service=<registry>&scope=<scope>` (Azure/acr
//     `docs/Token-BasicAuth.md`);
//   - a Microsoft Entra client exchanges its access token for a refresh token at
//     `POST /oauth2/exchange`, then trades it for an access token at
//     `POST /oauth2/token` (Azure/acr `docs/AAD-OAuth.md`).
//
// The access token carries only the scopes the credential authorizes, because
// the data plane enforces its `access` claim.
func registerACROAuth2(srv *sim.Server) {
	srv.HandleFunc("POST /oauth2/exchange", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			acrOAuthError(w, "invalid_request", "failed to parse request body: "+err.Error())
			return
		}
		reg, ok := acrTokenServiceRegistry(w, r)
		if !ok {
			return
		}
		subject, ok := acrExchangeIdentity(w, r)
		if !ok {
			return
		}
		refreshToken, err := acrMintRefreshToken(reg, subject)
		if err != nil {
			AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"refresh_token": refreshToken})
	})

	srv.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			acrOAuthError(w, "invalid_request", "failed to parse request body: "+err.Error())
			return
		}
		reg, ok := acrTokenServiceRegistry(w, r)
		if !ok {
			return
		}
		var identity acrTokenIdentity
		switch grant := r.PostFormValue("grant_type"); grant {
		case "refresh_token":
			refreshToken := r.PostFormValue("refresh_token")
			if refreshToken == "" {
				// The Azure SDK for Go asks for an anonymous token with the password grant and
				// an empty refresh token; under the refresh-token grant an empty one is
				// malformed.
				acrOAuthError(w, "invalid_request", "refresh_token is required")
				return
			}
			verified, err := acrVerifyRefreshToken(refreshToken, reg)
			if err != nil {
				acrOAuthUnauthorized(w, fmt.Sprintf("the refresh token is not valid: %v", err))
				return
			}
			identity = acrTokenIdentity{subject: verified.Subject, owner: true}
		case "password":
			authenticated, ok := acrPasswordGrantIdentity(w, r, reg)
			if !ok {
				return
			}
			identity = authenticated
		default:
			acrOAuthError(w, "unsupported_grant_type",
				fmt.Sprintf("grant_type %q is not supported; use refresh_token or password", grant))
			return
		}
		acrWriteAccessToken(w, reg, identity, r.Form["scope"])
	})

	srv.HandleFunc("GET /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		reg, ok := acrTokenServiceRegistry(w, r)
		if !ok {
			return
		}
		identity, ok := acrPasswordGrantIdentity(w, r, reg)
		if !ok {
			return
		}
		acrWriteAccessToken(w, reg, identity, r.URL.Query()["scope"])
	})
}

// acrTokenServiceRegistry resolves the registry from the Host, its login
// server; the challenge's `service` parameter must name the same registry.
func acrTokenServiceRegistry(w http.ResponseWriter, r *http.Request) (Registry, bool) {
	reg, ok := acrRegistryForHost(r.Host)
	if !ok {
		acrHostNotARegistry(w, r)
		return Registry{}, false
	}
	service := r.FormValue("service")
	if service != "" && !strings.EqualFold(acrBareHost(service), acrBareHost(acrLoginServer(reg))) {
		acrOAuthError(w, "invalid_request",
			fmt.Sprintf("service %q does not name the registry at %q", service, r.Host))
		return Registry{}, false
	}
	return reg, true
}

// acrExchangeIdentity verifies the Microsoft Entra credential an
// /oauth2/exchange request carries. The access-token grants require the access
// token, the refresh-token grant the refresh token.
func acrExchangeIdentity(w http.ResponseWriter, r *http.Request) (string, bool) {
	grant := r.PostFormValue("grant_type")
	switch grant {
	case "access_token", "access_token_refresh_token":
		accessToken := r.PostFormValue("access_token")
		if accessToken == "" {
			acrOAuthError(w, "invalid_request", "access_token is required")
			return "", false
		}
		claims, err := verifyAzureSimJWT(accessToken)
		if err != nil {
			acrOAuthUnauthorized(w, fmt.Sprintf("the access token is not valid: %v", err))
			return "", false
		}
		if audience := azureTokenAudience(claims); !acrEntraAudienceValid(audience) {
			acrOAuthUnauthorized(w, fmt.Sprintf(
				"the access token was issued for audience %q, which is not the Azure Container Registry audience (%s). Acquire the token with scope %s/.default.",
				audience, acrEntraAudience, acrEntraAudience))
			return "", false
		}
		return acrTokenSubject(claims), true
	case "refresh_token":
		refreshToken := r.PostFormValue("refresh_token")
		if refreshToken == "" {
			acrOAuthError(w, "invalid_request", "refresh_token is required")
			return "", false
		}
		stored, ok := lookupAzureRefreshToken(refreshToken)
		if !ok {
			acrOAuthUnauthorized(w, "the refresh token is not valid")
			return "", false
		}
		return stored.UserOID, true
	default:
		acrOAuthError(w, "unsupported_grant_type",
			fmt.Sprintf("grant_type %q is not supported; use access_token, access_token_refresh_token or refresh_token", grant))
		return "", false
	}
}

// acrEntraAudienceValid accepts either spelling of the container-registry
// audience Microsoft Entra mints.
func acrEntraAudienceValid(audience string) bool {
	return audience == acrEntraAudience || audience == acrEntraAudience+"/"
}

func acrTokenSubject(claims map[string]any) string {
	for _, claim := range []string{"preferred_username", "upn", "oid", "sub"} {
		if value, _ := claims[claim].(string); value != "" {
			return value
		}
	}
	return ""
}

// acrTokenIdentity: owner marks the registry's admin or an authenticated
// Microsoft Entra principal, granted every scope it asks for, as opposed to the
// anonymous caller of a registry with anonymous pull. credential and slot
// fingerprint the admin password, so regenerating it invalidates its tokens.
type acrTokenIdentity struct {
	subject    string
	credential string
	slot       string
	owner      bool
}

// acrPasswordGrantIdentity authenticates an HTTP Basic admin credential or,
// without one, the anonymous identity of a registry with anonymous pull. It
// writes the refusal and reports false when neither applies.
func acrPasswordGrantIdentity(w http.ResponseWriter, r *http.Request, reg Registry) (acrTokenIdentity, bool) {
	scheme, basic := sim.ParseAuthorization(r.Header.Get("Authorization"))
	if !strings.EqualFold(scheme, "Basic") || basic == "" {
		if acrAnonymousPullEnabled(reg) {
			return acrTokenIdentity{subject: "anonymous"}, true
		}
		acrOAuthUnauthorized(w, "authentication required")
		return acrTokenIdentity{}, false
	}
	username, password, decoded := sim.BasicCredential(basic)
	if !decoded {
		acrOAuthUnauthorized(w, "the Basic credential is malformed")
		return acrTokenIdentity{}, false
	}
	matched, valid := acrAdminCredentialSlot(reg, username, password)
	if !valid {
		acrOAuthUnauthorized(w, "the credential is not valid for this registry")
		return acrTokenIdentity{}, false
	}
	return acrTokenIdentity{
		subject:    username,
		credential: acrCredentialFingerprint(reg.ID, matched),
		slot:       matched,
		owner:      true,
	}, true
}

func acrWriteAccessToken(w http.ResponseWriter, reg Registry, identity acrTokenIdentity, scopes []string) {
	granted := acrGrantScopes(sim.ParseRegistryScopes(scopes), identity.owner)
	accessToken, err := acrMintAccessToken(reg, identity.subject, granted, identity.credential, identity.slot)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"access_token": accessToken})
}

func writeACRRegistryList(w http.ResponseWriter, r *http.Request, registries sim.Store[Registry], keep func(Registry) bool) {
	matched := registries.Filter(keep)
	page, next, pageOK := armPage(w, r, matched, func(reg Registry) string { return reg.ID })
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func acrOAuthError(w http.ResponseWriter, code, msg string) {
	sim.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error":             code,
		"error_description": msg,
	})
}

const acrARMBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerRegistry"

// acrSubResource is a generic registry child resource. It emits only the
// fields the matching Swagger schema declares: location and tags for tracked
// resources, identity where the schema allows it.
type acrSubResource struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Type       string            `json:"type,omitempty"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   json.RawMessage   `json:"identity,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

// acrChildKind's allow* flags gate the top-level fields the child's Swagger
// schema declares; patch enables PATCH, which some children lack.
type acrChildKind struct {
	seg           string // path segment, e.g. "replications"
	nameParam     string // route param name, e.g. "replicationName"
	typeName      string // ARM resource type, e.g. "Microsoft.ContainerRegistry/registries/replications"
	allowLocation bool
	allowTags     bool
	allowIdentity bool
	patch         bool
	store         sim.Store[acrSubResource]
	// afterWrite runs once a create, update or delete has been stored.
	afterWrite func()
}

func acrRegistryID(r *http.Request) (string, bool) {
	regID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.ContainerRegistry", "registries", sim.PathParam(r, "registryName"))
	_, ok := acrRegistries.Get(regID)
	return regID, ok
}

func (k acrChildKind) resourceID(r *http.Request) string {
	regID, _ := acrRegistryID(r)
	return fmt.Sprintf("%s/%s/%s", regID, k.seg, sim.PathParam(r, k.nameParam))
}

// registerACRChild also enrols the kind's store in the set a
// cross-resource-group move re-keys.
func registerACRChild(srv *sim.Server, k acrChildKind) {
	acrMovableChildStores = append(acrMovableChildStores, k.store)
	base := acrARMBase + "/registries/{registryName}/" + k.seg
	srv.HandleFunc("PUT "+base+"/{"+k.nameParam+"}", k.handlePut)
	srv.HandleFunc("GET "+base+"/{"+k.nameParam+"}", k.handleGet)
	srv.HandleFunc("GET "+base, k.handleList)
	if k.patch {
		srv.HandleFunc("PATCH "+base+"/{"+k.nameParam+"}", k.handlePatch)
	}
	srv.HandleFunc("DELETE "+base+"/{"+k.nameParam+"}", k.handleDelete)
}

func (k acrChildKind) handlePut(w http.ResponseWriter, r *http.Request) {
	regID, ok := acrRegistryID(r)
	if !ok {
		acrRegistryNotFound(w, r)
		return
	}
	name := sim.PathParam(r, k.nameParam)
	var req acrSubResource
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	resID := fmt.Sprintf("%s/%s/%s", regID, k.seg, name)
	res := acrSubResource{ID: resID, Name: name, Type: k.typeName}
	if k.allowLocation {
		res.Location = req.Location
	}
	if k.allowTags {
		res.Tags = req.Tags
	}
	if k.allowIdentity {
		res.Identity = req.Identity
	}
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	props["provisioningState"] = "Succeeded"
	res.Properties = props
	k.store.Put(resID, res)
	k.wrote()
	sim.WriteJSON(w, http.StatusOK, res)
}

func (k acrChildKind) wrote() {
	if k.afterWrite != nil {
		k.afterWrite()
	}
}

func (k acrChildKind) handleGet(w http.ResponseWriter, r *http.Request) {
	res, ok := k.store.Get(k.resourceID(r))
	if !ok {
		acrChildNotFound(w, k, r)
		return
	}
	sim.WriteJSON(w, http.StatusOK, res)
}

func (k acrChildKind) handleList(w http.ResponseWriter, r *http.Request) {
	regID, ok := acrRegistryID(r)
	if !ok {
		acrRegistryNotFound(w, r)
		return
	}
	prefix := regID + "/" + k.seg + "/"
	matched := k.store.Filter(func(s acrSubResource) bool {
		return strings.HasPrefix(s.ID, prefix)
	})
	if matched == nil {
		matched = []acrSubResource{}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": matched})
}

func (k acrChildKind) handlePatch(w http.ResponseWriter, r *http.Request) {
	resID := k.resourceID(r)
	if _, ok := k.store.Get(resID); !ok {
		acrChildNotFound(w, k, r)
		return
	}
	var req acrSubResource
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	k.store.Update(resID, func(s *acrSubResource) {
		if k.allowLocation && req.Location != "" {
			s.Location = req.Location
		}
		if k.allowTags && req.Tags != nil {
			s.Tags = req.Tags
		}
		if k.allowIdentity && req.Identity != nil {
			s.Identity = req.Identity
		}
		if req.Properties != nil {
			if s.Properties == nil {
				s.Properties = map[string]any{}
			}
			for key, v := range req.Properties {
				s.Properties[key] = v
			}
			s.Properties["provisioningState"] = "Succeeded"
		}
	})
	k.wrote()
	res, _ := k.store.Get(resID)
	sim.WriteJSON(w, http.StatusOK, res)
}

func (k acrChildKind) handleDelete(w http.ResponseWriter, r *http.Request) {
	if k.store.Delete(k.resourceID(r)) {
		k.wrote()
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// acrAdminCredentialsBody derives each admin password slot ("password",
// "password2") from the registry ID and that slot's rotation generation, so a
// regenerated slot reads back new while the other is unchanged.
func acrAdminCredentialsBody(registryID, registryName string) map[string]any {
	return map[string]any{
		"username": registryName,
		"passwords": []map[string]string{
			{"name": "password", "value": azureKeyMaterial32(registryID, "password")},
			{"name": "password2", "value": azureKeyMaterial32(registryID, "password2")},
		},
	}
}

// acrDropCredentialKeyGens lets a later registry of the same name start from
// fresh passwords.
func acrDropCredentialKeyGens(registryID string) {
	azureDropKeyGens(registryID, "password", "password2")
}

func acrRegistryNotFound(w http.ResponseWriter, r *http.Request) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.",
		sim.PathParam(r, "registryName"), sim.PathParam(r, "resourceGroupName"))
}

func acrChildNotFound(w http.ResponseWriter, k acrChildKind, r *http.Request) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource '%s/%s' under registry '%s' was not found.",
		k.typeName, sim.PathParam(r, k.nameParam), sim.PathParam(r, "registryName"))
}

func registerACRChildResources(srv *sim.Server) {
	const t = "Microsoft.ContainerRegistry/registries/"
	registerACRChild(srv, acrChildKind{
		seg: "replications", nameParam: "replicationName", typeName: t + "replications",
		allowLocation: true, allowTags: true, patch: true,
		store: sim.MakeStore[acrSubResource](srv.DB(), "acr_replications"),
	})
	registerACRChild(srv, acrChildKind{
		seg: "scopeMaps", nameParam: "scopeMapName", typeName: t + "scopeMaps", patch: true,
		store: sim.MakeStore[acrSubResource](srv.DB(), "acr_scope_maps"),
	})
	registerACRChild(srv, acrChildKind{
		seg: "tokens", nameParam: "tokenName", typeName: t + "tokens", patch: true,
		store: sim.MakeStore[acrSubResource](srv.DB(), "acr_tokens"),
	})
	registerACRChild(srv, acrChildKind{
		seg: "credentialSets", nameParam: "credentialSetName", typeName: t + "credentialSets",
		allowIdentity: true, patch: true,
		store: sim.MakeStore[acrSubResource](srv.DB(), "acr_credential_sets"),
	})
	connectedRegistries := acrChildKind{
		seg: "connectedRegistries", nameParam: "connectedRegistryName", typeName: t + "connectedRegistries", patch: true,
		store: sim.MakeStore[acrSubResource](srv.DB(), "acr_connected_registries"),
	}
	registerACRChild(srv, connectedRegistries)
	registerACRChild(srv, acrChildKind{
		seg: "privateEndpointConnections", nameParam: "privateEndpointConnectionName",
		typeName: t + "privateEndpointConnections", patch: false,
		store: sim.MakeStore[acrSubResource](srv.DB(), "acr_pe_connections"),
	})

	// Deactivate the connected registry for real: the read that follows reports
	// it.
	srv.HandleFunc("POST "+acrARMBase+"/registries/{registryName}/connectedRegistries/{connectedRegistryName}/deactivate",
		func(w http.ResponseWriter, r *http.Request) {
			if _, ok := acrRegistryID(r); !ok {
				acrRegistryNotFound(w, r)
				return
			}
			key := connectedRegistries.resourceID(r)
			updated := connectedRegistries.store.Update(key, func(child *acrSubResource) {
				if child.Properties == nil {
					child.Properties = map[string]any{}
				}
				child.Properties["activation"] = map[string]any{"status": "Inactive"}
				child.Properties["connectionState"] = "Offline"
			})
			if !updated {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The connected registry %q was not found.",
					sim.PathParam(r, "connectedRegistryName"))
				return
			}
			w.WriteHeader(http.StatusOK)
		})
}

// The create parameters carry write-only secrets (serviceUri, customHeaders)
// the Webhook schema does not declare: keep them for getCallbackConfig and
// emit only WebhookProperties.
type acrWebhookStored struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	Location          string            `json:"location"`
	Tags              map[string]string `json:"tags,omitempty"`
	Status            string            `json:"status,omitempty"`
	Scope             string            `json:"scope,omitempty"`
	Actions           []string          `json:"actions"`
	ProvisioningState string            `json:"provisioningState"`
	ServiceURI        string            `json:"serviceUri,omitempty"`
	CustomHeaders     map[string]string `json:"customHeaders,omitempty"`
}

// response renders the wire shape of a webhook (WebhookProperties only — the
// secret serviceUri/customHeaders are never returned).
func (wh acrWebhookStored) response() map[string]any {
	props := map[string]any{
		"actions":           wh.actionsOrEmpty(),
		"provisioningState": wh.ProvisioningState,
	}
	if wh.Status != "" {
		props["status"] = wh.Status
	}
	if wh.Scope != "" {
		props["scope"] = wh.Scope
	}
	out := map[string]any{
		"id":         wh.ID,
		"name":       wh.Name,
		"type":       wh.Type,
		"location":   wh.Location,
		"properties": props,
	}
	if len(wh.Tags) > 0 {
		out["tags"] = wh.Tags
	}
	return out
}

func (wh acrWebhookStored) actionsOrEmpty() []string {
	if wh.Actions == nil {
		return []string{}
	}
	return wh.Actions
}

type acrWebhookCreateParams struct {
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		ServiceURI    string            `json:"serviceUri"`
		CustomHeaders map[string]string `json:"customHeaders"`
		Status        string            `json:"status"`
		Scope         string            `json:"scope"`
		Actions       []string          `json:"actions"`
	} `json:"properties"`
}

func registerACRWebhooks(srv *sim.Server) {
	webhooks := sim.MakeStore[acrWebhookStored](srv.DB(), "acr_webhooks")
	acrWebhooks = webhooks
	const typeName = "Microsoft.ContainerRegistry/registries/webhooks"
	base := acrARMBase + "/registries/{registryName}/webhooks"

	webhookID := func(r *http.Request) (string, bool) {
		regID, ok := acrRegistryID(r)
		return regID + "/webhooks/" + sim.PathParam(r, "webhookName"), ok
	}

	srv.HandleFunc("PUT "+base+"/{webhookName}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := webhookID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		var req acrWebhookCreateParams
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		wh := acrWebhookStored{
			ID: id, Name: sim.PathParam(r, "webhookName"), Type: typeName,
			Location: req.Location, Tags: req.Tags,
			Status: req.Properties.Status, Scope: req.Properties.Scope, Actions: req.Properties.Actions,
			ProvisioningState: "Succeeded",
			ServiceURI:        req.Properties.ServiceURI, CustomHeaders: req.Properties.CustomHeaders,
		}
		webhooks.Put(id, wh)
		sim.WriteJSON(w, http.StatusOK, wh.response())
	})

	srv.HandleFunc("GET "+base+"/{webhookName}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := webhookID(r)
		wh, ok := webhooks.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Webhook '%s' under registry '%s' was not found.", sim.PathParam(r, "webhookName"), sim.PathParam(r, "registryName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, wh.response())
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		prefix := regID + "/webhooks/"
		matched := webhooks.Filter(func(wh acrWebhookStored) bool { return strings.HasPrefix(wh.ID, prefix) })
		sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
		out := make([]map[string]any, 0, len(matched))
		for _, wh := range matched {
			out = append(out, wh.response())
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	srv.HandleFunc("PATCH "+base+"/{webhookName}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := webhookID(r)
		if _, ok := webhooks.Get(id); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Webhook '%s' under registry '%s' was not found.", sim.PathParam(r, "webhookName"), sim.PathParam(r, "registryName"))
			return
		}
		var req acrWebhookCreateParams
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		webhooks.Update(id, func(wh *acrWebhookStored) {
			if req.Tags != nil {
				wh.Tags = req.Tags
			}
			if req.Properties.ServiceURI != "" {
				wh.ServiceURI = req.Properties.ServiceURI
			}
			if req.Properties.CustomHeaders != nil {
				wh.CustomHeaders = req.Properties.CustomHeaders
			}
			if req.Properties.Status != "" {
				wh.Status = req.Properties.Status
			}
			if req.Properties.Scope != "" {
				wh.Scope = req.Properties.Scope
			}
			if req.Properties.Actions != nil {
				wh.Actions = req.Properties.Actions
			}
			wh.ProvisioningState = "Succeeded"
		})
		wh, _ := webhooks.Get(id)
		sim.WriteJSON(w, http.StatusOK, wh.response())
	})

	srv.HandleFunc("DELETE "+base+"/{webhookName}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := webhookID(r)
		if webhooks.Delete(id) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// Ping answers an EventInfo id and delivers nothing.
	srv.HandleFunc("POST "+base+"/{webhookName}/ping", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"id": sim.NewUUID()})
	})

	srv.HandleFunc("POST "+base+"/{webhookName}/getCallbackConfig", func(w http.ResponseWriter, r *http.Request) {
		id, _ := webhookID(r)
		wh, ok := webhooks.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Webhook '%s' under registry '%s' was not found.", sim.PathParam(r, "webhookName"), sim.PathParam(r, "registryName"))
			return
		}
		out := map[string]any{"serviceUri": wh.ServiceURI}
		if len(wh.CustomHeaders) > 0 {
			out["customHeaders"] = wh.CustomHeaders
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// Answers an empty list: the simulator records no webhook deliveries.
	srv.HandleFunc("POST "+base+"/{webhookName}/listEvents", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})
}

func registerACRRegistryActions(srv *sim.Server, registries sim.Store[Registry]) {
	srv.HandleFunc("GET /providers/Microsoft.ContainerRegistry/operations", func(w http.ResponseWriter, r *http.Request) {
		op := func(name, resource, operation, desc string) map[string]any {
			return map[string]any{
				"name": name,
				"display": map[string]any{
					"provider": "Microsoft Container Registry", "resource": resource,
					"operation": operation, "description": desc,
				},
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
			op("Microsoft.ContainerRegistry/registries/read", "Registries", "Get Registry", "Gets the properties of the specified container registry."),
			op("Microsoft.ContainerRegistry/registries/write", "Registries", "Update Registry", "Creates or updates a container registry with the specified parameters."),
			op("Microsoft.ContainerRegistry/registries/delete", "Registries", "Delete Registry", "Deletes the specified container registry."),
			op("Microsoft.ContainerRegistry/registries/push/write", "Registries", "Push/Pull Registry", "Pushes images to the specified container registry."),
		}})
	})

	srv.HandleFunc("PATCH "+acrARMBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		var req struct {
			Tags       map[string]string `json:"tags"`
			SKU        *RegistrySku      `json:"sku"`
			Properties *struct {
				AdminUserEnabled         *bool  `json:"adminUserEnabled"`
				PublicNetworkAccess      string `json:"publicNetworkAccess"`
				NetworkRuleBypassOptions string `json:"networkRuleBypassOptions"`
				AnonymousPullEnabled     *bool  `json:"anonymousPullEnabled"`
				DataEndpointEnabled      *bool  `json:"dataEndpointEnabled"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		registries.Update(regID, func(reg *Registry) {
			if req.Tags != nil {
				reg.Tags = req.Tags
			}
			if req.SKU != nil {
				if req.SKU.Tier == "" {
					req.SKU.Tier = req.SKU.Name
				}
				reg.Sku = req.SKU
			}
			if req.Properties != nil {
				if req.Properties.AdminUserEnabled != nil {
					reg.Properties.AdminUserEnabled = *req.Properties.AdminUserEnabled
				}
				if req.Properties.PublicNetworkAccess != "" {
					reg.Properties.PublicNetworkAccess = req.Properties.PublicNetworkAccess
				}
				if req.Properties.NetworkRuleBypassOptions != "" {
					reg.Properties.NetworkRuleBypassOptions = req.Properties.NetworkRuleBypassOptions
				}
				if req.Properties.AnonymousPullEnabled != nil {
					reg.Properties.AnonymousPullEnabled = req.Properties.AnonymousPullEnabled
				}
				if req.Properties.DataEndpointEnabled != nil {
					reg.Properties.DataEndpointEnabled = req.Properties.DataEndpointEnabled
				}
			}
		})
		reg, _ := registries.Get(regID)
		sim.WriteJSON(w, http.StatusOK, reg)
	})

	// Answers fixed usage figures.
	srv.HandleFunc("GET "+acrARMBase+"/registries/{registryName}/listUsages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := acrRegistryID(r); !ok {
			acrRegistryNotFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
			{"name": "Size", "limit": 10737418240, "currentValue": 0, "unit": "Bytes"},
			{"name": "Webhooks", "limit": 100, "currentValue": 0, "unit": "Count"},
		}})
	})

	// Answers 200 and copies nothing.
	srv.HandleFunc("POST "+acrARMBase+"/registries/{registryName}/importImage", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := acrRegistryID(r); !ok {
			acrRegistryNotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("POST "+acrARMBase+"/registries/{registryName}/generateCredentials", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"username": sim.PathParam(r, "registryName"),
			"passwords": []map[string]string{
				{"name": "password1", "value": simListKey32(regID, "gen1")},
				{"name": "password2", "value": simListKey32(regID, "gen2")},
			},
		})
	})

	// RegenerateCredentialParameters names the slot (password | password2); a
	// subsequent listCredentials returns the new value.
	srv.HandleFunc("POST "+acrARMBase+"/registries/{registryName}/regenerateCredential", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name != "password" && req.Name != "password2" {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The value '%s' is not valid for parameter 'name'. Expected 'password' or 'password2'.", req.Name)
			return
		}
		azureBumpKeyGen(regID, req.Name, "")
		sim.WriteJSON(w, http.StatusOK, acrAdminCredentialsBody(regID, sim.PathParam(r, "registryName")))
	})

	// A registry exposes a single "registry" group.
	srv.HandleFunc("GET "+acrARMBase+"/registries/{registryName}/privateLinkResources", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{acrPrivateLinkResource(regID, "registry")}})
	})

	srv.HandleFunc("GET "+acrARMBase+"/registries/{registryName}/privateLinkResources/{groupName}", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrPrivateLinkResource(regID, sim.PathParam(r, "groupName")))
	})
}

func acrPrivateLinkResource(regID, group string) map[string]any {
	return map[string]any{
		"id":   regID + "/privateLinkResources/" + group,
		"name": group,
		"type": "Microsoft.ContainerRegistry/registries/privateLinkResources",
		"properties": map[string]any{
			"groupId":           group,
			"requiredMembers":   []string{"registry", "registry_data_" + group},
			"requiredZoneNames": []string{"privatelink.azurecr.io"},
		},
	}
}

// acrRepositoriesOf enumerates only manifests stored in the scope of the
// registry the Host addresses.
func acrRepositoriesOf(reg *sim.OCIRegistry, scope string) []string {
	seen := map[string]bool{}
	var repos []string
	for _, m := range reg.Manifests.List() {
		if m.Scope == scope && m.Repo != "" && !seen[m.Repo] {
			seen[m.Repo] = true
			repos = append(repos, m.Repo)
		}
	}
	return repos
}
