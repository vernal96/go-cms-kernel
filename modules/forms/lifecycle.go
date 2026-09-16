package forms

import (
	"errors"
	"sync"
)

// runtimeLifecycle is process-local because SiteRuntime publication and the
// public submit rate limiter are process-local. The read lock is held through
// result/status persistence and execution claiming so drain cannot race them.
type runtimeLifecycle struct {
	mu       sync.RWMutex
	draining bool
}

func (l *runtimeLifecycle) withActive(operation func() error) error {
	if l == nil {
		return errors.New("Forms runtime lifecycle is nil")
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.draining {
		return ErrRuntimeDraining
	}
	return operation()
}

func (l *runtimeLifecycle) beginDrain() error {
	if l == nil {
		return errors.New("Forms runtime lifecycle is nil")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining {
		return ErrRuntimeDraining
	}
	l.draining = true
	return nil
}

func (l *runtimeLifecycle) abortDrain() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.draining = false
	l.mu.Unlock()
}

type preparedRuntimeTransition struct {
	lifecycle *runtimeLifecycle
	once      sync.Once
}

func (p *preparedRuntimeTransition) Commit() {
	if p != nil {
		p.once.Do(func() {})
	}
}
func (p *preparedRuntimeTransition) Abort() {
	if p != nil {
		p.once.Do(p.lifecycle.abortDrain)
	}
}
