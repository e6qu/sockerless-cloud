package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// lambdaAsyncInvocationV1 is the exact row shape an earlier simulator wrote to
// lambda_async_invocations.
type lambdaAsyncInvocationV1 struct {
	ID            string
	Function      LambdaFunction
	Payload       []byte
	Qualifier     string
	RequestID     string
	StartedAt     time.Time
	NextAttemptAt time.Time
	InvokeCount   int
	Failures      int
	MaxRetries    int
	MaxAgeSeconds int
	Response      []byte
	Unhandled     bool
	Condition     string
	Configured    bool
	Destination   *lambdaDestinationConfig
}

// TestLambdaAdoptsInvocationsAnEarlierSimulatorPersisted proves an upgrade
// keeps a pending asynchronous invocation: the old row becomes a delivery
// with its attempts, age and next attempt time, and leaves the old table.
func TestLambdaAdoptsInvocationsAnEarlierSimulatorPersisted(t *testing.T) {
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	old, err := sim.NewSQLiteStore[lambdaAsyncInvocationV1](db, "lambda_async_invocations")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	next := started.Add(time.Minute)
	old.Put("req-1", lambdaAsyncInvocationV1{
		ID: "req-1", RequestID: "req-1", Qualifier: "live",
		Function: LambdaFunction{FunctionName: "fn", FunctionArn: "arn:aws:lambda:us-east-1:123456789012:function:fn"},
		Payload:  []byte(`{"n":1}`), StartedAt: started, NextAttemptAt: next,
		InvokeCount: 1, Failures: 1, MaxRetries: 2, MaxAgeSeconds: 3600,
		Unhandled: true, Condition: "Success", Configured: true,
		Destination: &lambdaDestinationConfig{OnFailure: &lambdaDestination{Destination: "arn:aws:sqs:us-east-1:123456789012:dlq"}},
	})

	into, err := sim.NewSQLiteStore[delivery.Item[LambdaAsyncInvocation]](db, "lambda_async_deliveries")
	if err != nil {
		t.Fatal(err)
	}
	lambdaAdoptLegacyInvocations(db, into)

	item, ok := into.Get("req-1")
	if !ok {
		t.Fatal("the pending invocation was dropped")
	}
	p := item.Payload
	if item.Attempts != 1 || !item.EnqueuedAt.Equal(started) || !item.NextAttemptAt.Equal(next) ||
		p.Function.FunctionName != "fn" || string(p.Payload) != `{"n":1}` || p.Qualifier != "live" ||
		p.MaxRetries != 2 || p.MaxAgeSeconds != 3600 || !p.Configured ||
		p.Destination == nil || p.Destination.OnFailure.Destination != "arn:aws:sqs:us-east-1:123456789012:dlq" {
		t.Fatalf("adopted delivery = %+v", item)
	}
	if old.Len() != 0 {
		t.Fatal("the old row stayed behind and would be adopted again")
	}
}
