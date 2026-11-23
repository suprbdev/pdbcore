// Package contrib models the operator vocabularies and value coercion for
// PostgreSQL's non-scalar built-in types and the common contrib extensions —
// full-text search (tsvector, and to_tsvector over text), pg_trgm, unaccent,
// hstore, ltree, citext, range and multirange types, inet/cidr and pgvector's
// vector, halfvec and sparsevec — as a small,
// catalog-driven layer shared by pdbq and pdbr, in the same style as the
// postgis package.
//
// Nothing here emits a whole statement. A [Vocabulary] answers, for one
// column type:
//
//   - which filter operators ([Op]) apply, what operand each takes, how it
//     renders and which index can serve it;
//   - which operand-taking value expressions ([Expr]) exist for ordering or
//     selection (trigram similarity and distance, vector distances);
//   - which derived accessors ([Accessor]) exist (ltree nlevel, inet family,
//     range bounds, vector dims);
//   - how an input value for the column becomes bind parameters and back
//     ([Codec]), where the text form is not good enough (hstore as a JSON
//     object, ranges as {start, end, startInclusive, endInclusive}, vectors
//     as lists of numbers).
//
// [ForColumn] and [ForType] resolve a column (by its resolved base type, so
// domains over these types work) to the vocabularies that apply to it.
//
// # The parameterization invariant
//
// Operands are user data and move only through the parameter list. Every
// Render, SQL and Codec function takes the column expression and placeholder
// strings ("$3") and nothing else, so compiled SQL text is a pure function of
// the query shape (which operator, which column, whether an optional operand
// such as a text search configuration was given), never of the values. The
// consumer protocol is:
//
//	params, err := op.Params(value)  // coerce and validate the operand
//	phs := make([]string, len(params))
//	for i, p := range params {
//		phs[i] = addParam(p)       // "$n"
//	}
//	sql := op.Render(colRef, phs...)
//
// Placeholders are always cast to their target type inside the rendered SQL
// ($1::text, $1::regconfig, ...), so parameter types never depend on the
// driver's inference and a parameter may appear more than once.
//
// # Extension schemas
//
// Extension types, operators and functions live in the schema the extension
// was installed into, which need not be on the search_path. A vocabulary
// bound to that schema (ForColumn/ForType bind it from
// [introspect.Catalog.Extensions]) schema-qualifies everything it renders:
// types as "ext".hstore, functions as "ext".similarity(...), and operators
// with the OPERATOR("ext".@>) syntax. An unbound vocabulary (schema "")
// renders bare names and relies on the search_path. Core types (tsvector,
// the built-in ranges, inet) are always rendered unqualified; range types
// recorded in [introspect.Catalog.Ranges] are rendered qualified with their
// own schemas ([RangeOf]).
package contrib

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
)

// Extension names the vocabularies depend on, as recorded in pg_extension.
const (
	ExtTrgm   = "pg_trgm"
	ExtHstore = "hstore"
	ExtLtree  = "ltree"
	ExtCitext = "citext"
	// ExtUnaccent is the unaccent extension.
	ExtUnaccent = "unaccent"
	// ExtVector is pgvector, whose extension name is "vector".
	ExtVector = "vector"
)

// Vocabulary names.
const (
	VocabFullText   = "fulltext"   // tsvector columns
	VocabTextSearch = "textsearch" // to_tsvector over text-like columns
	VocabTrgm       = "trgm"       // pg_trgm over text-like columns
	VocabCitext     = "citext"
	VocabHstore     = "hstore"
	VocabLtree      = "ltree"
	VocabLtreeArray = "ltree[]"
	VocabRange      = "range"
	VocabMultirange = "multirange"
	VocabInet       = "inet"
	VocabUnaccent   = "unaccent" // unaccent over text-like columns
	VocabVector     = "vector"   // pgvector vector, halfvec and sparsevec columns
)

// ArgKind classifies an operand (or a column value fed to a [Codec]) so
// callers know what to accept and how many parameters it becomes.
type ArgKind int

const (
	// ArgText is a single string; one text parameter.
	ArgText ArgKind = iota
	// ArgTextList is a list of strings; one text[] parameter.
	ArgTextList
	// ArgFullText is a text search query: a string, or an object
	// {query, config} where config names a text search configuration
	// (regconfig). One parameter, or two when config is given.
	ArgFullText
	// ArgHstore is a JSON object of string keys to string or null values;
	// one text[] parameter of alternating keys and values.
	ArgHstore
	// ArgRange is a range: an object {start, end, startInclusive,
	// endInclusive, isEmpty} or a range literal string ("[1,5)", "empty").
	// Three parameters: lower bound, upper bound, bounds text.
	ArgRange
	// ArgMultirange is a list of ranges (each as for ArgRange) or a
	// multirange literal string ("{[1,3), [5,7)}"). Three array
	// parameters: lower bounds, upper bounds, bounds texts.
	ArgMultirange
	// ArgElement is one value of a range's element type (a number, or a
	// string for numeric, date and timestamp elements); one parameter.
	ArgElement
	// ArgVector is a pgvector value: a list of numbers, or the type's text
	// form as a string; one text parameter.
	ArgVector
)

func (k ArgKind) String() string {
	switch k {
	case ArgText:
		return "text"
	case ArgTextList:
		return "text[]"
	case ArgFullText:
		return "fulltext"
	case ArgHstore:
		return "hstore"
	case ArgRange:
		return "range"
	case ArgMultirange:
		return "multirange"
	case ArgElement:
		return "element"
	case ArgVector:
		return "vector"
	}
	return ""
}

// arity reports the minimum and maximum number of placeholders an operand of
// this kind renders from.
func (k ArgKind) arity() (int, int) {
	switch k {
	case ArgFullText:
		return 1, 2
	case ArgRange, ArgMultirange:
		return 3, 3
	}
	return 1, 1
}

// IndexHint names an index access method (and operator class, when it is
// not the default for the type) that can serve an operator or expression.
type IndexHint struct {
	// Method is the access method: "gin", "gist", "spgist", "btree", or
	// pgvector's "hnsw" and "ivfflat".
	Method string
	// OpClass is the operator class the index must be built with, e.g.
	// "gin_trgm_ops" or "inet_ops".
	OpClass string
	// Note qualifies the hint, e.g. that only an expression index helps.
	Note string
	// Ordering marks an index that serves ORDER BY expr (KNN) rather than a
	// WHERE predicate.
	Ordering bool
}

// qual renders names belonging to an extension schema.
type qual struct{ schema string }

func (q qual) prefix() string {
	if q.schema == "" {
		return ""
	}
	return quoteIdent(q.schema) + "."
}

// op renders an infix operator, schema-qualified when bound.
func (q qual) op(name string) string {
	if q.schema == "" {
		return name
	}
	return "OPERATOR(" + q.prefix() + name + ")"
}

// fn renders a function name, schema-qualified when bound.
func (q qual) fn(name string) string { return q.prefix() + name }

// typ renders a type name, schema-qualified when bound.
func (q qual) typ(name string) string { return q.prefix() + name }

// cast renders placeholder::type with the type schema-qualified when bound.
func (q qual) cast(ph, typ string) string { return atom(ph) + "::" + q.typ(typ) }

// paren groups an operand of an infix operator. OPERATOR(schema.op) has the
// "any other operator" precedence while some bare operators (%) are
// multiplicative, so a non-atomic column expression would otherwise regroup
// differently in bound and unbound renders.
func paren(expr string) string { return "(" + expr + ")" }

// atom returns a bare $n placeholder unchanged and parenthesizes anything
// else, so a following ::type cast applies to the whole expression.
func atom(ph string) string {
	if len(ph) > 1 && ph[0] == '$' && strings.Trim(ph[1:], "0123456789") == "" {
		return ph
	}
	return paren(ph)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Op is one filter operator of a vocabulary.
type Op struct {
	// Name is the operator's field name (pdbq: inside the type's filter
	// input; pdbr maps it to its own grammar).
	Name string
	// Arg is the operand shape; Params coerces an operand of this shape.
	Arg ArgKind
	// Description documents the operator in generated schemas.
	Description string
	// Index lists the indexes that can serve the predicate; empty means
	// the operator always needs a scan.
	Index []IndexHint
	// Extension is the extension the operator needs, "" for core.
	Extension string

	q      qual
	render func(q qual, col string, a []string) string
	rank   func(q qual, fn, col string, a []string) string
	params func(v any) ([]any, error)
	// since is the first extension version providing the operator.
	since string
}

// Indexable reports whether some index can serve the predicate.
func (o Op) Indexable() bool { return len(o.Index) > 0 }

// Params coerces an operand into the bind parameters Render expects, in
// order. The error is user-facing.
func (o Op) Params(v any) ([]any, error) {
	if o.params != nil {
		return o.params(v)
	}
	return coerce(o.Arg, v)
}

// Render builds the SQL predicate over the column expression col from the
// placeholders for the values Params returned. It returns "" when the
// number of placeholders does not fit the operand kind.
func (o Op) Render(col string, args ...string) string {
	lo, hi := o.Arg.arity()
	if o.render == nil || len(args) < lo || len(args) > hi {
		return ""
	}
	return o.render(o.q, col, args)
}

// RankSQL renders a relevance expression for a full-text operator, reusing
// the placeholders its predicate was rendered with, so a result filtered by
// the operator can be ordered by how well it matched (higher is better). It
// returns "" for operators that are not full-text matches.
func (o Op) RankSQL(r Rank, col string, args ...string) string {
	lo, hi := o.Arg.arity()
	if o.rank == nil || len(args) < lo || len(args) > hi {
		return ""
	}
	return o.rank(o.q, string(r), col, args)
}

// Expr is a value expression over a column and one operand, used for
// ordering or as a computed output (trigram similarity and distance).
type Expr struct {
	// Name is the expression's field name.
	Name string
	// Arg is the operand shape.
	Arg ArgKind
	// Type is the pg type of the result, e.g. "float4".
	Type string
	// Description documents the expression.
	Description string
	// Ascending reports whether smaller values are better matches (a
	// distance), so a best-first ordering sorts ascending.
	Ascending bool
	// Index lists the indexes that can serve ORDER BY on the expression.
	Index []IndexHint
	// Extension is the extension the expression needs, "" for core.
	Extension string

	q      qual
	render func(q qual, col string, a []string) string
	params func(v any) ([]any, error)
	since  string
}

// Params coerces the operand into the bind parameters Render expects.
func (e Expr) Params(v any) ([]any, error) {
	if e.params != nil {
		return e.params(v)
	}
	return coerce(e.Arg, v)
}

// Render builds the expression over col; "" when the number of placeholders
// does not fit the operand kind.
func (e Expr) Render(col string, args ...string) string {
	lo, hi := e.Arg.arity()
	if e.render == nil || len(args) < lo || len(args) > hi {
		return ""
	}
	return e.render(e.q, col, args)
}

// Accessor is a derived, operand-free value of a column (ltree nlevel, inet
// family, range bounds), exposable as a sibling output field.
type Accessor struct {
	// Name is the accessor's field name (callers typically append its
	// UpperCamel form to the column's field name).
	Name string
	// Type is the pg type of the result, e.g. "int4", "bool", "text".
	// Range bound accessors return the range's element type.
	Type string
	// Description documents the accessor.
	Description string

	q   qual
	sql func(q qual, ref string) string
}

// SQL renders the accessor over a column reference.
func (a Accessor) SQL(ref string) string { return a.sql(a.q, ref) }

// Codec converts input values for a column of the vocabulary's type into
// bind parameters and a typed SQL expression, and renders the column for
// output.
type Codec struct {
	// Arg is the accepted input shape.
	Arg ArgKind

	q      qual
	sql    func(q qual, a []string) string
	output func(q qual, ref string) string
	// outputJSON is output producing json instead of jsonb; nil with output.
	outputJSON func(q qual, ref string) string
	params     func(v any) ([]any, error)
}

// Params coerces an input value into the bind parameters SQL expects.
func (c *Codec) Params(v any) ([]any, error) {
	if c.params != nil {
		return c.params(v)
	}
	return coerce(c.Arg, v)
}

// SQL renders the typed value expression from the placeholders for the
// values Params returned; "" when their number does not fit.
func (c *Codec) SQL(args ...string) string {
	lo, hi := c.Arg.arity()
	if len(args) < lo || len(args) > hi {
		return ""
	}
	return c.sql(c.q, args)
}

// OutputSQL renders the column reference for a JSON response: a structured
// jsonb value for hstore and range types, the reference itself for types
// whose text form is the API value. NULL stays NULL, and ref is evaluated
// once, so it may be a function call.
func (c *Codec) OutputSQL(ref string) string {
	if c.output == nil {
		return ref
	}
	return c.output(c.q, ref)
}

// OutputJSONSQL is [Codec.OutputSQL] producing json instead of jsonb for
// hstore and range types. json skips jsonb's parse and normalisation, so it is
// cheaper for values only embedded in a response, but it cannot be compared,
// grouped or aggregated with jsonb functions; use OutputSQL for those.
func (c *Codec) OutputJSONSQL(ref string) string {
	if c.outputJSON == nil {
		return ref
	}
	return c.outputJSON(c.q, ref)
}

// Vocabulary is the operator, expression and accessor set for one column
// type, optionally bound to the schema its extension lives in.
type Vocabulary struct {
	// Name identifies the vocabulary (VocabHstore, VocabRange, ...).
	Name string
	// Type is the pg type the vocabulary was built for ("int4range",
	// "hstore"); for the text vocabularies, the text-like column type.
	Type string
	// Extension is the extension the vocabulary needs, "" for core.
	Extension string
	// Schema is the extension schema everything renders qualified with;
	// "" renders bare names.
	Schema string
	// Ops are the filter operators. The order is append-only so generated
	// schemas only grow at the end.
	Ops []Op
	// Exprs are the operand-taking value expressions.
	Exprs []Expr
	// Accessors are the derived values.
	Accessors []Accessor
	// Codec converts values of the column's own type; nil when the
	// vocabulary only adds operators to a type it does not own (text
	// search, trigram).
	Codec *Codec
}

// Bind returns a copy of v that schema-qualifies its extension's types,
// functions and operators with schema. Binding a core vocabulary is a no-op.
func (v Vocabulary) Bind(schema string) Vocabulary {
	if v.Extension == "" {
		schema = ""
	}
	q := qual{schema: schema}
	v.Schema = schema
	v.Ops = append([]Op(nil), v.Ops...)
	for i := range v.Ops {
		v.Ops[i].q = q
	}
	v.Exprs = append([]Expr(nil), v.Exprs...)
	for i := range v.Exprs {
		v.Exprs[i].q = q
	}
	v.Accessors = append([]Accessor(nil), v.Accessors...)
	for i := range v.Accessors {
		v.Accessors[i].q = q
	}
	if v.Codec != nil {
		c := *v.Codec
		c.q = q
		v.Codec = &c
	}
	return v
}

// Operator renders an infix operator of the vocabulary's type (e.g. "=" or
// "<" for a generic comparison filter) schema-qualified with the
// vocabulary's extension schema, as OPERATOR("ext".=); unbound and core
// vocabularies return op unchanged. Generic comparisons on an extension type
// whose schema is not on the search_path fail to resolve (hstore, ltree) or
// resolve to the wrong operator (citext; see [Citext]) without it.
func (v Vocabulary) Operator(op string) string { return qual{schema: v.Schema}.op(op) }

// ForVersion returns a copy of v without the operators and expressions that
// the given version of its extension does not provide yet (pg_trgm before
// 1.2 lacks the word-similarity operators, before 1.4 the strict ones; a
// database upgraded with pg_upgrade keeps its old extension versions until
// ALTER EXTENSION ... UPDATE). An empty or unparsable version keeps
// everything.
func (v Vocabulary) ForVersion(version string) Vocabulary {
	var ops []Op
	for _, o := range v.Ops {
		if versionAtLeast(version, o.since) {
			ops = append(ops, o)
		}
	}
	var exprs []Expr
	for _, e := range v.Exprs {
		if versionAtLeast(version, e.since) {
			exprs = append(exprs, e)
		}
	}
	v.Ops, v.Exprs = ops, exprs
	return v
}

// versionAtLeast compares dotted numeric versions component by component
// ("1.10" > "1.4"). An empty or non-numeric have or an empty want is true.
func versionAtLeast(have, want string) bool {
	if want == "" || have == "" {
		return true
	}
	h, w := strings.Split(have, "."), strings.Split(want, ".")
	for i := 0; i < len(h) || i < len(w); i++ {
		hn, wn := 0, 0
		var err error
		if i < len(h) {
			if hn, err = strconv.Atoi(h[i]); err != nil {
				return true
			}
		}
		if i < len(w) {
			if wn, err = strconv.Atoi(w[i]); err != nil {
				return true
			}
		}
		if hn != wn {
			return hn > wn
		}
	}
	return true
}

// Op looks up an operator by name.
func (v Vocabulary) Op(name string) (Op, bool) {
	for _, o := range v.Ops {
		if o.Name == name {
			return o, true
		}
	}
	return Op{}, false
}

// Expr looks up an expression by name.
func (v Vocabulary) Expr(name string) (Expr, bool) {
	for _, e := range v.Exprs {
		if e.Name == name {
			return e, true
		}
	}
	return Expr{}, false
}

// Accessor looks up an accessor by name.
func (v Vocabulary) Accessor(name string) (Accessor, bool) {
	for _, a := range v.Accessors {
		if a.Name == name {
			return a, true
		}
	}
	return Accessor{}, false
}

// OpNames returns the operator names in declaration order.
func (v Vocabulary) OpNames() []string {
	out := make([]string, len(v.Ops))
	for i, o := range v.Ops {
		out[i] = o.Name
	}
	return out
}

// FindOp looks an operator up across the vocabularies of one column. Op
// names are unique within the set ForColumn returns for any column.
func FindOp(vs []Vocabulary, name string) (Op, bool) {
	for _, v := range vs {
		if o, ok := v.Op(name); ok {
			return o, true
		}
	}
	return Op{}, false
}

// ForColumn returns the vocabularies that apply to a column, bound to the
// schemas their extensions are installed in and limited to what the
// installed extension version provides ([Vocabulary.ForVersion]). Extension
// vocabularies are returned only when the catalog records the extension.
// Columns that other layers own (PostGIS spatial columns, table-backed
// enums) get none.
//
// The column's resolved base type is matched by name and by
// [introspect.Column.BaseTypeSchema]: core types must be in pg_catalog,
// extension types in their extension's schema, and range types recorded in
// [introspect.Catalog.Ranges] get the range vocabulary ([RangeOf]), so a
// user type that merely shares a name (an "hstore" in another schema) gets
// none. A column without BaseTypeSchema is matched by name alone.
func ForColumn(cat *introspect.Catalog, col *introspect.Column) []Vocabulary {
	if col == nil || col.Spatial != nil || col.EnumRef != "" {
		return nil
	}
	schema, name, arr := splitType(col.PGType)
	if col.BaseTypeSchema != "" {
		schema = col.BaseTypeSchema
	}
	return forType(cat, schema, name, arr || col.IsArray)
}

// ForType returns the vocabularies for a pg type name as Column.PGType
// spells it (the resolved base type of a domain; "_ltree" for an array),
// also accepting schema-qualified and quoted spellings ("ext.hstore",
// `"ext"."ltree"[]`). A schema qualifier is matched as in [ForColumn]; an
// unqualified name is matched by name alone, preferring built-in types.
// isArray marks an array type whose name carries no array marker. cat
// supplies installed extensions with their schemas and range types; nil
// means none are installed.
func ForType(cat *introspect.Catalog, pgType string, isArray bool) []Vocabulary {
	schema, name, arr := splitType(pgType)
	return forType(cat, schema, name, arr || isArray)
}

func forType(cat *introspect.Catalog, schema, name string, arr bool) []Vocabulary {
	base := strings.ToLower(name)
	core := schema == "" || schema == "pg_catalog"
	ext := func(extName string) (string, bool) {
		if cat == nil {
			return "", false
		}
		e := cat.Extension(extName)
		if e == nil || (schema != "" && schema != e.Schema) {
			return "", false
		}
		return e.Schema, true
	}
	if arr {
		if base == "ltree" {
			if s, ok := ext(ExtLtree); ok {
				return []Vocabulary{LtreeArray().Bind(s)}
			}
		}
		return nil
	}
	if core {
		switch base {
		case "tsvector":
			return []Vocabulary{FullText()}
		case "inet", "cidr":
			v, _ := Inet(base)
			return []Vocabulary{v}
		}
		if v, ok := Range(base); ok {
			return []Vocabulary{v}
		}
		if v, ok := Multirange(base); ok {
			return []Vocabulary{v}
		}
	}
	if r, multi := catalogRange(cat, schema, name); r != nil {
		if multi {
			v, _ := MultirangeOf(r)
			return []Vocabulary{v}
		}
		return []Vocabulary{RangeOf(r)}
	}
	switch base {
	case "vector", "halfvec", "sparsevec":
		if e, ok := ext(ExtVector); ok {
			v, _ := Vector(base)
			return []Vocabulary{v.Bind(e).ForVersion(cat.Extension(ExtVector).Version)}
		}
		return nil
	case "hstore":
		if s, ok := ext(ExtHstore); ok {
			return []Vocabulary{Hstore().Bind(s)}
		}
		return nil
	case "ltree":
		if s, ok := ext(ExtLtree); ok {
			return []Vocabulary{Ltree().Bind(s)}
		}
		return nil
	}
	if !IsText(base) {
		return nil
	}
	var out []Vocabulary
	if base == "citext" {
		s, ok := ext(ExtCitext)
		if !ok {
			return nil
		}
		out = append(out, Citext().Bind(s))
	} else if !core {
		return nil
	}
	out = append(out, TextSearch(base))
	if cat != nil {
		if e := cat.Extension(ExtTrgm); e != nil {
			out = append(out, Trgm(base).Bind(e.Schema).ForVersion(e.Version))
		}
		if e := cat.Extension(ExtUnaccent); e != nil {
			out = append(out, Unaccent(base).Bind(e.Schema))
		}
	}
	return out
}

// catalogRange finds the range type recorded in cat that a range or
// multirange type name refers to, reporting whether it is the multirange.
// An empty schema matches any.
func catalogRange(cat *introspect.Catalog, schema, name string) (*introspect.RangeType, bool) {
	if cat == nil {
		return nil, false
	}
	for _, r := range cat.Ranges {
		if (schema == "" || r.Schema == schema) && r.Name == name {
			return r, false
		}
	}
	for _, r := range cat.Ranges {
		if r.Multirange != "" && (schema == "" || r.MultirangeSchema == schema) && r.Multirange == name {
			return r, true
		}
	}
	return nil, false
}

// splitType splits a type name into its schema qualifier ("" when
// unqualified), its name, and whether it is an array ("_x" or "x[]"),
// stripping double quotes.
func splitType(t string) (string, string, bool) {
	t = strings.TrimSpace(t)
	arr := false
	if s, ok := strings.CutSuffix(t, "[]"); ok {
		t, arr = s, true
	}
	t = strings.ReplaceAll(t, `"`, "")
	schema := ""
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		schema, t = t[:i], t[i+1:]
	}
	if s, ok := strings.CutPrefix(t, "_"); ok && !arr {
		t, arr = s, true
	}
	return schema, t, arr
}

// normalizeType strips quoting, a schema qualifier and an array marker
// ("_x" or "x[]") from a type name.
func normalizeType(t string) (string, bool) {
	_, name, arr := splitType(t)
	return strings.ToLower(name), arr
}

// IsText reports whether a pg type (as Column.PGType spells it) is a
// character string type the text vocabularies apply to: text, varchar,
// bpchar (char(n)) and citext. Domains are resolved to their base type by
// introspection, so a domain over any of these is text too.
func IsText(pgType string) bool {
	base, arr := normalizeType(pgType)
	if arr {
		return false
	}
	switch base {
	case "text", "varchar", "bpchar", "citext":
		return true
	}
	return false
}

// IsCaseInsensitive reports whether a pg type compares case-insensitively
// by itself (citext): equality, LIKE and ordering already ignore case, so a
// compiler needs no ILIKE or lower() rewriting — but must keep operands
// typed as citext (see [Citext]), because comparing citext with a text
// operand resolves to the case-sensitive text operator.
func IsCaseInsensitive(pgType string) bool {
	base, arr := normalizeType(pgType)
	return !arr && base == "citext"
}
