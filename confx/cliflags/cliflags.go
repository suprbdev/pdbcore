// Package cliflags adapts a spf13/pflag flag set to [confx.WithFlags], so
// package confx itself does not depend on pflag. Config flags are named
// after koanf paths (--server.addr sets server.addr).
package cliflags

import (
	flag "github.com/spf13/pflag"
)

// Changed returns the flags explicitly set on fs, minus any names in except
// (e.g. "config"), as koanf path to value: a []string for slice flags
// (StringSlice, StringArray, IntSlice, ...), the flag's string form
// otherwise. Unset flags are left out so they cannot shadow file and
// environment values.
func Changed(fs *flag.FlagSet, except ...string) map[string]any {
	skip := make(map[string]bool, len(except))
	for _, n := range except {
		skip[n] = true
	}
	out := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		if skip[f.Name] {
			return
		}
		if sv, ok := f.Value.(flag.SliceValue); ok {
			out[f.Name] = sv.GetSlice()
			return
		}
		out[f.Name] = f.Value.String()
	})
	return out
}
