// Package entityhooks provides typed entity extension points without depending
// on the entities that declare them. Registries are built once and then sealed.
package entityhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/vernal96/go-cms-kernel/domainevent"
)

type Scope string

const (
	Application Scope = "application"
	Site        Scope = "site"
)

var (
	ErrSealed      = errors.New("entity hook registry is sealed")
	ErrBusy        = errors.New("entity hook registry has active mutations or pending deliveries")
	ErrUnavailable = errors.New("entity hook handler is unavailable")
)

// Key is declared by the entity owner. T is an explicit domain-owned DTO.
// The private type token prevents registrations with conflicting payload types.
type Key[T any] struct {
	owner, name string
	scope       Scope
}

func NewKey[T any](owner, name string, scope Scope) Key[T] {
	return Key[T]{owner: owner, name: name, scope: scope}
}
func (k Key[T]) Name() string { return k.name }

type Target struct {
	Module  string `json:"module"`
	Handler string `json:"handler"`
	Scope   Scope  `json:"scope"`
	ScopeID string `json:"scope_id"`
}

type Delivery struct {
	Source string
	Event  domainevent.Envelope
	Target Target
}

// IdempotencyKey is stable across attempts, process restarts and broker delivery.
func (d Delivery) IdempotencyKey() string {
	raw, _ := json.Marshal([]string{d.Source, string(d.Event.ID), d.Target.Module, d.Target.Handler, string(d.Target.Scope), d.Target.ScopeID})
	return string(raw)
}

type entry struct {
	owner  string
	name   string
	token  any // only typed nil pointers; never domain services or untyped payloads
	target Target
	before any // asserted to func(context.Context, *T) error at the typed boundary
	after  func(context.Context, Delivery) error
}

type Registry struct {
	mu               sync.Mutex
	scope            Scope
	id               string
	sealed, draining bool
	active           int
	entries          []entry
}

func NewRegistry(scope Scope, id string) *Registry { return &Registry{scope: scope, id: id} }

// Registrar is a module-bound build capability, not a service locator.
type Registrar struct {
	registry *Registry
	module   string
	allowed  map[string]bool
}

// Dispatcher is the capability retained by a module-owned service. It can
// execute hooks after sealing but cannot register handlers for another module.
type Dispatcher struct{ value *Registry }
type DispatchScope interface{ hookRegistry() *Registry }

func (r *Registry) hookRegistry() *Registry       { return r }
func (d Dispatcher) hookRegistry() *Registry      { return d.value }
func (r Registrar) Dispatcher() Dispatcher        { return Dispatcher{value: r.registry} }
func (d Dispatcher) Acquire() (func(), error)     { return d.value.Acquire() }
func (d Dispatcher) Targets(name string) []Target { return d.value.Targets(name) }

func (r *Registry) ForModule(module string, dependencies []string) Registrar {
	allowed := map[string]bool{module: true}
	for _, dependency := range dependencies {
		allowed[dependency] = true
	}
	return Registrar{registry: r, module: module, allowed: allowed}
}

func add[T any](r Registrar, key Key[T], code string, before any, after func(context.Context, Delivery) error) error {
	if r.registry == nil || strings.TrimSpace(r.module) == "" || strings.TrimSpace(code) == "" || code != strings.TrimSpace(code) {
		return errors.New("invalid entity hook registration")
	}
	if key.owner == "" || key.name == "" || !r.allowed[key.owner] {
		return fmt.Errorf("entity hook %q owner %q is not a declared dependency of %q", key.name, key.owner, r.module)
	}
	registry := r.registry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.sealed {
		return ErrSealed
	}
	if key.scope != registry.scope {
		return fmt.Errorf("entity hook %q requires %s scope", key.name, key.scope)
	}
	if registry.scope != Application && registry.scope != Site || registry.scope == Application && registry.id != "" || registry.scope == Site && registry.id == "" {
		return errors.New("invalid entity hook registry scope")
	}
	for _, item := range registry.entries {
		if item.target.Module == r.module && item.target.Handler == code {
			return fmt.Errorf("duplicate entity hook %s/%s", r.module, code)
		}
		if item.name == key.name && (item.owner != key.owner || item.token != any((*T)(nil)) || (item.before != nil) != (before != nil)) {
			return fmt.Errorf("conflicting entity hook definition %q", key.name)
		}
	}
	registry.entries = append(registry.entries, entry{owner: key.owner, name: key.name, token: (*T)(nil), target: Target{Module: r.module, Handler: code, Scope: registry.scope, ScopeID: registry.id}, before: before, after: after})
	return nil
}

func RegisterBefore[T any](r Registrar, key Key[T], code string, handler func(context.Context, *T) error) error {
	if handler == nil {
		return errors.New("entity before handler is nil")
	}
	return add(r, key, code, handler, nil)
}

func RegisterAfter[T any](r Registrar, key Key[T], code string, handler func(context.Context, Delivery, T) error) error {
	if handler == nil {
		return errors.New("entity after handler is nil")
	}
	return add(r, key, code, nil, func(ctx context.Context, delivery Delivery) error {
		var payload T
		if err := json.Unmarshal(delivery.Event.Payload, &payload); err != nil {
			return fmt.Errorf("decode %s: %w", key.name, err)
		}
		return handler(ctx, delivery, payload)
	})
}

func (r *Registry) Seal() error {
	if r == nil {
		return errors.New("entity hook registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return ErrSealed
	}
	r.sealed = true
	return nil
}

// Acquire pins this immutable registry for the whole mutation, including commit.
func (r *Registry) Acquire() (func(), error) {
	if r == nil {
		return nil, errors.New("entity hook registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sealed {
		return nil, errors.New("entity hook registry is not sealed")
	}
	if r.draining {
		return nil, ErrBusy
	}
	r.active++
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); r.active--; r.mu.Unlock() }) }, nil
}

// Drain excludes new mutations while a runtime transition checks durable work.
// On failure the caller must Abort; on successful replacement leave it drained.
func (r *Registry) Drain() (abort func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining || r.active != 0 {
		return nil, ErrBusy
	}
	r.draining = true
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); r.draining = false; r.mu.Unlock() }) }, nil
}

func Before[T any](ctx context.Context, scope DispatchScope, key Key[T], value *T, guards ...func(*T) error) error {
	if scope == nil {
		return errors.New("entity hook scope is nil")
	}
	r := scope.hookRegistry()
	if ctx == nil || value == nil || r == nil {
		return errors.New("invalid entity before invocation")
	}
	if key.scope != r.scope {
		return errors.New("entity hook invocation has wrong scope")
	}
	if !r.sealed {
		return errors.New("entity hook registry is not sealed")
	}
	for _, item := range r.entries {
		if item.name != key.name {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.owner != key.owner {
			return errors.New("entity hook invocation has wrong owner")
		}
		handler, ok := item.before.(func(context.Context, *T) error)
		if !ok {
			return fmt.Errorf("invalid payload for entity hook %q", key.name)
		}
		if err := handler(ctx, value); err != nil {
			return fmt.Errorf("entity hook %s/%s: %w", item.target.Module, item.target.Handler, err)
		}
		for _, guard := range guards {
			if err := guard(value); err != nil {
				return fmt.Errorf("entity hook %s/%s: %w", item.target.Module, item.target.Handler, err)
			}
		}
	}
	return nil
}

func (r *Registry) Targets(name string) []Target {
	var result []Target
	for _, item := range r.entries {
		if item.name == name && item.after != nil {
			result = append(result, item.target)
		}
	}
	return result
}

func (r *Registry) Has(name string, target Target) bool {
	if r == nil {
		return false
	}
	for _, item := range r.entries {
		if item.name == name && item.target == target && item.after != nil {
			return true
		}
	}
	return false
}

func (r *Registry) Handle(ctx context.Context, delivery Delivery) error {
	for _, item := range r.entries {
		if item.name == delivery.Event.Name && item.target == delivery.Target && item.after != nil {
			return item.after(ctx, delivery)
		}
	}
	return fmt.Errorf("%w: %s/%s", ErrUnavailable, delivery.Target.Module, delivery.Target.Handler)
}

func (r *Registry) Names() []string {
	seen := map[string]bool{}
	for _, item := range r.entries {
		if item.after != nil {
			seen[item.name] = true
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// ApplicationProvider registers global hooks once, in declared dependency order.
type ApplicationProvider interface {
	RegisterEntityHooks(context.Context, Registrar) error
}

// NamesProvider declares produced event topics even when a profile has no sites.
type NamesProvider interface{ EntityHookEventNames() []string }
