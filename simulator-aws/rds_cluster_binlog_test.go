package main

import (
	"encoding/binary"
	"testing"
	"time"
)

// rdsTestBinlogEvent encodes one binary log event of eventType; a GTID event
// carries committed as its immediate commit timestamp.
func rdsTestBinlogEvent(eventType byte, committed time.Time) []byte {
	body := make([]byte, rdsBinlogGTIDPostHeaderLength+rdsBinlogCommitTimestampBytes+4)
	if eventType == rdsBinlogAnonymousGTID {
		var encoded [8]byte
		binary.LittleEndian.PutUint64(encoded[:], uint64(committed.UnixMicro())|1<<55)
		copy(body[rdsBinlogGTIDPostHeaderLength:], encoded[:rdsBinlogCommitTimestampBytes])
	}
	event := make([]byte, rdsBinlogEventHeaderLength, rdsBinlogEventHeaderLength+len(body))
	event[4] = eventType
	binary.LittleEndian.PutUint32(event[9:], uint32(rdsBinlogEventHeaderLength+len(body)))
	return append(event, body...)
}

// The replay stops before the first transaction committed after the restore
// time, and runs to the end of the last complete event when none did.
func TestRDSBinaryLogReplayRangeStopsBeforeTheFirstLaterCommit(t *testing.T) {
	restoreTo := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	const formatDescription, xid = 15, 16
	first := []byte(rdsBinlogMagic)
	first = append(first, rdsTestBinlogEvent(formatDescription, time.Time{})...)
	first = append(first, rdsTestBinlogEvent(rdsBinlogAnonymousGTID, restoreTo.Add(-time.Second))...)
	first = append(first, rdsTestBinlogEvent(xid, time.Time{})...)
	second := []byte(rdsBinlogMagic)
	second = append(second, rdsTestBinlogEvent(formatDescription, time.Time{})...)
	cut := len(second)
	second = append(second, rdsTestBinlogEvent(rdsBinlogAnonymousGTID, restoreTo.Add(time.Millisecond))...)
	second = append(second, rdsTestBinlogEvent(xid, time.Time{})...)
	files := []rdsBinlogFile{{name: "binlog.000003", data: first}, {name: "binlog.000004", data: second}}

	names, size, err := rdsBinaryLogReplayRange(files, len(rdsBinlogMagic), restoreTo)
	if err != nil || len(names) != 2 || size != cut {
		t.Fatalf("replay range = %v up to %d (%v), want both files up to %d", names, size, err, cut)
	}
	names, size, err = rdsBinaryLogReplayRange(files, len(rdsBinlogMagic), restoreTo.Add(time.Second))
	if err != nil || len(names) != 2 || size != len(second) {
		t.Fatalf("replay range = %v up to %d (%v), want both files whole", names, size, err)
	}
	torn := append(append([]byte(nil), first...), rdsTestBinlogEvent(xid, time.Time{})[:10]...)
	names, size, err = rdsBinaryLogReplayRange([]rdsBinlogFile{{name: "binlog.000003", data: torn}}, len(rdsBinlogMagic), restoreTo)
	if err != nil || len(names) != 1 || size != len(first) {
		t.Fatalf("replay range = %v up to %d (%v), want the file up to its last complete event %d", names, size, err, len(first))
	}
}

// Archive recovery targets the restore time only when a transaction ended
// after it; pg_waldump's report of the log's end is not a failure.
func TestRDSWALListingFindsTransactionsAfterTheRestoreTime(t *testing.T) {
	restoreTo := time.Date(2026, 10, 2, 12, 0, 0, 500_000_000, time.UTC)
	listing := []string{
		"rmgr: Transaction len (rec/tot):     66/    66, tx:        730, lsn: 0/014F70B8, prev 0/014F7018, desc: COMMIT 2026-10-02 12:00:00.4 UTC; inval msgs: snapshot 2396",
		"pg_waldump: error: error in WAL record at 0/201FA80: invalid record length at 0/201FEA8: expected at least 24, got 0",
	}
	if later, err := rdsWALHasTransactionAfter(listing, restoreTo); err != nil || later {
		t.Fatalf("later = %v (%v), want no transaction after the restore time", later, err)
	}
	listing = append(listing, "rmgr: Transaction len (rec/tot):     66/    66, tx:        731, lsn: 0/01925D18, prev 0/01925CC8, desc: ABORT 2026-10-02 12:00:00.600001 UTC")
	if later, err := rdsWALHasTransactionAfter(listing, restoreTo); err != nil || !later {
		t.Fatalf("later = %v (%v), want the abort after the restore time", later, err)
	}
	if _, err := rdsWALHasTransactionAfter([]string{"pg_waldump: error: could not find file \"000000010000000000000002\""}, restoreTo); err == nil {
		t.Fatal("a missing segment must fail the restore")
	}
}
