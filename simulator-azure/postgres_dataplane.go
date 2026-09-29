package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// The Azure Database for PostgreSQL flexible server data plane.
//
// A flexible server is a real database engine. The simulator owns a loopback
// listener at PostgreSQL's port for each server — the ARM contract carries no
// address, only the fullyQualifiedDomainName, so the slice registers that
// name against the listener's address in the simulator's DNS front
// (SIM_AZURE_DNS_LISTEN_ADDR), exactly the coordinate a client resolves on
// Azure. The first client connection starts the engine — a real PostgreSQL
// container whose data directory is the named volume
// sockerless-azurepg-<rg>-<name> — and the front proxy owns TLS and
// authentication, then relays bytes.
//
// Azure's server-parameter defaults hold on the wire: require_secure_transport
// is ON, so a client that opens without TLS is refused with SQLSTATE 28000
// unless the server's configurations store holds require_secure_transport=OFF.
// The administrator credential is sealed at rest under service-managed key
// material — the simulator's analogue of Azure's default data encryption —
// and the ARM surface never echoes administratorLoginPassword back.
//
// A host that cannot provide port 5432 on a per-server loopback address
// (macOS refuses loopback aliases without root) leaves the server exactly as
// modeled as the whole slice was before the data plane existed, and says so
// on stderr. Linux provides it natively; CI exercises the real path.

// pgDataPlaneKeyRecord holds the service-managed key material that seals
// flexible-server administrator credentials — the simulator's analogue of
// Azure's default service-managed data encryption (a customer's Key Vault
// key is a different, optional mode this slice does not model).
type pgDataPlaneKeyRecord struct {
	Key []byte `json:"key"`
}

// pgServerCredential is a server's sealed administratorLoginPassword. The
// field is write-only on the ARM wire, so it lives here rather than in the
// stored server properties.
type pgServerCredential struct {
	Sealed []byte `json:"sealed"`
}

var (
	pgDataPlaneKeys     sim.Store[pgDataPlaneKeyRecord]
	pgServerCredentials sim.Store[pgServerCredential]
)

const pgDataPlaneKeyRow = "service-managed"

var azurePGSealMu sync.Mutex

// azurePGCredentialAEAD returns AES-256-GCM under the service-managed sealing
// key, generating the key on first use.
func azurePGCredentialAEAD() (cipher.AEAD, error) {
	azurePGSealMu.Lock()
	record, ok := pgDataPlaneKeys.Get(pgDataPlaneKeyRow)
	if !ok {
		material := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, material); err != nil {
			azurePGSealMu.Unlock()
			return nil, fmt.Errorf("generate flexible-server credential key: %w", err)
		}
		record = pgDataPlaneKeyRecord{Key: material}
		pgDataPlaneKeys.Put(pgDataPlaneKeyRow, record)
	}
	azurePGSealMu.Unlock()
	block, err := aes.NewCipher(record.Key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// azurePGSealSecret encrypts a credential under the service-managed key; the
// sealed blob is nonce || ciphertext.
func azurePGSealSecret(plaintext string) ([]byte, error) {
	aead, err := azurePGCredentialAEAD()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	return append(nonce, aead.Seal(nil, nonce, []byte(plaintext), nil)...), nil
}

// azurePGOpenSecret decrypts a credential sealed by azurePGSealSecret.
func azurePGOpenSecret(sealed []byte) (string, error) {
	aead, err := azurePGCredentialAEAD()
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize() {
		return "", fmt.Errorf("sealed credential is truncated")
	}
	plaintext, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

const azurePGEngineContainerLabel = "sockerless-azurepg-server"

var azurePGServerCertificate = dbengine.SelfSignedCertificate("Azure Database for PostgreSQL simulator")

// azurePGServerKey is the data-plane identity: resource group + server name,
// lowercase — ARM resource-group and server names are case-insensitive.
func azurePGServerKey(rg, name string) string {
	return strings.ToLower(rg) + "/" + strings.ToLower(name)
}

func azurePGServerVolume(rg, name string) string {
	return "sockerless-azurepg-" + strings.ToLower(rg) + "-" + strings.ToLower(name)
}

// pgParseServerResourceID splits a flexible-server ARM resource ID into its
// subscription, resource group and server name.
func pgParseServerResourceID(resourceID string) (sub, rg, name string, ok bool) {
	parts := strings.Split(strings.Trim(resourceID, "/"), "/")
	if len(parts) != 8 ||
		!strings.EqualFold(parts[0], "subscriptions") ||
		!strings.EqualFold(parts[2], "resourceGroups") ||
		!strings.EqualFold(parts[4], "providers") ||
		!strings.EqualFold(parts[5], "Microsoft.DBforPostgreSQL") ||
		!strings.EqualFold(parts[6], "flexibleServers") {
		return "", "", "", false
	}
	return parts[1], parts[3], parts[7], true
}

// azurePGDataPlane is a flexible server's endpoint and engine.
type azurePGDataPlane struct {
	*dbengine.Instance
	sub  string
	rg   string
	name string
}

var azurePGDataPlanes sync.Map // rg/name (lowercase) -> *azurePGDataPlane

func azurePGNewDataPlane(sub, rg, name string) *azurePGDataPlane {
	plane := &azurePGDataPlane{sub: sub, rg: rg, name: name}
	plane.Instance = &dbengine.Instance{
		Name:    "Azure Database for PostgreSQL " + rg + "/" + name,
		Engine:  dbengine.Postgres16,
		Volume:  azurePGServerVolume(rg, name),
		Labels:  map[string]string{azurePGEngineContainerLabel: azurePGServerKey(rg, name)},
		Sandbox: SandboxACA,
		Platform: func(ctx context.Context, image string) (string, error) {
			return workload.LocalImagePlatform(ctx, image, "")
		},
		Environment: func() (map[string]string, error) {
			login, password, err := azurePGAdminCredential(sub, rg, name)
			if err != nil {
				return nil, err
			}
			return dbengine.PostgresEnvironment(login, password, "postgres"), nil
		},
		Ready: func() error {
			if err := azurePGReconcileEngineState(plane); err != nil {
				return fmt.Errorf("reconcile declared databases into the engine: %w", err)
			}
			return nil
		},
		Certificate: azurePGServerCertificate,
		RefusePlaintext: func() (string, bool) {
			return "connections require SSL; set require_secure_transport to OFF to allow plaintext",
				azurePGRequireSecureTransport(sub, rg, name)
		},
		// The administrator is the only ARM-managed login on this surface.
		Authenticate: func(user, password string, _ bool) bool {
			login, stored, err := azurePGAdminCredential(sub, rg, name)
			return err == nil && user == login && subtle.ConstantTimeCompare([]byte(password), []byte(stored)) == 1
		},
	}
	return plane
}

// azurePGInstallDataPlane binds the server's loopback listener at
// PostgreSQL's port, registers the server's fullyQualifiedDomainName against
// the listener's address in the DNS front, and — on a control-plane restart —
// re-adopts the engine an earlier process left before serving. It returns
// false — with the reason — when this host cannot provide the listener; the
// caller keeps the server modeled and says so.
func azurePGInstallDataPlane(sub, rg, name string, adopt bool) (bool, error) {
	if sim.RequireContainerRuntime("the Azure Database for PostgreSQL data plane") != nil {
		return false, nil
	}
	key := azurePGServerKey(rg, name)
	if _, exists := azurePGDataPlanes.Load(key); exists {
		// A PUT on an existing server keeps its listener; ARM's PUT is
		// create-or-update and the address is already served.
		return true, nil
	}
	plane := azurePGNewDataPlane(sub, rg, name)
	listener, err := dbengine.ListenLoopback(key, plane.Engine.Port)
	if err != nil {
		return false, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return false, fmt.Errorf("listener returned address type %T", listener.Addr())
	}
	if adopt {
		if err := plane.Adopt(); err != nil {
			_ = listener.Close()
			return false, fmt.Errorf("re-adopt engine: %w", err)
		}
	}
	azurePGDataPlanes.Store(key, plane)
	if s, found := pgServers.Get(pgServerID(sub, rg, name)); found {
		if fqdn, isString := s.Properties["fullyQualifiedDomainName"].(string); isString && fqdn != "" {
			RegisterAzureDNSName(fqdn, address.IP.String())
		}
	}
	plane.Serve(listener)
	return true, nil
}

// azurePGInstallOrExplain installs a server's data plane when its
// administrator credential exists and the host is capable, and otherwise
// says loudly on stderr that the server stays on the modeled tier.
func azurePGInstallOrExplain(sub, rg, name string) {
	key := azurePGServerKey(rg, name)
	if credential, ok := pgServerCredentials.Get(key); !ok || len(credential.Sealed) == 0 {
		fmt.Fprintf(os.Stderr, "[sim-azurepg] server %s is modeled without a data plane: the request carried no administratorLoginPassword\n", key)
		return
	}
	installed, err := azurePGInstallDataPlane(sub, rg, name, false)
	if installed {
		return
	}
	reason := "this simulator was started API-only"
	if err != nil {
		reason = err.Error()
	} else if sim.RequireContainerRuntime("the Azure Database for PostgreSQL data plane") == nil {
		reason = "the host offers no loopback address at PostgreSQL's port"
	}
	fmt.Fprintf(os.Stderr, "[sim-azurepg] server %s is modeled without a data plane: %s\n", key, reason)
}

// azurePGRecoverDataPlanes rebinds every credentialed server's listener after
// a control-plane restart, re-registers its DNS name, and re-adopts engine
// containers an earlier process left running.
func azurePGRecoverDataPlanes() error {
	for _, s := range pgServers.List() {
		sub, rg, name, ok := pgParseServerResourceID(s.ID)
		if !ok {
			continue
		}
		if credential, found := pgServerCredentials.Get(azurePGServerKey(rg, name)); !found || len(credential.Sealed) == 0 {
			// A server without a credential never had a data plane.
			continue
		}
		if _, err := azurePGInstallDataPlane(sub, rg, name, true); err != nil {
			return fmt.Errorf("rebind flexible server %s/%s: %w", rg, name, err)
		}
	}
	return nil
}

// azurePGAdminCredential returns the server's administrator login and its
// password — the administratorLoginPassword the ARM request carried, sealed
// at rest.
func azurePGAdminCredential(sub, rg, name string) (string, string, error) {
	s, ok := pgServers.Get(pgServerID(sub, rg, name))
	if !ok {
		return "", "", fmt.Errorf("flexible server %s/%s does not exist", rg, name)
	}
	login, _ := s.Properties["administratorLogin"].(string)
	if login == "" {
		return "", "", fmt.Errorf("flexible server %s/%s declares no administratorLogin", rg, name)
	}
	credential, ok := pgServerCredentials.Get(azurePGServerKey(rg, name))
	if !ok || len(credential.Sealed) == 0 {
		return "", "", fmt.Errorf("flexible server %s/%s has no administrator credential: create or update it with administratorLoginPassword", rg, name)
	}
	password, err := azurePGOpenSecret(credential.Sealed)
	if err != nil {
		return "", "", fmt.Errorf("open administrator credential: %w", err)
	}
	return login, password, nil
}

// azurePGRequireSecureTransport reads the server's require_secure_transport
// parameter from the configurations store; Azure's default is ON.
func azurePGRequireSecureTransport(sub, rg, name string) bool {
	c, ok := pgConfigurations.Get(pgConfigKey(sub, rg, name, "require_secure_transport"))
	if !ok || c.Properties == nil {
		return true
	}
	value, _ := c.Properties["value"].(string)
	return !strings.EqualFold(value, "off")
}

// azurePGReconcileEngineState creates, inside the running engine, every
// database the ARM control plane declares for the server. It runs at engine
// readiness — first boot, control-plane restart, and a restored clone's
// first boot — and after control-plane mutations while the engine is up, so
// the API and the engine never disagree.
func azurePGReconcileEngineState(plane *azurePGDataPlane) error {
	login, _, err := azurePGAdminCredential(plane.sub, plane.rg, plane.name)
	if err != nil {
		return err
	}
	prefix := pgServerID(plane.sub, plane.rg, plane.name) + "/databases/"
	for _, d := range pgDatabases.List() {
		if !strings.HasPrefix(d.ID, prefix) {
			continue
		}
		if err := azurePGEngineEnsureDatabase(plane, login, d.Name); err != nil {
			return fmt.Errorf("ensure database %s: %w", d.Name, err)
		}
	}
	return nil
}

// azurePGEngineEnsureDatabase makes the engine hold the declared database,
// existence-checked so reconciliation is idempotent.
func azurePGEngineEnsureDatabase(plane *azurePGDataPlane, login, name string) error {
	check := "SELECT 1 FROM pg_database WHERE datname = " + dbengine.QuoteLiteral(name)
	create := "CREATE DATABASE " + dbengine.QuoteIdentifier(name)
	script := fmt.Sprintf(`[ -n "$(psql -U %s -d postgres -tAc %s)" ] || psql -v ON_ERROR_STOP=1 -U %s -d postgres -c %s`,
		dbengine.ShellQuote(login), dbengine.ShellQuote(check), dbengine.ShellQuote(login), dbengine.ShellQuote(create))
	return plane.Exec([]string{"/bin/sh", "-c", script})
}

// azurePGEnsureDatabaseIfRunning applies a databases PUT to a running engine
// immediately. An engine that is not running needs nothing: readiness
// reconciles the full declared state before serving any client.
func azurePGEnsureDatabaseIfRunning(sub, rg, server, database string) error {
	plane, running := azurePGRunningDataPlane(rg, server)
	if !running {
		return nil
	}
	login, _, err := azurePGAdminCredential(sub, rg, server)
	if err != nil {
		return err
	}
	return azurePGEngineEnsureDatabase(plane, login, database)
}

// azurePGDropDatabaseIfRunning removes a deleted database from a running
// engine.
func azurePGDropDatabaseIfRunning(sub, rg, server, database string) error {
	plane, running := azurePGRunningDataPlane(rg, server)
	if !running {
		return nil
	}
	login, _, err := azurePGAdminCredential(sub, rg, server)
	if err != nil {
		return err
	}
	statement := "DROP DATABASE IF EXISTS " + dbengine.QuoteIdentifier(database) + " WITH (FORCE)"
	return plane.Exec([]string{"psql", "-v", "ON_ERROR_STOP=1", "-U", login, "-d", "postgres", "-c", statement})
}

// azurePGRotateAdminPasswordIfRunning applies a rotated
// administratorLoginPassword to a running engine. An engine that is not
// running needs nothing: it is initialised from the sealed credential at its
// next start.
func azurePGRotateAdminPasswordIfRunning(sub, rg, server, password string) error {
	plane, running := azurePGRunningDataPlane(rg, server)
	if !running {
		return nil
	}
	s, ok := pgServers.Get(pgServerID(sub, rg, server))
	if !ok {
		return fmt.Errorf("flexible server %s/%s does not exist", rg, server)
	}
	login, _ := s.Properties["administratorLogin"].(string)
	if login == "" {
		return fmt.Errorf("flexible server %s/%s declares no administratorLogin", rg, server)
	}
	statement := "ALTER ROLE " + dbengine.QuoteIdentifier(login) + " WITH LOGIN PASSWORD " + dbengine.QuoteLiteral(password)
	return plane.Exec([]string{"psql", "-v", "ON_ERROR_STOP=1", "-U", login, "-d", "postgres", "-c", statement})
}

func azurePGRunningDataPlane(rg, name string) (*azurePGDataPlane, bool) {
	value, ok := azurePGDataPlanes.Load(azurePGServerKey(rg, name))
	if !ok {
		return nil, false
	}
	plane, ok := value.(*azurePGDataPlane)
	if !ok || !plane.Running() {
		return nil, false
	}
	return plane, true
}

// azurePGStopDataPlane closes the server's listener, stops its engine, and —
// when the server is being deleted — removes its data volume.
func azurePGStopDataPlane(rg, name string, deleteVolume bool) {
	if value, ok := azurePGDataPlanes.LoadAndDelete(azurePGServerKey(rg, name)); ok {
		if plane, isPlane := value.(*azurePGDataPlane); isPlane {
			if err := plane.Close(); err != nil {
				log.Printf("Azure Database for PostgreSQL %s/%s: stop database engine: %v", rg, name, err)
			}
		}
	}
	if deleteVolume {
		sim.RemoveVolumeSettled(azurePGServerVolume(rg, name), "azurepg")
	}
}
