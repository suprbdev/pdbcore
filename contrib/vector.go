package contrib

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxVectorDims bounds the number of elements a vector operand or input
// value may have: pgvector's limit for vector and halfvec columns, and for
// the non-zero elements of a sparsevec.
const MaxVectorDims = 16000

// VectorTypes lists the pgvector column types the vector vocabulary covers.
var VectorTypes = []string{"vector", "halfvec", "sparsevec"}

// IsVector reports whether a pg type name (as Column.PGType spells it) is
// one of [VectorTypes], ignoring a schema qualifier; arrays are not.
func IsVector(pgType string) bool {
	base, arr := normalizeType(pgType)
	if arr {
		return false
	}
	for _, t := range VectorTypes {
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
	name, op, opclass, desc string
	ivfflat                 bool
	since                   string
}

var vectorMetrics = []vectorMetric{
	{"l2Distance", "<->", "l2", "Euclidean (L2) distance to the given vector (<->).", true, ""},
	{"negativeInnerProduct", "<#>", "ip", "Negative inner product with the given vector (<#>); smallest is most similar.", true, ""},
	{"cosineDistance", "<=>", "cosine", "Cosine distance to the given vector, 1 - cosine similarity (<=>).", true, ""},
	{"l1Distance", "<+>", "l1", "Taxicab (L1) distance to the given vector (<+>).", false, "0.7.0"},
}

// Vector returns the pgvector vocabulary of a vector, halfvec or sparsevec
// column: the distance expressions for nearest-first ordering against a
// reference vector, the dims and norm accessors, and a codec taking a list
// of numbers (or the type's text form).
//
// Every distance is a smaller-is-closer value ([Expr.Ascending]); an HNSW
// (or, except for L1 and sparsevec, IVFFlat) index built with the matching
// operator class serves ORDER BY distance LIMIT n as an approximate
// nearest-neighbour scan. The operand is cast to the column's own type, so
// it must have the column's dimensions.
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
	exprs := make([]Expr, len(vectorMetrics))
	for i, m := range vectorMetrics {
		idx := []IndexHint{{Method: "hnsw", OpClass: typeName + "_" + m.opclass + "_ops", Ordering: true}}
		if m.ivfflat && !sparse {
			idx = append(idx, IndexHint{Method: "ivfflat", OpClass: typeName + "_" + m.opclass + "_ops", Ordering: true})
		}
		exprs[i] = Expr{
			Name: m.name, Arg: ArgVector, Type: "float8", Ascending: true, Index: idx, Extension: ExtVector,
			Description: m.desc,
			since:       m.since,
			params:      coerceVec,
			render: func(q qual, col string, a []string) string {
				return paren(col) + " " + q.op(m.op) + " " + q.cast(a[0], typeName)
			},
		}
	}
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
		Exprs: exprs, Accessors: accessors, Codec: codec,
	}, true
}
