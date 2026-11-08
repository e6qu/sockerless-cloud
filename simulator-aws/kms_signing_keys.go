package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"fmt"

	"github.com/cloudflare/circl/sign"
	"github.com/cloudflare/circl/sign/mldsa/mldsa44"
	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/cloudflare/circl/sign/mldsa/mldsa87"
)

// ML-DSA keys (FIPS 204) keep their 32-byte seed; an Ed25519 key keeps its
// PKCS#8 DER beside the RSA and ECDSA keys (kms-key-spec-reference.md.gz).

func kmsMLDSASchemeFor(spec string) sign.Scheme {
	switch spec {
	case "ML_DSA_44":
		return mldsa44.Scheme()
	case "ML_DSA_65":
		return mldsa65.Scheme()
	case "ML_DSA_87":
		return mldsa87.Scheme()
	}
	return nil
}

func kmsIsMLDSASpec(spec string) bool { return kmsMLDSASchemeFor(spec) != nil }

func kmsMLDSAKeyPair(mat KMSKeyMaterial, spec string) (sign.Scheme, sign.PublicKey, sign.PrivateKey) {
	scheme := kmsMLDSASchemeFor(spec)
	pub, priv := scheme.DeriveKey(mat.MLDSASeed)
	return scheme, pub, priv
}

// kmsSPKI encodes a raw public key under an algorithm OID with absent
// parameters, as the ML-DSA and ML-KEM SubjectPublicKeyInfo carries it.
func kmsSPKI(oid asn1.ObjectIdentifier, raw []byte) ([]byte, error) {
	return asn1.Marshal(struct {
		Algorithm struct{ Algorithm asn1.ObjectIdentifier }
		PublicKey asn1.BitString
	}{
		Algorithm: struct{ Algorithm asn1.ObjectIdentifier }{oid},
		PublicKey: asn1.BitString{Bytes: raw, BitLength: 8 * len(raw)},
	})
}

func kmsMLDSAPublicKeyDER(mat KMSKeyMaterial, spec string) ([]byte, error) {
	scheme, pub, _ := kmsMLDSAKeyPair(mat, spec)
	raw, err := pub.MarshalBinary()
	if err != nil {
		return nil, err
	}
	identified, ok := scheme.(interface{ Oid() asn1.ObjectIdentifier })
	if !ok {
		return nil, errKMSUnsupportedSpec
	}
	return kmsSPKI(identified.Oid(), raw)
}

// kmsSigningMessageError is a Sign or Verify request whose MessageType does
// not fit its signing algorithm.
type kmsSigningMessageError string

func (e kmsSigningMessageError) Error() string { return string(e) }

// kmsSigningInput reads Message as MessageType says: hashed by the algorithm
// for RAW, used as the digest for DIGEST, whose length must match the
// algorithm's hash.
func kmsSigningInput(alg, messageType string, message []byte) ([]byte, crypto.Hash, error) {
	digest, hash := kmsHashForSigningAlg(alg, message)
	switch messageType {
	case "", "RAW":
		return digest, hash, nil
	case "DIGEST":
		if len(message) != hash.Size() {
			return nil, 0, kmsSigningMessageError(fmt.Sprintf(
				"Digest is invalid length for algorithm %s.", alg))
		}
		return message, hash, nil
	}
	return nil, 0, kmsSigningMessageError(fmt.Sprintf("MessageType %s is not valid for %s.", messageType, alg))
}

// kmsSignWith signs per the key's spec. Ed25519 takes ED25519_SHA_512 over a
// RAW message and ED25519_PH_SHA_512 (HashEdDSA, still prehashing the digest
// with SHA-512) over a DIGEST; ML-DSA takes ML_DSA_SHAKE_256 over a RAW
// message.
func kmsSignWith(spec string, mat KMSKeyMaterial, alg, messageType string, message []byte) ([]byte, error) {
	if kmsIsMLDSASpec(spec) {
		if messageType != "" && messageType != "RAW" {
			return nil, kmsSigningMessageError("ML_DSA_SHAKE_256 signing takes MessageType RAW.")
		}
		scheme, _, priv := kmsMLDSAKeyPair(mat, spec)
		return scheme.Sign(priv, message, nil), nil
	}
	priv, err := x509.ParsePKCS8PrivateKey(mat.PrivateKeyDER)
	if err != nil {
		return nil, err
	}
	switch k := priv.(type) {
	case ed25519.PrivateKey:
		input, opts, err := kmsEd25519Input(alg, messageType, message)
		if err != nil {
			return nil, err
		}
		return k.Sign(nil, input, opts)
	case *rsa.PrivateKey:
		digest, hash, err := kmsSigningInput(alg, messageType, message)
		if err != nil {
			return nil, err
		}
		if isPSS(alg) {
			return rsa.SignPSS(rand.Reader, k, hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hash})
		}
		return rsa.SignPKCS1v15(rand.Reader, k, hash, digest)
	case *ecdsa.PrivateKey:
		digest, _, err := kmsSigningInput(alg, messageType, message)
		if err != nil {
			return nil, err
		}
		return ecdsa.SignASN1(rand.Reader, k, digest)
	}
	return nil, errKMSUnsupportedSpec
}

func kmsVerifyWith(spec string, mat KMSKeyMaterial, alg, messageType string, message, signature []byte) (bool, error) {
	if kmsIsMLDSASpec(spec) {
		if messageType != "" && messageType != "RAW" {
			return false, kmsSigningMessageError("ML_DSA_SHAKE_256 signing takes MessageType RAW.")
		}
		scheme, pub, _ := kmsMLDSAKeyPair(mat, spec)
		return scheme.Verify(pub, message, signature, nil), nil
	}
	priv, err := x509.ParsePKCS8PrivateKey(mat.PrivateKeyDER)
	if err != nil {
		return false, err
	}
	switch k := priv.(type) {
	case ed25519.PrivateKey:
		input, opts, err := kmsEd25519Input(alg, messageType, message)
		if err != nil {
			return false, err
		}
		public := ed25519.PublicKey(k[ed25519.SeedSize:])
		return ed25519.VerifyWithOptions(public, input, signature, opts) == nil, nil
	case *rsa.PrivateKey:
		digest, hash, err := kmsSigningInput(alg, messageType, message)
		if err != nil {
			return false, err
		}
		if isPSS(alg) {
			return rsa.VerifyPSS(&k.PublicKey, hash, digest, signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hash}) == nil, nil
		}
		return rsa.VerifyPKCS1v15(&k.PublicKey, hash, digest, signature) == nil, nil
	case *ecdsa.PrivateKey:
		digest, _, err := kmsSigningInput(alg, messageType, message)
		if err != nil {
			return false, err
		}
		return ecdsa.VerifyASN1(&k.PublicKey, digest, signature), nil
	}
	return false, errKMSUnsupportedSpec
}

func kmsEd25519Input(alg, messageType string, message []byte) ([]byte, *ed25519.Options, error) {
	switch alg {
	case "ED25519_SHA_512":
		if messageType != "" && messageType != "RAW" {
			return nil, nil, kmsSigningMessageError("ED25519_SHA_512 signing requires MessageType RAW.")
		}
		return message, &ed25519.Options{}, nil
	case "ED25519_PH_SHA_512":
		if messageType != "DIGEST" {
			return nil, nil, kmsSigningMessageError("ED25519_PH_SHA_512 signing requires MessageType DIGEST.")
		}
		if len(message) != sha512.Size {
			return nil, nil, kmsSigningMessageError("Digest is invalid length for algorithm ED25519_PH_SHA_512.")
		}
		prehash := sha512.Sum512(message)
		return prehash[:], &ed25519.Options{Hash: crypto.SHA512}, nil
	}
	return nil, nil, kmsSigningMessageError(fmt.Sprintf("%s is not a signing algorithm for ECC_NIST_EDWARDS25519.", alg))
}

// kmsMLKEMPrivateKeyDER encodes an ML-KEM data key pair's private key as the
// PKCS#8 PrivateKeyInfo KMS returns: the key in seed format, the 64-byte seed
// as the [0] choice of ML-KEM-PrivateKey.
func kmsMLKEMPrivateKeyDER(oid asn1.ObjectIdentifier, seed []byte) ([]byte, error) {
	inner, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, Bytes: seed})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct {
		Version    int
		Algorithm  struct{ Algorithm asn1.ObjectIdentifier }
		PrivateKey []byte
	}{0, struct{ Algorithm asn1.ObjectIdentifier }{oid}, inner})
}

// kmsGeneratePQDataKeyPair makes an Ed25519 or ML-KEM data key pair.
func kmsGeneratePQDataKeyPair(spec string) (pubDER, privDER []byte, err error) {
	if spec == "ECC_NIST_EDWARDS25519" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		if pubDER, err = x509.MarshalPKIXPublicKey(pub); err != nil {
			return nil, nil, err
		}
		privDER, err = x509.MarshalPKCS8PrivateKey(priv)
		return pubDER, privDER, err
	}
	scheme, oid := kmsMLKEMSchemeFor(spec)
	if scheme == nil {
		return nil, nil, errKMSUnsupportedSpec
	}
	seed, err := kmsNewMLKEMSeed(spec)
	if err != nil {
		return nil, nil, err
	}
	pub, _ := scheme.DeriveKeyPair(seed)
	if pubDER, err = kmsMLKEMPublicKeyDER(spec, pub); err != nil {
		return nil, nil, err
	}
	privDER, err = kmsMLKEMPrivateKeyDER(oid, seed)
	return pubDER, privDER, err
}
