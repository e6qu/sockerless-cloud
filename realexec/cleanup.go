package realexec

import (
	"context"
	"errors"
	"sync"
)

// CleanupStack runs cleanup actions in reverse creation order. Actions should
// be idempotent because callers use the same stack for rollback and normal
// teardown.
type CleanupStack struct {
	mu      sync.Mutex
	closed  bool
	actions []func(context.Context) error
}

func (s *CleanupStack) Add(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.actions = append(s.actions, fn)
}

func (s *CleanupStack) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	actions := append([]func(context.Context) error(nil), s.actions...)
	s.actions = nil
	s.mu.Unlock()

	var errs []error
	for i := len(actions) - 1; i >= 0; i-- {
		if err := actions[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *CleanupStack) Disarm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.actions = nil
}
