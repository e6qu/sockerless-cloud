package streamlog

import (
	"math/big"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

var t0 = time.UnixMilli(1_700_000_000_000)

func newLog(t *testing.T) (*Log[string], sim.Store[Record[string]]) {
	t.Helper()
	records := sim.MakeStore[Record[string]](nil, "test_streamlog_records")
	return New(records, sim.MakeStore[Head](nil, "test_streamlog_heads")), records
}

func values(recs []Record[string]) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Value)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestAppendAssignsDenseSequencesPerPartition(t *testing.T) {
	l, records := newLog(t)
	got := l.Append("p", t0, "a", "b")
	if got[0].Seq != 0 || got[1].Seq != 1 {
		t.Fatalf("sequences = %+v", got)
	}
	if other := l.Append("q", t0, "x"); other[0].Seq != 0 {
		t.Fatalf("partition q starts at %d", other[0].Seq)
	}
	l.Append("p", t0, "c")
	if h := l.Head("p"); h.First != 0 || h.Next != 3 {
		t.Fatalf("head = %+v", h)
	}
	if records.Len() != 4 {
		t.Fatalf("stored %d rows, want one per record", records.Len())
	}
	eq(t, values(l.Read("p", 1, 10)), []string{"b", "c"})
	eq(t, values(l.Read("p", 0, 2)), []string{"a", "b"})
	eq(t, values(l.Read("p", 3, 10)), nil)
	if last, ok := l.Last("p"); !ok || last.Value != "c" {
		t.Fatalf("last = %+v %v", last, ok)
	}
}

func TestTrimAgesOutOldRecordsWithoutRenumbering(t *testing.T) {
	l, records := newLog(t)
	l.Append("p", t0, "old1", "old2")
	l.Append("p", t0.Add(time.Hour), "new")
	if n := l.Trim("p", t0.Add(30*time.Minute)); n != 2 {
		t.Fatalf("trimmed %d", n)
	}
	if h := l.Head("p"); h.First != 2 || h.Next != 3 {
		t.Fatalf("head after trim = %+v", h)
	}
	if records.Len() != 1 {
		t.Fatalf("%d rows remain", records.Len())
	}
	eq(t, values(l.Read("p", 0, 10)), []string{"new"})
	if got := l.Append("p", t0.Add(2*time.Hour), "next"); got[0].Seq != 3 {
		t.Fatalf("append after trim took %d", got[0].Seq)
	}
	l.Trim("p", t0.Add(3*time.Hour))
	if _, ok := l.Last("p"); ok {
		t.Fatal("a fully trimmed partition has a last record")
	}
	if h := l.Head("p"); h.First != 4 || h.Next != 4 {
		t.Fatalf("head after trimming everything = %+v", h)
	}
}

func TestSeekTime(t *testing.T) {
	l, _ := newLog(t)
	for i := range 5 {
		l.Append("p", t0.Add(time.Duration(i)*time.Minute), "r")
	}
	for at, want := range map[time.Duration]int64{-time.Minute: 0, 0: 0, 90 * time.Second: 2, 4 * time.Minute: 4, time.Hour: 5} {
		if got := l.SeekTime("p", t0.Add(at)); got != want {
			t.Errorf("SeekTime(+%v) = %d, want %d", at, got, want)
		}
	}
}

func TestDrop(t *testing.T) {
	l, records := newLog(t)
	l.Append("p", t0, "a", "b")
	l.Append("q", t0, "x")
	l.Drop("p")
	if records.Len() != 1 || l.Head("p") != (Head{}) {
		t.Fatalf("rows %d head %+v", records.Len(), l.Head("p"))
	}
}

func TestPartitioners(t *testing.T) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	half := new(big.Int).Rsh(max, 1)
	ranges := HashRanges{
		{ID: "low", Start: big.NewInt(0), End: half},
		{ID: "high", Start: new(big.Int).Add(half, big.NewInt(1)), End: max},
	}
	var p Partitioner = ranges
	for _, key := range []string{"a", "b", "c", "d"} {
		want := "low"
		if KeyHash(key).Cmp(half) > 0 {
			want = "high"
		}
		if got := p.Partition(key); got != want {
			t.Errorf("Partition(%q) = %s, want %s", key, got, want)
		}
	}
	if got := ranges.PartitionHash(max); got != "high" {
		t.Errorf("PartitionHash(max) = %s", got)
	}
	m := Modulo{"0", "1", "2"}
	seen := map[string]bool{}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		placed := m.Partition(key)
		if placed != (Modulo{"0", "1", "2"}).Partition(key) {
			t.Errorf("Modulo placed %q differently on a second call", key)
		}
		seen[placed] = true
	}
	if len(seen) < 2 || Modulo(nil).Partition("k") != "" {
		t.Errorf("Modulo used partitions %v", seen)
	}
}

func TestImportKeepsSequencesAndTimes(t *testing.T) {
	l, _ := newLog(t)
	recs := []Record[string]{{Seq: 5, Time: t0.UnixMilli(), Value: "a"}, {Seq: 6, Time: t0.Add(time.Minute).UnixMilli(), Value: "b"}}
	if err := l.Import("p", 7, recs); err != nil {
		t.Fatal(err)
	}
	if h := l.Head("p"); h != (Head{First: 5, Next: 7}) {
		t.Fatalf("head = %+v", h)
	}
	eq(t, values(l.Read("p", 0, 10)), []string{"a", "b"})
	if got := l.Append("p", t0, "c"); got[0].Seq != 7 {
		t.Fatalf("append after import took %d", got[0].Seq)
	}
	if err := l.Import("p", 0, nil); err == nil {
		t.Fatal("imported into a partition that holds records")
	}
	if err := l.Import("gap", 9, []Record[string]{{Seq: 1}, {Seq: 3}}); err == nil {
		t.Fatal("imported records with a gap")
	}
	if err := l.Import("trimmed", 4, nil); err != nil || l.Head("trimmed") != (Head{First: 4, Next: 4}) {
		t.Fatalf("empty import: %v %+v", err, l.Head("trimmed"))
	}
}
