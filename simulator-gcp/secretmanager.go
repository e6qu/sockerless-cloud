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

	"github.com/e6qu/sockerless-cloud/sim"
)

// errSecretVersionNotEnabled is an access to a DISABLED or DESTROYED version,
// which Secret Manager answers with FAILED_PRECONDITION (400), not 404.
var errSecretVersionNotEnabled = errors.New("secret version is not enabled")

type Secret struct {
	Name           string            `json:"name"`
	CreateTime     string            `json:"createTime"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	VersionAliases map[string]string `json:"versionAliases,omitempty"`
	Ttl            string            `json:"ttl,omitempty"`
	ExpireTime     string            `json:"expireTime,omitempty"`
	Replication    map[string]any    `json:"replication,omitempty"`
	// Nested writable objects, stored verbatim so terraform-provider-google reads
	// back what it wrote.
	Rotation   json.RawMessage `json:"rotation,omitempty"`
	Topics     json.RawMessage `json:"topics,omitempty"`
	SecretType string          `json:"secretType,omitempty"`
}

// SecretVersion is a version's metadata: GetSecretVersion and
// ListSecretVersions carry no payload, which appears only in `:access`
// answers. The payload lives in smSecretPayloads, keyed by version name.
type SecretVersion struct {
	Name                           string `json:"name"`
	CreateTime                     string `json:"createTime"`
	State                          string `json:"state"`
	ClientSpecifiedPayloadChecksum bool   `json:"clientSpecifiedPayloadChecksum,omitempty"`
}

// smPayloadRecord holds a version's raw secret bytes, apart from the
// payload-free SecretVersion.
type smPayloadRecord struct {
	Data       []byte `json:"data"`
	DataCrc32c int64  `json:"dataCrc32c"`
}

// Package-level so Cloud Build resolves secret versions for build steps.
var (
	smSecrets        sim.Store[Secret]
	smSecretVersions sim.Store[SecretVersion]
	smSecretPayloads sim.Store[smPayloadRecord]
	// smVersionSeq holds each secret's version counter; AddSecretVersion bumps it
	// under the store write lock so concurrent adds never collide. The Secret
	// resource has no such field.
	smVersionSeq      sim.Store[smSeqRecord]
	smManagedRotation sim.Store[smManagedRotationRecord]
	smCRC32CTable     = crc32.MakeTable(crc32.Castagnoli)
)

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

	// The global surface: secrets live directly under the project.
	registerSecretManagerSecretRoutes(srv, "/v1/projects/{project}/secrets", smSecretParentGlobal)
	// The regional surface: secrets live under a location. One set of stores
	// keyed by full resource name backs both, so they never collide.
	registerSecretManagerSecretRoutes(srv, "/v1/projects/{project}/locations/{location}/secrets", smSecretParentRegional)
}

func smSecretParentGlobal(r *http.Request) string {
	return fmt.Sprintf("projects/%s/secrets", sim.PathParam(r, "project"))
}

func smSecretParentRegional(r *http.Request) string {
	return fmt.Sprintf("projects/%s/locations/%s/secrets",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

// registerSecretManagerSecretRoutes mounts the secrets, versions and IAM
// surface under prefix; parentFor derives the `…/secrets` parent from the
// request.
func registerSecretManagerSecretRoutes(srv *sim.Server, prefix string, parentFor func(*http.Request) string) {
	srv.HandleFunc("POST "+prefix, func(w http.ResponseWriter, r *http.Request) {
		secretManagerCreateSecret(w, r, parentFor(r))
	})

	srv.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
		secretManagerListSecrets(w, r, parentFor(r))
	})

	// GetSecret and getIamPolicy share the GET on {secretAction}: ServeMux cannot
	// spell `{secret}:verb`, so the handler fans in on the colon suffix.
	srv.HandleFunc("GET "+prefix+"/{secretAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerSecretGetAction(w, r, parentFor(r))
	})

	srv.HandleFunc("PATCH "+prefix+"/{secret}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerPatchSecret(w, r, parentFor(r))
	})

	srv.HandleFunc("DELETE "+prefix+"/{secret}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerDeleteSecret(w, r, parentFor(r))
	})

	// secretAction is "{secret}:verb" for addVersion and the IAM verbs.
	srv.HandleFunc("POST "+prefix+"/{secretAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerSecretPostAction(w, r, parentFor(r))
	})

	srv.HandleFunc("GET "+prefix+"/{secret}/versions", func(w http.ResponseWriter, r *http.Request) {
		secretManagerListVersions(w, r, parentFor(r))
	})

	// versionAction is the version, or "{version}:access".
	srv.HandleFunc("GET "+prefix+"/{secret}/versions/{versionAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerVersionGetAction(w, r, parentFor(r))
	})

	// versionAction is "{version}:enable", ":disable" or ":destroy".
	srv.HandleFunc("POST "+prefix+"/{secret}/versions/{versionAction}", func(w http.ResponseWriter, r *http.Request) {
		secretManagerVersionPostAction(w, r, parentFor(r))
	})
}

func secretManagerCreateSecret(w http.ResponseWriter, r *http.Request, parent string) {
	secretID := r.URL.Query().Get("secretId")
	if secretID == "" {
		GCPError(w, http.StatusBadRequest, "secretId query parameter is required", "INVALID_ARGUMENT")
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}

	name := parent + "/" + secretID
	if _, exists := smSecrets.Get(name); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "secret %s already exists", secretID)
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
	// ListSecrets supports `filter` (labels.env=prod).
	listed, listOK := gcpApplyListParams(w, r, all)
	if !listOK {
		return
	}
	all = listed
	page, next, ok := paginateList(w, r, all, gcpAsc(func(v Secret) string { return v.Name }))
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, secret)
}

func secretManagerPatchSecret(w http.ResponseWriter, r *http.Request, parent string) {
	name := parent + "/" + sim.PathParam(r, "secret")
	secret, ok := smSecrets.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", name)
		return
	}

	updateMask := strings.TrimSpace(r.URL.Query().Get("updateMask"))
	if updateMask == "" {
		GCPError(w, http.StatusBadRequest, "updateMask query parameter is required", "INVALID_ARGUMENT")
		return
	}

	var req struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Topics      json.RawMessage   `json:"topics"`
		Rotation    json.RawMessage   `json:"rotation"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported updateMask field %q", field)
			return
		}
	}
	smSecrets.Put(name, secret)
	sim.WriteJSON(w, http.StatusOK, secret)
}

func secretManagerDeleteSecret(w http.ResponseWriter, r *http.Request, parent string) {
	name := parent + "/" + sim.PathParam(r, "secret")
	if !smSecrets.Delete(name) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", name)
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
		secretManagerGetSecret(w, parent+"/"+secretAction)
		return
	}
	if action != "getIamPolicy" {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown secret action %q", secretAction)
		return
	}
	secretName := parent + "/" + secretID
	if _, ok := smSecrets.Get(secretName); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", secretName)
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), secretName, "getIamPolicy")
}

func secretManagerSecretPostAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretAction := sim.PathParam(r, "secretAction")
	secretID, action, found := gcpCustomMethod(secretAction)
	// Resolve the method before the secret, as Google's frontend does: an
	// unserved POST custom method is a routing failure, not a statement about the
	// secret.
	if !found || !secretManagerSecretPOSTMethods[action] {
		gcpMethodNotFound(w)
		return
	}
	secretName := parent + "/" + secretID
	if _, ok := smSecrets.Get(secretName); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", secretName)
		return
	}
	switch action {
	case "addVersion":
		secret, _ := smSecrets.Get(secretName)
		if secret.SecretType == "CLOUD_SQL_DB_CREDENTIALS" {
			GCPError(w, http.StatusBadRequest, "versions for CLOUD_SQL_DB_CREDENTIALS secrets are managed by Secret Manager", "FAILED_PRECONDITION")
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
		GCPError(w, http.StatusBadRequest, "managed rotation requires a CLOUD_SQL_DB_CREDENTIALS secret", "FAILED_PRECONDITION")
		return
	}
	if _, exists := smManagedRotation.Get(secretName); exists {
		GCPError(w, http.StatusBadRequest, "managed rotation has already been enabled", "FAILED_PRECONDITION")
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	project := strings.Split(secretName, "/")[1]
	if req.Credentials.InstanceID == "" || req.Credentials.Username == "" {
		GCPError(w, http.StatusBadRequest, "instanceId and username are required", "INVALID_ARGUMENT")
		return
	}
	user, ok := firstSQLUser(project, req.Credentials.InstanceID, req.Credentials.Username, "")
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "Cloud SQL user %s on instance %s not found", req.Credentials.Username, req.Credentials.InstanceID)
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
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	smManagedRotation.Put(secretName, record)
	sim.WriteJSON(w, http.StatusOK, version)
}

func secretManagerRotate(w http.ResponseWriter, secretName string) {
	record, ok := smManagedRotation.Get(secretName)
	if !ok {
		GCPError(w, http.StatusBadRequest, "managed rotation is not enabled", "FAILED_PRECONDITION")
		return
	}
	version, err := smApplyManagedRotation(secretName, record, smGeneratedPassword())
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
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
	// The rotated credential reaches the running engine as a users.update does,
	// so the database accepts the new password.
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Payload.Data)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "payload.data must be base64: %v", err)
		return
	}
	hasCRC := req.Payload.DataCrc32c != nil
	var suppliedCRC int64
	if hasCRC {
		suppliedCRC = *req.Payload.DataCrc32c
	}
	ver, err := smAddVersionPayload(secretName, raw, hasCRC, suppliedCRC)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ver)
}

// smAddVersionPayload appends an ENABLED version holding data, with the CRC32C
// AccessSecretVersion reports, validating the client's checksum when hasCRC.
// Version IDs come from the per-secret counter under the store write lock.
// The REST :addVersion and gRPC AddSecretVersion both use it.
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret %s not found", secretName)
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

	versions, ok := gcpApplyListParams(w, r, versions)
	if !ok {
		return
	}
	size, ok := gcpPageSizeParam(w, r, "pageSize")
	if !ok {
		return
	}
	byVersion := gcpDescNumber(func(v SecretVersion) string { return strings.TrimPrefix(v.Name, prefix) })
	page, next, ok := gcpKeyPage(w, versions, []gcpSortKey[SecretVersion]{byVersion}, r.URL.Query().Get("pageToken"), size, secretManagerMaxPageSize, secretManagerMaxPageSize)
	if !ok {
		return
	}
	resp := map[string]any{
		"versions":  page,
		"totalSize": len(versions),
	}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// secretManagerVersionGetAction serves GetSecretVersion and
// AccessSecretVersion (`:access`).
func secretManagerVersionGetAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretName := parent + "/" + sim.PathParam(r, "secret")
	versionAction := sim.PathParam(r, "versionAction")
	versionID, action, found := strings.Cut(versionAction, ":")
	if !found {
		// GetSecretVersion answers the version metadata, which terraform reads back
		// after create.
		versionID = versionAction
		if versionID == "latest" {
			resolved, ok := resolveLatestVersionIDForSecret(secretName)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no enabled versions for secret %s", secretName)
				return
			}
			versionID = resolved
		}
		versionName := fmt.Sprintf("%s/versions/%s", secretName, versionID)
		ver, ok := smSecretVersions.Get(versionName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret version %s not found", versionName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, ver)
		return
	}
	if action != "access" {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown version action %q", versionAction)
		return
	}
	payload, resolvedID, err := accessSecretPayloadResolvedForSecret(secretName, versionID)
	if err != nil {
		if errors.Is(err, errSecretVersionNotEnabled) {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "%s", err.Error())
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s", err.Error())
		return
	}
	// Secret Manager answers `latest` with the concrete version in `name`, so
	// clients can pin it, detect rotation and log what served them.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"name": fmt.Sprintf("%s/versions/%s", secretName, resolvedID),
		"payload": map[string]string{
			"data":       base64.StdEncoding.EncodeToString(payload.Data),
			"dataCrc32c": strconv.FormatInt(payload.DataCrc32c, 10),
		},
	})
}

// secretManagerVersionPostAction serves :enable, :disable and :destroy.
// google_secret_version POSTs :enable after create; versions start ENABLED,
// so it answers the version unchanged.
func secretManagerVersionPostAction(w http.ResponseWriter, r *http.Request, parent string) {
	secretName := parent + "/" + sim.PathParam(r, "secret")
	versionAction := sim.PathParam(r, "versionAction")
	versionID, action, found := strings.Cut(versionAction, ":")
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "missing :action suffix on version %q", versionAction)
		return
	}
	// :enable, :disable and :destroy on `latest` act on the version it resolves
	// to, which the answer's `name` carries.
	if versionID == "latest" {
		resolved, ok := resolveLatestVersionIDForSecret(secretName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no enabled versions for secret %s", secretName)
			return
		}
		versionID = resolved
	}
	versionName := fmt.Sprintf("%s/versions/%s", secretName, versionID)
	ver, ok := smSecretVersions.Get(versionName)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "secret version %s not found", versionName)
		return
	}
	updated, ok, err := smSetVersionState(versionName, ver, action)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown version action %q", versionAction)
		return
	}
	sim.WriteJSON(w, http.StatusOK, updated)
}

// smSetVersionState moves a version to enable, disable or destroy; destroy
// also drops the payload, keeping the metadata, as Secret Manager does. The
// REST and gRPC verbs both use it; ok is false for an unknown action.
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

// secretManagerMaxPageSize is the largest page ListSecretVersions returns.
const secretManagerMaxPageSize = 25000

// accessSecretPayload resolves a version reference, a number or "latest", to
// its payload, for Cloud Build's secretEnv expansion.
func accessSecretPayload(project, secretID, version string) ([]byte, error) {
	secretName := fmt.Sprintf("projects/%s/secrets/%s", project, secretID)
	payload, _, err := accessSecretPayloadResolvedForSecret(secretName, version)
	if err != nil {
		return nil, err
	}
	return payload.Data, nil
}

// accessSecretPayloadResolvedForSecret is accessSecretPayload keyed by full
// secret name, for both surfaces, and also returns the version "latest"
// resolved to, which Secret Manager echoes in every `:access` name.
func accessSecretPayloadResolvedForSecret(secretName, version string) (smPayloadRecord, string, error) {
	resolvedID := version
	if version == "latest" {
		// "latest" is the highest version whatever its state; accessing it when it is
		// DISABLED or DESTROYED fails rather than falling back.
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
			// The simulator assigns integer version IDs; a non-numeric one is corrupt
			// state, not one to skip.
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

// resolveSecretManagerReference resolves a
// `projects/{p}/secrets/{s}/versions/{v}` reference, as in Cloud Build's
// availableSecrets, to its payload.
func resolveSecretManagerReference(ref string) ([]byte, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "secrets" || parts[4] != "versions" {
		return nil, fmt.Errorf("invalid secret reference %q; expected projects/P/secrets/S/versions/V", ref)
	}
	return accessSecretPayload(parts[1], parts[3], parts[5])
}
