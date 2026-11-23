package introspect

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
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
				ADD g8 pdbcore_typmod_test.pt2, ADD g9 pdbcore_typmod_test.pt[]`,
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
		"g9": {Subtype: SpatialPoint, SRID: 4326},
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

// liveTx opens a transaction on PDBCORE_TEST_DATABASE_URL that the test
// rolls back, and runs ddl in it.
func liveTx(t *testing.T, ddl ...string) pgx.Tx {
	t.Helper()
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	for _, q := range ddl {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return tx
}

// TestIntrospectFunctionsLive covers parameter defaults, VARIADIC, OUT,
// INOUT and RETURNS TABLE parameters, and runs real calls rendered by
// Function.CallSQL with arguments left out and placeholders numbered by the
// caller.
func TestIntrospectFunctionsLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_funcs_test`,
		`SET LOCAL search_path = pdbcore_funcs_test`,
		`CREATE FUNCTION add_numbers(a int, b int DEFAULT 1) RETURNS int LANGUAGE sql AS 'SELECT a + b'`,
		`CREATE FUNCTION record_event(kind text, payload jsonb DEFAULT '{}') RETURNS jsonb
			LANGUAGE sql AS 'SELECT jsonb_build_object(''kind'', kind, ''payload'', payload)'`,
		`CREATE FUNCTION sum_ints(VARIADIC xs int[]) RETURNS int LANGUAGE sql AS 'SELECT sum(x)::int FROM unnest(xs) x'`,
		`CREATE FUNCTION join_words(sep text DEFAULT ',', VARIADIC words text[] DEFAULT '{}') RETURNS text
			LANGUAGE sql AS 'SELECT array_to_string(words, sep)'`,
		`CREATE FUNCTION split_pair(a int, OUT lo int, OUT hi int) LANGUAGE sql AS 'SELECT a - 1, a + 1'`,
		`CREATE FUNCTION unnamed_out(a int, OUT int, OUT text) LANGUAGE sql AS 'SELECT a, a::text'`,
		`CREATE FUNCTION inout_double(INOUT x int, OUT label text) LANGUAGE sql AS 'SELECT x * 2, ''doubled'''`,
		`CREATE FUNCTION single_out(a int, OUT doubled int) LANGUAGE sql AS 'SELECT a * 2'`,
		`CREATE FUNCTION squares(n int) RETURNS TABLE (i int, sq int)
			LANGUAGE sql AS 'SELECT g, g * g FROM generate_series(1, n) g'`,
		`CREATE FUNCTION mixed_defaults(a int, OUT r int, b int DEFAULT 2) LANGUAGE sql AS 'SELECT a + b'`,
		`CREATE FUNCTION three(a int, b int DEFAULT 10, c int DEFAULT 100) RETURNS int LANGUAGE sql AS 'SELECT a + b + c'`,
		`CREATE FUNCTION unnamed_in(int) RETURNS int LANGUAGE sql AS 'SELECT $1'`,
		`SET LOCAL search_path = pg_catalog`,
	)
	ctx := context.Background()
	cat, err := Introspect(ctx, tx, []string{"pdbcore_funcs_test"})
	if err != nil {
		t.Fatal(err)
	}
	fn := func(name string) *Function {
		t.Helper()
		for _, f := range cat.Functions {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("function %s not introspected", name)
		return nil
	}
	for _, f := range cat.Functions {
		if f.Name == "unnamed_in" {
			t.Errorf("function with an unnamed input parameter introspected: %+v", f)
		}
	}
	in := func(name, typ string) FuncArg {
		return FuncArg{Name: name, PGType: typ, TypeSchema: "pg_catalog", Mode: ArgModeIn}
	}
	withMode := func(a FuncArg, m ArgMode) FuncArg { a.Mode = m; return a }
	withDefault := func(a FuncArg, d string) FuncArg { a.HasDefault, a.Default = true, d; return a }
	for name, want := range map[string]struct {
		args, cols []FuncArg
		ret        string
		set        bool
	}{
		"add_numbers":  {args: []FuncArg{in("a", "int4"), withDefault(in("b", "int4"), "1")}, ret: "int4"},
		"record_event": {args: []FuncArg{in("kind", "text"), withDefault(in("payload", "jsonb"), "'{}'::jsonb")}, ret: "jsonb"},
		"sum_ints":     {args: []FuncArg{withMode(in("xs", "_int4"), ArgModeVariadic)}, ret: "int4"},
		"join_words": {args: []FuncArg{withDefault(in("sep", "text"), "','::text"),
			withDefault(withMode(in("words", "_text"), ArgModeVariadic), "'{}'::text[]")}, ret: "text"},
		"split_pair": {args: []FuncArg{in("a", "int4")},
			cols: []FuncArg{withMode(in("lo", "int4"), ArgModeOut), withMode(in("hi", "int4"), ArgModeOut)}, ret: "record"},
		"unnamed_out": {args: []FuncArg{in("a", "int4")},
			cols: []FuncArg{withMode(in("column1", "int4"), ArgModeOut), withMode(in("column2", "text"), ArgModeOut)}, ret: "record"},
		"inout_double": {args: []FuncArg{withMode(in("x", "int4"), ArgModeInOut)},
			cols: []FuncArg{withMode(in("x", "int4"), ArgModeInOut), withMode(in("label", "text"), ArgModeOut)}, ret: "record"},
		"single_out": {args: []FuncArg{in("a", "int4")}, cols: []FuncArg{withMode(in("doubled", "int4"), ArgModeOut)}, ret: "int4"},
		"squares": {args: []FuncArg{in("n", "int4")},
			cols: []FuncArg{withMode(in("i", "int4"), ArgModeTable), withMode(in("sq", "int4"), ArgModeTable)}, ret: "record", set: true},
		"mixed_defaults": {args: []FuncArg{in("a", "int4"), withDefault(in("b", "int4"), "2")},
			cols: []FuncArg{withMode(in("r", "int4"), ArgModeOut)}, ret: "int4"},
	} {
		f := fn(name)
		if !reflect.DeepEqual(f.Args, want.args) || !reflect.DeepEqual(f.ReturnColumns, want.cols) ||
			f.ReturnType != want.ret || f.ReturnsSet != want.set {
			t.Errorf("%s: Args %+v ReturnColumns %+v ReturnType %s ReturnsSet %v\nwant %+v", name, f.Args, f.ReturnColumns, f.ReturnType, f.ReturnsSet, want)
		}
	}
	if v := fn("join_words").Variadic(); v == nil || v.Name != "words" {
		t.Errorf("join_words Variadic() = %+v", v)
	}

	type call struct {
		fn   string
		args map[string]any
		sql  string
		want string
	}
	for _, c := range []call{
		{"add_numbers", map[string]any{"a": 5}, `"pdbcore_funcs_test"."add_numbers"("a" => $1::int4)`, "6"},
		{"add_numbers", map[string]any{"a": 5, "b": 10}, `"pdbcore_funcs_test"."add_numbers"("a" => $1::int4, "b" => $2::int4)`, "15"},
		{"add_numbers", map[string]any{"b": 10, "a": nil}, `"pdbcore_funcs_test"."add_numbers"("a" => $1::int4, "b" => $2::int4)`, ""},
		{"record_event", map[string]any{"kind": "login"}, `"pdbcore_funcs_test"."record_event"("kind" => $1::text)`, `{"kind": "login", "payload": {}}`},
		{"three", map[string]any{"a": 1, "c": 2}, `"pdbcore_funcs_test"."three"("a" => $1::int4, "c" => $2::int4)`, "13"},
		{"sum_ints", map[string]any{"xs": []int32{1, 2, 3}}, `"pdbcore_funcs_test"."sum_ints"(VARIADIC $1::_int4)`, "6"},
		{"sum_ints", map[string]any{"xs": []int32{}}, `"pdbcore_funcs_test"."sum_ints"(VARIADIC $1::_int4)`, ""},
		{"join_words", map[string]any{"sep": "-", "words": []string{"a", "b"}}, `"pdbcore_funcs_test"."join_words"($1::text, VARIADIC $2::_text)`, "a-b"},
		{"join_words", map[string]any{"sep": "-"}, `"pdbcore_funcs_test"."join_words"($1::text)`, ""},
		{"join_words", map[string]any{}, `"pdbcore_funcs_test"."join_words"()`, ""},
		{"mixed_defaults", map[string]any{"a": 1}, `"pdbcore_funcs_test"."mixed_defaults"("a" => $1::int4)`, "3"},
		{"inout_double", map[string]any{"x": 4}, `"pdbcore_funcs_test"."inout_double"("x" => $1::int4)`, `(8,doubled)`},
	} {
		var params []any
		sql, err := fn(c.fn).CallSQL(func(a FuncArg) (string, bool, error) {
			v, ok := c.args[a.Name]
			if !ok {
				return "", false, nil
			}
			params = append(params, v)
			return "$" + strconv.Itoa(len(params)) + "::" + a.PGType, true, nil
		})
		if err != nil || sql != c.sql {
			t.Errorf("%s(%v): CallSQL = %q, %v; want %q", c.fn, c.args, sql, err, c.sql)
			continue
		}
		var got *string
		if err := tx.QueryRow(ctx, `SELECT (`+sql+`)::text`, params...).Scan(&got); err != nil {
			t.Errorf("%s: %v", sql, err)
			continue
		}
		if (got == nil && c.want != "") || (got != nil && *got != c.want) {
			t.Errorf("%s = %v, want %q", sql, got, c.want)
		}
	}

	split, err := fn("split_pair").CallSQL(func(FuncArg) (string, bool, error) { return "$1::int4", true, nil })
	if err != nil {
		t.Fatal(err)
	}
	var lo, hi int
	if err := tx.QueryRow(ctx, `SELECT lo, hi FROM `+split, 7).Scan(&lo, &hi); err != nil || lo != 6 || hi != 8 {
		t.Errorf("split_pair(7) = %d, %d, %v", lo, hi, err)
	}
	sq, err := fn("squares").CallSQL(func(FuncArg) (string, bool, error) { return "$1::int4", true, nil })
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `SELECT sq FROM `+sq+` ORDER BY i`, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pgx.CollectRows(rows, pgx.RowTo[int32]); err != nil || !reflect.DeepEqual(got, []int32{1, 4, 9}) {
		t.Errorf("squares(3) = %v, %v", got, err)
	}

	var missing *MissingArgsError
	if _, err := fn("add_numbers").CallSQL(func(FuncArg) (string, bool, error) { return "", false, nil }); !errors.As(err, &missing) ||
		!reflect.DeepEqual(missing.Args, []string{"a"}) {
		t.Errorf("add_numbers() without a: err = %v", err)
	}
	wordsOnly := func(a FuncArg) (string, bool, error) { return "$1::_text", a.Variadic(), nil }
	if _, err := fn("join_words").CallSQL(wordsOnly); !errors.As(err, &missing) || !reflect.DeepEqual(missing.Args, []string{"sep"}) {
		t.Errorf("join_words(VARIADIC words) without sep: err = %v", err)
	}
}

// TestIntrospectPartitionsLive checks that partitioned tables are recorded
// with their keys and indexes, partitions are marked with their parent and
// carry only what is declared on them, and that no constraint PostgreSQL
// clones for partitions (onto each partition, or onto a table referencing a
// partitioned table) shows up.
func TestIntrospectPartitionsLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_part_test`,
		`SET LOCAL search_path = pdbcore_part_test`,
		`CREATE TABLE customers (id int PRIMARY KEY, name text)`,
		`CREATE TABLE purchases (
			id          int NOT NULL,
			customer_id int NOT NULL REFERENCES customers,
			bought      date NOT NULL,
			PRIMARY KEY (id, bought, customer_id),
			UNIQUE (customer_id, id, bought)
		) PARTITION BY RANGE (bought)`,
		`CREATE INDEX purchases_customer_idx ON purchases (customer_id)`,
		`CREATE TABLE purchases_2025 PARTITION OF purchases FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')`,
		`CREATE TABLE purchases_2026 PARTITION OF purchases FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`CREATE INDEX purchases_2026_local_idx ON purchases_2026 (bought)`,
		`CREATE TABLE purchases_2027 PARTITION OF purchases FOR VALUES FROM ('2027-01-01') TO ('2028-01-01')
			PARTITION BY LIST (customer_id)`,
		`CREATE TABLE purchases_2027_c1 PARTITION OF purchases_2027 FOR VALUES IN (1)`,
		`CREATE TABLE refunds (
			id          int PRIMARY KEY,
			purchase_id int NOT NULL,
			bought      date NOT NULL,
			customer_id int NOT NULL,
			FOREIGN KEY (purchase_id, bought, customer_id) REFERENCES purchases (id, bought, customer_id)
		)`,
		`SET LOCAL search_path = pg_catalog`,
	)
	cat, err := Introspect(context.Background(), tx, []string{"pdbcore_part_test"})
	if err != nil {
		t.Fatal(err)
	}
	tbl := func(name string) *Table {
		t.Helper()
		tb := cat.Table("pdbcore_part_test", name)
		if tb == nil {
			t.Fatalf("table %s not introspected", name)
		}
		return tb
	}
	indexNames := func(tb *Table) []string {
		var out []string
		for _, ix := range tb.Indexes {
			out = append(out, ix.Name)
		}
		return out
	}

	p := tbl("purchases")
	if !p.Partitioned || p.IsPartition() || p.Kind != RelTable || !p.Insertable() {
		t.Errorf("purchases: Partitioned %v PartitionOf %q Kind %s", p.Partitioned, p.PartitionOf, p.Kind)
	}
	if p.PrimaryKey == nil || p.PrimaryKey.Name != "purchases_pkey" || len(p.Uniques) != 1 || len(p.ForeignKeys) != 1 ||
		p.ForeignKeys[0].RefTable != "customers" {
		t.Errorf("purchases keys: pk %+v uniques %+v fks %+v", p.PrimaryKey, p.Uniques, p.ForeignKeys)
	}
	if got := indexNames(p); !reflect.DeepEqual(got, []string{"purchases_customer_id_id_bought_key", "purchases_customer_idx", "purchases_pkey"}) {
		t.Errorf("purchases indexes = %v", got)
	}
	if !p.IndexedColumns()["customer_id"] {
		t.Error("purchases.customer_id not indexed")
	}

	for name, want := range map[string]struct {
		partitioned bool
		indexes     []string
	}{
		"purchases_2025":    {},
		"purchases_2026":    {indexes: []string{"purchases_2026_local_idx"}},
		"purchases_2027":    {partitioned: true},
		"purchases_2027_c1": {},
	} {
		part := tbl(name)
		parent := "pdbcore_part_test.purchases"
		if name == "purchases_2027_c1" {
			parent = "pdbcore_part_test.purchases_2027"
		}
		if part.PartitionOf != parent || !part.IsPartition() || part.Partitioned != want.partitioned {
			t.Errorf("%s: PartitionOf %q Partitioned %v", name, part.PartitionOf, part.Partitioned)
		}
		if part.PrimaryKey != nil || len(part.Uniques) != 0 || len(part.ForeignKeys) != 0 {
			t.Errorf("%s carries cloned keys: pk %+v uniques %+v fks %+v", name, part.PrimaryKey, part.Uniques, part.ForeignKeys)
		}
		if got := indexNames(part); !reflect.DeepEqual(got, want.indexes) {
			t.Errorf("%s indexes = %v, want %v", name, got, want.indexes)
		}
	}

	r := tbl("refunds")
	if len(r.ForeignKeys) != 1 || r.ForeignKeys[0].RefTable != "purchases" || r.IsPartition() || r.Partitioned {
		t.Errorf("refunds foreign keys = %+v", r.ForeignKeys)
	}
	if c := tbl("customers"); c.Partitioned || c.IsPartition() || len(c.ForeignKeys) != 0 {
		t.Errorf("customers = %+v", c)
	}
}

// TestIntrospectBaseTypesAndRangesLive checks that BaseTypeSchema follows a
// domain chain into another schema (and that a same-named type elsewhere
// keeps its own schema), and that range types outside pg_catalog are
// recorded with their subtype and multirange.
func TestIntrospectBaseTypesAndRangesLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_base_a`,
		`CREATE SCHEMA pdbcore_base_b`,
		`CREATE TYPE pdbcore_base_a.hstore AS (k text)`,
		`CREATE DOMAIN pdbcore_base_b.words AS pdbcore_base_a.hstore`,
		`CREATE DOMAIN pdbcore_base_b.words2 AS pdbcore_base_b.words`,
		`CREATE TYPE pdbcore_base_a.floatrange AS RANGE (subtype = float8)`,
		`CREATE DOMAIN pdbcore_base_b.span AS pdbcore_base_a.floatrange`,
		`CREATE TABLE pdbcore_base_b.t (
			h  pdbcore_base_a.hstore,
			w  pdbcore_base_b.words2,
			r  pdbcore_base_a.floatrange,
			s  pdbcore_base_b.span,
			ra pdbcore_base_a.floatrange[],
			n  int4
		)`,
		`CREATE TYPE pdbcore_base_b.c AS (w pdbcore_base_b.words)`,
	)
	ctx := context.Background()
	cat, err := Introspect(ctx, tx, []string{"pdbcore_base_b"})
	if err != nil {
		t.Fatal(err)
	}
	tbl := cat.Table("pdbcore_base_b", "t")
	if tbl == nil {
		t.Fatal("table not introspected")
	}
	for name, want := range map[string][3]string{
		"h":  {"hstore", "pdbcore_base_a", "pdbcore_base_a"},
		"w":  {"hstore", "pdbcore_base_b", "pdbcore_base_a"},
		"r":  {"floatrange", "pdbcore_base_a", "pdbcore_base_a"},
		"s":  {"floatrange", "pdbcore_base_b", "pdbcore_base_a"},
		"ra": {"_floatrange", "pdbcore_base_a", "pdbcore_base_a"},
		"n":  {"int4", "pg_catalog", "pg_catalog"},
	} {
		c := tbl.Column(name)
		if got := [3]string{c.PGType, c.TypeSchema, c.BaseTypeSchema}; got != want {
			t.Errorf("column %s: (PGType, TypeSchema, BaseTypeSchema) = %v, want %v", name, got, want)
		}
	}
	comp := cat.Composite("pdbcore_base_b", "c")
	if comp == nil || comp.Fields[0].BaseTypeSchema != "pdbcore_base_a" || comp.Fields[0].TypeSchema != "pdbcore_base_b" {
		t.Errorf("composite field = %+v", comp)
	}

	r := cat.Range("pdbcore_base_a", "floatrange")
	if r == nil {
		t.Fatalf("floatrange not recorded: %+v", cat.Ranges)
	}
	want := RangeType{Schema: "pdbcore_base_a", Name: "floatrange", Subtype: "float8", SubtypeSchema: "pg_catalog"}
	if cat.ServerVersionNum >= 140000 {
		want.Multirange, want.MultirangeSchema = "floatmultirange", "pdbcore_base_a"
		if got := cat.RangeOfMultirange("pdbcore_base_a", "floatmultirange"); got != r {
			t.Errorf("RangeOfMultirange = %+v", got)
		}
	}
	if *r != want {
		t.Errorf("floatrange = %+v, want %+v", *r, want)
	}
	for _, rt := range cat.Ranges {
		if rt.Schema == "pg_catalog" {
			t.Errorf("built-in range recorded: %+v", rt)
		}
	}
}

func TestIntrospectSkipsTypeOwnedFunctionsLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_typefn_test`,
		`CREATE TYPE pdbcore_typefn_test.floatrange AS RANGE (subtype = float8)`,
		`CREATE FUNCTION pdbcore_typefn_test.keep_me(a int) RETURNS int LANGUAGE sql AS 'SELECT a'`,
	)
	cat, err := Introspect(context.Background(), tx, []string{"pdbcore_typefn_test"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range cat.Functions {
		got = append(got, f.Name)
	}
	if !reflect.DeepEqual(got, []string{"keep_me"}) {
		t.Fatalf("functions = %v, want only keep_me", got)
	}
	if cat.Range("pdbcore_typefn_test", "floatrange") == nil {
		t.Fatal("range type itself must still be listed")
	}
}

// TestIntrospectDomainsLive checks the recorded domains (constraints,
// defaults, comments, parents, array bases), which domains outside the
// introspected schemas are kept, and the Domain refs on columns, arrays of
// domains, composite attributes and function parameters and results.
func TestIntrospectDomainsLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_dom`,
		`CREATE SCHEMA pdbcore_dom_other`,
		`CREATE DOMAIN pdbcore_dom.email AS text CHECK (VALUE ~ '^[^@]+@[^@]+$')`,
		`COMMENT ON DOMAIN pdbcore_dom.email IS 'An email address.'`,
		`CREATE DOMAIN pdbcore_dom.positive_int AS int NOT NULL DEFAULT 1 CONSTRAINT a_positive CHECK (VALUE > 0)`,
		`CREATE DOMAIN pdbcore_dom.small_positive AS pdbcore_dom.positive_int
			CONSTRAINT b_upper CHECK (VALUE <= 100) CONSTRAINT a_odd CHECK (VALUE % 2 = 1)`,
		`CREATE DOMAIN pdbcore_dom.int_list AS int[]`,
		`CREATE DOMAIN pdbcore_dom_other.code AS varchar(10)`,
		`CREATE DOMAIN pdbcore_dom_other.code2 AS pdbcore_dom_other.code`,
		`CREATE DOMAIN pdbcore_dom_other.unused AS text`,
		`CREATE DOMAIN pdbcore_dom_other.arg AS text`,
		`CREATE TABLE pdbcore_dom.t (
			e  pdbcore_dom.email,
			es pdbcore_dom.email[],
			s  pdbcore_dom.small_positive,
			l  pdbcore_dom.int_list,
			c  pdbcore_dom_other.code2,
			cs pdbcore_dom_other.code[],
			n  int
		)`,
		`CREATE TYPE pdbcore_dom.comp AS (e pdbcore_dom.email, n int)`,
		`CREATE FUNCTION pdbcore_dom.f(e pdbcore_dom.email, es pdbcore_dom.email[], o pdbcore_dom_other.arg)
			RETURNS pdbcore_dom.small_positive LANGUAGE sql AS 'SELECT 1'`,
		`CREATE FUNCTION pdbcore_dom.g() RETURNS TABLE (x pdbcore_dom.email, y int) LANGUAGE sql AS $$SELECT 'a@b'::pdbcore_dom.email, 1$$`,
		`CREATE FUNCTION pdbcore_dom.h() RETURNS pdbcore_dom.email[] LANGUAGE sql AS $$SELECT ARRAY['a@b']::pdbcore_dom.email[]$$`,
	)
	cat, err := Introspect(context.Background(), tx, []string{"pdbcore_dom"})
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, d := range cat.Domains {
		refs = append(refs, d.Ref())
	}
	wantRefs := []string{
		"pdbcore_dom.email", "pdbcore_dom.int_list", "pdbcore_dom.positive_int", "pdbcore_dom.small_positive",
		"pdbcore_dom_other.arg", "pdbcore_dom_other.code", "pdbcore_dom_other.code2",
	}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Fatalf("domains = %v, want %v", refs, wantRefs)
	}
	for ref, want := range map[string]Domain{
		"pdbcore_dom.email": {Schema: "pdbcore_dom", Name: "email", BaseType: "text", BaseTypeSchema: "pg_catalog",
			Checks: []string{"CHECK ((VALUE ~ '^[^@]+@[^@]+$'::text))"}, Comment: "An email address."},
		"pdbcore_dom.positive_int": {Schema: "pdbcore_dom", Name: "positive_int", BaseType: "int4", BaseTypeSchema: "pg_catalog",
			NotNull: true, Default: "1", Checks: []string{"CHECK ((VALUE > 0))"}},
		"pdbcore_dom.small_positive": {Schema: "pdbcore_dom", Name: "small_positive", BaseType: "int4", BaseTypeSchema: "pg_catalog",
			Parent: "pdbcore_dom.positive_int", Default: "1",
			Checks: []string{"CHECK ((((VALUE)::integer % 2) = 1))", "CHECK (((VALUE)::integer <= 100))"}},
		"pdbcore_dom.int_list": {Schema: "pdbcore_dom", Name: "int_list", BaseType: "_int4", BaseTypeSchema: "pg_catalog", IsArrayBase: true},
		"pdbcore_dom_other.code2": {Schema: "pdbcore_dom_other", Name: "code2", BaseType: "varchar", BaseTypeSchema: "pg_catalog",
			Parent: "pdbcore_dom_other.code"},
	} {
		d := cat.DomainByRef(ref)
		if d == nil || !reflect.DeepEqual(*d, want) {
			t.Errorf("domain %s = %+v, want %+v", ref, d, want)
		}
	}
	if got := cat.DomainChain(cat.Domain("pdbcore_dom", "small_positive")); len(got) != 2 || got[1] != cat.Domain("pdbcore_dom", "positive_int") {
		t.Errorf("DomainChain = %+v", got)
	}

	tbl := cat.Table("pdbcore_dom", "t")
	type colWant struct {
		pgType, sqlType, domain string
		isArray                 bool
	}
	for name, want := range map[string]colWant{
		"e":  {"text", "", "pdbcore_dom.email", false},
		"es": {"_text", "", "pdbcore_dom.email", true},
		"s":  {"int4", "", "pdbcore_dom.small_positive", false},
		"l":  {"_int4", "", "pdbcore_dom.int_list", true},
		"c":  {"varchar", "character varying(10)", "pdbcore_dom_other.code2", false},
		"cs": {"_varchar", "character varying(10)[]", "pdbcore_dom_other.code", true},
		"n":  {"int4", "", "", false},
	} {
		c := tbl.Column(name)
		if got := (colWant{c.PGType, c.SQLType, c.Domain, c.IsArray}); got != want {
			t.Errorf("column %s = %+v, want %+v", name, got, want)
		}
		if c.BaseTypeSchema != "pg_catalog" {
			t.Errorf("column %s BaseTypeSchema = %q", name, c.BaseTypeSchema)
		}
	}
	if es := tbl.Column("es"); es.TypeSchema != "pdbcore_dom" {
		t.Errorf("column es TypeSchema = %q", es.TypeSchema)
	}
	comp := cat.Composite("pdbcore_dom", "comp")
	if comp == nil || comp.Fields[0].Domain != "pdbcore_dom.email" || comp.Fields[0].PGType != "text" || comp.Fields[1].Domain != "" {
		t.Errorf("composite = %+v", comp)
	}
	fns := map[string]*Function{}
	for _, f := range cat.Functions {
		fns[f.Name] = f
	}
	f := fns["f"]
	if f == nil || f.ReturnType != "small_positive" || f.ReturnDomain != "pdbcore_dom.small_positive" {
		t.Fatalf("f = %+v", f)
	}
	for i, want := range [][2]string{{"email", "pdbcore_dom.email"}, {"_email", "pdbcore_dom.email"}, {"arg", "pdbcore_dom_other.arg"}} {
		if got := [2]string{f.Args[i].PGType, f.Args[i].Domain}; got != want {
			t.Errorf("f arg %d (PGType, Domain) = %v, want %v", i, got, want)
		}
	}
	if g := fns["g"]; g == nil || g.ReturnColumns[0].Domain != "pdbcore_dom.email" || g.ReturnColumns[1].Domain != "" || g.ReturnDomain != "" {
		t.Errorf("g = %+v", g)
	}
	if h := fns["h"]; h == nil || h.ReturnType != "_email" || h.ReturnDomain != "pdbcore_dom.email" {
		t.Errorf("h = %+v", h)
	}
}

// TestIntrospectSkipsExtensionRelationsLive checks that relations an
// extension owns (pg_buffercache's view; PostGIS's spatial_ref_sys and
// catalog views, raster's where postgis_raster is available) are not
// introspected, while a user's own view over one, and a materialized view
// that merely depends on an extension, are.
func TestIntrospectSkipsExtensionRelationsLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_ext_test`,
		`CREATE EXTENSION pg_buffercache SCHEMA pdbcore_ext_test`,
		`CREATE VIEW pdbcore_ext_test.buffers AS SELECT bufferid FROM pdbcore_ext_test.pg_buffercache`,
		`CREATE MATERIALIZED VIEW pdbcore_ext_test.snap AS SELECT 1 AS n`,
		`ALTER MATERIALIZED VIEW pdbcore_ext_test.snap DEPENDS ON EXTENSION pg_buffercache`,
	)
	ctx := context.Background()
	var postgis, raster bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'postgis'),
		EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'postgis_raster')`).Scan(&postgis, &raster); err != nil {
		t.Fatal(err)
	}
	schemas := []string{"pdbcore_ext_test"}
	var hidden []string
	if postgis {
		var schema string
		if err := tx.QueryRow(ctx, `SELECT extnamespace::regnamespace::text FROM pg_extension WHERE extname = 'postgis'`).Scan(&schema); err != nil {
			t.Fatal(err)
		}
		schemas = append(schemas, schema)
		hidden = append(hidden, schema+".spatial_ref_sys", schema+".geometry_columns", schema+".geography_columns")
		if raster {
			if _, err := tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis_raster SCHEMA `+schema); err != nil {
				t.Fatal(err)
			}
			hidden = append(hidden, schema+".raster_columns", schema+".raster_overviews")
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL search_path = pg_catalog`); err != nil {
		t.Fatal(err)
	}
	cat, err := Introspect(ctx, tx, schemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range append(hidden, "pdbcore_ext_test.pg_buffercache") {
		s, n, _ := strings.Cut(ref, ".")
		if cat.Table(s, n) != nil {
			t.Errorf("extension relation %s introspected", ref)
		}
	}
	for _, n := range []string{"buffers", "snap"} {
		if cat.Table("pdbcore_ext_test", n) == nil {
			t.Errorf("user relation %s not introspected", n)
		}
	}
}

// TestIntrospectConstraintColumnsLive checks the columns recorded for key
// expressions, partial-index predicates, exclusion constraints and check
// constraints, including whole-row references, a dropped column shifting
// nothing, and a partitioned table's checks being recorded on the parent
// only; and that ConstraintReferences sees them.
func TestIntrospectConstraintColumnsLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_conscols_test`,
		`SET LOCAL search_path = pdbcore_conscols_test`,
		`CREATE TABLE ux (id int PRIMARY KEY, gone int, owner int, secret text, code text,
			CHECK (code IS DISTINCT FROM secret))`,
		`ALTER TABLE ux DROP COLUMN gone`,
		`CREATE FUNCTION row_code(ux) RETURNS text IMMUTABLE LANGUAGE sql AS 'SELECT $1.code'`,
		`CREATE UNIQUE INDEX ux_owner_secret_lower_key ON ux (owner, lower(secret))`,
		`CREATE UNIQUE INDEX ux_code_secret_partial_key ON ux (code) WHERE secret LIKE 'vip%'`,
		`CREATE UNIQUE INDEX ux_code_partial_self_key ON ux (code) WHERE code <> ''`,
		`CREATE INDEX ux_row_idx ON ux (row_code(ux))`,
		`CREATE INDEX ux_owner_idx ON ux (owner) INCLUDE (code)`,
		`ALTER TABLE ux ADD CONSTRAINT ux_code_excl EXCLUDE USING btree (lower(code) WITH =) WHERE (secret <> '')`,
		`ALTER TABLE ux ADD CONSTRAINT ux_secret_check CHECK (length(secret) < 100)`,
		`ALTER TABLE ux ADD CONSTRAINT ux_row_check CHECK (row_code(ux) IS NOT NULL) NOT VALID`,
		`ALTER TABLE ux ADD CONSTRAINT ux_const_check CHECK (true)`,
		`CREATE TABLE parted (id int NOT NULL, k text, CONSTRAINT parted_k_check CHECK (k <> '')) PARTITION BY LIST (id)`,
		`CREATE TABLE parted_1 PARTITION OF parted (CONSTRAINT parted_1_id_check CHECK (id = 1)) FOR VALUES IN (1)`,
		`SET LOCAL search_path = pg_catalog`,
	)
	cat, err := Introspect(context.Background(), tx, []string{"pdbcore_conscols_test"})
	if err != nil {
		t.Fatal(err)
	}
	ux := cat.Table("pdbcore_conscols_test", "ux")
	if ux == nil {
		t.Fatal("ux not introspected")
	}
	all := []string{"id", "owner", "secret", "code"}
	wantIndexes := map[string][2][]string{
		"ux_pkey":                    {{"id"}, nil},
		"ux_owner_secret_lower_key":  {{"owner"}, {"secret"}},
		"ux_code_secret_partial_key": {{"code"}, {"secret"}},
		"ux_code_partial_self_key":   {{"code"}, {"code"}},
		"ux_row_idx":                 {nil, all},
		"ux_owner_idx":               {{"owner", "code"}, nil},
		"ux_code_excl":               {nil, {"secret", "code"}},
	}
	for _, ix := range ux.Indexes {
		want, ok := wantIndexes[ix.Name]
		if !ok {
			t.Errorf("unexpected index %s", ix.Name)
			continue
		}
		delete(wantIndexes, ix.Name)
		if len(ix.Columns) == 0 {
			ix.Columns = nil
		}
		if !reflect.DeepEqual(ix.Columns, want[0]) || !reflect.DeepEqual(ix.ExpressionColumns, want[1]) {
			t.Errorf("index %s: Columns %v ExpressionColumns %v, want %v %v", ix.Name, ix.Columns, ix.ExpressionColumns, want[0], want[1])
		}
	}
	for name := range wantIndexes {
		t.Errorf("index %s not introspected", name)
	}
	wantChecks := map[string][]string{
		"ux_check":        {"secret", "code"},
		"ux_secret_check": {"secret"},
		"ux_row_check":    all,
		"ux_const_check":  nil,
	}
	got := map[string][]string{}
	for _, c := range ux.Checks {
		if len(c.Columns) == 0 {
			c.Columns = nil
		}
		got[c.Name] = c.Columns
	}
	if !reflect.DeepEqual(got, wantChecks) {
		t.Errorf("ux checks = %v, want %v", got, wantChecks)
	}

	secret := func(c string) bool { return c == "secret" }
	for name, want := range map[string]bool{
		"ux_pkey": false, "ux_owner_idx": false, "ux_code_partial_self_key": false, "ux_const_check": false,
		"ux_owner_secret_lower_key": true, "ux_code_secret_partial_key": true, "ux_row_idx": true,
		"ux_code_excl": true, "ux_check": true, "ux_secret_check": true, "ux_row_check": true,
	} {
		if refs, ok := ux.ConstraintReferences(name, secret); refs != want || !ok {
			t.Errorf("ConstraintReferences(%s) = %v, %v, want %v, true", name, refs, ok, want)
		}
	}
	if _, ok := ux.ConstraintReferences("ux_missing", secret); ok {
		t.Error("ConstraintReferences(ux_missing) reported a constraint")
	}

	checkNames := func(name string) []string {
		t.Helper()
		tb := cat.Table("pdbcore_conscols_test", name)
		if tb == nil {
			t.Fatalf("%s not introspected", name)
		}
		var out []string
		for _, c := range tb.Checks {
			out = append(out, c.Name+":"+strings.Join(c.Columns, ","))
		}
		return out
	}
	if got := checkNames("parted"); !reflect.DeepEqual(got, []string{"parted_k_check:k"}) {
		t.Errorf("parted checks = %v", got)
	}
	if got := checkNames("parted_1"); !reflect.DeepEqual(got, []string{"parted_1_id_check:id"}) {
		t.Errorf("parted_1 checks = %v", got)
	}
}

// TestIntrospectNotValidLive checks that NOT VALID constraints leave rows
// that may break them visible: a NOT NULL ... NOT VALID column (PostgreSQL
// 18+) is nullable, and a NOT VALID foreign key is NotValid until validated.
func TestIntrospectNotValidLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_notvalid_test`,
		`CREATE TABLE pdbcore_notvalid_test.owners (id int PRIMARY KEY)`,
		`CREATE TABLE pdbcore_notvalid_test.items (
			id       int PRIMARY KEY,
			name     text NOT NULL,
			label    text,
			owner_id int NOT NULL,
			buyer_id int
		)`,
		`INSERT INTO pdbcore_notvalid_test.owners VALUES (1)`,
		`INSERT INTO pdbcore_notvalid_test.items VALUES (1, 'a', NULL, 2, 1)`,
		`ALTER TABLE pdbcore_notvalid_test.items ADD CONSTRAINT items_owner_fkey
			FOREIGN KEY (owner_id) REFERENCES pdbcore_notvalid_test.owners NOT VALID`,
		`ALTER TABLE pdbcore_notvalid_test.items ADD CONSTRAINT items_buyer_fkey
			FOREIGN KEY (buyer_id) REFERENCES pdbcore_notvalid_test.owners NOT VALID`,
		`ALTER TABLE pdbcore_notvalid_test.items VALIDATE CONSTRAINT items_buyer_fkey`,
	)
	ctx := context.Background()
	var num int
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatal(err)
	}
	if num >= 180000 {
		if _, err := tx.Exec(ctx, `ALTER TABLE pdbcore_notvalid_test.items
			ADD CONSTRAINT items_label_not_null NOT NULL label NOT VALID`); err != nil {
			t.Fatal(err)
		}
		var attnotnull bool
		if err := tx.QueryRow(ctx, `SELECT attnotnull FROM pg_attribute
			WHERE attrelid = 'pdbcore_notvalid_test.items'::regclass AND attname = 'label'`).Scan(&attnotnull); err != nil {
			t.Fatal(err)
		}
		if !attnotnull {
			t.Fatal("NOT NULL ... NOT VALID no longer sets attnotnull; revisit notNullColumn")
		}
	}
	cat, err := Introspect(ctx, tx, []string{"pdbcore_notvalid_test"})
	if err != nil {
		t.Fatal(err)
	}
	items := cat.Table("pdbcore_notvalid_test", "items")
	for col, want := range map[string]bool{"id": true, "name": true, "label": false, "owner_id": true, "buyer_id": false} {
		if got := items.Column(col).NotNull; got != want {
			t.Errorf("column %s NotNull = %v, want %v", col, got, want)
		}
	}
	for _, fk := range items.ForeignKeys {
		want := fk.Name == "items_owner_fkey"
		if fk.NotValid != want || fk.MayDangle() != want || fk.NotEnforced {
			t.Errorf("%s: NotValid %v MayDangle %v NotEnforced %v, want NotValid %v", fk.Name, fk.NotValid, fk.MayDangle(), fk.NotEnforced, want)
		}
	}
	if len(items.ForeignKeys) != 2 {
		t.Fatalf("foreign keys = %+v", items.ForeignKeys)
	}
	if num < 180000 {
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE pdbcore_notvalid_test.items SET label = 'x', owner_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE pdbcore_notvalid_test.items VALIDATE CONSTRAINT items_label_not_null`); err != nil {
		t.Fatal(err)
	}
	cat, err = Introspect(ctx, tx, []string{"pdbcore_notvalid_test"})
	if err != nil {
		t.Fatal(err)
	}
	if !cat.Table("pdbcore_notvalid_test", "items").Column("label").NotNull {
		t.Error("label is not NotNull after VALIDATE CONSTRAINT")
	}
}

// TestIntrospectIndexOpClassesLive checks Index.OpClasses: one entry per
// Columns entry, explicit and default operator classes by name, "" for an
// INCLUDE column, and no entry for an expression column.
func TestIntrospectIndexOpClassesLive(t *testing.T) {
	tx := liveTx(t,
		`CREATE SCHEMA pdbcore_opclass_test`,
		`CREATE TABLE pdbcore_opclass_test.hosts (addr inet, name text, tags text[], n int)`,
		`CREATE INDEX hosts_addr_idx ON pdbcore_opclass_test.hosts USING gist (addr inet_ops)`,
		`CREATE INDEX hosts_name_idx ON pdbcore_opclass_test.hosts (name text_pattern_ops) INCLUDE (n)`,
		`CREATE INDEX hosts_tags_idx ON pdbcore_opclass_test.hosts USING gin (tags)`,
		`CREATE INDEX hosts_expr_idx ON pdbcore_opclass_test.hosts (lower(name), n)`,
	)
	cat, err := Introspect(context.Background(), tx, []string{"pdbcore_opclass_test"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2][]string{
		"hosts_addr_idx": {{"addr"}, {"inet_ops"}},
		"hosts_name_idx": {{"name", "n"}, {"text_pattern_ops", ""}},
		"hosts_tags_idx": {{"tags"}, {"array_ops"}},
		"hosts_expr_idx": {{"n"}, {"int4_ops"}},
	}
	hosts := cat.Table("pdbcore_opclass_test", "hosts")
	if len(hosts.Indexes) != len(want) {
		t.Fatalf("indexes = %+v", hosts.Indexes)
	}
	for _, ix := range hosts.Indexes {
		w, ok := want[ix.Name]
		if !ok || !reflect.DeepEqual(ix.Columns, w[0]) || !reflect.DeepEqual(ix.OpClasses, w[1]) {
			t.Errorf("%s: columns %v opclasses %v, want %v", ix.Name, ix.Columns, ix.OpClasses, w)
		}
	}
}
