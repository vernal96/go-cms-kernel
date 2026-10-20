package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type dependencyTarget struct {
	scope string
	code  Code
}

// RuntimeTarget identifies a physical store participating in one runtime's
// dependency scope.
type RuntimeTarget struct {
	Scope string
	Store Store
}

// RuntimeTargetSet collects physical targets while a site runtime is built.
// A candidate set is not active until its runtime is published.
type RuntimeTargetSet struct {
	mu      sync.Mutex
	targets map[dependencyTarget]Store
}

func NewRuntimeTargetSet() *RuntimeTargetSet {
	return &RuntimeTargetSet{targets: make(map[dependencyTarget]Store)}
}

func (s *RuntimeTargetSet) add(scope string, store Store) {
	if s == nil || scope == "" || store == nil {
		return
	}
	s.mu.Lock()
	s.targets[dependencyTarget{scope: scope, code: store.Code()}] = store
	s.mu.Unlock()
}

func (s *RuntimeTargetSet) Targets() []RuntimeTarget {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	targets := make([]RuntimeTarget, 0, len(s.targets))
	for key, store := range s.targets {
		targets = append(targets, RuntimeTarget{Scope: key.scope, Store: store})
	}
	return targets
}

// RuntimeTargetUpdate activates targets for a published site runtime.
type RuntimeTargetUpdate struct {
	Publish func()
	Abort   func()
}

// Coordinator owns static and active-runtime physical targets for logical
// dependency invalidation. Runtime targets are replaced at site publication.
type Coordinator struct {
	mu            sync.RWMutex
	targets       map[dependencyTarget]Store
	runtimeScopes map[string]map[dependencyTarget]Store
}

func NewCoordinator() *Coordinator {
	return &Coordinator{
		targets:       make(map[dependencyTarget]Store),
		runtimeScopes: make(map[string]map[dependencyTarget]Store),
	}
}

func (c *Coordinator) prepareRuntimeTargets(
	desired map[string]map[dependencyTarget]Store,
) RuntimeTargetUpdate {
	if c == nil {
		return RuntimeTargetUpdate{}
	}
	c.mu.Lock()
	previous := c.runtimeScopes
	combined := cloneRuntimeScopes(previous)
	for siteID, targets := range desired {
		current := combined[siteID]
		if current == nil {
			current = make(map[dependencyTarget]Store)
			combined[siteID] = current
		}
		for target, store := range targets {
			current[target] = store
		}
	}
	c.runtimeScopes = combined
	c.mu.Unlock()

	var once sync.Once
	return RuntimeTargetUpdate{
		Publish: func() {
			once.Do(func() {
				c.mu.Lock()
				c.runtimeScopes = cloneRuntimeScopes(desired)
				c.mu.Unlock()
			})
		},
		Abort: func() {
			once.Do(func() {
				c.mu.Lock()
				c.runtimeScopes = previous
				c.mu.Unlock()
			})
		},
	}
}

func cloneRuntimeScopes(
	scopes map[string]map[dependencyTarget]Store,
) map[string]map[dependencyTarget]Store {
	cloned := make(map[string]map[dependencyTarget]Store, len(scopes))
	for siteID, targets := range scopes {
		clonedTargets := make(map[dependencyTarget]Store, len(targets))
		for target, store := range targets {
			clonedTargets[target] = store
		}
		cloned[siteID] = clonedTargets
	}
	return cloned
}

func (c *Coordinator) targetsForScope(scope string) []Store {
	registered := make(map[dependencyTarget]Store)
	for target, store := range c.targets {
		if target.scope == scope {
			registered[target] = store
		}
	}
	for _, runtimeTargets := range c.runtimeScopes {
		for target, store := range runtimeTargets {
			if target.scope == scope {
				registered[target] = store
			}
		}
	}
	targets := make([]Store, 0, len(registered))
	for _, store := range registered {
		targets = append(targets, store)
	}
	return targets
}

func (c *Coordinator) register(scope string, store Store) {
	if c == nil || scope == "" || store == nil {
		return
	}
	c.mu.Lock()
	c.targets[dependencyTarget{scope: scope, code: store.Code()}] = store
	c.mu.Unlock()
}

func (c *Coordinator) invalidateScope(
	ctx context.Context,
	scope string,
	tags ...Tag,
) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	targets := c.targetsForScope(scope)
	return invalidateTargets(ctx, targets, scope, tags)
}

// Invalidate fans logical dependencies out to every registered site/profile
// scope. It is intended for application-owned mutation policies, not modules.
func (c *Coordinator) Invalidate(ctx context.Context, tags ...Tag) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	targets := make(map[dependencyTarget]Store, len(c.targets))
	for target, store := range c.targets {
		targets[target] = store
	}
	for _, runtimeTargets := range c.runtimeScopes {
		for target, store := range runtimeTargets {
			targets[target] = store
		}
	}
	var result []error
	seen := uniqueTags(tags)
	for target, store := range targets {
		for _, tag := range seen {
			if err := store.InvalidateTag(
				ctx,
				Tag(scopedValue(target.scope, string(tag))),
			); err != nil {
				result = append(result, fmt.Errorf(
					"invalidate cache store %q dependency %q: %w",
					target.code,
					tag,
					err,
				))
			}
		}
	}
	return errors.Join(result...)
}

func invalidateTargets(
	ctx context.Context,
	targets []Store,
	scope string,
	tags []Tag,
) error {
	var result []error
	for _, store := range targets {
		for _, tag := range uniqueTags(tags) {
			if err := store.InvalidateTag(
				ctx,
				Tag(scopedValue(scope, string(tag))),
			); err != nil {
				result = append(result, fmt.Errorf(
					"invalidate cache store %q dependency %q: %w",
					store.Code(),
					tag,
					err,
				))
			}
		}
	}
	return errors.Join(result...)
}

func uniqueTags(tags []Tag) []Tag {
	seen := make(map[Tag]struct{}, len(tags))
	result := make([]Tag, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	return result
}

var _ Invalidator = (*Coordinator)(nil)
