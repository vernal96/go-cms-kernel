package resourceviews

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type retryAccumulator struct {
	batch      *Batch
	failure    error
	ackFailure bool
	closed     bool
	claims     int
}

func (a *retryAccumulator) Increment(context.Context, site.ID, resource.ID) error { return a.failure }
func (a *retryAccumulator) Claim(context.Context, site.ID) (*Batch, error) {
	a.claims++
	return a.batch, a.failure
}
func (a *retryAccumulator) Acknowledge(context.Context, Batch) error {
	if a.ackFailure {
		a.ackFailure = false
		return errors.New("ack failed")
	}
	a.batch = nil
	return nil
}
func (a *retryAccumulator) Close() error { a.closed = true; return nil }

type dedupRepository struct {
	seen    map[string]bool
	total   int64
	failure bool
}

func (r *dedupRepository) ApplyBatch(_ context.Context, batch Batch) error {
	if r.failure {
		r.failure = false
		return errors.New("database failed")
	}
	if !r.seen[batch.ID] {
		r.seen[batch.ID] = true
		for _, value := range batch.Counts {
			r.total += value
		}
	}
	return nil
}
func (r *dedupRepository) Total(context.Context, site.ID, resource.ID) (int64, error) {
	return r.total, nil
}

func TestFlushRetriesDatabaseAndCommitBeforeAcknowledgement(t *testing.T) {
	for _, failure := range []string{"database", "ack"} {
		t.Run(failure, func(t *testing.T) {
			accumulator := &retryAccumulator{batch: &Batch{ID: "stable", SiteID: 1, Owner: "worker", Counts: map[resource.ID]int64{3: 7}}, ackFailure: failure == "ack"}
			repository := &dedupRepository{seen: make(map[string]bool), failure: failure == "database"}
			if err := Flush(context.Background(), accumulator, repository, 1); err == nil {
				t.Fatal("expected initial failure")
			}
			if accumulator.batch == nil {
				t.Fatal("lost unacknowledged batch")
			}
			if err := Flush(context.Background(), accumulator, repository, 1); err != nil {
				t.Fatal(err)
			}
			if repository.total != 7 || accumulator.batch != nil {
				t.Fatalf("total=%d remaining=%v", repository.total, accumulator.batch)
			}
		})
	}
}

func TestRunContinuesAfterStoreFailureAndClosesOnShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	accumulator := &retryAccumulator{failure: errors.New("store unavailable")}
	repository := &dedupRepository{seen: make(map[string]bool)}
	if err := Run(ctx, accumulator, repository, 1, 5*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if accumulator.claims < 2 || !accumulator.closed {
		t.Fatalf("claims=%d closed=%t", accumulator.claims, accumulator.closed)
	}
}
