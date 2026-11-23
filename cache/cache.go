// Package cache (de)serializes the introspected Catalog so a product can boot
// without touching pg_catalog: gzip-compressed JSON with a format version, a
// record of the program that wrote it, and a content hash for drift
// detection (`schema dump|check`).
package cache

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
)

const pdbcoreModule = "github.com/suprbdev/pdbcore"

// maxCacheBytes caps how much a cache file may decompress to. A real catalog
// is a few MB even for huge schemas; the limit only guards against a
// corrupted or malicious file acting as a gzip bomb.
var maxCacheBytes int64 = 256 << 20

// ErrCorrupt reports a file that is not a readable schema cache: not gzip,
// not JSON, too large once decompressed, without a catalog, or whose
// catalog does not match its recorded hash. Errors from [Load] and [Check]
// wrap it; test with errors.Is.
var ErrCorrupt = errors.New("cache: corrupt schema cache")

// FormatVersionError reports a schema cache written with a different
// [introspect.CatalogFormatVersion] than this build reads, typically by an
// older or newer release. Regenerate the cache with the running build.
type FormatVersionError struct {
	// Got is the cache's format version, Want the one this build reads.
	Got, Want int
	// Writer is the program that wrote the cache; zero for caches written
	// before format version 10, which did not record it.
	Writer Writer
}

func (e *FormatVersionError) Error() string {
	return fmt.Sprintf("cache: format version %d written by %s, this build reads %d: regenerate the schema cache",
		e.Got, e.Writer, e.Want)
}

// Writer identifies the program that wrote a schema cache.
type Writer struct {
	// Product is the program's name, e.g. "pdbq".
	Product string `json:"product,omitempty"`
	// Version is the program's version, e.g. "v0.12.0".
	Version string `json:"version,omitempty"`
	// Pdbcore is the pdbcore module version the program was built with.
	// [Save] fills it in from the build information when it is empty.
	Pdbcore string `json:"pdbcore,omitempty"`
}

// String renders w as "pdbq v0.12.0 (pdbcore v0.17.0)", leaving out what is
// unknown, or "an unknown program" when nothing is.
func (w Writer) String() string {
	s := strings.TrimSpace(w.Product + " " + w.Version)
	switch {
	case s == "" && w.Pdbcore == "":
		return "an unknown program"
	case s == "":
		return "pdbcore " + w.Pdbcore
	case w.Pdbcore != "":
		return s + " (pdbcore " + w.Pdbcore + ")"
	}
	return s
}

// pdbcoreVersion returns the pdbcore module version recorded in the running
// binary's build information, or "" when there is none.
func pdbcoreVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if bi.Main.Path == pdbcoreModule {
		return bi.Main.Version
	}
	for _, dep := range bi.Deps {
		if dep.Path == pdbcoreModule {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return ""
}

// envelope wraps the catalog with versioning, writer and hash metadata.
// Catalog stays raw until FormatVersion is known to match, so a cache in
// another format is reported as such rather than as undecodable.
type envelope struct {
	FormatVersion int             `json:"format_version"`
	Writer        Writer          `json:"writer"`
	Hash          string          `json:"hash"`
	Catalog       json.RawMessage `json:"catalog"`
}

// Save writes the catalog to path as gzipped JSON, recording w as its
// writer.
func Save(path string, cat *introspect.Catalog, w Writer) error {
	hash, err := cat.Hash()
	if err != nil {
		return fmt.Errorf("cache: hash: %w", err)
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		return fmt.Errorf("cache: encode: %w", err)
	}
	if w.Pdbcore == "" {
		w.Pdbcore = pdbcoreVersion()
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cache: create: %w", err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	if err := json.NewEncoder(zw).Encode(envelope{
		FormatVersion: introspect.CatalogFormatVersion,
		Writer:        w,
		Hash:          hash,
		Catalog:       raw,
	}); err != nil {
		return fmt.Errorf("cache: encode: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("cache: gzip: %w", err)
	}
	return f.Close()
}

// Load reads a catalog written by Save, verifying version and hash. A cache
// in another format fails with [*FormatVersionError], an unreadable one
// with an error wrapping [ErrCorrupt].
func Load(path string) (*introspect.Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cache: open: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%w: not gzip: %w", ErrCorrupt, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, maxCacheBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %w", ErrCorrupt, err)
	}
	if int64(len(raw)) > maxCacheBytes {
		return nil, fmt.Errorf("%w: decompressed size exceeds %d bytes", ErrCorrupt, maxCacheBytes)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrCorrupt, err)
	}
	if env.FormatVersion != introspect.CatalogFormatVersion {
		return nil, &FormatVersionError{Got: env.FormatVersion, Want: introspect.CatalogFormatVersion, Writer: env.Writer}
	}
	var cat *introspect.Catalog
	if err := json.Unmarshal(env.Catalog, &cat); err != nil {
		return nil, fmt.Errorf("%w: decode catalog: %w", ErrCorrupt, err)
	}
	if cat == nil {
		return nil, fmt.Errorf("%w: empty catalog", ErrCorrupt)
	}
	hash, err := cat.Hash()
	if err != nil {
		return nil, err
	}
	if hash != env.Hash {
		return nil, fmt.Errorf("%w: hash mismatch", ErrCorrupt)
	}
	return cat, nil
}

// Check compares a live catalog against a cache file; returns a non-empty
// description on drift. It fails as [Load] does when the cache cannot be
// read.
func Check(path string, live *introspect.Catalog) (string, error) {
	cached, err := Load(path)
	if err != nil {
		return "", err
	}
	ch, err := cached.Hash()
	if err != nil {
		return "", err
	}
	lh, err := live.Hash()
	if err != nil {
		return "", err
	}
	if ch != lh {
		lines := []string{fmt.Sprintf("schema drift detected: cache %s != live %s", ch[:12], lh[:12])}
		for _, d := range introspect.Diff(cached, live) {
			lines = append(lines, "  "+d)
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", nil
}
