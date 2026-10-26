package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// Restoring a Cloud Bigtable backup or snapshot yields the rows the source held
// when the copy was taken, so the capture store keeps the source table's schema
// and rows under the backup's or snapshot's resource name. Both doors use these
// helpers, so a backup taken over REST restores over gRPC and the reverse.
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

func btDeleteCapture(captureName string) {
	bigtableTableCaptures.Delete(captureName)
}

// btTableSizeBytes is the size of the data a table holds: every cell's row key,
// family, qualifier, eight-byte timestamp and value.
func btTableSizeBytes(tableName string) int64 {
	td := bigtableTableData(tableName)
	td.mu.Lock()
	defer td.mu.Unlock()
	var size int64
	for rowKey, families := range td.rows {
		for family, columns := range families {
			for qualifier, cells := range columns {
				for _, cell := range cells {
					size += int64(len(rowKey) + len(family) + len(qualifier) + 8 + len(cell.value))
				}
			}
		}
	}
	return size
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
