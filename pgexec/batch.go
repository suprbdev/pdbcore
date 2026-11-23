package pgexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnAcquirer hands out a dedicated pooled connection; satisfied by
// *pgxpool.Pool.
type ConnAcquirer interface {
	Acquire(ctx context.Context) (*pgxpool.Conn, error)
}

// Stmt is one statement of a [QueryTx] transaction. SQL must be a single
// statement without transaction control (BEGIN, START, COMMIT, END,
// ROLLBACK, ABORT, SAVEPOINT, RELEASE): QueryTx rejects one that starts
// with such a keyword, and anything that ended the transaction early would
// run the remaining statements as the connection's login role.
type Stmt struct {
	SQL  string
	Args []any
	// Scan, when non-nil, receives the statement's first result row once
	// the server has answered, in queue order. row.Scan returns
	// pgx.ErrNoRows when the statement produced no row, and the
	// statement's error when it failed. Without TxOptions.ScanBeforeCommit
	// the transaction has already committed when Scan runs, so a Scan
	// error cannot undo the statements. nil discards the result.
	Scan func(row pgx.Row) error
}

// ErrTxStillOpen is returned by [QueryTx] when a statement left a
// transaction open (it ran BEGIN, for example). The transaction is rolled
// back.
var ErrTxStillOpen = errors.New("pgexec: transaction still open after the batch")

// ErrRollback is returned by a [Stmt.Scan] callback, or by the function
// given to [RunTx], to roll the transaction back on purpose (a violated
// precondition, a dry run). QueryTx and RunTx return it as-is, so callers
// can detect it with errors.Is, and report the attempt to the [Observer]
// with a nil error: a requested rollback is not a database failure. For
// QueryTx the statements are only undone when [TxOptions.ScanBeforeCommit]
// is set.
var ErrRollback = errors.New("pgexec: rollback requested")

// QueryTx runs stmts in one transaction under identity id (when opts.RLS
// is set) and sends the whole transaction to the server as a single
// pipelined batch, so it costs one network round trip where [RunTx] costs
// at least four:
//
//   - opts.Isolation "" sends [identity, stmts...] in the implicit
//     transaction PostgreSQL runs a pipeline in, at the server's
//     default_transaction_isolation. It commits when the batch ends and
//     rolls back completely if any statement fails.
//   - Any other opts.Isolation sends [BEGIN ISOLATION LEVEL ..., identity,
//     stmts..., COMMIT]. A failure costs a second round trip for ROLLBACK.
//   - opts.ScanBeforeCommit sends [BEGIN ..., identity, stmts...] and
//     then COMMIT only after every Scan returned nil (two round trips), so
//     a Scan error rolls the statements back.
//
// The identity is applied in one statement with transaction-local
// set_config (the role through set_config('role', ...), which is what SET
// LOCAL ROLE runs), so it ends with the transaction on every path: commit,
// statement error, Scan error, cancellation. The role must pass
// [ValidRole]; an invalid role fails before anything is sent. Claims are
// encoded as in [ApplyIdentity].
//
// Once a statement fails PostgreSQL skips the rest of the batch, so later
// Scan callbacks are not called. The first error is returned: a Scan error
// as-is, a statement error raw (a *pgconn.PgError for [Classify]) and an
// identity error wrapped with its *pgconn.PgError in the chain. On a
// serialization failure or deadlock the whole batch is sent again, up to
// opts.MaxRetries times, so Scan may run more than once per statement and
// must reset any state it fills.
//
// Statements must not contain transaction control (see [Stmt]). A batch
// with a statement starting with CALL or DO always gets an explicit BEGIN
// … COMMIT, because a procedure or DO block that COMMITs would otherwise
// end the implicit transaction and the identity with it; inside the
// explicit block such a COMMIT fails instead. If a transaction is still
// open after the batch, QueryTx rolls it back and returns
// [ErrTxStillOpen].
func QueryTx(ctx context.Context, db ConnAcquirer, opts TxOptions, id Identity, stmts ...Stmt) error {
	p, err := newBatchPlan(opts, id)
	if err != nil {
		return err
	}
	if err := p.checkStmts(stmts); err != nil {
		return err
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	for attempt := 0; ; attempt++ {
		start := time.Now()
		err := p.runOnce(ctx, db, stmts)
		observeAttempt(ctx, opts.Observer, OpQueryTx, attempt, start, err)
		if err == nil || attempt >= opts.MaxRetries || !IsSerializationFailure(err) || ctx.Err() != nil {
			return err
		}
		log.Warn("retrying after serialization failure",
			"attempt", attempt+1, "max_retries", opts.MaxRetries)
	}
}

// batchPlan is the fixed part of a QueryTx batch: everything except the
// caller's statements.
type batchPlan struct {
	begin         string
	identity      string
	identityArgs  []any
	commitInBatch bool
	commitAfter   bool
}

func newBatchPlan(opts TxOptions, id Identity) (*batchPlan, error) {
	p := &batchPlan{}
	if opts.Isolation != "" || opts.ScanBeforeCommit {
		begin, err := beginSQL(opts.Isolation)
		if err != nil {
			return nil, err
		}
		p.begin = begin
		p.commitAfter = opts.ScanBeforeCommit
		p.commitInBatch = !opts.ScanBeforeCommit
	}
	if !opts.RLS {
		return p, nil
	}
	names, values, err := claimSettings(id)
	if err != nil {
		return nil, err
	}
	switch {
	case id.Role != "":
		if !ValidRole(id.Role) {
			return nil, fmt.Errorf("invalid role %q", id.Role)
		}
		if names == nil {
			names, values = []string{}, []string{}
		}
		p.identity, p.identityArgs = identitySQL, []any{id.Role, names, values}
	case len(names) > 0:
		p.identity, p.identityArgs = claimsSQL, []any{names, values}
	}
	return p, nil
}

// checkStmts rejects transaction control and switches an implicit plan to
// an explicit BEGIN ... COMMIT when a statement is a CALL or DO.
func (p *batchPlan) checkStmts(stmts []Stmt) error {
	for _, s := range stmts {
		switch kw := firstKeyword(s.SQL); kw {
		case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT", "SAVEPOINT", "RELEASE":
			return fmt.Errorf("pgexec: QueryTx statement must not be transaction control (%s)", kw)
		case "CALL", "DO":
			if p.begin == "" {
				p.begin, p.commitInBatch = "BEGIN", true
			}
		}
	}
	return nil
}

func beginSQL(iso pgx.TxIsoLevel) (string, error) {
	switch iso {
	case "":
		return "BEGIN", nil
	case pgx.Serializable, pgx.RepeatableRead, pgx.ReadCommitted, pgx.ReadUncommitted:
		return "BEGIN ISOLATION LEVEL " + strings.ToUpper(string(iso)), nil
	}
	return "", fmt.Errorf("invalid isolation level %q", iso)
}

func (p *batchPlan) batch(stmts []Stmt) *pgx.Batch {
	b := &pgx.Batch{QueuedQueries: make([]*pgx.QueuedQuery, 0, len(stmts)+3)}
	if p.begin != "" {
		b.Queue(p.begin)
	}
	if p.identity != "" {
		b.Queue(p.identity, p.identityArgs...)
	}
	for _, s := range stmts {
		b.Queue(s.SQL, s.Args...)
	}
	if p.commitInBatch {
		b.Queue("COMMIT")
	}
	return b
}

func (p *batchPlan) runOnce(ctx context.Context, db ConnAcquirer, stmts []Stmt) error {
	pc, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer pc.Release()
	conn := pc.Conn()
	err = p.read(conn.SendBatch(ctx, p.batch(stmts)), stmts)
	if err == nil && p.commitAfter {
		_, err = conn.Exec(ctx, "COMMIT")
	}
	if conn.IsClosed() || conn.PgConn().TxStatus() == 'I' {
		return err
	}
	abandonTx(ctx, conn)
	if err == nil {
		err = ErrTxStillOpen
	}
	return err
}

func (p *batchPlan) read(br pgx.BatchResults, stmts []Stmt) (err error) {
	defer func() {
		if cerr := br.Close(); err == nil {
			err = cerr
		}
	}()
	if p.begin != "" {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	if p.identity != "" {
		if _, err := br.Exec(); err != nil {
			// pgx reports a failure to prepare any statement of the batch
			// on the first result read, which may be this one.
			if errors.As(err, new(pgx.ErrPreprocessingBatch)) {
				return err
			}
			return fmt.Errorf("set identity: %w", err)
		}
	}
	for _, s := range stmts {
		if s.Scan == nil {
			if _, err := br.Exec(); err != nil {
				return err
			}
			continue
		}
		rows, _ := br.Query()
		scanErr := s.Scan(batchRow{rows})
		rows.Close()
		if scanErr != nil {
			return scanErr
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if p.commitInBatch {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// firstKeyword returns the first SQL word of sql in upper case, skipping
// whitespace, -- comments and (nested) /* */ comments.
func firstKeyword(sql string) string {
	i := 0
	for i < len(sql) {
		switch {
		case sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n' || sql[i] == '\r' || sql[i] == '\f' || sql[i] == '\v':
			i++
		case strings.HasPrefix(sql[i:], "--"):
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				return ""
			}
		case strings.HasPrefix(sql[i:], "/*"):
			depth := 0
			for i < len(sql) {
				if strings.HasPrefix(sql[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(sql[i:], "*/") {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		default:
			j := i
			for j < len(sql) && (sql[j] == '_' || (sql[j]|0x20 >= 'a' && sql[j]|0x20 <= 'z')) {
				j++
			}
			return strings.ToUpper(sql[i:j])
		}
	}
	return ""
}

// abandonTx ends a transaction QueryTx could not finish: ROLLBACK, or
// closing the connection when that is impossible, so the pool never hands
// out a connection inside a transaction that carries a request identity.
func abandonTx(ctx context.Context, conn *pgx.Conn) {
	if ctx.Err() == nil {
		if _, err := conn.Exec(ctx, "ROLLBACK"); err == nil && conn.PgConn().TxStatus() == 'I' {
			return
		}
	}
	_ = conn.Close(ctx)
}

// batchRow is pgx.Row over one batch result, with QueryRow semantics.
type batchRow struct{ rows pgx.Rows }

func (r batchRow) Scan(dest ...any) error {
	if err := r.rows.Err(); err != nil {
		return err
	}
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	err := r.rows.Scan(dest...)
	r.rows.Close()
	if err != nil {
		return err
	}
	return r.rows.Err()
}
