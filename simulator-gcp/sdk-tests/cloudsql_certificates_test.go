package gcp_sdk_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // Cloud SQL names certificates by SHA-1 fingerprint.
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// createSQLInstanceWithEngine creates a Cloud SQL instance and returns it
// once its data plane serves the loopback address it reports.
func createSQLInstanceWithEngine(t *testing.T, svc *sqladmin.Service, project string, instance *sqladmin.DatabaseInstance) *sqladmin.DatabaseInstance {
	t.Helper()
	op, err := svc.Instances.Insert(project, instance).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, instance.Name).Do() })
	waitSQLOperationDone(t, svc, project, op.Name)
	got, err := svc.Instances.Get(project, instance.Name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, got.IpAddresses)
	if ip := net.ParseIP(got.IpAddresses[0].IpAddress); ip == nil || !ip.IsLoopback() {
		if runtime.GOOS == "linux" {
			t.Fatalf("instance reports %s: on Linux the data plane must install", got.IpAddresses[0].IpAddress)
		}
		t.Skipf("host cannot provide a loopback alias at the engine's port (GOOS %s)", runtime.GOOS)
	}
	return got
}

func writePEMFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// connectVerifyingCA opens a session that verifies the data plane's server
// certificate chains to caPEM, the check a client running sslmode=verify-ca
// makes against the server-ca.pem it downloaded.
func connectVerifyingCA(ctx context.Context, address, user, password, database, caPEM string, extra string) (*pgx.Conn, error) {
	caFile := filepath.Join(os.TempDir(), fmt.Sprintf("server-ca-%d.pem", time.Now().UnixNano()))
	if err := os.WriteFile(caFile, []byte(caPEM), 0o600); err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(caFile) }()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s:5432/%s?sslmode=verify-ca&sslrootcert=%s%s", user, address, database, caFile, extra))
	if err != nil {
		return nil, err
	}
	config.Password = password
	return pgx.ConnectConfig(ctx, config)
}

func parseCertPEM(t *testing.T, text string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(text))
	require.NotNil(t, block, "certificate must be PEM")
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

func requireSQLAPIError(t *testing.T, err error, code int) {
	t.Helper()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, code, apiErr.Code, "%v", err)
}

// A per-instance CA rotation moves what the data plane presents: after
// addServerCa a client trusting the old CA still connects, after
// rotateServerCa only the new CA verifies the server, and a rollback to the
// previous version reverses it. resetSslConfig replaces the CA and deletes
// every client certificate.
func TestCloudSQL_ServerCaRotationChangesThePresentedCertificate(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const project, name, rootPassword = "test-project", "ssl-rotate-pg", "RootPassword-123!"
	inst := createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: rootPassword,
	})
	address := inst.IpAddresses[0].IpAddress
	require.NotNil(t, inst.ServerCaCert)
	originalCA := inst.ServerCaCert.Cert
	assert.True(t, parseCertPEM(t, originalCA).IsCA)

	cas, err := svc.Instances.ListServerCas(project, name).Do()
	require.NoError(t, err)
	require.Len(t, cas.Certs, 1)
	original := cas.ActiveVersion
	require.Equal(t, inst.ServerCaCert.Sha1Fingerprint, original)

	connect := func(caPEM string) error {
		conn, err := connectVerifyingCA(testContext, address, "postgres", rootPassword, "postgres", caPEM, "")
		if err == nil {
			_ = conn.Close(testContext)
		}
		return err
	}
	require.NoError(t, connect(originalCA), "the data plane must present a server certificate the instance's CA issued")

	addOp, err := svc.Instances.AddServerCa(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "UPDATE", addOp.OperationType)
	cas, err = svc.Instances.ListServerCas(project, name).Do()
	require.NoError(t, err)
	require.Len(t, cas.Certs, 2)
	assert.Equal(t, original, cas.ActiveVersion, "an added CA waits in the upcoming slot")
	var upcoming *sqladmin.SslCert
	for _, c := range cas.Certs {
		if c.Sha1Fingerprint != original {
			upcoming = c
		}
	}
	require.NotNil(t, upcoming)
	require.NoError(t, connect(originalCA))

	_, err = svc.Instances.RotateServerCa(project, name, &sqladmin.InstancesRotateServerCaRequest{}).Do()
	require.NoError(t, err)
	cas, err = svc.Instances.ListServerCas(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, upcoming.Sha1Fingerprint, cas.ActiveVersion)
	require.NoError(t, connect(upcoming.Cert), "after rotation the server certificate chains to the new CA")
	require.Error(t, connect(originalCA), "after rotation the old CA no longer verifies the server")
	rotated, err := svc.Instances.Get(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, upcoming.Sha1Fingerprint, rotated.ServerCaCert.Sha1Fingerprint)

	_, err = svc.Instances.RotateServerCa(project, name, &sqladmin.InstancesRotateServerCaRequest{
		RotateServerCaContext: &sqladmin.RotateServerCaContext{NextVersion: original},
	}).Do()
	require.NoError(t, err, "rotating to the previous version rolls the rotation back")
	cas, err = svc.Instances.ListServerCas(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, original, cas.ActiveVersion)
	require.NoError(t, connect(originalCA))

	_, err = svc.Instances.RotateServerCa(project, name, &sqladmin.InstancesRotateServerCaRequest{
		RotateServerCaContext: &sqladmin.RotateServerCaContext{NextVersion: "0000000000000000000000000000000000000000"},
	}).Do()
	requireSQLAPIError(t, err, 400)

	_, err = svc.SslCerts.Insert(project, name, &sqladmin.SslCertsInsertRequest{CommonName: "before-reset"}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.ResetSslConfig(project, name).Do()
	require.NoError(t, err)
	cas, err = svc.Instances.ListServerCas(project, name).Do()
	require.NoError(t, err)
	require.Len(t, cas.Certs, 1)
	assert.NotEqual(t, original, cas.ActiveVersion)
	certs, err := svc.SslCerts.List(project, name).Do()
	require.NoError(t, err)
	assert.Empty(t, certs.Items, "resetSslConfig deletes every client certificate")
	require.NoError(t, connect(cas.Certs[0].Cert))
	require.Error(t, connect(originalCA))
}

// sslCerts.insert issues a client certificate and private key the instance's
// CA signs, which a stock driver presents in the TLS handshake.
// createEphemeral and connect.generateEphemeralCert sign the caller's own
// public key for about an hour.
func TestCloudSQL_ClientCertificatesAreSignedByTheInstanceCA(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const project, name, rootPassword = "test-project", "ssl-client-pg", "RootPassword-123!"
	inst := createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: rootPassword,
	})
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(inst.ServerCaCert.Cert)))
	verifyClient := func(cert *x509.Certificate) {
		_, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		require.NoError(t, err, "the instance's CA must sign the client certificate")
	}

	inserted, err := svc.SslCerts.Insert(project, name, &sqladmin.SslCertsInsertRequest{CommonName: "app-client"}).Do()
	require.NoError(t, err)
	require.Equal(t, "DONE", inserted.Operation.Status)
	require.Equal(t, inst.ServerCaCert.Sha1Fingerprint, inserted.ServerCaCert.Sha1Fingerprint)
	clientCert := parseCertPEM(t, inserted.ClientCert.CertInfo.Cert)
	assert.Equal(t, "app-client", clientCert.Subject.CommonName)
	verifyClient(clientCert)
	_, err = tls.X509KeyPair([]byte(inserted.ClientCert.CertInfo.Cert), []byte(inserted.ClientCert.CertPrivateKey))
	require.NoError(t, err, "the private key must belong to the certificate")
	assert.WithinDuration(t, time.Now().Add(10*365*24*time.Hour), clientCert.NotAfter, 48*time.Hour)

	_, err = svc.SslCerts.Insert(project, name, &sqladmin.SslCertsInsertRequest{CommonName: "app-client"}).Do()
	requireSQLAPIError(t, err, 409)

	fp := inserted.ClientCert.CertInfo.Sha1Fingerprint
	got, err := svc.SslCerts.Get(project, name, fp).Do()
	require.NoError(t, err)
	assert.Equal(t, inserted.ClientCert.CertInfo.Cert, got.Cert)
	listed, err := svc.SslCerts.List(project, name).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	certFile := writePEMFile(t, "client-cert.pem", inserted.ClientCert.CertInfo.Cert)
	keyFile := writePEMFile(t, "client-key.pem", inserted.ClientCert.CertPrivateKey)
	conn, err := connectVerifyingCA(testContext, inst.IpAddresses[0].IpAddress, "postgres", rootPassword, "postgres",
		inserted.ServerCaCert.Cert, "&sslcert="+certFile+"&sslkey="+keyFile)
	require.NoError(t, err, "a client presenting the issued certificate connects")
	var one int
	require.NoError(t, conn.QueryRow(testContext, "SELECT 1").Scan(&one))
	require.NoError(t, conn.Close(testContext))

	_, err = svc.SslCerts.Delete(project, name, fp).Do()
	require.NoError(t, err)
	_, err = svc.SslCerts.Get(project, name, fp).Do()
	requireSQLAPIError(t, err, 404)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))

	ephemeral, err := svc.SslCerts.CreateEphemeral(project, name, &sqladmin.SslCertsCreateEphemeralRequest{PublicKey: publicPEM}).Do()
	require.NoError(t, err)
	ephemeralCert := parseCertPEM(t, ephemeral.Cert)
	verifyClient(ephemeralCert)
	assert.True(t, key.PublicKey.Equal(ephemeralCert.PublicKey), "the ephemeral certificate carries the caller's public key")
	assert.WithinDuration(t, time.Now().Add(time.Hour), ephemeralCert.NotAfter, 5*time.Minute)

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecDER, err := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	require.NoError(t, err)
	generated, err := svc.Connect.GenerateEphemeralCert(project, name, &sqladmin.GenerateEphemeralCertRequest{
		PublicKey:     string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER})),
		ValidDuration: "600s",
	}).Do()
	require.NoError(t, err)
	generatedCert := parseCertPEM(t, generated.EphemeralCert.Cert)
	verifyClient(generatedCert)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), generatedCert.NotAfter, 2*time.Minute)

	_, err = svc.SslCerts.CreateEphemeral(project, name, &sqladmin.SslCertsCreateEphemeralRequest{PublicKey: "not a key"}).Do()
	requireSQLAPIError(t, err, 400)
}

// An instance on the shared CA (GOOGLE_MANAGED_CAS_CA) carries server
// certificates the region's subordinate CA signs, naming the instance's DNS
// name; addServerCertificate and rotateServerCertificate move the one the data
// plane presents.
func TestCloudSQL_SharedCAServerCertificateRotation(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	const project, name, rootPassword = "test-project", "ssl-cas-pg", "RootPassword-123!"
	inst := createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: rootPassword,
		Settings: &sqladmin.Settings{Tier: "db-f1-micro", IpConfiguration: &sqladmin.IpConfiguration{ServerCaMode: "GOOGLE_MANAGED_CAS_CA"}},
	})
	settings, err := svc.Connect.Get(project, name).Do()
	require.NoError(t, err)

	listed, err := svc.Instances.ListServerCertificates(project, name).Do()
	require.NoError(t, err)
	require.Len(t, listed.ServerCerts, 1)
	require.Len(t, listed.CaCerts, 1, "caCerts[i] is the CA that signed serverCerts[i]")
	first := listed.ServerCerts[0]
	assert.Equal(t, first.Sha1Fingerprint, listed.ActiveVersion)
	leaf := parseCertPEM(t, first.Cert)
	assert.Contains(t, leaf.DNSNames, settings.DnsName, "a shared-CA server certificate names the instance's DNS name")
	assert.Equal(t, x509.ECDSA, leaf.PublicKeyAlgorithm)
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), leaf.NotAfter, 48*time.Hour)
	subordinate := listed.CaCerts[0].Cert

	_, err = svc.Instances.AddServerCa(project, name).Do()
	requireSQLAPIError(t, err, 400)

	_, err = svc.Instances.AddServerCertificate(project, name).Do()
	require.NoError(t, err)
	listed, err = svc.Instances.ListServerCertificates(project, name).Do()
	require.NoError(t, err)
	require.Len(t, listed.ServerCerts, 2)
	require.Len(t, listed.CaCerts, 2)
	assert.Equal(t, first.Sha1Fingerprint, listed.ActiveVersion)

	_, err = svc.Instances.RotateServerCertificate(project, name, &sqladmin.InstancesRotateServerCertificateRequest{}).Do()
	require.NoError(t, err)
	listed, err = svc.Instances.ListServerCertificates(project, name).Do()
	require.NoError(t, err)
	assert.NotEqual(t, first.Sha1Fingerprint, listed.ActiveVersion)

	presented := presentedPostgresCertificate(t, inst.IpAddresses[0].IpAddress)
	assert.Equal(t, listed.ActiveVersion, fmt.Sprintf("%x", sha1.Sum(presented.Raw)), "the data plane presents the rotated-in certificate")
	conn, err := connectVerifyingCA(testContext, inst.IpAddresses[0].IpAddress, "postgres", rootPassword, "postgres", subordinate, "")
	require.NoError(t, err)
	require.NoError(t, conn.Close(testContext))
}

// presentedPostgresCertificate runs PostgreSQL's SSLRequest against address
// and returns the certificate the server presents.
func presentedPostgresCertificate(t *testing.T, address string) *x509.Certificate {
	t.Helper()
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(address, "5432"), 30*time.Second)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f})
	require.NoError(t, err)
	answer := make([]byte, 1)
	_, err = raw.Read(answer)
	require.NoError(t, err)
	require.Equal(t, byte('S'), answer[0], "the data plane must accept TLS")
	session := tls.Client(raw, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // the test reads the certificate, then verifies it itself.
	require.NoError(t, session.Handshake())
	return session.ConnectionState().PeerCertificates[0]
}

// Microsoft Entra ID certificates rotate on a SQL Server instance: the first
// add is active, the next waits upcoming until a rotation.
func TestCloudSQL_EntraIdCertificateRotation(t *testing.T) {
	t.Parallel()
	svc := sqlAdminService(t)
	const project, name = "test-project", "entra-sqlserver"
	op, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "SQLSERVER_2019_STANDARD", RootPassword: "RootPassword-123!",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, name).Do() })
	waitSQLOperationDone(t, svc, project, op.Name)

	listed, err := svc.Instances.ListEntraIdCertificates(project, name).Do()
	require.NoError(t, err)
	assert.Empty(t, listed.Certs)

	_, err = svc.Instances.AddEntraIdCertificate(project, name).Do()
	require.NoError(t, err)
	listed, err = svc.Instances.ListEntraIdCertificates(project, name).Do()
	require.NoError(t, err)
	require.Len(t, listed.Certs, 1)
	first := listed.ActiveVersion
	require.Equal(t, listed.Certs[0].Sha1Fingerprint, first)
	assert.Equal(t, x509.RSA, parseCertPEM(t, listed.Certs[0].Cert).PublicKeyAlgorithm)

	_, err = svc.Instances.AddEntraIdCertificate(project, name).Do()
	require.NoError(t, err)
	listed, err = svc.Instances.ListEntraIdCertificates(project, name).Do()
	require.NoError(t, err)
	require.Len(t, listed.Certs, 2)
	assert.Equal(t, first, listed.ActiveVersion)

	_, err = svc.Instances.RotateEntraIdCertificate(project, name, &sqladmin.InstancesRotateEntraIdCertificateRequest{}).Do()
	require.NoError(t, err)
	listed, err = svc.Instances.ListEntraIdCertificates(project, name).Do()
	require.NoError(t, err)
	assert.NotEqual(t, first, listed.ActiveVersion)

	pgOp, err := svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name: "entra-pg", Region: "us-central1", DatabaseVersion: "POSTGRES_15",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, "entra-pg").Do() })
	waitSQLOperationDone(t, svc, project, pgOp.Name)
	_, err = svc.Instances.AddEntraIdCertificate(project, "entra-pg").Do()
	requireSQLAPIError(t, err, 400)
}

// Moving an instance to the shared CA with instances.patch moves the
// serverCaCert that instances.get and connect.get serve to the region's
// subordinate CA.
func TestCloudSQL_PatchingServerCaModeMovesServerCaCert(t *testing.T) {
	t.Parallel()
	svc := sqlAdminService(t)
	const project, name = "test-project", "ssl-mode-patch-pg"
	inst := createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: "RootPassword-123!",
		Settings: &sqladmin.Settings{Tier: "db-f1-micro"},
	})
	require.NotNil(t, inst.ServerCaCert)

	_, err := svc.Instances.Patch(project, name, &sqladmin.DatabaseInstance{
		Settings: &sqladmin.Settings{IpConfiguration: &sqladmin.IpConfiguration{ServerCaMode: "GOOGLE_MANAGED_CAS_CA"}},
	}).Do()
	require.NoError(t, err)

	listed, err := svc.Instances.ListServerCertificates(project, name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, listed.CaCerts)
	got, err := svc.Instances.Get(project, name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.ServerCaCert)
	assert.NotEqual(t, inst.ServerCaCert.Sha1Fingerprint, got.ServerCaCert.Sha1Fingerprint)
	assert.Equal(t, listed.CaCerts[0].Sha1Fingerprint, got.ServerCaCert.Sha1Fingerprint)
	settings, err := svc.Connect.Get(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, got.ServerCaCert.Sha1Fingerprint, settings.ServerCaCert.Sha1Fingerprint)
}
