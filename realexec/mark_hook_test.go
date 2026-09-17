package realexec

import (
	"context"
	"testing"
)

// Instrumentation that never fires is worse than none: it reads like evidence
// and reports nothing. This asserts the hook is reached on entry, before any
// step that needs a namespace or the ip binary, so it is meaningful on any
// machine -- an earlier version of this test asserted after EnsureEgress and
// went red simply because macOS has no ip(8).
func TestConfigureEgressPolicyMarksOnEntry(t *testing.T) {
	var steps []string
	n := &Network{NamespaceName: "mark-hook-probe"}
	n.Mark = func(step string) { steps = append(steps, step) }

	_ = n.ConfigureEgressPolicy(context.Background(), []string{"10.0.0.0/16"}, "markhookprobe")

	if len(steps) == 0 {
		t.Fatal("Mark was never called: the hook does not reach ConfigureEgressPolicy")
	}
	if steps[0] != "egress:begin" {
		t.Errorf("first step = %q, want %q", steps[0], "egress:begin")
	}
}

func TestConfigureEgressPolicyWithoutAMarkHookDoesNotPanic(t *testing.T) {
	n := &Network{NamespaceName: "mark-hook-absent"}
	_ = n.ConfigureEgressPolicy(context.Background(), nil, "markhookabsent")
}
