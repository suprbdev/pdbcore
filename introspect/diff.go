package introspect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Diff describes the structural differences between two catalogs as
// human-readable lines, phrased as the change from cached to live
// ("added" = present in live only). Empty means no drift.
func Diff(cached, live *Catalog) []string {
	var out []string
	if cached.ServerVersion != live.ServerVersion {
		out = append(out, fmt.Sprintf("server version changed: %s -> %s", cached.ServerVersion, live.ServerVersion))
	} else if cached.ServerVersionNum != live.ServerVersionNum {
		out = append(out, fmt.Sprintf("server version number changed: %d -> %d", cached.ServerVersionNum, live.ServerVersionNum))
	}
	out = append(out, diffStringSet("schema", cached.Schemas, live.Schemas)...)
	out = append(out, diffTables(cached.Tables, live.Tables)...)
	out = append(out, diffNamed("enum", enumMap(cached.Enums), enumMap(live.Enums))...)
	out = append(out, diffNamed("composite type", compositeMap(cached.Composites), compositeMap(live.Composites))...)
	out = append(out, diffFunctions(cached.Functions, live.Functions)...)
	out = append(out, diffExtensions(cached.Extensions, live.Extensions)...)
	out = append(out, diffNamed("range type", rangeMap(cached.Ranges), rangeMap(live.Ranges))...)
	out = append(out, diffNamed("domain", domainMap(cached.Domains), domainMap(live.Domains))...)
	return out
}

func diffExtensions(cached, live []*Extension) []string {
	var out []string
	c, l := map[string]*Extension{}, map[string]*Extension{}
	for _, e := range cached {
		c[e.Name] = e
	}
	for _, e := range live {
		l[e.Name] = e
	}
	for _, name := range sortedKeys(c) {
		if _, ok := l[name]; !ok {
			out = append(out, fmt.Sprintf("extension %s removed", name))
		}
	}
	for _, name := range sortedKeys(l) {
		le := l[name]
		ce, ok := c[name]
		if !ok {
			out = append(out, fmt.Sprintf("extension %s added (%s in %s)", name, le.Version, le.Schema))
			continue
		}
		if ce.Version != le.Version {
			out = append(out, fmt.Sprintf("extension %s version changed: %s -> %s", name, ce.Version, le.Version))
		}
		if ce.Schema != le.Schema {
			out = append(out, fmt.Sprintf("extension %s schema changed: %s -> %s", name, ce.Schema, le.Schema))
		}
	}
	return out
}

func diffStringSet(kind string, cached, live []string) []string {
	var out []string
	c, l := map[string]bool{}, map[string]bool{}
	for _, s := range cached {
		c[s] = true
	}
	for _, s := range live {
		l[s] = true
	}
	for _, s := range sortedKeys(c) {
		if !l[s] {
			out = append(out, fmt.Sprintf("%s %s removed", kind, s))
		}
	}
	for _, s := range sortedKeys(l) {
		if !c[s] {
			out = append(out, fmt.Sprintf("%s %s added", kind, s))
		}
	}
	return out
}

// diffNamed reports added/removed/changed objects, comparing by canonical
// JSON. The maps are name -> object.
func diffNamed(kind string, cached, live map[string]any) []string {
	var out []string
	for _, name := range sortedKeys(cached) {
		if _, ok := live[name]; !ok {
			out = append(out, fmt.Sprintf("%s %s removed", kind, name))
		}
	}
	for _, name := range sortedKeys(live) {
		cobj, ok := cached[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s %s added", kind, name))
			continue
		}
		if canonJSON(cobj) != canonJSON(live[name]) {
			out = append(out, fmt.Sprintf("%s %s changed", kind, name))
		}
	}
	return out
}

func diffTables(cached, live []*Table) []string {
	var out []string
	c, l := map[string]*Table{}, map[string]*Table{}
	for _, t := range cached {
		c[t.Schema+"."+t.Name] = t
	}
	for _, t := range live {
		l[t.Schema+"."+t.Name] = t
	}
	for _, name := range sortedKeys(c) {
		if _, ok := l[name]; !ok {
			out = append(out, fmt.Sprintf("%s %s removed", c[name].Kind, name))
		}
	}
	for _, name := range sortedKeys(l) {
		lt := l[name]
		ct, ok := c[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s %s added", lt.Kind, name))
			continue
		}
		out = append(out, diffTable(name, ct, lt)...)
	}
	return out
}

func diffTable(name string, cached, live *Table) []string {
	var out []string
	cc, lc := map[string]*Column{}, map[string]*Column{}
	for _, col := range cached.Columns {
		cc[col.Name] = col
	}
	for _, col := range live.Columns {
		lc[col.Name] = col
	}
	for _, cn := range sortedKeys(cc) {
		if _, ok := lc[cn]; !ok {
			out = append(out, fmt.Sprintf("%s %s: column %s removed", cached.Kind, name, cn))
		}
	}
	for _, cn := range sortedKeys(lc) {
		lcol := lc[cn]
		ccol, ok := cc[cn]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s %s: column %s added (%s)", live.Kind, name, cn, typeDesc(lcol)))
		case typeDesc(ccol) != typeDesc(lcol):
			out = append(out, fmt.Sprintf("%s %s: column %s type changed (%s -> %s)", live.Kind, name, cn, typeDesc(ccol), typeDesc(lcol)))
		case ccol.GeneratedKind != lcol.GeneratedKind:
			out = append(out, fmt.Sprintf("%s %s: column %s generation changed (%s -> %s)", live.Kind, name, cn, genDesc(ccol), genDesc(lcol)))
		case canonJSON(ccol) != canonJSON(lcol):
			out = append(out, fmt.Sprintf("%s %s: column %s changed", live.Kind, name, cn))
		}
	}
	// Columns equal but something else on the table drifted (keys, indexes,
	// privileges, RLS, comments): report it coarsely rather than staying silent.
	if len(out) == 0 && canonJSON(cached) != canonJSON(live) {
		out = append(out, fmt.Sprintf("%s %s changed (%s)", live.Kind, name,
			strings.Join(tableAspectChanges(cached, live), ", ")))
	}
	return out
}

func tableAspectChanges(cached, live *Table) []string {
	var parts []string
	aspect := func(label string, a, b any) {
		if canonJSON(a) != canonJSON(b) {
			parts = append(parts, label)
		}
	}
	aspect("kind", cached.Kind, live.Kind)
	aspect("comment", cached.Comment, live.Comment)
	aspect("rls", cached.RLSEnabled, live.RLSEnabled)
	aspect("primary key", cached.PrimaryKey, live.PrimaryKey)
	aspect("unique constraints", cached.Uniques, live.Uniques)
	aspect("foreign keys", cached.ForeignKeys, live.ForeignKeys)
	aspect("check constraints", cached.Checks, live.Checks)
	aspect("indexes", cached.Indexes, live.Indexes)
	aspect("privileges", cached.Privileges, live.Privileges)
	aspect("partitioning", [2]any{cached.Partitioned, cached.PartitionOf}, [2]any{live.Partitioned, live.PartitionOf})
	if len(parts) == 0 {
		parts = append(parts, "definition")
	}
	return parts
}

func typeDesc(c *Column) string {
	var t string
	switch {
	case c.SQLType != "":
		t = c.SQLType
	case c.Spatial != nil && c.IsArray:
		t = strings.TrimPrefix(c.PGType, "_") + c.Spatial.Typmod() + "[]"
	case c.Spatial != nil:
		t = c.PGType + c.Spatial.Typmod()
	default:
		t = c.PGType
		if c.IsArray {
			t += "[]"
		}
	}
	if c.Domain != "" {
		t += " (domain " + c.Domain + ")"
	}
	if c.NotNull {
		t += " not null"
	}
	return t
}

func genDesc(c *Column) string {
	if c.GeneratedKind == "" {
		return "none"
	}
	return string(c.GeneratedKind)
}

func enumMap(enums []*Enum) map[string]any {
	out := map[string]any{}
	for _, e := range enums {
		out[e.Schema+"."+e.Name] = e
	}
	return out
}

func rangeMap(ranges []*RangeType) map[string]any {
	out := map[string]any{}
	for _, r := range ranges {
		out[r.Schema+"."+r.Name] = r
	}
	return out
}

func domainMap(domains []*Domain) map[string]any {
	out := map[string]any{}
	for _, d := range domains {
		out[d.Ref()] = d
	}
	return out
}

func compositeMap(comps []*Composite) map[string]any {
	out := map[string]any{}
	for _, c := range comps {
		out[c.Schema+"."+c.Name] = c
	}
	return out
}

// diffFunctions reports added/removed functions by signature (name and
// input types) and, for changed ones, which aspects changed.
func diffFunctions(cached, live []*Function) []string {
	c, l := functionMap(cached), functionMap(live)
	var out []string
	for _, sig := range sortedKeys(c) {
		if _, ok := l[sig]; !ok {
			out = append(out, fmt.Sprintf("function %s removed", sig))
		}
	}
	for _, sig := range sortedKeys(l) {
		lf := l[sig]
		cf, ok := c[sig]
		if !ok {
			out = append(out, fmt.Sprintf("function %s added", sig))
			continue
		}
		if canonJSON(cf) != canonJSON(lf) {
			out = append(out, fmt.Sprintf("function %s changed (%s)", sig, strings.Join(functionAspectChanges(cf, lf), ", ")))
		}
	}
	return out
}

func functionAspectChanges(cached, live *Function) []string {
	var parts []string
	aspect := func(label string, a, b any) {
		if canonJSON(a) != canonJSON(b) {
			parts = append(parts, label)
		}
	}
	argsWith := func(f *Function, pick func(FuncArg) any) []any {
		out := make([]any, len(f.Args))
		for i, a := range f.Args {
			out[i] = pick(a)
		}
		return out
	}
	aspect("argument names", argsWith(cached, func(a FuncArg) any { return a.Name }), argsWith(live, func(a FuncArg) any { return a.Name }))
	aspect("argument modes", argsWith(cached, func(a FuncArg) any { return a.Mode }), argsWith(live, func(a FuncArg) any { return a.Mode }))
	aspect("argument defaults",
		argsWith(cached, func(a FuncArg) any { return [2]any{a.HasDefault, a.Default} }),
		argsWith(live, func(a FuncArg) any { return [2]any{a.HasDefault, a.Default} }))
	aspect("return type", [4]any{cached.ReturnType, cached.ReturnTypeSchema, cached.ReturnDomain, cached.ReturnsSet},
		[4]any{live.ReturnType, live.ReturnTypeSchema, live.ReturnDomain, live.ReturnsSet})
	aspect("return columns", cached.ReturnColumns, live.ReturnColumns)
	aspect("volatility", cached.Volatility, live.Volatility)
	aspect("comment", cached.Comment, live.Comment)
	if len(parts) == 0 {
		parts = append(parts, "definition")
	}
	return parts
}

func functionMap(funcs []*Function) map[string]*Function {
	out := map[string]*Function{}
	for _, f := range funcs {
		args := make([]string, len(f.Args))
		for i, a := range f.Args {
			args[i] = a.PGType
		}
		out[fmt.Sprintf("%s.%s(%s)", f.Schema, f.Name, strings.Join(args, ", "))] = f
	}
	return out
}

func canonJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("!%v", err)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
