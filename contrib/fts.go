package contrib

import "fmt"

// Variant selects the function that parses a full-text query string into a
// tsquery.
type Variant int

const (
	// Raw parses tsquery syntax (to_tsquery): & | ! <-> operators,
	// :* prefixes, weights. Malformed input is a syntax error raised by
	// Postgres. PostgREST fts, postgraphile-plugin-fulltext-filter matches.
	Raw Variant = iota
	// Plain ANDs the words of free text (plainto_tsquery). PostgREST plfts.
	Plain
	// Phrase matches the words of free text as a phrase
	// (phraseto_tsquery). PostgREST phfts.
	Phrase
	// Websearch accepts web-search syntax — "quoted phrases", or, -negation —
	// and never raises on malformed input (websearch_to_tsquery). PostgREST
	// wfts.
	Websearch
)

// Func returns the tsquery constructor the variant renders.
func (v Variant) Func() string {
	switch v {
	case Plain:
		return "plainto_tsquery"
	case Phrase:
		return "phraseto_tsquery"
	case Websearch:
		return "websearch_to_tsquery"
	}
	return "to_tsquery"
}

// Rank names a full-text relevance function.
type Rank string

const (
	// RankFrequency ranks by lexeme frequency (ts_rank).
	RankFrequency Rank = "ts_rank"
	// RankCoverDensity ranks by cover density, rewarding matched lexemes
	// that sit close together (ts_rank_cd).
	RankCoverDensity Rank = "ts_rank_cd"
)

// FullTextQuery is a parsed full-text operand.
type FullTextQuery struct {
	// Query is the query text, parsed by the operator's [Variant].
	Query string
	// Config is the text search configuration ("english", "simple"); empty
	// uses the session's default_text_search_config.
	Config string
}

// ParseFullTextQuery coerces a full-text operand: a query string, or an
// object {query, config}.
func ParseFullTextQuery(v any) (FullTextQuery, error) {
	switch in := v.(type) {
	case string:
		return FullTextQuery{Query: in}, nil
	case FullTextQuery:
		return in, nil
	case map[string]any:
		var q FullTextQuery
		for k, e := range in {
			s, ok := e.(string)
			switch {
			case k == "query" && ok:
				q.Query = s
			case k == "config" && (ok || e == nil):
				q.Config = s
			case k == "query" || k == "config":
				return FullTextQuery{}, fmt.Errorf("full-text %s must be a string", k)
			default:
				return FullTextQuery{}, fmt.Errorf("unknown full-text field %q", clip(k))
			}
		}
		if _, ok := in["query"]; !ok {
			return FullTextQuery{}, fmt.Errorf("full-text filter requires a query")
		}
		return q, nil
	}
	return FullTextQuery{}, fmt.Errorf("full-text operand must be a string or an object with a query")
}

// Params returns the bind parameters: the query, then the configuration
// when one is set.
func (q FullTextQuery) Params() []any {
	if q.Config == "" {
		return []any{q.Query}
	}
	return []any{q.Query, q.Config}
}

// TSQuerySQL renders the tsquery for a query placeholder and an optional
// configuration placeholder ("" omits it).
func TSQuerySQL(v Variant, query, config string) string {
	if config == "" {
		return v.Func() + "(" + query + "::text)"
	}
	return v.Func() + "(" + config + "::regconfig, " + query + "::text)"
}

// TSVectorSQL renders to_tsvector over a text expression with an optional
// configuration placeholder ("" omits it).
//
// Index implications: the one-argument form depends on
// default_text_search_config, so it is not immutable and no index can be
// built on it. An expression index on to_tsvector('english', col) serves the
// two-argument form only when the query passes that same configuration and
// the plan is built with the parameter's value (a custom plan; pgx's unnamed
// and cached statements get one until Postgres switches a cached statement
// to a generic plan). A stored generated tsvector column with a GIN index is
// the dependable choice for large tables.
func TSVectorSQL(col, config string) string {
	if config == "" {
		return "to_tsvector(" + col + ")"
	}
	return "to_tsvector(" + config + "::regconfig, " + col + ")"
}

// RankSQL renders a relevance expression over a tsvector and a tsquery
// expression.
func RankSQL(r Rank, vector, tsquery string) string {
	return string(r) + "(" + vector + ", " + tsquery + ")"
}

func ftsArgs(a []string) (query, config string) {
	if len(a) > 1 {
		return a[0], a[1]
	}
	return a[0], ""
}

var variants = []struct {
	name string
	v    Variant
	desc string
}{
	{"matches", Raw, "Matches a tsquery-syntax query (to_tsquery): & | ! <-> operators and :* prefixes."},
	{"matchesPlain", Plain, "Matches all words of free text (plainto_tsquery)."},
	{"matchesPhrase", Phrase, "Matches free text as a phrase, words adjacent and in order (phraseto_tsquery)."},
	{"matchesWebsearch", Websearch, `Matches web-search syntax: "quoted phrase", or, -negation (websearch_to_tsquery). Never raises on malformed input.`},
}

// FullText is the vocabulary of tsvector columns: @@ against a query parsed
// by each [Variant], with an optional configuration.
func FullText() Vocabulary {
	idx := []IndexHint{{Method: "gin"}, {Method: "gist", Note: "lossy; rechecks every candidate row"}}
	ops := make([]Op, len(variants))
	for i, vt := range variants {
		ops[i] = Op{
			Name: vt.name, Arg: ArgFullText, Index: idx,
			Description: vt.desc + " An optional config names the text search configuration of the query.",
			render: func(_ qual, col string, a []string) string {
				q, c := ftsArgs(a)
				return col + " @@ " + TSQuerySQL(vt.v, q, c)
			},
			rank: func(_ qual, fn, col string, a []string) string {
				q, c := ftsArgs(a)
				return RankSQL(Rank(fn), col, TSQuerySQL(vt.v, q, c))
			},
		}
	}
	return Vocabulary{
		Name: VocabFullText, Type: "tsvector", Ops: ops,
		Codec: &Codec{
			Arg: ArgText,
			sql: func(_ qual, a []string) string { return a[0] + "::tsvector" },
		},
	}
}

// TextSearch is the full-text vocabulary of a text-like column (textType is
// its pg type): the column is converted with to_tsvector using the same
// configuration as the query, as PostgREST does for fts on text columns.
// See [TSVectorSQL] for which indexes can serve it.
func TextSearch(textType string) Vocabulary {
	idx := []IndexHint{{
		Method: "gin",
		Note:   "expression index on to_tsvector('<config>', column); used only when the query passes that config",
	}}
	ops := make([]Op, len(variants))
	for i, vt := range variants {
		ops[i] = Op{
			Name: vt.name, Arg: ArgFullText, Index: idx,
			Description: vt.desc + " The column is parsed with to_tsvector in the same config as the query (optional; default_text_search_config otherwise).",
			render: func(_ qual, col string, a []string) string {
				q, c := ftsArgs(a)
				return TSVectorSQL(col, c) + " @@ " + TSQuerySQL(vt.v, q, c)
			},
			rank: func(_ qual, fn, col string, a []string) string {
				q, c := ftsArgs(a)
				return RankSQL(Rank(fn), TSVectorSQL(col, c), TSQuerySQL(vt.v, q, c))
			},
		}
	}
	return Vocabulary{Name: VocabTextSearch, Type: textType, Ops: ops}
}
