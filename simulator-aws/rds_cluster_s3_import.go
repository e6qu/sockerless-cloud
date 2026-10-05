package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// RestoreDBClusterFromS3 creates an Aurora MySQL cluster from a Percona
// XtraBackup of a MySQL 8.0 server in Amazon S3. The cluster is creating while
// Aurora reads the backup files under S3Prefix as the S3IngestionRoleArn role,
// unpacks them (xbstream, tar or gzip-compressed tar archives, which may be
// split into numbered parts, or the backup directory's own files), prepares
// the backup with xtrabackup --prepare and copies it back into the new
// cluster volume. The master user the request names then holds every
// privilege under the request's password. A backup that cannot be read or
// prepared leaves the cluster migration-failed.

// rdsXtraBackupImage is the Percona XtraBackup release that prepares MySQL 8.0
// backups.
const rdsXtraBackupImage = "docker.io/percona/percona-xtrabackup:8.0"

// rdsOpenDataDirectoryScript lets the XtraBackup image's own unprivileged
// user copy a backup into the new, empty data directory.
const rdsOpenDataDirectoryScript = `chmod 0777 "$DATA"`

// rdsPrepareXtraBackupScript unpacks the backup files under /backup, prepares
// the backup and copies it into the empty data directory at DATA.
const rdsPrepareXtraBackupScript = `set -e
mkdir -p /tmp/backup
cd /backup
streams=$(find . -type f \( -name '*.xbstream' -o -name '*.xbstream.[0-9]*' \) | sort)
tars=$(find . -type f \( -name '*.tar' -o -name '*.tar.[0-9]*' \) | sort)
gzips=$(find . -type f \( -name '*.tar.gz' -o -name '*.tgz' -o -name '*.tar.gz.[0-9]*' \) | sort)
if [ -n "$streams" ]; then
	cat $streams | xbstream -x -C /tmp/backup
elif [ -n "$tars" ]; then
	cat $tars | tar -x -C /tmp/backup
elif [ -n "$gzips" ]; then
	cat $gzips | tar -xz -C /tmp/backup
else
	cp -a /backup/. /tmp/backup/
fi
checkpoints=$(find /tmp/backup -name xtrabackup_checkpoints | head -n 1)
if [ -z "$checkpoints" ]; then
	echo "the files under the Amazon S3 prefix hold no Percona XtraBackup: no xtrabackup_checkpoints file"
	exit 1
fi
backup=$(dirname "$checkpoints")
xtrabackup --prepare --target-dir="$backup" 2>&1 | tail -n 20
xtrabackup --copy-back --target-dir="$backup" --datadir="$DATA" 2>&1 | tail -n 20
`

// rdsImportMasterUserScript gives the imported data directory to the mysql
// user, starts MySQL on it with no network listener and installs the
// cluster's master user and database. A user only the init file creates runs
// the statements, and goes before the server stops.
const rdsImportMasterUserScript = `set -e
chown -R mysql:mysql "$DATA"
chmod 0750 "$DATA"
password=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')
printf "CREATE USER 'sockerless_import'@'localhost' IDENTIFIED BY '%s';\nGRANT ALL ON *.* TO 'sockerless_import'@'localhost' WITH GRANT OPTION;\n" "$password" > /tmp/import-init.sql
chmod 644 /tmp/import-init.sql
mysqld --user=mysql --skip-networking --socket=/tmp/import.sock --default-authentication-plugin=mysql_native_password \
	--init-file=/tmp/import-init.sql &
server=$!
client() {
	mysql --socket=/tmp/import.sock --user=sockerless_import --password="$password" --batch --skip-column-names "$@" 2>&1
}
until client --execute='SELECT 1' >/dev/null; do
	kill -0 "$server"
	sleep 0.1
done
status=0
client --execute="$INSTALL_MASTER_USER" || status=$?
client --execute="DROP USER 'sockerless_import'@'localhost'"
kill -TERM "$server"
wait "$server"
exit "$status"
`

// rdsXtraBackupSandbox lets the import helpers set owners and modes on the
// data directory, and the MySQL helper drop to the mysql user and signal it.
var rdsXtraBackupSandbox = rdsReplaySandbox

func handleRDSRestoreClusterFromS3(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	newID := r.FormValue("DBClusterIdentifier")
	for _, name := range []string{"DBClusterIdentifier", "Engine", "MasterUsername", "SourceEngine", "SourceEngineVersion", "S3BucketName", "S3IngestionRoleArn"} {
		if r.FormValue(name) == "" {
			rdsErrorXML(w, "MissingParameter", fmt.Sprintf("The parameter %s must be provided.", name), http.StatusBadRequest, requestID)
			return
		}
	}
	if _, exists := rdsClusters.Get(newID); exists {
		rdsErrorXML(w, "DBClusterAlreadyExistsFault", fmt.Sprintf("DBCluster %q already exists", newID), http.StatusConflict, requestID)
		return
	}
	engine := r.FormValue("Engine")
	if !strings.EqualFold(engine, "aurora-mysql") {
		rdsErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("Engine %s does not restore from Amazon S3; only aurora-mysql does.", engine), http.StatusBadRequest, requestID)
		return
	}
	if r.FormValue("SourceEngine") != "mysql" {
		rdsErrorXML(w, "InvalidParameterValue", "The source engine must be mysql.", http.StatusBadRequest, requestID)
		return
	}
	if version := r.FormValue("SourceEngineVersion"); version != "8.0" && !strings.HasPrefix(version, "8.0.") {
		rdsErrorXML(w, "InvalidParameterCombination",
			fmt.Sprintf("Source engine version %s cannot be restored into Aurora MySQL version 3, which takes MySQL 8.0 backups.", version),
			http.StatusBadRequest, requestID)
		return
	}
	password := r.FormValue("MasterUserPassword")
	if !rdsValidMasterPassword(password) {
		rdsErrorXML(w, "InvalidParameterValue",
			"The parameter MasterUserPassword is not a valid password. It must contain from 8 to 41 printable ASCII characters other than '/', '\"' and '@'.",
			http.StatusBadRequest, requestID)
		return
	}
	if _, err := rdsNextBackupTime(rdsClusterBackupWindow(r), time.Now()); err != nil {
		rdsErrorXML(w, "InvalidParameterValue", "The backup window must be in the format hh24:mi-hh24:mi.", http.StatusBadRequest, requestID)
		return
	}
	bucket, prefix, role := r.FormValue("S3BucketName"), r.FormValue("S3Prefix"), r.FormValue("S3IngestionRoleArn")
	if err := rdsS3IngestionReadable(bucket, prefix, role); err != nil {
		rdsErrorXML(w, "InvalidS3BucketFault",
			fmt.Sprintf("The specified Amazon S3 bucket name can't be found or Amazon RDS isn't authorized to access it: %v", err),
			http.StatusBadRequest, requestID)
		return
	}
	sealed, err := rdsSealMasterPassword(password)
	if err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	cluster := rdsClusterFromSource(r, newID, "aurora-mysql", r.FormValue("EngineVersion"))
	cluster.AllocatedStorage = atoiOrZero(r.FormValue("AllocatedStorage"))
	cluster.MasterUserSecret = sealed
	cluster.BackendMasterUserSecret = append([]byte(nil), sealed...)
	cluster.DeletionProtection = strings.EqualFold(r.FormValue("DeletionProtection"), "true")
	cluster.EnableIAMDatabaseAuthentication = strings.EqualFold(r.FormValue("EnableIAMDatabaseAuthentication"), "true")
	cluster.StorageEncrypted = strings.EqualFold(r.FormValue("StorageEncrypted"), "true")
	if retention := atoiOrZero(r.FormValue("BackupRetentionPeriod")); retention > 0 {
		cluster.BackupRetentionPeriod = retention
	}
	cluster.Status = "creating"
	cluster.ImportS3Bucket, cluster.ImportS3Prefix, cluster.ImportS3Role = bucket, prefix, role
	if err := rdsInstallAuroraDataPlane(&cluster, ""); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	rdsClusters.Put(newID, cluster)
	resourceID := cluster.DbClusterResourceId
	bg.Go(func() { rdsFinishS3Import(newID, resourceID, bucket, prefix, role) })
	rdsXMLResponse(w, "RestoreDBClusterFromS3", renderRDSCluster(cluster), requestID)
}

// rdsS3IngestionReadable proves the bucket exists, holds objects under
// prefix, and that RDS can assume role and read each of them.
func rdsS3IngestionReadable(bucket, prefix, role string) error {
	if _, ok := s3Buckets_.Get(bucket); !ok {
		return fmt.Errorf("bucket %s does not exist", bucket)
	}
	actions := map[string]string{"s3:ListBucket": "arn:aws:s3:::" + bucket}
	if err := iamValidateServiceRole(role, "rds.amazonaws.com", actions); err != nil {
		return err
	}
	keys := rdsS3BackupKeys(bucket, prefix)
	if len(keys) == 0 {
		return fmt.Errorf("bucket %s holds no object under prefix %q", bucket, prefix)
	}
	for _, key := range keys {
		if err := iamValidateServiceRole(role, "rds.amazonaws.com", map[string]string{"s3:GetObject": "arn:aws:s3:::" + bucket + "/" + key}); err != nil {
			return err
		}
	}
	return nil
}

// rdsS3BackupKeys are the object keys under prefix in bucket.
func rdsS3BackupKeys(bucket, prefix string) []string {
	var keys []string
	for _, row := range s3Objects.ListPrefix(bucket + "/" + prefix) {
		keys = append(keys, strings.TrimPrefix(row.ID, bucket+"/"))
	}
	return keys
}

// rdsFinishS3Import imports the backup into the creating cluster's volume and
// lands it available, or migration-failed when the import fails. An import a
// previous process left part-way starts again on an empty volume.
func rdsFinishS3Import(clusterID, resourceID, bucket, prefix, role string) {
	volume := rdsClusterVolume(clusterID)
	sim.RemoveVolumeSettled(volume, "rds")
	err := fmt.Errorf("the cluster no longer exists")
	if cluster, ok := rdsClusters.Get(clusterID); ok {
		engine, _ := rdsLoggingEngine(cluster.Engine)
		err = rdsImportXtraBackup(rdsImportTarget{
			engine: engine, volume: volume, label: clusterID, masterUsername: cluster.MasterUsername,
			masterUserSecret: cluster.MasterUserSecret, database: rdsAuroraDatabaseName(cluster),
		}, bucket, prefix, role)
	}
	status := "available"
	if err != nil {
		log.Printf("Amazon Aurora %s: restore from s3://%s/%s: %v", clusterID, bucket, prefix, err)
		status = "migration-failed"
	}
	rdsClusters.Update(clusterID, func(stored *RDSCluster) {
		if stored.DbClusterResourceId == resourceID && stored.Status == "creating" {
			stored.Status = status
			stored.ImportS3Bucket, stored.ImportS3Prefix, stored.ImportS3Role = "", "", ""
		}
	})
	rdsTakeFirstClusterBackup(clusterID)
}

// rdsImportTarget is the volume an XtraBackup import fills and the master
// user and database it installs there.
type rdsImportTarget struct {
	engine           dbengine.Engine
	volume           string
	label            string
	masterUsername   string
	masterUserSecret []byte
	database         string
}

func rdsImportXtraBackup(target rdsImportTarget, bucket, prefix, role string) error {
	staging, err := os.MkdirTemp("", "sockerless-rds-s3-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	// The XtraBackup image runs as its own unprivileged user.
	if err := os.Chmod(staging, 0o755); err != nil {
		return err
	}
	if err := rdsStageS3Backup(staging, bucket, prefix, role); err != nil {
		return err
	}
	engine, volume := target.engine, target.volume
	if _, err := rdsRunVolumeHelper(engine, rdsOpenDataDirectoryScript, nil, rdsXtraBackupSandbox,
		[]string{volume + ":" + engine.DataPath}, target.label); err != nil {
		return fmt.Errorf("open the volume to the import: %w", err)
	}
	prepare := engine
	prepare.Image = rdsXtraBackupImage
	if _, err := rdsRunVolumeHelper(prepare, rdsPrepareXtraBackupScript, nil, rdsXtraBackupSandbox,
		[]string{staging + ":/backup:ro", volume + ":" + engine.DataPath}, target.label); err != nil {
		return fmt.Errorf("prepare the Percona XtraBackup: %w", err)
	}
	_, password, ok := kmsDecryptBytes(target.masterUserSecret)
	if !ok {
		return fmt.Errorf("decrypt the master-user credential")
	}
	install := rdsMySQLInstallMasterUserStatements(target.masterUsername, string(password), target.database)
	if _, err := rdsRunVolumeHelper(engine, rdsImportMasterUserScript, map[string]string{"INSTALL_MASTER_USER": install},
		rdsXtraBackupSandbox, []string{volume + ":" + engine.DataPath}, target.label); err != nil {
		return fmt.Errorf("install the master user: %w", err)
	}
	return nil
}

// rdsStageS3Backup copies every object under prefix into dir at its path below
// the prefix, readable to every user, reading each as the ingestion role
// again so a role narrowed since the request takes effect.
func rdsStageS3Backup(dir, bucket, prefix, role string) error {
	for _, key := range rdsS3BackupKeys(bucket, prefix) {
		if err := iamValidateServiceRole(role, "rds.amazonaws.com", map[string]string{"s3:GetObject": "arn:aws:s3:::" + bucket + "/" + key}); err != nil {
			return err
		}
		name := filepath.Clean("/" + strings.TrimPrefix(key, prefix))
		if name == "/" || strings.HasSuffix(key, "/") {
			continue
		}
		obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
		if !ok {
			return fmt.Errorf("object s3://%s/%s no longer exists", bucket, key)
		}
		if err := rdsStageS3Object(filepath.Join(dir, name), obj); err != nil {
			return fmt.Errorf("read s3://%s/%s: %w", bucket, key, err)
		}
	}
	return nil
}

func rdsStageS3Object(path string, obj S3Object) error {
	_, reader, err := s3OpenObject(obj)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// rdsMySQLInstallMasterUserStatements creates the master user, or resets the
// one the backup holds, with every privilege, gives root the same password,
// and creates the cluster's database.
func rdsMySQLInstallMasterUserStatements(user, password, database string) string {
	account := dbengine.QuoteMySQLLiteral(user) + "@'%'"
	identified := " IDENTIFIED WITH mysql_native_password BY " + dbengine.QuoteMySQLLiteral(password)
	return strings.Join([]string{
		"CREATE USER IF NOT EXISTS " + account + identified,
		"ALTER USER " + account + identified,
		"GRANT ALL PRIVILEGES ON *.* TO " + account + " WITH GRANT OPTION",
		"CREATE USER IF NOT EXISTS 'root'@'localhost'" + identified,
		"ALTER USER 'root'@'localhost'" + identified,
		"CREATE DATABASE IF NOT EXISTS " + dbengine.QuoteMySQLIdentifier(database),
	}, "; ")
}
