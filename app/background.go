package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vernal96/go-cms-kernel/background"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type scopedBackgroundTaskKey struct {
	scope string
	name  string
}

type scopedBackgroundTask struct {
	key   scopedBackgroundTaskKey
	owner *site.Runtime
	run   func(context.Context) error
}

type backgroundTaskUpdate struct {
	tasks map[scopedBackgroundTaskKey]scopedBackgroundTask
	done  chan struct{}
}

type runningBackgroundTask struct {
	owner  *site.Runtime
	cancel context.CancelFunc
	done   chan struct{}
}

type runtimeBackgroundTasks struct {
	ctx     context.Context
	logger  *slog.Logger
	updates chan backgroundTaskUpdate
}

func newRuntimeBackgroundTasks(ctx context.Context, logger *slog.Logger) *runtimeBackgroundTasks {
	return &runtimeBackgroundTasks{ctx: ctx, logger: logger, updates: make(chan backgroundTaskUpdate)}
}

func (m *runtimeBackgroundTasks) prepare(_ context.Context, plan site.RuntimePlan) (site.RuntimePreparation, error) {
	tasks, err := collectRuntimeBackgroundTasks(plan.Next())
	if err != nil {
		return site.RuntimePreparation{}, err
	}
	return site.RuntimePreparation{Publish: func() { m.publish(tasks) }}, nil
}

func collectRuntimeBackgroundTasks(runtimes []*site.Runtime) (map[scopedBackgroundTaskKey]scopedBackgroundTask, error) {
	result := make(map[scopedBackgroundTaskKey]scopedBackgroundTask)
	for _, runtime := range runtimes {
		if runtime == nil {
			return nil, errors.New("site runtime is nil")
		}
		scope := fmt.Sprint(runtime.Site().ID)
		for _, moduleRuntime := range runtime.Profile().Modules() {
			provider, ok := moduleRuntime.(background.Provider)
			if !ok {
				continue
			}
			for _, task := range provider.BackgroundTasks() {
				if task.Name == "" || task.Run == nil {
					return nil, fmt.Errorf("site %s background task is invalid", scope)
				}
				key := scopedBackgroundTaskKey{scope: scope, name: task.Name}
				if _, duplicate := result[key]; duplicate {
					return nil, fmt.Errorf("site %s background task %q is duplicated", scope, task.Name)
				}
				result[key] = scopedBackgroundTask{key: key, owner: runtime, run: task.Run}
			}
		}
	}
	return result, nil
}

func (m *runtimeBackgroundTasks) publish(tasks map[scopedBackgroundTaskKey]scopedBackgroundTask) {
	update := backgroundTaskUpdate{tasks: tasks, done: make(chan struct{})}
	select {
	case m.updates <- update:
	case <-m.ctx.Done():
		return
	}
	select {
	case <-update.done:
	case <-m.ctx.Done():
	}
}

func (m *runtimeBackgroundTasks) run() {
	active := make(map[scopedBackgroundTaskKey]*runningBackgroundTask)
	var workers sync.WaitGroup
	for {
		select {
		case <-m.ctx.Done():
			for _, task := range active {
				task.cancel()
			}
			workers.Wait()
			return
		case update := <-m.updates:
			m.apply(update.tasks, active, &workers)
			close(update.done)
		}
	}
}

func (m *runtimeBackgroundTasks) apply(desired map[scopedBackgroundTaskKey]scopedBackgroundTask, active map[scopedBackgroundTaskKey]*runningBackgroundTask, workers *sync.WaitGroup) {
	predecessors := make(map[scopedBackgroundTaskKey]<-chan struct{})
	for key, current := range active {
		next, exists := desired[key]
		if exists && next.owner == current.owner {
			continue
		}
		current.cancel()
		predecessors[key] = current.done
		delete(active, key)
	}
	for key, task := range desired {
		if _, exists := active[key]; exists {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		running := &runningBackgroundTask{owner: task.owner, cancel: cancel, done: make(chan struct{})}
		active[key] = running
		workers.Add(1)
		go func(previous <-chan struct{}) {
			defer workers.Done()
			defer close(running.done)
			if previous != nil {
				select {
				case <-previous:
				case <-ctx.Done():
					return
				}
			}
			m.runTask(ctx, task)
		}(predecessors[key])
	}
}

func (m *runtimeBackgroundTasks) runTask(ctx context.Context, task scopedBackgroundTask) {
	for ctx.Err() == nil {
		err := task.run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && m.logger != nil {
			m.logger.Error("background task exited", slog.String("event", "background.task.failed"), slog.String("scope", task.key.scope), slog.String("task", task.key.name), slog.Any("error", err))
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
