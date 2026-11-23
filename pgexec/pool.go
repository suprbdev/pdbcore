// Package pgexec is the transport-neutral execution layer shared by pdbq and
// pdbr: pool setup, per-request transactions with RLS role switching and
// claims via set_config (SET LOCAL only, so pooled connections never leak
// identity), serialization-failure retries, identifier quoting, and
// PostgreSQL error classification with the dev/prod/strict message policy.
package pgexec

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/introspect"
)

// PoolConfig is the subset of database settings the pool needs.
type PoolConfig struct {
	URL              string
	MaxConns         int32
	ConnectTimeout   time.Duration
	StatementTimeout time.Duration
}

// Connect opens a pool. StatementTimeout > 0 is applied as the connection's
// statement_timeout runtime parameter (milliseconds). Every connection the
// pool opens to a server older than PostgreSQL 14 fails with
// [*introspect.UnsupportedServerError].
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
	pcfg.AfterConnect = checkServerVersion
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return pool, nil
}

func checkServerVersion(_ context.Context, conn *pgx.Conn) error {
	v := conn.PgConn().ParameterStatus("server_version")
	if m := serverMajor(v); m > 0 && m < introspect.MinServerVersionNum/10000 {
		return &introspect.UnsupportedServerError{Version: v}
	}
	return nil
}

// serverMajor returns the major release of a server_version such as "18.1",
// "19beta1" or "16.4 (Debian 16.4-1.pgdg120+2)", or 0 when it has none.
func serverMajor(v string) int {
	end := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(v)
	}
	n, _ := strconv.Atoi(v[:end])
	return n
}
