package delivery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

type payload struct {
	Body  string `json:"body"`
	Notes []int  `json:"notes"`
}

func newServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "delivery-test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	return srv
}

type finished struct {
	item   Item[payload]
	reason Reason
}

func waitFinished(t *testing.T, ch <-chan finished) finished {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("the delivery never finished")
		return finished{}
	}
}

func TestDispatcherRetriesUntilDeliveredAndKeepsPayloadChanges(t *testing.T) {
	srv := newServer(t)
	done := make(chan finished, 1)
	var attempts atomic.Int32
	var waits []time.Duration
	store := sim.NewStateStore[Item[payload]]()
	d := New(srv, "test deliveries", store, Handler[payload]{
		Policy: func(payload) Policy {
			return Policy{MaxAttempts: 5, Backoff: func(n int) time.Duration {
				waits = append(waits, time.Duration(n)*time.Millisecond)
				return time.Duration(n) * time.Millisecond
			}, MinWait: func(last Outcome) time.Duration {
				if last.OK() || !last.Retry {
					t.Errorf("MinWait consulted with %+v", last)
				}
				return 2 * time.Millisecond
			}}
		},
		Attempt: func(_ context.Context, item *Item[payload]) Outcome {
			item.Payload.Notes = append(item.Payload.Notes, item.Attempts)
			if attempts.Add(1) < 3 {
				return Retryable(errors.New("not yet"))
			}
			return Delivered()
		},
		Finish: func(item Item[payload], reason Reason) { done <- finished{item, reason} },
	})
	d.Submit("one", payload{Body: "hello"})
	f := waitFinished(t, done)
	if f.reason != Succeeded || f.item.Attempts != 3 {
		t.Fatalf("finished %s after %d attempts, want Succeeded after 3", f.reason, f.item.Attempts)
	}
	if len(f.item.Payload.Notes) != 3 || f.item.Payload.Notes[2] != 3 {
		t.Fatalf("payload changes between attempts were lost: %v", f.item.Payload.Notes)
	}
	if len(waits) != 2 || waits[0] != time.Millisecond || waits[1] != 2*time.Millisecond {
		t.Fatalf("backoff consulted as %v, want retries 1 and 2", waits)
	}
	bg.Await()
	if store.Len() != 0 {
		t.Fatal("a finished delivery stayed persisted")
	}
}

func TestDispatcherEndsOnExhaustionRejectionAndAge(t *testing.T) {
	srv := newServer(t)
	done := make(chan finished, 3)
	start := time.Now()
	var elapsed atomic.Int64
	d := New(srv, "test deliveries", sim.NewStateStore[Item[payload]](), Handler[payload]{
		Policy: func(p payload) Policy {
			if p.Body == "old" {
				return Policy{MaxAge: time.Minute, Backoff: Steps(time.Millisecond)}
			}
			return Policy{MaxAttempts: 2, Backoff: Steps(time.Millisecond)}
		},
		Attempt: func(_ context.Context, item *Item[payload]) Outcome {
			if item.Payload.Body == "old" {
				elapsed.Store(int64(time.Hour))
			}
			if item.Payload.Body == "reject" {
				return Permanent(errors.New("bad request")).WithStatus(400)
			}
			return Retryable(errors.New("unavailable")).WithStatus(503)
		},
		Finish: func(item Item[payload], reason Reason) { done <- finished{item, reason} },
	})
	d.now = func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
	d.Submit("exhaust", payload{Body: "exhaust"})
	d.Submit("reject", payload{Body: "reject"})
	d.Submit("old", payload{Body: "old"})
	got := map[string]finished{}
	for range 3 {
		f := waitFinished(t, done)
		got[f.item.ID] = f
	}
	if f := got["exhaust"]; f.reason != AttemptsExhausted || f.item.Attempts != 2 || f.item.LastStatus != 503 {
		t.Errorf("exhaust: %s after %d attempts status %d", f.reason, f.item.Attempts, f.item.LastStatus)
	}
	if f := got["reject"]; f.reason != Rejected || f.item.Attempts != 1 || f.item.LastError != "bad request" {
		t.Errorf("reject: %s after %d attempts (%q)", f.reason, f.item.Attempts, f.item.LastError)
	}
	if f := got["old"]; f.reason != AgeExceeded || f.item.Attempts != 1 {
		t.Errorf("old: %s after %d attempts", f.reason, f.item.Attempts)
	}
}

func TestDispatcherResumesPersistedDeliveries(t *testing.T) {
	store := sim.NewStateStore[Item[payload]]()
	store.Put("carried", Item[payload]{
		ID: "carried", Payload: payload{Body: "from before"}, Attempts: 1,
		EnqueuedAt: time.Now().Add(-time.Minute), NextAttemptAt: time.Now().Add(time.Millisecond),
	})
	srv := newServer(t)
	done := make(chan finished, 1)
	d := New(srv, "test deliveries", store, Handler[payload]{
		Policy:  func(payload) Policy { return Policy{} },
		Attempt: func(context.Context, *Item[payload]) Outcome { return Delivered() },
		Finish:  func(item Item[payload], reason Reason) { done <- finished{item, reason} },
	})
	d.Resume()
	f := waitFinished(t, done)
	if f.reason != Succeeded || f.item.Attempts != 2 || f.item.Payload.Body != "from before" {
		t.Fatalf("resumed delivery finished %s after %d attempts with %+v", f.reason, f.item.Attempts, f.item.Payload)
	}
}

func TestDispatcherStopKeepsAnInterruptedDeliveryForTheNextStart(t *testing.T) {
	srv := newServer(t)
	store := sim.NewStateStore[Item[payload]]()
	entered := make(chan struct{})
	d := New(srv, "test deliveries", store, Handler[payload]{
		Policy: func(payload) Policy { return Policy{} },
		Attempt: func(ctx context.Context, _ *Item[payload]) Outcome {
			close(entered)
			<-ctx.Done()
			return Retryable(ctx.Err())
		},
	})
	d.Submit("interrupted", payload{})
	<-entered
	srv.StopBackground()
	item, ok := store.Get("interrupted")
	if !ok || item.Attempts != 0 {
		t.Fatalf("interrupted delivery persisted as %+v (present %v); want it kept with the attempt uncounted", item, ok)
	}
}

func TestPostClassifiesTheReceiversAnswer(t *testing.T) {
	var gotHeader string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusAccepted)
		case "/bad":
			w.WriteHeader(http.StatusBadRequest)
		case "/slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer receiver.Close()
	classify := func(status int) Class {
		switch {
		case status >= 200 && status < 300:
			return Accept
		case status == http.StatusBadRequest:
			return Reject
		}
		return Retry
	}
	ctx := context.Background()
	header := http.Header{"X-Test": {"yes"}}
	if out := Post(ctx, Request{URL: receiver.URL + "/ok", Header: header}, classify); !out.OK() || out.Status != 202 || gotHeader != "yes" {
		t.Errorf("ok: %+v header %q", out, gotHeader)
	}
	if out := Post(ctx, Request{URL: receiver.URL + "/bad"}, classify); out.OK() || out.Retry || out.Status != 400 {
		t.Errorf("bad: %+v", out)
	}
	if out := Post(ctx, Request{URL: receiver.URL + "/down"}, classify); out.OK() || !out.Retry || out.Status != 503 {
		t.Errorf("down: %+v", out)
	}
	if out := Post(ctx, Request{URL: receiver.URL + "/slow", Timeout: 20 * time.Millisecond}, classify); out.OK() || !out.Retry {
		t.Errorf("slow: %+v", out)
	}
}

func TestBackoffShapes(t *testing.T) {
	exp := Exponential(time.Second, 5*time.Second)
	for retry, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 5 * time.Second, 40: 5 * time.Second} {
		if got := exp(retry); got != want {
			t.Errorf("Exponential retry %d = %v, want %v", retry, got, want)
		}
	}
	steps := Steps(time.Second, time.Minute)
	if steps(1) != time.Second || steps(2) != time.Minute || steps(9) != time.Minute {
		t.Errorf("Steps: %v %v %v", steps(1), steps(2), steps(9))
	}
}

func TestSubmitAttemptedMakesTheFirstAttemptBeforeReturning(t *testing.T) {
	srv := newServer(t)
	var attempts atomic.Int32
	retried := make(chan struct{})
	d := New(srv, "test deliveries", sim.NewStateStore[Item[payload]](), Handler[payload]{
		Policy: func(payload) Policy { return Policy{MaxAttempts: 2, Backoff: Steps(time.Millisecond)} },
		Attempt: func(context.Context, *Item[payload]) Outcome {
			if attempts.Add(1) == 2 {
				close(retried)
				return Delivered()
			}
			return Retryable(errors.New("busy"))
		},
	})
	d.SubmitAttempted("inline", payload{})
	if n := attempts.Load(); n != 1 {
		t.Fatalf("SubmitAttempted returned after %d attempts, want the first one made", n)
	}
	select {
	case <-retried:
	case <-time.After(10 * time.Second):
		t.Fatal("the retry never ran")
	}
}
