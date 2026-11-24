package postgis

import (
	"strings"
	"testing"
)

func TestIsRaster(t *testing.T) {
	for in, want := range map[string]bool{
		"raster": true, "public.raster": true, `"postgis"."raster"`: true,
		"_raster": false, "raster[]": false, "geometry": false, "rasters": false, "": false,
	} {
		if got := IsRaster(in); got != want {
			t.Errorf("IsRaster(%q) = %v", in, got)
		}
	}
}

func TestRasterAccessors(t *testing.T) {
	want := map[string]string{
		"Width":       "ST_Width(t.r)",
		"Height":      "ST_Height(t.r)",
		"NumBands":    "ST_NumBands(t.r)",
		"Srid":        "ST_SRID(t.r)",
		"PixelWidth":  "ST_PixelWidth(t.r)",
		"PixelHeight": "ST_PixelHeight(t.r)",
		"Envelope":    "ST_AsGeoJSON(ST_Envelope(t.r))::jsonb",
	}
	if len(RasterAccessors()) != len(want) {
		t.Fatalf("%d accessors", len(RasterAccessors()))
	}
	for _, a := range RasterAccessors() {
		if got := a.SQL("t.r", Output{}); got != want[a.Suffix] {
			t.Errorf("%s = %s", a.Suffix, got)
		}
		if a.Description == "" || a.Type == "" || a.GeoJSON != (a.Type == TypeName) {
			t.Errorf("%s: %+v", a.Suffix, a)
		}
		if b, ok := RasterAccessorBySuffix(a.Suffix); !ok || b.Suffix != a.Suffix {
			t.Errorf("lookup %s", a.Suffix)
		}
	}
	env, _ := RasterAccessorBySuffix("Envelope")
	if got := env.SQL("t.r", Output{JSON: true}); got != "ST_AsGeoJSON(ST_Envelope(t.r))::json" {
		t.Errorf("Envelope json = %s", got)
	}
	w, _ := RasterAccessorBySuffix("Width")
	if w.SQL("t.r", Output{JSON: true}) != w.SQL("t.r", Output{}) {
		t.Error("JSONSQL changes a scalar accessor")
	}
	if _, ok := RasterAccessorBySuffix("Pixels"); ok {
		t.Error("unknown accessor found")
	}
}

func TestRasterValueSQL(t *testing.T) {
	pt := Value{Flavour: Geometry}.SQL("$1")
	got := RasterValueSQL("t.r", pt, "$2")
	for _, part := range []string{
		"ST_Value(__rv.r, $2::int4, ",
		"ST_Transform(__rv.p, ST_SRID(__rv.r))",
		"ST_SetSRID(__rv.p, ST_SRID(__rv.r))",
		"FROM (SELECT t.r AS r, " + pt + " AS p) AS __rv)",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("RasterValueSQL lacks %q: %s", part, got)
		}
	}
	if strings.Count(got, "t.r") != 1 || strings.Count(got, pt) != 1 {
		t.Errorf("operands evaluated more than once: %s", got)
	}
}

func TestRasterSummaryStatsAndIntersects(t *testing.T) {
	if got := RasterSummaryStatsSQL("t.r", "$1"); got != "to_jsonb(ST_SummaryStats(t.r, $1::int4, true))" {
		t.Errorf("stats = %s", got)
	}
	if got := RasterSummaryStatsJSONSQL("t.r", "$1"); got != "to_json(ST_SummaryStats(t.r, $1::int4, true))" {
		t.Errorf("stats json = %s", got)
	}
	if len(RasterSummaryStatsFields) != 6 {
		t.Errorf("fields = %v", RasterSummaryStatsFields)
	}
	if got := RasterIntersectsSQL("t.r", "g"); got != "ST_Intersects(t.r, g)" {
		t.Errorf("intersects = %s", got)
	}
}
