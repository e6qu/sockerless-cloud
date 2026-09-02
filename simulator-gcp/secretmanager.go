package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// errSecretVersionNotEnabled signals an access attempt on a DISABLED or
// DESTROYED version — real GCP returns FAILED_PRECONDITION (400), not 404.
var errSecretVersionNotEnabled = errors.New("secret version is not enabled")

// Secret Manager v1 slice. A build references secret versions through
// `availableSecrets.secretManager[].versionName`, so the simulator returns the
// secret payload for Cloud Build to expand into env vars before executing the
// build
// step. Real API: https://cloud.google.com/secret-manager/docs/reference/rest

// Secret represents a Cloud Secret Manager secret resource.
type Secret struct {
	Name           string            `json:"name"`
	CreateTime     string            `json:"createTime"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	VersionAliases map[string]string `json:"versionAliases,omitempty"`
	Ttl            string            `json:"ttl,omitempty"`
	ExpireTime     string            `json:"expireTime,omitempty"`
	Replication    map[string]any    `json:"replication,omitempty"`
	// Nested writable objects the sim persists verbatim so create→get
	// round-trips byte-exact for the terraform-provider-google read path.
	Rotation   json.RawMessage `json:"rotation,omitempty"`
	Topics     json.RawMessage `json:"topics,omitempty"`
	SecretType string          `json:"secretType,omitempty"`
}

// SecretVersion is the wire shape for a single secret version —
// metadata only. Real GCP's GetSecretVersion + ListSecretVersions
// return this shape (no payload bytes); the raw payload appears
// only in `:access` responses. The sim stores payload bytes in a
// parallel `smSecretPayloads` store keyed by version name so this
// struct stays payload-free even after sim.Store JSON round-trips.
type SecretVersion struct {
	Name                           string `json:"name"`
	CreateTime                     string `json:"createTime"`
	State                          string `json:"state"`
	ClientSpecifiedPayloadChecksum bool   `json:"clientSpecifiedPayloadChecksum,omitempty"`
}

// smPayloadRecord stores raw secret bytes keyed by full version name.
// Separate from SecretVersion so the wire-shaped struct stays
// payload-free (real GCP only returns payloads from :access).
type smPayloadRecord struct {
	Data       []byte `json:"data"`
	DataCrc32c int64  `json:"dataCrc32c"`
}

// Package-level stores so cloudbuild.go can resolve secret versions
// during build-step env expansion.
var (
	smSecrets        sim.Store[Secret]
	smSecretVersions sim.Store[SecretVersion]
	smSecretPayloads sim.Store[smPayloadRecord]
	// smVersionSeq holds the monotonic per-secret version counter, keyed by
	// secret name. AddSecretVersion bumps it atomically (store Update holds the
	// write lock) so concurrent adds never collide on a version ID. Kept out of
	// the Secret wire shape because GCP's Secret resource has no such field.
	smVersionSeq      sim.Store[smSeqRecord]
	smManagedRotation sim.Store[smManagedRotationRecord]
	smCRC32CTable     = crc32.MakeTable(crc32.Castagnoli)
)

// smSeqRecord is the persisted monotonic version counter for one secret.
type smSeqRecord struct {
	Next int `json:"next"`
}

type smManagedRotationRecord struct {
	Project    string `json:"project"`
	InstanceID string `json:"instanceId"`
	Username   string `json:"username"`
	UserHost   string `json:"userHost,omitempty"`
}

func registerSecretManager(srv *sim.Server) {
	smSecrets = sim.MakeStore[Secret](srv.DB(), "sm_secrets")
	smSecretVersions = sim.MakeStore[SecretVersion](srv.DB(), "sm_secret_versions")
	smSecretPayloads = sim.MakeStore[smPayloadRecord](srv.DB(), "sm_secret_payloads")
	smVersionSeq = sim.MakeStore[smSeqRecord](srv.DB(), "sm_version_seq")
	smManagedRotation = sim.MakeStore[smManagedRotationRecord](srv.DB(), "sm_managed_rotation")

	// Global secret surface: secrets live directly under the project.
	registerSecretManagerSecretRoutes(srv, "/v1/projects/{project}/secrets", smSecretParentGlobal)
	// Regional secret surface: secrets live under a location. The same
	// stores back both surfaces, keyed by the full (location-qualified)
	// resource name, so regional and global secrets never collide.
	registerSecretManagerSecretRoutes(srv, "/v1/projects/{project}/locations/{location}/secrets", smSecretParentRegional)
}

// smSecretParentGlobal builds the `projects/{p}/secrets` parent for a
// request against the global secret surface.
func smSecretParentGlobal(r *http.Request) string {
	return fmt.Sprintf("projects/%s/secrets", sim.PathParam(r, "project"))
}

// smSecretParentRegional builds the `projects/{p}/locations/{loc}/secrets`
// parent for a request against the regional secret surface.
func smSecretParentRegional(r *http.Request) string {
	return fmt.Sprintf("projects/%s/locations/%s/secrets",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

// registerSecretManagerSecretRoutes mounts the full secrets + versions +
// IAM surface under the given mux prefix. The global and regional surfaces
// are identical except for the parent path, so both register through here;
// parentFor derives the `…/secrets` resource-name parent from the request.
func registerSecretManagerSecretRoutes(srv *sim.Server, prefix string, parentFor func(*http.Request) string) {
	// CreateSecret: POST {prefix}?secretId=X
	srv.HandleFunc("POST "+prefix, func(w http.ResponseWriter, r *http.Request) {
		secretManagerCreateSecret(w, r, parentFor(r))
	})

	// ListSecrets: GET {prefix}
	srv.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
		secretManagerListSecrets(w, r, parentFor(r))
	})

	// GetSecret + GetIamPolicy share a single GET handler on {secretAction}.
	// GetSecret is plain "{secret}"; getIamPolicy arrives as the colon-verb
	// "{secret}:getIamPolicy" — Go's ServeMux can't spell `{secret}:verb` as
	// a separate pattern, and it would collide with this one, so the handler
	// fans in on the optional colon suffix.
	srv.HandleFunc("GET "+prefix+"/{secretAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerSecretGetAction(w, r, parentFor(r))
	})

	// UpdateSecret: PATCH {prefix}/{secret}?updateMask=labels
	srv.HandleFunc("PATCH "+prefix+"/{secret}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerPatchSecret(w, r, parentFor(r))
	})

	// DeleteSecret: DELETE {prefix}/{secret}
	srv.HandleFunc("DELETE "+prefix+"/{secret}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerDeleteSecret(w, r, parentFor(r))
	})

	// AddVersion / SetIamPolicy / TestIamPermissions:
	//   POST {prefix}/{secretAction} where secretAction is "{secret}:verb".
	srv.HandleFunc("POST "+prefix+"/{secretAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerSecretPostAction(w, r, parentFor(r))
	})

	// ListSecretVersions: GET {prefix}/{secret}/versions
	srv.HandleFunc("GET "+prefix+"/{secret}/versions", func(w http.ResponseWriter, r *http.Request) {
		secretManagerListVersions(w, r, parentFor(r))
	})

	// GetSecretVersion / AccessSecretVersion:
	//   GET {prefix}/{secret}/versions/{versionAction}
	srv.HandleFunc("GET "+prefix+"/{secret}/versions/{versionAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerVersionGetAction(w, r, parentFor(r))
	})

	// Enable / Disable / Destroy secret versions:
	//   POST {prefix}/{secret}/versions/{versionAction}
	srv.HandleFunc("POST "+prefix+"/{secret}/versions/{versionAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerVersionPostAction(w, r, parentFor(r))
	})
}

func secretManagerCreateSecret(w http.ResponseWriter, r *http.Request, parent string) {
	secretID := r.URL.Query().Get("secretId")
	if secretID == "" {
		sim.GCPError(w, http.StatusBadRequest, "secretId query parameter is required", "INVALID_ARGUMENT")
		return
	}

	var req struct {
		Labels         map[string]string `json:"labels,omitempty"`
		Annotations    map[string]string `json:"annotations,omitempty"`
		VersionAliases map[string]string `json:"versionAliases,omitempty"`
		Ttl            string            `json:"ttl,omitempty"`
		ExpireTime     string            `json:"expireTime,omitempty"`
		Replication    map[string]any    `json:"replication,omitempty"`
		Rotation       json.RawMessage   `json:"rotation,omitempty"`
		Topics         json.RawMessage   `json:"topics,omitempty"`
		SecretType     string            `json:"secretType,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}

	name := parent + "/" + secretID
	if _, exists := smSecrets.Get(name); exists {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "secret %s already exists", secretID)
		return
	}

	secret := Secret{
		Name:           name,
		CreateTime:     time.Now().UTC().Format(time.RFC3339),
		Labels:         req.Labels,
		Annotations:    req.Annotations,
		VersionAliases: req.VersionAliases,
		Ttl:            req.Ttl,
		ExpireTime:     req.ExpireTime,
		Replication:    req.Replication,
		Rotation:       req.Rotation,
		Topics:         req.Topics,
		SecretType:     req.SecretType,
	}
	smSecrets.Put(name, secret)
	smVersionSeq.Put(name, smSeqRecord{Next: 0})
	sim.WriteJSON(w, http.StatusOK, secret)
}

// ListSecrets is registered explicitly because the global GCS catch-all at
// the same path prefix used to swallow this request and return a GCS-shaped
// 404 with `bucket "v1"` error message.
func secretManagerListSecrets(w http.ResponseWriter, r *http.Request, parent string) {
	prefix := parent + "/"
	var all []Secret
	for _, s := range smSecrets.List() {
		if strings.HasPrefix(s.Name, prefix) {
			all = append(all, s)
		}
	}
	if all == nil {
		all = []Secret{}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	// Honor the `filter` query param (e.g. labels.env=prod) the Secret
	// Manager ListSecrets API supports.
	all = gcpApplyListParams(all, r)
	page, next, ok := paginateList(w, r, all)
	if !ok {
		return
	}
	resp := map[string]any{"secrets": page, "totalSize": len(all)}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func secretManagerGetSecret(w http.ResponseWriter, name string) {
	secret, ok := smSecrets.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, secret)
}

func secretManagerPatchSecret(w http.ResponseWriter, r *http.Request, parent string) {
	name := parent + "/" + sim.PathParam(r, "secret")
	secret, ok := smSecrets.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", name)
		return
	}

	updateMask := strings.TrimSpace(r.URL.Query().Get("updateMask"))
	if updateMask == "" {
		sim.GCPError(w, http.StatusBadRequest, "updateMask query parameter is required", "INVALID_ARGUMENT")
		return
	}

	var req struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Topics      json.RawMessage   `json:"topics"`
		Rotation    json.RawMessage   `json:"rotation"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}

	for _, field := range strings.Split(updateMask, ",") {
		switch strings.TrimSpace(field) {
		case "labels":
			secret.Labels = copyLabels(req.Labels)
		case "annotations":
			secret.Annotations = copyLabels(req.Annotations)
		case "topics":
			secret.Topics = req.Topics
		case "rotation":
			secret.Rotation = req.Rotation
		case "":
		default:
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported updateMask field %q", field)
			return
		}
	}
	smSecrets.Put(name, secret)
	sim.WriteJSON(w, http.StatusOK, secret)
}

func secretManagerDeleteSecret(w http.ResponseWriter, r *http.Request, parent string) {
	name := parent + "/" + sim.PathParam(r, "secret")
	if !smSecrets.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", name)
		return
	}

	smVersionSeq.Delete(name)
	smManagedRotation.Delete(name)
	gcpResourceIAMStore().Delete(name)
	prefix := name + "/versions/"
	for _, v := range smSecretVersions.List() {
		if strings.HasPrefix(v.Name, prefix) {
			smSecretVersions.Delete(v.Name)
			smSecretPayloads.Delete(v.Name)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// secretManagerSecretGetAction serves GetSecret (plain "{secret}") and the
// one GET colon-verb Discovery defines on a secret: `:getIamPolicy`.
func secretManagerSecretGetAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretAction := sim.PathParam(r, "secretAction")
	secretID, action, found := strings.Cut(secretAction, ":")
	if !found {
		// Plain GetSecret (no `:action` suffix).
		secretManagerGetSecret(w, parent+"/"+secretAction)
		return
	}
	if action != "getIamPolicy" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown secret action %q", secretAction)
		return
	}
	secretName := parent + "/" + secretID
	if _, ok := smSecrets.Get(secretName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", secretName)
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), secretName, "getIamPolicy")
}

// secretManagerSecretPostAction fans in the POST colon-verbs on a secret
// resource.
func secretManagerSecretPostAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretAction := sim.PathParam(r, "secretAction")
	secretID, action, found := gcpCustomMethod(secretAction)
	// The method is resolved before the secret, the way Google's frontend
	// resolves it: a POST custom method this handler does not serve is a
	// method-routing failure, not a statement about the secret.
	if !found || !secretManagerSecretPOSTMethods[action] {
		gcpMethodNotFound(w)
		return
	}
	secretName := parent + "/" + secretID
	if _, ok := smSecrets.Get(secretName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", secretName)
		return
	}
	switch action {
	case "addVersion":
		secret, _ := smSecrets.Get(secretName)
		if secret.SecretType == "CLOUD_SQL_DB_CREDENTIALS" {
			sim.GCPError(w, http.StatusBadRequest, "versions for CLOUD_SQL_DB_CREDENTIALS secrets are managed by Secret Manager", "FAILED_PRECONDITION")
			return
		}
		secretManagerAddVersion(w, r, secretName)
	case "enableManagedRotation":
		secretManagerEnableManagedRotation(w, r, secretName)
	case "rotateSecret":
		secretManagerRotate(w, secretName)
	case "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), secretName, action)
	}
}

// secretManagerSecretPOSTMethods are the POST custom methods served on a
// secret, including managed Cloud SQL credential rotation.
var secretManagerSecretPOSTMethods = map[string]bool{
	"addVersion":            true,
	"enableManagedRotation": true,
	"rotateSecret":          true,
	"setIamPolicy":          true,
	"testIamPermissions":    true,
}

func secretManagerEnableManagedRotation(w http.ResponseWriter, r *http.Request, secretName string) {
	secret, _ := smSecrets.Get(secretName)
	if secret.SecretType != "CLOUD_SQL_DB_CREDENTIALS" {
		sim.GCPError(w, http.StatusBadRequest, "managed rotation requires a CLOUD_SQL_DB_CREDENTIALS secret", "FAILED_PRECONDITION")
		return
	}
	if _, exists := smManagedRotation.Get(secretName); exists {
		sim.GCPError(w, http.StatusBadRequest, "managed rotation has already been enabled", "FAILED_PRECONDITION")
		return
	}
	var req struct {
		Credentials struct {
			InstanceID string `json:"instanceId"`
			Username   string `json:"username"`
			Password   string `json:"password"`
		} `json:"cloudSqlSingleUserCredentials"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	project := strings.Split(secretName, "/")[1]
	if req.Credentials.InstanceID == "" || req.Credentials.Username == "" {
		sim.GCPError(w, http.StatusBadRequest, "instanceId and username are required", "INVALID_ARGUMENT")
		return
	}
	user, ok := firstSQLUser(project, req.Credentials.InstanceID, req.Credentials.Username, "")
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Cloud SQL user %s on instance %s not found", req.Credentials.Username, req.Credentials.InstanceID)
		return
	}
	record := smManagedRotationRecord{
		Project: project, InstanceID: req.Credentials.InstanceID,
		Username: req.Credentials.Username, UserHost: user.Host,
	}
	password := req.Credentials.Password
	if password == "" {
		password = smGeneratedPassword()
	}
	version, err := smApplyManagedRotation(secretName, record, password)
	if err != nil {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	smManagedRotation.Put(secretName, record)
	sim.WriteJSON(w, http.StatusOK, version)
}

func secretManagerRotate(w http.ResponseWriter, secretName string) {
	record, ok := smManagedRotation.Get(secretName)
	if !ok {
		sim.GCPError(w, http.StatusBadRequest, "managed rotation is not enabled", "FAILED_PRECONDITION")
		return
	}
	version, err := smApplyManagedRotation(secretName, record, smGeneratedPassword())
	if err != nil {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, version)
}

func smApplyManagedRotation(secretName string, record smManagedRotationRecord, password string) (SecretVersion, error) {
	user, ok := firstSQLUser(record.Project, record.InstanceID, record.Username, record.UserHost)
	if !ok {
		return SecretVersion{}, fmt.Errorf("managed rotation could not find Cloud SQL user %s on instance %s", record.Username, record.InstanceID)
	}
	sealed, err := sqlSealSecret(password)
	if err != nil {
		return SecretVersion{}, fmt.Errorf("seal rotated credential: %w", err)
	}
	sqlUserSecrets.Put(
		sqlUserKey(record.Project, record.InstanceID, user.Host, user.Name),
		sqlUserCredential{Sealed: sealed},
	)
	// A rotated credential reaches the running engine the same way a
	// users.update does, so the database accepts the new password the
	// secret version now carries.
	if err := sqlReconcileIfRunning(record.Project, record.InstanceID); err != nil {
		return SecretVersion{}, fmt.Errorf("apply rotated credential to the database engine: %w", err)
	}
	return smAddVersionPayload(secretName, []byte(password), false, 0)
}

func smGeneratedPassword() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func secretManagerAddVersion(w http.ResponseWriter, r *http.Request, secretName string) {
	var req struct {
		Payload struct {
			Data       string `json:"data"` // base64-encoded
			DataCrc32c *int64 `json:"dataCrc32c,string,omitempty"`
		} `json:"payload"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Payload.Data)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "payload.data must be base64: %v", err)
		return
	}
	hasCRC := req.Payload.DataCrc32c != nil
	var suppliedCRC int64
	if hasCRC {
		suppliedCRC = *req.Payload.DataCrc32c
	}
	ver, err := smAddVersionPayload(secretName, raw, hasCRC, suppliedCRC)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ver)
}

// smAddVersionPayload appends a new ENABLED SecretVersion to the named secret,
// storing the supplied payload bytes and computing the CRC32C checksum the API
// reports back from AccessSecretVersion. When hasCRC is true, the supplied
// checksum is validated against the computed value. Version IDs are reserved
// atomically by bumping the per-secret counter under the store write lock, so
// concurrent AddSecretVersion calls never collide on an ID. Both the REST
// :addVersion handler and the gRPC AddSecretVersion RPC go through here.
func smAddVersionPayload(secretName string, data []byte, hasCRC bool, suppliedCRC32C int64) (SecretVersion, error) {
	checksum := int64(crc32.Checksum(data, smCRC32CTable))
	if hasCRC && suppliedCRC32C != checksum {
		return SecretVersion{}, fmt.Errorf("payload.dataCrc32c mismatch: got %d, want %d", suppliedCRC32C, checksum)
	}

	var assigned int
	if !smVersionSeq.Update(secretName, func(s *smSeqRecord) {
		s.Next++
		assigned = s.Next
	}) {
		n := 0
		for _, v := range smSecretVersions.List() {
			if strings.HasPrefix(v.Name, secretName+"/versions/") {
				n++
			}
		}
		assigned = n + 1
		smVersionSeq.Put(secretName, smSeqRecord{Next: assigned})
	}
	versionID := fmt.Sprintf("%d", assigned)
	versionName := fmt.Sprintf("%s/versions/%s", secretName, versionID)
	ver := SecretVersion{
		Name:                           versionName,
		CreateTime:                     time.Now().UTC().Format(time.RFC3339),
		State:                          "ENABLED",
		ClientSpecifiedPayloadChecksum: hasCRC,
	}
	smSecretVersions.Put(versionName, ver)
	smSecretPayloads.Put(versionName, smPayloadRecord{Data: data, DataCrc32c: checksum})
	return ver, nil
}

func secretManagerListVersions(w http.ResponseWriter, r *http.Request, parent string) {
	secretName := parent + "/" + sim.PathParam(r, "secret")
	if _, ok := smSecrets.Get(secretName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", secretName)
		return
	}
	if filter := strings.TrimSpace(r.URL.Query().Get("filter")); filter != "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported filter %q", filter)
		return
	}

	prefix := secretName + "/versions/"
	var versions []SecretVersion
	for _, v := range smSecretVersions.List() {
		if strings.HasPrefix(v.Name, prefix) {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		iv, iok := secretVersionNumber(versions[i].Name, prefix)
		jv, jok := secretVersionNumber(versions[j].Name, prefix)
		if iok && jok && iv != jv {
			return iv > jv
		}
		if versions[i].CreateTime != versions[j].CreateTime {
			return versions[i].CreateTime > versions[j].CreateTime
		}
		return versions[i].Name > versions[j].Name
	})

	start, pageSize, ok := secretManagerPagination(w, r, len(versions))
	if !ok {
		return
	}
	end := len(versions)
	if pageSize > 0 && start+pageSize < end {
		end = start + pageSize
	}
	page := versions[start:end]
	if page == nil {
		page = []SecretVersion{}
	}
	resp := map[string]any{
		"versions":  page,
		"totalSize": len(versions),
	}
	if end < len(versions) {
		resp["nextPageToken"] = strconv.Itoa(end)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// secretManagerVersionGetAction handles GetSecretVersion (no suffix) and
// AccessSecretVersion (`:access`). Same Go mux workaround as AddVersion.
func secretManagerVersionGetAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretName := parent + "/" + sim.PathParam(r, "secret")
	versionAction := sim.PathParam(r, "versionAction")
	versionID, action, found := strings.Cut(versionAction, ":")
	if !found {
		// Plain GetSecretVersion (no `:action` suffix): return the
		// version metadata. tf-google reads back the version after
		// create to populate the resource state.
		versionID = versionAction
		if versionID == "latest" {
			resolved, ok := resolveLatestVersionIDForSecret(secretName)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no enabled versions for secret %s", secretName)
				return
			}
			versionID = resolved
		}
		versionName := fmt.Sprintf("%s/versions/%s", secretName, versionID)
		ver, ok := smSecretVersions.Get(versionName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret version %s not found", versionName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, ver)
		return
	}
	if action != "access" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown version action %q", versionAction)
		return
	}
	payload, resolvedID, err := accessSecretPayloadResolvedForSecret(secretName, versionID)
	if err != nil {
		if errors.Is(err, errSecretVersionNotEnabled) {
			sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "%s", err.Error())
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s", err.Error())
		return
	}
	// Real GCP resolves `latest` to the concrete version number in the
	// response `name` so clients can pin downstream calls, detect
	// rotation, and log the exact version that served a request.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name": fmt.Sprintf("%s/versions/%s", secretName, resolvedID),
		"payload": map[string]string{
			"data":       base64.StdEncoding.EncodeToString(payload.Data),
			"dataCrc32c": strconv.FormatInt(payload.DataCrc32c, 10),
		},
	})
}

// secretManagerVersionPostAction handles :enable / :disable / :destroy.
// The terraform-provider-google secret_version resource POSTs :enable after
// creating a version. Versions default to ENABLED on create, so an explicit
// enable is an idempotent state transition that still returns the version.
func secretManagerVersionPostAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretName := parent + "/" + sim.PathParam(r, "secret")
	versionAction := sim.PathParam(r, "versionAction")
	versionID, action, found := strings.Cut(versionAction, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "missing :action suffix on version %q", versionAction)
		return
	}
	// Resolve `latest` alias to the concrete version number per real GCP
	// behaviour — :enable/:disable/:destroy on `latest` act on the
	// resolved version, and the response `name` carries that version.
	if versionID == "latest" {
		resolved, ok := resolveLatestVersionIDForSecret(secretName)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no enabled versions for secret %s", secretName)
			return
		}
		versionID = resolved
	}
	versionName := fmt.Sprintf("%s/versions/%s", secretName, versionID)
	ver, ok := smSecretVersions.Get(versionName)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret version %s not found", versionName)
		return
	}
	updated, ok, err := smSetVersionState(versionName, ver, action)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown version action %q", versionAction)
		return
	}
	sim.WriteJSON(w, http.StatusOK, updated)
}

// smSetVersionState transitions a SecretVersion to the requested state. The
// action is one of "enable" / "disable" / "destroy" (matching the REST colon
// verbs). Destroying a version also drops its stored payload bytes — the
// version's metadata is retained but the data is irretrievable, matching real
// Secret Manager. Both the REST :enable/:disable/:destroy handlers and the
// gRPC Enable/Disable/Destroy RPCs go through here. The returned bool is false
// when the action verb is unrecognized so callers can surface the right error.
func smSetVersionState(versionName string, ver SecretVersion, action string) (SecretVersion, bool, error) {
	switch action {
	case "enable":
		ver.State = "ENABLED"
	case "disable":
		ver.State = "DISABLED"
	case "destroy":
		ver.State = "DESTROYED"
		smSecretPayloads.Delete(versionName)
	default:
		return ver, false, nil
	}
	smSecretVersions.Put(versionName, ver)
	return ver, true, nil
}

func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func secretVersionNumber(name, prefix string) (int, bool) {
	raw := strings.TrimPrefix(name, prefix)
	n, err := strconv.Atoi(raw)
	return n, err == nil
}

func secretManagerPagination(w http.ResponseWriter, r *http.Request, total int) (int, int, bool) {
	start := 0
	if token := r.URL.Query().Get("pageToken"); token != "" {
		n, err := strconv.Atoi(token)
		if err != nil || n < 0 || n > total {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken %q", token)
			return 0, 0, false
		}
		start = n
	}

	pageSize := 0
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageSize %q", raw)
			return 0, 0, false
		}
		if n > 25000 {
			n = 25000
		}
		pageSize = n
	}
	return start, pageSize, true
}

// accessSecretPayload resolves a secret-version reference to its raw
// payload. Handles both explicit versions (e.g. "3") and the special
// "latest" alias. Exported for cloudbuild.go's build-step secretEnv
// expansion.
func accessSecretPayload(project, secretID, version string) ([]byte, error) {
	secretName := fmt.Sprintf("projects/%s/secrets/%s", project, secretID)
	payload, _, err := accessSecretPayloadResolvedForSecret(secretName, version)
	if err != nil {
		return nil, err
	}
	return payload.Data, nil
}

// accessSecretPayloadResolvedForSecret is like accessSecretPayload but
// keyed by the full secret resource name (so it serves both the global and
// the regional surface) and also returns the concrete version identifier
// that "latest" resolved to. Real GCP Secret Manager echoes the resolved
// version number in every `:access` response's `name` field — without that,
// rotation-tracking + audit-logging clients see `"latest"` forever and
// can't detect when the underlying version changes.
func accessSecretPayloadResolvedForSecret(secretName, version string) (smPayloadRecord, string, error) {
	resolvedID := version
	if version == "latest" {
		// "latest" is an alias for the highest version number — regardless of
		// state. Accessing it when that version is DISABLED/DESTROYED fails
		// (it does NOT fall back to an older enabled version).
		id, ok := resolveLatestVersionIDForSecret(secretName)
		if !ok {
			return smPayloadRecord{}, "", fmt.Errorf("no versions for secret %s", secretName)
		}
		resolvedID = id
	}
	versionName := fmt.Sprintf("%s/versions/%s", secretName, resolvedID)
	ver, ok := smSecretVersions.Get(versionName)
	if !ok {
		return smPayloadRecord{}, "", fmt.Errorf("secret version %s not found", versionName)
	}
	if ver.State != "ENABLED" {
		return smPayloadRecord{}, "", fmt.Errorf("%w: cannot access the payload of version %s in state %s", errSecretVersionNotEnabled, versionName, ver.State)
	}
	pl, ok := smSecretPayloads.Get(versionName)
	if !ok {
		return smPayloadRecord{}, "", fmt.Errorf("payload for %s not found", versionName)
	}
	return pl, resolvedID, nil
}

// resolveLatestVersionIDForSecret returns the concrete version number of the
// "latest" alias for the given full secret resource name. Returns "" + false
// if no versions exist.
func resolveLatestVersionIDForSecret(secretName string) (string, bool) {
	var latestN int
	for _, v := range smSecretVersions.List() {
		if !strings.HasPrefix(v.Name, secretName+"/versions/") {
			continue
		}
		idStr := strings.TrimPrefix(v.Name, secretName+"/versions/")
		n, err := strconv.Atoi(idStr)
		if err != nil {
			// Version IDs are sim-assigned integers; a non-numeric one is corrupt
			// own-state, not a version to silently skip past when picking "latest".
			continue
		}
		if n > latestN {
			latestN = n
		}
	}
	if latestN == 0 {
		return "", false
	}
	return fmt.Sprintf("%d", latestN), true
}

// resolveSecretManagerReference parses a `projects/{p}/secrets/{s}/versions/{v}`
// reference (as used in Cloud Build's availableSecrets.secretManager[].versionName)
// and returns the resolved payload. Returns an error if the reference is
// malformed or the version doesn't exist.
func resolveSecretManagerReference(ref string) ([]byte, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "secrets" || parts[4] != "versions" {
		return nil, fmt.Errorf("invalid secret reference %q; expected projects/P/secrets/S/versions/V", ref)
	}
	return accessSecretPayload(parts[1], parts[3], parts[5])
}
