package postgis_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/postgis"
)

// rasterTx opens a rolled-back transaction with postgis_raster installed
// and a table of three rasters: a 10x10 single-band 4326 raster of 5.5
// over (0..10, 0..10), a 4x4 two-band 3857 raster over (100..104,
// 100..104) and NULL. The watch package's tests create and drop DDL event
// triggers concurrently, so CREATE EXTENSION can fail with "cache lookup
// failed" when a trigger's function disappears mid-statement; it is retried.
func rasterTx(t *testing.T) (pgx.Tx, context.Context) {
	t.Helper()
	conn := testConn(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	for attempt := 1; ; attempt++ {
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis_raster`)
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			break
		}
		_ = sp.Rollback(ctx)
		if attempt == 5 || !strings.Contains(err.Error(), "cache lookup failed") {
			t.Fatalf("CREATE EXTENSION postgis_raster: %v", err)
		}
	}
	for _, q := range []string{
		`CREATE TEMP TABLE rasters (id int PRIMARY KEY, rast raster)`,
		`INSERT INTO rasters VALUES
			(1, ST_AddBand(ST_MakeEmptyRaster(10, 10, 0, 10, 1, -1, 0, 0, 4326), '32BF'::text, 5.5, NULL)),
			(2, ST_AddBand(ST_AddBand(ST_MakeEmptyRaster(4, 4, 100, 104, 1, -1, 0, 0, 3857), '8BUI'::text, 7, 0), '16BSI'::text, -3, NULL)),
			(3, NULL)`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return tx, ctx
}

func TestRasterAccessorsLive(t *testing.T) {
	tx, ctx := rasterTx(t)
	var cols []string
	for _, a := range postgis.RasterAccessors {
		cols = append(cols, "'"+a.Suffix+"', "+a.JSONSQL("r.rast"))
	}
	var out []map[string]any
	if err := tx.QueryRow(ctx, "SELECT json_agg(json_build_object("+strings.Join(cols, ", ")+") ORDER BY id) FROM rasters AS r").Scan(&out); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	want := `[{"Envelope":{"coordinates":[[[0,10],[10,10],[10,0],[0,0],[0,10]]],"type":"Polygon"},"Height":10,"NumBands":1,"PixelHeight":1,"PixelWidth":1,"Srid":4326,"Width":10},` +
		`{"Envelope":{"coordinates":[[[100,104],[104,104],[104,100],[100,100],[100,104]]],"crs":{"properties":{"name":"EPSG:3857"},"type":"name"},"type":"Polygon"},"Height":4,"NumBands":2,"PixelHeight":1,"PixelWidth":1,"Srid":3857,"Width":4},` +
		`{"Envelope":null,"Height":null,"NumBands":null,"PixelHeight":null,"PixelWidth":null,"Srid":null,"Width":null}]`
	if string(b) != want {
		t.Errorf("accessors = %s\nwant        %s", b, want)
	}
}

func TestRasterValueLive(t *testing.T) {
	tx, ctx := rasterTx(t)
	value := func(id int, point any, band int) *float64 {
		t.Helper()
		v, err := postgis.ParseFor(point, postgis.Geometry, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got *float64
		q := "SELECT " + postgis.RasterValueSQL("r.rast", v.SQL("$1"), "$2") + " FROM rasters AS r WHERE id = $3"
		if err := tx.QueryRow(ctx, q, v.Param, band, id).Scan(&got); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return got
	}
	f := func(x float64) *float64 { return &x }
	tests := []struct {
		id    int
		point any
		band  int
		want  *float64
	}{
		{1, map[string]any{"type": "Point", "coordinates": []any{2.5, 7.5}}, 1, f(5.5)},
		{1, "POINT(2.5 7.5)", 1, f(5.5)},
		{1, "SRID=4326;POINT(2.5 7.5)", 1, f(5.5)},
		{1, "POINT(20 7.5)", 1, nil},
		{1, "POINT(2.5 7.5)", 2, nil},
		{2, "SRID=3857;POINT(101.5 102.5)", 1, f(7)},
		{2, "POINT(101.5 102.5)", 2, f(-3)},
		{2, map[string]any{"type": "Point", "coordinates": []any{0.000912, 0.000921}}, 1, f(7)},
		{3, "POINT(1 1)", 1, nil},
	}
	for _, tc := range tests {
		got := value(tc.id, tc.point, tc.band)
		if (got == nil) != (tc.want == nil) || (got != nil && math.Abs(*got-*tc.want) > 1e-9) {
			t.Errorf("value(%d, %v, %d) = %v, want %v", tc.id, tc.point, tc.band, deref(got), deref(tc.want))
		}
	}
}

func deref(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func TestRasterSummaryStatsLive(t *testing.T) {
	tx, ctx := rasterTx(t)
	var out []map[string]any
	if err := tx.QueryRow(ctx, "SELECT json_agg("+postgis.RasterSummaryStatsJSONSQL("r.rast", "$1")+" ORDER BY id) FROM rasters AS r", 1).Scan(&out); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"count": 100.0, "sum": 550.0, "mean": 5.5, "stddev": 0.0, "min": 5.5, "max": 5.5},
		{"count": 16.0, "sum": 112.0, "mean": 7.0, "stddev": 0.0, "min": 7.0, "max": 7.0},
		nil,
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("stats = %v", out)
	}
	keys := map[string]bool{}
	for _, f := range postgis.RasterSummaryStatsFields {
		keys[f.Name] = true
	}
	for k := range out[0] {
		if !keys[k] {
			t.Errorf("undeclared member %q", k)
		}
	}
	var missing *string
	if err := tx.QueryRow(ctx, "SELECT "+postgis.RasterSummaryStatsSQL("r.rast", "$1")+"::text FROM rasters AS r WHERE id = 1", 4).Scan(&missing); err != nil || missing != nil {
		t.Errorf("missing band = %v, %v", missing, err)
	}
}

func TestRasterIntersectsLive(t *testing.T) {
	tx, ctx := rasterTx(t)
	ids := func(geom any) ([]int32, error) {
		v, err := postgis.ParseFor(geom, postgis.Geometry, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []int32
		err = tx.QueryRow(ctx, "SELECT coalesce(array_agg(id ORDER BY id), '{}') FROM rasters AS r WHERE "+postgis.RasterIntersectsSQL("r.rast", v.SQL("$1")), v.Param).Scan(&got)
		return got, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Exec(ctx, "DELETE FROM rasters WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		geom any
		want []int32
	}{
		{map[string]any{"type": "Point", "coordinates": []any{2.5, 7.5}}, []int32{1}},
		{"SRID=4326;LINESTRING(-5 -5, 1 1)", []int32{1}},
		{"SRID=4326;POINT(20 20)", []int32{}},
	} {
		if got, err := ids(tc.geom); err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("intersects %v = %v, %v; want %v", tc.geom, got, err, tc.want)
		}
	}
	if err := sp.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	sp, err = tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ids("SRID=3857;POINT(2.5 7.5)")
	_ = sp.Rollback(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgexec.Classify(err, pgexec.Prod).Class != pgexec.UserInput {
		t.Errorf("mixed SRID error = %v", err)
	}
}

// TestRasterIntersectsUsesIndex checks that a GiST expression index on
// ST_ConvexHull(rast) serves the intersects filter.
func TestRasterIntersectsUsesIndex(t *testing.T) {
	tx, ctx := rasterTx(t)
	for _, q := range []string{
		`CREATE INDEX rasters_hull ON rasters USING gist (ST_ConvexHull(rast))`,
		`SET LOCAL enable_seqscan = off`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.Query(ctx, "EXPLAIN SELECT id FROM rasters AS r WHERE "+postgis.RasterIntersectsSQL("r.rast", "'SRID=4326;POINT(1 1)'::geometry"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, l)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "rasters_hull") {
		t.Errorf("intersects not index-served:\n%s", strings.Join(plan, "\n"))
	}
}
