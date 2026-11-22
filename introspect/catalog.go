// Package introspect reads pg_catalog and produces a Catalog — the single
// serializable model that drives API building, caching, and plugins.
package introspect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// CatalogFormatVersion is bumped whenever the Catalog wire format changes
// incompatibly; the cache layer refuses to load mismatched versions.
const CatalogFormatVersion = 6

// Catalog is the complete introspected model of the database. It is the only
// input to API building, which keeps caching trivial and plugin transforms
// deterministic.
type Catalog struct {
	FormatVersion int    `json:"format_version"`
	ServerVersion string `json:"server_version"`
	// ServerVersionNum is current_setting('server_version_num'), e.g.
	// 180001 for 18.1. Gate version-dependent SQL on it rather than parsing
	// ServerVersion.
	ServerVersionNum int          `json:"server_version_num"`
	Schemas          []string     `json:"schemas"`
	Tables           []*Table     `json:"tables"`
	Enums            []*Enum      `json:"enums"`
	Composites       []*Composite `json:"composites"`
	Functions        []*Function  `json:"functions"`
	// Extensions lists every extension installed in the database
	// (pg_extension), regardless of which schemas were introspected.
	Extensions []*Extension `json:"extensions"`
}

// Extension is an installed extension as recorded in pg_extension.
type Extension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Schema is where the extension's objects live; qualify calls to its
	// functions with it when it may not be on the search_path.
	Schema string `json:"schema"`
}

// RelKind distinguishes tables from the view family.
type RelKind string

const (
	RelTable            RelKind = "table"
	RelView             RelKind = "view"
	RelMaterializedView RelKind = "matview"
)

type Table struct {
	Schema     string    `json:"schema"`
	Name       string    `json:"name"`
	Kind       RelKind   `json:"kind"`
	Comment    string    `json:"comment,omitempty"`
	RLSEnabled bool      `json:"rls_enabled"`
	Columns    []*Column `json:"columns"`
	// Partitioned marks a partitioned table (relkind 'p'): it holds no rows
	// itself, routes inserts to its partitions, and reads, updates and
	// deletes span all of them. Expose it like any other table.
	Partitioned bool `json:"partitioned,omitempty"`
	// PartitionOf is the parent ("schema.name") of a partition
	// (pg_class.relispartition), empty for every other relation. A
	// partition's rows are also reachable through its parent, so consumers
	// should leave partitions out of the API by default (see
	// [Table.IsPartition]). The primary key, unique constraints, foreign keys
	// and indexes a partition inherits from its parent are recorded on the
	// parent only; a partition lists just the ones declared on it directly.
	PartitionOf string `json:"partition_of,omitempty"`
	// PrimaryKey and Uniques may be temporal (WITHOUT OVERLAPS) keys; use
	// RowKeys for single-row lookups and ON CONFLICT targets.
	PrimaryKey  *Constraint   `json:"primary_key,omitempty"`
	Uniques     []*Constraint `json:"uniques,omitempty"`
	ForeignKeys []*ForeignKey `json:"foreign_keys,omitempty"`
	Indexes     []*Index      `json:"indexes,omitempty"`
	Privileges  Privileges    `json:"privileges"`
}

// IsPartition reports whether t is a partition of another table.
func (t *Table) IsPartition() bool { return t.PartitionOf != "" }

// Insertable reports whether generated mutations may INSERT into this relation.
func (t *Table) Insertable() bool { return t.Kind == RelTable && t.Privileges.Insert }

// Updatable reports whether generated mutations may UPDATE this relation.
func (t *Table) Updatable() bool { return t.Kind == RelTable && t.Privileges.Update }

// Deletable reports whether generated mutations may DELETE from this relation.
func (t *Table) Deletable() bool { return t.Kind == RelTable && t.Privileges.Delete }

// Column returns the column with the given name, or nil.
func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// RowKeys returns the primary key (first, when present) and unique
// constraints that identify a single row by equality on all their columns
// and can serve as ON CONFLICT targets. Temporal (WITHOUT OVERLAPS) keys are
// left out: see [Constraint.WithoutOverlaps].
func (t *Table) RowKeys() []*Constraint {
	var out []*Constraint
	if t.PrimaryKey.RowKey() {
		out = append(out, t.PrimaryKey)
	}
	for _, u := range t.Uniques {
		if u.RowKey() {
			out = append(out, u)
		}
	}
	return out
}

// IndexedColumns returns the set of column names that are the leading column
// of at least one index (the filterable set under the indexed-only policy).
func (t *Table) IndexedColumns() map[string]bool {
	out := map[string]bool{}
	for _, ix := range t.Indexes {
		if len(ix.Columns) > 0 {
			out[ix.Columns[0]] = true
		}
	}
	if t.PrimaryKey != nil && len(t.PrimaryKey.Columns) > 0 {
		out[t.PrimaryKey.Columns[0]] = true
	}
	for _, u := range t.Uniques {
		if len(u.Columns) > 0 {
			out[u.Columns[0]] = true
		}
	}
	return out
}

type Column struct {
	Name       string `json:"name"`
	Position   int    `json:"position"`
	PGType     string `json:"pg_type"`     // canonical type name, e.g. int4, text, _text (array)
	TypeSchema string `json:"type_schema"` // pg schema of the type (for enums/domains/composites)
	IsArray    bool   `json:"is_array"`
	NotNull    bool   `json:"not_null"`
	HasDefault bool   `json:"has_default"`
	Generated  bool   `json:"generated"` // identity or generated column: excluded from create/update input
	// GeneratedKind says what kind of Generated column this is; empty when
	// Generated is false.
	GeneratedKind GeneratedKind `json:"generated_kind,omitempty"`
	Comment       string        `json:"comment,omitempty"`
	// EnumRef binds the column to a table-backed enum ("schema.name" of the
	// @enum table it referenced via FK). The column keeps its real pg type
	// (text); schema building and compilation treat it as that enum.
	EnumRef string `json:"enum_ref,omitempty"`
	// Spatial holds the PostGIS type modifier of a geometry or geography
	// column (or array of either); nil for every other type.
	Spatial *SpatialType `json:"spatial,omitempty"`
	// SQLType is the declared type including its modifier as format_type
	// renders it, e.g. "character varying(255)", "numeric(10,2)",
	// "timestamp(3) with time zone". It is set only for built-in
	// (pg_catalog) types declared with a modifier, so it never depends on
	// the session's search_path; empty otherwise.
	SQLType string `json:"sql_type,omitempty"`
}

// GeneratedKind distinguishes identity columns from stored and virtual
// generated columns.
type GeneratedKind string

const (
	// GeneratedIdentity is a GENERATED { ALWAYS | BY DEFAULT } AS IDENTITY
	// column.
	GeneratedIdentity GeneratedKind = "identity"
	// GeneratedStored is a GENERATED ALWAYS AS (expr) STORED column.
	GeneratedStored GeneratedKind = "stored"
	// GeneratedVirtual is a GENERATED ALWAYS AS (expr) VIRTUAL column
	// (PostgreSQL 18+, the default kind there): computed on read and never
	// stored, so it cannot be indexed or be part of a primary key, unique or
	// foreign key constraint.
	GeneratedVirtual GeneratedKind = "virtual"
)

// Virtual reports whether c is a virtual generated column.
func (c *Column) Virtual() bool { return c.GeneratedKind == GeneratedVirtual }

type Constraint struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Comment string   `json:"comment,omitempty"`
	// WithoutOverlaps marks a temporal key (PostgreSQL 18+):
	// PRIMARY KEY or UNIQUE (..., p WITHOUT OVERLAPS). Its last column is a
	// range or multirange compared by overlap instead of equality, so the
	// leading columns repeat across rows, once per non-overlapping period.
	// PostgreSQL enforces it as an exclusion constraint: a duplicate raises
	// 23P01 rather than 23505, ON CONFLICT (cols) cannot infer it, and
	// ON CONFLICT ON CONSTRAINT supports only DO NOTHING. Empty periods are
	// rejected, so equality on all Columns still matches at most one row,
	// but the meaningful lookup is EqualityColumns plus PeriodColumn
	// containing (@>) a point or range.
	WithoutOverlaps bool `json:"without_overlaps,omitempty"`
}

// RowKey reports whether c is a plain primary key or unique constraint that
// rows can be looked up and upserted by: false for nil and for
// WithoutOverlaps keys.
func (c *Constraint) RowKey() bool { return c != nil && !c.WithoutOverlaps }

// EqualityColumns returns the columns compared by equality: all Columns, or
// all but the last for a WithoutOverlaps key.
func (c *Constraint) EqualityColumns() []string {
	if c.WithoutOverlaps && len(c.Columns) > 0 {
		return c.Columns[:len(c.Columns)-1]
	}
	return c.Columns
}

// PeriodColumn returns the WITHOUT OVERLAPS column, or "" for a plain key.
func (c *Constraint) PeriodColumn() string {
	if c.WithoutOverlaps && len(c.Columns) > 0 {
		return c.Columns[len(c.Columns)-1]
	}
	return ""
}

type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"ref_schema"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	Comment    string   `json:"comment,omitempty"`
	// Period marks a temporal foreign key (PostgreSQL 18+):
	// FOREIGN KEY (..., PERIOD p) REFERENCES t (..., PERIOD rp), always
	// referencing a WithoutOverlaps key. The last Columns/RefColumns pair
	// holds ranges or multiranges, and PostgreSQL requires each child's
	// period to be covered by the union of the periods of the referenced rows
	// that match the other columns. A child therefore relates to every
	// referenced row whose period overlaps its own, possibly several, and a
	// referenced row to every child overlapping it: the relation is to-many
	// in both directions. Join with JoinPairs.
	Period bool `json:"period,omitempty"`
	// NotEnforced marks a NOT ENFORCED foreign key (PostgreSQL 18+).
	// PostgreSQL never checks it, so a child may reference no row even when
	// its Columns are all non-null; it still names the intended relation.
	NotEnforced bool `json:"not_enforced,omitempty"`
}

// JoinOp is the operator relating a foreign key column to its referenced
// column in a join predicate.
type JoinOp string

const (
	// JoinEqual compares the pair by equality (=).
	JoinEqual JoinOp = "="
	// JoinOverlap compares a period pair by range overlap (&&).
	JoinOverlap JoinOp = "&&"
)

// JoinPair is one conjunct of a foreign key join predicate:
// child.Column Op parent.RefColumn.
type JoinPair struct {
	Column    string
	RefColumn string
	Op        JoinOp
}

// JoinPairs returns the conjuncts (ANDed) that join child rows to the rows
// they reference, in either direction: JoinEqual for every pair except the
// last pair of a Period key, which is JoinOverlap.
func (fk *ForeignKey) JoinPairs() []JoinPair {
	out := make([]JoinPair, len(fk.Columns))
	for i, col := range fk.Columns {
		out[i] = JoinPair{Column: col, RefColumn: fk.RefColumns[i], Op: JoinEqual}
	}
	if fk.Period && len(out) > 0 {
		out[len(out)-1].Op = JoinOverlap
	}
	return out
}

// ReferencesOne reports whether each child row references at most one row:
// false for Period keys.
func (fk *ForeignKey) ReferencesOne() bool { return !fk.Period }

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"` // expression indexes contribute no columns
	Unique  bool     `json:"unique"`
	Method  string   `json:"method"` // btree, gin, gist, ...
}

type Privileges struct {
	Select bool `json:"select"`
	Insert bool `json:"insert"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

type Enum struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Values  []string `json:"values"`
	Comment string   `json:"comment,omitempty"`
	// ValueDescriptions holds per-value descriptions parallel to Values
	// (table-backed enums; native pg enums have no per-value comments).
	ValueDescriptions []string `json:"value_descriptions,omitempty"`
	// TableBacked marks an enum synthesized from an @enum table's rows. No pg
	// type of this name exists, so SQL must never cast to it; the underlying
	// values live in plain text columns.
	TableBacked bool `json:"table_backed,omitempty"`
}

type Composite struct {
	Schema string    `json:"schema"`
	Name   string    `json:"name"`
	Fields []*Column `json:"fields"`
}

// Volatility mirrors pg_proc.provolatile.
type Volatility string

const (
	VolatilityImmutable Volatility = "immutable"
	VolatilityStable    Volatility = "stable"
	VolatilityVolatile  Volatility = "volatile"
)

type Function struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// Args are the input parameters (IN, INOUT and VARIADIC) in declaration
	// order, which is also the function's identity for overloading. All are
	// named: functions with an unnamed input parameter are not introspected.
	Args []FuncArg `json:"args"`
	// ReturnColumns are the output parameters (OUT, INOUT and TABLE) in
	// declaration order; empty when there are none. With two or more,
	// ReturnType is "record" and each result row has exactly these columns.
	// With one, PostgreSQL returns that column's type directly (ReturnType
	// is that type, and the column name only labels SELECT * FROM f()).
	ReturnColumns    []FuncArg  `json:"return_columns,omitempty"`
	ReturnType       string     `json:"return_type"`
	ReturnTypeSchema string     `json:"return_type_schema"`
	ReturnsSet       bool       `json:"returns_set"`
	Volatility       Volatility `json:"volatility"`
	Comment          string     `json:"comment,omitempty"`
}

// ArgMode mirrors pg_proc.proargmodes.
type ArgMode string

const (
	// ArgModeIn is an ordinary input parameter. An empty Mode means the same.
	ArgModeIn ArgMode = "in"
	// ArgModeOut is an OUT parameter: a result column, never passed.
	ArgModeOut ArgMode = "out"
	// ArgModeInOut is an INOUT parameter: passed in and returned as a column.
	ArgModeInOut ArgMode = "inout"
	// ArgModeVariadic is a VARIADIC parameter. Its PGType is the array type
	// (e.g. _int4); an array value must be passed with the VARIADIC keyword,
	// and named notation cannot leave out any parameter of a variadic
	// function (see [Function.CallSQL]).
	ArgModeVariadic ArgMode = "variadic"
	// ArgModeTable is a column of RETURNS TABLE (...).
	ArgModeTable ArgMode = "table"
)

type FuncArg struct {
	// Name is the parameter name. An unnamed output parameter is given the
	// name PostgreSQL gives its result column: "column" followed by its
	// 1-based position among the output parameters.
	Name       string  `json:"name"`
	PGType     string  `json:"pg_type"`
	TypeSchema string  `json:"type_schema"`
	Mode       ArgMode `json:"mode,omitempty"`
	// HasDefault marks an input parameter declared with a DEFAULT, which a
	// call may omit (see [Function.CallSQL]). PostgreSQL only allows
	// defaults on trailing input parameters (pronargdefaults counts them),
	// but named notation can omit any of them.
	HasDefault bool `json:"has_default,omitempty"`
	// Default is the DEFAULT expression as pg_get_function_arg_default
	// renders it, for documentation only: it is never needed to call the
	// function, which evaluates its own defaults.
	Default string `json:"default,omitempty"`
}

// Variadic reports whether a is the VARIADIC parameter.
func (a FuncArg) Variadic() bool { return a.Mode == ArgModeVariadic }

// Variadic returns the function's VARIADIC parameter (always the last of
// Args), or nil.
func (f *Function) Variadic() *FuncArg {
	if n := len(f.Args); n > 0 && f.Args[n-1].Variadic() {
		return &f.Args[n-1]
	}
	return nil
}

// Table returns the table with the given schema+name, or nil.
func (c *Catalog) Table(schema, name string) *Table {
	for _, t := range c.Tables {
		if t.Schema == schema && t.Name == name {
			return t
		}
	}
	return nil
}

// Enum returns the enum with the given schema+name, or nil.
func (c *Catalog) Enum(schema, name string) *Enum {
	for _, e := range c.Enums {
		if e.Schema == schema && e.Name == name {
			return e
		}
	}
	return nil
}

// EnumByRef returns the table-backed enum a Column.EnumRef points at, or nil.
func (c *Catalog) EnumByRef(ref string) *Enum {
	if ref == "" {
		return nil
	}
	for _, e := range c.Enums {
		if e.TableBacked && e.Schema+"."+e.Name == ref {
			return e
		}
	}
	return nil
}

// Extension returns the installed extension with the given name, or nil.
func (c *Catalog) Extension(name string) *Extension {
	for _, e := range c.Extensions {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// HasExtension reports whether the named extension is installed.
func (c *Catalog) HasExtension(name string) bool { return c.Extension(name) != nil }

// Composite returns the composite type with the given schema+name, or nil.
func (c *Catalog) Composite(schema, name string) *Composite {
	for _, ct := range c.Composites {
		if ct.Schema == schema && ct.Name == name {
			return ct
		}
	}
	return nil
}

// Hash returns a stable content hash of the catalog, used by the schema
// cache and the watch-mode poll fallback to detect drift.
func (c *Catalog) Hash() (string, error) {
	c.sortStable()
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (c *Catalog) sortStable() {
	sort.Strings(c.Schemas)
	sort.Slice(c.Tables, func(i, j int) bool {
		if c.Tables[i].Schema != c.Tables[j].Schema {
			return c.Tables[i].Schema < c.Tables[j].Schema
		}
		return c.Tables[i].Name < c.Tables[j].Name
	})
	sort.Slice(c.Enums, func(i, j int) bool {
		if c.Enums[i].Schema != c.Enums[j].Schema {
			return c.Enums[i].Schema < c.Enums[j].Schema
		}
		return c.Enums[i].Name < c.Enums[j].Name
	})
	sort.Slice(c.Composites, func(i, j int) bool {
		if c.Composites[i].Schema != c.Composites[j].Schema {
			return c.Composites[i].Schema < c.Composites[j].Schema
		}
		return c.Composites[i].Name < c.Composites[j].Name
	})
	sort.Slice(c.Functions, func(i, j int) bool {
		if c.Functions[i].Schema != c.Functions[j].Schema {
			return c.Functions[i].Schema < c.Functions[j].Schema
		}
		return c.Functions[i].Name < c.Functions[j].Name
	})
	sort.Slice(c.Extensions, func(i, j int) bool { return c.Extensions[i].Name < c.Extensions[j].Name })
}
