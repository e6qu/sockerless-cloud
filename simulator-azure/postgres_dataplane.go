package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
	"github.com/e6qu/sockerless-cloud/sim/simkms"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// The Azure Database for PostgreSQL flexible server data plane: a real
// PostgreSQL engine per server. The ARM contract carries only
// fullyQualifiedDomainName, so the simulator owns a loopback listener at port
// 5432 per server and registers the name against it in its DNS front
// (SIM_AZURE_DNS_LISTEN_ADDR). The first connection starts the engine, a
// container whose data is the volume sockerless-azurepg-<rg>-<name>; the front
// proxy terminates TLS and authenticates, then relays.
//
// require_secure_transport defaults ON, as in Azure, so a plaintext client is
// refused with SQLSTATE 28000 unless the configuration says OFF. The
// administrator credential is sealed at rest under service-managed key
// material, and administratorLoginPassword is never echoed.
//
// A host without per-server loopback addresses on port 5432 (macOS refuses
// loopback aliases without root) keeps servers API-only and says so on
// stderr.

// The simulator's analogue of Azure's default service-managed data
// encryption; customer-managed Key Vault keys are not modeled.
type pgDataPlaneKeyRecord struct {
	Key []byte `json:"key"`
}

// administratorLoginPassword is write-only on the wire, so it lives here
// rather than in the stored properties.
type pgServerCredential struct {
	Sealed []byte `json:"sealed"`
}

var (
	pgDataPlaneKeys     sim.Store[pgDataPlaneKeyRecord]
	pgServerCredentials sim.Store[pgServerCredential]
)

const pgDataPlaneKeyRow = "service-managed"

func azurePGCredentialKey() ([]byte, error) {
	key, err := simkms.EnsureKey(pgDataPlaneKeys, pgDataPlaneKeyRow,
		func(record pgDataPlaneKeyRecord) []byte { return record.Key },
		func(record *pgDataPlaneKeyRecord, key []byte) { record.Key = key })
	if err != nil {
		return nil, fmt.Errorf("generate flexible-server credential key: %w", err)
	}
	return key, nil
}

// The sealed blob is nonce || ciphertext.
func azurePGSealSecret(plaintext string) ([]byte, error) {
	key, err := azurePGCredentialKey()
	if err != nil {
		return nil, err
	}
	return simkms.Seal(key, []byte(plaintext), nil)
}

func azurePGOpenSecret(sealed []byte) (string, error) {
	key, err := azurePGCredentialKey()
	if err != nil {
		return "", err
	}
	plaintext, err := simkms.Open(key, sealed, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

const azurePGEngineContainerLabel = "sockerless-azurepg-server"

var azurePGServerCertificate = dbengine.SelfSignedCertificate("Azure Database for PostgreSQL simulator")

// Lowercase resource group and server name, which Azure treats
// case-insensitively.
func azurePGServerKey(rg, name string) string {
	return strings.ToLower(rg) + "/" + strings.ToLower(name)
}

func azurePGServerVolume(rg, name string) string {
	return "sockerless-azurepg-" + strings.ToLower(rg) + "-" + strings.ToLower(name)
}

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
		Engine:  dbengine.Postgres16.WithImage("mirror.gcr.io/library/postgres:16-alpine"),
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
		// The administrator is the only ARM-managed login.
		Authenticate: func(user, password string, _ bool) bool {
			login, stored, err := azurePGAdminCredential(sub, rg, name)
			return err == nil && user == login && subtle.ConstantTimeCompare([]byte(password), []byte(stored)) == 1
		},
	}
	return plane
}

// On a control-plane restart it re-adopts the engine an earlier process left.
// It returns false with the reason when the host cannot provide the listener.
func azurePGInstallDataPlane(sub, rg, name string, adopt bool) (bool, error) {
	if sim.RequireContainerRuntime("the Azure Database for PostgreSQL data plane") != nil {
		return false, nil
	}
	key := azurePGServerKey(rg, name)
	if _, exists := azurePGDataPlanes.Load(key); exists {
		// A PUT on an existing server keeps its listener.
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

// Otherwise say on stderr that the server stays API-only.
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

// Azure's default is ON.
func azurePGRequireSecureTransport(sub, rg, name string) bool {
	c, ok := pgConfigurations.Get(pgConfigKey(sub, rg, name, "require_secure_transport"))
	if !ok || c.Properties == nil {
		return true
	}
	value, _ := c.Properties["value"].(string)
	return !strings.EqualFold(value, "off")
}

// Runs at engine readiness — first boot, restart, a restored clone's first
// boot — and after control-plane mutations while the engine is up, so the API
// and the engine agree.
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

// Existence-checked, so reconciliation is idempotent.
func azurePGEngineEnsureDatabase(plane *azurePGDataPlane, login, name string) error {
	check := "SELECT 1 FROM pg_database WHERE datname = " + dbengine.QuoteLiteral(name)
	create := "CREATE DATABASE " + dbengine.QuoteIdentifier(name)
	script := fmt.Sprintf(`[ -n "$(psql -U %s -d postgres -tAc %s)" ] || psql -v ON_ERROR_STOP=1 -U %s -d postgres -c %s`,
		dbengine.ShellQuote(login), dbengine.ShellQuote(check), dbengine.ShellQuote(login), dbengine.ShellQuote(create))
	return plane.Exec([]string{"/bin/sh", "-c", script})
}

// A stopped engine needs nothing: readiness reconciles before serving.
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

// A stopped engine needs nothing: it initializes from the sealed credential at
// its next start.
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

// deleteVolume removes the data volume when the server is deleted.
func azurePGStopDataPlane(rg, name string, deleteVolume bool) {
	if value, ok := azurePGDataPlanes.LoadAndDelete(azurePGServerKey(rg, name)); ok {
		if plane, isPlane := value.(*azurePGDataPlane); isPlane {
			if err := plane.Close(); err != nil {
				log.Printf("Azure Database for PostgreSQL %s/%s: stop database engine: %v", rg, name, err)
			}
		}
	}
	if deleteVolume {
		dbengine.ReleaseLoopback(azurePGServerKey(rg, name))
		sim.RemoveVolumeSettled(azurePGServerVolume(rg, name), "azurepg")
	}
}
