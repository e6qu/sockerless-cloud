package main

import (
	"context"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Container Apps scales a revision on its scale rules, between its minimum
// and maximum replica counts. An app whose template names no rule takes the
// default one — "If you don't create a scale rule, the default scale rule is
// applied to your container app": HTTP, from 0 to 10 replicas — which only an
// app with ingress can meet. The HTTP rule sizes the revision every 15
// seconds: "the number of concurrent requests is calculated as the number of
// requests in the past 15 seconds divided by 15", against the rule's
// concurrentRequests, 10 unless it names another. A request that reaches a
// revision with no replica starts one. The revision scales in once the lower
// count has held for the scale-down stabilization window, 300 seconds, and
// to zero once no rule has asked for a replica for the cooldown period, 300
// seconds unless the scale names another ("Scale behavior" in "Set scaling
// rules in Azure Container Apps": "Scale down only happens if the condition
// is satisfied for 300 seconds (scale down stabilization window). Once the
// queue length is 0, KEDA waits for 300 seconds (cool down period) before
// scaling the app to 0"). An Azure Queue rule reads its queue's length every polling
// interval, 30 seconds unless the scale names another, through Get Queue
// Metadata as the rule's connection string or managed identity, and asks for
// one replica per queueLength messages, 5 unless the rule names another. A
// custom azure-servicebus rule reads its queue's or subscription's active
// messages through the Service Bus management endpoint, signed with the
// connection string's key, and asks for one replica per messageCount. The
// revision runs the most any rule asks for.

const (
	acaHTTPScaleWindow         = 15 * time.Second
	acaDefaultConcurrency      = 10
	acaDefaultMaxReplicas      = 10
	acaDefaultCooldownSeconds  = 300
	acaScaleDownStabilization  = 300 * time.Second
	acaDefaultPollingSeconds   = 30
	acaHTTPConcurrencyMetadata = "concurrentRequests"
	acaDefaultQueueLength      = 5
	acaServiceBusScaler        = "azure-servicebus"
)

// acaScaling is how a revision scales.
type acaScaling struct {
	min, max int
	// concurrency is the HTTP rule's concurrent requests per replica, 0 when
	// the revision scales on no HTTP rule.
	concurrency int
	// queues are the revision's Azure Queue rules, and serviceBus its custom
	// azure-servicebus rules.
	queues     []ContainerAppQueueScaleRule
	serviceBus []ContainerAppCustomScaleRule
	// otherRules reports a rule of another kind, which the simulator does not
	// run.
	otherRules bool
	cooldown   time.Duration
	polling    time.Duration
}

// demandDriven reports whether a rule the simulator runs sizes the revision,
// so it scales from what it is asked rather than holding a fixed count.
func (sc acaScaling) demandDriven() bool {
	return sc.concurrency > 0 || sc.polled()
}

// polled reports whether a rule reads a queue the scaler polls.
func (sc acaScaling) polled() bool { return len(sc.queues) > 0 || len(sc.serviceBus) > 0 }

func acaRevisionScaling(template *ContainerAppTemplate, ingress bool) acaScaling {
	sc := acaScaling{max: acaDefaultMaxReplicas, cooldown: acaDefaultCooldownSeconds * time.Second,
		polling: acaDefaultPollingSeconds * time.Second}
	if template == nil || len(template.Containers) == 0 {
		return acaScaling{}
	}
	scale := template.Scale
	if scale == nil {
		scale = &ContainerAppScale{}
	}
	if scale.MinReplicas != nil {
		sc.min = max(int(*scale.MinReplicas), 0)
	}
	if scale.MaxReplicas != nil {
		sc.max = max(int(*scale.MaxReplicas), 1)
	}
	sc.max = max(sc.max, sc.min)
	if scale.CooldownPeriod != nil {
		sc.cooldown = time.Duration(*scale.CooldownPeriod) * time.Second
	}
	if scale.PollingInterval != nil && *scale.PollingInterval > 0 {
		sc.polling = time.Duration(*scale.PollingInterval) * time.Second
	}
	if len(scale.Rules) == 0 {
		if ingress {
			sc.concurrency = acaDefaultConcurrency
		}
		return sc
	}
	for _, rule := range scale.Rules {
		switch {
		case rule.HTTP != nil:
			n := acaDefaultConcurrency
			if v, err := strconv.Atoi(rule.HTTP.Metadata[acaHTTPConcurrencyMetadata]); err == nil && v > 0 {
				n = v
			}
			if sc.concurrency == 0 || n < sc.concurrency {
				sc.concurrency = n
			}
		case rule.AzureQueue != nil:
			sc.queues = append(sc.queues, *rule.AzureQueue)
		case rule.Custom != nil && strings.EqualFold(rule.Custom.Type, acaServiceBusScaler):
			sc.serviceBus = append(sc.serviceBus, *rule.Custom)
		default:
			sc.otherRules = true
		}
	}
	return sc
}

// baseline is how many replicas the revision runs before any request: its
// minimum, and one for a revision whose only rules the simulator does not
// run, so its replica does the work those rules would start it for.
func (sc acaScaling) baseline() int {
	if sc.otherRules && !sc.demandDriven() {
		return max(sc.min, 1)
	}
	return sc.min
}

// acaScaler sizes one revision's replica set on its HTTP requests and its
// queues.
type acaScaler struct {
	run     *acaRevisionRun
	scaling acaScaling
	queues  acaQueueReader

	mu       sync.Mutex
	requests []time.Time
	// queueDesired is what the queue rules asked for at their last poll.
	queueDesired int
	// raised is when demand last asked for the count the set runs or more,
	// and active when a rule last asked for any replica.
	raised, active time.Time
	stop           chan struct{}
	once           sync.Once
}

func newACAScaler(run *acaRevisionRun, scaling acaScaling, queues acaQueueReader) *acaScaler {
	now := time.Now()
	sc := &acaScaler{run: run, scaling: scaling, queues: queues, raised: now, active: now, stop: make(chan struct{})}
	if scaling.concurrency > 0 {
		bg.Go(sc.evaluateEvery)
	}
	if scaling.polled() {
		bg.Go(sc.pollQueues)
	}
	return sc
}

// pollQueues reads the queue rules' queues every polling interval and sizes
// the revision on them. A rule whose queue cannot be read leaves the count
// its last reading asked for.
func (sc *acaScaler) pollQueues() {
	ticker := time.NewTicker(sc.scaling.polling)
	defer ticker.Stop()
	for {
		desired, ok := 0, true
		for _, rule := range sc.scaling.queues {
			length, err := sc.queues.length(context.Background(), rule)
			if err != nil {
				log.Printf("Container Apps: azure-queue scale rule on %s/%s: %v", rule.AccountName, rule.QueueName, err)
				ok = false
				continue
			}
			target := acaDefaultQueueLength
			if rule.QueueLength != nil && *rule.QueueLength > 0 {
				target = int(*rule.QueueLength)
			}
			desired = max(desired, (length+target-1)/target)
		}
		for _, rule := range sc.scaling.serviceBus {
			length, err := sc.queues.serviceBusLength(context.Background(), rule)
			if err != nil {
				log.Printf("Container Apps: azure-servicebus scale rule: %v", err)
				ok = false
				continue
			}
			target := acaDefaultQueueLength
			if v, err := strconv.Atoi(rule.Metadata["messageCount"]); err == nil && v > 0 {
				target = v
			}
			desired = max(desired, (length+target-1)/target)
		}
		sc.mu.Lock()
		if ok || desired > sc.queueDesired {
			sc.queueDesired = desired
		}
		sc.mu.Unlock()
		sc.evaluate(time.Now())
		select {
		case <-sc.stop:
			return
		case <-ticker.C:
		}
	}
}

func (sc *acaScaler) close() { sc.once.Do(func() { close(sc.stop) }) }

// record counts a request the ingress forwards to the revision.
func (sc *acaScaler) record(now time.Time) {
	sc.mu.Lock()
	sc.requests = append(sc.requests, now)
	sc.mu.Unlock()
}

func (sc *acaScaler) evaluateEvery() {
	ticker := time.NewTicker(acaHTTPScaleWindow)
	defer ticker.Stop()
	for {
		select {
		case <-sc.stop:
			return
		case now := <-ticker.C:
			sc.evaluate(now)
		}
	}
}

// evaluate sizes the revision from the requests of the window that just
// closed and the queues' last reading.
func (sc *acaScaler) evaluate(now time.Time) {
	sc.mu.Lock()
	recent := sc.requests[:0]
	for _, at := range sc.requests {
		if now.Sub(at) < acaHTTPScaleWindow {
			recent = append(recent, at)
		}
	}
	sc.requests = recent
	desired := sc.queueDesired
	if sc.scaling.concurrency > 0 {
		concurrent := float64(len(recent)) / acaHTTPScaleWindow.Seconds()
		desired = max(desired, int(math.Ceil(concurrent/float64(sc.scaling.concurrency))))
	}
	if desired > 0 {
		sc.active = now
	}
	desired = min(max(desired, sc.scaling.min), sc.scaling.max)
	running := len(sc.run.set.Replicas())
	if desired >= running {
		sc.raised = now
	}
	scaleIn := desired < running && acaScaleInDue(now, sc.raised, sc.active, desired, sc.scaling.cooldown)
	sc.mu.Unlock()
	switch {
	case desired > running:
		_ = sc.run.set.Raise(context.Background(), desired)
	case scaleIn:
		_ = sc.run.set.Scale(context.Background(), desired)
	}
}

// acaScaleInDue reports whether a revision running more replicas than desired
// scales in now: to zero once no rule has asked for a replica for the
// cooldown period, and to a lower nonzero count once the lower count has held
// for the scale-down stabilization window.
func acaScaleInDue(now, raised, active time.Time, desired int, cooldown time.Duration) bool {
	if desired == 0 {
		return now.Sub(active) >= cooldown
	}
	return now.Sub(raised) >= acaScaleDownStabilization
}

// activate starts a replica for a request that reached the revision when it
// ran none, as the HTTP scaler starts one for a request to a revision scaled
// to zero.
func (sc *acaScaler) activate(ctx context.Context) error {
	if sc.scaling.concurrency == 0 || len(sc.run.set.Replicas()) > 0 {
		return nil
	}
	sc.mu.Lock()
	sc.raised = time.Now()
	sc.active = sc.raised
	sc.mu.Unlock()
	return sc.run.set.Raise(ctx, 1)
}

// acaReadiness wakes the requests that wait for a replica to become ready.
var acaReadiness = struct {
	sync.Mutex
	changed chan struct{}
}{changed: make(chan struct{})}

func acaReadinessChanged() {
	acaReadiness.Lock()
	close(acaReadiness.changed)
	acaReadiness.changed = make(chan struct{})
	acaReadiness.Unlock()
}

func acaReadinessChange() <-chan struct{} {
	acaReadiness.Lock()
	defer acaReadiness.Unlock()
	return acaReadiness.changed
}
