package contrib

import "strconv"

func ltreeInfix(op, operandType string) func(qual, string, []string) string {
	return func(q qual, col string, a []string) string {
		return paren(col) + " " + q.op(op) + " " + q.cast(a[0], operandType)
	}
}

func ltreeAccessors() []Accessor {
	nlevel := func(q qual, ref string) string { return q.fn("nlevel") + "(" + ref + ")" }
	return []Accessor{
		{
			Name: "nlevel", Type: "int4",
			Description: "Number of labels in the path (nlevel).",
			sql:         nlevel,
		},
		{
			Name: "parent", Type: "ltree",
			Description: "The path without its last label (subpath(path, 0, nlevel(path) - 1)): empty for a " +
				"single-label path, null for the empty path.",
			sql: func(q qual, ref string) string {
				n := nlevel(q, ref)
				return "CASE WHEN " + n + " > 0 THEN " + q.fn("subpath") + "(" + ref + ", 0, " + n + " - 1) END"
			},
		},
	}
}

// ltreeExprs are the derived values of an ltree path that take an operand.
// subpath is guarded so that positions outside a row's path yield NULL
// instead of ltree's "invalid positions" error.
func ltreeExprs() []Expr {
	return []Expr{
		{
			Name: "lca", Arg: ArgText, Type: "ltree", Extension: ExtLtree, Derived: true,
			Description: "Longest common ancestor of the path and the given path (lca); empty when they share no label.",
			render: func(q qual, col string, a []string) string {
				return q.fn("lca") + "(" + col + ", " + q.cast(a[0], "ltree") + ")"
			},
		},
		{
			Name: "subpath", Arg: ArgSubpath, Type: "ltree", Extension: ExtLtree, Derived: true,
			Description: "The labels from offset (counted from the end when negative) on, length of them or the " +
				"rest of the path (subpath); null when offset lies outside the path.",
			render: func(q qual, col string, a []string) string {
				n := q.fn("nlevel") + "(" + col + ")"
				off := atom(a[0]) + "::int4"
				return "CASE WHEN " + off + " < " + n + " AND -" + off + " <= " + n + " THEN " +
					q.fn("subpath") + "(" + col + ", " + off + ", COALESCE(" + atom(a[1]) + "::int4, " +
					strconv.Itoa(MaxLtreeLevels) + ")) END"
			},
		},
	}
}

// Ltree is the vocabulary of ltree columns. Paths, lqueries and ltxtqueries
// are strings; Postgres validates their syntax. The nlevel and parent
// accessors and the lca and subpath expressions ([Expr.Derived]) derive
// paths and depths from the column.
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
		Exprs:     ltreeExprs(),
		Codec: &Codec{
			Arg: ArgText,
			sql: func(q qual, a []string) string { return q.cast(a[0], "ltree") },
		},
	}
}

// LtreeArray is the vocabulary of ltree[] columns; each operator holds when
// some element of the array satisfies it. The lca accessor is the longest
// common ancestor of the non-null elements.
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
		Accessors: []Accessor{{
			Name: "lca", Type: "ltree",
			Description: "Longest common ancestor of the non-null paths (lca); null for an empty array.",
			sql: func(q qual, ref string) string {
				return q.fn("lca") + "(array_remove(" + ref + ", NULL))"
			},
		}},
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
