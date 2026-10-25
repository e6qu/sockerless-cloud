package main

import (
	"crypto"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ecCurveByJWKName maps JWK curve names (P-256, P-384, P-521) to
// crypto/elliptic.Curve. Returns (nil, false) for unknown values.
func ecCurveByJWKName(name string) (elliptic.Curve, bool) {
	switch name {
	case "P-256":
		return elliptic.P256(), true
	case "P-384":
		return elliptic.P384(), true
	case "P-521":
		return elliptic.P521(), true
	}
	return nil, false
}

// handleKVGetRandomBytes answers GetRandomBytes (POST /rng): `count` bytes
// drawn from crypto/rand, base64url-encoded, within the documented 1–128
// range.
func handleKVGetRandomBytes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Count *int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Count == nil {
		AzureError(w, "BadParameter", "The 'count' property is required.", http.StatusBadRequest)
		return
	}
	if *body.Count < 1 || *body.Count > 128 {
		AzureErrorf(w, "BadParameter", http.StatusBadRequest,
			"The requested number of random bytes must be between 1 and 128; %d was requested.", *body.Count)
		return
	}
	buf := make([]byte, *body.Count)
	if _, err := rand.Read(buf); err != nil {
		AzureError(w, "InternalServerError", "failed to draw random bytes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": base64.RawURLEncoding.EncodeToString(buf),
	})
}

// kvIsCryptoVerb reports whether the path segment names a Key Vault key
// cryptographic operation (the ops handleKVCryptoKey dispatches).
func kvIsCryptoVerb(verb string) bool {
	switch verb {
	case "encrypt", "decrypt", "sign", "verify", "wrapkey", "unwrapkey":
		return true
	}
	return false
}

func handleKVCryptoKey(w http.ResponseWriter, r *http.Request, vault, name, version, operation string) {
	rec, ok := keyVaultKeys.Get(keyVaultKeyKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"A key with (name/id) %q was not found in this key vault.", name)
		return
	}
	keyVersion, ok := rec.findVersion(version)
	if !ok {
		AzureErrorf(w, "KeyNotFound", http.StatusNotFound,
			"Version %q of key %q was not found.", version, name)
		return
	}
	privateKey, err := rsaPrivateKeyFromPEM(keyVersion.PrivateKeyPEM)
	if err != nil {
		AzureError(w, "NotImplemented",
			"This key type does not support the requested local cryptographic operation.", http.StatusNotImplemented)
		return
	}
	switch operation {
	case "sign":
		handleKVRSAKeySign(w, r, keyVersion, privateKey)
	case "verify":
		handleKVRSAKeyVerify(w, r, privateKey)
	case "encrypt", "wrapkey":
		handleKVRSAKeyEncrypt(w, r, keyVersion, &privateKey.PublicKey)
	case "decrypt", "unwrapkey":
		handleKVRSAKeyDecrypt(w, r, keyVersion, privateKey)
	default:
		AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
	}
}

func handleKVRSAKeySign(w http.ResponseWriter, r *http.Request, version kvKeyVersion, privateKey *rsa.PrivateKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	digest, err := decodeKVURLBytes(body.Value)
	if err != nil {
		AzureError(w, "BadParameter", "invalid value: "+err.Error(), http.StatusBadRequest)
		return
	}
	hashAlg, pss, err := signatureHash(body.Algorithm)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var sig []byte
	if pss {
		sig, err = rsa.SignPSS(rand.Reader, privateKey, hashAlg, digest, nil)
	} else {
		sig, err = rsa.SignPKCS1v15(rand.Reader, privateKey, hashAlg, digest)
	}
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	writeKVOperationResult(w, version.ID, sig)
}

func handleKVRSAKeyVerify(w http.ResponseWriter, r *http.Request, privateKey *rsa.PrivateKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Digest    string `json:"digest"`
		Signature string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	digest, err := decodeKVURLBytes(body.Digest)
	if err != nil {
		AzureError(w, "BadParameter", "invalid digest: "+err.Error(), http.StatusBadRequest)
		return
	}
	signature, err := decodeKVURLBytes(body.Signature)
	if err != nil {
		AzureError(w, "BadParameter", "invalid signature: "+err.Error(), http.StatusBadRequest)
		return
	}
	hashAlg, pss, err := signatureHash(body.Algorithm)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	if pss {
		err = rsa.VerifyPSS(&privateKey.PublicKey, hashAlg, digest, signature, nil)
	} else {
		err = rsa.VerifyPKCS1v15(&privateKey.PublicKey, hashAlg, digest, signature)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": err == nil})
}

func handleKVRSAKeyEncrypt(w http.ResponseWriter, r *http.Request, version kvKeyVersion, publicKey *rsa.PublicKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	plain, err := decodeKVURLBytes(body.Value)
	if err != nil {
		AzureError(w, "BadParameter", "invalid value: "+err.Error(), http.StatusBadRequest)
		return
	}
	ciphertext, err := rsaEncrypt(body.Algorithm, publicKey, plain)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	writeKVOperationResult(w, version.ID, ciphertext)
}

func handleKVRSAKeyDecrypt(w http.ResponseWriter, r *http.Request, version kvKeyVersion, privateKey *rsa.PrivateKey) {
	var body struct {
		Algorithm string `json:"alg"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	ciphertext, err := decodeKVURLBytes(body.Value)
	if err != nil {
		AzureError(w, "BadParameter", "invalid value: "+err.Error(), http.StatusBadRequest)
		return
	}
	plain, err := rsaDecrypt(body.Algorithm, privateKey, ciphertext)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	writeKVOperationResult(w, version.ID, plain)
}

func publicJWK(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch k {
		case "d", "p", "q", "dp", "dq", "qi", "oth", "k":
			continue
		default:
			out[k] = v
		}
	}
	return out
}

func rsaPrivatePEMFromJWK(jwk map[string]any) string {
	nBytes, nOK := jwkBytes(jwk, "n")
	dBytes, dOK := jwkBytes(jwk, "d")
	pBytes, pOK := jwkBytes(jwk, "p")
	qBytes, qOK := jwkBytes(jwk, "q")
	if !nOK || !dOK || !pOK || !qOK {
		return ""
	}
	eBytes, eOK := jwkBytes(jwk, "e")
	if !eOK {
		return ""
	}
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	if e == 0 {
		e = 65537
	}
	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e},
		D:         new(big.Int).SetBytes(dBytes),
		Primes:    []*big.Int{new(big.Int).SetBytes(pBytes), new(big.Int).SetBytes(qBytes)},
	}
	if err := key.Validate(); err != nil {
		return ""
	}
	key.Precompute()
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func jwkBytes(jwk map[string]any, name string) ([]byte, bool) {
	v, ok := jwk[name]
	if !ok {
		return nil, false
	}
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	b, err := decodeKVURLBytes(s)
	return b, err == nil
}

func rsaPrivateKeyFromPEM(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("missing RSA private key")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func writeKVOperationResult(w http.ResponseWriter, kid string, value []byte) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kid":   kid,
		"value": base64.RawURLEncoding.EncodeToString(value),
	})
}

func signatureHash(alg string) (crypto.Hash, bool, error) {
	switch alg {
	case "RS256":
		return crypto.SHA256, false, nil
	case "RS384":
		return crypto.SHA384, false, nil
	case "RS512":
		return crypto.SHA512, false, nil
	case "PS256":
		return crypto.SHA256, true, nil
	case "PS384":
		return crypto.SHA384, true, nil
	case "PS512":
		return crypto.SHA512, true, nil
	default:
		return 0, false, fmt.Errorf("unsupported signature algorithm %q", alg)
	}
}

func rsaEncrypt(alg string, publicKey *rsa.PublicKey, value []byte) ([]byte, error) {
	switch alg {
	case "RSA-OAEP":
		return rsa.EncryptOAEP(sha1.New(), rand.Reader, publicKey, value, nil)
	case "RSA-OAEP-256":
		return rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, value, nil)
	case "RSA1_5":
		return rsa.EncryptPKCS1v15(rand.Reader, publicKey, value)
	default:
		return nil, fmt.Errorf("unsupported encryption algorithm %q", alg)
	}
}

func rsaDecrypt(alg string, privateKey *rsa.PrivateKey, value []byte) ([]byte, error) {
	switch alg {
	case "RSA-OAEP":
		return rsa.DecryptOAEP(sha1.New(), rand.Reader, privateKey, value, nil)
	case "RSA-OAEP-256":
		return rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, value, nil)
	case "RSA1_5":
		return rsa.DecryptPKCS1v15(rand.Reader, privateKey, value)
	default:
		return nil, fmt.Errorf("unsupported encryption algorithm %q", alg)
	}
}
