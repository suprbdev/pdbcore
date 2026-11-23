package pgexec

import (
	"fmt"
	"strings"
)

// QuoteIdent double-quotes an SQL identifier, doubling embedded quotes.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ValidRole reports whether s is a role a request may switch to: it matches
// [A-Za-z0-9_]+, is not "none" (assigning "none" to the role setting is
// RESET ROLE, which would run the request as the connection's login role),
// is not "public", and does not start with "pg_" (reserved for the
// server's predefined roles), in any letter case.
func ValidRole(s string) bool {
	if !simpleIdent(s) {
		return false
	}
	l := strings.ToLower(s)
	return l != "none" && l != "public" && !strings.HasPrefix(l, "pg_")
}

// ValidClaimKey reports whether s is a safe claim key for set_config
// ([A-Za-z0-9_]+).
func ValidClaimKey(s string) bool { return simpleIdent(s) }

// reservedClaimsNamespaces are first components of ClaimsPrefix that name
// real server or extension settings, which claims must never write.
var reservedClaimsNamespaces = map[string]bool{
	"plpgsql": true, "plperl": true, "plpython": true, "pltcl": true,
	"auto_explain": true, "postgis": true, "pgaudit": true, "sepgsql": true,
	"timescaledb": true, "citus": true, "pglogical": true, "pgsodium": true,
	"cron": true, "anon": true, "pgrst": true, "bdr": true, "pgroonga": true,
}

// ValidateClaimsPrefix checks an Identity.ClaimsPrefix ("" means
// DefaultClaimsPrefix): dot-separated components, each a letter or
// underscore followed by letters, digits and underscores, and a first
// component that is not a real setting namespace (plpgsql, auto_explain,
// postgis, anything starting with pg_, ...). Claims are written to
// prefix + "." + key, so a reserved prefix would let a token set server
// or extension settings.
func ValidateClaimsPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	parts := strings.Split(prefix, ".")
	for _, p := range parts {
		if p == "" || !simpleIdent(p) || (p[0] >= '0' && p[0] <= '9') {
			return fmt.Errorf("invalid claims prefix %q: use dot-separated identifiers such as %q", prefix, DefaultClaimsPrefix)
		}
	}
	first := strings.ToLower(parts[0])
	if strings.HasPrefix(first, "pg_") || reservedClaimsNamespaces[first] {
		return fmt.Errorf("invalid claims prefix %q: %q is a server or extension settings namespace", prefix, parts[0])
	}
	return nil
}

func simpleIdent(s string) bool {
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}
