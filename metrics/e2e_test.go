package metrics

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/pgexec"
)

func TestDBMetricsAgainstPostgres(t *testing.T) {
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	pool, err := pgexec.Connect(ctx, pgexec.PoolConfig{URL: url, MaxConns: 3, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m := mustNew(t, Options{Namespace: "e2e"})
	if err := m.RegisterPool(pool); err != nil {
		t.Fatal(err)
	}
	opts := pgexec.TxOptions{Observer: m.DBObserver()}

	var one int
	if err := pgexec.QueryTx(ctx, pool, opts, pgexec.Identity{}, pgexec.Stmt{
		SQL: "SELECT 1", Scan: func(row pgx.Row) error { return row.Scan(&one) },
	}); err != nil || one != 1 {
		t.Fatalf("QueryTx = %d, %v", one, err)
	}
	err = pgexec.QueryTx(ctx, pool, opts, pgexec.Identity{}, pgexec.Stmt{SQL: "SELECT * FROM no_such_table_metrics"})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		t.Fatalf("want undefined_table, got %v", err)
	}
	err = pgexec.RunTx(ctx, pool, opts, pgexec.Identity{}, func(q pgexec.Querier) error {
		_, err := q.Exec(ctx, "SELECT 1/0")
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "22012" {
		t.Fatalf("want division_by_zero, got %v", err)
	}
	start := time.Now()
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&one)
	pgexec.Observe(ctx, opts.Observer, "query", start, err)

	got := scrape(t, m.Handler())
	want(t, got, `e2e_db_duration_seconds_count{op="query_tx"}`, 2)
	want(t, got, `e2e_db_duration_seconds_count{op="run_tx"}`, 1)
	want(t, got, `e2e_db_duration_seconds_count{op="query"}`, 1)
	want(t, got, `e2e_db_errors_total{op="query_tx",sqlstate_class="42"}`, 1)
	want(t, got, `e2e_db_errors_total{op="run_tx",sqlstate_class="22"}`, 1)
	want(t, got, "e2e_db_pool_max_conns", 3)
	if got["e2e_db_pool_acquires_total"] < 4 {
		t.Errorf("acquires_total = %v, want >= 4", got["e2e_db_pool_acquires_total"])
	}
	if got["e2e_db_pool_total_conns"] < 1 || got["e2e_db_pool_new_conns_total"] < 1 {
		t.Errorf("total_conns = %v, new_conns_total = %v, want >= 1", got["e2e_db_pool_total_conns"], got["e2e_db_pool_new_conns_total"])
	}
	want(t, got, "e2e_db_pool_acquired_conns", 0)
}
