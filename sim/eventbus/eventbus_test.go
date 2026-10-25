package eventbus

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

type event struct {
	Kind string `json:"kind"`
	Body string `json:"body"`
}

type subscription struct {
	Name  string
	Kinds []string
	Limit Limits
}

type payload struct {
	Subscription string  `json:"subscription"`
	Events       []event `json:"events"`
}

func newServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "eventbus-test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	return srv
}

func admitsKind(sub subscription, e event) bool {
	for _, k := range sub.Kinds {
		if k == e.Kind {
			return true
		}
	}
	return false
}

func toPayload(sub subscription, events []event) (payload, bool) {
	return payload{Subscription: sub.Name, Events: append([]event(nil), events...)}, true
}

type sink struct {
	mu   sync.Mutex
	got  []payload
	subs map[string][]subscription
}

func (s *sink) put(_ subscription, p payload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, p)
}

func (s *sink) subscriptions(source string) []subscription { return s.subs[source] }

func TestPublishFiltersBatchesAndSinksPerSubscription(t *testing.T) {
	srv := newServer(t)
	s := &sink{subs: map[string][]subscription{
		"orders": {
			{Name: "all", Kinds: []string{"created", "deleted"}, Limit: Limits{MaxEvents: 2}},
			{Name: "created", Kinds: []string{"created"}},
			{Name: "none", Kinds: []string{"archived"}},
		},
	}}
	var archived []string
	bus := New(srv, Config[event, subscription, payload]{
		Subscriptions: s.subscriptions,
		Admits:        admitsKind,
		Batch:         func(sub subscription) Limits { return sub.Limit },
		Transform:     toPayload,
		Archive:       func(source string, e event) { archived = append(archived, source+"/"+e.Body) },
		Sink:          s.put,
	})
	result := bus.Publish("orders",
		event{"created", "a"}, event{"deleted", "b"}, event{"created", "c"})
	if result != (Result{Subscriptions: 2, Deliveries: 4}) {
		t.Fatalf("result %+v, want 2 subscriptions and 4 deliveries", result)
	}
	want := []payload{
		{"all", []event{{"created", "a"}, {"deleted", "b"}}},
		{"all", []event{{"created", "c"}}},
		{"created", []event{{"created", "a"}}},
		{"created", []event{{"created", "c"}}},
	}
	if !reflect.DeepEqual(s.got, want) {
		t.Fatalf("sank %+v, want %+v", s.got, want)
	}
	if strings.Join(archived, ",") != "orders/a,orders/b,orders/c" {
		t.Fatalf("archived %v, want every published event once", archived)
	}
	if got := bus.Publish("unknown", event{"created", "x"}); got != (Result{}) {
		t.Fatalf("a source without subscriptions delivered %+v", got)
	}
}

func TestReplaySkipsTheArchiveAndKeepsOnlyChosenSubscriptions(t *testing.T) {
	srv := newServer(t)
	s := &sink{subs: map[string][]subscription{
		"bus": {{Name: "one", Kinds: []string{"x"}}, {Name: "two", Kinds: []string{"x"}}},
	}}
	archives := 0
	bus := New(srv, Config[event, subscription, payload]{
		Subscriptions: s.subscriptions,
		Admits:        admitsKind,
		Transform:     toPayload,
		Archive:       func(string, event) { archives++ },
		Sink:          s.put,
	})
	result := bus.Replay("bus", []event{{"x", "1"}, {"y", "2"}}, func(sub subscription) bool { return sub.Name == "two" })
	if result != (Result{Subscriptions: 1, Deliveries: 1}) || archives != 0 {
		t.Fatalf("replay %+v with %d archives, want one delivery and none archived", result, archives)
	}
	if len(s.got) != 1 || s.got[0].Subscription != "two" || s.got[0].Events[0].Body != "1" {
		t.Fatalf("replayed %+v, want event 1 to subscription two", s.got)
	}
	if again := bus.Replay("bus", []event{{"x", "3"}}, nil); again.Subscriptions != 2 {
		t.Fatalf("an unrestricted replay reached %d subscriptions, want 2", again.Subscriptions)
	}
}

func TestDeliverToBypassesTheFilterAndTransformMayDrop(t *testing.T) {
	srv := newServer(t)
	s := &sink{}
	bus := New(srv, Config[event, subscription, payload]{
		Subscriptions: s.subscriptions,
		Admits:        func(subscription, event) bool { return false },
		Transform: func(sub subscription, events []event) (payload, bool) {
			return payload{Subscription: sub.Name, Events: events}, sub.Name != "dropped"
		},
		Sink: s.put,
	})
	result := bus.DeliverTo([]subscription{{Name: "kept"}, {Name: "dropped"}}, event{"tick", "now"})
	if result != (Result{Subscriptions: 2, Deliveries: 1}) {
		t.Fatalf("result %+v, want both subscriptions reached and one delivery", result)
	}
	if len(s.got) != 1 || s.got[0].Subscription != "kept" {
		t.Fatalf("sank %+v, want only the kept delivery", s.got)
	}
}

type ended struct {
	item       delivery.Item[payload]
	reason     delivery.Reason
	deadLetter bool
}

func waitEnded(t *testing.T, ch <-chan ended) ended {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("the delivery never settled")
		return ended{}
	}
}

func pushBus(t *testing.T, srv *sim.Server, store sim.Store[delivery.Item[payload]], policy delivery.Policy,
	attempt func(*delivery.Item[payload]) delivery.Outcome, inline bool, out chan<- ended) *Bus[event, subscription, payload] {
	t.Helper()
	return New(srv, Config[event, subscription, payload]{
		Subscriptions: func(string) []subscription { return []subscription{{Name: "hook"}} },
		Transform:     toPayload,
		Delivery: &Delivery[payload]{
			Name:   "test pushes",
			Store:  store,
			Policy: func(payload) delivery.Policy { return policy },
			Attempt: func(_ context.Context, item *delivery.Item[payload]) delivery.Outcome {
				return attempt(item)
			},
			Inline:  func(payload) bool { return inline },
			Settled: func(item delivery.Item[payload], reason delivery.Reason) { out <- ended{item: item, reason: reason} },
			DeadLetter: func(item delivery.Item[payload], reason delivery.Reason) {
				out <- ended{item: item, reason: reason, deadLetter: true}
			},
		},
	})
}

func TestDeliveryRetriesUnderThePolicyAndSettlesWithoutDeadLetter(t *testing.T) {
	srv := newServer(t)
	out := make(chan ended, 4)
	var attempts atomic.Int32
	bus := pushBus(t, srv, sim.NewStateStore[delivery.Item[payload]](),
		delivery.Policy{MaxAttempts: 5, Backoff: func(int) time.Duration { return time.Millisecond }},
		func(*delivery.Item[payload]) delivery.Outcome {
			if attempts.Add(1) < 3 {
				return delivery.Retryable(errors.New("busy"))
			}
			return delivery.Delivered()
		}, false, out)
	bus.Publish("any", event{"x", "1"})
	e := waitEnded(t, out)
	if e.reason != delivery.Succeeded || e.deadLetter || e.item.Attempts != 3 {
		t.Fatalf("settled %+v, want Succeeded after 3 attempts", e)
	}
	select {
	case extra := <-out:
		t.Fatalf("a delivered event reached the dead-letter hook: %+v", extra)
	default:
	}
}

func TestDeliveryDeadLettersWhenAttemptsRunOut(t *testing.T) {
	srv := newServer(t)
	out := make(chan ended, 4)
	bus := pushBus(t, srv, sim.NewStateStore[delivery.Item[payload]](),
		delivery.Policy{MaxAttempts: 2, Backoff: func(int) time.Duration { return time.Millisecond }},
		func(*delivery.Item[payload]) delivery.Outcome { return delivery.Retryable(errors.New("down")) }, false, out)
	bus.Publish("any", event{"x", "1"})
	settled, dead := waitEnded(t, out), waitEnded(t, out)
	if settled.deadLetter || settled.reason != delivery.AttemptsExhausted {
		t.Fatalf("settled %+v, want AttemptsExhausted reported to Settled first", settled)
	}
	if !dead.deadLetter || dead.reason != delivery.AttemptsExhausted || dead.item.Attempts != 2 ||
		dead.item.LastError != "down" || dead.item.Payload.Events[0].Body != "1" {
		t.Fatalf("dead-lettered %+v, want the event after 2 attempts with the last error", dead)
	}
}

func TestDeliveryDeadLettersPastTheMaximumAge(t *testing.T) {
	srv := newServer(t)
	out := make(chan ended, 4)
	bus := pushBus(t, srv, sim.NewStateStore[delivery.Item[payload]](),
		delivery.Policy{MaxAge: time.Nanosecond, Backoff: func(int) time.Duration { return time.Millisecond }},
		func(*delivery.Item[payload]) delivery.Outcome { return delivery.Retryable(errors.New("down")) }, false, out)
	bus.Publish("any", event{"x", "1"})
	waitEnded(t, out)
	if dead := waitEnded(t, out); !dead.deadLetter || dead.reason != delivery.AgeExceeded {
		t.Fatalf("dead-lettered %+v, want AgeExceeded", dead)
	}
}

func TestInlineDeliveryAttemptsBeforePublishReturns(t *testing.T) {
	srv := newServer(t)
	out := make(chan ended, 4)
	var attempted atomic.Bool
	bus := pushBus(t, srv, sim.NewStateStore[delivery.Item[payload]](), delivery.Policy{MaxAttempts: 1},
		func(*delivery.Item[payload]) delivery.Outcome { attempted.Store(true); return delivery.Delivered() }, true, out)
	bus.Publish("any", event{"x", "1"})
	if !attempted.Load() {
		t.Fatal("an inline delivery was not attempted before Publish returned")
	}
	if e := waitEnded(t, out); e.reason != delivery.Succeeded {
		t.Fatalf("settled %+v, want Succeeded", e)
	}
}

func TestSubmitAndResumeDeliverWhatAPreviousBusPersisted(t *testing.T) {
	store := sim.NewStateStore[delivery.Item[payload]]()
	store.Put("left-over", delivery.Item[payload]{ID: "left-over", Payload: payload{Subscription: "hook"}, EnqueuedAt: time.Now()})
	srv := newServer(t)
	out := make(chan ended, 4)
	bus := pushBus(t, srv, store, delivery.Policy{MaxAttempts: 1},
		func(*delivery.Item[payload]) delivery.Outcome { return delivery.Delivered() }, false, out)
	bus.Resume()
	if e := waitEnded(t, out); e.item.ID != "left-over" || e.reason != delivery.Succeeded {
		t.Fatalf("resumed %+v, want the persisted delivery to succeed", e)
	}
	id := bus.Submit(payload{Subscription: "confirmation"})
	if e := waitEnded(t, out); e.item.ID != id || e.item.Payload.Subscription != "confirmation" {
		t.Fatalf("submitted %+v, want delivery %s", e, id)
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	srv := newServer(t)
	for name, cfg := range map[string]Config[event, subscription, payload]{
		"no transform":          {Sink: func(subscription, payload) {}},
		"neither sink nor push": {Transform: toPayload},
		"both sink and push": {Transform: toPayload, Sink: func(subscription, payload) {},
			Delivery: &Delivery[payload]{}},
		"push without attempt": {Transform: toPayload, Delivery: &Delivery[payload]{
			Store: sim.NewStateStore[delivery.Item[payload]](), Policy: func(payload) delivery.Policy { return delivery.Policy{} }}},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("New accepted the configuration")
				}
			}()
			New(srv, cfg)
		})
	}
}

func TestBatchesHonourCountAndBytes(t *testing.T) {
	size := func(s string) int { return len(s) }
	cases := []struct {
		name   string
		events []string
		limits Limits
		want   [][]string
	}{
		{"one at a time by default", []string{"a", "b"}, Limits{}, [][]string{{"a"}, {"b"}}},
		{"by count", []string{"a", "b", "c"}, Limits{MaxEvents: 2}, [][]string{{"a", "b"}, {"c"}}},
		{"by bytes", []string{"aa", "bb", "c"}, Limits{MaxEvents: 10, MaxBytes: 3}, [][]string{{"aa"}, {"bb", "c"}}},
		{"an oversized event travels alone", []string{"a", "toolarge", "b"}, Limits{MaxEvents: 10, MaxBytes: 4},
			[][]string{{"a"}, {"toolarge"}, {"b"}}},
		{"nothing to batch", nil, Limits{MaxEvents: 3}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Batches(c.events, c.limits, size); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Batches = %v, want %v", got, c.want)
			}
		})
	}
}
