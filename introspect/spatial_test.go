package introspect

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSpatialTypmod(t *testing.T) {
	cases := []struct {
		in   string
		want SpatialType
	}{
		{"", SpatialType{Subtype: SpatialGeometry}},
		{"geometry", SpatialType{Subtype: SpatialGeometry}},
		{"public.geography", SpatialType{Subtype: SpatialGeometry}},
		{`"we(ird)".geometry`, SpatialType{Subtype: SpatialGeometry}},
		{"geometry[]", SpatialType{Subtype: SpatialGeometry}},
		{"geometry(Point,4326)", SpatialType{Subtype: SpatialPoint, SRID: 4326}},
		{"public.geometry(Point,4326)", SpatialType{Subtype: SpatialPoint, SRID: 4326}},
		{`"we(ird)".geometry(PointZ)`, SpatialType{Subtype: SpatialPoint, HasZ: true}},
		{"geometry(PointZM,3857)", SpatialType{Subtype: SpatialPoint, SRID: 3857, HasZ: true, HasM: true}},
		{"geometry(LineStringM,4326)", SpatialType{Subtype: SpatialLineString, SRID: 4326, HasM: true}},
		{"geography(Polygon,4326)", SpatialType{Subtype: SpatialPolygon, SRID: 4326}},
		{"geography(MultiPolygonZ,4326)", SpatialType{Subtype: SpatialMultiPolygon, SRID: 4326, HasZ: true}},
		{"geometry(MultiPoint)", SpatialType{Subtype: SpatialMultiPoint}},
		{"geometry(MultiLineString,2154)", SpatialType{Subtype: SpatialMultiLineString, SRID: 2154}},
		{"geometry(GeometryCollection)", SpatialType{Subtype: SpatialGeometryCollection}},
		{"geometry(Geometry,4326)", SpatialType{Subtype: SpatialGeometry, SRID: 4326}},
		{"geometry(GeometryZ)", SpatialType{Subtype: SpatialGeometry, HasZ: true}},
		{"geometry(Point,4326)[]", SpatialType{Subtype: SpatialPoint, SRID: 4326}},
		{"geometry(Point,4326)[][]", SpatialType{Subtype: SpatialPoint, SRID: 4326}},
		{"geometry(point, 4326)", SpatialType{Subtype: SpatialPoint, SRID: 4326}},
		{"geometry(CircularString,2154)", SpatialType{Subtype: "CircularString", SRID: 2154}},
		{"geometry(Tin)", SpatialType{Subtype: "Tin"}},
	}
	for _, tc := range cases {
		got, err := parseSpatialTypmod(tc.in)
		if err != nil {
			t.Errorf("parseSpatialTypmod(%q): %v", tc.in, err)
			continue
		}
		if *got != tc.want {
			t.Errorf("parseSpatialTypmod(%q) = %+v, want %+v", tc.in, *got, tc.want)
		}
	}
}

func TestParseSpatialTypmodErrors(t *testing.T) {
	for _, in := range []string{
		"geometry)",
		"geometry(Point,x)",
		"geometry(Point,-1)",
		"geometry(Point,4326,1)",
		"geometry(,4326)",
		"geometry(ZM)",
		"geometry(Point 4)",
	} {
		if got, err := parseSpatialTypmod(in); err == nil {
			t.Errorf("parseSpatialTypmod(%q) = %+v, want error", in, *got)
		}
	}
}

func TestSpatialTypmodRoundTrip(t *testing.T) {
	for _, in := range []string{
		"(Point,4326)", "(PointZ)", "(PointZM,3857)", "(LineStringM,4326)",
		"(Geometry,4326)", "(GeometryZ)", "(MultiPolygonZ,4326)", "(Tin)",
	} {
		st, err := parseSpatialTypmod("geometry" + in)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Typmod(); got != in {
			t.Errorf("Typmod() = %q, want %q", got, in)
		}
	}
	if got := (&SpatialType{Subtype: SpatialGeometry}).Typmod(); got != "" {
		t.Errorf("unconstrained Typmod() = %q, want empty", got)
	}
}

func TestApplyTypmod(t *testing.T) {
	cases := []struct {
		name      string
		col       Column
		formatted string
		builtin   bool
		spatial   *SpatialType
		sqlType   string
	}{
		{"constrained geometry", Column{PGType: "geometry"}, "public.geometry(Point,4326)", false,
			&SpatialType{Subtype: SpatialPoint, SRID: 4326}, ""},
		{"unconstrained geography", Column{PGType: "geography"}, "", false,
			&SpatialType{Subtype: SpatialGeometry}, ""},
		{"geometry array", Column{PGType: "_geometry", IsArray: true}, "geometry(PolygonZ)[]", false,
			&SpatialType{Subtype: SpatialPolygon, HasZ: true}, ""},
		{"malformed spatial degrades", Column{PGType: "geometry"}, "geometry(Point,x)", false,
			&SpatialType{Subtype: SpatialGeometry}, ""},
		{"varchar", Column{PGType: "varchar"}, "character varying(255)", true, nil, "character varying(255)"},
		{"numeric", Column{PGType: "numeric"}, "numeric(10,2)", true, nil, "numeric(10,2)"},
		{"unmodified builtin", Column{PGType: "text"}, "", true, nil, ""},
		{"extension type", Column{PGType: "citext"}, "public.citext(3)", false, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			col := tc.col
			applyTypmod(&col, tc.formatted, tc.builtin)
			if (col.Spatial == nil) != (tc.spatial == nil) || (col.Spatial != nil && *col.Spatial != *tc.spatial) {
				t.Errorf("Spatial = %+v, want %+v", col.Spatial, tc.spatial)
			}
			if col.SQLType != tc.sqlType {
				t.Errorf("SQLType = %q, want %q", col.SQLType, tc.sqlType)
			}
		})
	}
}

func TestColumnJSONOmitsTypmodWhenAbsent(t *testing.T) {
	b, err := json.Marshal(&Column{Name: "id", PGType: "int4"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "spatial") || strings.Contains(s, "sql_type") {
		t.Fatalf("plain column serialized typmod fields: %s", s)
	}
}

func TestHasExtension(t *testing.T) {
	cat := &Catalog{Extensions: []*Extension{{Name: "postgis", Version: "3.4.3", Schema: "public"}}}
	if !cat.HasExtension("postgis") || cat.HasExtension("pgcrypto") {
		t.Fatal("HasExtension mismatch")
	}
	if e := cat.Extension("postgis"); e == nil || e.Schema != "public" {
		t.Fatalf("Extension(postgis) = %+v", e)
	}
	if (&Catalog{}).HasExtension("postgis") {
		t.Fatal("empty catalog reports an extension")
	}
}
