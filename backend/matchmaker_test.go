package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestMatchMaker(t *testing.T) (*MatchMaker, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewMatchMaker(client), mr, client
}

func hashHas(t *testing.T, client *redis.Client, key, field string) bool {
	t.Helper()
	v, err := client.HExists(context.Background(), key, field).Result()
	if err != nil {
		t.Fatalf("HExists(%s, %s): %v", key, field, err)
	}
	return v
}

func TestMatchMaker_AddEnqueuesAndRecordsTimestamp(t *testing.T) {
	mm, mr, client := newTestMatchMaker(t)
	ctx := context.Background()

	mm.Add(ctx, "alice")

	if got, _ := client.LLen(ctx, redisQueueKey).Result(); got != 1 {
		t.Fatalf("queue length: want 1, got %d", got)
	}
	if !mr.Exists(redisEnqueueAtHash) {
		t.Fatalf("expected %q hash to exist after Add", redisEnqueueAtHash)
	}
	if !hashHas(t, client, redisEnqueueAtHash, "alice") {
		t.Fatalf("expected enqueue timestamp for alice")
	}
}

func TestMatchMaker_TryMatchPopsTwoUsers(t *testing.T) {
	mm, _, client := newTestMatchMaker(t)
	ctx := context.Background()

	mm.Add(ctx, "alice")
	mm.Add(ctx, "bob")

	a, b, err := mm.tryMatch(ctx)
	if err != nil {
		t.Fatalf("tryMatch: %v", err)
	}
	if a != "alice" || b != "bob" {
		t.Fatalf("want (alice, bob), got (%q, %q)", a, b)
	}
	if got, _ := client.LLen(ctx, redisQueueKey).Result(); got != 0 {
		t.Fatalf("queue length after match: want 0, got %d", got)
	}
}

func TestMatchMaker_SingleUserWaits(t *testing.T) {
	mm, _, client := newTestMatchMaker(t)
	ctx := context.Background()

	mm.Add(ctx, "alice")

	a, b, err := mm.tryMatch(ctx)
	if err != nil {
		t.Fatalf("tryMatch: %v", err)
	}
	if a != "" || b != "" {
		t.Fatalf("expected no match with one user, got (%q, %q)", a, b)
	}
	// User must remain enqueued for the next pairing attempt.
	if got, _ := client.LLen(ctx, redisQueueKey).Result(); got != 1 {
		t.Fatalf("queue length after no-match: want 1, got %d", got)
	}
}

func TestMatchMaker_RemoveOnDisconnect(t *testing.T) {
	mm, _, client := newTestMatchMaker(t)
	ctx := context.Background()

	mm.Add(ctx, "alice")
	mm.Remove(ctx, "alice")

	if got, _ := client.LLen(ctx, redisQueueKey).Result(); got != 0 {
		t.Fatalf("queue length after Remove: want 0, got %d", got)
	}
	if hashHas(t, client, redisEnqueueAtHash, "alice") {
		t.Fatalf("expected enqueue timestamp for alice to be cleared")
	}
}

func TestMatchMaker_ObserveMatchLatencyClearsHash(t *testing.T) {
	mm, mr, client := newTestMatchMaker(t)
	ctx := context.Background()

	mm.Add(ctx, "alice")
	mm.Add(ctx, "bob")

	// Move time forward inside miniredis so the observed latency is non-zero
	// (also exercises the path where dt > 0).
	mr.FastForward(50 * time.Millisecond)

	mm.observeMatchLatency(ctx, "alice", "bob")

	if hashHas(t, client, redisEnqueueAtHash, "alice") {
		t.Fatalf("alice timestamp not cleared after observeMatchLatency")
	}
	if hashHas(t, client, redisEnqueueAtHash, "bob") {
		t.Fatalf("bob timestamp not cleared after observeMatchLatency")
	}
}

func TestMatchMaker_ObserveMatchLatencyEmptyIDsNoop(t *testing.T) {
	mm, _, _ := newTestMatchMaker(t)
	// Should not panic, should not touch Redis.
	mm.observeMatchLatency(context.Background())
}
