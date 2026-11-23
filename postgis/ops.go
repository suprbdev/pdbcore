package postgis

import (
	"fmt"
	"strings"
)

// Op describes one spatial filter operator: its field name (as exposed by
// pdbq's GeometryFilterOps; pdbr maps it to its own grammar), the input shape
// it takes, and how it renders as SQL.
type Op struct {
	// Name is the operator's field name (pdbq: inside GeometryFilterOps).
	Name string
	// Arg is the operand type name (pdbq SDL spelling).
	Arg string
	// Description documents the operator in the generated schema.
	Description string
	// Index reports whether the operator can use a GiST index directly.
	// Index-usable operators are documented as such so users can tell the
	// cheap ones from the exact-but-expensive ones. For an operator without
	// a geography overload the flag describes geometry columns only: a
	// geography column is cast to geometry, which a GiST index on the
	// geography column cannot serve (an expression index on col::geometry
	// can).
	Index bool
	// Geography reports whether PostGIS provides a geography overload. The
	// others (ST_Within, ST_Contains, ~, @, ...) exist for geometry only, so
	// a compiler must cast a geography column and its operand to geometry
	// before rendering them.
	Geography bool
	// render builds the predicate. col is the column expression, geom the
	// already-parameterized geometry expression, and extra carries operator
	// specific placeholders (currently the distance parameter).
	render func(col, geom string, extra []string) string
}

// ArgKind classifies the operand shape so callers know what to parse.
type ArgKind int

const (
	// ArgGeometry: the operand is a bare GeoJSON/WKT value.
	ArgGeometry ArgKind = iota
	// ArgDWithin: the operand is {geometry, distance}.
	ArgDWithin
	// ArgBBox: the operand is {minX, minY, maxX, maxY, srid}.
	ArgBBox
	// ArgBoolean: the operand is a Boolean (isNull).
	ArgBoolean
)

// Kind reports the operand shape for an operator.
func (o Op) Kind() ArgKind {
	switch o.Name {
	case "dwithin", "beyond":
		return ArgDWithin
	case "bboxIntersects", "bboxContains", "bboxContainedBy":
		return ArgBBox
	case "isNull":
		return ArgBoolean
	}
	return ArgGeometry
}

// binary builds a render func for a plain two-argument predicate.
func binary(op string) func(string, string, []string) string {
	return func(col, geom string, _ []string) string {
		return col + " " + op + " " + geom
	}
}

// fn builds a render func for a two-argument PostGIS function.
func fn(name string) func(string, string, []string) string {
	return func(col, geom string, _ []string) string {
		return name + "(" + col + ", " + geom + ")"
	}
}

// Ops is the spatial operator vocabulary exposed on every geometry and
// geography column.
//
// Ordering matters only for schema readability: the index-usable bounding-box
// operators come first because they are the ones that stay fast on large
// tables, followed by the exact DE-9IM predicates. The list is append-only
// (everything after isNull was added for parity with
// postgraphile-plugin-connection-filter-postgis) so generated schemas only
// grow at the end.
//
// Operand shapes: bboxIntersects, bboxContains and bboxContainedBy take a
// BBoxInput envelope (the viewport case). Every other operator, including the
// directional and equality bounding-box operators (bboxLeftOf, bboxEquals,
// bboxIntersectsND, bboxIntersects2D, ...), takes a geometry, as connection-filter-postgis does:
// PostGIS compares against that geometry's bounding box, and a geometry can
// carry the Z/M extent that &&& needs where an envelope cannot.
var Ops = []Op{
	{
		Name: "bboxIntersects", Arg: "BBoxInput", Index: true, Geography: true,
		Description: "Bounding boxes overlap (&&). Index-usable; the cheap first pass for viewport queries.",
		render:      binary("&&"),
	},
	{
		Name: "bboxContains", Arg: "BBoxInput", Index: true,
		Description: "This column's bounding box contains the given box (~).",
		render:      binary("~"),
	},
	{
		Name: "bboxContainedBy", Arg: "BBoxInput", Index: true,
		Description: "This column's bounding box is contained by the given box (@).",
		render:      binary("@"),
	},
	{
		Name: "intersects", Arg: TypeName, Index: true, Geography: true,
		Description: "Geometries share any portion of space (ST_Intersects).",
		render:      fn("ST_Intersects"),
	},
	{
		Name: "disjoint", Arg: TypeName,
		Description: "Geometries share no space (ST_Disjoint). Cannot use an index.",
		render:      fn("ST_Disjoint"),
	},
	{
		Name: "contains", Arg: TypeName, Index: true,
		Description: "This column's geometry contains the given geometry (ST_Contains).",
		render:      fn("ST_Contains"),
	},
	{
		Name: "containsProperly", Arg: TypeName, Index: true,
		Description: "Contains with no boundary contact (ST_ContainsProperly).",
		render:      fn("ST_ContainsProperly"),
	},
	{
		Name: "within", Arg: TypeName, Index: true,
		Description: "This column's geometry lies within the given geometry (ST_Within).",
		render:      fn("ST_Within"),
	},
	{
		Name: "covers", Arg: TypeName, Index: true, Geography: true,
		Description: "No point of the given geometry lies outside this column (ST_Covers).",
		render:      fn("ST_Covers"),
	},
	{
		Name: "coveredBy", Arg: TypeName, Index: true, Geography: true,
		Description: "No point of this column lies outside the given geometry (ST_CoveredBy).",
		render:      fn("ST_CoveredBy"),
	},
	{
		Name: "crosses", Arg: TypeName, Index: true,
		Description: "Geometries cross (ST_Crosses).",
		render:      fn("ST_Crosses"),
	},
	{
		Name: "overlaps", Arg: TypeName, Index: true,
		Description: "Geometries overlap at the same dimension (ST_Overlaps).",
		render:      fn("ST_Overlaps"),
	},
	{
		Name: "touches", Arg: TypeName, Index: true,
		Description: "Geometries touch at a boundary but interiors do not intersect (ST_Touches).",
		render:      fn("ST_Touches"),
	},
	{
		Name: "equals", Arg: TypeName, Index: true,
		Description: "Geometries are spatially equal, ignoring vertex order (ST_Equals).",
		render:      fn("ST_Equals"),
	},
	{
		Name: "dwithin", Arg: "DWithinInput", Index: true, Geography: true,
		Description: "Within the given distance of the geometry (ST_DWithin). Distance is metres for geography, SRID units for geometry.",
		render: func(col, geom string, extra []string) string {
			return fmt.Sprintf("ST_DWithin(%s, %s, %s)", col, geom, extra[0])
		},
	},
	{
		Name: "beyond", Arg: "DWithinInput", Geography: true,
		Description: "Further than the given distance from the geometry (ST_DWithin negated).",
		render: func(col, geom string, extra []string) string {
			return fmt.Sprintf("NOT ST_DWithin(%s, %s, %s)", col, geom, extra[0])
		},
	},
	{
		Name: "isNull", Arg: "Boolean",
		Description: "Test whether the column is null.",
		render:      nil, // handled structurally by the compiler
	},
	{
		Name: "bboxLeftOf", Arg: TypeName, Index: true,
		Description: "This column's bounding box is strictly to the left of the given geometry's (<<).",
		render:      binary("<<"),
	},
	{
		Name: "bboxRightOf", Arg: TypeName, Index: true,
		Description: "This column's bounding box is strictly to the right of the given geometry's (>>).",
		render:      binary(">>"),
	},
	{
		Name: "bboxBelow", Arg: TypeName, Index: true,
		Description: "This column's bounding box is strictly below the given geometry's (<<|).",
		render:      binary("<<|"),
	},
	{
		Name: "bboxAbove", Arg: TypeName, Index: true,
		Description: "This column's bounding box is strictly above the given geometry's (|>>).",
		render:      binary("|>>"),
	},
	{
		Name: "bboxOverlapsOrLeftOf", Arg: TypeName, Index: true,
		Description: "This column's bounding box overlaps or is to the left of the given geometry's (&<).",
		render:      binary("&<"),
	},
	{
		Name: "bboxOverlapsOrRightOf", Arg: TypeName, Index: true,
		Description: "This column's bounding box overlaps or is to the right of the given geometry's (&>).",
		render:      binary("&>"),
	},
	{
		Name: "bboxOverlapsOrBelow", Arg: TypeName, Index: true,
		Description: "This column's bounding box overlaps or is below the given geometry's (&<|).",
		render:      binary("&<|"),
	},
	{
		Name: "bboxOverlapsOrAbove", Arg: TypeName, Index: true,
		Description: "This column's bounding box overlaps or is above the given geometry's (|&>).",
		render:      binary("|&>"),
	},
	{
		Name: "bboxEquals", Arg: TypeName, Index: true,
		Description: "This column's bounding box is the same as the given geometry's (~=).",
		render:      binary("~="),
	},
	{
		Name: "exactlyEquals", Arg: TypeName, Geography: true,
		Description: "Same coordinates in the same order and the same SRID (=). Uses a B-tree index, not a GiST one.",
		render:      binary("="),
	},
	{
		Name: "orderingEquals", Arg: TypeName, Index: true,
		Description: "Geometries are equal and their vertices are in the same order (ST_OrderingEquals).",
		render:      fn("ST_OrderingEquals"),
	},
	{
		Name: "intersects3D", Arg: TypeName, Index: true,
		Description: "Geometries share any portion of 3D space (ST_3DIntersects). The default 2D GiST index prefilters on the 2D bounding box; an n-D GiST index (gist_geometry_ops_nd) also prunes on Z.",
		render:      fn("ST_3DIntersects"),
	},
	{
		Name: "bboxIntersectsND", Arg: TypeName, Index: true,
		Description: "n-D bounding boxes (including Z and M) overlap (&&&). Uses an n-D GiST index (gist_geometry_ops_nd), not the default 2D one.",
		render:      binary("&&&"),
	},
	{
		Name: "bboxIntersects2D", Arg: TypeName, Index: true, Geography: true,
		Description: "This column's 2D bounding box overlaps the given geometry's (&&).",
		render:      binary("&&"),
	},
}

// OpByName looks up an operator by field name.
func OpByName(name string) (Op, bool) {
	for _, op := range Ops {
		if op.Name == name {
			return op, true
		}
	}
	return Op{}, false
}

// Render builds the SQL predicate for an operator.
func (o Op) Render(col, geom string, extra ...string) string {
	if o.render == nil {
		return ""
	}
	return o.render(col, geom, extra)
}

// BBoxSQL renders a bounding box as ST_MakeEnvelope over four numeric
// parameters. The placeholders are supplied by the caller so the values stay
// in the parameter list; only the SRID (an integer read from the query shape)
// is inlined, matching how the rest of the compiler treats structural values.
func BBoxSQL(minX, minY, maxX, maxY string, srid int, f Flavour) string {
	if srid == 0 {
		srid = defaultGeoJSONSRID
	}
	expr := fmt.Sprintf("ST_MakeEnvelope(%s, %s, %s, %s, %d)", minX, minY, maxX, maxY, srid)
	if f == Geography {
		expr = "(" + expr + ")::geography"
	}
	return expr
}

// OutputSQL renders a spatial column as the GeoJSON text the response carries.
//
// ST_AsGeoJSON returns text, so the result is re-parsed into jsonb to embed it
// as a real JSON object rather than a JSON-encoded string. No NULL guard is
// needed: ST_AsGeoJSON is STRICT (NULL in, NULL out) and NULL::jsonb is NULL —
// and ref may be a computed-column function call, which a CASE guard would
// evaluate twice.
func OutputSQL(ref string) string {
	return "ST_AsGeoJSON(" + ref + ")::jsonb"
}

// OutputJSONSQL is [OutputSQL] producing json instead of jsonb. It skips the
// jsonb parse and re-serialisation, so it is the cheaper choice when the value
// is only embedded in a response (json_build_object, json_agg) and never
// compared, grouped or aggregated with jsonb functions. Like [OutputSQL] it
// evaluates ref once and yields NULL for NULL.
func OutputJSONSQL(ref string) string {
	return "ST_AsGeoJSON(" + ref + ")::json"
}

// Derived is one computed spatial accessor exposed as a sibling field of a
// spatial column (e.g. `location` -> `locationArea`).
type Derived struct {
	// Suffix is appended to the column's field name.
	Suffix string
	// Type is the generated field's type name (pdbq SDL spelling).
	Type string
	// Description documents the field.
	Description string
	// GeoJSON marks accessors returning a geometry (rendered as GeoJSON).
	GeoJSON bool
	// sql builds the expression from the column reference and flavour.
	sql func(ref string, f Flavour) string
	// applies restricts the accessor to some typmod geometry types; nil
	// means every type.
	applies func(geomSubtype) bool
}

// Deriveds are the derived accessors generated for every spatial column.
//
// Each is a pure function of the column, so they compile into the same single
// statement as an ordinary column read — no extra round trip. Accessors that
// PostGIS only defines for one geometry type (X, Y, Z, M, StartPoint,
// EndPoint) render a CASE guard on ST_GeometryType that yields NULL for every
// other type instead of an error, so their SQL references the column twice.
// [Derived.Applies] reports which accessors are worth exposing once a
// column's typmod is known. The list is append-only so generated schemas
// only grow at the end.
var Deriveds = []Derived{
	{
		Suffix: "Area", Type: "Float",
		Description: "Area of the geometry (ST_Area): square metres for geography, SRID units for geometry.",
		sql:         func(ref string, _ Flavour) string { return "ST_Area(" + ref + ")" },
	},
	{
		Suffix: "Length", Type: "Float",
		Description: "Length/perimeter of the geometry (ST_Length): metres for geography, SRID units for geometry.",
		sql:         func(ref string, _ Flavour) string { return "ST_Length(" + ref + ")" },
	},
	{
		Suffix: "Perimeter", Type: "Float",
		Description: "Perimeter of a polygonal geometry (ST_Perimeter): metres for geography, SRID units for geometry.",
		sql:         func(ref string, _ Flavour) string { return "ST_Perimeter(" + ref + ")" },
	},
	{
		Suffix: "Centroid", Type: TypeName, GeoJSON: true,
		Description: "Centroid of the geometry (ST_Centroid), as GeoJSON; spheroidal for geography.",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				// ST_Centroid(geography) raises on a GeometryCollection.
				g := ref + "::geometry"
				return "CASE WHEN ST_GeometryType(" + g + ") = 'ST_GeometryCollection' THEN ST_Centroid(" + g + ") ELSE ST_Centroid(" + ref + ")::geometry END"
			}
			return "ST_Centroid(" + ref + ")"
		},
	},
	{
		Suffix: "Envelope", Type: TypeName, GeoJSON: true,
		Description: "Bounding box of the geometry (ST_Envelope), as GeoJSON.",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_Envelope(" + ref + "::geometry)"
			}
			return "ST_Envelope(" + ref + ")"
		},
	},
	{
		Suffix: "Srid", Type: "Int",
		Description: "Spatial reference identifier of the value (ST_SRID).",
		sql:         func(ref string, _ Flavour) string { return "ST_SRID(" + ref + ")" },
	},
	{
		Suffix: "GeometryType", Type: "String",
		// ST_GeometryType is the SQL/MM-standard spelling and returns the
		// "ST_"-prefixed name ("ST_Point"); the bare GeometryType() function
		// returns the unprefixed legacy form ("POINT").
		Description: "Geometry type name, e.g. ST_Point (ST_GeometryType).",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_GeometryType(" + ref + "::geometry)"
			}
			return "ST_GeometryType(" + ref + ")"
		},
	},
	{
		Suffix: "IsValid", Type: "Boolean",
		Description: "Whether the geometry is topologically valid (ST_IsValid).",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_IsValid(" + ref + "::geometry)"
			}
			return "ST_IsValid(" + ref + ")"
		},
	},
	{
		Suffix: "X", Type: "Float",
		Description: "X coordinate (longitude for geography) of a point (ST_X); null for other geometry types.",
		sql:         onlyTypes("ST_X", "ST_Point"),
		applies:     isPoint,
	},
	{
		Suffix: "Y", Type: "Float",
		Description: "Y coordinate (latitude for geography) of a point (ST_Y); null for other geometry types.",
		sql:         onlyTypes("ST_Y", "ST_Point"),
		applies:     isPoint,
	},
	{
		Suffix: "Z", Type: "Float",
		Description: "Z coordinate of a point (ST_Z); null for other geometry types or a point without Z.",
		sql:         onlyTypes("ST_Z", "ST_Point"),
		applies:     func(s geomSubtype) bool { return isPoint(s) && s.z },
	},
	{
		Suffix: "M", Type: "Float",
		Description: "M coordinate of a point (ST_M); null for other geometry types or a point without M.",
		sql:         onlyTypes("ST_M", "ST_Point"),
		applies:     func(s geomSubtype) bool { return isPoint(s) && s.m },
	},
	{
		Suffix: "NumPoints", Type: "Int",
		Description: "Number of vertices in the geometry (ST_NPoints).",
		sql:         unary("ST_NPoints"),
	},
	{
		Suffix: "NumGeometries", Type: "Int",
		Description: "Number of member geometries: 1 for a single geometry, 0 when empty (ST_NumGeometries).",
		sql:         unary("ST_NumGeometries"),
	},
	{
		Suffix: "StartPoint", Type: TypeName, GeoJSON: true,
		Description: "First point of a LineString, CircularString or CompoundCurve (ST_StartPoint), as GeoJSON; null for other geometry types.",
		sql:         onlyTypes("ST_StartPoint", curveTypes...),
		applies:     isCurve,
	},
	{
		Suffix: "EndPoint", Type: TypeName, GeoJSON: true,
		Description: "Last point of a LineString, CircularString or CompoundCurve (ST_EndPoint), as GeoJSON; null for other geometry types.",
		sql:         onlyTypes("ST_EndPoint", curveTypes...),
		applies:     isCurve,
	},
	{
		Suffix: "PointOnSurface", Type: TypeName, GeoJSON: true,
		Description: "A point guaranteed to lie on the geometry (ST_PointOnSurface), as GeoJSON.",
		sql:         unary("ST_PointOnSurface"),
	},
	{
		Suffix: "Wkt", Type: "String",
		Description: "Well-Known Text of the geometry, without SRID (ST_AsText).",
		sql:         func(ref string, _ Flavour) string { return "ST_AsText(" + ref + ")" },
	},
	{
		Suffix: "Ewkt", Type: "String",
		Description: "Extended Well-Known Text of the geometry, with an SRID=n; prefix (ST_AsEWKT).",
		sql:         func(ref string, _ Flavour) string { return "ST_AsEWKT(" + ref + ")" },
	},
	{
		Suffix: "IsEmpty", Type: "Boolean",
		Description: "Whether the geometry is empty (ST_IsEmpty).",
		sql:         unary("ST_IsEmpty"),
	},
	{
		Suffix: "IsSimple", Type: "Boolean",
		Description: "Whether the geometry has no self-intersections or self-tangencies (ST_IsSimple).",
		sql:         unary("ST_IsSimple"),
	},
	{
		Suffix: "Xmin", Type: "Float",
		Description: "Minimum X of the bounding box (ST_XMin); null when empty.",
		sql:         unary("ST_XMin"),
	},
	{
		Suffix: "Ymin", Type: "Float",
		Description: "Minimum Y of the bounding box (ST_YMin); null when empty.",
		sql:         unary("ST_YMin"),
	},
	{
		Suffix: "Xmax", Type: "Float",
		Description: "Maximum X of the bounding box (ST_XMax); null when empty.",
		sql:         unary("ST_XMax"),
	},
	{
		Suffix: "Ymax", Type: "Float",
		Description: "Maximum Y of the bounding box (ST_YMax); null when empty.",
		sql:         unary("ST_YMax"),
	},
}

// curveTypes are the single-curve types ST_EndPoint answers for. PostGIS 3.2+
// ST_StartPoint also answers for any other geometry, so both are guarded to
// keep the pair consistent.
var curveTypes = []string{"ST_LineString", "ST_CircularString", "ST_CompoundCurve"}

// asGeometry casts a geography reference to geometry for functions that have
// no geography overload.
func asGeometry(ref string, f Flavour) string {
	if f == Geography {
		return ref + "::geometry"
	}
	return ref
}

// unary builds a derived accessor calling a geometry-only PostGIS function.
func unary(name string) func(string, Flavour) string {
	return func(ref string, f Flavour) string {
		return name + "(" + asGeometry(ref, f) + ")"
	}
}

// onlyTypes builds a derived accessor calling a geometry-only PostGIS function
// that is NULL unless ST_GeometryType is one of types (ST_X raises an error on
// a non-point rather than returning NULL).
func onlyTypes(name string, types ...string) func(string, Flavour) string {
	return func(ref string, f Flavour) string {
		g := asGeometry(ref, f)
		quoted := make([]string, len(types))
		for i, t := range types {
			quoted[i] = "'" + t + "'"
		}
		cond := " = " + quoted[0]
		if len(quoted) > 1 {
			cond = " IN (" + strings.Join(quoted, ", ") + ")"
		}
		return "CASE WHEN ST_GeometryType(" + g + ")" + cond + " THEN " + name + "(" + g + ") END"
	}
}

// geomSubtype is a parsed typmod geometry type ("PointZ" -> point, z).
type geomSubtype struct {
	base string
	z, m bool
}

func isPoint(s geomSubtype) bool { return s.base == "point" }

func isCurve(s geomSubtype) bool {
	switch s.base {
	case "linestring", "circularstring", "compoundcurve":
		return true
	}
	return false
}

// typmodBases are the lower-cased geometry type names a typmod can carry.
var typmodBases = map[string]bool{
	"geometry": true, "point": true, "linestring": true, "polygon": true,
	"multipoint": true, "multilinestring": true, "multipolygon": true,
	"geometrycollection": true, "circularstring": true, "compoundcurve": true,
	"curvepolygon": true, "multicurve": true, "multisurface": true,
	"polyhedralsurface": true, "triangle": true, "tin": true,
}

// parseSubtype splits a typmod geometry type such as "PointZ",
// "MULTIPOLYGONZM" or a format_type spelling such as
// "public.geometry(PointZ,4326)" into its base type and dimension flags. ok
// is false for an empty or unrecognised spelling.
func parseSubtype(s string) (geomSubtype, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	if inner, ok := unwrapFormatType(t); ok {
		t = inner
	}
	if typmodBases[t] {
		return geomSubtype{base: t}, true
	}
	for _, suf := range []struct {
		s    string
		z, m bool
	}{{"zm", true, true}, {"z", true, false}, {"m", false, true}} {
		if base, ok := strings.CutSuffix(t, suf.s); ok && typmodBases[base] {
			return geomSubtype{base: base, z: suf.z, m: suf.m}, true
		}
	}
	return geomSubtype{}, false
}

// unwrapFormatType extracts the geometry type from a lower-cased format_type
// spelling: an optionally schema-qualified, optionally quoted geometry or
// geography type name followed by "(type[,srid])".
func unwrapFormatType(t string) (string, bool) {
	name, rest, ok := strings.Cut(t, "(")
	if !ok || !strings.HasSuffix(rest, ")") {
		return "", false
	}
	name = strings.ReplaceAll(strings.TrimSpace(name), `"`, "")
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	if name != "geometry" && name != "geography" {
		return "", false
	}
	typ, _, _ := strings.Cut(strings.TrimSuffix(rest, ")"), ",")
	return strings.TrimSpace(typ), true
}

// Applies reports whether the accessor can return a non-null value on a
// column whose typmod geometry type is subtype, spelled as
// postgis_typmod_type reports it ("Point", "PointZ", "MultiPolygonZM") or as
// the whole format_type ("geometry(PointZ,4326)"), case-insensitively. An
// empty or unrecognised subtype, or the generic "Geometry", leaves the column
// unconstrained and every accessor applies. Callers use it to skip accessors
// that would always be null, such as X on a LineString column or Z on a 2D
// Point column.
func (d Derived) Applies(subtype string) bool {
	return d.AppliesTo(subtype, false, false)
}

// AppliesTo is [Derived.Applies] with the typmod's Z and M dimensions given
// separately, for callers that hold the base type ("Point") and the
// dimension flags apart. hasZ and hasM add to any dimensions subtype already
// spells.
func (d Derived) AppliesTo(subtype string, hasZ, hasM bool) bool {
	if d.applies == nil {
		return true
	}
	st, ok := parseSubtype(subtype)
	if !ok || st.base == "geometry" {
		return true
	}
	st.z = st.z || hasZ
	st.m = st.m || hasM
	return d.applies(st)
}

// DerivedBySuffix looks up a derived accessor.
func DerivedBySuffix(suffix string) (Derived, bool) {
	for _, d := range Deriveds {
		if d.Suffix == suffix {
			return d, true
		}
	}
	return Derived{}, false
}

// SQL renders the derived accessor over a column reference, wrapping geometry
// valued results as GeoJSON.
func (d Derived) SQL(ref string, f Flavour) string {
	expr := d.sql(ref, f)
	if d.GeoJSON {
		return OutputSQL(expr)
	}
	return expr
}

// JSONSQL is [Derived.SQL] with geometry valued results rendered as json
// rather than jsonb (see [OutputJSONSQL]); other accessors are unchanged.
func (d Derived) JSONSQL(ref string, f Flavour) string {
	expr := d.sql(ref, f)
	if d.GeoJSON {
		return OutputJSONSQL(expr)
	}
	return expr
}

// TransformSQL renders the per-field `transform:` argument: reproject a value
// into another SRID. The SRID is an integer from the query shape, so inlining
// it keeps the value parameter list untouched.
func TransformSQL(ref string, srid int, f Flavour) string {
	if srid <= 0 {
		return ref
	}
	if f == Geography {
		return fmt.Sprintf("ST_Transform(%s::geometry, %d)", ref, srid)
	}
	return fmt.Sprintf("ST_Transform(%s, %d)", ref, srid)
}

// SimplifySQL renders the per-field `simplify:` argument: drop vertices under
// the given tolerance (ST_SimplifyPreserveTopology, which never produces an
// invalid geometry). The tolerance is a placeholder supplied by the caller.
func SimplifySQL(ref, tolerance string, f Flavour) string {
	if f == Geography {
		return fmt.Sprintf("ST_SimplifyPreserveTopology(%s::geometry, %s)", ref, tolerance)
	}
	return fmt.Sprintf("ST_SimplifyPreserveTopology(%s, %s)", ref, tolerance)
}

// DistanceSQL renders the ordering expression for distance-from-a-point sorts.
func DistanceSQL(col, geom string) string {
	return fmt.Sprintf("ST_Distance(%s, %s)", col, geom)
}

// KNNSQL renders the index-assisted nearest-neighbour operator (<->), which a
// GiST index can answer directly. Used for distance ordering because it is the
// operator PostGIS optimises for ORDER BY.
func KNNSQL(col, geom string) string {
	return fmt.Sprintf("%s <-> %s", col, geom)
}

// DerivedSuffixes returns the accessor suffixes in declaration order.
func DerivedSuffixes() []string {
	out := make([]string, len(Deriveds))
	for i, d := range Deriveds {
		out[i] = d.Suffix
	}
	return out
}

// OpNames returns the operator names in declaration order.
func OpNames() []string {
	out := make([]string, len(Ops))
	for i, op := range Ops {
		out[i] = op.Name
	}
	return out
}

// String renders the operator vocabulary for documentation/debugging.
func OpsDoc() string {
	var b strings.Builder
	for _, op := range Ops {
		fmt.Fprintf(&b, "%s(%s): %s\n", op.Name, op.Arg, op.Description)
	}
	return b.String()
}
