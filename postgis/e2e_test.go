package postgis_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/postgis"
)

func testConn(t *testing.T) *pgx.Conn {
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
	return conn
}

type sample struct {
	wkt, subtype string
	geography    bool
}

var samples = []sample{
	{"POINT(1 2)", "Point", true},
	{"POINT Z (1 2 3)", "PointZ", true},
	{"POINT M (1 2 4)", "PointM", true},
	{"POINT ZM (1 2 3 4)", "PointZM", true},
	{"POINT EMPTY", "Point", true},
	{"MULTIPOINT((1 2),(3 4))", "MultiPoint", true},
	{"LINESTRING(0 0,1 1,2 0)", "LineString", true},
	{"LINESTRING Z (0 0 0,1 1 1)", "LineStringZ", true},
	{"LINESTRING EMPTY", "LineString", true},
	{"CIRCULARSTRING(0 0,1 1,2 0)", "CircularString", false},
	{"COMPOUNDCURVE((0 0,1 1),(1 1,2 0))", "CompoundCurve", false},
	{"MULTILINESTRING((0 0,1 1),(2 2,3 3))", "MultiLineString", true},
	{"POLYGON((0 0,4 0,4 4,0 4,0 0))", "Polygon", true},
	{"MULTIPOLYGON(((0 0,1 0,1 1,0 0)))", "MultiPolygon", true},
	{"GEOMETRYCOLLECTION(POINT(1 2),LINESTRING(0 0,1 1))", "GeometryCollection", true},
	{"GEOMETRYCOLLECTION EMPTY", "GeometryCollection", true},
	{"CURVEPOLYGON(CIRCULARSTRING(0 0,4 0,4 4,0 4,0 0))", "CurvePolygon", false},
	{"MULTICURVE((0 0,1 1),CIRCULARSTRING(0 0,1 1,2 0))", "MultiCurve", false},
	{"MULTISURFACE(CURVEPOLYGON(CIRCULARSTRING(0 0,4 0,4 4,0 4,0 0)),((5 5,6 5,6 6,5 5)))", "MultiSurface", false},
	{"TRIANGLE((0 0,1 0,0 1,0 0))", "Triangle", false},
	{"TIN(((0 0 0,1 0 0,0 1 1,0 0 0)),((1 0 0,1 1 1,0 1 1,1 0 0)))", "Tin", false},
	{"POLYHEDRALSURFACE(((0 0 0,1 0 0,1 1 0,0 1 0,0 0 0)),((1 0 0,2 0 1,2 1 1,1 1 0,1 0 0)))", "PolyhedralSurface", false},
}

// column renders $1 (a WKT parameter) as a value of the given flavour in
// WGS84, the way a spatial column would hold it.
func column(f postgis.Flavour) string {
	expr := "ST_SetSRID(ST_GeomFromEWKT($1), 4326)"
	if f == postgis.Geography {
		expr = "(" + expr + ")::geography"
	}
	return expr
}

// TestDerivedsExecute runs every accessor over every sample geometry and
// geography: none may raise, and an accessor whose Applies rejects the
// sample's subtype must yield NULL.
func TestDerivedsExecute(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, s := range samples {
			if f == postgis.Geography && !s.geography {
				continue
			}
			for _, d := range postgis.Deriveds() {
				q := "SELECT " + d.SQL("t.g", f, postgis.Output{}) + " FROM (SELECT " + column(f) + " AS g) AS t"
				var got any
				if err := conn.QueryRow(ctx, q, s.wkt).Scan(&got); err != nil {
					t.Errorf("%s %s on %s: %v\n%s", f, d.Suffix, s.wkt, err, q)
					continue
				}
				if !d.Applies(s.subtype) && got != nil {
					t.Errorf("%s %s on %s = %v, but Applies(%q) is false", f, d.Suffix, s.wkt, got, s.subtype)
				}
			}
		}
	}
}

func TestDerivedValues(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	tests := []struct {
		suffix, wkt string
		want        string
	}{
		{"X", "POINT(1 2)", "1"},
		{"Y", "POINT(1 2)", "2"},
		{"Z", "POINT Z (1 2 3)", "3"},
		{"M", "POINT M (1 2 4)", "4"},
		{"Z", "POINT(1 2)", "<nil>"},
		{"M", "POINT Z (1 2 3)", "<nil>"},
		{"X", "POINT EMPTY", "<nil>"},
		{"X", "LINESTRING(0 0,1 1)", "<nil>"},
		{"Y", "MULTIPOINT((1 2))", "<nil>"},
		{"NumPoints", "LINESTRING(0 0,1 1,2 0)", "3"},
		{"NumGeometries", "MULTIPOINT((1 2),(3 4))", "2"},
		{"NumGeometries", "POINT(1 2)", "1"},
		{"StartPoint", "LINESTRING(0 0,1 1,2 0)", "map[coordinates:[0 0] type:Point]"},
		{"EndPoint", "LINESTRING(0 0,1 1,2 0)", "map[coordinates:[2 0] type:Point]"},
		{"StartPoint", "POLYGON((0 0,4 0,4 4,0 4,0 0))", "<nil>"},
		{"EndPoint", "POINT(1 2)", "<nil>"},
		{"PointOnSurface", "POINT(1 2)", "map[coordinates:[1 2] type:Point]"},
		{"Wkt", "POINT(1 2)", "POINT(1 2)"},
		{"Ewkt", "POINT(1 2)", "SRID=4326;POINT(1 2)"},
		{"IsEmpty", "POINT EMPTY", "true"},
		{"IsEmpty", "POINT(1 2)", "false"},
		{"IsSimple", "LINESTRING(0 0,2 2,2 0,0 2)", "false"},
		{"IsSimple", "LINESTRING(0 0,1 1)", "true"},
		{"Xmin", "LINESTRING(0.5 -1,3 2)", "0.5"},
		{"Ymin", "LINESTRING(0.5 -1,3 2)", "-1"},
		{"Xmax", "LINESTRING(0.5 -1,3 2)", "3"},
		{"Ymax", "LINESTRING(0.5 -1,3 2)", "2"},
		{"Xmin", "POINT EMPTY", "<nil>"},
	}
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, tc := range tests {
			d, ok := postgis.DerivedBySuffix(tc.suffix)
			if !ok {
				t.Fatalf("derived %q not found", tc.suffix)
			}
			q := "SELECT " + d.SQL("t.g", f, postgis.Output{}) + " FROM (SELECT " + column(f) + " AS g) AS t"
			var got any
			if err := conn.QueryRow(ctx, q, tc.wkt).Scan(&got); err != nil {
				t.Errorf("%s %s on %s: %v", f, tc.suffix, tc.wkt, err)
				continue
			}
			if s := fmt.Sprint(got); s != tc.want {
				t.Errorf("%s %s on %s = %s, want %s", f, tc.suffix, tc.wkt, s, tc.want)
			}
		}
	}
}

// TestGeographyMeasures pins the units of the measuring accessors on
// geography: metres on the spheroid, never degrees from a geometry cast.
func TestGeographyMeasures(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	const square = "POLYGON((-0.130 51.507,-0.126 51.507,-0.126 51.509,-0.130 51.509,-0.130 51.507))"
	for suffix, want := range map[string][2]float64{"Perimeter": {990, 1010}, "Length": {0, 0}, "Area": {61000, 62500}} {
		d, _ := postgis.DerivedBySuffix(suffix)
		q := "SELECT " + d.SQL("t.g", postgis.Geography, postgis.Output{}) + " FROM (SELECT " + column(postgis.Geography) + " AS g) AS t"
		var got float64
		if err := conn.QueryRow(ctx, q, square).Scan(&got); err != nil {
			t.Fatalf("%s: %v", suffix, err)
		}
		if got < want[0] || got > want[1] {
			t.Errorf("geography %s = %v, want within %v", suffix, got, want)
		}
	}
	d, _ := postgis.DerivedBySuffix("Centroid")
	q := "SELECT ST_AsText(ST_GeomFromGeoJSON(" + d.SQL("t.g", postgis.Geography, postgis.Output{}) + "::text)) FROM (SELECT " + column(postgis.Geography) + " AS g) AS t"
	for _, wkt := range []string{square, "GEOMETRYCOLLECTION(POINT(0 0),POINT(2 0))"} {
		var got string
		if err := conn.QueryRow(ctx, q, wkt).Scan(&got); err != nil {
			t.Errorf("geography Centroid on %s: %v", wkt, err)
			continue
		}
		if !strings.HasPrefix(got, "POINT(") {
			t.Errorf("geography Centroid on %s = %s", wkt, got)
		}
	}
}

// predicate renders op over t.g the way pdbr does: geography columns are cast
// to geometry, along with the operand, for operators without a geography
// overload. The column WKT is $1; the operand parameters follow.
func predicate(t *testing.T, op postgis.Op, f postgis.Flavour, operand any) (string, []any) {
	t.Helper()
	ref, render := "t.g", f
	if f == postgis.Geography && !op.Geography {
		ref, render = "t.g::geometry", postgis.Geometry
	}
	switch op.Kind() {
	case postgis.ArgBBox:
		return op.Render(ref, postgis.BBoxSQL("$2", "$3", "$4", "$5", 4326, render)), []any{0.0, 0.0, 10.0, 10.0}
	case postgis.ArgDWithin:
		v, err := postgis.Parse(operand, render)
		if err != nil {
			t.Fatal(err)
		}
		return op.Render(ref, v.SQL("$2"), "$3"), []any{v.Param, 5.0}
	default:
		v, err := postgis.Parse(operand, render)
		if err != nil {
			t.Fatal(err)
		}
		return op.Render(ref, v.SQL("$2")), []any{v.Param}
	}
}

// TestOpsExecute runs every operator against every sample geometry and
// geography with a GeoJSON polygon operand: none may raise.
func TestOpsExecute(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	operand := map[string]any{
		"type":        "Polygon",
		"coordinates": []any{[]any{[]any{0.0, 0.0}, []any{3.0, 0.0}, []any{3.0, 3.0}, []any{0.0, 0.0}}},
	}
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, op := range postgis.Ops() {
			if op.Kind() == postgis.ArgBoolean {
				continue
			}
			pred, params := predicate(t, op, f, operand)
			q := "SELECT " + pred + " FROM (SELECT " + column(f) + " AS g) AS t"
			for _, s := range samples {
				if f == postgis.Geography && !s.geography {
					continue
				}
				var got *bool
				if err := conn.QueryRow(ctx, q, append([]any{s.wkt}, params...)...).Scan(&got); err != nil {
					t.Errorf("%s %s on %s: %v\n%s", f, op.Name, s.wkt, err, q)
				}
			}
		}
	}
}

func TestOpValues(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	tests := []struct {
		op, col, operand string
		want             bool
	}{
		{"bboxLeftOf", "POINT(1 2)", "POINT(5 2)", true},
		{"bboxLeftOf", "POINT(5 2)", "POINT(1 2)", false},
		{"bboxRightOf", "POINT(5 2)", "POINT(1 2)", true},
		{"bboxRightOf", "POINT(1 2)", "POINT(5 2)", false},
		{"bboxBelow", "POINT(1 2)", "POINT(1 9)", true},
		{"bboxBelow", "POINT(1 9)", "POINT(1 2)", false},
		{"bboxAbove", "POINT(1 9)", "POINT(1 2)", true},
		{"bboxAbove", "POINT(1 2)", "POINT(1 9)", false},
		{"bboxOverlapsOrLeftOf", "LINESTRING(0 0,2 0)", "LINESTRING(1 0,3 0)", true},
		{"bboxOverlapsOrLeftOf", "LINESTRING(0 0,4 0)", "LINESTRING(1 0,3 0)", false},
		{"bboxOverlapsOrRightOf", "LINESTRING(2 0,4 0)", "LINESTRING(1 0,3 0)", true},
		{"bboxOverlapsOrRightOf", "LINESTRING(0 0,4 0)", "LINESTRING(1 0,3 0)", false},
		{"bboxOverlapsOrBelow", "LINESTRING(0 0,0 2)", "LINESTRING(0 1,0 3)", true},
		{"bboxOverlapsOrBelow", "LINESTRING(0 0,0 4)", "LINESTRING(0 1,0 3)", false},
		{"bboxOverlapsOrAbove", "LINESTRING(0 2,0 4)", "LINESTRING(0 1,0 3)", true},
		{"bboxOverlapsOrAbove", "LINESTRING(0 0,0 4)", "LINESTRING(0 1,0 3)", false},
		{"bboxEquals", "LINESTRING(0 0,1 1)", "LINESTRING(1 0,0 1)", true},
		{"bboxEquals", "LINESTRING(0 0,1 1)", "LINESTRING(0 0,2 2)", false},
		{"exactlyEquals", "LINESTRING(0 0,1 1)", "LINESTRING(0 0,1 1)", true},
		{"exactlyEquals", "LINESTRING(0 0,1 1)", "LINESTRING(1 1,0 0)", false},
		{"orderingEquals", "LINESTRING(0 0,1 1)", "LINESTRING(0 0,1 1)", true},
		{"orderingEquals", "LINESTRING(0 0,1 1)", "LINESTRING(1 1,0 0)", false},
		{"equals", "LINESTRING(0 0,1 1)", "LINESTRING(1 1,0 0)", true},
		{"intersects3D", "POINT Z (1 1 1)", "LINESTRING Z (1 0 0,1 2 2)", true},
		{"intersects3D", "POINT Z (1 1 5)", "LINESTRING Z (1 0 0,1 2 2)", false},
		{"bboxIntersectsND", "POINT Z (1 1 1)", "LINESTRING Z (1 0 0,1 2 2)", true},
		{"bboxIntersectsND", "POINT Z (1 1 5)", "LINESTRING Z (1 0 0,1 2 2)", false},
		{"intersects", "POINT Z (1 1 5)", "LINESTRING Z (1 0 0,1 2 2)", true},
		{"bboxIntersects2D", "LINESTRING(0 0,2 2)", "LINESTRING(1 1,3 3)", true},
		{"bboxIntersects2D", "POINT(5 5)", "LINESTRING(0 0,1 1)", false},
	}
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, tc := range tests {
			op, ok := postgis.OpByName(tc.op)
			if !ok {
				t.Fatalf("operator %q not found", tc.op)
			}
			pred, params := predicate(t, op, f, "SRID=4326;"+tc.operand)
			q := "SELECT " + pred + " FROM (SELECT " + column(f) + " AS g) AS t"
			var got bool
			if err := conn.QueryRow(ctx, q, append([]any{tc.col}, params...)...).Scan(&got); err != nil {
				t.Errorf("%s %s(%s, %s): %v", f, tc.op, tc.col, tc.operand, err)
				continue
			}
			if got != tc.want {
				t.Errorf("%s %s(%s, %s) = %v, want %v", f, tc.op, tc.col, tc.operand, got, tc.want)
			}
		}
	}
}

// TestParseForExecutes binds every input form to one prepared statement per
// column SRID (the SQL text must not depend on the form) under both custom and
// generic plans. Each table holds WGS84 (1 2) in its own SRID; the operands are
// polygons around it in 4326 or 3857. GeoJSON is reprojected to the column
// SRID; bare WKT takes the column SRID, so its 4326 coordinates only hit the
// WGS84 table; EWKT keeps its SRID, so it only works where that matches (a
// mismatch is PostGIS's mixed-SRID error).
func TestParseForExecutes(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	ring4326 := []any{[]any{0.0, 0.0}, []any{3.0, 0.0}, []any{3.0, 3.0}, []any{0.0, 3.0}, []any{0.0, 0.0}}
	ring3857 := []any{[]any{100000.0, 200000.0}, []any{120000.0, 200000.0}, []any{120000.0, 240000.0}, []any{100000.0, 240000.0}, []any{100000.0, 200000.0}}
	inputs := []struct {
		name      string
		in        any
		onlyWGS84 bool
		onlySRID  int
	}{
		{name: "bare wkt", in: "POLYGON((0 0,3 0,3 3,0 3,0 0))", onlyWGS84: true},
		{name: "ewkt 4326", in: "SRID=4326;POLYGON((0 0,3 0,3 3,0 3,0 0))", onlySRID: 4326},
		{name: "ewkt 3857", in: "SRID=3857;POLYGON((100000 200000,120000 200000,120000 240000,100000 240000,100000 200000))", onlySRID: 3857},
		{name: "geojson", in: map[string]any{"type": "Polygon", "coordinates": []any{ring4326}}},
		{name: "geojson feature", in: map[string]any{"type": "Feature", "geometry": map[string]any{"type": "Polygon", "coordinates": []any{ring4326}}}},
		{name: "geojson crs 3857", in: map[string]any{
			"type": "Polygon", "coordinates": []any{ring3857},
			"crs": map[string]any{"properties": map[string]any{"name": "EPSG:3857"}},
		}},
	}
	for _, srid := range []int{4326, 3857} {
		table := fmt.Sprintf("pdbcore_srid_%d", srid)
		if _, err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s; CREATE TABLE %s (g geometry(Point,%d)); INSERT INTO %s VALUES (ST_Transform(ST_SetSRID(ST_MakePoint(1,2),4326),%d)); CREATE INDEX ON %s USING gist (g)", table, table, srid, table, srid, table)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+table) })
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			if _, err := conn.Exec(ctx, "SET plan_cache_mode = "+mode); err != nil {
				t.Fatal(err)
			}
			var q string
			for _, tc := range inputs {
				v, err := postgis.ParseFor(tc.in, postgis.Geometry, srid)
				if err != nil {
					t.Fatal(err)
				}
				sql := fmt.Sprintf("SELECT count(*) FROM %s WHERE ST_Intersects(g, %s)", table, v.SQL("$1"))
				if q == "" {
					q = sql
				} else if sql != q {
					t.Fatalf("srid %d %s: SQL text differs:\n%s\n%s", srid, tc.name, sql, q)
				}
				var n int
				err = conn.QueryRow(ctx, q, v.Param).Scan(&n)
				if tc.onlySRID != 0 && tc.onlySRID != srid {
					if err == nil || !strings.Contains(err.Error(), "mixed SRID") {
						t.Errorf("srid %d %s %s: err %v, want mixed SRID", srid, mode, tc.name, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("srid %d %s %s: %v\n%s", srid, mode, tc.name, err, q)
					continue
				}
				wantHit := !tc.onlyWGS84 || srid == 4326
				if wantHit != (n == 1) {
					t.Errorf("srid %d %s %s: count %d, want hit %v", srid, mode, tc.name, n, wantHit)
				}
			}
		}
	}
	if _, err := conn.Exec(ctx, "RESET plan_cache_mode"); err != nil {
		t.Fatal(err)
	}
}

// TestJSONOutputMatchesJSONB checks the json variants decode to the same
// value as the jsonb ones for every accessor over every sample.
func TestJSONOutputMatchesJSONB(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, s := range samples {
			if f == postgis.Geography && !s.geography {
				continue
			}
			for _, d := range postgis.Deriveds() {
				from := " FROM (SELECT " + column(f) + " AS g) AS t"
				var a, b any
				if err := conn.QueryRow(ctx, "SELECT "+d.SQL("t.g", f, postgis.Output{})+from, s.wkt).Scan(&a); err != nil {
					t.Fatalf("%s %s on %s: %v", f, d.Suffix, s.wkt, err)
				}
				if err := conn.QueryRow(ctx, "SELECT "+d.SQL("t.g", f, postgis.Output{JSON: true})+from, s.wkt).Scan(&b); err != nil {
					t.Fatalf("%s %s json on %s: %v", f, d.Suffix, s.wkt, err)
				}
				if fmt.Sprint(a) != fmt.Sprint(b) {
					t.Errorf("%s %s on %s: jsonb %v, json %v", f, d.Suffix, s.wkt, a, b)
				}
			}
		}
	}
	var got any
	if err := conn.QueryRow(ctx, "SELECT "+postgis.OutputSQL("NULL::geometry", postgis.Output{JSON: true})).Scan(&got); err != nil || got != nil {
		t.Errorf("NULL geometry = %v, %v; want NULL", got, err)
	}
}

// TestDistanceUnits measures one degree of longitude on the equator: SRID
// units for geometry, metres for geography and for geometry on the
// spheroid.
func TestDistanceUnits(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	const a, b = "POINT(0 0)", "POINT(1 0)"
	tests := []struct {
		name     string
		sql      string
		min, max float64
	}{
		{"geometry", postgis.DistanceSQL(column(postgis.Geometry), strings.ReplaceAll(column(postgis.Geometry), "$1", "$2")), 1, 1},
		{"geography", postgis.DistanceSQL(column(postgis.Geography), strings.ReplaceAll(column(postgis.Geography), "$1", "$2")), 111000, 111400},
		{"spheroid", postgis.SpheroidDistanceSQL(column(postgis.Geometry), strings.ReplaceAll(column(postgis.Geometry), "$1", "$2")), 111000, 111400},
	}
	for _, tc := range tests {
		var got float64
		if err := conn.QueryRow(ctx, "SELECT "+tc.sql, a, b).Scan(&got); err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, tc.sql)
		}
		if got < tc.min || got > tc.max {
			t.Errorf("%s distance = %v, want within [%v, %v]", tc.name, got, tc.min, tc.max)
		}
	}
}

// TestPrecisionAndBufferLive rounds GeoJSON coordinates with a bound digits
// parameter and buffers geometry in SRID units and geography in metres.
func TestPrecisionAndBufferLive(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	const pt = "POINT(-0.128123456789 51.508123456789)"
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, tc := range []struct {
			digits int64
			want   string
		}{
			{0, "[0,52]"},
			{3, "[-0.128,51.508]"},
			{6, "[-0.128123,51.508123]"},
		} {
			var got string
			q := "SELECT (" + postgis.OutputSQL(column(f), postgis.Output{JSON: true, Precision: "$2"}) + "->'coordinates')::text"
			if err := conn.QueryRow(ctx, q, pt, tc.digits).Scan(&got); err != nil {
				t.Fatalf("%s precision %d: %v\n%s", f, tc.digits, err, q)
			}
			if strings.ReplaceAll(got, " ", "") != tc.want {
				t.Errorf("%s precision %d = %s, want %s", f, tc.digits, got, tc.want)
			}
			var jb string
			q = "SELECT (" + postgis.OutputSQL(column(f), postgis.Output{Precision: "$2"}) + "->'coordinates')::text"
			if err := conn.QueryRow(ctx, q, pt, tc.digits).Scan(&jb); err != nil {
				t.Fatalf("%s jsonb precision %d: %v", f, tc.digits, err)
			}
		}
	}
	var area float64
	q := "SELECT ST_Area(" + postgis.BufferSQL(column(postgis.Geography), "$2") + ")"
	if err := conn.QueryRow(ctx, q, "POINT(0 0)", 1000.0).Scan(&area); err != nil {
		t.Fatalf("geography buffer: %v\n%s", err, q)
	}
	if want := 3.14159 * 1000 * 1000; area < want*0.97 || area > want*1.01 {
		t.Errorf("geography buffer of 1000 m has area %v m², want about %v", area, want)
	}
	q = "SELECT ST_Area(" + postgis.BufferSQL(column(postgis.Geometry), "$2") + ")"
	if err := conn.QueryRow(ctx, q, "POINT(0 0)", 1.0).Scan(&area); err != nil {
		t.Fatalf("geometry buffer: %v\n%s", err, q)
	}
	if area < 3.1 || area > 3.15 {
		t.Errorf("geometry buffer of 1 unit has area %v, want about pi", area)
	}
	var empty bool
	q = "SELECT ST_IsEmpty(" + postgis.BufferSQL(column(postgis.Geometry), "$2") + ")"
	if err := conn.QueryRow(ctx, q, "POLYGON((0 0,1 0,1 1,0 1,0 0))", -1.0).Scan(&empty); err != nil || !empty {
		t.Errorf("negative buffer erasing a polygon = %v, %v; want empty", empty, err)
	}
	var typ string
	q = "SELECT pg_typeof(" + postgis.BufferSQL(column(postgis.Geography), "$2") + ")::text"
	if err := conn.QueryRow(ctx, q, "POINT(0 0)", 10.0).Scan(&typ); err != nil || typ != "geography" {
		t.Errorf("geography buffer type = %q, %v", typ, err)
	}
}
