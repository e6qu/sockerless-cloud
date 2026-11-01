package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/mlkem"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func kmsTestVersion(t *testing.T, algorithm string) (kmsCryptoKeyVersion, kmsKeyMaterialRecord) {
	t.Helper()
	material, err := kmsGenerateMaterial(algorithm)
	if err != nil {
		t.Fatalf("generate %s: %v", algorithm, err)
	}
	return kmsCryptoKeyVersion{Name: "projects/p/locations/global/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1", Algorithm: algorithm}, material
}

func kmsTestPublicKey(t *testing.T, version kmsCryptoKeyVersion, material kmsKeyMaterialRecord) any {
	t.Helper()
	export, err := kmsExportPublicKey(version, material, "")
	if err != nil {
		t.Fatalf("export %s: %v", version.Algorithm, err)
	}
	block, _ := pem.Decode([]byte(export.Pem))
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s public key: %v", version.Algorithm, err)
	}
	return pub
}

func requireKMSCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("error %v, want code %s", err, want)
	}
}

func TestKMSAsymmetricSignUsesTheAlgorithmsDigest(t *testing.T) {
	message := []byte("sign me with the key's own digest")
	sum384 := sha512.Sum384(message)
	sum512 := sha512.Sum512(message)
	sum256 := sha256.Sum256(message)

	t.Run("EC P-384 signs a SHA-384 digest and refuses SHA-256", func(t *testing.T) {
		version, material := kmsTestVersion(t, "EC_SIGN_P384_SHA384")
		pub := kmsTestPublicKey(t, version, material).(*ecdsa.PublicKey)
		sig, err := kmsAsymmetricSign(version, material, kmsSignInput{Digest: map[string][]byte{"sha384": sum384[:]}})
		if err != nil {
			t.Fatal(err)
		}
		if !ecdsa.VerifyASN1(pub, sum384[:], sig) {
			t.Fatal("signature does not verify over the SHA-384 digest")
		}
		sig, err = kmsAsymmetricSign(version, material, kmsSignInput{Data: message})
		if err != nil {
			t.Fatal(err)
		}
		if !ecdsa.VerifyASN1(pub, sum384[:], sig) {
			t.Fatal("data must be hashed with SHA-384")
		}
		_, err = kmsAsymmetricSign(version, material, kmsSignInput{Digest: map[string][]byte{"sha256": sum256[:]}})
		requireKMSCode(t, err, codes.InvalidArgument)
	})

	t.Run("RSA PSS SHA-512 signs a SHA-512 digest", func(t *testing.T) {
		version, material := kmsTestVersion(t, "RSA_SIGN_PSS_4096_SHA512")
		pub := kmsTestPublicKey(t, version, material).(*rsa.PublicKey)
		sig, err := kmsAsymmetricSign(version, material, kmsSignInput{Data: message})
		if err != nil {
			t.Fatal(err)
		}
		if err := rsa.VerifyPSS(pub, crypto.SHA512, sum512[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
			t.Fatalf("PSS signature does not verify over SHA-512: %v", err)
		}
		_, err = kmsAsymmetricSign(version, material, kmsSignInput{Digest: map[string][]byte{"sha512": sum256[:]}})
		requireKMSCode(t, err, codes.InvalidArgument)
	})

	t.Run("digest and data together are refused", func(t *testing.T) {
		version, material := kmsTestVersion(t, "EC_SIGN_P256_SHA256")
		_, err := kmsAsymmetricSign(version, material, kmsSignInput{Digest: map[string][]byte{"sha256": sum256[:]}, Data: message})
		requireKMSCode(t, err, codes.InvalidArgument)
	})

	t.Run("raw PKCS #1 signs the data itself", func(t *testing.T) {
		version, material := kmsTestVersion(t, "RSA_SIGN_RAW_PKCS1_2048")
		pub := kmsTestPublicKey(t, version, material).(*rsa.PublicKey)
		sig, err := kmsAsymmetricSign(version, material, kmsSignInput{Data: message})
		if err != nil {
			t.Fatal(err)
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.Hash(0), message, sig); err != nil {
			t.Fatalf("raw signature does not verify over the data: %v", err)
		}
		_, err = kmsAsymmetricSign(version, material, kmsSignInput{Digest: map[string][]byte{"sha256": sum256[:]}})
		requireKMSCode(t, err, codes.InvalidArgument)
		_, err = kmsAsymmetricSign(version, material, kmsSignInput{Data: make([]byte, 2048/8-10)})
		requireKMSCode(t, err, codes.InvalidArgument)
	})

	t.Run("Ed25519 signs the data itself", func(t *testing.T) {
		version, material := kmsTestVersion(t, "EC_SIGN_ED25519")
		pub := kmsTestPublicKey(t, version, material).(ed25519.PublicKey)
		sig, err := kmsAsymmetricSign(version, material, kmsSignInput{Data: message})
		if err != nil {
			t.Fatal(err)
		}
		if !ed25519.Verify(pub, message, sig) {
			t.Fatal("Ed25519 signature does not verify")
		}
		_, err = kmsAsymmetricSign(version, material, kmsSignInput{Digest: map[string][]byte{"sha512": sum512[:]}})
		requireKMSCode(t, err, codes.InvalidArgument)
	})
}

func TestKMSDecapsulateRecoversTheEncapsulatedSecret(t *testing.T) {
	for _, algorithm := range []string{"ML_KEM_768", "ML_KEM_1024"} {
		t.Run(algorithm, func(t *testing.T) {
			version, material := kmsTestVersion(t, algorithm)
			_, err := kmsExportPublicKey(version, material, "")
			requireKMSCode(t, err, codes.InvalidArgument)
			_, err = kmsExportPublicKey(version, material, "PEM")
			requireKMSCode(t, err, codes.InvalidArgument)
			export, err := kmsExportPublicKey(version, material, "NIST_PQC")
			if err != nil {
				t.Fatal(err)
			}
			var secret, ciphertext []byte
			if algorithm == "ML_KEM_768" {
				ek, err := mlkem.NewEncapsulationKey768(export.Data)
				if err != nil {
					t.Fatal(err)
				}
				secret, ciphertext = ek.Encapsulate()
			} else {
				ek, err := mlkem.NewEncapsulationKey1024(export.Data)
				if err != nil {
					t.Fatal(err)
				}
				secret, ciphertext = ek.Encapsulate()
			}
			got, err := kmsDecapsulate(version, material, ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(secret) {
				t.Fatal("decapsulated secret differs from the encapsulated one")
			}
			_, err = kmsDecapsulate(version, material, ciphertext[1:])
			requireKMSCode(t, err, codes.InvalidArgument)
		})
	}
	version, material := kmsTestVersion(t, "GOOGLE_SYMMETRIC_ENCRYPTION")
	_, err := kmsDecapsulate(version, material, make([]byte, 1088))
	requireKMSCode(t, err, codes.FailedPrecondition)
}

func TestKMSHPKEImportUnwrapsWhatHPKESealWrapped(t *testing.T) {
	for _, method := range []string{
		"HPKE_KEM_ML_KEM_768_HKDF_SHA256_AES_256_GCM",
		"HPKE_KEM_ML_KEM_1024_HKDF_SHA256_AES_256_GCM",
		"HPKE_KEM_XWING_HKDF_SHA256_AES_256_GCM",
	} {
		t.Run(method, func(t *testing.T) {
			_, _, err := kmsNewImportJobKey(method, "HSM")
			requireKMSCode(t, err, codes.InvalidArgument)

			material, pub, err := kmsNewImportJobKey(method, "SOFTWARE")
			if err != nil {
				t.Fatal(err)
			}
			job := kmsImportJob{ImportMethod: method, PublicKey: pub}
			view, err := kmsImportJobView(job, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = kmsImportJobView(job, "PEM")
			requireKMSCode(t, err, codes.InvalidArgument)

			kem := kmsHPKEImportKEM(method)
			recipient, err := kem.NewPublicKey(view.PublicKey.Data)
			if err != nil {
				t.Fatal(err)
			}
			target := []byte("0123456789abcdef0123456789abcdef")
			wrapped, err := hpke.Seal(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), nil, target)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := kmsUnwrapImported(job, material, wrapped)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != string(target) {
				t.Fatal("unwrapped material differs from what was sealed")
			}
			wrapped[len(wrapped)-1] ^= 1
			_, err = kmsUnwrapImported(job, material, wrapped)
			requireKMSCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestKMSUnservedAlgorithmsAreRefused(t *testing.T) {
	for _, algorithm := range []string{"KEM_XWING", "KEM_ECDH_P256", "PQ_SIGN_ML_DSA_65", "EC_SIGN_SECP256K1_SHA256"} {
		_, err := kmsGenerateMaterial(algorithm)
		requireKMSCode(t, err, codes.Unimplemented)
	}
}

func TestKMSCryptoKeyResourceTypeComesFromTheProto(t *testing.T) {
	if got := kmsCryptoKeyResourceType(); got != "cloudkms.googleapis.com/CryptoKey" {
		t.Fatalf("CryptoKey resource type = %q", got)
	}
}
