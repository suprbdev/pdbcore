package confx

import (
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	flag "github.com/spf13/pflag"
)

// Load layers configuration into dst (which already holds the defaults):
// optional YAML file, then envPrefix-prefixed environment variables, then
// flags. Pass only the flags the user actually set (see ChangedFlags) so
// unset flags cannot shadow YAML/env values; flag names are koanf paths.
// Validation is the caller's job.
func Load(envPrefix, path string, flags *flag.FlagSet, dst any) error {
	k := koanf.New(".")
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	if err := k.Load(env.ProviderWithValue(envPrefix, ".", envValueMapper(envPrefix, slicePaths(dst))), nil); err != nil {
		return fmt.Errorf("config: env: %w", err)
	}
	if flags != nil {
		if err := k.Load(posflag.Provider(flags, ".", k), nil); err != nil {
			return fmt.Errorf("config: flags: %w", err)
		}
	}
	if err := k.Unmarshal("", dst); err != nil {
		return fmt.Errorf("config: unmarshal: %w", err)
	}
	return nil
}

// ChangedFlags returns a FlagSet holding only the flags explicitly set on
// fs, minus any names in except (e.g. "config") — the only-changed-flags
// bridge products hand to Load.
func ChangedFlags(fs *flag.FlagSet, except ...string) *flag.FlagSet {
	skip := map[string]bool{}
	for _, n := range except {
		skip[n] = true
	}
	changed := flag.NewFlagSet("changed", flag.ContinueOnError)
	fs.Visit(func(f *flag.Flag) {
		if !skip[f.Name] {
			changed.AddFlag(f)
		}
	})
	return changed
}

// EnvKeyMapper maps PREFIX_SERVER_MAX__DEPTH -> server.max_depth: the prefix
// is stripped, a double underscore preserves an underscore inside a key, and
// a single underscore is a level separator.
func EnvKeyMapper(prefix string) func(string) string {
	return func(s string) string {
		s = strings.ToLower(strings.TrimPrefix(s, prefix))
		s = strings.ReplaceAll(s, "__", "\x00")
		s = strings.ReplaceAll(s, "_", ".")
		return strings.ReplaceAll(s, "\x00", "_")
	}
}

// envValueMapper maps env keys with EnvKeyMapper and splits comma-separated
// values for keys whose destination is a slice (PREFIX_SCHEMA_SCHEMAS=public,audit).
func envValueMapper(prefix string, lists map[string]bool) func(key, value string) (string, any) {
	keyMapper := EnvKeyMapper(prefix)
	return func(key, value string) (string, any) {
		k := keyMapper(key)
		if !lists[k] {
			return k, value
		}
		parts := strings.Split(value, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return k, out
	}
}

// slicePaths returns the koanf paths of every slice-typed field reachable
// from dst (a pointer to a struct with koanf tags); nil for anything else.
func slicePaths(dst any) map[string]bool {
	out := map[string]bool{}
	v := reflect.Indirect(reflect.ValueOf(dst))
	if v.Kind() == reflect.Struct {
		collectSlicePaths(v.Type(), "", out)
	}
	return out
}

func collectSlicePaths(t reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("koanf")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && (key == "" || strings.HasPrefix(key, ",")) {
			collectSlicePaths(f.Type, prefix, out)
			continue
		}
		if key == "" || key == "-" {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch f.Type.Kind() {
		case reflect.Struct:
			collectSlicePaths(f.Type, path, out)
		case reflect.Slice:
			out[path] = true
		}
	}
}

// Discover returns the first of names that exists in the working directory
// (e.g. Discover("pdbq.yaml", "pdbq.yml")), or "".
func Discover(names ...string) string {
	for _, cand := range names {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// ParseTrustedProxy parses one rls.auth.trusted_proxies entry: either a CIDR
// block ("10.0.0.0/8") or a single IP ("10.0.0.1", treated as /32 or /128).
func ParseTrustedProxy(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
