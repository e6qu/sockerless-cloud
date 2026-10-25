package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

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
	PublicNetworkAccess          string                 `json:"publicNetworkAccess,omitempty"`
	ProvisioningState            string                 `json:"provisioningState,omitempty"`
}

type KeyVaultSku struct {
	Family string `json:"family"`
	Name   string `json:"name"`
}

// KeyVaultAccessPolicy grants an object in the vault's tenant data-plane
// permissions; a vault with enableRbacAuthorization ignores its policies. An
// applicationId makes the policy a compound identity's: it applies only to
// tokens that application obtained on the object's behalf.
type KeyVaultAccessPolicy struct {
	TenantID      string              `json:"tenantId"`
	ObjectID      string              `json:"objectId"`
	ApplicationID string              `json:"applicationId,omitempty"`
	Permissions   KeyVaultPermissions `json:"permissions"`
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

	registerKeyVaultVaults(srv, armBase)

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

	// The data plane answers on `<vault>.vault.<suffix>`: the Host header names
	// the vault, and every request is authenticated and authorized against it
	// before a handler runs.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hostname := r.Host
			if i := strings.LastIndex(hostname, ":"); i >= 0 {
				hostname = hostname[:i]
			}
			parts := strings.SplitN(hostname, ".vault.", 2)
			if len(parts) == 2 {
				if keyVaultAuthorizeDataPlane(w, r, parts[0]) {
					handleKeyVaultDataPlane(w, r, parts[0])
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	})
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

// Each vault's data plane is its own host, and a list operation names exactly
// one vault, so these serve a vault's rows without decoding every other
// vault's. The rows are copied out because the caller sorts them.
var (
	keyVaultKeysByVault  sim.GenerationIndex[kvKeyStored]
	keyVaultCertsByVault sim.GenerationIndex[kvCertStored]
)
