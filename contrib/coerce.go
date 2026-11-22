package contrib

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
