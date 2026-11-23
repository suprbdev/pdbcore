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
// and PostGIS versions, the composites CREATE EXTENSION postgis installs,
// and the extra extensions the postgis image preinstalls).
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
// schema (introspection filters extension functions and relations but not
// types); they are not part of the fixture surface.
var postgisObjects = map[string]bool{
	"geometry_dump": true,
	"valid_detail":  true,
	"simple_detail": true,
}

// imageExtensions are created in POSTGRES_DB by the postgis/postgis image's
// own init script (CI's service container runs it; the compose stack mounts
// the fixture over it), so their presence depends on how the database was
// provisioned rather than on FixtureSQL.
var imageExtensions = map[string]bool{
	"postgis_topology":       true,
	"postgis_tiger_geocoder": true,
	"fuzzystrmatch":          true,
}

func normalize(live, fixture *introspect.Catalog) {
	fixture.ServerVersion = live.ServerVersion
	fixture.ServerVersionNum = live.ServerVersionNum
	if lp, fp := live.Extension("postgis"), fixture.Extension("postgis"); lp != nil && fp != nil {
		fp.Version = lp.Version
	}
	exts := live.Extensions[:0]
	for _, e := range live.Extensions {
		if !imageExtensions[e.Name] {
			exts = append(exts, e)
		}
	}
	live.Extensions = exts
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
	if !FixtureCatalog().HasExtension("postgis") {
		t.Fatal("FixtureCatalog must record the postgis extension")
	}
}
