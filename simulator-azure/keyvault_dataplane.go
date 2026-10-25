package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

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
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
			AzureError(w, "BadRequest", "Missing secret name", http.StatusBadRequest)
			return
		}
		name := segs[1]
		if len(segs) == 3 && segs[2] == "versions" {
			if r.Method != http.MethodGet {
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVListSecretVersions(w, r, vault, name)
			return
		}
		if len(segs) == 3 && segs[2] == "backup" {
			if r.Method != http.MethodPost {
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
					AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "secrets" || path == "secrets/":
		if r.Method != http.MethodGet {
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListSecrets(w, r, vault)
	case path == "deletedsecrets" || path == "deletedsecrets/":
		if r.Method != http.MethodGet {
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListDeletedSecrets(w, r, vault)
	case strings.HasPrefix(path, "deletedsecrets/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["deletedsecrets", "<name>"]            → soft-deleted secret Get / Purge
		//   ["deletedsecrets", "<name>", "recover"] → POST recover
		if len(segs) < 2 {
			AzureError(w, "BadRequest", "Missing secret name", http.StatusBadRequest)
			return
		}
		name := segs[1]
		if len(segs) == 3 && segs[2] == "recover" {
			if r.Method != http.MethodPost {
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "deletedkeys" || path == "deletedkeys/":
		if r.Method != http.MethodGet {
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListDeletedKeys(w, r, vault)
	case strings.HasPrefix(path, "deletedkeys/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["deletedkeys", "<name>"]            → soft-deleted key Get / Purge
		//   ["deletedkeys", "<name>", "recover"] → POST recover
		if len(segs) < 2 {
			AzureError(w, "BadRequest", "Missing key name", http.StatusBadRequest)
			return
		}
		if len(segs) == 3 && segs[2] == "recover" {
			if r.Method != http.MethodPost {
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "deletedcertificates" || path == "deletedcertificates/":
		if r.Method != http.MethodGet {
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVListDeletedCertificates(w, r, vault)
	case strings.HasPrefix(path, "deletedcertificates/"):
		segs := strings.Split(path, "/")
		// segs:
		//   ["deletedcertificates", "<name>"]            → soft-deleted certificate Get / Purge
		//   ["deletedcertificates", "<name>", "recover"] → POST recover
		if len(segs) < 2 {
			AzureError(w, "BadRequest", "Missing certificate name", http.StatusBadRequest)
			return
		}
		if len(segs) == 3 && segs[2] == "recover" {
			if r.Method != http.MethodPost {
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
	case path == "rng" || path == "rng/":
		// GetRandomBytes (POST /rng): random bytes drawn from the platform's
		// CSPRNG, the operation a managed HSM's RNG serves.
		if r.Method != http.MethodPost {
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
			return
		}
		handleKVGetRandomBytes(w, r)
	case strings.HasPrefix(path, "keys/") || path == "keys":
		handleKVKey(w, r, vault, path)
	case strings.HasPrefix(path, "certificates/") || path == "certificates":
		handleKVCertificate(w, r, vault, path)
	default:
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Key Vault data plane path %q not implemented", path)
	}
}

func kvVersionFromID(id string) string {
	id = strings.TrimRight(id, "/")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return ""
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
