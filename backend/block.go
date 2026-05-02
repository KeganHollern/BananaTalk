package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// blockRequest is the JSON body accepted by POST /block.
type blockRequest struct {
	BlockedUserID string `json:"blocked_user_id"`
}

// blockHandler lets an authenticated user block another user without filing
// a report. The block is persisted in Postgres and reflected in the blocker's
// online block SET so the matchmaker honors it on the next pop.
func blockHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	ctx := r.Context()

	blockerSub, ok := authenticate(ctx, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_token", "token invalid or expired")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<10) // 1 KiB; only a sub fits.
	defer func() { _ = r.Body.Close() }()

	var req blockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}

	blockedSub := strings.TrimSpace(req.BlockedUserID)
	if blockedSub == "" {
		writeError(w, http.StatusBadRequest, "missing_blocked_user", "blocked_user_id is required")
		return
	}
	if blockedSub == blockerSub {
		writeError(w, http.StatusBadRequest, "self_block", "cannot block yourself")
		return
	}

	blockerID, _, err := upsertUser(ctx, blockerSub)
	if err != nil {
		slog.Error("block: upsert blocker", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}

	blockedID, err := getUserIDByGoogleSub(ctx, blockedSub)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Stub the row so matchmaker bookkeeping has a target FK; mirrors
			// the same fallback in reportHandler.
			blockedID, _, err = upsertUser(ctx, blockedSub)
		}
		if err != nil {
			slog.Error("block: lookup blocked", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
			return
		}
	}

	if err := insertBlock(ctx, blockerID, blockedID); err != nil {
		slog.Error("block: insert", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}

	// Symmetric: insertBlock wrote both DB rows, so reflect both in Redis.
	// AddBlock is a no-op for offline users, who'll rehydrate from Postgres on
	// next connect.
	matchMaker.AddBlock(ctx, blockerSub, blockedSub)
	matchMaker.AddBlock(ctx, blockedSub, blockerSub)

	slog.Info("Block recorded", "blocker_sub", blockerSub, "blocked_sub", blockedSub)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"blocked":true}`))
}

// blocksHandler returns the authenticated user's full block list as
// `{"blocked_ids": [...]}`. Clients call this on launch to rehydrate their
// local cache so blocks survive reinstalls, new devices, and long offline
// periods. The matchmaker treats blocks as symmetric (insertBlock writes both
// directions), so this list also covers blocks where the caller was the
// blocked party.
func blocksHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	ctx := r.Context()

	sub, ok := authenticate(ctx, r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_token", "token invalid or expired")
		return
	}

	userID, err := getUserIDByGoogleSub(ctx, sub)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Caller has never been seen — no row, no blocks.
			writeBlocksResponse(w, nil)
			return
		}
		slog.Error("blocks: lookup user", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}

	subs, err := loadUserBlocks(ctx, userID)
	if err != nil {
		slog.Error("blocks: load", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	writeBlocksResponse(w, subs)
}

func writeBlocksResponse(w http.ResponseWriter, subs []string) {
	if subs == nil {
		subs = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"blocked_ids": subs})
}
