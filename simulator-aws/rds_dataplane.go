package main

import (
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

const rdsAWSOwnedKMSKeyID = "aws-owned-rds"
const rdsEmptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var rdsServerCertificate = dbengine.SelfSignedCertificate("Amazon RDS simulator")

// rdsDataPlane is an Amazon RDS instance's endpoint and engine, with the
// instance record its authentication and engine start read.
type rdsDataPlane struct {
	engine   *dbengine.Instance
	backups  *rdsAutomatedBackups
	mu       sync.RWMutex
	instance RDSInstance
}

func (plane *rdsDataPlane) current() RDSInstance {
	plane.mu.RLock()
	defer plane.mu.RUnlock()
	return plane.instance
}

func (plane *rdsDataPlane) record(instance RDSInstance) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	plane.instance = instance
}

var rdsDataPlanes sync.Map

func rdsLoadDataPlane(instanceID string) (*rdsDataPlane, bool) {
	value, ok := rdsDataPlanes.Load(instanceID)
	if !ok {
		return nil, false
	}
	plane, ok := value.(*rdsDataPlane)
	return plane, ok
}

func rdsRecoverDataPlanes() error {
	if err := rdsRecoverAuroraDataPlanes(); err != nil {
		return err
	}
	for _, instance := range rdsInstances.List() {
		if rdsIsAurora(instance.Engine) && instance.DBClusterIdentifier != "" {
			// A member of a cluster that holds no master-user credential, which
			// only a cluster restored from Amazon S3 lacks, has no cluster
			// volume to front.
			if _, served := rdsLoadAuroraDataPlane(instance.DBClusterIdentifier); served && instance.DBInstanceStatus == "available" {
				if err := rdsInstallAuroraInstanceEndpoint(&instance); err != nil {
					return fmt.Errorf("restore DB instance %s: %w", instance.DBInstanceIdentifier, err)
				}
				rdsInstances.Put(instance.DBInstanceIdentifier, instance)
			}
			continue
		}
		if instance.DBInstanceStatus == "creating" && (instance.RestoreSourceVolume != "" || instance.ImportS3Bucket != "") {
			id := instance.DBInstanceIdentifier
			bg.Go(func() { rdsFinishInstanceRestore(id) })
			continue
		}
		stopping := instance.DBInstanceStatus == "stopping"
		if stopping && len(instance.MasterUserSecret) == 0 {
			id := instance.DBInstanceIdentifier
			bg.Go(func() { rdsFinishStop(id) })
			continue
		}
		if (instance.DBInstanceStatus != "available" && !stopping) || len(instance.MasterUserSecret) == 0 {
			continue
		}
		_, masterPassword, ok := kmsDecryptBytes(instance.MasterUserSecret)
		if !ok {
			return fmt.Errorf("decrypt master-user credential for DB instance %s", instance.DBInstanceIdentifier)
		}
		if err := rdsInstallDataPlane(&instance, string(masterPassword)); err != nil {
			return fmt.Errorf("restore DB instance %s: %w", instance.DBInstanceIdentifier, err)
		}
		if plane, installed := rdsLoadDataPlane(instance.DBInstanceIdentifier); installed {
			if err := plane.engine.Adopt(); err != nil {
				return fmt.Errorf("restore DB instance %s backend: %w", instance.DBInstanceIdentifier, err)
			}
		}
		rdsInstances.Put(instance.DBInstanceIdentifier, instance)
		if stopping {
			// The process that took the StopDBInstance ended before the
			// engine it adopted here had stopped.
			id := instance.DBInstanceIdentifier
			bg.Go(func() { rdsFinishStop(id) })
		}
	}
	return nil
}

// rdsEngine names the engine an RDS engine runs. Aurora PostgreSQL 16 and
// Aurora MySQL 3 are compatible with PostgreSQL 16 and MySQL 8.0.
func rdsEngine(engine string) (dbengine.Engine, bool) {
	switch {
	case strings.HasPrefix(strings.ToLower(engine), "postgres"), strings.EqualFold(engine, "aurora-postgresql"):
		return dbengine.Postgres16.WithImage("public.ecr.aws/docker/library/postgres:16-alpine"), true
	case strings.EqualFold(engine, "mysql"), strings.EqualFold(engine, "aurora-mysql"):
		return dbengine.MySQL80.WithImage("public.ecr.aws/docker/library/mysql:8.0"), true
	case strings.EqualFold(engine, "mariadb"):
		return dbengine.MariaDB114.WithImage("public.ecr.aws/docker/library/mariadb:11.4"), true
	default:
		return dbengine.Engine{}, false
	}
}

// rdsSealMasterPassword encrypts a master-user password under the AWS owned
// RDS key.
func rdsSealMasterPassword(password string) ([]byte, error) {
	if _, ok := kmsGetKeyMaterial(rdsAWSOwnedKMSKeyID); !ok {
		if _, err := kmsGenerateKeyMaterial(rdsAWSOwnedKMSKeyID); err != nil {
			return nil, fmt.Errorf("generate AWS owned RDS key: %w", err)
		}
	}
	ciphertext, ok := kmsEncryptBytes(rdsAWSOwnedKMSKeyID, []byte(password))
	if !ok {
		return nil, fmt.Errorf("encrypt RDS master-user credential")
	}
	return ciphertext, nil
}

func rdsInstallDataPlane(instance *RDSInstance, masterPassword string) error {
	engine, ok := rdsLoggingEngine(instance.Engine)
	if !ok {
		return nil
	}
	if masterPassword == "" {
		return fmt.Errorf("MasterUserPassword is required for the %s data plane", instance.Engine)
	}
	if len(instance.MasterUserSecret) == 0 {
		sealed, err := rdsSealMasterPassword(masterPassword)
		if err != nil {
			return err
		}
		instance.MasterUserSecret = sealed
	}
	if len(instance.BackendMasterUserSecret) == 0 {
		instance.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
	}

	listener, err := rdsListenForEndpoint(instance.Endpoint, instance.Port, instance.DBInstanceIdentifier)
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
	plane := &rdsDataPlane{instance: *instance}
	plane.engine = &dbengine.Instance{
		Name:         "Amazon RDS " + instance.DBInstanceIdentifier,
		Engine:       engine,
		Volume:       rdsInstanceVolume(instance.DBInstanceIdentifier),
		Labels:       map[string]string{"sockerless-rds-instance": instance.DBInstanceIdentifier},
		Sandbox:      SandboxFargate,
		Platform:     dbengine.FixedPlatform("linux/amd64"),
		Environment:  plane.environment,
		Ready:        plane.ready,
		Certificate:  rdsServerCertificate,
		Authenticate: plane.authenticate,
		BackendLogin: plane.backendLogin,
		Log:          newRDSEngineLogSink(instance.DbiResourceId),
	}
	plane.backups = &rdsAutomatedBackups{
		owner:  rdsInstanceBackups{instanceID: instance.DBInstanceIdentifier},
		engine: plane.engine,
		volume: rdsInstanceVolume(instance.DBInstanceIdentifier),
	}
	rdsDataPlanes.Store(instance.DBInstanceIdentifier, plane)
	plane.backups.schedule()
	plane.engine.Serve(listener)
	return nil
}

// rdsListenForEndpoint rebinds an endpoint already advertised, and otherwise
// binds the requested port on a loopback address of its own.
func rdsListenForEndpoint(address string, port int, identifier string) (net.Listener, error) {
	if endpointIP := net.ParseIP(address); endpointIP != nil && endpointIP.IsLoopback() && port > 0 {
		return net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(port)))
	}
	return dbengine.ListenLoopback(identifier, port)
}

func rdsDatabaseName(instance RDSInstance) string {
	if instance.DBName != "" {
		return instance.DBName
	}
	return instance.MasterUsername
}

// backendPassword is the master-user password installed in the engine, which
// lags the recorded one while a ModifyDBInstance waits for the engine to run.
func (plane *rdsDataPlane) backendPassword() (string, error) {
	instance := plane.current()
	secret := instance.BackendMasterUserSecret
	if len(secret) == 0 {
		secret = instance.MasterUserSecret
	}
	_, password, ok := kmsDecryptBytes(secret)
	if !ok {
		return "", fmt.Errorf("RDS master-user credential could not be decrypted")
	}
	return string(password), nil
}

func (plane *rdsDataPlane) environment() (map[string]string, error) {
	instance := plane.current()
	password, err := plane.backendPassword()
	if err != nil {
		return nil, err
	}
	return rdsEngineEnvironment(plane.engine.Engine, instance.MasterUsername, password, rdsDatabaseName(instance)), nil
}

func rdsEngineEnvironment(engine dbengine.Engine, user, password, database string) map[string]string {
	switch engine.Client {
	case dbengine.MariaDB114.Client:
		return map[string]string{
			"MARIADB_USER":          user,
			"MARIADB_PASSWORD":      password,
			"MARIADB_ROOT_PASSWORD": password,
			"MARIADB_DATABASE":      database,
		}
	case dbengine.MySQL80.Client:
		return map[string]string{
			"MYSQL_USER":          user,
			"MYSQL_PASSWORD":      password,
			"MYSQL_ROOT_PASSWORD": password,
			"MYSQL_DATABASE":      database,
		}
	default:
		return dbengine.PostgresEnvironment(user, password, database)
	}
}

// ready reconciles the master password and takes the instance's first
// automated backup on the engine's first start.
func (plane *rdsDataPlane) ready() error {
	if err := plane.applyPendingMasterPassword(); err != nil {
		return err
	}
	return plane.backups.takeFirst()
}

// applyPendingMasterPassword installs the master-user password the control
// plane recorded while the engine was not running.
func (plane *rdsDataPlane) applyPendingMasterPassword() error {
	instance := plane.current()
	if bytes.Equal(instance.BackendMasterUserSecret, instance.MasterUserSecret) {
		return nil
	}
	_, desiredPassword, decrypted := kmsDecryptBytes(instance.MasterUserSecret)
	if !decrypted {
		return fmt.Errorf("decrypt pending Amazon RDS master-user credential")
	}
	if err := rdsRotateBackendMasterPassword(plane, string(desiredPassword)); err != nil {
		return fmt.Errorf("apply pending Amazon RDS master-user password: %w", err)
	}
	instance.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
	plane.record(instance)
	rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
		stored.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
	})
	return nil
}

// authenticate accepts the master user's password, or — inside TLS on an
// instance with IAM database authentication enabled — an RDS IAM
// authentication token.
func (plane *rdsDataPlane) authenticate(user, password string, secure bool) bool {
	instance := plane.current()
	_, masterPassword, ok := kmsDecryptBytes(instance.MasterUserSecret)
	if ok && user == instance.MasterUsername && subtle.ConstantTimeCompare([]byte(password), masterPassword) == 1 {
		return true
	}
	return secure && instance.EnableIAMDatabaseAuthentication &&
		rdsValidateIAMAuthToken([]string{net.JoinHostPort(instance.Endpoint, strconv.Itoa(instance.Port))}, instance.DbiResourceId, user, password)
}

// backendLogin runs every MySQL-family session as the master user: an IAM
// authentication token is no engine credential.
func (plane *rdsDataPlane) backendLogin(string, string) (string, string, error) {
	password, err := plane.backendPassword()
	return plane.current().MasterUsername, password, err
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
	plane, ok := rdsLoadDataPlane(instance.DBInstanceIdentifier)
	if !ok {
		return nil
	}
	if newPassword != nil {
		if plane.engine.Running() {
			if err := rdsRotateBackendMasterPassword(plane, *newPassword); err != nil {
				return err
			}
			instance.BackendMasterUserSecret = append([]byte(nil), instance.MasterUserSecret...)
		} else {
			instance.BackendMasterUserSecret = append([]byte(nil), plane.current().BackendMasterUserSecret...)
		}
	}
	plane.record(*instance)
	return nil
}

func rdsRotateBackendMasterPassword(plane *rdsDataPlane, newPassword string) error {
	instance := plane.current()
	oldPassword, err := plane.backendPassword()
	if err != nil {
		return err
	}
	return rdsRotateEnginePassword(plane.engine, instance.MasterUsername, rdsDatabaseName(instance), oldPassword, newPassword)
}

// rdsRotateEnginePassword changes the master user's password inside a running
// engine. A MySQL-family engine's root account shares the master password, and
// the next rotation logs in as root with it, so root moves along.
func rdsRotateEnginePassword(engine *dbengine.Instance, user, database, oldPassword, newPassword string) error {
	var command []string
	if engine.Engine.Family == dbengine.Postgres {
		statement := "ALTER ROLE " + dbengine.QuoteIdentifier(user) +
			" WITH PASSWORD " + dbengine.QuoteLiteral(newPassword)
		command = []string{engine.Engine.Client, "-v", "ON_ERROR_STOP=1", "-U", user, "-d", database, "-c", statement}
	} else {
		command = []string{engine.Engine.Client, "--user=root", "--password=" + oldPassword, "--execute=" + rdsMySQLSetMasterPasswordStatement(user, newPassword)}
	}
	if err := engine.Exec(command); err != nil {
		return fmt.Errorf("rotate the master-user password in the engine: %w", err)
	}
	return nil
}

// rdsMySQLSetMasterPasswordStatement sets the master user's password and
// root's, which the engine's image created with the same password.
func rdsMySQLSetMasterPasswordStatement(user, password string) string {
	identified := " IDENTIFIED BY " + dbengine.QuoteMySQLLiteral(password)
	return "ALTER USER IF EXISTS " + dbengine.QuoteMySQLLiteral(user) + "@'%'" + identified +
		"; ALTER USER IF EXISTS 'root'@'%'" + identified +
		"; ALTER USER IF EXISTS 'root'@'localhost'" + identified
}

// rdsValidateIAMAuthToken accepts a token signed for one of endpoints that
// grants rds-db:connect on resourceID's database user.
func rdsValidateIAMAuthToken(endpoints []string, resourceID, user, token string) bool {
	parsed, err := url.Parse("https://" + token)
	if err != nil || !slices.Contains(endpoints, parsed.Host) {
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
		awsRegion(), awsAccountID(), resourceID, user,
	)
	allowed, _, registered := iamAuthorize(request, "rds-db:connect", resource)
	return !registered || allowed
}

// rdsStartInstanceEngine reinstalls a stopped instance's endpoint and engine
// with its recorded master-user credential.
func rdsStartInstanceEngine(instance *RDSInstance) error {
	if rdsIsAurora(instance.Engine) {
		return rdsInstallAuroraInstanceEndpoint(instance)
	}
	if len(instance.MasterUserSecret) == 0 {
		return nil
	}
	_, password, decrypted := kmsDecryptBytes(instance.MasterUserSecret)
	if !decrypted {
		return fmt.Errorf("RDS master-user credential could not be decrypted")
	}
	return rdsInstallDataPlane(instance, string(password))
}

// rdsDataPlaneStops serializes the stops of one instance's data plane, so a
// DeleteDBInstance that arrives while a StopDBInstance is still stopping the
// engine removes the volume only once the engine has let go of it.
var rdsDataPlaneStops = sim.NewKeyedLocks()

// rdsStopDataPlane closes the instance's endpoint and stops its engine and,
// when the instance is being deleted, removes its data volume — which exists
// from the first engine start or from a snapshot restore's clone. It returns
// the engine's stop error.
func rdsStopDataPlane(instanceID string, deleteVolume bool) error {
	release := rdsDataPlaneStops.Lock(instanceID)
	defer release()
	rdsCloseAuroraInstanceEndpoint(instanceID)
	var stopErr error
	if value, ok := rdsDataPlanes.LoadAndDelete(instanceID); ok {
		if plane, ok := value.(*rdsDataPlane); ok {
			plane.backups.stop()
			if err := plane.engine.Close(); err != nil {
				stopErr = fmt.Errorf("stop database engine: %w", err)
				log.Printf("Amazon RDS %s: %v", instanceID, stopErr)
			}
		}
	}
	if deleteVolume {
		rdsRemoveEngineContainers("Amazon RDS "+instanceID, map[string]string{"sockerless-rds-instance": instanceID})
	}
	if deleteVolume && sim.VolumeExists(rdsInstanceVolume(instanceID)) {
		if err := sim.RemoveVolume(rdsInstanceVolume(instanceID)); err != nil {
			log.Printf("Amazon RDS %s: remove data volume: %v", instanceID, err)
		}
	}
	return stopErr
}

// rdsRemoveEngineContainers removes the engine containers an earlier process
// left for a resource being deleted, which no data plane of this process
// holds and which would otherwise keep its volume in use.
func rdsRemoveEngineContainers(name string, labels map[string]string) {
	if sim.RequireContainerRuntime("removing "+name+"'s engine") != nil {
		return
	}
	engines, err := sim.FindExistingContainers(labels)
	if err != nil {
		log.Printf("%s: list engine containers: %v", name, err)
		return
	}
	for _, engine := range engines {
		if err := sim.RemoveExistingContainer(engine.ID); err != nil {
			log.Printf("%s: remove engine container %s: %v", name, engine.ID, err)
		}
	}
}

// rdsDeletingInstance reports whether id still names the deleting instance
// whose DbiResourceId is resourceID, the one generation a deletion's teardown
// may touch.
func rdsDeletingInstance(id, resourceID string) bool {
	instance, ok := rdsInstances.Get(id)
	return ok && instance.DBInstanceStatus == "deleting" && instance.DbiResourceId == resourceID
}

// rdsFinishInstanceDeletion stops the deleting instance's engine, removes its
// volume and drops its record, which held the identifier until then.
func rdsFinishInstanceDeletion(id, resourceID string) {
	if !rdsDeletingInstance(id, resourceID) {
		return
	}
	// The instance goes either way; rdsStopDataPlane logs a failed stop.
	_ = rdsStopDataPlane(id, false)
	if instance, ok := rdsInstances.Get(id); ok && !rdsIsAurora(instance.Engine) {
		rdsKeepOrRemoveInstanceBackups(instance)
	}
	_ = rdsStopDataPlane(id, true)
	rdsDeleteEngineLogs(resourceID)
	if rdsDeletingInstance(id, resourceID) {
		rdsInstances.Delete(id)
	}
}

// rdsRecoverInstanceSnapshots resumes the snapshot captures and copies a
// previous process started but did not settle, and the deletions that waited
// on them: a deleting instance's teardown follows its final snapshot.
func rdsRecoverInstanceSnapshots() {
	captures := map[string][]string{}
	for _, snapshot := range rdsSnapshots.List() {
		if snapshot.Status != "creating" {
			continue
		}
		id := snapshot.DBSnapshotIdentifier
		if snapshot.SnapshotType == "automated" {
			// The next backup window takes the instance's next automated
			// backup; a capture cut short holds none.
			rdsSnapshots.Delete(id)
			sim.RemoveVolumeSettled(rdsSnapshotVolume(id), "rds")
			continue
		}
		if snapshot.SourceDBSnapshotIdentifier != "" {
			source, ok := findRDSSnapshotByARN(snapshot.SourceDBSnapshotIdentifier)
			if !ok {
				rdsSettleSnapshot(id, "failed", "the source DB snapshot no longer exists")
				continue
			}
			sourceID := source.DBSnapshotIdentifier
			bg.Go(func() { rdsCopySnapshotData(id, sourceID) })
			continue
		}
		if rdsSnapshotSourceGone(snapshot) {
			// rdsMoveVolumesToKindNames moved the instance's data into the
			// snapshot's volume.
			rdsSettleSnapshot(id, "available", "")
			continue
		}
		captures[snapshot.DBInstanceIdentifier] = append(captures[snapshot.DBInstanceIdentifier], id)
	}
	for _, instance := range rdsInstances.List() {
		if instance.DBInstanceStatus != "deleting" {
			continue
		}
		id, resourceID := instance.DBInstanceIdentifier, instance.DbiResourceId
		snapshots := captures[id]
		delete(captures, id)
		bg.Go(func() {
			for _, snapshotID := range snapshots {
				rdsCaptureSnapshotData(snapshotID, id)
			}
			rdsFinishInstanceDeletion(id, resourceID)
		})
	}
	for instanceID, snapshots := range captures {
		bg.Go(func() {
			for _, snapshotID := range snapshots {
				rdsCaptureSnapshotData(snapshotID, instanceID)
			}
		})
	}
}

// rdsFinishStop stops a stopping instance's engine and lands the instance
// stopped once the engine has stopped. An engine that fails to stop leaves the
// instance stopping, which is what it still is.
func rdsFinishStop(instanceID string) {
	if err := rdsStopDataPlane(instanceID, false); err != nil {
		return
	}
	rdsInstances.Update(instanceID, func(i *RDSInstance) {
		if i.DBInstanceStatus == "stopping" {
			i.DBInstanceStatus = "stopped"
		}
	})
}
