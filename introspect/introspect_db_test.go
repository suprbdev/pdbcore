package introspect

import (
	"context"
	"os"
	"reflect"
	"strings"
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

// TestIntrospectKeysAndGeneratedLive covers generation kinds and key flags
// inside a rolled-back transaction. Identity and stored generated columns
// and a plain foreign key run on every server; temporal keys
// (WITHOUT OVERLAPS, PERIOD), NOT ENFORCED and virtual generated columns
// need PostgreSQL 18, which also records NOT NULL constraints in
// pg_constraint (contype 'n') that must stay out of the key lists.
func TestIntrospectKeysAndGeneratedLive(t *testing.T) {
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

	var num int
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatal(err)
	}
	pg18 := num >= 180000
	ddl := []string{
		`CREATE SCHEMA pdbcore_keys_test`,
		`CREATE TABLE pdbcore_keys_test.owners (id int PRIMARY KEY)`,
		`CREATE TABLE pdbcore_keys_test.gen (
			i int GENERATED BY DEFAULT AS IDENTITY,
			a int NOT NULL,
			s int GENERATED ALWAYS AS (a * 2) STORED,
			owner_id int REFERENCES pdbcore_keys_test.owners
		)`,
	}
	if pg18 {
		ddl = append(ddl,
			`CREATE EXTENSION IF NOT EXISTS btree_gist SCHEMA pdbcore_keys_test`,
			`ALTER TABLE pdbcore_keys_test.gen ADD v int GENERATED ALWAYS AS (a * 3) VIRTUAL`,
			`CREATE TABLE pdbcore_keys_test.rooms (
				id       int NOT NULL,
				valid_at daterange NOT NULL,
				name     text NOT NULL,
				code     text UNIQUE,
				PRIMARY KEY (id, valid_at WITHOUT OVERLAPS),
				UNIQUE (name, valid_at WITHOUT OVERLAPS)
			)`,
			`CREATE TABLE pdbcore_keys_test.bookings (
				id       int PRIMARY KEY,
				room_id  int NOT NULL,
				during   daterange NOT NULL,
				owner_id int NOT NULL,
				FOREIGN KEY (room_id, PERIOD during) REFERENCES pdbcore_keys_test.rooms (id, PERIOD valid_at),
				CONSTRAINT bookings_owner_fkey FOREIGN KEY (owner_id) REFERENCES pdbcore_keys_test.owners NOT ENFORCED,
				CONSTRAINT bookings_owner_check CHECK (owner_id > 0) NOT ENFORCED
			)`,
			`INSERT INTO pdbcore_keys_test.rooms VALUES
				(1, '[2026-01-01,2026-02-01)', 'a', NULL),
				(1, '[2026-02-01,2026-03-01)', 'a', NULL),
				(1, '[2026-03-01,2026-04-01)', 'a', NULL),
				(2, '[2026-01-01,2026-04-01)', 'b', NULL)`,
			`INSERT INTO pdbcore_keys_test.bookings VALUES (1, 1, '[2026-01-15,2026-02-15)', 42)`,
		)
	}
	for _, q := range ddl {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	cat, err := Introspect(ctx, tx, []string{"pdbcore_keys_test"})
	if err != nil {
		t.Fatal(err)
	}
	if cat.ServerVersionNum != num {
		t.Fatalf("ServerVersionNum = %d, want %d", cat.ServerVersionNum, num)
	}
	gen := cat.Table("pdbcore_keys_test", "gen")
	want := map[string]GeneratedKind{"i": GeneratedIdentity, "a": "", "s": GeneratedStored, "owner_id": ""}
	if pg18 {
		want["v"] = GeneratedVirtual
	}
	if len(gen.Columns) != len(want) {
		t.Fatalf("gen columns = %d, want %d", len(gen.Columns), len(want))
	}
	for name, kind := range want {
		col := gen.Column(name)
		if col.GeneratedKind != kind || col.Generated != (kind != "") {
			t.Errorf("column %s: Generated %v GeneratedKind %q, want %q", name, col.Generated, col.GeneratedKind, kind)
		}
	}
	if gen.PrimaryKey != nil || len(gen.Uniques) != 0 || len(gen.ForeignKeys) != 1 {
		t.Fatalf("gen keys: pk %+v uniques %+v fks %+v", gen.PrimaryKey, gen.Uniques, gen.ForeignKeys)
	}
	if fk := gen.ForeignKeys[0]; fk.Period || fk.NotEnforced || !fk.ReferencesOne() {
		t.Errorf("plain foreign key = %+v", fk)
	}
	if !pg18 {
		return
	}

	rooms := cat.Table("pdbcore_keys_test", "rooms")
	if pk := rooms.PrimaryKey; pk == nil || pk.Name != "rooms_pkey" || !pk.WithoutOverlaps ||
		!reflect.DeepEqual(pk.Columns, []string{"id", "valid_at"}) || pk.PeriodColumn() != "valid_at" {
		t.Fatalf("rooms primary key = %+v", pk)
	}
	if len(rooms.Uniques) != 2 {
		t.Fatalf("rooms uniques = %+v, want code and name/valid_at only (no NOT NULL constraints)", rooms.Uniques)
	}
	for _, u := range rooms.Uniques {
		switch u.Name {
		case "rooms_code_key":
			if u.WithoutOverlaps {
				t.Errorf("%s: WithoutOverlaps set", u.Name)
			}
		case "rooms_name_valid_at_key":
			if !u.WithoutOverlaps || !reflect.DeepEqual(u.EqualityColumns(), []string{"name"}) {
				t.Errorf("%s = %+v", u.Name, u)
			}
		default:
			t.Errorf("unexpected unique constraint %+v", u)
		}
	}
	if keys := rooms.RowKeys(); len(keys) != 1 || keys[0].Name != "rooms_code_key" {
		t.Errorf("rooms RowKeys = %+v, want rooms_code_key only", keys)
	}

	bookings := cat.Table("pdbcore_keys_test", "bookings")
	if pk := bookings.PrimaryKey; pk == nil || pk.Name != "bookings_pkey" || pk.WithoutOverlaps || len(bookings.Uniques) != 0 {
		t.Fatalf("bookings keys: pk %+v uniques %+v", pk, bookings.Uniques)
	}
	if len(bookings.ForeignKeys) != 2 {
		t.Fatalf("bookings foreign keys = %+v", bookings.ForeignKeys)
	}
	var period *ForeignKey
	for _, fk := range bookings.ForeignKeys {
		switch fk.Name {
		case "bookings_owner_fkey":
			if !fk.NotEnforced || fk.Period {
				t.Errorf("%s = %+v", fk.Name, fk)
			}
		case "bookings_room_id_during_fkey":
			if fk.NotEnforced || !fk.Period || fk.ReferencesOne() || fk.RefTable != "rooms" ||
				!reflect.DeepEqual(fk.RefColumns, []string{"id", "valid_at"}) {
				t.Errorf("%s = %+v", fk.Name, fk)
			}
			period = fk
		default:
			t.Errorf("unexpected foreign key %+v", fk)
		}
	}
	if period == nil {
		t.Fatal("temporal foreign key not introspected")
	}

	var conds []string
	for _, p := range period.JoinPairs() {
		conds = append(conds, "b."+p.Column+" "+string(p.Op)+" r."+p.RefColumn)
	}
	rows, err := tx.Query(ctx, `SELECT lower(r.valid_at)::text FROM pdbcore_keys_test.bookings b
		JOIN pdbcore_keys_test.rooms r ON `+strings.Join(conds, " AND ")+` ORDER BY r.valid_at`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"2026-01-01", "2026-02-01"}) {
		t.Errorf("temporal join matched room periods starting %v, want the two the booking overlaps", got)
	}
}
