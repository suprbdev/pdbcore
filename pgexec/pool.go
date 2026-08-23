// Package pgexec is the transport-neutral execution layer shared by pdbq and
// pdbr: pool setup, per-request transactions with RLS role switching and
// claims via set_config (SET LOCAL only, so pooled connections never leak
// identity), serialization-failure retries, identifier quoting, and
// PostgreSQL error classification with the dev/prod/strict message policy.
package pgexec

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig is the subset of database settings the pool needs.
type PoolConfig struct {
	URL              string
	MaxConns         int32
	ConnectTimeout   time.Duration
	StatementTimeout time.Duration
}

// Connect opens a pool. StatementTimeout > 0 is applied as the connection's
// statement_timeout runtime parameter (milliseconds).
func Connect(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	if t := cfg.StatementTimeout; t > 0 {
		if pcfg.ConnConfig.RuntimeParams == nil {
			pcfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		pcfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", t.Milliseconds())
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return pool, nil
}
