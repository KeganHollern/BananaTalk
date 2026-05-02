package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisQueueKey      = "matchmaker:queue"
	redisSessionPfx    = "matchmaker:session:"
	redisNotifyPfx     = "matchmaker:notify:"
	redisTriggerKey    = "matchmaker:trigger"
	redisEnqueueAtHash = "matchmaker:enqueued_at"
	redisBlocksPfx     = "matchmaker:blocks:"
	// blocksTTL keeps a stale block SET alive long enough that a quick
	// reconnect doesn't have to re-hydrate from Postgres, but short enough
	// that a long-offline user's data is reaped from Redis. The set is
	// authoritatively rebuilt on every connect and DEL'd on disconnect, so
	// this is a safety net rather than a primary lifetime.
	blocksTTL = 24 * time.Hour
	// maxBlockedRejectionsPerCycle bounds work per processMatches call when
	// the head of the queue is dominated by mutually-blocked candidates so
	// the loop cannot starve other tickers / pub-sub events.
	maxBlockedRejectionsPerCycle = 32
)

// MatchMaker manages the matching queue via Redis, allowing multiple backend
// instances to share state.
type MatchMaker struct {
	rdb *redis.Client
}

func NewMatchMaker(rdb *redis.Client) *MatchMaker {
	return &MatchMaker{rdb: rdb}
}

// Add enqueues a user ID into the Redis waiting queue.
func (m *MatchMaker) Add(ctx context.Context, userID string) {
	now := time.Now().UnixNano()
	if err := m.rdb.RPush(ctx, redisQueueKey, userID).Err(); err != nil {
		slog.Error("MatchMaker: failed to enqueue user", "user_id", userID, "error", err)
		return
	}
	if err := m.rdb.HSet(ctx, redisEnqueueAtHash, userID, now).Err(); err != nil {
		slog.Error("MatchMaker: failed to record enqueue time", "user_id", userID, "error", err)
	}
	slog.Info("Adding client to match queue", "client_id", userID)
	// Signal all instances that a new user is waiting.
	m.rdb.Publish(ctx, redisTriggerKey, "1")
}

// Remove deletes a user ID from the Redis waiting queue.
func (m *MatchMaker) Remove(ctx context.Context, userID string) {
	if err := m.rdb.LRem(ctx, redisQueueKey, 0, userID).Err(); err != nil {
		slog.Error("MatchMaker: failed to dequeue user", "user_id", userID, "error", err)
		return
	}
	m.rdb.HDel(ctx, redisEnqueueAtHash, userID)
	slog.Info("Removed client from match queue", "client_id", userID)
}

// observeMatchLatency reads the enqueue timestamps for a matched pair and
// records the elapsed seconds for each into the match-latency histogram. The
// hash entries are deleted after observation so the hash does not grow.
func (m *MatchMaker) observeMatchLatency(ctx context.Context, ids ...string) {
	if len(ids) == 0 {
		return
	}
	vals, err := m.rdb.HMGet(ctx, redisEnqueueAtHash, ids...).Result()
	if err != nil {
		slog.Error("MatchMaker: failed to read enqueue times", "error", err)
		return
	}
	now := time.Now().UnixNano()
	for i, raw := range vals {
		s, ok := raw.(string)
		if !ok {
			continue
		}
		ns, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		dt := time.Duration(now - ns)
		if dt < 0 {
			continue
		}
		matchLatencySeconds.Observe(dt.Seconds())
		_ = i
	}
	m.rdb.HDel(ctx, redisEnqueueAtHash, ids...)
}

// popPairScript atomically pops two user IDs from the front of the queue.
// Returns an empty table if fewer than 2 users are waiting, preventing
// races between concurrent backend instances.
var popPairScript = redis.NewScript(`
local queue = KEYS[1]
if redis.call('LLEN', queue) < 2 then
    return {}
end
local c1 = redis.call('LPOP', queue)
local c2 = redis.call('LPOP', queue)
return {c1, c2}
`)

func (m *MatchMaker) tryMatch(ctx context.Context) (string, string, error) {
	result, err := popPairScript.Run(ctx, m.rdb, []string{redisQueueKey}).StringSlice()
	if err != nil {
		return "", "", err
	}
	if len(result) < 2 {
		return "", "", nil
	}
	return result[0], result[1], nil
}

// HydrateBlocks replaces the user's block SET in Redis with `subs`. Called on
// connect after loading the user's blocks from Postgres. Empty subs leaves no
// key behind so SISMEMBER short-circuits.
func (m *MatchMaker) HydrateBlocks(ctx context.Context, userID string, subs []string) {
	key := redisBlocksPfx + userID
	pipe := m.rdb.TxPipeline()
	pipe.Del(ctx, key)
	if len(subs) > 0 {
		members := make([]any, len(subs))
		for i, s := range subs {
			members[i] = s
		}
		pipe.SAdd(ctx, key, members...)
		pipe.Expire(ctx, key, blocksTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Error("MatchMaker: failed to hydrate blocks", "user_id", userID, "error", err)
	}
}

// AddBlock incrementally adds blockedSub to blockerSub's online block set.
// No-op if the blocker has no Redis set (offline) — their next connect will
// rehydrate from Postgres. Used by /report and /block when the blocker's
// session is currently active on some backend instance.
func (m *MatchMaker) AddBlock(ctx context.Context, blockerSub, blockedSub string) {
	key := redisBlocksPfx + blockerSub
	exists, err := m.rdb.Exists(ctx, key).Result()
	if err != nil {
		slog.Error("MatchMaker: blocks EXISTS failed", "user_id", blockerSub, "error", err)
		return
	}
	if exists == 0 {
		return
	}
	if err := m.rdb.SAdd(ctx, key, blockedSub).Err(); err != nil {
		slog.Error("MatchMaker: SADD block failed", "user_id", blockerSub, "error", err)
	}
}

// ClearBlocks removes the user's block SET from Redis on disconnect.
func (m *MatchMaker) ClearBlocks(ctx context.Context, userID string) {
	if err := m.rdb.Del(ctx, redisBlocksPfx+userID).Err(); err != nil {
		slog.Error("MatchMaker: failed to clear blocks", "user_id", userID, "error", err)
	}
}

// pairBlocked returns true if either side of the candidate pair has the
// other in their block SET. A Redis error is treated as not-blocked so a
// transient outage doesn't strand users in the queue forever.
func (m *MatchMaker) pairBlocked(ctx context.Context, a, b string) bool {
	if blocked, err := m.rdb.SIsMember(ctx, redisBlocksPfx+a, b).Result(); err == nil && blocked {
		return true
	} else if err != nil {
		slog.Error("MatchMaker: SISMEMBER failed", "blocker", a, "blocked", b, "error", err)
	}
	if blocked, err := m.rdb.SIsMember(ctx, redisBlocksPfx+b, a).Result(); err == nil && blocked {
		return true
	} else if err != nil {
		slog.Error("MatchMaker: SISMEMBER failed", "blocker", b, "blocked", a, "error", err)
	}
	return false
}

// SetSession records a user -> peer mapping in Redis with a 24-hour TTL.
func (m *MatchMaker) SetSession(ctx context.Context, userID, peerID string) {
	if err := m.rdb.Set(ctx, redisSessionPfx+userID, peerID, 24*time.Hour).Err(); err != nil {
		slog.Error("MatchMaker: failed to set session", "user_id", userID, "error", err)
	}
}

// DeleteSession removes a user's peer mapping from Redis.
func (m *MatchMaker) DeleteSession(ctx context.Context, userID string) {
	if err := m.rdb.Del(ctx, redisSessionPfx+userID).Err(); err != nil {
		slog.Error("MatchMaker: failed to delete session", "user_id", userID, "error", err)
	}
}

// Run starts the matching loop. It responds to trigger pub/sub messages from
// any instance and falls back to a periodic ticker so no matches are missed.
func (m *MatchMaker) Run(ctx context.Context) {
	sub := m.rdb.Subscribe(ctx, redisTriggerKey)
	defer func() { _ = sub.Close() }()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	triggerCh := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-triggerCh:
			m.processMatches(ctx)
		case <-ticker.C:
			m.processMatches(ctx)
		}
	}
}

// processMatches drains all matchable pairs from the shared queue.
func (m *MatchMaker) processMatches(ctx context.Context) {
	rejections := 0
	for {
		id1, id2, err := m.tryMatch(ctx)
		if err != nil {
			slog.Error("MatchMaker: tryMatch error", "error", err)
			return
		}
		if id1 == "" {
			return
		}

		if m.pairBlocked(ctx, id1, id2) {
			blockedPairingsTotal.Inc()
			slog.Info("MatchMaker: rejected blocked pair", "client1", id1, "client2", id2)
			// Re-queue both at the tail so a different candidate has a
			// chance to land between them on the next pop.
			if err := m.rdb.RPush(ctx, redisQueueKey, id1, id2).Err(); err != nil {
				slog.Error("MatchMaker: requeue after block failed", "error", err)
				return
			}
			rejections++
			if rejections >= maxBlockedRejectionsPerCycle {
				return
			}
			continue
		}

		slog.Info("Matching clients", "client1", id1, "client2", id2)
		matchesTotal.Inc()
		m.observeMatchLatency(ctx, id1, id2)
		m.SetSession(ctx, id1, id2)
		m.SetSession(ctx, id2, id1)

		// Publish match notifications on per-user channels so any backend
		// instance holding the matched client's WebSocket can deliver it.
		if err := m.rdb.Publish(ctx, redisNotifyPfx+id1, id2).Err(); err != nil {
			slog.Error("MatchMaker: failed to notify client", "client_id", id1, "error", err)
		}
		if err := m.rdb.Publish(ctx, redisNotifyPfx+id2, id1).Err(); err != nil {
			slog.Error("MatchMaker: failed to notify client", "client_id", id2, "error", err)
		}
	}
}
