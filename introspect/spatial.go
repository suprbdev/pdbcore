package introspect

import (
	"fmt"
	"strconv"
	"strings"
)

// SpatialSubtype is the geometry type a PostGIS column is constrained to, as
// PostGIS names it in a type modifier (without the Z/M dimension suffix).
// The constants cover the OGC simple-feature types; curve and surface types
// (CircularString, Tin, ...) are carried through under their PostGIS name.
type SpatialSubtype string

const (
	// SpatialGeometry is the unconstrained subtype: any geometry type is
	// accepted.
	SpatialGeometry           SpatialSubtype = "Geometry"
	SpatialPoint              SpatialSubtype = "Point"
	SpatialLineString         SpatialSubtype = "LineString"
	SpatialPolygon            SpatialSubtype = "Polygon"
	SpatialMultiPoint         SpatialSubtype = "MultiPoint"
	SpatialMultiLineString    SpatialSubtype = "MultiLineString"
	SpatialMultiPolygon       SpatialSubtype = "MultiPolygon"
	SpatialGeometryCollection SpatialSubtype = "GeometryCollection"
)

var knownSubtypes = []SpatialSubtype{
	SpatialGeometry, SpatialPoint, SpatialLineString, SpatialPolygon,
	SpatialMultiPoint, SpatialMultiLineString, SpatialMultiPolygon,
	SpatialGeometryCollection,
}

// SpatialType is the type modifier of a geometry or geography column, e.g.
// geometry(PointZ,4326) is {Subtype: Point, SRID: 4326, HasZ: true}. A bare
// geometry or geography column is {Subtype: Geometry, SRID: 0}.
type SpatialType struct {
	Subtype SpatialSubtype `json:"subtype"`
	// SRID is the spatial reference the column enforces; 0 means any SRID is
	// accepted. PostGIS fills in 4326 for a geography modifier that names
	// only a subtype, so a constrained geography column always reports one.
	SRID int  `json:"srid"`
	HasZ bool `json:"has_z"`
	HasM bool `json:"has_m"`
}

// Constrained reports whether the column restricts subtype, SRID or
// dimensions at all. An empty Subtype counts as Geometry.
func (s *SpatialType) Constrained() bool {
	return s.subtype() != SpatialGeometry || s.SRID != 0 || s.HasZ || s.HasM
}

func (s *SpatialType) subtype() SpatialSubtype {
	if s.Subtype == "" {
		return SpatialGeometry
	}
	return s.Subtype
}

// Typmod renders the modifier in PostGIS syntax, e.g. "(PointZ,4326)", or ""
// for an unconstrained column; an empty Subtype renders as Geometry.
// Appending it to the base type name yields a type usable in a cast:
// "geometry" + "(Point,4326)".
func (s *SpatialType) Typmod() string {
	if !s.Constrained() {
		return ""
	}
	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(string(s.subtype()))
	if s.HasZ {
		b.WriteByte('Z')
	}
	if s.HasM {
		b.WriteByte('M')
	}
	if s.SRID != 0 {
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(s.SRID))
	}
	b.WriteByte(')')
	return b.String()
}

// parseSpatialTypmod decodes format_type output for a geometry or geography
// type, such as "public.geometry(PointZ,4326)" or "geography(Polygon,4326)[]".
// The type name may be schema-qualified (and quoted) depending on the
// search_path, so only the trailing parenthesised modifier is inspected. Text
// without a modifier yields the unconstrained type.
func parseSpatialTypmod(formatted string) (*SpatialType, error) {
	s := formatted
	for strings.HasSuffix(s, "[]") {
		s = strings.TrimSuffix(s, "[]")
	}
	out := &SpatialType{Subtype: SpatialGeometry}
	if !strings.HasSuffix(s, ")") {
		return out, nil
	}
	open := strings.LastIndexByte(s, '(')
	if open < 0 {
		return nil, fmt.Errorf("spatial typmod %q: unbalanced parentheses", formatted)
	}
	inner := s[open+1 : len(s)-1]
	name, sridText, hasSRID := strings.Cut(inner, ",")
	name = strings.TrimSpace(name)
	if hasSRID {
		srid, err := strconv.Atoi(strings.TrimSpace(sridText))
		if err != nil || srid < 0 {
			return nil, fmt.Errorf("spatial typmod %q: invalid SRID", formatted)
		}
		out.SRID = srid
	}
	switch {
	case strings.HasSuffix(name, "ZM"):
		out.HasZ, out.HasM = true, true
		name = strings.TrimSuffix(name, "ZM")
	case strings.HasSuffix(name, "Z"):
		out.HasZ = true
		name = strings.TrimSuffix(name, "Z")
	case strings.HasSuffix(name, "M"):
		out.HasM = true
		name = strings.TrimSuffix(name, "M")
	}
	if name == "" || strings.IndexFunc(name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	}) >= 0 {
		return nil, fmt.Errorf("spatial typmod %q: invalid geometry type", formatted)
	}
	out.Subtype = SpatialSubtype(name)
	for _, k := range knownSubtypes {
		if strings.EqualFold(name, string(k)) {
			out.Subtype = k
			break
		}
	}
	return out, nil
}

// applyTypmod fills Column.Spatial and Column.SQLType from the column's
// format_type text (empty when it has none), whether its base type is built
// in, and whether it is PostGIS geometry or geography (or an array of
// either). A spatial modifier that does not parse degrades to the
// unconstrained type rather than failing introspection; PostgreSQL still
// enforces the real constraint on write.
func applyTypmod(col *Column, formatted string, builtin, spatial bool) {
	switch {
	case spatial:
		st, err := parseSpatialTypmod(formatted)
		if err != nil {
			st = &SpatialType{Subtype: SpatialGeometry}
		}
		col.Spatial = st
	case builtin && formatted != "":
		col.SQLType = formatted
	}
}
