package contrib

func ltreeInfix(op, operandType string) func(qual, string, []string) string {
	return func(q qual, col string, a []string) string {
		return paren(col) + " " + q.op(op) + " " + q.cast(a[0], operandType)
	}
}

func ltreeAccessors() []Accessor {
	return []Accessor{{
		Name: "nlevel", Type: "int4",
		Description: "Number of labels in the path (nlevel).",
		sql:         func(q qual, ref string) string { return q.fn("nlevel") + "(" + ref + ")" },
	}}
}

// Ltree is the vocabulary of ltree columns. Paths, lqueries and ltxtqueries
// are strings; Postgres validates their syntax.
func Ltree() Vocabulary {
	idx := []IndexHint{{Method: "gist", OpClass: "gist_ltree_ops"}}
	return Vocabulary{
		Name: VocabLtree, Type: "ltree", Extension: ExtLtree,
		Ops: []Op{
			{
				Name: "ancestorOf", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Is an ancestor of, or equal to, the given path (@>).",
				render:      ltreeInfix("@>", "ltree"),
			},
			{
				Name: "descendantOf", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Is a descendant of, or equal to, the given path (<@).",
				render:      ltreeInfix("<@", "ltree"),
			},
			{
				Name: "ancestorOfAny", Arg: ArgTextList, Extension: ExtLtree,
				Description: "Is an ancestor of, or equal to, any of the given paths (@> ltree[]). Cannot use an index on the column.",
				render:      ltreeInfix("@>", "ltree[]"),
			},
			{
				Name: "descendantOfAny", Arg: ArgTextList, Extension: ExtLtree,
				Description: "Is a descendant of, or equal to, any of the given paths (<@ ltree[]). Cannot use an index on the column.",
				render:      ltreeInfix("<@", "ltree[]"),
			},
			{
				Name: "matchesLquery", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Matches the given lquery pattern, e.g. *.foo.* (~).",
				render:      ltreeInfix("~", "lquery"),
			},
			{
				Name: "matchesAnyLquery", Arg: ArgTextList, Index: idx, Extension: ExtLtree,
				Description: "Matches any of the given lquery patterns (?).",
				render:      ltreeInfix("?", "lquery[]"),
			},
			{
				Name: "matchesLtxtquery", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Matches the given ltxtquery full-text-like label query, e.g. foo & !bar (@).",
				render:      ltreeInfix("@", "ltxtquery"),
			},
		},
		Accessors: ltreeAccessors(),
		Codec: &Codec{
			Arg: ArgText,
			sql: func(q qual, a []string) string { return q.cast(a[0], "ltree") },
		},
	}
}

// LtreeArray is the vocabulary of ltree[] columns; each operator holds when
// some element of the array satisfies it.
func LtreeArray() Vocabulary {
	idx := []IndexHint{{Method: "gist", OpClass: "gist__ltree_ops"}}
	return Vocabulary{
		Name: VocabLtreeArray, Type: "_ltree", Extension: ExtLtree,
		Ops: []Op{
			{
				Name: "ancestorOf", Arg: ArgText, Extension: ExtLtree,
				Description: "Some element is an ancestor of, or equal to, the given path (@>). Cannot use an index.",
				render:      ltreeInfix("@>", "ltree"),
			},
			{
				Name: "descendantOf", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Some element is a descendant of, or equal to, the given path (<@).",
				render:      ltreeInfix("<@", "ltree"),
			},
			{
				Name: "matchesLquery", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Some element matches the given lquery pattern (~).",
				render:      ltreeInfix("~", "lquery"),
			},
			{
				Name: "matchesAnyLquery", Arg: ArgTextList, Index: idx, Extension: ExtLtree,
				Description: "Some element matches any of the given lquery patterns (?).",
				render:      ltreeInfix("?", "lquery[]"),
			},
			{
				Name: "matchesLtxtquery", Arg: ArgText, Index: idx, Extension: ExtLtree,
				Description: "Some element matches the given ltxtquery (@).",
				render:      ltreeInfix("@", "ltxtquery"),
			},
		},
		Codec: &Codec{
			Arg: ArgTextList,
			sql: func(q qual, a []string) string { return q.cast(a[0], "ltree[]") },
		},
	}
}

// Citext is the vocabulary of citext columns' own values: no filter
// operators of its own (the text vocabularies apply), but a codec that keeps
// operands typed as citext.
//
// citext's comparison and pattern operators (=, <>, <, <=, >, >=, ~~ for
// LIKE, ~~* for ILIKE, !~~, !~~*, ~, ~*, !~, !~*) are case-insensitive, but
// they live in the extension's schema like everything else. Where that
// schema is not on the search_path, or the operand is typed text, Postgres
// silently resolves citext_col = $1 to the case-sensitive text operator
// through citext's implicit cast to text. A compiler must therefore cast the
// operand with Codec.SQL and spell the operator with
// [Vocabulary.Operator] — citext_col OPERATOR("ext".=) $1::"ext".citext, and
// OPERATOR("ext".~~) instead of the LIKE keyword. ORDER BY and indexes use
// the type's default operator class and need neither.
func Citext() Vocabulary {
	return Vocabulary{
		Name: VocabCitext, Type: "citext", Extension: ExtCitext,
		Codec: &Codec{
			Arg: ArgText,
			sql: func(q qual, a []string) string { return q.cast(a[0], "citext") },
		},
	}
}
