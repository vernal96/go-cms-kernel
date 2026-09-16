package background

import "context"

type Task struct {
	Name string
	Run  func(context.Context) error
}

// Provider is an optional module-runtime capability for site-scoped lifecycle
// work. App starts every task for every current site runtime and cancels stale
// tasks before replacing or removing that runtime.
type Provider interface {
	BackgroundTasks() []Task
}
