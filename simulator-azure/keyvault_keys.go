package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

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

// keyVaultKeysIn returns the vault's key records, sorted by name.
func keyVaultKeysIn(vault string) []kvKeyStored {
	rows := keyVaultKeysByVault.LookupAll(keyVaultKeys, vault,
		func(k kvKeyStored) []string { return []string{k.Vault} })
	out := append([]kvKeyStored(nil), rows...)
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
		AzureError(w, "BadRequest", "Missing key name", http.StatusBadRequest)
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
	AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
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
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultKeys.Get(keyVaultKeyKey(vault, name)); exists && rec.isDeleted() {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Key %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	version := sim.NewUUID()
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
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultKeys.Get(keyVaultKeyKey(vault, name)); exists && rec.isDeleted() {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Key %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	version := sim.NewUUID()
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, ok := rec.findVersion(version)
	if !ok {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
		name       string
	}
	all := keyVaultKeysIn(vault)
	items := make([]keyItem, 0, len(all))
	for _, k := range all {
		if !k.isDeleted() {
			v, ok := k.latest()
			if !ok {
				continue
			}
			items = append(items, keyItem{ID: buildKVURL(r, vault, "keys", k.Name, ""), Attributes: v.Attributes, Tags: v.Tags, name: k.Name})
		}
	}
	page, next, pageOK := kvPage(w, r, items, func(k keyItem) string { return k.name })
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVListKeyVersions(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	type keyItem struct {
		ID         string            `json:"kid"`
		Attributes KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
		order      string
	}
	versions := rec.Versions
	if len(versions) == 0 {
		if v, ok := rec.latest(); ok {
			versions = []kvKeyVersion{v}
		}
	}
	items := make([]keyItem, 0, len(versions))
	for i, v := range versions {
		items = append(items, keyItem{ID: v.ID, Attributes: v.Attributes, Tags: v.Tags, order: kvVersionOrder(v.Attributes.Created, i)})
	}
	page, next, pageOK := kvPage(w, r, items, func(k keyItem) string { return k.order })
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVUpdateKey(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		Attributes *KeyVaultAttrs    `json:"attributes,omitempty"`
		KeyOps     []string          `json:"key_ops,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Deleted key %q was not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, deletedKeyBundle(rec))
}

func handleKVListDeletedKeys(w http.ResponseWriter, r *http.Request, vault string) {
	deleted := []kvKeyStored{}
	for _, rec := range keyVaultKeysIn(vault) {
		if rec.isDeleted() {
			deleted = append(deleted, rec)
		}
	}
	recs, next, pageOK := kvPage(w, r, deleted, func(k kvKeyStored) string { return k.Name })
	if !pageOK {
		return
	}
	page := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		page = append(page, deletedKeyBundle(rec))
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVPurgeDeletedKey(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || !rec.isDeleted() {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	writeKVBackupBlob(w, blob)
}

func handleKVRestoreKey(w http.ResponseWriter, r *http.Request, vault string) {
	blob, err := readKVBackupBlob(r)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var rec kvKeyStored
	if err := json.Unmarshal(blob, &rec); err != nil {
		AzureError(w, "BadParameter", "invalid key backup blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec.Name == "" {
		AzureError(w, "BadParameter", "key backup blob is missing key name", http.StatusBadRequest)
		return
	}
	if _, exists := keyVaultKeys.Get(keyVaultKeyKey(vault, rec.Name)); exists {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Key %q already exists in this key vault.", rec.Name)
		return
	}
	rec.Vault = vault
	keyVaultKeys.Put(keyVaultKeyKey(vault, rec.Name), rec)
	if latest, ok := rec.latest(); ok {
		sim.WriteJSON(w, http.StatusOK, keyBundle(latest.KeyVaultKey))
		return
	}
	AzureError(w, "BadParameter", "key backup blob does not contain any key versions", http.StatusBadRequest)
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
	AzureError(w, e.code, e.message, e.status)
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Deleted key %q was not found.", name)
		return
	}
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultKeys.Put(key, rec)
	latest, ok := rec.latest()
	if !ok {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	current, ok := rec.latest()
	if !ok {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
	version := sim.NewUUID()
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		LifetimeActions []map[string]any `json:"lifetimeActions"`
		Attributes      map[string]any   `json:"attributes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if expiryTime, _ := body.Attributes["expiryTime"].(string); expiryTime != "" {
		if _, ok := kvAddISO8601Period(time.Now(), expiryTime); !ok {
			AzureErrorf(w, "BadParameter", http.StatusBadRequest,
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

func defaultKVKty(s string) string {
	if s == "" {
		return "RSA"
	}
	return s
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
