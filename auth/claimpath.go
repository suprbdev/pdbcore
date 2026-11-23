package auth

import (
	"fmt"
	"strconv"
	"strings"
)

// claimPath is a parsed role claim path: object keys (string) and array
// indexes (int), applied in order.
type claimPath []any

// ValidateClaimPath checks a role claim path, the subset of PostgREST's
// jwt-role-claim-key syntax pdbcore accepts: object keys separated by dots,
// with an optional leading dot, each a bare name (any characters but
// . [ ] " and spaces) or a double-quoted name (any characters but "),
// optionally followed by array indexes such as [0]. Examples: role,
// .app_metadata.role, realm_access.roles[0], "https://example.com/role".
func ValidateClaimPath(s string) error {
	_, err := parseClaimPath(s)
	return err
}

func parseClaimPath(s string) (claimPath, error) {
	bad := func(why string) (claimPath, error) {
		return nil, fmt.Errorf("invalid role claim path %q: %s", s, why)
	}
	rest := strings.TrimPrefix(s, ".")
	if rest == "" {
		return bad("empty")
	}
	var p claimPath
	for {
		var key string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return bad("unterminated quoted name")
			}
			key, rest = rest[1:end+1], rest[end+2:]
		} else {
			end := strings.IndexAny(rest, `.[]" `)
			if end < 0 {
				end = len(rest)
			}
			key, rest = rest[:end], rest[end:]
		}
		if key == "" {
			return bad("empty name")
		}
		p = append(p, key)
		for strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return bad("unterminated index")
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil || n < 0 || rest[1] == '+' {
				return bad("index must be a non-negative integer")
			}
			p = append(p, n)
			rest = rest[end+1:]
		}
		if rest == "" {
			return p, nil
		}
		if rest[0] != '.' {
			return bad(fmt.Sprintf("unexpected %q", rest[0]))
		}
		rest = rest[1:]
	}
}

// lookup returns the value at p in claims.
func (p claimPath) lookup(claims map[string]any) (any, bool) {
	var cur any = claims
	for _, seg := range p {
		switch s := seg.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[s]; !ok {
				return nil, false
			}
		case int:
			l, ok := cur.([]any)
			if !ok || s >= len(l) {
				return nil, false
			}
			cur = l[s]
		}
	}
	return cur, true
}
