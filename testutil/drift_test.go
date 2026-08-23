package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/introspect"
)

// TestFixtureCatalogMatchesLiveIntrospection is the permanent drift gate:
// FixtureSQL is loaded by the compose stack (see compose.test.yaml), the
// database is introspected, and the result must equal FixtureCatalog()
// after normalising the parts a hand-written fixture cannot mirror (server
// version and the relations/composites CREATE EXTENSION postgis installs).
func TestFixtureCatalogMatchesLiveIntrospection(t *testing.T) {
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	live, err := introspect.Introspect(ctx, conn, []string{"public"})
	if err != nil {
		t.Fatal(err)
	}
	fixture := FixtureCatalog()
	normalize(live, fixture)

	if diff := introspect.Diff(fixture, live); len(diff) > 0 {
		t.Fatalf("FixtureCatalog() drifted from FixtureSQL (fixture -> live):\n  %s",
			strings.Join(diff, "\n  "))
	}
	if diff := introspect.Diff(live, fixture); len(diff) > 0 {
		t.Fatalf("FixtureSQL drifted from FixtureCatalog() (live -> fixture):\n  %s",
			strings.Join(diff, "\n  "))
	}
}

// postgisObjects are installed by CREATE EXTENSION postgis into the same
// schema (introspection filters extension functions but not relations or
// types); they are not part of the fixture surface.
var postgisObjects = map[string]bool{
	"spatial_ref_sys":   true,
	"geometry_columns":  true,
	"geography_columns": true,
	"geometry_dump":     true,
	"valid_detail":      true,
}

func normalize(live, fixture *introspect.Catalog) {
	fixture.ServerVersion = live.ServerVersion
	tables := live.Tables[:0]
	for _, t := range live.Tables {
		if !postgisObjects[t.Name] {
			tables = append(tables, t)
		}
	}
	live.Tables = tables
	comps := live.Composites[:0]
	for _, c := range live.Composites {
		if !postgisObjects[c.Name] {
			comps = append(comps, c)
		}
	}
	live.Composites = comps
}

func TestFixtureCatalogHash(t *testing.T) {
	a, err := FixtureCatalog().Hash()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := FixtureCatalog().Hash()
	if a != b {
		t.Fatal("FixtureCatalog must be deterministic")
	}
	if !strings.Contains(FixtureSQL, "CREATE VIEW metrics") {
		t.Fatal("FixtureSQL must be embedded")
	}
}
