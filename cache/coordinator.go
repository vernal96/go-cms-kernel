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

// Coordinator owns the participating physical targets for logical dependency
// invalidation. Registrations are stable by dependency scope and store code, so
// rebuilding the same site runtime is idempotent.
type Coordinator struct {
	mu      sync.RWMutex
	targets map[dependencyTarget]Store
}

func NewCoordinator() *Coordinator {
	return &Coordinator{targets: make(map[dependencyTarget]Store)}
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
	targets := make([]Store, 0, len(c.targets))
	for target, store := range c.targets {
		if target.scope == scope {
			targets = append(targets, store)
		}
	}
	c.mu.RUnlock()
	return invalidateTargets(ctx, targets, scope, tags)
}

// Invalidate fans logical dependencies out to every registered site/profile
// scope. It is intended for application-owned mutation policies, not modules.
func (c *Coordinator) Invalidate(ctx context.Context, tags ...Tag) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	targets := make(map[dependencyTarget]Store, len(c.targets))
	for target, store := range c.targets {
		targets[target] = store
	}
	c.mu.RUnlock()

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
