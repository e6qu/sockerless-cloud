package main

import (
	"crypto/subtle"
	"net/url"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// An RDS endpoint, a DB instance's or an Aurora cluster's, owns the master
// user's login, under the password the control plane records, and checks
// IAM database authentication tokens. RDS for PostgreSQL and Aurora PostgreSQL
// grant IAM database authentication through the rds_iam role; RDS for MySQL,
// RDS for MariaDB and Aurora MySQL through users identified with
// AWSAuthenticationPlugin, which the endpoint relays a validated token's
// session to as the user the token names. Every other login reaches the
// engine, which checks it against its own users: a MySQL-family engine takes
// the client's credential in the relayed login, and a PostgreSQL engine,
// which trusts the relay, has the endpoint check the password against the
// role's stored SCRAM-SHA-256 or MD5 verifier.

// rdsPostgresLoginSettings hands the presented user and password to the
// checks below as session settings, so neither is ever spliced into SQL.
func rdsPostgresLoginSettings(user, password string) string {
	return "SELECT set_config('sockerless.login_user', " + dbengine.QuoteLiteral(user) +
		", false), set_config('sockerless.login_password', " + dbengine.QuoteLiteral(password) + ", false)"
}

// rdsPostgresIAMRoleCheck fails unless the login is a member of rds_iam,
// directly or through another role. pg_has_role counts every role as granted
// to a superuser, so the check walks the grants instead.
const rdsPostgresIAMRoleCheck = `DO $check$
BEGIN
	IF NOT EXISTS (WITH RECURSIVE granted(oid) AS (
			SELECT oid FROM pg_roles WHERE rolname = current_setting('sockerless.login_user')
			UNION SELECT m.roleid FROM pg_auth_members m JOIN granted g ON m.member = g.oid)
		SELECT FROM granted g JOIN pg_roles r ON r.oid = g.oid WHERE r.rolname = 'rds_iam') THEN
		RAISE EXCEPTION 'role is not granted rds_iam';
	END IF;
END
$check$`

// rdsPostgresPasswordCheck fails unless the presented password matches the
// login role's verifier: MD5 of the password and user name, or the
// SCRAM-SHA-256 StoredKey, SHA-256 of HMAC(SaltedPassword, "Client Key"),
// where SaltedPassword is PBKDF2-HMAC-SHA-256 of the password over the
// verifier's salt and iteration count.
const rdsPostgresPasswordCheck = `DO $check$
DECLARE
	login text := current_setting('sockerless.login_user');
	presented text := current_setting('sockerless.login_password');
	verifier text;
	iterations integer;
	salt bytea;
	stored_key bytea;
	hmac_key bytea;
	inner_pad bytea;
	outer_pad bytea;
	block bytea;
	accumulated bit(256);
	salted bytea := decode(repeat('00', 32), 'hex');
BEGIN
	SELECT rolpassword INTO verifier FROM pg_authid
	WHERE rolname = login AND rolcanlogin AND (rolvaliduntil IS NULL OR rolvaliduntil > now());
	IF verifier IS NULL THEN
		RAISE EXCEPTION 'role % signs in with no password', login;
	END IF;
	IF verifier LIKE 'md5%' THEN
		IF verifier <> 'md5' || md5(presented || login) THEN
			RAISE EXCEPTION 'password authentication failed for user %', login;
		END IF;
		RETURN;
	END IF;
	iterations := split_part(split_part(verifier, '$', 2), ':', 1)::integer;
	salt := decode(split_part(split_part(verifier, '$', 2), ':', 2), 'base64');
	stored_key := decode(split_part(split_part(verifier, '$', 3), ':', 1), 'base64');
	FOR pass IN 1..2 LOOP
		IF pass = 1 THEN
			hmac_key := convert_to(presented, 'UTF8');
		ELSE
			hmac_key := salted;
		END IF;
		IF length(hmac_key) > 64 THEN
			hmac_key := sha256(hmac_key);
		END IF;
		hmac_key := hmac_key || decode(repeat('00', 64 - length(hmac_key)), 'hex');
		inner_pad := hmac_key;
		outer_pad := hmac_key;
		FOR i IN 0..63 LOOP
			inner_pad := set_byte(inner_pad, i, get_byte(hmac_key, i) # 54);
			outer_pad := set_byte(outer_pad, i, get_byte(hmac_key, i) # 92);
		END LOOP;
		IF pass = 1 THEN
			block := sha256(outer_pad || sha256(inner_pad || salt || '\x00000001'::bytea));
			accumulated := ('x' || encode(block, 'hex'))::bit(256);
			FOR round IN 2..iterations LOOP
				block := sha256(outer_pad || sha256(inner_pad || block));
				accumulated := accumulated # ('x' || encode(block, 'hex'))::bit(256);
			END LOOP;
			FOR i IN 0..31 LOOP
				salted := set_byte(salted, i, substring(accumulated FROM i * 8 + 1 FOR 8)::bit(8)::integer);
			END LOOP;
		END IF;
	END LOOP;
	IF sha256(sha256(outer_pad || sha256(inner_pad || convert_to('Client Key', 'UTF8')))) <> stored_key THEN
		RAISE EXCEPTION 'password authentication failed for user %', login;
	END IF;
END
$check$`

// rdsPostgresCreateIAMRole creates the rds_iam role RDS for PostgreSQL and
// Aurora PostgreSQL grant IAM database authentication through.
const rdsPostgresCreateIAMRole = `DO $create$
BEGIN
	IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'rds_iam') THEN
		CREATE ROLE rds_iam;
	END IF;
END
$create$`

// rdsIsIAMAuthToken reports whether password has the shape of an RDS IAM
// authentication token, a presigned rds-db:connect request.
func rdsIsIAMAuthToken(password string) bool {
	parsed, err := url.Parse("https://" + password)
	if err != nil {
		return false
	}
	query := parsed.Query()
	return query.Get("Action") == "connect" && query.Get("X-Amz-Signature") != ""
}

// rdsEndpointLogins are the facts an endpoint authenticates its clients by.
type rdsEndpointLogins struct {
	engine         *dbengine.Instance
	masterUsername string
	masterPassword string
	database       string
	iamEnabled     bool
	resourceID     string
	// iamEndpoints lists the endpoints an IAM authentication token may be
	// signed for.
	iamEndpoints func() []string
	// backendPassword is the master password the engine holds.
	backendPassword func() (string, error)
}

func (logins rdsEndpointLogins) isMaster(user, password string) bool {
	return user == logins.masterUsername && subtle.ConstantTimeCompare([]byte(password), []byte(logins.masterPassword)) == 1
}

func (logins rdsEndpointLogins) authenticate(user, password string, secure bool) bool {
	iamToken := func() bool {
		return secure && logins.iamEnabled &&
			rdsValidateIAMAuthToken(logins.iamEndpoints(), logins.resourceID, user, password)
	}
	if logins.engine.Engine.Family == dbengine.Postgres {
		if logins.postgresCheck(user, password, rdsPostgresIAMRoleCheck) {
			return iamToken()
		}
		return logins.isMaster(user, password) || logins.postgresCheck(user, password, rdsPostgresPasswordCheck)
	}
	switch {
	case logins.isMaster(user, password):
		return true
	case rdsIsIAMAuthToken(password):
		return iamToken()
	}
	// The engine's AWSAuthenticationPlugin trusts the relay, so only a token
	// signs a user identified with it in.
	return logins.mysqlUserWithoutIAM(user)
}

// mysqlUserWithoutIAM reports whether the engine holds no account of user
// identified with AWSAuthenticationPlugin.
func (logins rdsEndpointLogins) mysqlUserWithoutIAM(user string) bool {
	password, err := logins.backendPassword()
	if err != nil {
		return false
	}
	query := "SELECT COUNT(*) FROM mysql.user WHERE user = " + dbengine.QuoteMySQLLiteral(user) +
		" AND plugin = " + dbengine.QuoteMySQLLiteral(rdsIAMAuthenticationPlugin)
	return logins.engine.Exec([]string{"sh", "-c", `test "$("$0" --user=root --password="$1" --batch --skip-column-names --execute="$2")" = 0`,
		logins.engine.Engine.Client, password, query}) == nil
}

// postgresCheck runs one of the login checks as the master user and reports
// whether it passed.
func (logins rdsEndpointLogins) postgresCheck(user, password, check string) bool {
	return logins.engine.Exec([]string{logins.engine.Engine.Client, "-v", "ON_ERROR_STOP=1",
		"-U", logins.masterUsername, "-d", logins.database,
		"-c", rdsPostgresLoginSettings(user, password), "-c", check}) == nil
}

// rdsBackendLogin runs a MySQL-family session as the client's own user under
// the client's own password or IAM authentication token: the engine checks a
// password itself, and admits a token only for a user identified with
// AWSAuthenticationPlugin. The master user's session logs in under the
// password installed in the engine, which holds a changed password only once
// the change is applied.
func rdsBackendLogin(logins rdsEndpointLogins, user, password string) (string, string, error) {
	if logins.isMaster(user, password) {
		installed, err := logins.backendPassword()
		return logins.masterUsername, installed, err
	}
	return user, password, nil
}

// rdsPrepareEngineAccounts gives the engine the accounts RDS has: the rds_iam
// role on PostgreSQL, and on a MySQL-family engine the master user with the
// global privileges RDS grants it and no remote root account beside it.
func rdsPrepareEngineAccounts(engine *dbengine.Instance, masterUsername, database, backendPassword string) error {
	if engine.Engine.Family == dbengine.Postgres {
		return engine.Exec([]string{engine.Engine.Client, "-v", "ON_ERROR_STOP=1",
			"-U", masterUsername, "-d", database, "-c", rdsPostgresCreateIAMRole})
	}
	statements := "GRANT " + rdsMySQLMasterPrivileges + " ON *.* TO " +
		dbengine.QuoteMySQLLiteral(masterUsername) + "@'%' WITH GRANT OPTION"
	if masterUsername != "root" {
		statements += "; DROP USER IF EXISTS 'root'@'%'"
	}
	if err := engine.Exec([]string{engine.Engine.Client, "--user=root", "--password=" + backendPassword, "--execute=" + statements}); err != nil {
		return err
	}
	return rdsInstallIAMAuthenticationPlugin(engine, backendPassword)
}

// rdsMySQLMasterPrivileges are the global privileges RDS for MySQL, RDS for
// MariaDB and Aurora MySQL version 3 grant the master user.
const rdsMySQLMasterPrivileges = "SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, RELOAD, PROCESS, REFERENCES, INDEX, " +
	"ALTER, SHOW DATABASES, CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, REPLICATION SLAVE, REPLICATION CLIENT, " +
	"CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, CREATE USER, EVENT, TRIGGER"

func (plane *rdsAuroraDataPlane) logins() (rdsEndpointLogins, error) {
	cluster, masterPassword, err := plane.masterPassword()
	if err != nil {
		return rdsEndpointLogins{}, err
	}
	return rdsEndpointLogins{
		engine:         plane.engine,
		masterUsername: cluster.MasterUsername,
		masterPassword: masterPassword,
		database:       rdsAuroraDatabaseName(cluster),
		iamEnabled:     cluster.EnableIAMDatabaseAuthentication,
		resourceID:     cluster.DbClusterResourceId,
		iamEndpoints:   func() []string { return rdsAuroraEndpoints(cluster) },
		backendPassword: func() (string, error) {
			current, err := plane.cluster()
			if err != nil {
				return "", err
			}
			return rdsAuroraBackendPassword(current)
		},
	}, nil
}

func (plane *rdsAuroraDataPlane) authenticate(user, password string, secure bool) bool {
	logins, err := plane.logins()
	return err == nil && logins.authenticate(user, password, secure)
}

func (plane *rdsAuroraDataPlane) backendLogin(user, password string) (string, string, error) {
	logins, err := plane.logins()
	if err != nil {
		return "", "", err
	}
	return rdsBackendLogin(logins, user, password)
}

func (plane *rdsAuroraDataPlane) prepareEngineAccounts() error {
	cluster, err := plane.cluster()
	if err != nil {
		return err
	}
	password, err := rdsAuroraBackendPassword(cluster)
	if err != nil {
		return err
	}
	return rdsPrepareEngineAccounts(plane.engine, cluster.MasterUsername, rdsAuroraDatabaseName(cluster), password)
}
