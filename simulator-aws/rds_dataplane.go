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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

const rdsAWSOwnedKMSKeyID = "aws-owned-rds"
const rdsEmptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var rdsServerCertificate = dbengine.SelfSignedCertificate("Amazon RDS simulator")

// rdsDataPlane is an Amazon RDS instance's endpoint and engine, with the
// instance record its authentication and engine start read.
type rdsDataPlane struct {
	engine   *dbengine.Instance
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
		if plane, installed := rdsLoadDataPlane(instance.DBInstanceIdentifier); installed {
			if err := plane.engine.Adopt(); err != nil {
				return fmt.Errorf("restore DB instance %s backend: %w", instance.DBInstanceIdentifier, err)
			}
		}
		rdsInstances.Put(instance.DBInstanceIdentifier, instance)
	}
	return nil
}

func rdsEngine(engine string) (dbengine.Engine, bool) {
	switch {
	case strings.HasPrefix(strings.ToLower(engine), "postgres"):
		return dbengine.Postgres16, true
	case strings.EqualFold(engine, "mysql"):
		return dbengine.MySQL80, true
	case strings.EqualFold(engine, "mariadb"):
		return dbengine.MariaDB114, true
	default:
		return dbengine.Engine{}, false
	}
}

func rdsInstallDataPlane(instance *RDSInstance, masterPassword string) error {
	engine, ok := rdsEngine(instance.Engine)
	if !ok {
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
	plane := &rdsDataPlane{instance: *instance}
	plane.engine = &dbengine.Instance{
		Name:         "Amazon RDS " + instance.DBInstanceIdentifier,
		Engine:       engine,
		Volume:       rdsInstanceVolume(instance.DBInstanceIdentifier),
		Labels:       map[string]string{"sockerless-rds-instance": instance.DBInstanceIdentifier},
		Sandbox:      SandboxFargate,
		Platform:     dbengine.FixedPlatform("linux/amd64"),
		Environment:  plane.environment,
		Ready:        plane.applyPendingMasterPassword,
		Certificate:  rdsServerCertificate,
		Authenticate: plane.authenticate,
		BackendLogin: plane.backendLogin,
	}
	rdsDataPlanes.Store(instance.DBInstanceIdentifier, plane)
	plane.engine.Serve(listener)
	return nil
}

// rdsListenForInstance rebinds the endpoint an instance already advertises,
// and otherwise binds the requested port on a loopback address of its own.
func rdsListenForInstance(instance RDSInstance) (net.Listener, error) {
	if endpointIP := net.ParseIP(instance.Endpoint); endpointIP != nil && endpointIP.IsLoopback() && instance.Port > 0 {
		return net.Listen("tcp", net.JoinHostPort(instance.Endpoint, strconv.Itoa(instance.Port)))
	}
	return dbengine.ListenLoopback(instance.DBInstanceIdentifier, instance.Port)
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
	database := rdsDatabaseName(instance)
	switch plane.engine.Engine.Client {
	case dbengine.MariaDB114.Client:
		return map[string]string{
			"MARIADB_USER":          instance.MasterUsername,
			"MARIADB_PASSWORD":      password,
			"MARIADB_ROOT_PASSWORD": password,
			"MARIADB_DATABASE":      database,
		}, nil
	case dbengine.MySQL80.Client:
		return map[string]string{
			"MYSQL_USER":          instance.MasterUsername,
			"MYSQL_PASSWORD":      password,
			"MYSQL_ROOT_PASSWORD": password,
			"MYSQL_DATABASE":      database,
		}, nil
	default:
		return dbengine.PostgresEnvironment(instance.MasterUsername, password, database), nil
	}
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
	return secure && instance.EnableIAMDatabaseAuthentication && rdsValidateIAMAuthToken(instance, user, password)
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
	client := plane.engine.Engine.Client
	var command []string
	if plane.engine.Engine.Family == dbengine.Postgres {
		statement := "ALTER ROLE " + dbengine.QuoteIdentifier(instance.MasterUsername) +
			" WITH PASSWORD " + dbengine.QuoteLiteral(newPassword)
		command = []string{client, "-v", "ON_ERROR_STOP=1", "-U", instance.MasterUsername, "-d", rdsDatabaseName(instance), "-c", statement}
	} else {
		oldPassword, err := plane.backendPassword()
		if err != nil {
			return err
		}
		statement := "ALTER USER " + dbengine.QuoteMySQLLiteral(instance.MasterUsername) +
			"@'%' IDENTIFIED BY " + dbengine.QuoteMySQLLiteral(newPassword)
		command = []string{client, "--user=root", "--password=" + oldPassword, "--execute=" + statement}
	}
	if err := plane.engine.Exec(command); err != nil {
		return fmt.Errorf("rotate the master-user password in the engine: %w", err)
	}
	return nil
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

// rdsStopDataPlane closes the instance's endpoint and stops its engine and,
// when the instance is being deleted, removes its data volume — which exists
// from the first engine start or from a snapshot restore's clone.
func rdsStopDataPlane(instanceID string, deleteVolume bool) {
	value, ok := rdsDataPlanes.LoadAndDelete(instanceID)
	if !ok {
		return
	}
	plane, ok := value.(*rdsDataPlane)
	if !ok {
		return
	}
	if err := plane.engine.Close(); err != nil {
		log.Printf("Amazon RDS %s: stop database engine: %v", instanceID, err)
	}
	if deleteVolume && sim.VolumeExists(rdsInstanceVolume(instanceID)) {
		if err := sim.RemoveVolume(rdsInstanceVolume(instanceID)); err != nil {
			log.Printf("Amazon RDS %s: remove data volume: %v", instanceID, err)
		}
	}
}
