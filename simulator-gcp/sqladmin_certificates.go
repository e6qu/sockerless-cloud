package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // Cloud SQL names a certificate by its SHA-1 fingerprint.
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Cloud SQL instance's certificates are real X.509 material
// (specs/cloud-api/gcp/sql-authorize-ssl.md, sql-manage-ssl-instance.md,
// sql-configure-ssl-instance.md). Each rotation set holds up to three
// versions: the active one, an upcoming one an add placed, and the previous
// one a rotation retired. The data plane presents the active server
// certificate on every new TLS session.
//
//   - Per-instance CA (GOOGLE_MANAGED_INTERNAL_CA, the default): each CA
//     version is an RSA 2048 / SHA-256 CA valid 10 years that issues the server
//     certificate of its own version, so rotating the CA rotates the server
//     certificate with it.
//   - Shared CA (GOOGLE_MANAGED_CAS_CA): a regional root (25 years) and
//     subordinate (10 years), ECDSA P-384 / SHA-384, shared by the region's
//     instances, sign server certificates valid one year whose SAN names the
//     instance's DNS name.
//
// Client certificates and ephemeral certificates are signed by the instance's
// active per-instance CA, which every instance holds whatever its server CA
// mode.

type sqlCertificateVersion struct {
	Cert      SQLSslCert `json:"cert"`
	SealedKey []byte     `json:"sealedKey,omitempty"`
	// Server is the server certificate a per-instance CA version issued.
	Server *sqlCertificateVersion `json:"server,omitempty"`
}

type sqlCertificateRotation struct {
	Versions []sqlCertificateVersion `json:"versions"`
	Active   string                  `json:"active,omitempty"`
	Upcoming string                  `json:"upcoming,omitempty"`
	Previous string                  `json:"previous,omitempty"`
}

type sqlInstanceCertificates struct {
	ServerCA    sqlCertificateRotation `json:"serverCa"`
	ServerCerts sqlCertificateRotation `json:"serverCerts"`
	EntraID     sqlCertificateRotation `json:"entraId"`
}

type sqlRegionalCA struct {
	Root        sqlCertificateVersion `json:"root"`
	Subordinate sqlCertificateVersion `json:"subordinate"`
}

var (
	sqlCertificates sim.Store[sqlInstanceCertificates]
	sqlRegionalCAs  sim.Store[sqlRegionalCA]
	// sqlCertificatesMu serialises read-modify-write of one instance's
	// certificate state across the handlers that rotate it.
	sqlCertificatesMu sync.Mutex
)

const (
	sqlCAModeInternal = "GOOGLE_MANAGED_INTERNAL_CA"
	sqlCAModeShared   = "GOOGLE_MANAGED_CAS_CA"
	sqlCAModeCustomer = "CUSTOMER_MANAGED_CAS_CA"

	sqlPerInstanceValidity = 10 * 365 * 24 * time.Hour
	sqlSharedRootValidity  = 25 * 365 * 24 * time.Hour
	sqlSharedSubValidity   = 10 * 365 * 24 * time.Hour
	sqlSharedLeafValidity  = 365 * 24 * time.Hour
	sqlClientCertValidity  = 10 * 365 * 24 * time.Hour
	sqlEphemeralValidity   = time.Hour
	sqlClientCertsPerInst  = 10
)

const sqlCustomerCAWhy = "a CUSTOMER_MANAGED_CAS_CA server CA lives in a Certificate Authority Service CA pool, and the simulator serves no Certificate Authority Service"

func registerCloudSQLCertificates(srv *sim.Server) {
	sqlCertificates = sim.MakeStore[sqlInstanceCertificates](srv.DB(), "sql_instance_certificates")
	sqlRegionalCAs = sim.MakeStore[sqlRegionalCA](srv.DB(), "sql_regional_cas")
	for _, prefix := range []string{"/v1", "/sql/v1beta4"} {
		base := prefix + "/projects/{project}/instances/{instance}/"
		srv.HandleFunc("POST "+base+"resetSslConfig", handleSQLResetSslConfig)
		srv.HandleFunc("POST "+base+"addServerCa", handleSQLAddServerCa)
		srv.HandleFunc("POST "+base+"rotateServerCa", handleSQLRotateServerCa)
		srv.HandleFunc("POST "+base+"addServerCertificate", handleSQLAddServerCertificate)
		srv.HandleFunc("POST "+base+"rotateServerCertificate", handleSQLRotateServerCertificate)
		srv.HandleFunc("POST "+base+"addEntraIdCertificate", handleSQLAddEntraIdCertificate)
		srv.HandleFunc("POST "+base+"rotateEntraIdCertificate", handleSQLRotateEntraIdCertificate)
	}
}

// sqlServerCaMode is the instance's settings.ipConfiguration.serverCaMode;
// CA_MODE_UNSPECIFIED is the per-instance CA.
func sqlServerCaMode(inst SQLInstance) string {
	ip, _ := inst.Settings["ipConfiguration"].(map[string]any)
	mode, _ := ip["serverCaMode"].(string)
	if mode == "" || mode == "CA_MODE_UNSPECIFIED" {
		return sqlCAModeInternal
	}
	return mode
}

func sqlFingerprint(der []byte) string {
	sum := sha1.Sum(der) //nolint:gosec // the API's sha1Fingerprint.
	return hex.EncodeToString(sum[:])
}

func sqlCertTime(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func sqlSerialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// sqlIssue signs template with the issuer's key, or self-signs it when
// issuer is nil, and seals the subject's private key.
func sqlIssue(template *x509.Certificate, public crypto.PublicKey, private crypto.Signer, issuer *x509.Certificate, issuerKey crypto.Signer) (sqlCertificateVersion, error) {
	serial, err := sqlSerialNumber()
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	template.SerialNumber = serial
	parent, signer := template, issuerKey
	if issuer != nil {
		parent = issuer
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, public, signer)
	if err != nil {
		return sqlCertificateVersion{}, fmt.Errorf("sign certificate: %w", err)
	}
	version := sqlCertificateVersion{Cert: SQLSslCert{
		Kind:             "sql#sslCert",
		CertSerialNumber: serial.String(),
		Cert:             string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		CreateTime:       sqlCertTime(time.Now()),
		ExpirationTime:   sqlCertTime(template.NotAfter),
		CommonName:       template.Subject.CommonName,
		Sha1Fingerprint:  sqlFingerprint(der),
	}}
	if private != nil {
		keyDER, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			return sqlCertificateVersion{}, fmt.Errorf("encode private key: %w", err)
		}
		sealed, err := sqlSealSecret(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})))
		if err != nil {
			return sqlCertificateVersion{}, fmt.Errorf("seal private key: %w", err)
		}
		version.SealedKey = sealed
	}
	return version, nil
}

// sqlParsed opens a stored version's certificate and private key.
func sqlParsed(version sqlCertificateVersion) (*x509.Certificate, crypto.Signer, error) {
	block, _ := pem.Decode([]byte(version.Cert.Cert))
	if block == nil {
		return nil, nil, fmt.Errorf("certificate %s holds no PEM block", version.Cert.Sha1Fingerprint)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if len(version.SealedKey) == 0 {
		return cert, nil, nil
	}
	keyPEM, err := sqlOpenSecret(version.SealedKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open private key of %s: %w", version.Cert.Sha1Fingerprint, err)
	}
	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("private key of %s holds no PEM block", version.Cert.Sha1Fingerprint)
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("private key of %s is a %T", version.Cert.Sha1Fingerprint, key)
	}
	return cert, signer, nil
}

func sqlNewRSAKey() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) }

func sqlNewP384Key() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
}

// sqlNewPerInstanceCA mints a per-instance CA version and the server
// certificate it issues.
func sqlNewPerInstanceCA(inst SQLInstance) (sqlCertificateVersion, error) {
	now := time.Now()
	caKey, err := sqlNewRSAKey()
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	caTemplate := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "Cloud SQL Server CA " + inst.ConnectionName, Organization: []string{"Google, Inc"}},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(sqlPerInstanceValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	ca, err := sqlIssue(caTemplate, &caKey.PublicKey, caKey, nil, caKey)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	caCert, _, err := sqlParsed(ca)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	serverKey, err := sqlNewRSAKey()
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	server, err := sqlIssue(&x509.Certificate{
		Subject:            pkix.Name{CommonName: inst.ConnectionName, Organization: []string{"Google, Inc"}},
		NotBefore:          now.Add(-time.Minute),
		NotAfter:           now.Add(sqlPerInstanceValidity),
		KeyUsage:           x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}, &serverKey.PublicKey, serverKey, caCert, caKey)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	ca.Cert.Instance, server.Cert.Instance = inst.Name, inst.Name
	ca.Server = &server
	return ca, nil
}

// sqlEnsureRegionalCA returns the region's shared root and subordinate CA,
// minting them on first use.
func sqlEnsureRegionalCA(region string) (sqlRegionalCA, error) {
	if ca, ok := sqlRegionalCAs.Get(region); ok {
		return ca, nil
	}
	now := time.Now()
	rootKey, err := sqlNewP384Key()
	if err != nil {
		return sqlRegionalCA{}, err
	}
	root, err := sqlIssue(&x509.Certificate{
		Subject:               pkix.Name{CommonName: "Google Cloud SQL Root CA", Organization: []string{"Google LLC"}},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(sqlSharedRootValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SignatureAlgorithm:    x509.ECDSAWithSHA384,
	}, &rootKey.PublicKey, rootKey, nil, rootKey)
	if err != nil {
		return sqlRegionalCA{}, err
	}
	rootCert, _, err := sqlParsed(root)
	if err != nil {
		return sqlRegionalCA{}, err
	}
	subKey, err := sqlNewP384Key()
	if err != nil {
		return sqlRegionalCA{}, err
	}
	sub, err := sqlIssue(&x509.Certificate{
		Subject:               pkix.Name{CommonName: "Google Cloud SQL Server CA " + region, Organization: []string{"Google LLC"}},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(sqlSharedSubValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SignatureAlgorithm:    x509.ECDSAWithSHA384,
	}, &subKey.PublicKey, subKey, rootCert, rootKey)
	if err != nil {
		return sqlRegionalCA{}, err
	}
	ca := sqlRegionalCA{Root: root, Subordinate: sub}
	sqlRegionalCAs.Put(region, ca)
	return ca, nil
}

// sqlNewSharedServerCert mints a server certificate the region's shared
// subordinate CA signs, naming the instance's DNS name in its SAN.
func sqlNewSharedServerCert(inst SQLInstance) (sqlCertificateVersion, error) {
	regional, err := sqlEnsureRegionalCA(inst.Region)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	subCert, subKey, err := sqlParsed(regional.Subordinate)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	key, err := sqlNewP384Key()
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		Subject:            pkix.Name{CommonName: inst.ConnectionName, Organization: []string{"Google LLC"}},
		NotBefore:          now.Add(-time.Minute),
		NotAfter:           now.Add(sqlSharedLeafValidity),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		SignatureAlgorithm: x509.ECDSAWithSHA384,
	}
	if dnsName := sqlInstanceDNSName(inst); dnsName != "" {
		template.DNSNames = []string{dnsName}
	}
	version, err := sqlIssue(template, &key.PublicKey, key, subCert, subKey)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	version.Cert.Instance = inst.Name
	return version, nil
}

// sqlNewEntraIDCert mints a self-signed certificate for a SQL Server
// instance's Microsoft Entra ID application, on the per-instance CA's key and
// lifetime.
func sqlNewEntraIDCert(inst SQLInstance) (sqlCertificateVersion, error) {
	key, err := sqlNewRSAKey()
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	now := time.Now()
	version, err := sqlIssue(&x509.Certificate{
		Subject:            pkix.Name{CommonName: "Cloud SQL Entra ID " + inst.ConnectionName},
		NotBefore:          now.Add(-time.Minute),
		NotAfter:           now.Add(sqlPerInstanceValidity),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}, &key.PublicKey, key, nil, key)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	version.Cert.Instance = inst.Name
	return version, nil
}

func (set *sqlCertificateRotation) find(fp string) (sqlCertificateVersion, bool) {
	for _, v := range set.Versions {
		if v.Cert.Sha1Fingerprint == fp {
			return v, true
		}
	}
	return sqlCertificateVersion{}, false
}

func (set *sqlCertificateRotation) drop(fp string) {
	if fp == "" {
		return
	}
	set.Versions = slices.DeleteFunc(set.Versions, func(v sqlCertificateVersion) bool {
		return v.Cert.Sha1Fingerprint == fp
	})
}

// add places version in the upcoming slot, replacing a version added earlier
// and never rotated in.
func (set *sqlCertificateRotation) add(version sqlCertificateVersion) {
	set.drop(set.Upcoming)
	set.Versions = append(set.Versions, version)
	set.Upcoming = version.Cert.Sha1Fingerprint
}

// reset leaves version the only one, and active.
func (set *sqlCertificateRotation) reset(version sqlCertificateVersion) {
	*set = sqlCertificateRotation{Versions: []sqlCertificateVersion{version}, Active: version.Cert.Sha1Fingerprint}
}

// rotate makes next active: the upcoming version (the default) moves the
// active one to previous; the previous version rolls back, moving the active
// one to upcoming.
func (set *sqlCertificateRotation) rotate(next string) error {
	if next == "" {
		next = set.Upcoming
	}
	switch {
	case next != "" && next == set.Upcoming:
		set.drop(set.Previous)
		set.Previous, set.Active, set.Upcoming = set.Active, next, ""
	case next != "" && next == set.Previous:
		set.drop(set.Upcoming)
		set.Upcoming, set.Active, set.Previous = set.Active, next, ""
	case next != "" && next == set.Active:
		return fmt.Errorf("version %s is already the active version", next)
	case next != "":
		return fmt.Errorf("no upcoming or previous version has fingerprint %s", next)
	default:
		return fmt.Errorf("no upcoming version exists")
	}
	return nil
}

func (set *sqlCertificateRotation) active() (sqlCertificateVersion, bool) {
	return set.find(set.Active)
}

func (set *sqlCertificateRotation) certs() []SQLSslCert {
	out := make([]SQLSslCert, 0, len(set.Versions))
	for _, v := range set.Versions {
		out = append(out, v.Cert)
	}
	return out
}

// sqlLoadCertificates returns the instance's certificate state, issuing what
// its server CA mode needs and it does not yet hold: a first per-instance CA
// for every instance, and a first shared-CA server certificate for one whose
// mode is GOOGLE_MANAGED_CAS_CA. The caller holds sqlCertificatesMu.
func sqlLoadCertificates(inst SQLInstance) (sqlInstanceCertificates, error) {
	key := sqlInstanceKey(inst.Project, inst.Name)
	state, _ := sqlCertificates.Get(key)
	changed := false
	if state.ServerCA.Active == "" {
		ca, err := sqlNewPerInstanceCA(inst)
		if err != nil {
			return state, err
		}
		state.ServerCA.reset(ca)
		changed = true
	}
	if sqlServerCaMode(inst) == sqlCAModeShared && state.ServerCerts.Active == "" {
		leaf, err := sqlNewSharedServerCert(inst)
		if err != nil {
			return state, err
		}
		state.ServerCerts.reset(leaf)
		changed = true
	}
	if changed {
		sqlCertificates.Put(key, state)
	}
	return state, nil
}

// sqlActiveServerCa is the CA certificate a client of the instance trusts:
// the active per-instance CA, or the region's shared subordinate CA.
func sqlActiveServerCa(inst SQLInstance, state sqlInstanceCertificates) (SQLSslCert, error) {
	if sqlServerCaMode(inst) == sqlCAModeShared {
		regional, err := sqlEnsureRegionalCA(inst.Region)
		if err != nil {
			return SQLSslCert{}, err
		}
		cert := regional.Subordinate.Cert
		cert.Instance = inst.Name
		return cert, nil
	}
	active, ok := state.ServerCA.active()
	if !ok {
		return SQLSslCert{}, fmt.Errorf("instance %s holds no active server CA", inst.Name)
	}
	return active.Cert, nil
}

// sqlIssueInstanceCertificates gives a new instance its server CA and records
// the CA and the service account Cloud SQL acts as for the instance.
func sqlIssueInstanceCertificates(inst *SQLInstance) error {
	sqlCertificatesMu.Lock()
	defer sqlCertificatesMu.Unlock()
	sqlCertificates.Delete(sqlInstanceKey(inst.Project, inst.Name))
	state, err := sqlLoadCertificates(*inst)
	if err != nil {
		return err
	}
	ca, err := sqlActiveServerCa(*inst, state)
	if err != nil {
		return err
	}
	inst.ServerCaCert = &ca
	if number, ok := crmProjectNumber(inst.Project); ok {
		inst.ServiceAccountEmailAddress = gcpServiceAgent(number, "cloud-sql")
	}
	return nil
}

// sqlDropInstanceCertificates removes a deleted instance's certificate state
// and client certificates.
func sqlDropInstanceCertificates(project, instance string) {
	sqlCertificatesMu.Lock()
	defer sqlCertificatesMu.Unlock()
	sqlCertificates.Delete(sqlInstanceKey(project, instance))
	for _, c := range sqlInstanceClientCerts(project, instance) {
		sqlSslCerts.Delete(sqlSslCertKey(project, instance, c.Sha1Fingerprint))
	}
}

// sqlRecordServerCa stores the CA clients trust on the instance resource,
// where instances.get and connect.get serve it as serverCaCert.
func sqlRecordServerCa(inst SQLInstance, state sqlInstanceCertificates) error {
	ca, err := sqlActiveServerCa(inst, state)
	if err != nil {
		return err
	}
	sqlInstances.Update(sqlInstanceKey(inst.Project, inst.Name), func(i *SQLInstance) { i.ServerCaCert = &ca })
	return nil
}

// sqlRefreshServerCa re-records serverCaCert after instances.patch or update,
// which can move the instance to another serverCaMode and so another CA.
func sqlRefreshServerCa(project, instance string) error {
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		return nil
	}
	sqlCertificatesMu.Lock()
	defer sqlCertificatesMu.Unlock()
	state, err := sqlLoadCertificates(inst)
	if err != nil {
		return err
	}
	return sqlRecordServerCa(inst, state)
}

// sqlServerTLSCertificate is the certificate chain the instance's data plane
// presents: the active server certificate, then the CA that issued it.
func sqlServerTLSCertificate(project, instance string) func() (tls.Certificate, error) {
	return func() (tls.Certificate, error) {
		inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
		if !ok {
			return tls.Certificate{}, fmt.Errorf("instance %s/%s does not exist", project, instance)
		}
		if sqlServerCaMode(inst) == sqlCAModeCustomer {
			return tls.Certificate{}, fmt.Errorf("instance %s presents no server certificate: %s", instance, sqlCustomerCAWhy)
		}
		sqlCertificatesMu.Lock()
		state, err := sqlLoadCertificates(inst)
		sqlCertificatesMu.Unlock()
		if err != nil {
			return tls.Certificate{}, err
		}
		var leaf sqlCertificateVersion
		var chain []string
		if sqlServerCaMode(inst) == sqlCAModeShared {
			active, _ := state.ServerCerts.active()
			regional, err := sqlEnsureRegionalCA(inst.Region)
			if err != nil {
				return tls.Certificate{}, err
			}
			leaf, chain = active, []string{regional.Subordinate.Cert.Cert}
		} else {
			active, _ := state.ServerCA.active()
			if active.Server == nil {
				return tls.Certificate{}, fmt.Errorf("instance %s's active server CA issued no server certificate", instance)
			}
			leaf, chain = *active.Server, []string{active.Cert.Cert}
		}
		keyPEM, err := sqlOpenSecret(leaf.SealedKey)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("open server certificate key: %w", err)
		}
		return tls.X509KeyPair([]byte(leaf.Cert.Cert+strings.Join(chain, "")), []byte(keyPEM))
	}
}

// sqlCertificateAction runs one certificate change on the instance's state
// under the lock, and answers the UPDATE operation the change completes.
func sqlCertificateAction(w http.ResponseWriter, r *http.Request, change func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error)) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sqlCertificatesMu.Lock()
	defer sqlCertificatesMu.Unlock()
	state, err := sqlLoadCertificates(inst)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	if status, reason, err := change(inst, &state); err != nil {
		GCPErrorf(w, status, reason, "%v", err)
		return
	}
	sqlCertificates.Put(sqlInstanceKey(project, instance), state)
	if err := sqlRecordServerCa(inst, state); err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, newSQLOperation(project, "UPDATE", instance))
}

// sqlRefusal is an error whose text is Cloud SQL's own message, which can open
// with a capital.
func sqlRefusal(format string, args ...any) error { return fmt.Errorf(format, args...) }

func sqlRequirePerInstanceCA(inst SQLInstance, method, alternative string) error {
	if mode := sqlServerCaMode(inst); mode != sqlCAModeInternal {
		return fmt.Errorf("%s applies to instances whose serverCaMode is GOOGLE_MANAGED_INTERNAL_CA; instance %s uses %s, so use %s", method, inst.Name, mode, alternative)
	}
	return nil
}

func sqlRequireCASCA(inst SQLInstance, method, alternative string) (int, string, error) {
	switch mode := sqlServerCaMode(inst); mode {
	case sqlCAModeShared:
		return 0, "", nil
	case sqlCAModeCustomer:
		return http.StatusNotImplemented, "UNIMPLEMENTED", fmt.Errorf("the simulator serves no %s for instance %s: %s", method, inst.Name, sqlCustomerCAWhy)
	default:
		return http.StatusBadRequest, "FAILED_PRECONDITION", fmt.Errorf("%s applies to instances that use a Certificate Authority Service server CA; instance %s uses %s, so use %s", method, inst.Name, mode, alternative)
	}
}

func handleSQLAddServerCa(w http.ResponseWriter, r *http.Request) {
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		if err := sqlRequirePerInstanceCA(inst, "addServerCa", "addServerCertificate"); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", err
		}
		ca, err := sqlNewPerInstanceCA(inst)
		if err != nil {
			return http.StatusInternalServerError, "INTERNAL", err
		}
		state.ServerCA.add(ca)
		return 0, "", nil
	})
}

func handleSQLRotateServerCa(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RotateServerCaContext struct {
			NextVersion string `json:"nextVersion"`
		} `json:"rotateServerCaContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		if err := sqlRequirePerInstanceCA(inst, "rotateServerCa", "rotateServerCertificate"); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", err
		}
		if err := state.ServerCA.rotate(req.RotateServerCaContext.NextVersion); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", sqlRefusal("No upcoming/previous Server CA Certificate exists: %v", err)
		}
		return 0, "", nil
	})
}

func handleSQLAddServerCertificate(w http.ResponseWriter, r *http.Request) {
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		if status, reason, err := sqlRequireCASCA(inst, "addServerCertificate", "addServerCa"); err != nil {
			return status, reason, err
		}
		leaf, err := sqlNewSharedServerCert(inst)
		if err != nil {
			return http.StatusInternalServerError, "INTERNAL", err
		}
		state.ServerCerts.add(leaf)
		return 0, "", nil
	})
}

func handleSQLRotateServerCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RotateServerCertificateContext struct {
			NextVersion string `json:"nextVersion"`
		} `json:"rotateServerCertificateContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		if status, reason, err := sqlRequireCASCA(inst, "rotateServerCertificate", "rotateServerCa"); err != nil {
			return status, reason, err
		}
		if err := state.ServerCerts.rotate(req.RotateServerCertificateContext.NextVersion); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", sqlRefusal("No upcoming/previous server certificate exists: %v", err)
		}
		return 0, "", nil
	})
}

// sqlRequireSQLServer refuses Microsoft Entra ID certificates on an instance
// that is not SQL Server: settings.entraidConfig configures "the SQL Server
// instance".
func sqlRequireSQLServer(inst SQLInstance) error {
	if !strings.HasPrefix(inst.DatabaseVersion, "SQLSERVER_") {
		return sqlRefusal("Microsoft Entra ID certificates apply to Cloud SQL for SQL Server instances; instance %s runs %s", inst.Name, inst.DatabaseVersion)
	}
	return nil
}

func handleSQLAddEntraIdCertificate(w http.ResponseWriter, r *http.Request) {
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		if err := sqlRequireSQLServer(inst); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", err
		}
		version, err := sqlNewEntraIDCert(inst)
		if err != nil {
			return http.StatusInternalServerError, "INTERNAL", err
		}
		if state.EntraID.Active == "" {
			state.EntraID.reset(version)
		} else {
			state.EntraID.add(version)
		}
		return 0, "", nil
	})
}

func handleSQLRotateEntraIdCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RotateEntraIdCertificateContext struct {
			NextVersion string `json:"nextVersion"`
		} `json:"rotateEntraIdCertificateContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		if err := sqlRequireSQLServer(inst); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", err
		}
		if err := state.EntraID.rotate(req.RotateEntraIdCertificateContext.NextVersion); err != nil {
			return http.StatusBadRequest, "FAILED_PRECONDITION", sqlRefusal("No upcoming/previous Entra ID certificate exists: %v", err)
		}
		return 0, "", nil
	})
}

// handleSQLResetSslConfig deletes every client certificate and replaces the
// server certificate configuration with a fresh one: a new per-instance CA
// and, for a shared-CA instance, a new server certificate.
func handleSQLResetSslConfig(w http.ResponseWriter, r *http.Request) {
	switch mode := r.URL.Query().Get("mode"); mode {
	case "", "RESET_SSL_MODE_UNSPECIFIED", "ALL":
	case "SYNC_FROM_PRIMARY":
		sqlUnserved("resetSslConfig mode SYNC_FROM_PRIMARY",
			"the simulator replicates no TLS configuration from a primary instance")(w, r)
		return
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid mode %q", mode)
		return
	}
	sqlCertificateAction(w, r, func(inst SQLInstance, state *sqlInstanceCertificates) (int, string, error) {
		ca, err := sqlNewPerInstanceCA(inst)
		if err != nil {
			return http.StatusInternalServerError, "INTERNAL", err
		}
		state.ServerCA.reset(ca)
		if sqlServerCaMode(inst) == sqlCAModeShared {
			leaf, err := sqlNewSharedServerCert(inst)
			if err != nil {
				return http.StatusInternalServerError, "INTERNAL", err
			}
			state.ServerCerts.reset(leaf)
		}
		for _, c := range sqlInstanceClientCerts(inst.Project, inst.Name) {
			sqlSslCerts.Delete(sqlSslCertKey(inst.Project, inst.Name, c.Sha1Fingerprint))
		}
		return 0, "", nil
	})
}

// sqlReadCertificates answers a list method from the instance's certificate
// state.
func sqlReadCertificates(w http.ResponseWriter, r *http.Request, answer func(inst SQLInstance, state sqlInstanceCertificates) (any, error)) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	sqlCertificatesMu.Lock()
	state, err := sqlLoadCertificates(inst)
	sqlCertificatesMu.Unlock()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	body, err := answer(inst, state)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleSQLListServerCas(w http.ResponseWriter, r *http.Request) {
	sqlReadCertificates(w, r, func(_ SQLInstance, state sqlInstanceCertificates) (any, error) {
		return map[string]any{
			"kind":          "sql#instancesListServerCas",
			"activeVersion": state.ServerCA.Active,
			"certs":         state.ServerCA.certs(),
		}, nil
	})
}

func handleSQLListServerCertificates(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	if inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); ok && sqlServerCaMode(inst) == sqlCAModeCustomer {
		sqlUnserved("listServerCertificates for a CUSTOMER_MANAGED_CAS_CA instance", sqlCustomerCAWhy)(w, r)
		return
	}
	sqlReadCertificates(w, r, func(inst SQLInstance, state sqlInstanceCertificates) (any, error) {
		if sqlServerCaMode(inst) == sqlCAModeShared {
			regional, err := sqlEnsureRegionalCA(inst.Region)
			if err != nil {
				return nil, err
			}
			// caCerts[i] is the CA that signed serverCerts[i]: the regional
			// subordinate, carrying its trust chain up to the root.
			chain := regional.Subordinate.Cert
			chain.Cert += regional.Root.Cert.Cert
			chain.Instance = inst.Name
			servers := state.ServerCerts.certs()
			cas := make([]SQLSslCert, len(servers))
			for i := range servers {
				cas[i] = chain
			}
			return map[string]any{
				"kind":          "sql#instancesListServerCertificates",
				"activeVersion": state.ServerCerts.Active,
				"caCerts":       cas,
				"serverCerts":   servers,
			}, nil
		}
		servers := []SQLSslCert{}
		activeServer := ""
		for _, v := range state.ServerCA.Versions {
			if v.Server == nil {
				continue
			}
			servers = append(servers, v.Server.Cert)
			if v.Cert.Sha1Fingerprint == state.ServerCA.Active {
				activeServer = v.Server.Cert.Sha1Fingerprint
			}
		}
		return map[string]any{
			"kind":          "sql#instancesListServerCertificates",
			"activeVersion": activeServer,
			"caCerts":       state.ServerCA.certs(),
			"serverCerts":   servers,
		}, nil
	})
}

func handleSQLListEntraIdCertificates(w http.ResponseWriter, r *http.Request) {
	sqlReadCertificates(w, r, func(_ SQLInstance, state sqlInstanceCertificates) (any, error) {
		return map[string]any{
			"kind":          "sql#instancesListEntraIdCertificates",
			"activeVersion": state.EntraID.Active,
			"certs":         state.EntraID.certs(),
		}, nil
	})
}

// sqlClientCommonName is the SslCert commonName constraint, [a-zA-Z.-_ ]+,
// with digits, which Cloud SQL's own examples use.
var sqlClientCommonName = regexp.MustCompile(`^[a-zA-Z0-9._ -]+$`)

// sqlSignWithInstanceCA issues a client certificate for public, signed by the
// instance's active per-instance CA. The caller holds sqlCertificatesMu.
func sqlSignWithInstanceCA(inst SQLInstance, commonName string, public crypto.PublicKey, private crypto.Signer, validity time.Duration) (sqlCertificateVersion, error) {
	state, err := sqlLoadCertificates(inst)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	active, _ := state.ServerCA.active()
	caCert, caKey, err := sqlParsed(active)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	now := time.Now()
	version, err := sqlIssue(&x509.Certificate{
		Subject:            pkix.Name{CommonName: commonName},
		NotBefore:          now.Add(-time.Minute),
		NotAfter:           now.Add(validity),
		KeyUsage:           x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}, public, private, caCert, caKey)
	if err != nil {
		return sqlCertificateVersion{}, err
	}
	version.Cert.Instance = inst.Name
	return version, nil
}

func sqlSslCertKey(project, instance, fp string) string {
	return project + "/" + instance + "/" + fp
}

func handleSQLInsertSslCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	var req struct {
		CommonName string `json:"commonName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if !sqlClientCommonName.MatchString(req.CommonName) {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "commonName %q must match [a-zA-Z.-_ ]+", req.CommonName)
		return
	}
	if inst.State != "RUNNABLE" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "instance %s must be running to create an SSL certificate; it is %s", instance, inst.State)
		return
	}
	sqlCertificatesMu.Lock()
	defer sqlCertificatesMu.Unlock()
	existing := sqlInstanceClientCerts(project, instance)
	for _, c := range existing {
		if c.CommonName == req.CommonName {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "a client certificate named %q already exists on instance %s", req.CommonName, instance)
			return
		}
	}
	if len(existing) >= sqlClientCertsPerInst {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "instance %s already holds %d client certificates, the most an instance can", instance, sqlClientCertsPerInst)
		return
	}
	key, err := sqlNewRSAKey()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	version, err := sqlSignWithInstanceCA(inst, req.CommonName, &key.PublicKey, nil, sqlClientCertValidity)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	cert := version.Cert
	cert.SelfLink = gcpSelfLink(r, sqlAPIPrefix(r)+"/projects/"+project+"/instances/"+instance+"/sslCerts/"+cert.Sha1Fingerprint)
	sqlSslCerts.Put(sqlSslCertKey(project, instance, cert.Sha1Fingerprint), cert)
	state, _ := sqlCertificates.Get(sqlInstanceKey(project, instance))
	serverCa, err := sqlActiveServerCa(inst, state)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind": "sql#sslCertsInsert",
		"clientCert": map[string]any{
			"certInfo":       cert,
			"certPrivateKey": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		},
		"serverCaCert": serverCa,
		"operation":    newSQLOperation(project, "UPDATE", instance),
	})
}

// sqlInstanceClientCerts are the client certificates stored for the instance.
func sqlInstanceClientCerts(project, instance string) []SQLSslCert {
	return sqlSslCerts.Filter(func(c SQLSslCert) bool {
		if c.Instance != instance {
			return false
		}
		_, ok := sqlSslCerts.Get(sqlSslCertKey(project, instance, c.Sha1Fingerprint))
		return ok
	})
}

func handleSQLListSslCerts(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	if _, ok := sqlInstances.Get(sqlInstanceKey(project, instance)); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	out := sqlInstanceClientCerts(project, instance)
	if out == nil {
		out = []SQLSslCert{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "sql#sslCertsList", "items": out})
}

func handleSQLGetSslCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	fp := sim.PathParam(r, "sha1Fingerprint")
	c, ok := sqlSslCerts.Get(sqlSslCertKey(project, instance, fp))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sslCert not found: %s", fp)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleSQLDeleteSslCert(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	fp := sim.PathParam(r, "sha1Fingerprint")
	if !sqlSslCerts.Delete(sqlSslCertKey(project, instance, fp)) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "sslCert not found: %s", fp)
		return
	}
	sim.WriteJSON(w, http.StatusOK, newSQLOperation(project, "UPDATE", instance))
}

// sqlParsePublicKey reads the PEM public key an ephemeral certificate request
// carries, PKIX ("PUBLIC KEY") or PKCS #1 ("RSA PUBLIC KEY").
func sqlParsePublicKey(text string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, fmt.Errorf("public_key is not a PEM-encoded public key")
	}
	if block.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(block.Bytes)
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

// sqlIssueEphemeral signs the request's public key with the instance's
// per-instance CA for validity, as the caller's identity.
func sqlIssueEphemeral(w http.ResponseWriter, r *http.Request, project, instance, publicKey string, validity time.Duration) (SQLSslCert, bool) {
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return SQLSslCert{}, false
	}
	public, err := sqlParsePublicKey(publicKey)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return SQLSslCert{}, false
	}
	commonName := strings.TrimPrefix(gcpRequestPrincipal(r), "serviceAccount:")
	if commonName == "" {
		commonName = "ephemeral"
	}
	sqlCertificatesMu.Lock()
	version, err := sqlSignWithInstanceCA(inst, commonName, public, nil, validity)
	sqlCertificatesMu.Unlock()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return SQLSslCert{}, false
	}
	return version.Cert, true
}

func handleSQLCreateEphemeralCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	cert, ok := sqlIssueEphemeral(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "instance"), req.PublicKey, sqlEphemeralValidity)
	if ok {
		sim.WriteJSON(w, http.StatusOK, cert)
	}
}

// handleSQLGenerateEphemeralCert serves connect.generateEphemeralCert, whose
// validDuration bounds the certificate below the hour Cloud SQL issues.
func handleSQLGenerateEphemeralCert(w http.ResponseWriter, r *http.Request, project, instance string) {
	var req struct {
		PublicKey     string `json:"public_key"`
		ValidDuration string `json:"validDuration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	validity := sqlEphemeralValidity
	if req.ValidDuration != "" {
		d, err := time.ParseDuration(req.ValidDuration)
		if err != nil || d <= 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "validDuration %q is not a positive duration", req.ValidDuration)
			return
		}
		validity = min(d, sqlEphemeralValidity)
	}
	cert, ok := sqlIssueEphemeral(w, r, project, instance, req.PublicKey, validity)
	if ok {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"ephemeralCert": cert})
	}
}
