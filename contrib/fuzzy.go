package contrib

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// MaxFuzzyLength is the longest text fuzzystrmatch's levenshtein functions
// accept, in characters, and its metaphone function, in bytes; longer
// arguments raise an error.
const MaxFuzzyLength = 255

// fuzzyText coerces a reference text for the length-limited fuzzystrmatch
// functions, rejecting one longer than [MaxFuzzyLength] characters (or
// bytes, when bytes is set).
func fuzzyText(v any, bytes bool) (string, error) {
	s, err := Text(v)
	if err != nil {
		return "", err
	}
	if n := utf8.RuneCountInString(s); n > MaxFuzzyLength || (bytes && len(s) > MaxFuzzyLength) {
		unit := "characters"
		if bytes {
			unit = "bytes"
		}
		return "", fmt.Errorf("text must be at most %d %s", MaxFuzzyLength, unit)
	}
	return s, nil
}

// Fuzzy is the fuzzystrmatch vocabulary of a text-like column (textType is
// its pg type: text, varchar, bpchar or citext, each passed to the
// functions as text): the Levenshtein edit distance to a reference text,
// as a selectable score and nearest-first ordering ([Expr.Score],
// levenshteinDistance) and as a threshold filter
// (levenshteinDistanceLessThan, taking {text, distance}), and phonetic
// equality by soundex, metaphone and double metaphone (primary code).
//
// No index serves any of them; an expression index on, say,
// (soundex(column)) could, but does not match the rendered expressions.
// Consumers treat them like the unaccent operators under an indexed-only
// policy.
//
// levenshtein rejects arguments longer than 255 characters and metaphone
// longer than 255 bytes. Reference texts beyond that are refused by Params;
// for column values beyond it the rendered SQL yields NULL instead of an
// error: no distance, and neither the predicate nor its negation holds. Comparisons are case-sensitive for
// levenshtein (citext included) and case-insensitive for the phonetic codes.
func Fuzzy(textType string) Vocabulary {
	txt := func(col string) string { return paren(col) + "::text" }
	limit := strconv.Itoa(MaxFuzzyLength)
	guarded := func(lenFn, col string) string {
		return "CASE WHEN " + lenFn + "(" + txt(col) + ") <= " + limit + " THEN "
	}
	textParam := func(bytes bool) func(any) ([]any, error) {
		return func(v any) ([]any, error) {
			s, err := fuzzyText(v, bytes)
			if err != nil {
				return nil, err
			}
			return []any{s}, nil
		}
	}
	phonetic := func(fn string) func(qual, string, []string) string {
		return func(q qual, col string, a []string) string {
			return q.fn(fn) + "(" + col + ") = " + q.fn(fn) + "(" + atom(a[0]) + "::text)"
		}
	}
	return Vocabulary{
		Name: VocabFuzzy, Type: textType, Extension: ExtFuzzy,
		Ops: []Op{
			{
				Name: "levenshteinDistance" + LessThanSuffix, Arg: ArgTextDistance, Extension: ExtFuzzy,
				Description: "The Levenshtein edit distance to the given text (at most 255 characters) is less than " +
					"the given distance (levenshtein_less_equal). Values longer than 255 characters have no distance: " +
					"neither this nor its negation matches them. " +
					"Cannot use an index.",
				params: func(v any) ([]any, error) {
					s, d, err := TextDistanceParam(v)
					if err != nil {
						return nil, err
					}
					if s, err = fuzzyText(s, false); err != nil {
						return nil, err
					}
					return []any{s, d}, nil
				},
				render: func(q qual, col string, a []string) string {
					d := atom(a[1]) + "::int4"
					return guarded("char_length", col) + q.fn("levenshtein_less_equal") + "(" + txt(col) + ", " +
						atom(a[0]) + "::text, " + d + ") END < " + d
				},
			},
			{
				Name: "soundexEqualTo", Arg: ArgText, Extension: ExtFuzzy,
				Description: "Has the same Soundex code as the given text (soundex). Cannot use an index.",
				render:      phonetic("soundex"),
			},
			{
				Name: "metaphoneEqualTo", Arg: ArgText, Extension: ExtFuzzy,
				Description: "Has the same Metaphone code as the given text (at most 255 bytes) (metaphone). Values " +
					"longer than 255 bytes have no code: neither this nor its negation matches them. Cannot use an index.",
				params: textParam(true),
				render: func(q qual, col string, a []string) string {
					return q.fn("metaphone") + "(" + guarded("octet_length", col) + txt(col) + " END, " + limit + ") = " +
						q.fn("metaphone") + "(" + atom(a[0]) + "::text, " + limit + ")"
				},
			},
			{
				Name: "doubleMetaphoneEqualTo", Arg: ArgText, Extension: ExtFuzzy,
				Description: "Has the same primary Double Metaphone code as the given text (dmetaphone). Cannot use an index.",
				render:      phonetic("dmetaphone"),
			},
		},
		Exprs: []Expr{{
			Name: "levenshteinDistance", Arg: ArgText, Type: "int4", Ascending: true, Extension: ExtFuzzy, Score: true,
			Description: "Levenshtein edit distance to the given text, at most 255 characters (levenshtein); " +
				"null for values longer than 255 characters.",
			params: textParam(false),
			render: func(q qual, col string, a []string) string {
				return guarded("char_length", col) + q.fn("levenshtein") + "(" + txt(col) + ", " + atom(a[0]) + "::text) END"
			},
		}},
	}
}
