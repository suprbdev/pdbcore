package postgis

import (
	"strings"
	"testing"
)

const (
	ctorSQL = "CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END"
	geogSQL = "(" + ctorSQL + ")::geography"
)

func TestFlavourOf(t *testing.T) {
	tests := []struct {
		pgType string
		want   Flavour
	}{
		{"geometry", Geometry},
		{"geography", Geography},
		{"_geometry", Geometry}, // array element type
		{"_geography", Geography},
		{"text", NotSpatial},
		{"jsonb", NotSpatial},
		{"", NotSpatial},
		{"geometry_columns", NotSpatial}, // the PostGIS metadata view, not a type
	}
	for _, tc := range tests {
		if got := FlavourOf(tc.pgType); got != tc.want {
			t.Errorf("FlavourOf(%q) = %v, want %v", tc.pgType, got, tc.want)
		}
	}
}

func TestParseGeoJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		flavour Flavour
		wantSQL string
		wantArg string
		wantErr string
	}{
		{
			name:    "point",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}},
			flavour: Geometry,
			wantSQL: ctorSQL,
			wantArg: `{"coordinates":[1,2],"type":"Point"}`,
		},
		{
			name:    "geography casts",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}},
			flavour: Geography,
			wantSQL: geogSQL,
		},
		{
			name: "feature unwrapped to its geometry",
			in: map[string]any{
				"type":     "Feature",
				"geometry": map[string]any{"type": "Point", "coordinates": []any{3.0, 4.0}},
			},
			flavour: Geometry,
			wantArg: `{"coordinates":[3,4],"type":"Point"}`,
		},
		{
			name: "feature collection becomes a geometry collection",
			in: map[string]any{
				"type": "FeatureCollection",
				"features": []any{
					map[string]any{"type": "Feature", "geometry": map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}},
				},
			},
			flavour: Geometry,
			wantArg: `{"geometries":[{"coordinates":[1,2],"type":"Point"}],"type":"GeometryCollection"}`,
		},
		{
			name:    "explicit EPSG CRS is canonicalized into the parameter",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"properties": map[string]any{"name": "EPSG:3857"}}},
			flavour: Geometry,
			wantSQL: ctorSQL,
			wantArg: `{"coordinates":[1,2],"crs":{"properties":{"name":"EPSG:3857"},"type":"name"},"type":"Point"}`,
		},
		{
			name:    "urn CRS is canonicalized",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"type": "name", "properties": map[string]any{"name": "urn:ogc:def:crs:EPSG::2193"}}},
			flavour: Geometry,
			wantArg: `{"coordinates":[1,2],"crs":{"properties":{"name":"EPSG:2193"},"type":"name"},"type":"Point"}`,
		},
		{
			name:    "default CRS is dropped",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"properties": map[string]any{"name": "EPSG:4326"}}},
			flavour: Geometry,
			wantArg: `{"coordinates":[1,2],"type":"Point"}`,
		},
		{
			name:    "unreadable CRS is dropped",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"type": "name", "properties": map[string]any{"name": "urn:ogc:def:crs:OGC:1.3:CRS84"}}},
			flavour: Geometry,
			wantArg: `{"coordinates":[1,2],"type":"Point"}`,
		},
		{
			name: "feature CRS moves onto the geometry",
			in: map[string]any{
				"type":     "Feature",
				"crs":      map[string]any{"properties": map[string]any{"name": "EPSG:3857"}},
				"geometry": map[string]any{"type": "Point", "coordinates": []any{3.0, 4.0}},
			},
			flavour: Geometry,
			wantArg: `{"coordinates":[3,4],"crs":{"properties":{"name":"EPSG:3857"},"type":"name"},"type":"Point"}`,
		},
		{
			name:    "missing type",
			in:      map[string]any{"coordinates": []any{1.0, 2.0}},
			flavour: Geometry,
			wantErr: "type",
		},
		{
			name:    "missing coordinates",
			in:      map[string]any{"type": "Point"},
			flavour: Geometry,
			wantErr: "coordinates",
		},
		{
			name:    "unknown geojson type",
			in:      map[string]any{"type": "Wormhole", "coordinates": []any{1.0}},
			flavour: Geometry,
			wantErr: "unknown GeoJSON type",
		},
		{
			name:    "wrong Go type",
			in:      42,
			flavour: Geometry,
			wantErr: "GeoJSON object or a WKT string",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in, tc.flavour)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantArg != "" && got.Param != tc.wantArg {
				t.Errorf("param = %q, want %q", got.Param, tc.wantArg)
			}
			if tc.wantSQL != "" {
				if sql := got.SQL("$1"); sql != tc.wantSQL {
					t.Errorf("SQL = %q, want %q", sql, tc.wantSQL)
				}
			}
		})
	}
}

func TestParseWKT(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		flavour  Flavour
		wantArg  string
		wantSRID int
		wantErr  bool
	}{
		{name: "plain point", in: "POINT(1 2)", flavour: Geometry, wantArg: "POINT(1 2)"},
		{name: "ewkt keeps its own srid", in: "SRID=4326;POINT(1 2)", flavour: Geometry, wantArg: "SRID=4326;POINT(1 2)", wantSRID: 4326},
		{name: "lowercase accepted", in: "polygon((0 0,1 0,1 1,0 0))", flavour: Geometry, wantArg: "polygon((0 0,1 0,1 1,0 0))"},
		{name: "3d point", in: "POINT Z (1 2 3)", flavour: Geometry, wantArg: "POINT Z (1 2 3)"},
		{name: "empty geometry", in: "POINT EMPTY", flavour: Geometry, wantArg: "POINT EMPTY"},
		{name: "surrounding space trimmed", in: "  POINT(1 2) ", flavour: Geometry, wantArg: "POINT(1 2)"},
		{name: "explicit srid 0 kept", in: "SRID=0;POINT(1 2)", flavour: Geometry, wantArg: "SRID=0;POINT(1 2)"},
		{
			name: "bare wkt on geography gets a srid", in: "POINT(1 2)", flavour: Geography,
			wantArg: "SRID=4326;POINT(1 2)", wantSRID: 4326,
		},
		{name: "empty string", in: "", flavour: Geometry, wantErr: true},
		{name: "not wkt at all", in: "DROP TABLE users", flavour: Geometry, wantErr: true},
		{name: "sql fragment", in: "1=1); DROP TABLE users --", flavour: Geometry, wantErr: true},
		{name: "bare number", in: "42", flavour: Geometry, wantErr: true},
		{name: "srid prefix on garbage", in: "SRID=4326;DROP TABLE users", flavour: Geometry, wantErr: true},
		{name: "overflowing srid", in: "SRID=99999999999999999999;POINT(1 2)", flavour: Geometry, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in, tc.flavour)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Param != tc.wantArg {
				t.Errorf("param = %q, want %q", got.Param, tc.wantArg)
			}
			if got.SRID != tc.wantSRID {
				t.Errorf("SRID = %d, want %d", got.SRID, tc.wantSRID)
			}
			want := ctorSQL
			if tc.flavour == Geography {
				want = geogSQL
			}
			if sql := got.SQL("$1"); sql != want {
				t.Errorf("SQL = %q, want %q", sql, want)
			}
		})
	}
}

// TestSQLTextIsValueIndependent is the package-level mirror of the compiler's
// FuzzFilter invariant: two different values of the same shape must produce
// identical SQL text.
func TestSQLTextIsValueIndependent(t *testing.T) {
	inputs := []any{
		"POINT(1 2)",
		"SRID=3857;POINT(1 2)",
		"SRID=0;POINT(1 2)",
		map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}},
		map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"properties": map[string]any{"name": "EPSG:3857"}}},
		map[string]any{"type": "Feature", "geometry": map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}},
	}
	for _, f := range []Flavour{Geometry, Geography} {
		for _, srid := range []int{0, 4326, 3857} {
			want := ""
			for _, in := range inputs {
				v, err := ParseFor(in, f, srid)
				if err != nil {
					t.Fatalf("%v %d %v: %v", f, srid, in, err)
				}
				got := v.SQL("$1")
				if want == "" {
					want = got
				} else if got != want {
					t.Errorf("%v srid %d: SQL for %v = %q, want %q", f, srid, in, got, want)
				}
			}
		}
	}

	a, err := Parse(map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}, Geometry)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(map[string]any{"type": "Point", "coordinates": []any{99.0, -3.5}}, Geometry)
	if err != nil {
		t.Fatal(err)
	}
	if a.SQL("$1") != b.SQL("$1") {
		t.Errorf("SQL text varies with value: %q vs %q", a.SQL("$1"), b.SQL("$1"))
	}
	if a.Param == b.Param {
		t.Errorf("distinct values collapsed to the same parameter")
	}
}

func TestUnwrapDepthIsBounded(t *testing.T) {
	// A Feature chain deeper than maxUnwrapDepth must be rejected rather than
	// recursing without bound.
	doc := map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}
	for range maxUnwrapDepth + 2 {
		doc = map[string]any{"type": "Feature", "geometry": doc}
	}
	if _, err := Parse(doc, Geometry); err == nil {
		t.Fatal("expected an error for deeply nested GeoJSON")
	}
}

func TestOpsRender(t *testing.T) {
	tests := []struct {
		op   string
		want string
	}{
		{"bboxIntersects", `"t"."geom" && $1`},
		{"bboxContains", `"t"."geom" ~ $1`},
		{"bboxContainedBy", `"t"."geom" @ $1`},
		{"intersects", `ST_Intersects("t"."geom", $1)`},
		{"within", `ST_Within("t"."geom", $1)`},
		{"contains", `ST_Contains("t"."geom", $1)`},
		{"equals", `ST_Equals("t"."geom", $1)`},
		{"disjoint", `ST_Disjoint("t"."geom", $1)`},
		{"containsProperly", `ST_ContainsProperly("t"."geom", $1)`},
		{"covers", `ST_Covers("t"."geom", $1)`},
		{"coveredBy", `ST_CoveredBy("t"."geom", $1)`},
		{"crosses", `ST_Crosses("t"."geom", $1)`},
		{"overlaps", `ST_Overlaps("t"."geom", $1)`},
		{"touches", `ST_Touches("t"."geom", $1)`},
		{"bboxLeftOf", `"t"."geom" << $1`},
		{"bboxRightOf", `"t"."geom" >> $1`},
		{"bboxBelow", `"t"."geom" <<| $1`},
		{"bboxAbove", `"t"."geom" |>> $1`},
		{"bboxOverlapsOrLeftOf", `"t"."geom" &< $1`},
		{"bboxOverlapsOrRightOf", `"t"."geom" &> $1`},
		{"bboxOverlapsOrBelow", `"t"."geom" &<| $1`},
		{"bboxOverlapsOrAbove", `"t"."geom" |&> $1`},
		{"bboxEquals", `"t"."geom" ~= $1`},
		{"exactlyEquals", `"t"."geom" = $1`},
		{"orderingEquals", `ST_OrderingEquals("t"."geom", $1)`},
		{"intersects3D", `ST_3DIntersects("t"."geom", $1)`},
		{"bboxIntersectsND", `"t"."geom" &&& $1`},
		{"bboxIntersects2D", `"t"."geom" && $1`},
	}
	covered := map[string]bool{"dwithin": true, "beyond": true, "isNull": true}
	for _, tc := range tests {
		covered[tc.op] = true
		op, ok := OpByName(tc.op)
		if !ok {
			t.Fatalf("operator %q not found", tc.op)
		}
		if got := op.Render(`"t"."geom"`, "$1"); got != tc.want {
			t.Errorf("%s rendered %q, want %q", tc.op, got, tc.want)
		}
	}
	for _, name := range OpNames() {
		if !covered[name] {
			t.Errorf("operator %q has no rendering test", name)
		}
	}
}

// TestOpOrderIsAppendOnly pins the original vocabulary's order: consumers
// generate schemas from Ops, so new operators go at the end.
func TestOpOrderIsAppendOnly(t *testing.T) {
	want := []string{
		"bboxIntersects", "bboxContains", "bboxContainedBy", "intersects",
		"disjoint", "contains", "containsProperly", "within", "covers",
		"coveredBy", "crosses", "overlaps", "touches", "equals", "dwithin",
		"beyond", "isNull",
		"bboxLeftOf", "bboxRightOf", "bboxBelow", "bboxAbove",
		"bboxOverlapsOrLeftOf", "bboxOverlapsOrRightOf", "bboxOverlapsOrBelow",
		"bboxOverlapsOrAbove", "bboxEquals", "exactlyEquals", "orderingEquals",
		"intersects3D", "bboxIntersectsND", "bboxIntersects2D",
	}
	if got := OpNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("OpNames() = %v\nwant %v", got, want)
	}
}

// TestOpFlags pins the overload and index flags checked against the PostGIS
// 3.4 and 3.7 catalogs (pg_operator, pg_amop, pg_proc.prosupport).
func TestOpFlags(t *testing.T) {
	tests := []struct {
		op               string
		geography, index bool
	}{
		{"bboxIntersects", true, true},
		{"bboxContains", false, true},
		{"bboxContainedBy", false, true},
		{"intersects", true, true},
		{"disjoint", false, false},
		{"equals", false, true},
		{"dwithin", true, true},
		{"beyond", true, false},
		{"bboxLeftOf", false, true},
		{"bboxRightOf", false, true},
		{"bboxBelow", false, true},
		{"bboxAbove", false, true},
		{"bboxOverlapsOrLeftOf", false, true},
		{"bboxOverlapsOrRightOf", false, true},
		{"bboxOverlapsOrBelow", false, true},
		{"bboxOverlapsOrAbove", false, true},
		{"bboxEquals", false, true},
		{"exactlyEquals", true, false},
		{"orderingEquals", false, true},
		{"intersects3D", false, true},
		{"bboxIntersectsND", false, true},
		{"bboxIntersects2D", true, true},
	}
	for _, tc := range tests {
		op, ok := OpByName(tc.op)
		if !ok {
			t.Fatalf("operator %q not found", tc.op)
		}
		if op.Geography != tc.geography {
			t.Errorf("%s Geography = %v, want %v", tc.op, op.Geography, tc.geography)
		}
		if op.Index != tc.index {
			t.Errorf("%s Index = %v, want %v", tc.op, op.Index, tc.index)
		}
		if op.Description == "" {
			t.Errorf("%s has no description", tc.op)
		}
	}
}

func TestDWithinRender(t *testing.T) {
	op, ok := OpByName("dwithin")
	if !ok {
		t.Fatal("dwithin not found")
	}
	want := `ST_DWithin("t"."geom", $1, $2)`
	if got := op.Render(`"t"."geom"`, "$1", "$2"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	beyond, _ := OpByName("beyond")
	wantBeyond := `NOT ST_DWithin("t"."geom", $1, $2)`
	if got := beyond.Render(`"t"."geom"`, "$1", "$2"); got != wantBeyond {
		t.Errorf("got %q, want %q", got, wantBeyond)
	}
}

func TestOpKinds(t *testing.T) {
	tests := map[string]ArgKind{
		"bboxIntersects":   ArgBBox,
		"dwithin":          ArgDWithin,
		"beyond":           ArgDWithin,
		"isNull":           ArgBoolean,
		"intersects":       ArgGeometry,
		"bboxContains":     ArgBBox,
		"bboxLeftOf":       ArgGeometry,
		"bboxEquals":       ArgGeometry,
		"intersects3D":     ArgGeometry,
		"exactlyEquals":    ArgGeometry,
		"bboxIntersectsND": ArgGeometry,
		"bboxIntersects2D": ArgGeometry,
	}
	for name, want := range tests {
		op, ok := OpByName(name)
		if !ok {
			t.Fatalf("operator %q not found", name)
		}
		if got := op.Kind(); got != want {
			t.Errorf("%s kind = %v, want %v", name, got, want)
		}
	}
}

func TestBBoxSQL(t *testing.T) {
	got := BBoxSQL("$1", "$2", "$3", "$4", 0, Geometry)
	want := "ST_MakeEnvelope($1, $2, $3, $4, 4326)" // 0 defaults to WGS84
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := BBoxSQL("$1", "$2", "$3", "$4", 3857, Geography); !strings.HasSuffix(got, "::geography") {
		t.Errorf("geography box not cast: %q", got)
	}
}

func TestOutputSQLEvaluatesRefOnce(t *testing.T) {
	// ST_AsGeoJSON is STRICT and NULL::jsonb is NULL, so no CASE guard is
	// needed — and ref may be a computed-column function call, so it must
	// appear exactly once to avoid double evaluation.
	ref := `"t"."geom"`
	got := OutputSQL(ref)
	for _, want := range []string{"ST_AsGeoJSON", "::jsonb"} {
		if !strings.Contains(got, want) {
			t.Errorf("OutputSQL missing %q: %s", want, got)
		}
	}
	if n := strings.Count(got, ref); n != 1 {
		t.Errorf("OutputSQL references ref %d times, want 1: %s", n, got)
	}
}

func TestDerivedGeographyCasts(t *testing.T) {
	// Accessors without a geography overload must cast to geometry so they do
	// not fail at execution time.
	for _, suffix := range []string{"Centroid", "Envelope", "GeometryType", "IsValid"} {
		d, ok := DerivedBySuffix(suffix)
		if !ok {
			t.Fatalf("derived %q not found", suffix)
		}
		if got := d.SQL(`"t"."g"`, Geography); !strings.Contains(got, "::geometry") {
			t.Errorf("%s on geography is not cast: %s", suffix, got)
		}
	}
	// Area, Length and Perimeter do have geography overloads (and mean metres
	// there), so they must NOT be cast away.
	for _, suffix := range []string{"Area", "Length", "Perimeter"} {
		d, _ := DerivedBySuffix(suffix)
		if got := d.SQL(`"t"."g"`, Geography); strings.Contains(got, "::geometry") {
			t.Errorf("%s on geography should keep spheroidal units: %s", suffix, got)
		}
	}
}

func TestTransformAndSimplify(t *testing.T) {
	if got := TransformSQL(`"t"."g"`, 0, Geometry); got != `"t"."g"` {
		t.Errorf("a zero SRID must be a no-op, got %q", got)
	}
	if got := TransformSQL(`"t"."g"`, 3857, Geometry); got != `ST_Transform("t"."g", 3857)` {
		t.Errorf("got %q", got)
	}
	if got := SimplifySQL(`"t"."g"`, "$1", Geometry); got != `ST_SimplifyPreserveTopology("t"."g", $1)` {
		t.Errorf("got %q", got)
	}
}

func TestDerivedSQL(t *testing.T) {
	const ref = `"t"."g"`
	tests := []struct {
		suffix        string
		geometry, geo string
	}{
		{"X", `CASE WHEN ST_GeometryType("t"."g") = 'ST_Point' THEN ST_X("t"."g") END`,
			`CASE WHEN ST_GeometryType("t"."g"::geometry) = 'ST_Point' THEN ST_X("t"."g"::geometry) END`},
		{"Y", `CASE WHEN ST_GeometryType("t"."g") = 'ST_Point' THEN ST_Y("t"."g") END`,
			`CASE WHEN ST_GeometryType("t"."g"::geometry) = 'ST_Point' THEN ST_Y("t"."g"::geometry) END`},
		{"Z", `CASE WHEN ST_GeometryType("t"."g") = 'ST_Point' THEN ST_Z("t"."g") END`,
			`CASE WHEN ST_GeometryType("t"."g"::geometry) = 'ST_Point' THEN ST_Z("t"."g"::geometry) END`},
		{"M", `CASE WHEN ST_GeometryType("t"."g") = 'ST_Point' THEN ST_M("t"."g") END`,
			`CASE WHEN ST_GeometryType("t"."g"::geometry) = 'ST_Point' THEN ST_M("t"."g"::geometry) END`},
		{"NumPoints", `ST_NPoints("t"."g")`, `ST_NPoints("t"."g"::geometry)`},
		{"NumGeometries", `ST_NumGeometries("t"."g")`, `ST_NumGeometries("t"."g"::geometry)`},
		{"StartPoint",
			`ST_AsGeoJSON(CASE WHEN ST_GeometryType("t"."g") IN ('ST_LineString', 'ST_CircularString', 'ST_CompoundCurve') THEN ST_StartPoint("t"."g") END)::jsonb`,
			`ST_AsGeoJSON(CASE WHEN ST_GeometryType("t"."g"::geometry) IN ('ST_LineString', 'ST_CircularString', 'ST_CompoundCurve') THEN ST_StartPoint("t"."g"::geometry) END)::jsonb`},
		{"EndPoint",
			`ST_AsGeoJSON(CASE WHEN ST_GeometryType("t"."g") IN ('ST_LineString', 'ST_CircularString', 'ST_CompoundCurve') THEN ST_EndPoint("t"."g") END)::jsonb`,
			`ST_AsGeoJSON(CASE WHEN ST_GeometryType("t"."g"::geometry) IN ('ST_LineString', 'ST_CircularString', 'ST_CompoundCurve') THEN ST_EndPoint("t"."g"::geometry) END)::jsonb`},
		{"PointOnSurface", `ST_AsGeoJSON(ST_PointOnSurface("t"."g"))::jsonb`, `ST_AsGeoJSON(ST_PointOnSurface("t"."g"::geometry))::jsonb`},
		{"Wkt", `ST_AsText("t"."g")`, `ST_AsText("t"."g")`},
		{"Ewkt", `ST_AsEWKT("t"."g")`, `ST_AsEWKT("t"."g")`},
		{"IsEmpty", `ST_IsEmpty("t"."g")`, `ST_IsEmpty("t"."g"::geometry)`},
		{"IsSimple", `ST_IsSimple("t"."g")`, `ST_IsSimple("t"."g"::geometry)`},
		{"Xmin", `ST_XMin("t"."g")`, `ST_XMin("t"."g"::geometry)`},
		{"Ymin", `ST_YMin("t"."g")`, `ST_YMin("t"."g"::geometry)`},
		{"Xmax", `ST_XMax("t"."g")`, `ST_XMax("t"."g"::geometry)`},
		{"Ymax", `ST_YMax("t"."g")`, `ST_YMax("t"."g"::geometry)`},
		{"Area", `ST_Area("t"."g")`, `ST_Area("t"."g")`},
		{"Length", `ST_Length("t"."g")`, `ST_Length("t"."g")`},
		{"Perimeter", `ST_Perimeter("t"."g")`, `ST_Perimeter("t"."g")`},
		{"Centroid", `ST_AsGeoJSON(ST_Centroid("t"."g"))::jsonb`, `ST_AsGeoJSON(CASE WHEN ST_GeometryType("t"."g"::geometry) = 'ST_GeometryCollection' THEN ST_Centroid("t"."g"::geometry) ELSE ST_Centroid("t"."g")::geometry END)::jsonb`},
		{"Envelope", `ST_AsGeoJSON(ST_Envelope("t"."g"))::jsonb`, `ST_AsGeoJSON(ST_Envelope("t"."g"::geometry))::jsonb`},
		{"Srid", `ST_SRID("t"."g")`, `ST_SRID("t"."g")`},
		{"GeometryType", `ST_GeometryType("t"."g")`, `ST_GeometryType("t"."g"::geometry)`},
		{"IsValid", `ST_IsValid("t"."g")`, `ST_IsValid("t"."g"::geometry)`},
	}
	covered := map[string]bool{}
	for _, tc := range tests {
		covered[tc.suffix] = true
		d, ok := DerivedBySuffix(tc.suffix)
		if !ok {
			t.Fatalf("derived %q not found", tc.suffix)
		}
		if got := d.SQL(ref, Geometry); got != tc.geometry {
			t.Errorf("%s geometry SQL = %s\nwant %s", tc.suffix, got, tc.geometry)
		}
		if got := d.SQL(ref, Geography); got != tc.geo {
			t.Errorf("%s geography SQL = %s\nwant %s", tc.suffix, got, tc.geo)
		}
	}
	for _, s := range DerivedSuffixes() {
		if !covered[s] {
			t.Errorf("derived %q has no rendering test", s)
		}
	}
}

// TestDerivedOrderIsAppendOnly pins the original accessors' order: consumers
// generate fields from Deriveds, so new accessors go at the end.
func TestDerivedOrderIsAppendOnly(t *testing.T) {
	want := []string{
		"Area", "Length", "Perimeter", "Centroid", "Envelope", "Srid", "GeometryType", "IsValid",
		"X", "Y", "Z", "M", "NumPoints", "NumGeometries", "StartPoint", "EndPoint",
		"PointOnSurface", "Wkt", "Ewkt", "IsEmpty", "IsSimple", "Xmin", "Ymin", "Xmax", "Ymax",
	}
	if got := DerivedSuffixes(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("DerivedSuffixes() = %v\nwant %v", got, want)
	}
}

func TestDerivedApplies(t *testing.T) {
	gated := map[string]bool{"X": true, "Y": true, "Z": true, "M": true, "StartPoint": true, "EndPoint": true}
	tests := []struct {
		subtype string
		want    []string
	}{
		{"", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"Geometry", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"GeometryZM", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"Wormhole", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"Point", []string{"X", "Y"}},
		{"point", []string{"X", "Y"}},
		{"PointZ", []string{"X", "Y", "Z"}},
		{"PointM", []string{"X", "Y", "M"}},
		{"POINTZM", []string{"X", "Y", "Z", "M"}},
		{"MultiPoint", nil},
		{"LineString", []string{"StartPoint", "EndPoint"}},
		{"LineStringZ", []string{"StartPoint", "EndPoint"}},
		{"CircularString", []string{"StartPoint", "EndPoint"}},
		{"CompoundCurve", []string{"StartPoint", "EndPoint"}},
		{"MultiLineString", nil},
		{"Polygon", nil},
		{"GeometryCollection", nil},
		{"geometry(PointZ,4326)", []string{"X", "Y", "Z"}},
		{"geography(Point,4326)", []string{"X", "Y"}},
		{"geometry(LineStringM)", []string{"StartPoint", "EndPoint"}},
		{"public.geometry(PointM, 4326)", []string{"X", "Y", "M"}},
		{`"postgis"."geography"(PointZM,4326)`, []string{"X", "Y", "Z", "M"}},
		{"geometry(Polygon,3857)", nil},
		{"geometry(Geometry,3857)", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"geometry", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"text(Point)", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
		{"geometry(Point", []string{"X", "Y", "Z", "M", "StartPoint", "EndPoint"}},
	}
	for _, tc := range tests {
		want := map[string]bool{}
		for _, s := range tc.want {
			want[s] = true
		}
		for _, d := range Deriveds {
			exp := !gated[d.Suffix] || want[d.Suffix]
			if got := d.Applies(tc.subtype); got != exp {
				t.Errorf("%s.Applies(%q) = %v, want %v", d.Suffix, tc.subtype, got, exp)
			}
		}
	}
}

func TestDerivedAppliesTo(t *testing.T) {
	tests := []struct {
		suffix     string
		subtype    string
		hasZ, hasM bool
		want       bool
	}{
		{"Z", "Point", false, false, false},
		{"Z", "Point", true, false, true},
		{"M", "Point", true, false, false},
		{"M", "Point", false, true, true},
		{"M", "PointM", false, false, true},
		{"X", "LineString", true, true, false},
		{"Z", "", false, false, true},
		{"Z", "Geometry", false, false, true},
		{"Area", "Point", false, false, true},
	}
	for _, tc := range tests {
		d, ok := DerivedBySuffix(tc.suffix)
		if !ok {
			t.Fatalf("derived %q not found", tc.suffix)
		}
		if got := d.AppliesTo(tc.subtype, tc.hasZ, tc.hasM); got != tc.want {
			t.Errorf("%s.AppliesTo(%q, %v, %v) = %v, want %v", tc.suffix, tc.subtype, tc.hasZ, tc.hasM, got, tc.want)
		}
	}
}

func TestParseFor(t *testing.T) {
	gj := map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}
	crs := map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"properties": map[string]any{"name": "EPSG:3857"}}}
	const gjArg = `{"coordinates":[1,2],"type":"Point"}`
	const crsArg = `{"coordinates":[1,2],"crs":{"properties":{"name":"EPSG:3857"},"type":"name"},"type":"Point"}`
	transform := func(srid string) string {
		return "CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), " + srid + ") ELSE ST_GeomFromEWKT($1) END"
	}
	tests := []struct {
		name     string
		in       any
		flavour  Flavour
		srid     int
		wantSQL  string
		wantArg  string
		wantSRID int
	}{
		{"bare wkt unconstrained", "POINT(1 2)", Geometry, 0, ctorSQL, "POINT(1 2)", 0},
		{"bare wkt constrained", "POINT(1 2)", Geometry, 4326, transform("4326"), "SRID=4326;POINT(1 2)", 4326},
		{"bare wkt other srid", "POINT(1 2)", Geometry, 3857, transform("3857"), "SRID=3857;POINT(1 2)", 3857},
		{"ewkt srid 0 kept", "SRID=0;POINT(1 2)", Geometry, 3857, transform("3857"), "SRID=0;POINT(1 2)", 0},
		{"ewkt keeps own", "SRID=3857;POINT(1 2)", Geometry, 4326, transform("4326"), "SRID=3857;POINT(1 2)", 3857},
		{"ewkt unconstrained", "SRID=3857;POINT(1 2)", Geometry, 0, ctorSQL, "SRID=3857;POINT(1 2)", 3857},
		{"geography bare wkt", "POINT(1 2)", Geography, 4326, geogSQL, "SRID=4326;POINT(1 2)", 4326},
		{"geography bare wkt unconstrained", "POINT(1 2)", Geography, 0, geogSQL, "SRID=4326;POINT(1 2)", 4326},
		{"geography bare wkt ignores column srid", "POINT(1 2)", Geography, 4269, geogSQL, "SRID=4326;POINT(1 2)", 4326},
		{"geojson matching", gj, Geometry, 4326, transform("4326"), gjArg, 4326},
		{"geojson unconstrained", gj, Geometry, 0, ctorSQL, gjArg, 4326},
		{"geojson other srid", gj, Geometry, 3857, transform("3857"), gjArg, 4326},
		{"geojson crs matching", crs, Geometry, 3857, transform("3857"), crsArg, 3857},
		{"geojson geography", gj, Geography, 4326, geogSQL, gjArg, 4326},
	}
	for _, tc := range tests {
		v, err := ParseFor(tc.in, tc.flavour, tc.srid)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := v.SQL("$1"); got != tc.wantSQL {
			t.Errorf("%s: SQL = %q, want %q", tc.name, got, tc.wantSQL)
		}
		if v.Param != tc.wantArg {
			t.Errorf("%s: param = %q, want %q", tc.name, v.Param, tc.wantArg)
		}
		if v.SRID != tc.wantSRID {
			t.Errorf("%s: SRID = %d, want %d", tc.name, v.SRID, tc.wantSRID)
		}
	}
}

func TestParseDoesNotMutateInput(t *testing.T) {
	crs := map[string]any{"properties": map[string]any{"name": "EPSG:3857"}}
	in := map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": crs}
	if _, err := Parse(in, Geometry); err != nil {
		t.Fatal(err)
	}
	if len(in) != 3 || len(crs) != 1 {
		t.Errorf("input mutated: %v", in)
	}
}

func TestOutputJSONSQL(t *testing.T) {
	ref := `"t"."geom"`
	if got, want := OutputJSONSQL(ref), `ST_AsGeoJSON("t"."geom")::json`; got != want {
		t.Errorf("OutputJSONSQL = %s, want %s", got, want)
	}
	for _, d := range Deriveds {
		got, jsonb := d.JSONSQL(ref, Geometry), d.SQL(ref, Geometry)
		want := strings.ReplaceAll(jsonb, "::jsonb", "::json")
		if got != want {
			t.Errorf("%s JSONSQL = %s, want %s", d.Suffix, got, want)
		}
		if !d.GeoJSON && strings.Contains(got, "json") {
			t.Errorf("%s is not geometry valued but JSONSQL mentions json: %s", d.Suffix, got)
		}
	}
}
