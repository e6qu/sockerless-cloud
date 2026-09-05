package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KMS — runner workflows interact with KMS through Secrets Manager
// (KmsKeyId), SSM Parameter Store (SecureString + KeyId), S3 SSE-KMS,
// and direct Encrypt/Decrypt. Without this slice every kms.Encrypt /
// terraform `aws_kms_key` 404s. Wire format follows the JSON protocol
// with `X-Amz-Target: TrentService.<Action>`.

// KMSKey is a customer master key. Real KMS wraps a per-key data key
// in HSM-protected key material; the sim doesn't have an HSM, so the
// "encryption" is a deterministic envelope (`<keyId>:<base64(plain)>`)
// — opaque to SDK callers (treated as bytes), reversible by the sim,
// and round-tripped exactly through the wire shape callers expect.
// KMSTag is the KMS tag wire shape (`TagKey`/`TagValue`), distinct from the
// Secrets Manager tag shape (`Key`/`Value`). CreateKey `--tags`, TagResource,
// and ListResourceTags all speak this shape — reusing SMTag here silently
// dropped tags because the JSON field names don't match.
type KMSTag struct {
	TagKey   string `json:"TagKey"`
	TagValue string `json:"TagValue"`
}

type KMSKey struct {
	KeyId        string   `json:"KeyId"`
	Arn          string   `json:"Arn"`
	Description  string   `json:"Description,omitempty"`
	KeyState     string   `json:"KeyState"`
	KeyUsage     string   `json:"KeyUsage,omitempty"`
	KeyManager   string   `json:"KeyManager,omitempty"`
	Origin       string   `json:"Origin,omitempty"`
	CreationDate float64  `json:"CreationDate,omitempty"`
	Aliases      []string `json:"Aliases,omitempty"`
	Tags         []KMSTag `json:"Tags,omitempty"`
	// PolicyJSON holds the JSON-encoded key policy document. Real KMS
	// stores + returns this as a string (not a parsed object); the sim
	// follows so GetKeyPolicy round-trips byte-identical to what
	// CreateKey + PutKeyPolicy received.
	PolicyJSON string           `json:"PolicyJSON,omitempty"`
	Grants     []map[string]any `json:"Grants,omitempty"`
	Spec       string           `json:"KeySpec,omitempty"`
	// RotationEnabled tracks automatic key rotation (EnableKeyRotation /
	// DisableKeyRotation). RotationPeriodInDays is the rotation cadence,
	// defaulting to 365 when rotation is turned on. Real KMS reports both
	// via GetKeyRotationStatus.
	RotationEnabled      bool `json:"RotationEnabled,omitempty"`
	RotationPeriodInDays int  `json:"RotationPeriodInDays,omitempty"`
	// Rotations records each completed key rotation (automatic or
	// on-demand) so ListKeyRotations can report them. Real KMS surfaces a
	// RotationsListEntry per rotation event.
	Rotations []KMSRotation `json:"Rotations,omitempty"`
	// HasImportedMaterial tracks whether EXTERNAL-origin key material has
	// been imported (ImportKeyMaterial) and not subsequently deleted
	// (DeleteImportedKeyMaterial). A freshly-created EXTERNAL key starts
	// PendingImport with no material.
	HasImportedMaterial bool `json:"HasImportedMaterial,omitempty"`
	// MultiRegion marks a multi-region key (CreateKey MultiRegion=true).
	// PrimaryRegion is the region currently holding the primary; Replicas
	// records the regions ReplicateKey has produced replicas in. The KeyId
	// of a multi-region key is shared across all its regional replicas
	// (real KMS prefixes such IDs with "mrk-").
	MultiRegion   bool     `json:"MultiRegion,omitempty"`
	PrimaryRegion string   `json:"PrimaryRegion,omitempty"`
	Replicas      []string `json:"Replicas,omitempty"`
	// LastUsedOperation / LastUsedDate record the most recent successful
	// cryptographic operation against this key, surfaced by GetKeyLastUsage.
	// Empty until the key is first used for crypto.
	LastUsedOperation string  `json:"LastUsedOperation,omitempty"`
	LastUsedDate      float64 `json:"LastUsedDate,omitempty"`
}

// kmsRecordUsage stamps the last successful cryptographic operation on a key
// so GetKeyLastUsage can report it. Called from the crypto handlers.
func kmsRecordUsage(keyId, operation string) {
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.LastUsedOperation = operation
		k.LastUsedDate = float64(time.Now().Unix())
	})
}

// KMSRotation is one entry in the key's rotation history (ListKeyRotations).
type KMSRotation struct {
	KeyId        string  `json:"KeyId"`
	RotationDate float64 `json:"RotationDate"`
	RotationType string  `json:"RotationType"` // AUTOMATIC | ON_DEMAND
}

// kmsImportParams holds the per-request wrapping keypair handed out by
// GetParametersForImport. ImportKeyMaterial decrypts the wrapped material
// with the RSA private key and uses the decrypted bytes as the CMK's
// AES-256 key material.
type kmsImportParams struct {
	KeyId             string
	ImportToken       string // base64 token
	ValidTo           float64
	PrivateKey        *rsa.PrivateKey
	WrappingAlgorithm string
}

var (
	kmsKeys         sim.Store[KMSKey]
	kmsAliases      sim.Store[string]          // alias -> keyId
	kmsImportTokens sim.Store[kmsImportParams] // importToken -> params
)

func kmsKeyArn(keyId string) string {
	return fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", awsRegion(), awsAccountID(), keyId)
}

func registerKMS(r *AWSRouter, srv *sim.Server) {
	kmsKeys = sim.MakeStore[KMSKey](srv.DB(), "kms_keys")
	kmsAliases = sim.MakeStore[string](srv.DB(), "kms_aliases")
	kmsAliasNames = sim.MakeStore[string](srv.DB(), "kms_alias_names")
	kmsImportTokens = sim.MakeStore[kmsImportParams](srv.DB(), "kms_import_tokens")
	registerKMSKeyMaterial(srv)

	r.Register("TrentService.CreateKey", handleKMSCreateKey)
	r.Register("TrentService.DescribeKey", handleKMSDescribeKey)
	r.Register("TrentService.ListKeys", handleKMSListKeys)
	r.Register("TrentService.ScheduleKeyDeletion", handleKMSScheduleKeyDeletion)
	r.Register("TrentService.Encrypt", handleKMSEncrypt)
	r.Register("TrentService.Decrypt", handleKMSDecrypt)
	r.Register("TrentService.GenerateDataKey", handleKMSGenerateDataKey)
	r.Register("TrentService.CreateAlias", handleKMSCreateAlias)
	r.Register("TrentService.DeleteAlias", handleKMSDeleteAlias)
	r.Register("TrentService.ListAliases", handleKMSListAliases)
	r.Register("TrentService.GetKeyPolicy", handleKMSGetKeyPolicy)
	r.Register("TrentService.PutKeyPolicy", handleKMSPutKeyPolicy)
	r.Register("TrentService.ListResourceTags", handleKMSListResourceTags)
	r.Register("TrentService.TagResource", handleKMSTagResource)
	r.Register("TrentService.UntagResource", handleKMSUntagResource)
	r.Register("TrentService.GetKeyRotationStatus", handleKMSGetKeyRotationStatus)
	r.Register("TrentService.EnableKeyRotation", handleKMSEnableKeyRotation)
	r.Register("TrentService.DisableKeyRotation", handleKMSDisableKeyRotation)

	r.Register("TrentService.EnableKey", handleKMSEnableKey)
	r.Register("TrentService.DisableKey", handleKMSDisableKey)
	r.Register("TrentService.CancelKeyDeletion", handleKMSCancelKeyDeletion)
	r.Register("TrentService.UpdateKeyDescription", handleKMSUpdateKeyDescription)
	r.Register("TrentService.UpdateAlias", handleKMSUpdateAlias)
	r.Register("TrentService.GenerateRandom", handleKMSGenerateRandom)
	r.Register("TrentService.ListKeyPolicies", handleKMSListKeyPolicies)
	r.Register("TrentService.ListKeyRotations", handleKMSListKeyRotations)
	r.Register("TrentService.RotateKeyOnDemand", handleKMSRotateKeyOnDemand)
	r.Register("TrentService.GetParametersForImport", handleKMSGetParametersForImport)
	r.Register("TrentService.ImportKeyMaterial", handleKMSImportKeyMaterial)
	r.Register("TrentService.DeleteImportedKeyMaterial", handleKMSDeleteImportedKeyMaterial)

	registerKMSGrants(r, srv)
	registerKMSCrypto(r, srv)
	registerKMSCustomKeyStores(r, srv)
	registerKMSMultiRegion(r, srv)
}

// kmsKeyMetadata builds the KeyMetadata map real KMS returns from CreateKey /
// DescribeKey / ReplicateKey. For asymmetric and HMAC keys it surfaces the
// algorithm lists (SigningAlgorithms / EncryptionAlgorithms /
// KeyAgreementAlgorithms / MacAlgorithms) the real API attaches, so SDK and
// terraform callers see the same shape.
func kmsKeyMetadata(key KMSKey) map[string]any {
	md := map[string]any{
		"KeyId":        key.KeyId,
		"Arn":          key.Arn,
		"AWSAccountId": awsAccountID(),
		"CreationDate": key.CreationDate,
		"Description":  key.Description,
		"KeyState":     key.KeyState,
		"KeyUsage":     key.KeyUsage,
		"KeyManager":   key.KeyManager,
		"Origin":       key.Origin,
		"KeySpec":      key.Spec,
		"Enabled":      key.KeyState == "Enabled",
		"MultiRegion":  key.MultiRegion,
	}
	if algs := kmsSigningAlgorithmsFor(key.Spec); len(algs) > 0 && key.KeyUsage == "SIGN_VERIFY" {
		md["SigningAlgorithms"] = algs
	}
	if algs := kmsEncryptionAlgorithmsFor(key.Spec, key.KeyUsage); len(algs) > 0 {
		md["EncryptionAlgorithms"] = algs
	}
	if algs := kmsKeyAgreementAlgorithmsFor(key.Spec, key.KeyUsage); len(algs) > 0 {
		md["KeyAgreementAlgorithms"] = algs
	}
	if algs := kmsMacAlgorithmsFor(key.Spec); len(algs) > 0 {
		md["MacAlgorithms"] = algs
	}
	if key.MultiRegion {
		md["MultiRegionConfiguration"] = kmsMultiRegionConfig(key)
	}
	return md
}

// kmsDefaultKeyPolicyJSON returns the default key policy that real AWS KMS
// stamps on a newly-created customer master key when the operator hasn't
// provided one. Matches the canonical "root account allow-all" doc that
// real KMS publishes so terraform's GetKeyPolicy refresh round-trips
// without spurious diffs.
func kmsDefaultKeyPolicyJSON() string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"kms:*","Resource":"*"}]}`, awsAccountID())
}

// handleKMSGetKeyPolicy returns the key policy for a KeyId. Real KMS
// supports only one policy name ("default"); the sim follows suit.
// Returns the custom policy stored on CreateKey / PutKeyPolicy if set,
// otherwise the canonical AWS default key policy.
func handleKMSGetKeyPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId      string `json:"KeyId"`
		PolicyName string `json:"PolicyName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	policyName := req.PolicyName
	if policyName == "" {
		policyName = "default"
	}
	if policyName != "default" {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"No such policy: %s", policyName)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	policy := key.PolicyJSON
	if policy == "" {
		policy = kmsDefaultKeyPolicyJSON()
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Policy":     policy,
		"PolicyName": "default",
	})
}

// handleKMSPutKeyPolicy persists the supplied policy doc on the key.
// Real KMS accepts only one policy name ("default") + the JSON-encoded
// policy string. terraform-provider-aws calls this when the user
// supplies `aws_kms_key.policy` and on every subsequent change.
func handleKMSPutKeyPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId      string `json:"KeyId"`
		PolicyName string `json:"PolicyName"`
		Policy     string `json:"Policy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	if req.PolicyName == "" {
		req.PolicyName = "default"
	}
	if req.PolicyName != "default" {
		AWSErrorf(w, "MalformedPolicyDocumentException", http.StatusBadRequest,
			"Unsupported policy name: %s", req.PolicyName)
		return
	}
	if req.Policy == "" {
		AWSError(w, "MalformedPolicyDocumentException", "Policy is required", http.StatusBadRequest)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	key.PolicyJSON = req.Policy
	kmsKeys.Put(keyId, key)
	kmsPutKeyPolicy(keyId, req.Policy)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSListResourceTags returns the tag set attached to a KMS key.
// terraform-provider-aws calls this after CreateKey to populate `tags`.
func handleKMSListResourceTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	tags := make([]KMSTag, 0, len(key.Tags))
	tags = append(tags, key.Tags...)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Tags":      tags,
		"Truncated": false,
	})
}

// handleKMSTagResource adds or overwrites tags on a key. terraform-provider-aws
// calls this when `aws_kms_key { tags = {...} }` changes after creation, then
// polls ListResourceTags until the tags propagate.
func handleKMSTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string   `json:"KeyId"`
		Tags  []KMSTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.Tags = mergeKMSTags(k.Tags, req.Tags)
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSUntagResource removes the named tag keys from a key.
func handleKMSUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId   string   `json:"KeyId"`
		TagKeys []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	remove := make(map[string]bool, len(req.TagKeys))
	for _, k := range req.TagKeys {
		remove[k] = true
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		kept := k.Tags[:0:0]
		for _, t := range k.Tags {
			if !remove[t.TagKey] {
				kept = append(kept, t)
			}
		}
		k.Tags = kept
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// mergeKMSTags overlays new tags onto existing ones, replacing values for
// duplicate keys (real KMS TagResource semantics).
func mergeKMSTags(existing, incoming []KMSTag) []KMSTag {
	out := make([]KMSTag, 0, len(existing)+len(incoming))
	out = append(out, existing...)
	for _, nt := range incoming {
		replaced := false
		for i := range out {
			if out[i].TagKey == nt.TagKey {
				out[i].TagValue = nt.TagValue
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, nt)
		}
	}
	return out
}

// handleKMSGetKeyRotationStatus returns whether automatic key rotation is
// enabled. Real AWS defaults to false for customer-managed keys.
// terraform-provider-aws calls this after CreateKey.
func handleKMSGetKeyRotationStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	resp := map[string]any{
		"KeyId":              key.KeyId,
		"KeyRotationEnabled": key.RotationEnabled,
	}
	if key.RotationEnabled {
		resp["RotationPeriodInDays"] = key.RotationPeriodInDays
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// kmsDefaultRotationPeriodDays is the rotation cadence AWS applies when
// EnableKeyRotation is called without RotationPeriodInDays.
const kmsDefaultRotationPeriodDays = 365

// handleKMSEnableKeyRotation turns on automatic annual rotation for a key.
// terraform-provider-aws calls this for `aws_kms_key { enable_key_rotation
// = true }`. RotationPeriodInDays is optional (default 365, range 90–2560).
func handleKMSEnableKeyRotation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId                string `json:"KeyId"`
		RotationPeriodInDays *int   `json:"RotationPeriodInDays"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	period := kmsDefaultRotationPeriodDays
	if req.RotationPeriodInDays != nil {
		period = *req.RotationPeriodInDays
		if period < 90 || period > 2560 {
			AWSErrorf(w, "ValidationException", http.StatusBadRequest,
				"RotationPeriodInDays must be between 90 and 2560, got %d", period)
			return
		}
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.RotationEnabled = true
		k.RotationPeriodInDays = period
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSDisableKeyRotation turns off automatic rotation for a key.
func handleKMSDisableKeyRotation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.RotationEnabled = false
		k.RotationPeriodInDays = 0
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleKMSCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description           string   `json:"Description"`
		KeyUsage              string   `json:"KeyUsage"`
		Origin                string   `json:"Origin"`
		KeySpec               string   `json:"KeySpec"`
		CustomerMasterKeySpec string   `json:"CustomerMasterKeySpec"`
		Policy                string   `json:"Policy"`
		Tags                  []KMSTag `json:"Tags"`
		MultiRegion           bool     `json:"MultiRegion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	keyId := generateUUID()
	if req.MultiRegion {
		// Real KMS gives multi-region keys an ID prefixed with "mrk-"; the
		// same ID is shared across every regional replica.
		keyId = "mrk-" + strings.ReplaceAll(keyId, "-", "")
	}
	// CustomerMasterKeySpec is the legacy name for KeySpec; honor it when
	// KeySpec is absent (older SDKs / terraform versions still send it).
	if req.KeySpec == "" && req.CustomerMasterKeySpec != "" {
		req.KeySpec = req.CustomerMasterKeySpec
	}
	if req.KeyUsage == "" {
		req.KeyUsage = "ENCRYPT_DECRYPT"
	}
	if req.Origin == "" {
		req.Origin = "AWS_KMS"
	}
	if req.KeySpec == "" {
		req.KeySpec = "SYMMETRIC_DEFAULT"
	}
	// An EXTERNAL-origin key has no key material at creation; real KMS
	// puts it in PendingImport until ImportKeyMaterial runs.
	keyState := "Enabled"
	if req.Origin == "EXTERNAL" {
		keyState = "PendingImport"
	}
	key := KMSKey{
		KeyId:        keyId,
		Arn:          kmsKeyArn(keyId),
		Description:  req.Description,
		KeyState:     keyState,
		KeyUsage:     req.KeyUsage,
		KeyManager:   "CUSTOMER",
		Origin:       req.Origin,
		Spec:         req.KeySpec,
		CreationDate: float64(time.Now().Unix()),
		Tags:         req.Tags,
		PolicyJSON:   req.Policy,
		MultiRegion:  req.MultiRegion,
	}
	if req.MultiRegion {
		key.PrimaryRegion = awsRegion()
	}
	kmsKeys.Put(keyId, key)

	// AWS_KMS-origin keys get real 256-bit key material generated locally.
	// EXTERNAL-origin keys remain without material until ImportKeyMaterial.
	if req.Origin != "EXTERNAL" {
		if _, err := kmsGenerateKeyMaterial(keyId); err != nil {
			kmsKeys.Delete(keyId)
			AWSError(w, "DependencyTimeoutException", "failed to generate key material", http.StatusInternalServerError)
			return
		}
	}
	kmsPutKeyPolicy(keyId, req.Policy)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyMetadata": kmsKeyMetadata(key),
	})
}

// resolveKMSKey accepts either a plain KeyId, a full ARN, or an alias
// (`alias/<name>`) and returns the canonical KeyId.
func resolveKMSKey(idOrArn string) (string, bool) {
	if _, ok := kmsKeys.Get(idOrArn); ok {
		return idOrArn, true
	}
	if strings.HasPrefix(idOrArn, "alias/") {
		if keyId, ok := kmsAliases.Get(idOrArn); ok {
			return keyId, true
		}
		return "", false
	}
	for _, k := range kmsKeys.List() {
		if k.Arn == idOrArn {
			return k.KeyId, true
		}
	}
	return "", false
}

func handleKMSDescribeKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyMetadata": kmsKeyMetadata(key),
	})
}

func handleKMSListKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Marker string `json:"Marker"`
		Limit  int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidMarkerException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := kmsKeys.List()
	sortBy(all, func(k KMSKey) string { return k.KeyId })
	page, next := awsPage(all, req.Marker, req.Limit, 100)
	out := make([]map[string]any, 0, len(page))
	for _, k := range page {
		out = append(out, map[string]any{
			"KeyId":  k.KeyId,
			"KeyArn": k.Arn,
		})
	}
	resp := map[string]any{"Keys": out, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleKMSScheduleKeyDeletion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId               string `json:"KeyId"`
		PendingWindowInDays int32  `json:"PendingWindowInDays"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	deletionDate := float64(time.Now().AddDate(0, 0, int(req.PendingWindowInDays)).Unix())
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.KeyState = "PendingDeletion"
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":        keyId,
		"DeletionDate": deletionDate,
		"KeyState":     "PendingDeletion",
	})
}

// handleKMSEncrypt encrypts plaintext under a CMK using real AES-256-GCM.
// The returned CiphertextBlob is authenticated and bound to the source key id.
func handleKMSEncrypt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId     string `json:"KeyId"`
		Plaintext []byte `json:"Plaintext"` // base64-decoded by the SDK
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	ciphertextBlob, ok := kmsEncryptBytes(keyId, req.Plaintext)
	if !ok {
		AWSError(w, "DependencyTimeoutException", "failed to encrypt", http.StatusInternalServerError)
		return
	}
	kmsRecordUsage(keyId, "Encrypt")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":          kmsKeyArn(keyId),
		"CiphertextBlob": ciphertextBlob, // SDK base64-encodes on the wire
	})
}

// handleKMSDecrypt decrypts a CiphertextBlob produced by Encrypt or
// GenerateDataKey. It validates the authentication tag and the source key id,
// and rejects disabled or pending-deletion keys.
func handleKMSDecrypt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId          string `json:"KeyId"`
		CiphertextBlob []byte `json:"CiphertextBlob"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	srcKeyId, plaintext, ok := kmsDecryptBytes(req.CiphertextBlob)
	if !ok {
		AWSErrorf(w, "InvalidCiphertextException", http.StatusBadRequest,
			"The ciphertext blob is not in the expected format.")
		return
	}
	key, exists := kmsKeys.Get(srcKeyId)
	if !exists {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", srcKeyId)
		return
	}
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(srcKeyId))
		return
	}
	if req.KeyId != "" {
		resolved, ok := resolveKMSKey(req.KeyId)
		if !ok || resolved != srcKeyId {
			AWSErrorf(w, "IncorrectKeyException", http.StatusBadRequest,
				"The key ID in the request does not match the key ID of the ciphertext.")
			return
		}
	}
	kmsRecordUsage(srcKeyId, "Decrypt")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":     kmsKeyArn(srcKeyId),
		"Plaintext": plaintext, // SDK base64-encodes on the wire
	})
}

func handleKMSGenerateDataKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId         string `json:"KeyId"`
		NumberOfBytes int    `json:"NumberOfBytes"`
		KeySpec       string `json:"KeySpec"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", req.KeyId)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	size := req.NumberOfBytes
	if size == 0 {
		switch req.KeySpec {
		case "AES_128":
			size = 16
		default: // "" or AES_256
			size = 32
		}
	}
	plaintext := make([]byte, size)
	if _, err := rand.Read(plaintext); err != nil {
		AWSError(w, "DependencyTimeoutException", "failed to generate random data key", http.StatusInternalServerError)
		return
	}
	ciphertextBlob, ok := kmsEncryptBytes(keyId, plaintext)
	if !ok {
		AWSError(w, "DependencyTimeoutException", "failed to encrypt data key", http.StatusInternalServerError)
		return
	}
	kmsRecordUsage(keyId, "GenerateDataKey")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":          kmsKeyArn(keyId),
		"Plaintext":      plaintext,
		"CiphertextBlob": ciphertextBlob,
	})
}

func handleKMSCreateAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AliasName   string `json:"AliasName"`
		TargetKeyId string `json:"TargetKeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(req.AliasName, "alias/") {
		AWSError(w, "ValidationException",
			"AliasName must start with 'alias/'", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.TargetKeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"TargetKeyId %q does not exist", req.TargetKeyId)
		return
	}
	if _, exists := kmsAliases.Get(req.AliasName); exists {
		AWSErrorf(w, "AlreadyExistsException", http.StatusBadRequest,
			"Alias %q already exists", req.AliasName)
		return
	}
	kmsAliases.Put(req.AliasName, keyId)
	kmsAliasNames.Put(req.AliasName, req.AliasName)
	w.WriteHeader(http.StatusOK)
}

func handleKMSDeleteAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AliasName string `json:"AliasName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	kmsAliases.Delete(req.AliasName)
	kmsAliasNames.Delete(req.AliasName)
	w.WriteHeader(http.StatusOK)
}

func handleKMSListAliases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId  string `json:"KeyId"`
		Limit  int    `json:"Limit"`
		Marker string `json:"Marker"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	// When KeyId is set, resolve it (it may be an ARN or key id) so aliases
	// filter to that one key, matching the real ListAliases KeyId parameter.
	var filterKeyID string
	if req.KeyId != "" {
		keyID, ok := resolveKMSKey(req.KeyId)
		if !ok {
			AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
				"Key %q does not exist", req.KeyId)
			return
		}
		filterKeyID = keyID
	}
	out := make([]map[string]any, 0)
	// `sim.Store` doesn't expose key iteration; instead, walk every
	// known key and ask listAliasesForKey which alias names point at
	// it. The alias store is bounded in practice (one alias per key
	// in most operator setups) so the O(keys × aliases) scan is fine.
	for _, key := range kmsKeys.List() {
		if filterKeyID != "" && key.KeyId != filterKeyID {
			continue
		}
		// For each key, find aliases pointing at it.
		for _, alias := range listAliasesForKey(key.KeyId) {
			out = append(out, map[string]any{
				"AliasName":   alias,
				"AliasArn":    fmt.Sprintf("arn:aws:kms:%s:%s:%s", awsRegion(), awsAccountID(), alias),
				"TargetKeyId": key.KeyId,
			})
		}
	}
	// Stable order so the offset-based Marker pages each alias once.
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["AliasName"].(string)
		b, _ := out[j]["AliasName"].(string)
		return a < b
	})
	page, next := awsPageExplicit(out, req.Marker, req.Limit)
	resp := map[string]any{"Aliases": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// listAliasesForKey scans the alias store for entries pointing at
// keyId. The sim's Store doesn't expose key iteration, but the alias
// table is small enough that a snapshot scan is fine.
func listAliasesForKey(keyId string) []string {
	// kmsAliasNames mirrors the alias keys for iteration. It's
	// maintained alongside kmsAliases in handleKMSCreateAlias and
	// handleKMSDeleteAlias.
	var out []string
	for _, name := range kmsAliasNames.List() {
		if id, ok := kmsAliases.Get(name); ok && id == keyId {
			out = append(out, name)
		}
	}
	return out
}

// kmsAliasNames holds the alias names for iteration. Real KMS exposes
// `ListAliases` paginated; the sim returns all at once.
var kmsAliasNames sim.Store[string]

// kmsResolveOr404 decodes a single-KeyId request body and resolves the
// key, writing the canonical KMS NotFoundException on miss. Returns the
// resolved KeyId and ok=false (after writing the error) when not found.
func kmsResolveOr404(w http.ResponseWriter, r *http.Request, keyIdRef string) (string, bool) {
	keyId, ok := resolveKMSKey(keyIdRef)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", keyIdRef)
		return "", false
	}
	return keyId, true
}

// handleKMSEnableKey moves a key to the Enabled state. terraform-provider-aws
// calls this for `aws_kms_key { is_enabled = true }`.
func handleKMSEnableKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) { k.KeyState = "Enabled" })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSDisableKey moves a key to the Disabled state.
func handleKMSDisableKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) { k.KeyState = "Disabled" })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSCancelKeyDeletion cancels a scheduled deletion, returning the key
// to the Disabled state (real KMS disables a key whose deletion is cancelled).
// Returns the KeyId.
func handleKMSCancelKeyDeletion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if key.KeyState != "PendingDeletion" {
		AWSErrorf(w, "KMSInvalidStateException", http.StatusBadRequest,
			"%s is not pending deletion.", kmsKeyArn(keyId))
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) { k.KeyState = "Disabled" })
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId": keyId,
	})
}

// handleKMSUpdateKeyDescription updates the free-text description.
func handleKMSUpdateKeyDescription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId       string `json:"KeyId"`
		Description string `json:"Description"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) { k.Description = req.Description })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSUpdateAlias re-points an existing alias at a different key.
// Real KMS requires the alias to already exist and the target key to exist.
func handleKMSUpdateAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AliasName   string `json:"AliasName"`
		TargetKeyId string `json:"TargetKeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, exists := kmsAliases.Get(req.AliasName); !exists {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Alias %q does not exist", req.AliasName)
		return
	}
	keyId, ok := resolveKMSKey(req.TargetKeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"TargetKeyId %q does not exist", req.TargetKeyId)
		return
	}
	kmsAliases.Put(req.AliasName, keyId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSGenerateRandom returns NumberOfBytes of cryptographically random
// data. Real KMS sources this from its HSM; the sim uses crypto/rand, which
// is real randomness — no fakery. The CustomKeyStoreId / Recipient
// (enclave-attestation) paths aren't modeled; a plain request is honored.
func handleKMSGenerateRandom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NumberOfBytes int `json:"NumberOfBytes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.NumberOfBytes < 1 || req.NumberOfBytes > 1024 {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"NumberOfBytes must be between 1 and 1024, got %d", req.NumberOfBytes)
		return
	}
	buf := make([]byte, req.NumberOfBytes)
	if _, err := rand.Read(buf); err != nil {
		AWSError(w, "KMSInternalException", "failed to generate random bytes", http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Plaintext": buf, // SDK base64-encodes on the wire
	})
}

// handleKMSListKeyPolicies returns the policy names attached to a key. Real
// KMS supports exactly one policy name ("default"); terraform/SDK callers
// enumerate via this op.
func handleKMSListKeyPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := kmsResolveOr404(w, r, req.KeyId); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"PolicyNames": []string{"default"},
		"Truncated":   false,
	})
}

// handleKMSListKeyRotations returns the rotation history of a key (each
// automatic or on-demand rotation recorded so far). A key that has never
// rotated returns an empty list, matching real KMS.
func handleKMSListKeyRotations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId  string `json:"KeyId"`
		Limit  int    `json:"Limit"`
		Marker string `json:"Marker"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	rotations := make([]map[string]any, 0, len(key.Rotations))
	for _, rot := range key.Rotations {
		rotations = append(rotations, map[string]any{
			"KeyId":        rot.KeyId,
			"RotationDate": rot.RotationDate,
			"RotationType": rot.RotationType,
		})
	}
	page, next := awsPageExplicit(rotations, req.Marker, req.Limit)
	resp := map[string]any{"Rotations": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleKMSRotateKeyOnDemand performs an immediate rotation of a symmetric
// key, recording an ON_DEMAND rotation entry. Real KMS rotates the backing
// key material and returns the KeyId.
func handleKMSRotateKeyOnDemand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.Rotations = append(k.Rotations, KMSRotation{
			KeyId:        keyId,
			RotationDate: float64(time.Now().Unix()),
			RotationType: "ON_DEMAND",
		})
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId": keyId,
	})
}

// handleKMSGetParametersForImport hands back a real RSA wrapping public key
// (DER-encoded) plus an opaque import token. A real client encrypts its key
// material against this public key; the sim records the token so
// ImportKeyMaterial can validate the round-trip. The wrapping key is genuine
// RSA — no fakery — only the HSM custody is simulated.
func handleKMSGetParametersForImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId             string `json:"KeyId"`
		WrappingAlgorithm string `json:"WrappingAlgorithm"`
		WrappingKeySpec   string `json:"WrappingKeySpec"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if key.Origin != "EXTERNAL" {
		AWSErrorf(w, "UnsupportedOperationException", http.StatusBadRequest,
			"%s origin is not EXTERNAL; import is not supported.", kmsKeyArn(keyId))
		return
	}
	bits := 2048
	switch req.WrappingKeySpec {
	case "RSA_3072":
		bits = 3072
	case "RSA_4096":
		bits = 4096
	}
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		AWSError(w, "KMSInternalException", "failed to generate wrapping key", http.StatusInternalServerError)
		return
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		AWSError(w, "KMSInternalException", "failed to marshal wrapping key", http.StatusInternalServerError)
		return
	}
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	importToken := base64.StdEncoding.EncodeToString(tokenBytes)
	validTo := float64(time.Now().Add(24 * time.Hour).Unix())
	kmsImportTokens.Put(importToken, kmsImportParams{
		KeyId:             keyId,
		ImportToken:       importToken,
		ValidTo:           validTo,
		PrivateKey:        priv,
		WrappingAlgorithm: req.WrappingAlgorithm,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":             keyId,
		"ImportToken":       []byte(importToken), // SDK base64-encodes on the wire
		"PublicKey":         pubDER,              // SDK base64-encodes on the wire
		"ParametersValidTo": validTo,
	})
}

// handleKMSImportKeyMaterial accepts wrapped key material for an EXTERNAL key.
// The import token must match one previously issued by
// GetParametersForImport. The sim decrypts the wrapped material with the RSA
// private key stored alongside the token and uses the decrypted 32 bytes as
// the CMK's AES-256 key material, then moves the key to Enabled.
func handleKMSImportKeyMaterial(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId                string  `json:"KeyId"`
		ImportToken          []byte  `json:"ImportToken"`
		EncryptedKeyMaterial []byte  `json:"EncryptedKeyMaterial"`
		ExpirationModel      string  `json:"ExpirationModel"`
		ValidTo              float64 `json:"ValidTo"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	if len(req.EncryptedKeyMaterial) == 0 {
		AWSError(w, "ValidationException", "EncryptedKeyMaterial is required", http.StatusBadRequest)
		return
	}
	params, ok := kmsImportTokens.Get(string(req.ImportToken))
	if !ok || params.KeyId != keyId {
		AWSErrorf(w, "InvalidImportTokenException", http.StatusBadRequest,
			"The import token is expired or does not match the key.")
		return
	}
	if params.PrivateKey == nil {
		AWSError(w, "KMSInternalException", "import token has no associated wrapping key", http.StatusInternalServerError)
		return
	}
	if len(req.EncryptedKeyMaterial) > params.PrivateKey.Size() {
		AWSError(w, "ValidationException", "EncryptedKeyMaterial exceeds wrapping key size", http.StatusBadRequest)
		return
	}
	plaintext, err := kmsDecryptImportedKeyMaterial(params.PrivateKey, params.WrappingAlgorithm, req.EncryptedKeyMaterial)
	if err != nil {
		AWSError(w, "ValidationException", "failed to decrypt key material", http.StatusBadRequest)
		return
	}
	if len(plaintext) != kmsKeyMaterialLen {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Imported key material must be %d bytes, got %d", kmsKeyMaterialLen, len(plaintext))
		return
	}
	kmsKeyMaterial.Put(keyId, plaintext)
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.HasImportedMaterial = true
		k.KeyState = "Enabled"
	})
	kmsImportTokens.Delete(string(req.ImportToken))
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId": keyId,
	})
}

// kmsDecryptImportedKeyMaterial unwraps key material that was encrypted with
// the RSA public key returned by GetParametersForImport. It honors the wrapping
// algorithm recorded on the import token (RSAES_OAEP_SHA_256, RSAES_OAEP_SHA_1,
// or RSAES_PKCS1_V1_5).
func kmsDecryptImportedKeyMaterial(priv *rsa.PrivateKey, alg string, ciphertext []byte) ([]byte, error) {
	switch alg {
	case "RSAES_OAEP_SHA_256":
		return rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, ciphertext, nil)
	case "RSAES_OAEP_SHA_1":
		return rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, ciphertext, nil)
	case "RSAES_PKCS1_V1_5", "":
		return rsa.DecryptPKCS1v15(rand.Reader, priv, ciphertext)
	default:
		return nil, fmt.Errorf("unsupported wrapping algorithm: %s", alg)
	}
}

// handleKMSDeleteImportedKeyMaterial deletes the imported key material,
// returning the key to PendingImport. Real KMS makes the key unusable until
// material is re-imported. Returns the KeyId.
func handleKMSDeleteImportedKeyMaterial(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.HasImportedMaterial = false
		k.KeyState = "PendingImport"
	})
	kmsDeleteKeyMaterial(keyId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId": keyId,
	})
}
