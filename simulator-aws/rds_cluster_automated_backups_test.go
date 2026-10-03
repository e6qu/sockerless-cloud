package main

import (
	"encoding/binary"
	"testing"
	"time"
)

// rdsTestQueryEvent encodes a query event for query in database, ending in a
// checksum.
func rdsTestQueryEvent(database, query string) []byte {
	body := make([]byte, rdsBinlogQueryPostHeaderLength)
	body[8] = byte(len(database))
	body = append(body, database...)
	body = append(body, 0)
	body = append(body, query...)
	body = append(body, 0, 0, 0, 0)
	event := make([]byte, rdsBinlogEventHeaderLength, rdsBinlogEventHeaderLength+len(body))
	event[4] = rdsBinlogQueryEvent
	binary.LittleEndian.PutUint32(event[9:], uint32(rdsBinlogEventHeaderLength+len(body)))
	return append(event, body...)
}

// A captured binary log holds the data up to the first transaction it does
// not hold whole: a transaction its XID or a completing statement ends is in
// the capture, and one cut off after BEGIN or mid-event is not.
func TestRDSBinlogConsistentStartEndsAtTheFirstPartialTransaction(t *testing.T) {
	const formatDescription, tableMap, writeRows = 15, 19, 30
	at := time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)
	log := []byte(rdsBinlogMagic)
	log = append(log, rdsTestBinlogEvent(formatDescription, time.Time{})...)
	log = append(log, rdsTestBinlogEvent(rdsBinlogAnonymousGTID, at)...)
	log = append(log, rdsTestQueryEvent("shop", "CREATE TABLE t (id INT PRIMARY KEY)")...)
	log = append(log, rdsTestBinlogEvent(rdsBinlogAnonymousGTID, at)...)
	log = append(log, rdsTestQueryEvent("shop", "BEGIN")...)
	log = append(log, rdsTestBinlogEvent(tableMap, time.Time{})...)
	log = append(log, rdsTestBinlogEvent(writeRows, time.Time{})...)
	log = append(log, rdsTestBinlogEvent(rdsBinlogXIDEvent, time.Time{})...)
	whole := len(log)

	if start, err := rdsBinlogConsistentStart(log); err != nil || start != whole {
		t.Fatalf("start = %d, %v; want the end %d", start, err, whole)
	}
	partial := append(append([]byte(nil), log...), rdsTestBinlogEvent(rdsBinlogAnonymousGTID, at)...)
	partial = append(partial, rdsTestQueryEvent("shop", "BEGIN")...)
	partial = append(partial, rdsTestBinlogEvent(tableMap, time.Time{})...)
	if start, err := rdsBinlogConsistentStart(partial); err != nil || start != whole {
		t.Fatalf("start = %d, %v; want the open transaction's GTID event at %d", start, err, whole)
	}
	torn := append(append([]byte(nil), log...), rdsTestBinlogEvent(rdsBinlogAnonymousGTID, at)[:12]...)
	if start, err := rdsBinlogConsistentStart(torn); err != nil || start != whole {
		t.Fatalf("start = %d, %v; want the torn event's offset %d", start, err, whole)
	}
	if _, err := rdsBinlogConsistentStart([]byte("not a binary log")); err == nil {
		t.Fatal("a file without the magic number was read")
	}
}

func TestRDSNextBackupTimeIsTheNextWindowStart(t *testing.T) {
	now := time.Date(2026, 10, 3, 7, 30, 0, 0, time.UTC)
	for window, want := range map[string]time.Time{
		"08:15-08:45": time.Date(2026, 10, 3, 8, 15, 0, 0, time.UTC),
		"07:30-08:00": time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC),
		"03:00-03:30": time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC),
	} {
		if got, err := rdsNextBackupTime(window, now); err != nil || !got.Equal(want) {
			t.Errorf("window %s starts next at %v (%v), want %v", window, got, err, want)
		}
	}
	for _, window := range []string{"", "7am-9am", "25:00-25:30", "07:61-08:00"} {
		if _, err := rdsNextBackupTime(window, now); err == nil {
			t.Errorf("window %q was accepted", window)
		}
	}
}

// The base backups a restore needs are the newest one taken by the start of
// the retention period and every later one; the restorable window starts at
// the later of the oldest base backup and the start of the retention period.
func TestRDSBaseBackupsCoverTheRetentionPeriod(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	day := func(n int) RDSClusterBaseBackup {
		return RDSClusterBaseBackup{SnapshotID: "rds:c-" + string(rune('a'+n)), Time: now.AddDate(0, 0, -n).Format(rdsRestorableTimeLayout)}
	}
	bases := []RDSClusterBaseBackup{day(5), day(4), day(3), day(2), day(1)}
	kept := rdsBaseBackupsToKeep(bases, now.AddDate(0, 0, -3).Add(time.Hour))
	if len(kept) != 3 || kept[0] != day(3) {
		t.Fatalf("kept %v, want the backups from three days ago on", kept)
	}
	if kept := rdsBaseBackupsToKeep(bases[3:], now.AddDate(0, 0, -7)); len(kept) != 2 {
		t.Fatalf("kept %v, want every backup when none is older than the period", kept)
	}

	cluster := RDSCluster{BackupRetentionPeriod: 3, BaseBackups: kept}
	earliest, latest, ok := rdsRestorableWindow(cluster)
	cutoff := now.AddDate(0, 0, -3)
	if !ok || earliest.Before(cutoff) || earliest.After(cutoff.Add(time.Minute)) || latest.Before(now) {
		t.Fatalf("window %v to %v (%v), want from the start of the retention period %v", earliest, latest, ok, cutoff)
	}
	if base, ok := rdsBaseBackupFor(cluster, now.AddDate(0, 0, -2).Add(-time.Hour)); !ok || base != day(3) {
		t.Fatalf("restore base %v (%v), want the backup of three days ago", base, ok)
	}
	if base, ok := rdsBaseBackupFor(cluster, now); !ok || base != day(1) {
		t.Fatalf("restore base %v (%v), want the newest backup", base, ok)
	}
	if _, _, ok := rdsRestorableWindow(RDSCluster{BackupRetentionPeriod: 1}); ok {
		t.Fatal("a cluster without a base backup reports a restorable window")
	}
}

func TestRDSAutomatedSnapshotVolumeNamesHoldNoColon(t *testing.T) {
	id := rdsAutomatedSnapshotID("orders", time.Date(2026, 10, 3, 7, 5, 0, 0, time.UTC))
	if id != "rds:orders-2026-10-03-07-05" {
		t.Fatalf("automated snapshot %q, want rds:orders-2026-10-03-07-05", id)
	}
	if volume := rdsClusterSnapshotVolume(id); volume != "sockerless-rds-cluster-snapshot_rds.orders-2026-10-03-07-05" {
		t.Fatalf("volume %q", volume)
	}
}
