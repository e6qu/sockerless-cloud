package kvstore

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestBucketsTakeRefillsAtRateUpToBurst(t *testing.T) {
	var b Buckets
	limit := Limit{Rate: 10, Burst: 20}
	start := time.Unix(1000, 0)
	if !b.Take("t", limit, 20, start) {
		t.Fatal("a new bucket starts full")
	}
	if b.Take("t", limit, 1, start) {
		t.Fatal("an empty bucket refuses")
	}
	if !b.Take("t", limit, 5, start.Add(500*time.Millisecond)) {
		t.Fatal("half a second at 10/s accrues 5")
	}
	if b.Take("t", limit, 21, start.Add(time.Hour)) {
		t.Fatal("the balance never exceeds the burst")
	}
	if !b.Take("t", limit, 20, start.Add(time.Hour)) {
		t.Fatal("a refused take spends nothing")
	}
}

func TestBucketsAdmitLetsDebtRepayBeforeTheNextRequest(t *testing.T) {
	var b Buckets
	limit := Limit{Rate: 100, Burst: 100}
	now := time.Unix(1000, 0)
	if ok, _ := b.Admit("c", limit, now); !ok {
		t.Fatal("a full bucket admits")
	}
	b.Charge("c", limit, 250, now)
	ok, wait := b.Admit("c", limit, now)
	if ok {
		t.Fatal("a bucket in debt refuses")
	}
	if wait != 1501*time.Millisecond {
		t.Fatalf("150 units of debt at 100/s: want 1.501s, got %v", wait)
	}
	if ok, _ := b.Admit("c", limit, now.Add(wait)); !ok {
		t.Fatal("the bucket admits once the debt is repaid")
	}
}

func TestBucketsStartFullWhenTheLimitChanges(t *testing.T) {
	var b Buckets
	now := time.Unix(1000, 0)
	b.Take("t", Limit{Rate: 1, Burst: 1}, 1, now)
	if !b.Take("t", Limit{Rate: 5, Burst: 5}, 5, now) {
		t.Fatal("a changed limit refills the bucket")
	}
	b.Forget("t")
	if !b.Take("t", Limit{Rate: 5, Burst: 5}, 5, now) {
		t.Fatal("a forgotten bucket starts full")
	}
}

func TestRWLocksExcludeWritersAndOrderMultiKeyCalls(t *testing.T) {
	var l RWLocks
	var inside atomic.Int32
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys := []string{"a", "b"}
			if i%2 == 0 {
				keys = []string{"b", "a", "a"}
			}
			release := l.Lock(true, keys...)
			if inside.Add(1) != 1 {
				t.Error("two writers held the same keys")
			}
			inside.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if l.Acquired() != 100 {
		t.Fatalf("each call takes two distinct keys: want 100, got %d", l.Acquired())
	}
	r1 := l.Lock(false, "a")
	r2 := l.Lock(false, "a")
	r1()
	r2()
	if l.Held() != 0 {
		t.Fatalf("released keys keep no entry: %d held", l.Held())
	}
}

func TestChangeLogReadsAfterASequenceAndKeepsDeletes(t *testing.T) {
	log := NewChangeLog[string](sim.NewStateStore[Change[string]](), time.Hour)
	at := time.Unix(1000, 0)
	v1, v2 := "v1", "v2"
	log.Append("a/b/c", Change[string]{Seq: 3, Key: "x", Op: OpCreate, Current: &v1, At: at})
	log.Append("a/b/c", Change[string]{Seq: 7, Key: "x", Op: OpReplace, Current: &v2, Previous: &v1, At: at})
	log.Append("a/b/c", Change[string]{Seq: 12, Key: "x", Op: OpDelete, Previous: &v2, At: at.Add(2 * time.Hour)})
	log.Append("a/b/cd", Change[string]{Seq: 8, Key: "y", Op: OpCreate, Current: &v1, At: at})

	if log.LastSeq() != 12 {
		t.Fatalf("last sequence: %d", log.LastSeq())
	}
	all := log.Read("a/b/c", 0, 0)
	if len(all) != 3 || all[0].Seq != 3 || all[2].Op != OpDelete {
		t.Fatalf("stream read: %+v", all)
	}
	if page := log.Read("a/b/c", 3, 1); len(page) != 1 || page[0].Seq != 7 {
		t.Fatalf("page after 3: %+v", page)
	}
	if n := log.Prune(at.Add(90 * time.Minute)); n != 3 {
		t.Fatalf("prune removes changes older than the retention: %d", n)
	}
	if rest := log.Read("a/b/c", 0, 0); len(rest) != 1 || rest[0].Seq != 12 {
		t.Fatalf("after prune: %+v", rest)
	}
	log.Drop("a/b/c/")
	if rest := log.Read("a/b/c", 0, 0); len(rest) != 0 {
		t.Fatalf("after drop: %+v", rest)
	}
}

func TestStartSweeperSweepsUntilTheServerStops(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "kvstore-sweeper-test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	var sweeps atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	StartSweeper(srv, "expiry", time.Millisecond, func(time.Time) {
		if sweeps.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	<-entered
	stopped := make(chan struct{})
	go func() {
		srv.StopBackground()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("StopBackground returned while a sweep was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-stopped
	if n := sweeps.Load(); n != 1 {
		t.Fatalf("the sweeper swept %d times; it must stop after the sweep the server waited for", n)
	}
}
