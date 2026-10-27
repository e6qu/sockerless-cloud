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
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KMSTag is the AWS KMS tag wire shape (TagKey/TagValue), unlike Secrets Manager's Key/Value.
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
	// AWS KMS stores and returns the key policy as a string, so GetKeyPolicy
	// round-trips the bytes CreateKey or PutKeyPolicy received.
	PolicyJSON string           `json:"PolicyJSON,omitempty"`
	Grants     []map[string]any `json:"Grants,omitempty"`
	Spec       string           `json:"KeySpec,omitempty"`
	// RotationPeriodInDays defaults to 365 when rotation turns on.
	RotationEnabled      bool `json:"RotationEnabled,omitempty"`
	RotationPeriodInDays int  `json:"RotationPeriodInDays,omitempty"`
	// One entry per completed rotation, automatic or on-demand, for ListKeyRotations.
	Rotations []KMSRotation `json:"Rotations,omitempty"`
	// A new EXTERNAL key starts PendingImport with no material.
	HasImportedMaterial bool `json:"HasImportedMaterial,omitempty"`
	// PrimaryRegion holds the primary; Replicas lists the Regions ReplicateKey
	// replicated to. Every replica shares the KeyId, which AWS prefixes with "mrk-".
	MultiRegion   bool     `json:"MultiRegion,omitempty"`
	PrimaryRegion string   `json:"PrimaryRegion,omitempty"`
	Replicas      []string `json:"Replicas,omitempty"`
	// The last successful cryptographic operation, for GetKeyLastUsage; empty until first use.
	LastUsedOperation string  `json:"LastUsedOperation,omitempty"`
	LastUsedDate      float64 `json:"LastUsedDate,omitempty"`
}

func kmsRecordUsage(keyId, operation string) {
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.LastUsedOperation = operation
		k.LastUsedDate = float64(time.Now().Unix())
	})
}

type KMSRotation struct {
	KeyId        string  `json:"KeyId"`
	RotationDate float64 `json:"RotationDate"`
	RotationType string  `json:"RotationType"` // AUTOMATIC | ON_DEMAND
}

// kmsImportParams is the wrapping key pair GetParametersForImport hands out;
// ImportKeyMaterial unwraps the material with its private key into the key's
// AES-256 material.
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

// kmsKeyMetadata builds KeyMetadata for CreateKey, DescribeKey and ReplicateKey,
// with the algorithm lists AWS attaches to asymmetric and HMAC keys.
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

// kmsDefaultKeyPolicyJSON returns the default key policy AWS KMS gives a key created without one.
func kmsDefaultKeyPolicyJSON() string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"kms:*","Resource":"*"}]}`, awsAccountID())
}

// handleKMSGetKeyPolicy returns the stored policy, or the default key policy.
// AWS KMS supports one policy name, "default".
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

// handleKMSPutKeyPolicy stores the policy under the only policy name, "default".
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

// mergeKMSTags applies TagResource: a new value replaces the existing one for the same key.
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

// Automatic rotation defaults to off for customer managed keys.
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

// handleKMSEnableKeyRotation turns on automatic rotation; RotationPeriodInDays
// defaults to 365, range 90–2560.
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

	keyId := sim.NewUUID()
	if req.MultiRegion {
		// AWS KMS prefixes a multi-region key's ID with "mrk-" and shares it across every replica.
		keyId = "mrk-" + strings.ReplaceAll(keyId, "-", "")
	}
	// CustomerMasterKeySpec is the deprecated name for KeySpec, which older SDKs and
	// Terraform versions still send.
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
	// An EXTERNAL key has no material at creation, so it stays PendingImport until ImportKeyMaterial.
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

	// Generate 256-bit material for an AWS_KMS key; an EXTERNAL key waits for ImportKeyMaterial.
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
	if k, ok := kmsKeysByARN.Lookup(kmsKeys, idOrArn, func(k KMSKey) []string { return []string{k.Arn} }); ok {
		return k.KeyId, true
	}
	return "", false
}

var kmsKeysByARN sim.GenerationIndex[KMSKey]

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
	page, next, pageOK := awsKeyPage(w, kmsBadToken, all, func(it KMSKey) string { return it.KeyId }, req.Marker, req.Limit, 100)
	if !pageOK {
		return
	}
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

// handleKMSEncrypt encrypts with AES-256-GCM; the CiphertextBlob is
// authenticated and bound to its key id.
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
	// Resolve KeyId, which may be an ID, ARN or alias, so the listing filters to that one key.
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
	// Walk the keys and ask listAliasesForKey which aliases point at each.
	for _, key := range kmsKeys.List() {
		if filterKeyID != "" && key.KeyId != filterKeyID {
			continue
		}
		for _, alias := range listAliasesForKey(key.KeyId) {
			out = append(out, map[string]any{
				"AliasName":   alias,
				"AliasArn":    fmt.Sprintf("arn:aws:kms:%s:%s:%s", awsRegion(), awsAccountID(), alias),
				"TargetKeyId": key.KeyId,
			})
		}
	}
	// Stable order so the offset-based Marker pages each alias once.
	page, next, pageOK := awsKeyPage(w, kmsBadToken, out, awsFieldKey("AliasName"), req.Marker, req.Limit, 0)
	if !pageOK {
		return
	}
	resp := map[string]any{"Aliases": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// listAliasesForKey scans the alias names for those pointing at keyId.
func listAliasesForKey(keyId string) []string {
	// kmsAliasNames mirrors the alias keys for iteration; CreateAlias and DeleteAlias maintain it.
	var out []string
	for _, name := range kmsAliasNames.List() {
		if id, ok := kmsAliases.Get(name); ok && id == keyId {
			out = append(out, name)
		}
	}
	return out
}

// kmsAliasNames holds the alias names, which kmsAliases cannot iterate.
var kmsAliasNames sim.Store[string]

// kmsResolveOr404 resolves keyIdRef, writing NotFoundException and returning
// false on a miss.
func kmsResolveOr404(w http.ResponseWriter, r *http.Request, keyIdRef string) (string, bool) {
	keyId, ok := resolveKMSKey(keyIdRef)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Key %q does not exist", keyIdRef)
		return "", false
	}
	return keyId, true
}

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

// AWS KMS returns a key whose deletion is cancelled to Disabled.
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

// AWS KMS requires both the alias and the target key to exist.
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

// crypto/rand stands in for the HSM. The CustomKeyStoreId and Recipient
// (enclave attestation) parameters are not modelled.
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

// AWS KMS supports exactly one policy name, "default".
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

// A key that has never rotated returns an empty list.
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
	page, next, pageOK := awsSeqPage(w, kmsBadToken, rotations, false, req.Marker, req.Limit, 0)
	if !pageOK {
		return
	}
	resp := map[string]any{"Rotations": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleKMSRotateKeyOnDemand records an ON_DEMAND rotation entry; it generates
// no new key material.
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

// handleKMSGetParametersForImport returns an RSA wrapping public key (DER) and an
// import token the simulator records for ImportKeyMaterial; only the HSM custody
// is simulated.
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

// handleKMSImportKeyMaterial requires a token GetParametersForImport issued,
// unwraps the material with that token's RSA private key into the key's AES-256
// material, and enables the key.
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

// handleKMSDeleteImportedKeyMaterial returns the key to PendingImport, unusable
// until material is imported again.
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
