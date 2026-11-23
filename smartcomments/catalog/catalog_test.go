package catalog_test

import (
	"context"
	"testing"

	"github.com/suprbdev/pdbcore/contract"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/plugin"
	"github.com/suprbdev/pdbcore/smartcomments/catalog"
	"github.com/suprbdev/pdbcore/testutil"
)

func run(t *testing.T, prep func(*introspect.Catalog)) (*introspect.Catalog, *catalog.Hook) {
	t.Helper()
	cat := testutil.FixtureCatalog()
	if prep != nil {
		prep(cat)
	}
	h := catalog.New()
	reg := plugin.NewRegistry(h)
	if err := reg.TransformCatalog(context.Background(), nil, cat); err != nil {
		t.Fatal(err)
	}
	return cat, h
}

func TestIdentity(t *testing.T) {
	h := catalog.New()
	if h.Name() != "smart-comments" || h.Priority() != 50 {
		t.Fatalf("name/priority = %s/%d", h.Name(), h.Priority())
	}
	var _ plugin.CatalogHook = h
	var _ plugin.Plugin = h
}

func TestOmitTableRemovesIt(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) { c.Table("public", "metrics").Comment = "@omit" })
	if cat.Table("public", "metrics") != nil {
		t.Fatal("@omit table must vanish from the catalog")
	}
	if cat.Table("public", "users") == nil {
		t.Fatal("other tables must survive")
	}
}

func TestOmitActionsGatePrivileges(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Comment = "@omit update,delete"
		c.Table("public", "posts").Comment = "@omit all"
		c.Table("public", "places").Comment = "@behavior -insert"
	})
	u := cat.Table("public", "users")
	if !u.Privileges.Insert || u.Privileges.Update || u.Privileges.Delete {
		t.Fatalf("users privileges = %+v", u.Privileges)
	}
	// @omit all has no catalog representation: the surface half handles it.
	p := cat.Table("public", "posts")
	if !p.Privileges.Insert || !p.Privileges.Update || !p.Privileges.Delete {
		t.Fatalf("posts privileges = %+v", p.Privileges)
	}
	if cat.Table("public", "places").Privileges.Insert {
		t.Fatal("@behavior -insert must map to @omit create")
	}
}

func TestOmitFunction(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		for _, f := range c.Functions {
			if f.Name == "search_posts" {
				f.Comment = "@omit"
			}
		}
	})
	for _, f := range cat.Functions {
		if f.Name == "search_posts" {
			t.Fatal("@omit function must vanish")
		}
	}
}

func TestViewKeysAndRelations(t *testing.T) {
	cat, h := run(t, func(c *introspect.Catalog) {
		m := c.Table("public", "metrics")
		m.Comment = "@primaryKey name\n@unique value\n@foreignKey (value) references users (id)|@fieldName owner|@foreignFieldName metrics"
		m.Column("value").Comment = "@notNull"
		m.Column("name").Comment = "@nullable"
	})
	m := cat.Table("public", "metrics")
	if m.PrimaryKey == nil || m.PrimaryKey.Name != "@primaryKey" || m.PrimaryKey.Columns[0] != "name" {
		t.Fatalf("primary key = %+v", m.PrimaryKey)
	}
	if len(m.Uniques) != 1 || m.Uniques[0].Name != "@unique value" || m.Uniques[0].Columns[0] != "value" {
		t.Fatalf("uniques = %+v", m.Uniques)
	}
	if len(m.ForeignKeys) != 1 {
		t.Fatalf("foreign keys = %+v", m.ForeignKeys)
	}
	fk := m.ForeignKeys[0]
	if fk.Name != "@foreignKey (value) references users (id)" || fk.RefSchema != "public" || fk.RefTable != "users" ||
		fk.Columns[0] != "value" || fk.RefColumns[0] != "id" {
		t.Fatalf("fk = %+v", fk)
	}
	if !m.Column("value").NotNull || m.Column("name").NotNull {
		t.Fatal("@notNull/@nullable not applied")
	}
	// The |@fieldName segments land in the index under the synthetic name.
	tags := h.Index().Constraint("public", fk.Name)
	if tags.First("fieldName") != "owner" || tags.First("foreignFieldName") != "metrics" {
		t.Fatalf("fk tags = %v", tags)
	}
}

func TestPrimaryKeyIgnoredWhenPresentOrUnknown(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		c.Table("public", "users").Comment = "@primaryKey email"
		c.Table("public", "metrics").Comment = "@primaryKey nope\n@unique nope"
	})
	if cat.Table("public", "users").PrimaryKey.Name != "users_pkey" {
		t.Fatal("real primary key must win")
	}
	m := cat.Table("public", "metrics")
	if m.PrimaryKey != nil || len(m.Uniques) != 0 {
		t.Fatalf("unknown columns must be ignored: pk=%v uniques=%v", m.PrimaryKey, m.Uniques)
	}
}

func TestForeignKeyRejects(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		c.Table("public", "metrics").Comment = "@foreignKey (value) references nowhere (id)\n" +
			"@foreignKey (value, name) references users (id)\n" +
			"@foreignKey (value) references users (nope)\n" +
			"@foreignKey garbage"
	})
	if fks := cat.Table("public", "metrics").ForeignKeys; len(fks) != 0 {
		t.Fatalf("invalid @foreignKey values must be dropped, got %+v", fks)
	}
}

func TestFilterableSortableSyntheticIndex(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		u := c.Table("public", "users")
		u.Column("full_name").Comment = "@filterable"
		u.Column("created_at").Comment = "@sortable\n@omit filter"
	})
	idx := cat.Table("public", "users").IndexedColumns()
	if !idx["full_name"] || !idx["created_at"] {
		t.Fatalf("synthetic indexes missing: %v", idx)
	}
	found := 0
	for _, ix := range cat.Table("public", "users").Indexes {
		if ix.Name == "@filterable full_name" || ix.Name == "@filterable created_at" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("expected two synthetic indexes, found %d", found)
	}
}

func TestOmitOnConstraints(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		c.Table("public", "posts").ForeignKeys[0].Comment = "@omit"
		c.Table("public", "users").Uniques[0].Comment = "@omit"
		c.Table("public", "comments").ForeignKeys[0].Comment = "@omit many"
	})
	if len(cat.Table("public", "posts").ForeignKeys) != 0 {
		t.Fatal("@omit on an FK must drop it")
	}
	if len(cat.Table("public", "users").Uniques) != 0 {
		t.Fatal("@omit on a unique constraint must drop it")
	}
	if !cat.Table("public", "users").IndexedColumns()["email"] {
		t.Fatal("filterability must persist via the backing index")
	}
	if len(cat.Table("public", "comments").ForeignKeys) != 2 {
		t.Fatal("@omit many is a surface-level tag; the FK stays in the catalog")
	}
}

func TestIndexLookups(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Table("public", "users").Comment = "@name customers\nA customer."
	cat.Table("public", "users").Column("mood").Comment = "@name vibe"
	cat.Enum("public", "mood").Comment = "@name feeling"
	cat.Functions[0].Comment = "@name findPosts" // search_posts
	cat.Table("public", "posts").ForeignKeys[0].Comment = "@fieldName writer"
	cat.Domains = append(cat.Domains, &introspect.Domain{Schema: "public", Name: "email", Comment: "@name address\nAn email."})
	ix := catalog.BuildIndex(cat)

	if ix.RenamedTable("public", "users") != "customers" || ix.RenamedTable("", "users") != "customers" {
		t.Fatal("table @name lookup (exact + schema-less)")
	}
	if ix.RenamedTable("public", "posts") != "posts" {
		t.Fatal("untagged table keeps its name")
	}
	if ix.RenamedColumn("public", "users", "mood") != "vibe" || ix.RenamedColumn("", "users", "mood") != "vibe" {
		t.Fatal("column @name lookup")
	}
	if got := ix.RenamedColumns("public", "users", []string{"id", "mood"}); got[0] != "id" || got[1] != "vibe" {
		t.Fatalf("RenamedColumns = %v", got)
	}
	if ix.Enum("public", "mood").First("name") != "feeling" || ix.Enum("", "mood").First("name") != "feeling" {
		t.Fatal("enum lookup")
	}
	if ix.Domain("public", "email").First("name") != "address" || ix.Domain("", "email").First("name") != "address" || ix.Domain("other", "email") != nil {
		t.Fatal("domain lookup")
	}
	if ix.Function("public", "search_posts").First("name") != "findPosts" || ix.Function("", "search_posts").First("name") != "findPosts" {
		t.Fatal("function lookup")
	}
	if ix.Constraint("public", "posts_author_id_fkey").First("fieldName") != "writer" {
		t.Fatal("constraint lookup")
	}
	if ix.Constraint("public", "") != nil {
		t.Fatal("empty constraint name must be nil")
	}
	if ix.Table("public", "users").First("name") != "customers" {
		t.Fatal("Table tags")
	}
}

func TestIndexAmbiguousNamesAcrossSchemas(t *testing.T) {
	cat := &introspect.Catalog{Tables: []*introspect.Table{
		{Schema: "a", Name: "t", Comment: "@name one"},
		{Schema: "b", Name: "t", Comment: "@name two"},
	}}
	ix := catalog.BuildIndex(cat)
	if ix.RenamedTable("a", "t") != "one" || ix.RenamedTable("b", "t") != "two" {
		t.Fatal("exact lookups must still work")
	}
	if ix.RenamedTable("", "t") != "t" {
		t.Fatal("ambiguous schema-less lookup must resolve to nothing")
	}
}

func TestIndexRebuiltPerTransform(t *testing.T) {
	h := catalog.New()
	first := testutil.FixtureCatalog()
	first.Table("public", "users").Comment = "@name a"
	if err := h.TransformCatalog(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if h.Index().RenamedTable("public", "users") != "a" {
		t.Fatal("first index")
	}
	second := testutil.FixtureCatalog()
	if err := h.TransformCatalog(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if h.Index().RenamedTable("public", "users") != "users" {
		t.Fatal("index must be rebuilt on every TransformCatalog")
	}
}

func TestPolymorphicTagsResolveFunctions(t *testing.T) {
	cat, _ := run(t, func(c *introspect.Catalog) {
		c.Functions = append(c.Functions,
			&introspect.Function{Schema: "public", Name: "bad_tag", Comment: "@anyelement no_such_type",
				Args:       []introspect.FuncArg{{Name: "v", PGType: "anyelement", TypeSchema: "pg_catalog"}},
				ReturnType: "int4", ReturnTypeSchema: "pg_catalog"},
			&introspect.Function{Schema: "public", Name: "wrong_family", Comment: "@anycompatible int",
				Args:       []introspect.FuncArg{{Name: "v", PGType: "anyelement", TypeSchema: "pg_catalog"}},
				ReturnType: "int4", ReturnTypeSchema: "pg_catalog"},
		)
	})
	fns := map[string]*introspect.Function{}
	for _, f := range cat.Functions {
		fns[f.Name] = f
	}
	sig := func(name string) string {
		f := fns[name]
		s := ""
		for _, a := range f.Args {
			s += a.TypeSchema + "." + a.PGType + ","
		}
		return s + "->" + f.ReturnTypeSchema + "." + f.ReturnType
	}
	for name, want := range map[string]string{
		"pick_first":   "pg_catalog.text,pg_catalog.text,->pg_catalog.text",
		"larger_of":    "pg_catalog.numeric,pg_catalog.numeric,->pg_catalog.numeric",
		"range_lower":  "pg_catalog.int4range,->pg_catalog.int4",
		"array_first":  "pg_catalog.anyarray,->pg_catalog.anyelement",
		"bad_tag":      "pg_catalog.anyelement,->pg_catalog.int4",
		"wrong_family": "pg_catalog.anyelement,->pg_catalog.int4",
	} {
		if got := sig(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if fns["pick_first"].Comment != "@anyelement text" {
		t.Error("the comment must survive resolution")
	}
}

func TestPolymorphicNeverResolvesToHiddenTable(t *testing.T) {
	poly := func(name, tag string) *introspect.Function {
		return &introspect.Function{Schema: "public", Name: name, Comment: tag,
			Args:       []introspect.FuncArg{{Name: "v", PGType: "anyelement", TypeSchema: "pg_catalog"}},
			ReturnType: "anyelement", ReturnTypeSchema: "pg_catalog"}
	}
	prep := func(c *introspect.Catalog) {
		c.Functions = append(c.Functions,
			poly("peek_q", "@anyelement public.metrics"),
			poly("peek_u", "@anyelement metrics"),
			poly("peek_arr", "@anyelement public.metrics[]"),
			poly("peek_users", "@anyelement public.users"),
		)
	}
	check := func(t *testing.T, cat *introspect.Catalog, hidden bool) {
		t.Helper()
		for _, f := range cat.Functions {
			switch f.Name {
			case "peek_q", "peek_u", "peek_arr":
				if got := f.ReturnType != "anyelement"; got == hidden {
					t.Errorf("%s: resolved=%v with hidden=%v", f.Name, got, hidden)
				}
			case "peek_users":
				if f.ReturnType != "users" {
					t.Errorf("peek_users must still resolve, got %s", f.ReturnType)
				}
			}
		}
	}

	t.Run("omitted table", func(t *testing.T) {
		cat, _ := run(t, func(c *introspect.Catalog) {
			prep(c)
			c.Table("public", "metrics").Comment = "@omit"
		})
		check(t, cat, true)
	})
	t.Run("contract applied first", func(t *testing.T) {
		cat := testutil.FixtureCatalog()
		prep(cat)
		cat.Table("public", "metrics").Comment = "@behavior -contract:public"
		contract.Contract{Name: "public"}.Apply(cat)
		if err := plugin.NewRegistry(catalog.New()).TransformCatalog(context.Background(), nil, cat); err != nil {
			t.Fatal(err)
		}
		check(t, cat, true)
	})
	t.Run("contract view clone", func(t *testing.T) {
		base := testutil.FixtureCatalog()
		prep(base)
		base.Table("public", "metrics").Comment = "@behavior -contract:public"
		cat, _ := contract.Contract{Name: "public"}.View(base)
		if err := plugin.NewRegistry(catalog.New()).TransformCatalog(context.Background(), nil, cat); err != nil {
			t.Fatal(err)
		}
		check(t, cat, true)
	})
	t.Run("visible table", func(t *testing.T) {
		cat, _ := run(t, prep)
		check(t, cat, false)
	})
}

func TestPolymorphicOnlyResolvesToKnownTypes(t *testing.T) {
	tags := map[string]string{
		"to_builtin":     "@anyelement int4",
		"to_builtin_q":   "@anyelement pg_catalog.text",
		"to_enum":        "@anyelement public.mood",
		"to_composite":   "@anyelement public.address",
		"to_domain":      "@anyelement public.posint",
		"to_other_enum":  "@anyelement private.secret_kind",
		"to_other_table": "@anyelement private.secrets",
		"to_other_arr":   "@anyelement private.secrets[]",
		"to_other_dom":   "@anyelement private.secret_dom",
	}
	cat, _ := run(t, func(c *introspect.Catalog) {
		c.Domains = append(c.Domains, &introspect.Domain{Schema: "public", Name: "posint", BaseType: "int4", BaseTypeSchema: "pg_catalog"})
		for name, tag := range tags {
			c.Functions = append(c.Functions, &introspect.Function{Schema: "public", Name: name, Comment: tag,
				Args:       []introspect.FuncArg{{Name: "v", PGType: "anyelement", TypeSchema: "pg_catalog"}},
				ReturnType: "anyelement", ReturnTypeSchema: "pg_catalog"})
		}
	})
	want := map[string]bool{
		"to_builtin": true, "to_builtin_q": true, "to_enum": true, "to_composite": true, "to_domain": true,
	}
	seen := 0
	for _, f := range cat.Functions {
		if _, ok := tags[f.Name]; !ok {
			continue
		}
		seen++
		if resolved := f.ReturnType != "anyelement"; resolved != want[f.Name] {
			t.Errorf("%s: resolved=%v, want %v", f.Name, resolved, want[f.Name])
		}
	}
	if seen != len(tags) {
		t.Fatalf("saw %d of %d functions", seen, len(tags))
	}
}
