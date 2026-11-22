package contrib

import (
	"fmt"
	"sort"
	"strings"
)

// rangeType describes a built-in range type.
type rangeType struct {
	name, elem, multi string
	integral          bool
	// sentinel is an element value: [sentinel, sentinel) is how an empty
	// range travels through the constructor.
	sentinel string
}

var rangeTypes = map[string]rangeType{
	"int4range": {name: "int4range", elem: "int4", multi: "int4multirange", integral: true, sentinel: "0"},
	"int8range": {name: "int8range", elem: "int8", multi: "int8multirange", integral: true, sentinel: "0"},
	"numrange":  {name: "numrange", elem: "numeric", multi: "nummultirange", sentinel: "0"},
	"tsrange":   {name: "tsrange", elem: "timestamp", multi: "tsmultirange", sentinel: "2000-01-01 00:00:00"},
	"tstzrange": {name: "tstzrange", elem: "timestamptz", multi: "tstzmultirange", sentinel: "2000-01-01 00:00:00+00"},
	"daterange": {name: "daterange", elem: "date", multi: "datemultirange", sentinel: "2000-01-01"},
}

func multirangeType(name string) (rangeType, bool) {
	for _, rt := range rangeTypes {
		if rt.multi == name {
			return rt, true
		}
	}
	return rangeType{}, false
}

// RangeTypes returns the built-in range type names the range vocabulary
// covers, sorted. Their multirange counterparts (PostgreSQL 14+) are
// covered by [Multirange].
func RangeTypes() []string {
	out := make([]string, 0, len(rangeTypes))
	for n := range rangeTypes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// RangeValue is a structured range: the API shape of range values in and out.
type RangeValue struct {
	// Start and End are the bounds in the element type's text form; nil is
	// unbounded (infinite).
	Start, End *string
	// StartInclusive and EndInclusive select [ or ( and ] or ). Postgres
	// normalizes an unbounded side to exclusive, and discrete types (int4,
	// int8, date) to [).
	StartInclusive, EndInclusive bool
	// Empty is the empty range; the other fields are ignored.
	Empty bool
}

// Bounds returns the constructor's bounds argument, e.g. "[)".
func (r RangeValue) Bounds() string {
	b := "("
	if r.StartInclusive {
		b = "["
	}
	if r.EndInclusive {
		return b + "]"
	}
	return b + ")"
}

// ParseRange coerces a range operand: an object {start, end,
// startInclusive, endInclusive, isEmpty} (start/end null or absent is
// unbounded; inclusivity defaults to [)), or a range literal string. Bounds
// that are not strings or numbers are rejected; an integral element type
// also rejects non-integers. Element syntax is otherwise left to Postgres.
func ParseRange(v any, integral bool) (RangeValue, error) {
	switch in := v.(type) {
	case RangeValue:
		return in, nil
	case string:
		r, err := ParseRangeLiteral(in)
		if err != nil {
			return RangeValue{}, err
		}
		return r, checkIntegral(r, integral)
	case map[string]any:
		r := RangeValue{StartInclusive: true}
		for k, e := range in {
			switch k {
			case "start", "end":
				if e == nil {
					continue
				}
				s, err := element(e, integral)
				if err != nil {
					return RangeValue{}, err
				}
				if k == "start" {
					r.Start = &s
				} else {
					r.End = &s
				}
			case "startInclusive", "endInclusive", "isEmpty":
				if e == nil {
					continue
				}
				b, ok := e.(bool)
				if !ok {
					return RangeValue{}, fmt.Errorf("range %s must be a boolean", k)
				}
				switch k {
				case "startInclusive":
					r.StartInclusive = b
				case "endInclusive":
					r.EndInclusive = b
				default:
					r.Empty = b
				}
			default:
				return RangeValue{}, fmt.Errorf("unknown range field %q", clip(k))
			}
		}
		return r, nil
	}
	return RangeValue{}, fmt.Errorf("range value must be an object with start and end, or a range literal")
}

func checkIntegral(r RangeValue, integral bool) error {
	for _, b := range []*string{r.Start, r.End} {
		if b != nil && integral {
			if _, err := element(*b, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// ParseRangeLiteral parses a range literal in Postgres's text format:
// "empty", or [ or ( then lower bound, comma, upper bound, then ] or ). An
// omitted bound is unbounded; bounds may be double-quoted with "" or
// backslash escapes.
func ParseRangeLiteral(s string) (RangeValue, error) {
	t := strings.TrimSpace(s)
	if strings.EqualFold(t, "empty") {
		return RangeValue{Empty: true}, nil
	}
	if len(t) < 3 || (t[0] != '[' && t[0] != '(') || (t[len(t)-1] != ']' && t[len(t)-1] != ')') {
		return RangeValue{}, fmt.Errorf("range literal %q must look like [lower,upper)", clip(s))
	}
	lo, rest, err := rangeBound(t[1:len(t)-1], true)
	if err != nil {
		return RangeValue{}, fmt.Errorf("range literal %q: %w", clip(s), err)
	}
	hi, rest, err := rangeBound(rest, false)
	if err != nil || rest != "" {
		return RangeValue{}, fmt.Errorf("range literal %q must have exactly two bounds", clip(s))
	}
	return RangeValue{
		Start: lo, End: hi,
		StartInclusive: t[0] == '[', EndInclusive: t[len(t)-1] == ']',
	}, nil
}

// rangeBound reads one bound. The lower bound ends at the first unquoted
// comma, which it consumes; the upper bound runs to the end of s.
func rangeBound(s string, lower bool) (*string, string, error) {
	var b strings.Builder
	quoted, inQuote := false, false
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 >= len(s) {
				return nil, "", fmt.Errorf("unterminated escape")
			}
			i++
			b.WriteByte(s[i])
		case c == '"' && inQuote && i+1 < len(s) && s[i+1] == '"':
			i++
			b.WriteByte('"')
		case c == '"':
			inQuote, quoted = !inQuote, true
		case inQuote:
			b.WriteByte(c)
		case c == ',' && lower:
			return boundValue(b.String(), quoted), s[i+1:], nil
		case c == ',' || c == '(' || c == ')' || c == '[' || c == ']':
			return nil, "", fmt.Errorf("unexpected %q", c)
		default:
			b.WriteByte(c)
		}
	}
	if inQuote {
		return nil, "", fmt.Errorf("unterminated quote")
	}
	if lower {
		return nil, "", fmt.Errorf("missing comma")
	}
	return boundValue(b.String(), quoted), "", nil
}

func boundValue(s string, quoted bool) *string {
	if !quoted {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
	}
	return &s
}

// ParseMultirangeLiteral parses a multirange literal: {} or a braced,
// comma-separated list of range literals.
func ParseMultirangeLiteral(s string) ([]RangeValue, error) {
	t := strings.TrimSpace(s)
	if len(t) < 2 || t[0] != '{' || t[len(t)-1] != '}' {
		return nil, fmt.Errorf("multirange literal %q must look like {[a,b), [c,d)}", clip(s))
	}
	body := strings.TrimSpace(t[1 : len(t)-1])
	var out []RangeValue
	for body != "" {
		end := rangeLiteralEnd(body)
		if end < 0 {
			return nil, fmt.Errorf("multirange literal %q is malformed", clip(s))
		}
		r, err := ParseRangeLiteral(body[:end])
		if err != nil {
			return nil, err
		}
		out = append(out, r)
		body = strings.TrimSpace(body[end:])
		if body == "" {
			break
		}
		if body[0] != ',' {
			return nil, fmt.Errorf("multirange literal %q is malformed", clip(s))
		}
		body = strings.TrimSpace(body[1:])
		if body == "" {
			return nil, fmt.Errorf("multirange literal %q is malformed", clip(s))
		}
	}
	return out, nil
}

// rangeLiteralEnd returns the length of the range literal s starts with, or
// -1.
func rangeLiteralEnd(s string) int {
	if len(s) >= 5 && strings.EqualFold(s[:5], "empty") {
		return 5
	}
	if s[0] != '[' && s[0] != '(' {
		return -1
	}
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (c == ']' || c == ')'):
			return i + 1
		}
	}
	return -1
}

// params returns the constructor's three parameters for r.
func (rt rangeType) params(r RangeValue) []any {
	if r.Empty {
		return []any{rt.sentinel, rt.sentinel, "[)"}
	}
	return []any{strOrNil(r.Start), strOrNil(r.End), r.Bounds()}
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (rt rangeType) parse(v any) ([]any, error) {
	r, err := ParseRange(v, rt.integral)
	if err != nil {
		return nil, err
	}
	return rt.params(r), nil
}

func (rt rangeType) parseElement(v any) ([]any, error) {
	if v == nil {
		return nil, fmt.Errorf("range element must not be null")
	}
	s, err := element(v, rt.integral)
	if err != nil {
		return nil, err
	}
	return []any{s}, nil
}

// parseMulti coerces a list of ranges or a multirange literal into three
// parallel array parameters.
func (rt rangeType) parseMulti(v any) ([]any, error) {
	var rs []RangeValue
	switch in := v.(type) {
	case string:
		l, err := ParseMultirangeLiteral(in)
		if err != nil {
			return nil, err
		}
		for _, r := range l {
			if err := checkIntegral(r, rt.integral); err != nil {
				return nil, err
			}
		}
		rs = l
	case []RangeValue:
		rs = in
	case []any:
		for _, e := range in {
			r, err := ParseRange(e, rt.integral)
			if err != nil {
				return nil, err
			}
			rs = append(rs, r)
		}
	default:
		return nil, fmt.Errorf("multirange value must be a list of ranges or a multirange literal")
	}
	lo, hi, bounds := make([]*string, len(rs)), make([]*string, len(rs)), make([]string, len(rs))
	for i, r := range rs {
		p := rt.params(r)
		if s, ok := p[0].(string); ok {
			lo[i] = &s
		}
		if s, ok := p[1].(string); ok {
			hi[i] = &s
		}
		bounds[i] = p[2].(string)
	}
	return []any{lo, hi, bounds}, nil
}

// sql renders the range constructor over three placeholders.
func (rt rangeType) sql(a []string) string {
	return rt.name + "(" + atom(a[0]) + "::" + rt.elem + ", " + atom(a[1]) + "::" + rt.elem + ", " + atom(a[2]) + "::text)"
}

// multiSQL renders the multirange constructor over three array placeholders.
func (rt rangeType) multiSQL(a []string) string {
	return rt.multi + "(VARIADIC ARRAY(SELECT " + rt.name + "(__l, __u, __b) FROM unnest(" +
		atom(a[0]) + "::" + rt.elem + "[], " + atom(a[1]) + "::" + rt.elem + "[], " + atom(a[2]) + "::text[]) AS __r(__l, __u, __b)))"
}

// rangeJSON renders a non-null range expression as the RangeValue object.
func rangeJSON(r string) string {
	return "jsonb_build_object('start', lower(" + r + "), 'end', upper(" + r + "), 'startInclusive', lower_inc(" + r +
		"), 'endInclusive', upper_inc(" + r + "), 'isEmpty', isempty(" + r + "))"
}

// RangeOutputSQL renders a range reference as a jsonb object {start, end,
// startInclusive, endInclusive, isEmpty}; start/end are null when
// unbounded or empty. NULL stays NULL and ref is evaluated once.
func RangeOutputSQL(ref string) string {
	return "(SELECT " + rangeJSON("__r") + " FROM (SELECT " + ref + " AS __r) AS __v WHERE __r IS NOT NULL)"
}

// MultirangeOutputSQL renders a multirange reference as a jsonb array of
// range objects (see [RangeOutputSQL]) in range order; the empty multirange
// is []. NULL stays NULL and ref is evaluated once.
func MultirangeOutputSQL(ref string) string {
	return "(SELECT CASE WHEN __m IS NULL THEN NULL ELSE COALESCE((SELECT jsonb_agg(" + rangeJSON("__e") +
		" ORDER BY __e) FROM unnest(__m) AS __u(__e)), '[]'::jsonb) END FROM (SELECT " + ref + " AS __m) AS __v)"
}

func rangeAccessors(elem string) []Accessor {
	fn := func(name string) func(qual, string) string {
		return func(_ qual, ref string) string { return name + "(" + ref + ")" }
	}
	return []Accessor{
		{Name: "start", Type: elem, Description: "Lower bound; null when unbounded or empty (lower).", sql: fn("lower")},
		{Name: "end", Type: elem, Description: "Upper bound; null when unbounded or empty (upper).", sql: fn("upper")},
		{Name: "startInclusive", Type: "bool", Description: "Whether the lower bound is inclusive (lower_inc).", sql: fn("lower_inc")},
		{Name: "endInclusive", Type: "bool", Description: "Whether the upper bound is inclusive (upper_inc).", sql: fn("upper_inc")},
		{Name: "startInfinite", Type: "bool", Description: "Whether the range has no lower bound (lower_inf).", sql: fn("lower_inf")},
		{Name: "endInfinite", Type: "bool", Description: "Whether the range has no upper bound (upper_inf).", sql: fn("upper_inf")},
		{Name: "isEmpty", Type: "bool", Description: "Whether the range is empty (isempty).", sql: fn("isempty")},
	}
}

// rangeOps builds the range operator set over a column of a range or
// multirange type whose range operands are rt.
func rangeOps(rt rangeType, idx []IndexHint) []Op {
	rangeOp := func(name, op, desc string) Op {
		return Op{
			Name: name, Arg: ArgRange, Index: idx, Description: desc, params: rt.parse,
			render: func(_ qual, col string, a []string) string { return paren(col) + " " + op + " " + rt.sql(a) },
		}
	}
	return []Op{
		rangeOp("overlaps", "&&", "Shares at least one point with the given range (&&)."),
		rangeOp("contains", "@>", "Contains the given range (@>)."),
		{
			Name: "containsElement", Arg: ArgElement, Index: idx, params: rt.parseElement,
			Description: "Contains the given value (@> element).",
			render: func(_ qual, col string, a []string) string {
				return paren(col) + " @> " + atom(a[0]) + "::" + rt.elem
			},
		},
		rangeOp("containedBy", "<@", "Is contained by the given range (<@)."),
		rangeOp("strictlyLeftOf", "<<", "Lies entirely before the given range (<<)."),
		rangeOp("strictlyRightOf", ">>", "Lies entirely after the given range (>>)."),
		rangeOp("notExtendsRightOf", "&<", "Does not extend past the end of the given range (&<)."),
		rangeOp("notExtendsLeftOf", "&>", "Does not extend before the start of the given range (&>)."),
		rangeOp("adjacentTo", "-|-", "Is adjacent to the given range, touching without overlap (-|-)."),
	}
}

// Range returns the vocabulary of a built-in range column type (int4range,
// int8range, numrange, tsrange, tstzrange, daterange). Range operands are
// structured values or literals ([ParseRange]) rendered through the type's
// constructor, e.g. int4range($1::int4, $2::int4, $3::text).
func Range(typeName string) (Vocabulary, bool) {
	rt, ok := rangeTypes[typeName]
	if !ok {
		return Vocabulary{}, false
	}
	idx := []IndexHint{{Method: "gist"}, {Method: "spgist"}}
	return Vocabulary{
		Name: VocabRange, Type: rt.name,
		Ops:       rangeOps(rt, idx),
		Accessors: rangeAccessors(rt.elem),
		Codec: &Codec{
			Arg:    ArgRange,
			params: rt.parse,
			sql:    func(_ qual, a []string) string { return rt.sql(a) },
			output: func(_ qual, ref string) string { return RangeOutputSQL(ref) },
		},
	}, true
}

// Multirange returns the vocabulary of a built-in multirange column type
// (int4multirange, ..., datemultirange; PostgreSQL 14+). Operators take a
// range operand of the matching range type (or an element for
// containsElement); values are lists of ranges.
func Multirange(typeName string) (Vocabulary, bool) {
	rt, ok := multirangeType(typeName)
	if !ok {
		return Vocabulary{}, false
	}
	idx := []IndexHint{{Method: "gist"}}
	return Vocabulary{
		Name: VocabMultirange, Type: rt.multi,
		Ops:       rangeOps(rt, idx),
		Accessors: rangeAccessors(rt.elem),
		Codec: &Codec{
			Arg:    ArgMultirange,
			params: rt.parseMulti,
			sql:    func(_ qual, a []string) string { return rt.multiSQL(a) },
			output: func(_ qual, ref string) string { return MultirangeOutputSQL(ref) },
		},
	}, true
}
