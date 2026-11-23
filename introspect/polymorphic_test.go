package introspect

import (
	"strings"
	"testing"
)

func polyCatalog() *Catalog {
	return &Catalog{
		Schemas:    []string{"public", "app"},
		Enums:      []*Enum{{Schema: "public", Name: "mood", Values: []string{"a"}}, {Schema: "public", Name: "kind", TableBacked: true}},
		Composites: []*Composite{{Schema: "app", Name: "pair"}},
		Domains:    []*Domain{{Schema: "public", Name: "email", BaseType: "text", BaseTypeSchema: "pg_catalog"}},
		Ranges: []*RangeType{
			{Schema: "public", Name: "floatrange", Subtype: "float8", SubtypeSchema: "pg_catalog", Multirange: "floatmultirange", MultirangeSchema: "public"},
			{Schema: "public", Name: "r1", Subtype: "money", SubtypeSchema: "pg_catalog"},
			{Schema: "public", Name: "r2", Subtype: "money", SubtypeSchema: "pg_catalog"},
			{Schema: "public", Name: "r3", Subtype: "bytea", SubtypeSchema: "pg_catalog"},
		},
		Extensions: []*Extension{{Name: "postgis", Schema: "ext"}},
	}
}

func TestParseTypeName(t *testing.T) {
	c := polyCatalog()
	for _, tc := range []struct{ in, want string }{
		{"integer", "pg_catalog.int4"},
		{"INT", "pg_catalog.int4"},
		{"double  precision", "pg_catalog.float8"},
		{"timestamp with time zone", "pg_catalog.timestamptz"},
		{"character varying(20)", "pg_catalog.varchar"},
		{"numeric(10,2)[]", "pg_catalog._numeric"},
		{"text[][]", "pg_catalog._text"},
		{"mood", "public.mood"},
		{"pair", "app.pair"},
		{"email", "public.email"},
		{"floatrange", "public.floatrange"},
		{"floatmultirange", "public.floatmultirange"},
		{"geometry", "ext.geometry"},
		{"other.thing", "other.thing"},
		{`"Odd"."Name"`, "Odd.Name"},
		{`"int"`, "public.int"},
	} {
		got, err := c.ParseTypeName(tc.in, "public")
		if tc.in == `"int"` {
			if err == nil {
				t.Errorf("%s: want unknown-type error for a quoted non-builtin, got %s", tc.in, got)
			}
			continue
		}
		if err != nil || got.String() != tc.want {
			t.Errorf("ParseTypeName(%q) = %s, %v; want %s", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "nope", "kind", "a.b.c", "x(", `"open`, "a.", "[]"} {
		if got, err := c.ParseTypeName(bad, "public"); err == nil {
			t.Errorf("ParseTypeName(%q) = %s, want error", bad, got)
		}
	}
}

func TestResolvePolymorphic(t *testing.T) {
	c := polyCatalog()
	ref := func(s string) *TypeRef {
		r, err := c.ParseTypeName(s, "public")
		if err != nil {
			t.Fatal(err)
		}
		return &r
	}
	fn := func(ret string, args ...string) *Function {
		f := &Function{Schema: "public", Name: "f", ReturnType: ret, ReturnTypeSchema: "pg_catalog"}
		for i, a := range args {
			f.Args = append(f.Args, FuncArg{Name: string(rune('a' + i)), PGType: a, TypeSchema: "pg_catalog"})
		}
		return f
	}
	sig := func(f *Function) string {
		var parts []string
		for _, a := range f.Args {
			parts = append(parts, a.TypeSchema+"."+a.PGType)
		}
		return strings.Join(parts, ",") + "->" + f.ReturnTypeSchema + "." + f.ReturnType
	}
	for _, tc := range []struct {
		name         string
		f            *Function
		elem, compat *TypeRef
		want, err    string
	}{
		{name: "element", f: fn("anyelement", "anyelement", "anyarray"), elem: ref("integer"),
			want: "pg_catalog.int4,pg_catalog._int4->pg_catalog.int4"},
		{name: "range", f: fn("anyelement", "anyrange", "anymultirange"), elem: ref("int"),
			want: "pg_catalog.int4range,pg_catalog.int4multirange->pg_catalog.int4"},
		{name: "custom range", f: fn("anyrange", "anyelement"), elem: ref("float8"),
			want: "pg_catalog.float8->public.floatrange"},
		{name: "enum", f: fn("text", "anyenum"), elem: ref("mood"),
			want: "public.mood->pg_catalog.text"},
		{name: "compatible", f: fn("anycompatiblearray", "anycompatible", "int4"), compat: ref("numeric"),
			want: "pg_catalog.numeric,pg_catalog.int4->pg_catalog._numeric"},
		{name: "array element", f: fn("anyelement", "anyelement"), elem: ref("text[]"),
			want: "pg_catalog._text->pg_catalog._text"},
		{name: "missing family", f: fn("int4", "anyelement", "anycompatible"), elem: ref("text"), err: "no type given for anycompatible"},
		{name: "not enum", f: fn("int4", "anyenum"), elem: ref("text"), err: "not an enum"},
		{name: "array of array", f: fn("int4", "anyarray"), elem: ref("text[]"), err: "is an array"},
		{name: "no range", f: fn("int4", "anyrange"), elem: ref("text"), err: "no range type"},
		{name: "ambiguous range", f: fn("int4", "anyrange"), elem: ref("money"), err: "several range types"},
		{name: "no multirange", f: fn("int4", "anymultirange"), elem: ref("bytea"), err: "no multirange"},
	} {
		got, err := c.ResolvePolymorphic(tc.f, tc.elem, tc.compat)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if s := sig(got); s != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, s, tc.want)
		}
		if len(got.PolymorphicFamilies()) != 0 {
			t.Errorf("%s: still polymorphic", tc.name)
		}
		if !strings.HasPrefix(tc.f.Args[0].PGType, "any") {
			t.Errorf("%s: input function mutated", tc.name)
		}
	}
}

func TestResolvePolymorphicDomainAndOutputs(t *testing.T) {
	c := polyCatalog()
	f := &Function{
		Schema: "public", Name: "f", ReturnType: "record", ReturnTypeSchema: "pg_catalog",
		Args:          []FuncArg{{Name: "v", PGType: "anyelement", TypeSchema: "pg_catalog"}},
		ReturnColumns: []FuncArg{{Name: "x", PGType: "anyarray", TypeSchema: "pg_catalog", Mode: ArgModeOut}, {Name: "y", PGType: "int4", TypeSchema: "pg_catalog", Mode: ArgModeOut}},
	}
	if fams := f.PolymorphicFamilies(); len(fams) != 1 || fams[0] != PolyAnyElement {
		t.Fatalf("families = %v", fams)
	}
	got, err := c.ResolvePolymorphic(f, &TypeRef{"public", "email"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Args[0]; a.PGType != "email" || a.Domain != "public.email" {
		t.Fatalf("arg = %+v", a)
	}
	if rc := got.ReturnColumns[0]; rc.PGType != "_email" || rc.Domain != "public.email" {
		t.Fatalf("return column = %+v", rc)
	}
	if got.ReturnType != "record" || f.ReturnColumns[0].PGType != "anyarray" {
		t.Fatal("non-polymorphic return changed or original mutated")
	}
}
