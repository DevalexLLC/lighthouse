// Package store is the control plane's PostgreSQL access layer (pgx).
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

// Connect opens a pool and verifies the database is reachable (preflight:
// a bad db.url fails startup here, loudly).
func Connect(ctx context.Context, url string, timeout time.Duration) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db.url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db unreachable at configured db.url: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Pool exposes the underlying pool for packages that need transactions.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
