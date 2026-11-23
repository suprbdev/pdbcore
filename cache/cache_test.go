package cache

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
)

func TestRoundTrip(t *testing.T) {
	cat := testutil.FixtureCatalog()
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := Save(path, cat, Writer{Product: "pdbtest", Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := cat.Hash()
	h2, _ := loaded.Hash()
	if h1 != h2 {
		t.Fatalf("hash mismatch after round trip: %s != %s", h1, h2)
	}
}

func TestCheckDetectsDrift(t *testing.T) {
	cat := testutil.FixtureCatalog()
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := Save(path, cat, Writer{Product: "pdbtest", Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if drift, err := Check(path, testutil.FixtureCatalog()); err != nil || drift != "" {
		t.Fatalf("no-drift check failed: drift=%q err=%v", drift, err)
	}
	changed := testutil.FixtureCatalog()
	changed.Tables[0].Columns = changed.Tables[0].Columns[1:]
	drift, err := Check(path, changed)
	if err != nil {
		t.Fatal(err)
	}
	if drift == "" {
		t.Fatal("expected drift to be detected")
	}
	if !strings.Contains(drift, "column") {
		t.Fatalf("expected drift detail naming the dropped column, got %q", drift)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bogus")
	if err := os.WriteFile(path, []byte("not a cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt for non-gzip input", err)
	}
}

func TestLoadRejectsDecompressionBomb(t *testing.T) {
	orig := maxCacheBytes
	maxCacheBytes = 1 << 20 // shrink so the test stays fast
	defer func() { maxCacheBytes = orig }()
	path := filepath.Join(t.TempDir(), "bomb.cache")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	// A stream of zeros compresses ~1000:1, so an over-limit payload is a
	// tiny file on disk — exactly the gzip-bomb shape Load must refuse.
	if _, err := io.CopyN(zw, zeroReader{}, maxCacheBytes+2); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want decompressed-size error", err)
	}
}

func writeEnvelope(t *testing.T, path string, env any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if err := json.NewEncoder(zw).Encode(env); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readEnvelope(t *testing.T, path string) map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.NewDecoder(zr).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestSaveRecordsWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := Save(path, testutil.FixtureCatalog(), Writer{Product: "pdbtest", Version: "v1.2.3", Pdbcore: "v0.99.0"}); err != nil {
		t.Fatal(err)
	}
	w, _ := readEnvelope(t, path)["writer"].(map[string]any)
	if w["product"] != "pdbtest" || w["version"] != "v1.2.3" || w["pdbcore"] != "v0.99.0" {
		t.Fatalf("writer = %v", w)
	}
}

func TestLoadRejectsOtherFormatVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.cache")
	writeEnvelope(t, path, map[string]any{
		"format_version": 9,
		"hash":           "x",
		"catalog":        map[string]any{"tables": "not even the right shape"},
	})
	_, err := Load(path)
	var fv *FormatVersionError
	if !errors.As(err, &fv) || fv.Got != 9 || fv.Want != introspect.CatalogFormatVersion || errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want *FormatVersionError 9", err)
	}
	if !strings.Contains(err.Error(), "unknown program") {
		t.Errorf("message %q does not mention the unknown writer", err)
	}

	writeEnvelope(t, path, map[string]any{
		"format_version": introspect.CatalogFormatVersion + 1,
		"writer":         Writer{Product: "pdbq", Version: "v9.0.0", Pdbcore: "v9.1.0"},
	})
	if _, err := Load(path); !errors.As(err, &fv) || !strings.Contains(err.Error(), "pdbq v9.0.0 (pdbcore v9.1.0)") {
		t.Fatalf("err = %v, want *FormatVersionError naming the writer", err)
	}
}

func TestLoadRejectsTamperedCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := Save(path, testutil.FixtureCatalog(), Writer{}); err != nil {
		t.Fatal(err)
	}
	env := readEnvelope(t, path)
	env["catalog"].(map[string]any)["schemas"] = []string{"other"}
	writeEnvelope(t, path, env)
	if _, err := Load(path); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("err = %v, want ErrCorrupt hash mismatch", err)
	}
	env["catalog"] = nil
	writeEnvelope(t, path, env)
	if _, err := Load(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt for an empty catalog", err)
	}
}

func TestWriterString(t *testing.T) {
	for w, want := range map[Writer]string{
		{}:                "an unknown program",
		{Pdbcore: "v1"}:   "pdbcore v1",
		{Product: "pdbr"}: "pdbr",
		{Product: "pdbr", Version: "v2", Pdbcore: "v1"}: "pdbr v2 (pdbcore v1)",
	} {
		if got := w.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", w, got, want)
		}
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
