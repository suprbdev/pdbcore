package introspect

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func callFixture() *Function {
	return &Function{
		Schema: "app", Name: `we"ird`,
		Args: []FuncArg{
			{Name: "a", PGType: "int4", Mode: ArgModeIn},
			{Name: "b", PGType: "text", Mode: ArgModeInOut, HasDefault: true, Default: "'x'::text"},
			{Name: "xs", PGType: "_int4", Mode: ArgModeVariadic, HasDefault: true, Default: "'{}'::integer[]"},
		},
	}
}

func TestCallSQL(t *testing.T) {
	named := &Function{
		Schema: "public", Name: "three",
		Args: []FuncArg{
			{Name: "a", PGType: "int4"},
			{Name: "b", PGType: "int4", HasDefault: true},
			{Name: "c", PGType: "int4", HasDefault: true},
		},
	}
	cases := []struct {
		name    string
		fn      *Function
		present map[string]bool
		want    string
		missing []string
	}{
		{"named all", named, map[string]bool{"a": true, "b": true, "c": true}, `"public"."three"("a" => $1::int4, "b" => $2::int4, "c" => $3::int4)`, nil},
		{"named gap", named, map[string]bool{"a": true, "c": true}, `"public"."three"("a" => $1::int4, "c" => $2::int4)`, nil},
		{"named required omitted", named, map[string]bool{"c": true}, "", []string{"a"}},
		{"variadic all", callFixture(), map[string]bool{"a": true, "b": true, "xs": true}, `"app"."we""ird"($1::int4, $2::text, VARIADIC $3::_int4)`, nil},
		{"variadic trailing omitted", callFixture(), map[string]bool{"a": true}, `"app"."we""ird"($1::int4)`, nil},
		{"variadic gap", callFixture(), map[string]bool{"a": true, "xs": true}, "", []string{"b"}},
		{"variadic required and gap", callFixture(), map[string]bool{"xs": true}, "", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			got, err := tc.fn.CallSQL(func(a FuncArg) (string, bool, error) {
				if !tc.present[a.Name] {
					return "", false, nil
				}
				n++
				return "$" + strconv.Itoa(n) + "::" + a.PGType, true, nil
			})
			if tc.missing != nil {
				var missing *MissingArgsError
				if !errors.As(err, &missing) || !reflect.DeepEqual(missing.Args, tc.missing) {
					t.Fatalf("CallSQL = %q, %v; want missing %v", got, err, tc.missing)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("CallSQL = %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	_, err := callFixture().CallSQL(func(FuncArg) (string, bool, error) { return "", false, nil })
	var missing *MissingArgsError
	if !errors.As(err, &missing) || missing.Function != `app.we"ird` || !reflect.DeepEqual(missing.Args, []string{"a"}) {
		t.Fatalf("missing required arg: err = %v", err)
	}

	boom := errors.New("boom")
	if _, err := callFixture().CallSQL(func(FuncArg) (string, bool, error) { return "", false, boom }); !errors.Is(err, boom) {
		t.Fatalf("callback error = %v, want boom", err)
	}

	noArgs := &Function{Schema: "public", Name: "now_ish"}
	if got, err := noArgs.CallSQL(nil); err != nil || got != `"public"."now_ish"()` {
		t.Fatalf("no-arg CallSQL = %q, %v", got, err)
	}
}

func TestBuildFuncArgs(t *testing.T) {
	f := &Function{}
	ok := buildFuncArgs(f, 1,
		[]string{"a", "", "", "b"},
		[]string{"i", "o", "o", "b"},
		[]string{"int4", "int4", "text", "text"},
		[]string{"pg_catalog", "pg_catalog", "pg_catalog", "pg_catalog"},
		[]string{"", "", "", "'x'::text"},
		[]string{"", "", "", ""})
	if !ok {
		t.Fatal("buildFuncArgs rejected a function with named inputs")
	}
	wantArgs := []FuncArg{
		{Name: "a", PGType: "int4", TypeSchema: "pg_catalog", Mode: ArgModeIn},
		{Name: "b", PGType: "text", TypeSchema: "pg_catalog", Mode: ArgModeInOut, HasDefault: true, Default: "'x'::text"},
	}
	wantCols := []FuncArg{
		{Name: "column1", PGType: "int4", TypeSchema: "pg_catalog", Mode: ArgModeOut},
		{Name: "column2", PGType: "text", TypeSchema: "pg_catalog", Mode: ArgModeOut},
		{Name: "b", PGType: "text", TypeSchema: "pg_catalog", Mode: ArgModeInOut},
	}
	if !reflect.DeepEqual(f.Args, wantArgs) || !reflect.DeepEqual(f.ReturnColumns, wantCols) {
		t.Fatalf("Args = %+v\nReturnColumns = %+v", f.Args, f.ReturnColumns)
	}
	if f.Variadic() != nil {
		t.Fatal("Variadic() on a function without one")
	}

	if buildFuncArgs(&Function{}, 0, []string{""}, []string{"i"}, []string{"int4"}, []string{"pg_catalog"}, []string{""}, []string{""}) {
		t.Fatal("buildFuncArgs accepted an unnamed input parameter")
	}
}
