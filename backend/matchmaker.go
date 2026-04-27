package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisQueueKey   = "matchmaker:queue"
	redisSessionPfx = "matchmaker:session:"
	redisNotifyPfx  = "matchmaker:notify:"
	redisTriggerKey = "matchmaker:trigger"
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
	if err := m.rdb.RPush(ctx, redisQueueKey, userID).Err(); err != nil {
		slog.Error("MatchMaker: failed to enqueue user", "user_id", userID, "error", err)
		return
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
	slog.Info("Removed client from match queue", "client_id", userID)
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
	for {
		id1, id2, err := m.tryMatch(ctx)
		if err != nil {
			slog.Error("MatchMaker: tryMatch error", "error", err)
			return
		}
		if id1 == "" {
			return
		}

		slog.Info("Matching clients", "client1", id1, "client2", id2)
		matchesTotal.Inc()
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
