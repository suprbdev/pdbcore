package introspect

import (
	"fmt"
	"slices"
	"strings"
)

// PolyFamily is a family of polymorphic pseudo-types whose occurrences in
// one call all resolve from a single type: [PolyAnyElement] covers
// anyelement, anyarray, anynonarray, anyenum, anyrange and anymultirange,
// [PolyAnyCompatible] the anycompatible* types.
type PolyFamily string

const (
	PolyAnyElement    PolyFamily = "anyelement"
	PolyAnyCompatible PolyFamily = "anycompatible"
)

// polyKind is how a polymorphic pseudo-type derives from its family's
// element type.
type polyKind int

const (
	polyElem polyKind = iota
	polyNonArray
	polyEnum
	polyArray
	polyRange
	polyMultirange
)

var polymorphicTypes = map[string]struct {
	family PolyFamily
	kind   polyKind
}{
	"anyelement":              {PolyAnyElement, polyElem},
	"anynonarray":             {PolyAnyElement, polyNonArray},
	"anyenum":                 {PolyAnyElement, polyEnum},
	"anyarray":                {PolyAnyElement, polyArray},
	"anyrange":                {PolyAnyElement, polyRange},
	"anymultirange":           {PolyAnyElement, polyMultirange},
	"anycompatible":           {PolyAnyCompatible, polyElem},
	"anycompatiblenonarray":   {PolyAnyCompatible, polyNonArray},
	"anycompatiblearray":      {PolyAnyCompatible, polyArray},
	"anycompatiblerange":      {PolyAnyCompatible, polyRange},
	"anycompatiblemultirange": {PolyAnyCompatible, polyMultirange},
}

// IsPolymorphic reports whether the type schema.typ (as [FuncArg.PGType]
// spells it) is one of PostgreSQL's polymorphic pseudo-types, which a call
// can only pass values to once their actual type is known.
func IsPolymorphic(schema, typ string) bool {
	_, ok := polymorphicTypes[typ]
	return ok && (schema == "" || schema == "pg_catalog")
}

// PolymorphicFamilies returns the families of the polymorphic types f takes
// or returns, in [PolyAnyElement], [PolyAnyCompatible] order; nil for a
// function with a fixed signature.
func (f *Function) PolymorphicFamilies() []PolyFamily {
	seen := map[PolyFamily]bool{}
	for _, a := range f.polyRefs() {
		if IsPolymorphic(a.TypeSchema, a.PGType) {
			seen[polymorphicTypes[a.PGType].family] = true
		}
	}
	var out []PolyFamily
	for _, fam := range []PolyFamily{PolyAnyElement, PolyAnyCompatible} {
		if seen[fam] {
			out = append(out, fam)
		}
	}
	return out
}

func (f *Function) polyRefs() []FuncArg {
	refs := append([]FuncArg{{PGType: f.ReturnType, TypeSchema: f.ReturnTypeSchema}}, f.Args...)
	return append(refs, f.ReturnColumns...)
}

// TypeRef names a type as pg_type does: Schema is its namespace and Name
// its typname ("int4", "_text" for text[]).
type TypeRef struct {
	Schema string
	Name   string
}

// IsArray reports whether the type is an array type.
func (t TypeRef) IsArray() bool { return strings.HasPrefix(t.Name, "_") }

func (t TypeRef) String() string { return t.Schema + "." + t.Name }

// sqlTypeNames maps the SQL spellings of built-in types to their typname.
var sqlTypeNames = map[string]string{
	"integer": "int4", "int": "int4", "int4": "int4",
	"bigint": "int8", "int8": "int8",
	"smallint": "int2", "int2": "int2",
	"boolean": "bool", "bool": "bool",
	"real": "float4", "float4": "float4",
	"double precision": "float8", "float": "float8", "float8": "float8",
	"numeric": "numeric", "decimal": "numeric",
	"text": "text", "name": "name", "oid": "oid",
	"character varying": "varchar", "varchar": "varchar",
	"character": "bpchar", "char": "bpchar", "bpchar": "bpchar",
	"timestamp with time zone": "timestamptz", "timestamptz": "timestamptz",
	"timestamp without time zone": "timestamp", "timestamp": "timestamp",
	"time with time zone": "timetz", "timetz": "timetz",
	"time without time zone": "time", "time": "time",
	"date": "date", "interval": "interval", "uuid": "uuid",
	"json": "json", "jsonb": "jsonb", "jsonpath": "jsonpath", "xml": "xml", "bytea": "bytea",
	"inet": "inet", "cidr": "cidr", "macaddr": "macaddr", "macaddr8": "macaddr8", "money": "money",
	"bit": "bit", "bit varying": "varbit", "varbit": "varbit",
	"tsvector": "tsvector", "tsquery": "tsquery",
	"point": "point", "line": "line", "lseg": "lseg", "box": "box", "path": "path", "polygon": "polygon", "circle": "circle",
	"int4range": "int4range", "int8range": "int8range", "numrange": "numrange",
	"tsrange": "tsrange", "tstzrange": "tstzrange", "daterange": "daterange",
	"int4multirange": "int4multirange", "int8multirange": "int8multirange", "nummultirange": "nummultirange",
	"tsmultirange": "tsmultirange", "tstzmultirange": "tstzmultirange", "datemultirange": "datemultirange",
}

// builtinRanges maps a built-in range's subtype to the range and its
// multirange.
var builtinRanges = map[string][2]string{
	"int4":        {"int4range", "int4multirange"},
	"int8":        {"int8range", "int8multirange"},
	"numeric":     {"numrange", "nummultirange"},
	"timestamp":   {"tsrange", "tsmultirange"},
	"timestamptz": {"tstzrange", "tstzmultirange"},
	"date":        {"daterange", "datemultirange"},
}

// extensionTypes maps types of common extensions to the extension that
// installs them, for unqualified type names.
var extensionTypes = map[string]string{
	"geometry": "postgis", "geography": "postgis", "box2d": "postgis", "box3d": "postgis",
	"hstore": "hstore", "ltree": "ltree", "lquery": "ltree", "ltxtquery": "ltree", "citext": "citext",
}

// ParseTypeName resolves a type as SQL spells it ("integer", "text[]",
// "timestamp with time zone", "varchar(20)", "public.mood", "mood") to the
// pg_type name. Type modifiers are dropped. An unqualified name that is no
// built-in type is looked up among the catalog's enums, composites, tables,
// domains and range types, in schema first and then in [Catalog.Schemas]
// order, and finally among the types of installed extensions (PostGIS,
// hstore, ltree, citext). A schema-qualified name is taken as written.
func (c *Catalog) ParseTypeName(spec, schema string) (TypeRef, error) {
	s := strings.TrimSpace(spec)
	array := false
	for strings.HasSuffix(s, "[]") {
		array = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "[]"))
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		if !strings.HasSuffix(s, ")") {
			return TypeRef{}, fmt.Errorf("invalid type %q", spec)
		}
		s = strings.TrimSpace(s[:i])
	}
	if s == "" {
		return TypeRef{}, fmt.Errorf("invalid type %q", spec)
	}
	ref, err := c.parseBaseType(s, schema)
	if err != nil {
		return TypeRef{}, fmt.Errorf("%w in %q", err, spec)
	}
	if array {
		ref.Name = "_" + ref.Name
	}
	return ref, nil
}

func (c *Catalog) parseBaseType(s, schema string) (TypeRef, error) {
	parts, err := splitQualified(s)
	if err != nil {
		return TypeRef{}, err
	}
	if len(parts) == 2 {
		return TypeRef{Schema: parts[0], Name: parts[1]}, nil
	}
	name := parts[0]
	if !strings.HasPrefix(s, `"`) {
		if n, ok := sqlTypeNames[strings.Join(strings.Fields(strings.ToLower(s)), " ")]; ok {
			return TypeRef{Schema: "pg_catalog", Name: n}, nil
		}
	}
	for _, sc := range append([]string{schema}, c.Schemas...) {
		if c.typeExists(sc, name) {
			return TypeRef{Schema: sc, Name: name}, nil
		}
	}
	if ext := c.Extension(extensionTypes[name]); ext != nil {
		return TypeRef{Schema: ext.Schema, Name: name}, nil
	}
	return TypeRef{}, fmt.Errorf("unknown type %q", name)
}

// splitQualified splits "a.b" / `"A"."b"` into lowercased (unless quoted)
// identifiers.
func splitQualified(s string) ([]string, error) {
	var parts []string
	for s != "" {
		var id string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			id, s = s[1:end+1], s[end+2:]
		} else {
			i := strings.IndexByte(s, '.')
			if i < 0 {
				i = len(s)
			}
			id, s = strings.ToLower(strings.TrimSpace(s[:i])), s[i:]
		}
		if id == "" {
			return nil, fmt.Errorf("empty identifier")
		}
		parts = append(parts, id)
		if s == "" {
			break
		}
		if s[0] != '.' {
			return nil, fmt.Errorf("unexpected %q", s)
		}
		s = s[1:]
		if s == "" {
			return nil, fmt.Errorf("empty identifier")
		}
	}
	if len(parts) == 0 || len(parts) > 2 {
		return nil, fmt.Errorf("invalid type name")
	}
	return parts, nil
}

func (c *Catalog) typeExists(schema, name string) bool {
	if schema == "" {
		return false
	}
	if e := c.Enum(schema, name); e != nil && !e.TableBacked {
		return true
	}
	return c.Composite(schema, name) != nil || c.Table(schema, name) != nil ||
		c.Domain(schema, name) != nil || c.Range(schema, name) != nil || c.RangeOfMultirange(schema, name) != nil
}

// ResolvePolymorphic returns a copy of f with every polymorphic argument,
// return type and output column replaced by the concrete type it takes
// when the family's element type is elem ([PolyAnyElement]) or compat
// ([PolyAnyCompatible]): anyelement/anynonarray/anyenum become the element
// type, anyarray its array type, anyrange/anymultirange the range type over
// it and that range's multirange (the built-in ranges, else the single
// range type in [Catalog.Ranges] with that subtype). A family f uses needs
// its element type; the same holds for the compatible variants. Calling the
// function with arguments cast to the resolved types makes PostgreSQL
// resolve its polymorphic signature the same way. The error names the
// first type that cannot be resolved (missing element type, anyenum over a
// non-enum, an array where an array or range is derived, no or several
// candidate ranges).
func (c *Catalog) ResolvePolymorphic(f *Function, elem, compat *TypeRef) (*Function, error) {
	out := *f
	resolve := func(a *FuncArg) error {
		if !IsPolymorphic(a.TypeSchema, a.PGType) {
			return nil
		}
		p := polymorphicTypes[a.PGType]
		base := elem
		if p.family == PolyAnyCompatible {
			base = compat
		}
		if base == nil {
			return fmt.Errorf("%s: no type given for %s", a.PGType, p.family)
		}
		ref, err := c.derivePolymorphic(p.kind, *base)
		if err != nil {
			return fmt.Errorf("%s: %w", a.PGType, err)
		}
		a.TypeSchema, a.PGType = ref.Schema, ref.Name
		a.Domain = ""
		if d := c.Domain(ref.Schema, strings.TrimPrefix(ref.Name, "_")); d != nil {
			a.Domain = d.Ref()
		}
		return nil
	}
	ret := FuncArg{PGType: f.ReturnType, TypeSchema: f.ReturnTypeSchema, Domain: f.ReturnDomain}
	if err := resolve(&ret); err != nil {
		return nil, err
	}
	out.ReturnType, out.ReturnTypeSchema, out.ReturnDomain = ret.PGType, ret.TypeSchema, ret.Domain
	out.Args = slices.Clone(f.Args)
	for i := range out.Args {
		if err := resolve(&out.Args[i]); err != nil {
			return nil, err
		}
	}
	out.ReturnColumns = slices.Clone(f.ReturnColumns)
	for i := range out.ReturnColumns {
		if err := resolve(&out.ReturnColumns[i]); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

func (c *Catalog) derivePolymorphic(kind polyKind, base TypeRef) (TypeRef, error) {
	if base.IsArray() && kind != polyElem {
		return TypeRef{}, fmt.Errorf("element type %s is an array", base)
	}
	switch kind {
	case polyEnum:
		if e := c.Enum(base.Schema, base.Name); e == nil || e.TableBacked {
			return TypeRef{}, fmt.Errorf("element type %s is not an enum", base)
		}
	case polyArray:
		return TypeRef{Schema: base.Schema, Name: "_" + base.Name}, nil
	case polyRange, polyMultirange:
		rng, multi, err := c.rangeOver(base)
		if err != nil {
			return TypeRef{}, err
		}
		if kind == polyRange {
			return rng, nil
		}
		if multi.Name == "" {
			return TypeRef{}, fmt.Errorf("range %s has no multirange type", rng)
		}
		return multi, nil
	}
	return base, nil
}

// rangeOver finds the range type (and multirange) whose subtype is base.
func (c *Catalog) rangeOver(base TypeRef) (rng, multi TypeRef, err error) {
	var found [][2]TypeRef
	if base.Schema == "pg_catalog" {
		if r, ok := builtinRanges[base.Name]; ok {
			found = append(found, [2]TypeRef{{"pg_catalog", r[0]}, {"pg_catalog", r[1]}})
		}
	}
	for _, r := range c.Ranges {
		if r.Subtype == base.Name && r.SubtypeSchema == base.Schema {
			found = append(found, [2]TypeRef{{r.Schema, r.Name}, {r.MultirangeSchema, r.Multirange}})
		}
	}
	switch len(found) {
	case 0:
		return TypeRef{}, TypeRef{}, fmt.Errorf("no range type over %s", base)
	case 1:
		return found[0][0], found[0][1], nil
	}
	return TypeRef{}, TypeRef{}, fmt.Errorf("several range types over %s", base)
}
