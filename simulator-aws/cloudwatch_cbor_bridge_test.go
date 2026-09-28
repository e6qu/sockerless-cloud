package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every operation the vendored Amazon CloudWatch model declares is served on
// rpc-v2-cbor by an implementation, hand-written or through its JSON handler.
// A newer model's operation fails here until it is implemented.
func TestCloudWatchServesEveryModelOperationOverCBOR(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SIM_DNS_PORT", "0")
	cwCBORUnimplemented = nil
	srv, _, _, err := buildSimulator(sim.Config{Provider: "aws", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	if len(cwCBORUnimplemented) > 0 {
		t.Fatalf("model operations with no implementation: %v", cwCBORUnimplemented)
	}
	for op := range cwCBOROperations {
		if !cwCBORRouted(srv, op) {
			t.Errorf("%s has no rpc-v2-cbor route", op)
		}
	}
}
