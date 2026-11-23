package contrib_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
)

// TestUnaccentLive runs the unaccent operators with the extension's schema
// off the search_path, where the one-argument unaccent cannot find its
// dictionary.
func TestUnaccentLive(t *testing.T) {
	d := openLive(t)
	tests := []struct {
		typ, col, op, operand string
		want                  bool
	}{
		{"text", "Crème Brûlée", "unaccentEqualTo", "CREME BRULEE", true},
		{"text", "Crème Brûlée", "unaccentEqualTo", "creme", false},
		{"varchar", "Crème Brûlée", "unaccentIncludes", "brûle", true},
		{"text", "Crème Brûlée", "unaccentIncludes", "x", false},
		{"text", "50% Rabatt", "unaccentIncludes", "0%", true},
		{"text", "500 Rabatt", "unaccentIncludes", "0%", false},
		{"text", "a_b", "unaccentIncludes", "_", true},
		{"text", "ab", "unaccentIncludes", "_", false},
		{"citext", "Zoë", "unaccentStartsWith", "zo", true},
		{"text", "Zoë", "unaccentStartsWith", "oe", false},
		{"text", "Ångström", "unaccentLike", "angs%m", true},
		{"bpchar", "Ångström", "unaccentLike", "_ngstrom", true},
		{"text", "Ångström", "unaccentLike", "ngs%", false},
	}
	for _, tc := range tests {
		typ := tc.typ
		if typ == "citext" {
			typ = quote(d.schemaOf(contrib.ExtCitext)) + ".citext"
		}
		if got := d.eval(t, typ, tc.col, tc.op, tc.operand); got != tc.want {
			t.Errorf("%s %q %s %q = %v, want %v", tc.typ, tc.col, tc.op, tc.operand, got, tc.want)
		}
	}
}

// openVector is openLive with pgvector installed in the extension schema; it
// skips the test when the server does not have pgvector.
func openVector(t *testing.T) *liveDB {
	t.Helper()
	d := openLive(t)
	sp, err := d.tx.Begin(d.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Exec(d.ctx, `CREATE EXTENSION IF NOT EXISTS vector SCHEMA `+quote(extSchema)); err != nil {
		_ = sp.Rollback(d.ctx)
		t.Skipf("pgvector is not available: %v", err)
	}
	if err := sp.Commit(d.ctx); err != nil {
		t.Fatal(err)
	}
	ext := quote(d.schemaOf0(contrib.ExtVector))
	d.exec(`CREATE TABLE vecs (id int PRIMARY KEY, e ` + ext + `.vector(3), h ` + ext + `.halfvec(3), s ` + ext + `.sparsevec(3), b bit(3), vb varbit)`)
	cat, err := introspect.Introspect(d.ctx, d.tx, []string{dataSchema})
	if err != nil {
		t.Fatal(err)
	}
	d.cat = cat
	return d
}

// schemaOf0 is schemaOf against a fresh catalog, for an extension created
// after the last introspection.
func (d *liveDB) schemaOf0(ext string) string {
	var s string
	if err := d.tx.QueryRow(d.ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = $1`, ext).Scan(&s); err != nil {
		d.t.Fatal(err)
	}
	return s
}

func TestVectorLive(t *testing.T) {
	d := openVector(t)
	tbl := d.cat.Table(dataSchema, "vecs")
	vocab := func(col string) contrib.Vocabulary {
		vs := contrib.ForColumn(d.cat, tbl.Column(col))
		if len(vs) != 1 || vs[0].Name != contrib.VocabVector || vs[0].Schema != d.schemaOf(contrib.ExtVector) {
			t.Fatalf("%s: vocabularies %+v", col, vs)
		}
		return vs[0]
	}
	cols := []string{"e", "h", "s"}
	rows := [][]any{{1.0, 0.0, 0.0}, {0.0, 2.0, 0.0}, {1.0, 1.0, 1e-7}}
	for i, r := range rows {
		var a args
		exprs := []string{a.add(i + 1)[0]}
		for _, c := range cols {
			ps, err := vocab(c).Codec.Params(r)
			if err != nil {
				t.Fatal(err)
			}
			exprs = append(exprs, vocab(c).Codec.SQL(a.add(ps...)...))
		}
		if _, err := d.tx.Exec(d.ctx, `INSERT INTO vecs VALUES (`+strings.Join(exprs, ", ")+`)`, a.vals...); err != nil {
			t.Fatal(err)
		}
	}
	d.exec(`INSERT INTO vecs (id) VALUES (4)`)

	var out []any
	q := "SELECT jsonb_agg(jsonb_build_array(" + vocab("e").Codec.OutputSQL("t.e") + ", " + vocab("h").Codec.OutputSQL("t.h") + ", to_jsonb(" + vocab("s").Codec.OutputSQL("t.s") + ")) ORDER BY id) FROM vecs AS t"
	if err := d.query(q, nil, &out); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	b, _ := json.Marshal(out)
	if want := `[[[1,0,0],[1,0,0],"{1:1}/3"],[[0,2,0],[0,2,0],"{2:2}/3"],[[1,1,1e-7],[1,1,1.1920929e-7],"{1:1,2:1,3:1e-07}/3"],[null,null,null]]`; string(b) != want {
		t.Errorf("output = %s\nwant      %s", b, want)
	}
	var js string
	q = "SELECT json_agg(" + vocab("e").Codec.OutputJSONSQL("t.e") + " ORDER BY id)::text FROM vecs AS t"
	if err := d.query(q, nil, &js); err != nil || !strings.HasPrefix(js, "[[1,0,0]") {
		t.Errorf("json output = %s, %v", js, err)
	}

	order := func(col, expr string, ref []any) []int {
		t.Helper()
		e, ok := vocab(col).Expr(expr)
		if !ok {
			t.Fatalf("%s: expr %s missing", col, expr)
		}
		var a args
		ps, err := e.Params(ref)
		if err != nil {
			t.Fatal(err)
		}
		q := "SELECT array_agg(id ORDER BY " + e.Render("t."+col, a.add(ps...)...) + ", id) FROM vecs AS t WHERE t." + col + " IS NOT NULL"
		var ids []int32
		if err := d.query(q, a.vals, &ids); err != nil {
			t.Fatalf("%s %s: %v\n%s", col, expr, err, q)
		}
		out := make([]int, len(ids))
		for i, id := range ids {
			out[i] = int(id)
		}
		return out
	}
	ref := []any{0.0, 3.0, 0.0}
	for _, c := range cols {
		for _, tc := range []struct {
			expr string
			want []int
		}{
			{"l2Distance", []int{2, 3, 1}},
			{"negativeInnerProduct", []int{2, 3, 1}},
			{"cosineDistance", []int{2, 3, 1}},
			{"l1Distance", []int{2, 3, 1}},
		} {
			if got := order(c, tc.expr, ref); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s by %s = %v, want %v", c, tc.expr, got, tc.want)
			}
		}
	}
	for _, c := range cols {
		for _, acc := range vocab(c).Accessors {
			var got any
			if err := d.query("SELECT "+acc.SQL("t."+c)+" FROM vecs AS t WHERE id = 2", nil, &got); err != nil {
				t.Fatalf("%s %s: %v", c, acc.Name, err)
			}
			want := map[string]any{"dims": int32(3), "norm": 2.0}[acc.Name]
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %s = %#v, want %#v", c, acc.Name, got, want)
			}
		}
	}
}

// TestVectorIndexServesOrdering checks the HNSW hint: ORDER BY the rendered
// cosine distance LIMIT n is planned as an index scan.
func TestVectorIndexServesOrdering(t *testing.T) {
	d := openVector(t)
	ext := quote(d.schemaOf(contrib.ExtVector))
	d.exec(`CREATE INDEX vecs_e_idx ON vecs USING hnsw (e ` + ext + `.vector_cosine_ops)`)
	d.exec(`INSERT INTO vecs (id, e) SELECT g, ARRAY[g, g % 7, 1]::real[]::` + ext + `.vector FROM generate_series(1, 2000) AS g`)
	d.exec(`ANALYZE vecs`)
	d.exec(`SET LOCAL enable_seqscan = off`)
	vs := contrib.ForColumn(d.cat, d.cat.Table(dataSchema, "vecs").Column("e"))
	e, _ := vs[0].Expr("cosineDistance")
	if e.Index[0].OpClass != "vector_cosine_ops" {
		t.Fatalf("hint = %+v", e.Index[0])
	}
	ps, _ := e.Params([]any{1, 2, 3})
	rows, err := d.tx.Query(d.ctx, "EXPLAIN SELECT id FROM vecs AS t ORDER BY "+e.Render("t.e", "$1")+" LIMIT 5", ps...)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	if !strings.Contains(plan.String(), "vecs_e_idx") {
		t.Errorf("plan does not use the HNSW index:\n%s", plan.String())
	}
}

func TestVectorOperandErrorsLive(t *testing.T) {
	d := openVector(t)
	d.exec(`INSERT INTO vecs (id, e) VALUES (1, '[1,2,3]')`)
	vs := contrib.ForColumn(d.cat, d.cat.Table(dataSchema, "vecs").Column("e"))
	e, _ := vs[0].Expr("l2Distance")
	ps, err := e.Params([]any{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	err = d.query("SELECT "+e.Render("t.e", "$1")+" FROM vecs AS t LIMIT 1", ps, new(any))
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Errorf("mismatched dimensions: %v", err)
	}
}

// TestVectorThresholdLive runs the <distance>LessThan filters of every
// vector type against the same rows as the ordering.
func TestVectorThresholdLive(t *testing.T) {
	d := openVector(t)
	d.exec(`INSERT INTO vecs (id, e, h, s) VALUES (1, '[1,0,0]', '[1,0,0]', '{1:1}/3'), (2, '[0,2,0]', '[0,2,0]', '{2:2}/3'), (3, '[1,1,0]', '[1,1,0]', '{1:1,2:1}/3'), (4, NULL, NULL, NULL)`)
	tbl := d.cat.Table(dataSchema, "vecs")
	ref := []any{0.0, 3.0, 0.0}
	for _, col := range []string{"e", "h", "s"} {
		v := contrib.ForColumn(d.cat, tbl.Column(col))[0]
		for _, tc := range []struct {
			op       string
			distance float64
			want     []int32
		}{
			{"l2DistanceLessThan", 2.5, []int32{2, 3}},
			{"l2DistanceLessThan", 1.5, []int32{2}},
			{"negativeInnerProductLessThan", -2, []int32{2, 3}},
			{"cosineDistanceLessThan", 0.5, []int32{2, 3}},
			{"l1DistanceLessThan", 2, []int32{2}},
		} {
			op, ok := v.Op(tc.op)
			if !ok {
				t.Fatalf("%s: %s missing", col, tc.op)
			}
			ps, err := op.Params(map[string]any{"vector": ref, "distance": tc.distance})
			if err != nil {
				t.Fatal(err)
			}
			var a args
			q := "SELECT coalesce(array_agg(id ORDER BY id), '{}') FROM vecs AS t WHERE " + op.Render("t."+col, a.add(ps...)...)
			var ids []int32
			if err := d.query(q, a.vals, &ids); err != nil {
				t.Fatalf("%s %s: %v\n%s", col, tc.op, err, q)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("%s %s < %v = %v, want %v", col, tc.op, tc.distance, ids, tc.want)
			}
		}
	}
}

// TestBitLive orders, filters and scores bit and varbit columns by the
// Hamming and Jaccard distances, with the reference given as a list.
func TestBitLive(t *testing.T) {
	d := openVector(t)
	d.exec(`INSERT INTO vecs (id, b, vb) VALUES (1, '110', '110'), (2, '011', '011'), (3, '000', '000'), (4, NULL, NULL)`)
	tbl := d.cat.Table(dataSchema, "vecs")
	ref := []any{1, 1, 1}
	for _, col := range []string{"b", "vb"} {
		vs := contrib.ForColumn(d.cat, tbl.Column(col))
		if len(vs) != 1 || vs[0].Name != contrib.VocabBit {
			t.Fatalf("%s: vocabularies %+v", col, vs)
		}
		v := vs[0]
		for _, tc := range []struct {
			expr   string
			scores []float64
		}{
			{"hammingDistance", []float64{1, 1, 3}},
			{"jaccardDistance", []float64{1.0 / 3, 1.0 / 3, 1}},
		} {
			e, _ := v.Expr(tc.expr)
			ps, err := e.Params(ref)
			if err != nil {
				t.Fatal(err)
			}
			var a args
			q := "SELECT array_agg((" + e.Render("t."+col, a.add(ps...)...) + ")::float8 ORDER BY id) FROM vecs AS t WHERE t." + col + " IS NOT NULL"
			var got []float64
			if err := d.query(q, a.vals, &got); err != nil {
				t.Fatalf("%s %s: %v\n%s", col, tc.expr, err, q)
			}
			for i := range got {
				if math.Abs(got[i]-tc.scores[i]) > 1e-9 {
					t.Errorf("%s %s = %v, want %v", col, tc.expr, got, tc.scores)
					break
				}
			}
		}
		op, _ := v.Op("hammingDistanceLessThan")
		ps, _ := op.Params(map[string]any{"vector": "111", "distance": 2})
		var a args
		var ids []int32
		if err := d.query("SELECT array_agg(id ORDER BY id) FROM vecs AS t WHERE "+op.Render("t."+col, a.add(ps...)...), a.vals, &ids); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids, []int32{1, 2}) {
			t.Errorf("%s hamming < 2 = %v", col, ids)
		}
	}
	e, _ := contrib.ForColumn(d.cat, tbl.Column("b"))[0].Expr("hammingDistance")
	ps, _ := e.Params("11")
	if err := d.query("SELECT "+e.Render("t.b", "$1")+" FROM vecs AS t WHERE id = 1", ps, new(any)); err == nil {
		t.Error("length mismatch accepted")
	}
}
