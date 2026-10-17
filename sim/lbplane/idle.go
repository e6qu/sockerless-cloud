package lbplane

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ErrIdleTimeout marks a forward that no byte moved through, in either
// direction, for the upstream's IdleTimeout. A SendError carrying it means the
// target never began its answer; returned bare, the answer was already partly
// written to the client.
var ErrIdleTimeout = errors.New("connection idle timeout elapsed")

// idleWatch calls expire once its idle period passes with no byte read from
// either peer, and restarts that wait whenever one is. A nil watch bounds
// nothing.
type idleWatch struct {
	mu       sync.Mutex
	timer    *time.Timer
	idle     time.Duration
	activity func()
}

func newIdleWatch(idle time.Duration, activity, expire func()) *idleWatch {
	if idle <= 0 {
		return nil
	}
	w := &idleWatch{idle: idle, activity: activity}
	w.timer = time.AfterFunc(idle, expire)
	return w
}

func (w *idleWatch) touch() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.timer.Reset(w.idle)
	w.mu.Unlock()
	if w.activity != nil {
		w.activity()
	}
}

func (w *idleWatch) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.timer.Stop()
	w.mu.Unlock()
}

// watchExchange bounds ctx by the idle timeout: the watch cancels it with
// ErrIdleTimeout as its cause.
func watchExchange(ctx context.Context, idle time.Duration, activity func()) (context.Context, *idleWatch, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	return ctx, newIdleWatch(idle, activity, func() { cancel(ErrIdleTimeout) }), cancel
}

func (w *idleWatch) reader(r io.Reader) io.Reader {
	if w == nil {
		return r
	}
	return idleReader{r: r, watch: w}
}

type idleReader struct {
	r     io.Reader
	watch *idleWatch
}

func (r idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.watch.touch()
	}
	return n, err
}

type idleReadCloser struct {
	io.Reader
	io.Closer
}
