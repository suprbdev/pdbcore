package contrib_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
)

func vocab(t *testing.T, v contrib.Vocabulary, ok bool) contrib.Vocabulary {
	t.Helper()
	if !ok {
		t.Fatal("vocabulary not found")
	}
	return v
}

func must(t *testing.T) func(contrib.Vocabulary, bool) contrib.Vocabulary {
	return func(v contrib.Vocabulary, ok bool) contrib.Vocabulary { return vocab(t, v, ok) }
}

// placeholders returns $1..$n.
func placeholders(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "$" + string(rune('1'+i))
	}
	return out
}

func TestOpRenders(t *testing.T) {
	m := must(t)
	tests := []struct {
		v    contrib.Vocabulary
		op   string
		n    int
		want string
	}{
		{contrib.FullText(), "matches", 1, "(t.c) @@ to_tsquery($1::text)"},
		{contrib.FullText(), "matches", 2, "(t.c) @@ to_tsquery($2::regconfig, $1::text)"},
		{contrib.FullText(), "matchesPlain", 2, "(t.c) @@ plainto_tsquery($2::regconfig, $1::text)"},
		{contrib.FullText(), "matchesPhrase", 1, "(t.c) @@ phraseto_tsquery($1::text)"},
		{contrib.FullText(), "matchesWebsearch", 2, "(t.c) @@ websearch_to_tsquery($2::regconfig, $1::text)"},
		{contrib.TextSearch("text"), "matches", 1, "to_tsvector(t.c) @@ to_tsquery($1::text)"},
		{contrib.TextSearch("text"), "matchesWebsearch", 2, "to_tsvector($2::regconfig, t.c) @@ websearch_to_tsquery($2::regconfig, $1::text)"},
		{contrib.Trgm("text"), "similarTo", 1, "(t.c) % $1::text"},
		{contrib.Trgm("text").Bind("ext"), "similarTo", 1, `(t.c) OPERATOR("ext".%) $1::text`},
		{contrib.Trgm("text").Bind("ext"), "wordSimilarTo", 1, `(t.c) OPERATOR("ext".%>) $1::text`},
		{contrib.Trgm("text").Bind("ext"), "strictWordSimilarTo", 1, `(t.c) OPERATOR("ext".%>>) $1::text`},
		{contrib.Hstore().Bind("ext"), "hasKey", 1, `(t.c) OPERATOR("ext".?) $1::text`},
		{contrib.Hstore().Bind("ext"), "hasAllKeys", 1, `(t.c) OPERATOR("ext".?&) $1::text[]`},
		{contrib.Hstore().Bind("ext"), "hasAnyKeys", 1, `(t.c) OPERATOR("ext".?|) $1::text[]`},
		{contrib.Hstore().Bind("ext"), "contains", 1, `(t.c) OPERATOR("ext".@>) "ext".hstore($1::text[])`},
		{contrib.Hstore().Bind("ext"), "containedBy", 1, `(t.c) OPERATOR("ext".<@) "ext".hstore($1::text[])`},
		{contrib.Hstore(), "contains", 1, `(t.c) @> hstore($1::text[])`},
		{contrib.Ltree().Bind("ext"), "ancestorOf", 1, `(t.c) OPERATOR("ext".@>) $1::"ext".ltree`},
		{contrib.Ltree().Bind("ext"), "descendantOf", 1, `(t.c) OPERATOR("ext".<@) $1::"ext".ltree`},
		{contrib.Ltree().Bind("ext"), "ancestorOfAny", 1, `(t.c) OPERATOR("ext".@>) $1::"ext".ltree[]`},
		{contrib.Ltree().Bind("ext"), "descendantOfAny", 1, `(t.c) OPERATOR("ext".<@) $1::"ext".ltree[]`},
		{contrib.Ltree().Bind("ext"), "matchesLquery", 1, `(t.c) OPERATOR("ext".~) $1::"ext".lquery`},
		{contrib.Ltree().Bind("ext"), "matchesAnyLquery", 1, `(t.c) OPERATOR("ext".?) $1::"ext".lquery[]`},
		{contrib.Ltree().Bind("ext"), "matchesLtxtquery", 1, `(t.c) OPERATOR("ext".@) $1::"ext".ltxtquery`},
		{contrib.Ltree(), "matchesLquery", 1, `(t.c) ~ $1::lquery`},
		{contrib.LtreeArray().Bind("ext"), "ancestorOf", 1, `(t.c) OPERATOR("ext".@>) $1::"ext".ltree`},
		{contrib.LtreeArray().Bind("ext"), "descendantOf", 1, `(t.c) OPERATOR("ext".<@) $1::"ext".ltree`},
		{contrib.LtreeArray().Bind("ext"), "matchesLquery", 1, `(t.c) OPERATOR("ext".~) $1::"ext".lquery`},
		{contrib.LtreeArray().Bind("ext"), "matchesAnyLquery", 1, `(t.c) OPERATOR("ext".?) $1::"ext".lquery[]`},
		{contrib.LtreeArray().Bind("ext"), "matchesLtxtquery", 1, `(t.c) OPERATOR("ext".@) $1::"ext".ltxtquery`},
		{m(contrib.Range("int4range")), "overlaps", 3, "(t.c) && int4range($1::int4, $2::int4, $3::text)"},
		{m(contrib.Range("tstzrange")), "contains", 3, "(t.c) @> tstzrange($1::timestamptz, $2::timestamptz, $3::text)"},
		{m(contrib.Range("daterange")), "containsElement", 1, "(t.c) @> $1::date"},
		{m(contrib.Range("numrange")), "containedBy", 3, "(t.c) <@ numrange($1::numeric, $2::numeric, $3::text)"},
		{m(contrib.Range("int8range")), "strictlyLeftOf", 3, "(t.c) << int8range($1::int8, $2::int8, $3::text)"},
		{m(contrib.Range("tsrange")), "strictlyRightOf", 3, "(t.c) >> tsrange($1::timestamp, $2::timestamp, $3::text)"},
		{m(contrib.Range("int4range")), "notExtendsRightOf", 3, "(t.c) &< int4range($1::int4, $2::int4, $3::text)"},
		{m(contrib.Range("int4range")), "notExtendsLeftOf", 3, "(t.c) &> int4range($1::int4, $2::int4, $3::text)"},
		{m(contrib.Range("int4range")), "adjacentTo", 3, "(t.c) -|- int4range($1::int4, $2::int4, $3::text)"},
		{m(contrib.Multirange("int4multirange")), "overlaps", 3, "(t.c) && int4range($1::int4, $2::int4, $3::text)"},
		{m(contrib.Multirange("datemultirange")), "containsElement", 1, "(t.c) @> $1::date"},
		{m(contrib.Inet("inet")), "containedBy", 1, "(t.c) << $1::inet"},
		{m(contrib.Inet("cidr")), "containedByOrEquals", 1, "(t.c) <<= $1::inet"},
		{m(contrib.Inet("inet")), "contains", 1, "(t.c) >> $1::inet"},
		{m(contrib.Inet("inet")), "containsOrEquals", 1, "(t.c) >>= $1::inet"},
		{m(contrib.Inet("inet")), "containsOrContainedBy", 1, "(t.c) && $1::inet"},
	}
	for _, tc := range tests {
		op, ok := tc.v.Op(tc.op)
		if !ok {
			t.Errorf("%s: op %q not found", tc.v.Name, tc.op)
			continue
		}
		if got := op.Render("t.c", placeholders(tc.n)...); got != tc.want {
			t.Errorf("%s %s = %s, want %s", tc.v.Name, tc.op, got, tc.want)
		}
	}
}

// allVocabularies lists every vocabulary, bound to a schema that needs
// quoting.
func allVocabularies(t *testing.T) []contrib.Vocabulary {
	m := must(t)
	vs := []contrib.Vocabulary{
		contrib.FullText(), contrib.TextSearch("text"), contrib.Trgm("citext"),
		contrib.Hstore(), contrib.Ltree(), contrib.LtreeArray(), contrib.Citext(),
		m(contrib.Inet("inet")), m(contrib.Inet("cidr")),
	}
	for _, r := range contrib.RangeTypes() {
		vs = append(vs, m(contrib.Range(r)))
		mr := strings.Replace(r, "range", "multirange", 1)
		vs = append(vs, m(contrib.Multirange(mr)))
	}
	vs = append(vs, contrib.RangeOf(customRange), m(contrib.MultirangeOf(customRange)))
	for i := range vs {
		vs[i] = vs[i].Bind(`my "ext"`)
	}
	return vs
}

// TestEveryRenderIsParameterized checks the shape invariants of every
// operator, expression, accessor and codec: each renders, uses every
// placeholder it is given, qualifies extension objects, and rejects a wrong
// placeholder count.
func TestEveryRenderIsParameterized(t *testing.T) {
	for _, v := range allVocabularies(t) {
		qualified := v.Extension == "" || v.Schema == `my "ext"`
		if !qualified {
			t.Errorf("%s: not bound", v.Name)
		}
		check := func(what, sql string, n int) {
			t.Helper()
			if sql == "" {
				t.Errorf("%s %s: empty render", v.Name, what)
				return
			}
			for _, p := range placeholders(n) {
				if !strings.Contains(sql, p) {
					t.Errorf("%s %s: %s does not use %s", v.Name, what, sql, p)
				}
			}
			if v.Extension != "" && strings.Contains(sql, "OPERATOR(") && !strings.Contains(sql, `OPERATOR("my ""ext""".`) {
				t.Errorf("%s %s: unqualified operator in %s", v.Name, what, sql)
			}
		}
		for _, op := range v.Ops {
			lo, hi := arity(op.Arg)
			for n := lo; n <= hi; n++ {
				check(op.Name, op.Render("t.c", placeholders(n)...), n)
			}
			if op.Render("t.c", placeholders(hi+1)...) != "" || op.Render("t.c") != "" {
				t.Errorf("%s %s: accepts a wrong placeholder count", v.Name, op.Name)
			}
			if v.Extension != "" && op.Extension != v.Extension {
				t.Errorf("%s %s: Extension = %q", v.Name, op.Name, op.Extension)
			}
			if op.Description == "" {
				t.Errorf("%s %s: no description", v.Name, op.Name)
			}
		}
		for _, e := range v.Exprs {
			check(e.Name, e.Render("t.c", "$1"), 1)
			if e.Render("t.c") != "" {
				t.Errorf("%s %s: accepts no placeholders", v.Name, e.Name)
			}
		}
		for _, a := range v.Accessors {
			if !strings.Contains(a.SQL("t.c"), "t.c") || a.Type == "" {
				t.Errorf("%s %s: %s", v.Name, a.Name, a.SQL("t.c"))
			}
		}
		if v.Codec != nil {
			lo, _ := arity(v.Codec.Arg)
			check("codec", v.Codec.SQL(placeholders(lo)...), lo)
			if !strings.Contains(v.Codec.OutputSQL("t.c"), "t.c") {
				t.Errorf("%s: output %s", v.Name, v.Codec.OutputSQL("t.c"))
			}
		}
	}
}

func arity(k contrib.ArgKind) (int, int) {
	switch k {
	case contrib.ArgFullText:
		return 1, 2
	case contrib.ArgRange, contrib.ArgMultirange:
		return 3, 3
	}
	return 1, 1
}

func TestUniqueOpNamesPerColumn(t *testing.T) {
	cat := &introspect.Catalog{Extensions: []*introspect.Extension{
		{Name: "pg_trgm", Schema: "ext"}, {Name: "citext", Schema: "ext"},
	}}
	seen := map[string]bool{}
	for _, v := range contrib.ForType(cat, "citext", false) {
		for _, n := range v.OpNames() {
			if seen[n] {
				t.Errorf("duplicate op %q", n)
			}
			seen[n] = true
		}
	}
	if !seen["matchesWebsearch"] || !seen["similarTo"] {
		t.Errorf("ops = %v", seen)
	}
	op, ok := contrib.FindOp(contrib.ForType(cat, "text", false), "wordSimilarTo")
	if !ok || op.Render("c", "$1") != `(c) OPERATOR("ext".%>) $1::text` {
		t.Errorf("FindOp = %v %q", ok, op.Render("c", "$1"))
	}
}

func TestExprRenders(t *testing.T) {
	v := contrib.Trgm("text").Bind("ext")
	tests := map[string]string{
		"similarity":           `"ext".similarity(t.c, $1::text)`,
		"wordSimilarity":       `"ext".word_similarity($1::text, t.c)`,
		"strictWordSimilarity": `"ext".strict_word_similarity($1::text, t.c)`,
		"distance":             `(t.c) OPERATOR("ext".<->) $1::text`,
		"wordDistance":         `(t.c) OPERATOR("ext".<->>) $1::text`,
		"strictWordDistance":   `(t.c) OPERATOR("ext".<->>>) $1::text`,
	}
	for name, want := range tests {
		e, ok := v.Expr(name)
		if !ok {
			t.Fatalf("expr %q not found", name)
		}
		if got := e.Render("t.c", "$1"); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
		if e.Ascending != strings.Contains(name, "istance") {
			t.Errorf("%s: Ascending = %v", name, e.Ascending)
		}
	}
	if got := contrib.Trgm("text").Exprs[0].Render("c", "$1"); got != "similarity(c, $1::text)" {
		t.Errorf("unbound similarity = %s", got)
	}
}

func TestRankSQL(t *testing.T) {
	op, _ := contrib.FullText().Op("matchesWebsearch")
	if got, want := op.RankSQL(contrib.RankFrequency, "t.v", "$1", "$2"), "ts_rank(t.v, websearch_to_tsquery($2::regconfig, $1::text))"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	op, _ = contrib.TextSearch("text").Op("matchesPlain")
	if got, want := op.RankSQL(contrib.RankCoverDensity, "t.c", "$1"), "ts_rank_cd(to_tsvector(t.c), plainto_tsquery($1::text))"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	op, _ = contrib.Hstore().Op("hasKey")
	if op.RankSQL(contrib.RankFrequency, "t.c", "$1") != "" {
		t.Error("RankSQL on a non-full-text op")
	}
}

func TestAccessors(t *testing.T) {
	m := must(t)
	tests := []struct {
		v          contrib.Vocabulary
		name, want string
		typ        string
	}{
		{contrib.Ltree().Bind("ext"), "nlevel", `"ext".nlevel(t.c)`, "int4"},
		{m(contrib.Inet("inet")), "family", "family(t.c)", "int4"},
		{m(contrib.Inet("inet")), "masklen", "masklen(t.c)", "int4"},
		{m(contrib.Inet("cidr")), "host", "host(t.c)", "text"},
		{m(contrib.Inet("cidr")), "network", "network(t.c)::text", "text"},
		{m(contrib.Range("int8range")), "start", "lower(t.c)", "int8"},
		{m(contrib.Range("daterange")), "end", "upper(t.c)", "date"},
		{m(contrib.Range("numrange")), "startInclusive", "lower_inc(t.c)", "bool"},
		{m(contrib.Range("numrange")), "endInclusive", "upper_inc(t.c)", "bool"},
		{m(contrib.Range("numrange")), "startInfinite", "lower_inf(t.c)", "bool"},
		{m(contrib.Range("numrange")), "endInfinite", "upper_inf(t.c)", "bool"},
		{m(contrib.Multirange("tstzmultirange")), "isEmpty", "isempty(t.c)", "bool"},
	}
	for _, tc := range tests {
		a, ok := tc.v.Accessor(tc.name)
		if !ok {
			t.Fatalf("%s: accessor %q not found", tc.v.Name, tc.name)
		}
		if got := a.SQL("t.c"); got != tc.want || a.Type != tc.typ {
			t.Errorf("%s %s = %s (%s), want %s (%s)", tc.v.Name, tc.name, got, a.Type, tc.want, tc.typ)
		}
	}
}

func TestCodecs(t *testing.T) {
	m := must(t)
	tests := []struct {
		v        contrib.Vocabulary
		n        int
		sql, out string
	}{
		{contrib.FullText(), 1, "$1::tsvector", "t.c"},
		{contrib.Hstore().Bind("ext"), 1, `"ext".hstore($1::text[])`, `"ext".hstore_to_jsonb(t.c)`},
		{contrib.Ltree().Bind("ext"), 1, `$1::"ext".ltree`, "t.c"},
		{contrib.LtreeArray().Bind("ext"), 1, `$1::"ext".ltree[]`, "t.c"},
		{contrib.Citext().Bind("ext"), 1, `$1::"ext".citext`, "t.c"},
		{m(contrib.Inet("cidr")), 1, "$1::cidr", "t.c"},
		{m(contrib.Range("int4range")), 3, "int4range($1::int4, $2::int4, $3::text)", contrib.RangeOutputSQL("t.c")},
		{
			m(contrib.Multirange("int4multirange")), 3,
			"int4multirange(VARIADIC ARRAY(SELECT int4range(__l, __u, __b) FROM unnest($1::int4[], $2::int4[], $3::text[]) AS __r(__l, __u, __b)))",
			contrib.MultirangeOutputSQL("t.c"),
		},
	}
	for _, tc := range tests {
		if got := tc.v.Codec.SQL(placeholders(tc.n)...); got != tc.sql {
			t.Errorf("%s codec SQL = %s, want %s", tc.v.Name, got, tc.sql)
		}
		if got := tc.v.Codec.OutputSQL("t.c"); got != tc.out {
			t.Errorf("%s codec output = %s, want %s", tc.v.Name, got, tc.out)
		}
	}
	jsonTests := []struct {
		v    contrib.Vocabulary
		want string
	}{
		{contrib.Hstore().Bind("ext"), `"ext".hstore_to_json(t.c)`},
		{contrib.Citext().Bind("ext"), "t.c"},
		{m(contrib.Range("int4range")), contrib.RangeOutputJSONSQL("t.c")},
		{m(contrib.Multirange("int4multirange")), contrib.MultirangeOutputJSONSQL("t.c")},
	}
	for _, tc := range jsonTests {
		if got := tc.v.Codec.OutputJSONSQL("t.c"); got != tc.want {
			t.Errorf("%s codec JSON output = %s, want %s", tc.v.Name, got, tc.want)
		}
	}
	for _, got := range []string{contrib.RangeOutputJSONSQL("t.c"), contrib.MultirangeOutputJSONSQL("t.c")} {
		if strings.Contains(got, "jsonb") {
			t.Errorf("JSON output mentions jsonb: %s", got)
		}
	}
	if contrib.Trgm("text").Codec != nil || contrib.TextSearch("text").Codec != nil {
		t.Error("text vocabularies must not own a codec")
	}
	want := "(SELECT jsonb_build_object('start', lower(__r), 'end', upper(__r), 'startInclusive', lower_inc(__r), 'endInclusive', upper_inc(__r), 'isEmpty', isempty(__r)) FROM (SELECT t.c AS __r) AS __v WHERE __r IS NOT NULL)"
	if got := contrib.RangeOutputSQL("t.c"); got != want {
		t.Errorf("RangeOutputSQL = %s", got)
	}
}

func strp(s string) *string { return &s }

func TestParams(t *testing.T) {
	m := must(t)
	hs := contrib.Hstore()
	int4 := m(contrib.Range("int4range"))
	num := m(contrib.Range("numrange"))
	tests := []struct {
		name    string
		params  func() ([]any, error)
		want    []any
		wantErr string
	}{
		{"text", func() ([]any, error) { return contrib.Trgm("text").Ops[0].Params("foo") }, []any{"foo"}, ""},
		{"text rejects numbers", func() ([]any, error) { return contrib.Trgm("text").Ops[0].Params(1.0) }, nil, "must be a string"},
		{"fts string", func() ([]any, error) { return contrib.FullText().Ops[0].Params("a & b") }, []any{"a & b"}, ""},
		{"fts object", func() ([]any, error) {
			return contrib.FullText().Ops[0].Params(map[string]any{"query": "cats", "config": "english"})
		}, []any{"cats", "english"}, ""},
		{"fts null config", func() ([]any, error) {
			return contrib.FullText().Ops[0].Params(map[string]any{"query": "cats", "config": nil})
		}, []any{"cats"}, ""},
		{"fts missing query", func() ([]any, error) {
			return contrib.FullText().Ops[0].Params(map[string]any{"config": "english"})
		}, nil, "requires a query"},
		{"fts unknown field", func() ([]any, error) {
			return contrib.FullText().Ops[0].Params(map[string]any{"query": "x", "lang": "english"})
		}, nil, "unknown full-text field"},
		{"key list", func() ([]any, error) {
			op, _ := hs.Op("hasAllKeys")
			return op.Params([]any{"a", "b"})
		}, []any{[]string{"a", "b"}}, ""},
		{"key list rejects null", func() ([]any, error) {
			op, _ := hs.Op("hasAnyKeys")
			return op.Params([]any{"a", nil})
		}, nil, "list of strings"},
		{"hstore object sorted", func() ([]any, error) {
			op, _ := hs.Op("contains")
			return op.Params(map[string]any{"b": nil, "a": "1"})
		}, []any{[]*string{strp("a"), strp("1"), strp("b"), nil}}, ""},
		{"hstore rejects numbers", func() ([]any, error) { return hs.Codec.Params(map[string]any{"a": 1.0}) }, nil, "string or null"},
		{"range object", func() ([]any, error) {
			return int4.Codec.Params(map[string]any{"start": 1.0, "end": json.Number("5"), "endInclusive": true})
		}, []any{"1", "5", "[]"}, ""},
		{"range defaults", func() ([]any, error) { return int4.Codec.Params(map[string]any{"start": 1}) }, []any{"1", nil, "[)"}, ""},
		{"range exclusive start", func() ([]any, error) {
			return num.Codec.Params(map[string]any{"start": "1.5", "startInclusive": false})
		}, []any{"1.5", nil, "()"}, ""},
		{"range literal", func() ([]any, error) { return num.Codec.Params("(1.5,2]") }, []any{"1.5", "2", "(]"}, ""},
		{"range empty", func() ([]any, error) { return int4.Codec.Params(map[string]any{"isEmpty": true}) }, []any{"0", "0", "[)"}, ""},
		{"range empty literal", func() ([]any, error) { return int4.Codec.Params("empty") }, []any{"0", "0", "[)"}, ""},
		{"range integral", func() ([]any, error) { return int4.Codec.Params(map[string]any{"start": 1.5}) }, nil, "integer"},
		{"range integral literal", func() ([]any, error) { return int4.Codec.Params("[a,2)") }, nil, "integer"},
		{"range unknown field", func() ([]any, error) { return int4.Codec.Params(map[string]any{"lo": 1}) }, nil, "unknown range field"},
		{"range bad bool", func() ([]any, error) { return int4.Codec.Params(map[string]any{"isEmpty": "yes"}) }, nil, "boolean"},
		{"range bad bound", func() ([]any, error) { return int4.Codec.Params(map[string]any{"start": true}) }, nil, "number or a string"},
		{"element", func() ([]any, error) {
			op, _ := int4.Op("containsElement")
			return op.Params(3.0)
		}, []any{"3"}, ""},
		{"element null", func() ([]any, error) {
			op, _ := int4.Op("containsElement")
			return op.Params(nil)
		}, nil, "must not be null"},
		{"range op", func() ([]any, error) {
			op, _ := num.Op("overlaps")
			return op.Params(map[string]any{"start": 0.25, "end": 1e21})
		}, []any{"0.25", "1000000000000000000000", "[)"}, ""},
		{"multirange list", func() ([]any, error) {
			mr := m(contrib.Multirange("int4multirange"))
			return mr.Codec.Params([]any{map[string]any{"start": 1, "end": 3}, "empty", map[string]any{"start": 5}})
		}, []any{[]*string{strp("1"), strp("0"), strp("5")}, []*string{strp("3"), strp("0"), nil}, []string{"[)", "[)", "[)"}}, ""},
		{"multirange literal", func() ([]any, error) {
			mr := m(contrib.Multirange("int4multirange"))
			return mr.Codec.Params("{[1,3), (,7]}")
		}, []any{[]*string{strp("1"), nil}, []*string{strp("3"), strp("7")}, []string{"[)", "(]"}}, ""},
		{"multirange empty literal", func() ([]any, error) {
			mr := m(contrib.Multirange("int4multirange"))
			return mr.Codec.Params("{}")
		}, []any{[]*string{}, []*string{}, []string{}}, ""},
	}
	for _, tc := range tests {
		got, err := tc.params()
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: params = %s, want %s", tc.name, show(got), show(tc.want))
		}
	}
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestParseRangeLiteral(t *testing.T) {
	tests := []struct {
		in       string
		lo, hi   *string
		bounds   string
		empty    bool
		wantFail bool
	}{
		{in: "[1,5)", lo: strp("1"), hi: strp("5"), bounds: "[)"},
		{in: " ( , 5 ] ", hi: strp("5"), bounds: "(]"},
		{in: "[1,)", lo: strp("1"), bounds: "[)"},
		{in: "(,)", bounds: "()"},
		{in: "EMPTY", empty: true},
		{in: `["2024-01-01 10:00","2024-01-02")`, lo: strp("2024-01-01 10:00"), hi: strp("2024-01-02"), bounds: "[)"},
		{in: `["a""b",c\,d]`, lo: strp(`a"b`), hi: strp("c,d"), bounds: "[]"},
		{in: `["",x)`, lo: strp(""), hi: strp("x"), bounds: "[)"},
		{in: "[1,2,3)", wantFail: true},
		{in: "[1)", wantFail: true},
		{in: "1,2", wantFail: true},
		{in: `["1,2)`, wantFail: true},
		{in: "", wantFail: true},
	}
	for _, tc := range tests {
		r, err := contrib.ParseRangeLiteral(tc.in)
		if tc.wantFail {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", tc.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if r.Empty != tc.empty || (!tc.empty && (!reflect.DeepEqual(r.Start, tc.lo) || !reflect.DeepEqual(r.End, tc.hi) || r.Bounds() != tc.bounds)) {
			t.Errorf("%q = %s %s, want %s %s %s", tc.in, show(r), r.Bounds(), show(tc.lo), show(tc.hi), tc.bounds)
		}
	}
	for _, bad := range []string{"[1,2)", "{[1,2)", "{[1,2),}", "{[1,2) [3,4)}", "{x}"} {
		if _, err := contrib.ParseMultirangeLiteral(bad); err == nil {
			t.Errorf("ParseMultirangeLiteral(%q): expected error", bad)
		}
	}
	got, err := contrib.ParseMultirangeLiteral(`{empty, ["a)",b]}`)
	if err != nil || len(got) != 2 || !got[0].Empty || *got[1].Start != "a)" {
		t.Errorf("multirange = %s, %v", show(got), err)
	}
}

// customRange is a range type as introspection records CREATE TYPE
// "my schema"."Float Range" AS RANGE (subtype = float8).
var customRange = &introspect.RangeType{
	Schema: "my schema", Name: "Float Range", Subtype: "float8", SubtypeSchema: "pg_catalog",
	Multirange: "Float Multi", MultirangeSchema: "my schema",
}

func TestCustomRangeRenders(t *testing.T) {
	m := must(t)
	r := contrib.RangeOf(customRange)
	mr := m(contrib.MultirangeOf(customRange))
	if r.Name != contrib.VocabRange || r.Type != "Float Range" || mr.Name != contrib.VocabMultirange || mr.Type != "Float Multi" {
		t.Errorf("vocabularies %s %s / %s %s", r.Name, r.Type, mr.Name, mr.Type)
	}
	ctor := `"my schema"."Float Range"($1::float8, $2::float8, $3::text)`
	for _, tc := range []struct {
		v    contrib.Vocabulary
		op   string
		args []string
		want string
	}{
		{r, "overlaps", []string{"$1", "$2", "$3"}, "(t.c) && " + ctor},
		{r, "containsElement", []string{"$1"}, "(t.c) @> $1::float8"},
		{mr, "contains", []string{"$1", "$2", "$3"}, "(t.c) @> " + ctor},
	} {
		op, ok := tc.v.Op(tc.op)
		if !ok {
			t.Fatalf("%s: no op %s", tc.v.Type, tc.op)
		}
		if got := op.Render("t.c", tc.args...); got != tc.want {
			t.Errorf("%s %s = %s, want %s", tc.v.Type, tc.op, got, tc.want)
		}
	}
	if got := r.Codec.SQL("$1", "$2", "$3"); got != ctor {
		t.Errorf("codec = %s", got)
	}
	want := `"my schema"."Float Multi"(VARIADIC ARRAY(SELECT "my schema"."Float Range"(__l, __u, __b) FROM unnest($1::float8[], $2::float8[], $3::text[]) AS __r(__l, __u, __b)))`
	if got := mr.Codec.SQL("$1", "$2", "$3"); got != want {
		t.Errorf("multirange codec = %s", got)
	}
	if a, _ := r.Accessor("start"); a.Type != "float8" {
		t.Errorf("start accessor type %q", a.Type)
	}
	if ps, err := r.Codec.Params("empty"); err != nil || !reflect.DeepEqual(ps, []any{"0", "0", "[)"}) {
		t.Errorf("empty params = %v, %v", ps, err)
	}
	if ps, err := r.Codec.Params(map[string]any{"start": 1.5}); err != nil || !reflect.DeepEqual(ps, []any{"1.5", nil, "[)"}) {
		t.Errorf("params = %v, %v", ps, err)
	}

	ints := contrib.RangeOf(&introspect.RangeType{Schema: "s", Name: "r", Subtype: "int2", SubtypeSchema: "pg_catalog"})
	if _, err := ints.Codec.Params(map[string]any{"start": 1.5}); err == nil {
		t.Error("int2 subtype accepted a fractional bound")
	}
	if _, ok := contrib.MultirangeOf(&introspect.RangeType{Schema: "s", Name: "r", Subtype: "int2", SubtypeSchema: "pg_catalog"}); ok {
		t.Error("MultirangeOf without a multirange type")
	}
	odd := contrib.RangeOf(&introspect.RangeType{Schema: "s", Name: "r", Subtype: "semver", SubtypeSchema: "s"})
	if got := odd.Codec.SQL("$1", "$2", "$3"); got != `"s"."r"($1::"s"."semver", $2::"s"."semver", $3::text)` {
		t.Errorf("custom subtype codec = %s", got)
	}
	if _, err := odd.Codec.Params("empty"); err == nil {
		t.Error("empty range accepted without a known sentinel")
	}
	if _, err := odd.Codec.Params("[1.0.0,2.0.0)"); err != nil {
		t.Errorf("custom subtype bounds: %v", err)
	}
}

func TestForColumnMatchesBaseTypeSchema(t *testing.T) {
	cat := &introspect.Catalog{
		Extensions: []*introspect.Extension{
			{Name: "pg_trgm", Schema: "trgm"}, {Name: "hstore", Schema: "ext"},
			{Name: "ltree", Schema: "ext"}, {Name: "citext", Schema: "ext"},
		},
		Ranges: []*introspect.RangeType{customRange},
	}
	col := func(typ, schema string) *introspect.Column {
		return &introspect.Column{PGType: typ, TypeSchema: "app", BaseTypeSchema: schema, IsArray: strings.HasPrefix(typ, "_")}
	}
	tests := []struct {
		col  *introspect.Column
		want string
	}{
		{col("hstore", "ext"), "hstore"},
		{col("hstore", "app"), ""},
		{col("hstore", ""), "hstore"},
		{col("ltree", "app"), ""},
		{col("_ltree", "ext"), "ltree[]"},
		{col("_ltree", "app"), ""},
		{col("citext", "ext"), "citext,textsearch,trgm"},
		{col("citext", "app"), ""},
		{col("text", "pg_catalog"), "textsearch,trgm"},
		{col("text", "app"), ""},
		{col("tsvector", "app"), ""},
		{col("inet", "app"), ""},
		{col("int4range", "pg_catalog"), "range"},
		{col("int4range", "app"), ""},
		{col("Float Range", "my schema"), "range"},
		{col("Float Range", "other"), ""},
		{col("Float Multi", "my schema"), "multirange"},
		{col("_Float Range", "my schema"), ""},
	}
	for _, tc := range tests {
		var got []string
		for _, v := range contrib.ForColumn(cat, tc.col) {
			got = append(got, v.Name)
		}
		if s := strings.Join(got, ","); s != tc.want {
			t.Errorf("%s in %q: %q, want %q", tc.col.PGType, tc.col.BaseTypeSchema, s, tc.want)
		}
	}
	if vs := contrib.ForType(cat, `"my schema"."Float Range"`, false); len(vs) != 1 || vs[0].Type != "Float Range" {
		t.Errorf("qualified custom range: %v", vs)
	}
	if vs := contrib.ForType(cat, "Float Multi", false); len(vs) != 1 || vs[0].Name != contrib.VocabMultirange {
		t.Errorf("unqualified custom multirange: %v", vs)
	}
	if vs := contrib.ForType(cat, "app.hstore", false); len(vs) != 0 {
		t.Errorf("app.hstore: %v", vs)
	}
}

func TestForType(t *testing.T) {
	all := &introspect.Catalog{Extensions: []*introspect.Extension{
		{Name: "pg_trgm", Schema: "trgm"}, {Name: "hstore", Schema: "ext"},
		{Name: "ltree", Schema: "public"}, {Name: "citext", Schema: "ci"},
	}}
	names := func(vs []contrib.Vocabulary) string {
		var out []string
		for _, v := range vs {
			s := v.Name
			if v.Schema != "" {
				s += "@" + v.Schema
			}
			out = append(out, s)
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		cat     *introspect.Catalog
		typ     string
		isArray bool
		want    string
	}{
		{all, "tsvector", false, "fulltext"},
		{all, "text", false, "textsearch,trgm@trgm"},
		{all, "varchar", false, "textsearch,trgm@trgm"},
		{all, "bpchar", false, "textsearch,trgm@trgm"},
		{all, "citext", false, "citext@ci,textsearch,trgm@trgm"},
		{all, `"ci"."citext"`, false, "citext@ci,textsearch,trgm@trgm"},
		{nil, "text", false, "textsearch"},
		{nil, "citext", false, ""},
		{nil, "hstore", false, ""},
		{all, "hstore", false, "hstore@ext"},
		{all, "ext.hstore", false, "hstore@ext"},
		{all, "public.hstore", false, ""},
		{all, "pg_catalog.text", false, "textsearch,trgm@trgm"},
		{all, "public.text", false, ""},
		{all, "public.int4range", false, ""},
		{all, "_hstore", true, ""},
		{all, "ltree", false, "ltree@public"},
		{all, "_ltree", true, "ltree[]@public"},
		{all, "public.ltree[]", false, "ltree[]@public"},
		{all, "ltree", true, "ltree[]@public"},
		{all, "int4range", false, "range"},
		{all, "tstzmultirange", false, "multirange"},
		{all, "_int4range", true, ""},
		{all, "inet", false, "inet"},
		{all, "cidr", false, "inet"},
		{all, "_text", true, ""},
		{all, "int4", false, ""},
		{all, "jsonb", false, ""},
	}
	for _, tc := range tests {
		if got := names(contrib.ForType(tc.cat, tc.typ, tc.isArray)); got != tc.want {
			t.Errorf("ForType(%q, %v) = %q, want %q", tc.typ, tc.isArray, got, tc.want)
		}
	}
	if vs := contrib.ForColumn(all, &introspect.Column{PGType: "text", EnumRef: "public.status"}); vs != nil {
		t.Errorf("enum column got %s", names(vs))
	}
	if vs := contrib.ForColumn(all, &introspect.Column{PGType: "geometry", Spatial: &introspect.SpatialType{}}); vs != nil {
		t.Errorf("spatial column got %s", names(vs))
	}
	if got := names(contrib.ForColumn(all, &introspect.Column{PGType: "_ltree", IsArray: true})); got != "ltree[]@public" {
		t.Errorf("ltree[] column got %s", got)
	}
	if contrib.ForColumn(all, nil) != nil {
		t.Error("nil column")
	}
}

func TestIsText(t *testing.T) {
	for typ, want := range map[string]bool{
		"text": true, "varchar": true, "bpchar": true, "citext": true, "ext.citext": true,
		"_text": false, "text[]": false, "name": false, "int4": false, "json": false,
	} {
		if got := contrib.IsText(typ); got != want {
			t.Errorf("IsText(%q) = %v", typ, got)
		}
	}
	if !contrib.IsCaseInsensitive("citext") || !contrib.IsCaseInsensitive(`"ext".citext`) ||
		contrib.IsCaseInsensitive("text") || contrib.IsCaseInsensitive("_citext") {
		t.Error("IsCaseInsensitive mismatch")
	}
}

func TestBindIsCopy(t *testing.T) {
	base := contrib.Hstore()
	bound := base.Bind("ext")
	if got := base.Ops[0].Render("c", "$1"); got != "(c) ? $1::text" {
		t.Errorf("Bind mutated the original: %s", got)
	}
	if bound.Schema != "ext" || bound.Codec == base.Codec {
		t.Error("Bind did not copy")
	}
	if v := contrib.FullText().Bind("ext"); v.Schema != "" {
		t.Error("core vocabulary took a schema")
	}
}

// TestNonAtomicOperandsAreGrouped renders every infix operator over a
// non-atomic column expression and placeholder: the column must stay
// grouped (bound OPERATOR() and bare % have different precedences), and a
// cast must apply to the whole placeholder expression.
func TestNonAtomicOperandsAreGrouped(t *testing.T) {
	const col = "t.a || t.b"
	for _, v := range allVocabularies(t) {
		for _, op := range v.Ops {
			lo, _ := arity(op.Arg)
			sql := op.Render(col, placeholders(lo)...)
			for i := 0; ; {
				j := strings.Index(sql[i:], col)
				if j < 0 {
					break
				}
				j += i
				before, after := sql[:j], sql[j+len(col):]
				if !strings.HasSuffix(before, "(") && !strings.HasSuffix(before, ", ") ||
					!strings.HasPrefix(after, ")") && !strings.HasPrefix(after, ",") {
					t.Errorf("%s %s: ungrouped column in %s", v.Name, op.Name, sql)
				}
				i = j + len(col)
			}
		}
	}
	trgm := contrib.Trgm("text")
	if got := trgm.Ops[0].Render(col, "$1 || 'x'"); got != "(t.a || t.b) % ($1 || 'x')::text" {
		t.Errorf("unbound = %s", got)
	}
	if got := trgm.Bind("ext").Ops[0].Render(col, "$1"); got != `(t.a || t.b) OPERATOR("ext".%) $1::text` {
		t.Errorf("bound = %s", got)
	}
	if got := contrib.Ltree().Bind("ext").Codec.SQL("$1 || $2"); got != `($1 || $2)::"ext".ltree` {
		t.Errorf("codec = %s", got)
	}
}

func TestTextListNil(t *testing.T) {
	for _, in := range []any{[]string(nil), []any(nil), []any{}} {
		got, err := contrib.TextList(in)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("TextList(%#v) = %#v, %v", in, got, err)
		}
	}
	if _, err := contrib.TextList(nil); err == nil {
		t.Error("TextList(nil) accepted a null operand")
	}
}

func TestTrgmBpcharNotIndexable(t *testing.T) {
	for _, op := range contrib.Trgm("bpchar").Ops {
		if op.Indexable() || !strings.Contains(op.Description, "(column::text)") {
			t.Errorf("bpchar %s: index %v, %q", op.Name, op.Index, op.Description)
		}
	}
	for _, e := range contrib.Trgm("bpchar").Exprs {
		if len(e.Index) != 0 {
			t.Errorf("bpchar %s: index %v", e.Name, e.Index)
		}
	}
	for _, op := range contrib.Trgm("varchar").Ops {
		if !op.Indexable() || strings.Contains(op.Description, "char(n)") {
			t.Errorf("varchar %s not indexable", op.Name)
		}
	}
}

func TestTrgmVersionGating(t *testing.T) {
	names := func(version string) string {
		cat := &introspect.Catalog{Extensions: []*introspect.Extension{{Name: "pg_trgm", Version: version, Schema: "ext"}}}
		v := contrib.ForType(cat, "text", false)[1]
		var out []string
		out = append(out, v.OpNames()...)
		for _, e := range v.Exprs {
			out = append(out, e.Name)
		}
		return strings.Join(out, ",")
	}
	all := "similarTo,wordSimilarTo,strictWordSimilarTo,similarity,wordSimilarity,strictWordSimilarity,distance,wordDistance,strictWordDistance"
	tests := map[string]string{
		"1.6":    all,
		"1.4":    all,
		"1.10":   all,
		"2":      all,
		"":       all,
		"1.4dev": all,
		"1.3":    "similarTo,wordSimilarTo,similarity,wordSimilarity,distance,wordDistance",
		"1.2":    "similarTo,wordSimilarTo,similarity,wordSimilarity,distance,wordDistance",
		"1.1":    "similarTo,similarity,distance",
		"0.9":    "similarTo,similarity,distance",
	}
	for version, want := range tests {
		if got := names(version); got != want {
			t.Errorf("pg_trgm %q: %s, want %s", version, got, want)
		}
	}
	if got := len(contrib.Trgm("text").ForVersion("1.3").Ops); got != 2 || len(contrib.Trgm("text").Ops) != 3 {
		t.Errorf("ForVersion mutated or miscounted: %d", got)
	}
}
