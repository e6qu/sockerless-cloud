package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

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
		AzureError(w, "InvalidRequest",
			"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key := keyVaultSecretKey(vault, name)
	rec, exists := keyVaultData.Get(key)
	if exists && rec.isDeleted() {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Secret %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	now := time.Now().Unix()
	version := sim.NewUUID()
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, found := rec.findVersion(version)
	if !found {
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		Tags        map[string]string   `json:"tags,omitempty"`
		ContentType *string             `json:"contentType,omitempty"`
		Attributes  *kvSecretAttrsPatch `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest",
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
	AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
		"Version %q of secret %q was not found.", version, name)
}

func handleKVDeleteSecret(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultSecretKey(vault, name)
	rec, ok := keyVaultData.Get(key)
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
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
		return s.Vault == vault && !s.isDeleted() && len(s.Versions) > 0
	})
	recs, next, pageOK := kvPage(w, r, all, func(s kvSecretStored) string { return s.Name })
	if !pageOK {
		return
	}
	page := make([]kvSecretItem, 0, len(recs))
	for _, s := range recs {
		latest := s.latest()
		page = append(page, kvSecretItem{
			ID:          buildKVURL(r, s.Vault, "secrets", s.Name, ""),
			Attributes:  latest.Attributes,
			Tags:        latest.Tags,
			ContentType: latest.ContentType,
		})
	}
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	type ordered struct {
		order   string
		version kvSecretVersion
	}
	versions := make([]ordered, len(rec.Versions))
	for i, v := range rec.Versions {
		versions[i] = ordered{kvVersionOrder(v.Attributes.Created, i), v}
	}
	got, next, pageOK := kvPage(w, r, versions, func(o ordered) string { return o.order })
	if !pageOK {
		return
	}
	page := make([]kvSecretItem, 0, len(got))
	for _, o := range got {
		page = append(page, kvSecretItem{
			ID:          buildKVURL(r, vault, "secrets", name, o.version.Version),
			Attributes:  o.version.Attributes,
			Tags:        o.version.Tags,
			ContentType: o.version.ContentType,
		})
	}
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"Deleted secret %q was not found.", name)
		return
	}
	emitDeletedSecretBundle(w, r, vault, name, rec)
}

// handleKVListDeletedSecrets returns the paged list of soft-deleted
// secrets in the vault. Path: `/deletedsecrets`.
func handleKVListDeletedSecrets(w http.ResponseWriter, r *http.Request, vault string) {
	all := keyVaultData.Filter(func(s kvSecretStored) bool {
		return s.Vault == vault && s.isDeleted() && len(s.Versions) > 0
	})
	type deletedItem struct {
		kvSecretItem
		RecoveryID         string `json:"recoveryId"`
		DeletedDate        int64  `json:"deletedDate"`
		ScheduledPurgeDate int64  `json:"scheduledPurgeDate"`
	}
	recs, next, pageOK := kvPage(w, r, all, func(s kvSecretStored) string { return s.Name })
	if !pageOK {
		return
	}
	page := make([]deletedItem, 0, len(recs))
	for _, s := range recs {
		latest := s.latest()
		page = append(page, deletedItem{
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"A secret with (name/id) %q was not found in this key vault.", name)
		return
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	writeKVBackupBlob(w, blob)
}

func handleKVRestoreSecret(w http.ResponseWriter, r *http.Request, vault string) {
	blob, err := readKVBackupBlob(r)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var rec kvSecretStored
	if err := json.Unmarshal(blob, &rec); err != nil {
		AzureError(w, "BadParameter", "invalid secret backup blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec.Name == "" {
		AzureError(w, "BadParameter", "secret backup blob is missing secret name", http.StatusBadRequest)
		return
	}
	if _, exists := keyVaultData.Get(keyVaultSecretKey(vault, rec.Name)); exists {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Secret %q already exists in this key vault.", rec.Name)
		return
	}
	rec.Vault = vault
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultData.Put(keyVaultSecretKey(vault, rec.Name), rec)
	if len(rec.Versions) == 0 {
		AzureError(w, "BadParameter", "secret backup blob does not contain any secret versions", http.StatusBadRequest)
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "SecretNotFound", http.StatusNotFound,
			"Deleted secret %q was not found.", name)
		return
	}
	keyVaultData.Delete(key)
	w.WriteHeader(http.StatusNoContent)
}
