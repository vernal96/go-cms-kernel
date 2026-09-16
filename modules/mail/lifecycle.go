package mail

import (
	"errors"
	"sync"
)

// runtimeLifecycle is process-local because SiteRuntime publication is
// process-local. Holding the read lock across a queue/claim mutation closes the
// gap between entering drain and inspecting active database messages.
type runtimeLifecycle struct {
	mu       sync.RWMutex
	draining bool
}

func (l *runtimeLifecycle) withActive(operation func() error) error {
	if l == nil {
		return errors.New("mail runtime lifecycle is nil")
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
		return errors.New("mail runtime lifecycle is nil")
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
	if p == nil {
		return
	}
	p.once.Do(func() {})
}

func (p *preparedRuntimeTransition) Abort() {
	if p == nil {
		return
	}
	p.once.Do(p.lifecycle.abortDrain)
}
