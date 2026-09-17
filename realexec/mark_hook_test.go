package realexec

import (
	"context"
	"sync"
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
	ctx := WithMark(context.Background(), func(step string) { steps = append(steps, step) })

	_ = n.ConfigureEgressPolicy(ctx, []string{"10.0.0.0/16"}, "markhookprobe")

	if len(steps) == 0 {
		t.Fatal("the hook was never called: WithMark does not reach ConfigureEgressPolicy")
	}
	if steps[0] != "egress:begin" {
		t.Errorf("first step = %q, want %q", steps[0], "egress:begin")
	}
}

// Every task start in a VPC shares one Network. The hook used to be a field on
// it, set for the duration of one start and cleared afterwards, so a second
// start in the same VPC replaced the first one's hook and its steps landed on
// the wrong task's phase line -- or on none, once the first start cleared it.
// Carried on the context, each call reports only to its own hook.
func TestConcurrentCallsOnOneNetworkKeepTheirOwnHooks(t *testing.T) {
	n := &Network{NamespaceName: "mark-hook-shared"}
	const calls = 8
	got := make([][]string, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := WithMark(context.Background(), func(step string) { got[i] = append(got[i], step) })
			_ = n.ConfigureEgressPolicy(ctx, nil, "markhookshared")
		}(i)
	}
	wg.Wait()
	for i, steps := range got {
		if len(steps) == 0 || steps[0] != "egress:begin" {
			t.Errorf("call %d recorded %v, want its own egress:begin first", i, steps)
		}
	}
}

func TestMarkFromWithoutAHookIsANoOp(t *testing.T) {
	mark := MarkFrom(context.Background())
	if mark == nil {
		t.Fatal("MarkFrom returned nil; callers would have to guard every mark")
	}
	mark("anything")
	if got := WithMark(context.Background(), nil); got != context.Background() {
		t.Error("WithMark(nil) wrapped the context; a nil hook should leave it unchanged")
	}
}
