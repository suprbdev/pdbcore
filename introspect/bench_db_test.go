package introspect

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func benchIntrospect(b *testing.B, tables int) {
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		b.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close(ctx)
	_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS pdbcore_bench CASCADE`)
	if _, err := conn.Exec(ctx, `CREATE SCHEMA pdbcore_bench`); err != nil {
		b.Fatal(err)
	}
	defer func() {
		for i := tables - 1; i >= 0; i-- {
			_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS pdbcore_bench.t%d CASCADE`, i))
		}
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS pdbcore_bench CASCADE`)
	}()
	for i := 0; i < tables; i++ {
		var cols strings.Builder
		for c := 0; c < 20; c++ {
			fmt.Fprintf(&cols, ", c%d text", c)
		}
		ddl := []string{
			fmt.Sprintf(`CREATE TABLE pdbcore_bench.t%d (id int PRIMARY KEY%s)`, i, cols.String()),
			fmt.Sprintf(`CREATE INDEX ON pdbcore_bench.t%d (c1)`, i),
			fmt.Sprintf(`CREATE INDEX ON pdbcore_bench.t%d (c2, c3)`, i),
		}
		if i > 0 {
			ddl = append(ddl,
				fmt.Sprintf(`ALTER TABLE pdbcore_bench.t%d ADD COLUMN p1 int REFERENCES pdbcore_bench.t%d (id)`, i, i-1),
				fmt.Sprintf(`ALTER TABLE pdbcore_bench.t%d ADD COLUMN p2 int REFERENCES pdbcore_bench.t%d (id)`, i, i/2))
		}
		if i%5 == 0 {
			ddl = append(ddl, fmt.Sprintf(`CREATE FUNCTION pdbcore_bench.f%d(a int) RETURNS int LANGUAGE sql AS 'SELECT a'`, i))
		}
		for _, q := range ddl {
			if _, err := conn.Exec(ctx, q); err != nil {
				b.Fatalf("%s: %v", q, err)
			}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cat, err := Introspect(ctx, conn, []string{"pdbcore_bench"})
		if err != nil {
			b.Fatal(err)
		}
		if len(cat.Tables) != tables {
			b.Fatalf("tables = %d", len(cat.Tables))
		}
	}
}

func BenchmarkIntrospect1000(b *testing.B) { benchIntrospect(b, 1000) }
func BenchmarkIntrospect3000(b *testing.B) { benchIntrospect(b, 3000) }
