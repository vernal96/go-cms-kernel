package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/messageid"
)

const TopicPrefix = "job."

var ErrObsolete = errors.New("job is obsolete")

type Envelope struct {
	ID            messageid.ID    `json:"id"`
	Name          string          `json:"name"`
	ScopeID       string          `json:"scope_id,omitempty"`
	SchemaVersion int             `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

func New(name string, schemaVersion int, payload any) (Envelope, error) {
	return NewScoped(name, schemaVersion, "", payload)
}

func NewScoped(name string, schemaVersion int, scopeID string, payload any) (Envelope, error) {
	id, err := messageid.New()
	if err != nil {
		return Envelope{}, fmt.Errorf("create job ID: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode job payload: %w", err)
	}
	job := Envelope{ID: id, Name: name, ScopeID: scopeID, SchemaVersion: schemaVersion, Payload: raw}
	if err := job.Validate(); err != nil {
		return Envelope{}, err
	}
	return job, nil
}

func (j Envelope) Validate() error {
	if err := j.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(j.Name) == "" || j.Name != strings.TrimSpace(j.Name) {
		return errors.New("job name is invalid")
	}
	if j.ScopeID != strings.TrimSpace(j.ScopeID) {
		return errors.New("job scope ID is invalid")
	}
	if j.SchemaVersion < 1 {
		return errors.New("job schema version is invalid")
	}
	if len(j.Payload) == 0 || !json.Valid(j.Payload) {
		return errors.New("job payload is invalid JSON")
	}
	return nil
}

func Topic(name string) string { return TopicPrefix + name }

type Handler func(context.Context, Envelope) error

type Definition struct {
	Name    string
	ScopeID string
	Handler Handler
}

// Provider is an optional module-runtime capability for application-scoped
// asynchronous job handlers.
type Provider interface {
	Jobs() []Definition
}

// NamesProvider lets a module declaration advertise stable job topics before
// any site runtime exists. Handlers remain runtime-owned and are resolved from
// the current published site runtimes for each delivery.
type NamesProvider interface {
	JobNames() []string
}

type Registry struct{ handlers map[string]Handler }

func NewRegistry() *Registry { return &Registry{handlers: make(map[string]Handler)} }

func (r *Registry) Register(name string, handler Handler) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("job handler name is empty")
	}
	if handler == nil {
		return fmt.Errorf("job handler %q is nil", name)
	}
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("job handler %q is already registered", name)
	}
	r.handlers[name] = handler
	return nil
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Handle(ctx context.Context, item Envelope) error {
	if err := item.Validate(); err != nil {
		return err
	}
	handler, exists := r.handlers[item.Name]
	if !exists {
		return fmt.Errorf("job handler %q is not registered", item.Name)
	}
	return handler(ctx, item)
}

type Dispatcher struct{ bus eventbus.Bus }

func NewDispatcher(bus eventbus.Bus) (*Dispatcher, error) {
	if bus == nil {
		return nil, errors.New("job dispatcher event bus is nil")
	}
	return &Dispatcher{bus: bus}, nil
}

func (d *Dispatcher) Dispatch(ctx context.Context, item Envelope) error {
	if err := item.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode job envelope: %w", err)
	}
	return d.bus.Publish(ctx, eventbus.Message{
		Topic: Topic(item.Name), Key: []byte(item.ID), Body: body,
		Headers: map[string][]byte{"content-type": []byte("application/json"), "x-cms-message-id": []byte(item.ID), "x-cms-job-name": []byte(item.Name)},
	})
}

type Runner struct{ registry *Registry }

func NewRunner(registry *Registry) (*Runner, error) {
	if registry == nil {
		return nil, errors.New("job registry is nil")
	}
	return &Runner{registry: registry}, nil
}

func (r *Runner) Handle(ctx context.Context, message eventbus.Message) error {
	var item Envelope
	if err := json.Unmarshal(message.Body, &item); err != nil {
		return fmt.Errorf("decode job envelope: %w", err)
	}
	if message.Topic != Topic(item.Name) {
		return errors.New("job topic and name do not match")
	}
	err := r.registry.Handle(ctx, item)
	if errors.Is(err, ErrObsolete) {
		return nil
	}
	return err
}

func (r *Runner) Run(ctx context.Context, bus eventbus.Bus, group string) error {
	if bus == nil {
		return errors.New("job runner event bus is nil")
	}
	names := r.registry.Names()
	if len(names) == 0 {
		return errors.New("job registry is empty")
	}
	topics := make([]string, len(names))
	for index, name := range names {
		topics[index] = Topic(name)
	}
	return bus.Consume(ctx, eventbus.Subscription{Topics: topics, Group: group}, r.Handle)
}
