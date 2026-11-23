package contrib_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
)

func TestUnaccentRenders(t *testing.T) {
	bound := contrib.Unaccent("text").Bind("ext")
	ua := func(x string) string { return `"ext".unaccent('"ext"."unaccent"'::regdictionary, ` + x + ")" }
	tests := map[string]string{
		"unaccentEqualTo":    "lower(" + ua("t.c") + ") = lower(" + ua("$1::text") + ")",
		"unaccentLike":       ua("t.c") + " ILIKE " + ua("$1::text"),
		"unaccentIncludes":   ua("t.c") + " ILIKE " + ua("$1::text"),
		"unaccentStartsWith": ua("t.c") + " ILIKE " + ua("$1::text"),
	}
	for name, want := range tests {
		op, ok := bound.Op(name)
		if !ok {
			t.Fatalf("op %q not found", name)
		}
		if got := op.Render("t.c", "$1"); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
		if op.Indexable() {
			t.Errorf("%s claims an index", name)
		}
	}
	op, _ := contrib.Unaccent("text").Op("unaccentLike")
	if got, want := op.Render("t.c", "$1"), "unaccent(t.c) ILIKE unaccent($1::text)"; got != want {
		t.Errorf("unbound = %s, want %s", got, want)
	}
	if got, want := contrib.UnaccentSQL(`it's`, "x"), `"it's".unaccent('"it''s"."unaccent"'::regdictionary, x)`; got != want {
		t.Errorf("UnaccentSQL = %s, want %s", got, want)
	}
}

func TestUnaccentParams(t *testing.T) {
	v := contrib.Unaccent("text")
	tests := []struct {
		op   string
		in   any
		want string
	}{
		{"unaccentEqualTo", `Café_%`, `Café_%`},
		{"unaccentLike", `caf%`, `caf%`},
		{"unaccentIncludes", `50%_off\`, `%50\%\_off\\%`},
		{"unaccentStartsWith", `Zoë`, `Zoë%`},
	}
	for _, tc := range tests {
		op, _ := v.Op(tc.op)
		got, err := op.Params(tc.in)
		if err != nil || len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s(%q) = %v, %v; want %q", tc.op, tc.in, got, err, tc.want)
		}
	}
	op, _ := v.Op("unaccentIncludes")
	if _, err := op.Params(3); err == nil {
		t.Error("non-string operand accepted")
	}
}

func TestVectorRenders(t *testing.T) {
	m := must(t)
	tests := []struct {
		typ, expr, want string
	}{
		{"vector", "l2Distance", `(t.c) OPERATOR("ext".<->) $1::"ext".vector`},
		{"vector", "negativeInnerProduct", `(t.c) OPERATOR("ext".<#>) $1::"ext".vector`},
		{"vector", "cosineDistance", `(t.c) OPERATOR("ext".<=>) $1::"ext".vector`},
		{"vector", "l1Distance", `(t.c) OPERATOR("ext".<+>) $1::"ext".vector`},
		{"halfvec", "cosineDistance", `(t.c) OPERATOR("ext".<=>) $1::"ext".halfvec`},
		{"sparsevec", "l2Distance", `(t.c) OPERATOR("ext".<->) $1::"ext".sparsevec`},
	}
	for _, tc := range tests {
		v := m(contrib.Vector(tc.typ)).Bind("ext")
		e, ok := v.Expr(tc.expr)
		if !ok {
			t.Fatalf("%s: expr %q not found", tc.typ, tc.expr)
		}
		if got := e.Render("t.c", "$1"); got != tc.want {
			t.Errorf("%s %s = %s, want %s", tc.typ, tc.expr, got, tc.want)
		}
		if !e.Ascending || e.Type != "float8" || len(e.Index) == 0 || !e.Index[0].Ordering {
			t.Errorf("%s %s: %+v", tc.typ, tc.expr, e)
		}
	}
	e, _ := m(contrib.Vector("vector")).Expr("l2Distance")
	if got := e.Render("t.c", "$1"); got != "(t.c) <-> $1::vector" {
		t.Errorf("unbound = %s", got)
	}
	if _, ok := contrib.Vector("vector[]"); ok {
		t.Error("vector[] has a vocabulary")
	}
	if _, ok := contrib.Vector("bit"); ok {
		t.Error("bit has a vocabulary")
	}
}

func TestVectorIndexHints(t *testing.T) {
	m := must(t)
	hints := func(typ, expr string) []string {
		e, _ := m(contrib.Vector(typ)).Expr(expr)
		var out []string
		for _, h := range e.Index {
			out = append(out, h.Method+":"+h.OpClass)
		}
		return out
	}
	tests := []struct {
		typ, expr string
		want      []string
	}{
		{"vector", "l2Distance", []string{"hnsw:vector_l2_ops", "ivfflat:vector_l2_ops"}},
		{"vector", "negativeInnerProduct", []string{"hnsw:vector_ip_ops", "ivfflat:vector_ip_ops"}},
		{"halfvec", "cosineDistance", []string{"hnsw:halfvec_cosine_ops", "ivfflat:halfvec_cosine_ops"}},
		{"vector", "l1Distance", []string{"hnsw:vector_l1_ops"}},
		{"sparsevec", "cosineDistance", []string{"hnsw:sparsevec_cosine_ops"}},
	}
	for _, tc := range tests {
		if got := hints(tc.typ, tc.expr); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s = %v, want %v", tc.typ, tc.expr, got, tc.want)
		}
	}
}

func TestVectorAccessorsAndCodec(t *testing.T) {
	m := must(t)
	vec := m(contrib.Vector("vector")).Bind("ext")
	half := m(contrib.Vector("halfvec")).Bind("ext")
	sparse := m(contrib.Vector("sparsevec")).Bind("ext")
	acc := func(v contrib.Vocabulary, name string) string {
		a, ok := v.Accessor(name)
		if !ok {
			return ""
		}
		return a.SQL("t.c") + " " + a.Type
	}
	for _, tc := range []struct{ got, want string }{
		{acc(vec, "dims"), `"ext".vector_dims(t.c) int4`},
		{acc(vec, "norm"), `"ext".vector_norm(t.c) float8`},
		{acc(half, "dims"), `"ext".vector_dims(t.c) int4`},
		{acc(half, "norm"), `"ext".l2_norm(t.c) float8`},
		{acc(sparse, "dims"), ""},
		{acc(sparse, "norm"), `"ext".l2_norm(t.c) float8`},
		{vec.Codec.SQL("$1"), `$1::"ext".vector`},
		{sparse.Codec.SQL("$1"), `$1::"ext".sparsevec`},
		{vec.Codec.OutputSQL("t.c"), "(t.c)::text::jsonb"},
		{half.Codec.OutputJSONSQL("t.c"), "(t.c)::text::json"},
		{sparse.Codec.OutputSQL("t.c"), "t.c"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

func TestVectorParams(t *testing.T) {
	m := must(t)
	vec, sparse := m(contrib.Vector("vector")), m(contrib.Vector("sparsevec"))
	ok := []struct {
		v    contrib.Vocabulary
		in   any
		want string
	}{
		{vec, []any{1.0, -2.5, json.Number("3"), int64(4)}, "[1,-2.5,3,4]"},
		{vec, []any{1e-7, 0.1}, "[1e-07,0.1]"},
		{vec, []float32{0.5}, "[0.5]"},
		{vec, "[1,2,3]", "[1,2,3]"},
		{sparse, []any{0.0, 1.5, 0.0, 2.0, 0.0}, "{2:1.5,4:2}/5"},
		{sparse, []any{0.0, 0.0}, "{}/2"},
		{sparse, "{1:1}/3", "{1:1}/3"},
	}
	for _, tc := range ok {
		got, err := tc.v.Codec.Params(tc.in)
		if err != nil || len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s %v = %v, %v; want %s", tc.v.Type, tc.in, got, err, tc.want)
		}
		e := tc.v.Exprs[0]
		if got2, err := e.Params(tc.in); err != nil || !reflect.DeepEqual(got2, got) {
			t.Errorf("%s expr params %v = %v, %v", tc.v.Type, tc.in, got2, err)
		}
	}
	bad := []any{
		[]any{}, []any{"1"}, []any{1.0, nil}, "", "  ", 3.0, map[string]any{},
		[]float64{math.NaN()}, []float64{math.Inf(1)}, make([]any, contrib.MaxVectorDims+1),
	}
	for _, in := range bad {
		if _, err := vec.Codec.Params(in); err == nil {
			t.Errorf("accepted %T %.40v", in, in)
		}
	}
	big := make([]float64, contrib.MaxVectorDims+1)
	if _, err := contrib.VectorParam(big); err == nil || !strings.Contains(err.Error(), "16000") {
		t.Errorf("oversized vector: %v", err)
	}
}

func TestForTypeVectorAndUnaccent(t *testing.T) {
	names := func(vs []contrib.Vocabulary) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Name+"@"+v.Schema)
		}
		return out
	}
	none := &introspect.Catalog{}
	if got := contrib.ForType(none, "vector", false); got != nil {
		t.Errorf("vector without the extension = %v", names(got))
	}
	cat := &introspect.Catalog{Extensions: []*introspect.Extension{
		{Name: "vector", Schema: "ext", Version: "0.6.2"}, {Name: "unaccent", Schema: "ext", Version: "1.1"},
	}}
	got := contrib.ForType(cat, `"ext".vector`, false)
	if want := []string{"vector@ext"}; !reflect.DeepEqual(names(got), want) {
		t.Fatalf("vector = %v, want %v", names(got), want)
	}
	if _, ok := got[0].Expr("l1Distance"); ok {
		t.Error("pgvector 0.6 has no L1 distance")
	}
	if _, ok := got[0].Expr("cosineDistance"); !ok {
		t.Error("cosineDistance missing")
	}
	if got := contrib.ForType(cat, "other.vector", false); got != nil {
		t.Errorf("a vector type outside the extension schema = %v", names(got))
	}
	if got := contrib.ForType(cat, "_vector", false); got != nil {
		t.Errorf("vector[] = %v", names(got))
	}
	if got, want := names(contrib.ForType(cat, "text", false)), []string{"textsearch@", "unaccent@ext"}; !reflect.DeepEqual(got, want) {
		t.Errorf("text = %v, want %v", got, want)
	}
	col := &introspect.Column{Name: "e", PGType: "halfvec", TypeSchema: "ext", BaseTypeSchema: "ext"}
	if got := contrib.ForColumn(cat, col); len(got) != 1 || got[0].Type != "halfvec" {
		t.Errorf("halfvec column = %v", names(got))
	}
}
