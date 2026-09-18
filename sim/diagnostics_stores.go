package sim

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
)

// What is in the database, and how much of it. A deployed simulator's database
// reached 2.4 GB and the question "of what?" had no answer from the outside:
// the file lives inside a Firecracker guest with no shell, so it was settled by
// dumping the first hundred bytes of the file off the guest's ext4 image and
// decoding the SQLite header by hand. The page count and the free list are in
// that header, but the per-table split -- the fact that Amazon S3 object bodies
// are blobs in this database and account for most of it -- is not.
//
// /debug/stores answers it in one request. It reports each table's size from
// SQLite's own dbstat virtual table, so it walks b-tree pages rather than
// reading every blob, and it names the write-ahead log's size beside the
// database's, because a WAL at its high-water mark is the other half of the
// disk a simulator holds.

// diagnosticsDB is one open database the diagnostics listener can report on.
type diagnosticsDB struct {
	path string
	db   *sql.DB
}

var diagnosticsDBs struct {
	mu  sync.Mutex
	all []diagnosticsDB
}

// registerDiagnosticsDB records a database for /debug/stores. A path that is
// already registered is replaced, so the test suites' many opens of the same
// file do not accumulate.
func registerDiagnosticsDB(path string, db *sql.DB) {
	diagnosticsDBs.mu.Lock()
	defer diagnosticsDBs.mu.Unlock()
	for i, entry := range diagnosticsDBs.all {
		if entry.path == path {
			diagnosticsDBs.all[i].db = db
			return
		}
	}
	diagnosticsDBs.all = append(diagnosticsDBs.all, diagnosticsDB{path: path, db: db})
}

// unregisterDiagnosticsDB drops a database as it closes, so the endpoint never
// queries a handle whose connections are gone.
func unregisterDiagnosticsDB(db *sql.DB) {
	diagnosticsDBs.mu.Lock()
	defer diagnosticsDBs.mu.Unlock()
	kept := diagnosticsDBs.all[:0]
	for _, entry := range diagnosticsDBs.all {
		if entry.db != db {
			kept = append(kept, entry)
		}
	}
	diagnosticsDBs.all = kept
}

// registeredDiagnosticsDBs is a snapshot, so a report does not hold the lock
// while it queries.
func registeredDiagnosticsDBs() []diagnosticsDB {
	diagnosticsDBs.mu.Lock()
	defer diagnosticsDBs.mu.Unlock()
	return append([]diagnosticsDB(nil), diagnosticsDBs.all...)
}

// storeTableSize is one table's share of the database.
type storeTableSize struct {
	Name  string
	Bytes int64
	Rows  int64 // -1 when not counted
}

// writeStoreReport writes the per-database report. countRows makes it count
// every table's rows, which is a full scan of each table's b-tree and is left
// to the caller to ask for.
func writeStoreReport(w io.Writer, countRows bool) {
	dbs := registeredDiagnosticsDBs()
	if len(dbs) == 0 {
		fmt.Fprintln(w, "no database is open in this process")
		return
	}
	for _, entry := range dbs {
		fmt.Fprintf(w, "%s\n", entry.path)
		pageSize, pageCount, freeCount, err := storeDatabasePages(entry.db)
		if err != nil {
			fmt.Fprintf(w, "  cannot read the database's page counts: %v\n", err)
			continue
		}
		fmt.Fprintf(w, "  database   %10s  (%d pages of %d bytes)\n",
			humanBytes(pageSize*pageCount), pageCount, pageSize)
		fmt.Fprintf(w, "  free list  %10s  (%d pages, reusable but not returned to the filesystem)\n",
			humanBytes(pageSize*freeCount), freeCount)
		if size, err := os.Stat(entry.path + "-wal"); err == nil {
			fmt.Fprintf(w, "  write-ahead log %5s\n", humanBytes(size.Size()))
		}

		tables, err := storeTableSizes(entry.db, countRows)
		if err != nil {
			fmt.Fprintf(w, "  cannot read dbstat: %v\n", err)
			continue
		}
		for _, table := range tables {
			if table.Rows >= 0 {
				fmt.Fprintf(w, "  %-40s %10s  %d rows\n", table.Name, humanBytes(table.Bytes), table.Rows)
				continue
			}
			fmt.Fprintf(w, "  %-40s %10s\n", table.Name, humanBytes(table.Bytes))
		}
	}
}

// storeDatabasePages reads the page geometry the SQLite header carries.
func storeDatabasePages(db *sql.DB) (pageSize, pageCount, freeCount int64, err error) {
	for _, probe := range []struct {
		pragma string
		into   *int64
	}{
		{"PRAGMA page_size", &pageSize},
		{"PRAGMA page_count", &pageCount},
		{"PRAGMA freelist_count", &freeCount},
	} {
		if err := db.QueryRow(probe.pragma).Scan(probe.into); err != nil {
			return 0, 0, 0, fmt.Errorf("%s: %w", probe.pragma, err)
		}
	}
	return pageSize, pageCount, freeCount, nil
}

// storeTableSizes reports every table and index by the bytes its pages occupy,
// largest first. dbstat is SQLite's own accounting, so the numbers add up to
// the database's page count rather than to an estimate of its contents.
func storeTableSizes(db *sql.DB, countRows bool) ([]storeTableSize, error) {
	rows, err := db.Query(`SELECT name, SUM(pgsize) FROM dbstat GROUP BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var sizes []storeTableSize
	for rows.Next() {
		var entry storeTableSize
		entry.Rows = -1
		if err := rows.Scan(&entry.Name, &entry.Bytes); err != nil {
			return nil, err
		}
		sizes = append(sizes, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(sizes, func(i, j int) bool {
		if sizes[i].Bytes != sizes[j].Bytes {
			return sizes[i].Bytes > sizes[j].Bytes
		}
		return sizes[i].Name < sizes[j].Name
	})
	if !countRows {
		return sizes, nil
	}
	for i, entry := range sizes {
		// An index has no rows of its own to count, and sqlite_schema is not a
		// store.
		if len(entry.Name) >= 7 && entry.Name[:7] == "sqlite_" {
			continue
		}
		var count int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + entry.Name + `"`).Scan(&count); err != nil {
			continue
		}
		sizes[i].Rows = count
	}
	return sizes, nil
}

// humanBytes renders a size the way an operator reads one.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, units := float64(n), []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f %s", value, units[len(units)-1])
}

// storeDiagnosticsHandler serves /debug/stores. `?rows=1` adds each table's row
// count, which costs a scan per table.
func storeDiagnosticsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writeStoreReport(w, r.URL.Query().Get("rows") != "")
}
