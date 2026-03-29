package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

var db *pgxpool.Pool

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id         BIGSERIAL PRIMARY KEY,
	google_sub TEXT        NOT NULL UNIQUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	banned_at  TIMESTAMPTZ
);
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
// already exists. Returns true if this was a brand-new user.
func upsertUser(ctx context.Context, googleSub string) (isNew bool, err error) {
	tag, err := db.Exec(ctx,
		`INSERT INTO users (google_sub) VALUES ($1) ON CONFLICT (google_sub) DO NOTHING`,
		googleSub,
	)
	if err != nil {
		return false, fmt.Errorf("upsertUser: %w", err)
	}
	isNew = tag.RowsAffected() == 1
	if isNew {
		slog.Info("New user registered", "google_sub", googleSub)
	}
	return isNew, nil
}
