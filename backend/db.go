package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var db *pgxpool.Pool

// AutoBanThreshold is the number of reports a user must accumulate within
// AutoBanWindow for the ban to fire.
const (
	AutoBanThreshold = 5
	AutoBanWindow    = 24 * time.Hour
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id         BIGSERIAL PRIMARY KEY,
	google_sub TEXT        NOT NULL UNIQUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	banned_at  TIMESTAMPTZ
);

ALTER TABLE users
	ADD COLUMN IF NOT EXISTS reports_received_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS reports (
	id             BIGSERIAL PRIMARY KEY,
	reporter_id    BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	reported_id    BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	reason         TEXT        NOT NULL,
	screenshot_url TEXT        NOT NULL,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS reports_reported_created_idx
	ON reports (reported_id, created_at DESC);
`

func initDB(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("schema init: %w", err)
	}

	return pool, nil
}

// upsertUser inserts a new user row on first login; does nothing if the user
// already exists. Returns the internal users.id and whether this was a
// brand-new user.
func upsertUser(ctx context.Context, googleSub string) (id int64, isNew bool, err error) {
	err = db.QueryRow(ctx,
		`INSERT INTO users (google_sub) VALUES ($1)
		 ON CONFLICT (google_sub) DO UPDATE SET google_sub = EXCLUDED.google_sub
		 RETURNING id, (xmax = 0)`,
		googleSub,
	).Scan(&id, &isNew)
	if err != nil {
		return 0, false, fmt.Errorf("upsertUser: %w", err)
	}
	if isNew {
		slog.Info("New user registered", "google_sub", googleSub)
	}
	return id, isNew, nil
}

// getUserIDByGoogleSub looks up the internal user id for a given google_sub.
// Returns pgx.ErrNoRows if the user does not exist.
func getUserIDByGoogleSub(ctx context.Context, googleSub string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx,
		`SELECT id FROM users WHERE google_sub = $1`,
		googleSub,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// isUserBanned returns true if the user with the given google_sub has a
// non-null banned_at timestamp.
func isUserBanned(ctx context.Context, googleSub string) (bool, error) {
	var bannedAt *time.Time
	err := db.QueryRow(ctx,
		`SELECT banned_at FROM users WHERE google_sub = $1`,
		googleSub,
	).Scan(&bannedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return bannedAt != nil, nil
}

// recordReport inserts the report row, increments the reported user's count,
// and applies the auto-ban if the 24-hour threshold is exceeded. All
// operations run inside a single transaction. Returns whether the reported
// user was banned as a result of this call.
func recordReport(ctx context.Context, reporterID, reportedID int64, reason, screenshotURL string) (banned bool, err error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx,
		`INSERT INTO reports (reporter_id, reported_id, reason, screenshot_url)
		 VALUES ($1, $2, $3, $4)`,
		reporterID, reportedID, reason, screenshotURL,
	); err != nil {
		return false, fmt.Errorf("insert report: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`UPDATE users SET reports_received_count = reports_received_count + 1 WHERE id = $1`,
		reportedID,
	); err != nil {
		return false, fmt.Errorf("increment count: %w", err)
	}

	var recent int
	if err = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM reports
		 WHERE reported_id = $1 AND created_at >= $2`,
		reportedID, time.Now().Add(-AutoBanWindow),
	).Scan(&recent); err != nil {
		return false, fmt.Errorf("count recent reports: %w", err)
	}

	if recent > AutoBanThreshold {
		ct, err := tx.Exec(ctx,
			`UPDATE users SET banned_at = NOW() WHERE id = $1 AND banned_at IS NULL`,
			reportedID,
		)
		if err != nil {
			return false, fmt.Errorf("set banned_at: %w", err)
		}
		banned = ct.RowsAffected() > 0
	}

	if err = tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit tx: %w", err)
	}
	return banned, nil
}
