package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"google.golang.org/api/idtoken"
)

func TestInsertBlock_PersistsRow(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, reported := seedUsers(ctx, t, 1)

	if err := insertBlock(ctx, reporters[0], reported); err != nil {
		t.Fatalf("insertBlock: %v", err)
	}

	var count int
	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`,
		reporters[0], reported,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("blocks row count: want 1, got %d", count)
	}
}

// App Store reviewers manually verify that blocking is two-directional. A
// one-sided block ships as broken blocking and fails review, so insertBlock
// must persist both (A→B) and (B→A) rows.
func TestInsertBlock_IsSymmetric(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, reported := seedUsers(ctx, t, 1)

	if err := insertBlock(ctx, reporters[0], reported); err != nil {
		t.Fatalf("insertBlock: %v", err)
	}

	// Reverse-direction row must exist: the reported user has the reporter
	// in their block list, so the matchmaker rejects the pair from B's pod
	// after rehydrate.
	reverseSubs, err := loadUserBlocks(ctx, reported)
	if err != nil {
		t.Fatalf("loadUserBlocks (reported side): %v", err)
	}
	if len(reverseSubs) != 1 || reverseSubs[0] != "reporter-0" {
		t.Fatalf("reported's block list after symmetric insert: want [reporter-0], got %v", reverseSubs)
	}

	// And the original direction is still there.
	forwardSubs, err := loadUserBlocks(ctx, reporters[0])
	if err != nil {
		t.Fatalf("loadUserBlocks (reporter side): %v", err)
	}
	if len(forwardSubs) != 1 || forwardSubs[0] != "reported-user-sub" {
		t.Fatalf("reporter's block list: want [reported-user-sub], got %v", forwardSubs)
	}
}

func TestInsertBlock_Idempotent(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, reported := seedUsers(ctx, t, 1)

	// Two inserts for the same pair must not error and must leave one row.
	if err := insertBlock(ctx, reporters[0], reported); err != nil {
		t.Fatalf("first insertBlock: %v", err)
	}
	if err := insertBlock(ctx, reporters[0], reported); err != nil {
		t.Fatalf("second insertBlock should be idempotent: %v", err)
	}

	var count int
	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`,
		reporters[0], reported,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("blocks row count after duplicate insert: want 1, got %d", count)
	}
}

func TestInsertBlock_RejectsSelfBlock(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	_, reported := seedUsers(ctx, t, 0)

	if err := insertBlock(ctx, reported, reported); err == nil {
		t.Fatalf("self-block must error")
	}
}

func TestRecordReport_AlsoInsertsBlock(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, reported := seedUsers(ctx, t, 1)

	if _, err := recordReport(ctx, reporters[0], reported, "spam", "https://example/k", "k"); err != nil {
		t.Fatalf("recordReport: %v", err)
	}

	subs, err := loadUserBlocks(ctx, reporters[0])
	if err != nil {
		t.Fatalf("loadUserBlocks: %v", err)
	}
	if len(subs) != 1 || subs[0] != "reported-user-sub" {
		t.Fatalf("loadUserBlocks: want [reported-user-sub], got %v", subs)
	}
}

func TestLoadUserBlocks_EmptyForFreshUser(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, _ := seedUsers(ctx, t, 1)

	subs, err := loadUserBlocks(ctx, reporters[0])
	if err != nil {
		t.Fatalf("loadUserBlocks: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected no blocks for fresh user, got %v", subs)
	}
}

// End-to-end coverage for the launch-time hydrate flow: post a couple of
// blocks via POST /block, then GET /blocks and confirm the body lists both
// targets. Guards against the "fresh install loses block list" regression.
func TestBlocksHandler_ReturnsListAfterPosts(t *testing.T) {
	setupTestDB(t)

	// Stub auth: the bearer string is treated as the google_sub directly so
	// each request maps cleanly to a known user.
	stubValidator(t, func(_ context.Context, token, _ string) (*idtoken.Payload, error) {
		return &idtoken.Payload{Subject: token, Expires: time.Now().Add(time.Hour).Unix()}, nil
	})

	// blockHandler calls matchMaker.AddBlock against Redis; back it with
	// miniredis for the duration of the test.
	mm, _, _ := newTestMatchMaker(t)
	prevMM := matchMaker
	matchMaker = mm
	t.Cleanup(func() { matchMaker = prevMM })

	post := func(blockerSub, blockedSub string) {
		t.Helper()
		body := bytes.NewBufferString(`{"blocked_user_id":"` + blockedSub + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/block", body)
		req.Header.Set("Authorization", "Bearer "+blockerSub)
		rr := httptest.NewRecorder()
		blockHandler(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("POST /block (block %s): status=%d body=%s", blockedSub, rr.Code, rr.Body.String())
		}
	}

	post("blocker-sub", "victim-a")
	post("blocker-sub", "victim-b")

	req := httptest.NewRequest(http.MethodGet, "/blocks", nil)
	req.Header.Set("Authorization", "Bearer blocker-sub")
	rr := httptest.NewRecorder()
	blocksHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /blocks: status=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		BlockedIDs []string `json:"blocked_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
	}
	got := append([]string(nil), resp.BlockedIDs...)
	sort.Strings(got)
	want := []string{"victim-a", "victim-b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("blocked_ids: want %v, got %v", want, got)
	}
}

func TestBlocksHandler_RejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/blocks", nil)
	rr := httptest.NewRecorder()
	blocksHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /blocks: want 405, got %d", rr.Code)
	}
}

func TestBlocksHandler_RejectsUnauthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/blocks", nil)
	rr := httptest.NewRecorder()
	blocksHandler(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /blocks: want 401, got %d", rr.Code)
	}
}
