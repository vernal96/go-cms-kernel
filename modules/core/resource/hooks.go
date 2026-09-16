package resource

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func ValidateMutationWidget(ctx context.Context, siteID site.ID, templateCode *template.Code, binding *widget.Binding) error {
	m := mutationFrom(ctx)
	if m == nil {
		return nil
	}
	runtime, ok := m.service.runtime(ctx, siteID)
	if !ok {
		return ErrInvalid
	}
	item := Resource{Template: templateCode, Widgets: []widget.Binding{*binding}}
	if err := validateSnapshotWidgets(runtime, &item); err != nil {
		return err
	}
	*binding = item.Widgets[0]
	return nil
}

type Operation string

const (
	OperationCreate        Operation = "create"
	OperationUpdate        Operation = "update"
	OperationMove          Operation = "move"
	OperationTransfer      Operation = "transfer"
	OperationTrash         Operation = "trash"
	OperationRestore       Operation = "restore"
	OperationDelete        Operation = "delete"
	OperationMediaCascade  Operation = "media_cascade"
	OperationRevision      Operation = "restore_revision"
	OperationWidgetCreate  Operation = "widget.create"
	OperationWidgetUpdate  Operation = "widget.update"
	OperationWidgetDelete  Operation = "widget.delete"
	OperationWidgetReorder Operation = "widget.reorder"
)

type EventState struct {
	InTrash   bool       `json:"in_trash"`
	ID        ID         `json:"id"`
	SiteID    site.ID    `json:"site_id"`
	Version   int64      `json:"version"`
	Path      *string    `json:"path,omitempty"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	Data      Snapshot   `json:"data"`
}

type Change struct {
	Operation Operation
	Actor     security.Actor
	Before    *EventState
	// Candidate metadata is read-only. Only Data may be modified.
	Candidate EventState
	Data      Snapshot
}

var (
	BeforeCreate = entityhooks.NewKey[Change]("core", "resource.before_create", entityhooks.Site)
	BeforeUpdate = entityhooks.NewKey[Change]("core", "resource.before_update", entityhooks.Site)
	AfterCreate  = entityhooks.NewKey[EventPayload]("core", EventCreated, entityhooks.Site)
	AfterUpdate  = entityhooks.NewKey[EventPayload]("core", EventUpdated, entityhooks.Site)
	AfterDelete  = entityhooks.NewKey[EventPayload]("core", EventDeleted, entityhooks.Site)
)

func StateFromResource(item Resource) EventState {
	item = Clone(item)
	return EventState{InTrash: item.DeletedAt != nil, ID: item.ID, SiteID: item.SiteID, Version: item.Version, Path: item.Path, DeletedAt: item.DeletedAt, Data: SnapshotFromResource(item)}
}
func StateFromLibraryItem(item LibraryItem) EventState {
	item = cloneLibraryItem(item)
	return EventState{InTrash: item.DeletedAt != nil, ID: item.ID, SiteID: item.SiteID, Version: item.Version, DeletedAt: item.DeletedAt, Data: SnapshotFromLibraryItem(item)}
}

type mutationKey struct{}

// mutation is operation-scoped and is never stored in a service or runtime.
// Persistence calls PrepareMutation with the transaction's actual before state.
type mutation struct {
	service   *Service
	actor     security.Actor
	operation Operation
	runtimes  map[site.ID]*site.Runtime
	releases  []func()
	before    map[ID]*EventState
	err       error
}

func mutationFrom(ctx context.Context) *mutation {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(mutationKey{}).(*mutation)
	return m
}

func (s *Service) beginMutation(ctx context.Context, actor security.Actor, operation Operation) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("resource mutation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if mutationFrom(ctx) != nil {
		return ctx, func() {}, nil
	}
	m := &mutation{service: s, actor: actor, operation: operation, runtimes: map[site.ID]*site.Runtime{}, before: map[ID]*EventState{}}
	return context.WithValue(ctx, mutationKey{}, m), func() {
		for i := len(m.releases) - 1; i >= 0; i-- {
			m.releases[i]()
		}
	}, nil
}

func (s *Service) runtime(ctx context.Context, id site.ID) (*site.Runtime, bool) {
	m := mutationFrom(ctx)
	if m == nil {
		return s.sites.RuntimeByID(id)
	}
	if m.err != nil {
		return nil, false
	}
	if runtime, ok := m.runtimes[id]; ok {
		return runtime, true
	}
	runtime, ok := s.sites.RuntimeByID(id)
	if !ok {
		return nil, false
	}
	release, err := runtime.Profile().EntityHooks().Acquire()
	if err != nil {
		m.err = err
		return nil, false
	}
	m.runtimes[id] = runtime
	m.releases = append(m.releases, release)
	return runtime, true
}

// PrepareMutation runs before handlers inside the adapter-owned transaction.
// Raw repository operations without a service invocation are maintenance writes.
func PrepareMutation(ctx context.Context, before *EventState, candidate EventState) (Snapshot, error) {
	m := mutationFrom(ctx)
	if m == nil {
		return candidate.Data, nil
	}
	if m.err != nil {
		return Snapshot{}, m.err
	}
	if before != nil {
		copy := *before
		m.before[candidate.ID] = &copy
	}
	ids := []site.ID{candidate.SiteID}
	if before != nil && before.SiteID != candidate.SiteID {
		ids = []site.ID{before.SiteID, candidate.SiteID}
	}
	data := candidate.Data
	for _, id := range ids {
		runtime, ok := m.service.runtime(ctx, id)
		if !ok {
			if m.err != nil {
				return Snapshot{}, m.err
			}
			return Snapshot{}, fmt.Errorf("resource site %d is unavailable", id)
		}
		change := Change{Operation: m.operation, Actor: m.actor, Before: cloneEventState(before), Candidate: *cloneEventState(&candidate), Data: cloneSnapshot(data)}
		key := BeforeUpdate
		if before == nil {
			key = BeforeCreate
		}
		if err := entityhooks.Before(ctx, runtime.Profile().EntityHooks(), key, &change, func(value *Change) error {
			if value.Operation != m.operation || !reflect.DeepEqual(value.Actor, m.actor) || !reflect.DeepEqual(value.Before, cloneEventState(before)) || !reflect.DeepEqual(value.Candidate, *cloneEventState(&candidate)) {
				return fmt.Errorf("%w: hook changed read-only mutation metadata", ErrInvalid)
			}
			return validateHookChanges(m.operation, data, value.Data)
		}); err != nil {
			return Snapshot{}, err
		}
		if err := validateHookChanges(m.operation, data, change.Data); err != nil {
			return Snapshot{}, err
		}
		data = change.Data
	}
	return data, nil
}

func validateHookChanges(operation Operation, original, next Snapshot) error {
	a, b := cloneSnapshot(original), cloneSnapshot(next)
	switch operation {
	case OperationCreate, OperationUpdate, OperationRevision:
		if operation != OperationRevision && !reflect.DeepEqual(a.Widgets, b.Widgets) {
			return fmt.Errorf("%w: widgets require a widget operation", ErrInvalid)
		}
		if a.StorageKind != b.StorageKind || !reflect.DeepEqual(a.LibraryID, b.LibraryID) {
			return fmt.Errorf("%w: hooks cannot change resource storage or library identity", ErrInvalid)
		}
		return nil
	case OperationMove:
		a.ParentID = b.ParentID
		a.LibraryID = b.LibraryID
		a.Sort = b.Sort
	case OperationWidgetCreate, OperationWidgetUpdate, OperationWidgetDelete, OperationWidgetReorder:
		a.Widgets = b.Widgets
	case OperationTransfer, OperationTrash, OperationRestore, OperationDelete, OperationMediaCascade:
	default:
		return fmt.Errorf("%w: unsupported hook operation %q", ErrInvalid, operation)
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("%w: hook changed fields outside %s", ErrInvalid, operation)
	}
	return nil
}

func PrepareResourceMutation(ctx context.Context, before *Resource, candidate Resource) (Resource, error) {
	var previous *EventState
	if before != nil {
		state := StateFromResource(*before)
		previous = &state
	}
	snapshot, err := PrepareMutation(ctx, previous, StateFromResource(candidate))
	if err != nil {
		return Resource{}, err
	}
	m := mutationFrom(ctx)
	if m == nil {
		return candidate, nil
	}
	// Lifecycle and transfer mutate only adapter-owned metadata. Their logical
	// fields were validated by their specific domain operation.
	if m.operation == OperationTrash || m.operation == OperationRestore || m.operation == OperationTransfer {
		return candidate, nil
	}
	result := resourceFromSnapshot(candidate, snapshot)
	// Preserve widget persistence identities; snapshots intentionally omit them.
	for i := range result.Widgets {
		if i < len(candidate.Widgets) {
			result.Widgets[i].ID = candidate.Widgets[i].ID
		}
	}
	runtime, ok := m.service.runtime(ctx, candidate.SiteID)
	if !ok {
		return Resource{}, ErrInvalid
	}
	if before != nil && before.Type != result.Type {
		currentType, exists := runtime.Profile().Registry().ResourceType(before.Type)
		if !exists || !currentType.Metadata().Capabilities.MutableType || result.Type == resourcetype.Library {
			return Resource{}, fmt.Errorf("%w: resource type is immutable", ErrInvalid)
		}
	}
	if err := m.service.ensureNoParentCycle(ctx, result); err != nil {
		return Resource{}, err
	}
	trusted := candidate.FileReferences
	historicalWidgets := result.Widgets
	if m.operation == OperationRevision {
		result.Widgets = nil
	}
	result, err = m.service.normalize(ctx, m.actor, result, runtime, nil, trusted)
	if m.operation == OperationRevision {
		result.Widgets = historicalWidgets
	}
	if err != nil {
		return Resource{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if before != nil && before.Path != nil && result.Path == nil {
		if err := m.service.ensureNoRouteDescendants(ctx, *before, runtime); err != nil {
			return Resource{}, err
		}
	}
	if err := validateSnapshotWidgets(runtime, &result); err != nil {
		return Resource{}, err
	}
	return result, nil
}

func PrepareLibraryMutation(ctx context.Context, before *EventState, candidate LibraryItem, state EventState) (LibraryItem, error) {
	snapshot, err := PrepareMutation(ctx, before, state)
	if err != nil {
		return LibraryItem{}, err
	}
	m := mutationFrom(ctx)
	if m == nil {
		return candidate, nil
	}
	if m.operation == OperationTrash || m.operation == OperationRestore || m.operation == OperationTransfer {
		return candidate, nil
	}
	result := libraryItemFromSnapshot(candidate, snapshot)
	for i := range result.Widgets {
		if i < len(candidate.Widgets) {
			result.Widgets[i].ID = candidate.Widgets[i].ID
		}
	}
	runtime, ok := m.service.runtime(ctx, candidate.SiteID)
	if !ok {
		return LibraryItem{}, ErrInvalid
	}
	library := &LibraryService{common: m.service}
	result, err = library.normalize(ctx, m.actor, result, runtime, candidate.FileReferences)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	projection := resourceProjection(result)
	if err := validateSnapshotWidgets(runtime, &projection); err != nil {
		return LibraryItem{}, err
	}
	result.Widgets = projection.Widgets
	return result, nil
}

// MutationEvent uses snapshots, not a later read by an asynchronous consumer.
func MutationEvent(ctx context.Context, name string, state EventState) (EventPayload, []entityhooks.Target, error) {
	payload := EventPayload{ResourceID: state.ID, SiteID: state.SiteID, StorageKind: state.Data.StorageKind, Version: state.Version, After: cloneEventState(&state)}
	if name == EventDeleted {
		payload.Before = payload.After
		payload.After = nil
	}
	m := mutationFrom(ctx)
	if m == nil {
		return payload, nil, nil
	}
	payload.Operation = m.operation
	payload.ActorID = m.actor.AuditUserID()
	if before := m.before[state.ID]; before != nil {
		payload.Before = cloneEventState(before)
	}
	ids := []site.ID{state.SiteID}
	if payload.Before != nil && payload.Before.SiteID != state.SiteID {
		ids = []site.ID{payload.Before.SiteID, state.SiteID}
	}
	var targets []entityhooks.Target
	for _, id := range ids {
		runtime, ok := m.service.runtime(ctx, id)
		if !ok {
			return EventPayload{}, nil, ErrInvalid
		}
		targets = append(targets, runtime.Profile().EntityHooks().Targets(name)...)
	}
	return payload, targets, nil
}

func cloneSnapshot(value Snapshot) Snapshot {
	// Reuse the domain's deep-copy implementation; retain library metadata.
	result := SnapshotFromResource(Clone(resourceFromSnapshot(Resource{}, value)))
	result.StorageKind = value.StorageKind
	result.LibraryID = cloneID(value.LibraryID)
	return result
}
func cloneEventState(value *EventState) *EventState {
	if value == nil {
		return nil
	}
	result := *value
	result.Path = cloneString(value.Path)
	result.DeletedAt = cloneTime(value.DeletedAt)
	result.Data = cloneSnapshot(value.Data)
	return &result
}
