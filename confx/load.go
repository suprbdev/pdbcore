package confx

import (
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// LoadOption customises Load.
type LoadOption func(*loadOpts)

type loadOpts struct {
	hints
	flags map[string]any
}

// WithMoved adds product keys to the keys Load knows were moved: old key
// (or old section, which maps every key below it) to the key that
// replaced it.
func WithMoved(moved map[string]string) LoadOption {
	return func(o *loadOpts) {
		for k, v := range moved {
			o.moved[k] = v
		}
	}
}

// WithRemoved names keys that were removed without a replacement: old key
// (or old section, covering every key below it) to the reason, e.g.
// "mutations always run in a transaction". Load reports such a key as
// "unknown key <key>: removed: <reason>".
func WithRemoved(removed map[string]string) LoadOption {
	return func(o *loadOpts) {
		for k, v := range removed {
			o.removed[k] = v
		}
	}
}

// WithFlags layers command-line values over the file and the environment:
// koanf path (e.g. server.addr) to value. Pass only the flags the user
// actually set so unset flags cannot shadow YAML/env values. Values are
// decoded like environment values: strings set booleans, numbers and
// durations, and a []string sets a list.
func WithFlags(values map[string]any) LoadOption {
	return func(o *loadOpts) {
		for k, v := range values {
			o.flags[k] = v
		}
	}
}

// FlagHint explains an unknown command-line flag name (without the dashes;
// flags are named after koanf paths) the way Load explains an unknown file
// key: "moved to --<key>", "removed: <reason>" or "did you mean --<key>?".
// dst is the defaults struct the flags configure and opts the same
// WithMoved and WithRemoved options the product passes to Load. It returns
// "" when nothing is close.
func FlagHint(dst any, name string, opts ...LoadOption) string {
	o := newLoadOpts()
	for _, opt := range opts {
		opt(&o)
	}
	return schemaOf(dst).unknownKey(name, o.hints, func(p string) string { return "--" + p })
}

func newLoadOpts() loadOpts {
	o := loadOpts{hints: hints{moved: map[string]string{}, removed: map[string]string{}}, flags: map[string]any{}}
	for k, v := range movedKeys {
		o.moved[k] = v
	}
	return o
}

// Load layers configuration into dst (which already holds the defaults):
// optional YAML file, then envPrefix-prefixed environment variables, then
// command-line values (WithFlags).
//
// Loading is strict: a file key that dst has no field for is an error
// naming the key it moved to (WithMoved), the reason it was removed
// (WithRemoved) or the closest valid key. An environment variable with
// the prefix is checked the same way when its first segment names a
// section of dst (or of a moved or removed key), or nearly does, or when
// it starts with a section name and a doubled underscore
// (PDBQ_ROLES__ALLOWED, a misspelt PDBQ_ROLES_ALLOWED); other
// variables with the prefix (PDBQ_TEST_DATABASE_URL) are ignored. Values
// are decoded with weak typing, so environment strings set booleans,
// numbers, durations and lists. Validation is the caller's job.
func Load(envPrefix, path string, dst any, opts ...LoadOption) error {
	o := newLoadOpts()
	for _, opt := range opts {
		opt(&o)
	}
	schema := schemaOf(dst)
	var errs []string
	k := koanf.New(".")
	if path != "" {
		fk := koanf.New(".")
		if err := fk.Load(file.Provider(path), yaml.Parser()); err != nil {
			return fmt.Errorf("config: read %s: %w", path, err)
		}
		for _, key := range fk.Keys() {
			if schema.known(key) {
				continue
			}
			msg := "unknown key " + key
			if hint := schema.unknownKey(key, o.hints, func(p string) string { return p }); hint != "" {
				msg += ": " + hint
			}
			errs = append(errs, msg)
		}
		if err := k.Merge(fk); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	envVars := map[string]string{}
	mapper := envValueMapper(envPrefix, slicePaths(dst))
	ek := koanf.New(".")
	if err := ek.Load(env.ProviderWithValue(envPrefix, ".", func(key, value string) (string, any) {
		path, v := mapper(key, value)
		envVars[path] = key
		return path, v
	}), nil); err != nil {
		return fmt.Errorf("config: env: %w", err)
	}
	for _, key := range ek.Keys() {
		if schema.known(key) {
			continue
		}
		envPath := func(p string) string { return envName(envPrefix, p) }
		top, _, _ := strings.Cut(key, ".")
		hintKey := key
		if !schema.section(top, o.hints) {
			fixed, ok := schema.underscoredSection(key, o.hints)
			if !ok {
				ek.Delete(key)
				continue
			}
			hintKey = fixed
		}
		msg := fmt.Sprintf("unknown environment variable %s (%s)", envVars[key], key)
		hint := ""
		switch {
		case hintKey != key && schema.known(hintKey):
			hint = fmt.Sprintf("did you mean %s?", envPath(hintKey))
		case hintKey != key:
			hint = schema.unknownKey(hintKey, o.hints, envPath)
		default:
			hint = schema.unknownKey(key, o.hints, envPath)
		}
		if hint != "" {
			msg += ": " + hint
		}
		errs = append(errs, msg)
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return InvalidConfig(errs)
	}
	if err := k.Merge(ek); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	for key, v := range o.flags {
		if err := k.Set(key, v); err != nil {
			return fmt.Errorf("config: flag %s: %w", key, err)
		}
	}
	err := k.UnmarshalWithConf("", dst, koanf.UnmarshalConf{DecoderConfig: &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.TextUnmarshallerHookFunc()),
		WeaklyTypedInput: true,
		ErrorUnused:      true,
	}})
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
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

// parseTrustedProxy parses one auth.headers.trusted_proxies entry: either a CIDR
// block ("10.0.0.0/8") or a single IP ("10.0.0.1", treated as /32 or /128).
func parseTrustedProxy(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
