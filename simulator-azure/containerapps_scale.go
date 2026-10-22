package main

import (
	"context"
	"math"
	"strconv"
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
// revision with no replica starts one, and the revision scales in — to zero
// when its minimum is 0 — once the lower count has held for its cooldown
// period.

const (
	acaHTTPScaleWindow         = 15 * time.Second
	acaDefaultConcurrency      = 10
	acaDefaultMaxReplicas      = 10
	acaDefaultCooldownSeconds  = 300
	acaDefaultPollingSeconds   = 30
	acaHTTPConcurrencyMetadata = "concurrentRequests"
)

// acaScaling is how a revision scales.
type acaScaling struct {
	min, max int
	// concurrency is the HTTP rule's concurrent requests per replica, 0 when
	// the revision scales on no HTTP rule.
	concurrency int
	// otherRules reports a rule of another kind, which the simulator does not
	// run.
	otherRules bool
	cooldown   time.Duration
}

func acaRevisionScaling(template *ContainerAppTemplate, ingress bool) acaScaling {
	sc := acaScaling{max: acaDefaultMaxReplicas, cooldown: acaDefaultCooldownSeconds * time.Second}
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
	if sc.otherRules && sc.concurrency == 0 {
		return max(sc.min, 1)
	}
	return sc.min
}

// acaScaler sizes one revision's replica set on its HTTP requests.
type acaScaler struct {
	run     *acaRevisionRun
	scaling acaScaling

	mu       sync.Mutex
	requests []time.Time
	// raised is when demand last asked for the count the set runs or more.
	raised time.Time
	stop   chan struct{}
	once   sync.Once
}

func newACAScaler(run *acaRevisionRun, scaling acaScaling) *acaScaler {
	sc := &acaScaler{run: run, scaling: scaling, raised: time.Now(), stop: make(chan struct{})}
	if scaling.concurrency > 0 {
		bg.Go(sc.evaluateEvery)
	}
	return sc
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
// closed.
func (sc *acaScaler) evaluate(now time.Time) {
	sc.mu.Lock()
	recent := sc.requests[:0]
	for _, at := range sc.requests {
		if now.Sub(at) < acaHTTPScaleWindow {
			recent = append(recent, at)
		}
	}
	sc.requests = recent
	concurrent := float64(len(recent)) / acaHTTPScaleWindow.Seconds()
	desired := int(math.Ceil(concurrent / float64(sc.scaling.concurrency)))
	desired = min(max(desired, sc.scaling.min), sc.scaling.max)
	running := len(sc.run.set.Replicas())
	if desired >= running {
		sc.raised = now
	}
	scaleIn := desired < running && now.Sub(sc.raised) >= sc.scaling.cooldown
	sc.mu.Unlock()
	switch {
	case desired > running:
		_ = sc.run.set.Raise(context.Background(), desired)
	case scaleIn:
		_ = sc.run.set.Scale(context.Background(), desired)
	}
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
