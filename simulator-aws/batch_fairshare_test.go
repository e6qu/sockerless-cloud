package main

import (
	"math"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func batchUseMemoryFairshareStores(t *testing.T) {
	t.Helper()
	batchUseMemoryJobStore(t)
	previousUsages, previousPolicies, previousQueues := batchShareUsages, batchSchedPols, batchJobQueues
	batchShareUsages = sim.NewStateStore[batchShareUsage]()
	batchSchedPols = sim.NewStateStore[BatchSchedulingPolicy]()
	batchJobQueues = sim.NewStateStore[BatchJobQueue]()
	t.Cleanup(func() {
		batchShareUsages, batchSchedPols, batchJobQueues = previousUsages, previousPolicies, previousQueues
	})
}

func weight(w float64) *float64 { return &w }

func batchPopShares(scheduler *batchFairshareScheduler, n int) []string {
	var shares []string
	for range n {
		entry, ok := scheduler.pop()
		if !ok {
			break
		}
		scheduler.placed(entry)
		shares = append(shares, entry.share)
	}
	return shares
}

func batchShareEntries(share string, n, firstSeq int) []batchRunnableEntry {
	entries := make([]batchRunnableEntry, n)
	for i := range entries {
		entries[i] = batchRunnableEntry{jobID: share + string(rune('0'+i)), queue: "q", vcpus: 1, share: share, seq: firstSeq + i}
	}
	return entries
}

func TestBatchFairshareAlternatesSharesOfEqualWeight(t *testing.T) {
	batchUseMemoryFairshareStores(t)
	entries := append(batchShareEntries("teamA", 3, 0), batchShareEntries("teamB", 3, 3)...)
	got := batchPopShares(newBatchFairshareScheduler("q", BatchFairsharePolicy{}, entries), 6)
	want := []string{"teamA", "teamB", "teamA", "teamB", "teamA", "teamB"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("placement order = %v, want %v", got, want)
		}
	}
}

func TestBatchFairshareGivesALowerWeightFactorMoreCapacity(t *testing.T) {
	batchUseMemoryFairshareStores(t)
	policy := BatchFairsharePolicy{ShareDistribution: []BatchShareAttribute{
		{ShareIdentifier: "team*", WeightFactor: weight(0.5)},
		{ShareIdentifier: "other", WeightFactor: weight(1)},
	}}
	entries := append(batchShareEntries("other", 6, 0), batchShareEntries("teamA", 6, 6)...)
	counts := map[string]int{}
	for _, share := range batchPopShares(newBatchFairshareScheduler("q", policy, entries), 6) {
		counts[share]++
	}
	if counts["teamA"] != 4 || counts["other"] != 2 {
		t.Fatalf("first six placements = %v, want teamA twice as many as other", counts)
	}
}

func TestBatchFairshareOrdersAShareByPriorityThenArrival(t *testing.T) {
	batchUseMemoryFairshareStores(t)
	entries := []batchRunnableEntry{
		{jobID: "low", share: "a", vcpus: 1, priority: 1, seq: 0},
		{jobID: "high", share: "a", vcpus: 1, priority: 5, seq: 1},
		{jobID: "low-later", share: "a", vcpus: 1, priority: 1, seq: 2},
	}
	scheduler := newBatchFairshareScheduler("q", BatchFairsharePolicy{}, entries)
	for _, want := range []string{"high", "low", "low-later"} {
		entry, ok := scheduler.pop()
		if !ok || entry.jobID != want {
			t.Fatalf("popped %q, want %q", entry.jobID, want)
		}
	}
}

func TestBatchFairshareCountsDecayedUsage(t *testing.T) {
	batchUseMemoryFairshareStores(t)
	batchShareUsages.Put(batchShareUsageKey("q", "busy"), batchShareUsage{Queue: "q", Share: "busy", Decayed: 600 * 4, At: time.Now()})
	entries := append(batchShareEntries("busy", 2, 0), batchShareEntries("idle", 2, 2)...)
	got := batchPopShares(newBatchFairshareScheduler("q", BatchFairsharePolicy{}, entries), 2)
	if got[0] != "idle" || got[1] != "idle" {
		t.Fatalf("placements = %v, want the idle share first while the busy one's recent usage decays", got)
	}

	usage := batchShareUsage{Running: 2, At: time.Unix(0, 0)}
	usage.advance(time.Unix(600, 0), 600)
	if want := 2 * 600 * (1 - math.Exp(-1)); math.Abs(usage.Decayed-want) > 1e-9 {
		t.Fatalf("decayed usage = %v, want %v", usage.Decayed, want)
	}
}

func TestBatchFairshareReservesCapacityForUnusedShares(t *testing.T) {
	batchUseMemoryFairshareStores(t)
	batchShareUsages.Put(batchShareUsageKey("q", "used"), batchShareUsage{Queue: "q", Share: "used", Running: 1, At: time.Now()})
	scheduler := newBatchFairshareScheduler("q", BatchFairsharePolicy{ComputeReservation: 50}, nil)
	if got := scheduler.capacity("used", 8); got != 4 {
		t.Fatalf("capacity for a share already in use = %v, want 4 with 50%% reserved for one active share", got)
	}
	if got := scheduler.capacity("new", 8); got != 8 {
		t.Fatalf("capacity for an unused share = %v, want all 8", got)
	}
}

func TestBatchCheckFairsharePolicyEnforcesTheDocumentedBounds(t *testing.T) {
	for name, tc := range map[string]struct {
		raw     map[string]any
		wantErr bool
	}{
		"absent":                   {raw: nil},
		"defaults":                 {raw: map[string]any{}},
		"week of decay":            {raw: map[string]any{"shareDecaySeconds": float64(604800)}},
		"decay past a week":        {raw: map[string]any{"shareDecaySeconds": float64(604801)}, wantErr: true},
		"reservation of 100":       {raw: map[string]any{"computeReservation": float64(100)}, wantErr: true},
		"weight too small":         {raw: map[string]any{"shareDistribution": []any{map[string]any{"shareIdentifier": "a", "weightFactor": 0.00001}}}, wantErr: true},
		"non-alphanumeric share":   {raw: map[string]any{"shareDistribution": []any{map[string]any{"shareIdentifier": "team-a"}}}, wantErr: true},
		"prefix overlaps its name": {raw: map[string]any{"shareDistribution": []any{map[string]any{"shareIdentifier": "UserA*"}, map[string]any{"shareIdentifier": "UserA1"}}}, wantErr: true},
		"disjoint prefixes":        {raw: map[string]any{"shareDistribution": []any{map[string]any{"shareIdentifier": "UserA*"}, map[string]any{"shareIdentifier": "UserB*"}}}},
	} {
		err := batchCheckFairsharePolicy(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: error = %v, want error %v", name, err, tc.wantErr)
		}
	}
}

func TestBatchCheckShareRequiresAShareOnlyOnAFairshareQueue(t *testing.T) {
	batchUseMemoryFairshareStores(t)
	batchSchedPols.Put("policy", BatchSchedulingPolicy{Name: "policy", Arn: batchARN("scheduling-policy/policy")})
	fifo := BatchJobQueue{JobQueueName: "fifo", JobQueueArn: batchARN("job-queue/fifo")}
	fair := BatchJobQueue{JobQueueName: "fair", JobQueueArn: batchARN("job-queue/fair"), SchedulingPolicyArn: batchARN("scheduling-policy/policy")}
	priority := func(p int) *int { return &p }
	for name, tc := range map[string]struct {
		queue    BatchJobQueue
		share    string
		priority *int
		wantErr  bool
	}{
		"FIFO without share":         {queue: fifo},
		"FIFO with share":            {queue: fifo, share: "teamA", wantErr: true},
		"fair share without share":   {queue: fair, wantErr: true},
		"fair share with share":      {queue: fair, share: "teamA", priority: priority(9999)},
		"priority out of range":      {queue: fair, share: "teamA", priority: priority(10000), wantErr: true},
		"share with an asterisk":     {queue: fair, share: "teamA*"},
		"share with a bad character": {queue: fair, share: "team/A", wantErr: true},
	} {
		err := batchCheckShareLocked(tc.queue, tc.share, tc.priority)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: error = %v, want error %v", name, err, tc.wantErr)
		}
	}
}
