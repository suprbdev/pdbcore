package introspect

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestIntrospectTypmodsLive introspects a scratch schema of modifier
// variants inside a transaction that is rolled back, so the DDL never
// commits (and never reaches the watch tests' event-trigger NOTIFY). The
// spatial half runs only where PostGIS is installed; the rest also checks
// that the query works on a database without it, and that a user type named
// geometry outside the postgis schema is not taken for PostGIS.
func TestIntrospectTypmodsLive(t *testing.T) {
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
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var postgis bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'postgis')`).Scan(&postgis); err != nil {
		t.Fatal(err)
	}
	ddl := []string{
		`CREATE SCHEMA pdbcore_typmod_test`,
		`CREATE DOMAIN pdbcore_typmod_test.code AS varchar(20)`,
		`CREATE DOMAIN pdbcore_typmod_test.code2 AS pdbcore_typmod_test.code`,
		`CREATE DOMAIN pdbcore_typmod_test.code3 AS pdbcore_typmod_test.code2`,
		`CREATE TYPE pdbcore_typmod_test.geometry AS (x int)`,
		`CREATE TABLE pdbcore_typmod_test.t (
			v  varchar(10),
			w  numeric(10,2),
			x  timestamptz(3),
			z  varchar(5)[],
			d  pdbcore_typmod_test.code,
			dd pdbcore_typmod_test.code3,
			ug pdbcore_typmod_test.geometry,
			ua pdbcore_typmod_test.geometry[],
			p  text,
			n  numeric
		)`,
		`CREATE TYPE pdbcore_typmod_test.c AS (v varchar(7))`,
	}
	if postgis {
		ddl = append(ddl,
			`CREATE DOMAIN pdbcore_typmod_test.pt AS public.geometry(Point,4326)`,
			`CREATE DOMAIN pdbcore_typmod_test.pt2 AS pdbcore_typmod_test.pt`,
			`ALTER TABLE pdbcore_typmod_test.t
				ADD g0 geometry, ADD g1 geometry(Point,4326), ADD g2 geometry(LineStringZM,3857),
				ADD g3 geometry(GeometryZ), ADD g4 geography, ADD g5 geography(Polygon),
				ADD g6 geometry(MultiPolygon,4326)[], ADD g7 pdbcore_typmod_test.pt,
				ADD g8 pdbcore_typmod_test.pt2`,
			`ALTER TYPE pdbcore_typmod_test.c ADD ATTRIBUTE g geometry(MultiPoint,2154)`,
		)
	}
	for _, q := range ddl {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL search_path = pg_catalog`); err != nil {
		t.Fatal(err)
	}

	cat, err := Introspect(ctx, tx, []string{"pdbcore_typmod_test"})
	if err != nil {
		t.Fatal(err)
	}
	if !cat.HasExtension("plpgsql") || cat.HasExtension("postgis") != postgis {
		t.Fatalf("extensions = %+v (postgis installed: %v)", cat.Extensions, postgis)
	}
	if e := cat.Extension("plpgsql"); e.Schema != "pg_catalog" || e.Version == "" {
		t.Fatalf("plpgsql = %+v", e)
	}
	tbl := cat.Table("pdbcore_typmod_test", "t")
	if tbl == nil {
		t.Fatal("scratch table not introspected")
	}
	for name, want := range map[string]struct{ pgType, sqlType string }{
		"v":  {"varchar", "character varying(10)"},
		"w":  {"numeric", "numeric(10,2)"},
		"x":  {"timestamptz", "timestamp(3) with time zone"},
		"z":  {"_varchar", "character varying(5)[]"},
		"d":  {"varchar", "character varying(20)"},
		"dd": {"varchar", "character varying(20)"},
		"ug": {"geometry", ""},
		"ua": {"_geometry", ""},
		"p":  {"text", ""},
		"n":  {"numeric", ""},
	} {
		col := tbl.Column(name)
		if col.PGType != want.pgType || col.SQLType != want.sqlType || col.Spatial != nil {
			t.Errorf("column %s: PGType %q SQLType %q Spatial %+v, want %+v", name, col.PGType, col.SQLType, col.Spatial, want)
		}
	}
	comp := cat.Composite("pdbcore_typmod_test", "c")
	if comp == nil || comp.Fields[0].SQLType != "character varying(7)" {
		t.Fatalf("composite = %+v", comp)
	}
	if !postgis {
		return
	}
	for name, want := range map[string]SpatialType{
		"g0": {Subtype: SpatialGeometry},
		"g1": {Subtype: SpatialPoint, SRID: 4326},
		"g2": {Subtype: SpatialLineString, SRID: 3857, HasZ: true, HasM: true},
		"g3": {Subtype: SpatialGeometry, HasZ: true},
		"g4": {Subtype: SpatialGeometry},
		"g5": {Subtype: SpatialPolygon, SRID: 4326},
		"g6": {Subtype: SpatialMultiPolygon, SRID: 4326},
		"g7": {Subtype: SpatialPoint, SRID: 4326},
		"g8": {Subtype: SpatialPoint, SRID: 4326},
	} {
		col := tbl.Column(name)
		if col.Spatial == nil || *col.Spatial != want || col.SQLType != "" || !isPostGISName(col) {
			t.Errorf("column %s: Spatial %+v SQLType %q, want %+v", name, col.Spatial, col.SQLType, want)
		}
	}
	if g := comp.Fields[1].Spatial; g == nil || *g != (SpatialType{Subtype: SpatialMultiPoint, SRID: 2154}) {
		t.Errorf("composite geometry field Spatial = %+v", g)
	}
}

func isPostGISName(col *Column) bool {
	return col.PGType == "geometry" || col.PGType == "geography" || col.PGType == "_geometry"
}
