package catalog

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
)

// Finding is a [smarttags.Issue] in the comment of a catalog object.
type Finding struct {
	// Kind is the object's kind: table, view, column, constraint,
	// function, enum or domain.
	Kind string
	// Object is the object's qualified name: schema.table,
	// schema.table.column (also for composite type fields),
	// schema.table.constraint, schema.function, schema.enum or
	// schema.domain.
	Object string
	smarttags.Issue
}

// Args returns the finding as slog key-value pairs: Kind=Object followed
// by the issue's [smarttags.Issue.Attrs].
func (f Finding) Args() []any {
	return append([]any{f.Kind, f.Object}, f.Attrs()...)
}

// Check reports the smart-tag issues (see [smarttags.Tags.Check]) in the
// comments of c's objects against v: tables and views with their columns,
// foreign keys, primary keys and unique constraints, then composite type
// fields, functions, enums and domains. Run it before the [Hook] removes
// omitted objects so their comments are checked too.
func Check(c *introspect.Catalog, v smarttags.Vocabulary) []Finding {
	var out []Finding
	check := func(kind, object, comment string) {
		if !strings.Contains(comment, "@") {
			return
		}
		tags, _ := smarttags.Parse(comment)
		for _, i := range tags.Check(v) {
			out = append(out, Finding{Kind: kind, Object: object, Issue: i})
		}
	}
	for _, t := range c.Tables {
		kind := "table"
		if t.Kind == introspect.RelView || t.Kind == introspect.RelMaterializedView {
			kind = "view"
		}
		key := tableKey(t.Schema, t.Name)
		check(kind, key, t.Comment)
		for _, col := range t.Columns {
			check("column", key+"."+col.Name, col.Comment)
		}
		for _, fk := range t.ForeignKeys {
			check("constraint", key+"."+fk.Name, fk.Comment)
		}
		if t.PrimaryKey != nil {
			check("constraint", key+"."+t.PrimaryKey.Name, t.PrimaryKey.Comment)
		}
		for _, u := range t.Uniques {
			check("constraint", key+"."+u.Name, u.Comment)
		}
	}
	for _, ct := range c.Composites {
		key := tableKey(ct.Schema, ct.Name)
		for _, f := range ct.Fields {
			check("column", key+"."+f.Name, f.Comment)
		}
	}
	for _, f := range c.Functions {
		check("function", tableKey(f.Schema, f.Name), f.Comment)
	}
	for _, e := range c.Enums {
		check("enum", tableKey(e.Schema, e.Name), e.Comment)
	}
	for _, d := range c.Domains {
		check("domain", tableKey(d.Schema, d.Name), d.Comment)
	}
	return out
}
