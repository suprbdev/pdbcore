package contrib

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// MaxVectorDims bounds the number of elements a vector operand or input
// value may have: pgvector's limit for vector and halfvec columns, and for
// the non-zero elements of a sparsevec.
const MaxVectorDims = 16000

// VectorTypes returns the pgvector column types the vector vocabulary
// covers. The slice is a fresh copy.
func VectorTypes() []string { return slices.Clone(vectorTypes) }

var vectorTypes = []string{"vector", "halfvec", "sparsevec"}

// IsVector reports whether a pg type name (as Column.PGType spells it) is
// one of [VectorTypes], ignoring a schema qualifier; arrays are not.
func IsVector(pgType string) bool {
	base, arr := normalizeType(pgType)
	if arr {
		return false
	}
	for _, t := range vectorTypes {
		if base == t {
			return true
		}
	}
	return false
}

// VectorParam coerces a dense vector value — a non-empty list of finite
// numbers, or a string in pgvector's text form ("[1,2,3]") — into the text
// parameter a vector or halfvec cast reads. Lists are rendered in the
// shortest form that round-trips float64; strings are passed through for
// Postgres to validate.
func VectorParam(v any) (string, error) {
	nums, s, err := vectorInput(v)
	if err != nil || nums == nil {
		return s, err
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, n := range nums {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(n, 'g', -1, 64))
	}
	b.WriteByte(']')
	return b.String(), nil
}

// SparseVectorParam coerces a sparsevec value — a dense list of finite
// numbers (zeros are dropped) or a string in pgvector's sparse text form
// ("{1:1.5,3:2}/5") — into the text parameter a sparsevec cast reads.
func SparseVectorParam(v any) (string, error) {
	nums, s, err := vectorInput(v)
	if err != nil || nums == nil {
		return s, err
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for i, n := range nums {
		if n == 0 {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteByte(':')
		b.WriteString(strconv.FormatFloat(n, 'g', -1, 64))
	}
	b.WriteString("}/")
	b.WriteString(strconv.Itoa(len(nums)))
	return b.String(), nil
}

// vectorInput splits a vector value into its numbers (a list) or its text
// form (a string; nums is nil).
func vectorInput(v any) ([]float64, string, error) {
	switch in := v.(type) {
	case string:
		if strings.TrimSpace(in) == "" {
			return nil, "", fmt.Errorf("vector value must not be empty")
		}
		return nil, in, nil
	case []float64:
		return checkVector(in)
	case []float32:
		out := make([]float64, len(in))
		for i, n := range in {
			out[i] = float64(n)
		}
		return checkVector(out)
	case []any:
		if len(in) > MaxVectorDims {
			return nil, "", fmt.Errorf("vector value has more than %d elements", MaxVectorDims)
		}
		out := make([]float64, len(in))
		for i, e := range in {
			n, ok := vectorElement(e)
			if !ok {
				return nil, "", fmt.Errorf("vector value must be a list of numbers")
			}
			out[i] = n
		}
		return checkVector(out)
	}
	return nil, "", fmt.Errorf("vector value must be a list of numbers or a vector literal string")
}

func checkVector(nums []float64) ([]float64, string, error) {
	switch {
	case len(nums) == 0:
		return nil, "", fmt.Errorf("vector value must have at least one element")
	case len(nums) > MaxVectorDims:
		return nil, "", fmt.Errorf("vector value has more than %d elements", MaxVectorDims)
	}
	for _, n := range nums {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, "", fmt.Errorf("vector elements must be finite numbers")
		}
	}
	return nums, "", nil
}

func vectorElement(e any) (float64, bool) {
	switch n := e.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// vectorMetric is one pgvector distance operator.
type vectorMetric struct {
	name, op, opclass, desc, label string
	ivfflat                        bool
	since                          string
}

var vectorMetrics = []vectorMetric{
	{"l2Distance", "<->", "l2", "Euclidean (L2) distance to the given vector (<->).", "Euclidean (L2) distance", true, ""},
	{"negativeInnerProduct", "<#>", "ip", "Negative inner product with the given vector (<#>); smallest is most similar.", "negative inner product", true, ""},
	{"cosineDistance", "<=>", "cosine", "Cosine distance to the given vector, 1 - cosine similarity (<=>).", "cosine distance", true, ""},
	{"l1Distance", "<+>", "l1", "Taxicab (L1) distance to the given vector (<+>).", "taxicab (L1) distance", false, "0.7.0"},
}

var bitMetrics = []vectorMetric{
	{"hammingDistance", "<~>", "hamming", "Hamming distance to the given bit string (<~>): the number of differing bits.", "Hamming distance", true, "0.7.0"},
	{"jaccardDistance", "<%>", "jaccard", "Jaccard distance to the given bit string (<%>), 1 - |a AND b| / |a OR b|.", "Jaccard distance", false, "0.7.0"},
}

// LessThanSuffix is appended to a distance expression's name to name the
// filter operator comparing that distance with a threshold
// ("cosineDistance" -> "cosineDistanceLessThan").
const LessThanSuffix = "LessThan"

// metricSurface builds the distance expressions and threshold filter
// operators of a pgvector metric set over a column type: opType names the
// index operator classes (<opType>_<metric>_ops), cast renders a bound
// operand and params coerces one.
func metricSurface(metrics []vectorMetric, opType string, ivfflat bool, cast func(q qual, ph string) string, params func(any) (string, error)) ([]Expr, []Op) {
	coerceVec := func(v any) ([]any, error) {
		s, err := params(v)
		if err != nil {
			return nil, err
		}
		return []any{s}, nil
	}
	coerceThreshold := func(v any) ([]any, error) {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("operand must be an object with vector and distance")
		}
		ref, ok := m["vector"]
		if !ok || ref == nil {
			return nil, fmt.Errorf("operand requires a vector")
		}
		s, err := params(ref)
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
	exprs := make([]Expr, len(metrics))
	ops := make([]Op, len(metrics))
	for i, m := range metrics {
		idx := []IndexHint{{Method: "hnsw", OpClass: opType + "_" + m.opclass + "_ops", Ordering: true}}
		if m.ivfflat && ivfflat {
			idx = append(idx, IndexHint{Method: "ivfflat", OpClass: opType + "_" + m.opclass + "_ops", Ordering: true})
		}
		dist := func(q qual, col, ph string) string {
			return paren(col) + " " + q.op(m.op) + " " + cast(q, ph)
		}
		exprs[i] = Expr{
			Name: m.name, Arg: ArgVector, Type: "float8", Ascending: true, Index: idx, Extension: ExtVector, Score: true,
			Description: m.desc,
			since:       m.since,
			params:      coerceVec,
			render:      func(q qual, col string, a []string) string { return dist(q, col, a[0]) },
		}
		ops[i] = Op{
			Name: m.name + LessThanSuffix, Arg: ArgVectorDistance, Extension: ExtVector,
			Description: "The " + m.label + " (" + m.op + ") to the given vector is less than the given distance. " +
				"No index serves it; combine it with nearest-first ordering by the same distance.",
			since:  m.since,
			params: coerceThreshold,
			render: func(q qual, col string, a []string) string {
				return "(" + dist(q, col, a[0]) + ") < " + atom(a[1]) + "::float8"
			},
		}
	}
	return exprs, ops
}

// finiteNumber coerces a JSON number (or numeric string) to a finite
// float64.
func finiteNumber(v any) (float64, error) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case float32:
		f = float64(n)
	case int:
		f = float64(n)
	case int32:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		x, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("must be a number")
		}
		f = x
	case string:
		x, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, fmt.Errorf("must be a number")
		}
		f = x
	default:
		return 0, fmt.Errorf("must be a number")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("must be a finite number")
	}
	return f, nil
}

// Vector returns the pgvector vocabulary of a vector, halfvec or sparsevec
// column: the distance expressions for nearest-first ordering against a
// reference vector (each also a selectable [Expr.Score]), a threshold
// filter per distance (<distance>LessThan, e.g. cosineDistanceLessThan:
// distance < threshold, taking {vector, distance}), the dims and norm
// accessors, and a codec taking a list of numbers (or the type's text
// form).
//
// Every distance is a smaller-is-closer value ([Expr.Ascending]); an HNSW
// (or, except for L1 and sparsevec, IVFFlat) index built with the matching
// operator class serves ORDER BY distance LIMIT n as an approximate
// nearest-neighbour scan. The threshold filters are not index-served (no
// [IndexHint]): pgvector indexes answer ordering, not range predicates. The
// operand is cast to the column's own type, so it must have the column's
// dimensions.
//
// vector and halfvec values are output as JSON arrays of numbers (their
// text form already is one); sparsevec values keep their text form.
func Vector(typeName string) (Vocabulary, bool) {
	if !IsVector(typeName) {
		return Vocabulary{}, false
	}
	typeName, _ = normalizeType(typeName)
	sparse := typeName == "sparsevec"
	params := VectorParam
	if sparse {
		params = SparseVectorParam
	}
	coerceVec := func(v any) ([]any, error) {
		s, err := params(v)
		if err != nil {
			return nil, err
		}
		return []any{s}, nil
	}
	exprs, ops := metricSurface(vectorMetrics, typeName, !sparse,
		func(q qual, ph string) string { return q.cast(ph, typeName) }, params)
	norm := "vector_norm"
	if typeName != "vector" {
		norm = "l2_norm"
	}
	accessors := []Accessor{{
		Name: "norm", Type: "float8",
		Description: "Euclidean norm of the vector (" + norm + ").",
		sql:         func(q qual, ref string) string { return q.fn(norm) + "(" + ref + ")" },
	}}
	if !sparse {
		accessors = append([]Accessor{{
			Name: "dims", Type: "int4",
			Description: "Number of dimensions (vector_dims).",
			sql:         func(q qual, ref string) string { return q.fn("vector_dims") + "(" + ref + ")" },
		}}, accessors...)
	}
	codec := &Codec{
		Arg:    ArgVector,
		params: coerceVec,
		sql:    func(q qual, a []string) string { return q.cast(a[0], typeName) },
	}
	if !sparse {
		codec.output = func(_ qual, ref string) string { return "(" + ref + ")::text::jsonb" }
		codec.outputJSON = func(_ qual, ref string) string { return "(" + ref + ")::text::json" }
	}
	return Vocabulary{
		Name: VocabVector, Type: typeName, Extension: ExtVector,
		Ops: ops, Exprs: exprs, Accessors: accessors, Codec: codec,
	}, true
}

// MaxBitDims bounds the length of a bit string operand: pgvector's HNSW
// limit for bit columns.
const MaxBitDims = 64000

// BitParam coerces a bit string operand — a string of 0 and 1 ("1010") or
// a non-empty list of 0/1 numbers or booleans — into the text parameter a
// varbit cast reads.
func BitParam(v any) (string, error) {
	switch in := v.(type) {
	case string:
		s := strings.TrimSpace(in)
		if s == "" {
			return "", fmt.Errorf("bit string must not be empty")
		}
		if len(s) > MaxBitDims {
			return "", fmt.Errorf("bit string has more than %d bits", MaxBitDims)
		}
		if strings.Trim(s, "01") != "" {
			return "", fmt.Errorf("bit string must consist of 0 and 1")
		}
		return s, nil
	case []any:
		if len(in) == 0 {
			return "", fmt.Errorf("bit string must not be empty")
		}
		if len(in) > MaxBitDims {
			return "", fmt.Errorf("bit string has more than %d bits", MaxBitDims)
		}
		b := make([]byte, len(in))
		for i, e := range in {
			switch x := e.(type) {
			case bool:
				b[i] = '0'
				if x {
					b[i] = '1'
				}
			default:
				n, ok := vectorElement(e)
				if !ok || (n != 0 && n != 1) {
					return "", fmt.Errorf("bit list elements must be 0, 1, true or false")
				}
				b[i] = '0' + byte(n)
			}
		}
		return string(b), nil
	}
	return "", fmt.Errorf("bit value must be a string of 0 and 1 or a list of bits")
}

// Bit returns pgvector's vocabulary over a bit or varbit column: the
// Hamming (<~>) and Jaccard (<%>) distances to a reference bit string, as
// nearest-first ordering expressions and selectable scores
// ([Expr.Score]), and their threshold filters (hammingDistanceLessThan,
// jaccardDistanceLessThan). The bit type is core, so the vocabulary has no
// codec; it applies only when pgvector (0.7 or later) is installed.
//
// An HNSW index built with bit_hamming_ops or bit_jaccard_ops (IVFFlat:
// bit_hamming_ops) serves the ordering. Operands are cast to varbit, which
// PostgreSQL coerces to bit implicitly, so a reference of the column's
// length compares with bit(n) columns; a length mismatch is rejected by
// pgvector. Pair a bit column holding binary_quantize(embedding) with
// re-ranking by the full vector's distance for binary-quantized search.
func Bit(typeName string) (Vocabulary, bool) {
	typeName, arr := normalizeType(typeName)
	if arr || (typeName != "bit" && typeName != "varbit") {
		return Vocabulary{}, false
	}
	exprs, ops := metricSurface(bitMetrics, "bit", true,
		func(_ qual, ph string) string { return atom(ph) + "::varbit" }, BitParam)
	return Vocabulary{
		Name: VocabBit, Type: typeName, Extension: ExtVector,
		Ops: ops, Exprs: exprs,
	}, true
}
