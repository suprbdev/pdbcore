package introspect

import (
	"reflect"
	"slices"
	"testing"
)

func hashCatalog() *Catalog {
	fn := func(name string, types ...string) *Function {
		f := &Function{Schema: "app", Name: name, ReturnType: "int4", ReturnTypeSchema: "pg_catalog"}
		for i, typ := range types {
			f.Args = append(f.Args, FuncArg{Name: string(rune('a' + i)), PGType: typ, TypeSchema: "pg_catalog"})
		}
		return f
	}
	return &Catalog{
		FormatVersion:    CatalogFormatVersion,
		ServerVersion:    "18.1",
		ServerVersionNum: 180001,
		Schemas:          []string{"b", "a"},
		Tables:           []*Table{{Schema: "b", Name: "t"}, {Schema: "a", Name: "u"}},
		Enums:            []*Enum{{Schema: "b", Name: "e"}, {Schema: "a", Name: "e"}},
		Composites:       []*Composite{{Schema: "b", Name: "c"}, {Schema: "a", Name: "c"}},
		Functions:        []*Function{fn("f", "text"), fn("f", "int4", "int4"), fn("f", "int4"), fn("e")},
		Extensions:       []*Extension{{Name: "y"}, {Name: "x"}},
		Ranges:           []*RangeType{{Schema: "b", Name: "r"}, {Schema: "a", Name: "r"}},
		Domains:          []*Domain{{Schema: "b", Name: "d"}, {Schema: "a", Name: "d"}},
	}
}

func TestHashDoesNotMutate(t *testing.T) {
	c := hashCatalog()
	before := hashCatalog()
	h1, err := c.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, before) {
		t.Fatal("Hash reordered the catalog")
	}
	c.sortStable()
	h2, _ := c.Hash()
	if h1 != h2 {
		t.Fatalf("hash depends on slice order: %s != %s", h1, h2)
	}
}

func TestHashIgnoresMinorVersion(t *testing.T) {
	a, b := hashCatalog(), hashCatalog()
	b.ServerVersion, b.ServerVersionNum = "18.3", 180003
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha != hb {
		t.Fatal("a minor version change altered the hash")
	}
	b.ServerVersion, b.ServerVersionNum = "19.0", 190000
	if hb, _ = b.Hash(); ha == hb {
		t.Fatal("a major version change left the hash unchanged")
	}
}

// TestHashCoversEveryField fails when a Catalog field is added without
// Hash covering it.
func TestHashCoversEveryField(t *testing.T) {
	base, _ := hashCatalog().Hash()
	typ := reflect.TypeFor[Catalog]()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() || f.Name == "ServerVersion" {
			continue
		}
		c := hashCatalog()
		v := reflect.ValueOf(c).Elem().Field(i)
		switch v.Kind() {
		case reflect.Int:
			v.SetInt(v.Int() + 10000)
		case reflect.Slice:
			v.Set(reflect.Append(v, reflect.New(v.Type().Elem()).Elem()))
			if e := v.Index(v.Len() - 1); e.Kind() == reflect.Pointer {
				e.Set(reflect.New(e.Type().Elem()))
			}
		default:
			t.Fatalf("field %s: unhandled kind %s", f.Name, v.Kind())
		}
		if h, _ := c.Hash(); h == base {
			t.Errorf("Hash ignores Catalog.%s", f.Name)
		}
	}
}

func TestFunctionOrder(t *testing.T) {
	c := hashCatalog()
	c.sortStable()
	var got []string
	for _, f := range c.Functions {
		sig := f.Name + "("
		for i, a := range f.Args {
			if i > 0 {
				sig += ","
			}
			sig += a.PGType
		}
		got = append(got, sig+")")
	}
	want := []string{"e()", "f(int4)", "f(text)", "f(int4,int4)"}
	if !slices.Equal(got, want) {
		t.Fatalf("function order = %v, want %v", got, want)
	}
	rev := hashCatalog()
	slices.Reverse(rev.Functions)
	rev.sortStable()
	for i := range c.Functions {
		if !reflect.DeepEqual(c.Functions[i], rev.Functions[i]) {
			t.Fatalf("order depends on input order at %d", i)
		}
	}
}
