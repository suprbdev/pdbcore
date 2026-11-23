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

// openCube is openLive with cube and earthdistance installed in the
// extension schema and a table of cube, earth and point columns. Before
// earthdistance 1.2 its functions call cube's unqualified, so the extension
// schema joins the search_path there.
func openCube(t *testing.T) *liveDB {
	t.Helper()
	d := openLive(t)
	d.exec(`CREATE EXTENSION IF NOT EXISTS cube SCHEMA ` + quote(extSchema))
	d.exec(`CREATE EXTENSION IF NOT EXISTS earthdistance SCHEMA ` + quote(extSchema))
	var version string
	if err := d.tx.QueryRow(d.ctx, `SELECT extversion FROM pg_extension WHERE extname = 'earthdistance'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < "1.2" {
		d.exec(`SET LOCAL search_path TO ` + dataSchema + `, ` + quote(d.schemaOf0(contrib.ExtEarthdistance)) + `, ` + quote(d.schemaOf0(contrib.ExtCube)))
	}
	cube, ed := quote(d.schemaOf0(contrib.ExtCube)), quote(d.schemaOf0(contrib.ExtEarthdistance))
	d.exec(`CREATE DOMAIN place AS ` + ed + `.earth`)
	d.exec(`CREATE TABLE cubes (id int PRIMARY KEY, c ` + cube + `.cube, loc ` + ed + `.earth, home place, p point)`)
	cat, err := introspect.Introspect(d.ctx, d.tx, []string{dataSchema})
	if err != nil {
		t.Fatal(err)
	}
	d.cat = cat
	return d
}

func (d *liveDB) vocabOf(t *testing.T, table, col, name string) contrib.Vocabulary {
	t.Helper()
	vs := contrib.ForColumn(d.cat, d.cat.Table(dataSchema, table).Column(col))
	if len(vs) != 1 || vs[0].Name != name {
		t.Fatalf("%s: vocabularies %+v", col, vs)
	}
	return vs[0]
}

// ids runs SELECT array_agg(id ORDER BY order) WHERE where over cubes.
func (d *liveDB) ids(t *testing.T, where, order string, vals []any) []int32 {
	t.Helper()
	if order == "" {
		order = "id"
	}
	q := "SELECT coalesce(array_agg(id ORDER BY " + order + "), '{}') FROM cubes AS t WHERE " + where
	var ids []int32
	if err := d.query(q, vals, &ids); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return ids
}

func TestCubeLive(t *testing.T) {
	d := openCube(t)
	v := d.vocabOf(t, "cubes", "c", contrib.VocabCube)
	if v.Schema != d.schemaOf(contrib.ExtCube) {
		t.Fatalf("cube bound to %q", v.Schema)
	}
	values := []any{
		[]any{0.0, 0.0}, []any{3.0, 4.0}, []any{[]any{1.0, 1.0}, []any{2.0, 5.0}}, []any{-1.5, 1e-7}, "(10, 10, 10)",
	}
	for i, val := range values {
		ps, err := v.Codec.Params(val)
		if err != nil {
			t.Fatal(err)
		}
		var a args
		id := a.add(i + 1)[0]
		if _, err := d.tx.Exec(d.ctx, `INSERT INTO cubes (id, c) VALUES (`+id+`, `+v.Codec.SQL(a.add(ps...)...)+`)`, a.vals...); err != nil {
			t.Fatal(err)
		}
	}
	d.exec(`INSERT INTO cubes (id) VALUES (6)`)
	var out []any
	if err := d.query("SELECT jsonb_agg("+v.Codec.OutputSQL("t.c")+" ORDER BY id) FROM cubes AS t", nil, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if want := `[[0,0],[3,4],[[1,1],[2,5]],[-1.5,1e-7],[10,10,10],null]`; string(b) != want {
		t.Errorf("output = %s, want %s", b, want)
	}
	var js string
	if err := d.query("SELECT json_agg("+v.Codec.OutputJSONSQL("t.c")+" ORDER BY id)::text FROM cubes AS t", nil, &js); err != nil || !strings.HasPrefix(js, "[[0, 0], [3, 4], [[1, 1],[2, 5]]") {
		t.Errorf("json output = %s, %v", js, err)
	}

	filter := func(op string, operand any) []int32 {
		t.Helper()
		o, ok := v.Op(op)
		if !ok {
			t.Fatalf("%s missing", op)
		}
		ps, err := o.Params(operand)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		var a args
		return d.ids(t, o.Render("t.c", a.add(ps...)...), "", a.vals)
	}
	for _, tc := range []struct {
		op      string
		operand any
		want    []int32
	}{
		{"contains", []any{1.5, 3.0}, []int32{3}},
		{"containedBy", []any{[]any{-2.0, -2.0}, []any{3.0, 4.0}}, []int32{1, 2, 4}},
		{"overlaps", []any{[]any{2.0, 0.0}, []any{5.0, 1.0}}, []int32{3}},
		{"l2DistanceLessThan", map[string]any{"cube": []any{3.0, 0.0}, "distance": 3.5}, []int32{1, 3}},
		{"l2DistanceLessThan", map[string]any{"cube": []any{3.0, 0.0}, "distance": 4.6}, []int32{1, 2, 3, 4}},
		{"l1DistanceLessThan", map[string]any{"cube": []any{3.0, 0.0}, "distance": 3.5}, []int32{1, 3}},
		{"chebyshevDistanceLessThan", map[string]any{"cube": []any{3.0, 0.0}, "distance": 3.5}, []int32{1, 3}},
		{"chebyshevDistanceLessThan", map[string]any{"cube": []any{3.0, 0.0}, "distance": 4.1}, []int32{1, 2, 3}},
		{"l2DistanceLessThan", map[string]any{"cube": "(10, 10, 10)", "distance": 0.5}, []int32{5}},
	} {
		if got := filter(tc.op, tc.operand); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %v = %v, want %v", tc.op, tc.operand, got, tc.want)
		}
	}
	for _, tc := range []struct {
		expr string
		want []int32
		id2  float64
	}{
		{"l2Distance", []int32{2, 3, 1, 4, 5}, math.Sqrt2},
		{"l1Distance", []int32{2, 3, 1, 4, 5}, 2},
		{"chebyshevDistance", []int32{2, 3, 1, 4, 5}, 1},
	} {
		e, _ := v.Expr(tc.expr)
		var a args
		ps, err := e.Params([]any{4.0, 5.0})
		if err != nil {
			t.Fatal(err)
		}
		phs := a.add(ps...)
		if got := d.ids(t, "t.c IS NOT NULL", e.OrderSQL("t.c", phs...)+", id", a.vals); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("order by %s = %v, want %v", tc.expr, got, tc.want)
		}
		var dist float64
		if err := d.query("SELECT "+e.Render("t.c", phs...)+" FROM cubes AS t WHERE id = 2", a.vals, &dist); err != nil || math.Abs(dist-tc.id2) > 1e-9 {
			t.Errorf("%s(id 2) = %v, %v", tc.expr, dist, err)
		}
	}
	o, _ := v.Op("contains")
	var a args
	if err := d.query("SELECT count(*) FROM cubes AS t WHERE "+o.Render("t.c", a.add("(1,")...), a.vals, new(int64)); err == nil {
		t.Error("malformed cube literal accepted")
	}
}

// TestCubeThresholdUsesIndex checks that the threshold filter's overlap
// prefilter is served by a GiST index.
func TestCubeThresholdUsesIndex(t *testing.T) {
	d := openCube(t)
	d.exec(`INSERT INTO cubes (id, c) SELECT g, ` + quote(d.schemaOf(contrib.ExtCube)) + `.cube(ARRAY[g % 97, g % 89]::float8[]) FROM generate_series(1, 3000) g`)
	d.exec(`CREATE INDEX cubes_c ON cubes USING gist (c)`)
	d.exec(`ANALYZE cubes`)
	d.exec(`SET LOCAL enable_seqscan = off`)
	v := d.vocabOf(t, "cubes", "c", contrib.VocabCube)
	o, _ := v.Op("l2DistanceLessThan")
	plan := d.explain(t, "SELECT id FROM cubes AS t WHERE "+o.Render("t.c", "'(5, 5)'", "1.5"))
	if !strings.Contains(plan, "cubes_c") {
		t.Errorf("threshold not index-served:\n%s", plan)
	}
	e, _ := v.Expr("l1Distance")
	plan = d.explain(t, "SELECT id FROM cubes AS t ORDER BY "+e.OrderSQL("t.c", "'(5, 5)'")+" LIMIT 3")
	if !strings.Contains(plan, "Index Scan using cubes_c") {
		t.Errorf("ordering not a KNN scan:\n%s", plan)
	}
}

func (d *liveDB) explain(t *testing.T, q string) string {
	t.Helper()
	rows, err := d.tx.Query(d.ctx, "EXPLAIN "+q)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return strings.Join(lines, "\n")
}

var places = []struct {
	name     string
	lat, lon float64
}{
	{"london", 51.5074, -0.1278},
	{"paris", 48.8566, 2.3522},
	{"berlin", 52.52, 13.405},
	{"new york", 40.7128, -74.006},
}

func TestEarthLive(t *testing.T) {
	d := openCube(t)
	for _, col := range []string{"loc", "home"} {
		v := d.vocabOf(t, "cubes", col, contrib.VocabEarth)
		if v.Schema != d.schemaOf(contrib.ExtEarthdistance) {
			t.Fatalf("%s bound to %q", col, v.Schema)
		}
	}
	v := d.vocabOf(t, "cubes", "loc", contrib.VocabEarth)
	for i, p := range places {
		ps, err := v.Codec.Params(map[string]any{"lat": p.lat, "lon": p.lon})
		if err != nil {
			t.Fatal(err)
		}
		var a args
		id := a.add(i + 1)[0]
		loc := v.Codec.SQL(a.add(ps...)...)
		if _, err := d.tx.Exec(d.ctx, `INSERT INTO cubes (id, loc, home, p) VALUES (`+id+`, `+loc+`, `+loc+`, point(`+a.add(p.lon)[0]+`::float8, `+a.add(p.lat)[0]+`::float8))`, a.vals...); err != nil {
			t.Fatal(err)
		}
	}
	d.exec(`INSERT INTO cubes (id) VALUES (5)`)
	var out []map[string]float64
	if err := d.query("SELECT jsonb_agg("+v.Codec.OutputSQL("t.home")+" ORDER BY id) FROM cubes AS t WHERE id IN (1, 5)", nil, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || math.Abs(out[0]["lat"]-51.5074) > 1e-9 || math.Abs(out[0]["lon"]+0.1278) > 1e-9 || out[1] != nil {
		t.Errorf("output = %v", out)
	}
	var js string
	if err := d.query("SELECT "+v.Codec.OutputJSONSQL("t.loc")+"::text FROM cubes AS t WHERE id = 2", nil, &js); err != nil || !strings.HasPrefix(js, `{"lat" : 48.85`) {
		t.Errorf("json output = %s, %v", js, err)
	}

	near := map[string]any{"lat": 51.5074, "lon": -0.1278}
	e, _ := v.Expr("earthDistance")
	ps, _ := e.Params(near)
	var a args
	phs := a.add(ps...)
	for _, col := range []string{"loc", "home"} {
		if got := d.ids(t, "t."+col+" IS NOT NULL", e.OrderSQL("t."+col, phs...), a.vals); !reflect.DeepEqual(got, []int32{1, 2, 3, 4}) {
			t.Errorf("%s nearest London = %v", col, got)
		}
	}
	var dist float64
	if err := d.query("SELECT "+e.Render("t.loc", phs...)+" FROM cubes AS t WHERE id = 2", a.vals, &dist); err != nil || dist < 340e3 || dist > 346e3 {
		t.Errorf("London-Paris = %v m, %v", dist, err)
	}
	o, _ := v.Op("earthDistanceLessThan")
	for _, tc := range []struct {
		distance float64
		want     []int32
	}{{1, []int32{1}}, {400e3, []int32{1, 2}}, {1000e3, []int32{1, 2, 3}}, {6000e3, []int32{1, 2, 3, 4}}} {
		ps, err := o.Params(map[string]any{"lat": near["lat"], "lon": near["lon"], "distance": tc.distance})
		if err != nil {
			t.Fatal(err)
		}
		var a args
		if got := d.ids(t, o.Render("t.home", a.add(ps...)...), "", a.vals); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("within %v m = %v, want %v", tc.distance, got, tc.want)
		}
	}

	pv := d.vocabOf(t, "cubes", "p", contrib.VocabEarthPoint)
	me, _ := pv.Expr("earthDistanceMiles")
	ps, _ = me.Params(near)
	var pa args
	pphs := pa.add(ps...)
	if got := d.ids(t, "t.p IS NOT NULL", me.OrderSQL("t.p", pphs...), pa.vals); !reflect.DeepEqual(got, []int32{1, 2, 3, 4}) {
		t.Errorf("point nearest London = %v", got)
	}
	if err := d.query("SELECT "+me.Render("t.p", pphs...)+" FROM cubes AS t WHERE id = 2", pa.vals, &dist); err != nil || dist < 210 || dist > 216 {
		t.Errorf("London-Paris = %v miles, %v", dist, err)
	}
	po, _ := pv.Op("earthDistanceMilesLessThan")
	ps, _ = po.Params(map[string]any{"lat": near["lat"], "lon": near["lon"], "distance": 600})
	var fa args
	if got := d.ids(t, po.Render("t.p", fa.add(ps...)...), "", fa.vals); !reflect.DeepEqual(got, []int32{1, 2, 3}) {
		t.Errorf("points within 600 miles = %v", got)
	}
}

// TestEarthUsesIndex checks the index hints: a GiST index on an earth
// column serves the radius filter and the nearest-first ordering.
func TestEarthUsesIndex(t *testing.T) {
	d := openCube(t)
	ed := quote(d.schemaOf(contrib.ExtEarthdistance))
	d.exec(`INSERT INTO cubes (id, loc) SELECT g, ` + ed + `.ll_to_earth((g % 170) - 85, (g % 359) - 179) FROM generate_series(1, 3000) g`)
	d.exec(`CREATE INDEX cubes_loc ON cubes USING gist (loc)`)
	d.exec(`ANALYZE cubes`)
	d.exec(`SET LOCAL enable_seqscan = off`)
	v := d.vocabOf(t, "cubes", "loc", contrib.VocabEarth)
	o, _ := v.Op("earthDistanceLessThan")
	if plan := d.explain(t, "SELECT id FROM cubes AS t WHERE "+o.Render("t.loc", "51.5", "0", "100000")); !strings.Contains(plan, "cubes_loc") {
		t.Errorf("radius filter not index-served:\n%s", plan)
	}
	e, _ := v.Expr("earthDistance")
	if plan := d.explain(t, "SELECT id FROM cubes AS t ORDER BY "+e.OrderSQL("t.loc", "51.5", "0")+" LIMIT 3"); !strings.Contains(plan, "Index Scan using cubes_loc") {
		t.Errorf("ordering not a KNN scan:\n%s", plan)
	}
}
