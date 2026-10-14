package logstore

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func newLog() *Log[string] {
	return New(sim.NewStateStore[Entry[string]](), sim.NewStateStore[Head]())
}

func equal(a, b []string) bool {
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

func TestAppendKeepsArrivalOrderAndTheEntrysOwnTime(t *testing.T) {
	l := newLog()
	now := time.UnixMilli(5000)
	got := l.Append("g:s", now, Item[string]{Time: 3000, Value: "late"}, Item[string]{Time: 1000, Value: "early"})
	if got[0].Seq != 0 || got[1].Seq != 1 || got[0].Ingested != 5000 || got[1].Time != 1000 {
		t.Fatalf("appended entries = %+v", got)
	}
	l.Append("g:s", now, Item[string]{Time: 4000, Value: "third"})
	if v := l.Values("g:s"); !equal(v, []string{"late", "early", "third"}) {
		t.Errorf("Values = %v, want arrival order", v)
	}
	if h := l.Head("g:s"); h.Next != 3 || h.Count != 3 {
		t.Errorf("Head = %+v", h)
	}
}

// A scope named as a prefix of another reads only its own entries.
func TestScopesWithSharedPrefixesStayApart(t *testing.T) {
	l := newLog()
	now := time.Now()
	l.Append("g:s", now, Item[string]{Value: "short"})
	l.Append("g:s2", now, Item[string]{Value: "long"})
	if v := l.Values("g:s"); !equal(v, []string{"short"}) {
		t.Errorf("Values(g:s) = %v", v)
	}
	if s := l.Scopes("g:"); !equal(s, []string{"g:s", "g:s2"}) {
		t.Errorf("Scopes = %v", s)
	}
}

// Expiry goes by the entry's own time, so an old entry that arrived late
// expires and leaves a gap the reads step over.
func TestExpireDeletesByEntryTimeWhereverTheEntrySits(t *testing.T) {
	l := newLog()
	now := time.Now()
	l.Append("g:s", now,
		Item[string]{Time: 5000, Value: "new"},
		Item[string]{Time: 1000, Value: "old-late"},
		Item[string]{Time: 6000, Value: "newer"})
	if n := l.Expire("g:s", time.UnixMilli(2000)); n != 1 {
		t.Fatalf("Expire deleted %d, want 1", n)
	}
	if v := l.Values("g:s"); !equal(v, []string{"new", "newer"}) {
		t.Errorf("Values after expiry = %v", v)
	}
	l.Append("g:s", now, Item[string]{Time: 7000, Value: "after"})
	if v := l.Values("g:s"); !equal(v, []string{"new", "newer", "after"}) {
		t.Errorf("Values after a later append = %v", v)
	}
	if h := l.Head("g:s"); h.Count != 3 || h.Next != 4 {
		t.Errorf("Head = %+v", h)
	}
}

func TestCapKeepsTheNewestArrivals(t *testing.T) {
	l := newLog()
	now := time.Now()
	for _, v := range []string{"a", "b", "c", "d"} {
		l.Append("t", now, Item[string]{Value: v})
	}
	if n := l.Cap("t", 2); n != 2 {
		t.Fatalf("Cap deleted %d, want 2", n)
	}
	if v := l.Values("t"); !equal(v, []string{"c", "d"}) {
		t.Errorf("Values = %v", v)
	}
	if n := l.Cap("t", 2); n != 0 {
		t.Errorf("a scope within its cap lost %d entries", n)
	}
}

func TestOpenImportAndDrop(t *testing.T) {
	l := newLog()
	l.Open("empty")
	if !l.Exists("empty") || len(l.Values("empty")) != 0 {
		t.Fatal("an opened scope should exist with no entries")
	}
	if err := l.Import("moved", []Entry[string]{{Time: 1, Ingested: 2, Value: "x"}, {Time: 3, Ingested: 4, Value: "y"}}); err != nil {
		t.Fatal(err)
	}
	got := l.Entries("moved")
	if len(got) != 2 || got[1].Seq != 1 || got[1].Ingested != 4 {
		t.Fatalf("imported entries = %+v", got)
	}
	if err := l.Import("moved", nil); err == nil {
		t.Error("Import into a scope that holds entries should fail")
	}
	l.Append("p:one", time.Now(), Item[string]{Value: "1"})
	l.Append("p:two", time.Now(), Item[string]{Value: "2"})
	l.DropPrefix("p:")
	if l.Exists("p:one") || len(l.Values("p:two")) != 0 || !l.Exists("moved") {
		t.Error("DropPrefix removed the wrong scopes")
	}
}

func TestEditRewritesAndDeletesInPlace(t *testing.T) {
	l := newLog()
	now := time.Now()
	l.Append("s", now, Item[string]{Value: "keep"}, Item[string]{Value: "drop"}, Item[string]{Value: "change"})
	n := l.Edit("s", func(e *Entry[string]) (bool, bool) {
		switch e.Value {
		case "drop":
			return false, false
		case "change":
			e.Value = "changed"
			return true, true
		}
		return true, false
	})
	if n != 1 {
		t.Fatalf("Edit deleted %d, want 1", n)
	}
	if v := l.Values("s"); !equal(v, []string{"keep", "changed"}) {
		t.Errorf("Values after Edit = %v", v)
	}
	if h := l.Head("s"); h.Count != 2 || h.Next != 3 {
		t.Errorf("Head after Edit = %+v", h)
	}
	if n := l.Edit("missing", func(*Entry[string]) (bool, bool) { return false, false }); n != 0 {
		t.Errorf("Edit of a missing scope deleted %d", n)
	}
}
