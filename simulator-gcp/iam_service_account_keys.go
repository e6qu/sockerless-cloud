package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/simjwt"
)

func registerIAMServiceAccountKeys(srv *sim.Server, serviceAccounts sim.Store[GCPServiceAccount], saKeys sim.Store[GCPServiceAccountKey], saKeyPublics sim.Store[GCPServiceAccountKeyMaterial]) {
	// Create service account key — returns full key on creation only.
	// Real GCP wire: POST /v1/projects/{p}/serviceAccounts/{email}/keys
	// project="-" is the GCP wildcard: extract the project from the email.
	srv.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{email}/keys", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		sa, ok := serviceAccounts.Get(saName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		if crmOrgPolicyBooleanEnforced("projects/"+project, crmConstraintDisableServiceAccountKeyCreation) {
			GCPError(w, http.StatusBadRequest,
				"Key creation is not allowed on this service account.", "FAILED_PRECONDITION")
			return
		}
		keyID := sim.NewUUID()
		keyName := fmt.Sprintf("%s/keys/%s", saName, keyID)
		now := time.Now().UTC()
		key := GCPServiceAccountKey{
			Name:            keyName,
			KeyAlgorithm:    "KEY_ALG_RSA_2048",
			ValidAfterTime:  now.Format(time.RFC3339),
			ValidBeforeTime: now.AddDate(10, 0, 0).Format(time.RFC3339),
			KeyType:         "USER_MANAGED",
			KeyOrigin:       "GOOGLE_PROVIDED",
		}
		// Generate the key material before persisting metadata: if generation
		// fails, the store must not retain a key that never had private-key
		// material (a subsequent Get would return a phantom key).
		privateKeyData, publicKeyPEM, certificatePEM, err := gcpMakeSAKeyJSON(project, keyID, email, sa.UniqueId)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "generate key: %v", err)
			return
		}
		saKeys.Put(keyName, key)
		// Register the published halves so the OAuth2 token endpoint can verify
		// JWT-bearer assertions signed with this key, as real Google does, and
		// the keys surface can serve either publicKeyType for it.
		saKeyPublics.Put(keyName, GCPServiceAccountKeyMaterial{
			Name:           keyName,
			PublicKeyPEM:   publicKeyPEM,
			CertificatePEM: certificatePEM,
		})

		resp := key
		resp.PrivateKeyData = privateKeyData
		resp.PrivateKeyType = "TYPE_GOOGLE_CREDENTIALS_FILE"
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Upload service account key — serviceAccounts.keys.upload. The caller
	// supplies only the public half: an RSA public key wrapped in an X.509 v3
	// certificate, base64-encoded in `publicKeyData` per the Discovery
	// request schema. The private half never reaches Google, which is the
	// method's point. The key is stored USER_MANAGED / USER_PROVIDED under
	// the public-key-derived identifier serviceAccountKeyID gives every key
	// here, and registering the public half makes JWT-bearer assertions
	// signed with the matching private key verifiable at the OAuth2 token
	// endpoint — an uploaded key authenticates, exactly as against real
	// Cloud IAM.
	srv.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{email}/keys:upload", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		if _, ok := serviceAccounts.Get(saName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Service account %s not found", email)
			return
		}
		if crmOrgPolicyBooleanEnforced("projects/"+project, crmConstraintDisableServiceAccountKeyUpload) {
			GCPError(w, http.StatusBadRequest,
				"Key upload is not allowed on this service account.", "FAILED_PRECONDITION")
			return
		}
		var req struct {
			PublicKeyData string `json:"publicKeyData"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(req.PublicKeyData)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "publicKeyData must be base64: %v", err)
			return
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			GCPError(w, http.StatusBadRequest,
				"The public key must be an RSA public key wrapped in an X.509 v3 certificate, in PEM format.", "INVALID_ARGUMENT")
			return
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid X.509 certificate: %v", err)
			return
		}
		rsaPub, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			GCPError(w, http.StatusBadRequest,
				"The certificate's public key must be an RSA key.", "INVALID_ARGUMENT")
			return
		}
		// The ServiceAccountKey keyAlgorithm enum names the two RSA sizes the
		// service accepts; a certificate carrying any other size is refused
		// rather than reported under an algorithm the API does not define.
		var algorithm string
		switch rsaPub.N.BitLen() {
		case 1024:
			algorithm = "KEY_ALG_RSA_1024"
		case 2048:
			algorithm = "KEY_ALG_RSA_2048"
		default:
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"the certificate's RSA key is %d bits; service account keys must be RSA 1024 or 2048", rsaPub.N.BitLen())
			return
		}
		keyID, err := serviceAccountKeyID(rsaPub)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "derive key id: %v", err)
			return
		}
		keyName := saName + "/keys/" + keyID
		if _, exists := saKeys.Get(keyName); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"a key with the same public key material already exists on service account %s", email)
			return
		}
		pubDER, err := x509.MarshalPKIXPublicKey(rsaPub)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "marshal public key: %v", err)
			return
		}
		// The key's validity window is the certificate's own — the caller
		// minted the certificate, so the caller set the window.
		key := GCPServiceAccountKey{
			Name:            keyName,
			KeyAlgorithm:    algorithm,
			ValidAfterTime:  cert.NotBefore.UTC().Format(time.RFC3339),
			ValidBeforeTime: cert.NotAfter.UTC().Format(time.RFC3339),
			KeyType:         "USER_MANAGED",
			KeyOrigin:       "USER_PROVIDED",
		}
		saKeys.Put(keyName, key)
		saKeyPublics.Put(keyName, GCPServiceAccountKeyMaterial{
			Name:           keyName,
			PublicKeyPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
			CertificatePEM: string(pem.EncodeToMemory(block)),
		})
		sim.WriteJSON(w, http.StatusOK, key)
	})

	// Enable / disable a service account key — the two AIP-136 custom methods
	// the keys collection publishes (`keys/{key}:enable`, `keys/{key}:disable`).
	// Go's mux captures the "{key}:{verb}" segment whole; the verb is resolved
	// before the key, the way Google's frontend resolves a method before the
	// resource. Both return Empty. The disabled bit is honored where it
	// matters: the OAuth2 token endpoint refuses assertions signed with a
	// disabled key until it is re-enabled.
	srv.HandleFunc("POST /v1/projects/{project}/serviceAccounts/{email}/keys/{keyAction}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		keyID, verb, found := gcpCustomMethod(sim.PathParam(r, "keyAction"))
		if !found || (verb != "enable" && verb != "disable") {
			gcpMethodNotFound(w)
			return
		}
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		keyName := saName + "/keys/" + keyID
		if _, ok := saKeys.Get(keyName); !ok {
			// A system-managed key belongs to Google, not to the account's
			// owner: it cannot be disabled, the same refusal Delete makes.
			if system, ok := serviceAccountSystemManagedKey(serviceAccounts, saName, email); ok && system.Name == keyName {
				GCPError(w, http.StatusBadRequest,
					"Request contains an invalid argument.", "INVALID_ARGUMENT")
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "key %s not found", keyID)
			return
		}
		saKeys.Update(keyName, func(k *GCPServiceAccountKey) {
			k.Disabled = verb == "disable"
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// Get service account key. The private half is returned only by Create;
	// the public half comes back when the caller names a publicKeyType, in the
	// encoding it names. The account's system-managed key — the key signBlob
	// and signJwt sign with — is addressable here alongside its user-managed
	// keys, which is what lets a client verify a signature it was handed.
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		keyID := sim.PathParam(r, "keyId")
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		keyName := saName + "/keys/" + keyID
		publicKeyType := r.URL.Query().Get("publicKeyType")

		key, ok := saKeys.Get(keyName)
		if ok {
			material, hasMaterial := saKeyPublics.Get(keyName)
			if publicKeyType != "" && hasMaterial {
				data, err := serviceAccountPublicKeyData(publicKeyType, material.PublicKeyPEM, material.CertificatePEM)
				if err != nil {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
					return
				}
				key.PublicKeyData = data
			}
			sim.WriteJSON(w, http.StatusOK, key)
			return
		}

		system, systemOK := serviceAccountSystemManagedKey(serviceAccounts, saName, email)
		if !systemOK || system.Name != keyName {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "key %s not found", keyID)
			return
		}
		if publicKeyType != "" {
			material, err := serviceAccountSigningKey(saName, email)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "resolve signing key: %v", err)
				return
			}
			data, err := serviceAccountPublicKeyData(publicKeyType, material.publicKeyPEM, material.certificatePEM)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			system.PublicKeyData = data
		}
		sim.WriteJSON(w, http.StatusOK, system)
	})

	// List service account keys. Both key types are listed unless keyTypes
	// narrows the result, which is how the API scopes the listing.
	srv.HandleFunc("GET /v1/projects/{project}/serviceAccounts/{email}/keys", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		prefix := saName + "/keys/"
		wanted := r.URL.Query()["keyTypes"]
		keys := []GCPServiceAccountKey{}
		if serviceAccountKeyTypeWanted(wanted, "USER_MANAGED") {
			keys = append(keys, saKeys.Filter(func(k GCPServiceAccountKey) bool {
				return strings.HasPrefix(k.Name, prefix)
			})...)
		}
		if serviceAccountKeyTypeWanted(wanted, "SYSTEM_MANAGED") {
			if system, ok := serviceAccountSystemManagedKey(serviceAccounts, saName, email); ok {
				keys = append(keys, system)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
	})

	// Delete service account key.
	srv.HandleFunc("DELETE /v1/projects/{project}/serviceAccounts/{email}/keys/{keyId}", func(w http.ResponseWriter, r *http.Request) {
		project, email := iamServiceAccountRef(serviceAccounts, sim.PathParam(r, "project"), sim.PathParam(r, "email"))
		keyID := sim.PathParam(r, "keyId")
		saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", project, email)
		keyName := saName + "/keys/" + keyID
		if !saKeys.Delete(keyName) {
			// A system-managed key belongs to Google, not to the account's
			// owner, so it is refused rather than reported missing.
			if system, ok := serviceAccountSystemManagedKey(serviceAccounts, saName, email); ok && system.Name == keyName {
				GCPError(w, http.StatusBadRequest,
					"Request contains an invalid argument.", "INVALID_ARGUMENT")
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "key %s not found", keyID)
			return
		}
		// A deleted key is revoked: the token endpoint stops accepting
		// assertions signed with it, exactly as real Google does.
		saKeyPublics.Delete(keyName)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})
}

// serviceAccountKeyTypeWanted reports whether a keyTypes filter admits a key
// type. An absent filter admits every type, which is how the API lists both a
// service account's user-managed and system-managed keys by default.
func serviceAccountKeyTypeWanted(filter []string, keyType string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, want := range filter {
		if want == keyType || want == "KEY_TYPE_UNSPECIFIED" {
			return true
		}
	}
	return false
}

// serviceAccountSystemManagedKey returns the resource describing an existing
// service account's system-managed key, resolving (and on first use creating)
// the key it names. It reports false for an account that does not exist, which
// has no keys of either kind.
func serviceAccountSystemManagedKey(accounts sim.Store[GCPServiceAccount], saName, email string) (GCPServiceAccountKey, bool) {
	if _, ok := accounts.Get(saName); !ok {
		return GCPServiceAccountKey{}, false
	}
	material, err := serviceAccountSigningKey(saName, email)
	if err != nil {
		return GCPServiceAccountKey{}, false
	}
	return GCPServiceAccountKey{
		Name:            saName + "/keys/" + material.keyID,
		KeyAlgorithm:    "KEY_ALG_RSA_2048",
		ValidAfterTime:  saCertificateEpoch.Format(time.RFC3339),
		ValidBeforeTime: saCertificateEpoch.AddDate(10, 0, 0).Format(time.RFC3339),
		KeyType:         "SYSTEM_MANAGED",
		KeyOrigin:       "GOOGLE_PROVIDED",
	}, true
}

// saSystemKeyMu serialises first-use generation of a service account's
// system-managed key so two concurrent signs cannot each generate one and race
// to persist it, which would leave a signature verifiable against a key the
// store no longer holds.
var saSystemKeyMu sync.Mutex

// saSigningMaterial is a service account's system-managed key resolved for use:
// the private half signBlob and signJwt sign with, the identifier the key is
// published under, and the two encodings of the public half the IAM keys
// surface serves.
type saSigningMaterial struct {
	key            *rsa.PrivateKey
	jwt            *simjwt.Signer
	keyID          string
	publicKeyPEM   string
	certificatePEM string
}

// serviceAccountSystemKeyStore persists system-managed keys in the record
// shape iamSASystemKeys has always held, keyed by service-account name.
type serviceAccountSystemKeyStore struct{}

func (serviceAccountSystemKeyStore) Get(saName string) (string, bool) {
	rec, ok := iamSASystemKeys.Get(saName)
	return rec.PrivateKeyPEM, ok
}

func (serviceAccountSystemKeyStore) Put(saName, pemText string) {
	iamSASystemKeys.Put(saName, serviceAccountSystemKey{Name: saName, PrivateKeyPEM: pemText})
}

// serviceAccountSigningKey returns the system-managed key Google holds for a
// service account, generating and persisting it on first use. signBlob, signJwt
// and the keys surface all resolve the key through here, so the identifier a
// signature reports is the identifier the key is published under and the public
// half a client fetches verifies the signature it was given.
func serviceAccountSigningKey(saName, email string) (saSigningMaterial, error) {
	saSystemKeyMu.Lock()
	defer saSystemKeyMu.Unlock()

	loaded, err := simjwt.LoadOrCreate(serviceAccountSystemKeyStore{}, saName, simjwt.RS256)
	if err != nil {
		return saSigningMaterial{}, fmt.Errorf("system-managed key for %s: %w", saName, err)
	}
	key, ok := loaded.Key().(*rsa.PrivateKey)
	if !ok {
		return saSigningMaterial{}, fmt.Errorf("system-managed key for %s is not RSA", saName)
	}

	id, err := serviceAccountKeyID(&key.PublicKey)
	if err != nil {
		return saSigningMaterial{}, err
	}
	jwtSigner, err := simjwt.NewSigner(key, id)
	if err != nil {
		return saSigningMaterial{}, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return saSigningMaterial{}, fmt.Errorf("marshal system-managed public key for %s: %w", saName, err)
	}
	certPEM, err := serviceAccountCertificatePEM(key, email)
	if err != nil {
		return saSigningMaterial{}, err
	}
	return saSigningMaterial{
		key:            key,
		jwt:            jwtSigner,
		keyID:          id,
		publicKeyPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		certificatePEM: certPEM,
	}, nil
}

// serviceAccountKeyID derives the 40-hex-char identifier a service-account key
// is published under from its public half, so the id is stable for the life of
// the key and changes when the key does.
func serviceAccountKeyID(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal service-account public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:20]), nil
}

// serviceAccountPublicKeyData renders a key's published material in the
// encoding a `publicKeyType` names and returns it base64-encoded, which is how
// the IAM keys surface carries `publicKeyData`. An empty publicKeyType asks for
// no material and yields none, matching the API's default.
func serviceAccountPublicKeyData(publicKeyType, publicKeyPEM, certificatePEM string) (string, error) {
	switch publicKeyType {
	case "":
		return "", nil
	case "TYPE_RAW_PUBLIC_KEY":
		return base64.StdEncoding.EncodeToString([]byte(publicKeyPEM)), nil
	case "TYPE_X509_PEM_FILE":
		return base64.StdEncoding.EncodeToString([]byte(certificatePEM)), nil
	default:
		return "", fmt.Errorf("unknown publicKeyType %q", publicKeyType)
	}
}

// saCertificateEpoch fixes the validity window stamped into a service-account
// certificate so a key's certificate does not drift with the clock.
var saCertificateEpoch = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

// serviceAccountCertificatePEM wraps a service-account key in the self-signed
// X.509 certificate the TYPE_X509_PEM_FILE encoding carries, the way Google
// publishes a service account's certificate under its own email.
func serviceAccountCertificatePEM(key *rsa.PrivateKey, email string) (string, error) {
	id, err := serviceAccountKeyID(&key.PublicKey)
	if err != nil {
		return "", err
	}
	serialBytes, err := hex.DecodeString(id)
	if err != nil {
		return "", fmt.Errorf("decode key id %s: %w", id, err)
	}
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serialBytes),
		Subject:               pkix.Name{CommonName: email},
		Issuer:                pkix.Name{CommonName: email},
		NotBefore:             saCertificateEpoch,
		NotAfter:              saCertificateEpoch.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", fmt.Errorf("certify service-account key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

// gcpMakeSAKeyJSON generates a real RSA-2048 key pair and returns the private
// half encoded as a base64 GCP service-account JSON credential file — matching
// the exact shape real GCP returns for CreateServiceAccountKey — together with
// the two published encodings of the public half. The caller registers those so
// the OAuth2 token endpoint can verify assertions signed with this key and the
// keys surface can serve either publicKeyType.
func gcpMakeSAKeyJSON(project, keyID, email, clientID string) (privateKeyData, publicKeyPEM, certificatePEM string, err error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", fmt.Errorf("generate RSA key: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	certPEM, err := serviceAccountCertificatePEM(priv, email)
	if err != nil {
		return "", "", "", err
	}

	payload := map[string]string{
		"type":                        "service_account",
		"project_id":                  project,
		"private_key_id":              keyID,
		"private_key":                 string(privPEM),
		"client_email":                email,
		"client_id":                   clientID,
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + email,
		"universe_domain":             "googleapis.com",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal JSON key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), string(pubPEM), certPEM, nil
}
