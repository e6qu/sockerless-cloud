package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// ecCurveByJWKName maps JWK curve names (P-256, P-384, P-521) to
// crypto/elliptic.Curve. Returns (nil, false) for unknown values.
func ecCurveByJWKName(name string) (elliptic.Curve, bool) {
	switch name {
	case "P-256":
		return elliptic.P256(), true
	case "P-384":
		return elliptic.P384(), true
	case "P-521":
		return elliptic.P521(), true
	}
	return nil, false
}

// Azure Key Vault — sockerless runner workflows commonly fetch
// secrets via `azure/get-keyvault-secrets`, `Get-AzKeyVaultSecret`
// (PowerShell), `az keyvault secret show` (CLI), or
// `armkeyvault.NewVaultsClient` + `azsecrets.NewClient` (Go SDK).
// Without this slice every credential-bootstrap step 404s.
//
// Real Key Vault has two planes:
//   1. ARM control plane creates/configures the vault resource at
//      `Microsoft.KeyVault/vaults/{name}`.
//   2. Data plane (`https://{vault}.vault.azure.net`) reads/writes
//      secret material via JSON over HTTPS.
//
// The sim mirrors both — control plane lives on the standard ARM
// path; data plane lives at `<vault>.vault.<sim-host>:<port>` and is
// routed by Host header through a WrapHandler middleware so the SDK
// can use the canonical URL pattern with no rewrites.

// KeyVault is a `Microsoft.KeyVault/vaults/{name}` ARM resource.
type KeyVault struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Location   string             `json:"location"`
	Tags       map[string]string  `json:"tags,omitempty"`
	Properties KeyVaultProperties `json:"properties"`
}

type DeletedKeyVault struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Type       string                    `json:"type"`
	Properties DeletedKeyVaultProperties `json:"properties"`
}

type DeletedKeyVaultProperties struct {
	VaultID            string `json:"vaultId"`
	Location           string `json:"location"`
	DeletionDate       string `json:"deletionDate"`
	ScheduledPurgeDate string `json:"scheduledPurgeDate"`
}

// KeyVaultProperties holds the per-vault settings.
type KeyVaultProperties struct {
	TenantID                     string                 `json:"tenantId"`
	Sku                          *KeyVaultSku           `json:"sku,omitempty"`
	AccessPolicies               []KeyVaultAccessPolicy `json:"accessPolicies,omitempty"`
	VaultURI                     string                 `json:"vaultUri,omitempty"`
	EnabledForDeployment         bool                   `json:"enabledForDeployment,omitempty"`
	EnabledForDiskEncryption     bool                   `json:"enabledForDiskEncryption,omitempty"`
	EnabledForTemplateDeployment bool                   `json:"enabledForTemplateDeployment,omitempty"`
	EnableSoftDelete             bool                   `json:"enableSoftDelete,omitempty"`
	EnablePurgeProtection        bool                   `json:"enablePurgeProtection,omitempty"`
	EnableRbacAuthorization      bool                   `json:"enableRbacAuthorization,omitempty"`
	SoftDeleteRetentionInDays    int                    `json:"softDeleteRetentionInDays,omitempty"`
	NetworkAcls                  *KeyVaultNetworkAcls   `json:"networkAcls,omitempty"`
	ProvisioningState            string                 `json:"provisioningState,omitempty"`
}

// KeyVaultSku envelope.
type KeyVaultSku struct {
	Family string `json:"family"`
	Name   string `json:"name"`
}

// KeyVaultAccessPolicy entries grant per-principal access — superseded
// by RBAC when `EnableRbacAuthorization=true` but still accepted on
// PUT for legacy callers.
type KeyVaultAccessPolicy struct {
	TenantID    string              `json:"tenantId"`
	ObjectID    string              `json:"objectId"`
	Permissions KeyVaultPermissions `json:"permissions"`
}

// KeyVaultPermissions lists per-policy verbs.
type KeyVaultPermissions struct {
	Keys         []string `json:"keys,omitempty"`
	Secrets      []string `json:"secrets,omitempty"`
	Certificates []string `json:"certificates,omitempty"`
	Storage      []string `json:"storage,omitempty"`
}

// KeyVaultNetworkAcls describes ingress filtering on the vault.
type KeyVaultNetworkAcls struct {
	Bypass              string             `json:"bypass,omitempty"`
	DefaultAction       string             `json:"defaultAction,omitempty"`
	IPRules             []KeyVaultIPRule   `json:"ipRules,omitempty"`
	VirtualNetworkRules []KeyVaultVNetRule `json:"virtualNetworkRules,omitempty"`
}

// KeyVaultIPRule is a per-CIDR allow entry.
type KeyVaultIPRule struct {
	Value string `json:"value"`
}

// KeyVaultVNetRule references a subnet by ID for VNet-scoped access.
type KeyVaultVNetRule struct {
	ID string `json:"id"`
}

// KeyVaultSecret is the data-plane secret resource. Real Azure stores
// per-version material; the sim collapses to the single current
// version (matches the read-most pattern runners use).
//
// This is the wire shape only — what handler responses serialise.
// The persistence shape (kvSecretStored) wraps it with Vault+Name
// fields needed for List filters; those fields must not appear on
// the wire so they live on the wrapper, not here.
type KeyVaultSecret struct {
	ID          string            `json:"id"` // Full URL `<vault>/secrets/{name}/<version>`
	Value       string            `json:"value"`
	Attributes  KeyVaultAttrs     `json:"attributes"`
	Tags        map[string]string `json:"tags,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
}

// kvSecretVersion is one row in the per-secret version chain. Real
// Key Vault stores a separate immutable version per Put; clients can
// list them via `GET /secrets/{name}/versions` and read a specific
// one via `GET /secrets/{name}/{version}`. The latest version is the
// default read target on `GET /secrets/{name}`.
type kvSecretVersion struct {
	Version     string            `json:"version"`
	Value       string            `json:"value"`
	Attributes  KeyVaultAttrs     `json:"attributes"`
	Tags        map[string]string `json:"tags,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
}

// kvSecretStored is the persistence record for a Key Vault secret —
// the chain of versions plus soft-delete state.
//
// State machine:
//
//	(SetSecret)            → active (versions appended)
//	(DeleteSecret)         → soft-deleted (DeletedAt set; row still
//	                         in primary store but reads via /secrets
//	                         404, reads via /deletedsecrets succeed)
//	(POST /deletedsecrets/{name}/recover)   → active again
//	(DELETE /deletedsecrets/{name})         → purged (row removed)
//
// See `.claude/skills/sim-state-machine-completeness/SKILL.md` for the
// rationale: the state field must exist + the canonical transitions
// must be implemented so SDKs that read DeletedAt / RecoveryId get
// real values instead of zero-string.
type kvSecretStored struct {
	Vault            string            `json:"vault"`
	Name             string            `json:"name"`
	Versions         []kvSecretVersion `json:"versions"`
	DeletedAt        int64             `json:"deletedAt,omitempty"`
	ScheduledPurgeAt int64             `json:"scheduledPurgeAt,omitempty"`
	RecoveryID       string            `json:"recoveryId,omitempty"`
}

// latest returns the most recently appended version. Empty struct
// when Versions is empty (shouldn't happen for an active secret —
// guarded at the handler level).
func (s kvSecretStored) latest() kvSecretVersion {
	if len(s.Versions) == 0 {
		return kvSecretVersion{}
	}
	return s.Versions[len(s.Versions)-1]
}

func (s kvSecretStored) findVersion(version string) (kvSecretVersion, bool) {
	for _, v := range s.Versions {
		if v.Version == version {
			return v, true
		}
	}
	return kvSecretVersion{}, false
}

// isDeleted reports whether the secret is in the soft-deleted state.
func (s kvSecretStored) isDeleted() bool { return s.DeletedAt > 0 }

// KeyVaultAttrs mirrors the data-plane SecretAttributes shape.
type KeyVaultAttrs struct {
	Enabled         bool   `json:"enabled"`
	Created         int64  `json:"created,omitempty"`
	Updated         int64  `json:"updated,omitempty"`
	NotBefore       int64  `json:"nbf,omitempty"`
	Expires         int64  `json:"exp,omitempty"`
	RecoveryLevel   string `json:"recoveryLevel,omitempty"`
	RecoverableDays *int   `json:"recoverableDays,omitempty"`
}

func (a KeyVaultAttrs) MarshalJSON() ([]byte, error) {
	type keyVaultAttrsWire struct {
		Enabled         bool   `json:"enabled"`
		Created         int64  `json:"created,omitempty"`
		Updated         int64  `json:"updated,omitempty"`
		NotBefore       int64  `json:"nbf,omitempty"`
		Expires         int64  `json:"exp,omitempty"`
		RecoveryLevel   string `json:"recoveryLevel,omitempty"`
		RecoverableDays *int   `json:"recoverableDays,omitempty"`
	}
	recoveryLevel := a.RecoveryLevel
	if recoveryLevel == "" {
		recoveryLevel = "Recoverable+Purgeable"
	}
	recoverableDays := a.RecoverableDays
	if recoverableDays == nil {
		days := 90
		recoverableDays = &days
	}
	return json.Marshal(keyVaultAttrsWire{
		Enabled:         a.Enabled,
		Created:         a.Created,
		Updated:         a.Updated,
		NotBefore:       a.NotBefore,
		Expires:         a.Expires,
		RecoveryLevel:   recoveryLevel,
		RecoverableDays: recoverableDays,
	})
}

// KeyVaultPrivateEndpointConnection is a private endpoint connection on a
// vault (Microsoft.KeyVault/vaults/privateEndpointConnections).
type KeyVaultPrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Etag       string         `json:"etag,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	keyVaults        sim.Store[KeyVault]
	deletedVaults    sim.Store[DeletedKeyVault]
	keyVaultData     sim.Store[kvSecretStored] // key: <vault>/<secretName>
	keyVaultPrivConn sim.Store[KeyVaultPrivateEndpointConnection]
)

func registerKeyVault(srv *sim.Server) {
	keyVaults = sim.MakeStore[KeyVault](srv.DB(), "keyvaults")
	deletedVaults = sim.MakeStore[DeletedKeyVault](srv.DB(), "keyvault_deleted_vaults")
	keyVaultData = sim.MakeStore[kvSecretStored](srv.DB(), "keyvault_secrets")
	keyVaultPrivConn = sim.MakeStore[KeyVaultPrivateEndpointConnection](srv.DB(), "keyvault_private_endpoint_connections")
	keyVaultKeys = sim.MakeStore[kvKeyStored](srv.DB(), "keyvault_keys")
	keyVaultCertificates = sim.MakeStore[kvCertStored](srv.DB(), "keyvault_certificates")
	keyVaultCertContacts = sim.MakeStore[kvCertContacts](srv.DB(), "keyvault_certificate_contacts")
	keyVaultCertIssuers = sim.MakeStore[kvCertIssuerStored](srv.DB(), "keyvault_certificate_issuers")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault"

	// ARM control plane — vault CRUD.
	srv.HandleFunc("PUT "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		var req KeyVault
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Location == "" {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		if req.Properties.Sku == nil {
			req.Properties.Sku = &KeyVaultSku{Family: "A", Name: "standard"}
		}
		if req.Properties.TenantID == "" {
			req.Properties.TenantID = "00000000-0000-0000-0000-000000000000"
		}
		// soft-delete retention echoes the requested value; Azure
		// defaults it to 90 days only when the request omits it.
		if req.Properties.SoftDeleteRetentionInDays == 0 {
			req.Properties.SoftDeleteRetentionInDays = 90
		}
		req.Properties.VaultURI = azureKeyVaultEndpointURL(r, name)
		req.Properties.ProvisioningState = "Succeeded"

		vault := KeyVault{
			ID:         resourceID,
			Name:       name,
			Type:       "Microsoft.KeyVault/vaults",
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: req.Properties,
		}
		keyVaults.Put(resourceID, vault)
		deleteDeletedVaultsFor(sub, name)
		sim.WriteJSON(w, http.StatusOK, vault)
	})

	srv.HandleFunc("PATCH "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		v, ok := keyVaults.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Vault %q not found in resource group %q.", name, rg)
			return
		}
		var req struct {
			Tags       *map[string]string `json:"tags,omitempty"`
			Properties struct {
				TenantID                     string                  `json:"tenantId,omitempty"`
				Sku                          *KeyVaultSku            `json:"sku,omitempty"`
				AccessPolicies               *[]KeyVaultAccessPolicy `json:"accessPolicies,omitempty"`
				EnabledForDeployment         *bool                   `json:"enabledForDeployment,omitempty"`
				EnabledForDiskEncryption     *bool                   `json:"enabledForDiskEncryption,omitempty"`
				EnabledForTemplateDeployment *bool                   `json:"enabledForTemplateDeployment,omitempty"`
				EnableSoftDelete             *bool                   `json:"enableSoftDelete,omitempty"`
				EnablePurgeProtection        *bool                   `json:"enablePurgeProtection,omitempty"`
				EnableRbacAuthorization      *bool                   `json:"enableRbacAuthorization,omitempty"`
				NetworkAcls                  *KeyVaultNetworkAcls    `json:"networkAcls,omitempty"`
			} `json:"properties,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tags != nil {
			v.Tags = *req.Tags
		}
		if req.Properties.TenantID != "" {
			v.Properties.TenantID = req.Properties.TenantID
		}
		if req.Properties.Sku != nil {
			v.Properties.Sku = req.Properties.Sku
		}
		if req.Properties.AccessPolicies != nil {
			v.Properties.AccessPolicies = *req.Properties.AccessPolicies
		}
		if req.Properties.EnabledForDeployment != nil {
			v.Properties.EnabledForDeployment = *req.Properties.EnabledForDeployment
		}
		if req.Properties.EnabledForDiskEncryption != nil {
			v.Properties.EnabledForDiskEncryption = *req.Properties.EnabledForDiskEncryption
		}
		if req.Properties.EnabledForTemplateDeployment != nil {
			v.Properties.EnabledForTemplateDeployment = *req.Properties.EnabledForTemplateDeployment
		}
		if req.Properties.EnableSoftDelete != nil {
			v.Properties.EnableSoftDelete = *req.Properties.EnableSoftDelete
		}
		if req.Properties.EnablePurgeProtection != nil {
			v.Properties.EnablePurgeProtection = *req.Properties.EnablePurgeProtection
		}
		if req.Properties.EnableRbacAuthorization != nil {
			v.Properties.EnableRbacAuthorization = *req.Properties.EnableRbacAuthorization
		}
		if req.Properties.NetworkAcls != nil {
			v.Properties.NetworkAcls = req.Properties.NetworkAcls
		}
		v.Properties.VaultURI = azureKeyVaultEndpointURL(r, name)
		v.Properties.ProvisioningState = "Succeeded"
		keyVaults.Put(resourceID, v)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("GET "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		v, ok := keyVaults.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Vault %q not found in resource group %q.", name, rg)
			return
		}
		v.Properties.VaultURI = azureKeyVaultEndpointURL(r, name)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("DELETE "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		if v, ok := keyVaults.Get(resourceID); ok {
			now := time.Now().UTC()
			purgeAt := now.Add(90 * 24 * time.Hour)
			deletedVaults.Put(deletedVaultID(sub, v.Location, name), DeletedKeyVault{
				ID:   deletedVaultID(sub, v.Location, name),
				Name: name,
				Type: "Microsoft.KeyVault/deletedVaults",
				Properties: DeletedKeyVaultProperties{
					VaultID:            resourceID,
					Location:           v.Location,
					DeletionDate:       now.Format(time.RFC3339),
					ScheduledPurgeDate: purgeAt.Format(time.RFC3339),
				},
			})
		}
		keyVaults.Delete(resourceID)
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+armBase+"/vaults", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/",
			sub, rg)
		all := keyVaults.Filter(func(v KeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		if all == nil {
			all = []KeyVault{}
		}
		for i := range all {
			all[i].Properties.VaultURI = azureKeyVaultEndpointURL(r, all[i].Name)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		filtered, err := azureApplyListQuery(all, r)
		if err != nil {
			sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		all = filtered
		page, next := armPage(r, all)
		if page == nil {
			page = []KeyVault{}
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// Subscription-scoped vault list — terraform-provider-azurerm
	// populates a per-subscription cache by walking this list when it
	// needs to map a vault URL back to a resource ID (e.g. resolving
	// azurerm_key_vault_secret's KV URI on every plan refresh).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/vaults", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/", sub)
		all := keyVaults.Filter(func(v KeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		if all == nil {
			all = []KeyVault{}
		}
		for i := range all {
			all[i].Properties.VaultURI = azureKeyVaultEndpointURL(r, all[i].Name)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		filtered, err := azureApplyListQuery(all, r)
		if err != nil {
			sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		all = filtered
		page, next := armPage(r, all)
		if page == nil {
			page = []KeyVault{}
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	srv.HandleFunc("PUT "+armBase+"/vaults/{name}/accessPolicies/{operationKind}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		operation := sim.PathParam(r, "operationKind")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		v, ok := keyVaults.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Vault %q not found in resource group %q.", name, rg)
			return
		}
		var req struct {
			Properties struct {
				AccessPolicies []KeyVaultAccessPolicy `json:"accessPolicies"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch operation {
		case "add":
			for _, incoming := range req.Properties.AccessPolicies {
				replaced := false
				for i, existing := range v.Properties.AccessPolicies {
					if existing.ObjectID == incoming.ObjectID {
						v.Properties.AccessPolicies[i] = incoming
						replaced = true
						break
					}
				}
				if !replaced {
					v.Properties.AccessPolicies = append(v.Properties.AccessPolicies, incoming)
				}
			}
		case "replace":
			v.Properties.AccessPolicies = req.Properties.AccessPolicies
		case "remove":
			remove := map[string]bool{}
			for _, p := range req.Properties.AccessPolicies {
				remove[p.ObjectID] = true
			}
			kept := v.Properties.AccessPolicies[:0]
			for _, p := range v.Properties.AccessPolicies {
				if !remove[p.ObjectID] {
					kept = append(kept, p)
				}
			}
			v.Properties.AccessPolicies = kept
		default:
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"access policy operation %q is invalid", operation)
			return
		}
		keyVaults.Put(resourceID, v)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":       resourceID + "/accessPolicies/" + operation,
			"name":     operation,
			"type":     "Microsoft.KeyVault/vaults/accessPolicies",
			"location": v.Location,
			"properties": map[string]any{
				"accessPolicies": v.Properties.AccessPolicies,
			},
		})
	})

	// GET /subscriptions/{sub}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}
	// terraform-provider-azurerm queries this pre-create to detect
	// recoverable soft-deleted vaults (KV soft-delete is enabled by
	// default and has a 90-day recovery window). The sim doesn't model
	// vault-level soft-delete, so the truthful response is 404 in the
	// Azure envelope (i.e. not the Go default 404 page that breaks the
	// provider's error parser). Same for the list variant.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		id := deletedVaultID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location"), sim.PathParam(r, "name"))
		v, ok := deletedVaults.Get(id)
		if !ok {
			sim.AzureErrorf(w, "VaultNotFound", http.StatusNotFound,
				"The vault %q was not found.", sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})

	// POST .../deletedVaults/{name}/purge accepts a purge operation. Azure
	// permits either immediate 200 completion or 202 with a Location poll URI;
	// use the latter so generated clients retain a truthful LRO lifecycle.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}/purge", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		location := sim.PathParam(r, "location")
		name := sim.PathParam(r, "name")
		deletedVaults.Delete(deletedVaultID(sub, location, name))
		operationPath := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/%s/deletedVaults/%s/purge/operation", sub, location, name)
		w.Header().Set("Location", azureRequestScheme(r)+"://"+r.Host+operationPath+"?api-version="+r.URL.Query().Get("api-version"))
		w.WriteHeader(http.StatusAccepted)
	})
	// GET .../purge/operation is the terminal Location returned by a completed
	// purge. A zero-length 200 is the Azure SDK's completion signal for this
	// documented Location-based LRO form.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}/purge/operation", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/deletedVaults", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/", sub)
		all := deletedVaults.Filter(func(v DeletedKeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		if all == nil {
			all = []DeletedKeyVault{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})

	// Vault name-availability check. The azurerm provider calls this
	// pre-create. A name is taken when an active vault already uses it.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/checknameavailability", handleKeyVaultCheckNameAvailability)

	// Vault private endpoint connections + private link resources.
	const vaultBase = armBase + "/vaults"
	srv.HandleFunc("PUT "+vaultBase+"/{name}/privateEndpointConnections/{pec}", handleKeyVaultPECPut)
	srv.HandleFunc("GET "+vaultBase+"/{name}/privateEndpointConnections/{pec}", handleKeyVaultPECGet)
	srv.HandleFunc("DELETE "+vaultBase+"/{name}/privateEndpointConnections/{pec}", handleKeyVaultPECDelete)
	srv.HandleFunc("GET "+vaultBase+"/{name}/privateEndpointConnections", handleKeyVaultPECList)
	srv.HandleFunc("GET "+vaultBase+"/{name}/privateLinkResources", handleKeyVaultPrivateLinkResources)

	// Data plane — subdomain routing via WrapHandler. Host pattern:
	// `<vault>.vault.<sim-host>:<port>`. Strip the suffix to identify
	// the vault and route to the right handler.
	//
	// Requests without an `Authorization` header receive a 401 +
	// `WWW-Authenticate: Bearer` challenge so the Azure SDK's KV
	// clients (azsecrets/azkeys/azcertificates) can complete their
	// challenge-then-retry token-acquisition flow. Real KV is
	// HTTPS-only and the SDK refuses to attach the token until it
	// has read the challenge; the sim trusts any Bearer token
	// thereafter (validation is real-AAD's job).
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			hostname := host
			if i := strings.LastIndex(hostname, ":"); i >= 0 {
				hostname = hostname[:i]
			}
			// Match "<vault>.vault." prefix — works for both
			// localhost (sim) and vault.azure.net (real cloud) suffixes.
			parts := strings.SplitN(hostname, ".vault.", 2)
			if len(parts) == 2 {
				if r.Header.Get("Authorization") == "" {
					// `authorization` must be a URL whose `/`-split
					// yields ≥ 4 segments — every official Azure SDK
					// (Go / .NET / Python / Java) extracts the tenant
					// via `parts[3]` on this URL, with no bounds
					// check. Real KV emits
					// `https://login.microsoftonline.com/<tenant>`;
					// for the sim we substitute the zero-UUID tenant
					// (the SDK only needs *some* extractable string
					// at `parts[3]` — it then asks its own configured
					// credential provider for a token, not the sim).
					// `resource` is the canonical KV audience URI; the
					// SDK does a host-suffix match against the request
					// host, so it must remain `https://vault.azure.net`.
					const kvChallengeTenant = "00000000-0000-0000-0000-000000000000"
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(
						`Bearer authorization="http://%s/%s", resource="https://vault.azure.net"`,
						r.Host, kvChallengeTenant))
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				handleKeyVaultDataPlane(w, r, parts[0])
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

func deletedVaultID(sub, location, name string) string {
	return fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/%s/deletedVaults/%s", sub, location, name)
}

func deleteDeletedVaultsFor(sub, name string) {
	if deletedVaults == nil {
		return
	}
	prefix := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/", sub)
	for _, v := range deletedVaults.List() {
		if strings.HasPrefix(v.ID, prefix) && v.Name == name {
			deletedVaults.Delete(v.ID)
		}
	}
}

func keyVaultResourceID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s", sub, rg, name)
}

// handleKeyVaultCheckNameAvailability reports whether a vault name is free.
// A name is taken when an active vault in the subscription already uses it.
func handleKeyVaultCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AzureError(w, "BadRequest", "name is required", http.StatusBadRequest)
		return
	}
	prefix := fmt.Sprintf("/subscriptions/%s/", sub)
	taken := false
	for _, v := range keyVaults.List() {
		if strings.HasPrefix(v.ID, prefix) && strings.EqualFold(v.Name, req.Name) {
			taken = true
			break
		}
	}
	resp := map[string]any{"nameAvailable": !taken}
	if taken {
		resp["reason"] = "AlreadyExists"
		resp["message"] = fmt.Sprintf("The vault name %q is already in use.", req.Name)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func keyVaultPECID(sub, rg, vault, pec string) string {
	return keyVaultResourceID(sub, rg, vault) + "/privateEndpointConnections/" + pec
}

func handleKeyVaultPECPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	vault := sim.PathParam(r, "name")
	pecName := sim.PathParam(r, "pec")
	if _, ok := keyVaults.Get(keyVaultResourceID(sub, rg, vault)); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Vault %q not found in resource group %q.", vault, rg)
		return
	}
	var req KeyVaultPrivateEndpointConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["provisioningState"] = "Succeeded"
	id := keyVaultPECID(sub, rg, vault, pecName)
	pec := KeyVaultPrivateEndpointConnection{
		ID:         id,
		Name:       pecName,
		Type:       "Microsoft.KeyVault/vaults/privateEndpointConnections",
		Properties: props,
	}
	keyVaultPrivConn.Put(id, pec)
	sim.WriteJSON(w, http.StatusOK, keyVaultPECWire(pec))
}

func handleKeyVaultPECGet(w http.ResponseWriter, r *http.Request) {
	id := keyVaultPECID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	pec, ok := keyVaultPrivConn.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Private endpoint connection %q not found.", sim.PathParam(r, "pec"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, keyVaultPECWire(pec))
}

// keyVaultPECWire projects a stored private endpoint connection onto the
// members Microsoft.KeyVault declares. The connection object is shared with
// the Microsoft.Network private-endpoint surface, which carries additional
// members of its own (group ids, the link identifier, the endpoint's location
// and address); emitting those here would answer a Key Vault client with
// fields its contract does not define.
func keyVaultPECWire(pec KeyVaultPrivateEndpointConnection) KeyVaultPrivateEndpointConnection {
	if pec.Properties == nil {
		return pec
	}
	declared := make(map[string]any, 3)
	for _, member := range []string{"privateEndpoint", "privateLinkServiceConnectionState", "provisioningState"} {
		if v, ok := pec.Properties[member]; ok {
			declared[member] = v
		}
	}
	pec.Properties = declared
	return pec
}

func handleKeyVaultPECDelete(w http.ResponseWriter, r *http.Request) {
	id := keyVaultPECID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	if !keyVaultPrivConn.Delete(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleKeyVaultPECList(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	vault := sim.PathParam(r, "name")
	if _, ok := keyVaults.Get(keyVaultResourceID(sub, rg, vault)); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Vault %q not found in resource group %q.", vault, rg)
		return
	}
	prefix := keyVaultResourceID(sub, rg, vault) + "/privateEndpointConnections/"
	out := keyVaultPrivConn.Filter(func(p KeyVaultPrivateEndpointConnection) bool {
		return strings.HasPrefix(p.ID, prefix)
	})
	wire := make([]KeyVaultPrivateEndpointConnection, 0, len(out))
	for _, pec := range out {
		wire = append(wire, keyVaultPECWire(pec))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": wire})
}

// handleKeyVaultPrivateLinkResources returns the vault's single "vault"
// private-link group, matching real Azure.
func handleKeyVaultPrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	vault := sim.PathParam(r, "name")
	id := keyVaultResourceID(sub, rg, vault)
	if _, ok := keyVaults.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Vault %q not found in resource group %q.", vault, rg)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{{
			"id":   id + "/privateLinkResources/vault",
			"name": "vault",
			"type": "Microsoft.KeyVault/vaults/privateLinkResources",
			"properties": map[string]any{
				"groupId":           "vault",
				"requiredMembers":   []string{"default"},
				"requiredZoneNames": []string{"privatelink.vaultcore.azure.net"},
			},
		}},
	})
}

// handleKeyVaultDataPlane routes requests with `<vault>.vault.*` Host
// to the right secret handler. Path patterns:
//
//	PUT    /secrets/{name}                — SetSecret
//	GET    /secrets/{name}                — GetLatest
//	GET    /secrets/{name}/{version}      — GetSpecific (sim collapses to latest)
//	GET    /secrets                       — ListSecrets
//	DELETE /secrets/{name}                — DeleteSecret
//
// The api-version query param is required by real Azure but ignored
// by the sim.
func handleKeyVaultDataPlane(w http.ResponseWriter, r *http.Request, vault string) {
	path := strings.TrimLeft(r.URL.Path, "/")
	switch {
	case path == "secrets/restore":
		if r.Method != http.MethodPost {
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVRestoreSecret(w, r, vault)
	case strings.HasPrefix(path, "secrets/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["secrets", "<name>"]                           → /secrets/{name}
		//   ["secrets", "<name>", "<version>"]              → /secrets/{name}/{version}
		//   ["secrets", "<name>", "versions"]               → /secrets/{name}/versions
		if len(segs) < 2 {
			sim.AzureError(w, "BadRequest", "Missing secret name", http.StatusBadRequest)
			return
		}
		name := segs[1]
		if len(segs) == 3 && segs[2] == "versions" {
			if r.Method != http.MethodGet {
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVListSecretVersions(w, r, vault, name)
			return
		}
		if len(segs) == 3 && segs[2] == "backup" {
			if r.Method != http.MethodPost {
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVBackupSecret(w, r, vault, name)
			return
		}
		if len(segs) == 3 {
			version := segs[2]
			// /secrets/{name}/ (empty version, trailing slash) — the
			// azsecrets SDK constructs this when GetSecret is called
			// with an empty version arg. Real Azure KV resolves it
			// against the latest version, same as /secrets/{name}.
			if version == "" {
				switch r.Method {
				case http.MethodGet:
					handleKVGetSecret(w, r, vault, name)
				case http.MethodPut:
					handleKVSetSecret(w, r, vault, name)
				case http.MethodDelete:
					handleKVDeleteSecret(w, r, vault, name)
				default:
					sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				}
				return
			}
			// /secrets/{name}/{version} — version-specific Get / Patch.
			switch r.Method {
			case http.MethodGet:
				handleKVGetSecretVersion(w, r, vault, name, version)
			case http.MethodPatch:
				handleKVPatchSecret(w, r, vault, name, version)
			default:
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			}
			return
		}
		switch r.Method {
		case http.MethodPut:
			handleKVSetSecret(w, r, vault, name)
		case http.MethodGet:
			handleKVGetSecret(w, r, vault, name)
		case http.MethodDelete:
			handleKVDeleteSecret(w, r, vault, name)
		default:
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "secrets" || path == "secrets/":
		if r.Method != http.MethodGet {
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListSecrets(w, r, vault)
	case path == "deletedsecrets" || path == "deletedsecrets/":
		if r.Method != http.MethodGet {
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListDeletedSecrets(w, r, vault)
	case strings.HasPrefix(path, "deletedsecrets/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["deletedsecrets", "<name>"]            → soft-deleted secret Get / Purge
		//   ["deletedsecrets", "<name>", "recover"] → POST recover
		if len(segs) < 2 {
			sim.AzureError(w, "BadRequest", "Missing secret name", http.StatusBadRequest)
			return
		}
		name := segs[1]
		if len(segs) == 3 && segs[2] == "recover" {
			if r.Method != http.MethodPost {
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVRecoverDeletedSecret(w, r, vault, name)
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleKVGetDeletedSecret(w, r, vault, name)
		case http.MethodDelete:
			handleKVPurgeDeletedSecret(w, r, vault, name)
		default:
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "deletedkeys" || path == "deletedkeys/":
		if r.Method != http.MethodGet {
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListDeletedKeys(w, r, vault)
	case strings.HasPrefix(path, "deletedkeys/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["deletedkeys", "<name>"]            → soft-deleted key Get / Purge
		//   ["deletedkeys", "<name>", "recover"] → POST recover
		if len(segs) < 2 {
			sim.AzureError(w, "BadRequest", "Missing key name", http.StatusBadRequest)
			return
		}
		if len(segs) == 3 && segs[2] == "recover" {
			if r.Method != http.MethodPost {
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVRecoverDeletedKey(w, r, vault, segs[1])
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleKVGetDeletedKey(w, r, vault, segs[1])
		case http.MethodDelete:
			handleKVPurgeDeletedKey(w, r, vault, segs[1])
		default:
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "deletedcertificates" || path == "deletedcertificates/":
		if r.Method != http.MethodGet {
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListDeletedCertificates(w, r, vault)
	case strings.HasPrefix(path, "deletedcertificates/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["deletedcertificates", "<name>"]            → soft-deleted certificate Get / Purge
		//   ["deletedcertificates", "<name>", "recover"] → POST recover
		if len(segs) < 2 {
			sim.AzureError(w, "BadRequest", "Missing certificate name", http.StatusBadRequest)
			return
		}
		if len(segs) == 3 && segs[2] == "recover" {
			if r.Method != http.MethodPost {
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVRecoverDeletedCertificate(w, r, vault, segs[1])
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleKVGetDeletedCertificate(w, r, vault, segs[1])
		case http.MethodDelete:
			handleKVPurgeDeletedCertificate(w, r, vault, segs[1])
		default:
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "rng" || path == "rng/":
		// GetRandomBytes (POST /rng): random bytes drawn from the platform's
		// CSPRNG, the operation a managed HSM's RNG serves.
		if r.Method != http.MethodPost {
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVGetRandomBytes(w, r)
	case strings.HasPrefix(path, "keys/") || path == "keys":
		handleKVKey(w, r, vault, path)
	case strings.HasPrefix(path, "certificates/") || path == "certificates":
		handleKVCertificate(w, r, vault, path)
	default:
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Key Vault data plane path %q not implemented", path)
	}
}

// KeyVaultKey is a key stored at /keys/{name}. This is the persistence
// shape; responses go through keyBundle, which emits the KeyBundle wire
// shape where the key id lives only inside $.key (JsonWebKey.kid).
type KeyVaultKey struct {
	ID         string            `json:"kid"`
	JsonWebKey map[string]any    `json:"key,omitempty"`
	Attributes KeyVaultAttrs     `json:"attributes,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

// keyBundle is the KeyBundle wire shape: key + attributes + tags, with
// no top-level kid member.
func keyBundle(k KeyVaultKey) map[string]any {
	out := map[string]any{
		"key":        k.JsonWebKey,
		"attributes": k.Attributes,
	}
	if len(k.Tags) > 0 {
		out["tags"] = k.Tags
	}
	return out
}

type kvKeyVersion struct {
	Version string `json:"version"`
	KeyVaultKey
	PrivateKeyPEM string `json:"privateKeyPem,omitempty"`
}

type kvKeyStored struct {
	Vault            string         `json:"vault"`
	Name             string         `json:"name"`
	Versions         []kvKeyVersion `json:"versions,omitempty"`
	PrivateKeyPEM    string         `json:"privateKeyPem,omitempty"`
	DeletedAt        int64          `json:"deletedAt,omitempty"`
	ScheduledPurgeAt int64          `json:"scheduledPurgeAt,omitempty"`
	RecoveryID       string         `json:"recoveryId,omitempty"`
	// RotationPolicy is the key's configured rotation policy document
	// (lifetimeActions + attributes), set through PUT /keys/{name}/rotationpolicy.
	// Nil means the policy was never configured, in which case reads answer Key
	// Vault's baseline policy (a Notify action 30 days before expiry).
	RotationPolicy map[string]any `json:"rotationPolicy,omitempty"`
	KeyVaultKey
}

func (k kvKeyStored) isDeleted() bool { return k.DeletedAt > 0 }

func (k kvKeyStored) latest() (kvKeyVersion, bool) {
	if len(k.Versions) > 0 {
		return k.Versions[len(k.Versions)-1], true
	}
	if k.ID != "" {
		return kvKeyVersion{Version: kvVersionFromID(k.ID), KeyVaultKey: k.KeyVaultKey, PrivateKeyPEM: k.PrivateKeyPEM}, true
	}
	return kvKeyVersion{}, false
}

func (k kvKeyStored) findVersion(version string) (kvKeyVersion, bool) {
	if version == "" {
		return k.latest()
	}
	for _, v := range k.Versions {
		if v.Version == version || kvVersionFromID(v.ID) == version {
			return v, true
		}
	}
	if k.ID != "" && kvVersionFromID(k.ID) == version {
		return kvKeyVersion{Version: version, KeyVaultKey: k.KeyVaultKey, PrivateKeyPEM: k.PrivateKeyPEM}, true
	}
	return kvKeyVersion{}, false
}

// KeyVaultCertificate is a certificate at /certificates/{name}.
// Wire shape only; persistence wrapper is kvCertStored.
type KeyVaultCertificate struct {
	ID                string            `json:"id"`
	KeyID             string            `json:"kid,omitempty"`
	SecretID          string            `json:"sid,omitempty"`
	CER               []byte            `json:"cer,omitempty"`
	ContentType       string            `json:"contentType,omitempty"`
	PreserveCertOrder *bool             `json:"preserveCertOrder,omitempty"`
	X509Thumbprint    string            `json:"x5t,omitempty"`
	Policy            *kvCertPolicy     `json:"policy,omitempty"`
	Attributes        KeyVaultAttrs     `json:"attributes,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// kvCertPolicy is the CertificatePolicy wire shape from the Key Vault
// certificates data plane: member names are the snake_case wire names
// (issuer, x509_props, ...), not the SDK client names the swagger maps
// them to via x-ms-client-name.
type kvCertPolicy struct {
	ID              string                 `json:"id,omitempty"`
	KeyProps        *kvCertKeyProps        `json:"key_props,omitempty"`
	SecretProps     *kvCertSecretProps     `json:"secret_props,omitempty"`
	X509Props       *kvCertX509Props       `json:"x509_props,omitempty"`
	LifetimeActions []kvCertLifetimeAction `json:"lifetime_actions,omitempty"`
	Issuer          *kvCertIssuer          `json:"issuer,omitempty"`
	Attributes      *KeyVaultAttrs         `json:"attributes,omitempty"`
}

type kvCertKeyProps struct {
	Exportable *bool  `json:"exportable,omitempty"`
	KeyType    string `json:"kty,omitempty"`
	KeySize    int    `json:"key_size,omitempty"`
	ReuseKey   *bool  `json:"reuse_key,omitempty"`
	Curve      string `json:"crv,omitempty"`
}

type kvCertSecretProps struct {
	ContentType string `json:"contentType,omitempty"`
}

type kvCertX509Props struct {
	Subject          string      `json:"subject,omitempty"`
	EKUs             []string    `json:"ekus,omitempty"`
	SANs             *kvCertSANs `json:"sans,omitempty"`
	KeyUsage         []string    `json:"key_usage,omitempty"`
	ValidityInMonths int         `json:"validity_months,omitempty"`
}

type kvCertSANs struct {
	Emails      []string `json:"emails,omitempty"`
	DNSNames    []string `json:"dns_names,omitempty"`
	UPNs        []string `json:"upns,omitempty"`
	URIs        []string `json:"uris,omitempty"`
	IPAddresses []string `json:"ipAddresses,omitempty"`
}

type kvCertLifetimeAction struct {
	Trigger *kvCertTrigger `json:"trigger,omitempty"`
	Action  *kvCertAction  `json:"action,omitempty"`
}

type kvCertTrigger struct {
	LifetimePercentage int `json:"lifetime_percentage,omitempty"`
	DaysBeforeExpiry   int `json:"days_before_expiry,omitempty"`
}

type kvCertAction struct {
	ActionType string `json:"action_type,omitempty"`
}

type kvCertIssuer struct {
	Name             string `json:"name,omitempty"`
	CertificateType  string `json:"cty,omitempty"`
	CertTransparency *bool  `json:"cert_transparency,omitempty"`
}

type kvCertVersion struct {
	Version string `json:"version"`
	KeyVaultCertificate
}

type kvCertOperation struct {
	ID                    string        `json:"id"`
	Issuer                *kvCertIssuer `json:"issuer,omitempty"`
	CSR                   []byte        `json:"csr,omitempty"`
	CancellationRequested bool          `json:"cancellation_requested"`
	Status                string        `json:"status"`
	StatusDetails         string        `json:"status_details,omitempty"`
	Target                string        `json:"target,omitempty"`
	RequestID             string        `json:"request_id,omitempty"`
}

type kvCertStored struct {
	Vault            string           `json:"vault"`
	Name             string           `json:"name"`
	Versions         []kvCertVersion  `json:"versions,omitempty"`
	PendingOperation *kvCertOperation `json:"pendingOperation,omitempty"`
	DeletedAt        int64            `json:"deletedAt,omitempty"`
	ScheduledPurgeAt int64            `json:"scheduledPurgeAt,omitempty"`
	RecoveryID       string           `json:"recoveryId,omitempty"`
	KeyVaultCertificate
}

func (c kvCertStored) isDeleted() bool { return c.DeletedAt > 0 }

func (c kvCertStored) latest() (kvCertVersion, bool) {
	if len(c.Versions) > 0 {
		return c.Versions[len(c.Versions)-1], true
	}
	if c.ID != "" {
		return kvCertVersion{Version: kvVersionFromID(c.ID), KeyVaultCertificate: c.KeyVaultCertificate}, true
	}
	return kvCertVersion{}, false
}

func (c kvCertStored) findVersion(version string) (kvCertVersion, bool) {
	if version == "" {
		return c.latest()
	}
	for _, v := range c.Versions {
		if v.Version == version || kvVersionFromID(v.ID) == version {
			return v, true
		}
	}
	if c.ID != "" && kvVersionFromID(c.ID) == version {
		return kvCertVersion{Version: version, KeyVaultCertificate: c.KeyVaultCertificate}, true
	}
	return kvCertVersion{}, false
}

var (
	keyVaultKeys         sim.Store[kvKeyStored]
	keyVaultCertificates sim.Store[kvCertStored]
	// keyVaultCertContacts holds each vault's certificate-contacts collection,
	// keyed by vault name (one collection per vault).
	keyVaultCertContacts sim.Store[kvCertContacts]
	// keyVaultCertIssuers holds the vaults' certificate issuers, keyed
	// "<vault>/<issuerName>".
	keyVaultCertIssuers sim.Store[kvCertIssuerStored]
)

// kvCertContact is one certificate contact — the Contact wire shape.
type kvCertContact struct {
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// kvCertContacts is a vault's certificate-contacts collection. The wire shape
// (Contacts) adds a read-only id, stamped on read.
type kvCertContacts struct {
	Vault    string          `json:"vault"`
	Contacts []kvCertContact `json:"contacts,omitempty"`
}

// kvIssuerCredentials is the IssuerCredentials wire shape. The password is
// write-only: it is stored but never rendered back, exactly as Key Vault
// omits pwd from issuer reads.
type kvIssuerCredentials struct {
	AccountID string `json:"account_id,omitempty"`
	Password  string `json:"pwd,omitempty"`
}

// kvIssuerAttributes is the IssuerAttributes wire shape.
type kvIssuerAttributes struct {
	Enabled bool  `json:"enabled"`
	Created int64 `json:"created,omitempty"`
	Updated int64 `json:"updated,omitempty"`
}

// kvCertIssuerStored is one certificate issuer as persisted; issuerBundle
// renders the IssuerBundle wire shape from it.
type kvCertIssuerStored struct {
	Vault       string               `json:"vault"`
	Name        string               `json:"name"`
	Provider    string               `json:"provider,omitempty"`
	Credentials *kvIssuerCredentials `json:"credentials,omitempty"`
	OrgDetails  map[string]any       `json:"org_details,omitempty"`
	Attributes  kvIssuerAttributes   `json:"attributes"`
}

// Each vault's data plane is its own host, and a list operation names exactly
// one vault, so these serve a vault's rows without decoding every other
// vault's. The rows are copied out because the caller sorts them.
var (
	keyVaultKeysByVault  sim.GenerationIndex[kvKeyStored]
	keyVaultCertsByVault sim.GenerationIndex[kvCertStored]
)

// keyVaultKeysIn returns the vault's key records, sorted by name.
func keyVaultKeysIn(vault string) []kvKeyStored {
	rows := keyVaultKeysByVault.LookupAll(keyVaultKeys, vault,
		func(k kvKeyStored) []string { return []string{k.Vault} })
	out := append([]kvKeyStored(nil), rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// keyVaultCertificatesIn returns the vault's certificate records, sorted by
// name.
func keyVaultCertificatesIn(vault string) []kvCertStored {
	rows := keyVaultCertsByVault.LookupAll(keyVaultCertificates, vault,
		func(c kvCertStored) []string { return []string{c.Vault} })
	out := append([]kvCertStored(nil), rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func handleKVKey(w http.ResponseWriter, r *http.Request, vault, path string) {
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		if r.Method == http.MethodGet {
			handleKVListKeys(w, r, vault)
			return
		}
		sim.AzureError(w, "BadRequest", "Missing key name", http.StatusBadRequest)
		return
	}
	name := segs[1]
	if name == "restore" && len(segs) == 2 && r.Method == http.MethodPost {
		handleKVRestoreKey(w, r, vault)
		return
	}
	verb := ""
	if len(segs) >= 3 {
		verb = segs[2]
	}
	switch r.Method {
	case http.MethodPost:
		switch {
		case verb == "create":
			handleKVCreateKey(w, r, vault, name)
			return
		case verb == "backup":
			handleKVBackupKey(w, r, vault, name)
			return
		case verb == "rotate" && len(segs) == 3:
			handleKVRotateKey(w, r, vault, name)
			return
		case len(segs) == 3 && kvIsCryptoVerb(verb):
			// Version-less crypto (POST /keys/{name}/{verb}) targets the key's
			// current version, like real Key Vault and azkeys.Encrypt(name, "").
			handleKVCryptoKey(w, r, vault, name, "", verb)
			return
		case len(segs) == 4:
			handleKVCryptoKey(w, r, vault, name, verb, segs[3])
			return
		}
	case http.MethodPut:
		// PUT /keys/{name}/rotationpolicy is the rotation-policy write, not a
		// key import — routing it into ImportKey would replace the stored key
		// with a version decoded from the policy document.
		if verb == "rotationpolicy" && len(segs) == 3 {
			handleKVUpdateKeyRotationPolicy(w, r, vault, name)
			return
		}
		handleKVImportKey(w, r, vault, name)
		return
	case http.MethodGet:
		if verb == "versions" {
			handleKVListKeyVersions(w, r, vault, name)
			return
		}
		if verb == "rotationpolicy" && len(segs) == 3 {
			handleKVGetKeyRotationPolicy(w, r, vault, name)
			return
		}
		handleKVGetKey(w, r, vault, name, verb)
		return
	case http.MethodPatch:
		handleKVUpdateKey(w, r, vault, name, verb)
		return
	case http.MethodDelete:
		handleKVDeleteKey(w, r, vault, name)
		return
	}
	sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
}

func keyVaultKeyKey(vault, name string) string { return vault + "/" + name }

func handleKVCreateKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Kty        string            `json:"kty"`
		KeySize    int               `json:"key_size,omitempty"`
		Crv        string            `json:"crv,omitempty"`
		KeyOps     []string          `json:"key_ops,omitempty"`
		Attributes *KeyVaultAttrs    `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultKeys.Get(keyVaultKeyKey(vault, name)); exists && rec.isDeleted() {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Key %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	version := generateUUID()
	id := buildKVURL(r, vault, "keys", name, version)
	kty := defaultKVKty(body.Kty)
	jwk, privateKeyPEM, keyErr := generateKVKeyMaterial(kty, body.KeySize, body.Crv)
	if keyErr != nil {
		keyErr.write(w)
		return
	}
	jwk["kid"] = id
	if body.Crv != "" {
		jwk["crv"] = body.Crv
	}
	// Echo the requested key_ops verbatim — Key Vault treats this as an
	// ordered list and returns it in the order the caller supplied. Only
	// fall back to the full default set when the request omits key_ops.
	if len(body.KeyOps) > 0 {
		jwk["key_ops"] = body.KeyOps
	} else {
		jwk["key_ops"] = []string{"encrypt", "decrypt", "sign", "verify", "wrapKey", "unwrapKey"}
	}
	now := time.Now().Unix()
	key := KeyVaultKey{
		ID: id, JsonWebKey: jwk,
		Attributes: KeyVaultAttrs{Enabled: true, Created: now, Updated: now},
		Tags:       body.Tags,
	}
	if body.Attributes != nil {
		key.Attributes.Enabled = body.Attributes.Enabled
	}
	versionRow := kvKeyVersion{Version: version, KeyVaultKey: key, PrivateKeyPEM: privateKeyPEM}
	keyVaultKeys.Put(keyVaultKeyKey(vault, name), kvKeyStored{
		Vault:         vault,
		Name:          name,
		Versions:      []kvKeyVersion{versionRow},
		PrivateKeyPEM: privateKeyPEM,
		KeyVaultKey:   key,
	})
	sim.WriteJSON(w, http.StatusOK, keyBundle(key))
}

func handleKVImportKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Key        map[string]any    `json:"key"`
		Attributes *KeyVaultAttrs    `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultKeys.Get(keyVaultKeyKey(vault, name)); exists && rec.isDeleted() {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Key %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	version := generateUUID()
	id := buildKVURL(r, vault, "keys", name, version)
	if body.Key == nil {
		body.Key = map[string]any{}
	}
	body.Key["kid"] = id
	privateKeyPEM := rsaPrivatePEMFromJWK(body.Key)
	publicKey := publicJWK(body.Key)
	now := time.Now().Unix()
	key := KeyVaultKey{
		ID: id, JsonWebKey: publicKey,
		Attributes: KeyVaultAttrs{Enabled: true, Created: now, Updated: now},
		Tags:       body.Tags,
	}
	if body.Attributes != nil {
		key.Attributes.Enabled = body.Attributes.Enabled
	}
	versionRow := kvKeyVersion{Version: version, KeyVaultKey: key, PrivateKeyPEM: privateKeyPEM}
	keyVaultKeys.Put(keyVaultKeyKey(vault, name), kvKeyStored{
		Vault:         vault,
		Name:          name,
		Versions:      []kvKeyVersion{versionRow},
		PrivateKeyPEM: privateKeyPEM,
		KeyVaultKey:   key,
	})
	sim.WriteJSON(w, http.StatusOK, keyBundle(key))
}

func handleKVGetKey(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, ok := rec.findVersion(version)
	if !ok {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Version %q of key %q was not found.", version, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, keyBundle(v.KeyVaultKey))
}

func handleKVListKeys(w http.ResponseWriter, r *http.Request, vault string) {
	type keyItem struct {
		ID         string            `json:"kid"`
		Attributes KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	all := keyVaultKeysIn(vault)
	items := make([]keyItem, 0, len(all))
	for _, k := range all {
		if !k.isDeleted() {
			v, ok := k.latest()
			if !ok {
				continue
			}
			items = append(items, keyItem{ID: buildKVURL(r, vault, "keys", k.Name, ""), Attributes: v.Attributes, Tags: v.Tags})
		}
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVListKeyVersions(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	type keyItem struct {
		ID         string            `json:"kid"`
		Attributes KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	versions := rec.Versions
	if len(versions) == 0 {
		if v, ok := rec.latest(); ok {
			versions = []kvKeyVersion{v}
		}
	}
	// Oldest-first by creation order (stable sort over append-ordered versions),
	// matching real Azure — not by random version UUID.
	sort.SliceStable(versions, func(i, j int) bool {
		return versions[i].Attributes.Created < versions[j].Attributes.Created
	})
	items := make([]keyItem, 0, len(versions))
	for _, v := range versions {
		items = append(items, keyItem{ID: v.ID, Attributes: v.Attributes, Tags: v.Tags})
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVUpdateKey(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		Attributes *KeyVaultAttrs    `json:"attributes,omitempty"`
		KeyOps     []string          `json:"key_ops,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	found := false
	for i, v := range rec.Versions {
		if version != "" && v.Version != version && kvVersionFromID(v.ID) != version {
			continue
		}
		updateKVKeyVersion(&v, body.Attributes, body.KeyOps, body.Tags)
		rec.Versions[i] = v
		rec.KeyVaultKey = v.KeyVaultKey
		rec.PrivateKeyPEM = v.PrivateKeyPEM
		found = true
		break
	}
	if !found && len(rec.Versions) == 0 && (version == "" || kvVersionFromID(rec.ID) == version) {
		v := kvKeyVersion{Version: kvVersionFromID(rec.ID), KeyVaultKey: rec.KeyVaultKey, PrivateKeyPEM: rec.PrivateKeyPEM}
		updateKVKeyVersion(&v, body.Attributes, body.KeyOps, body.Tags)
		rec.KeyVaultKey = v.KeyVaultKey
		rec.PrivateKeyPEM = v.PrivateKeyPEM
		found = true
	}
	if !found {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Version %q of key %q was not found.", version, name)
		return
	}
	keyVaultKeys.Put(keyVaultKeyKey(vault, name), rec)
	latest, _ := rec.findVersion(version)
	sim.WriteJSON(w, http.StatusOK, keyBundle(latest.KeyVaultKey))
}

func handleKVDeleteKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	now := time.Now().Unix()
	rec.DeletedAt = now
	rec.ScheduledPurgeAt = now + 90*24*60*60
	rec.RecoveryID = buildKVURL(r, vault, "deletedkeys", name, "")
	keyVaultKeys.Put(keyVaultKeyKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, deletedKeyBundle(rec))
}

func handleKVGetDeletedKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Deleted key %q was not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, deletedKeyBundle(rec))
}

func handleKVListDeletedKeys(w http.ResponseWriter, r *http.Request, vault string) {
	items := []map[string]any{}
	for _, rec := range keyVaultKeysIn(vault) {
		if !rec.isDeleted() {
			continue
		}
		items = append(items, deletedKeyBundle(rec))
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVPurgeDeletedKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Deleted key %q was not found.", name)
		return
	}
	keyVaultKeys.Delete(keyVaultKeyKey(vault, name))
	w.WriteHeader(http.StatusNoContent)
}

func deletedKeyBundle(rec kvKeyStored) map[string]any {
	return map[string]any{
		"key":                rec.JsonWebKey,
		"attributes":         rec.Attributes,
		"tags":               rec.Tags,
		"recoveryId":         rec.RecoveryID,
		"deletedDate":        rec.DeletedAt,
		"scheduledPurgeDate": rec.ScheduledPurgeAt,
	}
}

func handleKVBackupKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	writeKVBackupBlob(w, blob)
}

func handleKVRestoreKey(w http.ResponseWriter, r *http.Request, vault string) {
	blob, err := readKVBackupBlob(r)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var rec kvKeyStored
	if err := json.Unmarshal(blob, &rec); err != nil {
		sim.AzureError(w, "BadParameter", "invalid key backup blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec.Name == "" {
		sim.AzureError(w, "BadParameter", "key backup blob is missing key name", http.StatusBadRequest)
		return
	}
	if _, exists := keyVaultKeys.Get(keyVaultKeyKey(vault, rec.Name)); exists {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Key %q already exists in this key vault.", rec.Name)
		return
	}
	rec.Vault = vault
	keyVaultKeys.Put(keyVaultKeyKey(vault, rec.Name), rec)
	if latest, ok := rec.latest(); ok {
		sim.WriteJSON(w, http.StatusOK, keyBundle(latest.KeyVaultKey))
		return
	}
	sim.AzureError(w, "BadParameter", "key backup blob does not contain any key versions", http.StatusBadRequest)
}

// kvKeyMaterialError pairs a Key Vault error code, message and HTTP status so
// generateKVKeyMaterial's callers answer exactly what CreateKey answers for
// the same failure.
type kvKeyMaterialError struct {
	code    string
	message string
	status  int
}

func (e *kvKeyMaterialError) write(w http.ResponseWriter) {
	sim.AzureError(w, e.code, e.message, e.status)
}

// generateKVKeyMaterial mints real key material for one Key Vault key version:
// the public JWK (without a kid — the caller stamps the version URL) and the
// private half the cryptographic operations use. keySize is in bits and zero
// selects the service default (RSA 2048, oct 256); crv is the JWK curve name
// and empty selects P-256.
func generateKVKeyMaterial(kty string, keySize int, crv string) (map[string]any, string, *kvKeyMaterialError) {
	jwk := map[string]any{"kty": kty}
	privateKeyPEM := ""
	switch kty {
	case "RSA", "RSA-HSM":
		bits := keySize
		if bits <= 0 {
			bits = 2048
		}
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return nil, "", &kvKeyMaterialError{"InternalServerError",
				"failed to generate RSA key: " + err.Error(), http.StatusInternalServerError}
		}
		jwk["n"] = base64.RawURLEncoding.EncodeToString(k.N.Bytes())
		jwk["e"] = base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())
		privateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	case "EC", "EC-HSM":
		// Generate a real EC public key on the requested curve so
		// JWK consumers (go-jose, crypto/ecdsa) can parse the
		// resulting `x` / `y` / `crv` triple. The curve name is
		// the JWK form (P-256 / P-384 / P-521); map to crypto/elliptic.
		curveName := crv
		if curveName == "" {
			curveName = "P-256"
		}
		curve, ok := ecCurveByJWKName(curveName)
		if !ok {
			return nil, "", &kvKeyMaterialError{"InvalidRequest",
				"unsupported curve: " + curveName, http.StatusBadRequest}
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, "", &kvKeyMaterialError{"InternalServerError",
				"failed to generate EC key: " + err.Error(), http.StatusInternalServerError}
		}
		jwk["crv"] = curveName
		jwk["x"] = base64.RawURLEncoding.EncodeToString(k.X.Bytes())
		jwk["y"] = base64.RawURLEncoding.EncodeToString(k.Y.Bytes())
	case "oct", "oct-HSM":
		bits := keySize
		if bits <= 0 {
			bits = 256
		}
		keyMaterial := make([]byte, bits/8)
		if _, err := rand.Read(keyMaterial); err != nil {
			return nil, "", &kvKeyMaterialError{"InternalServerError",
				"failed to generate symmetric key: " + err.Error(), http.StatusInternalServerError}
		}
		privateKeyPEM = base64.RawURLEncoding.EncodeToString(keyMaterial)
	default:
		return nil, "", &kvKeyMaterialError{"InvalidRequest",
			"unsupported kty: " + kty, http.StatusBadRequest}
	}
	return jwk, privateKeyPEM, nil
}

// handleKVRecoverDeletedKey transitions a soft-deleted key back to active
// (RecoverDeletedKey, POST /deletedkeys/{name}/recover) and answers the
// recovered key's current version as a KeyBundle, as real Key Vault does.
func handleKVRecoverDeletedKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultKeyKey(vault, name)
	rec, ok := keyVaultKeys.Get(key)
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Deleted key %q was not found.", name)
		return
	}
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultKeys.Put(key, rec)
	latest, ok := rec.latest()
	if !ok {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, keyBundle(latest.KeyVaultKey))
}

// handleKVRotateKey mints a new version of the key (RotateKey, POST
// /keys/{name}/rotate): fresh key material of the same type and strength as
// the current version — same kty, same RSA modulus / symmetric-key size, same
// curve — with the current version's key_ops and tags carried over, exactly
// what Key Vault's rotation produces. When the key's rotation policy sets
// attributes.expiryTime, the new version's expiry is stamped from it.
func handleKVRotateKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	current, ok := rec.latest()
	if !ok {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	kty, _ := current.JsonWebKey["kty"].(string)
	crv, _ := current.JsonWebKey["crv"].(string)
	jwk, privateKeyPEM, keyErr := generateKVKeyMaterial(defaultKVKty(kty), kvStoredKeyBits(current), crv)
	if keyErr != nil {
		keyErr.write(w)
		return
	}
	version := generateUUID()
	id := buildKVURL(r, vault, "keys", name, version)
	jwk["kid"] = id
	if ops, ok := current.JsonWebKey["key_ops"]; ok {
		jwk["key_ops"] = ops
	}
	now := time.Now()
	newKey := KeyVaultKey{
		ID:         id,
		JsonWebKey: jwk,
		Attributes: KeyVaultAttrs{Enabled: current.Attributes.Enabled, Created: now.Unix(), Updated: now.Unix()},
		Tags:       current.Tags,
	}
	if expiry, ok := kvRotationExpiry(rec.RotationPolicy, now); ok {
		newKey.Attributes.Expires = expiry.Unix()
	}
	if len(rec.Versions) == 0 {
		// A record restored from a backup blob taken before versions were
		// tracked keeps its material in the top-level fields; fold it into the
		// version list so the rotation appends rather than replaces.
		rec.Versions = []kvKeyVersion{current}
	}
	rec.Versions = append(rec.Versions, kvKeyVersion{Version: version, KeyVaultKey: newKey, PrivateKeyPEM: privateKeyPEM})
	rec.KeyVaultKey = newKey
	rec.PrivateKeyPEM = privateKeyPEM
	keyVaultKeys.Put(keyVaultKeyKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, keyBundle(newKey))
}

// kvStoredKeyBits reads the strength of a stored key version: the RSA modulus
// size or the symmetric key size, in bits. Zero (EC keys, or material absent)
// lets generateKVKeyMaterial apply its default; EC strength travels in the
// curve name instead.
func kvStoredKeyBits(v kvKeyVersion) int {
	if n, ok := jwkBytes(v.JsonWebKey, "n"); ok {
		return len(n) * 8
	}
	kty, _ := v.JsonWebKey["kty"].(string)
	if kty == "oct" || kty == "oct-HSM" {
		if material, err := base64.RawURLEncoding.DecodeString(v.PrivateKeyPEM); err == nil {
			return len(material) * 8
		}
	}
	return 0
}

// kvRotationExpiry applies a rotation policy's attributes.expiryTime (an ISO
// 8601 period such as "P90D") to the rotation instant. It reports false when
// the policy sets no expiry.
func kvRotationExpiry(policy map[string]any, now time.Time) (time.Time, bool) {
	attrs, _ := policy["attributes"].(map[string]any)
	expiryTime, _ := attrs["expiryTime"].(string)
	if expiryTime == "" {
		return time.Time{}, false
	}
	expiry, ok := kvAddISO8601Period(now, expiryTime)
	return expiry, ok
}

// kvISO8601Period matches the date components of an ISO 8601 period — the
// granularity Key Vault rotation-policy durations use (P1Y, P6M, P90D, …).
var kvISO8601Period = regexp.MustCompile(`^P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?$`)

// kvAddISO8601Period adds an ISO 8601 date period to an instant. It reports
// false for a spelling that is not a date period (including a bare "P").
func kvAddISO8601Period(t time.Time, period string) (time.Time, bool) {
	m := kvISO8601Period.FindStringSubmatch(period)
	if m == nil || (m[1] == "" && m[2] == "" && m[3] == "" && m[4] == "") {
		return time.Time{}, false
	}
	n := func(s string) int {
		v, _ := strconv.Atoi(s)
		return v
	}
	return t.AddDate(n(m[1]), n(m[2]), n(m[3])*7+n(m[4])), true
}

// handleKVGetRandomBytes answers GetRandomBytes (POST /rng): `count` bytes
// drawn from crypto/rand, base64url-encoded, within the documented 1–128
// range.
func handleKVGetRandomBytes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Count *int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Count == nil {
		sim.AzureError(w, "BadParameter", "The 'count' property is required.", http.StatusBadRequest)
		return
	}
	if *body.Count < 1 || *body.Count > 128 {
		sim.AzureErrorf(w, "BadParameter", http.StatusBadRequest,
			"The requested number of random bytes must be between 1 and 128; %d was requested.", *body.Count)
		return
	}
	buf := make([]byte, *body.Count)
	if _, err := rand.Read(buf); err != nil {
		sim.AzureError(w, "InternalServerError", "failed to draw random bytes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": base64.RawURLEncoding.EncodeToString(buf),
	})
}

// kvDefaultRotationPolicy is the rotation policy Key Vault reports for a key
// whose policy was never configured: a Notify action 30 days before expiry
// and no automatic rotation. The attributes carry the key's own creation
// instant — the baseline policy exists from the key's birth.
func kvDefaultRotationPolicy(created int64) map[string]any {
	return map[string]any{
		"lifetimeActions": []map[string]any{{
			"trigger": map[string]any{"timeBeforeExpiry": "P30D"},
			"action":  map[string]any{"type": "Notify"},
		}},
		"attributes": map[string]any{"created": created, "updated": created},
	}
}

// handleKVGetKeyRotationPolicy serves GetKeyRotationPolicy
// (GET /keys/{name}/rotationpolicy): the stored policy document, or the
// service's baseline policy when none was ever configured.
func handleKVGetKeyRotationPolicy(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	policy := rec.RotationPolicy
	if policy == nil {
		policy = kvDefaultRotationPolicy(rec.Attributes.Created)
	}
	sim.WriteJSON(w, http.StatusOK, kvRotationPolicyWire(r, vault, name, policy))
}

// handleKVUpdateKeyRotationPolicy serves UpdateKeyRotationPolicy
// (PUT /keys/{name}/rotationpolicy): stores the client's policy document and
// echoes it back with the policy identifier and updated timestamps stamped.
func handleKVUpdateKeyRotationPolicy(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		LifetimeActions []map[string]any `json:"lifetimeActions"`
		Attributes      map[string]any   `json:"attributes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if expiryTime, _ := body.Attributes["expiryTime"].(string); expiryTime != "" {
		if _, ok := kvAddISO8601Period(time.Now(), expiryTime); !ok {
			sim.AzureErrorf(w, "BadParameter", http.StatusBadRequest,
				"The expiryTime %q is not a valid ISO 8601 period.", expiryTime)
			return
		}
	}
	now := time.Now().Unix()
	created := now
	if prev, _ := rec.RotationPolicy["attributes"].(map[string]any); prev != nil {
		if c, ok := prev["created"].(int64); ok {
			created = c
		} else if c, ok := prev["created"].(float64); ok {
			created = int64(c)
		}
	}
	attrs := map[string]any{"created": created, "updated": now}
	if expiryTime, _ := body.Attributes["expiryTime"].(string); expiryTime != "" {
		attrs["expiryTime"] = expiryTime
	}
	policy := map[string]any{"attributes": attrs}
	if body.LifetimeActions != nil {
		policy["lifetimeActions"] = body.LifetimeActions
	}
	rec.RotationPolicy = policy
	keyVaultKeys.Put(keyVaultKeyKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, kvRotationPolicyWire(r, vault, name, policy))
}

// kvRotationPolicyWire renders a rotation policy document with its read-only
// identifier — https://<vault-host>/keys/<name>/rotationpolicy — stamped, the
// KeyRotationPolicy wire shape.
func kvRotationPolicyWire(r *http.Request, vault, name string, policy map[string]any) map[string]any {
	out := make(map[string]any, len(policy)+1)
	for k, v := range policy {
		out[k] = v
	}
	out["id"] = strings.TrimSuffix(buildKVURL(r, vault, "keys", name, "rotationpolicy"), "/")
	return out
}

// kvIsCryptoVerb reports whether the path segment names a Key Vault key
// cryptographic operation (the ops handleKVCryptoKey dispatches).
func kvIsCryptoVerb(verb string) bool {
	switch verb {
	case "encrypt", "decrypt", "sign", "verify", "wrapkey", "unwrapkey":
		return true
	}
	return false
}

func handleKVCryptoKey(w http.ResponseWriter, r *http.Request, vault, name, version, operation string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	keyVersion, ok := rec.findVersion(version)
	if !ok {
		sim.AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Version %q of key %q was not found.", version, name)
		return
	}
	privateKey, err := rsaPrivateKeyFromPEM(keyVersion.PrivateKeyPEM)
	if err != nil {
		sim.AzureError(w, "NotImplemented",
			"This key type does not support the requested local cryptographic operation.", http.StatusNotImplemented)
		return
	}
	switch operation {
	case "sign":
		handleKVRSAKeySign(w, r, keyVersion, privateKey)
	case "verify":
		handleKVRSAKeyVerify(w, r, privateKey)
	case "encrypt", "wrapkey":
		handleKVRSAKeyEncrypt(w, r, keyVersion, &privateKey.PublicKey)
	case "decrypt", "unwrapkey":
		handleKVRSAKeyDecrypt(w, r, keyVersion, privateKey)
	default:
		sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
	}
}

func handleKVRSAKeySign(w http.ResponseWriter, r *http.Request, version kvKeyVersion, privateKey *rsa.PrivateKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	digest, err := decodeKVURLBytes(body.Value)
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid value: "+err.Error(), http.StatusBadRequest)
		return
	}
	hashAlg, pss, err := signatureHash(body.Algorithm)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var sig []byte
	if pss {
		sig, err = rsa.SignPSS(rand.Reader, privateKey, hashAlg, digest, nil)
	} else {
		sig, err = rsa.SignPKCS1v15(rand.Reader, privateKey, hashAlg, digest)
	}
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	writeKVOperationResult(w, version.ID, sig)
}

func handleKVRSAKeyVerify(w http.ResponseWriter, r *http.Request, privateKey *rsa.PrivateKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Digest    string `json:"digest"`
		Signature string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	digest, err := decodeKVURLBytes(body.Digest)
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid digest: "+err.Error(), http.StatusBadRequest)
		return
	}
	signature, err := decodeKVURLBytes(body.Signature)
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid signature: "+err.Error(), http.StatusBadRequest)
		return
	}
	hashAlg, pss, err := signatureHash(body.Algorithm)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	if pss {
		err = rsa.VerifyPSS(&privateKey.PublicKey, hashAlg, digest, signature, nil)
	} else {
		err = rsa.VerifyPKCS1v15(&privateKey.PublicKey, hashAlg, digest, signature)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": err == nil})
}

func handleKVRSAKeyEncrypt(w http.ResponseWriter, r *http.Request, version kvKeyVersion, publicKey *rsa.PublicKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	plain, err := decodeKVURLBytes(body.Value)
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid value: "+err.Error(), http.StatusBadRequest)
		return
	}
	ciphertext, err := rsaEncrypt(body.Algorithm, publicKey, plain)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	writeKVOperationResult(w, version.ID, ciphertext)
}

func handleKVRSAKeyDecrypt(w http.ResponseWriter, r *http.Request, version kvKeyVersion, privateKey *rsa.PrivateKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	ciphertext, err := decodeKVURLBytes(body.Value)
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid value: "+err.Error(), http.StatusBadRequest)
		return
	}
	plain, err := rsaDecrypt(body.Algorithm, privateKey, ciphertext)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	writeKVOperationResult(w, version.ID, plain)
}

func handleKVCertificate(w http.ResponseWriter, r *http.Request, vault, path string) {
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		if r.Method == http.MethodGet {
			handleKVListCertificates(w, r, vault)
			return
		}
		sim.AzureError(w, "BadRequest", "Missing certificate name", http.StatusBadRequest)
		return
	}
	name := segs[1]
	if name == "restore" && len(segs) == 2 && r.Method == http.MethodPost {
		handleKVRestoreCertificate(w, r, vault)
		return
	}
	// /certificates/contacts and /certificates/issuers are vault-level
	// collections that Key Vault addresses under the certificates prefix —
	// "contacts" and "issuers" are route segments, never certificate names.
	if name == "contacts" && len(segs) == 2 {
		switch r.Method {
		case http.MethodGet:
			handleKVGetCertificateContacts(w, r, vault)
		case http.MethodPut:
			handleKVSetCertificateContacts(w, r, vault)
		case http.MethodDelete:
			handleKVDeleteCertificateContacts(w, r, vault)
		default:
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	if name == "issuers" {
		if len(segs) == 2 {
			if r.Method != http.MethodGet {
				sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVListCertificateIssuers(w, r, vault)
			return
		}
		issuer := segs[2]
		switch r.Method {
		case http.MethodGet:
			handleKVGetCertificateIssuer(w, r, vault, issuer)
		case http.MethodPut:
			handleKVSetCertificateIssuer(w, r, vault, issuer)
		case http.MethodPatch:
			handleKVUpdateCertificateIssuer(w, r, vault, issuer)
		case http.MethodDelete:
			handleKVDeleteCertificateIssuer(w, r, vault, issuer)
		default:
			sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	verb := ""
	if len(segs) >= 3 {
		verb = segs[2]
	}
	switch r.Method {
	case http.MethodPost:
		switch {
		case verb == "create":
			handleKVCreateCertificate(w, r, vault, name)
			return
		case verb == "import":
			handleKVImportCertificate(w, r, vault, name)
			return
		case verb == "backup":
			handleKVBackupCertificate(w, r, vault, name)
			return
		case verb == "pending" && len(segs) == 4 && segs[3] == "merge":
			handleKVMergeCertificate(w, r, vault, name)
			return
		}
	case http.MethodGet:
		if verb == "pending" {
			handleKVGetCertificateOperation(w, r, vault, name)
			return
		}
		if verb == "versions" {
			handleKVListCertificateVersions(w, r, vault, name)
			return
		}
		if verb == "policy" && len(segs) == 3 {
			handleKVGetCertificatePolicy(w, r, vault, name)
			return
		}
		handleKVGetCertificate(w, r, vault, name, verb)
		return
	case http.MethodPatch:
		if verb == "pending" {
			handleKVUpdateCertificateOperation(w, r, vault, name)
			return
		}
		// PATCH /certificates/{name}/policy updates the certificate's policy
		// document — "policy" is a route segment, not a version.
		if verb == "policy" && len(segs) == 3 {
			handleKVUpdateCertificatePolicy(w, r, vault, name)
			return
		}
		handleKVUpdateCertificate(w, r, vault, name, verb)
		return
	case http.MethodDelete:
		if verb == "pending" {
			handleKVDeleteCertificateOperation(w, r, vault, name)
			return
		}
		handleKVDeleteCertificate(w, r, vault, name)
		return
	}
	sim.AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
}

func keyVaultCertKey(vault, name string) string { return vault + "/" + name }

func handleKVCreateCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Policy            *kvCertPolicy     `json:"policy,omitempty"`
		Attributes        *KeyVaultAttrs    `json:"attributes,omitempty"`
		PreserveCertOrder *bool             `json:"preserveCertOrder,omitempty"`
		Tags              map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		sim.AzureError(w, "InvalidRequest",
			"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultCertificates.Get(keyVaultCertKey(vault, name)); exists && rec.isDeleted() {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Certificate %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	version := generateUUID()
	id := buildKVURL(r, vault, "certificates", name, version)
	keyID := buildKVURL(r, vault, "keys", name, version)
	secretID := buildKVURL(r, vault, "secrets", name, version)
	now := time.Now().Unix()
	certDER, thumbprint, err := makeSelfSignedCertificateDER(name)
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	c := KeyVaultCertificate{
		ID:                id,
		KeyID:             keyID,
		SecretID:          secretID,
		CER:               certDER,
		ContentType:       certificateContentType(body.Policy),
		PreserveCertOrder: body.PreserveCertOrder,
		X509Thumbprint:    thumbprint,
		Policy:            body.Policy,
		Attributes:        KeyVaultAttrs{Enabled: true, Created: now, Updated: now},
		Tags:              body.Tags,
	}
	if body.Attributes != nil {
		c.Attributes.Enabled = body.Attributes.Enabled
	}
	op := certificateOperation(r, vault, name, c.ID, body.Policy, "completed")
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), kvCertStored{
		Vault:               vault,
		Name:                name,
		Versions:            []kvCertVersion{{Version: version, KeyVaultCertificate: c}},
		PendingOperation:    &op,
		KeyVaultCertificate: c,
	})
	w.Header().Set("Location", op.ID)
	sim.WriteJSON(w, http.StatusAccepted, op)
}

func handleKVImportCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Base64EncodedCertificate string            `json:"value"`
		Policy                   *kvCertPolicy     `json:"policy,omitempty"`
		Attributes               *KeyVaultAttrs    `json:"attributes,omitempty"`
		PreserveCertOrder        *bool             `json:"preserveCertOrder,omitempty"`
		Tags                     map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultCertificates.Get(keyVaultCertKey(vault, name)); exists && rec.isDeleted() {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Certificate %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	certDER, err := base64.StdEncoding.DecodeString(body.Base64EncodedCertificate)
	if err != nil {
		certDER, err = base64.RawStdEncoding.DecodeString(body.Base64EncodedCertificate)
	}
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid certificate value: "+err.Error(), http.StatusBadRequest)
		return
	}
	c := newKVCertificate(r, vault, name, certDER, body.Policy, body.Attributes, body.Tags, body.PreserveCertOrder)
	putKVCertificate(vault, name, c, nil)
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleKVMergeCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		X509Certificates []string          `json:"x5c"`
		Attributes       *KeyVaultAttrs    `json:"attributes,omitempty"`
		Tags             map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.X509Certificates) == 0 {
		sim.AzureError(w, "BadParameter", "x5c must contain at least one certificate", http.StatusBadRequest)
		return
	}
	certDER, err := base64.StdEncoding.DecodeString(body.X509Certificates[0])
	if err != nil {
		certDER, err = base64.RawStdEncoding.DecodeString(body.X509Certificates[0])
	}
	if err != nil {
		sim.AzureError(w, "BadParameter", "invalid x5c certificate: "+err.Error(), http.StatusBadRequest)
		return
	}
	policy := &kvCertPolicy{Issuer: &kvCertIssuer{Name: "Unknown"}}
	if rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name)); ok {
		if v, ok := rec.latest(); ok && v.Policy != nil {
			policy = v.Policy
		}
	}
	c := newKVCertificate(r, vault, name, certDER, policy, body.Attributes, body.Tags, nil)
	putKVCertificate(vault, name, c, nil)
	sim.WriteJSON(w, http.StatusCreated, c)
}

func handleKVGetCertificate(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, ok := rec.findVersion(version)
	if !ok {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Version %q of certificate %q was not found.", version, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, v.KeyVaultCertificate)
}

func handleKVListCertificates(w http.ResponseWriter, r *http.Request, vault string) {
	type certItem struct {
		ID             string            `json:"id"`
		Attributes     KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags           map[string]string `json:"tags,omitempty"`
		X509Thumbprint string            `json:"x5t,omitempty"`
	}
	all := keyVaultCertificatesIn(vault)
	items := make([]certItem, 0, len(all))
	for _, c := range all {
		if !c.isDeleted() {
			v, ok := c.latest()
			if !ok {
				continue
			}
			items = append(items, certItem{ID: buildKVURL(r, vault, "certificates", c.Name, ""), Attributes: v.Attributes, Tags: v.Tags, X509Thumbprint: v.X509Thumbprint})
		}
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVListCertificateVersions(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	type certItem struct {
		ID             string            `json:"id"`
		Attributes     KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags           map[string]string `json:"tags,omitempty"`
		X509Thumbprint string            `json:"x5t,omitempty"`
	}
	versions := rec.Versions
	if len(versions) == 0 {
		if v, ok := rec.latest(); ok {
			versions = []kvCertVersion{v}
		}
	}
	// Oldest-first by creation order (stable sort over append-ordered versions),
	// matching real Azure — not by random version UUID.
	sort.SliceStable(versions, func(i, j int) bool {
		return versions[i].Attributes.Created < versions[j].Attributes.Created
	})
	items := make([]certItem, 0, len(versions))
	for _, v := range versions {
		items = append(items, certItem{ID: v.ID, Attributes: v.Attributes, Tags: v.Tags, X509Thumbprint: v.X509Thumbprint})
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVUpdateCertificate(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		Attributes *KeyVaultAttrs    `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	for i, v := range rec.Versions {
		if version != "" && v.Version != version && kvVersionFromID(v.ID) != version {
			continue
		}
		updateCertificateFields(&v.KeyVaultCertificate, body.Attributes, body.Tags)
		rec.Versions[i] = v
		rec.KeyVaultCertificate = v.KeyVaultCertificate
		keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
		sim.WriteJSON(w, http.StatusOK, v.KeyVaultCertificate)
		return
	}
	if len(rec.Versions) == 0 && (version == "" || kvVersionFromID(rec.ID) == version) {
		updateCertificateFields(&rec.KeyVaultCertificate, body.Attributes, body.Tags)
		keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
		sim.WriteJSON(w, http.StatusOK, rec.KeyVaultCertificate)
		return
	}
	sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
		"Version %q of certificate %q was not found.", version, name)
}

func handleKVGetCertificateOperation(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() || rec.PendingOperation == nil {
		sim.AzureErrorf(w, "CertificateOperationNotFound", http.StatusNotFound,
			"Certificate operation for %q was not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rec.PendingOperation)
}

func handleKVUpdateCertificateOperation(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() || rec.PendingOperation == nil {
		sim.AzureErrorf(w, "CertificateOperationNotFound", http.StatusNotFound,
			"Certificate operation for %q was not found.", name)
		return
	}
	var body struct {
		CancellationRequested *bool `json:"cancellation_requested,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if body.CancellationRequested != nil {
		rec.PendingOperation.CancellationRequested = *body.CancellationRequested
		if *body.CancellationRequested {
			rec.PendingOperation.Status = "cancelled"
			rec.PendingOperation.StatusDetails = "Cancellation requested."
		}
	}
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, rec.PendingOperation)
}

func handleKVDeleteCertificateOperation(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() || rec.PendingOperation == nil {
		sim.AzureErrorf(w, "CertificateOperationNotFound", http.StatusNotFound,
			"Certificate operation for %q was not found.", name)
		return
	}
	op := rec.PendingOperation
	rec.PendingOperation = nil
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleKVDeleteCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	now := time.Now().Unix()
	rec.DeletedAt = now
	rec.ScheduledPurgeAt = now + 90*24*60*60
	rec.RecoveryID = buildKVURL(r, vault, "deletedcertificates", name, "")
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, deletedCertificateBundle(rec))
}

func handleKVGetDeletedCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Deleted certificate %q was not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, deletedCertificateBundle(rec))
}

func handleKVListDeletedCertificates(w http.ResponseWriter, r *http.Request, vault string) {
	items := []map[string]any{}
	for _, rec := range keyVaultCertificatesIn(vault) {
		if !rec.isDeleted() {
			continue
		}
		items = append(items, deletedCertificateBundle(rec))
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVPurgeDeletedCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Deleted certificate %q was not found.", name)
		return
	}
	keyVaultCertificates.Delete(keyVaultCertKey(vault, name))
	w.WriteHeader(http.StatusNoContent)
}

func deletedCertificateBundle(rec kvCertStored) map[string]any {
	return map[string]any{
		"id":                 rec.ID,
		"kid":                rec.KeyID,
		"sid":                rec.SecretID,
		"cer":                rec.CER,
		"x5t":                rec.X509Thumbprint,
		"policy":             rec.Policy,
		"attributes":         rec.Attributes,
		"tags":               rec.Tags,
		"recoveryId":         rec.RecoveryID,
		"deletedDate":        rec.DeletedAt,
		"scheduledPurgeDate": rec.ScheduledPurgeAt,
	}
}

func handleKVBackupCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	writeKVBackupBlob(w, blob)
}

func handleKVRestoreCertificate(w http.ResponseWriter, r *http.Request, vault string) {
	blob, err := readKVBackupBlob(r)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var rec kvCertStored
	if err := json.Unmarshal(blob, &rec); err != nil {
		sim.AzureError(w, "BadParameter", "invalid certificate backup blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec.Name == "" {
		sim.AzureError(w, "BadParameter", "certificate backup blob is missing certificate name", http.StatusBadRequest)
		return
	}
	if _, exists := keyVaultCertificates.Get(keyVaultCertKey(vault, rec.Name)); exists {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Certificate %q already exists in this key vault.", rec.Name)
		return
	}
	rec.Vault = vault
	keyVaultCertificates.Put(keyVaultCertKey(vault, rec.Name), rec)
	if latest, ok := rec.latest(); ok {
		sim.WriteJSON(w, http.StatusOK, latest.KeyVaultCertificate)
		return
	}
	sim.AzureError(w, "BadParameter", "certificate backup blob does not contain any certificate versions", http.StatusBadRequest)
}

// handleKVRecoverDeletedCertificate transitions a soft-deleted certificate
// back to active (RecoverDeletedCertificate, POST
// /deletedcertificates/{name}/recover) and answers the recovered
// certificate's current version as a CertificateBundle.
func handleKVRecoverDeletedCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultCertKey(vault, name)
	rec, ok := keyVaultCertificates.Get(key)
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Deleted certificate %q was not found.", name)
		return
	}
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultCertificates.Put(key, rec)
	latest, ok := rec.latest()
	if !ok {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, latest.KeyVaultCertificate)
}

// kvContactsWire renders a vault's contacts collection in the Contacts wire
// shape, with the read-only collection identifier stamped.
func kvContactsWire(r *http.Request, vault string, contacts []kvCertContact) map[string]any {
	return map[string]any{
		"id":       strings.TrimSuffix(buildKVURL(r, vault, "certificates", "contacts", ""), "/"),
		"contacts": contacts,
	}
}

// handleKVGetCertificateContacts serves GetCertificateContacts
// (GET /certificates/contacts). A vault with no contacts set answers 404, as
// real Key Vault does.
func handleKVGetCertificateContacts(w http.ResponseWriter, r *http.Request, vault string) {
	rec, ok := keyVaultCertContacts.Get(vault)
	if !ok {
		sim.AzureError(w, "ContactsNotFound", "Contacts not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, kvContactsWire(r, vault, rec.Contacts))
}

// handleKVSetCertificateContacts serves SetCertificateContacts
// (PUT /certificates/contacts): replaces the vault's contact list and echoes
// the stored collection.
func handleKVSetCertificateContacts(w http.ResponseWriter, r *http.Request, vault string) {
	var body struct {
		Contacts []kvCertContact `json:"contacts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	keyVaultCertContacts.Put(vault, kvCertContacts{Vault: vault, Contacts: body.Contacts})
	sim.WriteJSON(w, http.StatusOK, kvContactsWire(r, vault, body.Contacts))
}

// handleKVDeleteCertificateContacts serves DeleteCertificateContacts
// (DELETE /certificates/contacts): removes the collection and answers the
// contacts it held; 404 when none were set.
func handleKVDeleteCertificateContacts(w http.ResponseWriter, r *http.Request, vault string) {
	rec, ok := keyVaultCertContacts.Get(vault)
	if !ok {
		sim.AzureError(w, "ContactsNotFound", "Contacts not found", http.StatusNotFound)
		return
	}
	keyVaultCertContacts.Delete(vault)
	sim.WriteJSON(w, http.StatusOK, kvContactsWire(r, vault, rec.Contacts))
}

func keyVaultIssuerKey(vault, name string) string { return vault + "/" + name }

// issuerBundle renders a stored issuer in the IssuerBundle wire shape. The
// credential password is write-only and never rendered, as in real Key Vault.
func issuerBundle(r *http.Request, rec kvCertIssuerStored) map[string]any {
	out := map[string]any{
		"id":         strings.TrimSuffix(buildKVURL(r, rec.Vault, "certificates", "issuers", rec.Name), "/"),
		"provider":   rec.Provider,
		"attributes": rec.Attributes,
	}
	if rec.Credentials != nil && rec.Credentials.AccountID != "" {
		out["credentials"] = map[string]any{"account_id": rec.Credentials.AccountID}
	}
	if rec.OrgDetails != nil {
		out["org_details"] = rec.OrgDetails
	}
	return out
}

// keyVaultCertIssuersByVault indexes the issuers by the vault that holds
// them: the listing is reached from a data-plane handler, so decoding every
// vault's issuers on each request is the read the store-scan floor forbids.
var keyVaultCertIssuersByVault sim.GenerationIndex[kvCertIssuerStored]

func keyVaultCertIssuerVaultKeys(rec kvCertIssuerStored) []string {
	if rec.Vault == "" {
		return nil
	}
	return []string{rec.Vault}
}

// handleKVListCertificateIssuers serves GetCertificateIssuers
// (GET /certificates/issuers): the CertificateIssuerItem rows — issuer id and
// provider — for the vault, paged like the other vault collections.
func handleKVListCertificateIssuers(w http.ResponseWriter, r *http.Request, vault string) {
	rows := keyVaultCertIssuersByVault.LookupAll(keyVaultCertIssuers, vault, keyVaultCertIssuerVaultKeys)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	items := make([]map[string]any, 0, len(rows))
	for _, rec := range rows {
		items = append(items, map[string]any{
			"id":       strings.TrimSuffix(buildKVURL(r, vault, "certificates", "issuers", rec.Name), "/"),
			"provider": rec.Provider,
		})
	}
	page, next := kvPage(r, items)
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// handleKVGetCertificateIssuer serves GetCertificateIssuer
// (GET /certificates/issuers/{name}).
func handleKVGetCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name))
	if !ok {
		sim.AzureErrorf(w, "IssuerNotFound", http.StatusNotFound,
			"Issuer %q not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// handleKVSetCertificateIssuer serves SetCertificateIssuer
// (PUT /certificates/issuers/{name}): stores the issuer record and answers
// the IssuerBundle.
func handleKVSetCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Provider    string               `json:"provider"`
		Credentials *kvIssuerCredentials `json:"credentials,omitempty"`
		OrgDetails  map[string]any       `json:"org_details,omitempty"`
		Attributes  *kvIssuerAttributes  `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Provider == "" {
		sim.AzureError(w, "BadParameter", "The 'provider' property is required.", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	rec := kvCertIssuerStored{
		Vault:       vault,
		Name:        name,
		Provider:    body.Provider,
		Credentials: body.Credentials,
		OrgDetails:  body.OrgDetails,
		Attributes:  kvIssuerAttributes{Enabled: true, Created: now, Updated: now},
	}
	if prev, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name)); ok {
		rec.Attributes.Created = prev.Attributes.Created
	}
	if body.Attributes != nil {
		rec.Attributes.Enabled = body.Attributes.Enabled
	}
	keyVaultCertIssuers.Put(keyVaultIssuerKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// handleKVUpdateCertificateIssuer serves UpdateCertificateIssuer
// (PATCH /certificates/issuers/{name}): merges the provided members over the
// stored issuer.
func handleKVUpdateCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name))
	if !ok {
		sim.AzureErrorf(w, "IssuerNotFound", http.StatusNotFound,
			"Issuer %q not found.", name)
		return
	}
	var body struct {
		Provider    string               `json:"provider,omitempty"`
		Credentials *kvIssuerCredentials `json:"credentials,omitempty"`
		OrgDetails  map[string]any       `json:"org_details,omitempty"`
		Attributes  *kvIssuerAttributes  `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Provider != "" {
		rec.Provider = body.Provider
	}
	if body.Credentials != nil {
		rec.Credentials = body.Credentials
	}
	if body.OrgDetails != nil {
		rec.OrgDetails = body.OrgDetails
	}
	if body.Attributes != nil {
		rec.Attributes.Enabled = body.Attributes.Enabled
	}
	rec.Attributes.Updated = time.Now().Unix()
	keyVaultCertIssuers.Put(keyVaultIssuerKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// handleKVDeleteCertificateIssuer serves DeleteCertificateIssuer
// (DELETE /certificates/issuers/{name}): removes the issuer and answers the
// bundle it held.
func handleKVDeleteCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name))
	if !ok {
		sim.AzureErrorf(w, "IssuerNotFound", http.StatusNotFound,
			"Issuer %q not found.", name)
		return
	}
	keyVaultCertIssuers.Delete(keyVaultIssuerKey(vault, name))
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// kvCertPolicyWire stamps the read-only policy identifier
// (.../certificates/{name}/policy) onto a policy document.
func kvCertPolicyWire(r *http.Request, vault, name string, policy kvCertPolicy) kvCertPolicy {
	policy.ID = strings.TrimSuffix(buildKVURL(r, vault, "certificates", name, "policy"), "/")
	return policy
}

// kvDerivedCertPolicy reconstructs a certificate's policy from the stored
// certificate itself, for certificates imported or merged without an explicit
// policy document. Everything in it is read out of the real certificate — the
// subject from the parsed X.509, the key type and size from its public key,
// the content type from the stored record — mirroring how Key Vault derives
// an imported certificate's policy; the issuer is Unknown exactly as Key
// Vault reports certificates it did not enroll.
func kvDerivedCertPolicy(v KeyVaultCertificate) kvCertPolicy {
	policy := kvCertPolicy{
		SecretProps: &kvCertSecretProps{ContentType: v.ContentType},
		Issuer:      &kvCertIssuer{Name: "Unknown"},
		Attributes:  &KeyVaultAttrs{Enabled: true, Created: v.Attributes.Created, Updated: v.Attributes.Updated},
	}
	cert, err := x509.ParseCertificate(v.CER)
	if err != nil {
		return policy
	}
	policy.X509Props = &kvCertX509Props{Subject: cert.Subject.String()}
	switch key := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		policy.KeyProps = &kvCertKeyProps{KeyType: "RSA", KeySize: key.Size() * 8}
	case *ecdsa.PublicKey:
		policy.KeyProps = &kvCertKeyProps{KeyType: "EC", Curve: "P-" + strconv.Itoa(key.Curve.Params().BitSize)}
	}
	return policy
}

// handleKVGetCertificatePolicy serves GetCertificatePolicy
// (GET /certificates/{name}/policy): the certificate's stored policy, or the
// policy derived from the certificate itself when it was imported without
// one.
func handleKVGetCertificatePolicy(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, ok := rec.latest()
	if !ok {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	policy := kvDerivedCertPolicy(v.KeyVaultCertificate)
	if v.Policy != nil {
		policy = *v.Policy
	}
	sim.WriteJSON(w, http.StatusOK, kvCertPolicyWire(r, vault, name, policy))
}

// handleKVUpdateCertificatePolicy serves UpdateCertificatePolicy
// (PATCH /certificates/{name}/policy): replaces the provided policy members
// over the certificate's current policy and stores the result on the current
// version.
func handleKVUpdateCertificatePolicy(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	var patch kvCertPolicy
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		sim.AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	v, ok := rec.latest()
	if !ok {
		sim.AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	policy := kvDerivedCertPolicy(v.KeyVaultCertificate)
	if v.Policy != nil {
		policy = *v.Policy
	}
	if patch.KeyProps != nil {
		policy.KeyProps = patch.KeyProps
	}
	if patch.SecretProps != nil {
		policy.SecretProps = patch.SecretProps
	}
	if patch.X509Props != nil {
		policy.X509Props = patch.X509Props
	}
	if patch.LifetimeActions != nil {
		policy.LifetimeActions = patch.LifetimeActions
	}
	if patch.Issuer != nil {
		policy.Issuer = patch.Issuer
	}
	if patch.Attributes != nil {
		policy.Attributes = patch.Attributes
	}
	policy.ID = ""
	stored := policy
	if len(rec.Versions) > 0 {
		rec.Versions[len(rec.Versions)-1].Policy = &stored
	}
	rec.Policy = &stored
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, kvCertPolicyWire(r, vault, name, policy))
}

func defaultKVKty(s string) string {
	if s == "" {
		return "RSA"
	}
	return s
}

func kvVersionFromID(id string) string {
	id = strings.TrimRight(id, "/")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return ""
}

func publicJWK(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch k {
		case "d", "p", "q", "dp", "dq", "qi", "oth", "k":
			continue
		default:
			out[k] = v
		}
	}
	return out
}

func rsaPrivatePEMFromJWK(jwk map[string]any) string {
	nBytes, nOK := jwkBytes(jwk, "n")
	dBytes, dOK := jwkBytes(jwk, "d")
	pBytes, pOK := jwkBytes(jwk, "p")
	qBytes, qOK := jwkBytes(jwk, "q")
	if !nOK || !dOK || !pOK || !qOK {
		return ""
	}
	eBytes, eOK := jwkBytes(jwk, "e")
	if !eOK {
		return ""
	}
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	if e == 0 {
		e = 65537
	}
	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e},
		D:         new(big.Int).SetBytes(dBytes),
		Primes:    []*big.Int{new(big.Int).SetBytes(pBytes), new(big.Int).SetBytes(qBytes)},
	}
	if err := key.Validate(); err != nil {
		return ""
	}
	key.Precompute()
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func jwkBytes(jwk map[string]any, name string) ([]byte, bool) {
	v, ok := jwk[name]
	if !ok {
		return nil, false
	}
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	b, err := decodeKVURLBytes(s)
	return b, err == nil
}

func rsaPrivateKeyFromPEM(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("missing RSA private key")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func decodeKVURLBytes(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("invalid base64url data")
}

func writeKVOperationResult(w http.ResponseWriter, kid string, value []byte) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kid":   kid,
		"value": base64.RawURLEncoding.EncodeToString(value),
	})
}

func signatureHash(alg string) (crypto.Hash, bool, error) {
	switch alg {
	case "RS256":
		return crypto.SHA256, false, nil
	case "RS384":
		return crypto.SHA384, false, nil
	case "RS512":
		return crypto.SHA512, false, nil
	case "PS256":
		return crypto.SHA256, true, nil
	case "PS384":
		return crypto.SHA384, true, nil
	case "PS512":
		return crypto.SHA512, true, nil
	default:
		return 0, false, fmt.Errorf("unsupported signature algorithm %q", alg)
	}
}

func rsaEncrypt(alg string, publicKey *rsa.PublicKey, value []byte) ([]byte, error) {
	switch alg {
	case "RSA-OAEP":
		return rsa.EncryptOAEP(sha1.New(), rand.Reader, publicKey, value, nil)
	case "RSA-OAEP-256":
		return rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, value, nil)
	case "RSA1_5":
		return rsa.EncryptPKCS1v15(rand.Reader, publicKey, value)
	default:
		return nil, fmt.Errorf("unsupported encryption algorithm %q", alg)
	}
}

func rsaDecrypt(alg string, privateKey *rsa.PrivateKey, value []byte) ([]byte, error) {
	switch alg {
	case "RSA-OAEP":
		return rsa.DecryptOAEP(sha1.New(), rand.Reader, privateKey, value, nil)
	case "RSA-OAEP-256":
		return rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, value, nil)
	case "RSA1_5":
		return rsa.DecryptPKCS1v15(rand.Reader, privateKey, value)
	default:
		return nil, fmt.Errorf("unsupported encryption algorithm %q", alg)
	}
}

func updateKVKeyVersion(v *kvKeyVersion, attrs *KeyVaultAttrs, keyOps []string, tags map[string]string) {
	if attrs != nil {
		v.Attributes.Enabled = attrs.Enabled
		v.Attributes.NotBefore = attrs.NotBefore
		v.Attributes.Expires = attrs.Expires
		v.Attributes.Updated = time.Now().Unix()
	}
	if tags != nil {
		v.Tags = tags
	}
	if len(keyOps) > 0 {
		if v.JsonWebKey == nil {
			v.JsonWebKey = map[string]any{}
		}
		v.JsonWebKey["key_ops"] = keyOps
	}
}

func writeKVBackupBlob(w http.ResponseWriter, blob []byte) {
	sim.WriteJSON(w, http.StatusOK, map[string]string{
		"value": base64.RawURLEncoding.EncodeToString(blob),
	})
}

func readKVBackupBlob(r *http.Request) ([]byte, error) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Value == "" {
		return nil, errors.New("backup value is required")
	}
	return decodeKVURLBytes(body.Value)
}

func makeSelfSignedCertificateDER(name string) ([]byte, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, "", err
	}
	sum := sha1.Sum(der)
	return der, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func certificateContentType(policy *kvCertPolicy) string {
	if policy != nil && policy.SecretProps != nil && policy.SecretProps.ContentType != "" {
		return policy.SecretProps.ContentType
	}
	return "application/x-pkcs12"
}

func certificateOperation(r *http.Request, vault, name, target string, policy *kvCertPolicy, status string) kvCertOperation {
	if status == "" {
		status = "completed"
	}
	issuer := &kvCertIssuer{Name: "Self"}
	if policy != nil && policy.Issuer != nil {
		issuer = policy.Issuer
	}
	return kvCertOperation{
		ID:                    buildKVURL(r, vault, "certificates", name, "pending"),
		Issuer:                issuer,
		CSR:                   []byte("sockerless-keyvault-csr-" + name),
		CancellationRequested: false,
		Status:                status,
		StatusDetails:         "Certificate operation completed.",
		Target:                target,
		RequestID:             generateUUID(),
	}
}

func newKVCertificate(r *http.Request, vault, name string, certDER []byte, policy *kvCertPolicy, attrs *KeyVaultAttrs, tags map[string]string, preserve *bool) KeyVaultCertificate {
	version := generateUUID()
	sum := sha1.Sum(certDER)
	now := time.Now().Unix()
	c := KeyVaultCertificate{
		ID:                buildKVURL(r, vault, "certificates", name, version),
		KeyID:             buildKVURL(r, vault, "keys", name, version),
		SecretID:          buildKVURL(r, vault, "secrets", name, version),
		CER:               certDER,
		ContentType:       certificateContentType(policy),
		PreserveCertOrder: preserve,
		X509Thumbprint:    base64.RawURLEncoding.EncodeToString(sum[:]),
		Policy:            policy,
		Attributes:        KeyVaultAttrs{Enabled: true, Created: now, Updated: now},
		Tags:              tags,
	}
	if attrs != nil {
		c.Attributes.Enabled = attrs.Enabled
		c.Attributes.NotBefore = attrs.NotBefore
		c.Attributes.Expires = attrs.Expires
	}
	return c
}

func putKVCertificate(vault, name string, cert KeyVaultCertificate, op *kvCertOperation) {
	version := kvVersionFromID(cert.ID)
	rec, _ := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	rec.Vault = vault
	rec.Name = name
	rec.Versions = append(rec.Versions, kvCertVersion{Version: version, KeyVaultCertificate: cert})
	if op != nil {
		rec.PendingOperation = op
	}
	rec.KeyVaultCertificate = cert
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
}

func updateCertificateFields(c *KeyVaultCertificate, attrs *KeyVaultAttrs, tags map[string]string) {
	if attrs != nil {
		c.Attributes.Enabled = attrs.Enabled
		c.Attributes.NotBefore = attrs.NotBefore
		c.Attributes.Expires = attrs.Expires
		c.Attributes.Updated = time.Now().Unix()
	}
	if tags != nil {
		c.Tags = tags
	}
}

// buildKVURL constructs the canonical KV data-plane resource ID
// (`https://<vault>.vault.<host>/<kind>/<name>/<version>`).
//
// Real Key Vault always emits https URLs; SDKs (azsecrets,
// azkeys, azcertificates) parse the returned `id`/`kid` and reject
// http-scheme URLs at the URL-validation stage. The sim hard-codes
// https for fidelity even though its own listener may be HTTP —
// clients that follow the URL with their own HTTPS resolver
// against the canonical `<vault>.vault.azure.net` host succeed.
//
// `r.Host` already carries the `<vault>.vault.<sim-or-real-host>`
// subdomain the client connected on (the WrapHandler dispatch
// extracted `vault` from this same r.Host). `r.Host` IS the
// canonical host; prepending another `<vault>.vault.` would
// duplicate host segments like `kv.vault.kv.vault.azure.net`.
// Use `r.Host` directly.
func buildKVURL(r *http.Request, vault, kind, name, version string) string {
	host := r.Host
	if host == "" {
		host = vault + ".vault.azure.net"
	}
	return fmt.Sprintf("https://%s/%s/%s/%s", host, kind, name, version)
}

func keyVaultSecretKey(vault, name string) string { return vault + "/" + name }

// kvSecretBundle is the canonical SecretBundle wire shape KV emits
// on a single-secret read. Distinct from kvSecretVersion (which is
// the persistence row): adds the full URL `id` and omits the
// version-only fields.
type kvSecretBundle struct {
	ID          string            `json:"id"`
	Value       string            `json:"value"`
	Attributes  KeyVaultAttrs     `json:"attributes"`
	Tags        map[string]string `json:"tags,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
}

func secretBundle(r *http.Request, vault, name string, v kvSecretVersion) kvSecretBundle {
	return kvSecretBundle{
		ID:          buildKVURL(r, vault, "secrets", name, v.Version),
		Value:       v.Value,
		Attributes:  v.Attributes,
		Tags:        v.Tags,
		ContentType: v.ContentType,
	}
}

// kvSecretItem is the SecretItem shape used inside SecretListResult.
// No Value (real KV doesn't include value bytes in list responses).
type kvSecretItem struct {
	ID          string            `json:"id"`
	Attributes  KeyVaultAttrs     `json:"attributes"`
	Tags        map[string]string `json:"tags,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
}

// kvSecretListResult is the paged wrapper SDKs deserialise.
// `nextLink` is empty when there's only one page; this matches real
// KV for any sim that doesn't actually paginate.
type kvSecretListResult struct {
	Value    []kvSecretItem `json:"value"`
	NextLink string         `json:"nextLink,omitempty"`
}

// kvDeletedSecretBundle is the wire shape returned by `/deletedsecrets/...`
// reads — extends the SecretBundle with recovery metadata.
type kvDeletedSecretBundle struct {
	kvSecretBundle
	RecoveryID         string `json:"recoveryId"`
	DeletedDate        int64  `json:"deletedDate"`
	ScheduledPurgeDate int64  `json:"scheduledPurgeDate"`
}

func handleKVSetSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Value       string              `json:"value"`
		Tags        map[string]string   `json:"tags,omitempty"`
		ContentType string              `json:"contentType,omitempty"`
		Attributes  *kvSecretAttrsPatch `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest",
			"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key := keyVaultSecretKey(vault, name)
	rec, exists := keyVaultData.Get(key)
	if exists && rec.isDeleted() {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Secret %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	now := time.Now().Unix()
	version := generateUUID()
	attrs := KeyVaultAttrs{Enabled: true, Created: now, Updated: now}
	if body.Attributes != nil {
		if body.Attributes.Enabled != nil {
			attrs.Enabled = *body.Attributes.Enabled
		}
		if body.Attributes.NotBefore != nil {
			attrs.NotBefore = *body.Attributes.NotBefore
		}
		if body.Attributes.Expires != nil {
			attrs.Expires = *body.Attributes.Expires
		}
	}
	newVersion := kvSecretVersion{
		Version:     version,
		Value:       body.Value,
		Attributes:  attrs,
		Tags:        body.Tags,
		ContentType: body.ContentType,
	}
	if !exists {
		rec = kvSecretStored{Vault: vault, Name: name}
	}
	rec.Versions = append(rec.Versions, newVersion)
	keyVaultData.Put(key, rec)
	sim.WriteJSON(w, http.StatusOK, secretBundle(r, vault, name, newVersion))
}

func handleKVGetSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault, name))
	if !ok || rec.isDeleted() || len(rec.Versions) == 0 {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, secretBundle(r, vault, name, rec.latest()))
}

// handleKVGetSecretVersion reads a specific version. Path:
// `/secrets/{name}/{version}`.
func handleKVGetSecretVersion(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, found := rec.findVersion(version)
	if !found {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"Version %q of secret %q was not found.", version, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, secretBundle(r, vault, name, v))
}

// kvSecretAttrsPatch is the PATCH-update shape for SecretAttributes: every
// field is a pointer so an omitted attribute leaves the stored value unchanged.
// Real Key Vault UpdateSecret is a partial update — sending only `exp` must not
// reset `enabled`/`nbf` (a non-pointer decode would zero-fill and disable it).
type kvSecretAttrsPatch struct {
	Enabled   *bool  `json:"enabled"`
	NotBefore *int64 `json:"nbf"`
	Expires   *int64 `json:"exp"`
}

// handleKVPatchSecret updates a specific version's attributes /
// tags / contentType. Value is immutable per version; PATCH on the
// secret never changes value. Path: `/secrets/{name}/{version}`.
func handleKVPatchSecret(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		Tags        map[string]string   `json:"tags,omitempty"`
		ContentType *string             `json:"contentType,omitempty"`
		Attributes  *kvSecretAttrsPatch `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sim.AzureError(w, "InvalidRequest",
			"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	for i, v := range rec.Versions {
		if v.Version != version {
			continue
		}
		if body.Tags != nil {
			v.Tags = body.Tags
		}
		if body.ContentType != nil {
			v.ContentType = *body.ContentType
		}
		if body.Attributes != nil {
			if body.Attributes.Enabled != nil {
				v.Attributes.Enabled = *body.Attributes.Enabled
			}
			if body.Attributes.NotBefore != nil {
				v.Attributes.NotBefore = *body.Attributes.NotBefore
			}
			if body.Attributes.Expires != nil {
				v.Attributes.Expires = *body.Attributes.Expires
			}
			v.Attributes.Updated = time.Now().Unix()
		}
		rec.Versions[i] = v
		keyVaultData.Put(keyVaultSecretKey(vault, name), rec)
		sim.WriteJSON(w, http.StatusOK, secretBundle(r, vault, name, v))
		return
	}
	sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
		"Version %q of secret %q was not found.", version, name)
}

func handleKVDeleteSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultSecretKey(vault, name)
	rec, ok := keyVaultData.Get(key)
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	now := time.Now().Unix()
	rec.DeletedAt = now
	// Real KV defaults to 90-day soft-delete retention; sim uses the
	// same so tests asserting against `scheduledPurgeDate` see a
	// plausible interval.
	rec.ScheduledPurgeAt = now + 90*24*60*60
	rec.RecoveryID = buildKVURL(r, vault, "deletedsecrets", name, "")
	keyVaultData.Put(key, rec)
	emitDeletedSecretBundle(w, r, vault, name, rec)
}

func emitDeletedSecretBundle(w http.ResponseWriter, r *http.Request, vault, name string, rec kvSecretStored) {
	if len(rec.Versions) == 0 {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q has no versions.", name)
		return
	}
	bundle := kvDeletedSecretBundle{
		kvSecretBundle:     secretBundle(r, vault, name, rec.latest()),
		RecoveryID:         rec.RecoveryID,
		DeletedDate:        rec.DeletedAt,
		ScheduledPurgeDate: rec.ScheduledPurgeAt,
	}
	sim.WriteJSON(w, http.StatusOK, bundle)
}

func handleKVListSecrets(w http.ResponseWriter, r *http.Request, vault string) {
	all := keyVaultData.Filter(func(s kvSecretStored) bool {
		return s.Vault == vault && !s.isDeleted()
	})
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	items := make([]kvSecretItem, 0, len(all))
	for _, s := range all {
		if len(s.Versions) == 0 {
			continue
		}
		latest := s.latest()
		items = append(items, kvSecretItem{
			ID:          buildKVURL(r, s.Vault, "secrets", s.Name, ""),
			Attributes:  latest.Attributes,
			Tags:        latest.Tags,
			ContentType: latest.ContentType,
		})
	}
	page, next := kvPage(r, items)
	out := kvSecretListResult{Value: page}
	if next != "" {
		out.NextLink = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// handleKVListSecretVersions returns the canonical paged
// SecretListResult of every version of a named secret.
// Path: `/secrets/{name}/versions`.
func handleKVListSecretVersions(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	versions := rec.Versions
	// Oldest-first, matching real Azure. Versions are stored in creation
	// (append) order; a stable sort by Created preserves that order for the
	// common case where rapid same-second writes share a Created timestamp.
	// (Sorting by the random version UUID — the old behaviour — bore no
	// relation to creation order.)
	sort.SliceStable(versions, func(i, j int) bool {
		return versions[i].Attributes.Created < versions[j].Attributes.Created
	})
	items := make([]kvSecretItem, 0, len(versions))
	for _, v := range versions {
		items = append(items, kvSecretItem{
			ID:          buildKVURL(r, vault, "secrets", name, v.Version),
			Attributes:  v.Attributes,
			Tags:        v.Tags,
			ContentType: v.ContentType,
		})
	}
	page, next := kvPage(r, items)
	out := kvSecretListResult{Value: page}
	if next != "" {
		out.NextLink = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// handleKVGetDeletedSecret reads a soft-deleted secret. Path:
// `/deletedsecrets/{name}`.
func handleKVGetDeletedSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault, name))
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"Deleted secret %q was not found.", name)
		return
	}
	emitDeletedSecretBundle(w, r, vault, name, rec)
}

// handleKVListDeletedSecrets returns the paged list of soft-deleted
// secrets in the vault. Path: `/deletedsecrets`.
func handleKVListDeletedSecrets(w http.ResponseWriter, r *http.Request, vault string) {
	all := keyVaultData.Filter(func(s kvSecretStored) bool {
		return s.Vault == vault && s.isDeleted()
	})
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	type deletedItem struct {
		kvSecretItem
		RecoveryID         string `json:"recoveryId"`
		DeletedDate        int64  `json:"deletedDate"`
		ScheduledPurgeDate int64  `json:"scheduledPurgeDate"`
	}
	items := make([]deletedItem, 0, len(all))
	for _, s := range all {
		if len(s.Versions) == 0 {
			continue
		}
		latest := s.latest()
		items = append(items, deletedItem{
			kvSecretItem: kvSecretItem{
				ID:          buildKVURL(r, s.Vault, "secrets", s.Name, ""),
				Attributes:  latest.Attributes,
				Tags:        latest.Tags,
				ContentType: latest.ContentType,
			},
			RecoveryID:         s.RecoveryID,
			DeletedDate:        s.DeletedAt,
			ScheduledPurgeDate: s.ScheduledPurgeAt,
		})
	}
	page, next := kvPage(r, items)
	out := struct {
		Value    []deletedItem `json:"value"`
		NextLink string        `json:"nextLink,omitempty"`
	}{Value: page}
	if next != "" {
		out.NextLink = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVBackupSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultData.Get(keyVaultSecretKey(vault, name))
	if !ok || rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	writeKVBackupBlob(w, blob)
}

func handleKVRestoreSecret(w http.ResponseWriter, r *http.Request, vault string) {
	blob, err := readKVBackupBlob(r)
	if err != nil {
		sim.AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var rec kvSecretStored
	if err := json.Unmarshal(blob, &rec); err != nil {
		sim.AzureError(w, "BadParameter", "invalid secret backup blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec.Name == "" {
		sim.AzureError(w, "BadParameter", "secret backup blob is missing secret name", http.StatusBadRequest)
		return
	}
	if _, exists := keyVaultData.Get(keyVaultSecretKey(vault, rec.Name)); exists {
		sim.AzureErrorf(w, "Conflict", http.StatusConflict,
			"Secret %q already exists in this key vault.", rec.Name)
		return
	}
	rec.Vault = vault
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultData.Put(keyVaultSecretKey(vault, rec.Name), rec)
	if len(rec.Versions) == 0 {
		sim.AzureError(w, "BadParameter", "secret backup blob does not contain any secret versions", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, secretBundle(r, vault, rec.Name, rec.latest()))
}

// handleKVRecoverDeletedSecret transitions a soft-deleted secret
// back to active. Path: `POST /deletedsecrets/{name}/recover`.
func handleKVRecoverDeletedSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultSecretKey(vault, name)
	rec, ok := keyVaultData.Get(key)
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"Deleted secret %q was not found.", name)
		return
	}
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultData.Put(key, rec)
	sim.WriteJSON(w, http.StatusOK, secretBundle(r, vault, name, rec.latest()))
}

// handleKVPurgeDeletedSecret permanently removes a soft-deleted
// secret. Path: `DELETE /deletedsecrets/{name}`.
func handleKVPurgeDeletedSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultSecretKey(vault, name)
	rec, ok := keyVaultData.Get(key)
	if !ok || !rec.isDeleted() {
		sim.AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"Deleted secret %q was not found.", name)
		return
	}
	keyVaultData.Delete(key)
	w.WriteHeader(http.StatusNoContent)
}
