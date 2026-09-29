package main

import (
	"database/sql"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// lambdaLegacyAsyncInvocation is the row an earlier simulator persisted in
// lambda_async_invocations for an accepted asynchronous invocation.
type lambdaLegacyAsyncInvocation struct {
	ID            string
	Function      LambdaFunction
	Payload       []byte
	Qualifier     string
	RequestID     string
	StartedAt     time.Time
	NextAttemptAt time.Time
	InvokeCount   int
	MaxRetries    int
	MaxAgeSeconds int
	Response      []byte
	Unhandled     bool
	Configured    bool
	Destination   *lambdaDestinationConfig
}

// lambdaAdoptLegacyInvocations moves the invocations an earlier simulator
// accepted into the delivery store, keeping their attempts, next attempt time
// and age, so an upgrade delivers them as a restart would have.
func lambdaAdoptLegacyInvocations(db *sql.DB, into sim.Store[delivery.Item[LambdaAsyncInvocation]]) {
	legacy := sim.MakeStore[lambdaLegacyAsyncInvocation](db, "lambda_async_invocations")
	for _, row := range legacy.ListPrefix("") {
		old := row.Item
		into.Put(row.ID, delivery.Item[LambdaAsyncInvocation]{
			ID: row.ID,
			Payload: LambdaAsyncInvocation{
				Function:      old.Function,
				Payload:       old.Payload,
				Qualifier:     old.Qualifier,
				RequestID:     old.RequestID,
				MaxRetries:    old.MaxRetries,
				MaxAgeSeconds: old.MaxAgeSeconds,
				Response:      old.Response,
				Unhandled:     old.Unhandled,
				Configured:    old.Configured,
				Destination:   old.Destination,
			},
			EnqueuedAt:    old.StartedAt,
			NextAttemptAt: old.NextAttemptAt,
			Attempts:      old.InvokeCount,
		})
		legacy.Delete(row.ID)
	}
}
