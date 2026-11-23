package metrics

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/pgexec"
)

// DBObserver returns the [pgexec.Observer] that records database operation
// durations, errors by SQLSTATE class and retries. Set it as
// pgexec.TxOptions.Observer and pass it to pgexec.Observe for statements
// run outside the transaction helpers. pgx.ErrNoRows is not counted as an
// error, so a QueryRow that found nothing can be reported as-is. On a nil
// Metrics it returns nil, so observation is skipped entirely.
func (m *Metrics) DBObserver() pgexec.Observer {
	if m == nil {
		return nil
	}
	return dbObserver{m}
}

type dbObserver struct{ m *Metrics }

func (o dbObserver) ObserveQuery(_ context.Context, ev pgexec.QueryEvent) {
	op := ev.Op
	if op == "" {
		op = "unknown"
	}
	o.m.dbDuration.WithLabelValues(op).Observe(ev.Duration.Seconds())
	if ev.Attempt > 0 {
		o.m.dbRetries.WithLabelValues(op).Inc()
	}
	if ev.Err != nil && !errors.Is(ev.Err, pgx.ErrNoRows) {
		o.m.dbErrors.WithLabelValues(op, SQLStateClass(ev.Err)).Inc()
	}
}

// SQLStateClass is the error label of [Metrics.DBObserver]: the two-
// character SQLSTATE class of the first *pgconn.PgError in err's chain
// ("23" for integrity violations, "40" for serialization failures, ...),
// "canceled" or "timeout" for a canceled or expired context or a pgconn
// timeout, "other" for any other non-nil error and "" for nil.
func SQLStateClass(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) == 5 {
		return pgErr.Code[:2]
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), pgconn.Timeout(err):
		return "timeout"
	}
	return "other"
}
