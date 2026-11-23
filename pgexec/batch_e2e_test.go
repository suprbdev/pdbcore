package pgexec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/pgexec"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// execModes are the pgx query exec modes QueryTx is exercised under: the
// default prepared-statement pipeline, the simple protocol (one
// multi-statement Query message) and unprepared extended protocol.
var execModes = []string{"cache_statement", "simple_protocol", "exec"}

// txModes are the three batch shapes QueryTx can send.
var txModes = []struct {
	name string
	opts pgexec.TxOptions
}{
	{"implicit", pgexec.TxOptions{RLS: true}},
	{"explicit", pgexec.TxOptions{RLS: true, Isolation: pgx.Serializable}},
	{"scan-before-commit", pgexec.TxOptions{RLS: true, ScanBeforeCommit: true}},
}

// onePool returns a pool with a single connection, so every call reuses
// the connection the previous call ran on (unless that one was closed).
func onePool(t *testing.T, mode string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	if mode != "" {
		m, ok := map[string]pgx.QueryExecMode{
			"cache_statement": pgx.QueryExecModeCacheStatement,
			"simple_protocol": pgx.QueryExecModeSimpleProtocol,
			"exec":            pgx.QueryExecModeExec,
		}[mode]
		if !ok {
			t.Fatalf("unknown exec mode %q", mode)
		}
		cfg.ConnConfig.DefaultQueryExecMode = m
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type sessionState struct {
	pid               int
	user, sess, role  string
	userID, superuser string
}

const stateSQL = `SELECT pg_backend_pid(), current_user, session_user, current_setting('role'),
	coalesce(current_setting('pdbq.claims.user_id', true), ''), current_setting('is_superuser')`

func scanState(row pgx.Row) (sessionState, error) {
	var s sessionState
	err := row.Scan(&s.pid, &s.user, &s.sess, &s.role, &s.userID, &s.superuser)
	return s, err
}

// assertClean checks the pooled connection carries no request identity:
// the role is reset to the login role and no claim survives.
func assertClean(t *testing.T, pool *pgxpool.Pool) sessionState {
	t.Helper()
	s, err := scanState(pool.QueryRow(context.Background(), stateSQL))
	if err != nil {
		t.Fatal(err)
	}
	if s.user != s.sess || s.role != "none" || s.userID != "" {
		t.Fatalf("identity leaked onto the pooled connection: %+v", s)
	}
	return s
}

func TestQueryTxRLSMatrix(t *testing.T) {
	for _, mode := range execModes {
		for _, tm := range txModes {
			t.Run(mode+"/"+tm.name, func(t *testing.T) {
				pool := onePool(t, mode)
				ctx := context.Background()
				for _, tc := range []struct {
					id   pgexec.Identity
					want int
				}{
					{pgexec.Identity{Role: "anonymous"}, 3},
					{pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "1", "n": 5}}, 4},
					{pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "2"}}, 3},
				} {
					var in sessionState
					var n int
					var nClaim string
					err := pgexec.QueryTx(ctx, pool, tm.opts, tc.id,
						pgexec.Stmt{SQL: stateSQL, Scan: func(r pgx.Row) (err error) { in, err = scanState(r); return err }},
						pgexec.Stmt{SQL: "SELECT count(*), coalesce(current_setting('pdbq.claims.n', true), '') FROM posts",
							Scan: func(r pgx.Row) error { return r.Scan(&n, &nClaim) }},
					)
					if err != nil {
						t.Fatal(err)
					}
					wantUID, _ := tc.id.Claims["user_id"].(string)
					if in.user != tc.id.Role || in.role != tc.id.Role || in.userID != wantUID || in.superuser != "off" {
						t.Fatalf("inside the batch: %+v, want role %s user_id %q", in, tc.id.Role, wantUID)
					}
					if n != tc.want {
						t.Fatalf("%s sees %d posts, want %d", tc.id.Role, n, tc.want)
					}
					if tc.id.Claims["n"] != nil && nClaim != "5" {
						t.Fatalf("JSON-encoded claim n = %q, want 5", nClaim)
					}
					if after := assertClean(t, pool); after.pid != in.pid {
						t.Fatalf("connection replaced after a successful batch (pid %d -> %d)", in.pid, after.pid)
					}
				}
			})
		}
	}
}

// TestQueryTxRoleMatchesSetLocalRole compares the session state the batch
// identity (set_config('role', ...)) produces with RunTx's SET LOCAL ROLE.
func TestQueryTxRoleMatchesSetLocalRole(t *testing.T) {
	pool := onePool(t, "")
	ctx := context.Background()
	id := pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "9"}}
	var viaTx, viaBatch sessionState
	err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true}, id, func(q pgexec.Querier) (err error) {
		viaTx, err = scanState(q.QueryRow(ctx, stateSQL))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = pgexec.QueryTx(ctx, pool, pgexec.TxOptions{RLS: true}, id,
		pgexec.Stmt{SQL: stateSQL, Scan: func(r pgx.Row) (err error) { viaBatch, err = scanState(r); return err }})
	if err != nil {
		t.Fatal(err)
	}
	if viaTx != viaBatch {
		t.Fatalf("SET LOCAL ROLE: %+v\nset_config('role'): %+v", viaTx, viaBatch)
	}
	assertClean(t, pool)
}

func TestQueryTxNoLeakAfterFailure(t *testing.T) {
	id := pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "1"}}
	boom := errors.New("scan veto")
	for _, mode := range execModes {
		for _, tm := range txModes {
			t.Run(mode+"/"+tm.name, func(t *testing.T) {
				pool := onePool(t, mode)
				ctx := context.Background()
				var in sessionState
				state := pgexec.Stmt{SQL: stateSQL, Scan: func(r pgx.Row) (err error) { in, err = scanState(r); return err }}

				err := pgexec.QueryTx(ctx, pool, tm.opts, id, state, pgexec.Stmt{SQL: "SELECT 1/0"})
				if c := pgexec.Classify(err, pgexec.Prod); c.SQLState != "22012" || c.Class != pgexec.UserInput {
					t.Fatalf("statement error = %v (%+v)", err, c)
				}
				if in.user != "app_user" {
					t.Fatalf("identity not applied before the failing statement: %+v", in)
				}
				if after := assertClean(t, pool); after.pid != in.pid {
					t.Fatalf("connection replaced after a statement error (pid %d -> %d)", in.pid, after.pid)
				}

				err = pgexec.QueryTx(ctx, pool, tm.opts, id, state,
					pgexec.Stmt{SQL: "SELECT 1", Scan: func(pgx.Row) error { return boom }})
				if err != boom { //nolint:errorlint // Scan errors come back as-is
					t.Fatalf("scan error = %v, want the sentinel unwrapped", err)
				}
				assertClean(t, pool)

				err = pgexec.QueryTx(ctx, pool, tm.opts, pgexec.Identity{Role: "no_such_role_x"}, state)
				if c := pgexec.Classify(err, pgexec.Prod); c.SQLState != "22023" || c.Class != pgexec.UserInput {
					t.Fatalf("unknown role = %v (%+v)", err, c)
				}
				assertClean(t, pool)

				cctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
				err = pgexec.QueryTx(cctx, pool, tm.opts, id, state, pgexec.Stmt{SQL: "SELECT pg_sleep(5)"})
				cancel()
				if err == nil {
					t.Fatal("cancelled batch succeeded")
				}
				assertClean(t, pool)
			})
		}
	}
}

func TestQueryTxAtomic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	id := pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "1"}}
	prefix := fmt.Sprintf("qtx-%d-", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE email LIKE $1", prefix+"%") })
	count := func(email string) int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE email = $1", email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	insert := func(email string) pgexec.Stmt {
		return pgexec.Stmt{SQL: "INSERT INTO users (email) VALUES ($1)", Args: []any{email}}
	}
	veto := errors.New("veto")
	for _, tm := range txModes {
		t.Run(tm.name, func(t *testing.T) {
			ok := prefix + tm.name + "-ok@example.com"
			if err := pgexec.QueryTx(ctx, pool, tm.opts, id, insert(ok)); err != nil || count(ok) != 1 {
				t.Fatalf("commit: err=%v rows=%d", err, count(ok))
			}

			failed := prefix + tm.name + "-failed@example.com"
			err := pgexec.QueryTx(ctx, pool, tm.opts, id, insert(failed), pgexec.Stmt{SQL: "SELECT 1/0"})
			if err == nil || count(failed) != 0 {
				t.Fatalf("a failed batch must roll back the statements before the failure: err=%v rows=%d", err, count(failed))
			}

			dup := prefix + tm.name + "-dup@example.com"
			err = pgexec.QueryTx(ctx, pool, tm.opts, id, insert(dup), insert(ok))
			if c := pgexec.Classify(err, pgexec.Prod); c.Class != pgexec.Conflict || c.SQLState != "23505" || count(dup) != 0 {
				t.Fatalf("unique violation: %+v rows=%d", c, count(dup))
			}

			vetoed := prefix + tm.name + "-vetoed@example.com"
			err = pgexec.QueryTx(ctx, pool, tm.opts, id, pgexec.Stmt{
				SQL: "INSERT INTO users (email) VALUES ($1) RETURNING id", Args: []any{vetoed},
				Scan: func(pgx.Row) error { return veto },
			})
			if !errors.Is(err, veto) {
				t.Fatalf("veto err = %v", err)
			}
			want := 1
			if tm.opts.ScanBeforeCommit {
				want = 0
			}
			if n := count(vetoed); n != want {
				t.Fatalf("after a Scan error %d rows, want %d (ScanBeforeCommit=%v)", n, want, tm.opts.ScanBeforeCommit)
			}
		})
	}
}

func TestQueryTxPermissionDenied(t *testing.T) {
	pool := testPool(t)
	for _, tm := range txModes {
		err := pgexec.QueryTx(context.Background(), pool, tm.opts, pgexec.Identity{Role: "anonymous"},
			pgexec.Stmt{SQL: "INSERT INTO users (email) VALUES ('nobody@example.com')"})
		if c := pgexec.Classify(err, pgexec.Prod); c.Class != pgexec.Permission || c.SQLState != "42501" || c.Message != "permission denied" {
			t.Fatalf("%s: %+v", tm.name, c)
		}
	}
}

// TestQueryTxCommitTimeError checks an error raised by COMMIT itself (a
// deferred constraint) is returned and nothing is written.
func TestQueryTxCommitTimeError(t *testing.T) {
	for _, tm := range txModes {
		t.Run(tm.name, func(t *testing.T) {
			pool := onePool(t, "")
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `CREATE TEMP TABLE deferred_u (v int UNIQUE DEFERRABLE INITIALLY DEFERRED)`); err != nil {
				t.Fatal(err)
			}
			opts := tm.opts
			opts.RLS = false
			err := pgexec.QueryTx(ctx, pool, opts, pgexec.Identity{},
				pgexec.Stmt{SQL: "INSERT INTO deferred_u VALUES (1)"},
				pgexec.Stmt{SQL: "INSERT INTO deferred_u VALUES (1)"})
			if c := pgexec.Classify(err, pgexec.Prod); c.SQLState != "23505" {
				t.Fatalf("commit-time violation = %v", err)
			}
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM deferred_u").Scan(&n); err != nil || n != 0 {
				t.Fatalf("rows=%d err=%v", n, err)
			}
		})
	}
}

func TestQueryTxRetryProbe(t *testing.T) {
	for _, tm := range txModes {
		t.Run(tm.name, func(t *testing.T) {
			pool := onePool(t, "")
			ctx := context.Background()
			alignOdd := func() {
				var seq int64
				for {
					if err := pool.QueryRow(ctx, "SELECT nextval('retry_probe_seq')").Scan(&seq); err != nil {
						t.Fatal(err)
					}
					if seq%2 == 0 {
						return
					}
				}
			}
			id := pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "1"}}
			probe := func(retries int) (scans int, uid string, err error) {
				opts := tm.opts
				opts.MaxRetries, opts.Log = retries, quietLog
				err = pgexec.QueryTx(ctx, pool, opts, id, pgexec.Stmt{
					SQL: "SELECT retry_probe(), current_setting('pdbq.claims.user_id')",
					Scan: func(r pgx.Row) error {
						scans++
						var v int
						return r.Scan(&v, &uid)
					},
				})
				return scans, uid, err
			}
			alignOdd()
			if _, _, err := probe(0); !pgexec.IsSerializationFailure(err) {
				t.Fatalf("without retries: err = %v, want 40001", err)
			}
			assertClean(t, pool)
			alignOdd()
			scans, uid, err := probe(1)
			if err != nil || scans != 2 || uid != "1" {
				t.Fatalf("with one retry: scans=%d uid=%q err=%v (want the Scan re-invoked with the identity reapplied)", scans, uid, err)
			}
			assertClean(t, pool)
		})
	}
}

// TestQueryTxRejectsOpenTransaction checks a statement that opens a
// transaction cannot carry the identity past the batch: a leading BEGIN is
// rejected before anything is sent, and one hidden behind another
// statement (possible with the simple protocol) is rolled back.
func TestQueryTxRejectsOpenTransaction(t *testing.T) {
	for _, tm := range txModes {
		t.Run(tm.name, func(t *testing.T) {
			pool := onePool(t, "simple_protocol")
			ctx := context.Background()
			err := pgexec.QueryTx(ctx, pool, tm.opts, pgexec.Identity{Role: "anonymous"}, pgexec.Stmt{SQL: "BEGIN"})
			if err == nil || !strings.Contains(err.Error(), "must not be transaction control") {
				t.Fatalf("leading BEGIN: err = %v", err)
			}
			err = pgexec.QueryTx(ctx, pool, tm.opts, pgexec.Identity{Role: "anonymous"}, pgexec.Stmt{SQL: "SELECT 1; BEGIN"})
			if tm.opts.Isolation == "" && !tm.opts.ScanBeforeCommit && !errors.Is(err, pgexec.ErrTxStillOpen) {
				t.Fatalf("hidden BEGIN: err = %v, want ErrTxStillOpen", err)
			}
			assertClean(t, pool)
		})
	}
}

func TestRunTxClaimsOneStatement(t *testing.T) {
	pool := onePool(t, "")
	ctx := context.Background()
	id := pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "3", "roles": []string{"a", "b"}, "exp": 17}}
	err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true}, id, func(q pgexec.Querier) error {
		var uid, roles, exp string
		if err := q.QueryRow(ctx, `SELECT current_setting('pdbq.claims.user_id'), current_setting('pdbq.claims.roles'),
			current_setting('pdbq.claims.exp')`).Scan(&uid, &roles, &exp); err != nil {
			return err
		}
		if uid != "3" || roles != `["a","b"]` || exp != "17" {
			t.Errorf("claims = %q %q %q", uid, roles, exp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertClean(t, pool)
}

// TestQueryTxCallAndDoStayAtomic shows a procedure or DO block that
// COMMITs ends the implicit pipeline transaction (and with it the
// identity), then checks QueryTx gives such batches an explicit block in
// which the COMMIT fails instead.
func TestQueryTxCallAndDoStayAtomic(t *testing.T) {
	pool := onePool(t, "")
	ctx := context.Background()
	const schema = "pgexec_call_test"
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	for _, s := range []string{
		"DROP SCHEMA IF EXISTS " + schema + " CASCADE",
		"CREATE SCHEMA " + schema,
		"GRANT USAGE ON SCHEMA " + schema + " TO anonymous",
		"CREATE PROCEDURE " + schema + ".commits() LANGUAGE plpgsql AS $$ BEGIN COMMIT; END $$",
		"GRANT EXECUTE ON PROCEDURE " + schema + ".commits() TO anonymous",
	} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	nonAtomic := []string{
		"CALL " + schema + ".commits()",
		"DO $$ BEGIN COMMIT; END $$",
	}
	for _, sql := range nonAtomic {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b := &pgx.Batch{}
		b.Queue("SELECT set_config('role', 'anonymous', true)")
		b.Queue(sql)
		var after string
		b.Queue("SELECT current_user").QueryRow(func(r pgx.Row) error { return r.Scan(&after) })
		err = c.Conn().SendBatch(ctx, b).Close()
		c.Release()
		switch {
		case err != nil:
			if cl := pgexec.Classify(err, pgexec.Prod); cl.SQLState != "2D000" {
				t.Fatalf("raw pipeline %q: %v", sql, err)
			}
			t.Logf("this server keeps %q atomic in a pipeline", sql)
		case after == "anonymous":
			t.Fatalf("raw pipeline %q: COMMIT did not end the transaction", sql)
		default:
			t.Logf("raw pipeline %q: the next statement ran as %q", sql, after)
		}

		var user string
		err = pgexec.QueryTx(ctx, pool, pgexec.TxOptions{RLS: true}, pgexec.Identity{Role: "anonymous"},
			pgexec.Stmt{SQL: sql},
			pgexec.Stmt{SQL: "SELECT current_user", Scan: func(r pgx.Row) error { return r.Scan(&user) }})
		if c := pgexec.Classify(err, pgexec.Prod); c.SQLState != "2D000" {
			t.Fatalf("%q in QueryTx: err = %v, want 2D000 invalid transaction termination", sql, err)
		}
		if user != "" {
			t.Fatalf("%q: a statement after the COMMIT ran as %q", sql, user)
		}
		assertClean(t, pool)
	}
}

func TestIdentityRejectsRoleNone(t *testing.T) {
	pool := onePool(t, "")
	ctx := context.Background()
	for _, role := range []string{"none", "NONE", "pg_read_all_data"} {
		id := pgexec.Identity{Role: role}
		ran := false
		err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true}, id, func(pgexec.Querier) error { ran = true; return nil })
		if err == nil || ran {
			t.Fatalf("RunTx role %q: err=%v ran=%v", role, err, ran)
		}
		err = pgexec.QueryTx(ctx, pool, pgexec.TxOptions{RLS: true}, id,
			pgexec.Stmt{SQL: "SELECT current_user", Scan: func(pgx.Row) error { ran = true; return nil }})
		if err == nil || ran {
			t.Fatalf("QueryTx role %q: err=%v ran=%v", role, err, ran)
		}
		assertClean(t, pool)
	}
}
