package pgexec

import (
	"context"
	"errors"
	"fmt"
)

// savepointName is the savepoint every [Savepoint] call creates. PostgreSQL
// lets savepoint names repeat and resolves ROLLBACK TO and RELEASE to the
// most recent one, so calls nest and run in sequence without bookkeeping.
const savepointName = "pdb_savepoint"

// RolledBackError is returned by [Savepoint] when fn failed and its work was
// undone with ROLLBACK TO SAVEPOINT. The enclosing transaction is still
// usable and everything run before the savepoint is intact.
type RolledBackError struct {
	// Err is the error fn returned.
	Err error
}

func (e *RolledBackError) Error() string { return e.Err.Error() }

func (e *RolledBackError) Unwrap() error { return e.Err }

// Savepoint runs fn inside a SAVEPOINT of the transaction q belongs to (a
// [RunTx] callback's Querier, for example). When fn returns nil the
// savepoint is released and its work becomes part of the transaction. When
// fn fails, its work is rolled back to the savepoint, the savepoint is
// released and the error comes back as a [*RolledBackError], so the caller
// can record the failure and carry on in the same transaction.
//
// Any other error leaves the transaction unusable and must be returned from
// the [RunTx] callback so it rolls back (and retries, when asked to):
//
//   - fn failed with a serialization failure or deadlock
//     ([IsSerializationFailure]), which the retry loop of RunTx must see
//     rather than have it recorded as a per-item failure;
//   - fn returned [ErrRollback], or ctx is done;
//   - SAVEPOINT, ROLLBACK TO SAVEPOINT or RELEASE SAVEPOINT failed.
//
// Savepoints do not undo transaction-local settings applied before them, so
// the identity set by RunTx (role, claims) holds for every fn.
func Savepoint(ctx context.Context, q Querier, fn func() error) error {
	if _, err := q.Exec(ctx, "SAVEPOINT "+savepointName); err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	if err := fn(); err != nil {
		if IsSerializationFailure(err) || errors.Is(err, ErrRollback) || ctx.Err() != nil {
			return err
		}
		if _, rerr := q.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepointName); rerr != nil {
			return fmt.Errorf("rollback to savepoint: %w", rerr)
		}
		if _, rerr := q.Exec(ctx, "RELEASE SAVEPOINT "+savepointName); rerr != nil {
			return fmt.Errorf("release savepoint: %w", rerr)
		}
		return &RolledBackError{Err: err}
	}
	if _, err := q.Exec(ctx, "RELEASE SAVEPOINT "+savepointName); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}
