package postgres

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/messageid"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/outbox"
)

func TestPostgresOutboxClaimRetryPublishAndCleanup(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	if _, err := connector.Pool().Exec(ctx, `TRUNCATE core.outbox_messages;`); err != nil {
		t.Fatal(err)
	}
	source := database.OutboxSources()[0]
	firstID := insertOutboxTestRecord(t, ctx, connector.Pool(), "test.first")
	clockSkewID := insertOutboxTestRecord(t, ctx, connector.Pool(), "test.future")
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.outbox_messages SET available_at=clock_timestamp()+interval '1 hour' WHERE message_id=$1;`, clockSkewID); err != nil {
		t.Fatal(err)
	}
	var defaultsMatch, defaultsUseDatabaseClock bool
	if err := connector.Pool().QueryRow(ctx, `
SELECT created_at=available_at,
       created_at BETWEEN clock_timestamp()-interval '5 seconds' AND clock_timestamp()+interval '1 second'
FROM core.outbox_messages WHERE message_id=$1;`, firstID).Scan(&defaultsMatch, &defaultsUseDatabaseClock); err != nil {
		t.Fatal(err)
	}
	if !defaultsMatch || !defaultsUseDatabaseClock {
		t.Fatalf("initial operational timestamps did not use PostgreSQL defaults: match=%t database_clock=%t", defaultsMatch, defaultsUseDatabaseClock)
	}

	start := make(chan struct{})
	type result struct {
		owner   string
		records []outbox.Record
		err     error
	}
	results := make(chan result, 2)
	for _, owner := range []string{"publisher-a", "publisher-b"} {
		go func(owner string) {
			<-start
			records, err := source.Claim(ctx, outbox.Claim{Owner: owner, LeaseDuration: time.Minute, Limit: 1})
			results <- result{owner: owner, records: records, err: err}
		}(owner)
	}
	close(start)
	var winner string
	for range 2 {
		claimed := <-results
		if claimed.err != nil {
			t.Fatal(claimed.err)
		}
		if len(claimed.records) == 1 {
			winner = claimed.owner
		}
	}
	if winner == "" {
		t.Fatal("concurrent claimers did not claim the available message")
	}
	var leaseUsesDatabaseClock bool
	if err := connector.Pool().QueryRow(ctx, `
SELECT lease_until BETWEEN clock_timestamp()+interval '50 seconds' AND clock_timestamp()+interval '61 seconds'
FROM core.outbox_messages WHERE message_id=$1;`, firstID).Scan(&leaseUsesDatabaseClock); err != nil {
		t.Fatal(err)
	}
	if !leaseUsesDatabaseClock {
		t.Fatal("lease expiration was not based on PostgreSQL current time")
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.outbox_messages SET lease_until=clock_timestamp()-interval '1 microsecond' WHERE message_id=$1;`, firstID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := source.Claim(ctx, outbox.Claim{Owner: "publisher-c", LeaseDuration: time.Minute, Limit: 1})
	if err != nil || len(reclaimed) != 1 || reclaimed[0].MessageID != firstID {
		t.Fatalf("expired lease reclaim = %#v, %v", reclaimed, err)
	}
	if err := source.MarkFailed(ctx, firstID, "publisher-c", "temporary broker failure", 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	var retryUsesDatabaseClock bool
	if err := connector.Pool().QueryRow(ctx, `
SELECT available_at BETWEEN clock_timestamp()+interval '110 seconds' AND clock_timestamp()+interval '121 seconds'
FROM core.outbox_messages WHERE message_id=$1;`, firstID).Scan(&retryUsesDatabaseClock); err != nil {
		t.Fatal(err)
	}
	if !retryUsesDatabaseClock {
		t.Fatal("retry availability was not based on PostgreSQL current time plus the retry duration")
	}
	tooEarly, err := source.Claim(ctx, outbox.Claim{Owner: "publisher-d", LeaseDuration: time.Minute, Limit: 1})
	if err != nil || len(tooEarly) != 0 {
		t.Fatalf("early retry claim = %#v, %v", tooEarly, err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.outbox_messages SET available_at=clock_timestamp()-interval '1 microsecond' WHERE message_id=$1;`, firstID); err != nil {
		t.Fatal(err)
	}
	retry, err := source.Claim(ctx, outbox.Claim{Owner: "publisher-d", LeaseDuration: time.Minute, Limit: 1})
	if err != nil || len(retry) != 1 || retry[0].AttemptCount != 1 || retry[0].LastError != "temporary broker failure" {
		t.Fatalf("retry claim = %#v, %v", retry, err)
	}
	if err := source.MarkPublished(ctx, firstID, "publisher-d"); err != nil {
		t.Fatal(err)
	}
	var publishedUsesDatabaseClock bool
	if err := connector.Pool().QueryRow(ctx, `
SELECT published_at BETWEEN clock_timestamp()-interval '5 seconds' AND clock_timestamp()+interval '1 second'
FROM core.outbox_messages WHERE message_id=$1;`, firstID).Scan(&publishedUsesDatabaseClock); err != nil {
		t.Fatal(err)
	}
	if !publishedUsesDatabaseClock {
		t.Fatal("published_at was not assigned by PostgreSQL")
	}
	normalClaim, err := source.Claim(ctx, outbox.Claim{Owner: "publisher-e", LeaseDuration: time.Minute, Limit: 10})
	if err != nil || len(normalClaim) != 0 {
		t.Fatalf("published or PostgreSQL-future message was claimed: %#v, %v", normalClaim, err)
	}

	secondID := clockSkewID
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.outbox_messages SET topic='test.publisher',available_at=clock_timestamp()-interval '1 microsecond' WHERE message_id=$1;`, secondID); err != nil {
		t.Fatal(err)
	}
	bus := &lockingProbeBus{pool: connector.Pool(), published: make(chan error, 1)}
	publisher, err := outbox.NewPublisher(bus, []outbox.Source{source}, slog.New(slog.NewTextHandler(io.Discard, nil)), outbox.PublisherConfig{PollInterval: 5 * time.Millisecond, BatchSize: 1, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	workerContext, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(workerContext) }()
	select {
	case err := <-bus.published:
		if err != nil {
			t.Fatalf("broker probe could not update claimed row after claim commit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outbox publisher did not publish")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var publishedAt *time.Time
		if err := connector.Pool().QueryRow(ctx, `SELECT published_at FROM core.outbox_messages WHERE message_id=$1;`, secondID).Scan(&publishedAt); err != nil {
			t.Fatal(err)
		}
		if publishedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("published row was not marked successful")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher cancellation timed out")
	}

	if _, err := connector.Pool().Exec(ctx, `UPDATE core.outbox_messages SET published_at=clock_timestamp()-interval '8 days' WHERE message_id=$1;`, firstID); err != nil {
		t.Fatal(err)
	}
	recentID := insertOutboxTestRecord(t, ctx, connector.Pool(), "test.recent")
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.outbox_messages SET published_at=clock_timestamp() WHERE message_id=$1;`, recentID); err != nil {
		t.Fatal(err)
	}
	unpublishedID := insertOutboxTestRecord(t, ctx, connector.Pool(), "test.unpublished")
	deleted, err := source.CleanupPublished(ctx, 7*24*time.Hour, 10)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted %d: %v", deleted, err)
	}
	var remaining int
	if err := connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.outbox_messages WHERE message_id=ANY($1::text[]);`, []string{string(recentID), string(unpublishedID)}).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("cleanup removed ineligible rows, remaining = %d", remaining)
	}
}

type lockingProbeBus struct {
	pool      *pgxpool.Pool
	published chan error
	once      sync.Once
}

func (b *lockingProbeBus) Publish(ctx context.Context, message eventbus.Message) error {
	id := string(message.Headers["x-cms-message-id"])
	probeContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err := b.pool.Exec(probeContext, `UPDATE core.outbox_messages SET last_error=last_error WHERE message_id=$1;`, id)
	b.once.Do(func() { b.published <- err })
	return err
}
func (*lockingProbeBus) Consume(context.Context, eventbus.Subscription, eventbus.Handler) error {
	return nil
}

func insertOutboxTestRecord(t *testing.T, ctx context.Context, pool *pgxpool.Pool, topic string) messageid.ID {
	t.Helper()
	id, err := messageid.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.outbox_messages(message_id,topic,message_key,body,headers) VALUES($1,$2,'probe','{}','{}');`, id, topic); err != nil {
		t.Fatal(err)
	}
	return id
}

func openOutboxIntegrationDatabase(t *testing.T) (*connectorpostgres.Connector, *Database, context.Context) {
	t.Helper()
	host := os.Getenv("CMS_TEST_POSTGRES_HOST")
	if host == "" {
		t.Skip("set CMS_TEST_POSTGRES_HOST to run the PostgreSQL integration test")
	}
	port := 5432
	if value := os.Getenv("CMS_TEST_POSTGRES_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		port = parsed
	}
	sslMode := os.Getenv("CMS_TEST_POSTGRES_SSL_MODE")
	if sslMode == "" {
		sslMode = "disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{Code: "outbox-integration", Host: host, Port: port, Database: os.Getenv("CMS_TEST_POSTGRES_DB"), User: os.Getenv("CMS_TEST_POSTGRES_USER"), Password: os.Getenv("CMS_TEST_POSTGRES_PASSWORD"), SSLMode: sslMode, MaxConns: 4, ConnectTimeout: 5 * time.Second, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	if err := connector.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	database, err := NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	plan := migrations.Plan{Connection: string(connector.Code()), Target: connector, Source: database.MigrationSources()[0]}
	if err := migrations.NewManager().Up(ctx, plan); err != nil {
		t.Fatal(err)
	}
	return connector, database, ctx
}
