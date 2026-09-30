package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestEventBridgeInvocationGateSpacesInvocationsByTheRateLimit(t *testing.T) {
	var gate ebInvocationGate
	start := time.Unix(1_000_000, 0)
	for i, want := range []time.Duration{0, 250 * time.Millisecond, 500 * time.Millisecond, 750 * time.Millisecond, time.Second} {
		if got := gate.reserve(start, 4).Sub(start); got != want {
			t.Fatalf("invocation %d goes out after %s, want %s at four per second", i, got, want)
		}
	}
	later := start.Add(10 * time.Second)
	if got := gate.reserve(later, 4); !got.Equal(later) {
		t.Fatalf("an invocation after an idle spell waits until %s, want at once", got)
	}
}

func TestEventBridgeConnectionCredentialsMoveIntoTheirSecret(t *testing.T) {
	connections, secrets := ebConnections, smSecrets
	t.Cleanup(func() { ebConnections, smSecrets = connections, secrets })
	ebConnections = sim.MakeStore[EBConnection](nil, "test_eventbridge_connections")
	smSecrets = sim.MakeStore[SMSecret](nil, "test_sm_secrets")
	params := json.RawMessage(`{"ApiKeyAuthParameters":{"ApiKeyName":"x-api-key","ApiKeyValue":"held-by-the-connection"}}`)
	ebConnections.Put("held", EBConnection{Name: "held", SecretArn: "arn:aws:secretsmanager:us-east-1:000000000000:secret:gone", AuthParameters: params})

	ebMoveConnectionSecrets()

	moved, _ := ebConnections.Get("held")
	if len(moved.AuthParameters) != 0 {
		t.Fatal("the connection still holds its credentials itself")
	}
	stored, err := ebConnectionParameters(moved)
	if err != nil || string(stored) != string(params) {
		t.Fatalf("secret %s holds %s, %v; want the connection's parameters", moved.SecretArn, stored, err)
	}
}
