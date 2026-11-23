package contrib

// HstoreSQL renders the hstore value of a placeholder bound to the text[]
// parameter [HstoreParam] returns, through the hstore(text[]) constructor.
// schema qualifies the extension's type and function ("" for none).
func HstoreSQL(schema, placeholder string) string {
	q := qual{schema: schema}
	return q.fn("hstore") + "(" + atom(placeholder) + "::text[])"
}

// HstoreOutputSQL renders an hstore reference as a jsonb object of string
// or null values (hstore_to_jsonb, which is strict: NULL in, NULL out).
func HstoreOutputSQL(schema, ref string) string {
	return qual{schema: schema}.fn("hstore_to_jsonb") + "(" + ref + ")"
}

// HstoreOutputJSONSQL is [HstoreOutputSQL] producing json (hstore_to_json,
// strict, with the same string-or-null values). Cheaper when the value is only
// embedded in a response.
func HstoreOutputJSONSQL(schema, ref string) string {
	return qual{schema: schema}.fn("hstore_to_json") + "(" + ref + ")"
}

// Hstore is the vocabulary of hstore columns. Values are JSON objects of
// string keys to string or null values on the way in and out.
func Hstore() Vocabulary {
	idx := []IndexHint{{Method: "gin", OpClass: "gin_hstore_ops"}, {Method: "gist", OpClass: "gist_hstore_ops"}}
	return Vocabulary{
		Name: VocabHstore, Type: "hstore", Extension: ExtHstore,
		Ops: []Op{
			{
				Name: "hasKey", Arg: ArgText, Index: idx, Extension: ExtHstore,
				Description: "Contains the given key (?).",
				render: func(q qual, col string, a []string) string {
					return paren(col) + " " + q.op("?") + " " + atom(a[0]) + "::text"
				},
			},
			{
				Name: "hasAllKeys", Arg: ArgTextList, Index: idx, Extension: ExtHstore,
				Description: "Contains all of the given keys (?&).",
				render: func(q qual, col string, a []string) string {
					return paren(col) + " " + q.op("?&") + " " + atom(a[0]) + "::text[]"
				},
			},
			{
				Name: "hasAnyKeys", Arg: ArgTextList, Index: idx, Extension: ExtHstore,
				Description: "Contains any of the given keys (?|).",
				render: func(q qual, col string, a []string) string {
					return paren(col) + " " + q.op("?|") + " " + atom(a[0]) + "::text[]"
				},
			},
			{
				Name: "contains", Arg: ArgHstore, Index: idx, Extension: ExtHstore,
				Description: "Contains every key/value pair of the given object (@>).",
				render: func(q qual, col string, a []string) string {
					return paren(col) + " " + q.op("@>") + " " + HstoreSQL(q.schema, a[0])
				},
			},
			{
				Name: "containedBy", Arg: ArgHstore, Extension: ExtHstore,
				Description: "Every key/value pair is in the given object (<@). Cannot use an index.",
				render: func(q qual, col string, a []string) string {
					return paren(col) + " " + q.op("<@") + " " + HstoreSQL(q.schema, a[0])
				},
			},
		},
		Codec: &Codec{
			Arg:        ArgHstore,
			sql:        func(q qual, a []string) string { return HstoreSQL(q.schema, a[0]) },
			output:     func(q qual, ref string) string { return HstoreOutputSQL(q.schema, ref) },
			outputJSON: func(q qual, ref string) string { return HstoreOutputJSONSQL(q.schema, ref) },
		},
	}
}
