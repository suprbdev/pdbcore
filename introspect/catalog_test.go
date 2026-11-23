package introspect

import (
	"reflect"
	"slices"
	"testing"
)

func TestConstraintTemporalHelpers(t *testing.T) {
	plain := &Constraint{Name: "rooms_code_key", Columns: []string{"code"}}
	temporal := &Constraint{Name: "rooms_pkey", Columns: []string{"id", "valid_at"}, WithoutOverlaps: true}

	if !plain.RowKey() || temporal.RowKey() || (*Constraint)(nil).RowKey() {
		t.Fatal("RowKey: want plain true, temporal and nil false")
	}
	if got := plain.EqualityColumns(); !reflect.DeepEqual(got, []string{"code"}) {
		t.Fatalf("plain EqualityColumns = %v", got)
	}
	if got := temporal.EqualityColumns(); !reflect.DeepEqual(got, []string{"id"}) {
		t.Fatalf("temporal EqualityColumns = %v", got)
	}
	if plain.PeriodColumn() != "" || temporal.PeriodColumn() != "valid_at" {
		t.Fatalf("PeriodColumn = %q, %q", plain.PeriodColumn(), temporal.PeriodColumn())
	}

	tbl := &Table{PrimaryKey: temporal, Uniques: []*Constraint{
		{Name: "rooms_name_during_key", Columns: []string{"name", "during"}, WithoutOverlaps: true},
		plain,
	}}
	if got := tbl.RowKeys(); len(got) != 1 || got[0] != plain {
		t.Fatalf("RowKeys = %+v, want only %s", got, plain.Name)
	}
	tbl.PrimaryKey = &Constraint{Name: "rooms_pkey", Columns: []string{"id"}}
	if got := tbl.RowKeys(); len(got) != 2 || got[0] != tbl.PrimaryKey || got[1] != plain {
		t.Fatalf("RowKeys = %+v, want primary key then %s", got, plain.Name)
	}
	if got := (&Table{}).RowKeys(); len(got) != 0 {
		t.Fatalf("RowKeys of keyless table = %+v", got)
	}
}

func TestForeignKeyJoinPairs(t *testing.T) {
	fk := &ForeignKey{
		Columns:    []string{"tenant_id", "room_id", "during"},
		RefColumns: []string{"tenant_id", "id", "valid_at"},
	}
	want := []JoinPair{
		{Column: "tenant_id", RefColumn: "tenant_id", Op: JoinEqual},
		{Column: "room_id", RefColumn: "id", Op: JoinEqual},
		{Column: "during", RefColumn: "valid_at", Op: JoinEqual},
	}
	if got := fk.JoinPairs(); !reflect.DeepEqual(got, want) || !fk.ReferencesOne() {
		t.Fatalf("plain JoinPairs = %+v (ReferencesOne %v)", got, fk.ReferencesOne())
	}
	fk.Period = true
	want[2].Op = JoinOverlap
	if got := fk.JoinPairs(); !reflect.DeepEqual(got, want) || fk.ReferencesOne() {
		t.Fatalf("period JoinPairs = %+v (ReferencesOne %v)", got, fk.ReferencesOne())
	}
}

func TestColumnVirtual(t *testing.T) {
	for kind, want := range map[GeneratedKind]bool{
		"": false, GeneratedIdentity: false, GeneratedStored: false, GeneratedVirtual: true,
	} {
		if got := (&Column{GeneratedKind: kind}).Virtual(); got != want {
			t.Errorf("Virtual() for %q = %v, want %v", kind, got, want)
		}
	}
}

func TestConstraintColumns(t *testing.T) {
	tbl := &Table{
		PrimaryKey: &Constraint{Name: "ux_pkey", Columns: []string{"id"}},
		Uniques:    []*Constraint{{Name: "ux_owner_code_key", Columns: []string{"owner", "code"}}},
		Checks: []*Constraint{
			{Name: "ux_check", Columns: []string{"secret", "code"}},
			{Name: "ux_true_check"},
		},
		ForeignKeys: []*ForeignKey{{Name: "ux_owner_fkey", Columns: []string{"owner"}, RefTable: "owners", RefColumns: []string{"secret"}}},
		Indexes: []*Index{
			{Name: "ux_owner_code_key", Columns: []string{"owner", "code"}, Unique: true},
			{Name: "ux_owner_lower_key", Columns: []string{"owner"}, ExpressionColumns: []string{"secret"}, Unique: true},
			{Name: "ux_code_partial_key", Columns: []string{"code"}, ExpressionColumns: []string{"code", "secret"}, Unique: true},
			{Name: "ux_check", Columns: []string{"id"}},
		},
	}
	tests := []struct {
		name string
		cols []string
		ok   bool
	}{
		{"ux_pkey", []string{"id"}, true},
		{"ux_owner_code_key", []string{"owner", "code"}, true},
		{"ux_check", []string{"secret", "code", "id"}, true},
		{"ux_true_check", nil, true},
		{"ux_owner_fkey", []string{"owner"}, true},
		{"ux_owner_lower_key", []string{"owner", "secret"}, true},
		{"ux_code_partial_key", []string{"code", "secret"}, true},
		{"ux_missing", nil, false},
	}
	secret := func(c string) bool { return c == "secret" }
	for _, tc := range tests {
		cols, ok := tbl.ConstraintColumns(tc.name)
		if !reflect.DeepEqual(cols, tc.cols) || ok != tc.ok {
			t.Errorf("ConstraintColumns(%s) = %v, %v, want %v, %v", tc.name, cols, ok, tc.cols, tc.ok)
		}
		refs, ok := tbl.ConstraintReferences(tc.name, secret)
		if want := slices.Contains(tc.cols, "secret"); refs != want || ok != tc.ok {
			t.Errorf("ConstraintReferences(%s) = %v, %v, want %v, %v", tc.name, refs, ok, want, tc.ok)
		}
	}
}
