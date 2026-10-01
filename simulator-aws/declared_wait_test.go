package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// serveDeclaringWait serves body to h through sim.InFlightMiddleware and
// reports the wait h declared for the request.
func serveDeclaringWait(t *testing.T, h http.HandlerFunc, body any) (code int, wait time.Duration, openEnded bool) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	sim.InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(w, r)
		wait, openEnded = sim.DeclaredWait(r.Context())
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	return rec.Code, wait, openEnded
}

// A long poll blocks for its WaitTimeSeconds by design, so ReceiveMessage
// declares it; the slow-request diagnostic measures from its end.
func TestSQSReceiveMessageDeclaresItsLongPoll(t *testing.T) {
	sqsTestStore(t)
	cases := []struct {
		name  string
		attrs map[string]string
		req   map[string]any
		want  time.Duration
	}{
		{name: "request", req: map[string]any{"WaitTimeSeconds": 20}, want: 20 * time.Second},
		{name: "queue-default", attrs: map[string]string{"ReceiveMessageWaitTimeSeconds": "7"}, req: map[string]any{}, want: 7 * time.Second},
		{name: "short-poll", req: map[string]any{}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := sqsMustCreate(t, "declared-wait-"+tc.name, tc.attrs)
			if code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "ready"}); code != http.StatusOK {
				t.Fatalf("SendMessage: %d %v", code, out)
			}
			tc.req["QueueUrl"] = url
			code, wait, openEnded := serveDeclaringWait(t, handleSQSReceiveMessage, tc.req)
			if code != http.StatusOK || wait != tc.want || openEnded {
				t.Fatalf("ReceiveMessage answered %d declaring wait %s (open-ended %v), want 200 declaring %s", code, wait, openEnded, tc.want)
			}
		})
	}
}

func TestLambdaInvokeWaitLimitSpansInitAndTimeout(t *testing.T) {
	if got := lambdaInvokeWaitLimit(LambdaFunction{Timeout: 30}); got != lambdaInitPhaseLimit+30*time.Second {
		t.Fatalf("wait limit for a 30-second function = %s", got)
	}
	if got := lambdaInvokeWaitLimit(LambdaFunction{}); got != lambdaInitPhaseLimit+3*time.Second {
		t.Fatalf("wait limit for a function on the default timeout = %s, want the Init limit plus 3s", got)
	}
}

func TestLambdaDurableInvocationDeclaresItsExecutionTimeout(t *testing.T) {
	declare := func(fn LambdaFunction) (wait time.Duration, openEnded bool) {
		sim.InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lambdaDeclareDurableWait(r.Context(), fn)
			wait, openEnded = sim.DeclaredWait(r.Context())
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil).WithContext(context.Background()))
		return wait, openEnded
	}
	if wait, openEnded := declare(LambdaFunction{DurableConfig: map[string]any{"ExecutionTimeout": float64(900)}}); wait != 900*time.Second || openEnded {
		t.Fatalf("durable function with a 900-second execution timeout declared %s (open-ended %v)", wait, openEnded)
	}
	if _, openEnded := declare(LambdaFunction{DurableConfig: map[string]any{}}); !openEnded {
		t.Fatal("a durable function without an execution timeout must declare an open-ended wait")
	}
}
