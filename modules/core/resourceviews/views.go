// Package resourceviews accumulates resource statistics independently of content
// persistence and cache stores.
package resourceviews

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vernal96/go-cms-kernel/background"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

// Batch remains immutable until acknowledged. A reclaimed batch retains its ID.
type Batch struct {
	ID     string
	SiteID site.ID
	Owner  string
	Counts map[resource.ID]int64
}

// Accumulator stores increments durably across worker/process lifetimes. Claim
// leases one bounded batch; expired leases are reclaimed before new work. Ack
// must reject owners whose lease has expired or been replaced.
type Accumulator interface {
	Increment(context.Context, site.ID, resource.ID) error
	Claim(context.Context, site.ID) (*Batch, error)
	Acknowledge(context.Context, Batch) error
	Close() error
}

// Factory creates an isolated, lazy client owned by one site runtime. Open must
// validate configuration without requiring the external service to be healthy.
type Factory interface {
	Open(context.Context) (Accumulator, error)
}

type Repository interface {
	// ApplyBatch atomically deduplicates batch.ID and adds each count.
	ApplyBatch(context.Context, Batch) error
	Total(context.Context, site.ID, resource.ID) (int64, error)
}

type Definition struct {
	Code    string
	Factory Factory
}

// BindingProvider exposes the separate accumulator selection for validation.
type BindingProvider interface {
	ResourceViewAccumulator() string
}

type Config struct {
	Accumulator   string
	FlushInterval time.Duration
}

func (c Config) Normalize() (Config, error) {
	if c.Accumulator == "" {
		return Config{}, errors.New("resource views accumulator code is empty")
	}
	if c.FlushInterval == 0 {
		c.FlushInterval = 45 * time.Second
	}
	if c.FlushInterval < time.Second {
		return Config{}, errors.New("resource views flush interval must be at least one second")
	}
	return c, nil
}

// Flush retries the same immutable batch after either persistence or ack fails.
// PostgreSQL deduplication also protects against commits from an expired owner.
func Flush(ctx context.Context, accumulator Accumulator, repository Repository, siteID site.ID) error {
	for i := 0; i < 10; i++ {
		batch, err := accumulator.Claim(ctx, siteID)
		if err != nil {
			return err
		}
		if batch == nil {
			return nil
		}
		if err := repository.ApplyBatch(ctx, *batch); err != nil {
			return err
		}
		if err := accumulator.Acknowledge(ctx, *batch); err != nil {
			return err
		}
	}
	return nil
}

func Run(ctx context.Context, accumulator Accumulator, repository Repository, siteID site.ID, interval time.Duration, logger *slog.Logger) error {
	defer accumulator.Close()
	return background.RunPeriodic(ctx, interval, func(ctx context.Context) error {
		operation, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := Flush(operation, accumulator, repository, siteID); err != nil && ctx.Err() == nil && logger != nil {
			logger.WarnContext(ctx, "resource views flush failed", "event", "resource_views.flush.failed", "site_id", siteID, "error", err)
		}
		return nil
	})
}
