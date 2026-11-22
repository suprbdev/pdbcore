package contrib

// Trgm is the pg_trgm vocabulary of a text-like column (textType is its pg
// type: text, varchar, bpchar or citext; pg_trgm's functions take text, to
// which each casts implicitly).
//
// The predicates compare against the pg_trgm.similarity_threshold,
// pg_trgm.word_similarity_threshold and
// pg_trgm.strict_word_similarity_threshold settings (0.3, 0.6, 0.5 by
// default). They are rendered with the column on the left (col %> value
// rather than value <% col), the form the trigram operator classes index.
//
// A bpchar (char(n)) column reaches the text operators through a cast that
// strips trailing blanks, so an index on the column itself cannot serve them
// and the vocabulary carries no index hints; an expression index on
// (column::text) can.
//
// The word operators need pg_trgm 1.2 and the strict ones 1.4; see
// [Vocabulary.ForVersion].
func Trgm(textType string) Vocabulary {
	pred := []IndexHint{{Method: "gin", OpClass: "gin_trgm_ops"}, {Method: "gist", OpClass: "gist_trgm_ops"}}
	knn := []IndexHint{{Method: "gist", OpClass: "gist_trgm_ops", Ordering: true}}
	note := ""
	if textType == "bpchar" {
		pred, knn = nil, nil
		note = " On char(n) only an expression index on (column::text) can serve it."
	}
	infix := func(op string) func(qual, string, []string) string {
		return func(q qual, col string, a []string) string {
			return paren(col) + " " + q.op(op) + " " + atom(a[0]) + "::text"
		}
	}
	call := func(fn string, valueFirst bool) func(qual, string, []string) string {
		return func(q qual, col string, a []string) string {
			if valueFirst {
				return q.fn(fn) + "(" + atom(a[0]) + "::text, " + col + ")"
			}
			return q.fn(fn) + "(" + col + ", " + atom(a[0]) + "::text)"
		}
	}
	return Vocabulary{
		Name: VocabTrgm, Type: textType, Extension: ExtTrgm,
		Ops: []Op{
			{
				Name: "similarTo", Arg: ArgText, Index: pred, Extension: ExtTrgm,
				Description: "Trigram similarity with the given text exceeds pg_trgm.similarity_threshold (%)." + note,
				render:      infix("%"),
			},
			{
				Name: "wordSimilarTo", Arg: ArgText, Index: pred, Extension: ExtTrgm,
				Description: "The column contains an extent similar to the given word(s) beyond pg_trgm.word_similarity_threshold (value <% column)." + note,
				since:       "1.2",
				render:      infix("%>"),
			},
			{
				Name: "strictWordSimilarTo", Arg: ArgText, Index: pred, Extension: ExtTrgm,
				Description: "The column contains whole words similar to the given word(s) beyond pg_trgm.strict_word_similarity_threshold (value <<% column)." + note,
				since:       "1.4",
				render:      infix("%>>"),
			},
		},
		Exprs: []Expr{
			{
				Name: "similarity", Arg: ArgText, Type: "float4", Extension: ExtTrgm,
				Description: "Trigram similarity between the column and the given text, 0 to 1 (similarity).",
				render:      call("similarity", false),
			},
			{
				Name: "wordSimilarity", Arg: ArgText, Type: "float4", Extension: ExtTrgm,
				Description: "Greatest similarity between the given text and an extent of the column, 0 to 1 (word_similarity).",
				since:       "1.2",
				render:      call("word_similarity", true),
			},
			{
				Name: "strictWordSimilarity", Arg: ArgText, Type: "float4", Extension: ExtTrgm,
				Description: "Greatest similarity between the given text and whole words of the column, 0 to 1 (strict_word_similarity).",
				since:       "1.4",
				render:      call("strict_word_similarity", true),
			},
			{
				Name: "distance", Arg: ArgText, Type: "float4", Ascending: true, Index: knn, Extension: ExtTrgm,
				Description: "Trigram distance, 1 - similarity (<->). Index-assisted nearest-first ordering with a GiST trigram index." + note,
				render:      infix("<->"),
			},
			{
				Name: "wordDistance", Arg: ArgText, Type: "float4", Ascending: true, Index: knn, Extension: ExtTrgm,
				Description: "Word distance, 1 - word_similarity (value <<-> column)." + note,
				since:       "1.2",
				render:      infix("<->>"),
			},
			{
				Name: "strictWordDistance", Arg: ArgText, Type: "float4", Ascending: true, Index: knn, Extension: ExtTrgm,
				Description: "Strict word distance, 1 - strict_word_similarity (value <<<-> column)." + note,
				since:       "1.4",
				render:      infix("<->>>"),
			},
		},
	}
}
