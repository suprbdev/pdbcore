package catalog

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
)

// Name and Priority of the smart-comments plugin (shared by both products'
// wrappers so plugins.disabled works the same everywhere).
const (
	Name = "smart-comments"
	// Priority 50: the CatalogHook shapes the catalog before other plugins
	// read it, and the products' naming halves apply @name before naming
	// plugins (simple-names, 100) format.
	Priority = 50
)

// Hook is the CatalogHook. The tag index is rebuilt on every
// TransformCatalog (watch-mode rebuilds reuse the instance) and is exposed
// through Index for the products' inflection/surface halves.
type Hook struct {
	idx *Index
	log *slog.Logger
}

// New returns a Hook logging to slog.Default().
func New() *Hook { return &Hook{idx: BuildIndex(&introspect.Catalog{}), log: slog.Default()} }

func (h *Hook) Name() string  { return Name }
func (h *Hook) Priority() int { return Priority }

// SetLogger replaces the warning logger.
func (h *Hook) SetLogger(l *slog.Logger) {
	if l != nil {
		h.log = l
	}
}

// Index returns the tag index built by the last TransformCatalog.
func (h *Hook) Index() *Index { return h.idx }

// TransformCatalog applies every tag with a catalog-level representation.
func (h *Hook) TransformCatalog(_ context.Context, c *introspect.Catalog) error {
	h.idx = BuildIndex(c)

	// Filter fully-omitted tables first, then apply the remaining tags:
	// @foreignKey validation must not see tables that are about to vanish.
	tables := make([]*introspect.Table, 0, len(c.Tables))
	for _, t := range c.Tables {
		if _, everything := h.idx.Tables[tableKey(t.Schema, t.Name)].Omits(); !everything {
			tables = append(tables, t)
		}
	}
	c.Tables = tables
	for _, t := range c.Tables {
		h.applyTableCatalogTags(c, t, h.idx.Tables[tableKey(t.Schema, t.Name)])
	}

	functions := c.Functions[:0]
	for _, f := range c.Functions {
		tags := h.idx.Functions[f.Schema+"."+f.Name]
		if _, everything := tags.Omits(); everything {
			continue
		}
		functions = append(functions, f)
	}
	c.Functions = functions
	return nil
}

// applyTableCatalogTags applies the catalog-level tags of one table: mutation
// gating, logical keys/relations, column nullability and filterability, and
// omitted constraints.
func (h *Hook) applyTableCatalogTags(c *introspect.Catalog, t *introspect.Table, tags smarttags.Tags) {
	omits, _ := tags.Omits()
	if omits["create"] {
		t.Privileges.Insert = false
	}
	if omits["update"] {
		t.Privileges.Update = false
	}
	if omits["delete"] {
		t.Privileges.Delete = false
	}

	if v := tags.First("primaryKey"); v != "" {
		cols := smarttags.SplitList(v)
		switch {
		case t.PrimaryKey != nil:
			h.log.Warn("smart-comments: @primaryKey ignored, table already has one",
				"table", tableKey(t.Schema, t.Name))
		case !columnsExist(t, cols):
			h.log.Warn("smart-comments: @primaryKey references unknown column",
				"table", tableKey(t.Schema, t.Name), "value", v)
		default:
			t.PrimaryKey = &introspect.Constraint{Name: "@primaryKey", Columns: cols}
		}
	}
	for _, v := range tags.All("unique") {
		cols := smarttags.SplitList(v)
		if len(cols) == 0 || !columnsExist(t, cols) {
			h.log.Warn("smart-comments: @unique references unknown column",
				"table", tableKey(t.Schema, t.Name), "value", v)
			continue
		}
		t.Uniques = append(t.Uniques, &introspect.Constraint{Name: "@unique " + v, Columns: cols})
	}
	for _, v := range tags.All("foreignKey") {
		fk := h.parseForeignKey(c, t, v)
		if fk != nil {
			t.ForeignKeys = append(t.ForeignKeys, fk)
		}
	}

	for _, col := range t.Columns {
		ctags := h.idx.Columns[tableKey(t.Schema, t.Name)+"."+col.Name]
		if ctags == nil {
			continue
		}
		if ctags.Has("notNull") {
			col.NotNull = true
		}
		if ctags.Has("nullable") {
			col.NotNull = false
		}
		// @filterable / @sortable bypass filters.indexed_only via a synthetic
		// index (the builder's allow-set is "leading column of any index").
		// The surface half does not prune the other half: both tags admit
		// the column to filtering and ordering; combine with @omit
		// filter/order.
		if ctags.Has("filterable") || ctags.Has("sortable") {
			t.Indexes = append(t.Indexes, &introspect.Index{
				Name: "@filterable " + col.Name, Columns: []string{col.Name}, Method: "btree",
			})
		}
	}

	uniques := t.Uniques[:0]
	for _, u := range t.Uniques {
		if _, everything := h.idx.Constraints[t.Schema+"."+u.Name].Omits(); everything {
			continue // drop the lookup; filterability persists via the backing index
		}
		uniques = append(uniques, u)
	}
	t.Uniques = uniques

	fks := t.ForeignKeys[:0]
	for _, fk := range t.ForeignKeys {
		if _, everything := h.idx.Constraints[t.Schema+"."+fk.Name].Omits(); everything {
			continue // both relation directions and relation filters disappear
		}
		fks = append(fks, fk)
	}
	t.ForeignKeys = fks
}

// fkRe matches "@foreignKey (a, b) references [schema.]table (x, y)".
var fkRe = regexp.MustCompile(`(?i)^\(([^)]+)\)\s+references\s+(?:([\w$]+)\.)?([\w$]+)\s*\(([^)]+)\)$`)

// parseForeignKey parses a @foreignKey value into a logical ForeignKey.
// Trailing "|@fieldName x|@foreignFieldName y" segments name the generated
// relation fields (the synthetic constraint cannot carry its own comment);
// they are recorded in the Index under the synthetic constraint name.
func (h *Hook) parseForeignKey(c *introspect.Catalog, t *introspect.Table, v string) *introspect.ForeignKey {
	warn := func(reason string) *introspect.ForeignKey {
		h.log.Warn("smart-comments: @foreignKey ignored: "+reason,
			"table", tableKey(t.Schema, t.Name), "value", v)
		return nil
	}
	spec, rest, _ := strings.Cut(v, "|")
	m := fkRe.FindStringSubmatch(strings.TrimSpace(spec))
	if m == nil {
		return warn("want (cols) references [schema.]table (cols)")
	}
	cols, refSchema, refTable, refCols := smarttags.SplitList(m[1]), m[2], m[3], smarttags.SplitList(m[4])
	if refSchema == "" {
		refSchema = t.Schema
	}
	ref := c.Table(refSchema, refTable)
	switch {
	case len(cols) == 0 || len(cols) != len(refCols):
		return warn("column lists must be non-empty and the same length")
	case !columnsExist(t, cols):
		return warn("unknown local column")
	case ref == nil:
		return warn("unknown referenced table")
	case !columnsExist(ref, refCols):
		return warn("unknown referenced column")
	}
	name := "@foreignKey " + spec
	fk := &introspect.ForeignKey{
		Name: name, Columns: cols,
		RefSchema: refSchema, RefTable: refTable, RefColumns: refCols,
	}
	if rest != "" {
		tags := smarttags.Tags{}
		for _, part := range strings.Split(rest, "|") {
			if part = strings.TrimPrefix(strings.TrimSpace(part), "@"); part != "" {
				n, val, _ := strings.Cut(part, " ")
				tags[n] = append(tags[n], strings.TrimSpace(val))
			}
		}
		h.idx.Constraints[t.Schema+"."+name] = tags
	}
	return fk
}

func columnsExist(t *introspect.Table, cols []string) bool {
	for _, c := range cols {
		if t.Column(c) == nil {
			return false
		}
	}
	return true
}
