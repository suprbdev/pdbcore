// Package cliflags adapts a spf13/pflag flag set to [confx.WithFlags], so
// package confx itself does not depend on pflag. Config flags are named
// after koanf paths (--server.addr sets server.addr).
package cliflags

import (
	"fmt"
	"regexp"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/confx"
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

var unknownFlag = regexp.MustCompile(`^unknown flag: --([^\s=]+)$`)

// Explain adds the moved-to, removed or closest-key hint of
// [confx.FlagHint] to the error pflag returns for an unknown flag
// (unknown flag: --rls.enabled becomes "unknown flag: --rls.enabled: moved
// to --roles.switch"). Flags without a dot (--config, --output) are
// command-local and left alone, as is any other error. dst and opts are the
// defaults and the options the product passes to [confx.Load]. Install it
// with cobra's SetFlagErrorFunc.
func Explain(err error, dst any, opts ...confx.LoadOption) error {
	if err == nil {
		return nil
	}
	m := unknownFlag.FindStringSubmatch(err.Error())
	if m == nil || !strings.Contains(m[1], ".") {
		return err
	}
	if hint := confx.FlagHint(dst, m[1], opts...); hint != "" {
		return fmt.Errorf("%w: %s", err, hint)
	}
	return err
}
