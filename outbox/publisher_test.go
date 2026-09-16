package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/messageid"
)

type testBus struct {
	mu        sync.Mutex
	fail      map[string]error
	published []eventbus.Message
}

func (b *testBus) Publish(_ context.Context, message eventbus.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.fail[message.Topic]; err != nil {
		return err
	}
	b.published = append(b.published, message)
	return nil
}
func (*testBus) Consume(context.Context, eventbus.Subscription, eventbus.Handler) error { return nil }

type testSource struct {
	mu                sync.Mutex
	name              string
	now               time.Time
	records           []Record
	retryDelays       []time.Duration
	cleanupCalls      int
	cleanupErrors     int
	cleanupAfterBatch func(int)
	cleanupSignal     chan int
}

func (s *testSource) Name() string { return s.name }
func (s *testSource) Claim(_ context.Context, claim Claim) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Record, 0, claim.Limit)
	for index := range s.records {
		record := &s.records[index]
		if record.PublishedAt != nil || record.AvailableAt.After(s.now) ||
			(record.LeaseUntil != nil && record.LeaseUntil.After(s.now)) {
			continue
		}
		until := s.now.Add(claim.LeaseDuration)
		record.LeaseOwner, record.LeaseUntil = claim.Owner, &until
		result = append(result, *record)
		if len(result) == claim.Limit {
			break
		}
	}
	return result, nil
}
func (s *testSource) MarkPublished(_ context.Context, id messageid.ID, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		if s.records[index].MessageID == id && s.records[index].LeaseOwner == owner {
			publishedAt := s.now
			s.records[index].PublishedAt = &publishedAt
			s.records[index].LeaseOwner = ""
			s.records[index].LeaseUntil = nil
			return nil
		}
	}
	return ErrLeaseLost
}
func (s *testSource) MarkFailed(_ context.Context, id messageid.ID, owner, failure string, retryAfter time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		if s.records[index].MessageID == id && s.records[index].LeaseOwner == owner {
			s.records[index].AttemptCount++
			s.records[index].LastError = failure
			s.records[index].AvailableAt = s.now.Add(retryAfter)
			s.records[index].LeaseOwner = ""
			s.records[index].LeaseUntil = nil
			s.retryDelays = append(s.retryDelays, retryAfter)
			return nil
		}
	}
	return ErrLeaseLost
}
func (s *testSource) CleanupPublished(_ context.Context, retention time.Duration, limit int) (int64, error) {
	s.mu.Lock()
	s.cleanupCalls++
	call := s.cleanupCalls
	if s.cleanupSignal != nil {
		select {
		case s.cleanupSignal <- call:
		default:
		}
	}
	if s.cleanupErrors > 0 {
		s.cleanupErrors--
		s.mu.Unlock()
		return 0, errors.New("cleanup failed")
	}
	cutoff := s.now.Add(-retention)
	kept := s.records[:0]
	var deleted int64
	for _, record := range s.records {
		if deleted < int64(limit) && record.PublishedAt != nil && !record.PublishedAt.After(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, record)
	}
	s.records = kept
	hook := s.cleanupAfterBatch
	s.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	return deleted, nil
}

func TestPublisherUsesSourceClockAndRelativeRetryDelay(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	source := &testSource{name: "core:test", now: now, records: []Record{
		newTestRecord(t, "failing", now), newTestRecord(t, "working", now),
	}}
	bus := &testBus{fail: map[string]error{"failing": errors.New("broker unavailable")}}
	publisher, err := NewPublisher(bus, []Source{source}, discardLogger(), PublisherConfig{
		InitialRetryDelay: time.Second, MaximumRetryDelay: 4 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher.process(context.Background())
	if source.records[0].AttemptCount != 1 || source.records[0].LastError == "" ||
		!source.records[0].AvailableAt.Equal(now.Add(time.Second)) ||
		len(source.retryDelays) != 1 || source.retryDelays[0] != time.Second {
		t.Fatalf("failed record = %#v, retry delays = %#v", source.records[0], source.retryDelays)
	}
	if source.records[1].PublishedAt == nil || !source.records[1].PublishedAt.Equal(now) ||
		len(bus.published) != 1 || bus.published[0].Topic != "working" {
		t.Fatalf("successful continuation records=%#v published=%#v", source.records, bus.published)
	}
	delete(bus.fail, "failing")
	publisher.process(context.Background())
	if source.records[0].PublishedAt != nil {
		t.Fatal("retry delay was ignored")
	}
	source.now = now.Add(time.Second)
	publisher.process(context.Background())
	if source.records[0].PublishedAt == nil || !source.records[0].PublishedAt.Equal(source.now) {
		t.Fatal("source-clock retryable record was not published")
	}
	if publisher.retryDelay(10) != 4*time.Second {
		t.Fatalf("capped retry delay = %s", publisher.retryDelay(10))
	}
}

func TestPublisherCleanupSweepDeletesMultipleBoundedBatches(t *testing.T) {
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	t.Run("drains more than one batch", func(t *testing.T) {
		source := cleanupTestSource(t, now, 25)
		publisher := newTestPublisher(t, source, PublisherConfig{
			BatchSize: 10, CleanupMaxBatches: 3, PublishedRetention: 7 * 24 * time.Hour,
		})
		publisher.cleanup(context.Background())
		if source.cleanupCalls != 3 || len(source.records) != 2 {
			t.Fatalf("cleanup calls = %d, remaining records = %d", source.cleanupCalls, len(source.records))
		}
		assertRecentAndUnpublishedRemain(t, source.records)
	})

	t.Run("overall sweep stays bounded", func(t *testing.T) {
		source := cleanupTestSource(t, now, 35)
		publisher := newTestPublisher(t, source, PublisherConfig{
			BatchSize: 10, CleanupMaxBatches: 2, PublishedRetention: 7 * 24 * time.Hour,
		})
		publisher.cleanup(context.Background())
		if source.cleanupCalls != 2 || len(source.records) != 17 {
			t.Fatalf("bounded cleanup calls = %d, remaining records = %d", source.cleanupCalls, len(source.records))
		}
	})
}

func TestPublisherCleanupRespectsCancellationBetweenBatches(t *testing.T) {
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	source := cleanupTestSource(t, now, 25)
	source.cleanupAfterBatch = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	publisher := newTestPublisher(t, source, PublisherConfig{
		BatchSize: 10, CleanupMaxBatches: 3, PublishedRetention: 7 * 24 * time.Hour,
	})
	publisher.cleanup(ctx)
	if source.cleanupCalls != 1 || len(source.records) != 17 {
		t.Fatalf("canceled cleanup calls = %d, remaining records = %d", source.cleanupCalls, len(source.records))
	}
}

func TestPublisherCleanupFailureDoesNotTerminateRun(t *testing.T) {
	now := time.Now().UTC()
	source := &testSource{
		name: "core:test", now: now, cleanupErrors: 1, cleanupSignal: make(chan int, 4),
	}
	publisher := newTestPublisher(t, source, PublisherConfig{
		PollInterval: time.Hour, CleanupInterval: time.Millisecond, CleanupMaxBatches: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()
	for {
		select {
		case call := <-source.cleanupSignal:
			if call >= 2 {
				cancel()
				goto stopped
			}
		case <-time.After(time.Second):
			t.Fatal("publisher did not run cleanup again after a source error")
		}
	}
stopped:
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop after cancellation")
	}
}

func TestPublisherConfigDefaultsAndValidation(t *testing.T) {
	config, err := NormalizePublisherConfig(PublisherConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if config.CleanupMaxBatches != 100 {
		t.Fatalf("default cleanup maximum batches = %d", config.CleanupMaxBatches)
	}
	if _, err := NormalizePublisherConfig(PublisherConfig{CleanupMaxBatches: -1}); err == nil {
		t.Fatal("negative cleanup maximum batches was accepted")
	}
}

func cleanupTestSource(t *testing.T, now time.Time, eligible int) *testSource {
	t.Helper()
	records := make([]Record, 0, eligible+2)
	old := now.Add(-8 * 24 * time.Hour)
	for index := 0; index < eligible; index++ {
		record := newTestRecord(t, "old", old)
		record.PublishedAt = &old
		records = append(records, record)
	}
	recent := newTestRecord(t, "recent", now)
	recent.PublishedAt = &now
	records = append(records, recent, newTestRecord(t, "unpublished", now))
	return &testSource{name: "core:test", now: now, records: records}
}

func assertRecentAndUnpublishedRemain(t *testing.T, records []Record) {
	t.Helper()
	if len(records) != 2 || records[0].Topic != "recent" || records[0].PublishedAt == nil ||
		records[1].Topic != "unpublished" || records[1].PublishedAt != nil {
		t.Fatalf("ineligible cleanup records = %#v", records)
	}
}

func newTestPublisher(t *testing.T, source Source, config PublisherConfig) *Publisher {
	t.Helper()
	publisher, err := NewPublisher(&testBus{fail: map[string]error{}}, []Source{source}, discardLogger(), config)
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestRecord(t *testing.T, topic string, at time.Time) Record {
	t.Helper()
	id, err := messageid.New()
	if err != nil {
		t.Fatal(err)
	}
	return Record{MessageID: id, Topic: topic, Body: []byte(topic), AvailableAt: at, CreatedAt: at}
}
