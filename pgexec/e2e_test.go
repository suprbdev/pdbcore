package pgexec_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/pgexec"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	pool, err := pgexec.Connect(context.Background(), pgexec.PoolConfig{
		URL: url, MaxConns: 4, ConnectTimeout: 5 * time.Second, StatementTimeout: 7 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestConnectAppliesStatementTimeout(t *testing.T) {
	pool := testPool(t)
	var st string
	if err := pool.QueryRow(context.Background(), "SHOW statement_timeout").Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "7s" {
		t.Fatalf("statement_timeout = %q, want 7s", st)
	}
}

func TestRunTxRLSMatrix(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	count := func(id pgexec.Identity) int {
		var n int
		err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true}, id, func(q pgexec.Querier) error {
			var role, uid string
			if err := q.QueryRow(ctx, "SELECT current_user, coalesce(current_setting('jwt.claims.user_id', true), '')").Scan(&role, &uid); err != nil {
				return err
			}
			if role != id.Role {
				t.Errorf("current_user = %q, want %q", role, id.Role)
			}
			if want, _ := id.Claims["user_id"].(string); uid != want {
				t.Errorf("claim user_id = %q, want %q", uid, want)
			}
			return q.QueryRow(ctx, "SELECT count(*) FROM posts").Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The fixture RLS matrix: anonymous sees 3 published posts, app_user #1
	// additionally their own draft (4), app_user #2 only published (3).
	if n := count(pgexec.Identity{Role: "anonymous"}); n != 3 {
		t.Fatalf("anonymous sees %d posts, want 3", n)
	}
	if n := count(pgexec.Identity{Role: "app_user", ClaimsPrefix: "jwt.claims", Claims: map[string]any{"user_id": "1"}}); n != 4 {
		t.Fatalf("app_user#1 sees %d posts, want 4", n)
	}
	if n := count(pgexec.Identity{Role: "app_user", ClaimsPrefix: "jwt.claims", Claims: map[string]any{"user_id": "2"}}); n != 3 {
		t.Fatalf("app_user#2 sees %d posts, want 3", n)
	}
	// SET LOCAL: identity must not leak to the pooled connection.
	var user string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if user == "anonymous" || user == "app_user" {
		t.Fatalf("role leaked out of the transaction: %s", user)
	}
}

func TestClaimsJSONSettingVisibleToPolicies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	admin, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Release()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS json_claims_probe (owner text NOT NULL)`,
		`TRUNCATE json_claims_probe`,
		`INSERT INTO json_claims_probe VALUES ('u1'), ('u2'), ('u2')`,
		`ALTER TABLE json_claims_probe ENABLE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS own ON json_claims_probe`,
		`CREATE POLICY own ON json_claims_probe FOR SELECT TO app_user
			USING (owner = current_setting('request.jwt.claims', true)::json->>'sub')`,
		`GRANT SELECT ON json_claims_probe TO app_user`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS json_claims_probe`) })
	count := func(id pgexec.Identity) (n int, doc string) {
		err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true}, id, func(q pgexec.Querier) error {
			return q.QueryRow(ctx, `SELECT (SELECT count(*) FROM json_claims_probe), current_setting('request.jwt.claims', true)`).Scan(&n, &doc)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n, doc
	}
	id := pgexec.Identity{Role: "app_user", ClaimsJSONSetting: "request.jwt.claims", Claims: map[string]any{"sub": "u2", "role": "app_user"}}
	if n, doc := count(id); n != 2 || doc != `{"role":"app_user","sub":"u2"}` {
		t.Fatalf("u2 sees %d rows with claims %s, want 2", n, doc)
	}
	id.Claims = nil
	if n, doc := count(id); n != 0 || doc != "{}" {
		t.Fatalf("no claims: %d rows with claims %q, want 0 and {}", n, doc)
	}
	var batched int
	err = pgexec.QueryTx(ctx, pool, pgexec.TxOptions{RLS: true},
		pgexec.Identity{Role: "app_user", ClaimsJSONSetting: "request.jwt.claims", Claims: map[string]any{"sub": "u1"}},
		pgexec.Stmt{SQL: `SELECT count(*) FROM json_claims_probe`, Scan: func(r pgx.Row) error { return r.Scan(&batched) }})
	if err != nil || batched != 1 {
		t.Fatalf("QueryTx: %d rows, err %v, want 1", batched, err)
	}
}

func TestRunTxRollbackOnError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	email := "rollback-" + time.Now().Format("150405.000") + "@example.com"
	boom := errors.New("abort")
	err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{}, pgexec.Identity{}, func(q pgexec.Querier) error {
		if _, err := q.Exec(ctx, "INSERT INTO users (email) VALUES ($1)", email); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE email = $1", email).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("rollback leaked the inserted row")
	}
}

func TestRunTxRetryProbe(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// retry_probe() raises SQLSTATE 40001 on odd sequence values; the
	// sequence advance survives the rollback, so a single retry always
	// succeeds. Align to an odd value first so the no-retry case fails.
	var seq int64
	for {
		if err := pool.QueryRow(ctx, "SELECT nextval('retry_probe_seq')").Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if seq%2 == 0 {
			break
		}
	}
	probe := func(retries int) (int, error) {
		var out int
		err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{MaxRetries: retries, Isolation: pgx.ReadCommitted, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, pgexec.Identity{},
			func(q pgexec.Querier) error { return q.QueryRow(ctx, "SELECT retry_probe()").Scan(&out) })
		return out, err
	}
	if _, err := probe(0); !pgexec.IsSerializationFailure(err) {
		t.Fatalf("without retries: err = %v, want 40001", err)
	}
	// Sequence is now odd+1 = even after the failed call advanced it; the
	// next call succeeds directly, so consume once more to land on odd.
	if _, err := pool.Exec(ctx, "SELECT nextval('retry_probe_seq')"); err != nil {
		t.Fatal(err)
	}
	if v, err := probe(1); err != nil || v <= 0 {
		t.Fatalf("with one retry: v=%d err=%v", v, err)
	}
}

func TestClassifyRealErrors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, "INSERT INTO users (email) VALUES ('ada@example.com')")
	c := pgexec.Classify(err, pgexec.Prod)
	if c.Class != pgexec.Conflict || c.SQLState != "23505" || c.Message == "" {
		t.Fatalf("unique violation = %+v", c)
	}
	if s := pgexec.Classify(err, pgexec.Strict); s.Message != "constraint violation" {
		t.Fatalf("strict = %+v", s)
	}
	err = pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true}, pgexec.Identity{Role: "anonymous"}, func(q pgexec.Querier) error {
		_, err := q.Exec(ctx, "INSERT INTO users (email) VALUES ('nobody@example.com')")
		return err
	})
	if c := pgexec.Classify(err, pgexec.Prod); c.Class != pgexec.Permission || c.SQLState != "42501" || c.Message != "permission denied" {
		t.Fatalf("permission = %+v", c)
	}
	_, err = pool.Exec(ctx, "SELECT 'abc'::integer")
	if c := pgexec.Classify(err, pgexec.Prod); c.Class != pgexec.UserInput || c.SQLState != "22P02" {
		t.Fatalf("data exception = %+v", c)
	}
	tctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = pool.Exec(tctx, "SELECT pg_sleep(2)")
	if c := pgexec.Classify(err, pgexec.Prod); c.Class != pgexec.Timeout {
		t.Fatalf("deadline = %+v (err %v)", c, err)
	}
}

// TestClassifyRestrictViolation checks that an ON DELETE RESTRICT violation
// is a Conflict with its message passed through on every server: SQLSTATE
// 23001 from PostgreSQL 18, 23503 before.
func TestClassifyRestrictViolation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var num int
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TEMP TABLE restrict_parent (id int PRIMARY KEY)`,
		`CREATE TEMP TABLE restrict_child (parent_id int REFERENCES restrict_parent ON DELETE RESTRICT)`,
		`INSERT INTO restrict_parent VALUES (1)`,
		`INSERT INTO restrict_child VALUES (1)`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM restrict_parent`)
	want := "23503"
	if num >= 180000 {
		want = "23001"
	}
	c := pgexec.Classify(err, pgexec.Prod)
	if c.Class != pgexec.Conflict || c.SQLState != want {
		t.Fatalf("restrict violation = %+v, want Conflict %s with the PG message", c, want)
	}
	if c.Message != c.PG.Message {
		t.Fatalf("message %q not passed through (PG: %q)", c.Message, c.PG.Message)
	}
}

func TestSavepointPartialCommit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	stamp := time.Now().Format("150405.000000")
	good1, good2, dup := "sp-a-"+stamp+"@example.com", "sp-b-"+stamp+"@example.com", "sp-a-"+stamp+"@example.com"
	var failures []error
	err := pgexec.RunTx(ctx, pool, pgexec.TxOptions{}, pgexec.Identity{}, func(q pgexec.Querier) error {
		failures = nil
		for _, email := range []string{good1, dup, good2} {
			err := pgexec.Savepoint(ctx, q, func() error {
				_, err := q.Exec(ctx, "INSERT INTO users (email) VALUES ($1)", email)
				return err
			})
			var rb *pgexec.RolledBackError
			if errors.As(err, &rb) {
				failures = append(failures, rb.Err)
				continue
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly the duplicate", failures)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE email IN ($1, $2)", good1, good2).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("committed %d rows, want 2", n)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE email IN ($1, $2)", good1, good2); err != nil {
		t.Fatal(err)
	}
}
