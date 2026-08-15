package main

import (
	"bytes"
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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	dockerclient "github.com/moby/moby/client"
)

const rdsAWSOwnedKMSKeyID = "aws-owned-rds"
const rdsEmptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// rdsEngineInitializationBudget bounds the wait for a database engine to accept
// clients. Bringing an Amazon RDS instance to "available" takes minutes on the
// real service, and a real engine's first boot lays down its whole data
// directory before it listens — a busy host stretches that well past the minute
// mark, and a tighter bound would report a slow host as a broken engine. The
// wait ends the moment the engine container stops, so a genuinely broken engine
// still fails immediately rather than burning this budget.
const rdsEngineInitializationBudget = 10 * time.Minute

// rdsEngineLivenessInterval is how often the wait re-reads the engine
// container's real state. The readiness probe already runs every 100ms; asking
// the container engine as often would add API load without telling us anything
// the next probe would not.
const rdsEngineLivenessInterval = 2 * time.Second

type rdsDataPlaneRuntime struct {
	mu       sync.RWMutex
	instance RDSInstance
	listener net.Listener
	backend  string
	handle   *sim.ContainerHandle
	start    sync.Once
	startErr error
}

func (runtime *rdsDataPlaneRuntime) snapshot() (RDSInstance, string, *sim.ContainerHandle) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.instance, runtime.backend, runtime.handle
}

func (runtime *rdsDataPlaneRuntime) snapshotInstance() RDSInstance {
	instance, _, _ := runtime.snapshot()
	return instance
}

func (runtime *rdsDataPlaneRuntime) update(instance RDSInstance, backend string, handle *sim.ContainerHandle) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.instance = instance
	runtime.backend = backend
	runtime.handle = handle
}

var (
	rdsDataPlanes sync.Map
	rdsTLSOnce    sync.Once
	rdsTLSCert    tls.Certificate
	rdsTLSErr     error
)

func rdsRecoverDataPlanes() error {
	for _, instance := range rdsInstances.List() {
		if instance.DBInstanceStatus != "available" || len(instance.MasterUserSecret) == 0 {
			continue
		}
		_, masterPassword, ok := kmsDecryptBytes(instance.MasterUserSecret)
		if !ok {
			return fmt.Errorf("decrypt master-user credential for DB instance %s", instance.DBInstanceIdentifier)
		}
		if err := rdsInstallDataPlane(&instance, string(masterPassword)); err != nil {
			return fmt.Errorf("restore DB instance %s: %w", instance.DBInstanceIdentifier, err)
		}
		if err := rdsAdoptDataPlaneBackend(instance); err != nil {
			return fmt.Errorf("restore DB instance %s backend: %w", instance.DBInstanceIdentifier, err)
		}
		rdsInstances.Put(instance.DBInstanceIdentifier, instance)
	}
	return nil
}

func rdsAdoptDataPlaneBackend(instance RDSInstance) error {
	runtimeValue, ok := rdsDataPlanes.Load(instance.DBInstanceIdentifier)
	if !ok {
		return fmt.Errorf("endpoint listener was not installed")
	}
	runtime, ok := runtimeValue.(*rdsDataPlaneRuntime)
	if !ok {
		return fmt.Errorf("endpoint runtime has unexpected type %T", runtimeValue)
	}
	existing, err := sim.FindExistingContainers(map[string]string{
		"sockerless-rds-instance": instance.DBInstanceIdentifier,
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
	containerPort, ok := rdsEngineContainerPort(instance.Engine)
	if !ok {
		return nil
	}
	backendPort := existing[0].PublishedPorts[containerPort]
	if backendPort == 0 {
		return fmt.Errorf("container %s has no published database port %d", existing[0].ID, containerPort)
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
	runtime.update(instance, net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle)
	return nil
}

func rdsEngineContainerPort(engine string) (int, bool) {
	switch {
	case strings.HasPrefix(strings.ToLower(engine), "postgres"):
		return 5432, true
	case strings.EqualFold(engine, "mysql"), strings.EqualFold(engine, "mariadb"):
		return 3306, true
	default:
		return 0, false
	}
}

func rdsInstallDataPlane(instance *RDSInstance, masterPassword string) error {
	engine := strings.ToLower(instance.Engine)
	if !strings.HasPrefix(engine, "postgres") && engine != "mysql" && engine != "mariadb" {
		return nil
	}
	if masterPassword == "" {
		return fmt.Errorf("MasterUserPassword is required for the %s data plane", instance.Engine)
	}
	if _, ok := kmsGetKeyMaterial(rdsAWSOwnedKMSKeyID); !ok {
		if _, err := kmsGenerateKeyMaterial(rdsAWSOwnedKMSKeyID); err != nil {
			return fmt.Errorf("generate AWS owned RDS key: %w", err)
		}
	}
	if len(instance.MasterUserSecret) == 0 {
		ciphertext, ok := kmsEncryptBytes(rdsAWSOwnedKMSKeyID, []byte(masterPassword))
		if !ok {
			return fmt.Errorf("encrypt RDS master-user credential")
		}
		instance.MasterUserSecret = ciphertext
	}
	if len(instance.BackendMasterUserSecret) == 0 {
		instance.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
	}

	listener, err := rdsListenForInstance(*instance)
	if err != nil {
		return fmt.Errorf("allocate RDS endpoint: %w", err)
	}
	listenAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return fmt.Errorf("RDS endpoint listener returned address type %T", listener.Addr())
	}
	instance.Endpoint = listenAddress.IP.String()
	instance.Port = listenAddress.Port
	runtime := &rdsDataPlaneRuntime{instance: *instance, listener: listener}
	rdsDataPlanes.Store(instance.DBInstanceIdentifier, runtime)
	go runtime.serve()
	return nil
}

func rdsListenForInstance(instance RDSInstance) (net.Listener, error) {
	if endpointIP := net.ParseIP(instance.Endpoint); endpointIP != nil && endpointIP.IsLoopback() && instance.Port > 0 {
		return net.Listen("tcp", net.JoinHostPort(instance.Endpoint, strconv.Itoa(instance.Port)))
	}
	return rdsListenOnLoopback(instance.DBInstanceIdentifier, instance.Port)
}

func rdsListenOnLoopback(identifier string, port int) (net.Listener, error) {
	var seed byte = 2
	for i := 0; i < len(identifier); i++ {
		seed += identifier[i]
	}
	for offset := 0; offset < 253; offset++ {
		octet := 2 + (int(seed)+offset)%253
		address := net.JoinHostPort(fmt.Sprintf("127.0.0.%d", octet), strconv.Itoa(port))
		listener, err := net.Listen("tcp", address)
		if err == nil {
			return listener, nil
		}
	}
	// A host database or another simulator may already bind the engine's
	// conventional port on all loopback addresses. The endpoint port is a
	// coordinate, so ask the kernel for an isolated one rather than sharing or
	// replacing that listener.
	return net.Listen("tcp", "127.0.0.1:0")
}

func (runtime *rdsDataPlaneRuntime) serve() {
	for {
		connection, err := runtime.listener.Accept()
		if err != nil {
			return
		}
		go runtime.serveConnection(connection)
	}
}

// ensureBackend brings the instance's database engine up — starting its
// container the first time a client reaches the endpoint, or picking up the
// container an earlier control-plane process left running — and does not return
// until that engine accepts client connections. An endpoint that forwarded a
// client before then would answer with the engine's own startup rejection,
// which an Amazon RDS instance reporting "available" never does.
func (runtime *rdsDataPlaneRuntime) ensureBackend() error {
	runtime.start.Do(func() {
		if _, _, handle := runtime.snapshot(); handle != nil {
			runtime.startErr = runtime.awaitDatabaseEngine()
			return
		}
		runtime.startErr = runtime.startBackend()
	})
	return runtime.startErr
}

func (runtime *rdsDataPlaneRuntime) startBackend() error {
	instance := runtime.snapshotInstance()
	backendSecret := instance.BackendMasterUserSecret
	if len(backendSecret) == 0 {
		backendSecret = instance.MasterUserSecret
	}
	_, password, ok := kmsDecryptBytes(backendSecret)
	if !ok {
		return fmt.Errorf("RDS master-user credential could not be decrypted")
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
	database := instance.DBName
	if database == "" {
		database = instance.MasterUsername
	}
	engine := strings.ToLower(instance.Engine)
	var image string
	var containerPort int
	var env map[string]string
	var args []string
	var dataPath string
	switch {
	case strings.HasPrefix(engine, "postgres"):
		image = "public.ecr.aws/docker/library/postgres:16-alpine"
		containerPort = 5432
		dataPath = "/var/lib/postgresql/data"
		env = map[string]string{
			"POSTGRES_USER":             instance.MasterUsername,
			"POSTGRES_PASSWORD":         string(password),
			"POSTGRES_DB":               database,
			"POSTGRES_HOST_AUTH_METHOD": "trust",
		}
	case engine == "mysql":
		image = "public.ecr.aws/docker/library/mysql:8.0"
		containerPort = 3306
		dataPath = "/var/lib/mysql"
		env = map[string]string{
			"MYSQL_USER":          instance.MasterUsername,
			"MYSQL_PASSWORD":      string(password),
			"MYSQL_ROOT_PASSWORD": string(password),
			"MYSQL_DATABASE":      database,
		}
		args = []string{"--default-authentication-plugin=mysql_native_password"}
	case engine == "mariadb":
		image = "public.ecr.aws/docker/library/mariadb:11.4"
		containerPort = 3306
		dataPath = "/var/lib/mysql"
		env = map[string]string{
			"MARIADB_USER":          instance.MasterUsername,
			"MARIADB_PASSWORD":      string(password),
			"MARIADB_ROOT_PASSWORD": string(password),
			"MARIADB_DATABASE":      database,
		}
	default:
		return fmt.Errorf("database engine %q has no data plane", instance.Engine)
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image: image, Architecture: "linux/amd64", Args: args, Env: env,
		PublishPorts: map[int]int{containerPort: backendPort},
		Binds:        []string{"sockerless-rds-" + instance.DBInstanceIdentifier + ":" + dataPath},
		Labels:       map[string]string{"sockerless-rds-instance": instance.DBInstanceIdentifier},
		Sandbox:      sim.SandboxFargate,
	}, sim.NoopSink{})
	if err != nil {
		return fmt.Errorf("start %s database engine: %w", instance.Engine, err)
	}
	backend := net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort))
	runtime.update(instance, backend, handle)
	return runtime.awaitDatabaseEngine()
}

// awaitDatabaseEngine blocks until the engine container accepts client
// connections, then applies the master-user password the control plane recorded
// while the engine was not running. Both callers need it: a freshly started
// container is still initialising, and a container reclaimed from an earlier
// control-plane process is still replaying its write-ahead log.
func (runtime *rdsDataPlaneRuntime) awaitDatabaseEngine() error {
	instance, backend, handle := runtime.snapshot()
	engine := strings.ToLower(instance.Engine)
	database := instance.DBName
	if database == "" {
		database = instance.MasterUsername
	}
	deadline := time.Now().Add(rdsEngineInitializationBudget)
	nextLivenessCheck := time.Now().Add(rdsEngineLivenessInterval)
	for !rdsDatabaseEngineReady(engine, backend, instance.MasterUsername, database) {
		now := time.Now()
		if !now.Before(nextLivenessCheck) {
			if err := rdsEngineContainerRunning(handle.ContainerID); err != nil {
				handle.Cancel()
				return fmt.Errorf("%s database engine stopped before accepting connections: %w", instance.Engine, err)
			}
			nextLivenessCheck = now.Add(rdsEngineLivenessInterval)
		}
		if !now.Before(deadline) {
			handle.Cancel()
			return fmt.Errorf("%s database engine did not become ready", instance.Engine)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if bytes.Equal(instance.BackendMasterUserSecret, instance.MasterUserSecret) {
		return nil
	}
	_, desiredPassword, decrypted := kmsDecryptBytes(instance.MasterUserSecret)
	if !decrypted {
		handle.Cancel()
		return fmt.Errorf("decrypt pending Amazon RDS master-user credential")
	}
	if err := rdsRotateBackendMasterPassword(runtime, string(desiredPassword)); err != nil {
		handle.Cancel()
		return fmt.Errorf("apply pending Amazon RDS master-user password: %w", err)
	}
	instance.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
	runtime.update(instance, backend, handle)
	rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
		stored.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
	})
	return nil
}

// rdsEngineContainerRunning reports the engine container's real state so that a
// readiness wait ends as soon as the engine dies instead of running out its
// initialization budget.
func rdsEngineContainerRunning(containerID string) error {
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

func rdsDatabaseEngineReady(engine, address, username, database string) bool {
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if strings.HasPrefix(engine, "postgres") {
		return rdsPostgreSQLAcceptsConnections(connection, username, database)
	}
	// MySQL and MariaDB bind their client port only once the server is serving,
	// and greet every accepted connection with the initial handshake packet
	// (protocol version 10). Anything else — a connection the port forwarder
	// accepted before the engine listened, or an error packet — is not a server
	// ready for clients.
	_, payload, err := mysqlReadPacket(connection)
	return err == nil && len(payload) > 0 && payload[0] == 10
}

// rdsPostgreSQLAcceptsConnections classifies a startup-packet exchange the way
// libpq's PQping — and therefore pg_isready — classifies it. A server that
// answers is up even when it rejects the credentials, with one exception: while
// the postmaster is listening but the startup process has not finished
// recovery, it answers every client with "the database system is starting up"
// (SQLSTATE 57P03) and accepts none of them.
func rdsPostgreSQLAcceptsConnections(connection net.Conn, username, database string) bool {
	parameters := []byte("user\x00" + username + "\x00database\x00" + database + "\x00\x00")
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
		return rdsPostgreSQLErrorFieldC(connection, header) != "57P03"
	default:
		return false
	}
}

// rdsPostgreSQLErrorFieldC returns the SQLSTATE carried by an ErrorResponse
// whose five-byte header has already been read.
func rdsPostgreSQLErrorFieldC(connection net.Conn, header []byte) string {
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

func (runtime *rdsDataPlaneRuntime) serveConnection(client net.Conn) {
	instance := runtime.snapshotInstance()
	defer client.Close()
	if err := runtime.ensureBackend(); err != nil {
		log.Printf("Amazon RDS %s data plane: %v", instance.DBInstanceIdentifier, err)
		return
	}
	if strings.HasPrefix(strings.ToLower(runtime.snapshotInstance().Engine), "postgres") {
		runtime.servePostgreSQL(client)
		return
	}
	runtime.serveMySQL(client)
}

func (runtime *rdsDataPlaneRuntime) servePostgreSQL(client net.Conn) {
	instance := runtime.snapshotInstance()
	startup, secure, err := rdsReadPostgreSQLStartup(client)
	if err != nil {
		log.Printf("Amazon RDS %s PostgreSQL startup: %v", instance.DBInstanceIdentifier, err)
		return
	}
	if secure != nil {
		client = secure
		defer client.Close()
	}
	user := rdsPostgreSQLStartupParameter(startup, "user")
	if err := rdsPostgreSQLAuthenticate(client, instance, user, secure != nil); err != nil {
		rdsPostgreSQLAuthError(client, err.Error())
		return
	}
	_, backendAddress, _ := runtime.snapshot()
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		log.Printf("Amazon RDS %s PostgreSQL backend dial: %v", instance.DBInstanceIdentifier, err)
		return
	}
	defer backend.Close()
	if _, err := backend.Write(startup); err != nil {
		log.Printf("Amazon RDS %s PostgreSQL backend startup: %v", instance.DBInstanceIdentifier, err)
		return
	}
	rdsRelay(client, backend)
}

func rdsReadPostgreSQLStartup(connection net.Conn) ([]byte, net.Conn, error) {
	packet, err := rdsReadPostgreSQLPacket(connection)
	if err != nil {
		return nil, nil, err
	}
	if len(packet) == 8 && binary.BigEndian.Uint32(packet[4:]) == 80877103 {
		if _, err := connection.Write([]byte{'S'}); err != nil {
			return nil, nil, err
		}
		certificate, err := rdsServerCertificate()
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
		packet, err = rdsReadPostgreSQLPacket(secure)
		return packet, secure, err
	}
	return packet, nil, nil
}

func rdsReadPostgreSQLPacket(connection net.Conn) ([]byte, error) {
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

func rdsPostgreSQLStartupParameter(packet []byte, wanted string) string {
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

func rdsPostgreSQLAuthenticate(connection net.Conn, instance RDSInstance, user string, secure bool) error {
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
	_, masterPassword, ok := kmsDecryptBytes(instance.MasterUserSecret)
	if ok && user == instance.MasterUsername && password == string(masterPassword) {
		return nil
	}
	if secure && instance.EnableIAMDatabaseAuthentication && rdsValidateIAMAuthToken(instance, user, password) {
		return nil
	}
	return fmt.Errorf("password authentication failed for user %q", user)
}

func rdsModifyDataPlaneAuthentication(instance *RDSInstance, newPassword *string) error {
	if newPassword != nil {
		if *newPassword == "" {
			return fmt.Errorf("MasterUserPassword cannot be empty")
		}
		ciphertext, encrypted := kmsEncryptBytes(rdsAWSOwnedKMSKeyID, []byte(*newPassword))
		if !encrypted {
			return fmt.Errorf("encrypt Amazon RDS master-user credential")
		}
		instance.MasterUserSecret = ciphertext
	}
	value, ok := rdsDataPlanes.Load(instance.DBInstanceIdentifier)
	if !ok {
		return nil
	}
	runtime, ok := value.(*rdsDataPlaneRuntime)
	if !ok {
		return fmt.Errorf("data-plane runtime has an invalid type")
	}
	if newPassword != nil {
		current, backend, handle := runtime.snapshot()
		if handle != nil {
			if err := rdsRotateBackendMasterPassword(runtime, *newPassword); err != nil {
				return err
			}
			instance.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
		} else {
			instance.BackendMasterUserSecret = append([]byte(nil), current.BackendMasterUserSecret...)
		}
		runtime.update(*instance, backend, handle)
		return nil
	}
	_, backend, handle := runtime.snapshot()
	runtime.update(*instance, backend, handle)
	return nil
}

func rdsRotateBackendMasterPassword(runtime *rdsDataPlaneRuntime, newPassword string) error {
	instance, _, handle := runtime.snapshot()
	if handle == nil {
		return fmt.Errorf("database engine is not running")
	}
	database := instance.DBName
	if database == "" {
		database = instance.MasterUsername
	}
	var command []string
	if strings.HasPrefix(strings.ToLower(instance.Engine), "postgres") {
		statement := "ALTER ROLE " + rdsPostgreSQLQuoteIdentifier(instance.MasterUsername) +
			" WITH PASSWORD " + rdsSQLQuoteLiteral(newPassword)
		command = []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", instance.MasterUsername, "-d", database, "-c", statement}
	} else {
		backendSecret := instance.BackendMasterUserSecret
		if len(backendSecret) == 0 {
			backendSecret = instance.MasterUserSecret
		}
		_, oldPassword, decrypted := kmsDecryptBytes(backendSecret)
		if !decrypted {
			return fmt.Errorf("decrypt Amazon RDS master-user credential")
		}
		statement := "ALTER USER " + rdsSQLQuoteLiteral(instance.MasterUsername) +
			"@'%' IDENTIFIED BY " + rdsSQLQuoteLiteral(newPassword)
		client := "mysql"
		if strings.EqualFold(instance.Engine, "mariadb") {
			client = "mariadb"
		}
		command = []string{client, "--user=root", "--password=" + string(oldPassword), "--execute=" + statement}
	}
	execContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := sim.DockerClient().ExecCreate(execContext, handle.ContainerID, dockerclient.ExecCreateOptions{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("create database password rotation command: %w", err)
	}
	attached, err := sim.DockerClient().ExecAttach(execContext, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attach database password rotation command: %w", err)
	}
	_, _ = io.Copy(io.Discard, attached.Reader)
	attached.Close()
	inspected, err := sim.DockerClient().ExecInspect(execContext, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect database password rotation command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return fmt.Errorf("database engine rejected master-user password rotation with status %d", inspected.ExitCode)
	}
	return nil
}

func rdsPostgreSQLQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func rdsSQLQuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func rdsValidateIAMAuthToken(instance RDSInstance, user, token string) bool {
	parsed, err := url.Parse("https://" + token)
	if err != nil || parsed.Host != net.JoinHostPort(instance.Endpoint, strconv.Itoa(instance.Port)) {
		return false
	}
	query := parsed.Query()
	if query.Get("Action") != "connect" || query.Get("DBUser") != user {
		return false
	}
	credential, ok := parseCredScope(query.Get("X-Amz-Credential"))
	if !ok || credential.service != "rds-db" || credential.region != awsRegion() {
		return false
	}
	expires, err := strconv.Atoi(query.Get("X-Amz-Expires"))
	if err != nil || expires <= 0 || expires > 900 {
		return false
	}
	signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
	if err != nil || time.Now().Before(signedAt.Add(-5*time.Minute)) || time.Now().After(signedAt.Add(time.Duration(expires)*time.Second)) {
		return false
	}
	request := &http.Request{Method: http.MethodGet, URL: parsed, Host: parsed.Host, Header: make(http.Header)}
	request.Header.Set("X-Amz-Content-Sha256", rdsEmptyPayloadSHA256)
	result, signatureErr := sigv4VerifyPresigned(request, query, true)
	if signatureErr != nil || result != sigv4Verified {
		return false
	}
	request.Header.Set(
		"Authorization",
		fmt.Sprintf(
			"AWS4-HMAC-SHA256 Credential=%s, SignedHeaders=%s, Signature=%s",
			query.Get("X-Amz-Credential"),
			query.Get("X-Amz-SignedHeaders"),
			query.Get("X-Amz-Signature"),
		),
	)
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = "127.0.0.1:0"
	resource := fmt.Sprintf(
		"arn:aws:rds-db:%s:%s:dbuser:%s/%s",
		awsRegion(), awsAccountID(), instance.DbiResourceId, user,
	)
	allowed, _, registered := iamAuthorize(request, "rds-db:connect", resource)
	return !registered || allowed
}

func rdsPostgreSQLAuthError(connection net.Conn, message string) {
	payload := []byte("SERROR\x00C28P01\x00M" + message + "\x00\x00")
	packet := make([]byte, 5+len(payload))
	packet[0] = 'E'
	binary.BigEndian.PutUint32(packet[1:5], uint32(len(payload)+4))
	copy(packet[5:], payload)
	_, _ = connection.Write(packet)
}

func rdsRelay(left, right net.Conn) {
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

func rdsServerCertificate() (tls.Certificate, error) {
	rdsTLSOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			rdsTLSErr = err
			return
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(now.UnixNano()),
			Subject:      pkix.Name{CommonName: "Amazon RDS simulator"},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.AddDate(1, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			rdsTLSErr = err
			return
		}
		keyDER := x509.MarshalPKCS1PrivateKey(key)
		rdsTLSCert, rdsTLSErr = tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}),
		)
	})
	return rdsTLSCert, rdsTLSErr
}

func rdsStopDataPlane(instanceID string, deleteVolume bool) {
	value, ok := rdsDataPlanes.LoadAndDelete(instanceID)
	if !ok {
		return
	}
	runtime, ok := value.(*rdsDataPlaneRuntime)
	if !ok {
		return
	}
	_ = runtime.listener.Close()
	instance, _, handle := runtime.snapshot()
	if handle != nil {
		handle.Cancel()
		_ = handle.Wait()
		if deleteVolume {
			_ = sim.RemoveVolume("sockerless-rds-" + instance.DBInstanceIdentifier)
		}
	}
}

// serveMySQL is implemented in rds_dataplane_mysql.go.
func (runtime *rdsDataPlaneRuntime) serveMySQL(client net.Conn) {
	rdsServeMySQL(runtime, client)
}
