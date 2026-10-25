package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KeyVaultCertificate is a certificate at /certificates/{name}.
// Wire shape only; persistence wrapper is kvCertStored.
type KeyVaultCertificate struct {
	ID                string            `json:"id"`
	KeyID             string            `json:"kid,omitempty"`
	SecretID          string            `json:"sid,omitempty"`
	CER               []byte            `json:"cer,omitempty"`
	ContentType       string            `json:"contentType,omitempty"`
	PreserveCertOrder *bool             `json:"preserveCertOrder,omitempty"`
	X509Thumbprint    string            `json:"x5t,omitempty"`
	Policy            *kvCertPolicy     `json:"policy,omitempty"`
	Attributes        KeyVaultAttrs     `json:"attributes,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// kvCertPolicy is the CertificatePolicy wire shape from the Key Vault
// certificates data plane: member names are the snake_case wire names
// (issuer, x509_props, ...), not the SDK client names the swagger maps
// them to via x-ms-client-name.
type kvCertPolicy struct {
	ID              string                 `json:"id,omitempty"`
	KeyProps        *kvCertKeyProps        `json:"key_props,omitempty"`
	SecretProps     *kvCertSecretProps     `json:"secret_props,omitempty"`
	X509Props       *kvCertX509Props       `json:"x509_props,omitempty"`
	LifetimeActions []kvCertLifetimeAction `json:"lifetime_actions,omitempty"`
	Issuer          *kvCertIssuer          `json:"issuer,omitempty"`
	Attributes      *KeyVaultAttrs         `json:"attributes,omitempty"`
}

type kvCertKeyProps struct {
	Exportable *bool  `json:"exportable,omitempty"`
	KeyType    string `json:"kty,omitempty"`
	KeySize    int    `json:"key_size,omitempty"`
	ReuseKey   *bool  `json:"reuse_key,omitempty"`
	Curve      string `json:"crv,omitempty"`
}

type kvCertSecretProps struct {
	ContentType string `json:"contentType,omitempty"`
}

type kvCertX509Props struct {
	Subject          string      `json:"subject,omitempty"`
	EKUs             []string    `json:"ekus,omitempty"`
	SANs             *kvCertSANs `json:"sans,omitempty"`
	KeyUsage         []string    `json:"key_usage,omitempty"`
	ValidityInMonths int         `json:"validity_months,omitempty"`
}

type kvCertSANs struct {
	Emails      []string `json:"emails,omitempty"`
	DNSNames    []string `json:"dns_names,omitempty"`
	UPNs        []string `json:"upns,omitempty"`
	URIs        []string `json:"uris,omitempty"`
	IPAddresses []string `json:"ipAddresses,omitempty"`
}

type kvCertLifetimeAction struct {
	Trigger *kvCertTrigger `json:"trigger,omitempty"`
	Action  *kvCertAction  `json:"action,omitempty"`
}

type kvCertTrigger struct {
	LifetimePercentage int `json:"lifetime_percentage,omitempty"`
	DaysBeforeExpiry   int `json:"days_before_expiry,omitempty"`
}

type kvCertAction struct {
	ActionType string `json:"action_type,omitempty"`
}

type kvCertIssuer struct {
	Name             string `json:"name,omitempty"`
	CertificateType  string `json:"cty,omitempty"`
	CertTransparency *bool  `json:"cert_transparency,omitempty"`
}

type kvCertVersion struct {
	Version string `json:"version"`
	KeyVaultCertificate
}

type kvCertOperation struct {
	ID                    string        `json:"id"`
	Issuer                *kvCertIssuer `json:"issuer,omitempty"`
	CSR                   []byte        `json:"csr,omitempty"`
	CancellationRequested bool          `json:"cancellation_requested"`
	Status                string        `json:"status"`
	StatusDetails         string        `json:"status_details,omitempty"`
	Target                string        `json:"target,omitempty"`
	RequestID             string        `json:"request_id,omitempty"`
}

type kvCertStored struct {
	Vault            string           `json:"vault"`
	Name             string           `json:"name"`
	Versions         []kvCertVersion  `json:"versions,omitempty"`
	PendingOperation *kvCertOperation `json:"pendingOperation,omitempty"`
	DeletedAt        int64            `json:"deletedAt,omitempty"`
	ScheduledPurgeAt int64            `json:"scheduledPurgeAt,omitempty"`
	RecoveryID       string           `json:"recoveryId,omitempty"`
	KeyVaultCertificate
}

func (c kvCertStored) isDeleted() bool { return c.DeletedAt > 0 }

func (c kvCertStored) latest() (kvCertVersion, bool) {
	if len(c.Versions) > 0 {
		return c.Versions[len(c.Versions)-1], true
	}
	if c.ID != "" {
		return kvCertVersion{Version: kvVersionFromID(c.ID), KeyVaultCertificate: c.KeyVaultCertificate}, true
	}
	return kvCertVersion{}, false
}

func (c kvCertStored) findVersion(version string) (kvCertVersion, bool) {
	if version == "" {
		return c.latest()
	}
	for _, v := range c.Versions {
		if v.Version == version || kvVersionFromID(v.ID) == version {
			return v, true
		}
	}
	if c.ID != "" && kvVersionFromID(c.ID) == version {
		return kvCertVersion{Version: version, KeyVaultCertificate: c.KeyVaultCertificate}, true
	}
	return kvCertVersion{}, false
}

// kvCertContact is one certificate contact — the Contact wire shape.
type kvCertContact struct {
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// kvCertContacts is a vault's certificate-contacts collection. The wire shape
// (Contacts) adds a read-only id, stamped on read.
type kvCertContacts struct {
	Vault    string          `json:"vault"`
	Contacts []kvCertContact `json:"contacts,omitempty"`
}

// kvIssuerCredentials is the IssuerCredentials wire shape. The password is
// write-only: it is stored but never rendered back, exactly as Key Vault
// omits pwd from issuer reads.
type kvIssuerCredentials struct {
	AccountID string `json:"account_id,omitempty"`
	Password  string `json:"pwd,omitempty"`
}

// kvIssuerAttributes is the IssuerAttributes wire shape.
type kvIssuerAttributes struct {
	Enabled bool  `json:"enabled"`
	Created int64 `json:"created,omitempty"`
	Updated int64 `json:"updated,omitempty"`
}

// kvCertIssuerStored is one certificate issuer as persisted; issuerBundle
// renders the IssuerBundle wire shape from it.
type kvCertIssuerStored struct {
	Vault       string               `json:"vault"`
	Name        string               `json:"name"`
	Provider    string               `json:"provider,omitempty"`
	Credentials *kvIssuerCredentials `json:"credentials,omitempty"`
	OrgDetails  map[string]any       `json:"org_details,omitempty"`
	Attributes  kvIssuerAttributes   `json:"attributes"`
}

// keyVaultCertificatesIn returns the vault's certificate records, sorted by
// name.
func keyVaultCertificatesIn(vault string) []kvCertStored {
	rows := keyVaultCertsByVault.LookupAll(keyVaultCertificates, vault,
		func(c kvCertStored) []string { return []string{c.Vault} })
	out := append([]kvCertStored(nil), rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func handleKVCertificate(w http.ResponseWriter, r *http.Request, vault, path string) {
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		if r.Method == http.MethodGet {
			handleKVListCertificates(w, r, vault)
			return
		}
		AzureError(w, "BadRequest", "Missing certificate name", http.StatusBadRequest)
		return
	}
	name := segs[1]
	if name == "restore" && len(segs) == 2 && r.Method == http.MethodPost {
		handleKVRestoreCertificate(w, r, vault)
		return
	}
	// /certificates/contacts and /certificates/issuers are vault-level
	// collections that Key Vault addresses under the certificates prefix —
	// "contacts" and "issuers" are route segments, never certificate names.
	if name == "contacts" && len(segs) == 2 {
		switch r.Method {
		case http.MethodGet:
			handleKVGetCertificateContacts(w, r, vault)
		case http.MethodPut:
			handleKVSetCertificateContacts(w, r, vault)
		case http.MethodDelete:
			handleKVDeleteCertificateContacts(w, r, vault)
		default:
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	if name == "issuers" {
		if len(segs) == 2 {
			if r.Method != http.MethodGet {
				AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
				return
			}
			handleKVListCertificateIssuers(w, r, vault)
			return
		}
		issuer := segs[2]
		switch r.Method {
		case http.MethodGet:
			handleKVGetCertificateIssuer(w, r, vault, issuer)
		case http.MethodPut:
			handleKVSetCertificateIssuer(w, r, vault, issuer)
		case http.MethodPatch:
			handleKVUpdateCertificateIssuer(w, r, vault, issuer)
		case http.MethodDelete:
			handleKVDeleteCertificateIssuer(w, r, vault, issuer)
		default:
			AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
		}
		return
	}
	verb := ""
	if len(segs) >= 3 {
		verb = segs[2]
	}
	switch r.Method {
	case http.MethodPost:
		switch {
		case verb == "create":
			handleKVCreateCertificate(w, r, vault, name)
			return
		case verb == "import":
			handleKVImportCertificate(w, r, vault, name)
			return
		case verb == "backup":
			handleKVBackupCertificate(w, r, vault, name)
			return
		case verb == "pending" && len(segs) == 4 && segs[3] == "merge":
			handleKVMergeCertificate(w, r, vault, name)
			return
		}
	case http.MethodGet:
		if verb == "pending" {
			handleKVGetCertificateOperation(w, r, vault, name)
			return
		}
		if verb == "versions" {
			handleKVListCertificateVersions(w, r, vault, name)
			return
		}
		if verb == "policy" && len(segs) == 3 {
			handleKVGetCertificatePolicy(w, r, vault, name)
			return
		}
		handleKVGetCertificate(w, r, vault, name, verb)
		return
	case http.MethodPatch:
		if verb == "pending" {
			handleKVUpdateCertificateOperation(w, r, vault, name)
			return
		}
		// PATCH /certificates/{name}/policy updates the certificate's policy
		// document — "policy" is a route segment, not a version.
		if verb == "policy" && len(segs) == 3 {
			handleKVUpdateCertificatePolicy(w, r, vault, name)
			return
		}
		handleKVUpdateCertificate(w, r, vault, name, verb)
		return
	case http.MethodDelete:
		if verb == "pending" {
			handleKVDeleteCertificateOperation(w, r, vault, name)
			return
		}
		handleKVDeleteCertificate(w, r, vault, name)
		return
	}
	AzureError(w, "MethodNotAllowed", "Method not supported", http.StatusMethodNotAllowed)
}

func keyVaultCertKey(vault, name string) string { return vault + "/" + name }

func handleKVCreateCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Policy            *kvCertPolicy     `json:"policy,omitempty"`
		Attributes        *KeyVaultAttrs    `json:"attributes,omitempty"`
		PreserveCertOrder *bool             `json:"preserveCertOrder,omitempty"`
		Tags              map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		AzureError(w, "InvalidRequest",
			"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultCertificates.Get(keyVaultCertKey(vault, name)); exists && rec.isDeleted() {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Certificate %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	version := sim.NewUUID()
	id := buildKVURL(r, vault, "certificates", name, version)
	keyID := buildKVURL(r, vault, "keys", name, version)
	secretID := buildKVURL(r, vault, "secrets", name, version)
	now := time.Now().Unix()
	certDER, thumbprint, err := makeSelfSignedCertificateDER(name)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	c := KeyVaultCertificate{
		ID:                id,
		KeyID:             keyID,
		SecretID:          secretID,
		CER:               certDER,
		ContentType:       certificateContentType(body.Policy),
		PreserveCertOrder: body.PreserveCertOrder,
		X509Thumbprint:    thumbprint,
		Policy:            body.Policy,
		Attributes:        KeyVaultAttrs{Enabled: true, Created: now, Updated: now},
		Tags:              body.Tags,
	}
	if body.Attributes != nil {
		c.Attributes.Enabled = body.Attributes.Enabled
	}
	op := certificateOperation(r, vault, name, c.ID, body.Policy, "completed")
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), kvCertStored{
		Vault:               vault,
		Name:                name,
		Versions:            []kvCertVersion{{Version: version, KeyVaultCertificate: c}},
		PendingOperation:    &op,
		KeyVaultCertificate: c,
	})
	w.Header().Set("Location", op.ID)
	sim.WriteJSON(w, http.StatusAccepted, op)
}

func handleKVImportCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Base64EncodedCertificate string            `json:"value"`
		Policy                   *kvCertPolicy     `json:"policy,omitempty"`
		Attributes               *KeyVaultAttrs    `json:"attributes,omitempty"`
		PreserveCertOrder        *bool             `json:"preserveCertOrder,omitempty"`
		Tags                     map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if rec, exists := keyVaultCertificates.Get(keyVaultCertKey(vault, name)); exists && rec.isDeleted() {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Certificate %q is currently in a deleted state and must be purged or recovered before re-creating.", name)
		return
	}
	certDER, err := base64.StdEncoding.DecodeString(body.Base64EncodedCertificate)
	if err != nil {
		certDER, err = base64.RawStdEncoding.DecodeString(body.Base64EncodedCertificate)
	}
	if err != nil {
		AzureError(w, "BadParameter", "invalid certificate value: "+err.Error(), http.StatusBadRequest)
		return
	}
	c := newKVCertificate(r, vault, name, certDER, body.Policy, body.Attributes, body.Tags, body.PreserveCertOrder)
	putKVCertificate(vault, name, c, nil)
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleKVMergeCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		X509Certificates []string          `json:"x5c"`
		Attributes       *KeyVaultAttrs    `json:"attributes,omitempty"`
		Tags             map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.X509Certificates) == 0 {
		AzureError(w, "BadParameter", "x5c must contain at least one certificate", http.StatusBadRequest)
		return
	}
	certDER, err := base64.StdEncoding.DecodeString(body.X509Certificates[0])
	if err != nil {
		certDER, err = base64.RawStdEncoding.DecodeString(body.X509Certificates[0])
	}
	if err != nil {
		AzureError(w, "BadParameter", "invalid x5c certificate: "+err.Error(), http.StatusBadRequest)
		return
	}
	policy := &kvCertPolicy{Issuer: &kvCertIssuer{Name: "Unknown"}}
	if rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name)); ok {
		if v, ok := rec.latest(); ok && v.Policy != nil {
			policy = v.Policy
		}
	}
	c := newKVCertificate(r, vault, name, certDER, policy, body.Attributes, body.Tags, nil)
	putKVCertificate(vault, name, c, nil)
	sim.WriteJSON(w, http.StatusCreated, c)
}

func handleKVGetCertificate(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, ok := rec.findVersion(version)
	if !ok {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Version %q of certificate %q was not found.", version, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, v.KeyVaultCertificate)
}

func handleKVListCertificates(w http.ResponseWriter, r *http.Request, vault string) {
	type certItem struct {
		ID             string            `json:"id"`
		Attributes     KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags           map[string]string `json:"tags,omitempty"`
		X509Thumbprint string            `json:"x5t,omitempty"`
		name           string
	}
	all := keyVaultCertificatesIn(vault)
	items := make([]certItem, 0, len(all))
	for _, c := range all {
		if !c.isDeleted() {
			v, ok := c.latest()
			if !ok {
				continue
			}
			items = append(items, certItem{ID: buildKVURL(r, vault, "certificates", c.Name, ""), Attributes: v.Attributes, Tags: v.Tags, X509Thumbprint: v.X509Thumbprint, name: c.Name})
		}
	}
	page, next, pageOK := kvPage(w, r, items, func(c certItem) string { return c.name })
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVListCertificateVersions(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	type certItem struct {
		ID             string            `json:"id"`
		Attributes     KeyVaultAttrs     `json:"attributes,omitempty"`
		Tags           map[string]string `json:"tags,omitempty"`
		X509Thumbprint string            `json:"x5t,omitempty"`
		order          string
	}
	versions := rec.Versions
	if len(versions) == 0 {
		if v, ok := rec.latest(); ok {
			versions = []kvCertVersion{v}
		}
	}
	items := make([]certItem, 0, len(versions))
	for i, v := range versions {
		items = append(items, certItem{ID: v.ID, Attributes: v.Attributes, Tags: v.Tags, X509Thumbprint: v.X509Thumbprint, order: kvVersionOrder(v.Attributes.Created, i)})
	}
	page, next, pageOK := kvPage(w, r, items, func(c certItem) string { return c.order })
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVUpdateCertificate(w http.ResponseWriter, r *http.Request, vault, name, version string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	var body struct {
		Attributes *KeyVaultAttrs    `json:"attributes,omitempty"`
		Tags       map[string]string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	for i, v := range rec.Versions {
		if version != "" && v.Version != version && kvVersionFromID(v.ID) != version {
			continue
		}
		updateCertificateFields(&v.KeyVaultCertificate, body.Attributes, body.Tags)
		rec.Versions[i] = v
		rec.KeyVaultCertificate = v.KeyVaultCertificate
		keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
		sim.WriteJSON(w, http.StatusOK, v.KeyVaultCertificate)
		return
	}
	if len(rec.Versions) == 0 && (version == "" || kvVersionFromID(rec.ID) == version) {
		updateCertificateFields(&rec.KeyVaultCertificate, body.Attributes, body.Tags)
		keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
		sim.WriteJSON(w, http.StatusOK, rec.KeyVaultCertificate)
		return
	}
	AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
		"Version %q of certificate %q was not found.", version, name)
}

func handleKVGetCertificateOperation(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() || rec.PendingOperation == nil {
		AzureErrorf(w, "CertificateOperationNotFound", http.StatusNotFound,
			"Certificate operation for %q was not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rec.PendingOperation)
}

func handleKVUpdateCertificateOperation(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() || rec.PendingOperation == nil {
		AzureErrorf(w, "CertificateOperationNotFound", http.StatusNotFound,
			"Certificate operation for %q was not found.", name)
		return
	}
	var body struct {
		CancellationRequested *bool `json:"cancellation_requested,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if body.CancellationRequested != nil {
		rec.PendingOperation.CancellationRequested = *body.CancellationRequested
		if *body.CancellationRequested {
			rec.PendingOperation.Status = "cancelled"
			rec.PendingOperation.StatusDetails = "Cancellation requested."
		}
	}
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, rec.PendingOperation)
}

func handleKVDeleteCertificateOperation(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() || rec.PendingOperation == nil {
		AzureErrorf(w, "CertificateOperationNotFound", http.StatusNotFound,
			"Certificate operation for %q was not found.", name)
		return
	}
	op := rec.PendingOperation
	rec.PendingOperation = nil
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleKVDeleteCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	now := time.Now().Unix()
	rec.DeletedAt = now
	rec.ScheduledPurgeAt = now + 90*24*60*60
	rec.RecoveryID = buildKVURL(r, vault, "deletedcertificates", name, "")
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, deletedCertificateBundle(rec))
}

func handleKVGetDeletedCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || !rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Deleted certificate %q was not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, deletedCertificateBundle(rec))
}

func handleKVListDeletedCertificates(w http.ResponseWriter, r *http.Request, vault string) {
	deleted := []kvCertStored{}
	for _, rec := range keyVaultCertificatesIn(vault) {
		if rec.isDeleted() {
			deleted = append(deleted, rec)
		}
	}
	recs, next, pageOK := kvPage(w, r, deleted, func(c kvCertStored) string { return c.Name })
	if !pageOK {
		return
	}
	page := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		page = append(page, deletedCertificateBundle(rec))
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleKVPurgeDeletedCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || !rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Deleted certificate %q was not found.", name)
		return
	}
	keyVaultCertificates.Delete(keyVaultCertKey(vault, name))
	w.WriteHeader(http.StatusNoContent)
}

func deletedCertificateBundle(rec kvCertStored) map[string]any {
	return map[string]any{
		"id":                 rec.ID,
		"kid":                rec.KeyID,
		"sid":                rec.SecretID,
		"cer":                rec.CER,
		"x5t":                rec.X509Thumbprint,
		"policy":             rec.Policy,
		"attributes":         rec.Attributes,
		"tags":               rec.Tags,
		"recoveryId":         rec.RecoveryID,
		"deletedDate":        rec.DeletedAt,
		"scheduledPurgeDate": rec.ScheduledPurgeAt,
	}
}

func handleKVBackupCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	writeKVBackupBlob(w, blob)
}

func handleKVRestoreCertificate(w http.ResponseWriter, r *http.Request, vault string) {
	blob, err := readKVBackupBlob(r)
	if err != nil {
		AzureError(w, "BadParameter", err.Error(), http.StatusBadRequest)
		return
	}
	var rec kvCertStored
	if err := json.Unmarshal(blob, &rec); err != nil {
		AzureError(w, "BadParameter", "invalid certificate backup blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rec.Name == "" {
		AzureError(w, "BadParameter", "certificate backup blob is missing certificate name", http.StatusBadRequest)
		return
	}
	if _, exists := keyVaultCertificates.Get(keyVaultCertKey(vault, rec.Name)); exists {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"Certificate %q already exists in this key vault.", rec.Name)
		return
	}
	rec.Vault = vault
	keyVaultCertificates.Put(keyVaultCertKey(vault, rec.Name), rec)
	if latest, ok := rec.latest(); ok {
		sim.WriteJSON(w, http.StatusOK, latest.KeyVaultCertificate)
		return
	}
	AzureError(w, "BadParameter", "certificate backup blob does not contain any certificate versions", http.StatusBadRequest)
}

// handleKVRecoverDeletedCertificate transitions a soft-deleted certificate
// back to active (RecoverDeletedCertificate, POST
// /deletedcertificates/{name}/recover) and answers the recovered
// certificate's current version as a CertificateBundle.
func handleKVRecoverDeletedCertificate(w http.ResponseWriter, r *http.Request, vault, name string) {
	key := keyVaultCertKey(vault, name)
	rec, ok := keyVaultCertificates.Get(key)
	if !ok || !rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"Deleted certificate %q was not found.", name)
		return
	}
	rec.DeletedAt = 0
	rec.ScheduledPurgeAt = 0
	rec.RecoveryID = ""
	keyVaultCertificates.Put(key, rec)
	latest, ok := rec.latest()
	if !ok {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, latest.KeyVaultCertificate)
}

// kvContactsWire renders a vault's contacts collection in the Contacts wire
// shape, with the read-only collection identifier stamped.
func kvContactsWire(r *http.Request, vault string, contacts []kvCertContact) map[string]any {
	return map[string]any{
		"id":       strings.TrimSuffix(buildKVURL(r, vault, "certificates", "contacts", ""), "/"),
		"contacts": contacts,
	}
}

// handleKVGetCertificateContacts serves GetCertificateContacts
// (GET /certificates/contacts). A vault with no contacts set answers 404, as
// real Key Vault does.
func handleKVGetCertificateContacts(w http.ResponseWriter, r *http.Request, vault string) {
	rec, ok := keyVaultCertContacts.Get(vault)
	if !ok {
		AzureError(w, "ContactsNotFound", "Contacts not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, kvContactsWire(r, vault, rec.Contacts))
}

// handleKVSetCertificateContacts serves SetCertificateContacts
// (PUT /certificates/contacts): replaces the vault's contact list and echoes
// the stored collection.
func handleKVSetCertificateContacts(w http.ResponseWriter, r *http.Request, vault string) {
	var body struct {
		Contacts []kvCertContact `json:"contacts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	keyVaultCertContacts.Put(vault, kvCertContacts{Vault: vault, Contacts: body.Contacts})
	sim.WriteJSON(w, http.StatusOK, kvContactsWire(r, vault, body.Contacts))
}

// handleKVDeleteCertificateContacts serves DeleteCertificateContacts
// (DELETE /certificates/contacts): removes the collection and answers the
// contacts it held; 404 when none were set.
func handleKVDeleteCertificateContacts(w http.ResponseWriter, r *http.Request, vault string) {
	rec, ok := keyVaultCertContacts.Get(vault)
	if !ok {
		AzureError(w, "ContactsNotFound", "Contacts not found", http.StatusNotFound)
		return
	}
	keyVaultCertContacts.Delete(vault)
	sim.WriteJSON(w, http.StatusOK, kvContactsWire(r, vault, rec.Contacts))
}

func keyVaultIssuerKey(vault, name string) string { return vault + "/" + name }

// issuerBundle renders a stored issuer in the IssuerBundle wire shape. The
// credential password is write-only and never rendered, as in real Key Vault.
func issuerBundle(r *http.Request, rec kvCertIssuerStored) map[string]any {
	out := map[string]any{
		"id":         strings.TrimSuffix(buildKVURL(r, rec.Vault, "certificates", "issuers", rec.Name), "/"),
		"provider":   rec.Provider,
		"attributes": rec.Attributes,
	}
	if rec.Credentials != nil && rec.Credentials.AccountID != "" {
		out["credentials"] = map[string]any{"account_id": rec.Credentials.AccountID}
	}
	if rec.OrgDetails != nil {
		out["org_details"] = rec.OrgDetails
	}
	return out
}

// keyVaultCertIssuersByVault indexes the issuers by the vault that holds
// them: the listing is reached from a data-plane handler, so decoding every
// vault's issuers on each request is the read the store-scan floor forbids.
var keyVaultCertIssuersByVault sim.GenerationIndex[kvCertIssuerStored]

func keyVaultCertIssuerVaultKeys(rec kvCertIssuerStored) []string {
	if rec.Vault == "" {
		return nil
	}
	return []string{rec.Vault}
}

// handleKVListCertificateIssuers serves GetCertificateIssuers
// (GET /certificates/issuers): the CertificateIssuerItem rows — issuer id and
// provider — for the vault, paged like the other vault collections.
func handleKVListCertificateIssuers(w http.ResponseWriter, r *http.Request, vault string) {
	rows := keyVaultCertIssuersByVault.LookupAll(keyVaultCertIssuers, vault, keyVaultCertIssuerVaultKeys)
	recs, next, pageOK := kvPage(w, r, rows, func(rec kvCertIssuerStored) string { return rec.Name })
	if !pageOK {
		return
	}
	page := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		page = append(page, map[string]any{
			"id":       strings.TrimSuffix(buildKVURL(r, vault, "certificates", "issuers", rec.Name), "/"),
			"provider": rec.Provider,
		})
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// handleKVGetCertificateIssuer serves GetCertificateIssuer
// (GET /certificates/issuers/{name}).
func handleKVGetCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name))
	if !ok {
		AzureErrorf(w, "IssuerNotFound", http.StatusNotFound,
			"Issuer %q not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// handleKVSetCertificateIssuer serves SetCertificateIssuer
// (PUT /certificates/issuers/{name}): stores the issuer record and answers
// the IssuerBundle.
func handleKVSetCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	var body struct {
		Provider    string               `json:"provider"`
		Credentials *kvIssuerCredentials `json:"credentials,omitempty"`
		OrgDetails  map[string]any       `json:"org_details,omitempty"`
		Attributes  *kvIssuerAttributes  `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Provider == "" {
		AzureError(w, "BadParameter", "The 'provider' property is required.", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	rec := kvCertIssuerStored{
		Vault:       vault,
		Name:        name,
		Provider:    body.Provider,
		Credentials: body.Credentials,
		OrgDetails:  body.OrgDetails,
		Attributes:  kvIssuerAttributes{Enabled: true, Created: now, Updated: now},
	}
	if prev, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name)); ok {
		rec.Attributes.Created = prev.Attributes.Created
	}
	if body.Attributes != nil {
		rec.Attributes.Enabled = body.Attributes.Enabled
	}
	keyVaultCertIssuers.Put(keyVaultIssuerKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// handleKVUpdateCertificateIssuer serves UpdateCertificateIssuer
// (PATCH /certificates/issuers/{name}): merges the provided members over the
// stored issuer.
func handleKVUpdateCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name))
	if !ok {
		AzureErrorf(w, "IssuerNotFound", http.StatusNotFound,
			"Issuer %q not found.", name)
		return
	}
	var body struct {
		Provider    string               `json:"provider,omitempty"`
		Credentials *kvIssuerCredentials `json:"credentials,omitempty"`
		OrgDetails  map[string]any       `json:"org_details,omitempty"`
		Attributes  *kvIssuerAttributes  `json:"attributes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Provider != "" {
		rec.Provider = body.Provider
	}
	if body.Credentials != nil {
		rec.Credentials = body.Credentials
	}
	if body.OrgDetails != nil {
		rec.OrgDetails = body.OrgDetails
	}
	if body.Attributes != nil {
		rec.Attributes.Enabled = body.Attributes.Enabled
	}
	rec.Attributes.Updated = time.Now().Unix()
	keyVaultCertIssuers.Put(keyVaultIssuerKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// handleKVDeleteCertificateIssuer serves DeleteCertificateIssuer
// (DELETE /certificates/issuers/{name}): removes the issuer and answers the
// bundle it held.
func handleKVDeleteCertificateIssuer(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertIssuers.Get(keyVaultIssuerKey(vault, name))
	if !ok {
		AzureErrorf(w, "IssuerNotFound", http.StatusNotFound,
			"Issuer %q not found.", name)
		return
	}
	keyVaultCertIssuers.Delete(keyVaultIssuerKey(vault, name))
	sim.WriteJSON(w, http.StatusOK, issuerBundle(r, rec))
}

// kvCertPolicyWire stamps the read-only policy identifier
// (.../certificates/{name}/policy) onto a policy document.
func kvCertPolicyWire(r *http.Request, vault, name string, policy kvCertPolicy) kvCertPolicy {
	policy.ID = strings.TrimSuffix(buildKVURL(r, vault, "certificates", name, "policy"), "/")
	return policy
}

// kvDerivedCertPolicy reconstructs a certificate's policy from the stored
// certificate itself, for certificates imported or merged without an explicit
// policy document. Everything in it is read out of the real certificate — the
// subject from the parsed X.509, the key type and size from its public key,
// the content type from the stored record — mirroring how Key Vault derives
// an imported certificate's policy; the issuer is Unknown exactly as Key
// Vault reports certificates it did not enroll.
func kvDerivedCertPolicy(v KeyVaultCertificate) kvCertPolicy {
	policy := kvCertPolicy{
		SecretProps: &kvCertSecretProps{ContentType: v.ContentType},
		Issuer:      &kvCertIssuer{Name: "Unknown"},
		Attributes:  &KeyVaultAttrs{Enabled: true, Created: v.Attributes.Created, Updated: v.Attributes.Updated},
	}
	cert, err := x509.ParseCertificate(v.CER)
	if err != nil {
		return policy
	}
	policy.X509Props = &kvCertX509Props{Subject: cert.Subject.String()}
	switch key := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		policy.KeyProps = &kvCertKeyProps{KeyType: "RSA", KeySize: key.Size() * 8}
	case *ecdsa.PublicKey:
		policy.KeyProps = &kvCertKeyProps{KeyType: "EC", Curve: "P-" + strconv.Itoa(key.Curve.Params().BitSize)}
	}
	return policy
}

// handleKVGetCertificatePolicy serves GetCertificatePolicy
// (GET /certificates/{name}/policy): the certificate's stored policy, or the
// policy derived from the certificate itself when it was imported without
// one.
func handleKVGetCertificatePolicy(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	v, ok := rec.latest()
	if !ok {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	policy := kvDerivedCertPolicy(v.KeyVaultCertificate)
	if v.Policy != nil {
		policy = *v.Policy
	}
	sim.WriteJSON(w, http.StatusOK, kvCertPolicyWire(r, vault, name, policy))
}

// handleKVUpdateCertificatePolicy serves UpdateCertificatePolicy
// (PATCH /certificates/{name}/policy): replaces the provided policy members
// over the certificate's current policy and stores the result on the current
// version.
func handleKVUpdateCertificatePolicy(w http.ResponseWriter, r *http.Request, vault, name string) {
	rec, ok := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	if !ok || rec.isDeleted() {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	var patch kvCertPolicy
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		AzureError(w, "BadParameter", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	v, ok := rec.latest()
	if !ok {
		AzureErrorf(w, "CertificateNotFound", http.StatusNotFound,
			"A certificate with (name/id) %q was not found in this key vault.", name)
		return
	}
	policy := kvDerivedCertPolicy(v.KeyVaultCertificate)
	if v.Policy != nil {
		policy = *v.Policy
	}
	if patch.KeyProps != nil {
		policy.KeyProps = patch.KeyProps
	}
	if patch.SecretProps != nil {
		policy.SecretProps = patch.SecretProps
	}
	if patch.X509Props != nil {
		policy.X509Props = patch.X509Props
	}
	if patch.LifetimeActions != nil {
		policy.LifetimeActions = patch.LifetimeActions
	}
	if patch.Issuer != nil {
		policy.Issuer = patch.Issuer
	}
	if patch.Attributes != nil {
		policy.Attributes = patch.Attributes
	}
	policy.ID = ""
	stored := policy
	if len(rec.Versions) > 0 {
		rec.Versions[len(rec.Versions)-1].Policy = &stored
	}
	rec.Policy = &stored
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
	sim.WriteJSON(w, http.StatusOK, kvCertPolicyWire(r, vault, name, policy))
}

func makeSelfSignedCertificateDER(name string) ([]byte, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, "", err
	}
	sum := sha1.Sum(der)
	return der, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func certificateContentType(policy *kvCertPolicy) string {
	if policy != nil && policy.SecretProps != nil && policy.SecretProps.ContentType != "" {
		return policy.SecretProps.ContentType
	}
	return "application/x-pkcs12"
}

func certificateOperation(r *http.Request, vault, name, target string, policy *kvCertPolicy, status string) kvCertOperation {
	if status == "" {
		status = "completed"
	}
	issuer := &kvCertIssuer{Name: "Self"}
	if policy != nil && policy.Issuer != nil {
		issuer = policy.Issuer
	}
	return kvCertOperation{
		ID:                    buildKVURL(r, vault, "certificates", name, "pending"),
		Issuer:                issuer,
		CSR:                   []byte("sockerless-keyvault-csr-" + name),
		CancellationRequested: false,
		Status:                status,
		StatusDetails:         "Certificate operation completed.",
		Target:                target,
		RequestID:             sim.NewUUID(),
	}
}

func newKVCertificate(r *http.Request, vault, name string, certDER []byte, policy *kvCertPolicy, attrs *KeyVaultAttrs, tags map[string]string, preserve *bool) KeyVaultCertificate {
	version := sim.NewUUID()
	sum := sha1.Sum(certDER)
	now := time.Now().Unix()
	c := KeyVaultCertificate{
		ID:                buildKVURL(r, vault, "certificates", name, version),
		KeyID:             buildKVURL(r, vault, "keys", name, version),
		SecretID:          buildKVURL(r, vault, "secrets", name, version),
		CER:               certDER,
		ContentType:       certificateContentType(policy),
		PreserveCertOrder: preserve,
		X509Thumbprint:    base64.RawURLEncoding.EncodeToString(sum[:]),
		Policy:            policy,
		Attributes:        KeyVaultAttrs{Enabled: true, Created: now, Updated: now},
		Tags:              tags,
	}
	if attrs != nil {
		c.Attributes.Enabled = attrs.Enabled
		c.Attributes.NotBefore = attrs.NotBefore
		c.Attributes.Expires = attrs.Expires
	}
	return c
}

func putKVCertificate(vault, name string, cert KeyVaultCertificate, op *kvCertOperation) {
	version := kvVersionFromID(cert.ID)
	rec, _ := keyVaultCertificates.Get(keyVaultCertKey(vault, name))
	rec.Vault = vault
	rec.Name = name
	rec.Versions = append(rec.Versions, kvCertVersion{Version: version, KeyVaultCertificate: cert})
	if op != nil {
		rec.PendingOperation = op
	}
	rec.KeyVaultCertificate = cert
	keyVaultCertificates.Put(keyVaultCertKey(vault, name), rec)
}

func updateCertificateFields(c *KeyVaultCertificate, attrs *KeyVaultAttrs, tags map[string]string) {
	if attrs != nil {
		c.Attributes.Enabled = attrs.Enabled
		c.Attributes.NotBefore = attrs.NotBefore
		c.Attributes.Expires = attrs.Expires
		c.Attributes.Updated = time.Now().Unix()
	}
	if tags != nil {
		c.Tags = tags
	}
}
