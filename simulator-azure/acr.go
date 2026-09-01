package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Registry represents an Azure Container Registry.
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

// ACRCacheRule models an Azure Container Registry cache rule
// (pull-through cache) as returned by the `cacheRules` sub-resource.
// Sockerless and terraform callers register one rule per registered
// upstream (e.g., `docker-hub` → `docker.io/library/*`) so Docker
// Hub references can be rewritten to
// `<acrName>.azurecr.io/<targetRepository>:<tag>` at container launch.
type ACRCacheRule struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type,omitempty"`
	Properties ACRCacheRuleProperties `json:"properties"`
}

// ACRCacheRuleProperties mirrors armcontainerregistry.CacheRuleProperties.
// SourceRepository is the upstream ref (e.g. `docker.io/library/alpine`);
// TargetRepository is the local ACR path (e.g. `docker-hub/library/alpine`).
type ACRCacheRuleProperties struct {
	CredentialSetResourceID string `json:"credentialSetResourceId,omitempty"`
	SourceRepository        string `json:"sourceRepository,omitempty"`
	TargetRepository        string `json:"targetRepository,omitempty"`
	CreationDate            string `json:"creationDate,omitempty"`
	ProvisioningState       string `json:"provisioningState,omitempty"`
}

// Package-level store for dashboard access.
var acrRegistries sim.Store[Registry]

// A registry is stored under its full resource id while a name check names
// only the registry, so the name reaches it through an index rather than a
// walk of every registry. A registry name is global in Azure Container
// Registry, which is why the check exists at all and why the index is keyed on
// the name alone.
var acrRegistriesByName sim.GenerationIndex[Registry]

// acrNamePattern is the constraint the vendored document declares on the name
// a check is asked about: 5 to 50 characters, alphanumeric only.
var acrNamePattern = regexp.MustCompile(`^[a-zA-Z0-9]{5,50}$`)

func acrRegistryNameKeys(reg Registry) []string {
	if reg.Name == "" {
		return nil
	}
	return []string{strings.ToLower(reg.Name)}
}

func registerACR(srv *sim.Server) {
	makeAzureKeyGens(srv)
	// A second buildSimulator in the same process re-collects its own child
	// stores rather than inheriting the previous build's.
	acrMovableChildStores = nil
	registries := sim.MakeStore[Registry](srv.DB(), "acr_registries")
	acrRegistries = registries
	// OCI Distribution data plane (shared registry library). ACR has no
	// pull-through hydration here; the catalog API below reads reg.Manifests.
	// Every /v2/ request authenticates against the registry its Host addresses,
	// with the Docker Registry v2 Bearer challenge and the scope enforcement
	// acr_dataplane_auth.go implements.
	reg := &sim.OCIRegistry{
		Manifests: sim.MakeStore[sim.OCIManifest](srv.DB(), "acr_manifests"),
		Blobs:     sim.MakeStore[sim.OCIBlob](srv.DB(), "acr_blobs"),
		Uploads:   sim.MakeStore[sim.OCIUpload](srv.DB(), "acr_uploads"),
		Authorize: acrAuthorizeV2,
		Scope:     acrDataPlaneScope,
	}
	// cacheRules stores pull-through cache rules keyed by ARM resource ID.
	cacheRules := sim.MakeStore[ACRCacheRule](srv.DB(), "acr_cache_rules")
	acrCacheRules = cacheRules

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerRegistry"

	// POST - Check name availability (azurerm v3 calls this before
	// creating a registry). Lowercase registration; the middleware
	// canonicalizes camelCase → lowercase before dispatch.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/checknameavailability", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// The name a caller is about to create with. Answering "available" for
		// every name makes the check worthless: a client asks precisely so it
		// can avoid the conflict, and this simulator knows which names it
		// already holds.
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

	// PUT - Create or update registry
	srv.HandleFunc("PUT "+armBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")

		var req Registry
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s", sub, rg, name)

		sku := req.Sku
		if sku == nil {
			sku = &RegistrySku{Name: "Basic"}
		}
		// Azure echoes sku.tier equal to sku.name when the client sends only
		// the name (Basic/Standard/Premium).
		if sku.Tier == "" {
			sku.Tier = sku.Name
		}

		// networkRuleBypassOptions defaults to "AzureServices" when the request
		// omits it (ARM Microsoft.ContainerRegistry/registries default).
		bypass := req.Properties.NetworkRuleBypassOptions
		if bypass == "" {
			bypass = "AzureServices"
		}
		// publicNetworkAccess / zoneRedundancy default to Enabled / Disabled
		// but are honored when the client specifies them.
		publicNetworkAccess := req.Properties.PublicNetworkAccess
		if publicNetworkAccess == "" {
			publicNetworkAccess = "Enabled"
		}
		zoneRedundancy := req.Properties.ZoneRedundancy
		if zoneRedundancy == "" {
			zoneRedundancy = "Disabled"
		}
		// Microsoft.ContainerRegistry returns this default when a registry
		// request does not opt into repository-level Azure ABAC permissions.
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

		// go-azure-sdk expects 200 for sync creates
		sim.WriteJSON(w, http.StatusOK, reg)
	})

	// GET - Get registry
	srv.HandleFunc("GET "+armBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s", sub, rg, name)

		reg, ok := registries.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, reg)
	})

	// GET - List registries by resource group
	// (armcontainerregistry.RegistriesClient.NewListByResourceGroupPager, az acr list).
	srv.HandleFunc("GET "+armBase+"/registries", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
		writeACRRegistryList(w, r, registries, func(reg Registry) bool {
			return strings.HasPrefix(reg.ID, prefix)
		})
	})

	// GET - List registries by subscription
	// (armcontainerregistry.RegistriesClient.NewListPager, az acr list).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/registries", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sim.PathParam(r, "subscriptionId"))
		writeACRRegistryList(w, r, registries, func(reg Registry) bool {
			return strings.HasPrefix(reg.ID, prefix)
		})
	})

	// POST - List admin credentials (RegistriesClient.ListCredentials,
	// az acr credential show; terraform reads admin_username/admin_password).
	// Lowercase registration; the middleware canonicalizes the action verb.
	srv.HandleFunc("POST "+armBase+"/registries/{registryName}/listcredentials", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s", sub, rg, name)
		reg, ok := registries.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		if !reg.Properties.AdminUserEnabled {
			sim.AzureError(w, "BadRequest",
				"The admin user is not enabled for the registry. Enable it before requesting credentials.",
				http.StatusBadRequest)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrAdminCredentialsBody(resourceID, name))
	})

	// DELETE - Delete registry
	srv.HandleFunc("DELETE "+armBase+"/registries/{registryName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "registryName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s", sub, rg, name)

		if registries.Delete(resourceID) {
			acrDropCredentialKeyGens(resourceID)
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// Matches armcontainerregistry.CacheRulesClient endpoints. Reference:
	// subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.ContainerRegistry
	//   registries/{registry}/cacheRules[/{rule}]
	// BeginCreate accepts 200/201 (we return 200 sync with final body).
	// BeginDelete accepts 202/204 (we return 204 sync).
	// Parallels the AWS ECR pull-through + GCP Artifact Registry slices.

	// PUT cache rule (Create or Update — LRO collapsed to sync 200).
	srv.HandleFunc("PUT "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")

			regID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s",
				sub, rg, regName)
			if _, ok := registries.Get(regID); !ok {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Registry '%s' under resource group '%s' was not found.", regName, rg)
				return
			}

			var req ACRCacheRule
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.AzureError(w, "InvalidRequestContent",
					"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if req.Properties.SourceRepository == "" || req.Properties.TargetRepository == "" {
				sim.AzureError(w, "InvalidRequestContent",
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

	// GET cache rule.
	srv.HandleFunc("GET "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")

			ruleID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s/cacheRules/%s",
				sub, rg, regName, ruleName)
			rule, ok := cacheRules.Get(ruleID)
			if !ok {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Cache rule '%s' under registry '%s' was not found.", ruleName, regName)
				return
			}
			sim.WriteJSON(w, http.StatusOK, rule)
		})

	// LIST cache rules under a registry.
	srv.HandleFunc("GET "+armBase+"/registries/{registryName}/cacheRules",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")

			regPrefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s/cacheRules/",
				sub, rg, regName)
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

	// DELETE cache rule.
	srv.HandleFunc("DELETE "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")

			ruleID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s/cacheRules/%s",
				sub, rg, regName, ruleName)
			cacheRules.Delete(ruleID)
			w.WriteHeader(http.StatusNoContent)
		})

	// PATCH cache rule (Update — only credentialSetResourceId is mutable).
	srv.HandleFunc("PATCH "+armBase+"/registries/{registryName}/cacheRules/{cacheRuleName}",
		func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			regName := sim.PathParam(r, "registryName")
			ruleName := sim.PathParam(r, "cacheRuleName")
			ruleID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s/cacheRules/%s",
				sub, rg, regName, ruleName)
			if _, ok := cacheRules.Get(ruleID); !ok {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Cache rule '%s' under registry '%s' was not found.", ruleName, regName)
				return
			}
			var req ACRCacheRule
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.AzureError(w, "InvalidRequestContent",
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

	// Registry-level operations, sub-resources, and actions (replications,
	// webhooks, scopeMaps, tokens, credentialSets, connectedRegistries,
	// privateEndpointConnections, privateLinkResources, listUsages, import,
	// credentials, and the provider operations list).
	registerACRRegistryActions(srv, registries)
	registerACRChildResources(srv)
	registerACRWebhooks(srv)

	// OCI Distribution data plane — mounted from the shared registry library.
	reg.Register(srv)

	// GET /acr/v1/_catalog - List all repositories (ACR data-plane catalog API).
	// Reading the catalog needs the registry-wide `registry:catalog:*` access
	// the Bearer challenge asks for.
	srv.HandleFunc("GET /acr/v1/_catalog", func(w http.ResponseWriter, r *http.Request) {
		if !acrAuthorize(w, r, acrRegistryCatalogResource(acrActionAll)) {
			return
		}
		// A registry's catalog is its own: only the manifests stored in the
		// scope of the registry the Host addresses are enumerated.
		scope := acrDataPlaneScope(r)
		all := reg.Manifests.List()
		seen := map[string]bool{}
		var repos []string
		for _, m := range all {
			if m.Scope == scope && m.Repo != "" && !seen[m.Repo] {
				seen[m.Repo] = true
				repos = append(repos, m.Repo)
			}
		}
		page, last := acrCatalogPage(r, repos)
		if page == nil {
			page = []string{}
		}
		if last != "" {
			q := r.URL.Query()
			q.Set("last", last)
			link := fmt.Sprintf("</acr/v1/_catalog?%s>; rel=\"next\"", q.Encode())
			w.Header().Set("Link", link)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"repositories": page,
		})
	})

	// GET /acr/v1/{name}/_tags - List tags for a repository (ACR data-plane tags API)
	// {name} can contain slashes (e.g. "myrepo/myimage"), so matched via {path...}.
	srv.HandleFunc("GET /acr/v1/{path...}", func(w http.ResponseWriter, r *http.Request) {
		target, ok := acrParsePropertiesPath(sim.PathParam(r, "path"))
		if !ok {
			acrPropertiesNotFound(w, "the path does not address a repository, manifest or tag")
			return
		}
		// Only the tag list is answered here; the repository, manifest and tag
		// reads that share this path are served beside the writes they belong
		// with. They used to fall through to a bare 404, which reads as "no
		// such API" for an API the registry does offer.
		if target.kind != "tags" {
			acrReadProperties(w, r, reg, target)
			return
		}
		repoName := target.repo
		// Listing a repository's tags needs the repository's metadata_read
		// access — the action ACR names in the challenge for this API.
		if !acrAuthorize(w, r, acrRepositoryResource(repoName, acrActionMetadataRead, acrActionMetadataRead)) {
			return
		}
		scope := acrDataPlaneScope(r)
		tags := reg.Manifests.Filter(func(m sim.OCIManifest) bool {
			return m.Scope == scope && m.Repo == repoName && m.Ref != "" && !strings.HasPrefix(m.Ref, "sha256:")
		})
		tagList := make([]map[string]any, 0, len(tags))
		for _, m := range tags {
			tagList = append(tagList, map[string]any{
				"name":   m.Ref,
				"digest": m.Digest,
				"changeableAttributes": map[string]any{
					"deleteEnabled": true,
					"writeEnabled":  true,
					"readEnabled":   true,
					"listEnabled":   true,
				},
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"imageName": repoName,
			"tags":      tagList,
		})
	})

	registerACRDataPlaneProperties(srv, reg)
	registerACROAuth2(srv)
}

// registerACROAuth2 mounts a registry's token service — the realm the Docker
// Registry v2 Bearer challenge points at. A client that follows the challenge
// reaches it one of two ways:
//
//   - `docker login <loginServer>` and every Docker-protocol client present the
//     admin credential as HTTP Basic on
//     `GET /oauth2/token?service=<registry>&scope=<scope>` and receive the
//     scoped access token (Azure/acr `docs/Token-BasicAuth.md`);
//   - a Microsoft Entra client exchanges its access token for an ACR refresh
//     token at `POST /oauth2/exchange`, then trades that refresh token and the
//     challenge scope for an access token at `POST /oauth2/token`
//     (Azure/acr `docs/AAD-OAuth.md`).
//
// Every credential on those routes is verified, and the access token carries
// only the scopes the credential authorizes, because the data plane enforces
// the token's `access` claim on each request.
func registerACROAuth2(srv *sim.Server) {
	// POST /oauth2/exchange — Microsoft Entra token → ACR refresh token.
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
			sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"refresh_token": refreshToken})
	})

	// POST /oauth2/token — ACR refresh token + scope → scoped access token.
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
				// The Azure SDK for Go asks for an anonymous token with the
				// password grant and an empty refresh token; an empty refresh
				// token under the refresh-token grant is a malformed request.
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

	// GET /oauth2/token — the Docker Registry v2 token endpoint: HTTP Basic
	// admin credentials plus the challenge's service and scope, in exchange for
	// the scoped access token.
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

// acrTokenServiceRegistry resolves the registry whose token service a request
// reached. A registry's token service is served on its own login server, so
// the Host names it; the `service` parameter the challenge told the client to
// send must name the same registry.
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
// /oauth2/exchange request carries and returns the subject the ACR refresh
// token is issued for. The grant decides which credential is mandatory: the
// access-token grants require the Entra access token, the refresh-token grant
// requires the Entra refresh token.
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

// acrEntraAudienceValid reports whether a Microsoft Entra token's audience is
// the container-registry audience, in either spelling the directory mints.
func acrEntraAudienceValid(audience string) bool {
	return audience == acrEntraAudience || audience == acrEntraAudience+"/"
}

// acrTokenSubject reads the identity of a verified Microsoft Entra token.
func acrTokenSubject(claims map[string]any) string {
	for _, claim := range []string{"preferred_username", "upn", "oid", "sub"} {
		if value, _ := claims[claim].(string); value != "" {
			return value
		}
	}
	return ""
}

// acrTokenIdentity is the identity a token request authenticated as. owner
// distinguishes the registry's own admin (or an authenticated Microsoft Entra
// principal), which is granted every scope it asks for, from the anonymous
// caller a registry with anonymous pull serves. credential and slot fingerprint
// the admin password the identity came from, so regenerating that password
// invalidates the tokens it produced.
type acrTokenIdentity struct {
	subject    string
	credential string
	slot       string
	owner      bool
}

// acrPasswordGrantIdentity authenticates the password grant: an HTTP Basic
// admin credential, or — when the request carries none — the anonymous
// identity a registry with anonymous pull enabled serves. It writes the
// refusal and reports false when neither authenticates.
func acrPasswordGrantIdentity(w http.ResponseWriter, r *http.Request, reg Registry) (acrTokenIdentity, bool) {
	basic := acrSchemeValue(strings.TrimSpace(r.Header.Get("Authorization")), "Basic")
	if basic == "" {
		if acrAnonymousPullEnabled(reg) {
			return acrTokenIdentity{subject: "anonymous"}, true
		}
		acrOAuthUnauthorized(w, "authentication required")
		return acrTokenIdentity{}, false
	}
	username, password, decoded := acrBasicCredential(basic)
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

// acrWriteAccessToken mints the access token for the scopes the credential
// authorizes and writes the token service's response.
func acrWriteAccessToken(w http.ResponseWriter, reg Registry, identity acrTokenIdentity, scopes []string) {
	granted := acrGrantScopes(acrParseScopes(scopes), identity.owner)
	accessToken, err := acrMintAccessToken(reg, identity.subject, granted, identity.credential, identity.slot)
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"access_token": accessToken})
}

// writeACRRegistryList emits a paginated {value, nextLink} envelope of the
// registries matching keep. Pagination engages only when the client supplies an
// explicit $top (armPage default otherwise returns the full list).
func writeACRRegistryList(w http.ResponseWriter, r *http.Request, registries sim.Store[Registry], keep func(Registry) bool) {
	matched := registries.Filter(keep)
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	page, next := armPage(r, matched)
	if page == nil {
		page = []Registry{}
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// acrOAuthError writes an ACR-shaped OAuth2 error envelope.
func acrOAuthError(w http.ResponseWriter, code, msg string) {
	sim.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error":             code,
		"error_description": msg,
	})
}

// acrARMBase is the Microsoft.ContainerRegistry ARM provider path prefix shared
// by every registry control-plane route.
const acrARMBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerRegistry"

// acrSubResource is a generic ARM child resource of a container registry
// (replication, scopeMap, token, credentialSet, connectedRegistry,
// privateEndpointConnection, and the registry-tasks children task / taskRun /
// agentPool). Only the fields the matching Swagger schema declares are emitted:
// id/name/type for every resource, location/tags for tracked resources,
// identity where the schema allows it, and a properties object echoed from the
// request plus a settled provisioningState.
type acrSubResource struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Type       string            `json:"type,omitempty"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   json.RawMessage   `json:"identity,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

// acrChildKind configures the CRUD handlers for one registry child resource.
// allowLocation/allowTags/allowIdentity gate the top-level fields the child's
// Swagger schema declares; patch enables the PATCH verb (some children, e.g.
// privateEndpointConnections, have no update operation).
type acrChildKind struct {
	seg           string // path segment, e.g. "replications"
	nameParam     string // route param name, e.g. "replicationName"
	typeName      string // ARM resource type, e.g. "Microsoft.ContainerRegistry/registries/replications"
	allowLocation bool
	allowTags     bool
	allowIdentity bool
	patch         bool
	store         sim.Store[acrSubResource]
}

// acrRegistryID returns the registry ARM resource ID for the request and
// whether that registry exists in the store.
func acrRegistryID(r *http.Request) (string, bool) {
	regID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerRegistry/registries/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "registryName"))
	_, ok := acrRegistries.Get(regID)
	return regID, ok
}

func (k acrChildKind) resourceID(r *http.Request) string {
	regID, _ := acrRegistryID(r)
	return fmt.Sprintf("%s/%s/%s", regID, k.seg, sim.PathParam(r, k.nameParam))
}

// registerACRChild mounts PUT/GET/list/PATCH/DELETE for one child kind, and
// enrols its store in the set a cross-resource-group move re-keys.
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
		sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
	sim.WriteJSON(w, http.StatusOK, res)
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
		sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
	res, _ := k.store.Get(resID)
	sim.WriteJSON(w, http.StatusOK, res)
}

func (k acrChildKind) handleDelete(w http.ResponseWriter, r *http.Request) {
	if k.store.Delete(k.resourceID(r)) {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// acrAdminCredentialsBody is the RegistryListCredentialsResult shape
// listCredentials and regenerateCredential both return. Real Azure Container
// Registry names the two admin password slots "password" and "password2";
// each carries deterministic material derived from the registry resource ID
// and that slot's rotation generation, so a regenerated slot reads back with
// its new value while the other slot is unchanged.
func acrAdminCredentialsBody(registryID, registryName string) map[string]any {
	return map[string]any{
		"username": registryName,
		"passwords": []map[string]string{
			{"name": "password", "value": azureKeyMaterial32(registryID, "password")},
			{"name": "password2", "value": azureKeyMaterial32(registryID, "password2")},
		},
	}
}

// acrDropCredentialKeyGens removes a deleted registry's admin-credential
// rotation state so a later registry of the same name starts from fresh
// passwords.
func acrDropCredentialKeyGens(registryID string) {
	azureDropKeyGens(registryID, "password", "password2")
}

func acrRegistryNotFound(w http.ResponseWriter, r *http.Request) {
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.ContainerRegistry/registries/%s' under resource group '%s' was not found.",
		sim.PathParam(r, "registryName"), sim.PathParam(r, "resourceGroupName"))
}

func acrChildNotFound(w http.ResponseWriter, k acrChildKind, r *http.Request) {
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource '%s/%s' under registry '%s' was not found.",
		k.typeName, sim.PathParam(r, k.nameParam), sim.PathParam(r, "registryName"))
}

// registerACRChildResources mounts the registry child sub-resources that share
// the generic CRUD shape.
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

	// POST .../connectedRegistries/{name}/deactivate — LRO action, no body.
	// It deactivates the connected registry it names: answering 200 while
	// leaving the registry Active reports work that did not happen, and the
	// read that follows contradicts it.
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
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The connected registry %q was not found.",
					sim.PathParam(r, "connectedRegistryName"))
				return
			}
			w.WriteHeader(http.StatusOK)
		})
}

// Webhooks need bespoke handlers because the create/update parameters carry
// write-only secret fields (serviceUri, customHeaders) that the Webhook
// response schema does not declare. The stored row keeps the secrets for
// getCallbackConfig; the HTTP response emits only the declared WebhookProperties
// members (status, scope, actions, provisioningState).
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

// acrWebhookCreateParams is the WebhookCreateParameters request body.
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
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Webhook '%s' under registry '%s' was not found.", sim.PathParam(r, "webhookName"), sim.PathParam(r, "registryName"))
			return
		}
		var req acrWebhookCreateParams
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	// POST .../webhooks/{name}/ping — fires a ping notification, returns EventInfo.
	srv.HandleFunc("POST "+base+"/{webhookName}/ping", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"id": generateUUID()})
	})

	// POST .../webhooks/{name}/getCallbackConfig — returns the stored secrets.
	srv.HandleFunc("POST "+base+"/{webhookName}/getCallbackConfig", func(w http.ResponseWriter, r *http.Request) {
		id, _ := webhookID(r)
		wh, ok := webhooks.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Webhook '%s' under registry '%s' was not found.", sim.PathParam(r, "webhookName"), sim.PathParam(r, "registryName"))
			return
		}
		out := map[string]any{"serviceUri": wh.ServiceURI}
		if len(wh.CustomHeaders) > 0 {
			out["customHeaders"] = wh.CustomHeaders
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// POST .../webhooks/{name}/listEvents — the delivered-event history (empty
	// until a notification fires; the sim does not retain delivery records).
	srv.HandleFunc("POST "+base+"/{webhookName}/listEvents", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})
}

// registerACRRegistryActions mounts the registry-level operations and POST/GET
// actions: the provider operations list, registry PATCH, listUsages,
// importImage, generateCredentials, regenerateCredential, and the
// privateLinkResources read endpoints.
func registerACRRegistryActions(srv *sim.Server, registries sim.Store[Registry]) {
	// GET /providers/Microsoft.ContainerRegistry/operations — provider op list.
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

	// PATCH registry — update mutable properties / tags / sku.
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
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	// GET .../listUsages — quota usage report.
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

	// POST .../importImage — copies an image from a source registry. LRO with
	// no response body.
	srv.HandleFunc("POST "+acrARMBase+"/registries/{registryName}/importImage", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := acrRegistryID(r); !ok {
			acrRegistryNotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// POST .../generateCredentials — mints token credentials.
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

	// POST .../regenerateCredential — regenerates an admin password. The body's
	// RegenerateCredentialParameters names the slot (password | password2);
	// the response is the full credentials with that slot's new value, which a
	// subsequent listCredentials returns.
	srv.HandleFunc("POST "+acrARMBase+"/registries/{registryName}/regenerateCredential", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		_ = sim.ReadJSON(r, &req)
		if req.Name != "password" && req.Name != "password2" {
			sim.AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The value '%s' is not valid for parameter 'name'. Expected 'password' or 'password2'.", req.Name)
			return
		}
		azureBumpKeyGen(regID, req.Name, "")
		sim.WriteJSON(w, http.StatusOK, acrAdminCredentialsBody(regID, sim.PathParam(r, "registryName")))
	})

	// GET .../privateLinkResources — the registry exposes a single "registry" group.
	srv.HandleFunc("GET "+acrARMBase+"/registries/{registryName}/privateLinkResources", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{acrPrivateLinkResource(regID, "registry")}})
	})

	// GET .../privateLinkResources/{groupName} — the single registry group.
	srv.HandleFunc("GET "+acrARMBase+"/registries/{registryName}/privateLinkResources/{groupName}", func(w http.ResponseWriter, r *http.Request) {
		regID, ok := acrRegistryID(r)
		if !ok {
			acrRegistryNotFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, acrPrivateLinkResource(regID, sim.PathParam(r, "groupName")))
	})
}

// acrPrivateLinkResource builds a PrivateLinkResource for the registry group.
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
