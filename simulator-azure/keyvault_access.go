package main

import (
	"net/http"
	"slices"
	"strings"
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

// The access-policy update operations (Vaults_UpdateAccessPolicy) address a
// policy by its tenant, object and application IDs and work per permission:
// add merges the request's permissions into the matching policy, replace sets
// the matching policy's permissions to the request's, and remove takes the
// request's permissions away from it; a policy an update leaves with no
// permissions is removed. Add and replace append a policy no entry matches,
// remove ignores it, and every other policy of the vault stays as it was.

func keyVaultSamePolicyHolder(a, b KeyVaultAccessPolicy) bool {
	return strings.EqualFold(a.TenantID, b.TenantID) &&
		strings.EqualFold(a.ObjectID, b.ObjectID) &&
		strings.EqualFold(a.ApplicationID, b.ApplicationID)
}

// keyVaultPermissionUnion is have with each permission of add it lacks,
// compared case-insensitively as Key Vault compares permissions.
func keyVaultPermissionUnion(have, add []string) []string {
	out := append([]string(nil), have...)
	for _, p := range add {
		if !slices.ContainsFunc(out, func(h string) bool { return strings.EqualFold(h, p) }) {
			out = append(out, p)
		}
	}
	return out
}

// keyVaultPermissionDifference is have without the permissions of remove.
func keyVaultPermissionDifference(have, remove []string) []string {
	var out []string
	for _, h := range have {
		if !slices.ContainsFunc(remove, func(p string) bool { return strings.EqualFold(h, p) }) {
			out = append(out, h)
		}
	}
	return out
}

func keyVaultPermissionsEmpty(p KeyVaultPermissions) bool {
	return len(p.Keys) == 0 && len(p.Secrets) == 0 && len(p.Certificates) == 0 && len(p.Storage) == 0
}

// keyVaultUpdateAccessPolicies applies each requested policy to the matching
// policy of current through update, appending a requested policy no policy
// matches when appendUnmatched is set, and drops a policy the update leaves
// with no permissions.
func keyVaultUpdateAccessPolicies(current, requested []KeyVaultAccessPolicy, appendUnmatched bool,
	update func(have, req KeyVaultPermissions) KeyVaultPermissions) []KeyVaultAccessPolicy {
	out := append([]KeyVaultAccessPolicy(nil), current...)
	emptied := map[int]bool{}
	for _, req := range requested {
		i := slices.IndexFunc(out, func(p KeyVaultAccessPolicy) bool { return keyVaultSamePolicyHolder(p, req) })
		if i < 0 {
			if appendUnmatched {
				out = append(out, req)
			}
			continue
		}
		out[i].Permissions = update(out[i].Permissions, req.Permissions)
		emptied[i] = keyVaultPermissionsEmpty(out[i].Permissions)
	}
	kept := out[:0]
	for i, p := range out {
		if !emptied[i] {
			kept = append(kept, p)
		}
	}
	return kept
}

func keyVaultAddAccessPolicies(current, requested []KeyVaultAccessPolicy) []KeyVaultAccessPolicy {
	return keyVaultUpdateAccessPolicies(current, requested, true, func(have, req KeyVaultPermissions) KeyVaultPermissions {
		return KeyVaultPermissions{
			Keys:         keyVaultPermissionUnion(have.Keys, req.Keys),
			Secrets:      keyVaultPermissionUnion(have.Secrets, req.Secrets),
			Certificates: keyVaultPermissionUnion(have.Certificates, req.Certificates),
			Storage:      keyVaultPermissionUnion(have.Storage, req.Storage),
		}
	})
}

func keyVaultReplaceAccessPolicies(current, requested []KeyVaultAccessPolicy) []KeyVaultAccessPolicy {
	return keyVaultUpdateAccessPolicies(current, requested, true, func(_, req KeyVaultPermissions) KeyVaultPermissions {
		return req
	})
}

func keyVaultRemoveAccessPolicies(current, requested []KeyVaultAccessPolicy) []KeyVaultAccessPolicy {
	return keyVaultUpdateAccessPolicies(current, requested, false, func(have, req KeyVaultPermissions) KeyVaultPermissions {
		return KeyVaultPermissions{
			Keys:         keyVaultPermissionDifference(have.Keys, req.Keys),
			Secrets:      keyVaultPermissionDifference(have.Secrets, req.Secrets),
			Certificates: keyVaultPermissionDifference(have.Certificates, req.Certificates),
			Storage:      keyVaultPermissionDifference(have.Storage, req.Storage),
		}
	})
}
