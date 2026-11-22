package introspect

import (
	"strings"
	"testing"
)

func diffFixture() *Catalog {
	return &Catalog{
		ServerVersion:    "16.0",
		ServerVersionNum: 160000,
		Schemas:          []string{"public"},
		Tables: []*Table{{
			Schema: "public",
			Name:   "users",
			Kind:   RelTable,
			Columns: []*Column{
				{Name: "id", PGType: "int4", NotNull: true},
				{Name: "email", PGType: "text", NotNull: true},
			},
			PrimaryKey: &Constraint{Name: "users_pkey", Columns: []string{"id"}},
			Privileges: Privileges{Select: true},
		}},
		Enums:      []*Enum{{Schema: "public", Name: "mood", Values: []string{"HAPPY", "SAD"}}},
		Functions:  []*Function{{Schema: "public", Name: "f", Args: []FuncArg{{Name: "q", PGType: "text", Mode: ArgModeIn}}, ReturnType: "int4"}},
		Extensions: []*Extension{{Name: "plpgsql", Version: "1.0", Schema: "pg_catalog"}},
	}
}

func TestDiffEqual(t *testing.T) {
	if d := Diff(diffFixture(), diffFixture()); len(d) != 0 {
		t.Fatalf("expected no diff, got %v", d)
	}
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{"table added", func(c *Catalog) {
			c.Tables = append(c.Tables, &Table{Schema: "public", Name: "posts", Kind: RelTable})
		}, "table public.posts added"},
		{"table removed", func(c *Catalog) { c.Tables = nil }, "table public.users removed"},
		{"column added", func(c *Catalog) {
			c.Tables[0].Columns = append(c.Tables[0].Columns, &Column{Name: "bio", PGType: "text"})
		}, "table public.users: column bio added (text)"},
		{"column removed", func(c *Catalog) {
			c.Tables[0].Columns = c.Tables[0].Columns[:1]
		}, "table public.users: column email removed"},
		{"column type changed", func(c *Catalog) {
			c.Tables[0].Columns[1].PGType = "varchar"
		}, "table public.users: column email type changed (text not null -> varchar not null)"},
		{"column attr changed", func(c *Catalog) {
			c.Tables[0].Columns[1].HasDefault = true
		}, "table public.users: column email changed"},
		{"index/key change", func(c *Catalog) {
			c.Tables[0].Uniques = []*Constraint{{Name: "users_email_key", Columns: []string{"email"}}}
		}, "table public.users changed (unique constraints)"},
		{"privilege change", func(c *Catalog) {
			c.Tables[0].Privileges.Insert = true
		}, "table public.users changed (privileges)"},
		{"enum changed", func(c *Catalog) {
			c.Enums[0].Values = append(c.Enums[0].Values, "ANGRY")
		}, "enum public.mood changed"},
		{"function removed", func(c *Catalog) { c.Functions = nil }, "function public.f(text) removed"},
		{"function default added", func(c *Catalog) {
			c.Functions[0].Args[0].HasDefault = true
			c.Functions[0].Args[0].Default = "'x'::text"
		}, "function public.f(text) changed (argument defaults)"},
		{"function made variadic", func(c *Catalog) {
			c.Functions[0].Args[0].Mode = ArgModeVariadic
		}, "function public.f(text) changed (argument modes)"},
		{"function out params", func(c *Catalog) {
			c.Functions[0].ReturnType = "record"
			c.Functions[0].ReturnColumns = []FuncArg{
				{Name: "a", PGType: "int4", Mode: ArgModeOut}, {Name: "b", PGType: "text", Mode: ArgModeOut},
			}
		}, "function public.f(text) changed (return type, return columns)"},
		{"schema added", func(c *Catalog) {
			c.Schemas = append(c.Schemas, "app")
		}, "schema app added"},
		{"server version", func(c *Catalog) {
			c.ServerVersion = "17.0"
		}, "server version changed: 16.0 -> 17.0"},
		{"server version number", func(c *Catalog) {
			c.ServerVersionNum = 160001
		}, "server version number changed: 160000 -> 160001"},
		{"generation kind", func(c *Catalog) {
			c.Tables[0].Columns[1].Generated = true
			c.Tables[0].Columns[1].GeneratedKind = GeneratedVirtual
		}, "table public.users: column email generation changed (none -> virtual)"},
		{"temporal key", func(c *Catalog) {
			c.Tables[0].PrimaryKey.WithoutOverlaps = true
		}, "table public.users changed (primary key)"},
		{"extension added", func(c *Catalog) {
			c.Extensions = append(c.Extensions, &Extension{Name: "postgis", Version: "3.4.3", Schema: "public"})
		}, "extension postgis added (3.4.3 in public)"},
		{"extension removed", func(c *Catalog) { c.Extensions = nil }, "extension plpgsql removed"},
		{"extension upgraded", func(c *Catalog) {
			c.Extensions[0].Version = "1.1"
		}, "extension plpgsql version changed: 1.0 -> 1.1"},
		{"extension moved", func(c *Catalog) {
			c.Extensions[0].Schema = "extensions"
		}, "extension plpgsql schema changed: pg_catalog -> extensions"},
		{"spatial column added", func(c *Catalog) {
			c.Tables[0].Columns = append(c.Tables[0].Columns, &Column{
				Name: "home", PGType: "geometry", Spatial: &SpatialType{Subtype: SpatialPoint, SRID: 4326},
			})
		}, "table public.users: column home added (geometry(Point,4326))"},
		{"typmod added", func(c *Catalog) {
			c.Tables[0].Columns[1].SQLType = "character varying(255)"
		}, "table public.users: column email type changed (text not null -> character varying(255) not null)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := diffFixture()
			tc.mutate(live)
			got := Diff(diffFixture(), live)
			if len(got) == 0 {
				t.Fatalf("expected diff containing %q, got none", tc.want)
			}
			joined := strings.Join(got, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("diff %q does not contain %q", joined, tc.want)
			}
			if len(got) != 1 {
				t.Fatalf("expected exactly one diff line, got %v", got)
			}
		})
	}
}

func TestDiffSpatialTypmod(t *testing.T) {
	withSpatial := func(st *SpatialType, isArray bool) *Catalog {
		c := diffFixture()
		pgType := "geometry"
		if isArray {
			pgType = "_geometry"
		}
		c.Tables[0].Columns = append(c.Tables[0].Columns, &Column{Name: "home", PGType: pgType, IsArray: isArray, Spatial: st})
		return c
	}
	cases := []struct {
		name         string
		cached, live *SpatialType
		isArray      bool
		want         string
	}{
		{"srid", &SpatialType{Subtype: SpatialPoint, SRID: 4326}, &SpatialType{Subtype: SpatialPoint, SRID: 3857}, false,
			"table public.users: column home type changed (geometry(Point,4326) -> geometry(Point,3857))"},
		{"subtype", &SpatialType{Subtype: SpatialPoint}, &SpatialType{Subtype: SpatialPolygon}, false,
			"table public.users: column home type changed (geometry(Point) -> geometry(Polygon))"},
		{"dims", &SpatialType{Subtype: SpatialPoint, SRID: 4326}, &SpatialType{Subtype: SpatialPoint, SRID: 4326, HasZ: true, HasM: true}, false,
			"table public.users: column home type changed (geometry(Point,4326) -> geometry(PointZM,4326))"},
		{"constraint dropped", &SpatialType{Subtype: SpatialPoint, SRID: 4326}, &SpatialType{Subtype: SpatialGeometry}, false,
			"table public.users: column home type changed (geometry(Point,4326) -> geometry)"},
		{"array", &SpatialType{Subtype: SpatialPoint}, &SpatialType{Subtype: SpatialPoint, SRID: 4326}, true,
			"table public.users: column home type changed (geometry(Point)[] -> geometry(Point,4326)[])"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(withSpatial(tc.cached, tc.isArray), withSpatial(tc.live, tc.isArray))
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("Diff = %v, want [%s]", got, tc.want)
			}
		})
	}
	if d := Diff(withSpatial(&SpatialType{Subtype: SpatialPoint}, false), withSpatial(&SpatialType{Subtype: SpatialPoint}, false)); len(d) != 0 {
		t.Fatalf("equal spatial columns diffed: %v", d)
	}
}
