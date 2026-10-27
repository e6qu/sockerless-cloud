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

// Container Apps scales a revision on its rules between its minimum and maximum
// replica counts ("Set scaling rules in Azure Container Apps"):
//   - No rule: the default HTTP rule, 0 to 10 replicas, which only an app with
//     ingress can meet.
//   - HTTP: every 15 seconds, requests of the past 15 seconds divided by 15,
//     against concurrentRequests (default 10). A request reaching a revision
//     with no replica starts one.
//   - Azure Queue: reads the queue length every pollingInterval (default 30
//     seconds) through Get Queue Metadata, one replica per queueLength (default
//     5) messages.
//   - Custom azure-servicebus: reads active messages through the Service Bus
//     management endpoint, one replica per messageCount.
//
// The revision runs the most any rule asks for, scales in once the lower count
// has held for the 300-second stabilization window, and to zero once no rule
// has asked for a replica for the cooldown period (default 300 seconds).

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

type acaScaling struct {
	min, max int
	// concurrency is 0 when the revision scales on no HTTP rule.
	concurrency int
	queues      []ContainerAppQueueScaleRule
	serviceBus  []ContainerAppCustomScaleRule
	// otherRules reports a rule of a kind the simulator does not run.
	otherRules bool
	cooldown   time.Duration
	polling    time.Duration
}

// demandDriven: a rule the simulator runs sizes the revision.
func (sc acaScaling) demandDriven() bool {
	return sc.concurrency > 0 || sc.polled()
}

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

// baseline is the minimum, or one for a revision whose only rules the
// simulator does not run, so its replica does the work those rules would start
// it for.
func (sc acaScaling) baseline() int {
	if sc.otherRules && !sc.demandDriven() {
		return max(sc.min, 1)
	}
	return sc.min
}

type acaScaler struct {
	run     *acaRevisionRun
	scaling acaScaling
	queues  acaQueueReader

	mu           sync.Mutex
	requests     []time.Time
	queueDesired int
	// raised is when demand last asked for the running count or more; active when
	// a rule last asked for any replica.
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

// A rule whose queue cannot be read keeps the count its last reading asked
// for.
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

func acaScaleInDue(now, raised, active time.Time, desired int, cooldown time.Duration) bool {
	if desired == 0 {
		return now.Sub(active) >= cooldown
	}
	return now.Sub(raised) >= acaScaleDownStabilization
}

// activate starts a replica for a request reaching a revision that runs none.
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
