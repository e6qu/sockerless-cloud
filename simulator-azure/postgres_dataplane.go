package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	dockerclient "github.com/moby/moby/client"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
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

// azurePGKeyMaterial returns the service-managed sealing key, generating it
// on first use.
func azurePGKeyMaterial() ([]byte, error) {
	azurePGSealMu.Lock()
	defer azurePGSealMu.Unlock()
	record, ok := pgDataPlaneKeys.Get(pgDataPlaneKeyRow)
	if !ok {
		material := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, material); err != nil {
			return nil, fmt.Errorf("generate flexible-server credential key: %w", err)
		}
		record = pgDataPlaneKeyRecord{Key: material}
		pgDataPlaneKeys.Put(pgDataPlaneKeyRow, record)
	}
	return record.Key, nil
}

// azurePGSealSecret encrypts a credential under the service-managed key with
// AES-256-GCM; the sealed blob is nonce || ciphertext.
func azurePGSealSecret(plaintext string) ([]byte, error) {
	key, err := azurePGKeyMaterial()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
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
	key, err := azurePGKeyMaterial()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
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

const (
	azurePGPort                        = 5432
	azurePGEngineImage                 = "public.ecr.aws/docker/library/postgres:16-alpine"
	azurePGEngineInitializationBudget  = 10 * time.Minute
	azurePGEngineLivenessCheckInterval = 2 * time.Second
	azurePGEngineContainerLabel        = "sockerless-azurepg-server"
)

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

type azurePGDataPlaneRuntime struct {
	mu       sync.RWMutex
	sub      string
	rg       string
	name     string
	listener net.Listener
	backend  string
	handle   *sim.ContainerHandle
	start    *sync.Once
	startErr error
}

func (runtime *azurePGDataPlaneRuntime) snapshot() (string, *sim.ContainerHandle) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.backend, runtime.handle
}

func (runtime *azurePGDataPlaneRuntime) update(backend string, handle *sim.ContainerHandle) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.backend = backend
	runtime.handle = handle
}

var azurePGDataPlanes sync.Map // rg/name (lowercase) -> *azurePGDataPlaneRuntime

var (
	azurePGTLSOnce sync.Once
	azurePGTLSCert tls.Certificate
	azurePGTLSErr  error
)

// azurePGInstallDataPlane binds the server's loopback listener at
// PostgreSQL's port and registers the server's fullyQualifiedDomainName
// against the listener's address in the DNS front. It returns false — with
// the reason — when this host cannot provide the listener; the caller keeps
// the server modeled and says so.
func azurePGInstallDataPlane(sub, rg, name string) (bool, error) {
	if sim.RequireContainerRuntime("the Azure Database for PostgreSQL data plane") != nil {
		return false, nil
	}
	key := azurePGServerKey(rg, name)
	if _, exists := azurePGDataPlanes.Load(key); exists {
		// A PUT on an existing server keeps its listener; ARM's PUT is
		// create-or-update and the address is already served.
		return true, nil
	}
	listener, err := azurePGListenOnLoopback(key, azurePGPort)
	if err != nil {
		return false, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return false, fmt.Errorf("listener returned address type %T", listener.Addr())
	}
	runtime := &azurePGDataPlaneRuntime{
		sub:      sub,
		rg:       rg,
		name:     name,
		listener: listener,
		start:    &sync.Once{},
	}
	azurePGDataPlanes.Store(key, runtime)
	if s, found := pgServers.Get(pgServerID(sub, rg, name)); found {
		if fqdn, isString := s.Properties["fullyQualifiedDomainName"].(string); isString && fqdn != "" {
			RegisterAzureDNSName(fqdn, address.IP.String())
		}
	}
	go runtime.serve()
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
	installed, err := azurePGInstallDataPlane(sub, rg, name)
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

// azurePGListenOnLoopback binds PostgreSQL's port on a loopback address
// derived from the server identity. The ARM contract carries no port, so the
// port is part of the contract. Hosts that refuse loopback aliases get one
// last chance at 127.0.0.1 before the server stays modeled.
func azurePGListenOnLoopback(identifier string, port int) (net.Listener, error) {
	var seed byte = 2
	for i := 0; i < len(identifier); i++ {
		seed += identifier[i]
	}
	var lastErr error
	for offset := 0; offset < 253; offset++ {
		octet := 2 + (int(seed)+offset)%253
		listener, err := net.Listen("tcp", net.JoinHostPort(fmt.Sprintf("127.0.0.%d", octet), strconv.Itoa(port)))
		if err == nil {
			return listener, nil
		}
		lastErr = err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return listener, nil
	}
	return nil, fmt.Errorf("no loopback address offers port %d (last error: %v)", port, lastErr)
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
		installed, err := azurePGInstallDataPlane(sub, rg, name)
		if err != nil {
			return fmt.Errorf("rebind flexible server %s/%s: %w", rg, name, err)
		}
		if !installed {
			continue
		}
		if err := azurePGAdoptEngine(rg, name); err != nil {
			return fmt.Errorf("re-adopt flexible server %s/%s engine: %w", rg, name, err)
		}
	}
	return nil
}

// azurePGAdoptEngine picks up the engine container an earlier control-plane
// process left for this server, if one exists.
func azurePGAdoptEngine(rg, name string) error {
	key := azurePGServerKey(rg, name)
	value, ok := azurePGDataPlanes.Load(key)
	if !ok {
		return nil
	}
	runtime, ok := value.(*azurePGDataPlaneRuntime)
	if !ok {
		return fmt.Errorf("data-plane runtime has an invalid type")
	}
	existing, err := sim.FindExistingContainers(map[string]string{azurePGEngineContainerLabel: key})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	if len(existing) != 1 {
		return fmt.Errorf("found %d database engine containers", len(existing))
	}
	backendPort := existing[0].PublishedPorts[azurePGPort]
	if backendPort == 0 {
		return fmt.Errorf("container %s has no published database port", existing[0].ID)
	}
	if !existing[0].Running {
		if err := sim.StartExistingContainer(existing[0].ID); err != nil {
			return fmt.Errorf("resume database engine container %s: %w", existing[0].ID, err)
		}
	}
	platform, err := localImagePlatform(context.Background(), azurePGEngineImage)
	if err != nil {
		return fmt.Errorf("resolve database engine platform: %w", err)
	}
	handle, err := sim.AdoptContainer(existing[0].ID, sim.ContainerConfig{Architecture: platform}, sim.NoopSink{})
	if err != nil {
		return err
	}
	runtime.update(net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle)
	return nil
}

func (runtime *azurePGDataPlaneRuntime) serve() {
	for {
		connection, err := runtime.listener.Accept()
		if err != nil {
			return
		}
		go runtime.serveConnection(connection)
	}
}

// ensureEngine brings the server's database engine up on first use and
// blocks until it accepts clients and the declared databases exist in it.
func (runtime *azurePGDataPlaneRuntime) ensureEngine() error {
	runtime.mu.RLock()
	start := runtime.start
	runtime.mu.RUnlock()
	start.Do(func() {
		if _, handle := runtime.snapshot(); handle != nil {
			runtime.startErr = runtime.awaitEngine()
			return
		}
		runtime.startErr = runtime.startEngine()
	})
	return runtime.startErr
}

func (runtime *azurePGDataPlaneRuntime) startEngine() error {
	login, password, err := azurePGAdminCredential(runtime.sub, runtime.rg, runtime.name)
	if err != nil {
		return err
	}
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("allocate database engine port: %w", err)
	}
	backendAddress, ok := backendListener.Addr().(*net.TCPAddr)
	if !ok {
		_ = backendListener.Close()
		return fmt.Errorf("database engine listener returned address type %T", backendListener.Addr())
	}
	backendPort := backendAddress.Port
	if err := backendListener.Close(); err != nil {
		return fmt.Errorf("release database engine port: %w", err)
	}
	platform, err := localImagePlatform(context.Background(), azurePGEngineImage)
	if err != nil {
		return fmt.Errorf("resolve database engine platform: %w", err)
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        azurePGEngineImage,
		Architecture: platform,
		Env: map[string]string{
			"POSTGRES_USER":     login,
			"POSTGRES_PASSWORD": password,
			"POSTGRES_DB":       "postgres",
			// The front proxy authenticates every client against the sealed
			// administrator credential the ARM control plane holds; the engine
			// trusts the proxy, which lets the proxy relay the client's own
			// startup without replaying the password.
			"POSTGRES_HOST_AUTH_METHOD": "trust",
		},
		PublishPorts: map[int]int{azurePGPort: backendPort},
		Binds:        []string{azurePGServerVolume(runtime.rg, runtime.name) + ":/var/lib/postgresql/data"},
		Labels:       map[string]string{azurePGEngineContainerLabel: azurePGServerKey(runtime.rg, runtime.name)},
		Sandbox:      sim.SandboxACA,
	}, sim.NoopSink{})
	if err != nil {
		return fmt.Errorf("start PostgreSQL database engine: %w", err)
	}
	runtime.update(net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle)
	return runtime.awaitEngine()
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

// awaitEngine blocks until the engine accepts client connections, then
// reconciles the databases the ARM control plane declares into it, so the
// engine serves real databases.
func (runtime *azurePGDataPlaneRuntime) awaitEngine() error {
	backend, handle := runtime.snapshot()
	deadline := time.Now().Add(azurePGEngineInitializationBudget)
	nextLivenessCheck := time.Now().Add(azurePGEngineLivenessCheckInterval)
	for !azurePGEngineReady(backend) {
		now := time.Now()
		if !now.Before(nextLivenessCheck) {
			if err := azurePGEngineContainerRunning(handle.ContainerID); err != nil {
				handle.Cancel()
				return fmt.Errorf("PostgreSQL database engine stopped before accepting connections: %w", err)
			}
			nextLivenessCheck = now.Add(azurePGEngineLivenessCheckInterval)
		}
		if !now.Before(deadline) {
			handle.Cancel()
			return fmt.Errorf("PostgreSQL database engine did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := azurePGReconcileEngineState(runtime); err != nil {
		return fmt.Errorf("reconcile declared databases into the engine: %w", err)
	}
	return nil
}

func azurePGEngineContainerRunning(containerID string) error {
	inspectContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := sim.DockerClient().ContainerInspect(inspectContext, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return fmt.Errorf("container %s exited", containerID)
	}
	return nil
}

func azurePGEngineReady(address string) bool {
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
	return azurePGAcceptsConnections(connection)
}

// azurePGAcceptsConnections classifies a startup exchange the way libpq's
// PQping does: a server that answers is up, except while it still answers
// every client "the database system is starting up" (SQLSTATE 57P03).
func azurePGAcceptsConnections(connection net.Conn) bool {
	parameters := []byte("user\x00postgres\x00database\x00postgres\x00\x00")
	packet := make([]byte, 8+len(parameters))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(packet)))
	binary.BigEndian.PutUint32(packet[4:8], 196608)
	copy(packet[8:], parameters)
	if _, err := connection.Write(packet); err != nil {
		return false
	}
	header := make([]byte, 5)
	if _, err := io.ReadFull(connection, header); err != nil {
		return false
	}
	switch header[0] {
	case 'R':
		return true
	case 'E':
		return azurePGErrorSQLState(connection, header) != "57P03"
	default:
		return false
	}
}

func azurePGErrorSQLState(connection net.Conn, header []byte) string {
	length := int(binary.BigEndian.Uint32(header[1:5]))
	if length < 4 || length > 1<<16 {
		return ""
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(connection, body); err != nil {
		return ""
	}
	for _, field := range strings.Split(string(body), "\x00") {
		if strings.HasPrefix(field, "C") {
			return field[1:]
		}
	}
	return ""
}

// azurePGReconcileEngineState creates, inside the running engine, every
// database the ARM control plane declares for the server. It runs at engine
// readiness — first boot, control-plane restart, and a restored clone's
// first boot — and after control-plane mutations while the engine is up, so
// the API and the engine never disagree.
func azurePGReconcileEngineState(runtime *azurePGDataPlaneRuntime) error {
	login, _, err := azurePGAdminCredential(runtime.sub, runtime.rg, runtime.name)
	if err != nil {
		return err
	}
	prefix := pgServerID(runtime.sub, runtime.rg, runtime.name) + "/databases/"
	for _, d := range pgDatabases.List() {
		if !strings.HasPrefix(d.ID, prefix) {
			continue
		}
		if err := azurePGEngineEnsureDatabase(runtime, login, d.Name); err != nil {
			return fmt.Errorf("ensure database %s: %w", d.Name, err)
		}
	}
	return nil
}

// azurePGEngineEnsureDatabase makes the engine hold the declared database,
// existence-checked so reconciliation is idempotent.
func azurePGEngineEnsureDatabase(runtime *azurePGDataPlaneRuntime, login, name string) error {
	check := "SELECT 1 FROM pg_database WHERE datname = " + azurePGQuoteLiteral(name)
	create := "CREATE DATABASE " + azurePGQuoteIdentifier(name)
	script := fmt.Sprintf(`[ -n "$(psql -U %s -d postgres -tAc %s)" ] || psql -v ON_ERROR_STOP=1 -U %s -d postgres -c %s`,
		azurePGShellQuote(login), azurePGShellQuote(check), azurePGShellQuote(login), azurePGShellQuote(create))
	return azurePGEngineExec(runtime, []string{"/bin/sh", "-c", script})
}

// azurePGEnsureDatabaseIfRunning applies a databases PUT to a running engine
// immediately. An engine that is not running needs nothing: readiness
// reconciles the full declared state before serving any client.
func azurePGEnsureDatabaseIfRunning(sub, rg, server, database string) error {
	runtime, running := azurePGRunningRuntime(rg, server)
	if !running {
		return nil
	}
	login, _, err := azurePGAdminCredential(sub, rg, server)
	if err != nil {
		return err
	}
	return azurePGEngineEnsureDatabase(runtime, login, database)
}

// azurePGDropDatabaseIfRunning removes a deleted database from a running
// engine.
func azurePGDropDatabaseIfRunning(sub, rg, server, database string) error {
	runtime, running := azurePGRunningRuntime(rg, server)
	if !running {
		return nil
	}
	login, _, err := azurePGAdminCredential(sub, rg, server)
	if err != nil {
		return err
	}
	statement := "DROP DATABASE IF EXISTS " + azurePGQuoteIdentifier(database) + " WITH (FORCE)"
	return azurePGEngineExec(runtime, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", login, "-d", "postgres", "-c", statement})
}

// azurePGRotateAdminPasswordIfRunning applies a rotated
// administratorLoginPassword to a running engine. An engine that is not
// running needs nothing: it is initialised from the sealed credential at its
// next start.
func azurePGRotateAdminPasswordIfRunning(sub, rg, server, password string) error {
	runtime, running := azurePGRunningRuntime(rg, server)
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
	statement := "ALTER ROLE " + azurePGQuoteIdentifier(login) + " WITH LOGIN PASSWORD " + azurePGQuoteLiteral(password)
	return azurePGEngineExec(runtime, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", login, "-d", "postgres", "-c", statement})
}

// azurePGRunningRuntime returns the server's data-plane runtime only when
// its engine container is up.
func azurePGRunningRuntime(rg, name string) (*azurePGDataPlaneRuntime, bool) {
	value, ok := azurePGDataPlanes.Load(azurePGServerKey(rg, name))
	if !ok {
		return nil, false
	}
	runtime, ok := value.(*azurePGDataPlaneRuntime)
	if !ok {
		return nil, false
	}
	if _, handle := runtime.snapshot(); handle == nil {
		return nil, false
	}
	return runtime, true
}

func azurePGEngineExec(runtime *azurePGDataPlaneRuntime, command []string) error {
	_, handle := runtime.snapshot()
	if handle == nil {
		return fmt.Errorf("database engine is not running")
	}
	execContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := sim.DockerClient().ExecCreate(execContext, handle.ContainerID, dockerclient.ExecCreateOptions{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("create engine command: %w", err)
	}
	attached, err := sim.DockerClient().ExecAttach(execContext, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attach engine command: %w", err)
	}
	output, _ := io.ReadAll(attached.Reader)
	attached.Close()
	inspected, err := sim.DockerClient().ExecInspect(execContext, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return fmt.Errorf("engine command exited %d: %s", inspected.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}

func azurePGQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func azurePGQuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func azurePGShellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'\''`) + `'`
}

func (runtime *azurePGDataPlaneRuntime) serveConnection(client net.Conn) {
	defer client.Close()
	if err := runtime.ensureEngine(); err != nil {
		log.Printf("Azure Database for PostgreSQL %s/%s data plane: %v", runtime.rg, runtime.name, err)
		return
	}
	startup, secure, err := runtime.readStartup(client)
	if err != nil {
		log.Printf("Azure Database for PostgreSQL %s/%s startup: %v", runtime.rg, runtime.name, err)
		return
	}
	if secure != nil {
		client = secure
		defer client.Close()
	}
	user := azurePGStartupParameter(startup, "user")
	if err := runtime.authenticateClient(client, user); err != nil {
		azurePGWriteErrorResponse(client, "FATAL", "28P01", err.Error())
		return
	}
	backendAddress, _ := runtime.snapshot()
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		log.Printf("Azure Database for PostgreSQL %s/%s backend dial: %v", runtime.rg, runtime.name, err)
		return
	}
	defer backend.Close()
	// The engine trusts the proxy, so relaying the client's own startup keeps
	// the session's identity and parameters verbatim.
	if _, err := backend.Write(startup); err != nil {
		log.Printf("Azure Database for PostgreSQL %s/%s backend startup: %v", runtime.rg, runtime.name, err)
		return
	}
	azurePGRelay(client, backend)
}

// readStartup reads the client's opening exchange. An SSLRequest is answered
// with the server certificate and the startup is read inside TLS. A plain
// startup is refused while the server's require_secure_transport parameter
// holds its Azure default (ON): Azure Database for PostgreSQL refuses
// non-TLS clients with SQLSTATE 28000 unless an operator turns the parameter
// OFF through the configurations API.
func (runtime *azurePGDataPlaneRuntime) readStartup(connection net.Conn) ([]byte, net.Conn, error) {
	packet, err := azurePGReadPacket(connection)
	if err != nil {
		return nil, nil, err
	}
	if len(packet) == 8 && binary.BigEndian.Uint32(packet[4:]) == 80877103 {
		if _, err := connection.Write([]byte{'S'}); err != nil {
			return nil, nil, err
		}
		certificate, err := azurePGServerCertificate()
		if err != nil {
			return nil, nil, err
		}
		secure := tls.Server(connection, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := secure.Handshake(); err != nil {
			return nil, nil, err
		}
		packet, err = azurePGReadPacket(secure)
		return packet, secure, err
	}
	if azurePGRequireSecureTransport(runtime.sub, runtime.rg, runtime.name) {
		azurePGWriteErrorResponse(connection, "FATAL", "28000",
			"connections require SSL; set require_secure_transport to OFF to allow plaintext")
		return nil, nil, fmt.Errorf("refused a plaintext client: require_secure_transport is ON")
	}
	return packet, nil, nil
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

// authenticateClient runs a cleartext-password exchange and checks the
// password against the sealed administrator credential the ARM control plane
// holds — the administrator is the only ARM-managed login on this surface.
func (runtime *azurePGDataPlaneRuntime) authenticateClient(connection net.Conn, user string) error {
	request := make([]byte, 9)
	request[0] = 'R'
	binary.BigEndian.PutUint32(request[1:5], 8)
	binary.BigEndian.PutUint32(request[5:9], 3)
	if _, err := connection.Write(request); err != nil {
		return err
	}
	header := make([]byte, 5)
	if _, err := io.ReadFull(connection, header); err != nil {
		return err
	}
	if header[0] != 'p' {
		return fmt.Errorf("password authentication failed for user %q", user)
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 5 || length > 1<<20 {
		return fmt.Errorf("password authentication failed for user %q", user)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(connection, body); err != nil {
		return err
	}
	password := strings.TrimSuffix(string(body), "\x00")
	login, stored, err := azurePGAdminCredential(runtime.sub, runtime.rg, runtime.name)
	if err == nil && user == login && password == stored {
		return nil
	}
	return fmt.Errorf("password authentication failed for user %q", user)
}

func azurePGReadPacket(connection net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint32(header))
	if length < 8 || length > 1<<20 {
		return nil, fmt.Errorf("invalid PostgreSQL startup packet length %d", length)
	}
	packet := make([]byte, length)
	copy(packet, header)
	_, err := io.ReadFull(connection, packet[4:])
	return packet, err
}

func azurePGStartupParameter(packet []byte, wanted string) string {
	if len(packet) < 9 {
		return ""
	}
	fields := strings.Split(string(packet[8:len(packet)-1]), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == wanted {
			return fields[i+1]
		}
	}
	return ""
}

// azurePGWriteErrorResponse sends a PostgreSQL ErrorResponse with the given
// severity, SQLSTATE and message — the refusal wire shape a real server
// speaks to a client it will not serve.
func azurePGWriteErrorResponse(connection net.Conn, severity, sqlState, message string) {
	payload := []byte("S" + severity + "\x00V" + severity + "\x00C" + sqlState + "\x00M" + message + "\x00\x00")
	packet := make([]byte, 5+len(payload))
	packet[0] = 'E'
	binary.BigEndian.PutUint32(packet[1:5], uint32(len(payload)+4))
	copy(packet[5:], payload)
	_, _ = connection.Write(packet)
}

func azurePGRelay(left, right net.Conn) {
	done := make(chan struct{}, 2)
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copySide(left, right)
	go copySide(right, left)
	<-done
}

func azurePGServerCertificate() (tls.Certificate, error) {
	azurePGTLSOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			azurePGTLSErr = err
			return
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(now.UnixNano()),
			Subject:      pkix.Name{CommonName: "Azure Database for PostgreSQL simulator"},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.AddDate(1, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			azurePGTLSErr = err
			return
		}
		keyDER := x509.MarshalPKCS1PrivateKey(key)
		azurePGTLSCert, azurePGTLSErr = tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}),
		)
	})
	return azurePGTLSCert, azurePGTLSErr
}

// azurePGStopDataPlane closes the server's listener, stops its engine, and —
// when the server is being deleted — removes its data volume.
func azurePGStopDataPlane(rg, name string, deleteVolume bool) {
	value, ok := azurePGDataPlanes.LoadAndDelete(azurePGServerKey(rg, name))
	if ok {
		if runtime, isRuntime := value.(*azurePGDataPlaneRuntime); isRuntime {
			_ = runtime.listener.Close()
			if _, handle := runtime.snapshot(); handle != nil {
				handle.Cancel()
				_ = handle.Wait()
			}
		}
	}
	if deleteVolume {
		azurePGRemoveVolumeSettled(azurePGServerVolume(rg, name))
	}
}

// azurePGRemoveVolumeSettled removes a volume, retrying while the engine that
// held it is still being torn down — the container's removal completes on a
// goroutine the handle's Wait does not cover.
func azurePGRemoveVolumeSettled(name string) {
	if sim.RequireContainerRuntime("removing a flexible-server volume") != nil {
		return
	}
	if !sim.VolumeExists(name) {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := sim.RemoveVolume(name)
		if err == nil || !sim.VolumeExists(name) {
			return
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "[sim-azurepg] volume %s was not removed: %v\n", name, err)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
