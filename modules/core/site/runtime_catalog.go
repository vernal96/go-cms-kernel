package site

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
)

type runtimeSnapshot struct {
	byDomain map[string]*Runtime
	byID     map[ID]*Runtime
}

type RuntimePlan struct {
	current []*Runtime
	next    []*Runtime
}

func (p RuntimePlan) Current() []*Runtime {
	return append([]*Runtime(nil), p.current...)
}

func (p RuntimePlan) Next() []*Runtime {
	return append([]*Runtime(nil), p.next...)
}

type RuntimePreparation struct {
	Publish func()
	Abort   func()
}

type RuntimePreparer func(
	context.Context,
	RuntimePlan,
) (RuntimePreparation, error)

type Catalog struct {
	repository Repository
	profiles   ProfileResolver
	access     Access
	files      file.Service
	preparers  []RuntimePreparer

	snapshot   atomic.Pointer[runtimeSnapshot]
	mutationMu sync.Mutex
}

func (c *Catalog) AddRuntimePreparer(
	ctx context.Context,
	preparer RuntimePreparer,
) error {
	if c == nil {
		return errors.New("site catalog is nil")
	}
	if ctx == nil {
		return errors.New("site runtime prepare context is nil")
	}
	if preparer == nil {
		return errors.New("site runtime preparer is nil")
	}
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return errors.New("site runtime snapshot is nil")
	}
	runtimes := snapshotRuntimes(snapshot)
	preparation, err := preparer(ctx, RuntimePlan{
		current: runtimes,
		next:    runtimes,
	})
	if err != nil {
		return fmt.Errorf("prepare current site runtimes: %w", err)
	}
	applyRuntimePreparations([]RuntimePreparation{preparation})
	c.preparers = append(c.preparers, preparer)
	return nil
}

func (c *Catalog) prepareRuntimePlan(
	ctx context.Context,
	current *runtimeSnapshot,
	next *runtimeSnapshot,
) ([]RuntimePreparation, error) {
	plan := RuntimePlan{
		current: snapshotRuntimes(current),
		next:    snapshotRuntimes(next),
	}
	result, err := prepareRuntimeTransitions(ctx, plan)
	if err != nil {
		return nil, err
	}
	for _, preparer := range c.preparers {
		preparation, err := preparer(ctx, plan)
		if err != nil {
			abortRuntimePreparations(result)
			return nil, err
		}
		result = append(result, preparation)
	}
	return result, nil
}

func prepareRuntimeTransitions(
	ctx context.Context,
	plan RuntimePlan,
) ([]RuntimePreparation, error) {
	nextByID := make(map[ID]*Runtime, len(plan.next))
	for _, runtime := range plan.next {
		if runtime != nil {
			nextByID[runtime.site.ID] = runtime
		}
	}
	result := make([]RuntimePreparation, 0)
	for _, current := range plan.current {
		if current == nil || current.profileRuntime == nil {
			abortRuntimePreparations(result)
			return nil, errors.New("current site runtime is invalid")
		}
		next, exists := nextByID[current.site.ID]
		transition := kernel.RuntimeTransition{
			ScopeID:     fmt.Sprint(current.site.ID),
			FromProfile: current.site.ProfileCode,
		}
		switch {
		case !exists:
			transition.Reason = kernel.RuntimeTransitionSiteDelete
		case next.site.ProfileCode != current.site.ProfileCode:
			transition.Reason = kernel.RuntimeTransitionProfileChange
			transition.ToProfile = next.site.ProfileCode
		default:
			continue
		}
		for _, moduleRuntime := range current.profileRuntime.Modules() {
			participant, ok := moduleRuntime.(kernel.RuntimeTransitionParticipant)
			if !ok {
				continue
			}
			prepared, err := participant.PrepareRuntimeTransition(ctx, transition)
			if err != nil {
				abortRuntimePreparations(result)
				return nil, fmt.Errorf(
					"prepare module %q for %s: %w",
					moduleRuntime.ModuleCode(), transition.Reason, err,
				)
			}
			if prepared == nil {
				abortRuntimePreparations(result)
				return nil, fmt.Errorf(
					"module %q returned a nil prepared runtime transition",
					moduleRuntime.ModuleCode(),
				)
			}
			result = append(result, RuntimePreparation{
				Publish: prepared.Commit,
				Abort:   prepared.Abort,
			})
		}
	}
	return result, nil
}

func (c *Catalog) publishRuntimeSnapshot(
	next *runtimeSnapshot,
	preparations []RuntimePreparation,
) {
	c.snapshot.Store(next)
	applyRuntimePreparations(preparations)
}

func applyRuntimePreparations(preparations []RuntimePreparation) {
	for _, preparation := range preparations {
		if preparation.Publish != nil {
			preparation.Publish()
		}
	}
}

func abortRuntimePreparations(preparations []RuntimePreparation) {
	for index := len(preparations) - 1; index >= 0; index-- {
		if preparations[index].Abort != nil {
			preparations[index].Abort()
		}
	}
}
