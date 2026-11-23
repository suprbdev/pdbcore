package contrib

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// coerce converts a decoded JSON operand (or the equivalent Go values) into
// bind parameters for an operand kind. Range kinds are handled by their
// vocabulary, which knows the element type.
func coerce(k ArgKind, v any) ([]any, error) {
	switch k {
	case ArgText:
		s, err := Text(v)
		if err != nil {
			return nil, err
		}
		return []any{s}, nil
	case ArgTextList:
		l, err := TextList(v)
		if err != nil {
			return nil, err
		}
		return []any{l}, nil
	case ArgFullText:
		q, err := ParseFullTextQuery(v)
		if err != nil {
			return nil, err
		}
		return q.Params(), nil
	case ArgHstore:
		h, err := HstoreParam(v)
		if err != nil {
			return nil, err
		}
		return []any{h}, nil
	case ArgVector:
		s, err := VectorParam(v)
		if err != nil {
			return nil, err
		}
		return []any{s}, nil
	case ArgTextDistance:
		s, d, err := TextDistanceParam(v)
		if err != nil {
			return nil, err
		}
		return []any{s, d}, nil
	case ArgSubpath:
		return SubpathParams(v)
	case ArgCube:
		c, err := CubeParam(v)
		if err != nil {
			return nil, err
		}
		return []any{c}, nil
	case ArgCubeDistance:
		return CubeDistanceParams(v)
	case ArgLatLon:
		return LatLonParams(v)
	case ArgLatLonDistance:
		return LatLonDistanceParams(v)
	case ArgRange, ArgMultirange, ArgElement:
		return nil, fmt.Errorf("%s operand needs a range type", k)
	}
	return nil, fmt.Errorf("unsupported operand kind %d", k)
}

// Text coerces a string operand.
func Text(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("operand must be a string")
	}
	return s, nil
}

// TextList coerces a list-of-strings operand ([]any of strings or
// []string). Null elements are rejected; a nil slice becomes an empty list,
// never a NULL parameter.
func TextList(v any) ([]string, error) {
	switch l := v.(type) {
	case []string:
		if l == nil {
			return []string{}, nil
		}
		return l, nil
	case []any:
		out := make([]string, len(l))
		for i, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("operand must be a list of strings")
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("operand must be a list of strings")
}

// HstoreParam coerces a JSON object of string keys to string or null values
// into the alternating key/value text[] parameter that hstore(text[])
// accepts. Keys are sorted so the parameter is deterministic.
func HstoreParam(v any) ([]*string, error) {
	var m map[string]*string
	switch in := v.(type) {
	case map[string]*string:
		m = in
	case map[string]string:
		m = make(map[string]*string, len(in))
		for k, s := range in {
			m[k] = &s
		}
	case map[string]any:
		m = make(map[string]*string, len(in))
		for k, e := range in {
			switch s := e.(type) {
			case nil:
				m[k] = nil
			case string:
				m[k] = &s
			default:
				return nil, fmt.Errorf("hstore value for key %q must be a string or null", clip(k))
			}
		}
	default:
		return nil, fmt.Errorf("hstore value must be an object of strings")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*string, 0, 2*len(keys))
	for _, k := range keys {
		out = append(out, &k, m[k])
	}
	return out, nil
}

// element renders a range element operand as the text form the element
// type's input function accepts; integral element types must be whole
// numbers.
func element(v any, integral bool) (string, error) {
	var s string
	switch n := v.(type) {
	case string:
		s = n
	case json.Number:
		s = n.String()
	case float64:
		if integral && n != float64(int64(n)) {
			return "", fmt.Errorf("range bound must be an integer")
		}
		s = strconv.FormatFloat(n, 'f', -1, 64)
	case float32:
		return element(float64(n), integral)
	case int:
		s = strconv.Itoa(n)
	case int32:
		s = strconv.FormatInt(int64(n), 10)
	case int64:
		s = strconv.FormatInt(n, 10)
	case time.Time:
		s = n.Format(time.RFC3339Nano)
	default:
		return "", fmt.Errorf("range bound must be a number or a string")
	}
	if integral {
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "", fmt.Errorf("range bound %q must be an integer", clip(s))
		}
	}
	return s, nil
}

// clip bounds untrusted text echoed back in error messages.
func clip(s string) string {
	const limit = 40
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// Integer coerces a whole JSON number (or the equivalent Go integer, or a
// string of decimal digits) to an int64.
func Integer(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, fmt.Errorf("must be an integer")
		}
		return int64(n), nil
	case float32:
		return Integer(float64(n))
	case json.Number:
		return Integer(n.String())
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("must be an integer")
		}
		return i, nil
	}
	return 0, fmt.Errorf("must be an integer")
}

// int4 coerces an integer operand that must lie in [lo, hi].
func int4(v any, lo, hi int64) (int32, error) {
	i, err := Integer(v)
	if err != nil {
		return 0, err
	}
	if i < lo || i > hi {
		return 0, fmt.Errorf("must be between %d and %d", lo, hi)
	}
	return int32(i), nil
}

// TextDistanceParam coerces an [ArgTextDistance] operand, an object {text,
// distance} with a non-negative integer distance, into its text and
// threshold.
func TextDistanceParam(v any) (string, int32, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", 0, fmt.Errorf("operand must be an object with text and distance")
	}
	t, ok := m["text"]
	if !ok || t == nil {
		return "", 0, fmt.Errorf("operand requires a text")
	}
	s, err := Text(t)
	if err != nil {
		return "", 0, fmt.Errorf("text %w", err)
	}
	d, ok := m["distance"]
	if !ok || d == nil {
		return "", 0, fmt.Errorf("operand requires a distance")
	}
	n, err := int4(d, 0, math.MaxInt32)
	if err != nil {
		return "", 0, fmt.Errorf("distance %w", err)
	}
	return s, n, nil
}

// MaxLtreeLevels is the most labels an ltree path can have.
const MaxLtreeLevels = 65535

// SubpathParams coerces an [ArgSubpath] operand, an object {offset,
// length} with length optional, into two parameters: the offset (int32,
// within ±[MaxLtreeLevels]) and the non-negative length (int32), or nil
// when length is absent or null, meaning the rest of the path.
func SubpathParams(v any) ([]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("operand must be an object with offset and length")
	}
	o, ok := m["offset"]
	if !ok || o == nil {
		return nil, fmt.Errorf("operand requires an offset")
	}
	off, err := int4(o, -MaxLtreeLevels, MaxLtreeLevels)
	if err != nil {
		return nil, fmt.Errorf("offset %w", err)
	}
	l, ok := m["length"]
	if !ok || l == nil {
		return []any{off, nil}, nil
	}
	n, err := int4(l, 0, MaxLtreeLevels)
	if err != nil {
		return nil, fmt.Errorf("length %w", err)
	}
	return []any{off, n}, nil
}
