package main

import (
	"crypto/rand"
	"encoding/asn1"
	"net/http"

	"github.com/cloudflare/circl/kem"
	"github.com/cloudflare/circl/kem/mlkem/mlkem1024"
	"github.com/cloudflare/circl/kem/mlkem/mlkem512"
	"github.com/cloudflare/circl/kem/mlkem/mlkem768"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ML-KEM keys (FIPS 203) back Encapsulate and Decapsulate. The private key is
// kept as its 64-byte seed (d || z), from which the key pair re-derives.

func registerKMSMLKEM(r *AWSRouter) {
	r.Register("TrentService.Encapsulate", handleKMSEncapsulate)
	r.Register("TrentService.Decapsulate", handleKMSDecapsulate)
}

// kmsMLKEMSchemeFor returns the ML-KEM parameter set of a key spec and the
// algorithm OID its SubjectPublicKeyInfo carries (NIST CSOR, id-alg-ml-kem-*).
func kmsMLKEMSchemeFor(spec string) (kem.Scheme, asn1.ObjectIdentifier) {
	switch spec {
	case "ML_KEM_512":
		return mlkem512.Scheme(), asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 4, 1}
	case "ML_KEM_768":
		return mlkem768.Scheme(), asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 4, 2}
	case "ML_KEM_1024":
		return mlkem1024.Scheme(), asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 4, 3}
	}
	return nil, nil
}

func kmsIsMLKEMSpec(spec string) bool {
	s, _ := kmsMLKEMSchemeFor(spec)
	return s != nil
}

func kmsEncapsulationAlgorithmsFor(spec, usage string) []string {
	if usage != "ENCAPSULATE_DECAPSULATE" || !kmsIsMLKEMSpec(spec) {
		return nil
	}
	return []string{"ML_KEM"}
}

func kmsNewMLKEMSeed(spec string) ([]byte, error) {
	scheme, _ := kmsMLKEMSchemeFor(spec)
	seed := make([]byte, scheme.SeedSize())
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return seed, nil
}

// kmsMLKEMKeyPair loads an ML-KEM key's pair from its stored seed.
func kmsMLKEMKeyPair(keyId, spec string) (kem.Scheme, kem.PublicKey, kem.PrivateKey, error) {
	mat, err := kmsEnsureAsymmetricMaterial(keyId, spec)
	if err != nil {
		return nil, nil, nil, err
	}
	scheme, _ := kmsMLKEMSchemeFor(spec)
	pub, priv := scheme.DeriveKeyPair(mat.MLKEMSeed)
	return scheme, pub, priv, nil
}

// kmsMLKEMPublicKeyDER encodes an ML-KEM encapsulation key as the DER
// SubjectPublicKeyInfo GetPublicKey returns: the algorithm OID with absent
// parameters, and the raw encapsulation key as the BIT STRING.
func kmsMLKEMPublicKeyDER(spec string, pub kem.PublicKey) ([]byte, error) {
	_, oid := kmsMLKEMSchemeFor(spec)
	raw, err := pub.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct {
		Algorithm struct{ Algorithm asn1.ObjectIdentifier }
		PublicKey asn1.BitString
	}{
		Algorithm: struct{ Algorithm asn1.ObjectIdentifier }{oid},
		PublicKey: asn1.BitString{Bytes: raw, BitLength: 8 * len(raw)},
	})
}

// kmsMLKEMKey resolves the request's key and checks that it is an enabled
// ML-KEM key with ENCAPSULATE_DECAPSULATE usage, writing the error KMS
// answers otherwise.
func kmsMLKEMKey(w http.ResponseWriter, r *http.Request, keyIdRef, algorithm string) (KMSKey, bool) {
	keyId, ok := kmsResolveOr404(w, r, keyIdRef)
	if !ok {
		return KMSKey{}, false
	}
	key, _ := kmsKeys.Get(keyId)
	if !kmsIsUsable(key) {
		kmsCryptoDisabledError(w, kmsKeyArn(keyId))
		return KMSKey{}, false
	}
	if key.KeyUsage != "ENCAPSULATE_DECAPSULATE" || !kmsIsMLKEMSpec(key.Spec) {
		AWSErrorf(w, "InvalidKeyUsageException", http.StatusBadRequest,
			"%s key usage is %s which is not valid for this operation.", kmsKeyArn(keyId), key.KeyUsage)
		return KMSKey{}, false
	}
	if algorithm != "ML_KEM" {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"1 validation error detected: Value '%s' at 'encapsulationAlgorithm' failed to satisfy constraint: Member must satisfy enum value set: [ML_KEM]", algorithm)
		return KMSKey{}, false
	}
	return key, true
}

func kmsDryRunError(w http.ResponseWriter) {
	AWSError(w, "DryRunOperationException",
		"The request would have succeeded, but the DryRun option is set.", http.StatusPreconditionFailed)
}

func handleKMSEncapsulate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId                  string   `json:"KeyId"`
		EncapsulationAlgorithm string   `json:"EncapsulationAlgorithm"`
		GrantTokens            []string `json:"GrantTokens"`
		DryRun                 bool     `json:"DryRun"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	key, ok := kmsMLKEMKey(w, r, req.KeyId, req.EncapsulationAlgorithm)
	if !ok {
		return
	}
	if req.DryRun {
		kmsDryRunError(w)
		return
	}
	scheme, pub, _, err := kmsMLKEMKeyPair(key.KeyId, key.Spec)
	if err != nil {
		AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	ciphertext, shared, err := scheme.Encapsulate(pub)
	if err != nil {
		AWSError(w, "KMSInternalException", "failed to encapsulate", http.StatusInternalServerError)
		return
	}
	kmsRecordUsage(key.KeyId, "Encapsulate")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":                      kmsKeyArn(key.KeyId),
		"SharedSecret":               shared,
		"SharedSecretCiphertextBlob": ciphertext,
		"EncapsulationAlgorithm":     req.EncapsulationAlgorithm,
	})
}

func handleKMSDecapsulate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId                      string   `json:"KeyId"`
		SharedSecretCiphertextBlob []byte   `json:"SharedSecretCiphertextBlob"`
		EncapsulationAlgorithm     string   `json:"EncapsulationAlgorithm"`
		GrantTokens                []string `json:"GrantTokens"`
		DryRun                     bool     `json:"DryRun"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	key, ok := kmsMLKEMKey(w, r, req.KeyId, req.EncapsulationAlgorithm)
	if !ok {
		return
	}
	scheme, _, priv, err := kmsMLKEMKeyPair(key.KeyId, key.Spec)
	if err != nil {
		AWSError(w, "KMSInternalException", "failed to materialize key", http.StatusInternalServerError)
		return
	}
	if len(req.SharedSecretCiphertextBlob) != scheme.CiphertextSize() {
		AWSError(w, "InvalidCiphertextException",
			"The ciphertext is not a valid "+scheme.Name()+" ciphertext.", http.StatusBadRequest)
		return
	}
	if req.DryRun {
		kmsDryRunError(w)
		return
	}
	shared, err := scheme.Decapsulate(priv, req.SharedSecretCiphertextBlob)
	if err != nil {
		AWSError(w, "InvalidCiphertextException", err.Error(), http.StatusBadRequest)
		return
	}
	kmsRecordUsage(key.KeyId, "Decapsulate")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyId":                  kmsKeyArn(key.KeyId),
		"SharedSecret":           shared,
		"EncapsulationAlgorithm": req.EncapsulationAlgorithm,
	})
}
