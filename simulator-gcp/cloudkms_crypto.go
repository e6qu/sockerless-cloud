package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"strings"

	kmspb "cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/e6qu/sockerless-cloud/sim/simkms"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The Cloud KMS key operations both doors share. Each returns a gRPC status
// error, which the REST door renders with GCPStatusError.

// kmsUnservedAlgorithm names why the simulator holds no key material for an
// algorithm the Discovery document lists, or "". The Go standard library has
// no secp256k1 curve, no ML-DSA or SLH-DSA, and keeps the X-Wing shared secret
// inside crypto/hpke; Cloud KMS documents no ciphertext format for KEM_ECDH.
func kmsUnservedAlgorithm(algorithm string) string {
	switch {
	case algorithm == "EC_SIGN_SECP256K1_SHA256":
		return "the simulator has no secp256k1 implementation"
	case strings.HasPrefix(algorithm, "PQ_SIGN_"):
		return "the simulator has no ML-DSA or SLH-DSA implementation"
	case algorithm == "KEM_XWING":
		return "the simulator cannot decapsulate an X-Wing shared secret"
	case strings.HasPrefix(algorithm, "KEM_ECDH_"):
		return "the simulator has no KEM_ECDH encapsulation format"
	}
	return ""
}

func kmsCheckAlgorithmServed(algorithm string) error {
	if reason := kmsUnservedAlgorithm(algorithm); reason != "" {
		return status.Errorf(codes.Unimplemented, "algorithm %s is not supported: %s", algorithm, reason)
	}
	return nil
}

// kmsGenerateMaterial mints a version's key material for its algorithm.
func kmsGenerateMaterial(algorithm string) (kmsKeyMaterialRecord, error) {
	if err := kmsCheckAlgorithmServed(algorithm); err != nil {
		return kmsKeyMaterialRecord{}, err
	}
	var priv any
	switch {
	case strings.HasPrefix(algorithm, "RSA_"):
		bits := 2048
		switch {
		case strings.Contains(algorithm, "4096"):
			bits = 4096
		case strings.Contains(algorithm, "3072"):
			bits = 3072
		}
		key, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		priv = key
	case algorithm == "EC_SIGN_ED25519":
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		priv = key
	case strings.HasPrefix(algorithm, "EC_SIGN_"):
		curve := elliptic.P256()
		if strings.Contains(algorithm, "P384") {
			curve = elliptic.P384()
		}
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		priv = key
	case algorithm == "ML_KEM_768":
		key, err := mlkem.GenerateKey768()
		if err != nil {
			return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		return kmsKeyMaterialRecord{Seed: key.Bytes()}, nil
	case algorithm == "ML_KEM_1024":
		key, err := mlkem.GenerateKey1024()
		if err != nil {
			return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		return kmsKeyMaterialRecord{Seed: key.Bytes()}, nil
	default:
		material, err := simkms.NewKey()
		if err != nil {
			return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not generate key version: %v", err)
		}
		return kmsKeyMaterialRecord{Key: material}, nil
	}
	pemBytes, err := kmsPrivateKeyPEM(priv)
	if err != nil {
		return kmsKeyMaterialRecord{}, status.Errorf(codes.Internal, "could not encode key version: %v", err)
	}
	return kmsKeyMaterialRecord{PrivatePEM: pemBytes}, nil
}

// kmsPublicKeyExport is a getPublicKey answer: pem when the caller named no
// format, data in the named format otherwise.
type kmsPublicKeyExport struct {
	Pem    string
	Data   []byte
	Format string
}

// kmsExportPublicKey renders a version's public key in format, the
// PublicKeyFormat enum name ("" for unspecified).
func kmsExportPublicKey(version kmsCryptoKeyVersion, material kmsKeyMaterialRecord, format string) (kmsPublicKeyExport, error) {
	if format == "PUBLIC_KEY_FORMAT_UNSPECIFIED" {
		format = ""
	}
	algorithm := version.Algorithm
	if strings.HasPrefix(algorithm, "ML_KEM_") {
		switch format {
		case "":
			return kmsPublicKeyExport{}, status.Errorf(codes.InvalidArgument, "public_key_format is required for the PQC algorithm %s", algorithm)
		case "NIST_PQC":
		default:
			return kmsPublicKeyExport{}, status.Errorf(codes.InvalidArgument, "public_key_format %s is not supported for %s; use NIST_PQC", format, algorithm)
		}
		encapsulationKey, err := kmsMLKEMEncapsulationKey(algorithm, material.Seed)
		if err != nil {
			return kmsPublicKeyExport{}, err
		}
		return kmsPublicKeyExport{Data: encapsulationKey, Format: format}, nil
	}
	if len(material.PrivatePEM) == 0 {
		return kmsPublicKeyExport{}, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s has no public key (not an asymmetric key)", version.Name)
	}
	pub, err := kmsPublicFromPEM(material.PrivatePEM)
	if err != nil {
		return kmsPublicKeyExport{}, status.Errorf(codes.Internal, "could not derive public key: %v", err)
	}
	pemStr, err := kmsPublicKeyPEM(pub)
	if err != nil {
		return kmsPublicKeyExport{}, status.Errorf(codes.Internal, "could not encode public key: %v", err)
	}
	switch format {
	case "":
		return kmsPublicKeyExport{Pem: pemStr, Format: "PEM"}, nil
	case "PEM":
		return kmsPublicKeyExport{Data: []byte(pemStr), Format: format}, nil
	case "DER":
		block, _ := pem.Decode([]byte(pemStr))
		return kmsPublicKeyExport{Data: block.Bytes, Format: format}, nil
	default:
		return kmsPublicKeyExport{}, status.Errorf(codes.InvalidArgument, "public_key_format %s is supported only for PQC algorithms, not %s", format, algorithm)
	}
}

func kmsMLKEMEncapsulationKey(algorithm string, seed []byte) ([]byte, error) {
	switch algorithm {
	case "ML_KEM_768":
		key, err := mlkem.NewDecapsulationKey768(seed)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "stored ML-KEM-768 key is invalid: %v", err)
		}
		return key.EncapsulationKey().Bytes(), nil
	case "ML_KEM_1024":
		key, err := mlkem.NewDecapsulationKey1024(seed)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "stored ML-KEM-1024 key is invalid: %v", err)
		}
		return key.EncapsulationKey().Bytes(), nil
	}
	return nil, status.Errorf(codes.FailedPrecondition, "algorithm %s is not a key-encapsulation algorithm", algorithm)
}

// kmsDecapsulate recovers the shared secret a sender encapsulated under the
// version's public key.
func kmsDecapsulate(version kmsCryptoKeyVersion, material kmsKeyMaterialRecord, ciphertext []byte) ([]byte, error) {
	var decapsulator interface {
		Decapsulate([]byte) ([]byte, error)
	}
	switch version.Algorithm {
	case "ML_KEM_768":
		key, err := mlkem.NewDecapsulationKey768(material.Seed)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "stored ML-KEM-768 key is invalid: %v", err)
		}
		decapsulator = key
	case "ML_KEM_1024":
		key, err := mlkem.NewDecapsulationKey1024(material.Seed)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "stored ML-KEM-1024 key is invalid: %v", err)
		}
		decapsulator = key
	default:
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not a key-encapsulation key (algorithm %s)", version.Name, version.Algorithm)
	}
	secret, err := decapsulator.Decapsulate(ciphertext)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Decapsulation failed: %v", err)
	}
	return secret, nil
}

// kmsSignInput is an AsymmetricSign request's message: the digest oneof
// (keyed by its JSON member name) or the data.
type kmsSignInput struct {
	Digest map[string][]byte
	Data   []byte
}

// kmsSignDigestField names the Digest member an algorithm signs and its hash.
// Raw PKCS #1 and Ed25519 sign the data itself and have none.
func kmsSignDigestField(algorithm string) (string, crypto.Hash, bool) {
	switch {
	case strings.HasPrefix(algorithm, "RSA_SIGN_RAW_PKCS1_"), algorithm == "EC_SIGN_ED25519":
		return "", 0, false
	case strings.HasSuffix(algorithm, "_SHA512"):
		return "sha512", crypto.SHA512, true
	case strings.HasSuffix(algorithm, "_SHA384"):
		return "sha384", crypto.SHA384, true
	default:
		return "sha256", crypto.SHA256, true
	}
}

// kmsAsymmetricSign signs with the digest the key's algorithm names, refusing a
// digest of another type, and signs the data itself for the algorithms that
// take data.
func kmsAsymmetricSign(version kmsCryptoKeyVersion, material kmsKeyMaterialRecord, in kmsSignInput) ([]byte, error) {
	if len(material.PrivatePEM) == 0 || !strings.Contains(version.Algorithm, "SIGN") {
		return nil, status.Errorf(codes.FailedPrecondition, "CryptoKeyVersion %s is not an asymmetric-sign key", version.Name)
	}
	if len(in.Digest) > 0 && len(in.Data) > 0 {
		return nil, status.Error(codes.InvalidArgument, "digest and data cannot both be supplied")
	}
	if len(in.Digest) > 1 {
		return nil, status.Error(codes.InvalidArgument, "digest must set exactly one of sha256, sha384, sha512 or externalMu")
	}
	priv, err := kmsPrivateFromPEM(material.PrivatePEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not parse private key: %v", err)
	}
	field, hash, hashed := kmsSignDigestField(version.Algorithm)
	if !hashed {
		if len(in.Digest) > 0 {
			return nil, status.Errorf(codes.InvalidArgument, "algorithm %s signs data; supply data instead of a digest", version.Algorithm)
		}
		var sig []byte
		switch key := priv.(type) {
		case ed25519.PrivateKey:
			sig = ed25519.Sign(key, in.Data)
		case *rsa.PrivateKey:
			if limit := key.Size() - 11; len(in.Data) > limit {
				return nil, status.Errorf(codes.InvalidArgument, "algorithm %s signs at most %d bytes of data, got %d", version.Algorithm, limit, len(in.Data))
			}
			sig, err = rsa.SignPKCS1v15(nil, key, crypto.Hash(0), in.Data)
		default:
			return nil, status.Errorf(codes.Internal, "algorithm %s holds a %T key", version.Algorithm, priv)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "signing failed: %v", err)
		}
		return sig, nil
	}

	var digest []byte
	if len(in.Digest) == 1 {
		for got, value := range in.Digest {
			if got != field {
				return nil, status.Errorf(codes.InvalidArgument, "algorithm %s requires digest.%s, got digest.%s", version.Algorithm, field, got)
			}
			digest = value
		}
		if len(digest) != hash.Size() {
			return nil, status.Errorf(codes.InvalidArgument, "digest.%s must be %d bytes, got %d", field, hash.Size(), len(digest))
		}
	} else {
		h := hash.New()
		h.Write(in.Data)
		digest = h.Sum(nil)
	}

	var sig []byte
	switch key := priv.(type) {
	case *rsa.PrivateKey:
		if strings.HasPrefix(version.Algorithm, "RSA_SIGN_PSS_") {
			sig, err = rsa.SignPSS(rand.Reader, key, hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hash})
		} else {
			sig, err = rsa.SignPKCS1v15(nil, key, hash, digest)
		}
	case *ecdsa.PrivateKey:
		sig, err = ecdsa.SignASN1(rand.Reader, key, digest)
	default:
		return nil, status.Errorf(codes.Internal, "algorithm %s holds a %T key", version.Algorithm, priv)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "signing failed: %v", err)
	}
	return sig, nil
}

// kmsHPKEImportKEM is the KEM an HPKE import method encapsulates with, or nil
// for the RSA methods.
func kmsHPKEImportKEM(importMethod string) hpke.KEM {
	switch importMethod {
	case "HPKE_KEM_ML_KEM_768_HKDF_SHA256_AES_256_GCM":
		return hpke.MLKEM768()
	case "HPKE_KEM_ML_KEM_1024_HKDF_SHA256_AES_256_GCM":
		return hpke.MLKEM1024()
	case "HPKE_KEM_XWING_HKDF_SHA256_AES_256_GCM":
		return hpke.MLKEM768X25519()
	}
	return nil
}

// kmsHPKEPublicKeyFormat is the raw format an HPKE import job's wrapping key
// takes: FIPS 203 bytes for ML-KEM, the X-Wing draft's bytes for X-Wing.
func kmsHPKEPublicKeyFormat(importMethod string) string {
	if strings.Contains(importMethod, "XWING") {
		return "XWING_RAW_BYTES"
	}
	return "NIST_PQC"
}

// kmsNewImportJobKey generates an import job's wrapping key pair.
func kmsNewImportJobKey(importMethod, protection string) (kmsKeyMaterialRecord, *kmsWrappingPubKey, error) {
	if kem := kmsHPKEImportKEM(importMethod); kem != nil {
		if protection != kmsDefaultProtectionLevel {
			return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.InvalidArgument,
				"import method %s supports only the SOFTWARE protection level, not %s", importMethod, protection)
		}
		priv, err := kem.GenerateKey()
		if err != nil {
			return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.Internal, "could not generate wrapping key: %v", err)
		}
		seed, err := priv.Bytes()
		if err != nil {
			return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.Internal, "could not serialize wrapping key: %v", err)
		}
		return kmsKeyMaterialRecord{Seed: seed}, &kmsWrappingPubKey{Data: priv.PublicKey().Bytes()}, nil
	}
	bits, ok := kmsWrappingKeyBits(importMethod)
	if !ok {
		return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.InvalidArgument, "unsupported importMethod %q", importMethod)
	}
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.Internal, "could not generate wrapping key: %v", err)
	}
	pemStr, err := kmsPublicKeyPEM(&priv.PublicKey)
	if err != nil {
		return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.Internal, "could not encode wrapping public key: %v", err)
	}
	privatePEM, err := kmsPrivateKeyPEM(priv)
	if err != nil {
		return kmsKeyMaterialRecord{}, nil, status.Errorf(codes.Internal, "could not encode wrapping private key: %v", err)
	}
	return kmsKeyMaterialRecord{PrivatePEM: privatePEM}, &kmsWrappingPubKey{Pem: pemStr}, nil
}

// kmsImportJobView renders an import job with its wrapping key in the format
// GetImportJob names; "" takes the method's default.
func kmsImportJobView(job kmsImportJob, format string) (kmsImportJob, error) {
	if format == "PUBLIC_KEY_FORMAT_UNSPECIFIED" {
		format = ""
	}
	if job.PublicKey == nil {
		return job, nil
	}
	pub := *job.PublicKey
	if kmsHPKEImportKEM(job.ImportMethod) != nil {
		if want := kmsHPKEPublicKeyFormat(job.ImportMethod); format != "" && format != want {
			return kmsImportJob{}, status.Errorf(codes.InvalidArgument, "public_key_format %s is not supported for import method %s; use %s", format, job.ImportMethod, want)
		}
	} else {
		switch format {
		case "", "PEM":
		case "DER":
			block, _ := pem.Decode([]byte(pub.Pem))
			if block == nil {
				return kmsImportJob{}, status.Error(codes.Internal, "stored wrapping key is not PEM")
			}
			pub = kmsWrappingPubKey{Data: block.Bytes}
		default:
			return kmsImportJob{}, status.Errorf(codes.InvalidArgument, "public_key_format %s is not supported for import method %s", format, job.ImportMethod)
		}
	}
	job.PublicKey = &pub
	job.PublicKeyFormat = format
	return job, nil
}

// kmsUnwrapImported recovers the key material a caller wrapped under an
// import job's public key: an HPKE Seal (enc || ciphertext, empty info) for the
// HPKE methods, RSA-OAEP with an optional AES-KWP layer otherwise.
func kmsUnwrapImported(job kmsImportJob, material kmsKeyMaterialRecord, wrapped []byte) ([]byte, error) {
	if kem := kmsHPKEImportKEM(job.ImportMethod); kem != nil {
		priv, err := kem.NewPrivateKey(material.Seed)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "ImportJob wrapping key is invalid: %v", err)
		}
		raw, err := hpke.Open(priv, hpke.HKDFSHA256(), hpke.AES256GCM(), nil, wrapped)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "could not unwrap key material: %v", err)
		}
		return raw, nil
	}
	if len(material.PrivatePEM) == 0 {
		return nil, status.Error(codes.Internal, "ImportJob wrapping key material is unavailable")
	}
	privateKey, err := kmsPrivateFromPEM(material.PrivatePEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ImportJob private key is invalid: %v", err)
	}
	rsaPrivate, ok := privateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, status.Error(codes.Internal, "ImportJob wrapping key is not RSA")
	}
	raw, err := kmsUnwrapImportJobMaterial(rsaPrivate, job.ImportMethod, wrapped)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "could not unwrap key material: %v", err)
	}
	return raw, nil
}

// kmsCryptoKeyResourceType is the resource type CryptoKey declares in its
// google.api.resource annotation, which a RetiredResource records.
func kmsCryptoKeyResourceType() string {
	options := (&kmspb.CryptoKey{}).ProtoReflect().Descriptor().Options()
	d, _ := proto.GetExtension(options, annotations.E_Resource).(*annotations.ResourceDescriptor)
	return d.GetType()
}
