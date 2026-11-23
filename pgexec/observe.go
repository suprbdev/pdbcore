package pgexec

import (
	"context"
	"errors"
	"time"
)

// Operation names reported in [QueryEvent.Op] by the transaction helpers.
const (
	OpRunTx   = "run_tx"
	OpQueryTx = "query_tx"
)

// QueryEvent describes one finished database operation.
type QueryEvent struct {
	// Op names the operation: [OpRunTx] or [OpQueryTx] for one attempt of
	// a transaction helper, or a caller-chosen name for a statement the
	// caller runs itself (see [Observe]).
	Op string
	// Attempt is 0 for the first attempt and n for the n-th retry after a
	// serialization failure or deadlock.
	Attempt int
	// Duration is the wall-clock time of the attempt, from acquiring the
	// connection or beginning the transaction to the commit or failure.
	Duration time.Duration
	// Err is the attempt's error, as the helper would return it; nil on
	// success or when the caller requested a rollback with [ErrRollback].
	Err error
}

// Observer receives a [QueryEvent] after each attempt of [RunTx] or
// [QueryTx] (set through [TxOptions.Observer]) and for each statement a
// caller reports with [Observe]. ObserveQuery runs synchronously on the
// request goroutine, possibly from many goroutines at once, so it must be
// safe for concurrent use and should return quickly.
type Observer interface {
	ObserveQuery(ctx context.Context, ev QueryEvent)
}

// Observe reports a statement the caller ran outside [RunTx] and [QueryTx]
// (for example a single QueryRow on the pool): Duration is the time since
// start. A nil o does nothing, so callers need no guard.
func Observe(ctx context.Context, o Observer, op string, start time.Time, err error) {
	if o == nil {
		return
	}
	o.ObserveQuery(ctx, QueryEvent{Op: op, Duration: time.Since(start), Err: err})
}

func observeAttempt(ctx context.Context, o Observer, op string, attempt int, start time.Time, err error) {
	if o == nil {
		return
	}
	if errors.Is(err, ErrRollback) {
		err = nil
	}
	o.ObserveQuery(ctx, QueryEvent{Op: op, Attempt: attempt, Duration: time.Since(start), Err: err})
}
