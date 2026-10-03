package main

import (
	"crypto/subtle"
	"net/url"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// An Aurora endpoint owns two logins: the master user's, under the password
// the control plane records, and IAM database authentication, which Aurora
// PostgreSQL grants through the rds_iam role. Every other login reaches the
// engine, which checks it against its own users: Aurora MySQL's engine takes
// the client's credential in the relayed login, and Aurora PostgreSQL's
// engine, which trusts the relay, checks the password against the role's
// stored SCRAM-SHA-256 or MD5 verifier.

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

// rdsPostgresCreateIAMRole creates the rds_iam role Aurora PostgreSQL grants
// IAM database authentication through.
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

func (plane *rdsAuroraDataPlane) authenticate(user, password string, secure bool) bool {
	cluster, masterPassword, err := plane.masterPassword()
	if err != nil {
		return false
	}
	iamToken := func() bool {
		return secure && cluster.EnableIAMDatabaseAuthentication &&
			rdsValidateIAMAuthToken(rdsAuroraEndpoints(cluster), cluster.DbClusterResourceId, user, password)
	}
	isMaster := user == cluster.MasterUsername && subtle.ConstantTimeCompare([]byte(password), []byte(masterPassword)) == 1
	if plane.engine.Engine.Family == dbengine.Postgres {
		if plane.postgresCheck(cluster, user, password, rdsPostgresIAMRoleCheck) {
			return iamToken()
		}
		return isMaster || plane.postgresCheck(cluster, user, password, rdsPostgresPasswordCheck)
	}
	if isMaster {
		return true
	}
	if rdsIsIAMAuthToken(password) {
		return iamToken()
	}
	return true
}

// postgresCheck runs one of the login checks as the master user and reports
// whether it passed.
func (plane *rdsAuroraDataPlane) postgresCheck(cluster RDSCluster, user, password, check string) bool {
	return plane.engine.Exec([]string{plane.engine.Engine.Client, "-v", "ON_ERROR_STOP=1",
		"-U", cluster.MasterUsername, "-d", rdsAuroraDatabaseName(cluster),
		"-c", rdsPostgresLoginSettings(user, password), "-c", check}) == nil
}

// backendLogin runs an Aurora MySQL session as the client's own user, whose
// password the engine checks, except for the logins the endpoint owns: the
// master user's, whose password the engine may hold only once a pending
// change is applied, and an IAM authentication token, which is no engine
// credential.
func (plane *rdsAuroraDataPlane) backendLogin(user, password string) (string, string, error) {
	cluster, masterPassword, err := plane.masterPassword()
	if err != nil {
		return "", "", err
	}
	isMaster := user == cluster.MasterUsername && subtle.ConstantTimeCompare([]byte(password), []byte(masterPassword)) == 1
	if !isMaster && !rdsIsIAMAuthToken(password) {
		return user, password, nil
	}
	backendPassword, err := rdsAuroraBackendPassword(cluster)
	return cluster.MasterUsername, backendPassword, err
}

// prepareEngineAccounts gives the engine the accounts Aurora has: Aurora
// PostgreSQL's rds_iam role, and Aurora MySQL's master user with the global
// privileges Aurora grants it and no remote root account beside it.
func (plane *rdsAuroraDataPlane) prepareEngineAccounts() error {
	cluster, err := plane.cluster()
	if err != nil {
		return err
	}
	engine := plane.engine.Engine
	if engine.Family == dbengine.Postgres {
		return plane.engine.Exec([]string{engine.Client, "-v", "ON_ERROR_STOP=1",
			"-U", cluster.MasterUsername, "-d", rdsAuroraDatabaseName(cluster), "-c", rdsPostgresCreateIAMRole})
	}
	password, err := rdsAuroraBackendPassword(cluster)
	if err != nil {
		return err
	}
	statements := "GRANT " + rdsAuroraMySQLMasterPrivileges + " ON *.* TO " +
		dbengine.QuoteMySQLLiteral(cluster.MasterUsername) + "@'%' WITH GRANT OPTION"
	if cluster.MasterUsername != "root" {
		statements += "; DROP USER IF EXISTS 'root'@'%'"
	}
	return plane.engine.Exec([]string{engine.Client, "--user=root", "--password=" + password, "--execute=" + statements})
}

// rdsAuroraMySQLMasterPrivileges are the global privileges Aurora MySQL
// version 3 grants its master user.
const rdsAuroraMySQLMasterPrivileges = "SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, RELOAD, PROCESS, REFERENCES, INDEX, " +
	"ALTER, SHOW DATABASES, CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, REPLICATION SLAVE, REPLICATION CLIENT, " +
	"CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, CREATE USER, EVENT, TRIGGER"
