package main

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud KMS v1 slice (cloudkms.googleapis.com). Models keyRings,
// cryptoKeys, cryptoKeyVersions, and symmetric encrypt/decrypt so a
// key-management client (the google.golang.org/api/cloudkms REST client
// and `gcloud kms`) has a real endpoint to hit. AWS KMS and Azure Key
// Vault keys are already simulated; this brings GCP to parity.
//
// Crypto is real, not faked: each ENABLED cryptoKeyVersion gets a random
// AES-256 key (non-exportable, like real KMS), and encrypt/decrypt use
// AES-256-GCM. The ciphertext is opaque to the client and round-trips.
// Real API: https://cloud.google.com/kms/docs/reference/rest

const (
	kmsDefaultProtectionLevel = "SOFTWARE"
	kmsSymmetricAlgorithm     = "GOOGLE_SYMMETRIC_ENCRYPTION"
	kmsPurposeEncryptDecrypt  = "ENCRYPT_DECRYPT"
	// kmsDefaultDestroyScheduledDuration is how long a version spends in
	// DESTROY_SCHEDULED before it becomes DESTROYED, when its CryptoKey did
	// not set destroyScheduledDuration. Cloud KMS documents 30 days.
	kmsDefaultDestroyScheduledDuration = 30 * 24 * time.Hour
)

// kmsKeyRing is the wire shape for a KeyRing resource.
type kmsKeyRing struct {
	Name       string `json:"name"`
	CreateTime string `json:"createTime"`
}

// kmsCryptoKeyVersionTemplate is the versionTemplate on a CryptoKey.
type kmsCryptoKeyVersionTemplate struct {
	ProtectionLevel string `json:"protectionLevel,omitempty"`
	Algorithm       string `json:"algorithm,omitempty"`
}

// kmsCryptoKeyVersion is the wire shape for a CryptoKeyVersion.
type kmsCryptoKeyVersion struct {
	Name                   string `json:"name"`
	State                  string `json:"state,omitempty"`
	ProtectionLevel        string `json:"protectionLevel,omitempty"`
	Algorithm              string `json:"algorithm,omitempty"`
	CreateTime             string `json:"createTime,omitempty"`
	GenerateTime           string `json:"generateTime,omitempty"`
	DestroyTime            string `json:"destroyTime,omitempty"`
	DestroyEventTime       string `json:"destroyEventTime,omitempty"`
	ImportJob              string `json:"importJob,omitempty"`
	ImportTime             string `json:"importTime,omitempty"`
	TrustedWrappingEnabled bool   `json:"trustedWrappingEnabled,omitempty"`
	HSMTrusted             bool   `json:"hsmTrusted,omitempty"`
	ReimportEligible       bool   `json:"reimportEligible,omitempty"`
}

// kmsCryptoKey is the wire shape for a CryptoKey. `primary` is assembled
// from the live primary version on read so destroy/disable state shows
// through.
type kmsCryptoKey struct {
	Name                     string                       `json:"name"`
	Primary                  *kmsCryptoKeyVersion         `json:"primary,omitempty"`
	Purpose                  string                       `json:"purpose,omitempty"`
	CreateTime               string                       `json:"createTime,omitempty"`
	NextRotationTime         string                       `json:"nextRotationTime,omitempty"`
	RotationPeriod           string                       `json:"rotationPeriod,omitempty"`
	DestroyScheduledDuration string                       `json:"destroyScheduledDuration,omitempty"`
	VersionTemplate          *kmsCryptoKeyVersionTemplate `json:"versionTemplate,omitempty"`
	Labels                   map[string]string            `json:"labels,omitempty"`
}

// kmsStoredCryptoKey is the persisted CryptoKey metadata. Versions live
// in their own store; the primary is resolved by ID at read time.
type kmsStoredCryptoKey struct {
	Name             string            `json:"name"`
	Purpose          string            `json:"purpose"`
	CreateTime       string            `json:"createTime"`
	NextRotationTime string            `json:"nextRotationTime,omitempty"`
	RotationPeriod   string            `json:"rotationPeriod,omitempty"`
	ProtectionLevel  string            `json:"protectionLevel"`
	Algorithm        string            `json:"algorithm"`
	Labels           map[string]string `json:"labels,omitempty"`
	PrimaryVersionID string            `json:"primaryVersionId"`
	// DestroyScheduledDuration is immutable and set at creation; it decides
	// when a scheduled destroy completes.
	DestroyScheduledDuration string `json:"destroyScheduledDuration,omitempty"`
	// VersionSeq is the monotonic version counter for this key. Reserved
	// atomically under the store write lock so concurrent
	// CreateCryptoKeyVersion calls never collide on a version ID. Stored only;
	// kmsAssembleCryptoKey never copies it onto the wire CryptoKey.
	VersionSeq int `json:"versionSeq,omitempty"`
}

// kmsKeyMaterialRecord holds the (non-exportable) key material for a
// version, keyed by full version name. Never surfaced on the wire.
//
// Symmetric (ENCRYPT_DECRYPT / RAW_ENCRYPT_DECRYPT) and MAC keys keep their
// raw bytes in Key. Asymmetric keys (ASYMMETRIC_SIGN / ASYMMETRIC_DECRYPT)
// keep a real, freshly-generated RSA or EC private key in PrivatePEM (PKCS#8)
// so signatures and OAEP decryptions are produced with Go's standard library —
// the public half is derived from it on demand for getPublicKey.
type kmsKeyMaterialRecord struct {
	Key        []byte `json:"key,omitempty"`
	PrivatePEM []byte `json:"privatePem,omitempty"`
}

// kmsIamPolicy mirrors google.iam.v1.Policy as the Cloud KMS Discovery
// document defines it (version / bindings / auditConfigs / etag). Stored per
// IAM-bearing resource (keyRing, cryptoKey, importJob, ekmConnection, ekmConfig).
type kmsIamPolicy struct {
	Version      int               `json:"version,omitempty"`
	Bindings     []kmsIamBinding   `json:"bindings,omitempty"`
	AuditConfigs []json.RawMessage `json:"auditConfigs,omitempty"`
	Etag         string            `json:"etag,omitempty"`
}

// kmsIamBinding is one role→members binding in a kmsIamPolicy.
type kmsIamBinding struct {
	Role      string          `json:"role"`
	Members   []string        `json:"members,omitempty"`
	Condition json.RawMessage `json:"condition,omitempty"`
}

// kmsImportJob is the wire shape for an ImportJob. Wrapping-key material is a
// real RSA key pair so a client can fetch the publicKey and the sim could (in a
// future import-method round-trip) unwrap. The metadata and lifecycle states
// are faithful to the real API.
type kmsImportJob struct {
	Name             string             `json:"name"`
	ImportMethod     string             `json:"importMethod,omitempty"`
	ProtectionLevel  string             `json:"protectionLevel,omitempty"`
	State            string             `json:"state,omitempty"`
	PublicKey        *kmsWrappingPubKey `json:"publicKey,omitempty"`
	CreateTime       string             `json:"createTime,omitempty"`
	GenerateTime     string             `json:"generateTime,omitempty"`
	ExpireTime       string             `json:"expireTime,omitempty"`
	ExpireEventTime  string             `json:"expireEventTime,omitempty"`
	Attestation      json.RawMessage    `json:"attestation,omitempty"`
	CryptoKeyBackend string             `json:"cryptoKeyBackend,omitempty"`
}

// kmsWrappingPubKey is the WrappingPublicKey on an ImportJob.
type kmsWrappingPubKey struct {
	Pem string `json:"pem,omitempty"`
}

// kmsEkmConnection is the wire shape for an EkmConnection — metadata pointing at
// an external key manager. The sim faithfully persists and returns the CRUD
// metadata; the actual cryptographic operations against the external HSM/EKM are
// out of scope (the external manager is not part of the simulator), so an
// EkmConnection is a metadata-only resource by design.
type kmsEkmConnection struct {
	Name              string            `json:"name"`
	CreateTime        string            `json:"createTime,omitempty"`
	ServiceResolvers  []json.RawMessage `json:"serviceResolvers,omitempty"`
	Etag              string            `json:"etag,omitempty"`
	KeyManagementMode string            `json:"keyManagementMode,omitempty"`
	CryptoSpacePath   string            `json:"cryptoSpacePath,omitempty"`
}

// kmsEkmConfig is the per-location EkmConfig singleton.
type kmsEkmConfig struct {
	Name                 string `json:"name"`
	DefaultEkmConnection string `json:"defaultEkmConnection,omitempty"`
}

// kmsKeyHandle is the wire shape for a KeyHandle (Autokey).
type kmsKeyHandle struct {
	Name                 string `json:"name"`
	KmsKey               string `json:"kmsKey,omitempty"`
	ResourceTypeSelector string `json:"resourceTypeSelector,omitempty"`
}

// kmsAutokeyConfig is the per-folder/project AutokeyConfig singleton.
type kmsAutokeyConfig struct {
	Name                     string `json:"name"`
	KeyProject               string `json:"keyProject,omitempty"`
	State                    string `json:"state,omitempty"`
	KeyProjectResolutionMode string `json:"keyProjectResolutionMode,omitempty"`
	Etag                     string `json:"etag,omitempty"`
}

type kmsEffectiveAutokeyConfig struct {
	KeyProject               string `json:"keyProject,omitempty"`
	KeyProjectResolutionMode string `json:"keyProjectResolutionMode,omitempty"`
	Source                   *struct {
		Name string `json:"name"`
	} `json:"source,omitempty"`
}

// kmsKajPolicyConfig is the per-resource KeyAccessJustificationsPolicyConfig.
type kmsKajPolicyConfig struct {
	Name                           string          `json:"name"`
	DefaultKeyAccessJustifications json.RawMessage `json:"defaultKeyAccessJustificationPolicy,omitempty"`
}

// kmsSingleTenantHsmInstance is the wire shape for a SingleTenantHsmInstance.
type kmsSingleTenantHsmInstance struct {
	Name                  string `json:"name"`
	State                 string `json:"state,omitempty"`
	CreateTime            string `json:"createTime,omitempty"`
	KeyPortabilityEnabled bool   `json:"keyPortabilityEnabled,omitempty"`
}

// kmsHsmProposal is the wire shape for a SingleTenantHsmInstanceProposal.
type kmsHsmProposal struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

// kmsRetiredResource is the wire shape for a RetiredResource.
type kmsRetiredResource struct {
	Name             string `json:"name"`
	ResourceType     string `json:"resourceType,omitempty"`
	OriginalResource string `json:"originalResource,omitempty"`
	DeleteTime       string `json:"deleteTime,omitempty"`
}

var (
	kmsKeyRings          sim.Store[kmsKeyRing]
	kmsCryptoKeys        sim.Store[kmsStoredCryptoKey]
	kmsCryptoKeyVersions sim.Store[kmsCryptoKeyVersion]
	kmsKeyMaterial       sim.Store[kmsKeyMaterialRecord]
	kmsIamPolicies       sim.Store[kmsIamPolicy]
	kmsImportJobs        sim.Store[kmsImportJob]
	kmsImportJobMaterial sim.Store[kmsKeyMaterialRecord]
	kmsEkmConnections    sim.Store[kmsEkmConnection]
	kmsEkmConfigs        sim.Store[kmsEkmConfig]
	kmsKeyHandles        sim.Store[kmsKeyHandle]
	kmsAutokeyConfigs    sim.Store[kmsAutokeyConfig]
	kmsKajPolicyConfigs  sim.Store[kmsKajPolicyConfig]
	kmsHsmInstances      sim.Store[kmsSingleTenantHsmInstance]
	kmsHsmProposals      sim.Store[kmsHsmProposal]
	kmsRetiredResources  sim.Store[kmsRetiredResource]
	kmsCRC32CTable       = crc32.MakeTable(crc32.Castagnoli)
)

func registerCloudKMS(srv *sim.Server) {
	kmsKeyRings = sim.MakeStore[kmsKeyRing](srv.DB(), "kms_key_rings")
	kmsCryptoKeys = sim.MakeStore[kmsStoredCryptoKey](srv.DB(), "kms_crypto_keys")
	kmsCryptoKeyVersions = sim.MakeStore[kmsCryptoKeyVersion](srv.DB(), "kms_crypto_key_versions")
	kmsKeyMaterial = sim.MakeStore[kmsKeyMaterialRecord](srv.DB(), "kms_key_material")
	kmsIamPolicies = sim.MakeStore[kmsIamPolicy](srv.DB(), "kms_iam_policies")
	kmsImportJobs = sim.MakeStore[kmsImportJob](srv.DB(), "kms_import_jobs")
	kmsImportJobMaterial = sim.MakeStore[kmsKeyMaterialRecord](srv.DB(), "kms_import_job_material")
	kmsEkmConnections = sim.MakeStore[kmsEkmConnection](srv.DB(), "kms_ekm_connections")
	kmsEkmConfigs = sim.MakeStore[kmsEkmConfig](srv.DB(), "kms_ekm_configs")
	kmsKeyHandles = sim.MakeStore[kmsKeyHandle](srv.DB(), "kms_key_handles")
	kmsAutokeyConfigs = sim.MakeStore[kmsAutokeyConfig](srv.DB(), "kms_autokey_configs")
	kmsKajPolicyConfigs = sim.MakeStore[kmsKajPolicyConfig](srv.DB(), "kms_kaj_policy_configs")
	kmsHsmInstances = sim.MakeStore[kmsSingleTenantHsmInstance](srv.DB(), "kms_hsm_instances")
	kmsHsmProposals = sim.MakeStore[kmsHsmProposal](srv.DB(), "kms_hsm_proposals")
	kmsRetiredResources = sim.MakeStore[kmsRetiredResource](srv.DB(), "kms_retired_resources")

	// CreateKeyRing: POST .../keyRings?keyRingId=X
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		id := r.URL.Query().Get("keyRingId")
		if id == "" {
			GCPError(w, http.StatusBadRequest, "keyRingId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		name := kmsKeyRingName(project, location, id)
		if _, exists := kmsKeyRings.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "KeyRing %s already exists", name)
			return
		}
		kr := kmsKeyRing{Name: name, CreateTime: kmsNow()}
		kmsKeyRings.Put(name, kr)
		sim.WriteJSON(w, http.StatusOK, kr)
	})

	// ListKeyRings: GET .../keyRings
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := kmsKeyRingName(project, location, "")
		var all []kmsKeyRing
		for _, kr := range kmsKeyRings.List() {
			if strings.HasPrefix(kr.Name, prefix) {
				all = append(all, kr)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"keyRings": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetKeyRing: GET .../keyRings/{keyRing}. Also fans in the GET colon-verb
	// GetIamPolicy ("{keyRing}:getIamPolicy").
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}", func(w http.ResponseWriter, r *http.Request) {
		ringID, action, isAction := strings.Cut(sim.PathParam(r, "keyRing"), ":")
		name := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), ringID)
		if isAction {
			if action != "getIamPolicy" {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown keyRing action %q", action)
				return
			}
			kmsHandleGetIamPolicy(w, r, name)
			return
		}
		kr, ok := kmsKeyRings.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "KeyRing %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, kr)
	})

	// A key ring is deleted only when it holds no keys. Cloud KMS never deletes
	// key material as a side effect of removing its container, so a ring with
	// keys in it is refused rather than taking them with it.
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		if _, ok := kmsKeyRings.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "KeyRing %s not found", name)
			return
		}
		held := kmsCryptoKeys.Filter(func(k kmsStoredCryptoKey) bool {
			return strings.HasPrefix(k.Name, name+"/cryptoKeys/")
		})
		if len(held) > 0 {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"KeyRing %s still holds %d key(s); a key ring is deleted only when it is empty", name, len(held))
			return
		}
		kmsKeyRings.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name": name + "/operations/delete", "done": true,
		})
	})

	// SetIamPolicy / TestIamPermissions on a keyRing:
	//   POST .../keyRings/{keyRingAction} where keyRingAction is "{keyRing}:verb".
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRingAction}", func(w http.ResponseWriter, r *http.Request) {
		ringID, action, found := strings.Cut(sim.PathParam(r, "keyRingAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown keyRing action %q", sim.PathParam(r, "keyRingAction"))
			return
		}
		name := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), ringID)
		switch action {
		case "setIamPolicy":
			kmsHandleSetIamPolicy(w, r, name)
		case "testIamPermissions":
			kmsHandleTestIamPermissions(w, r, name)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown keyRing action %q", action)
		}
	})

	// CreateCryptoKey: POST .../keyRings/{keyRing}/cryptoKeys?cryptoKeyId=X
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys", func(w http.ResponseWriter, r *http.Request) {
		project, location, keyRing := sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing")
		ringName := kmsKeyRingName(project, location, keyRing)
		if _, ok := kmsKeyRings.Get(ringName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "KeyRing %s not found", ringName)
			return
		}
		id := r.URL.Query().Get("cryptoKeyId")
		if id == "" {
			GCPError(w, http.StatusBadRequest, "cryptoKeyId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var req kmsCryptoKey
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := ringName + "/cryptoKeys/" + id
		if _, exists := kmsCryptoKeys.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "CryptoKey %s already exists", name)
			return
		}
		purpose := req.Purpose
		if purpose == "" {
			purpose = kmsPurposeEncryptDecrypt
		}
		protection, algorithm := kmsDefaultProtectionLevel, kmsSymmetricAlgorithm
		if req.VersionTemplate != nil {
			if req.VersionTemplate.ProtectionLevel != "" {
				protection = req.VersionTemplate.ProtectionLevel
			}
			if req.VersionTemplate.Algorithm != "" {
				algorithm = req.VersionTemplate.Algorithm
			}
		}
		stored := kmsStoredCryptoKey{
			Name:             name,
			Purpose:          purpose,
			CreateTime:       kmsNow(),
			NextRotationTime: req.NextRotationTime,
			RotationPeriod:   req.RotationPeriod,

			DestroyScheduledDuration: kmsDestroyScheduledDuration(req.DestroyScheduledDuration),

			ProtectionLevel: protection,
			Algorithm:       algorithm,
			Labels:          req.Labels,
		}
		// Every purpose gets an initial version unless the caller opts out.
		// Real KMS auto-creates the first CryptoKeyVersion (with material of the
		// version-template algorithm) for ENCRYPT_DECRYPT, MAC, ASYMMETRIC_* and
		// RAW_ENCRYPT_DECRYPT keys. The `primary` pointer is only meaningful for
		// (RAW_)ENCRYPT_DECRYPT keys, so PrimaryVersionID tracks it just for
		// those — asymmetric/MAC ops address a specific version by name.
		if r.URL.Query().Get("skipInitialVersionCreation") != "true" {
			ver, err := kmsCreateVersionForAlg(name, "1", protection, algorithm)
			if err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not generate key version: %v", err)
				return
			}
			stored.VersionSeq = 1
			if r.URL.Query().Get("trustedWrappingEnabled") == "true" {
				versionName := name + "/cryptoKeyVersions/" + ver
				version, _ := kmsGetCryptoKeyVersion(versionName)
				version.TrustedWrappingEnabled = true
				kmsCryptoKeyVersions.Put(versionName, version)
			}
			if purpose == kmsPurposeEncryptDecrypt || purpose == "RAW_ENCRYPT_DECRYPT" {
				stored.PrimaryVersionID = ver
			}
		}
		kmsCryptoKeys.Put(name, stored)
		sim.WriteJSON(w, http.StatusOK, kmsAssembleCryptoKey(stored))
	})

	// ListCryptoKeys: GET .../keyRings/{keyRing}/cryptoKeys
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		prefix := ringName + "/cryptoKeys/"
		var all []kmsCryptoKey
		for _, k := range kmsCryptoKeys.List() {
			if strings.HasPrefix(k.Name, prefix) {
				all = append(all, kmsAssembleCryptoKey(k))
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"cryptoKeys": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetCryptoKey: GET .../cryptoKeys/{cryptoKey}. The handler also fans in
	// the GET colon-verb GetIamPolicy ("{cryptoKey}:getIamPolicy"), since
	// Go's mux can't spell `{id}:verb` as a distinct pattern.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		keyID, action, isAction := strings.Cut(sim.PathParam(r, "cryptoKey"), ":")
		name := ringName + "/cryptoKeys/" + keyID
		if isAction {
			if action != "getIamPolicy" {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown cryptoKey action %q", action)
				return
			}
			kmsHandleGetIamPolicy(w, r, name)
			return
		}
		k, ok := kmsCryptoKeys.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, kmsAssembleCryptoKey(k))
	})

	// UpdateCryptoKey: PATCH .../cryptoKeys/{cryptoKey}?updateMask=...
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsCryptoKeyName(r)
		k, ok := kmsCryptoKeys.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", name)
			return
		}
		var req kmsCryptoKey
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		for _, field := range strings.Split(mask, ",") {
			switch strings.TrimSpace(field) {
			case "rotationPeriod":
				k.RotationPeriod = req.RotationPeriod
			case "nextRotationTime":
				k.NextRotationTime = req.NextRotationTime
			case "labels":
				k.Labels = req.Labels
			}
		}
		kmsCryptoKeys.Put(name, k)
		sim.WriteJSON(w, http.StatusOK, kmsAssembleCryptoKey(k))
	})

	// Encrypt / Decrypt: POST .../cryptoKeys/{cryptoKeyAction}
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKeyAction}", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		keyID, action, found := strings.Cut(sim.PathParam(r, "cryptoKeyAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown cryptoKey action %q", sim.PathParam(r, "cryptoKeyAction"))
			return
		}
		name := ringName + "/cryptoKeys/" + keyID
		switch action {
		case "encrypt":
			kmsHandleEncrypt(w, r, name)
		case "decrypt":
			kmsHandleDecrypt(w, r, name)
		case "updatePrimaryVersion":
			kmsHandleUpdatePrimaryVersion(w, r, name)
		case "setIamPolicy":
			kmsHandleSetIamPolicy(w, r, name)
		case "testIamPermissions":
			kmsHandleTestIamPermissions(w, r, name)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown cryptoKey action %q", action)
		}
	})

	// ListCryptoKeyVersions: GET .../cryptoKeys/{cryptoKey}/cryptoKeyVersions
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions", func(w http.ResponseWriter, r *http.Request) {
		keyName := kmsCryptoKeyName(r)
		if _, ok := kmsCryptoKeys.Get(keyName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
			return
		}
		prefix := keyName + "/cryptoKeyVersions/"
		var all []kmsCryptoKeyVersion
		for _, v := range kmsCryptoKeyVersions.List() {
			if strings.HasPrefix(v.Name, prefix) {
				all = append(all, kmsVersionSettled(v))
			}
		}
		sort.Slice(all, func(i, j int) bool { return kmsVersionLess(all[i].Name, all[j].Name) })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"cryptoKeyVersions": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetCryptoKeyVersion: GET .../cryptoKeyVersions/{cryptoKeyVersion}
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}", func(w http.ResponseWriter, r *http.Request) {
		versionID, action, isCustomMethod := gcpCustomMethod(sim.PathParam(r, "cryptoKeyVersion"))
		if isCustomMethod {
			if action == "exportTrustedKeyWrappedCryptoKeyVersion" {
				kmsHandleExportTrustedKeyWrappedCryptoKeyVersion(
					w, r, kmsCryptoKeyName(r)+"/cryptoKeyVersions/"+versionID,
				)
				return
			}
			gcpMethodNotFound(w)
			return
		}
		name := kmsCryptoKeyName(r) + "/cryptoKeyVersions/" + versionID
		v, ok := kmsGetCryptoKeyVersion(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKeyVersion %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})

	// CreateCryptoKeyVersion: POST .../cryptoKeys/{cryptoKey}/cryptoKeyVersions
	// Mints a fresh ENABLED version with its own AES-256 key material. The
	// numeric ID is one past the current highest version. terraform's
	// google_kms_crypto_key_version provisions versions this way.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions", func(w http.ResponseWriter, r *http.Request) {
		keyName := kmsCryptoKeyName(r)
		key, ok := kmsCryptoKeys.Get(keyName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
			return
		}
		var req kmsCryptoKeyVersion
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// Reserve the next version ID atomically by bumping the per-key counter
		// under the store write lock, so concurrent CreateCryptoKeyVersion calls
		// never collide on a version ID.
		var assigned int
		kmsCryptoKeys.Update(keyName, func(k *kmsStoredCryptoKey) {
			if k.VersionSeq < kmsHighestVersionID(keyName) {
				k.VersionSeq = kmsHighestVersionID(keyName)
			}
			k.VersionSeq++
			assigned = k.VersionSeq
		})
		next := fmt.Sprintf("%d", assigned)
		versionID, err := kmsCreateVersion(keyName, next, key.ProtectionLevel, key.Algorithm)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not generate key version: %v", err)
			return
		}
		v, _ := kmsGetCryptoKeyVersion(keyName + "/cryptoKeyVersions/" + versionID)
		v.TrustedWrappingEnabled = req.TrustedWrappingEnabled
		kmsCryptoKeyVersions.Put(v.Name, v)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions:importTrustedKeyWrappedCryptoKeyVersion", func(w http.ResponseWriter, r *http.Request) {
		kmsHandleImportTrustedKeyWrappedCryptoKeyVersion(w, r, kmsCryptoKeyName(r))
	})

	// UpdateCryptoKeyVersion: PATCH .../cryptoKeyVersions/{cryptoKeyVersion}?updateMask=state
	// The canonical enable/disable toggle — terraform flips a version
	// between ENABLED and DISABLED through this PATCH, not a dedicated verb.
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsCryptoKeyName(r) + "/cryptoKeyVersions/" + sim.PathParam(r, "cryptoKeyVersion")
		v, ok := kmsGetCryptoKeyVersion(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKeyVersion %s not found", name)
			return
		}
		var req kmsCryptoKeyVersion
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			if strings.TrimSpace(field) != "state" {
				continue
			}
			if req.State != "ENABLED" && req.State != "DISABLED" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"state may only be set to ENABLED or DISABLED, got %q", req.State)
				return
			}
			v.State = req.State
		}
		kmsCryptoKeyVersions.Put(name, v)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	// DestroyCryptoKeyVersion / RestoreCryptoKeyVersion:
	// POST .../cryptoKeyVersions/{cryptoKeyVersionAction}
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersionAction}", func(w http.ResponseWriter, r *http.Request) {
		versionID, action, found := strings.Cut(sim.PathParam(r, "cryptoKeyVersionAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown cryptoKeyVersion action %q", sim.PathParam(r, "cryptoKeyVersionAction"))
			return
		}
		name := kmsCryptoKeyName(r) + "/cryptoKeyVersions/" + versionID
		switch action {
		case "asymmetricSign":
			kmsHandleAsymmetricSign(w, r, name)
			return
		case "asymmetricDecrypt":
			kmsHandleAsymmetricDecrypt(w, r, name)
			return
		case "macSign":
			kmsHandleMacSign(w, r, name)
			return
		case "macVerify":
			kmsHandleMacVerify(w, r, name)
			return
		case "rawEncrypt":
			kmsHandleRawEncrypt(w, r, name)
			return
		case "rawDecrypt":
			kmsHandleRawDecrypt(w, r, name)
			return
		case "decapsulate":
			// Key-encapsulation (ML-KEM / X-Wing) decapsulation is a
			// post-quantum primitive Go's standard library does not yet
			// expose. The route exists for API-surface fidelity; the op is
			// rejected as unimplemented rather than faked.
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "decapsulate is not supported for CryptoKeyVersion %s", name)
			return
		}
		if action != "destroy" && action != "restore" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown cryptoKeyVersion action %q", action)
			return
		}
		v, ok := kmsGetCryptoKeyVersion(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKeyVersion %s not found", name)
			return
		}
		switch action {
		case "destroy":
			if v.State == "DESTROY_SCHEDULED" || v.State == "DESTROYED" {
				GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is already %s", name, v.State)
				return
			}
			v.State = "DESTROY_SCHEDULED"
			v.DestroyTime = time.Now().UTC().Add(kmsDestroyDelayFor(kmsCryptoKeyName(r))).Format(time.RFC3339)
		case "restore":
			if v.State != "DESTROY_SCHEDULED" {
				GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not DESTROY_SCHEDULED (state %s)", name, v.State)
				return
			}
			v.State = "DISABLED"
			v.DestroyTime = ""
		}
		kmsCryptoKeyVersions.Put(name, v)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	registerCloudKMSExtras(srv)
}

// registerCloudKMSExtras mounts the remaining Cloud KMS surface: cryptoKey /
// version deletes, importJobs, ekmConnections, ekmConfig, keyHandles,
// autokeyConfig / kajPolicyConfig singletons, singleTenantHsmInstances +
// proposals + retiredResources, the location-level generateRandomBytes verb, and
// the per-version publicKey read. It is split out of registerCloudKMS purely to
// keep each function a readable length.
func registerCloudKMSExtras(srv *sim.Server) {
	// DeleteCryptoKeyVersion is not a real KMS op (versions are destroyed via
	// :destroy, not DELETE) — only DeleteCryptoKey would be, but the real API
	// has neither: cryptoKeys.delete and cryptoKeyVersions.delete DO exist as of
	// recent revisions. Honor them: a cryptoKey delete removes the key and its
	// versions + material; a version delete removes a single version.
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsCryptoKeyName(r)
		if _, ok := kmsCryptoKeys.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", name)
			return
		}
		if reason := kmsCryptoKeyUndeletable(name); reason != "" {
			GCPError(w, http.StatusBadRequest, reason, "FAILED_PRECONDITION")
			return
		}
		kmsCryptoKeys.Delete(name)
		kmsIamPolicies.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsCryptoKeyName(r) + "/cryptoKeyVersions/" + sim.PathParam(r, "cryptoKeyVersion")
		version, ok := kmsGetCryptoKeyVersion(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKeyVersion %s not found", name)
			return
		}
		if reason := kmsVersionUndeletable(version); reason != "" {
			GCPError(w, http.StatusBadRequest, reason, "FAILED_PRECONDITION")
			return
		}
		kmsCryptoKeyVersions.Delete(name)
		kmsKeyMaterial.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// GetPublicKey: GET .../cryptoKeyVersions/{cryptoKeyVersion}/publicKey
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}/publicKey", func(w http.ResponseWriter, r *http.Request) {
		kmsHandleGetPublicKey(w, r, kmsCryptoKeyName(r)+"/cryptoKeyVersions/"+sim.PathParam(r, "cryptoKeyVersion"))
	})

	// ImportCryptoKeyVersion / CreateCryptoKeyVersion-via-collection-verb:
	//   POST .../cryptoKeys/{cryptoKey}/{cryptoKeyVersionsCollectionAction}
	// captures "cryptoKeyVersions:import". The literal
	// POST .../cryptoKeys/{cryptoKey}/cryptoKeyVersions create route wins for
	// the bare collection; this wildcard catches the colon-verb spelling.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/{cryptoKeyVersionsCollectionAction}", func(w http.ResponseWriter, r *http.Request) {
		seg := sim.PathParam(r, "cryptoKeyVersionsCollectionAction")
		coll, action, found := strings.Cut(seg, ":")
		if !found || coll != "cryptoKeyVersions" || action != "import" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown cryptoKey collection action %q", seg)
			return
		}
		kmsHandleImportCryptoKeyVersion(w, r, kmsCryptoKeyName(r))
	})

	kmsRegisterImportJobs(srv)
	kmsRegisterEkm(srv)
	kmsRegisterKeyHandles(srv)
	kmsRegisterConfigSingletons(srv)
	kmsRegisterSingleTenantHsm(srv)

	// GenerateRandomBytes: POST .../locations/{locationAction} where
	// locationAction is "{location}:generateRandomBytes" — the verb hangs off
	// the location resource itself, so it is a single segment after /locations/.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{locationAction}", func(w http.ResponseWriter, r *http.Request) {
		_, action, found := strings.Cut(sim.PathParam(r, "locationAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown location action %q", sim.PathParam(r, "locationAction"))
			return
		}
		switch action {
		case "generateRandomBytes":
			kmsHandleGenerateRandomBytes(w, r)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown location action %q", action)
		}
	})

	// GET location-level colon-verb: ekmConfig:getIamPolicy.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/{locationGetAction}", func(w http.ResponseWriter, r *http.Request) {
		res, action, found := strings.Cut(sim.PathParam(r, "locationGetAction"), ":")
		if !found || res != "ekmConfig" || action != "getIamPolicy" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown location action %q", sim.PathParam(r, "locationGetAction"))
			return
		}
		kmsHandleGetIamPolicy(w, r, kmsLocationName(r)+"/ekmConfig")
	})
}

// kmsRegisterImportJobs mounts the ImportJobs CRUD + IAM surface.
func kmsRegisterImportJobs(srv *sim.Server) {
	// CreateImportJob: POST .../keyRings/{keyRing}/importJobs?importJobId=X
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		if _, ok := kmsKeyRings.Get(ringName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "KeyRing %s not found", ringName)
			return
		}
		id := r.URL.Query().Get("importJobId")
		if id == "" {
			GCPError(w, http.StatusBadRequest, "importJobId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var req kmsImportJob
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := ringName + "/importJobs/" + id
		if _, exists := kmsImportJobs.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "ImportJob %s already exists", name)
			return
		}
		protection := req.ProtectionLevel
		if protection == "" {
			protection = kmsDefaultProtectionLevel
		}
		var bits int
		switch req.ImportMethod {
		case "RSA_OAEP_3072_SHA1_AES_256", "RSA_OAEP_3072_SHA256_AES_256", "RSA_OAEP_3072_SHA256":
			bits = 3072
		case "RSA_OAEP_4096_SHA1_AES_256", "RSA_OAEP_4096_SHA256_AES_256", "RSA_OAEP_4096_SHA256":
			bits = 4096
		default:
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported importMethod %q", req.ImportMethod)
			return
		}
		// Generate a real wrapping key of the size selected by importMethod.
		// The public half is returned to the client and the private half stays
		// inside the Cloud KMS service.
		priv, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not generate wrapping key: %v", err)
			return
		}
		pemStr, err := kmsPublicKeyPEM(&priv.PublicKey)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not encode wrapping public key: %v", err)
			return
		}
		now := kmsNow()
		job := kmsImportJob{
			Name:            name,
			ImportMethod:    req.ImportMethod,
			ProtectionLevel: protection,
			State:           "ACTIVE",
			PublicKey:       &kmsWrappingPubKey{Pem: pemStr},
			CreateTime:      now,
			GenerateTime:    now,
		}
		privatePEM, err := kmsPrivateKeyPEM(priv)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not encode wrapping private key: %v", err)
			return
		}
		kmsImportJobs.Put(name, job)
		kmsImportJobMaterial.Put(name, kmsKeyMaterialRecord{PrivatePEM: privatePEM})
		sim.WriteJSON(w, http.StatusOK, job)
	})

	// ListImportJobs: GET .../keyRings/{keyRing}/importJobs
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		prefix := ringName + "/importJobs/"
		var all []kmsImportJob
		for _, j := range kmsImportJobs.List() {
			if strings.HasPrefix(j.Name, prefix) {
				all = append(all, j)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"importJobs": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetImportJob / GetIamPolicy: GET .../importJobs/{importJobAction}
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs/{importJobAction}", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		id, action, isAction := strings.Cut(sim.PathParam(r, "importJobAction"), ":")
		name := ringName + "/importJobs/" + id
		if isAction {
			if action != "getIamPolicy" {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown importJob action %q", action)
				return
			}
			kmsHandleGetIamPolicy(w, r, name)
			return
		}
		job, ok := kmsImportJobs.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "ImportJob %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, job)
	})

	// SetIamPolicy / TestIamPermissions: POST .../importJobs/{importJobAction}
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs/{importJobAction}", func(w http.ResponseWriter, r *http.Request) {
		ringName := kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing"))
		id, action, found := strings.Cut(sim.PathParam(r, "importJobAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown importJob action %q", sim.PathParam(r, "importJobAction"))
			return
		}
		name := ringName + "/importJobs/" + id
		switch action {
		case "setIamPolicy":
			kmsHandleSetIamPolicy(w, r, name)
		case "testIamPermissions":
			kmsHandleTestIamPermissions(w, r, name)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown importJob action %q", action)
		}
	})
}

// kmsRegisterEkm mounts the EkmConnection CRUD + IAM + verifyConnectivity
// surface and the per-location EkmConfig singleton.
func kmsRegisterEkm(srv *sim.Server) {
	// CreateEkmConnection: POST .../ekmConnections?ekmConnectionId=X
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/ekmConnections", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("ekmConnectionId")
		if id == "" {
			GCPError(w, http.StatusBadRequest, "ekmConnectionId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var req kmsEkmConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := kmsLocationName(r) + "/ekmConnections/" + id
		if _, exists := kmsEkmConnections.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "EkmConnection %s already exists", name)
			return
		}
		conn := kmsEkmConnection{
			Name:              name,
			CreateTime:        kmsNow(),
			ServiceResolvers:  req.ServiceResolvers,
			KeyManagementMode: req.KeyManagementMode,
			CryptoSpacePath:   req.CryptoSpacePath,
			Etag:              gcpPolicyETag(),
		}
		kmsEkmConnections.Put(name, conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// ListEkmConnections: GET .../ekmConnections
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/ekmConnections", func(w http.ResponseWriter, r *http.Request) {
		prefix := kmsLocationName(r) + "/ekmConnections/"
		var all []kmsEkmConnection
		for _, c := range kmsEkmConnections.List() {
			if strings.HasPrefix(c.Name, prefix) {
				all = append(all, c)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"ekmConnections": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetEkmConnection / GetIamPolicy / VerifyConnectivity:
	//   GET .../ekmConnections/{ekmConnectionAction}
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnectionAction}", func(w http.ResponseWriter, r *http.Request) {
		id, action, isAction := strings.Cut(sim.PathParam(r, "ekmConnectionAction"), ":")
		name := kmsLocationName(r) + "/ekmConnections/" + id
		if isAction {
			switch action {
			case "getIamPolicy":
				kmsHandleGetIamPolicy(w, r, name)
			case "verifyConnectivity":
				if _, ok := kmsEkmConnections.Get(name); !ok {
					GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "EkmConnection %s not found", name)
					return
				}
				// Real verifyConnectivity probes the external key manager; the
				// sim has no external EKM, so it reports the connection
				// metadata as reachable (empty success body, like the real API).
				sim.WriteJSON(w, http.StatusOK, map[string]any{})
			default:
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown ekmConnection action %q", action)
			}
			return
		}
		conn, ok := kmsEkmConnections.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "EkmConnection %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// SetIamPolicy / TestIamPermissions: POST .../ekmConnections/{ekmConnectionAction}
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnectionAction}", func(w http.ResponseWriter, r *http.Request) {
		id, action, found := strings.Cut(sim.PathParam(r, "ekmConnectionAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown ekmConnection action %q", sim.PathParam(r, "ekmConnectionAction"))
			return
		}
		name := kmsLocationName(r) + "/ekmConnections/" + id
		switch action {
		case "setIamPolicy":
			kmsHandleSetIamPolicy(w, r, name)
		case "testIamPermissions":
			kmsHandleTestIamPermissions(w, r, name)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown ekmConnection action %q", action)
		}
	})

	// UpdateEkmConnection: PATCH .../ekmConnections/{ekmConnection}?updateMask=...
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnection}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/ekmConnections/" + sim.PathParam(r, "ekmConnection")
		conn, ok := kmsEkmConnections.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "EkmConnection %s not found", name)
			return
		}
		var req kmsEkmConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			switch strings.TrimSpace(field) {
			case "serviceResolvers":
				conn.ServiceResolvers = req.ServiceResolvers
			case "keyManagementMode":
				conn.KeyManagementMode = req.KeyManagementMode
			case "cryptoSpacePath":
				conn.CryptoSpacePath = req.CryptoSpacePath
			}
		}
		conn.Etag = gcpPolicyETag()
		kmsEkmConnections.Put(name, conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// GetEkmConfig: GET .../ekmConfig
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/ekmConfig", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/ekmConfig"
		cfg, ok := kmsEkmConfigs.Get(name)
		if !ok {
			cfg = kmsEkmConfig{Name: name}
		}
		sim.WriteJSON(w, http.StatusOK, cfg)
	})

	// UpdateEkmConfig: PATCH .../ekmConfig?updateMask=defaultEkmConnection
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/ekmConfig", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/ekmConfig"
		cfg, ok := kmsEkmConfigs.Get(name)
		if !ok {
			cfg = kmsEkmConfig{Name: name}
		}
		var req kmsEkmConfig
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			if strings.TrimSpace(field) == "defaultEkmConnection" {
				cfg.DefaultEkmConnection = req.DefaultEkmConnection
			}
		}
		kmsEkmConfigs.Put(name, cfg)
		sim.WriteJSON(w, http.StatusOK, cfg)
	})

	// SetIamPolicy / TestIamPermissions on ekmConfig:
	//   POST .../ekmConfig/{ekmConfigAction} where ekmConfigAction is ":verb".
	// The leading segment is the literal "ekmConfig"; Go's mux delivers the
	// ":verb"-suffixed final segment here.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/ekmConfig/{ekmConfigAction}", func(w http.ResponseWriter, r *http.Request) {
		seg := sim.PathParam(r, "ekmConfigAction")
		_, action, found := strings.Cut(seg, ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown ekmConfig action %q", seg)
			return
		}
		name := kmsLocationName(r) + "/ekmConfig"
		switch action {
		case "setIamPolicy":
			kmsHandleSetIamPolicy(w, r, name)
		case "testIamPermissions":
			kmsHandleTestIamPermissions(w, r, name)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown ekmConfig action %q", action)
		}
	})
}

// kmsRegisterKeyHandles mounts the Autokey KeyHandles surface. Create returns a
// completed LRO whose response is the KeyHandle (real Autokey semantics).
func kmsRegisterKeyHandles(srv *sim.Server) {
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/keyHandles", func(w http.ResponseWriter, r *http.Request) {
		project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
		id := r.URL.Query().Get("keyHandleId")
		if id == "" {
			id = generateUUID()
		}
		var req kmsKeyHandle
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := kmsLocationName(r) + "/keyHandles/" + id
		if _, exists := kmsKeyHandles.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "KeyHandle %s already exists", name)
			return
		}
		kmsKey := req.KmsKey
		if kmsKey == "" {
			// Autokey provisions a CMEK for the handle; the sim records a
			// deterministic key path under an autokey ring.
			kmsKey = fmt.Sprintf("projects/%s/locations/%s/keyRings/autokey/cryptoKeys/%s", project, location, id)
		}
		kh := kmsKeyHandle{Name: name, KmsKey: kmsKey, ResourceTypeSelector: req.ResourceTypeSelector}
		kmsKeyHandles.Put(name, kh)
		op := newLRO(project, location, kh, "type.googleapis.com/google.cloud.kms.v1.KeyHandle")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyHandles", func(w http.ResponseWriter, r *http.Request) {
		prefix := kmsLocationName(r) + "/keyHandles/"
		var all []kmsKeyHandle
		for _, kh := range kmsKeyHandles.List() {
			if strings.HasPrefix(kh.Name, prefix) {
				all = append(all, kh)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"keyHandles": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/keyHandles/{keyHandle}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/keyHandles/" + sim.PathParam(r, "keyHandle")
		kh, ok := kmsKeyHandles.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "KeyHandle %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, kh)
	})
}

// kmsRegisterConfigSingletons mounts the AutokeyConfig (folder/project) and
// KajPolicyConfig (folder/project/organization) singletons.
func kmsRegisterConfigSingletons(srv *sim.Server) {
	getAutokey := func(name string) kmsAutokeyConfig {
		if c, ok := kmsAutokeyConfigs.Get(name); ok {
			return c
		}
		return kmsAutokeyConfig{Name: name}
	}
	patchAutokey := func(w http.ResponseWriter, r *http.Request, name string) {
		cfg := getAutokey(name)
		var req kmsAutokeyConfig
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			switch strings.TrimSpace(field) {
			case "keyProject":
				cfg.KeyProject = req.KeyProject
			case "keyProjectResolutionMode":
				cfg.KeyProjectResolutionMode = req.KeyProjectResolutionMode
			}
		}
		cfg.Etag = gcpPolicyETag()
		kmsAutokeyConfigs.Put(name, cfg)
		sim.WriteJSON(w, http.StatusOK, cfg)
	}
	getKaj := func(name string) kmsKajPolicyConfig {
		if c, ok := kmsKajPolicyConfigs.Get(name); ok {
			return c
		}
		return kmsKajPolicyConfig{Name: name}
	}
	patchKaj := func(w http.ResponseWriter, r *http.Request, name string) {
		cfg := getKaj(name)
		var req kmsKajPolicyConfig
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if len(req.DefaultKeyAccessJustifications) > 0 {
			cfg.DefaultKeyAccessJustifications = req.DefaultKeyAccessJustifications
		}
		kmsKajPolicyConfigs.Put(name, cfg)
		sim.WriteJSON(w, http.StatusOK, cfg)
	}

	// AutokeyConfig: projects + folders.
	srv.HandleFunc("GET /v1/projects/{project}/autokeyConfig", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, getAutokey("projects/"+sim.PathParam(r, "project")+"/autokeyConfig"))
	})
	srv.HandleFunc("PATCH /v1/projects/{project}/autokeyConfig", func(w http.ResponseWriter, r *http.Request) {
		patchAutokey(w, r, "projects/"+sim.PathParam(r, "project")+"/autokeyConfig")
	})
	srv.HandleFunc("GET /v1/folders/{folder}/autokeyConfig", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, getAutokey("folders/"+sim.PathParam(r, "folder")+"/autokeyConfig"))
	})
	srv.HandleFunc("PATCH /v1/folders/{folder}/autokeyConfig", func(w http.ResponseWriter, r *http.Request) {
		patchAutokey(w, r, "folders/"+sim.PathParam(r, "folder")+"/autokeyConfig")
	})
	srv.HandleFunc("GET /v1/folders/{folderAction}", func(w http.ResponseWriter, r *http.Request) {
		folder, action, found := gcpCustomMethod(sim.PathParam(r, "folderAction"))
		if !found || action != "showEffectiveAutokeyConfig" {
			gcpMethodNotFound(w)
			return
		}
		kmsHandleShowEffectiveAutokeyConfig(w, "folders/"+folder)
	})

	// KajPolicyConfig: projects + folders + organizations.
	srv.HandleFunc("GET /v1/projects/{project}/kajPolicyConfig", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, getKaj("projects/"+sim.PathParam(r, "project")+"/kajPolicyConfig"))
	})
	srv.HandleFunc("PATCH /v1/projects/{project}/kajPolicyConfig", func(w http.ResponseWriter, r *http.Request) {
		patchKaj(w, r, "projects/"+sim.PathParam(r, "project")+"/kajPolicyConfig")
	})
	srv.HandleFunc("GET /v1/folders/{folder}/kajPolicyConfig", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, getKaj("folders/"+sim.PathParam(r, "folder")+"/kajPolicyConfig"))
	})
	srv.HandleFunc("PATCH /v1/folders/{folder}/kajPolicyConfig", func(w http.ResponseWriter, r *http.Request) {
		patchKaj(w, r, "folders/"+sim.PathParam(r, "folder")+"/kajPolicyConfig")
	})
	srv.HandleFunc("GET /v1/organizations/{organization}/kajPolicyConfig", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, getKaj("organizations/"+sim.PathParam(r, "organization")+"/kajPolicyConfig"))
	})
	srv.HandleFunc("PATCH /v1/organizations/{organization}/kajPolicyConfig", func(w http.ResponseWriter, r *http.Request) {
		patchKaj(w, r, "organizations/"+sim.PathParam(r, "organization")+"/kajPolicyConfig")
	})
}

func kmsHandleShowEffectiveAutokeyConfig(w http.ResponseWriter, parent string) {
	current := parent
	switch {
	case strings.HasPrefix(parent, "projects/"):
		projectID := strings.TrimPrefix(parent, "projects/")
		project, ok := crmResolveProject(projectID)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "project %s not found", parent)
			return
		}
		current = "projects/" + project.ProjectId
	case strings.HasPrefix(parent, "folders/"):
		if _, ok := crmFolders.Get(parent); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "folder %s not found", parent)
			return
		}
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported parent %s", parent)
		return
	}

	for current != "" {
		if config, ok := kmsAutokeyConfigs.Get(current + "/autokeyConfig"); ok &&
			(config.KeyProject != "" || config.KeyProjectResolutionMode != "") {
			source := &struct {
				Name string `json:"name"`
			}{Name: current}
			sim.WriteJSON(w, http.StatusOK, kmsEffectiveAutokeyConfig{
				KeyProject:               config.KeyProject,
				KeyProjectResolutionMode: config.KeyProjectResolutionMode,
				Source:                   source,
			})
			return
		}
		switch {
		case strings.HasPrefix(current, "projects/"):
			project, _ := crmResolveProject(strings.TrimPrefix(current, "projects/"))
			current = project.Parent
		case strings.HasPrefix(current, "folders/"):
			folder, ok := crmFolders.Get(current)
			if !ok {
				current = ""
			} else {
				current = folder.Parent
			}
		default:
			current = ""
		}
	}
	sim.WriteJSON(w, http.StatusOK, kmsEffectiveAutokeyConfig{})
}

// kmsRegisterSingleTenantHsm mounts the SingleTenantHsmInstances + proposals +
// retiredResources surface. These are infrastructure resources; the sim
// persists their metadata and lifecycle states faithfully (no HSM hardware is
// emulated, so they are metadata-only by design).
func kmsRegisterSingleTenantHsm(srv *sim.Server) {
	instancePrefix := "/v1/projects/{project}/locations/{location}/singleTenantHsmInstances"

	srv.HandleFunc("POST "+instancePrefix, func(w http.ResponseWriter, r *http.Request) {
		project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
		id := r.URL.Query().Get("singleTenantHsmInstanceId")
		if id == "" {
			id = generateUUID()
		}
		name := kmsLocationName(r) + "/singleTenantHsmInstances/" + id
		if _, exists := kmsHsmInstances.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "SingleTenantHsmInstance %s already exists", name)
			return
		}
		inst := kmsSingleTenantHsmInstance{Name: name, State: "ACTIVE", CreateTime: kmsNow()}
		kmsHsmInstances.Put(name, inst)
		sim.WriteJSON(w, http.StatusOK, newLRO(project, location, inst, "type.googleapis.com/google.cloud.kms.v1.SingleTenantHsmInstance"))
	})

	srv.HandleFunc("GET "+instancePrefix, func(w http.ResponseWriter, r *http.Request) {
		prefix := kmsLocationName(r) + "/singleTenantHsmInstances/"
		var all []kmsSingleTenantHsmInstance
		for _, inst := range kmsHsmInstances.List() {
			if strings.HasPrefix(inst.Name, prefix) {
				all = append(all, inst)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"singleTenantHsmInstances": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET "+instancePrefix+"/{instance}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/singleTenantHsmInstances/" + sim.PathParam(r, "instance")
		inst, ok := kmsHsmInstances.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "SingleTenantHsmInstance %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, inst)
	})

	// Proposals.
	srv.HandleFunc("POST "+instancePrefix+"/{instance}/proposals", func(w http.ResponseWriter, r *http.Request) {
		project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
		instName := kmsLocationName(r) + "/singleTenantHsmInstances/" + sim.PathParam(r, "instance")
		if _, ok := kmsHsmInstances.Get(instName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "SingleTenantHsmInstance %s not found", instName)
			return
		}
		id := r.URL.Query().Get("proposalId")
		if id == "" {
			id = generateUUID()
		}
		name := instName + "/proposals/" + id
		prop := kmsHsmProposal{Name: name, State: "PENDING"}
		kmsHsmProposals.Put(name, prop)
		sim.WriteJSON(w, http.StatusOK, newLRO(project, location, prop, "type.googleapis.com/google.cloud.kms.v1.SingleTenantHsmInstanceProposal"))
	})

	srv.HandleFunc("GET "+instancePrefix+"/{instance}/proposals", func(w http.ResponseWriter, r *http.Request) {
		prefix := kmsLocationName(r) + "/singleTenantHsmInstances/" + sim.PathParam(r, "instance") + "/proposals/"
		var all []kmsHsmProposal
		for _, p := range kmsHsmProposals.List() {
			if strings.HasPrefix(p.Name, prefix) {
				all = append(all, p)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		resp := map[string]any{"proposals": page, "totalSize": len(all)}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GetProposal / DeleteProposal: GET|DELETE .../proposals/{proposal}
	srv.HandleFunc("GET "+instancePrefix+"/{instance}/proposals/{proposal}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/singleTenantHsmInstances/" + sim.PathParam(r, "instance") + "/proposals/" + sim.PathParam(r, "proposal")
		prop, ok := kmsHsmProposals.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "proposal %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, prop)
	})

	srv.HandleFunc("DELETE "+instancePrefix+"/{instance}/proposals/{proposal}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/singleTenantHsmInstances/" + sim.PathParam(r, "instance") + "/proposals/" + sim.PathParam(r, "proposal")
		if _, ok := kmsHsmProposals.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "proposal %s not found", name)
			return
		}
		kmsHsmProposals.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// ApproveProposal / ExecuteProposal: POST .../proposals/{proposalAction}
	srv.HandleFunc("POST "+instancePrefix+"/{instance}/proposals/{proposalAction}", func(w http.ResponseWriter, r *http.Request) {
		project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
		instName := kmsLocationName(r) + "/singleTenantHsmInstances/" + sim.PathParam(r, "instance")
		id, action, found := strings.Cut(sim.PathParam(r, "proposalAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown proposal action %q", sim.PathParam(r, "proposalAction"))
			return
		}
		name := instName + "/proposals/" + id
		prop, ok := kmsHsmProposals.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "proposal %s not found", name)
			return
		}
		switch action {
		case "approve":
			prop.State = "APPROVED"
			kmsHsmProposals.Put(name, prop)
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
		case "execute":
			prop.State = "EXECUTED"
			kmsHsmProposals.Put(name, prop)
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, prop, "type.googleapis.com/google.cloud.kms.v1.SingleTenantHsmInstanceProposal"))
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown proposal action %q", action)
		}
	})

	// RetiredResources.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/retiredResources", func(w http.ResponseWriter, r *http.Request) {
		prefix := kmsLocationName(r) + "/retiredResources/"
		var all []kmsRetiredResource
		for _, rr := range kmsRetiredResources.List() {
			if strings.HasPrefix(rr.Name, prefix) {
				all = append(all, rr)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next, ok := paginateList(w, r, all)
		if !ok {
			return
		}
		// ListRetiredResourcesResponse declares totalSize as an int64, which
		// proto3 JSON carries as a string — unlike the int32 totalSize every
		// other Cloud KMS list response declares.
		resp := map[string]any{
			"retiredResources": page,
			"totalSize":        strconv.Itoa(len(all)),
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/retiredResources/{retiredResource}", func(w http.ResponseWriter, r *http.Request) {
		name := kmsLocationName(r) + "/retiredResources/" + sim.PathParam(r, "retiredResource")
		rr, ok := kmsRetiredResources.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "RetiredResource %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rr)
	})
}

// kmsHighestVersionID returns the highest existing numeric version ID for a
// key, or 0 when none exist. Used to seed the per-key version counter for keys
// created before VersionSeq tracking existed.
func kmsHighestVersionID(keyName string) int {
	prefix := keyName + "/cryptoKeyVersions/"
	highest := 0
	for _, v := range kmsCryptoKeyVersions.List() {
		if !strings.HasPrefix(v.Name, prefix) {
			continue
		}
		if n, ok := kmsVersionNumber(v.Name); ok && n > highest {
			highest = n
		}
	}
	return highest
}

// kmsDestroyScheduledDuration normalizes a CryptoKey's destroy schedule,
// falling back to the documented default when the create left it unset.
func kmsDestroyScheduledDuration(requested string) string {
	if requested != "" {
		return requested
	}
	return strconv.Itoa(int(kmsDefaultDestroyScheduledDuration.Seconds())) + "s"
}

// kmsDestroyDelayFor returns how long a version of this key spends in
// DESTROY_SCHEDULED. A key carrying no duration, or one this simulator cannot
// parse, uses the default rather than destroying immediately.
func kmsDestroyDelayFor(keyName string) time.Duration {
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok || key.DestroyScheduledDuration == "" {
		return kmsDefaultDestroyScheduledDuration
	}
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(key.DestroyScheduledDuration, "s"), 64)
	if err != nil || seconds < 0 {
		return kmsDefaultDestroyScheduledDuration
	}
	return time.Duration(seconds * float64(time.Second))
}

// kmsVersionSettled applies the one time-driven transition a CryptoKeyVersion
// has: a destroy is scheduled for a point in the future, and when that point
// passes the version is DESTROYED and its material is gone.
//
// The transition is derived from DestroyTime rather than driven by a timer, so
// it holds for a version written before the process started and needs no clock
// running to be correct. Without it a scheduled destroy never completes and
// the version sits in DESTROY_SCHEDULED forever — a state machine with a dead
// end, and one that would make DeleteCryptoKeyVersion unreachable.
func kmsVersionSettled(v kmsCryptoKeyVersion) kmsCryptoKeyVersion {
	if v.State != "DESTROY_SCHEDULED" || v.DestroyTime == "" {
		return v
	}
	due, err := time.Parse(time.RFC3339, v.DestroyTime)
	if err != nil || time.Now().UTC().Before(due) {
		return v
	}
	v.State = "DESTROYED"
	v.DestroyEventTime = v.DestroyTime
	return v
}

// kmsGetCryptoKeyVersion reads a version with its scheduled destroy settled.
// Every read goes through here so no caller sees a stale DESTROY_SCHEDULED.
func kmsGetCryptoKeyVersion(name string) (kmsCryptoKeyVersion, bool) {
	v, ok := kmsCryptoKeyVersions.Get(name)
	if !ok {
		return v, false
	}
	return kmsVersionSettled(v), true
}

// kmsCryptoKeyVersionsOf returns a CryptoKey's versions.
func kmsCryptoKeyVersionsOf(keyName string) []kmsCryptoKeyVersion {
	prefix := keyName + "/cryptoKeyVersions/"
	var out []kmsCryptoKeyVersion
	for _, v := range kmsCryptoKeyVersions.List() {
		if strings.HasPrefix(v.Name, prefix) {
			out = append(out, kmsVersionSettled(v))
		}
	}
	return out
}

// kmsVersionUndeletable reports why a CryptoKeyVersion cannot be permanently
// deleted, or "" when the delete is allowed.
//
// Cloud KMS accepts the delete only for a version that was never imported and
// has reached DESTROYED, IMPORT_FAILED or GENERATION_FAILED. Deleting outside
// those states would drop material a client can still name and decrypt with,
// leaving the caller to discover the loss at the next Decrypt.
func kmsVersionUndeletable(v kmsCryptoKeyVersion) string {
	if v.ImportTime != "" || v.ImportJob != "" {
		return fmt.Sprintf("CryptoKeyVersion %s was imported and cannot be deleted", v.Name)
	}
	switch v.State {
	case "DESTROYED", "IMPORT_FAILED", "GENERATION_FAILED":
		return ""
	}
	return fmt.Sprintf("CryptoKeyVersion %s is %s; only a DESTROYED, IMPORT_FAILED or GENERATION_FAILED version can be deleted", v.Name, v.State)
}

// kmsCryptoKeyUndeletable reports why a CryptoKey cannot be deleted, or "" when
// the delete is allowed. Every child version must already have been deleted;
// the key delete does not cascade.
func kmsCryptoKeyUndeletable(keyName string) string {
	if remaining := kmsCryptoKeyVersionsOf(keyName); len(remaining) > 0 {
		return fmt.Sprintf("CryptoKey %s still has %d CryptoKeyVersion(s); delete them first", keyName, len(remaining))
	}
	return ""
}

func kmsHandleEncrypt(w http.ResponseWriter, r *http.Request, keyName string) {
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
		return
	}
	if key.Purpose != kmsPurposeEncryptDecrypt {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKey %s is not for ENCRYPT_DECRYPT", keyName)
		return
	}
	var req struct {
		Plaintext                         string `json:"plaintext"`
		PlaintextCrc32c                   *int64 `json:"plaintextCrc32c,string,omitempty"`
		AdditionalAuthenticatedData       string `json:"additionalAuthenticatedData"`
		AdditionalAuthenticatedDataCrc32c *int64 `json:"additionalAuthenticatedDataCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	plaintext, err := kmsDecodeBytes(req.Plaintext)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "plaintext must be base64: %v", err)
		return
	}
	aad, err := kmsDecodeBytes(req.AdditionalAuthenticatedData)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "additionalAuthenticatedData must be base64: %v", err)
		return
	}
	verifiedPlaintext, ok := kmsVerifyCRC(w, plaintext, req.PlaintextCrc32c, "plaintext")
	if !ok {
		return
	}
	verifiedAAD, ok := kmsVerifyCRC(w, aad, req.AdditionalAuthenticatedDataCrc32c, "additionalAuthenticatedData")
	if !ok {
		return
	}

	versionID := key.PrimaryVersionID
	if versionID == "" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKey %s has no primary version", keyName)
		return
	}
	versionName := keyName + "/cryptoKeyVersions/" + versionID
	version, ok := kmsGetCryptoKeyVersion(versionName)
	if !ok || version.State != "ENABLED" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "primary version of %s is not enabled", keyName)
		return
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "missing key material for %s", versionName)
		return
	}
	versionNum, _ := kmsVersionNumber(versionName)
	ciphertext, err := kmsEncryptBytes(material.Key, versionNum, plaintext, aad)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "encryption failed: %v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":                    versionName,
		"ciphertext":              base64.StdEncoding.EncodeToString(ciphertext),
		"ciphertextCrc32c":        fmt.Sprintf("%d", kmsCRC(ciphertext)),
		"verifiedPlaintextCrc32c": verifiedPlaintext,
		"verifiedAdditionalAuthenticatedDataCrc32c": verifiedAAD,
		"protectionLevel": key.ProtectionLevel,
	})
}

func kmsHandleDecrypt(w http.ResponseWriter, r *http.Request, keyName string) {
	if _, ok := kmsCryptoKeys.Get(keyName); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
		return
	}
	var req struct {
		Ciphertext                        string `json:"ciphertext"`
		CiphertextCrc32c                  *int64 `json:"ciphertextCrc32c,string,omitempty"`
		AdditionalAuthenticatedData       string `json:"additionalAuthenticatedData"`
		AdditionalAuthenticatedDataCrc32c *int64 `json:"additionalAuthenticatedDataCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	ciphertext, err := kmsDecodeBytes(req.Ciphertext)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "ciphertext must be base64: %v", err)
		return
	}
	aad, err := kmsDecodeBytes(req.AdditionalAuthenticatedData)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "additionalAuthenticatedData must be base64: %v", err)
		return
	}
	if _, ok := kmsVerifyCRC(w, ciphertext, req.CiphertextCrc32c, "ciphertext"); !ok {
		return
	}
	versionNum, blob, err := kmsParseCiphertext(ciphertext)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Decryption failed: the ciphertext is malformed")
		return
	}
	versionName := fmt.Sprintf("%s/cryptoKeyVersions/%d", keyName, versionNum)
	version, ok := kmsGetCryptoKeyVersion(versionName)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Decryption failed: the version used to encrypt does not exist")
		return
	}
	if version.State != "ENABLED" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not enabled (state %s)", versionName, version.State)
		return
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "missing key material for %s", versionName)
		return
	}
	plaintext, err := kmsDecryptBytes(material.Key, blob, aad)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Decryption failed: verify the ciphertext and AAD match what was used to encrypt")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"plaintext":       base64.StdEncoding.EncodeToString(plaintext),
		"plaintextCrc32c": fmt.Sprintf("%d", kmsCRC(plaintext)),
		"protectionLevel": kmsDefaultProtectionLevel,
	})
}

// kmsHandleUpdatePrimaryVersion sets the cryptoKey's primary version.
func kmsHandleUpdatePrimaryVersion(w http.ResponseWriter, r *http.Request, keyName string) {
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
		return
	}
	var req struct {
		CryptoKeyVersionId string `json:"cryptoKeyVersionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.CryptoKeyVersionId == "" {
		GCPError(w, http.StatusBadRequest, "cryptoKeyVersionId is required", "INVALID_ARGUMENT")
		return
	}
	if _, ok := kmsGetCryptoKeyVersion(keyName + "/cryptoKeyVersions/" + req.CryptoKeyVersionId); !ok {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "CryptoKeyVersion %s not found", req.CryptoKeyVersionId)
		return
	}
	key.PrimaryVersionID = req.CryptoKeyVersionId
	kmsCryptoKeys.Put(keyName, key)
	sim.WriteJSON(w, http.StatusOK, kmsAssembleCryptoKey(key))
}

func kmsHandleGetIamPolicy(w http.ResponseWriter, r *http.Request, resource string) {
	pol, ok := kmsIamPolicies.Get(resource)
	if !ok {
		// Real Cloud KMS returns an empty (etag-bearing) policy for a resource
		// that has none set yet, not a 404.
		pol = kmsIamPolicy{Etag: gcpPolicyETag()}
	}
	sim.WriteJSON(w, http.StatusOK, pol)
}

func kmsHandleSetIamPolicy(w http.ResponseWriter, r *http.Request, resource string) {
	var req struct {
		Policy kmsIamPolicy `json:"policy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	pol := req.Policy
	pol.Etag = gcpPolicyETag()
	kmsIamPolicies.Put(resource, pol)
	sim.WriteJSON(w, http.StatusOK, pol)
}

func kmsHandleTestIamPermissions(w http.ResponseWriter, r *http.Request, resource string) {
	var req struct {
		Permissions []string `json:"permissions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	stored, _ := kmsIamPolicies.Get(resource)
	policy := IAMPolicy{Version: stored.Version, Etag: stored.Etag}
	for _, binding := range stored.Bindings {
		policy.Bindings = append(policy.Bindings,
			IAMBinding{Role: binding.Role, Members: binding.Members})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"permissions": gcpAnswerTestIamPermissions(r, policy, req.Permissions)})
}

// kmsHandleGenerateRandomBytes returns cryptographically-random bytes from
// crypto/rand, with a real CRC32C over the result.
func kmsHandleGenerateRandomBytes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LengthBytes     int    `json:"lengthBytes"`
		ProtectionLevel string `json:"protectionLevel"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.LengthBytes < 8 || req.LengthBytes > 1024 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "lengthBytes must be between 8 and 1024, got %d", req.LengthBytes)
		return
	}
	data := make([]byte, req.LengthBytes)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not read random bytes: %v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"data":       base64.StdEncoding.EncodeToString(data),
		"dataCrc32c": fmt.Sprintf("%d", kmsCRC(data)),
	})
}

func kmsHandleMacSign(w http.ResponseWriter, r *http.Request, versionName string) {
	version, material, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	if !strings.HasPrefix(version.Algorithm, "HMAC_") {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not a MAC key", versionName)
		return
	}
	var req struct {
		Data       string `json:"data"`
		DataCrc32c *int64 `json:"dataCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	data, err := kmsDecodeBytes(req.Data)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "data must be base64: %v", err)
		return
	}
	verifiedData, ok := kmsVerifyCRC(w, data, req.DataCrc32c, "data")
	if !ok {
		return
	}
	h := hmac.New(kmsHashForAlg(version.Algorithm), material.Key)
	h.Write(data)
	mac := h.Sum(nil)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":               versionName,
		"mac":                base64.StdEncoding.EncodeToString(mac),
		"macCrc32c":          fmt.Sprintf("%d", kmsCRC(mac)),
		"verifiedDataCrc32c": verifiedData,
		"protectionLevel":    version.ProtectionLevel,
	})
}

func kmsHandleMacVerify(w http.ResponseWriter, r *http.Request, versionName string) {
	version, material, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	if !strings.HasPrefix(version.Algorithm, "HMAC_") {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not a MAC key", versionName)
		return
	}
	var req struct {
		Data       string `json:"data"`
		DataCrc32c *int64 `json:"dataCrc32c,string,omitempty"`
		Mac        string `json:"mac"`
		MacCrc32c  *int64 `json:"macCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	data, err := kmsDecodeBytes(req.Data)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "data must be base64: %v", err)
		return
	}
	mac, err := kmsDecodeBytes(req.Mac)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "mac must be base64: %v", err)
		return
	}
	verifiedData, ok := kmsVerifyCRC(w, data, req.DataCrc32c, "data")
	if !ok {
		return
	}
	verifiedMac, ok := kmsVerifyCRC(w, mac, req.MacCrc32c, "mac")
	if !ok {
		return
	}
	h := hmac.New(kmsHashForAlg(version.Algorithm), material.Key)
	h.Write(data)
	success := hmac.Equal(mac, h.Sum(nil))
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":                     versionName,
		"success":                  success,
		"verifiedDataCrc32c":       verifiedData,
		"verifiedMacCrc32c":        verifiedMac,
		"verifiedSuccessIntegrity": success,
		"protectionLevel":          version.ProtectionLevel,
	})
}

func kmsHandleRawEncrypt(w http.ResponseWriter, r *http.Request, versionName string) {
	version, material, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	var req struct {
		Plaintext                         string `json:"plaintext"`
		PlaintextCrc32c                   *int64 `json:"plaintextCrc32c,string,omitempty"`
		AdditionalAuthenticatedData       string `json:"additionalAuthenticatedData"`
		AdditionalAuthenticatedDataCrc32c *int64 `json:"additionalAuthenticatedDataCrc32c,string,omitempty"`
		InitializationVector              string `json:"initializationVector"`
		InitializationVectorCrc32c        *int64 `json:"initializationVectorCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	plaintext, err := kmsDecodeBytes(req.Plaintext)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "plaintext must be base64: %v", err)
		return
	}
	aad, err := kmsDecodeBytes(req.AdditionalAuthenticatedData)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "additionalAuthenticatedData must be base64: %v", err)
		return
	}
	verifiedPlaintext, ok := kmsVerifyCRC(w, plaintext, req.PlaintextCrc32c, "plaintext")
	if !ok {
		return
	}
	verifiedAAD, ok := kmsVerifyCRC(w, aad, req.AdditionalAuthenticatedDataCrc32c, "additionalAuthenticatedData")
	if !ok {
		return
	}
	block, err := aes.NewCipher(material.Key)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "cipher init failed: %v", err)
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "GCM init failed: %v", err)
		return
	}
	// The caller may supply an IV; otherwise the server generates one. Real
	// rawEncrypt for AES-GCM uses a 12-byte IV.
	var iv []byte
	verifiedIV := false
	if req.InitializationVector != "" {
		iv, err = kmsDecodeBytes(req.InitializationVector)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "initializationVector must be base64: %v", err)
			return
		}
		if len(iv) != gcm.NonceSize() {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "initializationVector must be %d bytes", gcm.NonceSize())
			return
		}
		verifiedIV, ok = kmsVerifyCRC(w, iv, req.InitializationVectorCrc32c, "initializationVector")
		if !ok {
			return
		}
	} else {
		iv = make([]byte, gcm.NonceSize())
		if _, err := io.ReadFull(rand.Reader, iv); err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not generate IV: %v", err)
			return
		}
	}
	sealed := gcm.Seal(nil, iv, plaintext, aad)
	// gcm.Seal appends a 16-byte tag; rawEncrypt reports it separately.
	tagLen := gcm.Overhead()
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":                       versionName,
		"ciphertext":                 base64.StdEncoding.EncodeToString(sealed),
		"ciphertextCrc32c":           fmt.Sprintf("%d", kmsCRC(sealed)),
		"initializationVector":       base64.StdEncoding.EncodeToString(iv),
		"initializationVectorCrc32c": fmt.Sprintf("%d", kmsCRC(iv)),
		"tagLength":                  tagLen,
		"verifiedPlaintextCrc32c":    verifiedPlaintext,
		"verifiedAdditionalAuthenticatedDataCrc32c": verifiedAAD,
		"verifiedInitializationVectorCrc32c":        verifiedIV,
		"protectionLevel":                           version.ProtectionLevel,
	})
}

func kmsHandleRawDecrypt(w http.ResponseWriter, r *http.Request, versionName string) {
	version, material, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	var req struct {
		Ciphertext                        string `json:"ciphertext"`
		CiphertextCrc32c                  *int64 `json:"ciphertextCrc32c,string,omitempty"`
		AdditionalAuthenticatedData       string `json:"additionalAuthenticatedData"`
		AdditionalAuthenticatedDataCrc32c *int64 `json:"additionalAuthenticatedDataCrc32c,string,omitempty"`
		InitializationVector              string `json:"initializationVector"`
		InitializationVectorCrc32c        *int64 `json:"initializationVectorCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	ciphertext, err := kmsDecodeBytes(req.Ciphertext)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "ciphertext must be base64: %v", err)
		return
	}
	aad, err := kmsDecodeBytes(req.AdditionalAuthenticatedData)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "additionalAuthenticatedData must be base64: %v", err)
		return
	}
	iv, err := kmsDecodeBytes(req.InitializationVector)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "initializationVector must be base64: %v", err)
		return
	}
	verifiedCiphertext, ok := kmsVerifyCRC(w, ciphertext, req.CiphertextCrc32c, "ciphertext")
	if !ok {
		return
	}
	verifiedAAD, ok := kmsVerifyCRC(w, aad, req.AdditionalAuthenticatedDataCrc32c, "additionalAuthenticatedData")
	if !ok {
		return
	}
	verifiedIV, ok := kmsVerifyCRC(w, iv, req.InitializationVectorCrc32c, "initializationVector")
	if !ok {
		return
	}
	block, err := aes.NewCipher(material.Key)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "cipher init failed: %v", err)
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "GCM init failed: %v", err)
		return
	}
	if len(iv) != gcm.NonceSize() {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "initializationVector must be %d bytes", gcm.NonceSize())
		return
	}
	plaintext, err := gcm.Open(nil, iv, ciphertext, aad)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Decryption failed: verify the ciphertext, IV and AAD")
		return
	}
	_ = verifiedCiphertext
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"plaintext":                base64.StdEncoding.EncodeToString(plaintext),
		"plaintextCrc32c":          fmt.Sprintf("%d", kmsCRC(plaintext)),
		"verifiedCiphertextCrc32c": verifiedCiphertext,
		"verifiedAdditionalAuthenticatedDataCrc32c": verifiedAAD,
		"verifiedInitializationVectorCrc32c":        verifiedIV,
		"protectionLevel":                           version.ProtectionLevel,
	})
}

func kmsHandleGetPublicKey(w http.ResponseWriter, r *http.Request, versionName string) {
	version, ok := kmsGetCryptoKeyVersion(versionName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKeyVersion %s not found", versionName)
		return
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok || len(material.PrivatePEM) == 0 {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s has no public key (not an asymmetric key)", versionName)
		return
	}
	pub, err := kmsPublicFromPEM(material.PrivatePEM)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not derive public key: %v", err)
		return
	}
	pemStr, err := kmsPublicKeyPEM(pub)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not encode public key: %v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":            versionName,
		"pem":             pemStr,
		"pemCrc32c":       fmt.Sprintf("%d", kmsCRC([]byte(pemStr))),
		"algorithm":       version.Algorithm,
		"protectionLevel": version.ProtectionLevel,
		"publicKeyFormat": "PEM",
	})
}

func kmsHandleAsymmetricSign(w http.ResponseWriter, r *http.Request, versionName string) {
	version, material, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	if len(material.PrivatePEM) == 0 || !strings.Contains(version.Algorithm, "SIGN") {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not an asymmetric-sign key", versionName)
		return
	}
	var req struct {
		Data         string            `json:"data"`
		DataCrc32c   *int64            `json:"dataCrc32c,string,omitempty"`
		Digest       map[string]string `json:"digest"`
		DigestCrc32c *int64            `json:"digestCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}

	priv, err := kmsPrivateFromPEM(material.PrivatePEM)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not parse private key: %v", err)
		return
	}

	// The client supplies either the message (data) or its precomputed digest.
	// The sim hashes the data with SHA-256 (the algorithm the test keys use)
	// when a digest isn't given.
	var digest []byte
	verifiedDigest := false
	verifiedData := false
	if d, ok := req.Digest["sha256"]; ok && d != "" {
		digest, err = kmsDecodeBytes(d)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "digest.sha256 must be base64: %v", err)
			return
		}
		verifiedDigest, ok = kmsVerifyCRC(w, digest, req.DigestCrc32c, "digest")
		if !ok {
			return
		}
	} else {
		data, err := kmsDecodeBytes(req.Data)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "data must be base64: %v", err)
			return
		}
		verifiedData, ok = kmsVerifyCRC(w, data, req.DataCrc32c, "data")
		if !ok {
			return
		}
		sum := sha256.Sum256(data)
		digest = sum[:]
	}

	var sig []byte
	switch key := priv.(type) {
	case *rsa.PrivateKey:
		if strings.Contains(version.Algorithm, "PSS") {
			sig, err = rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest)
		}
	case *ecdsa.PrivateKey:
		sig, err = ecdsa.SignASN1(rand.Reader, key, digest)
	default:
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "unsupported asymmetric-sign key type")
		return
	}
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "signing failed: %v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name":                 versionName,
		"signature":            base64.StdEncoding.EncodeToString(sig),
		"signatureCrc32c":      fmt.Sprintf("%d", kmsCRC(sig)),
		"verifiedDigestCrc32c": verifiedDigest,
		"verifiedDataCrc32c":   verifiedData,
		"protectionLevel":      version.ProtectionLevel,
	})
}

func kmsHandleAsymmetricDecrypt(w http.ResponseWriter, r *http.Request, versionName string) {
	version, material, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	if len(material.PrivatePEM) == 0 || !strings.Contains(version.Algorithm, "DECRYPT") {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not an asymmetric-decrypt key", versionName)
		return
	}
	var req struct {
		Ciphertext       string `json:"ciphertext"`
		CiphertextCrc32c *int64 `json:"ciphertextCrc32c,string,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	ciphertext, err := kmsDecodeBytes(req.Ciphertext)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "ciphertext must be base64: %v", err)
		return
	}
	verifiedCiphertext, ok := kmsVerifyCRC(w, ciphertext, req.CiphertextCrc32c, "ciphertext")
	if !ok {
		return
	}
	priv, err := kmsPrivateFromPEM(material.PrivatePEM)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not parse private key: %v", err)
		return
	}
	rsaKey, ok := priv.(*rsa.PrivateKey)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "asymmetric-decrypt requires an RSA key")
		return
	}
	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, rsaKey, ciphertext, nil)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Decryption failed: verify the ciphertext")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"plaintext":                base64.StdEncoding.EncodeToString(plaintext),
		"plaintextCrc32c":          fmt.Sprintf("%d", kmsCRC(plaintext)),
		"verifiedCiphertextCrc32c": verifiedCiphertext,
		"protectionLevel":          version.ProtectionLevel,
	})
}

// kmsHandleImportCryptoKeyVersion unwraps the exact caller-provided material
// with the selected ImportJob's private wrapping key and persists that material
// in the imported CryptoKeyVersion.
func kmsHandleImportCryptoKeyVersion(w http.ResponseWriter, r *http.Request, keyName string) {
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
		return
	}
	var req struct {
		ImportJob              string `json:"importJob"`
		Algorithm              string `json:"algorithm"`
		CryptoKeyVersion       string `json:"cryptoKeyVersion"`
		WrappedKey             string `json:"wrappedKey"`
		RsaAesWrappedKey       string `json:"rsaAesWrappedKey"`
		TrustedWrappingEnabled bool   `json:"trustedWrappingEnabled"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.ImportJob == "" {
		GCPError(w, http.StatusBadRequest, "importJob is required", "INVALID_ARGUMENT")
		return
	}
	job, ok := kmsImportJobs.Get(req.ImportJob)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "ImportJob %s not found", req.ImportJob)
		return
	}
	if job.State != "ACTIVE" || !strings.HasPrefix(req.ImportJob, strings.Split(keyName, "/cryptoKeys/")[0]+"/importJobs/") {
		GCPError(w, http.StatusBadRequest, "ImportJob is not active in the target key ring", "FAILED_PRECONDITION")
		return
	}
	jobMaterial, ok := kmsImportJobMaterial.Get(req.ImportJob)
	if !ok || len(jobMaterial.PrivatePEM) == 0 {
		GCPError(w, http.StatusInternalServerError, "ImportJob wrapping key material is unavailable", "INTERNAL")
		return
	}
	privateKey, err := kmsPrivateFromPEM(jobMaterial.PrivatePEM)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "ImportJob private key is invalid: %v", err)
		return
	}
	rsaPrivate, ok := privateKey.(*rsa.PrivateKey)
	if !ok {
		GCPError(w, http.StatusInternalServerError, "ImportJob wrapping key is not RSA", "INTERNAL")
		return
	}
	if (req.WrappedKey == "") == (req.RsaAesWrappedKey == "") {
		GCPError(w, http.StatusBadRequest, "exactly one of wrappedKey or rsaAesWrappedKey is required", "INVALID_ARGUMENT")
		return
	}
	encodedWrapped := req.WrappedKey
	if encodedWrapped == "" {
		encodedWrapped = req.RsaAesWrappedKey
	}
	wrapped, err := kmsDecodeBytes(encodedWrapped)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "wrapped key must be base64: %v", err)
		return
	}
	raw, err := kmsUnwrapImportJobMaterial(rsaPrivate, job.ImportMethod, wrapped)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "could not unwrap key material: %v", err)
		return
	}
	algorithm := req.Algorithm
	if algorithm == "" {
		algorithm = key.Algorithm
	}
	record, err := kmsImportedMaterialRecord(algorithm, raw)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}

	versionName := req.CryptoKeyVersion
	if versionName == "" {
		var assigned int
		kmsCryptoKeys.Update(keyName, func(k *kmsStoredCryptoKey) {
			if k.VersionSeq < kmsHighestVersionID(keyName) {
				k.VersionSeq = kmsHighestVersionID(keyName)
			}
			k.VersionSeq++
			assigned = k.VersionSeq
		})
		versionName = keyName + "/cryptoKeyVersions/" + strconv.Itoa(assigned)
	} else {
		if !strings.HasPrefix(versionName, keyName+"/cryptoKeyVersions/") {
			GCPError(w, http.StatusBadRequest, "cryptoKeyVersion must be a child of parent", "INVALID_ARGUMENT")
			return
		}
		existing, exists := kmsGetCryptoKeyVersion(versionName)
		if !exists || !existing.ReimportEligible ||
			(existing.State != "DESTROYED" && existing.State != "IMPORT_FAILED") {
			GCPError(w, http.StatusBadRequest, "cryptoKeyVersion is not eligible for reimport", "FAILED_PRECONDITION")
			return
		}
		if existing.Algorithm != algorithm {
			GCPError(w, http.StatusBadRequest, "algorithm must match the existing CryptoKeyVersion", "INVALID_ARGUMENT")
			return
		}
	}
	now := kmsNow()
	version := kmsCryptoKeyVersion{
		Name:             versionName,
		State:            "ENABLED",
		ProtectionLevel:  job.ProtectionLevel,
		Algorithm:        algorithm,
		CreateTime:       now,
		GenerateTime:     now,
		ImportJob:        req.ImportJob,
		ImportTime:       now,
		ReimportEligible: true,

		// An imported version is exportable under a trusted wrapping key only
		// when the import asked for it, the same way a generated version
		// carries the flag its create call set.
		TrustedWrappingEnabled: req.TrustedWrappingEnabled,
	}
	kmsCryptoKeyVersions.Put(versionName, version)
	kmsKeyMaterial.Put(versionName, record)
	sim.WriteJSON(w, http.StatusOK, version)
}

func kmsUnwrapImportJobMaterial(privateKey *rsa.PrivateKey, importMethod string, wrapped []byte) ([]byte, error) {
	var digest hash.Hash
	switch {
	case strings.Contains(importMethod, "SHA1"):
		digest = sha1.New()
	case strings.Contains(importMethod, "SHA256"):
		digest = sha256.New()
	default:
		return nil, fmt.Errorf("unsupported import method %s", importMethod)
	}
	rsaSize := privateKey.Size()
	if len(wrapped) < rsaSize {
		return nil, fmt.Errorf("wrapped material is shorter than the RSA ciphertext")
	}
	unwrapped, err := rsa.DecryptOAEP(digest, rand.Reader, privateKey, wrapped[:rsaSize], nil)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(importMethod, "_AES_256") {
		if len(unwrapped) != 32 {
			return nil, fmt.Errorf("hybrid import did not contain a 256-bit AES wrapping key")
		}
		if len(wrapped) == rsaSize {
			return nil, fmt.Errorf("hybrid import is missing AES-wrapped key material")
		}
		return kmsAESKeyUnwrapWithPadding(unwrapped, wrapped[rsaSize:])
	}
	if len(wrapped) != rsaSize {
		return nil, fmt.Errorf("direct RSA import contains trailing bytes")
	}
	return unwrapped, nil
}

func kmsHandleExportTrustedKeyWrappedCryptoKeyVersion(w http.ResponseWriter, r *http.Request, versionName string) {
	version, targetMaterial, ok := kmsLoadEnabledVersion(w, versionName)
	if !ok {
		return
	}
	if !version.TrustedWrappingEnabled {
		GCPError(w, http.StatusBadRequest, "CryptoKeyVersion does not have trusted wrapping enabled", "FAILED_PRECONDITION")
		return
	}
	wrappingName := r.URL.Query().Get("wrappingKey")
	wrappingVersion, wrappingMaterial, ok := kmsLoadTrustedWrappingKey(w, wrappingName)
	if !ok {
		return
	}
	_ = wrappingVersion
	targetBytes := targetMaterial.Key
	if len(targetBytes) == 0 && len(targetMaterial.PrivatePEM) > 0 {
		block, _ := pem.Decode(targetMaterial.PrivatePEM)
		if block == nil {
			GCPError(w, http.StatusInternalServerError, "stored private key is not valid PKCS#8 PEM", "INTERNAL")
			return
		}
		targetBytes = block.Bytes
	}
	wrapped, err := kmsAESKeyWrapWithPadding(wrappingMaterial.Key, targetBytes)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "could not wrap key material: %v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"wrappedKey":       base64.StdEncoding.EncodeToString(wrapped),
		"wrappedKeyCrc32c": fmt.Sprintf("%d", kmsCRC(wrapped)),
	})
}

func kmsHandleImportTrustedKeyWrappedCryptoKeyVersion(w http.ResponseWriter, r *http.Request, keyName string) {
	key, ok := kmsCryptoKeys.Get(keyName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKey %s not found", keyName)
		return
	}
	var req struct {
		WrappedKey       string `json:"wrappedKey"`
		ImportingKey     string `json:"importingKey"`
		CryptoKeyVersion string `json:"cryptoKeyVersion"`
		Algorithm        string `json:"algorithm"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.WrappedKey == "" || req.ImportingKey == "" || req.Algorithm == "" {
		GCPError(w, http.StatusBadRequest, "wrappedKey, importingKey, and algorithm are required", "INVALID_ARGUMENT")
		return
	}
	_, wrappingMaterial, ok := kmsLoadTrustedWrappingKey(w, req.ImportingKey)
	if !ok {
		return
	}
	wrapped, err := kmsDecodeBytes(req.WrappedKey)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "wrappedKey must be base64: %v", err)
		return
	}
	raw, err := kmsAESKeyUnwrapWithPadding(wrappingMaterial.Key, wrapped)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "trusted wrapped key is invalid: %v", err)
		return
	}
	record, err := kmsImportedMaterialRecord(req.Algorithm, raw)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}

	versionName := req.CryptoKeyVersion
	if versionName == "" {
		var assigned int
		kmsCryptoKeys.Update(keyName, func(k *kmsStoredCryptoKey) {
			if k.VersionSeq < kmsHighestVersionID(keyName) {
				k.VersionSeq = kmsHighestVersionID(keyName)
			}
			k.VersionSeq++
			assigned = k.VersionSeq
		})
		versionName = keyName + "/cryptoKeyVersions/" + strconv.Itoa(assigned)
	} else {
		if !strings.HasPrefix(versionName, keyName+"/cryptoKeyVersions/") {
			GCPError(w, http.StatusBadRequest, "cryptoKeyVersion must be a child of parent", "INVALID_ARGUMENT")
			return
		}
		existing, exists := kmsGetCryptoKeyVersion(versionName)
		if !exists || !existing.ReimportEligible ||
			(existing.State != "DESTROYED" && existing.State != "IMPORT_FAILED") {
			GCPError(w, http.StatusBadRequest, "cryptoKeyVersion is not eligible for reimport", "FAILED_PRECONDITION")
			return
		}
		if existing.Algorithm != req.Algorithm {
			GCPError(w, http.StatusBadRequest, "algorithm must match the existing CryptoKeyVersion", "INVALID_ARGUMENT")
			return
		}
	}
	now := kmsNow()
	version := kmsCryptoKeyVersion{
		Name:                   versionName,
		State:                  "ENABLED",
		ProtectionLevel:        key.ProtectionLevel,
		Algorithm:              req.Algorithm,
		CreateTime:             now,
		GenerateTime:           now,
		ImportTime:             now,
		TrustedWrappingEnabled: true,
		ReimportEligible:       true,
	}
	kmsCryptoKeyVersions.Put(versionName, version)
	kmsKeyMaterial.Put(versionName, record)
	sim.WriteJSON(w, http.StatusOK, version)
}

func kmsLoadTrustedWrappingKey(w http.ResponseWriter, name string) (kmsCryptoKeyVersion, kmsKeyMaterialRecord, bool) {
	if !strings.Contains(name, "/cryptoKeyVersions/") {
		prefix := strings.TrimSuffix(name, "/") + "/cryptoKeyVersions/"
		var candidates []kmsCryptoKeyVersion
		for _, version := range kmsCryptoKeyVersions.List() {
			if strings.HasPrefix(version.Name, prefix) && version.State == "ENABLED" {
				candidates = append(candidates, version)
			}
		}
		sort.Slice(candidates, func(i, j int) bool { return kmsVersionLess(candidates[j].Name, candidates[i].Name) })
		if len(candidates) == 0 {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "trusted wrapping key %s has no enabled version", name)
			return kmsCryptoKeyVersion{}, kmsKeyMaterialRecord{}, false
		}
		name = candidates[0].Name
	}
	version, material, ok := kmsLoadEnabledVersion(w, name)
	if !ok {
		return version, material, false
	}
	if !version.HSMTrusted || version.Algorithm != "AES_256_KWP" ||
		version.ProtectionLevel != "HSM_SINGLE_TENANT" || len(material.Key) != 32 {
		GCPError(w, http.StatusBadRequest, "wrappingKey must be an HSM-trusted AES_256_KWP CryptoKeyVersion", "FAILED_PRECONDITION")
		return version, material, false
	}
	return version, material, true
}

func kmsImportedMaterialRecord(algorithm string, raw []byte) (kmsKeyMaterialRecord, error) {
	switch {
	case strings.HasPrefix(algorithm, "RSA_"), strings.HasPrefix(algorithm, "EC_SIGN_"):
		privateKey, err := x509.ParsePKCS8PrivateKey(raw)
		if err != nil {
			return kmsKeyMaterialRecord{}, fmt.Errorf("wrapped key material must contain a PKCS#8 private key: %w", err)
		}
		switch {
		case strings.HasPrefix(algorithm, "RSA_"):
			rsaKey, ok := privateKey.(*rsa.PrivateKey)
			if !ok {
				return kmsKeyMaterialRecord{}, fmt.Errorf("algorithm %s requires an RSA private key", algorithm)
			}
			wantBits := 0
			switch {
			case strings.Contains(algorithm, "_2048_"):
				wantBits = 2048
			case strings.Contains(algorithm, "_3072_"):
				wantBits = 3072
			case strings.Contains(algorithm, "_4096_"):
				wantBits = 4096
			}
			if wantBits != 0 && rsaKey.N.BitLen() != wantBits {
				return kmsKeyMaterialRecord{}, fmt.Errorf("algorithm %s requires a %d-bit RSA private key", algorithm, wantBits)
			}
		case strings.HasPrefix(algorithm, "EC_SIGN_"):
			ecKey, ok := privateKey.(*ecdsa.PrivateKey)
			if !ok {
				return kmsKeyMaterialRecord{}, fmt.Errorf("algorithm %s requires an EC private key", algorithm)
			}
			wantBits := 256
			if strings.Contains(algorithm, "P384") {
				wantBits = 384
			}
			if ecKey.Curve.Params().BitSize != wantBits {
				return kmsKeyMaterialRecord{}, fmt.Errorf("algorithm %s requires a P-%d EC private key", algorithm, wantBits)
			}
		}
		pemBytes, err := kmsPrivateKeyPEM(privateKey)
		if err != nil {
			return kmsKeyMaterialRecord{}, err
		}
		return kmsKeyMaterialRecord{PrivatePEM: pemBytes}, nil
	default:
		var wantBytes int
		switch algorithm {
		case "GOOGLE_SYMMETRIC_ENCRYPTION", "AES_256_GCM", "AES_256_CBC", "AES_256_CTR", "AES_256_KWP", "HMAC_SHA256":
			wantBytes = 32
		case "AES_128_GCM", "AES_128_CBC", "AES_128_CTR":
			wantBytes = 16
		case "HMAC_SHA1":
			wantBytes = 20
		case "HMAC_SHA224":
			wantBytes = 28
		case "HMAC_SHA384":
			wantBytes = 48
		case "HMAC_SHA512":
			wantBytes = 64
		default:
			return kmsKeyMaterialRecord{}, fmt.Errorf("algorithm %s is not supported for imported key material", algorithm)
		}
		if len(raw) != wantBytes {
			return kmsKeyMaterialRecord{}, fmt.Errorf("algorithm %s requires exactly %d bytes of key material", algorithm, wantBytes)
		}
		return kmsKeyMaterialRecord{Key: append([]byte(nil), raw...)}, nil
	}
}

var kmsKWPAIVPrefix = [4]byte{0xa6, 0x59, 0x59, 0xa6}

func kmsAESKeyWrapWithPadding(kek, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	if len(plaintext) == 0 || uint64(len(plaintext)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("plaintext length must be between 1 and 2^32-1 bytes")
	}
	n := (len(plaintext) + 7) / 8
	a := make([]byte, 8)
	copy(a[:4], kmsKWPAIVPrefix[:])
	binary.BigEndian.PutUint32(a[4:], uint32(len(plaintext)))
	padded := make([]byte, n*8)
	copy(padded, plaintext)
	if n == 1 {
		out := make([]byte, 16)
		block.Encrypt(out, append(a, padded...))
		return out, nil
	}
	r := make([]byte, len(padded))
	copy(r, padded)
	b := make([]byte, 16)
	for j := 0; j < 6; j++ {
		for i := 1; i <= n; i++ {
			copy(b[:8], a)
			copy(b[8:], r[(i-1)*8:i*8])
			block.Encrypt(b, b)
			t := uint64(n*j + i)
			binary.BigEndian.PutUint64(a, binary.BigEndian.Uint64(b[:8])^t)
			copy(r[(i-1)*8:i*8], b[8:])
		}
	}
	return append(a, r...), nil
}

func kmsAESKeyUnwrapWithPadding(kek, wrapped []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < 16 || len(wrapped)%8 != 0 {
		return nil, fmt.Errorf("wrapped key length must be a multiple of 8 and at least 16 bytes")
	}
	n := len(wrapped)/8 - 1
	var a []byte
	var padded []byte
	if n == 1 {
		decrypted := make([]byte, 16)
		block.Decrypt(decrypted, wrapped)
		a = decrypted[:8]
		padded = decrypted[8:]
	} else {
		a = append([]byte(nil), wrapped[:8]...)
		padded = append([]byte(nil), wrapped[8:]...)
		b := make([]byte, 16)
		for j := 5; j >= 0; j-- {
			for i := n; i >= 1; i-- {
				t := uint64(n*j + i)
				binary.BigEndian.PutUint64(b[:8], binary.BigEndian.Uint64(a)^t)
				copy(b[8:], padded[(i-1)*8:i*8])
				block.Decrypt(b, b)
				copy(a, b[:8])
				copy(padded[(i-1)*8:i*8], b[8:])
			}
		}
	}
	if !bytes.Equal(a[:4], kmsKWPAIVPrefix[:]) {
		return nil, fmt.Errorf("integrity check failed")
	}
	length := int(binary.BigEndian.Uint32(a[4:]))
	if length <= 8*(n-1) || length > 8*n {
		return nil, fmt.Errorf("invalid message length indicator")
	}
	for _, b := range padded[length:] {
		if b != 0 {
			return nil, fmt.Errorf("non-zero RFC 5649 padding")
		}
	}
	return append([]byte(nil), padded[:length]...), nil
}

func kmsNow() string { return time.Now().UTC().Format(time.RFC3339) }

// kmsDecodeBytes accepts both standard and URL-safe base64, padded or not.
// proto3 JSON bytes fields accept either alphabet, and `gcloud kms` emits
// URL-safe base64 (with `-`/`_`) for plaintext/ciphertext — which a plain
// StdEncoding decode rejects.
func kmsDecodeBytes(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}

func kmsCRC(b []byte) int64 { return int64(crc32.Checksum(b, kmsCRC32CTable)) }

// kmsVerifyCRC checks a client-supplied CRC32C against the data. Returns
// (verified, ok): verified is true when the client supplied a checksum
// that matched; ok is false (and an error already written) on mismatch.
func kmsVerifyCRC(w http.ResponseWriter, data []byte, supplied *int64, field string) (bool, bool) {
	if supplied == nil {
		return false, true
	}
	if *supplied != kmsCRC(data) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"%s.crc32c mismatch: got %d, want %d", field, *supplied, kmsCRC(data))
		return false, false
	}
	return true, true
}

func kmsKeyRingName(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", project, location, id)
}

func kmsCryptoKeyName(r *http.Request) string {
	return kmsKeyRingName(sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "keyRing")) +
		"/cryptoKeys/" + sim.PathParam(r, "cryptoKey")
}

// kmsCreateVersion generates a new ENABLED version with fresh key material
// appropriate for the algorithm and returns its numeric ID.
func kmsCreateVersion(keyName, versionID, protection, algorithm string) (string, error) {
	return kmsCreateVersionForAlg(keyName, versionID, protection, algorithm)
}

// kmsCreateVersionForAlg mints an ENABLED version whose key material matches the
// algorithm family: AES-256 bytes for symmetric (GOOGLE_SYMMETRIC_ENCRYPTION /
// AES_*), an HMAC key for HMAC_*, a real RSA-2048 key for RSA_* and an EC P-256
// key for EC_SIGN_*. The crypto is genuine (crypto/rand + crypto/rsa /
// crypto/ecdsa), never faked.
func kmsCreateVersionForAlg(keyName, versionID, protection, algorithm string) (string, error) {
	versionName := keyName + "/cryptoKeyVersions/" + versionID
	now := kmsNow()
	rec := kmsKeyMaterialRecord{}
	switch {
	case strings.HasPrefix(algorithm, "RSA_"):
		bits := 2048
		switch {
		case strings.Contains(algorithm, "4096"):
			bits = 4096
		case strings.Contains(algorithm, "3072"):
			bits = 3072
		}
		priv, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return "", err
		}
		pemBytes, err := kmsPrivateKeyPEM(priv)
		if err != nil {
			return "", err
		}
		rec.PrivatePEM = pemBytes
	case strings.HasPrefix(algorithm, "EC_SIGN_"):
		curve := elliptic.P256()
		if strings.Contains(algorithm, "P384") {
			curve = elliptic.P384()
		}
		priv, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return "", err
		}
		pemBytes, err := kmsPrivateKeyPEM(priv)
		if err != nil {
			return "", err
		}
		rec.PrivatePEM = pemBytes
	default:
		// Symmetric (ENCRYPT_DECRYPT / RAW / AES_*) and HMAC keys: 32 raw bytes.
		material := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, material); err != nil {
			return "", err
		}
		rec.Key = material
	}
	kmsCryptoKeyVersions.Put(versionName, kmsCryptoKeyVersion{
		Name:            versionName,
		State:           "ENABLED",
		ProtectionLevel: protection,
		Algorithm:       algorithm,
		CreateTime:      now,
		GenerateTime:    now,
		HSMTrusted:      protection == "HSM_SINGLE_TENANT" && algorithm == "AES_256_KWP",
	})
	kmsKeyMaterial.Put(versionName, rec)
	return versionID, nil
}

// kmsLocationName builds the projects/{p}/locations/{loc} parent for a request.
func kmsLocationName(r *http.Request) string {
	return fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

// kmsLoadEnabledVersion fetches an ENABLED version and its key material, writing
// the appropriate error and returning ok=false otherwise.
func kmsLoadEnabledVersion(w http.ResponseWriter, versionName string) (kmsCryptoKeyVersion, kmsKeyMaterialRecord, bool) {
	version, ok := kmsGetCryptoKeyVersion(versionName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "CryptoKeyVersion %s not found", versionName)
		return version, kmsKeyMaterialRecord{}, false
	}
	if version.State != "ENABLED" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "CryptoKeyVersion %s is not enabled (state %s)", versionName, version.State)
		return version, kmsKeyMaterialRecord{}, false
	}
	material, ok := kmsKeyMaterial.Get(versionName)
	if !ok {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "missing key material for %s", versionName)
		return version, kmsKeyMaterialRecord{}, false
	}
	return version, material, true
}

// kmsHashForAlg maps an HMAC_* algorithm to its hash constructor.
func kmsHashForAlg(algorithm string) func() hash.Hash {
	switch algorithm {
	case "HMAC_SHA1":
		return sha1.New
	case "HMAC_SHA224":
		return sha256.New224
	case "HMAC_SHA384":
		return sha512.New384
	case "HMAC_SHA512":
		return sha512.New
	default: // HMAC_SHA256
		return sha256.New
	}
}

// kmsPrivateKeyPEM marshals an RSA or EC private key to PKCS#8 PEM.
func kmsPrivateKeyPEM(priv any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// kmsPublicKeyPEM marshals a public key to PKIX PEM (the form KMS returns).
func kmsPublicKeyPEM(pub any) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// kmsPrivateFromPEM parses a PKCS#8 private key PEM.
func kmsPrivateFromPEM(pemBytes []byte) (any, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid private key PEM")
	}
	return x509.ParsePKCS8PrivateKey(block.Bytes)
}

// kmsPublicFromPEM parses a PKCS#8 private key PEM and returns its public half.
func kmsPublicFromPEM(pemBytes []byte) (any, error) {
	priv, err := kmsPrivateFromPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey, nil
	case *ecdsa.PrivateKey:
		return &k.PublicKey, nil
	default:
		return nil, fmt.Errorf("unsupported private key type %T", priv)
	}
}

// kmsAssembleCryptoKey builds the wire CryptoKey, resolving the live
// primary version.
func kmsAssembleCryptoKey(k kmsStoredCryptoKey) kmsCryptoKey {
	out := kmsCryptoKey{
		Name:                     k.Name,
		Purpose:                  k.Purpose,
		CreateTime:               k.CreateTime,
		NextRotationTime:         k.NextRotationTime,
		RotationPeriod:           k.RotationPeriod,
		DestroyScheduledDuration: k.DestroyScheduledDuration,
		Labels:                   k.Labels,
		VersionTemplate: &kmsCryptoKeyVersionTemplate{
			ProtectionLevel: k.ProtectionLevel,
			Algorithm:       k.Algorithm,
		},
	}
	if k.PrimaryVersionID != "" {
		if v, ok := kmsGetCryptoKeyVersion(k.Name + "/cryptoKeyVersions/" + k.PrimaryVersionID); ok {
			primary := v
			out.Primary = &primary
		}
	}
	return out
}

// kmsVersionNumber extracts the trailing numeric version ID from a version
// resource name. Key versions are numbered from one, and the identifier is the
// whole final segment: a segment that is not a positive integer names no
// version, so "0x10" and "1-old" are rejected rather than read as far as the
// first non-digit.
func kmsVersionNumber(versionName string) (int, bool) {
	i := strings.LastIndex(versionName, "/cryptoKeyVersions/")
	if i < 0 {
		return 0, false
	}
	segment := versionName[i+len("/cryptoKeyVersions/"):]
	n, err := strconv.Atoi(segment)
	if err != nil || n < 1 {
		return 0, false
	}
	// The segment has to be the number, not merely parse to it. Atoi accepts a
	// leading sign and leading zeros, so `/cryptoKeyVersions/01` and
	// `/cryptoKeyVersions/+1` both addressed version 1 — extra names for a
	// resource that has exactly one, which is how a caller comes to believe it
	// read a version it never named. Cloud KMS numbers versions from one and
	// writes them plainly.
	if strconv.Itoa(n) != segment {
		return 0, false
	}
	return n, true
}

// kmsVersionLess orders version names by their numeric ID.
func kmsVersionLess(a, b string) bool {
	na, _ := kmsVersionNumber(a)
	nb, _ := kmsVersionNumber(b)
	return na < nb
}

// kmsEncryptBytes seals plaintext with AES-256-GCM and frames the result
// as version(4) || nonce || sealed so decrypt can pick the version.
func kmsEncryptBytes(material []byte, versionNum int, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	blob := make([]byte, 4+len(nonce)+len(sealed))
	binary.BigEndian.PutUint32(blob[:4], uint32(versionNum))
	copy(blob[4:], nonce)
	copy(blob[4+len(nonce):], sealed)
	return blob, nil
}

// kmsParseCiphertext splits the framed ciphertext into its version number
// and the nonce||sealed remainder.
func kmsParseCiphertext(ciphertext []byte) (int, []byte, error) {
	if len(ciphertext) < 4 {
		return 0, nil, fmt.Errorf("ciphertext too short")
	}
	return int(binary.BigEndian.Uint32(ciphertext[:4])), ciphertext[4:], nil
}

func kmsDecryptBytes(material, blob, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, blob[:ns], blob[ns:], aad)
}

// kmsHandleShowEffectiveKajPolicyConfig reports the Key Access Justifications
// policy in force on a project. The effective policy is the one recorded
// against the project, and a project with none has the default available to it
// rather than a policy nobody set.
func kmsHandleShowEffectiveKajPolicyConfig(w http.ResponseWriter, project string) {
	name := "projects/" + project + "/kajPolicyConfig"
	held, ok := kmsKajPolicyConfigs.Get(name)
	if !ok {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"effectiveKajPolicy": map[string]any{
				"name": name, "defaultPolicyAvailable": true,
			},
		})
		return
	}
	effective := map[string]any{"name": held.Name, "defaultPolicyAvailable": true}
	if len(held.DefaultKeyAccessJustifications) > 0 {
		effective["defaultKeyAccessJustificationPolicy"] = held.DefaultKeyAccessJustifications
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"effectiveKajPolicy": effective})
}

// kmsHandleShowEffectiveKajEnrollmentConfig reports how a project is enrolled
// in Key Access Justifications, per protection level. Enrolment is off until
// something turns it on, and reporting it as on would tell a client its keys
// are demanding justifications when they are not.
func kmsHandleShowEffectiveKajEnrollmentConfig(w http.ResponseWriter, project string) {
	off := func() map[string]any {
		return map[string]any{"policyEnforcement": false, "auditLogging": false}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"softwareConfig": off(), "hardwareConfig": off(), "externalConfig": off(),
	})
}
