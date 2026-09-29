// Package dbengine runs the data plane of a managed relational database: a
// real PostgreSQL, MySQL or MariaDB engine in a container whose data directory
// is a named volume, behind an endpoint listener the simulator owns. The
// endpoint terminates TLS and authenticates each client through the cloud's
// own hook, then relays the session to the engine. Which credentials are
// valid, how they are sealed at rest, and what state the control plane
// reconciles into the engine stay in each cloud's module.
package dbengine

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine/mysqlwire"
	"github.com/e6qu/sockerless-cloud/sim/dbengine/pgwire"
)

type Family string

const (
	Postgres Family = "postgres"
	MySQL    Family = "mysql"
)

// Engine is a database engine image and the facts about it the data plane
// depends on.
type Engine struct {
	Family   Family
	Image    string
	Port     int
	DataPath string
	Args     []string
	// Client is the engine's command-line client inside the image.
	Client string
}

var (
	Postgres16 = Engine{
		Family: Postgres, Image: "public.ecr.aws/docker/library/postgres:16-alpine",
		Port: 5432, DataPath: "/var/lib/postgresql/data", Client: "psql",
	}
	MySQL80 = Engine{
		Family: MySQL, Image: "public.ecr.aws/docker/library/mysql:8.0",
		Port: 3306, DataPath: "/var/lib/mysql", Client: "mysql",
		Args: []string{"--default-authentication-plugin=mysql_native_password"},
	}
	MariaDB114 = Engine{
		Family: MySQL, Image: "public.ecr.aws/docker/library/mariadb:11.4",
		Port: 3306, DataPath: "/var/lib/mysql", Client: "mariadb",
	}
)

// PostgresEnvironment initialises a PostgreSQL engine whose superuser is user.
// The engine trusts every connection because the endpoint authenticated the
// client already: relaying the client's own startup then runs the session as
// the user the client named without the endpoint replaying a password.
func PostgresEnvironment(user, password, database string) map[string]string {
	return map[string]string{
		"POSTGRES_USER":             user,
		"POSTGRES_PASSWORD":         password,
		"POSTGRES_DB":               database,
		"POSTGRES_HOST_AUTH_METHOD": "trust",
	}
}

// FixedPlatform runs the engine image for one platform whatever the host is.
func FixedPlatform(platform string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return platform, nil }
}

const (
	// initializationBudget bounds the wait for an engine to accept clients. A
	// real engine's first boot lays down its whole data directory before it
	// listens, and a busy host stretches that well past a minute; the wait
	// ends as soon as the container stops, so a broken engine still fails at
	// once rather than burning the budget.
	initializationBudget = 10 * time.Minute
	// livenessInterval spaces the container-state reads the wait makes between
	// 100 ms readiness probes.
	livenessInterval = 2 * time.Second
	probeInterval    = 100 * time.Millisecond
	stopGrace        = 5 * time.Second
	removalTimeout   = 30 * time.Second
	execTimeout      = 30 * time.Second
)

// Instance is one database instance's data plane: its endpoint listener and
// the engine container behind it. The engine starts on the first client
// connection, or is adopted from an earlier control-plane process.
type Instance struct {
	// Name prefixes the data plane's log lines, e.g. "Amazon RDS db-1".
	Name     string
	Engine   Engine
	Volume   string
	Labels   map[string]string
	Sandbox  sim.SandboxProfile
	Platform func(ctx context.Context, image string) (string, error)
	// Environment is read at every engine start.
	Environment func() (map[string]string, error)
	// Ready runs once the engine accepts clients and before the first client
	// is relayed, to reconcile what the control plane recorded while the
	// engine was down.
	Ready func() error

	Certificate  func() (tls.Certificate, error)
	Authenticate func(user, password string, secure bool) bool
	// RefusePlaintext applies to PostgreSQL clients that did not ask for TLS.
	RefusePlaintext func() (message string, refuse bool)
	// BackendLogin names the engine account a MySQL-family session logs in as.
	BackendLogin func(user, password string) (backendUser, backendPassword string, err error)

	listener net.Listener

	mu      sync.RWMutex
	backend string
	handle  *sim.ContainerHandle

	startMu   sync.Mutex
	attempted bool
	startErr  error
}

// Serve accepts clients on listener until Close.
func (i *Instance) Serve(listener net.Listener) {
	i.listener = listener
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go i.serveConnection(client)
		}
	}()
}

// Close stops accepting clients and stops the engine; the volume stays.
func (i *Instance) Close() error {
	if i.listener != nil {
		_ = i.listener.Close()
	}
	return i.Stop()
}

// Running reports whether the engine container is up.
func (i *Instance) Running() bool {
	_, handle := i.snapshot()
	return handle != nil
}

func (i *Instance) snapshot() (string, *sim.ContainerHandle) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.backend, i.handle
}

// Adopt picks up the engine container an earlier control-plane process left
// for this instance, resuming it when it had stopped.
func (i *Instance) Adopt() error {
	existing, err := sim.FindExistingContainers(i.Labels)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	if len(existing) != 1 {
		return fmt.Errorf("found %d database engine containers", len(existing))
	}
	backendPort := existing[0].PublishedPorts[i.Engine.Port]
	if backendPort == 0 {
		return fmt.Errorf("container %s has no published database port %d", existing[0].ID, i.Engine.Port)
	}
	if !existing[0].Running {
		if err := sim.StartExistingContainer(existing[0].ID); err != nil {
			return fmt.Errorf("resume database engine container %s: %w", existing[0].ID, err)
		}
	}
	handle, err := sim.AdoptContainer(existing[0].ID, sim.ContainerConfig{CancelGracePeriod: stopGrace}, sim.NoopSink{})
	if err != nil {
		return err
	}
	i.mu.Lock()
	i.backend, i.handle = net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle
	i.mu.Unlock()
	return nil
}

// Ensure brings the engine up and does not return until it accepts clients
// and Ready has run. An endpoint that relayed a client before then would
// answer with the engine's own startup refusal, which a managed database
// reporting itself available never does. The outcome holds until Stop.
func (i *Instance) Ensure() error {
	i.startMu.Lock()
	defer i.startMu.Unlock()
	if !i.attempted {
		i.attempted = true
		i.startErr = i.bringUp()
	}
	return i.startErr
}

func (i *Instance) bringUp() error {
	backend, handle := i.snapshot()
	if handle == nil {
		var err error
		if backend, handle, err = i.start(); err != nil {
			return err
		}
	}
	if err := awaitReady(i.Engine, backend, handle); err != nil {
		_ = i.stopEngine()
		return err
	}
	if i.Ready != nil {
		if err := i.Ready(); err != nil {
			_ = i.stopEngine()
			return err
		}
	}
	return nil
}

func (i *Instance) start() (string, *sim.ContainerHandle, error) {
	environment, err := i.Environment()
	if err != nil {
		return "", nil, err
	}
	platform, err := i.Platform(context.Background(), i.Engine.Image)
	if err != nil {
		return "", nil, fmt.Errorf("resolve database engine platform: %w", err)
	}
	backendPort, err := reservePort()
	if err != nil {
		return "", nil, err
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		CancelGracePeriod: stopGrace,
		Image:             i.Engine.Image,
		Architecture:      platform,
		Args:              i.Engine.Args,
		Env:               environment,
		PublishPorts:      map[int]int{i.Engine.Port: backendPort},
		Binds:             []string{i.Volume + ":" + i.Engine.DataPath},
		Labels:            i.Labels,
		Sandbox:           i.Sandbox,
	}, sim.NoopSink{})
	if err != nil {
		return "", nil, fmt.Errorf("start %s database engine: %w", i.Engine.Family, err)
	}
	backend := net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort))
	i.mu.Lock()
	i.backend, i.handle = backend, handle
	i.mu.Unlock()
	return backend, handle, nil
}

func reservePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate database engine port: %w", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return 0, fmt.Errorf("database engine listener returned address type %T", listener.Addr())
	}
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release database engine port: %w", err)
	}
	return address.Port, nil
}

func awaitReady(engine Engine, backend string, handle *sim.ContainerHandle) error {
	deadline := time.Now().Add(initializationBudget)
	nextLivenessCheck := time.Now().Add(livenessInterval)
	for !serving(engine.Family, backend) {
		now := time.Now()
		if !now.Before(nextLivenessCheck) {
			if !sim.ContainerRunning(handle.ContainerID) {
				return fmt.Errorf("%s database engine stopped before accepting connections: container %s is not running", engine.Family, handle.ContainerID)
			}
			nextLivenessCheck = now.Add(livenessInterval)
		}
		if !now.Before(deadline) {
			return fmt.Errorf("%s database engine did not become ready within %s", engine.Family, initializationBudget)
		}
		time.Sleep(probeInterval)
	}
	return nil
}

func serving(family Family, address string) bool {
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if family == Postgres {
		return pgwire.AcceptsConnections(connection, "postgres", "postgres")
	}
	return mysqlwire.Greets(connection)
}

// Stop stops the engine and forgets the outcome of the last start, so the
// next client starts a fresh engine on the same volume. It returns once the
// container is gone and its volume is free to remove or replace.
func (i *Instance) Stop() error {
	// Stopping the running engine first ends a start that is waiting on it.
	firstErr := i.stopEngine()
	i.startMu.Lock()
	defer i.startMu.Unlock()
	err := i.stopEngine()
	i.attempted, i.startErr = false, nil
	if firstErr != nil {
		return firstErr
	}
	return err
}

func (i *Instance) stopEngine() error {
	i.mu.Lock()
	handle := i.handle
	i.backend, i.handle = "", nil
	i.mu.Unlock()
	if handle == nil {
		return nil
	}
	handle.Cancel()
	_ = handle.Wait()
	return sim.WaitContainerRemoved(handle.ContainerID, removalTimeout)
}

// Exec runs command inside the running engine container and fails with the
// command's output when it exits non-zero.
func (i *Instance) Exec(command []string) error {
	_, handle := i.snapshot()
	if handle == nil {
		return fmt.Errorf("database engine is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	docker := sim.DockerClient()
	created, err := docker.ExecCreate(ctx, handle.ContainerID, dockerclient.ExecCreateOptions{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("create engine command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attach engine command: %w", err)
	}
	output, readErr := io.ReadAll(attached.Reader)
	attached.Close()
	if readErr != nil {
		return fmt.Errorf("read engine command output: %w", readErr)
	}
	inspected, err := docker.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return fmt.Errorf("engine command exited %d: %s", inspected.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}

func (i *Instance) serveConnection(client net.Conn) {
	defer client.Close()
	if err := i.Ensure(); err != nil {
		log.Printf("%s data plane: %v", i.Name, err)
		return
	}
	backendAddress, _ := i.snapshot()
	var err error
	if i.Engine.Family == Postgres {
		err = i.servePostgres(client, backendAddress)
	} else {
		err = i.serveMySQL(client, backendAddress)
	}
	if err != nil {
		log.Printf("%s %s session: %v", i.Name, i.Engine.Family, err)
	}
}

func (i *Instance) servePostgres(client net.Conn, backendAddress string) error {
	frontend := pgwire.Frontend{
		Certificate:     i.Certificate,
		RefusePlaintext: i.RefusePlaintext,
		Authenticate:    i.Authenticate,
	}
	startup, session, err := frontend.Accept(client)
	if err != nil {
		return err
	}
	if session != client {
		defer session.Close()
	}
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial engine: %w", err)
	}
	defer backend.Close()
	if _, err := backend.Write(startup); err != nil {
		return fmt.Errorf("forward startup packet: %w", err)
	}
	relay(session, backend)
	return nil
}

func (i *Instance) serveMySQL(client net.Conn, backendAddress string) error {
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial engine: %w", err)
	}
	defer backend.Close()
	frontend := mysqlwire.Frontend{
		Certificate:  i.Certificate,
		Authenticate: i.Authenticate,
		BackendLogin: i.BackendLogin,
	}
	session, err := frontend.Accept(client, backend)
	if err != nil {
		return err
	}
	if session != client {
		defer session.Close()
	}
	relay(session, backend)
	return nil
}

// relay copies both directions until either side finishes, half-closing the
// other side's write direction so a client's EOF reaches the engine.
func relay(left, right net.Conn) {
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

// ListenLoopback binds port on a loopback address derived from identifier, so
// every instance serves the engine's port on an address of its own. The port
// is the one the control plane advertises, so no other port stands in for it:
// when no loopback address offers it, the error says so.
func ListenLoopback(identifier string, port int) (net.Listener, error) {
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
	// Hosts without loopback aliases (macOS without root) have only 127.0.0.1.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return listener, nil
	}
	return nil, fmt.Errorf("no loopback address offers port %d (last error: %v)", port, lastErr)
}

// SelfSignedCertificate returns the TLS certificate an endpoint presents,
// generated once on first use.
func SelfSignedCertificate(commonName string) func() (tls.Certificate, error) {
	return sync.OnceValues(func() (tls.Certificate, error) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return tls.Certificate{}, err
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(now.UnixNano()),
			Subject:      pkix.Name{CommonName: commonName},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.AddDate(1, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			return tls.Certificate{}, err
		}
		return tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		)
	})
}

// QuoteIdentifier quotes a PostgreSQL identifier.
func QuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func QuoteMySQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

// QuoteLiteral quotes a PostgreSQL string literal; standard_conforming_strings
// leaves backslashes literal.
func QuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

// QuoteMySQLLiteral quotes a MySQL string literal. MySQL treats a backslash in
// a literal as an escape unless NO_BACKSLASH_ESCAPES is set, so a backslash is
// doubled as well as a quote.
func QuoteMySQLLiteral(value string) string {
	return `'` + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value) + `'`
}

// ShellQuote quotes a word for /bin/sh.
func ShellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'\''`) + `'`
}
