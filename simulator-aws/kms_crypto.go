package main

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// KMS crypto: real AES-256-GCM for symmetric operations and real Go-stdlib
// cryptography for asymmetric/MAC operations. No fake signatures, MACs, or
// ciphertexts — Sign/Verify, GenerateMac/VerifyMac, data-key-pairs, and ECDH
// all use persisted real key material.
//
// Symmetric ciphertext blob format v1:
//   magic    [3]byte  "SK1"
//   version  [1]byte  0x01
//   keyIdLen [2]byte  big-endian uint16
//   keyId    []byte
//   nonce    [12]byte AES-GCM nonce
//   ciphertext+tag []byte

const kmsBlobMagic = "SK1"
const kmsBlobVersion = byte(0x01)
const kmsKeyMaterialLen = 32

func registerKMSCrypto(r *sim.AWSRouter, srv *sim.Server) {
	registerKMSKeyMaterial(srv)
	kmsAsymmetricKeyMaterial = sim.MakeStore[KMSKeyMaterial](srv.DB(), "kms_asymmetric_key_material")

	r.Register("TrentService.Sign", handleKMSSign)
	r.Register("TrentService.Verify", handleKMSVerify)
	r.Register("TrentService.GetPublicKey", handleKMSGetPublicKey)
	r.Register("TrentService.GenerateMac", handleKMSGenerateMac)
	r.Register("TrentService.VerifyMac", handleKMSVerifyMac)
	r.Register("TrentService.GenerateDataKeyPair", handleKMSGenerateDataKeyPair)
	r.Register("TrentService.GenerateDataKeyPairWithoutPlaintext", handleKMSGenerateDataKeyPairWithoutPlaintext)
	r.Register("TrentService.DeriveSharedSecret", handleKMSDeriveSharedSecret)
}

var kmsKeyMaterial sim.Store[[]byte]

func registerKMSKeyMaterial(srv *sim.Server) {
	kmsKeyMaterial = sim.MakeStore[[]byte](srv.DB(), "kms_key_material")
}

// KMSKeyMaterial holds the real backing key bytes for an asymmetric or HMAC
// CMK. KMS never exposes the private material on the wire, so the sim keeps
// it in a side store keyed by KeyId. For an asymmetric key, PrivateKeyDER is
// the PKCS#8 DER of the real RSA/ECDSA private key generated for that KeySpec.
// For an HMAC key, HMACSecret is the raw MAC key bytes.
type KMSKeyMaterial struct {
	KeyId         string `json:"KeyId"`
	PrivateKeyDER []byte `json:"PrivateKeyDER,omitempty"`
	HMACSecret    []byte `json:"HMACSecret,omitempty"`
}

var kmsAsymmetricKeyMaterial sim.Store[KMSKeyMaterial]

// kmsSigningAlgorithmsFor returns the signing algorithms real KMS advertises
// for an asymmetric signing key based on its KeySpec.
func kmsSigningAlgorithmsFor(spec string) []string {
	switch spec {
	case "RSA_2048", "RSA_3072", "RSA_4096":
		return []string{
			"RSASSA_PSS_SHA_256",
			"RSASSA_PSS_SHA_384",
			"RSASSA_PSS_SHA_512",
			"RSASSA_PKCS1_V1_5_SHA_256",
			"RSASSA_PKCS1_V1_5_SHA_384",
			"RSASSA_PKCS1_V1_5_SHA_512",
		}
	case "ECC_NIST_P256", "ECC_SECG_P256K1":
		return []string{"ECDSA_SHA_256"}
	case "ECC_NIST_P384":
		return []string{"ECDSA_SHA_384"}
	case "ECC_NIST_P521":
		return []string{"ECDSA_SHA_512"}
	}
	return nil
}

// kmsEncryptionAlgorithmsFor returns the encryption algorithms real KMS
// advertises for an asymmetric encryption key based on its KeySpec and usage.
func kmsEncryptionAlgorithmsFor(spec, usage string) []string {
	if usage != "ENCRYPT_DECRYPT" {
		return nil
	}
	switch spec {
	case "RSA_2048", "RSA_3072", "RSA_4096":
		return []string{"RSAES_OAEP_SHA_1", "RSAES_OAEP_SHA_256"}
	}
	return nil
}

// kmsKeyAgreementAlgorithmsFor returns the key-agreement algorithms real KMS
// advertises for an ECC key with KEY_AGREEMENT usage.
func kmsKeyAgreementAlgorithmsFor(spec, usage string) []string {
	if usage != "KEY_AGREEMENT" {
		return nil
	}
	switch spec {
	case "ECC_NIST_P256", "ECC_NIST_P384", "ECC_NIST_P521", "ECC_SECG_P256K1":
		return []string{"ECDH"}
	}
	return nil
}

// kmsMacAlgorithmsFor returns the MAC algorithms real KMS advertises for an
// HMAC key based on its KeySpec.
func kmsMacAlgorithmsFor(spec string) []string {
	switch spec {
	case "HMAC_224":
		return []string{"HMAC_SHA_224"}
	case "HMAC_256":
		return []string{"HMAC_SHA_256"}
	case "HMAC_384":
		return []string{"HMAC_SHA_384"}
	case "HMAC_512":
		return []string{"HMAC_SHA_512"}
	}
	return nil
}

func kmsIsAsymmetricSpec(spec string) bool {
	switch spec {
	case "RSA_2048", "RSA_3072", "RSA_4096",
		"ECC_NIST_P256", "ECC_NIST_P384", "ECC_NIST_P521", "ECC_SECG_P256K1":
		return true
	}
	return false
}

func kmsIsHMACSpec(spec string) bool {
	switch spec {
	case "HMAC_224", "HMAC_256", "HMAC_384", "HMAC_512":
		return true
	}
	return false
}

// kmsEllipticCurveFor maps an ECC KeySpec to its stdlib curve.
func kmsEllipticCurveFor(spec string) elliptic.Curve {
	switch spec {
	case "ECC_NIST_P256", "ECC_SECG_P256K1":
		return elliptic.P256()
	case "ECC_NIST_P384":
		return elliptic.P384()
	case "ECC_NIST_P521":
		return elliptic.P521()
	}
	return nil
}

// kmsGenerateKeyMaterial creates and persists 32 random bytes for a new CMK.
func kmsGenerateKeyMaterial(keyId string) ([]byte, error) {
	material := make([]byte, kmsKeyMaterialLen)
	if _, err := io.ReadFull(rand.Reader, material); err != nil {
		return nil, err
	}
	kmsKeyMaterial.Put(keyId, material)
	return material, nil
}

// kmsGetKeyMaterial returns the persisted AES key for a CMK.
func kmsGetKeyMaterial(keyId string) ([]byte, bool) {
	return kmsKeyMaterial.Get(keyId)
}

// kmsDeleteKeyMaterial removes the key material for a CMK.
func kmsDeleteKeyMaterial(keyId string) {
	kmsKeyMaterial.Delete(keyId)
}

// kmsEncryptBytes encrypts plaintext under the named CMK and returns the
// opaque ciphertext blob. Returns ok=false when the key has no material.
func kmsEncryptBytes(keyId string, plaintext []byte) ([]byte, bool) {
	material, ok := kmsGetKeyMaterial(keyId)
	if !ok {
		return nil, false
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, false
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	keyIdBytes := []byte(keyId)
	if len(keyIdBytes) > 65535 {
		return nil, false
	}
	out := make([]byte, 0, 3+1+2+len(keyIdBytes)+len(nonce)+len(ciphertext))
	out = append(out, []byte(kmsBlobMagic)...)
	out = append(out, kmsBlobVersion)
	out = binary.BigEndian.AppendUint16(out, uint16(len(keyIdBytes)))
	out = append(out, keyIdBytes...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, true
}

// kmsDecryptBytes decrypts a blob produced by kmsEncryptBytes. It returns the
// source key id, plaintext, and ok. Authentication tag verification happens
// inside GCM.Open; a tampered blob returns ok=false.
func kmsDecryptBytes(blob []byte) (keyId string, plaintext []byte, ok bool) {
	if len(blob) < 3+1+2 {
		return "", nil, false
	}
	if string(blob[0:3]) != kmsBlobMagic {
		return "", nil, false
	}
	if blob[3] != kmsBlobVersion {
		return "", nil, false
	}
	keyIdLen := binary.BigEndian.Uint16(blob[4:6])
	off := 6
	if len(blob) < off+int(keyIdLen)+12 {
		return "", nil, false
	}
	keyId = string(blob[off : off+int(keyIdLen)])
	off += int(keyIdLen)

	material, exists := kmsGetKeyMaterial(keyId)
	if !exists {
		return "", nil, false
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return "", nil, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, false
	}
	nonceSize := gcm.NonceSize()
	if len(blob) < off+nonceSize {
		return "", nil, false
	}
	nonce := blob[off : off+nonceSize]
	ciphertext := blob[off+nonceSize:]
	plaintext, err = gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", nil, false
	}
	return keyId, plaintext, true
}

// kmsIsUsable reports whether a CMK can currently perform cryptographic
// operations. Keys in Disabled or PendingDeletion states must reject crypto.
func kmsIsUsable(key KMSKey) bool {
	return key.KeyState == "Enabled"
}

// kmsCryptoDisabledError returns the service-specific error real KMS emits
// when a key is not in a valid state for the requested operation.
func kmsCryptoDisabledError(w http.ResponseWriter, op string) {
	sim.AWSErrorf(w, "DisabledException", http.StatusConflict,
		"%s is disabled.", op)
}

// kmsKeyPolicyArn returns the ARN used as the resource-policy key for a CMK.
func kmsKeyPolicyArn(keyId string) string {
	return kmsKeyArn(keyId)
}

// kmsPutKeyPolicy mirrors a KMS key policy into the central resource-policy
// store so the IAM enforcement gate evaluates it for crypto operations.
func kmsPutKeyPolicy(keyId, policyJSON string) {
	if policyJSON == "" {
		policyJSON = kmsDefaultKeyPolicyJSON()
	}
	iamPutResourcePolicy(kmsKeyPolicyArn(keyId), policyJSON)
}

// kmsEnsureAsymmetricMaterial generates and stores real backing key material
// for an asymmetric or HMAC CMK on first use, so Sign->Verify and
// GenerateMac->VerifyMac round-trip.
func kmsEnsureAsymmetricMaterial(keyId, spec string) (KMSKeyMaterial, error) {
	if m, ok := kmsAsymmetricKeyMaterial.Get(keyId); ok {
		return m, nil
	}
	m := KMSKeyMaterial{KeyId: keyId}
	switch {
	case kmsIsHMACSpec(spec):
		size := 32
		switch spec {
		case "HMAC_224":
			size = 28
		case "HMAC_256":
			size = 32
		case "HMAC_384":
			size = 48
		case "HMAC_512":
			size = 64
		}
		secret := make([]byte, size)
		if _, err := rand.Read(secret); err != nil {
			return m, err
		}
		m.HMACSecret = secret
	case spec == "RSA_2048" || spec == "RSA_3072" || spec == "RSA_4096":
		bits := 2048
		switch spec {
		case "RSA_3072":
			bits = 3072
		case "RSA_4096":
			bits = 4096
		}
		priv, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return m, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return m, err
		}
		m.PrivateKeyDER = der
	default:
		curve := kmsEllipticCurveFor(spec)
		if curve == nil {
			return m, errKMSUnsupportedSpec
		}
		priv, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return m, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return m, err
		}
		m.PrivateKeyDER = der
	}
	kmsAsymmetricKeyMaterial.Put(keyId, m)
	return m, nil
}

type kmsSimpleError string

func (e kmsSimpleError) Error() string { return string(e) }

const errKMSUnsupportedSpec = kmsSimpleError("unsupported key spec")

// kmsHashForSigningAlg returns the message digest for a signing algorithm and
// the crypto.Hash that produced it (RSA Sign/Verify need the hash identifier).
func kmsHashForSigningAlg(alg string, message []byte) ([]byte, crypto.Hash) {
	switch {
	case strings.HasSuffix(alg, "SHA_384"):
		s := sha512.Sum384(message)
		return s[:], crypto.SHA384
	case strings.HasSuffix(alg, "SHA_512"):
		s := sha512.Sum512(message)
		return s[:], crypto.SHA512
	default: // SHA_256 (and the default fallback)
		s := sha256.Sum256(message)
		return s[:], crypto.SHA256
	}
}

// isPSS reports whether a signing algorithm uses RSASSA-PSS padding.
func isPSS(alg string) bool { return strings.HasPrefix(alg, "RSASSA_PSS") }

func handleKMSSign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId            string `json:"KeyId"`
		Message          []byte `json:"Message"`
		MessageType      string `json:"MessageType"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	if !kmsIsAsymmetricSpec(key.Spec) || key.KeyUsage != "SIGN_VERIFY" {
		sim.AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s key usage is %s, not SIGN_VERIFY.", kmsKeyArn(keyId), key.KeyUsage)
		return
	}
	if req.SigningAlgorithm == "" {
		sim.AWSError(w, "ValidationException", "SigningAlgorithm is required", http.StatusBadRequest)
		return
	}
	mat, err := kmsEnsureAsymmetricMaterial(keyId, key.Spec)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	priv, err := x509.ParsePKCS8PrivateKey(mat.PrivateKeyDER)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to load key material", http.StatusInternalServerError)
		return
	}
	digest, hkind := kmsHashForSigningAlg(req.SigningAlgorithm, req.Message)
	var signature []byte
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		if isPSS(req.SigningAlgorithm) {
			signature, err = rsa.SignPSS(rand.Reader, k, hkind, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hkind})
		} else {
			signature, err = rsa.SignPKCS1v15(rand.Reader, k, hkind, digest)
		}
	case *ecdsa.PrivateKey:
		signature, err = ecdsa.SignASN1(rand.Reader, k, digest)
	default:
		sim.AWSError(w, "InvalidKeyUsageException", "key material is not a signing key", http.StatusBadRequest)
		return
	}
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to sign message", http.StatusInternalServerError)
		return
	}
	kmsRecordUsage(keyId, "Sign")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":            kmsKeyArn(keyId),
		"Signature":        signature, // SDK base64-encodes on the wire
		"SigningAlgorithm": req.SigningAlgorithm,
	})
}

func handleKMSVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId            string `json:"KeyId"`
		Message          []byte `json:"Message"`
		MessageType      string `json:"MessageType"`
		Signature        []byte `json:"Signature"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsAsymmetricSpec(key.Spec) || key.KeyUsage != "SIGN_VERIFY" {
		sim.AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s key usage is %s, not SIGN_VERIFY.", kmsKeyArn(keyId), key.KeyUsage)
		return
	}
	mat, err := kmsEnsureAsymmetricMaterial(keyId, key.Spec)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	priv, err := x509.ParsePKCS8PrivateKey(mat.PrivateKeyDER)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to load key material", http.StatusInternalServerError)
		return
	}
	digest, hkind := kmsHashForSigningAlg(req.SigningAlgorithm, req.Message)
	var verifyErr error
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		if isPSS(req.SigningAlgorithm) {
			verifyErr = rsa.VerifyPSS(&k.PublicKey, hkind, digest, req.Signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hkind})
		} else {
			verifyErr = rsa.VerifyPKCS1v15(&k.PublicKey, hkind, digest, req.Signature)
		}
	case *ecdsa.PrivateKey:
		if !ecdsa.VerifyASN1(&k.PublicKey, digest, req.Signature) {
			verifyErr = kmsSimpleError("invalid ecdsa signature")
		}
	default:
		sim.AWSError(w, "InvalidKeyUsageException", "key material is not a signing key", http.StatusBadRequest)
		return
	}
	if verifyErr != nil {
		sim.AWSErrorf(w, "KMSInvalidSignatureException", http.StatusBadRequest,
			"The signature is not valid for the message and key.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":            kmsKeyArn(keyId),
		"SignatureValid":   true,
		"SigningAlgorithm": req.SigningAlgorithm,
	})
}

func handleKMSGetPublicKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsAsymmetricSpec(key.Spec) {
		sim.AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s is not an asymmetric key.", kmsKeyArn(keyId))
		return
	}
	mat, err := kmsEnsureAsymmetricMaterial(keyId, key.Spec)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	priv, err := x509.ParsePKCS8PrivateKey(mat.PrivateKeyDER)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to load key material", http.StatusInternalServerError)
		return
	}
	var pubDER []byte
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		pubDER, err = x509.MarshalPKIXPublicKey(&k.PublicKey)
	case *ecdsa.PrivateKey:
		pubDER, err = x509.MarshalPKIXPublicKey(&k.PublicKey)
	default:
		sim.AWSError(w, "KMSInternalException", "unsupported key material", http.StatusInternalServerError)
		return
	}
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to marshal public key", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{
		"KeyId":                 kmsKeyArn(keyId),
		"PublicKey":             pubDER, // SDK base64-encodes on the wire
		"CustomerMasterKeySpec": key.Spec,
		"KeySpec":               key.Spec,
		"KeyUsage":              key.KeyUsage,
	}
	if algs := kmsSigningAlgorithmsFor(key.Spec); len(algs) > 0 && key.KeyUsage == "SIGN_VERIFY" {
		resp["SigningAlgorithms"] = algs
	}
	if algs := kmsEncryptionAlgorithmsFor(key.Spec, key.KeyUsage); len(algs) > 0 {
		resp["EncryptionAlgorithms"] = algs
	}
	if algs := kmsKeyAgreementAlgorithmsFor(key.Spec, key.KeyUsage); len(algs) > 0 {
		resp["KeyAgreementAlgorithms"] = algs
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// kmsHMACHash maps a MAC algorithm to its hash constructor.
func kmsHMACHash(alg string) func() hash.Hash {
	switch alg {
	case "HMAC_SHA_224":
		return sha256.New224
	case "HMAC_SHA_256":
		return sha256.New
	case "HMAC_SHA_384":
		return sha512.New384
	case "HMAC_SHA_512":
		return sha512.New
	}
	return nil
}

func handleKMSGenerateMac(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId        string `json:"KeyId"`
		Message      []byte `json:"Message"`
		MacAlgorithm string `json:"MacAlgorithm"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	if !kmsIsHMACSpec(key.Spec) || key.KeyUsage != "GENERATE_VERIFY_MAC" {
		sim.AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s is not an HMAC key.", kmsKeyArn(keyId))
		return
	}
	hfn := kmsHMACHash(req.MacAlgorithm)
	if hfn == nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Unsupported MacAlgorithm: %s", req.MacAlgorithm)
		return
	}
	mat, err := kmsEnsureAsymmetricMaterial(keyId, key.Spec)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	mac := hmac.New(hfn, mat.HMACSecret)
	mac.Write(req.Message)
	sum := mac.Sum(nil)
	kmsRecordUsage(keyId, "GenerateMac")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":        kmsKeyArn(keyId),
		"Mac":          sum, // SDK base64-encodes on the wire
		"MacAlgorithm": req.MacAlgorithm,
	})
}

func handleKMSVerifyMac(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId        string `json:"KeyId"`
		Message      []byte `json:"Message"`
		Mac          []byte `json:"Mac"`
		MacAlgorithm string `json:"MacAlgorithm"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsHMACSpec(key.Spec) || key.KeyUsage != "GENERATE_VERIFY_MAC" {
		sim.AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s is not an HMAC key.", kmsKeyArn(keyId))
		return
	}
	hfn := kmsHMACHash(req.MacAlgorithm)
	if hfn == nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Unsupported MacAlgorithm: %s", req.MacAlgorithm)
		return
	}
	mat, err := kmsEnsureAsymmetricMaterial(keyId, key.Spec)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	mac := hmac.New(hfn, mat.HMACSecret)
	mac.Write(req.Message)
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, req.Mac) {
		sim.AWSErrorf(w, "KMSInvalidMacException", http.StatusBadRequest,
			"The HMAC is not valid for the message and key.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":        kmsKeyArn(keyId),
		"MacValid":     true,
		"MacAlgorithm": req.MacAlgorithm,
	})
}

// kmsGenerateDataKeyPairMaterial generates a real RSA/ECC data keypair for
// the requested DataKeyPairSpec, returning the DER-encoded SPKI public key and
// the PKCS#8 DER private key bytes.
func kmsGenerateDataKeyPairMaterial(spec string) (pubDER, privDER []byte, err error) {
	switch spec {
	case "RSA_2048", "RSA_3072", "RSA_4096":
		bits := 2048
		switch spec {
		case "RSA_3072":
			bits = 3072
		case "RSA_4096":
			bits = 4096
		}
		priv, gerr := rsa.GenerateKey(rand.Reader, bits)
		if gerr != nil {
			return nil, nil, gerr
		}
		pubDER, err = x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if err != nil {
			return nil, nil, err
		}
		privDER, err = x509.MarshalPKCS8PrivateKey(priv)
		return pubDER, privDER, err
	default:
		curve := kmsEllipticCurveFor(spec)
		if curve == nil {
			return nil, nil, errKMSUnsupportedSpec
		}
		priv, gerr := ecdsa.GenerateKey(curve, rand.Reader)
		if gerr != nil {
			return nil, nil, gerr
		}
		pubDER, err = x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if err != nil {
			return nil, nil, err
		}
		privDER, err = x509.MarshalPKCS8PrivateKey(priv)
		return pubDER, privDER, err
	}
}

// kmsKeyMaterialID returns the identifier of the backing key material a key is
// currently using. The model admits only hex digits, so the key's own dashed
// UUID is not one: the material id is derived from the key id, stable for as
// long as the key backs itself with the same material.
func kmsKeyMaterialID(keyID string) string {
	sum := sha256.Sum256([]byte("kms-key-material:" + keyID))
	return hex.EncodeToString(sum[:])
}

func handleKMSGenerateDataKeyPair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId       string `json:"KeyId"`
		KeyPairSpec string `json:"KeyPairSpec"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	pubDER, privDER, err := kmsGenerateDataKeyPairMaterial(req.KeyPairSpec)
	if err != nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Unsupported KeyPairSpec: %s", req.KeyPairSpec)
		return
	}
	wrapped, ok := kmsEncryptBytes(keyId, privDER)
	if !ok {
		sim.AWSError(w, "DependencyTimeoutException", "failed to wrap private key", http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":                    kmsKeyArn(keyId),
		"KeyPairSpec":              req.KeyPairSpec,
		"PublicKey":                pubDER,  // SDK base64-encodes on the wire
		"PrivateKeyPlaintext":      privDER, // SDK base64-encodes on the wire
		"PrivateKeyCiphertextBlob": wrapped,
		"KeyMaterialId":            kmsKeyMaterialID(keyId),
	})
}

func handleKMSGenerateDataKeyPairWithoutPlaintext(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId       string `json:"KeyId"`
		KeyPairSpec string `json:"KeyPairSpec"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	pubDER, privDER, err := kmsGenerateDataKeyPairMaterial(req.KeyPairSpec)
	if err != nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Unsupported KeyPairSpec: %s", req.KeyPairSpec)
		return
	}
	wrapped, ok := kmsEncryptBytes(keyId, privDER)
	if !ok {
		sim.AWSError(w, "DependencyTimeoutException", "failed to wrap private key", http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":                    kmsKeyArn(keyId),
		"KeyPairSpec":              req.KeyPairSpec,
		"PublicKey":                pubDER, // SDK base64-encodes on the wire
		"PrivateKeyCiphertextBlob": wrapped,
		"KeyMaterialId":            kmsKeyMaterialID(keyId),
	})
}

// handleKMSDeriveSharedSecret runs a real ECDH between the CMK's EC private key
// and the supplied peer public key (DER-encoded SPKI), returning the raw shared
// secret.
func handleKMSDeriveSharedSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId                 string `json:"KeyId"`
		KeyAgreementAlgorithm string `json:"KeyAgreementAlgorithm"`
		PublicKey             []byte `json:"PublicKey"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return
	}
	curve := kmsEllipticCurveFor(key.Spec)
	if curve == nil || key.KeyUsage != "KEY_AGREEMENT" {
		sim.AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s is not a KEY_AGREEMENT EC key.", kmsKeyArn(keyId))
		return
	}
	if req.KeyAgreementAlgorithm == "" {
		req.KeyAgreementAlgorithm = "ECDH"
	}
	mat, err := kmsEnsureAsymmetricMaterial(keyId, key.Spec)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	priv, err := x509.ParsePKCS8PrivateKey(mat.PrivateKeyDER)
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to load key material", http.StatusInternalServerError)
		return
	}
	ecdsaPriv, ok := priv.(*ecdsa.PrivateKey)
	if !ok {
		sim.AWSError(w, "InvalidKeyUsageException", "key material is not an EC key", http.StatusBadRequest)
		return
	}
	ecdhPriv, err := ecdsaPriv.ECDH()
	if err != nil {
		sim.AWSError(w, "KMSInternalException", "failed to convert EC key for ECDH", http.StatusInternalServerError)
		return
	}
	peerPubAny, err := x509.ParsePKIXPublicKey(req.PublicKey)
	if err != nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"PublicKey is not a valid DER-encoded SubjectPublicKeyInfo: %v", err)
		return
	}
	peerEcdsa, ok := peerPubAny.(*ecdsa.PublicKey)
	if !ok {
		sim.AWSError(w, "ValidationException", "PublicKey is not an EC public key", http.StatusBadRequest)
		return
	}
	peerEcdh, err := peerEcdsa.ECDH()
	if err != nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"PublicKey is not on the expected curve: %v", err)
		return
	}
	shared, err := ecdhPriv.ECDH(peerEcdh)
	if err != nil {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"Failed to derive shared secret: %v", err)
		return
	}
	kmsRecordUsage(keyId, "DeriveSharedSecret")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":                 kmsKeyArn(keyId),
		"SharedSecret":          shared, // SDK base64-encodes on the wire
		"KeyAgreementAlgorithm": req.KeyAgreementAlgorithm,
		"KeyOrigin":             key.Origin,
	})
}
