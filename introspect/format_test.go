package introspect

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var updateFormat = flag.Bool("update", false, "rewrite testdata/catalog_format.golden (only after bumping CatalogFormatVersion)")

const formatGolden = "testdata/catalog_format.golden"

// catalogFormat describes the Catalog wire format: every JSON field of every
// type reachable from Catalog, with its Go type, and the values of the
// string enums the format uses.
func catalogFormat() string {
	var b strings.Builder
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			fmt.Fprintf(&b, "%s %s %s\n", t.Name(), f.Tag.Get("json"), f.Type)
		}
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeFor[Catalog]())
	enums := []struct {
		name   string
		values []any
	}{
		{"RelKind", []any{RelTable, RelView, RelMaterializedView}},
		{"GeneratedKind", []any{GeneratedIdentityAlways, GeneratedIdentityByDefault, GeneratedStored, GeneratedVirtual}},
		{"Volatility", []any{VolatilityImmutable, VolatilityStable, VolatilityVolatile}},
		{"ArgMode", []any{ArgModeIn, ArgModeOut, ArgModeInOut, ArgModeVariadic, ArgModeTable}},
		{"SpatialSubtype", []any{SpatialGeometry, SpatialPoint, SpatialLineString, SpatialPolygon,
			SpatialMultiPoint, SpatialMultiLineString, SpatialMultiPolygon, SpatialGeometryCollection}},
	}
	for _, e := range enums {
		fmt.Fprintf(&b, "enum %s %v\n", e.name, e.values)
	}
	return b.String()
}

// TestCatalogFormat fails when the Catalog wire format changes while
// CatalogFormatVersion stays the same, so a schema cache written by an older
// build is refused rather than misread. After bumping the version, rerun
// with -update to record the new format.
func TestCatalogFormat(t *testing.T) {
	got := catalogFormat()
	header := fmt.Sprintf("format_version %d\n", CatalogFormatVersion)
	raw, err := os.ReadFile(formatGolden)
	if err != nil && !*updateFormat {
		t.Fatal(err)
	}
	goldenHeader, goldenBody, _ := bytes.Cut(raw, []byte("\n"))
	var goldenVersion int
	_, _ = fmt.Sscanf(string(goldenHeader), "format_version %d", &goldenVersion)
	changed := string(goldenBody) != got
	if changed && goldenVersion == CatalogFormatVersion {
		t.Fatalf("the Catalog JSON format changed but CatalogFormatVersion is still %d: bump it, then run go test ./introspect -run TestCatalogFormat -update\n%s",
			CatalogFormatVersion, lineDiff(string(goldenBody), got))
	}
	if !changed && goldenVersion == CatalogFormatVersion {
		return
	}
	if !*updateFormat {
		t.Fatalf("%s records format_version %d, CatalogFormatVersion is %d: run go test ./introspect -run TestCatalogFormat -update",
			formatGolden, goldenVersion, CatalogFormatVersion)
	}
	if err := os.WriteFile(formatGolden, []byte(header+got), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for _, l := range w {
		if !slices.Contains(g, l) {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range g {
		if !slices.Contains(w, l) {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	return b.String()
}
