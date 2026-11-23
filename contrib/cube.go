package contrib

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxCubeDims is the cube extension's limit on the number of dimensions of
// a cube value (CUBE_MAX_DIM).
const MaxCubeDims = 100

// CubeParam coerces a cube value into the text parameter a cube cast reads:
// a non-empty list of finite numbers is a point ("(1, 2, 3)"), a list of two
// such lists of equal length is a box given by two opposite corners
// ("(1, 2),(3, 4)"), and a string is passed through in the cube text form
// for Postgres to validate.
func CubeParam(v any) (string, error) {
	switch in := v.(type) {
	case string:
		if strings.TrimSpace(in) == "" {
			return "", fmt.Errorf("cube value must not be empty")
		}
		return in, nil
	case []float64:
		return cubeCorner(in)
	case []any:
		if len(in) == 2 {
			lo, lok := in[0].([]any)
			hi, hok := in[1].([]any)
			if lok && hok {
				if len(lo) != len(hi) {
					return "", fmt.Errorf("cube box corners must have the same number of coordinates")
				}
				a, err := cubeList(lo)
				if err != nil {
					return "", err
				}
				b, err := cubeList(hi)
				if err != nil {
					return "", err
				}
				return a + "," + b, nil
			}
		}
		return cubeList(in)
	}
	return "", fmt.Errorf("cube value must be a list of numbers, a pair of such lists or a cube literal string")
}

func cubeList(in []any) (string, error) {
	if len(in) > MaxCubeDims {
		return "", fmt.Errorf("cube value has more than %d coordinates", MaxCubeDims)
	}
	nums := make([]float64, len(in))
	for i, e := range in {
		n, ok := vectorElement(e)
		if !ok {
			return "", fmt.Errorf("cube value must be a list of numbers, a pair of such lists or a cube literal string")
		}
		nums[i] = n
	}
	return cubeCorner(nums)
}

func cubeCorner(nums []float64) (string, error) {
	switch {
	case len(nums) == 0:
		return "", fmt.Errorf("cube value must have at least one coordinate")
	case len(nums) > MaxCubeDims:
		return "", fmt.Errorf("cube value has more than %d coordinates", MaxCubeDims)
	}
	var b strings.Builder
	b.WriteByte('(')
	for i, n := range nums {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", fmt.Errorf("cube coordinates must be finite numbers")
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatFloat(n, 'g', -1, 64))
	}
	b.WriteByte(')')
	return b.String(), nil
}

// CubeDistanceParams coerces a distance threshold against a reference cube,
// an object {cube, distance}, into the cube text parameter and the
// threshold.
func CubeDistanceParams(v any) ([]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("operand must be an object with cube and distance")
	}
	ref, ok := m["cube"]
	if !ok || ref == nil {
		return nil, fmt.Errorf("operand requires a cube")
	}
	s, err := CubeParam(ref)
	if err != nil {
		return nil, err
	}
	d, ok := m["distance"]
	if !ok || d == nil {
		return nil, fmt.Errorf("operand requires a distance")
	}
	f, err := finiteNumber(d)
	if err != nil {
		return nil, fmt.Errorf("distance %w", err)
	}
	return []any{s, f}, nil
}

// LatLonParams coerces a point on the Earth's surface, an object {lat, lon}
// of finite numbers in degrees (latitude within ±90, longitude within ±180),
// into its two float8 parameters, latitude first.
func LatLonParams(v any) ([]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("location must be an object with lat and lon")
	}
	lat, lon, err := latLon(m)
	if err != nil {
		return nil, err
	}
	return []any{lat, lon}, nil
}

// LatLonDistanceParams coerces a radius around a point on the Earth's
// surface, an object {lat, lon, distance}, into its three float8
// parameters: latitude, longitude and distance.
func LatLonDistanceParams(v any) ([]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("operand must be an object with lat, lon and distance")
	}
	lat, lon, err := latLon(m)
	if err != nil {
		return nil, err
	}
	d, ok := m["distance"]
	if !ok || d == nil {
		return nil, fmt.Errorf("operand requires a distance")
	}
	f, err := finiteNumber(d)
	if err != nil {
		return nil, fmt.Errorf("distance %w", err)
	}
	return []any{lat, lon, f}, nil
}

func latLon(m map[string]any) (float64, float64, error) {
	var out [2]float64
	for i, k := range []struct {
		name  string
		limit float64
	}{{"lat", 90}, {"lon", 180}} {
		raw, ok := m[k.name]
		if !ok || raw == nil {
			return 0, 0, fmt.Errorf("location requires %s", k.name)
		}
		f, err := finiteNumber(raw)
		if err != nil {
			return 0, 0, fmt.Errorf("%s %w", k.name, err)
		}
		if f < -k.limit || f > k.limit {
			return 0, 0, fmt.Errorf("%s must be between -%g and %g", k.name, k.limit, k.limit)
		}
		out[i] = f
	}
	return out[0], out[1], nil
}

// cubeMetric is one cube distance operator.
type cubeMetric struct{ name, op, label string }

var cubeMetrics = []cubeMetric{
	{"l2Distance", "<->", "Euclidean (L2) distance"},
	{"l1Distance", "<#>", "taxicab (L1) distance"},
	{"chebyshevDistance", "<=>", "Chebyshev (L-infinity) distance"},
}

// cubeOutput renders a cube reference as JSON: a point as a list of its
// coordinates, a box as the pair of its corners. The cube text form,
// "(1, 2)" or "(1, 2),(3, 4)", becomes "[[1, 2]]" or "[[1, 2],[3, 4]]",
// whose single element is unwrapped for a point. ref is evaluated once.
func cubeOutput(typ, ref string) string {
	return "(SELECT CASE WHEN " + typ + "_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || " +
		"translate((" + ref + ")::text, '()', '[]') || ']')::" + typ + ") AS __cube(j))"
}

// Cube returns the vocabulary of the cube extension's cube type: the
// containment and overlap operators (contains @>, containedBy <@, overlaps
// &&), the Euclidean, taxicab and Chebyshev distances to a reference cube
// as nearest-first ordering expressions and selectable scores
// ([Expr.Score]: l2Distance <->, l1Distance <#>, chebyshevDistance <=>),
// a threshold filter per distance (<distance>LessThan, taking {cube,
// distance}) and a codec reading and writing cubes as JSON lists.
//
// A GiST index on the column serves all of it: the operators directly, the
// orderings as KNN scans, and the thresholds through an overlap prefilter
// with the reference box enlarged by the distance in every dimension
// (cube_enlarge), which holds every cube within that distance whatever the
// metric, before the exact distance comparison.
//
// Values are lists of numbers for points ([1, 2, 3]) and pairs of corner
// lists for boxes ([[1, 2], [3, 4]]), or the cube text form as a string;
// output takes the same JSON shape. Non-finite coordinates cannot be
// output as JSON.
func Cube() Vocabulary {
	idx := []IndexHint{{Method: "gist"}}
	knn := []IndexHint{{Method: "gist", Ordering: true}}
	coerceCube := func(v any) ([]any, error) {
		s, err := CubeParam(v)
		if err != nil {
			return nil, err
		}
		return []any{s}, nil
	}
	infix := func(op string) func(q qual, col string, a []string) string {
		return func(q qual, col string, a []string) string {
			return paren(col) + " " + q.op(op) + " " + q.cast(a[0], "cube")
		}
	}
	ops := []Op{
		{Name: "contains", Arg: ArgCube, Index: idx, Extension: ExtCube, params: coerceCube, render: infix("@>"),
			Description: "Contains the given cube (@>)."},
		{Name: "containedBy", Arg: ArgCube, Index: idx, Extension: ExtCube, params: coerceCube, render: infix("<@"),
			Description: "Is contained by the given cube (<@)."},
		{Name: "overlaps", Arg: ArgCube, Index: idx, Extension: ExtCube, params: coerceCube, render: infix("&&"),
			Description: "Overlaps the given cube (&&)."},
	}
	var exprs []Expr
	for _, m := range cubeMetrics {
		dist := func(q qual, col, ph string) string {
			return paren(col) + " " + q.op(m.op) + " " + q.cast(ph, "cube")
		}
		exprs = append(exprs, Expr{
			Name: m.name, Arg: ArgCube, Type: "float8", Ascending: true, Index: knn, Extension: ExtCube, Score: true,
			Description: strings.ToUpper(m.label[:1]) + m.label[1:] + " to the given cube (" + m.op + ").",
			params:      coerceCube,
			render:      func(q qual, col string, a []string) string { return dist(q, col, a[0]) },
		})
		ops = append(ops, Op{
			Name: m.name + LessThanSuffix, Arg: ArgCubeDistance, Index: idx, Extension: ExtCube,
			Description: "The " + m.label + " (" + m.op + ") to the given cube is less than the given distance. " +
				"A GiST index serves it through an overlap prefilter with the cube enlarged by the distance.",
			params: CubeDistanceParams,
			render: func(q qual, col string, a []string) string {
				ref := q.cast(a[0], "cube")
				return "(" + paren(col) + " " + q.op("&&") + " " + q.fn("cube_enlarge") + "(" + ref + ", " + atom(a[1]) + "::float8, " +
					q.fn("cube_dim") + "(" + ref + ")) AND (" + dist(q, col, a[0]) + ") < " + atom(a[1]) + "::float8)"
			},
		})
	}
	return Vocabulary{
		Name: VocabCube, Type: "cube", Extension: ExtCube,
		Ops: ops, Exprs: exprs,
		Codec: &Codec{
			Arg:        ArgCube,
			params:     coerceCube,
			sql:        func(q qual, a []string) string { return q.cast(a[0], "cube") },
			output:     func(_ qual, ref string) string { return cubeOutput("jsonb", ref) },
			outputJSON: func(_ qual, ref string) string { return cubeOutput("json", ref) },
		},
	}
}

// IsEarth reports whether a column declared as the given domain ref
// ("schema.name", see [introspect.Column.Domain]) is earthdistance's earth
// domain installed in schema extSchema.
func IsEarth(domain, extSchema string) bool {
	return domain != "" && domain == extSchema+".earth"
}

// Earth returns earthdistance's vocabulary of an earth column, a point on
// the Earth's surface stored as a cube domain: the great-circle distance in
// metres to a reference location (earthDistance, a nearest-first ordering
// and selectable score taking {lat, lon}), its radius filter
// (earthDistanceLessThan, taking {lat, lon, distance} in metres) and a
// codec reading and writing locations as {lat, lon} objects in degrees.
//
// cubeSchema is the schema the cube extension lives in, whose operators
// the vocabulary renders; Bind the result to earthdistance's schema. A
// GiST index on the column serves both: the filter is the index-friendly
// earth_box(ll_to_earth(lat, lon), distance) @> column prefilter refined
// by earth_distance (the box also holds points slightly farther away), and
// nearest-first ordering ([Expr.OrderSQL]) sorts by cube's <-> distance
// between the points, the chord through the Earth, which orders exactly
// like the great-circle distance and is a KNN scan.
//
// earthdistance before 1.2 (PostgreSQL 15 and earlier) calls the cube
// functions unqualified, so its schema and cube's must be on the
// search_path there.
func Earth(cubeSchema string) Vocabulary {
	cq := qual{schema: cubeSchema}
	ll := func(q qual, lat, lon string) string {
		return q.fn("ll_to_earth") + "(" + atom(lat) + "::float8, " + atom(lon) + "::float8)"
	}
	distance := func(q qual, col string, a []string) string {
		return q.fn("earth_distance") + "(" + paren(col) + ", " + ll(q, a[0], a[1]) + ")"
	}
	output := func(typ string) func(q qual, ref string) string {
		return func(q qual, ref string) string {
			return "(SELECT CASE WHEN __earth.e IS NULL THEN NULL ELSE " + typ + "_build_object('lat', " + q.fn("latitude") +
				"(__earth.e), 'lon', " + q.fn("longitude") + "(__earth.e)) END FROM (SELECT " + ref + " AS e) AS __earth)"
		}
	}
	return Vocabulary{
		Name: VocabEarth, Type: "earth", Extension: ExtEarthdistance,
		Ops: []Op{{
			Name: "earthDistance" + LessThanSuffix, Arg: ArgLatLonDistance, Extension: ExtEarthdistance,
			Index: []IndexHint{{Method: "gist"}},
			Description: "The great-circle distance in metres to the given location is less than the given distance " +
				"(earth_box prefilter, which a GiST index serves, refined by earth_distance).",
			params: LatLonDistanceParams,
			render: func(q qual, col string, a []string) string {
				return "(" + q.fn("earth_box") + "(" + ll(q, a[0], a[1]) + ", " + atom(a[2]) + "::float8) " + cq.op("@>") + " " + paren(col) +
					" AND " + distance(q, col, a) + " < " + atom(a[2]) + "::float8)"
			},
		}},
		Exprs: []Expr{{
			Name: "earthDistance", Arg: ArgLatLon, Type: "float8", Ascending: true, Extension: ExtEarthdistance, Score: true,
			Index:       []IndexHint{{Method: "gist", Ordering: true}},
			Description: "Great-circle distance in metres to the given location (earth_distance).",
			params:      LatLonParams,
			render:      distance,
			order: func(q qual, col string, a []string) string {
				return paren(col) + " " + cq.op("<->") + " " + ll(q, a[0], a[1])
			},
		}},
		Accessors: []Accessor{
			{Name: "latitude", Type: "float8", Description: "Latitude in degrees (latitude).",
				sql: func(q qual, ref string) string { return q.fn("latitude") + "(" + ref + ")" }},
			{Name: "longitude", Type: "float8", Description: "Longitude in degrees (longitude).",
				sql: func(q qual, ref string) string { return q.fn("longitude") + "(" + ref + ")" }},
		},
		Codec: &Codec{
			Arg:        ArgLatLon,
			params:     LatLonParams,
			sql:        func(q qual, a []string) string { return ll(q, a[0], a[1]) },
			output:     output("jsonb"),
			outputJSON: output("json"),
		},
	}
}

// EarthPoint returns earthdistance's vocabulary over a point column holding
// (longitude, latitude) in degrees: the <@> distance in statute miles to a
// reference location as an ordering expression and selectable score
// (earthDistanceMiles, taking {lat, lon}) and its threshold filter
// (earthDistanceMilesLessThan, taking {lat, lon, distance} in miles). The
// point type is core, so the vocabulary has no codec; it applies only when
// earthdistance is installed.
//
// No index serves <@>: the filter and the ordering scan. Store an earth
// column (or index ll_to_earth over the coordinates) for index-assisted
// radius search.
func EarthPoint() Vocabulary {
	miles := func(q qual, col, lat, lon string) string {
		return paren(col) + " " + q.op("<@>") + " point(" + atom(lon) + "::float8, " + atom(lat) + "::float8)"
	}
	return Vocabulary{
		Name: VocabEarthPoint, Type: "point", Extension: ExtEarthdistance,
		Ops: []Op{{
			Name: "earthDistanceMiles" + LessThanSuffix, Arg: ArgLatLonDistance, Extension: ExtEarthdistance,
			Description: "The distance in statute miles (<@>) to the given location is less than the given distance. " +
				"No index serves it.",
			params: LatLonDistanceParams,
			render: func(q qual, col string, a []string) string {
				return "(" + miles(q, col, a[0], a[1]) + ") < " + atom(a[2]) + "::float8"
			},
		}},
		Exprs: []Expr{{
			Name: "earthDistanceMiles", Arg: ArgLatLon, Type: "float8", Ascending: true, Extension: ExtEarthdistance, Score: true,
			Description: "Distance in statute miles to the given location, the point being (longitude, latitude) (<@>).",
			params:      LatLonParams,
			render:      func(q qual, col string, a []string) string { return miles(q, col, a[0], a[1]) },
		}},
	}
}
