package confx

import (
	"encoding"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// movedKeys maps configuration keys removed before 1.0 to the key that
// replaced them. Load names the replacement when a file or environment
// variable still uses an old key; products add their own with WithMoved.
var movedKeys = map[string]string{
	"rls.enabled":              "roles.switch",
	"rls.anonymous_role":       "roles.anonymous",
	"rls.default_role":         "roles.default",
	"rls.allowed_roles":        "roles.allowed",
	"rls.role_claim":           "auth.jwt.role_claim_path",
	"rls.claims_prefix":        "claims.prefix",
	"rls.auth.mode":            "auth.mode",
	"rls.auth.jwt_secret":      "auth.jwt.secret",
	"rls.auth.jwks_url":        "auth.jwt.jwks_url",
	"rls.auth.jwks_cache_ttl":  "auth.jwt.jwks_cache_ttl",
	"rls.auth.jwt_issuer":      "auth.jwt.issuer",
	"rls.auth.jwt_audience":    "auth.jwt.audience",
	"rls.auth.header_prefix":   "auth.headers.prefix",
	"rls.auth.header_claims":   "auth.headers.claims",
	"rls.auth.trusted_proxies": "auth.headers.trusted_proxies",
	"rls.auth.jwt_type":        "auth.mint_type",
}

type keyKind int

const (
	// leafKey holds a value.
	leafKey keyKind = iota
	// sectionKey is a struct whose children are checked.
	sectionKey
	// openKey is a map or interface accepting any child key.
	openKey
)

// keySchema is the set of koanf paths a config struct accepts.
type keySchema map[string]keyKind

var (
	durationType = reflect.TypeOf(time.Duration(0))
	textType     = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func schemaOf(dst any) keySchema {
	s := keySchema{}
	t := reflect.TypeOf(dst)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil && t.Kind() == reflect.Struct {
		s.collect(t, "")
	}
	return s
}

func (s keySchema) collect(t reflect.Type, prefix string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("koanf")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && (key == "" || strings.HasPrefix(key, ",")) {
			s.collect(f.Type, prefix)
			continue
		}
		if key == "" || key == "-" || !f.IsExported() {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		ft := f.Type
		switch {
		case ft == durationType || reflect.PointerTo(ft).Implements(textType):
			s[path] = leafKey
		case ft.Kind() == reflect.Struct:
			s[path] = sectionKey
			s.collect(ft, path)
		case ft.Kind() == reflect.Map || ft.Kind() == reflect.Interface:
			s[path] = openKey
		default:
			s[path] = leafKey
		}
	}
}

// known reports whether key is accepted: a path of the schema, or a key
// below a map or interface path.
func (s keySchema) known(key string) bool {
	if _, ok := s[key]; ok {
		return true
	}
	for i := len(key) - 1; i > 0; i-- {
		if key[i] != '.' {
			continue
		}
		switch kind, ok := s[key[:i]]; {
		case ok && kind == openKey:
			return true
		case ok:
			return false
		}
	}
	return false
}

// hints holds the keys an unknown key is explained by: moved maps an old
// key (or section) to its replacement, removed maps one to the reason it
// is gone.
type hints struct {
	moved, removed map[string]string
}

// section reports whether name is a top-level key of the schema, of a
// moved key or of a removed key, or a near miss of one.
func (s keySchema) section(name string, h hints) bool {
	tops := []string{}
	for p := range s {
		if !strings.Contains(p, ".") {
			if p == name {
				return true
			}
			tops = append(tops, p)
		}
	}
	for _, m := range []map[string]string{h.moved, h.removed} {
		for old := range m {
			if top, _, _ := strings.Cut(old, "."); top == name {
				return true
			}
		}
	}
	return len(name) >= 4 && closest(name, tops) != ""
}

// unknownKey describes an unknown key: where it moved to, why it was
// removed, or the closest known or moved key; name renders a path for the
// message.
func (s keySchema) unknownKey(key string, h hints, name func(string) string) string {
	if to, ok := movedTo(key, h.moved); ok {
		return fmt.Sprintf("moved to %s", name(to))
	}
	if why, ok := removedAt(key, h.removed); ok {
		return "removed: " + why
	}
	cands := make([]string, 0, len(s)+len(h.moved))
	for p := range s {
		cands = append(cands, p)
	}
	for old := range h.moved {
		cands = append(cands, old)
	}
	best := closest(key, cands)
	if best == "" {
		return ""
	}
	if to, ok := movedTo(best, h.moved); ok {
		return fmt.Sprintf("did you mean %s (formerly %s)?", name(to), best)
	}
	return fmt.Sprintf("did you mean %s?", name(best))
}

// movedTo maps key through moved: an exact entry, or the longest entry
// that is a dotted prefix of key, with the rest of key appended.
func movedTo(key string, moved map[string]string) (string, bool) {
	if to, ok := moved[key]; ok {
		return to, true
	}
	best := ""
	for old := range moved {
		if strings.HasPrefix(key, old+".") && len(old) > len(best) {
			best = old
		}
	}
	if best == "" {
		return "", false
	}
	return moved[best] + key[len(best):], true
}

// removedAt reports why key, or the section it lies in, was removed.
func removedAt(key string, removed map[string]string) (string, bool) {
	for k := key; ; {
		if why, ok := removed[k]; ok {
			return why, true
		}
		i := strings.LastIndexByte(k, '.')
		if i < 0 {
			return "", false
		}
		k = k[:i]
	}
}

// closest returns the candidate nearest to s by edit distance, or "" when
// none is within max(2, len(s)/5) edits. Ties go to the lexically smallest.
func closest(s string, cands []string) string {
	sorted := append([]string(nil), cands...)
	sort.Strings(sorted)
	limit := max(2, len(s)/5)
	best, bestD := "", limit+1
	for _, c := range sorted {
		if d := editDistance(s, c); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b, in bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// envName renders a koanf path as the environment variable that sets it:
// the inverse of EnvKeyMapper.
func envName(prefix, path string) string {
	s := strings.ReplaceAll(path, "_", "__")
	s = strings.ReplaceAll(s, ".", "_")
	return prefix + strings.ToUpper(s)
}
