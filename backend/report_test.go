package main

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// setupTestDB connects to TEST_DATABASE_URL (or skips if unset), installs the
// schema, and returns a teardown that closes the pool. Each call also wipes
// users + reports so tests don't bleed into each other. The package-level `db`
// global is set so production code paths can be exercised unchanged.
func setupTestDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := initDB(ctx, dsn)
	if err != nil {
		t.Fatalf("initDB: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE reports, blocks, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	db = pool
	t.Cleanup(func() { pool.Close() })
}

// seedUsers creates `reporters` distinct reporter rows and a single reported
// row. Distinct reporters keep recordReport's COUNT-based threshold logic
// straightforward to assert against.
func seedUsers(ctx context.Context, t *testing.T, reporters int) (reporterIDs []int64, reportedID int64) {
	t.Helper()
	for i := range reporters {
		id, _, err := upsertUser(ctx, "reporter-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("upsertUser reporter %d: %v", i, err)
		}
		reporterIDs = append(reporterIDs, id)
	}
	id, _, err := upsertUser(ctx, "reported-user-sub")
	if err != nil {
		t.Fatalf("upsertUser reported: %v", err)
	}
	return reporterIDs, id
}

func TestRecordReport_IncrementsCount(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, reported := seedUsers(ctx, t, 1)

	banned, err := recordReport(ctx, reporters[0], reported, "spam", "https://example/k", "k")
	if err != nil {
		t.Fatalf("recordReport: %v", err)
	}
	if banned {
		t.Fatalf("first report should not auto-ban")
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT reports_received_count FROM users WHERE id = $1`, reported).Scan(&count); err != nil {
		t.Fatalf("select count: %v", err)
	}
	if count != 1 {
		t.Fatalf("reports_received_count: want 1, got %d", count)
	}

	var bannedAt *string
	if err := db.QueryRow(ctx, `SELECT banned_at::text FROM users WHERE id = $1`, reported).Scan(&bannedAt); err != nil {
		t.Fatalf("select banned_at: %v", err)
	}
	if bannedAt != nil {
		t.Fatalf("banned_at should be NULL after one report, got %q", *bannedAt)
	}
}

func TestRecordReport_AutoBansAfterThreshold(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	// AutoBanThreshold==5 and the trigger is `recent > AutoBanThreshold`,
	// i.e. the 6th report inside the window flips banned_at. Use 6 distinct
	// reporters so the rows are unambiguous.
	reporters, reported := seedUsers(ctx, t, AutoBanThreshold+1)

	for i, rid := range reporters {
		banned, err := recordReport(ctx, rid, reported, "spam", "https://example/k", "k")
		if err != nil {
			t.Fatalf("recordReport %d: %v", i, err)
		}
		isLast := i == len(reporters)-1
		if isLast && !banned {
			t.Fatalf("report %d (Threshold+1) should have flipped banned_at", i+1)
		}
		if !isLast && banned {
			t.Fatalf("report %d should not have auto-banned (still under threshold)", i+1)
		}
	}

	var bannedAt *string
	if err := db.QueryRow(ctx, `SELECT banned_at::text FROM users WHERE id = $1`, reported).Scan(&bannedAt); err != nil {
		t.Fatalf("select banned_at: %v", err)
	}
	if bannedAt == nil {
		t.Fatalf("banned_at should be set after Threshold+1 reports")
	}

	// And isUserBanned should now report true.
	wasBanned, err := isUserBanned(ctx, "reported-user-sub")
	if err != nil {
		t.Fatalf("isUserBanned: %v", err)
	}
	if !wasBanned {
		t.Fatalf("isUserBanned: want true after auto-ban")
	}
}

// A report from A about B must block both directions; otherwise B can rematch
// with A on a different pod and resume harassing them. App Store review treats
// one-directional blocking as a defect.
func TestRecordReport_BlockIsSymmetric(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	reporters, reported := seedUsers(ctx, t, 1)

	if _, err := recordReport(ctx, reporters[0], reported, "spam", "https://example/k", "k"); err != nil {
		t.Fatalf("recordReport: %v", err)
	}

	reporterSubs, err := loadUserBlocks(ctx, reporters[0])
	if err != nil {
		t.Fatalf("loadUserBlocks (reporter): %v", err)
	}
	if len(reporterSubs) != 1 || reporterSubs[0] != "reported-user-sub" {
		t.Fatalf("reporter's block list: want [reported-user-sub], got %v", reporterSubs)
	}

	reportedSubs, err := loadUserBlocks(ctx, reported)
	if err != nil {
		t.Fatalf("loadUserBlocks (reported): %v", err)
	}
	if len(reportedSubs) != 1 || reportedSubs[0] != "reporter-0" {
		t.Fatalf("reported user's block list (reverse direction): want [reporter-0], got %v", reportedSubs)
	}
}

func TestRecordReport_BannedFlagOnlyOnTransition(t *testing.T) {
	setupTestDB(t)
	ctx := context.Background()

	// Push the user past the ban line, then file an extra report and confirm
	// recordReport returns banned=false (no state change).
	reporters, reported := seedUsers(ctx, t, AutoBanThreshold+2)

	for _, rid := range reporters[:len(reporters)-1] {
		if _, err := recordReport(ctx, rid, reported, "spam", "https://example/k", "k"); err != nil {
			t.Fatalf("recordReport (setup): %v", err)
		}
	}

	banned, err := recordReport(ctx, reporters[len(reporters)-1], reported, "spam", "https://example/k", "k")
	if err != nil {
		t.Fatalf("recordReport (final): %v", err)
	}
	if banned {
		t.Fatalf("recordReport on already-banned user should return banned=false (no transition)")
	}
}
