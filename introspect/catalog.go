// Package introspect reads pg_catalog and produces a Catalog — the single
// serializable model that drives API building, caching, and plugins.
package introspect

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// CatalogFormatVersion is bumped whenever the Catalog wire format changes:
// a field added, removed, renamed or retyped, or a field's values changing
// meaning. The cache layer refuses to load mismatched versions, and a
// golden test of the field set (testdata/catalog_format.golden) fails until
// it is bumped.
const CatalogFormatVersion = 10

// Catalog is the complete introspected model of the database. It is the only
// input to API building, which keeps caching trivial and plugin transforms
// deterministic.
type Catalog struct {
	FormatVersion int `json:"format_version"`
	// ServerVersion is current_setting('server_version'), e.g. "18.1". It is
	// informational: [Catalog.Hash] leaves it out.
	ServerVersion string `json:"server_version"`
	// ServerVersionNum is current_setting('server_version_num'), e.g.
	// 180001 for 18.1. Gate version-dependent SQL on it rather than parsing
	// ServerVersion. [Catalog.Hash] covers its major part only.
	ServerVersionNum int          `json:"server_version_num"`
	Schemas          []string     `json:"schemas"`
	Tables           []*Table     `json:"tables"`
	Enums            []*Enum      `json:"enums"`
	Composites       []*Composite `json:"composites"`
	// [Introspect] orders Functions by schema, then name, then input parameters
	// (Args): fewer parameters first, then parameter by parameter by type
	// schema and type name. PostgreSQL identifies a function by exactly
	// these, so the order is total and the same on every run and every
	// database, whatever the functions' oids: of several overloads of one
	// name, the first is the one with the fewest parameters, and a
	// consumer that keeps the first of colliding functions always keeps
	// the same one.
	Functions []*Function `json:"functions"`
	// Extensions lists every extension installed in the database
	// (pg_extension), regardless of which schemas were introspected.
	Extensions []*Extension `json:"extensions"`
	// Ranges lists every range type outside pg_catalog (CREATE TYPE ...
	// AS RANGE, including those extensions install), regardless of which
	// schemas were introspected, so a column whose range type lives
	// elsewhere still resolves. The built-in range types are not listed.
	Ranges []*RangeType `json:"ranges,omitempty"`
	// Domains lists the domains (CREATE DOMAIN) defined in the introspected
	// schemas plus every domain a recorded column, composite attribute or
	// function parameter or result uses, wherever it is defined, and the
	// parent domains of all of these: every Domain and Parent ref in the
	// catalog resolves through [Catalog.DomainByRef].
	Domains []*Domain `json:"domains,omitempty"`

	idx catalogIndexes

	hiddenRelations map[string]bool
}

// Domain is a domain type as recorded in pg_type (typtype 'd').
type Domain struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// BaseType is the first non-domain type of the domain's chain as
	// Column.PGType spells a type, e.g. "text" or "_int4" for a domain over
	// int[], and BaseTypeSchema its schema.
	BaseType       string `json:"base_type"`
	BaseTypeSchema string `json:"base_type_schema"`
	// IsArrayBase reports that BaseType is an array type: the domain itself
	// holds arrays, as opposed to a column declared as an array of the
	// domain (see [Column.Domain]).
	IsArrayBase bool `json:"is_array_base,omitempty"`
	// Parent is the "schema.name" ref of the domain this one is declared
	// over (CREATE DOMAIN d AS other_domain), empty when its base is not a
	// domain. The parent's NOT NULL and CHECK constraints apply to this
	// domain's values too; see [Catalog.DomainChain].
	Parent string `json:"parent,omitempty"`
	// NotNull is the domain's own NOT NULL constraint.
	NotNull bool `json:"not_null,omitempty"`
	// Checks are the domain's own CHECK constraints as pg_get_constraintdef
	// renders them, e.g. "CHECK ((VALUE > 0))", ordered by constraint name.
	// Over a parent domain VALUE may render cast to the base type, e.g.
	// "CHECK (((VALUE)::integer <= 100))".
	Checks []string `json:"checks,omitempty"`
	// Default is the domain's DEFAULT expression as text (pg_type.typdefault),
	// empty when it has none. A domain declared over another domain without
	// a DEFAULT of its own inherits its parent's.
	Default string `json:"default,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// Ref returns the domain's "schema.name" ref, the form [Column.Domain],
// [FuncArg.Domain] and [Domain.Parent] use.
func (d *Domain) Ref() string { return d.Schema + "." + d.Name }

// RangeType is a range type as recorded in pg_range.
type RangeType struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// Subtype is the element type (pg_range.rngsubtype) as Column.PGType
	// spells a type, e.g. "float8", and SubtypeSchema its schema.
	Subtype       string `json:"subtype"`
	SubtypeSchema string `json:"subtype_schema"`
	// Multirange is the multirange type PostgreSQL creates alongside the
	// range (pg_range.rngmultitypid), and MultirangeSchema its schema; both
	// are empty only in hand-built catalogs.
	Multirange       string `json:"multirange,omitempty"`
	MultirangeSchema string `json:"multirange_schema,omitempty"`
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
	// So are its check constraints.
	PartitionOf string `json:"partition_of,omitempty"`
	// PrimaryKey and Uniques may be temporal (WITHOUT OVERLAPS) keys; use
	// RowKeys for single-row lookups and ON CONFLICT targets.
	PrimaryKey  *Constraint   `json:"primary_key,omitempty"`
	Uniques     []*Constraint `json:"uniques,omitempty"`
	ForeignKeys []*ForeignKey `json:"foreign_keys,omitempty"`
	// Checks are the table's CHECK constraints. A check's Columns are the
	// columns its expression references, in column order (every column for
	// a whole-row reference); its WithoutOverlaps is always false.
	Checks     []*Constraint `json:"checks,omitempty"`
	Indexes    []*Index      `json:"indexes,omitempty"`
	Privileges Privileges    `json:"privileges"`
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

// ConstraintColumns returns the columns referenced by the primary key,
// unique, foreign key and check constraints and the indexes of t named
// name, as an integrity error naming it (PgError.ConstraintName) may
// reveal them: key and INCLUDE columns, the columns of key expressions and
// partial-index predicates ([Index.ExpressionColumns]), a foreign key's
// local columns and the columns a check references. A constraint and the
// index enforcing it share a name, and the result is the union of every
// match, without duplicates. ok is false when t records nothing named
// name, e.g. a partition's own copy of an inherited index; callers should
// then assume the worst. A foreign key's referenced columns belong to the
// other table and are not included.
func (t *Table) ConstraintColumns(name string) (cols []string, ok bool) {
	seen := map[string]bool{}
	add := func(cs []string) {
		ok = true
		for _, c := range cs {
			if !seen[c] {
				seen[c] = true
				cols = append(cols, c)
			}
		}
	}
	if t.PrimaryKey != nil && t.PrimaryKey.Name == name {
		add(t.PrimaryKey.Columns)
	}
	for _, list := range [][]*Constraint{t.Uniques, t.Checks} {
		for _, c := range list {
			if c.Name == name {
				add(c.Columns)
			}
		}
	}
	for _, fk := range t.ForeignKeys {
		if fk.Name == name {
			add(fk.Columns)
		}
	}
	for _, ix := range t.Indexes {
		if ix.Name == name {
			add(ix.Columns)
			add(ix.ExpressionColumns)
		}
	}
	return cols, ok
}

// ConstraintReferences reports whether a column [Table.ConstraintColumns]
// returns for name satisfies hidden, and, as ok, whether t records anything
// named name. Integrity error sanitizers call it with their own notion of a
// hidden column to decide whether an error naming the constraint may pass
// its name through; when ok is false they should treat the constraint as
// hidden if t has any hidden column.
func (t *Table) ConstraintReferences(name string, hidden func(column string) bool) (references, ok bool) {
	cols, ok := t.ConstraintColumns(name)
	return slices.ContainsFunc(cols, hidden), ok
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
	// NotNull reports that no row holds NULL in the column: a validated NOT
	// NULL constraint. A NOT NULL ... NOT VALID constraint (PostgreSQL 18+)
	// rejects new NULLs but leaves the column nullable here, since rows
	// that predate it may still hold NULL.
	NotNull bool `json:"not_null"`
	// HasDefault reports a DEFAULT expression (pg_attribute.atthasdef),
	// serial columns included. Identity columns have none: see
	// [Column.OmittableOnInsert].
	HasDefault bool `json:"has_default"`
	// GeneratedKind says whether the column is an identity or generated
	// column, and which kind; empty for every other column. See
	// [Column.GeneratedAlways] for the columns writes may not set.
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
	// BaseTypeSchema is the schema of PGType: the declared type's schema,
	// or for a domain the schema of the base type its chain resolves to
	// (an extension type such as hstore lives in the extension's schema
	// while a domain over it may live anywhere). Empty when unknown
	// (hand-built catalogs).
	BaseTypeSchema string `json:"base_type_schema,omitempty"`
	// Domain is the "schema.name" ref of the domain the column is declared
	// as, the outermost one when domains are layered, or of the element
	// domain of a column declared as an array of a domain (IsArray then
	// holds and PGType is the array type of the element domain's base,
	// e.g. "_text" for email[]). A column declared as a domain over an
	// array type has IsArray set too; [Domain.IsArrayBase] tells the two
	// apart. PGType, BaseTypeSchema, SQLType and Spatial always describe
	// the resolved base type. Empty for a column whose type involves no
	// domain.
	Domain string `json:"domain,omitempty"`
}

// GeneratedKind distinguishes the two kinds of identity column from stored
// and virtual generated columns.
type GeneratedKind string

const (
	// GeneratedIdentityAlways is a GENERATED ALWAYS AS IDENTITY column: an
	// INSERT or UPDATE that sets it to anything but DEFAULT fails unless
	// the INSERT says OVERRIDING SYSTEM VALUE.
	GeneratedIdentityAlways GeneratedKind = "identity_always"
	// GeneratedIdentityByDefault is a GENERATED BY DEFAULT AS IDENTITY
	// column: like a serial column, it takes its sequence's next value when
	// an INSERT omits it, and writes may set it.
	GeneratedIdentityByDefault GeneratedKind = "identity_by_default"
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

// Identity reports whether c is an identity column of either kind.
func (c *Column) Identity() bool {
	return c.GeneratedKind == GeneratedIdentityAlways || c.GeneratedKind == GeneratedIdentityByDefault
}

// GeneratedAlways reports whether PostgreSQL computes every value of c, so
// generated writes must never set it: an identity ALWAYS column or a stored
// or virtual generated column. Identity BY DEFAULT columns are writable.
func (c *Column) GeneratedAlways() bool {
	switch c.GeneratedKind {
	case GeneratedIdentityAlways, GeneratedStored, GeneratedVirtual:
		return true
	}
	return false
}

// OmittableOnInsert reports whether an INSERT may leave c out without
// PostgreSQL failing for want of a value: c is nullable, has a DEFAULT, or
// is an identity or generated column.
func (c *Column) OmittableOnInsert() bool {
	return !c.NotNull || c.HasDefault || c.GeneratedKind != ""
}

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
	// NotValid marks a foreign key added NOT VALID and not validated since:
	// PostgreSQL checks new and updated rows only, so rows that predate it
	// may reference no row.
	NotValid bool `json:"not_valid,omitempty"`
}

// MayDangle reports whether a child row may reference no row although its
// Columns are all non-null: the key is NotEnforced or NotValid.
func (fk *ForeignKey) MayDangle() bool { return fk.NotEnforced || fk.NotValid }

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
	// ExpressionColumns are the columns the index's key expressions and
	// partial-index predicate (WHERE) reference, in column order: secret
	// for an index on (owner, lower(secret)) or on (code) WHERE secret
	// <> '', every column for a whole-row reference. A column may be in
	// Columns too. Empty for an index on plain columns only. An exclusion
	// constraint's index is recorded here, so its expressions are too.
	ExpressionColumns []string `json:"expression_columns,omitempty"`
	Unique            bool     `json:"unique"`
	Method            string   `json:"method"` // btree, gin, gist, ...
	// OpClasses holds the operator class name (pg_opclass.opcname, without
	// its schema) of each Columns entry, "" for an INCLUDE column. It is nil
	// in hand-built catalogs that do not record operator classes.
	OpClasses []string `json:"op_classes,omitempty"`
}

// OpClass returns the operator class of the i-th entry of Columns, or ""
// when it is not recorded.
func (ix *Index) OpClass(i int) string {
	if i < 0 || i >= len(ix.OpClasses) {
		return ""
	}
	return ix.OpClasses[i]
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
	ReturnColumns    []FuncArg `json:"return_columns,omitempty"`
	ReturnType       string    `json:"return_type"`
	ReturnTypeSchema string    `json:"return_type_schema"`
	// ReturnDomain is the "schema.name" ref of the domain ReturnType is, or
	// of its element domain when ReturnType is an array of a domain; empty
	// otherwise. ReturnType keeps naming the declared type.
	ReturnDomain string     `json:"return_domain,omitempty"`
	ReturnsSet   bool       `json:"returns_set"`
	Volatility   Volatility `json:"volatility"`
	Comment      string     `json:"comment,omitempty"`
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
	// Domain is the "schema.name" ref of the domain PGType is, or of its
	// element domain when PGType is an array of a domain (e.g. "_email");
	// empty otherwise. Unlike a column's, a parameter's PGType and
	// TypeSchema keep naming the declared type, so casts to it apply the
	// domain's constraints; resolve the base type through
	// [Catalog.DomainByRef].
	Domain string `json:"domain,omitempty"`
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

// Table returns the table with the given schema+name, or nil. Lookups use a
// lazily built map that is rebuilt when Tables changes shape; see
// [Catalog.Reindex]. Safe for concurrent readers.
func (c *Catalog) Table(schema, name string) *Table {
	return lookup(&c.idx.tables, c.Tables, nameKey{schema, name}, func(t *Table) nameKey { return nameKey{t.Schema, t.Name} })
}

// Enum returns the enum with the given schema+name, or nil.
func (c *Catalog) Enum(schema, name string) *Enum {
	return lookup(&c.idx.enums, c.Enums, nameKey{schema, name}, func(e *Enum) nameKey { return nameKey{e.Schema, e.Name} })
}

// EnumByRef returns the table-backed enum a Column.EnumRef points at, or nil.
func (c *Catalog) EnumByRef(ref string) *Enum {
	if ref == "" {
		return nil
	}
	return lookup(&c.idx.enumRefs, c.Enums, ref, func(e *Enum) string {
		if !e.TableBacked {
			return ""
		}
		return e.Schema + "." + e.Name
	})
}

// Extension returns the installed extension with the given name, or nil.
func (c *Catalog) Extension(name string) *Extension {
	return lookup(&c.idx.extensions, c.Extensions, name, func(e *Extension) string { return e.Name })
}

// HasExtension reports whether the named extension is installed.
func (c *Catalog) HasExtension(name string) bool { return c.Extension(name) != nil }

// Range returns the range type with the given schema+name from
// [Catalog.Ranges], or nil.
func (c *Catalog) Range(schema, name string) *RangeType {
	return lookup(&c.idx.ranges, c.Ranges, nameKey{schema, name}, func(r *RangeType) nameKey { return nameKey{r.Schema, r.Name} })
}

// RangeOfMultirange returns the range type whose multirange type has the
// given schema+name, or nil.
func (c *Catalog) RangeOfMultirange(schema, name string) *RangeType {
	if name == "" {
		return nil
	}
	return lookup(&c.idx.multis, c.Ranges, nameKey{schema, name}, func(r *RangeType) nameKey { return nameKey{r.MultirangeSchema, r.Multirange} })
}

// Domain returns the domain with the given schema+name from
// [Catalog.Domains], or nil.
func (c *Catalog) Domain(schema, name string) *Domain {
	return lookup(&c.idx.domains, c.Domains, nameKey{schema, name}, func(d *Domain) nameKey { return nameKey{d.Schema, d.Name} })
}

// DomainByRef returns the domain a "schema.name" ref ([Column.Domain],
// [FuncArg.Domain], [Function.ReturnDomain], [Domain.Parent]) points at, or
// nil for an empty or unknown ref.
func (c *Catalog) DomainByRef(ref string) *Domain {
	if ref == "" {
		return nil
	}
	return lookup(&c.idx.domainRefs, c.Domains, ref, func(d *Domain) string { return d.Ref() })
}

// DomainChain returns d followed by its parent domains, outermost first,
// stopping at a parent missing from the catalog. A value of d satisfies the
// NOT NULL and CHECK constraints of every domain in the chain.
func (c *Catalog) DomainChain(d *Domain) []*Domain {
	var out []*Domain
	seen := map[*Domain]bool{}
	for ; d != nil && !seen[d]; d = c.DomainByRef(d.Parent) {
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Composite returns the composite type with the given schema+name, or nil.
func (c *Catalog) Composite(schema, name string) *Composite {
	return lookup(&c.idx.composites, c.Composites, nameKey{schema, name}, func(ct *Composite) nameKey { return nameKey{ct.Schema, ct.Name} })
}

// Hash returns a stable content hash of the catalog, used by the schema
// cache and the watch-mode poll fallback to detect drift. It does not modify
// c, so it is safe alongside concurrent readers. Hashing is independent of
// the order of the top-level slices.
//
// The hash covers the server's major version but not its minor version: it
// leaves out ServerVersion and reduces ServerVersionNum to its major part
// (180001 hashes as 180000), so a minor upgrade does not count as drift
// while a major upgrade, which can change what the catalog means, does.
func (c *Catalog) Hash() (string, error) {
	v := &Catalog{
		FormatVersion:    c.FormatVersion,
		ServerVersionNum: c.ServerVersionNum / 10000 * 10000,
		Schemas:          slices.Clone(c.Schemas),
		Tables:           slices.Clone(c.Tables),
		Enums:            slices.Clone(c.Enums),
		Composites:       slices.Clone(c.Composites),
		Functions:        slices.Clone(c.Functions),
		Extensions:       slices.Clone(c.Extensions),
		Ranges:           slices.Clone(c.Ranges),
		Domains:          slices.Clone(c.Domains),
	}
	v.sortStable()
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// sortStable puts the catalog's top-level slices in the canonical order
// [Introspect] returns: schemas by name; tables, enums, composites, ranges
// and domains by schema then name; extensions by name; and functions by
// [compareFunctions].
func (c *Catalog) sortStable() {
	slices.Sort(c.Schemas)
	byName := func(as, an, bs, bn string) int {
		return cmp.Or(strings.Compare(as, bs), strings.Compare(an, bn))
	}
	slices.SortStableFunc(c.Tables, func(a, b *Table) int { return byName(a.Schema, a.Name, b.Schema, b.Name) })
	slices.SortStableFunc(c.Enums, func(a, b *Enum) int { return byName(a.Schema, a.Name, b.Schema, b.Name) })
	slices.SortStableFunc(c.Composites, func(a, b *Composite) int { return byName(a.Schema, a.Name, b.Schema, b.Name) })
	slices.SortStableFunc(c.Functions, compareFunctions)
	slices.SortStableFunc(c.Extensions, func(a, b *Extension) int { return strings.Compare(a.Name, b.Name) })
	slices.SortStableFunc(c.Ranges, func(a, b *RangeType) int { return byName(a.Schema, a.Name, b.Schema, b.Name) })
	slices.SortStableFunc(c.Domains, func(a, b *Domain) int { return byName(a.Schema, a.Name, b.Schema, b.Name) })
}

// compareFunctions orders functions as documented on [Catalog.Functions].
func compareFunctions(a, b *Function) int {
	if c := cmp.Or(strings.Compare(a.Schema, b.Schema), strings.Compare(a.Name, b.Name), cmp.Compare(len(a.Args), len(b.Args))); c != 0 {
		return c
	}
	for i := range a.Args {
		if c := cmp.Or(strings.Compare(a.Args[i].TypeSchema, b.Args[i].TypeSchema), strings.Compare(a.Args[i].PGType, b.Args[i].PGType)); c != 0 {
			return c
		}
	}
	return 0
}

// HideRelation records that the table schema.name was removed from the
// catalog on purpose (by a contract or a full @omit), so that
// [Catalog.ResolvePolymorphic] never resolves a polymorphic function to its
// row type. The record survives [Catalog.Clone].
func (c *Catalog) HideRelation(schema, name string) {
	if c.hiddenRelations == nil {
		c.hiddenRelations = map[string]bool{}
	}
	c.hiddenRelations[schema+"."+name] = true
}

// RelationHidden reports whether the table schema.name, or the array type
// named so ("_name"), was recorded with [Catalog.HideRelation].
func (c *Catalog) RelationHidden(schema, name string) bool {
	if len(c.hiddenRelations) == 0 {
		return false
	}
	return c.hiddenRelations[schema+"."+name] ||
		strings.HasPrefix(name, "_") && c.hiddenRelations[schema+"."+name[1:]]
}
