package realexec

import "context"

type markKey struct{}

// WithMark returns a context whose configure calls report each finished step to
// mark, so a task start's phase line can say where a slow phase spends its time.
// The hook travels with the call rather than on the per-VPC Network, which
// every task start in that VPC shares: a field there let two concurrent starts
// overwrite, and then clear, each other's hook.
func WithMark(ctx context.Context, mark func(step string)) context.Context {
	if mark == nil {
		return ctx
	}
	return context.WithValue(ctx, markKey{}, mark)
}

// MarkFrom returns the hook WithMark attached to ctx, or a no-op when there is
// none.
func MarkFrom(ctx context.Context) func(step string) {
	if mark, ok := ctx.Value(markKey{}).(func(string)); ok {
		return mark
	}
	return func(string) {}
}
