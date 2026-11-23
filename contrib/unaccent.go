package contrib

import "strings"

// UnaccentSQL renders unaccent over a text expression. Bound to the
// extension's schema it uses the two-argument form naming the dictionary
// explicitly: the one-argument form looks the "unaccent" dictionary up
// through the search_path at execution time and fails when the extension's
// schema is not on it. schema "" renders the bare one-argument call.
func UnaccentSQL(schema, expr string) string {
	if schema == "" {
		return "unaccent(" + expr + ")"
	}
	q := qual{schema: schema}
	dict := strings.ReplaceAll(q.prefix()+quoteIdent("unaccent"), "'", "''")
	return q.fn("unaccent") + "('" + dict + "'::regdictionary, " + expr + ")"
}

// escapeLike escapes the LIKE wildcards and the default escape character so
// s matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// likeParams returns a params func that coerces a text operand and wraps it
// in LIKE wildcards after escaping its own.
func likeParams(prefix, suffix string) func(any) ([]any, error) {
	return func(v any) ([]any, error) {
		s, err := Text(v)
		if err != nil {
			return nil, err
		}
		return []any{prefix + escapeLike(s) + suffix}, nil
	}
}

// Unaccent is the unaccent vocabulary of a text-like column (textType is its
// pg type: text, varchar, bpchar or citext, each cast to text implicitly):
// comparisons that ignore both accents and case, with the column and the
// operand passed through unaccent.
//
// unaccent is STABLE rather than IMMUTABLE (its dictionary can change), so
// no index on the column can serve these operators; the usual workaround,
// an expression index over an IMMUTABLE wrapper function, does not match
// the expressions rendered here.
func Unaccent(textType string) Vocabulary {
	ua := func(q qual, expr string) string { return UnaccentSQL(q.schema, expr) }
	ilike := func(q qual, col string, a []string) string {
		return ua(q, col) + " ILIKE " + ua(q, atom(a[0])+"::text")
	}
	return Vocabulary{
		Name: VocabUnaccent, Type: textType, Extension: ExtUnaccent,
		Ops: []Op{
			{
				Name: "unaccentEqualTo", Arg: ArgText, Extension: ExtUnaccent,
				Description: "Equal to the given text, ignoring accents and case (lower(unaccent(column)) = lower(unaccent(value))).",
				render: func(q qual, col string, a []string) string {
					return "lower(" + ua(q, col) + ") = lower(" + ua(q, atom(a[0])+"::text") + ")"
				},
			},
			{
				Name: "unaccentLike", Arg: ArgText, Extension: ExtUnaccent,
				Description: "Matches the given LIKE pattern (% and _ wildcards), ignoring accents and case (unaccent(column) ILIKE unaccent(pattern)).",
				render:      ilike,
			},
			{
				Name: "unaccentIncludes", Arg: ArgText, Extension: ExtUnaccent,
				Description: "Contains the given text, ignoring accents and case.",
				render:      ilike,
				params:      likeParams("%", "%"),
			},
			{
				Name: "unaccentStartsWith", Arg: ArgText, Extension: ExtUnaccent,
				Description: "Starts with the given text, ignoring accents and case.",
				render:      ilike,
				params:      likeParams("", "%"),
			},
		},
	}
}
