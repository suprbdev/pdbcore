package pgexec

import "strings"

// QuoteIdent double-quotes an SQL identifier, doubling embedded quotes.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ValidRole reports whether s is a safe role name: [A-Za-z0-9_]+.
func ValidRole(s string) bool { return simpleIdent(s) }

// ValidClaimKey reports whether s is a safe claim key for set_config
// (same alphabet as ValidRole).
func ValidClaimKey(s string) bool { return simpleIdent(s) }

func simpleIdent(s string) bool {
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}
