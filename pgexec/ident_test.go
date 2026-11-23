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

func TestValidRoleRejectsSpecialRoles(t *testing.T) {
	for _, r := range []string{"none", "NONE", "None", "public", "PUBLIC", "pg_read_all_data", "pg_signal_backend", "PG_monitor", "pg_"} {
		if ValidRole(r) {
			t.Errorf("ValidRole(%q) = true, want false", r)
		}
		if !ValidClaimKey(r) {
			t.Errorf("ValidClaimKey(%q) = false; claim keys are not roles", r)
		}
	}
	for _, r := range []string{"nonesuch", "anonymous", "app_pg_user", "publicist"} {
		if !ValidRole(r) {
			t.Errorf("ValidRole(%q) = false, want true", r)
		}
	}
}

func TestValidateClaimsPrefix(t *testing.T) {
	ok := []string{"", "pdbq.claims", "pdbr.claims", "request.jwt.claims", "claims", "_x.y1", "app"}
	bad := []string{
		".claims", "claims.", "a..b", "1abc.claims", "a.1b", "a-b.c", "a b", "pdbq.claims;", `a"b`,
		"plpgsql", "PLPGSQL.x", "pg_stat_statements", "pg_trgm.x", "PG_x", "auto_explain", "postgis.x", "pgaudit", "cron.x", "anon",
	}
	for _, p := range ok {
		if err := ValidateClaimsPrefix(p); err != nil {
			t.Errorf("ValidateClaimsPrefix(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range bad {
		if err := ValidateClaimsPrefix(p); err == nil {
			t.Errorf("ValidateClaimsPrefix(%q) = nil, want an error", p)
		}
	}
}

func TestValidateClaimsSetting(t *testing.T) {
	for _, s := range []string{"request.jwt.claims", "app.claims", "_x.y1"} {
		if err := ValidateClaimsSetting(s); err != nil {
			t.Errorf("ValidateClaimsSetting(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", "claims", ".claims", "a..b", "1a.b", "plpgsql.x", "pg_trgm.x", "pgrst.claims", "a.b;"} {
		if err := ValidateClaimsSetting(s); err == nil {
			t.Errorf("ValidateClaimsSetting(%q) = nil, want an error", s)
		}
	}
}
