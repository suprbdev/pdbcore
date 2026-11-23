package contrib

// Intarray is the intarray vocabulary of int4[] columns: the query_int
// match (matchesQueryInt: col @@ $1::query_int, e.g. "1&(2|3)" or "!4"),
// which a GIN index built with gin__int_ops or a GiST index with
// gist__int_ops or gist__intbig_ops serves, and the icount accessor
// (number of elements). The core array operators (@>, <@, &&) need no
// vocabulary; with intarray's schema on the search_path they resolve to
// intarray's own operators, which those operator classes serve too.
//
// intarray's operators reject arrays containing NULL elements ("array must
// not contain nulls", SQLSTATE 22004), so a row holding one raises that
// error when the predicate is evaluated on it.
func Intarray() Vocabulary {
	return Vocabulary{
		Name: VocabIntarray, Type: "_int4", Extension: ExtIntarray,
		Ops: []Op{{
			Name: "matchesQueryInt", Arg: ArgText, Extension: ExtIntarray,
			Index: []IndexHint{
				{Method: "gin", OpClass: "gin__int_ops"},
				{Method: "gist", OpClass: "gist__int_ops"},
				{Method: "gist", OpClass: "gist__intbig_ops"},
			},
			Description: "Matches the given query_int boolean query over the elements, e.g. 1&(2|3) or !4 (@@).",
			render: func(q qual, col string, a []string) string {
				return paren(col) + " " + q.op("@@") + " " + q.cast(a[0], "query_int")
			},
		}},
		Accessors: []Accessor{{
			Name: "icount", Type: "int4",
			Description: "Number of elements (icount).",
			sql:         func(q qual, ref string) string { return q.fn("icount") + "(" + ref + ")" },
		}},
	}
}
