package contract_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contract"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbcore/testutil"
)

func col(name, comment string) *introspect.Column {
	return &introspect.Column{Name: name, PGType: "text", TypeSchema: "pg_catalog", Comment: comment}
}

func catalog() *introspect.Catalog {
	return &introspect.Catalog{
		Tables: []*introspect.Table{
			{Schema: "app", Name: "users", Columns: []*introspect.Column{
				col("id", ""), col("email", "@behavior -contract:public\nLogin."), col("notes", "@contract internal"),
				{Name: "secret_row", PGType: "secrets", TypeSchema: "app"},
				{Name: "secret_rows", PGType: "_secrets", TypeSchema: "app", IsArray: true},
			}},
			{Schema: "app", Name: "posts", Comment: "@behavior +contract:partner", Columns: []*introspect.Column{col("id", ""), col("author_id", "")},
				ForeignKeys: []*introspect.ForeignKey{
					{Name: "posts_author_id_fkey", Columns: []string{"author_id"}, RefSchema: "app", RefTable: "users", RefColumns: []string{"id"}},
					{Name: "posts_secret_fkey", Columns: []string{"id"}, RefSchema: "app", RefTable: "secrets", RefColumns: []string{"id"}},
					{Name: "posts_self_fkey", Columns: []string{"id"}, RefSchema: "app", RefTable: "posts", RefColumns: []string{"id"}, Comment: "@behavior -contract:partner"},
				}},
			{Schema: "app", Name: "secrets", Comment: "@contract internal\nOnly for staff.", Columns: []*introspect.Column{col("id", "")}},
			{Schema: "app", Name: "events", Partitioned: true, Columns: []*introspect.Column{col("id", "")}},
			{Schema: "app", Name: "events_2026", PartitionOf: "app.events", Columns: []*introspect.Column{col("id", "")}},
			{Schema: "app", Name: "events_2026_q1", PartitionOf: "app.events_2026", Columns: []*introspect.Column{col("id", "")}},
		},
		Functions: []*introspect.Function{
			{Schema: "app", Name: "search", ReturnType: "posts", ReturnTypeSchema: "app", ReturnsSet: true},
			{Schema: "app", Name: "staff_only", ReturnType: "int4", ReturnTypeSchema: "pg_catalog", Comment: "@contract internal"},
			{Schema: "app", Name: "secrets_label", Args: []introspect.FuncArg{{Name: "s", PGType: "secrets", TypeSchema: "app"}}, ReturnType: "text", ReturnTypeSchema: "pg_catalog"},
			{Schema: "app", Name: "all_secrets", ReturnType: "_secrets", ReturnTypeSchema: "app"},
			{Schema: "app", Name: "secret_table", ReturnType: "record", ReturnTypeSchema: "pg_catalog",
				ReturnColumns: []introspect.FuncArg{{Name: "s", PGType: "secrets", TypeSchema: "app", Mode: introspect.ArgModeTable}}},
			{Schema: "app", Name: "partner_feed", ReturnType: "int4", ReturnTypeSchema: "pg_catalog", Comment: "@behavior +contract:Partner"},
		},
	}
}

func tableNames(c *introspect.Catalog) []string {
	var out []string
	for _, t := range c.Tables {
		out = append(out, t.Name)
	}
	return out
}

func funcNames(c *introspect.Catalog) []string {
	var out []string
	for _, f := range c.Functions {
		out = append(out, f.Name)
	}
	return out
}

func omitted(c *introspect.Catalog, table string) []string {
	var out []string
	for _, col := range c.Table("app", table).Columns {
		tags, _ := smarttags.Parse(col.Comment)
		if _, all := tags.Omits(); all {
			out = append(out, col.Name)
		}
	}
	return out
}

func fkNames(c *introspect.Catalog, table string) []string {
	var out []string
	for _, fk := range c.Table("app", table).ForeignKeys {
		out = append(out, fk.Name)
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestApplyIncludePolicy(t *testing.T) {
	orig := catalog()
	view, sum := mustView(t, contract.Contract{Name: "public"}, orig)

	eq(t, "tables", tableNames(view), []string{"users", "posts", "events", "events_2026", "events_2026_q1"})
	eq(t, "users omitted columns", omitted(view, "users"), []string{"email", "notes", "secret_row", "secret_rows"})
	eq(t, "posts foreign keys", fkNames(view, "posts"), []string{"posts_author_id_fkey", "posts_self_fkey"})
	eq(t, "functions", funcNames(view), []string{"search", "partner_feed"})
	if want := (contract.Summary{Tables: 1, Columns: 4, Functions: 4, ForeignKeys: 1}); sum != want {
		t.Errorf("summary = %+v, want %+v", sum, want)
	}
	if got := view.Table("app", "users").Column("email").Comment; got != "@omit\n@behavior -contract:public\nLogin." {
		t.Errorf("email comment = %q", got)
	}
	if view.Table("app", "secrets") != nil {
		t.Error("lookup still finds the hidden table")
	}
	if len(orig.Tables) != 6 || orig.Table("app", "users").Column("email").Comment != "@behavior -contract:public\nLogin." {
		t.Error("View modified its input")
	}
}

func TestApplyExcludePolicy(t *testing.T) {
	view, _ := mustView(t, contract.Contract{Name: "PARTNER", Default: contract.Exclude}, catalog())

	eq(t, "tables", tableNames(view), []string{"posts"})
	eq(t, "posts foreign keys", fkNames(view, "posts"), nil)
	eq(t, "posts omitted columns", omitted(view, "posts"), nil)
	eq(t, "functions", funcNames(view), []string{"partner_feed"})
}

func TestApplyInternal(t *testing.T) {
	view, sum := mustView(t, contract.Contract{Name: "internal", Default: contract.Include}, catalog())

	eq(t, "tables", tableNames(view), []string{"users", "posts", "secrets", "events", "events_2026", "events_2026_q1"})
	eq(t, "users omitted columns", omitted(view, "users"), nil)
	eq(t, "functions", funcNames(view), []string{"search", "staff_only", "secrets_label", "all_secrets", "secret_table", "partner_feed"})
	if sum != (contract.Summary{}) {
		t.Errorf("summary = %+v", sum)
	}
}

func TestPartitionsFollowTheirParent(t *testing.T) {
	c := catalog()
	c.Table("app", "events").Comment = "@behavior -contract:public"
	view, _ := mustView(t, contract.Contract{Name: "public"}, c)
	eq(t, "tables", tableNames(view), []string{"users", "posts"})

	c = catalog()
	c.Table("app", "events").Comment = "@behavior +contract:partner"
	c.Table("app", "events_2026_q1").Comment = "@behavior -contract:partner"
	view, _ = mustView(t, contract.Contract{Name: "partner", Default: contract.Exclude}, c)
	eq(t, "tables", tableNames(view), []string{"posts", "events", "events_2026"})

	c = catalog()
	c.Table("app", "events").PartitionOf = "app.events_2026_q1"
	view, _ = mustView(t, contract.Contract{Name: "public"}, c)
	eq(t, "tables (partition cycle)", tableNames(view), []string{"users", "posts"})
}

func TestViewRejectsUnknownPolicy(t *testing.T) {
	_, _, err := contract.Contract{Name: "public", Default: "exlude"}.View(catalog())
	if err == nil || !strings.Contains(err.Error(), `"exlude" is not 'include' or 'exclude'`) {
		t.Fatalf("err = %v", err)
	}
}

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]contract.Policy{"": contract.Include, "include": contract.Include, "exclude": contract.Exclude} {
		if got, err := contract.ParsePolicy(in); err != nil || got != want {
			t.Errorf("ParsePolicy(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"Include", "none", " exclude"} {
		if _, err := contract.ParsePolicy(in); err == nil {
			t.Errorf("ParsePolicy(%q) accepted", in)
		}
	}
}

// TestApplyOnlyRemoves applies contracts to the shared fixture with every
// object tagged in or out at random positions and checks that the view is
// a subset of the input: no table, column, foreign key or function appears,
// and a column comment only ever gains a leading @omit line.
func TestApplyOnlyRemoves(t *testing.T) {
	tags := []string{"", "@contract a", "@contract b", "@behavior -contract:a", "@behavior +contract:b", "@contract", "@omit read"}
	for seed := range 50 {
		c := testutil.FixtureCatalog()
		i := seed
		next := func() string { i = (i*31 + 7) % 1009; return tags[i%len(tags)] }
		for _, tb := range c.Tables {
			tb.Comment = next()
			for _, col := range tb.Columns {
				col.Comment = next()
			}
			for _, fk := range tb.ForeignKeys {
				fk.Comment = next()
			}
		}
		for _, f := range c.Functions {
			f.Comment = next()
		}
		for _, ct := range []contract.Contract{{Name: "a"}, {Name: "b", Default: contract.Exclude}, {Name: "c"}} {
			view, _ := mustView(t, ct, c)
			for _, vt := range view.Tables {
				ot := c.Table(vt.Schema, vt.Name)
				if ot == nil {
					t.Fatalf("%s: table %s added", ct.Name, vt.Name)
				}
				if len(vt.Columns) != len(ot.Columns) {
					t.Fatalf("%s: %s columns changed", ct.Name, vt.Name)
				}
				for k, vc := range vt.Columns {
					if oc := ot.Columns[k]; vc.Name != oc.Name || (vc.Comment != oc.Comment && vc.Comment != omittedComment(oc.Comment)) {
						t.Fatalf("%s: %s.%s comment %q from %q", ct.Name, vt.Name, vc.Name, vc.Comment, oc.Comment)
					}
				}
				for _, fk := range vt.ForeignKeys {
					if !slices.ContainsFunc(ot.ForeignKeys, func(o *introspect.ForeignKey) bool { return o.Name == fk.Name }) {
						t.Fatalf("%s: foreign key %s added", ct.Name, fk.Name)
					}
					if view.Table(fk.RefSchema, fk.RefTable) == nil {
						t.Fatalf("%s: foreign key %s references a hidden table", ct.Name, fk.Name)
					}
				}
			}
			for _, f := range view.Functions {
				if !slices.ContainsFunc(c.Functions, func(o *introspect.Function) bool { return o.Name == f.Name && o.Schema == f.Schema }) {
					t.Fatalf("%s: function %s added", ct.Name, f.Name)
				}
				if strings.Contains(f.Comment, "-contract:"+ct.Name) {
					t.Fatalf("%s: function %s tagged out but kept", ct.Name, f.Name)
				}
			}
		}
	}
}

func TestCloneOfFixtureHashesTheSame(t *testing.T) {
	c := testutil.FixtureCatalog()
	cp := c.Clone()
	want, err := c.Hash()
	if err != nil {
		t.Fatal(err)
	}
	got, err := cp.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("clone hash %s, want %s", got, want)
	}
	view, _ := mustView(t, contract.Contract{Name: "nobody", Default: contract.Exclude}, c)
	if len(view.Tables) != 0 || len(view.Functions) != 0 {
		t.Fatalf("exclude-all view kept %d tables, %d functions", len(view.Tables), len(view.Functions))
	}
	if after, _ := c.Hash(); after != want {
		t.Fatal("View changed its input")
	}
}

func mustView(t *testing.T, ct contract.Contract, c *introspect.Catalog) (*introspect.Catalog, contract.Summary) {
	t.Helper()
	view, sum, err := ct.View(c)
	if err != nil {
		t.Fatal(err)
	}
	return view, sum
}

func omittedComment(comment string) string {
	if comment == "" {
		return "@omit"
	}
	return "@omit\n" + comment
}
