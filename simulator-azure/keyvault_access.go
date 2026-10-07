package main

import (
	"strings"
	"time"
)

// keyVaultSecretGetDataAction is the Azure RBAC data action Key Vault checks
// before it returns a secret's value.
const keyVaultSecretGetDataAction = "Microsoft.KeyVault/vaults/secrets/getSecret/action"

// keyVaultGrantsSecretGet reports whether a vault lets principalID read the
// value of the named secret, under the vault's permission model: a vault that
// uses Azure RBAC grants it through a role assignment at the secret, the vault
// or a scope above it; any other vault through an access policy for that
// object in the vault's tenant that carries the secret get permission.
func keyVaultGrantsSecretGet(v KeyVault, principalID, secretName string) bool {
	if principalID == "" {
		return false
	}
	if v.Properties.EnableRbacAuthorization {
		return rbacPrincipalHasDataAction(principalID, v.ID+"/secrets/"+secretName, keyVaultSecretGetDataAction)
	}
	for _, pol := range v.Properties.AccessPolicies {
		if !strings.EqualFold(pol.ObjectID, principalID) {
			continue
		}
		if pol.TenantID != "" && v.Properties.TenantID != "" && !strings.EqualFold(pol.TenantID, v.Properties.TenantID) {
			continue
		}
		for _, verb := range pol.Permissions.Secrets {
			if strings.EqualFold(verb, "get") || strings.EqualFold(verb, "all") {
				return true
			}
		}
	}
	return false
}

// keyVaultSecretVersionUnusable names why Key Vault refuses to return a
// secret version's value at now — it is disabled, expired or not yet valid —
// or is empty when the version is usable.
func keyVaultSecretVersionUnusable(a KeyVaultAttrs, now time.Time) string {
	switch {
	case !a.Enabled:
		return "is disabled"
	case a.Expires != 0 && now.Unix() >= a.Expires:
		return "has expired"
	case a.NotBefore != 0 && now.Unix() < a.NotBefore:
		return "is not yet valid"
	}
	return ""
}
