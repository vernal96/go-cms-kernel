package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	hookspostgres "github.com/vernal96/go-cms-kernel/entityhooks/adapters/postgres"
)

func TestPostgresEntityHookLeasesAndRetention(t *testing.T) {
	connector, _, ctx := openOutboxIntegrationDatabase(t)
	source := hookspostgres.NewSource(connector.Pool(), "core:"+string(connector.Code()), "core")
	event, err := domainevent.New("test.completed", 1, time.Now().UTC(), struct {
		Value string `json:"value"`
	}{"snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	a := entityhooks.Target{Module: "test", Handler: "first", Scope: entityhooks.Application}
	b := entityhooks.Target{Module: "test", Handler: "second", Scope: entityhooks.Application}
	tx, err := connector.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := source.Append(ctx, tx, event, []entityhooks.Target{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	if calls, err := source.Calls(ctx, event.ID); err != nil || len(calls) != 0 {
		t.Fatalf("uncommitted calls visible: %d %v", len(calls), err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if calls, err := source.Calls(ctx, event.ID); err != nil || len(calls) != 0 {
		t.Fatalf("rollback leaked calls: %d %v", len(calls), err)
	}
	tx, err = connector.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := source.Append(ctx, tx, event, []entityhooks.Target{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	first := entityhooks.Delivery{Source: source.Name(), Event: event, Target: a}
	if ok, err := source.Claim(ctx, first, "worker-a", time.Minute); err != nil || !ok {
		t.Fatalf("first claim=%v %v", ok, err)
	}
	if ok, err := source.Claim(ctx, first, "worker-b", time.Minute); err != nil || ok {
		t.Fatalf("concurrent claim=%v %v", ok, err)
	}
	if err := source.Complete(ctx, first, "worker-b"); err == nil {
		t.Fatal("wrong lease owner completed call")
	}
	if err := source.Fail(ctx, first, "worker-a", "retry"); err != nil {
		t.Fatal(err)
	}
	if ok, err := source.Claim(ctx, first, "worker-b", time.Minute); err != nil || !ok {
		t.Fatalf("retry claim=%v %v", ok, err)
	}
	if err := source.Complete(ctx, first, "worker-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.entity_hook_calls SET completed_at=clock_timestamp()-interval '10 days' WHERE event_id=$1 AND handler='first'`, event.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := source.Cleanup(ctx, 7*24*time.Hour, 1); err != nil || count != 0 {
		t.Fatalf("completed sibling of pending call cleaned: %d %v", count, err)
	}
	second := entityhooks.Delivery{Source: source.Name(), Event: event, Target: b}
	if ok, err := source.Claim(ctx, second, "crashed-worker", time.Minute); err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.entity_hook_calls SET lease_until=clock_timestamp()-interval '1 second' WHERE event_id=$1 AND handler='second'`, event.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := source.Claim(ctx, second, "replacement", time.Minute); err != nil || !ok {
		t.Fatalf("expired lease recovery=%v %v", ok, err)
	}
	if err := source.Complete(ctx, second, "replacement"); err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.entity_hook_calls SET completed_at=clock_timestamp()-interval '10 days' WHERE event_id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := source.Cleanup(ctx, 7*24*time.Hour, 1); err != nil || count != 1 {
		t.Fatalf("bounded cleanup=%d %v", count, err)
	}
	if count, err := source.Cleanup(ctx, 7*24*time.Hour, 1); err != nil || count != 1 {
		t.Fatalf("remaining cleanup=%d %v", count, err)
	}
}
