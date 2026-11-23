package contrib_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
)

func TestCubeRenders(t *testing.T) {
	v := contrib.Cube().Bind("ext")
	ops := map[string]string{
		"contains":    `(t.c) OPERATOR("ext".@>) $1::"ext".cube`,
		"containedBy": `(t.c) OPERATOR("ext".<@) $1::"ext".cube`,
		"overlaps":    `(t.c) OPERATOR("ext".&&) $1::"ext".cube`,
		"l2DistanceLessThan": `((t.c) OPERATOR("ext".&&) "ext".cube_enlarge($1::"ext".cube, $2::float8, "ext".cube_dim($1::"ext".cube)) AND ` +
			`((t.c) OPERATOR("ext".<->) $1::"ext".cube) < $2::float8)`,
		"chebyshevDistanceLessThan": `((t.c) OPERATOR("ext".&&) "ext".cube_enlarge($1::"ext".cube, $2::float8, "ext".cube_dim($1::"ext".cube)) AND ` +
			`((t.c) OPERATOR("ext".<=>) $1::"ext".cube) < $2::float8)`,
	}
	for name, want := range ops {
		o, ok := v.Op(name)
		if !ok {
			t.Fatalf("op %s missing", name)
		}
		lo, _ := arity(o.Arg)
		if got := o.Render("t.c", placeholders(lo)...); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
		if !o.Indexable() {
			t.Errorf("%s: not indexable", name)
		}
	}
	exprs := map[string]string{
		"l2Distance":        `(t.c) OPERATOR("ext".<->) $1::"ext".cube`,
		"l1Distance":        `(t.c) OPERATOR("ext".<#>) $1::"ext".cube`,
		"chebyshevDistance": `(t.c) OPERATOR("ext".<=>) $1::"ext".cube`,
	}
	for name, want := range exprs {
		e, ok := v.Expr(name)
		if !ok {
			t.Fatalf("expr %s missing", name)
		}
		if got := e.Render("t.c", "$1"); got != want || e.OrderSQL("t.c", "$1") != want {
			t.Errorf("%s = %s / %s, want %s", name, got, e.OrderSQL("t.c", "$1"), want)
		}
		if !e.Score || !e.Ascending || e.Type != "float8" || len(e.Index) == 0 || !e.Index[0].Ordering {
			t.Errorf("%s: %+v", name, e)
		}
	}
	if got := v.Codec.SQL("$1"); got != `$1::"ext".cube` {
		t.Errorf("codec = %s", got)
	}
	if got := contrib.Cube().Ops[0].Render("c", "$1"); got != "(c) @> $1::cube" {
		t.Errorf("unbound = %s", got)
	}
	out := v.Codec.OutputJSONSQL("t.c")
	if !strings.Contains(out, "translate((t.c)::text, '()', '[]')") || strings.Count(out, "t.c") != 1 || !strings.Contains(out, "json_array_length") {
		t.Errorf("output = %s", out)
	}
	if strings.Contains(out, "jsonb") || !strings.Contains(v.Codec.OutputSQL("t.c"), "::jsonb") {
		t.Errorf("output flavours = %s / %s", out, v.Codec.OutputSQL("t.c"))
	}
}

func TestCubeParams(t *testing.T) {
	good := []struct {
		in   any
		want string
	}{
		{[]any{1.0, 2.5, -3.0}, "(1, 2.5, -3)"},
		{[]any{float64(1)}, "(1)"},
		{[]any{[]any{1.0, 2.0}, []any{3.0, 4.0}}, "(1, 2),(3, 4)"},
		{[]any{[]any{1.0}, []any{2.0}}, "(1),(2)"},
		{[]float64{1e300, 0.1}, "(1e+300, 0.1)"},
		{"(1,2),(3,4)", "(1,2),(3,4)"},
	}
	for _, tc := range good {
		got, err := contrib.CubeParam(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("CubeParam(%v) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	long := make([]any, contrib.MaxCubeDims+1)
	for i := range long {
		long[i] = 1.0
	}
	for _, in := range []any{
		nil, "", " ", 3.0, []any{}, []any{"x"}, []any{1.0, "2"}, long,
		[]any{[]any{1.0, 2.0}, []any{3.0}}, []any{[]any{}, []any{}}, []any{[]any{1.0}, 2.0},
		[]any{[]any{1.0}, []any{2.0}, []any{3.0}}, []float64{}, map[string]any{},
	} {
		if got, err := contrib.CubeParam(in); err == nil {
			t.Errorf("CubeParam(%v) = %q, want error", in, got)
		}
	}
	got, err := contrib.CubeDistanceParams(map[string]any{"cube": []any{1.0, 2.0}, "distance": 3})
	if err != nil || !reflect.DeepEqual(got, []any{"(1, 2)", 3.0}) {
		t.Errorf("CubeDistanceParams = %v, %v", got, err)
	}
	for _, in := range []any{
		nil, []any{1.0}, map[string]any{"distance": 1.0}, map[string]any{"cube": []any{1.0}},
		map[string]any{"cube": []any{"x"}, "distance": 1.0}, map[string]any{"cube": []any{1.0}, "distance": "far"},
	} {
		if _, err := contrib.CubeDistanceParams(in); err == nil {
			t.Errorf("CubeDistanceParams(%v) accepted", in)
		}
	}
}

func TestEarthRenders(t *testing.T) {
	v := contrib.Earth("cube").Bind("ed")
	ll := `"ed".ll_to_earth($1::float8, $2::float8)`
	o, ok := v.Op("earthDistanceLessThan")
	if !ok {
		t.Fatal("earthDistanceLessThan missing")
	}
	want := `("ed".earth_box(` + ll + `, $3::float8) OPERATOR("cube".@>) (t.c) AND "ed".earth_distance((t.c), ` + ll + `) < $3::float8)`
	if got := o.Render("t.c", "$1", "$2", "$3"); got != want {
		t.Errorf("filter = %s\nwant %s", got, want)
	}
	if !o.Indexable() || o.Arg != contrib.ArgLatLonDistance {
		t.Errorf("filter: %+v", o)
	}
	e, ok := v.Expr("earthDistance")
	if !ok {
		t.Fatal("earthDistance missing")
	}
	if got := e.Render("t.c", "$1", "$2"); got != `"ed".earth_distance((t.c), `+ll+`)` {
		t.Errorf("score = %s", got)
	}
	if got := e.OrderSQL("t.c", "$1", "$2"); got != `(t.c) OPERATOR("cube".<->) `+ll {
		t.Errorf("order = %s", got)
	}
	if e.OrderSQL("t.c", "$1") != "" || e.Render("t.c", "$1") != "" {
		t.Error("wrong placeholder count accepted")
	}
	if !e.Score || !e.Ascending || len(e.Index) == 0 || !e.Index[0].Ordering {
		t.Errorf("expr: %+v", e)
	}
	if got := v.Codec.SQL("$1", "$2"); got != ll {
		t.Errorf("codec = %s", got)
	}
	out := v.Codec.OutputSQL("t.c")
	if strings.Count(out, "t.c") != 1 || !strings.Contains(out, `jsonb_build_object('lat', "ed".latitude(__earth.e), 'lon', "ed".longitude(__earth.e))`) {
		t.Errorf("output = %s", out)
	}
	if !strings.Contains(v.Codec.OutputJSONSQL("t.c"), "json_build_object(") {
		t.Errorf("json output = %s", v.Codec.OutputJSONSQL("t.c"))
	}
	if a, ok := v.Accessor("latitude"); !ok || a.SQL("t.c") != `"ed".latitude(t.c)` {
		t.Errorf("latitude = %v", a.SQL("t.c"))
	}
	unbound := contrib.Earth("")
	if got := unbound.Exprs[0].OrderSQL("c", "$1", "$2"); got != "(c) <-> ll_to_earth($1::float8, $2::float8)" {
		t.Errorf("unbound order = %s", got)
	}
}

func TestEarthPointRenders(t *testing.T) {
	v := contrib.EarthPoint().Bind("ed")
	e, _ := v.Expr("earthDistanceMiles")
	want := `(t.c) OPERATOR("ed".<@>) point($2::float8, $1::float8)`
	if got := e.Render("t.c", "$1", "$2"); got != want || e.OrderSQL("t.c", "$1", "$2") != want {
		t.Errorf("miles = %s", got)
	}
	if len(e.Index) != 0 || !e.Score {
		t.Errorf("expr: %+v", e)
	}
	o, _ := v.Op("earthDistanceMilesLessThan")
	if got := o.Render("t.c", "$1", "$2", "$3"); got != "("+want+") < $3::float8" {
		t.Errorf("filter = %s", got)
	}
	if o.Indexable() || v.Codec != nil {
		t.Error("point vocabulary claims an index or a codec")
	}
}

func TestLatLonParams(t *testing.T) {
	got, err := contrib.LatLonParams(map[string]any{"lat": 51.5, "lon": "-0.12"})
	if err != nil || !reflect.DeepEqual(got, []any{51.5, -0.12}) {
		t.Errorf("LatLonParams = %v, %v", got, err)
	}
	got, err = contrib.LatLonDistanceParams(map[string]any{"lat": -90, "lon": 180, "distance": 1000})
	if err != nil || !reflect.DeepEqual(got, []any{-90.0, 180.0, 1000.0}) {
		t.Errorf("LatLonDistanceParams = %v, %v", got, err)
	}
	for _, in := range []any{
		nil, []any{1.0, 2.0}, map[string]any{"lat": 1.0}, map[string]any{"lon": 1.0},
		map[string]any{"lat": 90.5, "lon": 0}, map[string]any{"lat": 0, "lon": -180.1},
		map[string]any{"lat": "north", "lon": 0}, map[string]any{"lat": nil, "lon": 0},
	} {
		if _, err := contrib.LatLonParams(in); err == nil {
			t.Errorf("LatLonParams(%v) accepted", in)
		}
	}
	for _, in := range []any{
		map[string]any{"lat": 1.0, "lon": 2.0}, map[string]any{"lat": 1.0, "lon": 2.0, "distance": "x"},
		map[string]any{"lat": 100.0, "lon": 2.0, "distance": 1},
	} {
		if _, err := contrib.LatLonDistanceParams(in); err == nil {
			t.Errorf("LatLonDistanceParams(%v) accepted", in)
		}
	}
}

func TestCubeEarthForColumn(t *testing.T) {
	cat := &introspect.Catalog{
		Extensions: []*introspect.Extension{
			{Name: "cube", Schema: "c"}, {Name: "earthdistance", Schema: "ed", Version: "1.2"},
		},
		Domains: []*introspect.Domain{
			{Schema: "app", Name: "location", BaseType: "cube", BaseTypeSchema: "c", Parent: "ed.earth"},
		},
	}
	names := func(vs []contrib.Vocabulary) string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Name+"@"+v.Schema)
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		cat  *introspect.Catalog
		col  *introspect.Column
		want string
	}{
		{cat, &introspect.Column{PGType: "cube", BaseTypeSchema: "c"}, "cube@c"},
		{cat, &introspect.Column{PGType: "cube", BaseTypeSchema: "public"}, ""},
		{cat, &introspect.Column{PGType: "cube"}, "cube@c"},
		{cat, &introspect.Column{PGType: "_cube", IsArray: true, BaseTypeSchema: "c"}, ""},
		{cat, &introspect.Column{PGType: "cube", BaseTypeSchema: "c", Domain: "ed.earth"}, "earth@ed"},
		{cat, &introspect.Column{PGType: "cube", BaseTypeSchema: "c", Domain: "app.location"}, "earth@ed"},
		{cat, &introspect.Column{PGType: "cube", BaseTypeSchema: "c", Domain: "public.earth"}, "cube@c"},
		{cat, &introspect.Column{PGType: "_cube", IsArray: true, BaseTypeSchema: "c", Domain: "ed.earth"}, ""},
		{cat, &introspect.Column{PGType: "point", BaseTypeSchema: "pg_catalog"}, "earthpoint@ed"},
		{cat, &introspect.Column{PGType: "_point", IsArray: true, BaseTypeSchema: "pg_catalog"}, ""},
		{cat, &introspect.Column{PGType: "point", BaseTypeSchema: "app"}, ""},
		{&introspect.Catalog{}, &introspect.Column{PGType: "point", BaseTypeSchema: "pg_catalog"}, ""},
		{&introspect.Catalog{}, &introspect.Column{PGType: "cube"}, ""},
		{nil, &introspect.Column{PGType: "cube", Domain: "ed.earth"}, ""},
		{&introspect.Catalog{Extensions: cat.Extensions[1:]}, &introspect.Column{PGType: "cube", Domain: "ed.earth"}, ""},
	}
	for _, tc := range tests {
		if got := names(contrib.ForColumn(tc.cat, tc.col)); got != tc.want {
			t.Errorf("ForColumn(%+v) = %q, want %q", tc.col, got, tc.want)
		}
	}
	vs := contrib.ForColumn(cat, &introspect.Column{PGType: "cube", Domain: "ed.earth"})
	if o, _ := vs[0].Op("earthDistanceLessThan"); !strings.Contains(o.Render("x", "$1", "$2", "$3"), `OPERATOR("c".@>)`) {
		t.Errorf("earth does not use cube's schema: %s", o.Render("x", "$1", "$2", "$3"))
	}
	if got := names(contrib.ForType(cat, "c.cube", false)); got != "cube@c" {
		t.Errorf("ForType(c.cube) = %q", got)
	}
}
