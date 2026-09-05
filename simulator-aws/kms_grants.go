package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sort"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KMS grants + secondary crypto ops. AWS services (ECS, ECR,
// DynamoDB, S3) create grants on customer-managed CMKs to use them for
// encryption at rest; the Terraform `aws_kms_grant` resource is the explicit
// surface. GenerateDataKeyWithoutPlaintext and ReEncrypt are the remaining
// data-key/rotation crypto ops layered on the existing kms-sim envelope.

type KMSGrant struct {
	GrantId           string         `json:"GrantId"`
	KeyId             string         `json:"KeyId"`
	Name              string         `json:"Name,omitempty"`
	GranteePrincipal  string         `json:"GranteePrincipal"`
	RetiringPrincipal string         `json:"RetiringPrincipal,omitempty"`
	Operations        []string       `json:"Operations"`
	Constraints       map[string]any `json:"Constraints,omitempty"`
	CreationDate      float64        `json:"CreationDate"`
	IssuingAccount    string         `json:"IssuingAccount"`
}

var kmsGrants sim.Store[KMSGrant]

func registerKMSGrants(r *AWSRouter, srv *sim.Server) {
	kmsGrants = sim.MakeStore[KMSGrant](srv.DB(), "kms_grants")
	r.Register("TrentService.CreateGrant", handleKMSCreateGrant)
	r.Register("TrentService.ListGrants", handleKMSListGrants)
	r.Register("TrentService.RevokeGrant", handleKMSRevokeGrant)
	r.Register("TrentService.GenerateDataKeyWithoutPlaintext", handleKMSGenerateDataKeyWithoutPlaintext)
	r.Register("TrentService.ReEncrypt", handleKMSReEncrypt)
}

func handleKMSCreateGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId             string         `json:"KeyId"`
		GranteePrincipal  string         `json:"GranteePrincipal"`
		RetiringPrincipal string         `json:"RetiringPrincipal"`
		Operations        []string       `json:"Operations"`
		Constraints       map[string]any `json:"Constraints"`
		Name              string         `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest, "Key %q does not exist", req.KeyId)
		return
	}
	if req.GranteePrincipal == "" {
		AWSError(w, "ValidationException", "GranteePrincipal is required", http.StatusBadRequest)
		return
	}
	grant := KMSGrant{
		GrantId:           generateUUID(),
		KeyId:             keyId,
		Name:              req.Name,
		GranteePrincipal:  req.GranteePrincipal,
		RetiringPrincipal: req.RetiringPrincipal,
		Operations:        req.Operations,
		Constraints:       req.Constraints,
		CreationDate:      float64(time.Now().Unix()),
		IssuingAccount:    "arn:aws:iam::" + awsAccountID() + ":root",
	}
	kmsGrants.Put(grant.GrantId, grant)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"GrantId":    grant.GrantId,
		"GrantToken": kmsGrantToken(),
	})
}

func handleKMSListGrants(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId            string `json:"KeyId"`
		GrantId          string `json:"GrantId"`
		GranteePrincipal string `json:"GranteePrincipal"`
		Limit            int    `json:"Limit"`
		Marker           string `json:"Marker"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := resolveKMSKey(req.KeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest, "Key %q does not exist", req.KeyId)
		return
	}
	var matched []KMSGrant
	for _, g := range kmsGrants.List() {
		if g.KeyId != keyId {
			continue
		}
		if req.GrantId != "" && g.GrantId != req.GrantId {
			continue
		}
		if req.GranteePrincipal != "" && g.GranteePrincipal != req.GranteePrincipal {
			continue
		}
		matched = append(matched, g)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].GrantId < matched[j].GrantId })
	page, next := awsPage(matched, req.Marker, req.Limit, 100)
	out := map[string]any{
		"Grants":    page,
		"Truncated": next != "",
	}
	if next != "" {
		out["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKMSRevokeGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId   string `json:"KeyId"`
		GrantId string `json:"GrantId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := resolveKMSKey(req.KeyId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest, "Key %q does not exist", req.KeyId)
		return
	}
	if _, ok := kmsGrants.Get(req.GrantId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest, "Grant %q does not exist", req.GrantId)
		return
	}
	kmsGrants.Delete(req.GrantId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleKMSGenerateDataKeyWithoutPlaintext(w http.ResponseWriter, r *http.Request) {
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
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest, "Key %q does not exist", req.KeyId)
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	size := req.NumberOfBytes
	if size == 0 {
		if req.KeySpec == "AES_128" {
			size = 16
		} else {
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
	// Real GenerateDataKeyWithoutPlaintext returns only the encrypted key — the
	// plaintext is never put on the wire.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":          kmsKeyArn(keyId),
		"CiphertextBlob": ciphertextBlob,
	})
}

func handleKMSReEncrypt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CiphertextBlob   []byte `json:"CiphertextBlob"`
		DestinationKeyId string `json:"DestinationKeyId"`
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
	srcKey, exists := kmsKeys.Get(srcKeyId)
	if !exists {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest, "Source key %q does not exist", srcKeyId)
		return
	}
	if !kmsIsUsable(srcKey) {
		kmsCryptoDisabledError(w, kmsKeyArn(srcKeyId))
		return
	}
	destKeyId, ok := resolveKMSKey(req.DestinationKeyId)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
			"Destination key %q does not exist", req.DestinationKeyId)
		return
	}
	destKey, _ := kmsKeys.Get(destKeyId)
	if !kmsIsUsable(destKey) {
		kmsCryptoDisabledError(w, kmsKeyArn(destKeyId))
		return
	}
	newBlob, ok := kmsEncryptBytes(destKeyId, plaintext)
	if !ok {
		AWSError(w, "DependencyTimeoutException", "failed to re-encrypt", http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"CiphertextBlob": newBlob,
		"KeyId":          kmsKeyArn(destKeyId),
		"SourceKeyId":    kmsKeyArn(srcKeyId),
	})
}

func kmsGrantToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}
