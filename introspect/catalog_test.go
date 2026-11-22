package introspect

import (
	"reflect"
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
