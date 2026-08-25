package main

import (
	"context"
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

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The Cloud SQL data plane.
//
// A Cloud SQL instance is a real database engine. The simulator serves the
// instance's PRIMARY address as a listener it actually owns: the address in
// `ipAddresses` is a loopback address this process binds at the engine's
// conventional port (PostgreSQL 5432, MySQL 3306 — the Cloud SQL Admin API
// carries no port, so the port is the engine's, exactly as on Google Cloud).
// The first client connection starts the engine — a real PostgreSQL or MySQL
// container whose data directory is the named volume
// sockerless-cloudsql-<project>-<instance> — and the front proxy owns TLS and
// authentication, then relays bytes.
//
// Identity is real: the users the Cloud SQL Admin API declares are reconciled
// into the engine as real roles with their declared passwords, and a
// PostgreSQL session runs as the user the client named, not as a shared
// superuser. Credentials are sealed under a Google-owned Cloud KMS key from
// the simulator's own KMS slice — never stored in the clear.
//
// A host that cannot provide the conventional port on a per-instance loopback
// address (macOS refuses loopback aliases without root) leaves the instance
// exactly as modeled as the whole slice was before the data plane existed,
// and says so on stderr. Linux provides it natively; CI exercises the real
// path.

// sqlGoogleOwnedKeyVersion is the Cloud KMS key version that seals Cloud SQL
// credentials — the simulator's analogue of the Google-owned key Cloud SQL
// encrypts instance secrets under. It lives in the same key-material store
// the Cloud KMS slice serves, under a name no customer request can produce.
const sqlGoogleOwnedKeyVersion = "projects/google-managed/locations/global/keyRings/cloud-sql/cryptoKeys/instance-credentials/cryptoKeyVersions/1"

var sqlSealMu sync.Mutex

// sqlSealSecret encrypts a credential under the Cloud-SQL-owned KMS key,
// generating the key material on first use.
func sqlSealSecret(plaintext string) ([]byte, error) {
	sqlSealMu.Lock()
	record, ok := kmsKeyMaterial.Get(sqlGoogleOwnedKeyVersion)
	if !ok {
		material := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, material); err != nil {
			sqlSealMu.Unlock()
			return nil, fmt.Errorf("generate Cloud SQL credential key: %w", err)
		}
		record = kmsKeyMaterialRecord{Key: material}
		kmsKeyMaterial.Put(sqlGoogleOwnedKeyVersion, record)
	}
	sqlSealMu.Unlock()
	return kmsEncryptBytes(record.Key, 1, []byte(plaintext), nil)
}

// sqlOpenSecret decrypts a credential sealed by sqlSealSecret.
func sqlOpenSecret(sealed []byte) (string, error) {
	record, ok := kmsKeyMaterial.Get(sqlGoogleOwnedKeyVersion)
	if !ok {
		return "", fmt.Errorf("the Cloud SQL credential key does not exist")
	}
	// The sealed blob is framed version(4) || nonce || sealed; the version
	// prefix comes off before the AEAD open.
	_, blob, err := kmsParseCiphertext(sealed)
	if err != nil {
		return "", err
	}
	plaintext, err := kmsDecryptBytes(record.Key, blob, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// sqlEngineFamily reports the engine behind a databaseVersion, and whether
// the version has a data plane at all.
func sqlEngineFamily(databaseVersion string) (family string, ok bool) {
	switch {
	case strings.HasPrefix(databaseVersion, "POSTGRES_"):
		return "postgres", true
	case strings.HasPrefix(databaseVersion, "MYSQL_"):
		return "mysql", true
	}
	return "", false
}

func sqlEnginePort(family string) int {
	if family == "postgres" {
		return 5432
	}
	return 3306
}

// sqlBuiltInAdminUser is the user the engine is initialised with — the one
// Cloud SQL creates from the insert request's rootPassword.
func sqlBuiltInAdminUser(family string) string {
	if family == "postgres" {
		return "postgres"
	}
	return "root"
}

func sqlInstanceVolume(project, instance string) string {
	return "sockerless-cloudsql-" + project + "-" + instance
}

const sqlEngineInitializationBudget = 10 * time.Minute
const sqlEngineLivenessInterval = 2 * time.Second

type sqlDataPlaneRuntime struct {
	mu       sync.RWMutex
	project  string
	instance string
	family   string
	listener net.Listener
	backend  string
	handle   *sim.ContainerHandle
	start    *sync.Once
	startErr error
}

func (runtime *sqlDataPlaneRuntime) snapshot() (string, *sim.ContainerHandle, *sync.Once) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.backend, runtime.handle, runtime.start
}

func (runtime *sqlDataPlaneRuntime) update(backend string, handle *sim.ContainerHandle) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.backend = backend
	runtime.handle = handle
}

// resetEngine forgets the engine so the next connection starts a fresh one —
// the restore path stops the engine, replaces its volume, and resets.
func (runtime *sqlDataPlaneRuntime) resetEngine() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.backend = ""
	runtime.handle = nil
	runtime.start = &sync.Once{}
	runtime.startErr = nil
}

var sqlDataPlanes sync.Map // project/instance -> *sqlDataPlaneRuntime

var (
	sqlTLSOnce sync.Once
	sqlTLSCert tls.Certificate
	sqlTLSErr  error
)

// sqlInstallDataPlane binds the instance's PRIMARY address and records it on
// the instance. It returns false — with the reason — when this host cannot
// provide a loopback address at the engine's conventional port; the caller
// keeps the instance modeled and says so.
func sqlInstallDataPlane(inst *SQLInstance) (bool, error) {
	family, ok := sqlEngineFamily(inst.DatabaseVersion)
	if !ok {
		return false, nil
	}
	if sim.RequireContainerRuntime("the Cloud SQL data plane") != nil {
		return false, nil
	}
	port := sqlEnginePort(family)
	listener, err := sqlListenOnLoopback(inst.Project+"/"+inst.Name, port)
	if err != nil {
		return false, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return false, fmt.Errorf("listener returned address type %T", listener.Addr())
	}
	inst.IpAddresses = []map[string]any{
		{"type": "PRIMARY", "ipAddress": address.IP.String()},
	}
	runtime := &sqlDataPlaneRuntime{
		project:  inst.Project,
		instance: inst.Name,
		family:   family,
		listener: listener,
		start:    &sync.Once{},
	}
	sqlDataPlanes.Store(sqlInstanceKey(inst.Project, inst.Name), runtime)
	go runtime.serve()
	return true, nil
}

// sqlListenOnLoopback binds the engine's conventional port on a loopback
// address derived from the instance identity. The Cloud SQL Admin API has no
// port field, so unlike Amazon RDS there is no ephemeral-port refuge: the
// port is part of the contract. Hosts that refuse loopback aliases get one
// last chance at 127.0.0.1 before the instance stays modeled.
func sqlListenOnLoopback(identifier string, port int) (net.Listener, error) {
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

// sqlRecoverDataPlanes rebinds every instance's address after a control-plane
// restart and re-adopts engine containers an earlier process left running.
func sqlRecoverDataPlanes() error {
	for _, inst := range sqlInstances.List() {
		family, ok := sqlEngineFamily(inst.DatabaseVersion)
		if !ok {
			continue
		}
		if len(inst.IpAddresses) != 1 {
			continue
		}
		recorded, _ := inst.IpAddresses[0]["ipAddress"].(string)
		ip := net.ParseIP(recorded)
		if ip == nil || !ip.IsLoopback() {
			// A modeled instance from a host without the capability.
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(recorded, strconv.Itoa(sqlEnginePort(family))))
		if err != nil {
			return fmt.Errorf("rebind Cloud SQL instance %s at %s: %w", inst.Name, recorded, err)
		}
		runtime := &sqlDataPlaneRuntime{
			project:  inst.Project,
			instance: inst.Name,
			family:   family,
			listener: listener,
			start:    &sync.Once{},
		}
		key := sqlInstanceKey(inst.Project, inst.Name)
		sqlDataPlanes.Store(key, runtime)
		if err := sqlAdoptEngine(runtime); err != nil {
			return fmt.Errorf("re-adopt Cloud SQL instance %s engine: %w", inst.Name, err)
		}
		go runtime.serve()
	}
	return nil
}

// sqlAdoptEngine picks up the engine container an earlier control-plane
// process left for this instance, if one exists.
func sqlAdoptEngine(runtime *sqlDataPlaneRuntime) error {
	existing, err := sim.FindExistingContainers(map[string]string{
		"sockerless-cloudsql-instance": runtime.project + "/" + runtime.instance,
	})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	if len(existing) != 1 {
		return fmt.Errorf("found %d database engine containers", len(existing))
	}
	backendPort := existing[0].PublishedPorts[sqlEnginePort(runtime.family)]
	if backendPort == 0 {
		return fmt.Errorf("container %s has no published database port", existing[0].ID)
	}
	if !existing[0].Running {
		if err := sim.StartExistingContainer(existing[0].ID); err != nil {
			return fmt.Errorf("resume database engine container %s: %w", existing[0].ID, err)
		}
	}
	handle, err := sim.AdoptContainer(existing[0].ID, sim.ContainerConfig{}, sim.NoopSink{})
	if err != nil {
		return err
	}
	runtime.update(net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle)
	return nil
}

func (runtime *sqlDataPlaneRuntime) serve() {
	for {
		connection, err := runtime.listener.Accept()
		if err != nil {
			return
		}
		go runtime.serveConnection(connection)
	}
}

// ensureEngine brings the instance's database engine up on first use and
// blocks until it accepts clients and the declared users and databases exist
// in it.
func (runtime *sqlDataPlaneRuntime) ensureEngine() error {
	runtime.mu.RLock()
	start := runtime.start
	runtime.mu.RUnlock()
	start.Do(func() {
		if _, handle, _ := runtime.snapshot(); handle != nil {
			runtime.startErr = runtime.awaitEngine()
			return
		}
		runtime.startErr = runtime.startEngine()
	})
	return runtime.startErr
}

func (runtime *sqlDataPlaneRuntime) startEngine() error {
	adminPassword, err := sqlEngineAdminPassword(runtime.project, runtime.instance, runtime.family)
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
	var image string
	var env map[string]string
	var args []string
	var dataPath string
	switch runtime.family {
	case "postgres":
		image = "public.ecr.aws/docker/library/postgres:16-alpine"
		dataPath = "/var/lib/postgresql/data"
		env = map[string]string{
			"POSTGRES_USER":     "postgres",
			"POSTGRES_PASSWORD": adminPassword,
			"POSTGRES_DB":       "postgres",
			// The front proxy authenticates every client against the sealed
			// credentials the Cloud SQL Admin API holds; the engine trusts
			// the proxy, which lets a session run as the user the client
			// named without the proxy replaying that user's password.
			"POSTGRES_HOST_AUTH_METHOD": "trust",
		}
	case "mysql":
		image = "public.ecr.aws/docker/library/mysql:8.0"
		dataPath = "/var/lib/mysql"
		env = map[string]string{
			"MYSQL_ROOT_PASSWORD": adminPassword,
		}
		args = []string{"--default-authentication-plugin=mysql_native_password"}
	default:
		return fmt.Errorf("database engine %q has no data plane", runtime.family)
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        image,
		Architecture: "linux/amd64",
		Args:         args,
		Env:          env,
		PublishPorts: map[int]int{sqlEnginePort(runtime.family): backendPort},
		Binds:        []string{sqlInstanceVolume(runtime.project, runtime.instance) + ":" + dataPath},
		Labels:       map[string]string{"sockerless-cloudsql-instance": runtime.project + "/" + runtime.instance},
		Sandbox:      sim.SandboxCloudRun,
	}, sim.NoopSink{})
	if err != nil {
		return fmt.Errorf("start %s database engine: %w", runtime.family, err)
	}
	runtime.update(net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle)
	return runtime.awaitEngine()
}

// sqlEngineAdminPassword returns the built-in admin user's password —
// the rootPassword the insert request carried, sealed at rest.
func sqlEngineAdminPassword(project, instance, family string) (string, error) {
	admin := sqlBuiltInAdminUser(family)
	for _, u := range sqlUsers.List() {
		if u.Project == project && u.Instance == instance && u.Name == admin {
			credential, ok := sqlUserSecrets.Get(sqlUserKey(project, instance, u.Host, u.Name))
			if !ok || len(credential.Sealed) == 0 {
				break
			}
			return sqlOpenSecret(credential.Sealed)
		}
	}
	return "", fmt.Errorf("instance %s has no %s credential: create the instance with rootPassword or set one with users.update", instance, admin)
}

// awaitEngine blocks until the engine accepts client connections, then
// reconciles the users and databases the Cloud SQL Admin API declares into
// it, so the engine serves real roles and real databases.
func (runtime *sqlDataPlaneRuntime) awaitEngine() error {
	backend, handle, _ := runtime.snapshot()
	deadline := time.Now().Add(sqlEngineInitializationBudget)
	nextLivenessCheck := time.Now().Add(sqlEngineLivenessInterval)
	for !sqlEngineReady(runtime.family, backend) {
		now := time.Now()
		if !now.Before(nextLivenessCheck) {
			if err := sqlEngineContainerRunning(handle.ContainerID); err != nil {
				handle.Cancel()
				return fmt.Errorf("%s database engine stopped before accepting connections: %w", runtime.family, err)
			}
			nextLivenessCheck = now.Add(sqlEngineLivenessInterval)
		}
		if !now.Before(deadline) {
			handle.Cancel()
			return fmt.Errorf("%s database engine did not become ready", runtime.family)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := sqlReconcileEngineState(runtime); err != nil {
		return fmt.Errorf("reconcile declared users and databases into the engine: %w", err)
	}
	return nil
}

func sqlEngineContainerRunning(containerID string) error {
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

func sqlEngineReady(family, address string) bool {
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if family == "postgres" {
		return sqlPostgreSQLAcceptsConnections(connection)
	}
	// MySQL binds its client port only once the server is serving, and greets
	// every accepted connection with the initial handshake packet (protocol
	// version 10).
	_, payload, err := sqlMySQLReadPacket(connection)
	return err == nil && len(payload) > 0 && payload[0] == 10
}

// sqlPostgreSQLAcceptsConnections classifies a startup exchange the way
// libpq's PQping does: a server that answers is up, except while it still
// answers every client "the database system is starting up" (SQLSTATE 57P03).
func sqlPostgreSQLAcceptsConnections(connection net.Conn) bool {
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
		return sqlPostgreSQLErrorSQLState(connection, header) != "57P03"
	default:
		return false
	}
}

func sqlPostgreSQLErrorSQLState(connection net.Conn, header []byte) string {
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

// sqlReconcileEngineState creates or updates, inside the running engine,
// every user and database the Cloud SQL Admin API declares for the instance.
// It runs at engine readiness — first boot, control-plane restart, and after
// a restore replaced the data directory — and after control-plane mutations
// while the engine is up, so the API and the engine never disagree.
func sqlReconcileEngineState(runtime *sqlDataPlaneRuntime) error {
	for _, u := range sqlUsers.List() {
		if u.Project != runtime.project || u.Instance != runtime.instance {
			continue
		}
		credential, ok := sqlUserSecrets.Get(sqlUserKey(u.Project, u.Instance, u.Host, u.Name))
		if !ok || len(credential.Sealed) == 0 {
			continue
		}
		password, err := sqlOpenSecret(credential.Sealed)
		if err != nil {
			return fmt.Errorf("open credential for user %s: %w", u.Name, err)
		}
		if err := sqlEngineEnsureUser(runtime, u.Name, password); err != nil {
			return fmt.Errorf("ensure user %s: %w", u.Name, err)
		}
	}
	for _, d := range sqlDatabases.List() {
		if d.Project != runtime.project || d.Instance != runtime.instance {
			continue
		}
		if err := sqlEngineEnsureDatabase(runtime, d.Name); err != nil {
			return fmt.Errorf("ensure database %s: %w", d.Name, err)
		}
	}
	return nil
}

// sqlEngineEnsureUser makes the engine hold the user with the declared
// password. PostgreSQL users are members of the admin role, mirroring Cloud
// SQL's cloudsqlsuperuser membership.
func sqlEngineEnsureUser(runtime *sqlDataPlaneRuntime, name, password string) error {
	admin := sqlBuiltInAdminUser(runtime.family)
	if runtime.family == "postgres" {
		if name == admin {
			statement := "ALTER ROLE " + sqlQuoteIdentifier(name) + " WITH LOGIN PASSWORD " + sqlQuoteLiteral(password)
			return sqlEngineExec(runtime, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", admin, "-d", "postgres", "-c", statement})
		}
		script := fmt.Sprintf(
			`DO $$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s; END IF; END$$; `+
				`ALTER ROLE %s WITH LOGIN CREATEDB CREATEROLE PASSWORD %s; GRANT %s TO %s;`,
			sqlQuoteLiteral(name), sqlQuoteIdentifier(name),
			sqlQuoteIdentifier(name), sqlQuoteLiteral(password),
			sqlQuoteIdentifier(admin), sqlQuoteIdentifier(name),
		)
		return sqlEngineExec(runtime, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", admin, "-d", "postgres", "-c", script})
	}
	adminPassword, err := sqlEngineAdminPassword(runtime.project, runtime.instance, runtime.family)
	if err != nil {
		return err
	}
	if name == admin {
		statement := "ALTER USER 'root'@'%' IDENTIFIED BY " + sqlQuoteLiteral(password) + "; ALTER USER 'root'@'localhost' IDENTIFIED BY " + sqlQuoteLiteral(password) + "; FLUSH PRIVILEGES;"
		return sqlEngineExec(runtime, []string{"mysql", "--user=root", "--password=" + adminPassword, "--execute=" + statement})
	}
	statement := "CREATE USER IF NOT EXISTS " + sqlQuoteLiteral(name) + "@'%'; " +
		"ALTER USER " + sqlQuoteLiteral(name) + "@'%' IDENTIFIED BY " + sqlQuoteLiteral(password) + "; " +
		"GRANT ALL PRIVILEGES ON *.* TO " + sqlQuoteLiteral(name) + "@'%' WITH GRANT OPTION; FLUSH PRIVILEGES;"
	return sqlEngineExec(runtime, []string{"mysql", "--user=root", "--password=" + adminPassword, "--execute=" + statement})
}

func sqlEngineEnsureDatabase(runtime *sqlDataPlaneRuntime, name string) error {
	if runtime.family == "postgres" {
		admin := sqlBuiltInAdminUser(runtime.family)
		check := "SELECT 1 FROM pg_database WHERE datname = " + sqlQuoteLiteral(name)
		create := "CREATE DATABASE " + sqlQuoteIdentifier(name)
		script := fmt.Sprintf(`[ -n "$(psql -U %s -d postgres -tAc %s)" ] || psql -v ON_ERROR_STOP=1 -U %s -d postgres -c %s`,
			admin, sqlShellQuote(check), admin, sqlShellQuote(create))
		return sqlEngineExec(runtime, []string{"/bin/sh", "-c", script})
	}
	adminPassword, err := sqlEngineAdminPassword(runtime.project, runtime.instance, runtime.family)
	if err != nil {
		return err
	}
	statement := "CREATE DATABASE IF NOT EXISTS " + sqlQuoteMySQLIdentifier(name)
	return sqlEngineExec(runtime, []string{"mysql", "--user=root", "--password=" + adminPassword, "--execute=" + statement})
}

// sqlEngineDropUserIfRunning removes a deleted user's role from a running
// engine. An engine that is not running needs nothing: readiness reconciles
// only users that still exist, and a role in a cold data directory whose API
// user is gone cannot authenticate — the front proxy holds no credential.
func sqlEngineDropUserIfRunning(project, instance, name string) error {
	runtime, running := sqlRunningRuntime(project, instance)
	if !running {
		return nil
	}
	if runtime.family == "postgres" {
		statement := "DROP ROLE IF EXISTS " + sqlQuoteIdentifier(name)
		return sqlEngineExec(runtime, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", statement})
	}
	adminPassword, err := sqlEngineAdminPassword(project, instance, runtime.family)
	if err != nil {
		return err
	}
	statement := "DROP USER IF EXISTS " + sqlQuoteLiteral(name) + "@'%'"
	return sqlEngineExec(runtime, []string{"mysql", "--user=root", "--password=" + adminPassword, "--execute=" + statement})
}

// sqlEngineDropDatabaseIfRunning removes a deleted database from a running
// engine.
func sqlEngineDropDatabaseIfRunning(project, instance, name string) error {
	runtime, running := sqlRunningRuntime(project, instance)
	if !running {
		return nil
	}
	if runtime.family == "postgres" {
		statement := "DROP DATABASE IF EXISTS " + sqlQuoteIdentifier(name) + " WITH (FORCE)"
		return sqlEngineExec(runtime, []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", statement})
	}
	adminPassword, err := sqlEngineAdminPassword(project, instance, runtime.family)
	if err != nil {
		return err
	}
	statement := "DROP DATABASE IF EXISTS " + sqlQuoteMySQLIdentifier(name)
	return sqlEngineExec(runtime, []string{"mysql", "--user=root", "--password=" + adminPassword, "--execute=" + statement})
}

// sqlRunningRuntime returns the instance's data-plane runtime only when its
// engine container is up.
func sqlRunningRuntime(project, instance string) (*sqlDataPlaneRuntime, bool) {
	value, ok := sqlDataPlanes.Load(sqlInstanceKey(project, instance))
	if !ok {
		return nil, false
	}
	runtime, ok := value.(*sqlDataPlaneRuntime)
	if !ok {
		return nil, false
	}
	if _, handle, _ := runtime.snapshot(); handle == nil {
		return nil, false
	}
	return runtime, true
}

// sqlReconcileIfRunning applies control-plane user/database mutations to a
// running engine immediately. An engine that is not running needs nothing:
// readiness reconciles the full declared state before serving any client.
func sqlReconcileIfRunning(project, instance string) error {
	value, ok := sqlDataPlanes.Load(sqlInstanceKey(project, instance))
	if !ok {
		return nil
	}
	runtime, ok := value.(*sqlDataPlaneRuntime)
	if !ok {
		return fmt.Errorf("data-plane runtime has an invalid type")
	}
	if _, handle, _ := runtime.snapshot(); handle == nil {
		return nil
	}
	return sqlReconcileEngineState(runtime)
}

func sqlEngineExec(runtime *sqlDataPlaneRuntime, command []string) error {
	_, handle, _ := runtime.snapshot()
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

func sqlQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqlQuoteMySQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func sqlQuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func sqlShellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'\''`) + `'`
}

func (runtime *sqlDataPlaneRuntime) serveConnection(client net.Conn) {
	defer client.Close()
	if err := runtime.ensureEngine(); err != nil {
		log.Printf("Cloud SQL %s/%s data plane: %v", runtime.project, runtime.instance, err)
		return
	}
	if runtime.family == "postgres" {
		runtime.servePostgreSQL(client)
		return
	}
	runtime.serveMySQL(client)
}

func (runtime *sqlDataPlaneRuntime) servePostgreSQL(client net.Conn) {
	startup, secure, err := sqlReadPostgreSQLStartup(client)
	if err != nil {
		log.Printf("Cloud SQL %s/%s PostgreSQL startup: %v", runtime.project, runtime.instance, err)
		return
	}
	if secure != nil {
		client = secure
		defer client.Close()
	}
	user := sqlPostgreSQLStartupParameter(startup, "user")
	if err := runtime.authenticatePostgreSQLClient(client, user); err != nil {
		sqlPostgreSQLAuthError(client, err.Error())
		return
	}
	backendAddress, _, _ := runtime.snapshot()
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		log.Printf("Cloud SQL %s/%s PostgreSQL backend dial: %v", runtime.project, runtime.instance, err)
		return
	}
	defer backend.Close()
	// The engine trusts the proxy, so relaying the client's own startup keeps
	// the session's identity: current_user is the user the client named.
	if _, err := backend.Write(startup); err != nil {
		log.Printf("Cloud SQL %s/%s PostgreSQL backend startup: %v", runtime.project, runtime.instance, err)
		return
	}
	sqlRelay(client, backend)
}

// authenticatePostgreSQLClient runs a cleartext-password exchange and checks
// the password against the sealed credential the Cloud SQL Admin API holds
// for that user.
func (runtime *sqlDataPlaneRuntime) authenticatePostgreSQLClient(connection net.Conn, user string) error {
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
	if sqlCredentialMatches(runtime.project, runtime.instance, user, password) {
		return nil
	}
	return fmt.Errorf("password authentication failed for user %q", user)
}

// sqlCredentialMatches checks a presented password against the sealed
// credential stored for the named user.
func sqlCredentialMatches(project, instance, user, password string) bool {
	for _, u := range sqlUsers.List() {
		if u.Project != project || u.Instance != instance || u.Name != user {
			continue
		}
		credential, ok := sqlUserSecrets.Get(sqlUserKey(project, instance, u.Host, u.Name))
		if !ok || len(credential.Sealed) == 0 {
			continue
		}
		stored, err := sqlOpenSecret(credential.Sealed)
		if err != nil {
			continue
		}
		if stored == password {
			return true
		}
	}
	return false
}

func sqlReadPostgreSQLStartup(connection net.Conn) ([]byte, net.Conn, error) {
	packet, err := sqlReadPostgreSQLPacket(connection)
	if err != nil {
		return nil, nil, err
	}
	if len(packet) == 8 && binary.BigEndian.Uint32(packet[4:]) == 80877103 {
		if _, err := connection.Write([]byte{'S'}); err != nil {
			return nil, nil, err
		}
		certificate, err := sqlServerCertificate()
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
		packet, err = sqlReadPostgreSQLPacket(secure)
		return packet, secure, err
	}
	return packet, nil, nil
}

func sqlReadPostgreSQLPacket(connection net.Conn) ([]byte, error) {
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

func sqlPostgreSQLStartupParameter(packet []byte, wanted string) string {
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

func sqlPostgreSQLAuthError(connection net.Conn, message string) {
	payload := []byte("SERROR\x00C28P01\x00M" + message + "\x00\x00")
	packet := make([]byte, 5+len(payload))
	packet[0] = 'E'
	binary.BigEndian.PutUint32(packet[1:5], uint32(len(payload)+4))
	copy(packet[5:], payload)
	_, _ = connection.Write(packet)
}

func sqlRelay(left, right net.Conn) {
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

func sqlServerCertificate() (tls.Certificate, error) {
	sqlTLSOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			sqlTLSErr = err
			return
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(now.UnixNano()),
			Subject:      pkix.Name{CommonName: "Cloud SQL simulator"},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.AddDate(1, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			sqlTLSErr = err
			return
		}
		keyDER := x509.MarshalPKCS1PrivateKey(key)
		sqlTLSCert, sqlTLSErr = tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}),
		)
	})
	return sqlTLSCert, sqlTLSErr
}

// sqlStopEngine stops the instance's engine container, leaving the listener
// and the volume in place; the next client connection starts a fresh engine.
// The restore path uses it to swap the data directory underneath.
func sqlStopEngine(project, instance string) {
	value, ok := sqlDataPlanes.Load(sqlInstanceKey(project, instance))
	if !ok {
		return
	}
	runtime, ok := value.(*sqlDataPlaneRuntime)
	if !ok {
		return
	}
	_, handle, _ := runtime.snapshot()
	if handle != nil {
		handle.Cancel()
		_ = handle.Wait()
	}
	runtime.resetEngine()
}

// sqlStopDataPlane closes the instance's listener, stops its engine, and —
// when the instance is being deleted — removes its data volume.
func sqlStopDataPlane(project, instance string, deleteVolume bool) {
	value, ok := sqlDataPlanes.LoadAndDelete(sqlInstanceKey(project, instance))
	if !ok {
		return
	}
	runtime, ok := value.(*sqlDataPlaneRuntime)
	if !ok {
		return
	}
	_ = runtime.listener.Close()
	_, handle, _ := runtime.snapshot()
	if handle != nil {
		handle.Cancel()
		_ = handle.Wait()
	}
	if deleteVolume {
		sqlRemoveVolumeSettled(sqlInstanceVolume(project, instance))
	}
}

// sqlRemoveVolumeSettled removes a volume, retrying while the engine that
// held it is still being torn down — the container's removal completes on a
// goroutine the handle's Wait does not cover.
func sqlRemoveVolumeSettled(name string) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := sim.RemoveVolume(name)
		if err == nil || !sim.VolumeExists(name) {
			return
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "[sim-cloudsql] volume %s was not removed: %v\n", name, err)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
