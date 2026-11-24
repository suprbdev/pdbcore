package postgis

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Op describes one spatial filter operator: its name (products expose it
// under their own grammar), the input shape it takes, and how it renders
// as SQL.
type Op struct {
	// Name is the operator's name, e.g. intersects or dwithin.
	Name string
	// OperandType names the operand's type: [TypeName] for a geometry,
	// "BBoxInput" for an envelope, "DWithinInput" for a geometry and
	// distance, "Boolean" for isNull. [Op.Kind] classifies the same shape.
	OperandType string
	// Description documents the operator in the generated schema.
	Description string
	// Indexable reports whether the operator can use a GiST index directly.
	// Index-usable operators are documented as such so users can tell the
	// cheap ones from the exact-but-expensive ones. For an operator without
	// a geography overload the flag describes geometry columns only: a
	// geography column is cast to geometry, which a GiST index on the
	// geography column cannot serve (an expression index on col::geometry
	// can).
	Indexable bool
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

// ops is the spatial operator vocabulary returned by [Ops].
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
var ops = []Op{
	{
		Name: "bboxIntersects", OperandType: "BBoxInput", Indexable: true, Geography: true,
		Description: "Bounding boxes overlap (&&). Index-usable; the cheap first pass for viewport queries.",
		render:      binary("&&"),
	},
	{
		Name: "bboxContains", OperandType: "BBoxInput", Indexable: true,
		Description: "This column's bounding box contains the given box (~).",
		render:      binary("~"),
	},
	{
		Name: "bboxContainedBy", OperandType: "BBoxInput", Indexable: true,
		Description: "This column's bounding box is contained by the given box (@).",
		render:      binary("@"),
	},
	{
		Name: "intersects", OperandType: TypeName, Indexable: true, Geography: true,
		Description: "Geometries share any portion of space (ST_Intersects).",
		render:      fn("ST_Intersects"),
	},
	{
		Name: "disjoint", OperandType: TypeName,
		Description: "Geometries share no space (ST_Disjoint). Cannot use an index.",
		render:      fn("ST_Disjoint"),
	},
	{
		Name: "contains", OperandType: TypeName, Indexable: true,
		Description: "This column's geometry contains the given geometry (ST_Contains).",
		render:      fn("ST_Contains"),
	},
	{
		Name: "containsProperly", OperandType: TypeName, Indexable: true,
		Description: "Contains with no boundary contact (ST_ContainsProperly).",
		render:      fn("ST_ContainsProperly"),
	},
	{
		Name: "within", OperandType: TypeName, Indexable: true,
		Description: "This column's geometry lies within the given geometry (ST_Within).",
		render:      fn("ST_Within"),
	},
	{
		Name: "covers", OperandType: TypeName, Indexable: true, Geography: true,
		Description: "No point of the given geometry lies outside this column (ST_Covers).",
		render:      fn("ST_Covers"),
	},
	{
		Name: "coveredBy", OperandType: TypeName, Indexable: true, Geography: true,
		Description: "No point of this column lies outside the given geometry (ST_CoveredBy).",
		render:      fn("ST_CoveredBy"),
	},
	{
		Name: "crosses", OperandType: TypeName, Indexable: true,
		Description: "Geometries cross (ST_Crosses).",
		render:      fn("ST_Crosses"),
	},
	{
		Name: "overlaps", OperandType: TypeName, Indexable: true,
		Description: "Geometries overlap at the same dimension (ST_Overlaps).",
		render:      fn("ST_Overlaps"),
	},
	{
		Name: "touches", OperandType: TypeName, Indexable: true,
		Description: "Geometries touch at a boundary but interiors do not intersect (ST_Touches).",
		render:      fn("ST_Touches"),
	},
	{
		Name: "equals", OperandType: TypeName, Indexable: true,
		Description: "Geometries are spatially equal, ignoring vertex order (ST_Equals).",
		render:      fn("ST_Equals"),
	},
	{
		Name: "dwithin", OperandType: "DWithinInput", Indexable: true, Geography: true,
		Description: "Within the given distance of the geometry (ST_DWithin). Distance is metres for geography, SRID units for geometry.",
		render: func(col, geom string, extra []string) string {
			return fmt.Sprintf("ST_DWithin(%s, %s, %s)", col, geom, extra[0])
		},
	},
	{
		Name: "beyond", OperandType: "DWithinInput", Geography: true,
		Description: "Further than the given distance from the geometry (ST_DWithin negated).",
		render: func(col, geom string, extra []string) string {
			return fmt.Sprintf("NOT ST_DWithin(%s, %s, %s)", col, geom, extra[0])
		},
	},
	{
		Name: "isNull", OperandType: "Boolean",
		Description: "Test whether the column is null.",
		render:      nil, // handled structurally by the compiler
	},
	{
		Name: "bboxLeftOf", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box is strictly to the left of the given geometry's (<<).",
		render:      binary("<<"),
	},
	{
		Name: "bboxRightOf", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box is strictly to the right of the given geometry's (>>).",
		render:      binary(">>"),
	},
	{
		Name: "bboxBelow", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box is strictly below the given geometry's (<<|).",
		render:      binary("<<|"),
	},
	{
		Name: "bboxAbove", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box is strictly above the given geometry's (|>>).",
		render:      binary("|>>"),
	},
	{
		Name: "bboxOverlapsOrLeftOf", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box overlaps or is to the left of the given geometry's (&<).",
		render:      binary("&<"),
	},
	{
		Name: "bboxOverlapsOrRightOf", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box overlaps or is to the right of the given geometry's (&>).",
		render:      binary("&>"),
	},
	{
		Name: "bboxOverlapsOrBelow", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box overlaps or is below the given geometry's (&<|).",
		render:      binary("&<|"),
	},
	{
		Name: "bboxOverlapsOrAbove", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box overlaps or is above the given geometry's (|&>).",
		render:      binary("|&>"),
	},
	{
		Name: "bboxEquals", OperandType: TypeName, Indexable: true,
		Description: "This column's bounding box is the same as the given geometry's (~=).",
		render:      binary("~="),
	},
	{
		Name: "exactlyEquals", OperandType: TypeName, Geography: true,
		Description: "Same coordinates in the same order and the same SRID (=). Uses a B-tree index, not a GiST one.",
		render:      binary("="),
	},
	{
		Name: "orderingEquals", OperandType: TypeName, Indexable: true,
		Description: "Geometries are equal and their vertices are in the same order (ST_OrderingEquals).",
		render:      fn("ST_OrderingEquals"),
	},
	{
		Name: "intersects3D", OperandType: TypeName, Indexable: true,
		Description: "Geometries share any portion of 3D space (ST_3DIntersects). The default 2D GiST index prefilters on the 2D bounding box; an n-D GiST index (gist_geometry_ops_nd) also prunes on Z.",
		render:      fn("ST_3DIntersects"),
	},
	{
		Name: "bboxIntersectsND", OperandType: TypeName, Indexable: true,
		Description: "n-D bounding boxes (including Z and M) overlap (&&&). Uses an n-D GiST index (gist_geometry_ops_nd), not the default 2D one.",
		render:      binary("&&&"),
	},
	{
		Name: "bboxIntersects2D", OperandType: TypeName, Indexable: true, Geography: true,
		Description: "This column's 2D bounding box overlaps the given geometry's (&&).",
		render:      binary("&&"),
	},
}

// Ops returns the spatial operator vocabulary exposed on every geometry
// and geography column, in declaration order: the index-usable
// bounding-box operators first, then the exact DE-9IM predicates. The list
// is append-only, so generated schemas only grow at the end. The slice is
// a fresh copy.
func Ops() []Op { return slices.Clone(ops) }

// OpByName looks up an operator by name.
func OpByName(name string) (Op, bool) {
	for _, op := range ops {
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

// Output selects how [OutputSQL] renders GeoJSON. The zero Output renders
// jsonb with PostGIS's default precision.
type Output struct {
	// JSON renders json instead of jsonb. It skips the jsonb parse and
	// re-serialisation, so it is the cheaper choice when the value is only
	// embedded in a response (json_build_object, json_agg) and never
	// compared, grouped or aggregated with jsonb functions.
	JSON bool
	// Precision rounds the coordinates to that many decimal places
	// (ST_AsGeoJSON's maxdecimaldigits; PostGIS defaults to 9, 6 is about
	// 10 cm on lon/lat data and shrinks payloads substantially). It is the
	// placeholder of an integer parameter validated with
	// [CheckPrecision], so the SQL text does not depend on its value; ""
	// keeps the default.
	Precision string
}

// OutputSQL renders a spatial value as the GeoJSON a response carries.
//
// ST_AsGeoJSON returns text, so the result is re-parsed into jsonb (or json,
// see [Output.JSON]) to embed it as a real JSON object rather than a
// JSON-encoded string. No NULL guard is needed: ST_AsGeoJSON is STRICT (NULL
// in, NULL out) and NULL::jsonb is NULL — and ref may be a computed-column
// function call, which a CASE guard would evaluate twice.
func OutputSQL(ref string, o Output) string {
	args := ref
	if o.Precision != "" {
		args += ", " + cast(o.Precision, "int4")
	}
	typ := "jsonb"
	if o.JSON {
		typ = "json"
	}
	return "ST_AsGeoJSON(" + args + ")::" + typ
}

// MaxPrecision is the largest GeoJSON precision consumers accept: 15
// decimal places already resolve far below a micrometre on lon/lat data, and
// a request without a precision keeps PostGIS's default of 9.
const MaxPrecision = 15

// CheckPrecision validates a GeoJSON precision argument (the number of
// decimal digits of each coordinate). The error is user-facing.
func CheckPrecision(digits int64) error {
	if digits < 0 || digits > MaxPrecision {
		return fmt.Errorf("precision must be an integer between 0 and %d", MaxPrecision)
	}
	return nil
}

// BufferCost is the cost, in column reads per returned row, a consumer's cost
// estimate charges for a buffer projection argument ([BufferSQL]):
// ST_Buffer builds a new polygon around every vertex of the value.
const BufferCost = 5

// CheckBuffer validates a buffer distance: any finite number, negative
// distances shrinking polygons (and erasing points and lines). The error is
// user-facing.
func CheckBuffer(distance float64) error {
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return fmt.Errorf("buffer must be a finite number")
	}
	return nil
}

// BufferSQL renders the per-field `buffer` projection argument: the area
// within distance of the value (ST_Buffer), in metres for geography and in
// the SRID's units for geometry. The result keeps the flavour of ref, so a
// geography value stays geography. distance is the placeholder of a float8
// parameter validated with [CheckBuffer].
func BufferSQL(ref, distance string) string {
	return "ST_Buffer(" + ref + ", " + cast(distance, "float8") + ")"
}

// cast renders placeholder::type, parenthesizing anything but a bare $n
// placeholder so the cast applies to the whole expression.
func cast(ph, typ string) string {
	if len(ph) > 1 && ph[0] == '$' && strings.Trim(ph[1:], "0123456789") == "" {
		return ph + "::" + typ
	}
	return "(" + ph + ")::" + typ
}

// Derived is one computed spatial accessor exposed as a sibling field of a
// spatial column (e.g. `location` -> `locationArea`).
type Derived struct {
	// Suffix is appended to the column's field name.
	Suffix string
	// Type is the generated field's type name: Boolean, Int, Float, String
	// or [TypeName].
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

// deriveds backs [Deriveds]. The list is append-only.
var deriveds = []Derived{
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

// Deriveds returns the derived accessors generated for every spatial
// column, in declaration order (append-only, so generated schemas only
// grow at the end). Each is a pure function of the column, so it compiles
// into the same single statement as an ordinary column read. Accessors
// that PostGIS only defines for one geometry type (X, Y, Z, M, StartPoint,
// EndPoint) render a CASE guard on ST_GeometryType that yields NULL for
// every other type instead of an error, so their SQL references the
// column twice. [Derived.AppliesTo] reports which accessors are worth
// exposing once a column's typmod is known. The slice is a fresh copy.
func Deriveds() []Derived { return slices.Clone(deriveds) }

// DerivedBySuffix looks up a derived accessor.
func DerivedBySuffix(suffix string) (Derived, bool) {
	for _, d := range deriveds {
		if d.Suffix == suffix {
			return d, true
		}
	}
	return Derived{}, false
}

// SQL renders the derived accessor over a column reference, rendering a
// geometry valued result as GeoJSON with o (see [OutputSQL]); accessors
// that return no geometry ignore o.
func (d Derived) SQL(ref string, f Flavour, o Output) string {
	expr := d.sql(ref, f)
	if d.GeoJSON {
		return OutputSQL(expr, o)
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

// DistanceSQL renders the distance between col and geom (ST_Distance), the
// value a nearest-first ordering sorts by: in the units of the SRID for
// geometry (degrees for 4326), in metres on the spheroid for geography,
// where geom must be geography too. Both arguments are evaluated once.
func DistanceSQL(col, geom string) string {
	return fmt.Sprintf("ST_Distance(%s, %s)", col, geom)
}

// SpheroidDistanceSQL renders the distance in metres on the WGS 84
// spheroid between a geometry col and geom, by casting both to geography
// (the PostGIS idiom for lon/lat data stored as geometry, as in
// ST_DWithin's useSpheroid form). The geometries must carry a lon/lat SRID
// (4326 or another geodetic one); PostgreSQL rejects the cast otherwise.
func SpheroidDistanceSQL(col, geom string) string {
	return fmt.Sprintf("ST_Distance((%s)::geography, (%s)::geography)", col, geom)
}

// KNNSQL renders the index-assisted nearest-neighbour operator (<->), which a
// GiST index can answer directly. Used for distance ordering because it is the
// operator PostGIS optimises for ORDER BY.
func KNNSQL(col, geom string) string {
	return fmt.Sprintf("%s <-> %s", col, geom)
}
