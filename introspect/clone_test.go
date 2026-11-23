package introspect

import (
	"encoding/json"
	"reflect"
	"testing"
)

// fill sets every exported field reachable from v to a non-zero value:
// slices get two elements, pointers a fresh value.
func fill(v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.Pointer:
		if depth > 6 {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), depth+1)
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i), depth+1)
		}
		v.Set(s)
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	}
}

// assertDisjoint fails when a and b (deep-equal values) share a pointer or
// a slice backing array anywhere below them.
func assertDisjoint(t *testing.T, path string, a, b reflect.Value) {
	t.Helper()
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s: pointer shared with the original", path)
			return
		}
		assertDisjoint(t, path, a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if a.Type().Field(i).IsExported() {
				assertDisjoint(t, path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i))
			}
		}
	case reflect.Slice:
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			t.Errorf("%s: slice backing array shared with the original", path)
		}
		for i := 0; i < a.Len(); i++ {
			assertDisjoint(t, path+"[]", a.Index(i), b.Index(i))
		}
	}
}

func TestCloneIsDeep(t *testing.T) {
	var c Catalog
	fill(reflect.ValueOf(&c).Elem(), 0)
	cp := c.Clone()

	want, _ := json.Marshal(&c)
	got, _ := json.Marshal(cp)
	if string(want) != string(got) {
		t.Fatalf("clone differs:\n got %s\nwant %s", got, want)
	}
	assertDisjoint(t, "Catalog", reflect.ValueOf(cp).Elem(), reflect.ValueOf(&c).Elem())

	cp.Tables[0].Columns[0].Comment = "changed"
	cp.Tables[0].Name = "renamed"
	if c.Tables[0].Columns[0].Comment != "x" || c.Tables[0].Name != "x" {
		t.Fatal("mutating the clone changed the original")
	}
}

func TestCloneLookups(t *testing.T) {
	c := &Catalog{Tables: []*Table{{Schema: "public", Name: "users"}, {Schema: "public", Name: "posts"}}}
	if c.Table("public", "posts") == nil {
		t.Fatal("original lookup")
	}
	cp := c.Clone()
	got := cp.Table("public", "posts")
	if got == nil || got == c.Tables[1] {
		t.Fatalf("clone lookup = %p, want the clone's own table", got)
	}
	if (*Catalog)(nil).Clone() != nil {
		t.Fatal("nil Clone")
	}
	empty := (&Catalog{}).Clone()
	if empty.Tables != nil || empty.Domains != nil {
		t.Fatal("nil slices must stay nil (cache hash stability)")
	}
}
