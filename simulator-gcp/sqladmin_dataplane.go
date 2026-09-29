package main

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
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
// into the engine as real roles with their declared passwords, and a session
// runs as the user the client named, not as a shared superuser. Credentials
// are sealed under a Google-owned Cloud KMS key from the simulator's own KMS
// slice — never stored in the clear.
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
func sqlEngineFamily(databaseVersion string) (dbengine.Family, bool) {
	switch {
	case strings.HasPrefix(databaseVersion, "POSTGRES_"):
		return dbengine.Postgres, true
	case strings.HasPrefix(databaseVersion, "MYSQL_"):
		return dbengine.MySQL, true
	}
	return "", false
}

func sqlEngine(family dbengine.Family) dbengine.Engine {
	if family == dbengine.Postgres {
		return dbengine.Postgres16.WithImage("public.ecr.aws/docker/library/postgres:16-alpine")
	}
	return dbengine.MySQL80.WithImage("public.ecr.aws/docker/library/mysql:8.0")
}

// sqlBuiltInAdminUser is the user the engine is initialised with — the one
// Cloud SQL creates from the insert request's rootPassword.
func sqlBuiltInAdminUser(family dbengine.Family) string {
	if family == dbengine.Postgres {
		return "postgres"
	}
	return "root"
}

func sqlInstanceVolume(project, instance string) string {
	return "sockerless-cloudsql-" + project + "-" + instance
}

var sqlServerCertificate = dbengine.SelfSignedCertificate("Cloud SQL simulator")

// sqlDataPlane is a Cloud SQL instance's endpoint and engine.
type sqlDataPlane struct {
	*dbengine.Instance
	project  string
	instance string
}

var sqlDataPlanes sync.Map // project/instance -> *sqlDataPlane

func sqlLoadDataPlane(project, instance string) (*sqlDataPlane, bool) {
	value, ok := sqlDataPlanes.Load(sqlInstanceKey(project, instance))
	if !ok {
		return nil, false
	}
	plane, ok := value.(*sqlDataPlane)
	return plane, ok
}

func sqlNewDataPlane(project, instance string, family dbengine.Family) *sqlDataPlane {
	plane := &sqlDataPlane{project: project, instance: instance}
	plane.Instance = &dbengine.Instance{
		Name:     "Cloud SQL " + project + "/" + instance,
		Engine:   sqlEngine(family),
		Volume:   sqlInstanceVolume(project, instance),
		Labels:   map[string]string{"sockerless-cloudsql-instance": project + "/" + instance},
		Sandbox:  SandboxCloudRun,
		Platform: dbengine.FixedPlatform("linux/amd64"),
		Environment: func() (map[string]string, error) {
			adminPassword, err := sqlEngineAdminPassword(project, instance, family)
			if err != nil {
				return nil, err
			}
			if family == dbengine.Postgres {
				return dbengine.PostgresEnvironment(sqlBuiltInAdminUser(family), adminPassword, "postgres"), nil
			}
			return map[string]string{"MYSQL_ROOT_PASSWORD": adminPassword}, nil
		},
		Ready: func() error {
			if err := sqlReconcileEngineState(plane); err != nil {
				return fmt.Errorf("reconcile declared users and databases into the engine: %w", err)
			}
			return nil
		},
		Certificate: sqlServerCertificate,
		Authenticate: func(user, password string, _ bool) bool {
			return sqlCredentialMatches(project, instance, user, password)
		},
		// The engine holds every declared user as a real account with the
		// declared password, so the session logs in as the client's own user.
		BackendLogin: func(user, password string) (string, string, error) {
			return user, password, nil
		},
	}
	sqlDataPlanes.Store(sqlInstanceKey(project, instance), plane)
	return plane
}

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
	listener, err := dbengine.ListenLoopback(inst.Project+"/"+inst.Name, sqlEngine(family).Port)
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
	sqlNewDataPlane(inst.Project, inst.Name, family).Serve(listener)
	return true, nil
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
		listener, err := net.Listen("tcp", net.JoinHostPort(recorded, strconv.Itoa(sqlEngine(family).Port)))
		if err != nil {
			return fmt.Errorf("rebind Cloud SQL instance %s at %s: %w", inst.Name, recorded, err)
		}
		plane := sqlNewDataPlane(inst.Project, inst.Name, family)
		if err := plane.Adopt(); err != nil {
			_ = listener.Close()
			return fmt.Errorf("re-adopt Cloud SQL instance %s engine: %w", inst.Name, err)
		}
		plane.Serve(listener)
	}
	return nil
}

// sqlEngineAdminPassword returns the built-in admin user's password —
// the rootPassword the insert request carried, sealed at rest.
func sqlEngineAdminPassword(project, instance string, family dbengine.Family) (string, error) {
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

// sqlReconcileEngineState creates or updates, inside the running engine,
// every user and database the Cloud SQL Admin API declares for the instance.
// It runs at engine readiness — first boot, control-plane restart, and after
// a restore replaced the data directory — and after control-plane mutations
// while the engine is up, so the API and the engine never disagree.
func sqlReconcileEngineState(plane *sqlDataPlane) error {
	for _, u := range sqlUsers.List() {
		if u.Project != plane.project || u.Instance != plane.instance {
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
		if err := sqlEngineEnsureUser(plane, u.Name, password); err != nil {
			return fmt.Errorf("ensure user %s: %w", u.Name, err)
		}
	}
	for _, d := range sqlDatabases.List() {
		if d.Project != plane.project || d.Instance != plane.instance {
			continue
		}
		if err := sqlEngineEnsureDatabase(plane, d.Name); err != nil {
			return fmt.Errorf("ensure database %s: %w", d.Name, err)
		}
	}
	return nil
}

func sqlPostgresCommand(statement string) []string {
	return []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", statement}
}

func (plane *sqlDataPlane) mysqlCommand(statement string) ([]string, error) {
	adminPassword, err := sqlEngineAdminPassword(plane.project, plane.instance, dbengine.MySQL)
	if err != nil {
		return nil, err
	}
	return []string{"mysql", "--user=root", "--password=" + adminPassword, "--execute=" + statement}, nil
}

func (plane *sqlDataPlane) execMySQL(statement string) error {
	command, err := plane.mysqlCommand(statement)
	if err != nil {
		return err
	}
	return plane.Exec(command)
}

// sqlEngineEnsureUser makes the engine hold the user with the declared
// password. PostgreSQL users are members of the admin role, mirroring Cloud
// SQL's cloudsqlsuperuser membership.
func sqlEngineEnsureUser(plane *sqlDataPlane, name, password string) error {
	if plane.Engine.Family == dbengine.Postgres {
		if name == sqlBuiltInAdminUser(dbengine.Postgres) {
			return plane.Exec(sqlPostgresCommand("ALTER ROLE " + dbengine.QuoteIdentifier(name) + " WITH LOGIN PASSWORD " + dbengine.QuoteLiteral(password)))
		}
		script := fmt.Sprintf(
			`DO $$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s; END IF; END$$; `+
				`ALTER ROLE %s WITH LOGIN CREATEDB CREATEROLE PASSWORD %s; GRANT %s TO %s;`,
			dbengine.QuoteLiteral(name), dbengine.QuoteIdentifier(name),
			dbengine.QuoteIdentifier(name), dbengine.QuoteLiteral(password),
			dbengine.QuoteIdentifier("postgres"), dbengine.QuoteIdentifier(name),
		)
		return plane.Exec(sqlPostgresCommand(script))
	}
	quotedPassword := dbengine.QuoteMySQLLiteral(password)
	if name == sqlBuiltInAdminUser(dbengine.MySQL) {
		return plane.execMySQL("ALTER USER 'root'@'%' IDENTIFIED BY " + quotedPassword + "; ALTER USER 'root'@'localhost' IDENTIFIED BY " + quotedPassword + "; FLUSH PRIVILEGES;")
	}
	account := dbengine.QuoteMySQLLiteral(name) + "@'%'"
	return plane.execMySQL("CREATE USER IF NOT EXISTS " + account + "; " +
		"ALTER USER " + account + " IDENTIFIED BY " + quotedPassword + "; " +
		"GRANT ALL PRIVILEGES ON *.* TO " + account + " WITH GRANT OPTION; FLUSH PRIVILEGES;")
}

func sqlEngineEnsureDatabase(plane *sqlDataPlane, name string) error {
	if plane.Engine.Family == dbengine.Postgres {
		check := "SELECT 1 FROM pg_database WHERE datname = " + dbengine.QuoteLiteral(name)
		create := "CREATE DATABASE " + dbengine.QuoteIdentifier(name)
		script := fmt.Sprintf(`[ -n "$(psql -U postgres -d postgres -tAc %s)" ] || psql -v ON_ERROR_STOP=1 -U postgres -d postgres -c %s`,
			dbengine.ShellQuote(check), dbengine.ShellQuote(create))
		return plane.Exec([]string{"/bin/sh", "-c", script})
	}
	return plane.execMySQL("CREATE DATABASE IF NOT EXISTS " + dbengine.QuoteMySQLIdentifier(name))
}

// sqlEngineDropUserIfRunning removes a deleted user's role from a running
// engine. An engine that is not running needs nothing: readiness reconciles
// only users that still exist, and a role in a cold data directory whose API
// user is gone cannot authenticate — the front proxy holds no credential.
func sqlEngineDropUserIfRunning(project, instance, name string) error {
	plane, running := sqlRunningDataPlane(project, instance)
	if !running {
		return nil
	}
	if plane.Engine.Family == dbengine.Postgres {
		return plane.Exec(sqlPostgresCommand("DROP ROLE IF EXISTS " + dbengine.QuoteIdentifier(name)))
	}
	return plane.execMySQL("DROP USER IF EXISTS " + dbengine.QuoteMySQLLiteral(name) + "@'%'")
}

// sqlEngineDropDatabaseIfRunning removes a deleted database from a running
// engine.
func sqlEngineDropDatabaseIfRunning(project, instance, name string) error {
	plane, running := sqlRunningDataPlane(project, instance)
	if !running {
		return nil
	}
	if plane.Engine.Family == dbengine.Postgres {
		return plane.Exec(sqlPostgresCommand("DROP DATABASE IF EXISTS " + dbengine.QuoteIdentifier(name) + " WITH (FORCE)"))
	}
	return plane.execMySQL("DROP DATABASE IF EXISTS " + dbengine.QuoteMySQLIdentifier(name))
}

func sqlRunningDataPlane(project, instance string) (*sqlDataPlane, bool) {
	plane, ok := sqlLoadDataPlane(project, instance)
	if !ok || !plane.Running() {
		return nil, false
	}
	return plane, true
}

// sqlReconcileIfRunning applies control-plane user/database mutations to a
// running engine immediately. An engine that is not running needs nothing:
// readiness reconciles the full declared state before serving any client.
func sqlReconcileIfRunning(project, instance string) error {
	plane, running := sqlRunningDataPlane(project, instance)
	if !running {
		return nil
	}
	return sqlReconcileEngineState(plane)
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
		if subtle.ConstantTimeCompare([]byte(stored), []byte(password)) == 1 {
			return true
		}
	}
	return false
}

// sqlStopEngine stops the instance's engine container, leaving the listener
// and the volume in place; the next client connection starts a fresh engine.
// The restore path uses it to swap the data directory underneath.
func sqlStopEngine(project, instance string) {
	if plane, ok := sqlLoadDataPlane(project, instance); ok {
		if err := plane.Stop(); err != nil {
			log.Printf("Cloud SQL %s/%s: stop database engine: %v", project, instance, err)
		}
	}
}

// sqlStopDataPlane closes the instance's listener, stops its engine, and —
// when the instance is being deleted — removes its data volume.
func sqlStopDataPlane(project, instance string, deleteVolume bool) {
	value, ok := sqlDataPlanes.LoadAndDelete(sqlInstanceKey(project, instance))
	if !ok {
		return
	}
	plane, ok := value.(*sqlDataPlane)
	if !ok {
		return
	}
	if err := plane.Close(); err != nil {
		log.Printf("Cloud SQL %s/%s: stop database engine: %v", project, instance, err)
	}
	if deleteVolume {
		sim.RemoveVolumeSettled(sqlInstanceVolume(project, instance), "cloudsql")
	}
}
