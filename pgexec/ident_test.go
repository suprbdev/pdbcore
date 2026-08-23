package pgexec

import "testing"

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"users":      `"users"`,
		`we"ird`:     `"we""ird"`,
		"Mixed Case": `"Mixed Case"`,
	}
	for in, want := range cases {
		if got := QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestValidRoleAndClaimKey(t *testing.T) {
	cases := map[string]bool{
		"app_user": true, "APP1": true, "_x": true,
		"": false, "app-user": false, "app user": false, `a"b`: false, "rôle": false,
	}
	for in, want := range cases {
		if got := ValidRole(in); got != want {
			t.Errorf("ValidRole(%q) = %v, want %v", in, got, want)
		}
		if got := ValidClaimKey(in); got != want {
			t.Errorf("ValidClaimKey(%q) = %v, want %v", in, got, want)
		}
	}
}
