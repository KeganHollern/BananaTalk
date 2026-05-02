package main

import (
	"context"
	"testing"
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
