package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// instances.export and instances.import move data between the instance's
// engine and a Cloud Storage object with the engine's own tools: pg_dump,
// psql and COPY for PostgreSQL, mysqldump, mysql, SELECT ... INTO OUTFILE and
// LOAD DATA INFILE for MySQL (specs/cloud-api/gcp/sql-*-import-export-*.md).
// Cloud SQL reads and writes the object through the Cloud Storage API as the
// instance's service account, so the bucket's own policy decides, and a
// missing bucket, object or role fails the operation. instances.executeSql
// runs statements on the engine as the database user the request names, with
// the password Secret Manager holds (sql-executesql-instance.md).

func registerCloudSQLData(srv *sim.Server) {
	for _, prefix := range []string{"/v1", "/sql/v1beta4"} {
		base := prefix + "/projects/{project}/instances/{instance}/"
		srv.HandleFunc("POST "+base+"export", handleSQLExport)
		srv.HandleFunc("POST "+base+"import", handleSQLImport)
	}
}

// sqlTransferFailure is a transfer that failed with an operation error code.
type sqlTransferFailure struct {
	code string
	err  error
}

func (f sqlTransferFailure) Error() string { return f.err.Error() }

func sqlTransferFailed(code, format string, args ...any) error {
	return sqlTransferFailure{code: code, err: fmt.Errorf(format, args...)}
}

// sqlTransfers tracks the instance's running import or export, which Cloud
// SQL runs one at a time, and lets operations.cancel stop it.
var sqlTransfers = struct {
	sync.Mutex
	byInstance  map[string]string
	byOperation map[string]sqlRunningTransfer
}{byInstance: map[string]string{}, byOperation: map[string]sqlRunningTransfer{}}

type sqlRunningTransfer struct {
	instanceKey string
	cancel      context.CancelFunc
}

// sqlCancelTransfer stops a running import or export; it reports false when
// the operation is not one.
func sqlCancelTransfer(project, operation string) bool {
	sqlTransfers.Lock()
	defer sqlTransfers.Unlock()
	running, ok := sqlTransfers.byOperation[sqlOperationKey(project, operation)]
	if !ok {
		return false
	}
	delete(sqlTransfers.byOperation, sqlOperationKey(project, operation))
	delete(sqlTransfers.byInstance, running.instanceKey)
	running.cancel()
	return true
}

// sqlSettleTransfer records a transfer's outcome, unless operations.cancel
// already settled it.
func sqlSettleTransfer(project, operation string, workErr error) {
	sqlTransfers.Lock()
	defer sqlTransfers.Unlock()
	running, ok := sqlTransfers.byOperation[sqlOperationKey(project, operation)]
	if !ok {
		return
	}
	delete(sqlTransfers.byOperation, sqlOperationKey(project, operation))
	delete(sqlTransfers.byInstance, running.instanceKey)
	running.cancel()
	defer gcpOperationDone.Announce(operation)
	sqlOperations.Update(sqlOperationKey(project, operation), func(op *SQLOperation) {
		op.Status = "DONE"
		op.EndTime = nowTimestamp()
		if workErr == nil {
			return
		}
		code := "INTERNAL_ERROR"
		var failure sqlTransferFailure
		if errors.As(workErr, &failure) {
			code = failure.code
		}
		op.Error = &SQLOperationErrorList{Kind: "sql#operationErrors", Errors: []SQLOperationError{{
			Kind: "sql#operationError", Code: code, Message: workErr.Error(),
		}}}
	})
}

type sqlCSVOptions struct {
	SelectQuery        string   `json:"selectQuery,omitempty"`
	Table              string   `json:"table,omitempty"`
	Columns            []string `json:"columns,omitempty"`
	EscapeCharacter    string   `json:"escapeCharacter,omitempty"`
	QuoteCharacter     string   `json:"quoteCharacter,omitempty"`
	FieldsTerminatedBy string   `json:"fieldsTerminatedBy,omitempty"`
	LinesTerminatedBy  string   `json:"linesTerminatedBy,omitempty"`
}

type sqlExportContext struct {
	URI              string         `json:"uri"`
	FileType         string         `json:"fileType"`
	Databases        []string       `json:"databases"`
	Offload          bool           `json:"offload"`
	CsvExportOptions *sqlCSVOptions `json:"csvExportOptions"`
	SQLExportOptions *struct {
		Tables             []string `json:"tables"`
		SchemaOnly         bool     `json:"schemaOnly"`
		Parallel           bool     `json:"parallel"`
		MysqlExportOptions *struct {
			MasterData int `json:"masterData"`
		} `json:"mysqlExportOptions"`
		PostgresExportOptions *struct {
			Clean    bool `json:"clean"`
			IfExists bool `json:"ifExists"`
		} `json:"postgresExportOptions"`
	} `json:"sqlExportOptions"`
}

type sqlImportContext struct {
	URI              string         `json:"uri"`
	FileType         string         `json:"fileType"`
	Database         string         `json:"database"`
	ImportUser       string         `json:"importUser"`
	CsvImportOptions *sqlCSVOptions `json:"csvImportOptions"`
	SQLImportOptions *struct {
		Parallel bool `json:"parallel"`
	} `json:"sqlImportOptions"`
}

// sqlTransferTarget checks what an import or an export needs before it is
// accepted: the instance, an engine the simulator runs for the file type, and
// a gs:// URI.
func sqlTransferTarget(w http.ResponseWriter, r *http.Request, fileType, uri string) (SQLInstance, *sqlDataPlane, string, string, bool) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return inst, nil, "", "", false
	}
	_, hasEngine := sqlEngineFamily(inst.DatabaseVersion)
	switch fileType {
	case "SQL", "CSV":
		if strings.HasPrefix(inst.DatabaseVersion, "SQLSERVER_") {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a Cloud SQL for SQL Server instance imports and exports BAK files, not %s files", fileType)
			return inst, nil, "", "", false
		}
	case "BAK", "TDE":
		if !strings.HasPrefix(inst.DatabaseVersion, "SQLSERVER_") {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s files apply to Cloud SQL for SQL Server instances only; instance %s runs %s", fileType, instance, inst.DatabaseVersion)
			return inst, nil, "", "", false
		}
		sqlUnserved(fileType+" import and export", "the simulator runs no SQL Server engine")(w, r)
		return inst, nil, "", "", false
	default:
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "fileType %q is not one of SQL, CSV, BAK or TDE", fileType)
		return inst, nil, "", "", false
	}
	if !hasEngine {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "instance %s runs %s, for which the simulator serves no engine", instance, inst.DatabaseVersion)
		return inst, nil, "", "", false
	}
	bucket, object, ok := msRedisParseObjectURI(uri)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "uri %q is not of the form gs://bucketName/fileName", uri)
		return inst, nil, "", "", false
	}
	plane, ok := sqlLoadDataPlane(project, instance)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "instance %s has no data plane: it serves no address", instance)
		return inst, nil, "", "", false
	}
	return inst, plane, bucket, object, true
}

// sqlStartTransfer records the RUNNING operation and runs work in the
// background, or answers 409 while the instance runs another transfer.
func sqlStartTransfer(w http.ResponseWriter, inst SQLInstance, opType string, requestContext json.RawMessage, work func(ctx context.Context) error) {
	instanceKey := sqlInstanceKey(inst.Project, inst.Name)
	sqlTransfers.Lock()
	if _, busy := sqlTransfers.byInstance[instanceKey]; busy {
		sqlTransfers.Unlock()
		GCPErrorf(w, http.StatusConflict, "ABORTED", "Operation failed because another operation was already in progress.")
		return
	}
	op := newSQLOperationRunning(inst.Project, opType, inst.Name)
	op.StartTime = op.InsertTime
	if opType == "EXPORT" {
		op.ExportContext = requestContext
	} else {
		op.ImportContext = requestContext
	}
	sqlOperations.Put(sqlOperationKey(inst.Project, op.Name), op)
	ctx, cancel := context.WithCancel(gcpServer.RequestContext(context.Background()))
	sqlTransfers.byInstance[instanceKey] = op.Name
	sqlTransfers.byOperation[sqlOperationKey(inst.Project, op.Name)] = sqlRunningTransfer{instanceKey: instanceKey, cancel: cancel}
	sqlTransfers.Unlock()
	name := op.Name
	bg.Go(func() { sqlSettleTransfer(inst.Project, name, work(ctx)) })
	sim.WriteJSON(w, http.StatusOK, op)
}

// sqlWithKind echoes a request context with its kind, as the operation
// carries it.
func sqlWithKind(raw json.RawMessage, kind string) json.RawMessage {
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		fields = map[string]any{}
	}
	fields["kind"] = kind
	out, _ := json.Marshal(fields)
	return out
}

func handleSQLExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExportContext json.RawMessage `json:"exportContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	var ec sqlExportContext
	if len(req.ExportContext) == 0 || json.Unmarshal(req.ExportContext, &ec) != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "exportContext is required")
		return
	}
	inst, plane, bucket, object, ok := sqlTransferTarget(w, r, ec.FileType, ec.URI)
	if !ok {
		return
	}
	family := plane.Engine.Family
	var dump func(ctx context.Context) ([]byte, error)
	switch ec.FileType {
	case "SQL":
		if ec.SQLExportOptions != nil && ec.SQLExportOptions.Parallel {
			sqlUnserved("parallel export", "the simulator writes a single dump file, not a directory export")(w, r)
			return
		}
		command, env, err := sqlDumpCommand(plane, ec)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		dump = func(ctx context.Context) ([]byte, error) {
			if command == nil {
				databases, err := sqlMySQLUserDatabases(ctx, plane)
				if err != nil {
					return nil, err
				}
				ec.Databases = databases
				if command, env, err = sqlDumpCommand(plane, ec); err != nil {
					return nil, err
				}
			}
			return sqlEngineRun(ctx, plane, command, env, nil)
		}
	case "CSV":
		command, env, err := sqlCSVExportCommand(family, plane, ec)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		dump = func(ctx context.Context) ([]byte, error) { return sqlEngineRun(ctx, plane, command, env, nil) }
	}
	sqlStartTransfer(w, inst, "EXPORT", sqlWithKind(req.ExportContext, "sql#exportContext"), func(ctx context.Context) error {
		data, err := dump(ctx)
		if err != nil {
			return err
		}
		if strings.HasSuffix(object, ".gz") {
			var compressed bytes.Buffer
			zw := gzip.NewWriter(&compressed)
			if _, err := zw.Write(data); err != nil {
				return err
			}
			if err := zw.Close(); err != nil {
				return err
			}
			data = compressed.Bytes()
		}
		return sqlWriteObject(ctx, inst, bucket, object, data)
	})
}

func handleSQLImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportContext json.RawMessage `json:"importContext"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	var ic sqlImportContext
	if len(req.ImportContext) == 0 || json.Unmarshal(req.ImportContext, &ic) != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "importContext is required")
		return
	}
	inst, plane, bucket, object, ok := sqlTransferTarget(w, r, ic.FileType, ic.URI)
	if !ok {
		return
	}
	family := plane.Engine.Family
	if ic.ImportUser != "" && family != dbengine.Postgres {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "importUser applies to PostgreSQL instances only")
		return
	}
	var load func(ctx context.Context, data []byte) error
	switch ic.FileType {
	case "SQL":
		if ic.SQLImportOptions != nil && ic.SQLImportOptions.Parallel {
			sqlUnserved("parallel import", "the simulator reads a single dump file, not a directory export")(w, r)
			return
		}
		command, env, err := sqlRestoreCommand(plane, ic)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		load = func(ctx context.Context, data []byte) error {
			_, err := sqlEngineRun(ctx, plane, command, env, bytes.NewReader(data))
			return err
		}
	case "CSV":
		command, env, err := sqlCSVImportCommand(family, plane, ic)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		load = func(ctx context.Context, data []byte) error {
			_, err := sqlEngineRun(ctx, plane, command, env, bytes.NewReader(data))
			return err
		}
	}
	sqlStartTransfer(w, inst, "IMPORT", sqlWithKind(req.ImportContext, "sql#importContext"), func(ctx context.Context) error {
		data, err := sqlReadObject(ctx, inst, bucket, object)
		if err != nil {
			return err
		}
		if strings.HasSuffix(object, ".gz") || bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
			zr, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return sqlTransferFailed("INVALID_ARGUMENT", "gs://%s/%s is not a gzip file: %v", bucket, object, err)
			}
			if data, err = io.ReadAll(zr); err != nil {
				return sqlTransferFailed("INVALID_ARGUMENT", "decompress gs://%s/%s: %v", bucket, object, err)
			}
		}
		return load(ctx, data)
	})
}

// sqlMySQLUserDatabases lists the databases a MySQL SQL export with no
// database names takes: all of them except the mysql system database and the
// server's own schemas.
func sqlMySQLUserDatabases(ctx context.Context, plane *sqlDataPlane) ([]string, error) {
	env, err := sqlMySQLEnv(plane)
	if err != nil {
		return nil, err
	}
	out, err := sqlEngineRun(ctx, plane, []string{"mysql", "--user=root", "--batch", "--skip-column-names", "--execute=SHOW DATABASES"}, env, nil)
	if err != nil {
		return nil, err
	}
	var databases []string
	for _, name := range strings.Fields(string(out)) {
		switch name {
		case "mysql", "information_schema", "performance_schema", "sys":
			continue
		}
		databases = append(databases, name)
	}
	return databases, nil
}

func sqlMySQLEnv(plane *sqlDataPlane) ([]string, error) {
	password, err := sqlEngineAdminPassword(plane.project, plane.instance, dbengine.MySQL)
	if err != nil {
		return nil, err
	}
	return []string{"MYSQL_PWD=" + password}, nil
}

// sqlDumpCommand builds the SQL export's pg_dump or mysqldump command. A
// MySQL export naming no database returns a nil command: the caller lists the
// databases on the engine first.
func sqlDumpCommand(plane *sqlDataPlane, ec sqlExportContext) ([]string, []string, error) {
	var tables []string
	schemaOnly := false
	if o := ec.SQLExportOptions; o != nil {
		tables, schemaOnly = o.Tables, o.SchemaOnly
	}
	if len(tables) > 0 && len(ec.Databases) != 1 {
		return nil, nil, fmt.Errorf("an export of tables names one and only one database")
	}
	if plane.Engine.Family == dbengine.Postgres {
		if len(ec.Databases) != 1 {
			return nil, nil, fmt.Errorf("a PostgreSQL SQL export names one database; exporting all user databases is only available for a parallel export")
		}
		if len(tables) > 1 {
			return nil, nil, fmt.Errorf("a PostgreSQL SQL export names at most one table")
		}
		command := []string{"pg_dump", "--username=postgres", "--dbname=" + ec.Databases[0]}
		if schemaOnly {
			command = append(command, "--schema-only")
		}
		if o := ec.SQLExportOptions; o != nil && o.PostgresExportOptions != nil {
			if o.PostgresExportOptions.Clean {
				command = append(command, "--clean")
			}
			if o.PostgresExportOptions.IfExists {
				if !o.PostgresExportOptions.Clean {
					return nil, nil, fmt.Errorf("ifExists applies to the DROP statements clean produces")
				}
				command = append(command, "--if-exists")
			}
		}
		for _, table := range tables {
			command = append(command, "--table="+table)
		}
		return command, nil, nil
	}
	env, err := sqlMySQLEnv(plane)
	if err != nil {
		return nil, nil, err
	}
	if len(ec.Databases) == 0 {
		return nil, env, nil
	}
	command := []string{"mysqldump", "--user=root", "--single-transaction", "--hex-blob"}
	masterData := 0
	if o := ec.SQLExportOptions; o != nil && o.MysqlExportOptions != nil {
		masterData = o.MysqlExportOptions.MasterData
	}
	switch masterData {
	case 0:
	case 1, 2:
		command = append(command, "--source-data="+strconv.Itoa(masterData))
	default:
		return nil, nil, fmt.Errorf("masterData %d is not 1 or 2", masterData)
	}
	if masterData == 1 {
		command = append(command, "--set-gtid-purged=ON")
	} else {
		command = append(command, "--set-gtid-purged=OFF")
	}
	if schemaOnly {
		command = append(command, "--no-data")
	}
	if len(tables) > 0 {
		return append(append(command, ec.Databases[0]), tables...), env, nil
	}
	return append(append(command, "--databases"), ec.Databases...), env, nil
}

// sqlRestoreCommand builds the SQL import's psql or mysql command, which
// reads the dump on its standard input.
func sqlRestoreCommand(plane *sqlDataPlane, ic sqlImportContext) ([]string, []string, error) {
	if plane.Engine.Family == dbengine.Postgres {
		user := ic.ImportUser
		if user == "" {
			user = sqlBuiltInAdminUser(dbengine.Postgres)
		}
		database := ic.Database
		if database == "" {
			database = "postgres"
		}
		return []string{"psql", "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1", "--username=" + user, "--dbname=" + database}, nil, nil
	}
	env, err := sqlMySQLEnv(plane)
	if err != nil {
		return nil, nil, err
	}
	command := []string{"mysql", "--user=root"}
	if ic.Database != "" {
		command = append(command, ic.Database)
	}
	return command, env, nil
}

// sqlCSVCharacter decodes one CSV option's ASCII hex code ("2C" is a comma),
// or returns the default when the option is unset.
func sqlCSVCharacter(name, hexCode, fallback string) (string, error) {
	if hexCode == "" {
		return fallback, nil
	}
	decoded, err := hex.DecodeString(hexCode)
	if err != nil || len(decoded) != 1 {
		return "", fmt.Errorf("%s %q must be one character in ASCII hex code, for example \"2C\" for a comma", name, hexCode)
	}
	return string(decoded), nil
}

type sqlCSVFormat struct{ escape, quote, field, line string }

// sqlCSVFormatOf reads the CSV options, whose defaults are a double-quote
// quote and escape, a comma field delimiter and a newline line delimiter.
func sqlCSVFormatOf(family dbengine.Family, o *sqlCSVOptions) (sqlCSVFormat, error) {
	if o == nil {
		o = &sqlCSVOptions{}
	}
	if family == dbengine.Postgres && o.LinesTerminatedBy != "" {
		return sqlCSVFormat{}, fmt.Errorf("linesTerminatedBy is available only for MySQL")
	}
	var f sqlCSVFormat
	var err error
	if f.escape, err = sqlCSVCharacter("escapeCharacter", o.EscapeCharacter, `"`); err != nil {
		return f, err
	}
	if f.quote, err = sqlCSVCharacter("quoteCharacter", o.QuoteCharacter, `"`); err != nil {
		return f, err
	}
	if f.field, err = sqlCSVCharacter("fieldsTerminatedBy", o.FieldsTerminatedBy, ","); err != nil {
		return f, err
	}
	if f.line, err = sqlCSVCharacter("linesTerminatedBy", o.LinesTerminatedBy, "\n"); err != nil {
		return f, err
	}
	return f, nil
}

func (f sqlCSVFormat) postgres() string {
	return fmt.Sprintf("(FORMAT csv, DELIMITER %s, QUOTE %s, ESCAPE %s)",
		dbengine.QuoteLiteral(f.field), dbengine.QuoteLiteral(f.quote), dbengine.QuoteLiteral(f.escape))
}

func (f sqlCSVFormat) mysql() string {
	return fmt.Sprintf(" FIELDS TERMINATED BY %s OPTIONALLY ENCLOSED BY %s ESCAPED BY %s LINES TERMINATED BY %s",
		dbengine.QuoteMySQLLiteral(f.field), dbengine.QuoteMySQLLiteral(f.quote),
		dbengine.QuoteMySQLLiteral(f.escape), dbengine.QuoteMySQLLiteral(f.line))
}

// sqlMySQLFilesDir is the MySQL server's secure_file_priv directory, the one
// place INTO OUTFILE and LOAD DATA INFILE may touch.
const sqlMySQLFilesDir = "/var/lib/mysql-files/"

func sqlCSVExportCommand(family dbengine.Family, plane *sqlDataPlane, ec sqlExportContext) ([]string, []string, error) {
	o := ec.CsvExportOptions
	if o == nil || strings.TrimSpace(o.SelectQuery) == "" {
		return nil, nil, fmt.Errorf("a CSV export needs csvExportOptions.selectQuery")
	}
	format, err := sqlCSVFormatOf(family, o)
	if err != nil {
		return nil, nil, err
	}
	query := strings.TrimRight(strings.TrimSpace(o.SelectQuery), ";")
	if family == dbengine.Postgres {
		if len(ec.Databases) != 1 {
			return nil, nil, fmt.Errorf("a PostgreSQL CSV export names the one database its selectQuery reads")
		}
		return []string{"psql", "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1", "--username=postgres", "--dbname=" + ec.Databases[0],
			"--command=COPY (" + query + ") TO STDOUT WITH " + format.postgres()}, nil, nil
	}
	if len(ec.Databases) > 1 {
		return nil, nil, fmt.Errorf("a MySQL CSV export names at most one database")
	}
	env, err := sqlMySQLEnv(plane)
	if err != nil {
		return nil, nil, err
	}
	file := sqlMySQLFilesDir + sim.NewUUID() + ".csv"
	statement := query + " INTO OUTFILE " + dbengine.QuoteMySQLLiteral(file) + format.mysql()
	database := ""
	if len(ec.Databases) == 1 {
		database = ec.Databases[0]
	}
	script := `mysql --user=root ${3:+"$3"} --execute="$1" && cat "$2"; status=$?; rm -f "$2"; exit $status`
	return []string{"/bin/sh", "-c", script, "sh", statement, file, database}, env, nil
}

func sqlCSVImportCommand(family dbengine.Family, plane *sqlDataPlane, ic sqlImportContext) ([]string, []string, error) {
	o := ic.CsvImportOptions
	if o == nil || o.Table == "" {
		return nil, nil, fmt.Errorf("a CSV import needs csvImportOptions.table")
	}
	if ic.Database == "" {
		return nil, nil, fmt.Errorf("a CSV import names one database")
	}
	format, err := sqlCSVFormatOf(family, o)
	if err != nil {
		return nil, nil, err
	}
	if family == dbengine.Postgres {
		target := sqlQuoteQualified(o.Table, dbengine.QuoteIdentifier)
		if len(o.Columns) > 0 {
			target += " (" + sqlQuoteEach(o.Columns, dbengine.QuoteIdentifier) + ")"
		}
		user := ic.ImportUser
		if user == "" {
			user = sqlBuiltInAdminUser(dbengine.Postgres)
		}
		return []string{"psql", "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1", "--username=" + user, "--dbname=" + ic.Database,
			"--command=COPY " + target + " FROM STDIN WITH " + format.postgres()}, nil, nil
	}
	env, err := sqlMySQLEnv(plane)
	if err != nil {
		return nil, nil, err
	}
	file := sqlMySQLFilesDir + sim.NewUUID() + ".csv"
	statement := "LOAD DATA INFILE " + dbengine.QuoteMySQLLiteral(file) + " INTO TABLE " +
		sqlQuoteQualified(o.Table, dbengine.QuoteMySQLIdentifier) + format.mysql()
	if len(o.Columns) > 0 {
		statement += " (" + sqlQuoteEach(o.Columns, dbengine.QuoteMySQLIdentifier) + ")"
	}
	script := `cat > "$2" && mysql --user=root "$3" --execute="$1"; status=$?; rm -f "$2"; exit $status`
	return []string{"/bin/sh", "-c", script, "sh", statement, file, ic.Database}, env, nil
}

func sqlQuoteQualified(name string, quote func(string) string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		parts[i] = quote(part)
	}
	return strings.Join(parts, ".")
}

func sqlQuoteEach(names []string, quote func(string) string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}
	return strings.Join(quoted, ", ")
}

// sqlEngineContainer is the running engine container behind the data plane,
// started if the instance has not served a client yet.
func sqlEngineContainer(plane *sqlDataPlane) (sim.ExistingContainer, error) {
	if err := plane.Ensure(); err != nil {
		return sim.ExistingContainer{}, sqlTransferFailed("INTERNAL_ERROR", "start the database engine: %v", err)
	}
	found, err := sim.FindExistingContainers(plane.Labels)
	if err != nil {
		return sim.ExistingContainer{}, err
	}
	for _, c := range found {
		if c.Running {
			return c, nil
		}
	}
	return sim.ExistingContainer{}, fmt.Errorf("instance %s's database engine is not running", plane.instance)
}

// sqlEngineRun runs command in the engine container with stdin on its
// standard input, and returns its standard output; a non-zero exit fails with
// its standard error.
func sqlEngineRun(ctx context.Context, plane *sqlDataPlane, command, env []string, stdin io.Reader) ([]byte, error) {
	container, err := sqlEngineContainer(plane)
	if err != nil {
		return nil, err
	}
	release, err := sim.HoldThawed(ctx, container.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	docker := sim.DockerClient()
	created, err := docker.ExecCreate(ctx, container.ID, dockerclient.ExecCreateOptions{
		Cmd: command, Env: env, AttachStdin: stdin != nil, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create engine command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach engine command: %w", err)
	}
	defer attached.Close()
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()
	if stdin != nil {
		go func() {
			_, _ = io.Copy(attached.Conn, stdin)
			_ = attached.CloseWrite()
		}()
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("read engine command output: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	inspected, err := docker.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return nil, sqlTransferFailed("ERROR_RDBMS", "%s exited %d: %s", command[0], inspected.ExitCode, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// sqlStorageCall sends a Cloud Storage request as the instance's service
// account and turns a refusal into the operation's failure.
func sqlStorageCall(ctx context.Context, inst SQLInstance, method, path string, body []byte, contentType string) ([]byte, error) {
	principal := inst.ServiceAccountEmailAddress
	if principal == "" {
		number, ok := crmProjectNumber(inst.Project)
		if !ok {
			return nil, sqlTransferFailed("PERMISSION_DENIED", "project %s is not an active project, so instance %s has no service account", inst.Project, inst.Name)
		}
		principal = gcpServiceAgent(number, "cloud-sql")
	}
	ctx = sim.WithCaller(gcpServer.RequestContext(ctx), sim.Caller{Service: principal})
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = "storage.googleapis.com"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	now := time.Now()
	req.Header.Set("Authorization", "Bearer "+signAccessToken(principal, now, now.Add(time.Hour)))
	response := gcpServer.Call(req)
	if response.StatusCode < 400 {
		return response.Body.Bytes(), nil
	}
	var failure struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &failure)
	switch response.StatusCode {
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, sqlTransferFailed("ERROR_RDBMS", "HTTP Error 403: The service account does not have the required permissions for the bucket. %s", failure.Error.Message)
	case http.StatusNotFound:
		return nil, sqlTransferFailed("ERROR_RDBMS", "HTTP Error 404: %s", failure.Error.Message)
	case http.StatusPreconditionFailed:
		return nil, sqlTransferFailed("ERROR_RDBMS", "HTTP Error 412: the export file already exists: %s", failure.Error.Message)
	}
	return nil, sqlTransferFailed("ERROR_RDBMS", "HTTP Error %d: %s", response.StatusCode, failure.Error.Message)
}

// sqlWriteObject creates the export object, failing when it already exists.
func sqlWriteObject(ctx context.Context, inst SQLInstance, bucket, object string, data []byte) error {
	contentType := "application/sql"
	if strings.HasSuffix(object, ".gz") {
		contentType = "application/gzip"
	}
	path := "/upload/storage/v1/b/" + url.PathEscape(bucket) + "/o?uploadType=media&ifGenerationMatch=0&name=" + url.QueryEscape(object)
	_, err := sqlStorageCall(ctx, inst, http.MethodPost, path, data, contentType)
	return err
}

func sqlReadObject(ctx context.Context, inst SQLInstance, bucket, object string) ([]byte, error) {
	path := "/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(object) + "?alt=media"
	return sqlStorageCall(ctx, inst, http.MethodGet, path, nil, "")
}

// The Data API's limits (sql-executesql-instance.md).
const (
	sqlExecuteRequestLimit    = 512 << 10
	sqlExecuteResponseLimit   = 10 << 20
	sqlExecuteTimeout         = 30 * time.Second
	sqlExecuteConcurrentLimit = 10
	sqlExecuteMessageLimit    = 10
)

var sqlExecuteSlots = struct {
	sync.Mutex
	running map[string]int
}{running: map[string]int{}}

var sqlRegionalSecretVersion = regexp.MustCompile(`^projects/[^/]+/locations/([^/]+)/secrets/[^/]+/versions/[^/]+$`)

type sqlExecutePayload struct {
	SQLStatement          string `json:"sqlStatement"`
	Database              string `json:"database"`
	User                  string `json:"user"`
	PasswordSecretVersion string `json:"passwordSecretVersion"`
	AutoIamAuthn          bool   `json:"autoIamAuthn"`
	RowLimit              string `json:"rowLimit"`
	PartialResultMode     string `json:"partialResultMode"`
	Application           string `json:"application"`
}

type sqlQueryValue struct {
	Value     *string `json:"value,omitempty"`
	NullValue bool    `json:"nullValue,omitempty"`
}

type sqlQueryRow struct {
	Values []sqlQueryValue `json:"values"`
}

type sqlQueryColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type sqlQueryResult struct {
	Columns       []sqlQueryColumn `json:"columns,omitempty"`
	Rows          []sqlQueryRow    `json:"rows,omitempty"`
	Message       string           `json:"message,omitempty"`
	PartialResult bool             `json:"partialResult,omitempty"`
}

type sqlQueryMessage struct {
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type sqlStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// sqlQueryOutput collects statement results within the Data API's limits.
type sqlQueryOutput struct {
	results   []sqlQueryResult
	messages  []sqlQueryMessage
	rowLimit  int64
	allowPart bool
	size      int
	truncated bool
}

var errSQLResultTooLarge = errors.New("the result exceeds the 10 MB response limit; set partialResultMode to ALLOW_PARTIAL_RESULT to receive a truncated result")

func (o *sqlQueryOutput) message(severity, text string) {
	if len(o.messages) < sqlExecuteMessageLimit {
		o.messages = append(o.messages, sqlQueryMessage{Message: text, Severity: severity})
	}
}

// addRow appends a row to the latest result, or reports that the result is
// complete without it.
func (o *sqlQueryOutput) addRow(values []sqlQueryValue) error {
	result := &o.results[len(o.results)-1]
	if o.rowLimit > 0 && int64(len(result.Rows)) >= o.rowLimit {
		return nil
	}
	if o.truncated {
		return nil
	}
	for _, v := range values {
		if v.Value != nil {
			o.size += len(*v.Value)
		}
	}
	if o.size > sqlExecuteResponseLimit {
		if !o.allowPart {
			return errSQLResultTooLarge
		}
		o.truncated = true
		result.PartialResult = true
		return nil
	}
	result.Rows = append(result.Rows, sqlQueryRow{Values: values})
	return nil
}

func handleSQLExecuteSql(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	instance := sim.PathParam(r, "instance")
	inst, ok := sqlInstances.Get(sqlInstanceKey(project, instance))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance not found: %s", instance)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, sqlExecuteRequestLimit+1))
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if len(body) > sqlExecuteRequestLimit {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the request exceeds the Data API's 0.5 MB limit")
		return
	}
	var payload sqlExecutePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if access, _ := inst.Settings["dataApiAccess"].(string); access != "ALLOW_DATA_API" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			`The instance doesn't allow using ExecuteSql to access this instance. You can allow it by patching the instance with {settings: { dataApiAccess: "ALLOW_DATA_API" }}`)
		return
	}
	if strings.TrimSpace(payload.SQLStatement) == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "sqlStatement is required")
		return
	}
	if payload.AutoIamAuthn {
		if payload.PasswordSecretVersion != "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Secret cannot be provided when auto_iam_authn is true.")
			return
		}
		sqlUnserved("executeSql with autoIamAuthn", "the simulator serves no IAM database authentication: its engines hold no role for an IAM principal")(w, r)
		return
	}
	family, hasEngine := sqlEngineFamily(inst.DatabaseVersion)
	if !hasEngine {
		sqlUnserved("executeSql on "+inst.DatabaseVersion, "the simulator runs no engine for it")(w, r)
		return
	}
	if family == dbengine.Postgres && payload.Database == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "database is required for a PostgreSQL instance")
		return
	}
	if payload.User == "" || payload.PasswordSecretVersion == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "name the database user and the passwordSecretVersion holding its password, or set autoIamAuthn")
		return
	}
	match := sqlRegionalSecretVersion.FindStringSubmatch(payload.PasswordSecretVersion)
	if match == nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"passwordSecretVersion %q is not a regional secret version, projects/{project}/locations/{location}/secrets/{secret}/versions/{secret_version}", payload.PasswordSecretVersion)
		return
	}
	if match[1] != inst.Region {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the secret must be stored in the instance's region %s, not %s", inst.Region, match[1])
		return
	}
	password, status, reason, err := sqlAccessSecret(r, payload.PasswordSecretVersion, match[1])
	if err != nil {
		GCPErrorf(w, status, reason, "%v", err)
		return
	}
	if password == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "The Data API can't authenticate as built-in users with empty passwords.")
		return
	}
	var rowLimit int64
	if payload.RowLimit != "" {
		if rowLimit, err = strconv.ParseInt(payload.RowLimit, 10, 64); err != nil || rowLimit < 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "rowLimit %q is not a non-negative integer", payload.RowLimit)
			return
		}
	}
	plane, ok := sqlLoadDataPlane(project, instance)
	if !ok {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "instance %s has no data plane: it serves no address", instance)
		return
	}
	key := sqlInstanceKey(project, instance)
	sqlExecuteSlots.Lock()
	if sqlExecuteSlots.running[key] >= sqlExecuteConcurrentLimit {
		sqlExecuteSlots.Unlock()
		GCPErrorf(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED",
			"At most '%d' concurrent queries may be run on this instance. Try again later.", sqlExecuteConcurrentLimit)
		return
	}
	sqlExecuteSlots.running[key]++
	sqlExecuteSlots.Unlock()
	defer func() {
		sqlExecuteSlots.Lock()
		sqlExecuteSlots.running[key]--
		sqlExecuteSlots.Unlock()
	}()
	if len(inst.IpAddresses) == 0 {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "instance %s serves no address", instance)
		return
	}
	ip, _ := inst.IpAddresses[0]["ipAddress"].(string)
	// The session authenticates as the named user at the instance's own
	// endpoint, which checks the password as the engine does.
	address := net.JoinHostPort(ip, strconv.Itoa(plane.Engine.Port))
	ctx, cancel := context.WithTimeout(r.Context(), sqlExecuteTimeout)
	defer cancel()
	out := &sqlQueryOutput{rowLimit: rowLimit, allowPart: payload.PartialResultMode == "ALLOW_PARTIAL_RESULT"}
	started := time.Now()
	if family == dbengine.Postgres {
		err = sqlExecutePostgres(ctx, address, payload, password, out)
	} else {
		err = sqlExecuteMySQL(ctx, address, payload, password, out)
	}
	response := map[string]any{
		"metadata": map[string]any{"sqlStatementExecutionTime": fmt.Sprintf("%.6fs", time.Since(started).Seconds())},
	}
	if out.messages != nil {
		response["messages"] = out.messages
	}
	if err != nil {
		code := 3
		if ctx.Err() != nil {
			code, err = 4, fmt.Errorf("the request took longer than 30 seconds and was canceled")
		}
		response["status"] = sqlStatus{Code: code, Message: err.Error()}
	} else {
		response["results"] = out.results
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

// sqlAccessSecret reads the password secret version with the caller's own
// credential, so Secret Manager decides whether the caller may.
func sqlAccessSecret(r *http.Request, name, location string) (string, int, string, error) {
	req, err := http.NewRequestWithContext(gcpServer.RequestContext(r.Context()), http.MethodGet, "/v1/"+name+":access", nil)
	if err != nil {
		return "", http.StatusInternalServerError, "INTERNAL", err
	}
	req.Host = "secretmanager." + location + ".rep.googleapis.com"
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	response := gcpServer.Call(req)
	var answer struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &answer)
	if response.StatusCode >= 400 {
		return "", response.StatusCode, answer.Error.Status, fmt.Errorf("read passwordSecretVersion %s: %s", name, answer.Error.Message)
	}
	data, err := base64.StdEncoding.DecodeString(answer.Payload.Data)
	if err != nil {
		return "", http.StatusInternalServerError, "INTERNAL", fmt.Errorf("decode secret payload: %w", err)
	}
	return string(data), 0, "", nil
}

func sqlExecutePostgres(ctx context.Context, address string, payload sqlExecutePayload, password string, out *sqlQueryOutput) error {
	host, port, _ := net.SplitHostPort(address)
	config, err := pgconn.ParseConfig(fmt.Sprintf("host=%s port=%s sslmode=disable", host, port))
	if err != nil {
		return err
	}
	config.User, config.Password, config.Database = payload.User, password, payload.Database
	if payload.Application != "" {
		config.RuntimeParams["application_name"] = payload.Application
	}
	config.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		out.message(n.Severity, fmt.Sprintf("%s:  %s", n.Severity, n.Message))
	}
	conn, err := pgconn.ConnectConfig(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	types := pgtype.NewMap()
	reader := conn.Exec(ctx, payload.SQLStatement)
	for reader.NextResult() {
		rr := reader.ResultReader()
		result := sqlQueryResult{}
		for _, field := range rr.FieldDescriptions() {
			typeName := strconv.FormatUint(uint64(field.DataTypeOID), 10)
			if t, ok := types.TypeForOID(field.DataTypeOID); ok {
				typeName = t.Name
			}
			result.Columns = append(result.Columns, sqlQueryColumn{Name: field.Name, Type: typeName})
		}
		out.results = append(out.results, result)
		for rr.NextRow() {
			raw := rr.Values()
			values := make([]sqlQueryValue, len(raw))
			for i, v := range raw {
				if v == nil {
					values[i] = sqlQueryValue{NullValue: true}
					continue
				}
				text := string(v)
				values[i] = sqlQueryValue{Value: &text}
			}
			if err := out.addRow(values); err != nil {
				_, _ = rr.Close()
				_ = reader.Close()
				return err
			}
		}
		tag, err := rr.Close()
		if err != nil {
			_ = reader.Close()
			return err
		}
		out.results[len(out.results)-1].Message = tag.String()
	}
	return reader.Close()
}

func sqlExecuteMySQL(ctx context.Context, address string, payload sqlExecutePayload, password string, out *sqlQueryOutput) error {
	config := mysqldriver.NewConfig()
	config.User, config.Passwd, config.Net, config.Addr, config.DBName = payload.User, password, "tcp", address, payload.Database
	config.MultiStatements = true
	config.AllowCleartextPasswords = true
	if payload.Application != "" {
		config.ConnectionAttributes = "program_name:" + payload.Application
	}
	connector, err := mysqldriver.NewConnector(config)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	rows, err := conn.QueryContext(ctx, payload.SQLStatement)
	if err != nil {
		return err
	}
	for {
		columns, err := rows.ColumnTypes()
		if err != nil {
			_ = rows.Close()
			return err
		}
		result := sqlQueryResult{}
		for _, c := range columns {
			result.Columns = append(result.Columns, sqlQueryColumn{Name: c.Name(), Type: c.DatabaseTypeName()})
		}
		out.results = append(out.results, result)
		for rows.Next() {
			raw := make([]sql.RawBytes, len(columns))
			targets := make([]any, len(columns))
			for i := range raw {
				targets[i] = &raw[i]
			}
			if err := rows.Scan(targets...); err != nil {
				_ = rows.Close()
				return err
			}
			values := make([]sqlQueryValue, len(raw))
			for i, v := range raw {
				if v == nil {
					values[i] = sqlQueryValue{NullValue: true}
					continue
				}
				text := string(v)
				values[i] = sqlQueryValue{Value: &text}
			}
			if err := out.addRow(values); err != nil {
				_ = rows.Close()
				return err
			}
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	warnings, err := conn.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return err
	}
	defer func() { _ = warnings.Close() }()
	for warnings.Next() {
		var level, message string
		var code int
		if err := warnings.Scan(&level, &code, &message); err != nil {
			return err
		}
		out.message(strings.ToUpper(level), message)
	}
	return warnings.Err()
}
