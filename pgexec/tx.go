package pgexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the statement-execution surface handed to callbacks; satisfied
// by *pgxpool.Pool, *pgx.Conn and pgx.Tx, so callers can run the same code
// inside and outside a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// TxBeginner starts transactions; satisfied by *pgxpool.Pool and *pgx.Conn.
type TxBeginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// DefaultClaimsPrefix is the set_config namespace used when
// Identity.ClaimsPrefix is empty (kept as pdbq's so existing RLS policies
// work with either product).
const DefaultClaimsPrefix = "pdbq.claims"

// Identity is the request identity applied inside a transaction.
type Identity struct {
	// Role is the database role to SET LOCAL ROLE to ("" = no switch).
	Role string
	// Claims are exposed as set_config(ClaimsPrefix.key, value, true).
	// String values are set verbatim; anything else is JSON-encoded. Keys
	// failing ValidClaimKey are skipped.
	Claims map[string]any
	// ClaimsPrefix defaults to DefaultClaimsPrefix.
	ClaimsPrefix string
}

// TxOptions controls RunTx.
type TxOptions struct {
	Isolation pgx.TxIsoLevel
	// MaxRetries re-runs the whole transaction after a serialization
	// failure or deadlock (SQLSTATE 40001/40P01), no backoff.
	MaxRetries int
	// RLS enables the SET LOCAL ROLE + set_config claims step.
	RLS bool
	// Log receives the retry warning; nil uses slog.Default().
	Log *slog.Logger
}

// RunTx begins a transaction with opts.Isolation, applies the identity (RLS
// true), calls fn, and commits unless fn returned an error (rollback). The
// error from fn is returned as-is (unwrapped) so callers can inspect their
// own error types; begin/RLS/commit errors are returned raw too. On a
// serialization failure or deadlock — anywhere in the attempt — the whole
// thing re-runs up to opts.MaxRetries times, so fn may run more than once
// and must not have side effects outside the transaction.
func RunTx(ctx context.Context, db TxBeginner, opts TxOptions, id Identity, fn func(q Querier) error) error {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	for attempt := 0; ; attempt++ {
		err := runOnce(ctx, db, opts, id, fn)
		if err == nil || attempt >= opts.MaxRetries || !IsSerializationFailure(err) || ctx.Err() != nil {
			return err
		}
		// Serialization failures and deadlocks roll the transaction back
		// completely, so re-running the whole operation is safe.
		log.Warn("retrying after serialization failure",
			"attempt", attempt+1, "max_retries", opts.MaxRetries)
	}
}

func runOnce(ctx context.Context, db TxBeginner, opts TxOptions, id Identity, fn func(q Querier) error) error {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: opts.Isolation})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if opts.RLS {
		if err := ApplyIdentity(ctx, tx, id); err != nil {
			return err
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ApplyIdentity switches role and exposes claims inside an open transaction.
// It uses SET LOCAL exclusively (never SET) so pooled connections cannot
// leak request identity across requests once the connection is reused.
func ApplyIdentity(ctx context.Context, tx Querier, id Identity) error {
	if id.Role != "" {
		if !ValidRole(id.Role) {
			return fmt.Errorf("invalid role %q", id.Role)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+QuoteIdent(id.Role)); err != nil {
			return fmt.Errorf("set role: %w", err)
		}
	}
	prefix := id.ClaimsPrefix
	if prefix == "" {
		prefix = DefaultClaimsPrefix
	}
	for k, v := range id.Claims {
		if !ValidClaimKey(k) {
			continue
		}
		val := ""
		switch tv := v.(type) {
		case string:
			val = tv
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			val = string(b)
		}
		// set_config(..., true) scopes the setting to the transaction.
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", prefix+"."+k, val); err != nil {
			return fmt.Errorf("set claim %s: %w", k, err)
		}
	}
	return nil
}

// IsSerializationFailure reports whether err carries SQLSTATE 40001
// (serialization_failure) or 40P01 (deadlock_detected), anywhere in its
// wrap chain.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

// ParseIsolation maps the config spelling ("read_committed",
// "repeatable_read", "serializable") to pgx's level; anything else is
// ReadCommitted.
func ParseIsolation(s string) pgx.TxIsoLevel {
	switch s {
	case "repeatable_read":
		return pgx.RepeatableRead
	case "serializable":
		return pgx.Serializable
	}
	return pgx.ReadCommitted
}
