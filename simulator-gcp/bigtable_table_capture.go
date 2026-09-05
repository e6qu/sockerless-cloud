package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// A Cloud Bigtable backup and a Cloud Bigtable snapshot are both copies of a
// table, and restoring either yields a table holding the rows the source held
// when the copy was taken. Recording only the Backup or Snapshot resource's
// metadata would make RestoreTable and CreateTableFromSnapshot answer with an
// empty table: a restore that reports success and silently drops every row.
//
// The capture store below holds what those resources capture — the source
// table's schema and its rows at capture time, keyed by the backup's or
// snapshot's own resource name — and it is the only thing a restore reads.
// Both doors onto the backup family write and read it through the helpers
// here, so a backup taken over REST restores over gRPC and the reverse.
type btBackupPayload struct {
	Table bigtableTable     `json:"table"`
	Rows  btStoredTableData `json:"rows"`
}

var bigtableTableCaptures sim.Store[btBackupPayload]

// btCaptureTable copies the source table's schema and rows into the capture
// named for the backup or snapshot holding them, reporting whether the source
// table exists.
func btCaptureTable(captureName, sourceTable string) bool {
	table, ok := bigtableTables.Get(sourceTable)
	if !ok {
		return false
	}
	td := bigtableTableData(sourceTable)
	td.mu.Lock()
	rows := btStoredTableData{Rows: btRowsToStored(td.rows)}
	td.mu.Unlock()
	bigtableTableCaptures.Put(captureName, btBackupPayload{Table: table, Rows: rows})
	return true
}

// btCopyCapture gives a copied backup its own capture of the source's
// contents, so deleting the source leaves the copy restorable.
func btCopyCapture(destination, source string) {
	if payload, ok := bigtableTableCaptures.Get(source); ok {
		bigtableTableCaptures.Put(destination, payload)
	}
}

// btDeleteCapture drops a deleted backup's or snapshot's captured contents.
func btDeleteCapture(captureName string) {
	bigtableTableCaptures.Delete(captureName)
}

// btCaptureRowCount reports how many rows a capture holds, which is what a
// Backup's or Snapshot's size fields describe.
func btCaptureRowCount(captureName string) int {
	payload, ok := bigtableTableCaptures.Get(captureName)
	if !ok {
		return 0
	}
	return len(payload.Rows.Rows)
}

// btRestoreCapture writes a capture's rows into the named table and returns
// the table it restored. The table carries the source's column families and
// granularity; its cluster states belong to the instance it was restored into,
// not to the one the copy was taken from.
func btRestoreCapture(captureName, tableName string) (bigtableTable, bool) {
	payload, ok := bigtableTableCaptures.Get(captureName)
	if !ok {
		return bigtableTable{}, false
	}
	families := payload.Table.ColumnFamilies
	if families == nil {
		families = map[string]map[string]any{}
	}
	granularity := payload.Table.Granularity
	if granularity == "" {
		granularity = "MILLIS"
	}
	table := bigtableTable{
		Name:           tableName,
		Granularity:    granularity,
		ColumnFamilies: families,
		ClusterStates:  map[string]map[string]any{},
	}
	bigtableTables.Put(tableName, table)
	td := bigtableTableData(tableName)
	td.mu.Lock()
	td.rows = btRowsFromStored(payload.Rows)
	btPersistTableData(tableName, td)
	td.mu.Unlock()
	return table, true
}
