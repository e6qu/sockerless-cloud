package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const (
	batchDefaultShareDecaySeconds = 600
	batchMaxShareDecaySeconds     = 604800
	batchMaxActiveShares          = 500
	batchMaxSchedulingPriority    = 9999
)

var batchShareIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,255}\*?$`)

type BatchFairsharePolicy struct {
	ShareDecaySeconds  int                   `json:"shareDecaySeconds"`
	ComputeReservation int                   `json:"computeReservation"`
	ShareDistribution  []BatchShareAttribute `json:"shareDistribution"`
}

type BatchShareAttribute struct {
	ShareIdentifier string   `json:"shareIdentifier"`
	WeightFactor    *float64 `json:"weightFactor"`
}

// batchShareUsage is a share identifier's use of a fair-share job queue: the
// vCPUs its jobs hold now, and the vCPU-seconds they held before, decayed over
// the policy's shareDecaySeconds as of At.
type batchShareUsage struct {
	Queue   string    `json:"queue"`
	Share   string    `json:"share"`
	Running float64   `json:"running"`
	Decayed float64   `json:"decayed"`
	At      time.Time `json:"at"`
}

var batchShareUsages sim.Store[batchShareUsage]

func batchShareUsageKey(queue, share string) string {
	return queue + "/" + share
}

func batchParseFairsharePolicy(raw map[string]any) (BatchFairsharePolicy, error) {
	var policy BatchFairsharePolicy
	encoded, err := json.Marshal(raw)
	if err != nil {
		return policy, fmt.Errorf("fairsharePolicy is malformed: %w", err)
	}
	if err := json.Unmarshal(encoded, &policy); err != nil {
		return policy, fmt.Errorf("fairsharePolicy is malformed: %w", err)
	}
	return policy, nil
}

// batchCheckFairsharePolicy enforces the bounds the FairsharePolicy and
// ShareAttributes shapes document.
func batchCheckFairsharePolicy(raw map[string]any) error {
	if raw == nil {
		return nil
	}
	policy, err := batchParseFairsharePolicy(raw)
	if err != nil {
		return err
	}
	if policy.ShareDecaySeconds < 0 || policy.ShareDecaySeconds > batchMaxShareDecaySeconds {
		return fmt.Errorf("fairsharePolicy.shareDecaySeconds must be between 0 and %d, got %d", batchMaxShareDecaySeconds, policy.ShareDecaySeconds)
	}
	if policy.ComputeReservation < 0 || policy.ComputeReservation > 99 {
		return fmt.Errorf("fairsharePolicy.computeReservation must be between 0 and 99, got %d", policy.ComputeReservation)
	}
	if len(policy.ShareDistribution) > batchMaxActiveShares {
		return fmt.Errorf("fairsharePolicy.shareDistribution takes at most %d share identifiers, got %d", batchMaxActiveShares, len(policy.ShareDistribution))
	}
	for i, share := range policy.ShareDistribution {
		if !batchShareIdentifierPattern.MatchString(share.ShareIdentifier) {
			return fmt.Errorf("shareIdentifier %q must be up to 255 alphanumeric characters, optionally followed by an asterisk", share.ShareIdentifier)
		}
		if share.WeightFactor != nil && (*share.WeightFactor < 0.0001 || *share.WeightFactor > 999.9999) {
			return fmt.Errorf("weightFactor for %s must be between 0.0001 and 999.9999, got %v", share.ShareIdentifier, *share.WeightFactor)
		}
		for _, other := range policy.ShareDistribution[:i] {
			if batchSharesOverlap(share.ShareIdentifier, other.ShareIdentifier) {
				return fmt.Errorf("shareIdentifier %s overlaps %s", share.ShareIdentifier, other.ShareIdentifier)
			}
		}
	}
	return nil
}

func batchShareMatches(pattern, share string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(share, prefix)
	}
	return pattern == share
}

func batchSharesOverlap(a, b string) bool {
	aPrefix, aWild := strings.CutSuffix(a, "*")
	bPrefix, bWild := strings.CutSuffix(b, "*")
	switch {
	case aWild && bWild:
		return strings.HasPrefix(aPrefix, bPrefix) || strings.HasPrefix(bPrefix, aPrefix)
	case aWild:
		return strings.HasPrefix(b, aPrefix)
	case bWild:
		return strings.HasPrefix(a, bPrefix)
	}
	return a == b
}

func (policy BatchFairsharePolicy) weight(share string) float64 {
	for _, entry := range policy.ShareDistribution {
		if batchShareMatches(entry.ShareIdentifier, share) && entry.WeightFactor != nil {
			return *entry.WeightFactor
		}
	}
	return 1
}

func (policy BatchFairsharePolicy) decaySeconds() float64 {
	if policy.ShareDecaySeconds == 0 {
		return batchDefaultShareDecaySeconds
	}
	return float64(policy.ShareDecaySeconds)
}

// batchQueueFairsharePolicy returns the fair-share policy of a job queue, and
// false for a FIFO queue.
func batchQueueFairsharePolicy(queue BatchJobQueue) (BatchFairsharePolicy, bool) {
	if queue.SchedulingPolicyArn == "" {
		return BatchFairsharePolicy{}, false
	}
	sp, ok := batchSchedPols.Get(batchNameFromARN(queue.SchedulingPolicyArn))
	if !ok {
		return BatchFairsharePolicy{}, true
	}
	policy, err := batchParseFairsharePolicy(sp.FairsharePolicy)
	if err != nil {
		return BatchFairsharePolicy{}, true
	}
	return policy, true
}

// batchCheckShareLocked applies SubmitJob's share rules: a fair-share queue
// takes only jobs with a share identifier, at most 500 of them active at
// once, and a FIFO queue none.
func batchCheckShareLocked(queue BatchJobQueue, share string, priority *int) error {
	_, fairshare := batchQueueFairsharePolicy(queue)
	if !fairshare {
		if share != "" {
			return errors.New("shareIdentifier applies only to a job queue with a fair-share scheduling policy")
		}
		return nil
	}
	if share == "" {
		return errors.New("shareIdentifier is required for a job queue with a fair-share scheduling policy")
	}
	if !batchShareIdentifierPattern.MatchString(share) {
		return fmt.Errorf("shareIdentifier %q must be up to 255 alphanumeric characters, optionally followed by an asterisk", share)
	}
	if priority != nil && (*priority < 0 || *priority > batchMaxSchedulingPriority) {
		return fmt.Errorf("schedulingPriorityOverride must be between 0 and %d, got %d", batchMaxSchedulingPriority, *priority)
	}
	active := map[string]bool{}
	for _, job := range batchJobs.List() {
		if job.JobQueue == queue.JobQueueArn && job.ShareIdentifier != "" && !batchTerminal(job.Status) {
			active[job.ShareIdentifier] = true
		}
	}
	if !active[share] && len(active) >= batchMaxActiveShares {
		return fmt.Errorf("job queue %s already has %d active share identifiers", queue.JobQueueName, batchMaxActiveShares)
	}
	return nil
}

// advance folds the vCPUs the share held since At into its decayed usage: the
// integral of the running vCPUs under an exponential decay with time constant
// tau.
func (usage *batchShareUsage) advance(now time.Time, tau float64) {
	if !usage.At.IsZero() {
		elapsed := now.Sub(usage.At).Seconds()
		if elapsed > 0 {
			factor := math.Exp(-elapsed / tau)
			usage.Decayed = usage.Decayed*factor + usage.Running*tau*(1-factor)
		}
	}
	usage.At = now
}

func batchShareUsageLocked(queue, share string, now time.Time, tau float64) batchShareUsage {
	usage, ok := batchShareUsages.Get(batchShareUsageKey(queue, share))
	if !ok {
		usage = batchShareUsage{Queue: queue, Share: share}
	}
	usage.advance(now, tau)
	return usage
}

// batchChargeShareLocked records vcpus (negative when an attempt ends) that a
// job of share started or stopped holding in a fair-share queue.
func batchChargeShareLocked(queueName, share string, vcpus float64) {
	if share == "" {
		return
	}
	queue, ok := batchJobQueues.Get(queueName)
	if !ok {
		return
	}
	policy, fairshare := batchQueueFairsharePolicy(queue)
	if !fairshare {
		return
	}
	usage := batchShareUsageLocked(queueName, share, time.Now(), policy.decaySeconds())
	usage.Running = max(usage.Running+vcpus, 0)
	batchShareUsages.Put(batchShareUsageKey(queueName, share), usage)
}

// batchFairshareScheduler orders one fair-share queue's RUNNABLE jobs: the
// share with the least weighted usage goes first, where usage is the vCPUs
// its jobs hold plus their decayed average over shareDecaySeconds, and a
// share's own jobs go by schedulingPriority, then arrival.
type batchFairshareScheduler struct {
	policy BatchFairsharePolicy
	usage  map[string]float64
	shares map[string][]batchRunnableEntry
	active int
}

func newBatchFairshareScheduler(queueName string, policy BatchFairsharePolicy, entries []batchRunnableEntry) *batchFairshareScheduler {
	now, tau := time.Now(), policy.decaySeconds()
	scheduler := &batchFairshareScheduler{policy: policy, usage: map[string]float64{}, shares: map[string][]batchRunnableEntry{}}
	active := map[string]bool{}
	for _, usage := range batchShareUsages.List() {
		if usage.Queue != queueName {
			continue
		}
		usage.advance(now, tau)
		scheduler.usage[usage.Share] = usage.Decayed/tau + usage.Running
		if usage.Running > 0 {
			active[usage.Share] = true
		}
	}
	for _, entry := range entries {
		scheduler.shares[entry.share] = append(scheduler.shares[entry.share], entry)
		active[entry.share] = true
	}
	for _, queued := range scheduler.shares {
		sort.SliceStable(queued, func(i, j int) bool { return queued[i].priority > queued[j].priority })
	}
	scheduler.active = len(active)
	return scheduler
}

func (scheduler *batchFairshareScheduler) score(share string) float64 {
	return scheduler.usage[share] * scheduler.policy.weight(share)
}

// pop removes and returns the job to place next: the head of the share with
// the least weighted usage, the earlier arrival on a tie.
func (scheduler *batchFairshareScheduler) pop() (batchRunnableEntry, bool) {
	best := ""
	for share, queued := range scheduler.shares {
		if best == "" {
			best = share
			continue
		}
		shareScore, bestScore := scheduler.score(share), scheduler.score(best)
		if shareScore < bestScore || shareScore == bestScore && queued[0].seq < scheduler.shares[best][0].seq {
			best = share
		}
	}
	if best == "" {
		return batchRunnableEntry{}, false
	}
	entry := scheduler.shares[best][0]
	if rest := scheduler.shares[best][1:]; len(rest) > 0 {
		scheduler.shares[best] = rest
	} else {
		delete(scheduler.shares, best)
	}
	return entry, true
}

func (scheduler *batchFairshareScheduler) placed(entry batchRunnableEntry) {
	scheduler.usage[entry.share] += entry.vcpus
}

// capacity is how many of a compute environment's maxVCPUs a job of share may
// use: computeReservation holds back (computeReservation/100)^activeShares of
// them for share identifiers that hold none yet.
func (scheduler *batchFairshareScheduler) capacity(share string, maxVCPUs float64) float64 {
	if scheduler.usage[share] == 0 || scheduler.policy.ComputeReservation == 0 {
		return maxVCPUs
	}
	reserved := math.Pow(float64(scheduler.policy.ComputeReservation)/100, float64(scheduler.active))
	return maxVCPUs * (1 - reserved)
}
