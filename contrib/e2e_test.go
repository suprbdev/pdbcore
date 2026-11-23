package contrib_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/pgexec"
)

const (
	dataSchema = "contrib_e2e"
	extSchema  = "contrib e2e ext"
)

// liveDB is a transaction that is rolled back at the end of the test, with
// the extensions installed in extSchema — a schema that needs quoting and is
// not on the search_path — unless the database already has them.
type liveDB struct {
	t   *testing.T
	ctx context.Context
	tx  pgx.Tx
	cat *introspect.Catalog
}

func openLive(t *testing.T) *liveDB {
	t.Helper()
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	d := &liveDB{t: t, ctx: ctx, tx: tx}
	d.exec(`CREATE SCHEMA ` + quote(extSchema))
	d.exec(`CREATE SCHEMA ` + dataSchema)
	for _, ext := range []string{contrib.ExtTrgm, contrib.ExtHstore, contrib.ExtLtree, contrib.ExtCitext, contrib.ExtUnaccent, contrib.ExtFuzzy, contrib.ExtIntarray} {
		d.exec(`CREATE EXTENSION IF NOT EXISTS ` + ext + ` SCHEMA ` + quote(extSchema))
	}
	d.exec(`SET LOCAL search_path TO ` + dataSchema)
	d.exec(`CREATE TYPE floatrange AS RANGE (subtype = float8)`)
	d.exec(`CREATE TYPE "Span Range" AS RANGE (subtype = int4, multirange_type_name = "Span Multi")`)
	cat, err := introspect.Introspect(ctx, tx, []string{dataSchema})
	if err != nil {
		t.Fatal(err)
	}
	d.cat = cat
	for _, ext := range []string{contrib.ExtTrgm, contrib.ExtHstore, contrib.ExtLtree, contrib.ExtCitext, contrib.ExtUnaccent, contrib.ExtFuzzy, contrib.ExtIntarray} {
		if s := d.schemaOf(ext); s != extSchema {
			t.Logf("%s is already installed in schema %q; testing it there", ext, s)
		}
	}
	return d
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// schemaOf returns the schema an extension actually lives in.
func (d *liveDB) schemaOf(ext string) string {
	e := d.cat.Extension(ext)
	if e == nil {
		d.t.Fatalf("extension %s not installed", ext)
	}
	return e.Schema
}

func (d *liveDB) exec(sql string, args ...any) {
	d.t.Helper()
	if _, err := d.tx.Exec(d.ctx, sql, args...); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
}

// query runs q in a savepoint under the extended and the simple protocol
// (which pgx interpolates client-side, so operators such as ? must survive
// it) and scans the extended-protocol result into dest.
func (d *liveDB) query(q string, args []any, dest any) error {
	for i, mode := range []pgx.QueryExecMode{pgx.QueryExecModeCacheStatement, pgx.QueryExecModeSimpleProtocol} {
		sp, err := d.tx.Begin(d.ctx)
		if err != nil {
			return err
		}
		var scratch any
		target := dest
		if i > 0 {
			target = &scratch
		}
		err = sp.QueryRow(d.ctx, q, append([]any{mode}, args...)...).Scan(target)
		if err != nil {
			_ = sp.Rollback(d.ctx)
			return fmt.Errorf("%v: %w", mode, err)
		}
		if err := sp.Commit(d.ctx); err != nil {
			return err
		}
	}
	return nil
}

// args numbers placeholders for a list of parameter values.
type args struct{ vals []any }

func (a *args) add(vs ...any) []string {
	phs := make([]string, len(vs))
	for i, v := range vs {
		a.vals = append(a.vals, v)
		phs[i] = "$" + strconv.Itoa(len(a.vals))
	}
	return phs
}

// sampleColumns lists the columns of the e2e table: name, declared type,
// and the values of the rows (nil is NULL), fed through each column's codec.
var sampleColumns = []struct {
	name, typ string
	rows      []any
}{
	{"doc", "tsvector", []any{"cat:1 dog:2", nil, ""}},
	{"body", "text", []any{"The quick brown fox", nil, ""}},
	{"title", "varchar(50)", []any{"hello world", nil, ""}},
	{"code", "char(12)", []any{"hello", nil, ""}},
	{"name", "EXT.citext", []any{"Hello World", nil, ""}},
	{"name_d", "name_d", []any{"Hello World", nil, ""}},
	{"attrs", "EXT.hstore", []any{map[string]any{"a": "1", "b": nil, "c": "3"}, nil, map[string]any{}}},
	{"attrs_d", "attrs_d", []any{map[string]any{"a": "1"}, nil, map[string]any{}}},
	{"path", "EXT.ltree", []any{"a.b.c", nil, ""}},
	{"paths", "EXT.ltree[]", []any{[]any{"a.b.c", "x.y"}, nil, []any{}}},
	{"i4", "int4range", []any{"[1,10)", nil, "empty"}},
	{"i8", "int8range", []any{map[string]any{"start": 1, "end": 10}, nil, map[string]any{"end": 0}}},
	{"num", "numrange", []any{map[string]any{"start": "1.5", "end": 10, "endInclusive": true}, nil, "(,)"}},
	{"ts", "tsrange", []any{map[string]any{"start": "2024-01-01 00:00", "end": "2025-01-01"}, nil, "empty"}},
	{"tstz", "tstzrange", []any{map[string]any{"start": "2024-01-01T00:00:00Z"}, nil, "empty"}},
	{"d", "daterange", []any{"[2024-01-01,2024-12-31]", nil, "empty"}},
	{"i4m", "int4multirange", []any{"{[1,3), [5,7)}", nil, []any{}}},
	{"i8m", "int8multirange", []any{[]any{map[string]any{"start": 1, "end": 3}}, nil, "{}"}},
	{"numm", "nummultirange", []any{[]any{"[1.5,2]", "empty"}, nil, "{}"}},
	{"tsm", "tsmultirange", []any{[]any{map[string]any{"start": "2024-01-01"}}, nil, "{}"}},
	{"tstzm", "tstzmultirange", []any{"{[2024-01-01T00:00:00Z,)}", nil, "{}"}},
	{"dm", "datemultirange", []any{"{[2024-01-01,2024-02-01)}", nil, "{}"}},
	{"fr", "floatrange", []any{map[string]any{"start": 1.5, "end": 2}, nil, "empty"}},
	{"frm", "floatmultirange", []any{"{[1,2.5), [3,)}", nil, "{}"}},
	{"span", `"Span Range"`, []any{"[1,10)", nil, "empty"}},
	{"spanm", `"Span Multi"`, []any{[]any{map[string]any{"start": 1, "end": 3}}, nil, "{}"}},
	{"addr", "inet", []any{"10.1.2.3", nil, "::1"}},
	{"net", "cidr", []any{"10.0.0.0/8", nil, "2001:db8::/32"}},
	{"ids", "int4[]", []any{"{1,2,3}", nil, "{}"}},
	{"meta", "jsonb", []any{`{"title": "The quick brown fox", "tags": ["cat", 1]}`, nil, "{}"}},
	{"meta_j", "json", []any{`{"title": "hello world"}`, nil, "[]"}},
}

// textCast renders a value for a column without a codec from its text form.
func textCast(typ, ph string) string { return ph + "::text::" + typ }

func setupTable(t *testing.T, d *liveDB) {
	t.Helper()
	extType := func(typ string) string {
		for _, e := range []string{contrib.ExtHstore, contrib.ExtLtree, contrib.ExtCitext} {
			typ = strings.ReplaceAll(typ, "EXT."+e, quote(d.schemaOf(e))+"."+e)
		}
		return typ
	}
	d.exec(`CREATE DOMAIN attrs_d AS ` + extType("EXT.hstore"))
	d.exec(`CREATE DOMAIN name_d AS ` + extType("EXT.citext"))
	var cols []string
	for _, c := range sampleColumns {
		cols = append(cols, c.name+" "+extType(c.typ))
	}
	d.exec(`CREATE TABLE samples (id int PRIMARY KEY, ` + strings.Join(cols, ", ") + `)`)
	cat, err := introspect.Introspect(d.ctx, d.tx, []string{dataSchema})
	if err != nil {
		t.Fatal(err)
	}
	d.cat = cat
	tbl := cat.Table(dataSchema, "samples")
	for row := range 3 {
		var a args
		exprs := []string{a.add(row)[0]}
		names := []string{"id"}
		for _, c := range sampleColumns {
			v := c.rows[row]
			names = append(names, c.name)
			if v == nil {
				exprs = append(exprs, "NULL")
				continue
			}
			codec := codecFor(t, d, tbl.Column(c.name))
			if codec == nil {
				exprs = append(exprs, textCast(extType(c.typ), a.add(v)[0]))
				continue
			}
			ps, err := codec.Params(v)
			if err != nil {
				t.Fatalf("%s row %d: %v", c.name, row, err)
			}
			exprs = append(exprs, codec.SQL(a.add(ps...)...))
		}
		q := `INSERT INTO samples (` + strings.Join(names, ", ") + `) VALUES (` + strings.Join(exprs, ", ") + `)`
		if _, err := d.tx.Exec(d.ctx, q, a.vals...); err != nil {
			t.Fatalf("insert row %d: %v\n%s", row, err, q)
		}
	}
}

func codecFor(t *testing.T, d *liveDB, col *introspect.Column) *contrib.Codec {
	t.Helper()
	for _, v := range contrib.ForColumn(d.cat, col) {
		if v.Codec != nil {
			return v.Codec
		}
	}
	return nil
}

// operand returns a sample operand for an operator of a vocabulary.
func operand(v contrib.Vocabulary, op contrib.Op) any {
	switch v.Name {
	case contrib.VocabFullText, contrib.VocabTextSearch, contrib.VocabJSONSearch:
		return map[string]any{"query": "cat & dog", "config": "english"}
	case contrib.VocabLtree, contrib.VocabLtreeArray:
		switch op.Name {
		case "matchesLquery":
			return "*.b.*"
		case "matchesAnyLquery":
			return []any{"*.b.*", "x.*"}
		case "matchesLtxtquery":
			return "b & !z"
		case "ancestorOfAny", "descendantOfAny":
			return []any{"a.b", "x"}
		}
		return "a.b"
	case contrib.VocabInet:
		return "10.0.0.0/8"
	case contrib.VocabRange, contrib.VocabMultirange:
		el := map[string]any{
			"int4range": 5, "int8range": 5, "numrange": "2.5", "tsrange": "2024-06-01",
			"tstzrange": "2024-06-01T00:00:00Z", "daterange": "2024-06-01",
			"floatrange": 2.5, "Span Range": 5, "Span Multi": 5,
		}[strings.Replace(v.Type, "multirange", "range", 1)]
		if op.Arg == contrib.ArgElement {
			return el
		}
		return map[string]any{"start": el}
	case contrib.VocabFuzzy:
		if op.Arg == contrib.ArgTextDistance {
			return map[string]any{"text": "hello", "distance": 3}
		}
		return "hello"
	case contrib.VocabIntarray:
		return "1&(2|!4)"
	case contrib.VocabHstore:
		switch op.Arg {
		case contrib.ArgTextList:
			return []any{"a", "z"}
		case contrib.ArgHstore:
			return map[string]any{"a": "1", "b": nil}
		}
		return "a"
	}
	return "hello"
}

// exprOperand returns a sample operand for an expression.
func exprOperand(e contrib.Expr) any {
	switch e.Name {
	case "subpath":
		return map[string]any{"offset": 1, "length": 1}
	case "lca":
		return "a.b"
	case "value":
		return "a"
	}
	return "hello"
}

// TestEveryOpExecutes renders every operator, expression and accessor of
// every vocabulary ForColumn finds for each sample column, bound from the
// live catalog, and runs it over the sample rows (values, NULLs, empties).
func TestEveryOpExecutes(t *testing.T) {
	d := openLive(t)
	setupTable(t, d)
	tbl := d.cat.Table(dataSchema, "samples")
	seen := map[string]bool{}
	for _, c := range sampleColumns {
		col := tbl.Column(c.name)
		vs := contrib.ForColumn(d.cat, col)
		if len(vs) == 0 {
			t.Errorf("%s (%s): no vocabulary", c.name, col.PGType)
			continue
		}
		ref := "t." + c.name
		for _, v := range vs {
			seen[v.Name] = true
			if v.Extension != "" && v.Schema != d.schemaOf(v.Extension) {
				t.Errorf("%s %s: bound to %q", c.name, v.Name, v.Schema)
			}
			for _, op := range v.Ops {
				ps, err := op.Params(operand(v, op))
				if err != nil {
					t.Errorf("%s %s: %v", c.name, op.Name, err)
					continue
				}
				var a args
				phs := a.add(ps...)
				pred := op.Render(ref, phs...)
				q := "SELECT count(*) FROM samples AS t WHERE " + pred
				var n int64
				if err := d.query(q, a.vals, &n); err != nil {
					t.Errorf("%s %s: %v\n%s", c.name, op.Name, err, q)
				}
				if rank := op.RankSQL(contrib.RankCoverDensity, ref, phs...); rank != "" {
					var r any
					q := "SELECT max(" + rank + ") FROM samples AS t"
					if err := d.query(q, a.vals, &r); err != nil {
						t.Errorf("%s %s rank: %v\n%s", c.name, op.Name, err, q)
					}
					doc := "t.body"
					if contrib.IsDocument(col.PGType) {
						doc = ref
					}
					opts := a.add(`MaxWords=5, MinWords=2, StartSel="<em class=hit>", FragmentDelimiter=" , "`)
					q = "SELECT array_agg((" + op.HeadlineSQL(doc, opts[0], phs...) + ")::text ORDER BY id) FROM samples AS t"
					if err := d.query(q, a.vals, &r); err != nil {
						t.Errorf("%s %s headline: %v\n%s", c.name, op.Name, err, q)
					}
				}
			}
			for _, e := range v.Exprs {
				var a args
				ps, err := e.Params(exprOperand(e))
				if err != nil {
					t.Errorf("%s %s: %v", c.name, e.Name, err)
					continue
				}
				q := "SELECT max((" + e.Render(ref, a.add(ps...)...) + ")::text) FROM samples AS t"
				var r any
				if err := d.query(q, a.vals, &r); err != nil {
					t.Errorf("%s %s: %v\n%s", c.name, e.Name, err, q)
				}
			}
			for _, acc := range v.Accessors {
				q := "SELECT " + acc.SQL(ref) + " FROM samples AS t ORDER BY id LIMIT 1"
				var r any
				if err := d.query(q, nil, &r); err != nil {
					t.Errorf("%s %s: %v\n%s", c.name, acc.Name, err, q)
				}
			}
			if v.Codec != nil {
				q := "SELECT jsonb_agg(" + v.Codec.OutputSQL(ref) + " ORDER BY id) FROM samples AS t"
				var r any
				if err := d.query(q, nil, &r); err != nil {
					t.Errorf("%s output: %v\n%s", c.name, err, q)
				}
			}
		}
	}
	if d.schemaOf(contrib.ExtHstore) == extSchema {
		op, _ := contrib.Hstore().Op("hasKey")
		var n int64
		if err := d.query("SELECT count(*) FROM samples AS t WHERE "+op.Render("t.attrs", "$1"), []any{"a"}, &n); err == nil {
			t.Error("an unbound operator resolved although the extension schema is not on the search_path")
		}
	}
	for _, n := range []string{contrib.VocabFullText, contrib.VocabTextSearch, contrib.VocabJSONSearch, contrib.VocabTrgm, contrib.VocabCitext,
		contrib.VocabHstore, contrib.VocabLtree, contrib.VocabLtreeArray, contrib.VocabRange, contrib.VocabMultirange, contrib.VocabInet,
		contrib.VocabUnaccent, contrib.VocabFuzzy, contrib.VocabIntarray} {
		if !seen[n] {
			t.Errorf("vocabulary %s not exercised", n)
		}
	}
}

// eval evaluates one operator of the vocabulary bound for a column type over
// a column value (fed through the column's codec, or cast to text) and
// returns the predicate's result.
func (d *liveDB) eval(t *testing.T, typ string, colValue any, opName string, operandValue any) bool {
	t.Helper()
	vs := contrib.ForType(d.cat, typ, false)
	op, ok := contrib.FindOp(vs, opName)
	if !ok {
		t.Fatalf("%s: op %s not found", typ, opName)
	}
	var a args
	colSQL := ""
	for _, v := range vs {
		if v.Codec != nil {
			ps, err := v.Codec.Params(colValue)
			if err != nil {
				t.Fatal(err)
			}
			colSQL = v.Codec.SQL(a.add(ps...)...)
		}
	}
	if colSQL == "" {
		colSQL = textCast(typ, a.add(colValue)[0])
	}
	ps, err := op.Params(operandValue)
	if err != nil {
		t.Fatalf("%s %s(%v): %v", typ, opName, operandValue, err)
	}
	q := "SELECT (" + op.Render("t.c", a.add(ps...)...) + ") IS TRUE FROM (SELECT " + colSQL + " AS c) AS t"
	var got bool
	if err := d.query(q, a.vals, &got); err != nil {
		t.Fatalf("%s %s(%v): %v\n%s", typ, opName, operandValue, err, q)
	}
	return got
}

func TestOpValuesLive(t *testing.T) {
	d := openLive(t)
	en := func(q string) map[string]any { return map[string]any{"query": q, "config": "english"} }
	simple := func(q string) map[string]any { return map[string]any{"query": q, "config": "simple"} }
	tests := []struct {
		typ      string
		col      any
		op       string
		operand  any
		expected bool
	}{
		{"tsvector", "cat:1 dog:2", "matches", en("cats & dogs"), true},
		{"tsvector", "cat:1 dog:2", "matches", en("cat & !dog"), false},
		{"tsvector", "cat:1 dog:2", "matches", "cat:*", true},
		{"tsvector", "cat:1 dog:2", "matchesPlain", en("cats dogs"), true},
		{"tsvector", "cat:1 dog:2", "matchesPhrase", en("cat dog"), true},
		{"tsvector", "cat:1 dog:2", "matchesPhrase", en("dog cat"), false},
		{"tsvector", "cat:1 dog:2", "matchesWebsearch", en(`cat -dog`), false},
		{"tsvector", "cat:1 dog:2", "matchesWebsearch", en(`"cat dog" or bird`), true},
		{"text", "The quick brown fox", "matchesWebsearch", en("quick foxes"), true},
		{"text", "The quick brown fox", "matches", en("quick & cat"), false},
		{"text", "The quick brown fox", "matchesPhrase", simple("brown fox"), true},
		{"text", "The quick brown fox", "matchesPhrase", simple("fox brown"), false},
		{"varchar", "The quick brown fox", "matchesPlain", simple("quick"), true},
		{"jsonb", `{"title": "The quick brown fox", "n": 7}`, "matchesWebsearch", en("quick foxes"), true},
		{"jsonb", `{"title": "The quick brown fox"}`, "matches", simple("title"), false},
		{"jsonb", `{"tags": ["cat dog"]}`, "matchesPhrase", simple("cat dog"), true},
		{"jsonb", `{"tags": ["cat", "dog"]}`, "matchesPhrase", simple("cat dog"), false},
		{"json", `{"title": "The quick brown fox"}`, "matchesPlain", en("brown"), true},
		{"json", `{"n": 42}`, "matches", simple("42"), false},
		{"text", "hello world", "similarTo", "hello world!", true},
		{"text", "hello world", "similarTo", "xyz", false},
		{"text", "hello world", "wordSimilarTo", "worl", true},
		{"text", "hello world", "wordSimilarTo", "xyz", false},
		{"text", "hello world", "strictWordSimilarTo", "world", true},
		{"text", "hello world", "strictWordSimilarTo", "xyz", false},
		{"citext", "Hello World", "similarTo", "hello world", true},
		{"bpchar", "hello", "similarTo", "hello", true},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "hasKey", "a", true},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "hasKey", "z", false},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "hasAllKeys", []any{"a", "b"}, true},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "hasAllKeys", []any{"a", "z"}, false},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "hasAnyKeys", []any{"z", "c"}, true},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "hasAnyKeys", []any{"z"}, false},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "contains", map[string]any{"a": "1", "b": nil}, true},
		{"hstore", map[string]any{"a": "1", "b": nil, "c": "3"}, "contains", map[string]any{"a": "2"}, false},
		{"hstore", map[string]any{"a": "1", "b": nil}, "containedBy", map[string]any{"a": "1", "b": nil, "c": "3"}, true},
		{"hstore", map[string]any{"a": "1", "b": nil}, "containedBy", map[string]any{"a": "1"}, false},
		{"hstore", map[string]any{"k\"=>": "v,\\"}, "contains", map[string]any{"k\"=>": "v,\\"}, true},
		{"ltree", "a.b.c", "ancestorOf", "a.b.c.d", true},
		{"ltree", "a.b.c", "ancestorOf", "a.x", false},
		{"ltree", "a.b.c", "descendantOf", "a.b", true},
		{"ltree", "a.b.c", "descendantOf", "x", false},
		{"ltree", "a.b.c", "ancestorOfAny", []any{"x", "a.b.c.d"}, true},
		{"ltree", "a.b.c", "ancestorOfAny", []any{"x"}, false},
		{"ltree", "a.b.c", "descendantOfAny", []any{"x", "a"}, true},
		{"ltree", "a.b.c", "descendantOfAny", []any{"x"}, false},
		{"ltree", "a.b.c", "matchesLquery", "*.b.*", true},
		{"ltree", "a.b.c", "matchesLquery", "x.*", false},
		{"ltree", "a.b.c", "matchesAnyLquery", []any{"x.*", "a.*"}, true},
		{"ltree", "a.b.c", "matchesAnyLquery", []any{"x.*"}, false},
		{"ltree", "a.b.c", "matchesLtxtquery", "b & !z", true},
		{"ltree", "a.b.c", "matchesLtxtquery", "z", false},
		{"_ltree", []any{"a.b.c", "x.y"}, "ancestorOf", "x.y.z", true},
		{"_ltree", []any{"a.b.c", "x.y"}, "ancestorOf", "q", false},
		{"_ltree", []any{"a.b.c", "x.y"}, "descendantOf", "a", true},
		{"_ltree", []any{"a.b.c", "x.y"}, "descendantOf", "q", false},
		{"_ltree", []any{"a.b.c", "x.y"}, "matchesLquery", "x.*", true},
		{"_ltree", []any{"a.b.c", "x.y"}, "matchesAnyLquery", []any{"q", "*.c"}, true},
		{"_ltree", []any{"a.b.c", "x.y"}, "matchesLtxtquery", "y", true},
		{"_ltree", []any{"a.b.c", "x.y"}, "matchesLtxtquery", "q", false},
		{"int4range", "[1,10)", "overlaps", map[string]any{"start": 5, "end": 20}, true},
		{"int4range", "[1,10)", "overlaps", map[string]any{"start": 10}, false},
		{"int4range", "[1,10)", "contains", "[2,5)", true},
		{"int4range", "[1,10)", "contains", "[2,10]", false},
		{"int4range", "[1,10)", "containsElement", 9, true},
		{"int4range", "[1,10)", "containsElement", 10, false},
		{"int4range", "[1,10)", "containedBy", map[string]any{"start": 0}, true},
		{"int4range", "[1,10)", "containedBy", "[2,)", false},
		{"int4range", "[1,10)", "strictlyLeftOf", map[string]any{"start": 10}, true},
		{"int4range", "[1,10)", "strictlyLeftOf", map[string]any{"start": 9}, false},
		{"int4range", "[1,10)", "strictlyRightOf", map[string]any{"end": 1}, true},
		{"int4range", "[1,10)", "strictlyRightOf", map[string]any{"end": 2}, false},
		{"int4range", "[1,10)", "notExtendsRightOf", "[0,10)", true},
		{"int4range", "[1,10)", "notExtendsRightOf", "[0,9)", false},
		{"int4range", "[1,10)", "notExtendsLeftOf", "[1,2)", true},
		{"int4range", "[1,10)", "notExtendsLeftOf", "[2,3)", false},
		{"int4range", "[1,10)", "adjacentTo", "[10,20)", true},
		{"int4range", "[1,10)", "adjacentTo", "[9,20)", false},
		{"int4range", "empty", "overlaps", "(,)", false},
		{"int4range", "[1,10)", "contains", map[string]any{"isEmpty": true}, true},
		{"numrange", "[1.5,2.5]", "containsElement", "2.5", true},
		{"numrange", "[1.5,2.5]", "containsElement", 2.75, false},
		{"tstzrange", map[string]any{"start": "2024-01-01T00:00:00Z", "end": "2025-01-01T00:00:00Z"}, "containsElement", "2024-06-01T00:00:00Z", true},
		{"tsrange", "[2024-01-01,2025-01-01)", "containsElement", "2025-01-01", false},
		{"daterange", "[2024-01-01,2024-01-31]", "adjacentTo", "[2024-02-01,2024-03-01)", true},
		{"int4multirange", "{[1,3), [5,7)}", "overlaps", "[3,5)", false},
		{"int4multirange", "{[1,3), [5,7)}", "overlaps", "[2,6)", true},
		{"int4multirange", "{[1,3), [5,7)}", "containsElement", 6, true},
		{"int4multirange", "{[1,3), [5,7)}", "containsElement", 4, false},
		{"int4multirange", "{[1,3), [5,7)}", "contains", "[5,6)", true},
		{"int4multirange", "{[1,3), [5,7)}", "containedBy", "[0,7)", true},
		{"int4multirange", "{[1,3), [5,7)}", "strictlyLeftOf", "[7,8)", true},
		{"int4multirange", "{[1,3), [5,7)}", "adjacentTo", "[7,8)", true},
		{"datemultirange", []any{"[2024-01-01,2024-02-01)"}, "containsElement", "2024-01-15", true},
		{"inet", "10.1.2.3", "containedBy", "10.0.0.0/8", true},
		{"inet", "10.1.2.3", "containedBy", "10.1.2.3/32", false},
		{"inet", "10.1.2.3", "containedByOrEquals", "10.1.2.3/32", true},
		{"inet", "10.1.2.3", "containedByOrEquals", "11.0.0.0/8", false},
		{"cidr", "10.0.0.0/8", "contains", "10.1.0.0/16", true},
		{"cidr", "10.0.0.0/8", "contains", "10.0.0.0/8", false},
		{"cidr", "10.0.0.0/8", "containsOrEquals", "10.0.0.0/8", true},
		{"cidr", "10.0.0.0/8", "containsOrContainedBy", "10.1.0.0/16", true},
		{"cidr", "10.0.0.0/8", "containsOrContainedBy", "11.0.0.0/8", false},
		{"cidr", "2001:db8::/32", "contains", "2001:db8::1", true},
		{"hstore", map[string]any{"a": "1", "b": nil}, "hasDefinedKey", "a", true},
		{"hstore", map[string]any{"a": "1", "b": nil}, "hasDefinedKey", "b", false},
		{"hstore", map[string]any{"a": "1", "b": nil}, "hasDefinedKey", "z", false},
		{"text", "kitten", "levenshteinDistanceLessThan", map[string]any{"text": "sitting", "distance": 4}, true},
		{"text", "kitten", "levenshteinDistanceLessThan", map[string]any{"text": "sitting", "distance": 3}, false},
		{"text", "kitten", "levenshteinDistanceLessThan", map[string]any{"text": "kitten", "distance": 0}, false},
		{"text", "kitten", "levenshteinDistanceLessThan", map[string]any{"text": "kitten", "distance": 1}, true},
		{"text", strings.Repeat("a", 300), "levenshteinDistanceLessThan", map[string]any{"text": "a", "distance": 1000}, false},
		{"varchar", "Robert", "soundexEqualTo", "Rupert", true},
		{"bpchar", "Robert", "soundexEqualTo", "Smith", false},
		{"citext", "Knight", "metaphoneEqualTo", "night", true},
		{"text", "Knight", "metaphoneEqualTo", "Kite", false},
		{"text", strings.Repeat("a", 300), "metaphoneEqualTo", "a", false},
		{"text", "Smith", "doubleMetaphoneEqualTo", "Smyth", true},
		{"text", "Smith", "doubleMetaphoneEqualTo", "Jones", false},
		{"_int4", "{1,2,3}", "matchesQueryInt", "1&(2|4)", true},
		{"_int4", "{1,2,3}", "matchesQueryInt", "4", false},
		{"_int4", "{1,2,3}", "matchesQueryInt", "!4", true},
		{"_int4", "{}", "matchesQueryInt", "!4", true},
	}
	for _, tc := range tests {
		if got := d.eval(t, tc.typ, tc.col, tc.op, tc.operand); got != tc.expected {
			t.Errorf("%s %v %s %v = %v, want %v", tc.typ, tc.col, tc.op, tc.operand, got, tc.expected)
		}
	}
}

// scalar evaluates expr (with placeholders bound to vals) to a Go value.
func (d *liveDB) scalar(t *testing.T, expr string, vals ...any) any {
	t.Helper()
	var got any
	if err := d.query("SELECT "+expr, vals, &got); err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return got
}

func TestExprsAndAccessorsLive(t *testing.T) {
	d := openLive(t)
	trgm := contrib.ForType(d.cat, "text", false)[1]
	sim, _ := trgm.Expr("similarity")
	dist, _ := trgm.Expr("distance")
	s := d.scalar(t, sim.Render("$1::text", "$2"), "hello world", "hello word").(float32)
	ds := d.scalar(t, dist.Render("$1::text", "$2"), "hello world", "hello word").(float32)
	if s <= 0.3 || s >= 1 || fmt.Sprintf("%.4f", 1-s) != fmt.Sprintf("%.4f", ds) {
		t.Errorf("similarity %v, distance %v", s, ds)
	}
	for _, name := range []string{"wordSimilarity", "strictWordSimilarity", "wordDistance", "strictWordDistance"} {
		e, _ := trgm.Expr(name)
		v := d.scalar(t, e.Render("$1::text", "$2"), "hello world", "world").(float32)
		if v < 0 || v > 1 {
			t.Errorf("%s = %v", name, v)
		}
	}

	fts := contrib.FullText()
	op, _ := fts.Op("matchesPlain")
	if r := d.scalar(t, op.RankSQL(contrib.RankFrequency, "$1::tsvector", "$2", "$3"), "cat:1 dog:2", "cat", "english").(float32); r <= 0 {
		t.Errorf("ts_rank = %v", r)
	}

	accessor := func(typ, name string, val any) any {
		t.Helper()
		vs := contrib.ForType(d.cat, typ, false)
		a, ok := vs[0].Accessor(name)
		if !ok {
			t.Fatalf("%s: no accessor %s", typ, name)
		}
		ps, err := vs[0].Codec.Params(val)
		if err != nil {
			t.Fatal(err)
		}
		var ar args
		return d.scalar(t, a.SQL("("+vs[0].Codec.SQL(ar.add(ps...)...)+")"), ar.vals...)
	}
	tests := []struct {
		typ, name string
		val       any
		want      string
	}{
		{"ltree", "nlevel", "a.b.c", "3"},
		{"inet", "family", "10.1.2.3", "4"},
		{"inet", "family", "::1", "6"},
		{"cidr", "masklen", "10.0.0.0/8", "8"},
		{"inet", "host", "10.1.2.3/24", "10.1.2.3"},
		{"inet", "network", "10.1.2.3/24", "10.1.2.0/24"},
		{"int4range", "start", "[1,10)", "1"},
		{"int4range", "end", "[1,10]", "11"},
		{"int4range", "startInclusive", "(1,10]", "true"},
		{"numrange", "endInclusive", "(1,10]", "true"},
		{"numrange", "startInfinite", "(,10]", "true"},
		{"numrange", "endInfinite", "(,10]", "false"},
		{"int4range", "isEmpty", "empty", "true"},
		{"int4multirange", "start", "{[1,3), [5,7)}", "1"},
		{"int4multirange", "end", "{[1,3), [5,7)}", "7"},
		{"int4multirange", "isEmpty", "{}", "true"},
		{"ltree", "parent", "a.b.c", "a.b"},
		{"ltree", "parent", "a", ""},
		{"ltree", "parent", "", "<nil>"},
	}
	for _, tc := range tests {
		if got := fmt.Sprint(accessor(tc.typ, tc.name, tc.val)); got != tc.want {
			t.Errorf("%s %s(%v) = %s, want %s", tc.typ, tc.name, tc.val, got, tc.want)
		}
	}

	text := func(expr string, vals ...any) string {
		t.Helper()
		return fmt.Sprint(d.scalar(t, "("+expr+")::text", vals...))
	}
	arr, _ := contrib.ForType(d.cat, "_ltree", false)[0].Accessor("lca")
	for val, want := range map[string]string{
		"{a.b.c,a.b.d}": "a.b", "{a.b,NULL,a.c}": "a", "{x}": "", "{}": "<nil>",
	} {
		if got := text(arr.SQL(textCast(quote(d.schemaOf(contrib.ExtLtree))+".ltree[]", "$1")), val); got != want {
			t.Errorf("ltree[] lca(%s) = %s, want %s", val, got, want)
		}
	}
	icount, _ := contrib.ForType(d.cat, "_int4", false)[0].Accessor("icount")
	if got := text(icount.SQL("$1::text::int4[]"), "{1,NULL,3}"); got != "3" {
		t.Errorf("icount = %s", got)
	}

	derived := func(typ, name, col string, operand any) string {
		t.Helper()
		vs := contrib.ForType(d.cat, typ, false)
		var e contrib.Expr
		var ok bool
		for _, v := range vs {
			if e, ok = v.Expr(name); ok {
				break
			}
		}
		if !ok {
			t.Fatalf("%s: no expression %s", typ, name)
		}
		ps, err := e.Params(operand)
		if err != nil {
			t.Fatal(err)
		}
		var a args
		colSQL := textCast(typ, a.add(col)[0])
		return text(e.Render("("+colSQL+")", a.add(ps...)...), a.vals...)
	}
	hs := quote(d.schemaOf(contrib.ExtHstore)) + ".hstore"
	lt := quote(d.schemaOf(contrib.ExtLtree)) + ".ltree"
	derivedTests := []struct {
		typ, name, col string
		operand        any
		want           string
	}{
		{hs, "value", "a=>1, b=>NULL", "a", "1"},
		{hs, "value", "a=>1, b=>NULL", "b", "<nil>"},
		{hs, "value", "a=>1, b=>NULL", "z", "<nil>"},
		{lt, "lca", "a.b.c", "a.b.d", "a.b"},
		{lt, "lca", "a.b.c", "x", ""},
		{lt, "subpath", "a.b.c", map[string]any{"offset": 1}, "b.c"},
		{lt, "subpath", "a.b.c", map[string]any{"offset": 0, "length": 2}, "a.b"},
		{lt, "subpath", "a.b.c", map[string]any{"offset": -1}, "c"},
		{lt, "subpath", "a.b.c", map[string]any{"offset": -3, "length": 1}, "a"},
		{lt, "subpath", "a.b.c", map[string]any{"offset": 1, "length": 0}, ""},
		{lt, "subpath", "a.b.c", map[string]any{"offset": 1, "length": 100}, "b.c"},
		{lt, "subpath", "a.b.c", map[string]any{"offset": 3}, "<nil>"},
		{lt, "subpath", "a.b.c", map[string]any{"offset": -4}, "<nil>"},
		{lt, "subpath", "", map[string]any{"offset": 0}, "<nil>"},
		{"text", "levenshteinDistance", "kitten", "sitting", "3"},
		{"bpchar", "levenshteinDistance", "kitten", "kitten", "0"},
		{"text", "levenshteinDistance", strings.Repeat("a", 256), "a", "<nil>"},
	}
	long, _ := contrib.FindOp(contrib.ForType(d.cat, "text", false), "levenshteinDistanceLessThan")
	pred := long.Render("$1::text", "$2", "$3")
	if got := text("("+pred+") IS NULL AND (NOT ("+pred+")) IS NULL", strings.Repeat("a", 256), "a", 1000); got != "true" {
		t.Errorf("levenshtein threshold over a long value: %s", got)
	}
	for _, tc := range derivedTests {
		if got := derived(tc.typ, tc.name, tc.col, tc.operand); got != tc.want {
			t.Errorf("%s %s(%q, %v) = %s, want %s", tc.typ, tc.name, tc.col, tc.operand, got, tc.want)
		}
	}
}

// TestCodecRoundTripLive feeds values through each codec, reads them back
// with OutputSQL, re-feeds the decoded output and checks Postgres sees the
// same value.
func TestCodecRoundTripLive(t *testing.T) {
	d := openLive(t)
	tests := []struct {
		typ  string
		val  any
		want string
	}{
		{"hstore", map[string]any{"a": "1", "b": nil, "": "empty key"}, `{"": "empty key", "a": "1", "b": null}`},
		{"hstore", map[string]any{}, `{}`},
		{"int4range", "[1,10)", `{"end": 10, "start": 1, "isEmpty": false, "endInclusive": false, "startInclusive": true}`},
		{"int4range", "(1,10]", `{"end": 11, "start": 2, "isEmpty": false, "endInclusive": false, "startInclusive": true}`},
		{"int4range", "empty", `{"end": null, "start": null, "isEmpty": true, "endInclusive": false, "startInclusive": false}`},
		{"int4range", "[5,5)", `{"end": null, "start": null, "isEmpty": true, "endInclusive": false, "startInclusive": false}`},
		{"int8range", map[string]any{"start": 9007199254740993.0}, ""},
		{"numrange", map[string]any{"start": "1.50", "end": "2", "endInclusive": true}, `{"end": 2, "start": 1.50, "isEmpty": false, "endInclusive": true, "startInclusive": true}`},
		{"numrange", "(,)", `{"end": null, "start": null, "isEmpty": false, "endInclusive": false, "startInclusive": false}`},
		{"tsrange", "[2024-01-01 10:00,2024-01-02)", `{"end": "2024-01-02T00:00:00", "start": "2024-01-01T10:00:00", "isEmpty": false, "endInclusive": false, "startInclusive": true}`},
		{"tstzrange", map[string]any{"start": "2024-01-01T00:00:00Z"}, ""},
		{"daterange", "[2024-01-01,2024-01-31]", `{"end": "2024-02-01", "start": "2024-01-01", "isEmpty": false, "endInclusive": false, "startInclusive": true}`},
		{"int4multirange", "{[5,7), [1,3)}", `[{"end": 3, "start": 1, "isEmpty": false, "endInclusive": false, "startInclusive": true}, {"end": 7, "start": 5, "isEmpty": false, "endInclusive": false, "startInclusive": true}]`},
		{"int4multirange", []any{}, `[]`},
		{"nummultirange", []any{"empty", map[string]any{"start": 1}}, ""},
		{"tstzmultirange", "{}", `[]`},
		{"floatrange", map[string]any{"start": 1.5, "end": 2.25, "endInclusive": true}, `{"end": 2.25, "start": 1.5, "isEmpty": false, "endInclusive": true, "startInclusive": true}`},
		{"floatrange", "empty", `{"end": null, "start": null, "isEmpty": true, "endInclusive": false, "startInclusive": false}`},
		{"floatmultirange", []any{"[3,4)", map[string]any{"start": 1}}, `[{"end": null, "start": 1, "isEmpty": false, "endInclusive": false, "startInclusive": true}]`},
		{dataSchema + `."Span Range"`, "[1,5]", `{"end": 5, "start": 1, "isEmpty": false, "endInclusive": true, "startInclusive": true}`},
		{`"Span Multi"`, "{[1,3), [2,5)}", `[{"end": 5, "start": 1, "isEmpty": false, "endInclusive": false, "startInclusive": true}]`},
	}
	for _, tc := range tests {
		v := contrib.ForType(d.cat, tc.typ, false)[0]
		ps, err := v.Codec.Params(tc.val)
		if err != nil {
			t.Fatalf("%s %v: %v", tc.typ, tc.val, err)
		}
		var a args
		in := v.Codec.SQL(a.add(ps...)...)
		var out any
		if err := d.query("SELECT "+v.Codec.OutputSQL("("+in+")"), a.vals, &out); err != nil {
			t.Fatalf("%s %v: %v", tc.typ, tc.val, err)
		}
		if tc.want != "" {
			var want any
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out, want) {
				t.Errorf("%s %v output = %s, want %s", tc.typ, tc.val, show(out), tc.want)
			}
		}
		ps2, err := v.Codec.Params(out)
		if err != nil {
			t.Errorf("%s: output %s does not parse back: %v", tc.typ, show(out), err)
			continue
		}
		var b args
		lhs := v.Codec.OutputSQL("(" + v.Codec.SQL(b.add(ps...)...) + ")")
		rhs := v.Codec.OutputSQL("(" + v.Codec.SQL(b.add(ps2...)...) + ")")
		var jsonOut any
		if err := d.query("SELECT "+v.Codec.OutputJSONSQL("("+in+")"), a.vals, &jsonOut); err != nil {
			t.Fatalf("%s %v json: %v", tc.typ, tc.val, err)
		}
		if !reflect.DeepEqual(out, jsonOut) {
			t.Errorf("%s %v json output = %s, jsonb output = %s", tc.typ, tc.val, show(jsonOut), show(out))
		}
		var same bool
		if err := d.query("SELECT "+lhs+" = "+rhs, b.vals, &same); err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if !same && tc.typ != "int8range" {
			t.Errorf("%s %v: round trip through %s changed the value", tc.typ, tc.val, show(out))
		}
	}
	for _, expr := range []string{
		contrib.RangeOutputSQL("NULL::int4range"),
		contrib.MultirangeOutputSQL("NULL::int4multirange"),
		contrib.HstoreOutputSQL(d.schemaOf(contrib.ExtHstore), "NULL::"+quote(d.schemaOf(contrib.ExtHstore))+".hstore"),
		contrib.RangeOutputJSONSQL("NULL::int4range"),
		contrib.MultirangeOutputJSONSQL("NULL::int4multirange"),
		contrib.HstoreOutputJSONSQL(d.schemaOf(contrib.ExtHstore), "NULL::"+quote(d.schemaOf(contrib.ExtHstore))+".hstore"),
	} {
		if got := d.scalar(t, expr); got != nil {
			t.Errorf("%s = %v, want NULL", expr, got)
		}
	}
}

// TestCitextOperandTyping pins why Citext documents operand typing and
// operator qualification: without both, citext comparisons silently fall
// back to the case-sensitive text operators.
func TestCitextOperandTyping(t *testing.T) {
	d := openLive(t)
	v := contrib.ForType(d.cat, "citext", false)[0]
	col, val := v.Codec.SQL("$1"), v.Codec.SQL("$2")
	tests := []struct {
		pred string
		want bool
	}{
		{col + " " + v.Operator("=") + " " + val, true},
		{col + " " + v.Operator("~~") + " " + val, true},
		{col + " " + v.Operator("<>") + " " + val, false},
		{col + " = $2::text", false},
	}
	if v.Schema != "public" {
		tests = append(tests, struct {
			pred string
			want bool
		}{col + " = " + val, false})
	}
	for _, tc := range tests {
		var got bool
		if err := d.query("SELECT "+tc.pred, []any{"ABC", "abc"}, &got); err != nil {
			t.Fatalf("%s: %v", tc.pred, err)
		}
		if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.pred, got, tc.want)
		}
	}
	h := contrib.ForType(d.cat, "hstore", false)[0]
	var eq bool
	if err := d.query("SELECT "+h.Codec.SQL("$1")+" "+h.Operator("=")+" "+h.Codec.SQL("$2"),
		[]any{[]string{"a", "1"}, []string{"a", "1"}}, &eq); err != nil || !eq {
		t.Errorf("hstore qualified equality = %v, %v", eq, err)
	}
}

// TestUnboundOnSearchPath checks the unbound vocabularies render working
// SQL when the extension schema is on the search_path.
func TestUnboundOnSearchPath(t *testing.T) {
	d := openLive(t)
	d.exec(`SET LOCAL search_path TO ` + dataSchema + `, ` + quote(d.schemaOf(contrib.ExtHstore)) + `, ` +
		quote(d.schemaOf(contrib.ExtLtree)) + `, ` + quote(d.schemaOf(contrib.ExtTrgm)))
	for _, v := range []contrib.Vocabulary{contrib.Hstore(), contrib.Ltree(), contrib.Trgm("text")} {
		for _, op := range v.Ops {
			var a args
			col := "$1::text"
			if v.Codec != nil {
				val := any(map[string]any{"a": "1"})
				if v.Name == contrib.VocabLtree {
					val = "a.b"
				}
				cps, err := v.Codec.Params(val)
				if err != nil {
					t.Fatal(err)
				}
				col = v.Codec.SQL(a.add(cps...)...)
			} else {
				a.add("hello")
			}
			ps, err := op.Params(operand(v, op))
			if err != nil {
				t.Fatal(err)
			}
			var got bool
			q := "SELECT " + op.Render("("+col+")", a.add(ps...)...)
			if err := d.query(q, a.vals, &got); err != nil {
				t.Errorf("%s %s: %v\n%s", v.Name, op.Name, err, q)
			}
		}
	}
}

// TestTrgmOldVersionLive installs pg_trgm 1.3, which predates the strict
// word-similarity operators, and checks ForColumn leaves them out and every
// remaining operator and expression runs.
func TestTrgmOldVersionLive(t *testing.T) {
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var installed bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')`).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Skip("pg_trgm is already installed in this database")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	d := &liveDB{t: t, ctx: ctx, tx: tx}
	d.exec(`CREATE SCHEMA ` + quote(extSchema))
	d.exec(`CREATE SCHEMA ` + dataSchema)
	d.exec(`CREATE EXTENSION pg_trgm VERSION '1.3' SCHEMA ` + quote(extSchema))
	d.exec(`CREATE TABLE ` + dataSchema + `.words (w text)`)
	d.exec(`INSERT INTO ` + dataSchema + `.words VALUES ('hello world'), (NULL)`)
	d.exec(`SET LOCAL search_path TO ` + dataSchema)
	if d.cat, err = introspect.Introspect(ctx, tx, []string{dataSchema}); err != nil {
		t.Fatal(err)
	}
	if v := d.cat.Extension(contrib.ExtTrgm).Version; v != "1.3" {
		t.Fatalf("pg_trgm version %q", v)
	}
	if err := d.query("SELECT 'a'::text OPERATOR("+quote(extSchema)+".<<%) 'b'::text", nil, new(bool)); err == nil {
		t.Fatal("pg_trgm 1.3 unexpectedly has <<%")
	}
	vs := contrib.ForColumn(d.cat, d.cat.Table(dataSchema, "words").Column("w"))
	var v contrib.Vocabulary
	for _, x := range vs {
		if x.Name == contrib.VocabTrgm {
			v = x
		}
	}
	if v.Name != contrib.VocabTrgm {
		t.Fatalf("vocabularies %v", vs)
	}
	if got := strings.Join(v.OpNames(), ","); got != "similarTo,wordSimilarTo" {
		t.Errorf("ops = %s", got)
	}
	for _, op := range v.Ops {
		var n int64
		if err := d.query("SELECT count(*) FROM words AS t WHERE "+op.Render("t.w", "$1"), []any{"hello"}, &n); err != nil {
			t.Errorf("%s: %v", op.Name, err)
		}
	}
	for _, e := range v.Exprs {
		if strings.HasPrefix(e.Name, "strict") {
			t.Errorf("expr %s offered on pg_trgm 1.3", e.Name)
		}
		var r any
		if err := d.query("SELECT max("+e.Render("t.w", "$1")+") FROM words AS t", []any{"hello"}, &r); err != nil {
			t.Errorf("%s: %v", e.Name, err)
		}
	}
}

// TestOperandErrorsClassifyLive checks that malformed operands surface as
// user-input errors under pgexec.Classify.
func TestOperandErrorsClassifyLive(t *testing.T) {
	d := openLive(t)
	tests := []struct {
		typ, op string
		col     any
		operand any
	}{
		{"tsvector", "matches", "cat:1", "a b"},
		{"tsvector", "matches", "cat:1", "a &"},
		{"tsvector", "matchesWebsearch", "cat:1", map[string]any{"query": "cat", "config": "klingon"}},
		{"text", "matchesPlain", "cat", map[string]any{"query": "cat", "config": "klingon"}},
		{"ltree", "ancestorOf", "a.b", "a..b"},
		{"ltree", "matchesLquery", "a.b", "a.b.*{"},
		{"ltree", "matchesAnyLquery", "a.b", []any{"*{2,1}"}},
		{"ltree", "matchesLtxtquery", "a.b", "(a"},
		{"ltree", "matchesLtxtquery", "a.b", "a | | b"},
		{"_int4", "matchesQueryInt", "{1,2}", "1 &"},
		{"_int4", "matchesQueryInt", "{1,NULL}", "1"},
	}
	for _, tc := range tests {
		vs := contrib.ForType(d.cat, tc.typ, false)
		op, _ := contrib.FindOp(vs, tc.op)
		var a args
		var colSQL string
		if vs[0].Codec != nil {
			colSQL = vs[0].Codec.SQL(a.add(tc.col)...)
		} else {
			colSQL = textCast(tc.typ, a.add(tc.col)[0])
		}
		ps, err := op.Params(tc.operand)
		if err != nil {
			t.Fatal(err)
		}
		q := "SELECT " + op.Render("t.c", a.add(ps...)...) + " FROM (SELECT " + colSQL + " AS c) AS t"
		err = d.query(q, a.vals, new(bool))
		if c := pgexec.Classify(err, pgexec.Prod); err == nil || c.Class != pgexec.UserInput || c.Message == "invalid operation" {
			t.Errorf("%s %s(%v): %v -> %v %q", tc.typ, tc.op, tc.operand, err, c.Class, c.Message)
		}
	}
}

// TestExtensionTypeSchemaLive checks that a user type sharing an extension
// type's or a core type's name, in another schema, gets no vocabulary,
// while a domain over the real extension type in that schema does.
func TestExtensionTypeSchemaLive(t *testing.T) {
	d := openLive(t)
	hs := quote(d.schemaOf(contrib.ExtHstore))
	d.exec(`CREATE TYPE hstore AS (k text)`)
	d.exec(`CREATE TYPE ltree AS ENUM ('a')`)
	d.exec(`CREATE TYPE ` + dataSchema + `.tsvector AS (k text)`)
	d.exec(`CREATE DOMAIN real_attrs AS ` + hs + `.hstore`)
	d.exec(`CREATE TABLE fakes (h hstore, l ltree, v ` + dataSchema + `.tsvector, w tsvector, r real_attrs, x ` + hs + `.hstore)`)
	cat, err := introspect.Introspect(d.ctx, d.tx, []string{dataSchema})
	if err != nil {
		t.Fatal(err)
	}
	tbl := cat.Table(dataSchema, "fakes")
	for col, want := range map[string]string{"h": "", "l": "", "v": "", "w": contrib.VocabFullText, "r": contrib.VocabHstore, "x": contrib.VocabHstore} {
		vs := contrib.ForColumn(cat, tbl.Column(col))
		got := ""
		if len(vs) > 0 {
			got = vs[0].Name
		}
		if got != want || len(vs) > 1 {
			t.Errorf("%s: vocabularies %v, want %q", col, vs, want)
		}
	}
	if vs := contrib.ForType(cat, dataSchema+".hstore", false); len(vs) != 0 {
		t.Errorf("ForType(%s.hstore) = %v", dataSchema, vs)
	}
	if vs := contrib.ForType(cat, hs+".hstore", false); len(vs) != 1 || vs[0].Name != contrib.VocabHstore {
		t.Errorf("ForType(%s.hstore) = %v", hs, vs)
	}
}

// TestHeadlineLive checks ts_headline snippets against the query of a
// full-text match over a tsvector column with a separate source text, a text
// column and a jsonb document, with options bound as one text parameter.
func TestHeadlineLive(t *testing.T) {
	d := openLive(t)
	body := "PostgreSQL full text search finds documents. A headline shows where the query matched in the document."
	opts := func(in map[string]any) string {
		h, err := contrib.ParseHeadlineOptions(in)
		if err != nil {
			t.Fatal(err)
		}
		return h.String()
	}
	tests := []struct {
		vocab   contrib.Vocabulary
		op      string
		doc     string
		operand any
		options string
		want    string
	}{
		{contrib.FullText(), "matches", "$1::text", map[string]any{"query": "headline", "config": "english"}, "",
			"full text search finds documents. A <b>headline</b> shows where the query matched in the document."},
		{contrib.FullText(), "matchesWebsearch", "$1::text", "headline", opts(map[string]any{
			"maxWords": 4, "minWords": 2, "startSel": "[", "stopSel": "]",
		}), "[headline] shows"},
		{contrib.TextSearch("text"), "matchesPlain", "$1::text", map[string]any{"query": "documents query", "config": "english"},
			opts(map[string]any{"maxFragments": 2, "maxWords": 5, "minWords": 1, "fragmentDelimiter": " , ", "startSel": "<em class=hit>", "stopSel": "</em>"}),
			"search finds <em class=hit>documents</em>. A headline , <em class=hit>query</em> matched in the <em class=hit>document</em>"},
		{contrib.TextSearch("text"), "matches", "$1::text", "matched", opts(map[string]any{"highlightAll": true}),
			"PostgreSQL full text search finds documents. A headline shows where the query <b>matched</b> in the document."},
		{contrib.JSONSearch("jsonb"), "matchesPhrase", `jsonb_build_object('title', $1::text, 'n', 1)`,
			map[string]any{"query": "full text", "config": "english"}, opts(map[string]any{"startSel": "*", "stopSel": "*"}),
			`{"n": 1, "title": "*full* *text* search finds documents. A headline shows where the query matched in the document"}`},
	}
	for _, tc := range tests {
		op, ok := tc.vocab.Op(tc.op)
		if !ok {
			t.Fatalf("no op %s", tc.op)
		}
		a := args{vals: []any{body}}
		ps, err := op.Params(tc.operand)
		if err != nil {
			t.Fatal(err)
		}
		phs := a.add(ps...)
		optPh := ""
		if tc.options != "" {
			optPh = a.add(tc.options)[0]
		}
		q := "SELECT (" + op.HeadlineSQL(tc.doc, optPh, phs...) + ")::text"
		var got string
		if err := d.query(q, a.vals, &got); err != nil {
			t.Errorf("%s %s: %v\n%s", tc.vocab.Name, tc.op, err, q)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %s:\n got %s\nwant %s", tc.vocab.Name, tc.op, got, tc.want)
		}
	}
}
