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

// The presets name no image: which image a managed service runs is that
// cloud's choice, and each simulator's image scan must see it in its own tree.
var (
	Postgres16 = Engine{
		Family: Postgres,
		Port:   5432, DataPath: "/var/lib/postgresql/data", Client: "psql",
	}
	MySQL80 = Engine{
		Family: MySQL,
		Port:   3306, DataPath: "/var/lib/mysql", Client: "mysql",
		Args: []string{"--default-authentication-plugin=mysql_native_password"},
	}
	MariaDB114 = Engine{
		Family: MySQL,
		Port:   3306, DataPath: "/var/lib/mysql", Client: "mariadb",
	}
)

// WithImage is the engine run from image.
func (e Engine) WithImage(image string) Engine {
	e.Image = image
	return e
}

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
	// Log receives the engine container's output, each line dated by the
	// container runtime. An adopted container replays its whole output, so
	// the sink sees lines it already holds again. Nil discards the output.
	Log sim.LogSink

	mu        sync.RWMutex
	listeners []net.Listener
	backend   string
	handle    *sim.ContainerHandle
	// stopped closes when the engine behind handle stops, which ends a start
	// waiting on it.
	stopped chan struct{}
	// accepting records that the engine behind handle accepted clients.
	accepting bool

	startMu   sync.Mutex
	attempted bool
	startErr  error
}

// Serve accepts clients on listener until Close.
func (i *Instance) Serve(listener net.Listener) { i.serve(listener, false) }

// ServeReadOnly accepts clients on listener until Close and runs each session
// read-only in the engine, the way a replica serves it: PostgreSQL opens the
// session with default_transaction_read_only on and MySQL sets the session's
// transactions READ ONLY, so the engine itself refuses every write.
func (i *Instance) ServeReadOnly(listener net.Listener) { i.serve(listener, true) }

func (i *Instance) serve(listener net.Listener, readOnly bool) {
	i.mu.Lock()
	i.listeners = append(i.listeners, listener)
	i.mu.Unlock()
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go i.serveConnection(client, readOnly)
		}
	}()
}

// Close stops accepting clients and stops the engine; the volume stays.
func (i *Instance) Close() error {
	i.closeListeners()
	return i.Stop()
}

// Discard stops accepting clients and stops the engine of a resource being
// deleted, whose volume goes with it. An engine that has not yet accepted
// clients is killed rather than given the stop grace: the MySQL image's
// entrypoint ignores SIGTERM while it initialises the data directory, and a
// directory about to be removed needs no clean shutdown.
func (i *Instance) Discard() error {
	i.closeListeners()
	i.mu.RLock()
	handle, accepting := i.handle, i.accepting
	i.mu.RUnlock()
	if handle != nil && !accepting {
		sim.StopContainer(handle.ContainerID, 0)
	}
	return i.Stop()
}

func (i *Instance) closeListeners() {
	i.mu.Lock()
	listeners := i.listeners
	i.listeners = nil
	i.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
	}
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
	if !existing[0].Running {
		if err := sim.StartExistingContainer(existing[0].ID); err != nil {
			return fmt.Errorf("resume database engine container %s: %w", existing[0].ID, err)
		}
	}
	handle, err := sim.AdoptContainer(existing[0].ID, sim.ContainerConfig{CancelGracePeriod: stopGrace}, i.logSink())
	if err != nil {
		return err
	}
	backendPort, err := handle.PublishedPort(context.Background(), i.Engine.Port)
	if err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return fmt.Errorf("database engine container %s: %w", existing[0].ID, err)
	}
	i.mu.Lock()
	i.backend, i.handle = net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort)), handle
	i.stopped, i.accepting = make(chan struct{}), false
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
	i.mu.RLock()
	backend, handle, stopped := i.backend, i.handle, i.stopped
	i.mu.RUnlock()
	if handle == nil {
		var err error
		if backend, handle, stopped, err = i.start(); err != nil {
			return err
		}
	}
	if err := awaitReady(i.Engine, backend, handle, stopped); err != nil {
		_ = i.stopEngine()
		return err
	}
	i.mu.Lock()
	if i.handle == handle {
		i.accepting = true
	}
	i.mu.Unlock()
	if i.Ready != nil {
		if err := i.Ready(); err != nil {
			_ = i.stopEngine()
			return err
		}
	}
	return nil
}

func (i *Instance) start() (string, *sim.ContainerHandle, chan struct{}, error) {
	if i.Engine.Image == "" {
		return "", nil, nil, fmt.Errorf("%s: the database engine names no image", i.Name)
	}
	environment, err := i.Environment()
	if err != nil {
		return "", nil, nil, err
	}
	platform, err := i.Platform(context.Background(), i.Engine.Image)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve database engine platform: %w", err)
	}
	handle, err := sim.StartContainerSyncContext(context.Background(), sim.ContainerConfig{
		CancelGracePeriod: stopGrace,
		Image:             i.Engine.Image,
		Architecture:      platform,
		Args:              i.Engine.Args,
		Env:               environment,
		PublishPorts:      []int{i.Engine.Port},
		Binds:             []string{i.Volume + ":" + i.Engine.DataPath},
		Labels:            i.Labels,
		Sandbox:           i.Sandbox,
	}, i.logSink())
	if err != nil {
		return "", nil, nil, fmt.Errorf("start %s database engine: %w", i.Engine.Family, err)
	}
	backendPort, err := handle.PublishedPort(context.Background(), i.Engine.Port)
	if err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return "", nil, nil, fmt.Errorf("start %s database engine: %w", i.Engine.Family, err)
	}
	backend := net.JoinHostPort("127.0.0.1", strconv.Itoa(backendPort))
	stopped := make(chan struct{})
	i.mu.Lock()
	i.backend, i.handle, i.stopped, i.accepting = backend, handle, stopped, false
	i.mu.Unlock()
	return backend, handle, stopped, nil
}

func (i *Instance) logSink() sim.LogSink {
	if i.Log == nil {
		return sim.NoopSink{}
	}
	return i.Log
}

func awaitReady(engine Engine, backend string, handle *sim.ContainerHandle, stopped <-chan struct{}) error {
	deadline := time.Now().Add(initializationBudget)
	nextLivenessCheck := time.Now().Add(livenessInterval)
	for !serving(engine.Family, backend) {
		select {
		case <-stopped:
			return fmt.Errorf("%s database engine stopped before accepting connections", engine.Family)
		default:
		}
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
		select {
		case <-stopped:
			return fmt.Errorf("%s database engine stopped before accepting connections", engine.Family)
		case <-time.After(probeInterval):
		}
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
	handle, stopped := i.handle, i.stopped
	i.backend, i.handle, i.stopped, i.accepting = "", nil, nil, false
	i.mu.Unlock()
	if handle == nil {
		return nil
	}
	close(stopped)
	handle.Cancel()
	_ = handle.Wait()
	return sim.WaitContainerRemoved(handle.ContainerID, removalTimeout)
}

// Exec runs command inside the running engine container and fails with the
// command's output when it exits non-zero. A command that lands while a
// volume capture holds the engine frozen waits for the capture, which bounds
// the freeze, to thaw it.
func (i *Instance) Exec(command []string) error {
	_, handle := i.snapshot()
	if handle == nil {
		return fmt.Errorf("database engine is not running")
	}
	release, err := sim.HoldThawed(context.Background(), handle.ContainerID)
	if err != nil {
		return err
	}
	defer release()
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

func (i *Instance) serveConnection(client net.Conn, readOnly bool) {
	defer client.Close()
	if err := i.Ensure(); err != nil {
		log.Printf("%s data plane: %v", i.Name, err)
		return
	}
	backendAddress, _ := i.snapshot()
	var err error
	if i.Engine.Family == Postgres {
		err = i.servePostgres(client, backendAddress, readOnly)
	} else {
		err = i.serveMySQL(client, backendAddress, readOnly)
	}
	if err != nil {
		log.Printf("%s %s session: %v", i.Name, i.Engine.Family, err)
	}
}

func (i *Instance) servePostgres(client net.Conn, backendAddress string, readOnly bool) error {
	frontend := pgwire.Frontend{
		Certificate:     i.Certificate,
		RefusePlaintext: i.RefusePlaintext,
		Authenticate:    i.Authenticate,
		ReadOnly:        readOnly,
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

func (i *Instance) serveMySQL(client net.Conn, backendAddress string, readOnly bool) error {
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial engine: %w", err)
	}
	defer backend.Close()
	frontend := mysqlwire.Frontend{
		Certificate:  i.Certificate,
		Authenticate: i.Authenticate,
		BackendLogin: i.BackendLogin,
		ReadOnly:     readOnly,
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
