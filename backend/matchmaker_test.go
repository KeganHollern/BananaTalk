package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	dto "github.com/prometheus/client_model/go"
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

func TestMatchMaker_PairBlockedDetectsEitherDirection(t *testing.T) {
	mm, _, _ := newTestMatchMaker(t)
	ctx := context.Background()

	mm.HydrateBlocks(ctx, "alice", []string{"bob"})

	if !mm.pairBlocked(ctx, "alice", "bob") {
		t.Fatalf("expected pair to be blocked when alice blocks bob")
	}
	if !mm.pairBlocked(ctx, "bob", "alice") {
		t.Fatalf("expected pair to be blocked symmetrically (bob, alice)")
	}
	if mm.pairBlocked(ctx, "alice", "carol") {
		t.Fatalf("did not expect alice/carol to be blocked")
	}
}

func TestMatchMaker_HydrateBlocksReplacesExisting(t *testing.T) {
	mm, _, _ := newTestMatchMaker(t)
	ctx := context.Background()

	mm.HydrateBlocks(ctx, "alice", []string{"bob", "carol"})
	mm.HydrateBlocks(ctx, "alice", []string{"dan"})

	if mm.pairBlocked(ctx, "alice", "bob") {
		t.Fatalf("hydrate should have replaced the SET; bob shouldn't be blocked anymore")
	}
	if !mm.pairBlocked(ctx, "alice", "dan") {
		t.Fatalf("dan should be in alice's block set after rehydrate")
	}
}

func TestMatchMaker_AddBlockNoopWhenOffline(t *testing.T) {
	mm, _, client := newTestMatchMaker(t)
	ctx := context.Background()

	// alice has no Redis SET (offline). AddBlock must NOT create one — that
	// would leak across reconnects and shadow the DB-backed source of truth.
	mm.AddBlock(ctx, "alice", "bob")

	exists, err := client.Exists(ctx, redisBlocksPfx+"alice").Result()
	if err != nil {
		t.Fatalf("EXISTS: %v", err)
	}
	if exists != 0 {
		t.Fatalf("expected no key to be created for offline blocker, got exists=%d", exists)
	}
}

func TestMatchMaker_AddBlockExtendsOnlineSet(t *testing.T) {
	mm, _, _ := newTestMatchMaker(t)
	ctx := context.Background()

	// Alice "connects" — empty hydrate creates no key, so seed with one entry.
	mm.HydrateBlocks(ctx, "alice", []string{"carol"})
	mm.AddBlock(ctx, "alice", "bob")

	if !mm.pairBlocked(ctx, "alice", "bob") {
		t.Fatalf("AddBlock should have added bob to alice's online set")
	}
}

func TestMatchMaker_ClearBlocksRemovesSet(t *testing.T) {
	mm, _, client := newTestMatchMaker(t)
	ctx := context.Background()

	mm.HydrateBlocks(ctx, "alice", []string{"bob"})
	mm.ClearBlocks(ctx, "alice")

	exists, _ := client.Exists(ctx, redisBlocksPfx+"alice").Result()
	if exists != 0 {
		t.Fatalf("expected blocks key to be cleared, got exists=%d", exists)
	}
}

func TestMatchMaker_ProcessMatchesRejectsBlockedPair(t *testing.T) {
	mm, _, client := newTestMatchMaker(t)
	ctx := context.Background()

	// alice ↔ bob have blocked each other. carol is the only viable peer.
	mm.HydrateBlocks(ctx, "alice", []string{"bob"})
	mm.Add(ctx, "alice")
	mm.Add(ctx, "bob")
	mm.Add(ctx, "carol")

	// Capture the blocked-pairings counter before draining.
	before := readCounter(t, blockedPairingsTotal)

	mm.processMatches(ctx)

	// One of {alice, bob} must remain in the queue (whichever is paired with
	// carol leaves; the other goes back to the tail). Exactly one should be
	// matched with carol via session mappings.
	qlen, _ := client.LLen(ctx, redisQueueKey).Result()
	if qlen != 1 {
		t.Fatalf("queue length after process: want 1, got %d", qlen)
	}

	carolPeer, _ := client.Get(ctx, redisSessionPfx+"carol").Result()
	if carolPeer != "alice" && carolPeer != "bob" {
		t.Fatalf("carol should be matched with alice or bob, got %q", carolPeer)
	}

	if got := readCounter(t, blockedPairingsTotal); got <= before {
		t.Fatalf("blocked_pairings_total should have incremented; before=%v after=%v", before, got)
	}
}

func readCounter(t *testing.T, c interface {
	Write(*dto.Metric) error
}) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter Write: %v", err)
	}
	if m.Counter == nil {
		t.Fatalf("expected counter metric")
	}
	return m.Counter.GetValue()
}
