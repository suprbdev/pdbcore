// Package catalog is the catalog half of the smart-comments plugin: it
// indexes PostGraphile-style tags parsed from database COMMENTs and applies
// the ones with a structural representation in the introspect.Catalog —
// @omit on whole objects, @omit create/update/delete → privilege flags,
// @primaryKey / @unique / @foreignKey synthesis, @notNull / @nullable, and
// @filterable / @sortable as a synthetic index. Products embed the Hook and
// add their naming (InflectionHook) and surface (schema/API hook) halves on
// top of the same Index.
package catalog

import (
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
)

// Index holds the parsed tags of every commented catalog object. The *By
// maps are schema-less fallbacks for call sites that don't carry the pg
// schema; an ambiguous name (same table name in two schemas) maps to nil.
// The Constraints map is also written by the Hook when a @foreignKey value
// carries |@fieldName / |@foreignFieldName segments.
type Index struct {
	Tables      map[string]smarttags.Tags // "schema.table"
	TablesBy    map[string]smarttags.Tags // "table"
	Columns     map[string]smarttags.Tags // "schema.table.column"
	ColumnsBy   map[string]smarttags.Tags // "table.column"
	Enums       map[string]smarttags.Tags // "schema.enum"
	EnumsBy     map[string]smarttags.Tags // "enum"
	Functions   map[string]smarttags.Tags // "schema.function"
	FunctionsBy map[string]smarttags.Tags // "function"
	Constraints map[string]smarttags.Tags // "schema.constraint"
}

// BuildIndex parses and indexes the smart tags of every commented object.
func BuildIndex(c *introspect.Catalog) *Index {
	ix := &Index{
		Tables:      map[string]smarttags.Tags{},
		TablesBy:    map[string]smarttags.Tags{},
		Columns:     map[string]smarttags.Tags{},
		ColumnsBy:   map[string]smarttags.Tags{},
		Enums:       map[string]smarttags.Tags{},
		EnumsBy:     map[string]smarttags.Tags{},
		Functions:   map[string]smarttags.Tags{},
		FunctionsBy: map[string]smarttags.Tags{},
		Constraints: map[string]smarttags.Tags{},
	}
	index := func(exact, by map[string]smarttags.Tags, exactKey, byKey, comment string) {
		tags, _ := smarttags.Parse(comment)
		if tags == nil {
			return
		}
		exact[exactKey] = tags
		if _, dup := by[byKey]; dup {
			by[byKey] = nil // ambiguous across schemas: exact lookups only
		} else {
			by[byKey] = tags
		}
	}
	for _, t := range c.Tables {
		index(ix.Tables, ix.TablesBy, tableKey(t.Schema, t.Name), t.Name, t.Comment)
		for _, col := range t.Columns {
			index(ix.Columns, ix.ColumnsBy,
				tableKey(t.Schema, t.Name)+"."+col.Name, t.Name+"."+col.Name, col.Comment)
		}
		for _, fk := range t.ForeignKeys {
			if tags, _ := smarttags.Parse(fk.Comment); tags != nil {
				ix.Constraints[t.Schema+"."+fk.Name] = tags
			}
		}
		for _, u := range t.Uniques {
			if tags, _ := smarttags.Parse(u.Comment); tags != nil {
				ix.Constraints[t.Schema+"."+u.Name] = tags
			}
		}
	}
	for _, e := range c.Enums {
		index(ix.Enums, ix.EnumsBy, e.Schema+"."+e.Name, e.Name, e.Comment)
	}
	for _, f := range c.Functions {
		index(ix.Functions, ix.FunctionsBy, f.Schema+"."+f.Name, f.Name, f.Comment)
	}
	return ix
}

func lookup(exact, by map[string]smarttags.Tags, schema, name string) smarttags.Tags {
	if schema != "" {
		return exact[schema+"."+name]
	}
	return by[name]
}

// Table resolves tags by "schema.table", falling back to the schema-less
// index when the call site did not carry the pg schema.
func (ix *Index) Table(schema, table string) smarttags.Tags {
	return lookup(ix.Tables, ix.TablesBy, schema, table)
}

// Column resolves a column's tags (schema-less fallback as Table).
func (ix *Index) Column(schema, table, column string) smarttags.Tags {
	if schema != "" {
		return ix.Columns[schema+"."+table+"."+column]
	}
	return ix.ColumnsBy[table+"."+column]
}

// Enum resolves an enum type's tags (schema-less fallback as Table).
func (ix *Index) Enum(schema, name string) smarttags.Tags {
	return lookup(ix.Enums, ix.EnumsBy, schema, name)
}

// Function resolves a function's tags (schema-less fallback as Table).
func (ix *Index) Function(schema, name string) smarttags.Tags {
	return lookup(ix.Functions, ix.FunctionsBy, schema, name)
}

// Constraint resolves a constraint's tags; constraints are always
// schema-qualified. Empty name → nil.
func (ix *Index) Constraint(schema, constraint string) smarttags.Tags {
	if constraint == "" {
		return nil
	}
	return ix.Constraints[schema+"."+constraint]
}

// RenamedTable returns the @name of a table, or the table itself.
func (ix *Index) RenamedTable(schema, table string) string {
	if name := ix.Table(schema, table).First("name"); name != "" {
		return name
	}
	return table
}

// RenamedColumn returns the @name of a column, or the column itself.
func (ix *Index) RenamedColumn(schema, table, column string) string {
	if name := ix.Column(schema, table, column).First("name"); name != "" {
		return name
	}
	return column
}

// RenamedColumns maps RenamedColumn over cols.
func (ix *Index) RenamedColumns(schema, table string, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = ix.RenamedColumn(schema, table, c)
	}
	return out
}

func tableKey(schema, name string) string { return schema + "." + name }
