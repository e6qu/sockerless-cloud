package simulator

import (
	"context"
	"testing"
)

// The two halves of InitObservability are told apart by the log bridge, not by
// the shape of the struct: with no exporter endpoint the function returns a
// no-op whose Shutdown does nothing and which carries no writer, and with one
// it builds the trace, log and metric providers and hands back the zerolog →
// OpenTelemetry bridge. Asserting only "non-nil struct with a non-nil Shutdown"
// is satisfied by the no-op in both directions, so a configured endpoint that
// was quietly ignored would read as a pass.

func TestInitObservabilityNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	obs, err := InitObservability("test-service")
	if err != nil {
		t.Fatalf("InitObservability failed: %v", err)
	}
	if obs == nil || obs.Shutdown == nil {
		t.Fatal("expected non-nil Observability with Shutdown")
	}
	if obs.LogWriter != nil {
		t.Error("with no exporter endpoint there is nowhere to mirror logs to; the bridge must be absent")
	}
	if err := obs.Shutdown(context.Background()); err != nil {
		t.Errorf("no-op Shutdown should return nil, got %v", err)
	}
}

func TestInitObservabilityWithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	obs, err := InitObservability("test-service")
	if err != nil {
		t.Fatalf("InitObservability failed: %v", err)
	}
	if obs == nil || obs.Shutdown == nil {
		t.Fatal("expected non-nil Observability with Shutdown")
	}
	if obs.LogWriter == nil {
		t.Fatal("a configured exporter endpoint must produce the log bridge; a nil writer means the endpoint was ignored and the no-op was returned")
	}

	// Shutdown runs against an already-cancelled context on purpose: nothing is
	// listening on the endpoint, and a live context would spend the batch
	// processors' full flush timeout here. Its error is not asserted because a
	// cancelled flush legitimately produces one; that it returns at all is what
	// the test's own deadline holds it to.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = obs.Shutdown(ctx)
}
