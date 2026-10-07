package main

import (
	"net/http"
	"time"
)

// keyVaultGrantsSecretGet reports whether a vault lets principalID read the
// value of the named secret under the vault's permission model, as the data
// plane decides it for GetSecret.
func keyVaultGrantsSecretGet(v KeyVault, principalID, secretName string) bool {
	op, _ := kvDataPlaneOperation(http.MethodGet, "/secrets/"+secretName)
	return keyVaultGrants(v, kvCaller{oid: principalID}, op)
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
