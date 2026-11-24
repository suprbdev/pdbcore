package confx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRejectsUnknownFileKeys(t *testing.T) {
	path := writeYAML(t, `
rls:
  enabled: false
  enabeld: true
  auth:
    jwt_secret: s
roles:
  anonymus: web_anon
server:
  addr: ":1"
  adr: ":2"
plugins:
  settings:
    advanced-filters:
      relations: false
    advanced_filters:
      relation: false
totally_unrelated: 1
`)
	cfg := pdbqDefaults()
	err := Load("PDBX_", path, &cfg, WithMoved(map[string]string{"plugins.settings.advanced-filters": "plugins.settings.advanced_filters"}))
	if err == nil {
		t.Fatal("unknown keys accepted")
	}
	for _, want := range []string{
		"unknown key rls.enabled: moved to roles.switch",
		"unknown key rls.enabeld: did you mean roles.switch (formerly rls.enabled)?",
		"unknown key rls.auth.jwt_secret: moved to auth.jwt.secret",
		"unknown key roles.anonymus: did you mean roles.anonymous?",
		"unknown key server.adr: did you mean server.addr?",
		"unknown key plugins.settings.advanced-filters.relations: moved to plugins.settings.advanced_filters.relations",
		"unknown key plugins.settings.advanced_filters.relation: did you mean plugins.settings.advanced_filters.relations?",
		"unknown key totally_unrelated\n",
	} {
		if !strings.Contains(err.Error()+"\n", want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if !strings.HasPrefix(err.Error(), "invalid config:\n  - ") {
		t.Errorf("error shape: %v", err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("a rejected load must not apply values, addr = %q", cfg.Server.Addr)
	}
}

func TestLoadRejectsUnknownEnvKeysInKnownSections(t *testing.T) {
	t.Setenv("PDBX_RLS_ENABLED", "false")
	t.Setenv("PDBX_ROLES_ANONYMUS", "web_anon")
	t.Setenv("PDBX_DATABSE_URL", "postgres://x")
	t.Setenv("PDBX_TEST_DATABASE_URL", "postgres://test")
	t.Setenv("PDBX_PRINT_SDL_DIGEST", "1")
	cfg := pdbqDefaults()
	err := Load("PDBX_", "", &cfg)
	if err == nil {
		t.Fatal("unknown env keys accepted")
	}
	for _, want := range []string{
		"unknown environment variable PDBX_RLS_ENABLED (rls.enabled): moved to PDBX_ROLES_SWITCH",
		"unknown environment variable PDBX_ROLES_ANONYMUS (roles.anonymus): did you mean PDBX_ROLES_ANONYMOUS?",
		"unknown environment variable PDBX_DATABSE_URL (databse.url): did you mean PDBX_DATABASE_URL?",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	for _, unwanted := range []string{"TEST", "PRINT"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("variable outside the config sections reported:\n%v", err)
		}
	}
}

func TestLoadIgnoresUnrelatedPrefixedEnv(t *testing.T) {
	t.Setenv("PDBX_TEST_DATABASE_URL", "postgres://test")
	t.Setenv("PDBX_TEST_PDBQ_DIR", "/x")
	cfg := pdbqDefaults()
	if err := Load("PDBX_", "", &cfg); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDecodesEnvIntoTypedSettings(t *testing.T) {
	t.Setenv("PDBX_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS", "true")
	t.Setenv("PDBX_PLUGINS_SETTINGS_ADVANCED__FILTERS_MAX__DEPTH", "4")
	t.Setenv("PDBX_ROLES_SWITCH", "false")
	t.Setenv("PDBX_AUTH_JWT_LEEWAY", "30s")
	t.Setenv("PDBX_ROLES_ALLOWED", "app_user, app_admin")
	cfg := pdbqDefaults()
	if err := Load("PDBX_", "", &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.Plugins.Settings.AdvancedFilters
	if !s.Relations || s.MaxDepth != 4 || cfg.Roles.Switch || cfg.Auth.JWT.Leeway != 30*time.Second ||
		len(cfg.Roles.Allowed) != 2 || cfg.Roles.Allowed[1] != "app_admin" {
		t.Fatalf("env decode: settings %+v roles %+v leeway %v", s, cfg.Roles, cfg.Auth.JWT.Leeway)
	}
	t.Setenv("PDBX_PLUGINS_SETTINGS_ADVANCED__FILTERS_MAX__DEPTH", "deep")
	cfg = pdbqDefaults()
	if err := Load("PDBX_", "", &cfg); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Fatalf("bad int accepted: %v", err)
	}
}

func TestLoadRejectsUnknownKeysInListEntries(t *testing.T) {
	path := writeYAML(t, "contracts:\n  - name: public\n    default_policy: exclude\n")
	var c struct {
		Contracts []Contract `koanf:"contracts"`
	}
	err := Load("PDBX_", path, &c)
	if err == nil || !strings.Contains(err.Error(), "default_policy") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadAcceptsMapKeys(t *testing.T) {
	path := writeYAML(t, "filters:\n  allow_columns:\n    public.users: [email]\n")
	cfg := pdbqDefaults()
	if err := Load("PDBX_", path, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Filters.AllowColumns) == 0 {
		t.Fatalf("allow_columns = %v", cfg.Filters.AllowColumns)
	}
}

func TestEnvName(t *testing.T) {
	for path, want := range map[string]string{
		"roles.switch":     "PDBQ_ROLES_SWITCH",
		"server.max_depth": "PDBQ_SERVER_MAX__DEPTH",
		"plugins.settings.advanced_filters.relations": "PDBQ_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS",
	} {
		got := envName("PDBQ_", path)
		if got != want || EnvKeyMapper("PDBQ_")(got) != path {
			t.Errorf("envName(%q) = %q, want %q (and back)", path, got, want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"a", "", 1}, {"kitten", "sitting", 3}, {"enabeld", "enabled", 2}, {"test", "auth", 4}} {
		if got := editDistance(tc.a, tc.b); got != tc.d {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.d)
		}
	}
}

func TestLoadNamesRemovedKeys(t *testing.T) {
	path := writeYAML(t, `
transactions:
  mutations: true
legacy:
  thing: 1
`)
	t.Setenv("PDBX_TRANSACTIONS_MUTATIONS", "false")
	t.Setenv("PDBX_LEGACY_OTHER", "x")
	cfg := pdbqDefaults()
	err := Load("PDBX_", path, &cfg, WithRemoved(map[string]string{
		"transactions.mutations": "mutations always run in a transaction",
		"legacy":                 "the legacy section is gone",
	}))
	if err == nil {
		t.Fatal("removed keys accepted")
	}
	for _, want := range []string{
		"unknown key transactions.mutations: removed: mutations always run in a transaction",
		"unknown key legacy.thing: removed: the legacy section is gone",
		"unknown environment variable PDBX_TRANSACTIONS_MUTATIONS (transactions.mutations): removed: mutations always run in a transaction",
		"unknown environment variable PDBX_LEGACY_OTHER (legacy.other): removed: the legacy section is gone",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestLoadRejectsDoubledUnderscoreAfterSection(t *testing.T) {
	for env, want := range map[string]string{
		"PDBX_ROLES__ALLOWED":         "unknown environment variable PDBX_ROLES__ALLOWED (roles_allowed): did you mean PDBX_ROLES_ALLOWED?",
		"PDBX_AUTH__MODE":             "unknown environment variable PDBX_AUTH__MODE (auth_mode): did you mean PDBX_AUTH_MODE?",
		"PDBX_FILTERS__INDEXED__ONLY": "unknown environment variable PDBX_FILTERS__INDEXED__ONLY (filters_indexed_only): did you mean PDBX_FILTERS_INDEXED__ONLY?",
		"PDBX_AUTH__JWT_SECRET":       "unknown environment variable PDBX_AUTH__JWT_SECRET (auth_jwt.secret): did you mean PDBX_AUTH_JWT_SECRET?",
		"PDBX_ROLES__NO__SUCH__THING": "unknown environment variable PDBX_ROLES__NO__SUCH__THING (roles_no_such_thing)",
		"PDBX_RLS__ENABLED":           "unknown environment variable PDBX_RLS__ENABLED (rls_enabled): moved to PDBX_ROLES_SWITCH",
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "x")
			cfg := pdbqDefaults()
			err := Load("PDBX_", "", &cfg)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
	t.Setenv("PDBX_UNRELATED__THING", "1")
	t.Setenv("PDBX_TEST_DATABASE_URL", "postgres://test")
	cfg := pdbqDefaults()
	if err := Load("PDBX_", "", &cfg); err != nil {
		t.Fatalf("variables outside every section must stay ignored: %v", err)
	}
}

func TestValidateSharedDurations(t *testing.T) {
	for want, errs := range map[string][]string{
		"database.max_conns must be >= 1":         ValidateDatabase(Database{ConnectTimeout: time.Second}),
		"database.connect_timeout must be > 0":    ValidateDatabase(Database{MaxConns: 1}),
		"database.statement_timeout must be >= 0": ValidateDatabase(Database{MaxConns: 1, ConnectTimeout: time.Second, StatementTimeout: -time.Second}),
		"auth.jwt.jwks_cache_ttl must be > 0":     ValidateIdentity(Auth{Mode: "jwt", JWT: JWT{JWKSURL: "https://idp.example/jwks", RoleClaimPath: "role"}}, Roles{Switch: true}, Claims{}),
		"watch.poll_interval must be > 0":         ValidateWatch(Watch{Enabled: true, Channel: "c"}, ""),
	} {
		if !slices.ContainsFunc(errs, func(e string) bool { return strings.HasPrefix(e, want) }) {
			t.Errorf("want %q, got %q", want, errs)
		}
	}
	if errs := ValidateDatabase(Database{MaxConns: 1, ConnectTimeout: time.Second}); len(errs) != 0 {
		t.Errorf("statement_timeout 0 rejected: %q", errs)
	}
	if errs := ValidateWatch(Watch{Channel: "c"}, ""); len(errs) != 0 {
		t.Errorf("disabled watch checked: %q", errs)
	}
}
