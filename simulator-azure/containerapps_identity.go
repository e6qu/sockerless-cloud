package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Managed identities of container apps and Container Apps jobs, and the
// secrets they read from Key Vault through them.
//
// An app or job carries a ManagedServiceIdentity (common-types v3, which
// spells the combined type "SystemAssigned,UserAssigned"). Its workload gets
// IDENTITY_ENDPOINT and IDENTITY_HEADER and acquires the identities' tokens
// from the endpoint App Service serves. A secret that names a keyVaultUrl
// carries no value of its own: Container Apps reads the Key Vault secret as
// the identity the secret's `identity` names — "system", or a user-assigned
// identity's resource ID attached to the app — and the workload sees the
// secret's value wherever an environment variable's secretRef names it
// (learn.microsoft.com/azure/container-apps/manage-secrets, "Reference secret
// from Key Vault").

// acaIdentityNone is the identity Container Apps reports for an app or job
// that has none.
func acaIdentityNone() *SiteIdentity {
	return &SiteIdentity{Type: "None"}
}

// acaApplyIdentity settles the identity a container app or job PUT or PATCH
// asks for, given the previous one and its system-assigned identity's client
// ID, keeping the previous identity when the request names none.
func acaApplyIdentity(requested, prev *SiteIdentity, prevClientID string) (*SiteIdentity, string, error) {
	identity, clientID, err := applyManagedIdentity(requested, prev, prevClientID)
	if err != nil {
		return nil, "", err
	}
	if identity == nil || identity.Type == "None" {
		return acaIdentityNone(), "", nil
	}
	identity.Type = strings.ReplaceAll(identity.Type, ", ", ",")
	return identity, clientID, nil
}

// acaSecret is one Container Apps secret, of an app or of a job.
type acaSecret struct {
	Name        string
	Value       string
	Identity    string
	KeyVaultURL string
}

func acaAppSecrets(app ContainerApp) []acaSecret {
	if app.Properties.Configuration == nil {
		return nil
	}
	out := make([]acaSecret, 0, len(app.Properties.Configuration.Secrets))
	for _, s := range app.Properties.Configuration.Secrets {
		out = append(out, acaSecret(s))
	}
	return out
}

func acaJobSecrets(job ContainerAppJob) []acaSecret {
	if job.Properties.Configuration == nil {
		return nil
	}
	out := make([]acaSecret, 0, len(job.Properties.Configuration.Secrets))
	for _, s := range job.Properties.Configuration.Secrets {
		out = append(out, acaSecret(s))
	}
	return out
}

// acaFieldInvalid is the message Container Apps answers a field of a request
// it cannot use with.
type acaFieldInvalid struct {
	field, value, reason string
}

func (e acaFieldInvalid) Error() string {
	return fmt.Sprintf("Field '%s' is invalid with details: 'Invalid value: \"%s\": %s'", e.field, e.value, e.reason)
}

func acaSecretInvalid(secret, reason string) error {
	return acaFieldInvalid{field: "configuration.secrets", value: secret, reason: reason}
}

// acaSecretPrincipal is the principal a Key Vault secret reference reads as:
// the system-assigned identity for "system", else the attached user-assigned
// identity whose resource ID the reference names.
func acaSecretPrincipal(identity *SiteIdentity, ref string) (string, bool) {
	if strings.EqualFold(ref, "system") {
		if identity.systemAssigned() && identity.PrincipalID != "" {
			return identity.PrincipalID, true
		}
		return "", false
	}
	if !identity.userAssigned() {
		return "", false
	}
	for rid, uai := range identity.UserAssignedIdentities {
		if uai != nil && strings.EqualFold(rid, ref) {
			return uai.PrincipalID, true
		}
	}
	return "", false
}

// acaKeyVaultSecretValue reads the Key Vault secret keyVaultURL names —
// https://<vault>.vault.azure.net/secrets/<name>[/<version>] — as principal,
// under the vault's permission model.
func acaKeyVaultSecretValue(principal, keyVaultURL string) (string, error) {
	u, err := url.Parse(keyVaultURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the Key Vault URL '%s' is not a secret identifier", keyVaultURL)
	}
	vaultName, _, _ := strings.Cut(u.Hostname(), ".")
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 || len(segs) > 3 || !strings.EqualFold(segs[0], "secrets") || segs[1] == "" {
		return "", fmt.Errorf("the Key Vault URL '%s' is not a secret identifier", keyVaultURL)
	}
	secretName := segs[1]
	vault, found := keyVaultByName(vaultName)
	if !found {
		return "", fmt.Errorf("the vault '%s' could not be found", vaultName)
	}
	if !keyVaultGrantsSecretGet(vault, principal, secretName) {
		return "", fmt.Errorf("the identity was denied the secrets get permission on vault '%s'", vaultName)
	}
	stored, found := keyVaultData.Get(keyVaultSecretKey(vault.Name, secretName))
	if !found || stored.isDeleted() || len(stored.Versions) == 0 {
		return "", fmt.Errorf("the secret '%s' could not be found in vault '%s'", secretName, vaultName)
	}
	version := stored.latest()
	if len(segs) == 3 && segs[2] != "" {
		if version, found = stored.findVersion(segs[2]); !found {
			return "", fmt.Errorf("version '%s' of secret '%s' could not be found in vault '%s'", segs[2], secretName, vaultName)
		}
	}
	if reason := keyVaultSecretVersionUnusable(version.Attributes, time.Now()); reason != "" {
		return "", fmt.Errorf("the secret '%s' %s", secretName, reason)
	}
	return version.Value, nil
}

// acaResolveSecrets resolves each secret's value: the value it carries, or the
// Key Vault secret its keyVaultUrl names, read as the identity it names.
func acaResolveSecrets(identity *SiteIdentity, secrets []acaSecret) (map[string]string, error) {
	values := make(map[string]string, len(secrets))
	for _, s := range secrets {
		if s.KeyVaultURL == "" {
			values[s.Name] = s.Value
			continue
		}
		if s.Identity == "" {
			return nil, acaSecretInvalid(s.Name, "a secret that references Key Vault must name the managed identity that reads it")
		}
		unable := func(reason string) error {
			return acaSecretInvalid(s.Name, fmt.Sprintf("Unable to get value using Managed identity %s for secret %s: %s", s.Identity, s.Name, reason))
		}
		principal, ok := acaSecretPrincipal(identity, s.Identity)
		if !ok {
			return nil, unable("the identity is not assigned to the resource")
		}
		value, err := acaKeyVaultSecretValue(principal, s.KeyVaultURL)
		if err != nil {
			return nil, unable(err.Error())
		}
		values[s.Name] = value
	}
	return values, nil
}

// acaValidateSecretRefs checks that every environment variable's secretRef
// names one of the secrets.
func acaValidateSecretRefs(containers []JobContainer, secrets []acaSecret) error {
	names := make(map[string]bool, len(secrets))
	for _, s := range secrets {
		names[s.Name] = true
	}
	for _, c := range containers {
		for _, ev := range c.Env {
			if ev.SecretRef != "" && !names[ev.SecretRef] {
				return acaFieldInvalid{
					field:  "template.containers." + c.Name + ".env",
					value:  ev.Name,
					reason: "the secretRef '" + ev.SecretRef + "' names no secret of the resource",
				}
			}
		}
	}
	return nil
}

// acaContainerEnv is a container's environment: each variable's value, or the
// value of the secret its secretRef names.
func acaContainerEnv(c JobContainer, secrets map[string]string) map[string]string {
	env := make(map[string]string, len(c.Env)+1)
	for _, ev := range c.Env {
		if ev.SecretRef != "" {
			env[ev.Name] = secrets[ev.SecretRef]
			continue
		}
		env[ev.Name] = ev.Value
	}
	return env
}
