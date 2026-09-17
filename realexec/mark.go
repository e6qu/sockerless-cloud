package realexec

import "context"

type markKey struct{}

// WithMark returns a context whose configure calls report each finished step to
// mark. The hook rides on the context because every task start in a VPC shares
// one Network, and concurrent starts must each report to their own.
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
