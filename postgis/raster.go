package postgis

import (
	"slices"
	"strings"
)

// RasterTypeName is the pg type name of a postgis_raster column.
const RasterTypeName = "raster"

// ExtRaster is the extension providing the raster type (PostGIS 3 split it
// out of postgis).
const ExtRaster = "postgis_raster"

// IsRaster reports whether a pg type name (as Column.PGType spells it,
// optionally schema-qualified or quoted) is the PostGIS raster type; arrays
// of rasters are not.
func IsRaster(pgType string) bool {
	t := strings.ReplaceAll(strings.TrimSpace(pgType), `"`, "")
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	return t == RasterTypeName
}

// Relative costs of the raster surface, in units of one column read, for
// the products' cost estimators. The metadata accessors read the raster
// header only and cost one; a pixel value detoasts the raster and locates
// the pixel; summary statistics visit every pixel of a band; the
// intersects filter tests each candidate row's raster against the
// geometry (with nband omitted, against the raster's convex hull).
const (
	RasterAccessorCost     = 1
	RasterValueCost        = 10
	RasterSummaryStatsCost = 100
	RasterIntersectsCost   = 10
)

// RasterAccessor is one metadata accessor of a raster column, exposed as a
// sibling field (rast -> rastWidth) or pseudo-column.
type RasterAccessor struct {
	// Suffix is appended to the column's field name.
	Suffix string
	// Type is the generated field's type name: Int, Float or [TypeName].
	Type string
	// Description documents the field.
	Description string
	// GeoJSON marks accessors returning a geometry (rendered as GeoJSON).
	GeoJSON bool

	sql func(ref string) string
}

// SQL renders the accessor over a raster reference, rendering a geometry
// valued result as GeoJSON with o (see [OutputSQL]); accessors that return
// no geometry ignore o. It is NULL for a NULL raster.
func (a RasterAccessor) SQL(ref string, o Output) string {
	if a.GeoJSON {
		return OutputSQL(a.sql(ref), o)
	}
	return a.sql(ref)
}

func rasterFn(name string) func(string) string {
	return func(ref string) string { return name + "(" + ref + ")" }
}

// RasterAccessors returns the metadata accessors of a raster column. They
// read only the raster header, so they are cheap whatever the raster's
// size. The list is append-only so generated schemas only grow at the end.
// The slice is a fresh copy.
func RasterAccessors() []RasterAccessor { return slices.Clone(rasterAccessors) }

var rasterAccessors = []RasterAccessor{
	{Suffix: "Width", Type: "Int", Description: "Width of the raster in pixels (ST_Width).", sql: rasterFn("ST_Width")},
	{Suffix: "Height", Type: "Int", Description: "Height of the raster in pixels (ST_Height).", sql: rasterFn("ST_Height")},
	{Suffix: "NumBands", Type: "Int", Description: "Number of bands of the raster (ST_NumBands).", sql: rasterFn("ST_NumBands")},
	{Suffix: "Srid", Type: "Int", Description: "Spatial reference identifier of the raster (ST_SRID).", sql: rasterFn("ST_SRID")},
	{Suffix: "PixelWidth", Type: "Float", Description: "Width of a pixel in the units of the spatial reference system (ST_PixelWidth).", sql: rasterFn("ST_PixelWidth")},
	{Suffix: "PixelHeight", Type: "Float", Description: "Height of a pixel in the units of the spatial reference system (ST_PixelHeight).", sql: rasterFn("ST_PixelHeight")},
	{Suffix: "Envelope", Type: TypeName, GeoJSON: true, Description: "Extent of the raster as a polygon (ST_Envelope), as GeoJSON.", sql: rasterFn("ST_Envelope")},
}

// RasterAccessorBySuffix looks up a raster metadata accessor.
func RasterAccessorBySuffix(suffix string) (RasterAccessor, bool) {
	for _, a := range rasterAccessors {
		if a.Suffix == suffix {
			return a, true
		}
	}
	return RasterAccessor{}, false
}

// RasterValueSQL renders the pixel value (ST_Value, float8) of a raster
// reference at a point: geom is a geometry expression (a [Value] rendered
// for [Geometry]) and band the placeholder of the 1-based band number. The
// point is reprojected to the raster's SRID when both carry a different
// non-zero one and otherwise takes the raster's, so a GeoJSON point (WGS 84)
// samples a raster stored in any projection. NODATA pixels, points outside
// the raster and missing bands yield NULL. ref and geom are evaluated once.
func RasterValueSQL(ref, geom, band string) string {
	return "(SELECT ST_Value(__rv.r, " + band + "::int4, CASE WHEN ST_SRID(__rv.p) = ST_SRID(__rv.r) OR ST_SRID(__rv.p) = 0 OR ST_SRID(__rv.r) = 0 " +
		"THEN ST_SetSRID(__rv.p, ST_SRID(__rv.r)) ELSE ST_Transform(__rv.p, ST_SRID(__rv.r)) END, true) FROM (SELECT " +
		ref + " AS r, " + geom + " AS p) AS __rv)"
}

// RasterSummaryStatsSQL renders the summary statistics of one band of a
// raster reference (ST_SummaryStats, NODATA pixels excluded) as a jsonb
// object {count, sum, mean, stddev, min, max}; band is the placeholder of
// the 1-based band number. It is NULL for a NULL raster or a missing band.
// Every pixel of the band is read, so it is expensive on large rasters.
func RasterSummaryStatsSQL(ref, band string) string {
	return "to_jsonb(ST_SummaryStats(" + ref + ", " + band + "::int4, true))"
}

// RasterSummaryStatsJSONSQL is [RasterSummaryStatsSQL] producing json.
func RasterSummaryStatsJSONSQL(ref, band string) string {
	return "to_json(ST_SummaryStats(" + ref + ", " + band + "::int4, true))"
}

// RasterSummaryStatsFields are the members of a summary statistics object,
// with the pdbq SDL type of each.
var RasterSummaryStatsFields = []struct{ Name, Type, Description string }{
	{"count", "Int", "Number of pixels counted (NODATA pixels excluded)."},
	{"sum", "Float", "Sum of the pixel values."},
	{"mean", "Float", "Arithmetic mean of the pixel values."},
	{"stddev", "Float", "Population standard deviation of the pixel values."},
	{"min", "Float", "Minimum pixel value."},
	{"max", "Float", "Maximum pixel value."},
}

// RasterIntersectsSQL renders the raster intersects filter: whether the
// raster's convex hull and the geometry expression geom share any space
// (ST_Intersects(raster, geometry), which inlines to an && prefilter on
// raster::geometry before the exact test). A GiST expression index on
// ST_ConvexHull(column) serves the prefilter. geom must be in the raster's
// SRID: a row whose bounding box overlaps a geometry in another SRID raises
// an error (pgexec.Classify treats it as user input), other rows just do not
// match.
func RasterIntersectsSQL(ref, geom string) string {
	return "ST_Intersects(" + ref + ", " + geom + ")"
}
