package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sppb "cloud.google.com/go/spanner/apiv1/spannerpb"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	structpb "google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	// Pure-Go SQLite driver — same one sim.Store relies on. Registering it
	// here keeps the data plane self-contained even though the parent binary
	// already pulls the driver in transitively.
	_ "modernc.org/sqlite"
)

// Cloud Spanner gRPC data plane (google.spanner.v1.Spanner). The admin REST
// slice in spanner.go owns the instance/database/DDL/session stores; every
// RPC here reads those same stores so the REST and gRPC surfaces observe one
// consistent cloud state. The high-level cloud.google.com/go/spanner client is
// gRPC-only and reaches the simulator through SPANNER_EMULATOR_HOST, the same
// coordinate it uses for Google's own Spanner emulator.
//
// ExecuteSql / ExecuteStreamingSql / Read / StreamingRead / Commit execute
// against a real in-memory SQLite engine that is materialized from each
// database's CREATE TABLE DDL. A CREATE TABLE followed by an INSERT followed by
// a SELECT therefore returns the inserted row — no synthetic result sets. SQL
// constructs Spanner supports that SQLite cannot express (interleaved tables,
// proto-typed columns, generated columns) are rejected with a loud error at DDL
// time so the untranslatable table is simply absent from the backing store,
// rather than returning empty rows at query time.

type spannerDataGRPC struct {
	sppb.UnimplementedSpannerServer
}

func registerSpannerGRPC(gs *grpc.Server) {
	sppb.RegisterSpannerServer(gs, &spannerDataGRPC{})
}

// ---------------------------------------------------------------------------
// per-database SQLite backing engine
// ---------------------------------------------------------------------------

// spannerBackend holds the materialized SQLite engine for one database plus
// the count of DDL statements already applied, so a subsequent
// UpdateDatabaseDdl that appends statements can be reconciled incrementally.
type spannerBackend struct {
	mu              sync.Mutex
	db              *sql.DB
	appliedDDLCount int
}

var (
	spannerBackends      = map[string]*spannerBackend{}
	spannerBackendsMutex sync.Mutex
	spannerTxns          = map[string]*spannerTxnRuntime{}
	spannerTxnsMutex     sync.Mutex
	spannerPartitions    = map[string]spannerPartitionRuntime{}
	spannerPartitionsMu  sync.RWMutex
)

// spannerTxnRuntime owns the real SQLite transaction behind one Spanner
// transaction ID. Reads, SQL DML, mutations, commit, and rollback all use this
// same transaction, so uncommitted writes remain isolated and rollback really
// discards them.
type spannerTxnRuntime struct {
	mu             sync.Mutex
	session        string
	database       string
	readOnly       bool
	partitionedDML bool
	tx             *sql.Tx
	lastSeqno      int64
	lastStatements bool
	dmlResponses   map[int64]*sppb.ResultSet
	batchResponses map[int64]*sppb.ExecuteBatchDmlResponse
}

type spannerPartitionRuntime struct {
	session       string
	transactionID []byte
	query         *sppb.PartitionQueryRequest
	read          *sppb.PartitionReadRequest
}

// spannerDataDir returns the directory holding file-backed Spanner engines,
// or "" when the simulator runs without a data directory (engines are then
// in-memory, matching the rest of the process-lifetime state).
func spannerDataDir() string {
	dir := os.Getenv("SIM_DATA_DIR")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "spanner")
}

// spannerBackendFile derives the deterministic, filesystem-safe backing
// filename for a database resource name: the name with every non-portable
// rune replaced, plus a short digest of the original name so two names that
// sanitize identically never share a file.
func spannerBackendFile(dbName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '.', r == '_':
			return r
		default:
			return '_'
		}
	}, dbName)
	sum := sha256.Sum256([]byte(dbName))
	return safe + "-" + hex.EncodeToString(sum[:8]) + ".db"
}

// The engine records how many of the database's DDL statements it has already
// applied inside the engine itself, so a file-backed database reopened after a
// restart does not re-run CREATE TABLE statements against its existing schema.
func spannerReadAppliedDDLCount(db *sql.DB) (int, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _sockerless_applied_ddl (id INTEGER PRIMARY KEY CHECK (id = 0), count INTEGER NOT NULL)`); err != nil {
		return 0, err
	}
	var n int
	err := db.QueryRow(`SELECT count FROM _sockerless_applied_ddl WHERE id = 0`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func spannerWriteAppliedDDLCount(db *sql.DB, n int) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO _sockerless_applied_ddl (id, count) VALUES (0, ?)`, n)
	return err
}

// spannerBackendFor returns the materialized SQLite backend for a database,
// creating it and applying any pending DDL on first access. DDL statements
// beyond what has already been applied are reconciled incrementally, mirroring
// how UpdateDatabaseDdl extends a database's schema over time.
func spannerBackendFor(dbName string) (*spannerBackend, error) {
	spannerBackendsMutex.Lock()
	defer spannerBackendsMutex.Unlock()
	b, ok := spannerBackends[dbName]
	if !ok {
		// In-memory by default: the unique shared cache name keeps each
		// database isolated; modernc honors file::memory:?cache=shared
		// semantics with a unique id so concurrent databases do not collide.
		// With a data directory the engine is a real file under
		// <SIM_DATA_DIR>/spanner so committed rows survive a restart the way
		// the SQLite-backed schema/session stores do.
		dsn := "file:" + dbName + "?mode=memory&cache=shared"
		if dir := spannerDataDir(); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, status.Errorf(codes.Internal, "create Spanner data dir for %s: %v", dbName, err)
			}
			dsn = "file:" + filepath.Join(dir, spannerBackendFile(dbName))
		}
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "open backing store for %s: %v", dbName, err)
		}
		if err := sqlDB.Ping(); err != nil {
			_ = sqlDB.Close()
			return nil, status.Errorf(codes.Internal, "ping backing store for %s: %v", dbName, err)
		}
		applied, err := spannerReadAppliedDDLCount(sqlDB)
		if err != nil {
			_ = sqlDB.Close()
			return nil, status.Errorf(codes.Internal, "read applied DDL count for %s: %v", dbName, err)
		}
		b = &spannerBackend{db: sqlDB, appliedDDLCount: applied}
		spannerBackends[dbName] = b
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ddl, hasDDL := spannerDDLs.Get(dbName)
	if !hasDDL || len(ddl.Statements) == 0 {
		return b, nil
	}
	for i := b.appliedDDLCount; i < len(ddl.Statements); i++ {
		stmt := ddl.Statements[i]
		translated, ok := spannerTranslateDDL(stmt)
		if !ok {
			return nil, status.Errorf(codes.Unimplemented, "Cloud Spanner DDL is not supported: %s", stmt)
		}
		if _, err := b.db.Exec(translated); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "apply Cloud Spanner DDL %q: %v", stmt, err)
		}
		b.appliedDDLCount++
		if err := spannerWriteAppliedDDLCount(b.db, b.appliedDDLCount); err != nil {
			return nil, status.Errorf(codes.Internal, "record applied DDL for %s: %v", dbName, err)
		}
	}
	return b, nil
}

// spannerDropBackend closes and discards a database's materialized engine and
// removes its file backing, so DropDatabase releases the rows together with
// the schema. An absent file (the database was never queried) is not an error.
func spannerDropBackend(dbName string) error {
	spannerBackendsMutex.Lock()
	b := spannerBackends[dbName]
	delete(spannerBackends, dbName)
	spannerBackendsMutex.Unlock()
	var errs []error
	if b != nil {
		b.mu.Lock()
		errs = append(errs, b.db.Close())
		b.mu.Unlock()
	}
	if dir := spannerDataDir(); dir != "" {
		if err := os.Remove(filepath.Join(dir, spannerBackendFile(dbName))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// DDL translation: Spanner SQL dialect → SQLite
// ---------------------------------------------------------------------------

var (
	spannerInterleaveRe = regexp.MustCompile(`(?is)\s*,?\s*INTERLEAVE\s+IN\s+PARENT\b.*$`)
)

// spannerTranslateDDL converts a Spanner DDL statement to a SQLite-equivalent
// statement. Unsupported statements return ok=false and make UpdateDatabaseDdl
// complete with an UNIMPLEMENTED operation error; schema updates are never
// silently skipped.
func spannerTranslateDDL(stmt string) (string, bool) {
	trimmed := strings.TrimSpace(stmt)
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "CREATE TABLE"):
		return spannerTranslateCreateTable(trimmed)
	case strings.HasPrefix(upper, "DROP TABLE"),
		strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, " ADD COLUMN "),
		strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, " DROP COLUMN "),
		strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, " RENAME TO "):
		return spannerRewriteTypes(trimmed), true
	case strings.HasPrefix(upper, "CREATE") && strings.Contains(upper, "INDEX"):
		return trimmed, true
	case strings.HasPrefix(upper, "DROP INDEX"):
		return trimmed, true
	case strings.HasPrefix(upper, "CREATE VIEW"),
		strings.HasPrefix(upper, "DROP VIEW"):
		return spannerRewriteTypes(trimmed), true
	}
	return "", false
}

// spannerApplyDDLStatements executes one UpdateDatabaseDdl request atomically
// against the database's real backing engine. The durable DDL store is updated
// only after the backing schema commits, so a rejected statement never becomes
// a latent failure on the next data-plane request.
func spannerApplyDDLStatements(dbName string, statements []string) error {
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tx, err := b.db.Begin()
	if err != nil {
		return status.Errorf(codes.Internal, "begin Cloud Spanner schema update: %v", err)
	}
	for _, stmt := range statements {
		translated, ok := spannerTranslateDDL(stmt)
		if !ok {
			_ = tx.Rollback()
			return status.Errorf(codes.Unimplemented, "Cloud Spanner DDL is not supported: %s", stmt)
		}
		if _, err := tx.Exec(translated); err != nil {
			_ = tx.Rollback()
			return status.Errorf(codes.InvalidArgument, "apply Cloud Spanner DDL %q: %v", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return status.Errorf(codes.Internal, "commit Cloud Spanner schema update: %v", err)
	}
	b.appliedDDLCount += len(statements)
	if err := spannerWriteAppliedDDLCount(b.db, b.appliedDDLCount); err != nil {
		return status.Errorf(codes.Internal, "record applied DDL for %s: %v", dbName, err)
	}
	return nil
}

// spannerCreateTableHeadRe captures the CREATE TABLE keyword, the table name,
// and everything after the opening paren of the column list. The column list's
// matching close paren is located by balanced paren scanning in the caller, so
// nested parens inside column types (STRING(100), ARRAY<...>) survive.
var spannerCreateTableHeadRe = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "`?" + `([A-Za-z0-9_\.]+)` + "`?" + `\s*\((.*)$`)

// spannerTranslateCreateTable rewrites a Spanner CREATE TABLE into a SQLite one
// the engine will accept: Spanner-specific column types are mapped onto
// SQLite's dynamic typing, and clauses SQLite has no analogue for (INTERLEAVE
// IN PARENT, ON DELETE CASCADE) are dropped. The trailing PRIMARY KEY (...)
// clause Spanner places after the column list is moved inside the parentheses
// as a table-level constraint, which is where SQLite expects it.
func spannerTranslateCreateTable(stmt string) (string, bool) {
	m := spannerCreateTableHeadRe.FindStringSubmatch(stmt)
	if m == nil {
		return "", false
	}
	table := m[1]
	rest := m[2]
	// Scan for the matching close paren of the column list, accounting for
	// nested parens (STRING(100), ARRAY<...> when rewritten, etc.).
	depth := 1
	close := -1
	for i, r := range rest {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				close = i
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 {
		return "", false
	}
	body := rest[:close]
	trailing := strings.TrimSpace(rest[close+1:])
	trailing = spannerInterleaveRe.ReplaceAllString(trailing, "")
	trailing = regexp.MustCompile(`(?is)\s*,?\s*ON\s+DELETE\s+\w+\s*\(?[^)]*\)?`).ReplaceAllString(trailing, "")
	trailing = strings.TrimSpace(strings.Trim(strings.TrimSpace(trailing), ","))
	body = spannerRewriteTypes(body)
	inner := body
	if trailing != "" {
		inner += ", " + trailing
	}
	return "CREATE TABLE " + quoteIdent(table) + " (" + inner + ")", true
}

// spannerTypeRe matches Spanner column type tokens. It also handles the
// column-name prefix so the rewrite leaves names intact.
var spannerTypeRe = regexp.MustCompile(`(?i)\b(ARRAY\s*<[^>]+>|STRING\s*\(\s*(?:MAX|\d+)\s*\)|BYTES\s*\(\s*(?:MAX|\d+)\s*\)|INT64\b|FLOAT64\b|BOOL\b|TIMESTAMP\b|DATE\b|NUMERIC\b|JSON\b|TOKENLIST\b)`)

func spannerRewriteTypes(s string) string {
	return spannerTypeRe.ReplaceAllStringFunc(s, func(tok string) string {
		upper := strings.ToUpper(strings.TrimSpace(tok))
		switch {
		case strings.HasPrefix(upper, "INT64"):
			return "INTEGER"
		case strings.HasPrefix(upper, "FLOAT64"), strings.HasPrefix(upper, "NUMERIC"):
			return "REAL"
		case strings.HasPrefix(upper, "BOOL"):
			return "INTEGER"
		case strings.HasPrefix(upper, "BYTES"):
			return "BLOB"
		case strings.HasPrefix(upper, "ARRAY"):
			// Arrays are stored as a JSON-encoded TEXT column.
			return "TEXT"
		case strings.HasPrefix(upper, "STRING"),
			strings.HasPrefix(upper, "TIMESTAMP"),
			strings.HasPrefix(upper, "DATE"),
			strings.HasPrefix(upper, "JSON"),
			strings.HasPrefix(upper, "TOKENLIST"):
			return "TEXT"
		}
		return tok
	})
}

func quoteIdent(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}

// ---------------------------------------------------------------------------
// path / name normalization
// ---------------------------------------------------------------------------

// spannerSessionDatabase resolves a session name to its parent database full
// name (projects/.../databases/...). Returns "" if the session does not exist.
func spannerSessionDatabase(sessionName string) (string, error) {
	sess, ok := spannerSessions.Get(sessionName)
	if !ok {
		return "", status.Errorf(codes.NotFound, "session not found: %s", sessionName)
	}
	// Session name = projects/{p}/instances/{i}/databases/{d}/sessions/{s}
	idx := strings.LastIndex(sess.Name, "/sessions/")
	if idx < 0 {
		return "", status.Errorf(codes.InvalidArgument, "malformed session name: %s", sess.Name)
	}
	return sess.Name[:idx], nil
}

// ---------------------------------------------------------------------------
// proto Value <-> Go value conversion (per Spanner Type)
// ---------------------------------------------------------------------------

// spannerProtoToGo converts a proto Value to a Go value suitable for binding
// to a SQLite parameter, guided by the column's Spanner type. INT64 values may
// arrive as string_value (canonical Spanner encoding) or number_value; both are
// honored.
func spannerProtoToGo(v *structpb.Value, t *sppb.Type) any {
	if v == nil {
		return nil
	}
	switch t.GetCode() {
	case sppb.TypeCode_INT64:
		switch k := v.Kind.(type) {
		case *structpb.Value_StringValue:
			if n, err := strconv.ParseInt(k.StringValue, 10, 64); err == nil {
				return n
			}
			return k.StringValue
		case *structpb.Value_NumberValue:
			return int64(k.NumberValue)
		case *structpb.Value_NullValue:
			return nil
		}
	case sppb.TypeCode_FLOAT64, sppb.TypeCode_NUMERIC:
		switch k := v.Kind.(type) {
		case *structpb.Value_NumberValue:
			return k.NumberValue
		case *structpb.Value_StringValue:
			if f, err := strconv.ParseFloat(k.StringValue, 64); err == nil {
				return f
			}
			return k.StringValue
		case *structpb.Value_NullValue:
			return nil
		}
	case sppb.TypeCode_BOOL:
		switch k := v.Kind.(type) {
		case *structpb.Value_BoolValue:
			return k.BoolValue
		case *structpb.Value_NullValue:
			return nil
		}
	case sppb.TypeCode_BYTES:
		switch k := v.Kind.(type) {
		case *structpb.Value_StringValue:
			if b, err := base64.StdEncoding.DecodeString(k.StringValue); err == nil {
				return b
			}
			return []byte(k.StringValue)
		case *structpb.Value_NullValue:
			return nil
		}
	case sppb.TypeCode_ARRAY:
		// Arrays bind as a JSON string in the TEXT column.
		switch k := v.Kind.(type) {
		case *structpb.Value_ListValue:
			return spannerArrayToJSON(k.ListValue)
		case *structpb.Value_NullValue:
			return nil
		}
	}
	// STRING, DATE, TIMESTAMP, JSON and anything else: take the string form.
	switch k := v.Kind.(type) {
	case *structpb.Value_StringValue:
		return k.StringValue
	case *structpb.Value_NullValue:
		return nil
	case *structpb.Value_NumberValue:
		// No type hint — let SQLite store the number natively.
		return k.NumberValue
	case *structpb.Value_BoolValue:
		return k.BoolValue
	}
	return nil
}

func spannerArrayToJSON(lv *structpb.ListValue) string {
	parts := make([]string, 0, len(lv.GetValues()))
	for _, e := range lv.GetValues() {
		switch k := e.Kind.(type) {
		case *structpb.Value_StringValue:
			b, _ := json.Marshal(k.StringValue)
			parts = append(parts, string(b))
		case *structpb.Value_NumberValue:
			parts = append(parts, strconv.FormatFloat(k.NumberValue, 'f', -1, 64))
		case *structpb.Value_BoolValue:
			parts = append(parts, strconv.FormatBool(k.BoolValue))
		case *structpb.Value_NullValue:
			parts = append(parts, "null")
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// spannerGoToProto converts a Go value read from SQLite back to a proto Value,
// guided by the column's Spanner type.
func spannerGoToProto(v any, t *sppb.Type) *structpb.Value {
	if v == nil {
		return &structpb.Value{Kind: &structpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
	}
	switch t.GetCode() {
	case sppb.TypeCode_INT64:
		switch n := v.(type) {
		case int64:
			return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: strconv.FormatInt(n, 10)}}
		case int:
			return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: strconv.FormatInt(int64(n), 10)}}
		case float64:
			return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: strconv.FormatInt(int64(n), 10)}}
		}
	case sppb.TypeCode_FLOAT64, sppb.TypeCode_NUMERIC:
		if f, ok := v.(float64); ok {
			return &structpb.Value{Kind: &structpb.Value_NumberValue{NumberValue: f}}
		}
	case sppb.TypeCode_BOOL:
		switch b := v.(type) {
		case bool:
			return &structpb.Value{Kind: &structpb.Value_BoolValue{BoolValue: b}}
		case int64:
			return &structpb.Value{Kind: &structpb.Value_BoolValue{BoolValue: b != 0}}
		case float64:
			return &structpb.Value{Kind: &structpb.Value_BoolValue{BoolValue: int64(b) != 0}}
		}
	case sppb.TypeCode_BYTES:
		if b, ok := v.([]byte); ok {
			return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: base64.StdEncoding.EncodeToString(b)}}
		}
	case sppb.TypeCode_ARRAY:
		if s, ok := v.(string); ok {
			return spannerArrayFromString(s, t.GetArrayElementType())
		}
	}
	// STRING / DATE / TIMESTAMP / JSON / fallback: text representation.
	return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: fmt.Sprintf("%v", v)}}
}

// spannerArrayFromString parses a JSON array stored in a TEXT column back into a
// proto ListValue of the element type.
func spannerArrayFromString(s string, elem *sppb.Type) *structpb.Value {
	var raw []any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return &structpb.Value{Kind: &structpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
	}
	out := &structpb.ListValue{Values: make([]*structpb.Value, 0, len(raw))}
	for _, e := range raw {
		out.Values = append(out.Values, anyValueToProto(e, elem))
	}
	return &structpb.Value{Kind: &structpb.Value_ListValue{ListValue: out}}
}

func anyValueToProto(e any, t *sppb.Type) *structpb.Value {
	switch x := e.(type) {
	case string:
		return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: x}}
	case float64:
		if t != nil && t.GetCode() == sppb.TypeCode_INT64 {
			return &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: strconv.FormatInt(int64(x), 10)}}
		}
		return &structpb.Value{Kind: &structpb.Value_NumberValue{NumberValue: x}}
	case bool:
		return &structpb.Value{Kind: &structpb.Value_BoolValue{BoolValue: x}}
	case nil:
		return &structpb.Value{Kind: &structpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
	}
	return &structpb.Value{Kind: &structpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
}

// spannerTypeForSQLite maps a SQLite declared column type to the closest
// Spanner Type.
func spannerTypeForSQLite(declType string) *sppb.Type {
	upper := strings.ToUpper(strings.TrimSpace(declType))
	switch {
	case strings.Contains(upper, "INT"):
		return &sppb.Type{Code: sppb.TypeCode_INT64}
	case strings.Contains(upper, "REAL") || strings.Contains(upper, "FLOA") || strings.Contains(upper, "DOUB"):
		return &sppb.Type{Code: sppb.TypeCode_FLOAT64}
	case strings.Contains(upper, "BLOB"):
		return &sppb.Type{Code: sppb.TypeCode_BYTES}
	case strings.Contains(upper, "BOOL"):
		return &sppb.Type{Code: sppb.TypeCode_BOOL}
	}
	return &sppb.Type{Code: sppb.TypeCode_STRING}
}

// ---------------------------------------------------------------------------
// metadata helpers
// ---------------------------------------------------------------------------

// spannerTableColumns returns the SQLite column names and Spanner types for a
// table, in declared order. Used to shape ResultSet row types and to coerce
// mutation values to the right Go type.
func spannerTableColumns(b *spannerBackend, table string) ([]string, []*sppb.Type, error) {
	rows, err := b.db.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "read table_info for %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	var types []*sppb.Type
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, nil, status.Errorf(codes.Internal, "scan table_info: %v", err)
		}
		names = append(names, name)
		types = append(types, spannerTypeForSQLite(ctype))
	}
	if err := rows.Err(); err != nil {
		return nil, nil, status.Errorf(codes.Internal, "table_info rows: %v", err)
	}
	if len(names) == 0 {
		return nil, nil, status.Errorf(codes.NotFound, "table not found: %s", table)
	}
	return names, types, nil
}

// spannerPrimaryKeyColumns returns the primary-key column names of a table in
// SQLite-defined order. Used to scope UPDATE/DELETE mutations and point reads.
func spannerPrimaryKeyColumns(b *spannerBackend, table string) ([]string, error) {
	rows, err := b.db.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read table_info for %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var pkCols []string
	maxPK := 0
	pkOrder := map[string]int{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, status.Errorf(codes.Internal, "scan table_info: %v", err)
		}
		if pk > 0 {
			pkOrder[name] = pk
			if pk > maxPK {
				maxPK = pk
			}
		}
	}
	// Emit in pk-index order.
	for i := 1; i <= maxPK; i++ {
		for name, idx := range pkOrder {
			if idx == i {
				pkCols = append(pkCols, name)
			}
		}
	}
	if len(pkCols) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "table %s has no primary key; mutations and point reads require one", table)
	}
	return pkCols, nil
}

// ---------------------------------------------------------------------------
// query execution core
// ---------------------------------------------------------------------------

// spannerExecResult captures everything needed to shape a ResultSet: the column
// names, their Spanner types, and the materialized row values (already in proto
// form).
type spannerExecResult struct {
	columns []string
	types   []*sppb.Type
	rows    []*structpb.ListValue
}

type spannerQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type spannerExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// spannerRunQuery executes a SQL statement (with optional bind args) against
// the database's SQLite backend and returns its rows shaped for Spanner's
// ResultSet. The column types are derived from the SQLite result columns; for
// SELECTs these come from the declared schema, for expressions they default to
// STRING. Args may be sql.NamedArg (for @name placeholders in ExecuteSql) or
// plain positional values (for ? placeholders in Read).
func spannerRunQuery(ctx context.Context, b *spannerBackend, queryer spannerQueryer, sqlText string, args []any) (*spannerExecResult, error) {
	q, err := queryer.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "execute sql: %v", err)
	}
	defer func() { _ = q.Close() }()

	colNames, err := q.Columns()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "result columns: %v", err)
	}
	colTypes, err := q.ColumnTypes()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "column types: %v", err)
	}
	res := &spannerExecResult{
		columns: colNames,
		types:   make([]*sppb.Type, len(colNames)),
	}
	// Prefer the table's declared types (richer than the runtime affinity SQLite
	// reports via ColumnTypes.DatabaseTypeName, which is often blank for
	// expressions). For a plain SELECT col1, col2 FROM table the declared type
	// is recoverable by mapping the column name back through the table schema;
	// for expressions we fall back to DatabaseTypeName.
	tableColTypes := spannerLookupSelectTypes(b, sqlText, colNames, colTypes)
	for i := range colNames {
		if tableColTypes != nil && tableColTypes[i] != nil {
			res.types[i] = tableColTypes[i]
		} else {
			res.types[i] = spannerTypeForSQLite(spannerColumnDeclType(colTypes[i]))
		}
	}

	for q.Next() {
		raw := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := q.Scan(ptrs...); err != nil {
			return nil, status.Errorf(codes.Internal, "scan row: %v", err)
		}
		lv := &structpb.ListValue{Values: make([]*structpb.Value, len(raw))}
		for i, v := range raw {
			lv.Values[i] = spannerGoToProto(spannerNormalizeScanValue(v), res.types[i])
		}
		res.rows = append(res.rows, lv)
	}
	if err := q.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "query rows: %v", err)
	}
	return res, nil
}

func spannerIsDML(sqlText string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sqlText))
	return strings.HasPrefix(upper, "INSERT ") ||
		strings.HasPrefix(upper, "UPDATE ") ||
		strings.HasPrefix(upper, "DELETE ")
}

func spannerRunDML(ctx context.Context, execer spannerExecer, sqlText string, args []any) (*sppb.ResultSet, error) {
	if !spannerIsDML(sqlText) {
		return nil, status.Error(codes.InvalidArgument, "statement is not Cloud Spanner DML")
	}
	result, err := execer.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "execute DML: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read DML affected-row count: %v", err)
	}
	return &sppb.ResultSet{
		Metadata: &sppb.ResultSetMetadata{},
		Stats: &sppb.ResultSetStats{
			RowCount: &sppb.ResultSetStats_RowCountExact{RowCountExact: rows},
		},
	}, nil
}

// spannerColumnDeclType extracts the declared type name a *sql.ColumnType
// reports, falling back to an empty string.
func spannerColumnDeclType(ct *sql.ColumnType) string {
	if ct == nil {
		return ""
	}
	return ct.DatabaseTypeName()
}

// spannerBindArgs turns the params Struct into a slice of sql.NamedArg bound to
// the @name placeholders Spanner SQL uses. SQLite (modernc) honors @name named
// parameters natively, so the SQL text is passed through unchanged.
func spannerBindArgs(params *structpb.Struct, paramTypes map[string]*sppb.Type) []any {
	if params == nil {
		return nil
	}
	args := make([]any, 0, len(params.GetFields()))
	for name, val := range params.GetFields() {
		t := paramTypes[name]
		args = append(args, sql.Named(name, spannerProtoToGo(val, t)))
	}
	return args
}

// spannerNormalizeScanValue coerces a value read out of SQLite into one of the
// canonical Go types the proto converter understands.
func spannerNormalizeScanValue(v any) any {
	switch x := v.(type) {
	case []byte:
		// SQLite returns TEXT as []byte; turn it back into a string so it shapes
		// as a Spanner STRING rather than BYTES.
		return string(x)
	}
	return v
}

// spannerLookupSelectTypes best-effort maps a SELECT's result columns to the
// declared Spanner types of the underlying table columns, by recognizing the
// common "SELECT cols FROM table" shape the high-level client emits for point
// and range reads. Returns nil if the shape is not recognized.
var spannerSimpleSelectRe = regexp.MustCompile(`(?is)^\s*SELECT\s+(.+?)\s+FROM\s+([A-Za-z0-9_\.]+)(?:\s|$)`)

func spannerLookupSelectTypes(b *spannerBackend, sqlText string, colNames []string, colTypes []*sql.ColumnType) []*sppb.Type {
	m := spannerSimpleSelectRe.FindStringSubmatch(sqlText)
	if m == nil {
		return nil
	}
	colsPart := strings.TrimSpace(m[1])
	table := strings.Trim(m[2], "`\"")
	// Only handle the simple comma-separated column list (no expressions, no
	// aliases); anything else falls through to the runtime type guess.
	var want []string
	for _, c := range strings.Split(colsPart, ",") {
		want = append(want, strings.Trim(strings.TrimSpace(c), "`\""))
	}
	declNames, declTypes, err := spannerTableColumns(b, table)
	if err != nil {
		return nil
	}
	byName := map[string]*sppb.Type{}
	for i, n := range declNames {
		byName[n] = declTypes[i]
	}
	out := make([]*sppb.Type, len(colNames))
	for i, name := range colNames {
		// Map by position when the request column order matches the SELECT list
		// order; otherwise resolve by name.
		if i < len(want) {
			if t, ok := byName[want[i]]; ok {
				out[i] = t
				continue
			}
		}
		if t, ok := byName[name]; ok {
			out[i] = t
		}
	}
	return out
}

// spannerShapeResultSet builds a non-streaming ResultSet from an exec result.
func spannerShapeResultSet(r *spannerExecResult) *sppb.ResultSet {
	fields := make([]*sppb.StructType_Field, len(r.columns))
	for i, name := range r.columns {
		fields[i] = &sppb.StructType_Field{Name: name, Type: r.types[i]}
	}
	return &sppb.ResultSet{
		Metadata: &sppb.ResultSetMetadata{RowType: &sppb.StructType{Fields: fields}},
		Rows:     r.rows,
	}
}

// ---------------------------------------------------------------------------
// Sessions RPCs
// ---------------------------------------------------------------------------

// spannerResolveTxn interprets a request's TransactionSelector. When the
// selector asks to begin a transaction, a new id is minted and recorded against
// the database, and begun=true so the caller can surface it in the response
// metadata (the high-level client relies on this inline-begin path for
// ReadWriteTransaction). A selector carrying an existing id is validated against
// the store; single-use and absent selectors are no-ops.
func spannerBeginTxn(session, dbName string, readOnly, partitionedDML bool) ([]byte, *spannerTxnRuntime, error) {
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return nil, nil, err
	}
	// A Spanner transaction outlives the BeginTransaction RPC that created it.
	// database/sql rolls back a BeginTx transaction when its context ends, so use
	// Begin and let Commit, Rollback, or session deletion own the lifecycle.
	tx, err := b.db.Begin()
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "begin Cloud Spanner transaction: %v", err)
	}
	id := generateUUID()
	runtime := &spannerTxnRuntime{
		session:        session,
		database:       dbName,
		readOnly:       readOnly,
		partitionedDML: partitionedDML,
		tx:             tx,
		dmlResponses:   map[int64]*sppb.ResultSet{},
		batchResponses: map[int64]*sppb.ExecuteBatchDmlResponse{},
	}
	spannerTxnsMutex.Lock()
	spannerTxns[id] = runtime
	spannerTxnsMutex.Unlock()
	return []byte(id), runtime, nil
}

func spannerGetTxn(id []byte) (*spannerTxnRuntime, bool) {
	spannerTxnsMutex.Lock()
	defer spannerTxnsMutex.Unlock()
	runtime, ok := spannerTxns[string(id)]
	return runtime, ok
}

func spannerDeleteTxn(id []byte) {
	spannerTxnsMutex.Lock()
	delete(spannerTxns, string(id))
	spannerTxnsMutex.Unlock()
}

func spannerRollbackSessionTransactions(session string) {
	spannerTxnsMutex.Lock()
	var doomed []*spannerTxnRuntime
	for id, runtime := range spannerTxns {
		if runtime.session == session {
			doomed = append(doomed, runtime)
			delete(spannerTxns, id)
		}
	}
	spannerTxnsMutex.Unlock()
	for _, runtime := range doomed {
		runtime.mu.Lock()
		_ = runtime.tx.Rollback()
		runtime.mu.Unlock()
	}
	spannerPartitionsMu.Lock()
	for token, partition := range spannerPartitions {
		if partition.session == session {
			delete(spannerPartitions, token)
		}
	}
	spannerPartitionsMu.Unlock()
}

func spannerResolveTxn(session, dbName string, sel *sppb.TransactionSelector) (id []byte, begun bool, runtime *spannerTxnRuntime, err error) {
	if sel == nil {
		return nil, false, nil, nil
	}
	switch sel.Selector.(type) {
	case *sppb.TransactionSelector_Begin:
		readOnly := sel.GetBegin().GetReadOnly() != nil
		partitionedDML := sel.GetBegin().GetPartitionedDml() != nil
		newID, runtime, err := spannerBeginTxn(session, dbName, readOnly, partitionedDML)
		return newID, true, runtime, err
	case *sppb.TransactionSelector_Id:
		runtime, ok := spannerGetTxn(sel.GetId())
		if !ok {
			return nil, false, nil, status.Error(codes.InvalidArgument, "invalid transaction id")
		}
		if runtime.database != dbName || runtime.session != session {
			return nil, false, nil, status.Error(codes.InvalidArgument, "transaction does not belong to this session")
		}
		return sel.GetId(), false, runtime, nil
	}
	return nil, false, nil, nil
}

func (s *spannerDataGRPC) CreateSession(_ context.Context, req *sppb.CreateSessionRequest) (*sppb.Session, error) {
	dbName := req.GetDatabase()
	if _, ok := spannerDatabases.Get(dbName); !ok {
		return nil, status.Errorf(codes.NotFound, "database not found: %s", dbName)
	}
	if _, err := spannerBackendFor(dbName); err != nil {
		return nil, err
	}
	sess := req.GetSession()
	if sess == nil {
		sess = &sppb.Session{}
	}
	sessionID := generateUUID()
	out := &sppb.Session{
		Name:        dbName + "/sessions/" + sessionID,
		Labels:      sess.GetLabels(),
		CreateTime:  timestamppb.Now(),
		CreatorRole: sess.GetCreatorRole(),
	}
	spannerSessions.Put(out.Name, spannerSession{
		Name:       out.Name,
		CreateTime: time.Now().UTC().Format(time.RFC3339Nano),
		Labels:     out.Labels,
	})
	return out, nil
}

func (s *spannerDataGRPC) BatchCreateSessions(_ context.Context, req *sppb.BatchCreateSessionsRequest) (*sppb.BatchCreateSessionsResponse, error) {
	dbName := req.GetDatabase()
	if _, ok := spannerDatabases.Get(dbName); !ok {
		return nil, status.Errorf(codes.NotFound, "database not found: %s", dbName)
	}
	if _, err := spannerBackendFor(dbName); err != nil {
		return nil, err
	}
	count := int(req.GetSessionCount())
	if count <= 0 {
		return nil, status.Error(codes.InvalidArgument, "session_count must be greater than zero")
	}
	tmpl := req.GetSessionTemplate()
	resp := &sppb.BatchCreateSessionsResponse{Session: make([]*sppb.Session, 0, count)}
	for i := 0; i < count; i++ {
		sessionID := generateUUID()
		sess := &sppb.Session{
			Name:        dbName + "/sessions/" + sessionID,
			Labels:      tmpl.GetLabels(),
			CreateTime:  timestamppb.Now(),
			CreatorRole: tmpl.GetCreatorRole(),
		}
		spannerSessions.Put(sess.Name, spannerSession{
			Name:       sess.Name,
			CreateTime: time.Now().UTC().Format(time.RFC3339Nano),
			Labels:     sess.Labels,
		})
		resp.Session = append(resp.Session, sess)
	}
	return resp, nil
}

func (s *spannerDataGRPC) GetSession(_ context.Context, req *sppb.GetSessionRequest) (*sppb.Session, error) {
	stored, ok := spannerSessions.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session not found: %s", req.GetName())
	}
	return spannerStoredSessionToProto(stored), nil
}

func (s *spannerDataGRPC) ListSessions(_ context.Context, req *sppb.ListSessionsRequest) (*sppb.ListSessionsResponse, error) {
	prefix := req.GetDatabase() + "/sessions/"
	out := spannerSessions.Filter(func(sess spannerSession) bool { return strings.HasPrefix(sess.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	resp := &sppb.ListSessionsResponse{Sessions: make([]*sppb.Session, 0, len(out))}
	for _, sess := range out {
		resp.Sessions = append(resp.Sessions, spannerStoredSessionToProto(sess))
	}
	return resp, nil
}

func (s *spannerDataGRPC) DeleteSession(_ context.Context, req *sppb.DeleteSessionRequest) (*emptypb.Empty, error) {
	if !spannerSessions.Delete(req.GetName()) {
		return nil, status.Errorf(codes.NotFound, "session not found: %s", req.GetName())
	}
	spannerRollbackSessionTransactions(req.GetName())
	return &emptypb.Empty{}, nil
}

func spannerStoredSessionToProto(s spannerSession) *sppb.Session {
	out := &sppb.Session{Name: s.Name, Labels: s.Labels}
	if s.CreateTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.CreateTime); err == nil {
			out.CreateTime = timestamppb.New(t.UTC())
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// ExecuteSql / ExecuteStreamingSql
// ---------------------------------------------------------------------------

func (s *spannerDataGRPC) ExecuteSql(ctx context.Context, req *sppb.ExecuteSqlRequest) (*sppb.ResultSet, error) {
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return nil, err
	}
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return nil, err
	}
	if err := spannerValidateQueryPartition(req); err != nil {
		return nil, err
	}
	txnID, begun, runtime, err := spannerResolveTxn(req.GetSession(), dbName, req.GetTransaction())
	if err != nil {
		return nil, err
	}
	var target spannerQueryer = b.db
	if runtime != nil {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if runtime.lastStatements {
			return nil, status.Error(codes.FailedPrecondition, "transaction accepted its final DML statements and must be committed or rolled back")
		}
		target = runtime.tx
	}
	args := spannerBindArgs(req.GetParams(), req.GetParamTypes())
	if spannerIsDML(req.GetSql()) {
		if runtime == nil {
			return nil, status.Error(codes.FailedPrecondition, "Cloud Spanner DML requires a read-write transaction")
		}
		if runtime.readOnly {
			return nil, status.Error(codes.FailedPrecondition, "Cloud Spanner DML cannot execute in a read-only transaction")
		}
		if cached, ok := runtime.dmlResponses[req.GetSeqno()]; ok {
			return cached, nil
		}
		if _, usedByBatch := runtime.batchResponses[req.GetSeqno()]; usedByBatch {
			return nil, status.Error(codes.Aborted, "sequence number was already used by a different DML request")
		}
		if len(runtime.dmlResponses)+len(runtime.batchResponses) > 0 && req.GetSeqno() <= runtime.lastSeqno {
			return nil, status.Error(codes.Aborted, "ExecuteSql sequence number is not monotonically increasing")
		}
		rs, err := spannerRunDML(ctx, runtime.tx, req.GetSql(), args)
		if err != nil {
			return nil, err
		}
		if begun {
			rs.Metadata.Transaction = &sppb.Transaction{Id: txnID}
		}
		runtime.lastSeqno = req.GetSeqno()
		runtime.dmlResponses[req.GetSeqno()] = rs
		if req.GetLastStatement() {
			runtime.lastStatements = true
		}
		if runtime.partitionedDML {
			exact := rs.GetStats().GetRowCountExact()
			rs.Stats.RowCount = &sppb.ResultSetStats_RowCountLowerBound{RowCountLowerBound: exact}
			if err := runtime.tx.Commit(); err != nil {
				spannerDeleteTxn(txnID)
				return nil, status.Errorf(codes.Internal, "commit partitioned DML: %v", err)
			}
			spannerDeleteTxn(txnID)
		}
		return rs, nil
	}
	res, err := spannerRunQuery(ctx, b, target, req.GetSql(), args)
	if err != nil {
		return nil, err
	}
	rs := spannerShapeResultSet(res)
	if begun {
		rs.Metadata.Transaction = &sppb.Transaction{Id: txnID}
	}
	return rs, nil
}

func (s *spannerDataGRPC) ExecuteStreamingSql(req *sppb.ExecuteSqlRequest, stream sppb.Spanner_ExecuteStreamingSqlServer) error {
	rs, err := s.ExecuteSql(stream.Context(), req)
	if err != nil {
		return err
	}
	if err := stream.Send(spannerResultSetToPartial(rs)); err != nil {
		return err
	}
	return nil
}

func spannerResultSetToPartial(rs *sppb.ResultSet) *sppb.PartialResultSet {
	partial := &sppb.PartialResultSet{Metadata: rs.GetMetadata(), Stats: rs.GetStats(), Last: true}
	for _, row := range rs.GetRows() {
		partial.Values = append(partial.Values, row.GetValues()...)
	}
	return partial
}

// ---------------------------------------------------------------------------
// Read / StreamingRead
// ---------------------------------------------------------------------------

// spannerBuildReadQuery translates a Spanner Read request into a parameterized
// SQLite SELECT plus the bind args. It supports AllKeys, point-key lists, and
// lexicographic ranges over full or prefix primary keys.
func spannerBuildReadQuery(b *spannerBackend, req *sppb.ReadRequest) (string, []any, error) {
	table := req.GetTable()
	cols := req.GetColumns()
	if table == "" {
		return "", nil, status.Error(codes.InvalidArgument, "table is required")
	}
	pkCols, err := spannerPrimaryKeyColumns(b, table)
	if err != nil {
		return "", nil, err
	}
	declNames, declTypes, err := spannerTableColumns(b, table)
	if err != nil {
		return "", nil, err
	}
	typeByName := make(map[string]*sppb.Type, len(declNames))
	for i, name := range declNames {
		typeByName[name] = declTypes[i]
	}
	colList := "*"
	if len(cols) > 0 {
		quoted := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = quoteIdent(c)
		}
		colList = strings.Join(quoted, ", ")
	}
	var args []any
	var where string
	ks := req.GetKeySet()
	if ks == nil || ks.GetAll() {
		where = ""
	} else if len(ks.GetKeys()) > 0 {
		// Point keys. Each key is a ListValue of the PK columns in declared
		// order. Build (pk1=? AND pk2=?) OR (pk1=? AND pk2=?) …
		var ors []string
		for _, key := range ks.GetKeys() {
			vals := key.GetValues()
			if len(vals) < len(pkCols) {
				return "", nil, status.Errorf(codes.InvalidArgument, "key has %d values, expected %d", len(vals), len(pkCols))
			}
			var conds []string
			for pi, pk := range pkCols {
				val := vals[pi]
				args = append(args, spannerProtoToGo(val, typeByName[pk]))
				conds = append(conds, quoteIdent(pk)+" = ?")
			}
			ors = append(ors, "("+strings.Join(conds, " AND ")+")")
		}
		where = "WHERE " + strings.Join(ors, " OR ")
	} else if len(ks.GetRanges()) > 0 {
		rangeSQL, rangeArgs, err := spannerKeyRangesPredicate(ks.GetRanges(), pkCols, typeByName)
		if err != nil {
			return "", nil, err
		}
		if rangeSQL != "" {
			where = "WHERE " + rangeSQL
			args = append(args, rangeArgs...)
		}
	}
	// ORDER BY the primary key so a read returns rows in key order, matching
	// Spanner's guarantee.
	orderedPK := make([]string, len(pkCols))
	for i, pk := range pkCols {
		orderedPK[i] = quoteIdent(pk)
	}
	order := " ORDER BY " + strings.Join(orderedPK, ", ")
	q := "SELECT " + colList + " FROM " + quoteIdent(table) + " " + where + order
	if limit := req.GetLimit(); limit > 0 {
		q += " LIMIT " + strconv.FormatInt(limit, 10)
	}
	return q, args, nil
}

func spannerKeyRangesPredicate(ranges []*sppb.KeyRange, pkCols []string, typeByName map[string]*sppb.Type) (string, []any, error) {
	var (
		rangePredicates []string
		args            []any
	)
	for _, keyRange := range ranges {
		var bounds []string
		addBound := func(values []*structpb.Value, operator string) error {
			if len(values) == 0 {
				return nil
			}
			if len(values) > len(pkCols) {
				return status.Errorf(codes.InvalidArgument, "range endpoint has %d values, primary key has %d columns", len(values), len(pkCols))
			}
			columns := make([]string, len(values))
			placeholders := make([]string, len(values))
			for i, value := range values {
				columns[i] = quoteIdent(pkCols[i])
				placeholders[i] = "?"
				args = append(args, spannerProtoToGo(value, typeByName[pkCols[i]]))
			}
			if len(values) == 1 {
				bounds = append(bounds, columns[0]+" "+operator+" ?")
			} else {
				bounds = append(bounds, "("+strings.Join(columns, ", ")+") "+operator+" ("+strings.Join(placeholders, ", ")+")")
			}
			return nil
		}
		switch start := keyRange.GetStartKeyType().(type) {
		case *sppb.KeyRange_StartClosed:
			if err := addBound(start.StartClosed.GetValues(), ">="); err != nil {
				return "", nil, err
			}
		case *sppb.KeyRange_StartOpen:
			if err := addBound(start.StartOpen.GetValues(), ">"); err != nil {
				return "", nil, err
			}
		}
		switch end := keyRange.GetEndKeyType().(type) {
		case *sppb.KeyRange_EndClosed:
			if err := addBound(end.EndClosed.GetValues(), "<="); err != nil {
				return "", nil, err
			}
		case *sppb.KeyRange_EndOpen:
			if err := addBound(end.EndOpen.GetValues(), "<"); err != nil {
				return "", nil, err
			}
		}
		if len(bounds) > 0 {
			rangePredicates = append(rangePredicates, "("+strings.Join(bounds, " AND ")+")")
		}
	}
	return strings.Join(rangePredicates, " OR "), args, nil
}

func (s *spannerDataGRPC) Read(ctx context.Context, req *sppb.ReadRequest) (*sppb.ResultSet, error) {
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return nil, err
	}
	if err := spannerValidateReadPartition(req); err != nil {
		return nil, err
	}
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return nil, err
	}
	q, args, err := spannerBuildReadQuery(b, req)
	if err != nil {
		return nil, err
	}
	txnID, begun, runtime, err := spannerResolveTxn(req.GetSession(), dbName, req.GetTransaction())
	if err != nil {
		return nil, err
	}
	var target spannerQueryer = b.db
	if runtime != nil {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if runtime.lastStatements {
			return nil, status.Error(codes.FailedPrecondition, "transaction accepted its final DML statements and must be committed or rolled back")
		}
		target = runtime.tx
	}
	res, err := spannerRunQuery(ctx, b, target, q, args)
	if err != nil {
		return nil, err
	}
	// Read requests project the requested columns in order; reorder the result
	// columns to match if the query selected * (which preserves declared order).
	res = spannerReorderReadColumns(req, res)
	rs := spannerShapeResultSet(res)
	if begun {
		rs.Metadata.Transaction = &sppb.Transaction{Id: txnID}
	}
	return rs, nil
}

func (s *spannerDataGRPC) StreamingRead(req *sppb.ReadRequest, stream sppb.Spanner_StreamingReadServer) error {
	rs, err := s.Read(stream.Context(), req)
	if err != nil {
		return err
	}
	if err := stream.Send(spannerResultSetToPartial(rs)); err != nil {
		return err
	}
	return nil
}

// spannerReorderReadColumns projects a Read's result onto the requested columns
// in the requested order. The query always selects the requested columns
// already, so this is only needed when the query selected * (no explicit column
// list) to project down to the requested set.
func spannerReorderReadColumns(req *sppb.ReadRequest, res *spannerExecResult) *spannerExecResult {
	cols := req.GetColumns()
	if len(cols) == 0 || len(res.columns) == 0 {
		return res
	}
	// If the result columns already match the request order, nothing to do.
	match := len(cols) == len(res.columns)
	if match {
		for i, c := range cols {
			if res.columns[i] != c {
				match = false
				break
			}
		}
	}
	if match {
		return res
	}
	// Build a projection from the result's declared columns.
	idx := map[string]int{}
	for i, c := range res.columns {
		idx[c] = i
	}
	out := &spannerExecResult{columns: cols, types: make([]*sppb.Type, len(cols))}
	for i, c := range cols {
		if j, ok := idx[c]; ok {
			out.types[i] = res.types[j]
		} else {
			out.types[i] = &sppb.Type{Code: sppb.TypeCode_STRING}
		}
	}
	for _, row := range res.rows {
		nv := &structpb.ListValue{Values: make([]*structpb.Value, len(cols))}
		for i, c := range cols {
			if j, ok := idx[c]; ok && j < len(row.GetValues()) {
				nv.Values[i] = row.GetValues()[j]
			} else {
				nv.Values[i] = &structpb.Value{Kind: &structpb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
			}
		}
		out.rows = append(out.rows, nv)
	}
	return out
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

func (s *spannerDataGRPC) BeginTransaction(_ context.Context, req *sppb.BeginTransactionRequest) (*sppb.Transaction, error) {
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return nil, err
	}
	readOnly := req.GetOptions().GetReadOnly() != nil
	partitionedDML := req.GetOptions().GetPartitionedDml() != nil
	id, _, err := spannerBeginTxn(req.GetSession(), dbName, readOnly, partitionedDML)
	if err != nil {
		return nil, err
	}
	return &sppb.Transaction{Id: id}, nil
}

func (s *spannerDataGRPC) Commit(ctx context.Context, req *sppb.CommitRequest) (*sppb.CommitResponse, error) {
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return nil, err
	}
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return nil, err
	}
	var tx *sql.Tx
	var runtime *spannerTxnRuntime
	if txID := req.GetTransactionId(); len(txID) > 0 {
		var ok bool
		runtime, ok = spannerGetTxn(txID)
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "invalid transaction id")
		}
		if runtime.database != dbName || runtime.session != req.GetSession() {
			return nil, status.Error(codes.InvalidArgument, "transaction does not belong to this session")
		}
		if runtime.readOnly {
			return nil, status.Error(codes.FailedPrecondition, "cannot commit mutations in a read-only transaction")
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		tx = runtime.tx
	} else {
		tx, err = b.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "begin commit transaction: %v", err)
		}
	}

	for _, m := range req.GetMutations() {
		if err := spannerApplyMutation(tx, m); err != nil {
			_ = tx.Rollback()
			if len(req.GetTransactionId()) > 0 {
				spannerDeleteTxn(req.GetTransactionId())
			}
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		if len(req.GetTransactionId()) > 0 {
			spannerDeleteTxn(req.GetTransactionId())
		}
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	if len(req.GetTransactionId()) > 0 {
		spannerDeleteTxn(req.GetTransactionId())
	}
	return &sppb.CommitResponse{CommitTimestamp: timestamppb.Now()}, nil
}

func (s *spannerDataGRPC) Rollback(_ context.Context, req *sppb.RollbackRequest) (*emptypb.Empty, error) {
	txID := req.GetTransactionId()
	if len(txID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "transaction_id is required")
	}
	runtime, ok := spannerGetTxn(txID)
	if !ok {
		return &emptypb.Empty{}, nil
	}
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return nil, err
	}
	if runtime.database != dbName || runtime.session != req.GetSession() {
		return nil, status.Error(codes.InvalidArgument, "transaction does not belong to this session")
	}
	runtime.mu.Lock()
	err = runtime.tx.Rollback()
	runtime.mu.Unlock()
	spannerDeleteTxn(txID)
	if err != nil && err != sql.ErrTxDone {
		return nil, status.Errorf(codes.Internal, "rollback: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// spannerApplyMutation applies one write mutation inside a SQLite transaction.
// Insert / InsertOrUpdate / Replace / Update / Delete map onto SQLite's own
// INSERT / INSERT OR REPLACE / UPDATE / DELETE statements; the table's primary
// key scopes UPDATE and DELETE.
func spannerApplyMutation(tx *sql.Tx, m *sppb.Mutation) error {
	if m == nil {
		return nil
	}
	switch op := m.Operation.(type) {
	case *sppb.Mutation_Insert:
		return spannerExecWrite(tx, op.Insert, "INSERT")
	case *sppb.Mutation_Update:
		return spannerExecWrite(tx, op.Update, "UPDATE")
	case *sppb.Mutation_InsertOrUpdate:
		return spannerExecWrite(tx, op.InsertOrUpdate, "INSERT OR REPLACE")
	case *sppb.Mutation_Replace:
		return spannerExecWrite(tx, op.Replace, "INSERT OR REPLACE")
	case *sppb.Mutation_Delete_:
		return spannerExecDelete(tx, op.Delete)
	}
	return status.Error(codes.InvalidArgument, "mutation carried no recognized operation")
}

// spannerExecWrite applies an Insert/Update/InsertOrUpdate/Replace mutation.
// Column types are looked up from the SQLite schema so proto values are coerced
// to the right Go type on the way in (notably INT64 → int64).
func spannerExecWrite(tx *sql.Tx, w *sppb.Mutation_Write, verb string) error {
	if w == nil {
		return status.Error(codes.InvalidArgument, "write mutation is empty")
	}
	table := w.GetTable()
	cols := w.GetColumns()
	values := w.GetValues()
	if table == "" {
		return status.Error(codes.InvalidArgument, "mutation.table is required")
	}
	declCols, declTypes, err := spannerTableColumnsFromTx(tx, table)
	if err != nil {
		return err
	}
	typeByName := map[string]*sppb.Type{}
	for i, n := range declCols {
		typeByName[n] = declTypes[i]
	}
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = "?"
	}
	var stmt string
	switch verb {
	case "INSERT", "INSERT OR REPLACE":
		quoted := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = quoteIdent(c)
		}
		stmt = verb + " INTO " + quoteIdent(table) + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	case "UPDATE":
		setClauses := make([]string, len(cols))
		for i, c := range cols {
			setClauses[i] = quoteIdent(c) + " = ?"
		}
		pkCols, err := spannerPrimaryKeyColumnsFromTx(tx, table)
		if err != nil {
			return err
		}
		// UPDATE's WHERE must pin the row by primary key. The mutation supplies
		// column values in declared order; the PK columns are a subset, so build
		// the WHERE by mapping each PK back to its position in cols.
		colIndex := map[string]int{}
		for i, c := range cols {
			colIndex[c] = i
		}
		var whereConds []string
		for _, pk := range pkCols {
			pos, ok := colIndex[pk]
			if !ok {
				return status.Errorf(codes.InvalidArgument, "UPDATE mutation for %s does not specify primary key column %s", table, pk)
			}
			_ = pos
			whereConds = append(whereConds, quoteIdent(pk)+" = ?")
		}
		stmt = "UPDATE " + quoteIdent(table) + " SET " + strings.Join(setClauses, ", ") + " WHERE " + strings.Join(whereConds, " AND ")
	}

	for _, row := range values {
		vals := row.GetValues()
		if len(vals) != len(cols) {
			return status.Errorf(codes.InvalidArgument, "mutation row has %d values, expected %d", len(vals), len(cols))
		}
		args := make([]any, 0, len(vals)+len(cols))
		for i, c := range cols {
			args = append(args, spannerProtoToGo(vals[i], typeByName[c]))
		}
		if verb == "UPDATE" {
			// Append the PK values (already included in the SET positions) so
			// the WHERE placeholders bind to the same row's keys.
			colIndex := map[string]int{}
			for i, c := range cols {
				colIndex[c] = i
			}
			pkCols, _ := spannerPrimaryKeyColumnsFromTx(tx, table)
			for _, pk := range pkCols {
				pos := colIndex[pk]
				args = append(args, spannerProtoToGo(vals[pos], typeByName[pk]))
			}
		}
		if _, err := tx.Exec(stmt, args...); err != nil {
			return status.Errorf(codes.Internal, "apply %s on %s: %v", verb, table, err)
		}
	}
	return nil
}

// spannerExecDelete applies a Delete mutation scoped by its key set.
func spannerExecDelete(tx *sql.Tx, d *sppb.Mutation_Delete) error {
	if d == nil {
		return status.Error(codes.InvalidArgument, "delete mutation is empty")
	}
	table := d.GetTable()
	if table == "" {
		return status.Error(codes.InvalidArgument, "delete.table is required")
	}
	pkCols, err := spannerPrimaryKeyColumnsFromTx(tx, table)
	if err != nil {
		return err
	}
	declCols, declTypes, err := spannerTableColumnsFromTx(tx, table)
	if err != nil {
		return err
	}
	typeByName := map[string]*sppb.Type{}
	for i, n := range declCols {
		typeByName[n] = declTypes[i]
	}
	ks := d.GetKeySet()
	if ks == nil || ks.GetAll() {
		if _, err := tx.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
			return status.Errorf(codes.Internal, "delete all from %s: %v", table, err)
		}
		return nil
	}
	if len(ks.GetKeys()) > 0 {
		var ors []string
		var args []any
		for _, key := range ks.GetKeys() {
			vals := key.GetValues()
			if len(vals) < len(pkCols) {
				return status.Errorf(codes.InvalidArgument, "delete key has %d values, expected %d", len(vals), len(pkCols))
			}
			var conds []string
			for pi, pk := range pkCols {
				args = append(args, spannerProtoToGo(vals[pi], typeByName[pkCols[pi]]))
				conds = append(conds, quoteIdent(pk)+" = ?")
			}
			ors = append(ors, "("+strings.Join(conds, " AND ")+")")
		}
		stmt := "DELETE FROM " + quoteIdent(table) + " WHERE " + strings.Join(ors, " OR ")
		if _, err := tx.Exec(stmt, args...); err != nil {
			return status.Errorf(codes.Internal, "delete from %s: %v", table, err)
		}
		return nil
	}
	if len(ks.GetRanges()) > 0 {
		predicate, args, err := spannerKeyRangesPredicate(ks.GetRanges(), pkCols, typeByName)
		if err != nil {
			return err
		}
		if predicate == "" {
			return nil
		}
		stmt := "DELETE FROM " + quoteIdent(table) + " WHERE " + predicate
		if _, err := tx.Exec(stmt, args...); err != nil {
			return status.Errorf(codes.Internal, "delete range from %s: %v", table, err)
		}
		return nil
	}
	return nil
}

// spannerTableColumnsFromTx is the *sql.Tx variant of spannerTableColumns, so
// mutations running inside a Commit transaction see a consistent view.
func spannerTableColumnsFromTx(tx *sql.Tx, table string) ([]string, []*sppb.Type, error) {
	rows, err := tx.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, nil, status.Errorf(codes.NotFound, "table %q not found: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	var types []*sppb.Type
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, nil, status.Errorf(codes.Internal, "scan table_info: %v", err)
		}
		names = append(names, name)
		types = append(types, spannerTypeForSQLite(ctype))
	}
	if len(names) == 0 {
		return nil, nil, status.Errorf(codes.NotFound, "table not found: %s", table)
	}
	return names, types, nil
}

func spannerPrimaryKeyColumnsFromTx(tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read table_info for %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	pkOrder := map[string]int{}
	maxPK := 0
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, status.Errorf(codes.Internal, "scan table_info: %v", err)
		}
		if pk > 0 {
			pkOrder[name] = pk
			if pk > maxPK {
				maxPK = pk
			}
		}
	}
	var pkCols []string
	for i := 1; i <= maxPK; i++ {
		for name, idx := range pkOrder {
			if idx == i {
				pkCols = append(pkCols, name)
			}
		}
	}
	if len(pkCols) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "table %s has no primary key", table)
	}
	return pkCols, nil
}

// ---------------------------------------------------------------------------
// Batch DML and batch mutations
// ---------------------------------------------------------------------------

func (s *spannerDataGRPC) ExecuteBatchDml(ctx context.Context, req *sppb.ExecuteBatchDmlRequest) (*sppb.ExecuteBatchDmlResponse, error) {
	if len(req.GetStatements()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one DML statement is required")
	}
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return nil, err
	}
	txnID, begun, runtime, err := spannerResolveTxn(req.GetSession(), dbName, req.GetTransaction())
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, status.Error(codes.FailedPrecondition, "ExecuteBatchDml requires a read-write transaction")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.readOnly {
		return nil, status.Error(codes.FailedPrecondition, "ExecuteBatchDml cannot execute in a read-only transaction")
	}
	if runtime.partitionedDML {
		return nil, status.Error(codes.FailedPrecondition, "ExecuteBatchDml cannot execute in a partitioned DML transaction")
	}
	if runtime.lastStatements {
		return nil, status.Error(codes.FailedPrecondition, "transaction accepted its final DML statements and must be committed or rolled back")
	}
	if cached, ok := runtime.batchResponses[req.GetSeqno()]; ok {
		return cached, nil
	}
	if _, usedByDML := runtime.dmlResponses[req.GetSeqno()]; usedByDML {
		return nil, status.Error(codes.Aborted, "sequence number was already used by a different DML request")
	}
	if len(runtime.dmlResponses)+len(runtime.batchResponses) > 0 && req.GetSeqno() <= runtime.lastSeqno {
		return nil, status.Error(codes.Aborted, "ExecuteBatchDml sequence number is not monotonically increasing")
	}

	response := &sppb.ExecuteBatchDmlResponse{
		Status: &statuspb.Status{Code: int32(codes.OK)},
	}
	for i, statement := range req.GetStatements() {
		rs, execErr := spannerRunDML(ctx, runtime.tx, statement.GetSql(), spannerBindArgs(statement.GetParams(), statement.GetParamTypes()))
		if execErr != nil {
			response.Status = status.Convert(execErr).Proto()
			break
		}
		if i == 0 && begun {
			rs.Metadata.Transaction = &sppb.Transaction{Id: txnID}
		}
		response.ResultSets = append(response.ResultSets, rs)
	}
	runtime.lastSeqno = req.GetSeqno()
	runtime.batchResponses[req.GetSeqno()] = response
	if req.GetLastStatements() && response.Status.GetCode() == int32(codes.OK) {
		runtime.lastStatements = true
	}
	return response, nil
}

func spannerApplyMutationGroup(ctx context.Context, b *spannerBackend, index int, group *sppb.BatchWriteRequest_MutationGroup) *sppb.BatchWriteResponse {
	response := &sppb.BatchWriteResponse{Indexes: []int32{int32(index)}}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		response.Status = status.Convert(status.Errorf(codes.Internal, "begin mutation group: %v", err)).Proto()
		return response
	}
	for _, mutation := range group.GetMutations() {
		if err := spannerApplyMutation(tx, mutation); err != nil {
			_ = tx.Rollback()
			response.Status = status.Convert(err).Proto()
			return response
		}
	}
	if err := tx.Commit(); err != nil {
		response.Status = status.Convert(status.Errorf(codes.Internal, "commit mutation group: %v", err)).Proto()
		return response
	}
	response.Status = &statuspb.Status{Code: int32(codes.OK)}
	response.CommitTimestamp = timestamppb.Now()
	return response
}

func (s *spannerDataGRPC) BatchWrite(req *sppb.BatchWriteRequest, stream sppb.Spanner_BatchWriteServer) error {
	dbName, err := spannerSessionDatabase(req.GetSession())
	if err != nil {
		return err
	}
	b, err := spannerBackendFor(dbName)
	if err != nil {
		return err
	}
	if len(req.GetMutationGroups()) == 0 {
		return status.Error(codes.InvalidArgument, "at least one mutation group is required")
	}
	for i, group := range req.GetMutationGroups() {
		if err := stream.Send(spannerApplyMutationGroup(stream.Context(), b, i, group)); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Partitioning
// ---------------------------------------------------------------------------

// The backing database lives in one SQLite engine, so a partition request has
// exactly one real partition. Its opaque token remains bound to the session,
// read-only transaction, and original query/read fields, matching Cloud
// Spanner's rule that an execution request must exactly match the request that
// created the token.
func (s *spannerDataGRPC) PartitionQuery(_ context.Context, req *sppb.PartitionQueryRequest) (*sppb.PartitionResponse, error) {
	if strings.TrimSpace(req.GetSql()) == "" {
		return nil, status.Error(codes.InvalidArgument, "sql is required")
	}
	if spannerIsDML(req.GetSql()) {
		return nil, status.Error(codes.InvalidArgument, "DML statements cannot be partitioned as queries")
	}
	return spannerCreatePartition(req.GetSession(), req.GetTransaction(), req, nil)
}

func (s *spannerDataGRPC) PartitionRead(_ context.Context, req *sppb.PartitionReadRequest) (*sppb.PartitionResponse, error) {
	if req.GetTable() == "" || len(req.GetColumns()) == 0 || req.GetKeySet() == nil {
		return nil, status.Error(codes.InvalidArgument, "table, columns, and key_set are required")
	}
	return spannerCreatePartition(req.GetSession(), req.GetTransaction(), nil, req)
}

func spannerCreatePartition(session string, selector *sppb.TransactionSelector, query *sppb.PartitionQueryRequest, read *sppb.PartitionReadRequest) (*sppb.PartitionResponse, error) {
	dbName, err := spannerSessionDatabase(session)
	if err != nil {
		return nil, err
	}
	transactionID, begun, runtime, err := spannerResolveTxn(session, dbName, selector)
	if err != nil {
		return nil, err
	}
	if runtime == nil || !runtime.readOnly || runtime.partitionedDML {
		return nil, status.Error(codes.FailedPrecondition, "partitioning requires a read-only transaction")
	}
	token := generateUUID()
	partition := spannerPartitionRuntime{
		session:       session,
		transactionID: append([]byte(nil), transactionID...),
	}
	if query != nil {
		cloned, ok := proto.Clone(query).(*sppb.PartitionQueryRequest)
		if !ok {
			return nil, status.Error(codes.Internal, "failed to clone partition query")
		}
		partition.query = cloned
	}
	if read != nil {
		cloned, ok := proto.Clone(read).(*sppb.PartitionReadRequest)
		if !ok {
			return nil, status.Error(codes.Internal, "failed to clone partition read")
		}
		partition.read = cloned
	}
	spannerPartitionsMu.Lock()
	spannerPartitions[token] = partition
	spannerPartitionsMu.Unlock()
	response := &sppb.PartitionResponse{
		Partitions: []*sppb.Partition{{PartitionToken: []byte(token)}},
	}
	if begun {
		response.Transaction = &sppb.Transaction{Id: transactionID}
	}
	return response, nil
}

func spannerPartitionFor(token []byte) (spannerPartitionRuntime, error) {
	if len(token) == 0 {
		return spannerPartitionRuntime{}, nil
	}
	spannerPartitionsMu.RLock()
	partition, ok := spannerPartitions[string(token)]
	spannerPartitionsMu.RUnlock()
	if !ok {
		return spannerPartitionRuntime{}, status.Error(codes.InvalidArgument, "invalid partition token")
	}
	return partition, nil
}

func spannerValidateQueryPartition(req *sppb.ExecuteSqlRequest) error {
	partition, err := spannerPartitionFor(req.GetPartitionToken())
	if err != nil || len(req.GetPartitionToken()) == 0 {
		return err
	}
	if partition.query == nil || partition.session != req.GetSession() {
		return status.Error(codes.InvalidArgument, "partition token does not belong to this query session")
	}
	if !proto.Equal(partition.query.GetParams(), req.GetParams()) ||
		partition.query.GetSql() != req.GetSql() ||
		!spannerTypeMapsEqual(partition.query.GetParamTypes(), req.GetParamTypes()) ||
		!spannerSelectorUsesID(req.GetTransaction(), partition.transactionID) {
		return status.Error(codes.InvalidArgument, "query fields do not match the partition request")
	}
	return nil
}

func spannerValidateReadPartition(req *sppb.ReadRequest) error {
	partition, err := spannerPartitionFor(req.GetPartitionToken())
	if err != nil || len(req.GetPartitionToken()) == 0 {
		return err
	}
	if partition.read == nil || partition.session != req.GetSession() {
		return status.Error(codes.InvalidArgument, "partition token does not belong to this read session")
	}
	want := partition.read
	if want.GetTable() != req.GetTable() ||
		want.GetIndex() != req.GetIndex() ||
		!slicesEqual(want.GetColumns(), req.GetColumns()) ||
		!proto.Equal(want.GetKeySet(), req.GetKeySet()) ||
		!spannerSelectorUsesID(req.GetTransaction(), partition.transactionID) {
		return status.Error(codes.InvalidArgument, "read fields do not match the partition request")
	}
	if req.GetLimit() != 0 {
		return status.Error(codes.InvalidArgument, "limit cannot be specified with a partition token")
	}
	return nil
}

func spannerSelectorUsesID(selector *sppb.TransactionSelector, id []byte) bool {
	return selector != nil && string(selector.GetId()) == string(id)
}

func spannerTypeMapsEqual(a, b map[string]*sppb.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for name, typ := range a {
		if !proto.Equal(typ, b[name]) {
			return false
		}
	}
	return true
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
