package pgexec_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/pgexec"
)

const benchSQL = `SELECT jsonb_build_object('id', t.id, 'email', t.email, 'uid', current_setting('pdbq.claims.user_id', true)) FROM users AS t WHERE t.id = $1`

var benchIdentities = []struct {
	name string
	id   pgexec.Identity
}{
	{"anonymous", pgexec.Identity{Role: "anonymous"}},
	{"role+2claims", pgexec.Identity{Role: "app_user", Claims: map[string]any{"user_id": "1", "role": "app_user"}}},
	{"role+7claims", pgexec.Identity{Role: "app_user", Claims: map[string]any{
		"user_id": "1", "role": "app_user", "sub": "1", "iss": "https://issuer.example", "aud": "pdb", "exp": 1900000000, "iat": 1800000000,
	}}},
}

// BenchmarkIdentity measures one RLS read (identity + one statement) per
// op through each execution path against PDBCORE_TEST_DATABASE_URL.
// Serial ops approximate single-request latency; the parallel variants
// share a 16-connection pool.
func BenchmarkIdentity(b *testing.B) {
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		b.Skip("PDBCORE_TEST_DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		b.Fatal(err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	paths := []struct {
		name string
		run  func(id pgexec.Identity) error
	}{
		{"RunTx", func(id pgexec.Identity) error {
			return pgexec.RunTx(ctx, pool, pgexec.TxOptions{RLS: true, Isolation: pgx.ReadCommitted}, id, func(q pgexec.Querier) error {
				var raw []byte
				return q.QueryRow(ctx, benchSQL, 1).Scan(&raw)
			})
		}},
		{"QueryTx-implicit", queryTxPath(ctx, pool, pgexec.TxOptions{RLS: true})},
		{"QueryTx-explicit", queryTxPath(ctx, pool, pgexec.TxOptions{RLS: true, Isolation: pgx.ReadCommitted})},
		{"QueryTx-scan-before-commit", queryTxPath(ctx, pool, pgexec.TxOptions{RLS: true, ScanBeforeCommit: true})},
		{"no-RLS-QueryRow", func(pgexec.Identity) error {
			var raw []byte
			return pool.QueryRow(ctx, benchSQL, 1).Scan(&raw)
		}},
	}
	for _, bi := range benchIdentities {
		for _, p := range paths {
			if err := p.run(bi.id); err != nil {
				b.Fatalf("%s/%s: %v", bi.name, p.name, err)
			}
			b.Run(bi.name+"/"+p.name+"/serial", func(b *testing.B) {
				for b.Loop() {
					if err := p.run(bi.id); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(bi.name+"/"+p.name+"/parallel", func(b *testing.B) {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if err := p.run(bi.id); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		}
	}
}

func queryTxPath(ctx context.Context, pool *pgxpool.Pool, opts pgexec.TxOptions) func(pgexec.Identity) error {
	return func(id pgexec.Identity) error {
		var raw []byte
		return pgexec.QueryTx(ctx, pool, opts, id, pgexec.Stmt{
			SQL: benchSQL, Args: []any{1},
			Scan: func(r pgx.Row) error { return r.Scan(&raw) },
		})
	}
}
