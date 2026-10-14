package redis

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
)

func TestRedisFactoryIsLazyAndFailureBounded(t *testing.T) {
	factory := Factory{Options: goredis.UniversalOptions{Addrs: []string{"127.0.0.1:1"}}, Prefix: "test-lazy"}
	client, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := client.Increment(ctx, 1, 2); err == nil {
		t.Fatal("expected unavailable store")
	}
}

func TestRedisClaimsReclaimAfterWorkerShutdownAndPreserveNewIncrements(t *testing.T) {
	address := os.Getenv("CMS_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("set CMS_TEST_REDIS_ADDR for Redis integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	factory := Factory{Options: goredis.UniversalOptions{Addrs: []string{address}}, Prefix: time.Now().Format("150405.000000000")}
	first, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Increment(ctx, 7, 9); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan *resourceviews.Batch, 2)
	failures := make(chan error, 2)
	for _, client := range []resourceviews.Accumulator{first, second} {
		wg.Add(1)
		go func(client resourceviews.Accumulator) {
			defer wg.Done()
			batch, err := client.Claim(ctx, 7)
			claims <- batch
			failures <- err
		}(client)
	}
	wg.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var claimed *resourceviews.Batch
	for batch := range claims {
		if batch != nil {
			if claimed != nil {
				t.Fatal("two workers claimed same batch")
			}
			claimed = batch
		}
	}
	if claimed == nil || claimed.Counts[9] != 1 {
		t.Fatalf("batch=%v", claimed)
	}
	// Abrupt owner exit does not touch the Redis batch. New views use pending.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Increment(ctx, 7, 9); err != nil {
		t.Fatal(err)
	}
	administrator := goredis.NewUniversalClient(&factory.Options)
	defer administrator.Close()
	keys := second.(*Accumulator).keys(7)
	defer administrator.Del(context.Background(), keys...)
	// Accelerate the same Redis TTL expiration used after a crashed process.
	if err := administrator.PExpire(ctx, keys[3], time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	reclaimed, err := second.Claim(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed == nil || reclaimed.ID != claimed.ID || reclaimed.Owner == claimed.Owner || reclaimed.Counts[9] != 1 {
		t.Fatalf("reclaimed=%+v old=%+v", reclaimed, claimed)
	}
	if err := second.Acknowledge(ctx, *claimed); err == nil {
		t.Fatal("expired owner acknowledged reclaimed batch")
	}
	if err := second.Acknowledge(ctx, *reclaimed); err != nil {
		t.Fatal(err)
	}
	next, err := second.Claim(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID == reclaimed.ID || next.Counts[9] != 1 {
		t.Fatalf("new increments lost: %+v", next)
	}
	if err := second.Acknowledge(ctx, *next); err != nil {
		t.Fatal(err)
	}
	// Claims are bounded even with many resource identities.
	for id := resource.ID(1); id <= 1200; id++ {
		if err := second.Increment(ctx, 7, id); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := second.Claim(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Counts) > 1000 {
		t.Fatalf("unbounded batch size: %v", batch)
	}
}
