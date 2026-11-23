package contrib

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on [HeadlineOptions]. ts_headline parses its whole document on
// every call whatever the options say; the limits bound what it returns
// per row (MaxFragments fragments of up to MaxWords words each, or the
// whole document with HighlightAll) and the markup it repeats.
const (
	// MaxHeadlineWords bounds MaxWords, MinWords and ShortWord.
	MaxHeadlineWords = 100
	// MaxHeadlineFragments bounds MaxFragments.
	MaxHeadlineFragments = 10
	// MaxHeadlineMarkup bounds StartSel, StopSel and FragmentDelimiter, in
	// bytes.
	MaxHeadlineMarkup = 32
)

// HeadlineCost is the cost, in column reads per returned row, a consumer's
// cost estimate charges for a headline: ts_headline re-parses the whole
// document with the text search parser on every call.
const HeadlineCost = 10

// ts_headline's defaults, which apply to the options a request leaves out.
const (
	defaultMaxWords = 35
	defaultMinWords = 15
)

// HeadlineOptions are the ts_headline options of a request; nil fields keep
// PostgreSQL's defaults (MaxWords=35, MinWords=15, ShortWord=3,
// HighlightAll=false, MaxFragments=0, StartSel=<b>, StopSel=</b>,
// FragmentDelimiter=" ... ").
type HeadlineOptions struct {
	MaxWords, MinWords, ShortWord, MaxFragments *int
	HighlightAll                                *bool
	StartSel, StopSel, FragmentDelimiter        *string
}

// headlineOption describes one option: its key in a request (camelCase, as
// pdbq's input fields spell it), its ts_headline name, and where it is
// stored.
type headlineOption struct {
	key, name string
	num       func(*HeadlineOptions) **int
	flag      func(*HeadlineOptions) **bool
	text      func(*HeadlineOptions) **string
	lo, hi    int
}

var headlineOptions = []headlineOption{
	{key: "maxWords", name: "MaxWords", num: func(h *HeadlineOptions) **int { return &h.MaxWords }, lo: 1, hi: MaxHeadlineWords},
	{key: "minWords", name: "MinWords", num: func(h *HeadlineOptions) **int { return &h.MinWords }, lo: 1, hi: MaxHeadlineWords},
	{key: "shortWord", name: "ShortWord", num: func(h *HeadlineOptions) **int { return &h.ShortWord }, lo: 0, hi: MaxHeadlineWords},
	{key: "maxFragments", name: "MaxFragments", num: func(h *HeadlineOptions) **int { return &h.MaxFragments }, lo: 0, hi: MaxHeadlineFragments},
	{key: "highlightAll", name: "HighlightAll", flag: func(h *HeadlineOptions) **bool { return &h.HighlightAll }},
	{key: "startSel", name: "StartSel", text: func(h *HeadlineOptions) **string { return &h.StartSel }},
	{key: "stopSel", name: "StopSel", text: func(h *HeadlineOptions) **string { return &h.StopSel }},
	{key: "fragmentDelimiter", name: "FragmentDelimiter", text: func(h *HeadlineOptions) **string { return &h.FragmentDelimiter }},
}

// HeadlineOptionKeys returns the option keys [ParseHeadlineOptions] accepts,
// in documentation order.
func HeadlineOptionKeys() []string {
	out := make([]string, len(headlineOptions))
	for i, o := range headlineOptions {
		out[i] = o.key
	}
	return out
}

// ParseHeadlineOptions coerces headline options: nil, or an object keyed by
// [HeadlineOptionKeys] whose null members are left out. Numbers may be
// integers or integer strings and highlightAll a boolean or "true"/"false",
// so text grammars can pass their raw values. Each value is checked against
// the limits above, and MinWords against MaxWords with PostgreSQL's
// defaults filled in (MinWords must stay below MaxWords). The error is
// user-facing.
func ParseHeadlineOptions(v any) (HeadlineOptions, error) {
	var h HeadlineOptions
	if v == nil {
		return h, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return h, fmt.Errorf("headline options must be an object")
	}
	for k, val := range m {
		var opt *headlineOption
		for i := range headlineOptions {
			if headlineOptions[i].key == k {
				opt = &headlineOptions[i]
			}
		}
		if opt == nil {
			return h, fmt.Errorf("unknown headline option %q (want one of %s)", clip(k), strings.Join(HeadlineOptionKeys(), ", "))
		}
		if val == nil {
			continue
		}
		switch {
		case opt.num != nil:
			n, ok := optionInt(val)
			if !ok || n < opt.lo || n > opt.hi {
				return h, fmt.Errorf("headline option %s must be an integer between %d and %d", k, opt.lo, opt.hi)
			}
			*opt.num(&h) = &n
		case opt.flag != nil:
			b, ok := optionBool(val)
			if !ok {
				return h, fmt.Errorf("headline option %s must be a boolean", k)
			}
			*opt.flag(&h) = &b
		default:
			s, ok := val.(string)
			if !ok || !validMarkup(s) {
				return h, fmt.Errorf("headline option %s must be a string of 1 to %d bytes without double quotes, backslashes or control characters", k, MaxHeadlineMarkup)
			}
			*opt.text(&h) = &s
		}
	}
	minWords, maxWords := defaultMinWords, defaultMaxWords
	if h.MinWords != nil {
		minWords = *h.MinWords
	}
	if h.MaxWords != nil {
		maxWords = *h.MaxWords
	}
	if minWords >= maxWords {
		return h, fmt.Errorf("headline option minWords (%d) must be less than maxWords (%d)", minWords, maxWords)
	}
	return h, nil
}

// String renders the options as ts_headline's options text ("" when none is
// set), with the markup double-quoted. It is a value: bind it as a
// parameter, never splice it into SQL.
func (h HeadlineOptions) String() string {
	var parts []string
	for _, o := range headlineOptions {
		switch {
		case o.num != nil:
			if p := *o.num(&h); p != nil {
				parts = append(parts, o.name+"="+strconv.Itoa(*p))
			}
		case o.flag != nil:
			if p := *o.flag(&h); p != nil {
				parts = append(parts, o.name+"="+strconv.FormatBool(*p))
			}
		default:
			if p := *o.text(&h); p != nil {
				parts = append(parts, o.name+`="`+*p+`"`)
			}
		}
	}
	return strings.Join(parts, ", ")
}

// validMarkup reports whether s can be a double-quoted ts_headline option
// value of at most MaxHeadlineMarkup bytes.
func validMarkup(s string) bool {
	if s == "" || len(s) > MaxHeadlineMarkup || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '"' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func optionInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		if n < math.MinInt32 || n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	case float64:
		if n != math.Trunc(n) || n < math.MinInt32 || n > math.MaxInt32 {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := strconv.ParseInt(string(n), 10, 32)
		return int(i), err == nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 32)
		return int(i), err == nil
	}
	return 0, false
}

func optionBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch b {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// IsDocument reports whether a pg type (as Column.PGType spells it) holds a
// document ts_headline can highlight: a text-like type ([IsText]), json or
// jsonb. ts_headline returns text for a text document and a document of the
// same type, its string values highlighted, for json and jsonb.
func IsDocument(pgType string) bool {
	if IsText(pgType) {
		return true
	}
	base, arr := normalizeType(pgType)
	return !arr && (base == "json" || base == "jsonb")
}

// HeadlineSQL renders ts_headline over the document expression doc against
// the query of a full-text operator, reusing the placeholders its predicate
// was rendered with (the query, and the configuration when one was given),
// so a result filtered by the operator can show where it matched. doc is
// the filtered column itself for the text and json search vocabularies, or
// another column holding the source of a tsvector column; it must be a
// [IsDocument] type. options is the placeholder of the text parameter
// holding [HeadlineOptions.String], or "" for PostgreSQL's defaults. It
// returns "" for operators that are not full-text matches.
func (o Op) HeadlineSQL(doc, options string, args ...string) string {
	lo, hi := o.Arg.arity()
	if o.headline == nil || len(args) < lo || len(args) > hi {
		return ""
	}
	return o.headline(o.q, doc, options, args)
}

func headlineFunc(v Variant) func(q qual, doc, options string, a []string) string {
	return func(_ qual, doc, options string, a []string) string {
		query, config := ftsArgs(a)
		var b strings.Builder
		b.WriteString("ts_headline(")
		if config != "" {
			b.WriteString(atom(config) + "::regconfig, ")
		}
		b.WriteString(doc + ", " + TSQuerySQL(v, query, config))
		if options != "" {
			b.WriteString(", " + atom(options) + "::text")
		}
		b.WriteString(")")
		return b.String()
	}
}
