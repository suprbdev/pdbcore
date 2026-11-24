package confx

import (
	stdflag "flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// pdbqServer mirrors pdbq's Server section so the reference-YAML golden
// (testdata/pdbq.example.yaml, shaped like the body of pdbq's committed
// example) pins what ExampleYAML renders over the shared sections.
type pdbqServer struct {
	Addr                 string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	GraphiQL             bool          `koanf:"graphiql" doc:"Serve the GraphiQL playground at / (off by default; enable for development)."`
	ExposeSchema         bool          `koanf:"expose_schema" doc:"Serve the generated SDL at /schema.graphql (off by default; it reveals every table, column and relation to unauthenticated callers)."`
	RequestTimeout       time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout."`
	MaxBodyBytes         int64         `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes."`
	MaxDepth             int           `koanf:"max_depth" doc:"Maximum GraphQL selection depth per operation."`
	MaxCost              int           `koanf:"max_cost" doc:"Maximum estimated cost (selected fields x list multipliers) per operation."`
	MaxPageSize          int           `koanf:"max_page_size" doc:"Maximum rows per page; first/last above this are rejected and it is the default page size when neither is given."`
	CORSOrigins          []string      `koanf:"cors_origins" doc:"Allowed CORS origins for /graphql (exact match, or '*' for any). Empty disables CORS headers entirely."`
	Compression          bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	APQ                  bool          `koanf:"apq" doc:"Enable Apollo automatic persisted queries: clients send a sha256 hash in the persistedQuery extension and register the document once on a miss (in-memory cache)."`
	PersistedQueriesPath string        `koanf:"persisted_queries_path" doc:"JSON file mapping sha256 hex hashes to GraphQL documents, preloaded as persisted queries (never evicted)."`
	PersistedOnly        bool          `koanf:"persisted_only" doc:"Reject requests that do not reference a persisted query via the persistedQuery extension (requires apq or persisted_queries_path)."`
	ReadOnly             bool          `koanf:"read_only" doc:"Reject every mutation operation (read-only mode) for maintenance, demos or defense in depth. Also toggleable at runtime when embedding pdbq as a library (App.SetReadOnly)."`
	DisableIntrospection bool          `koanf:"disable_introspection" doc:"Reject __schema / __type introspection queries (off by default). Enable in production to hide the schema shape; __typename keeps working."`
}

// pdbqTX is pdbq's own TX (per_request sits first, which struct
// embedding cannot express — see TestSquashEmbedding).
type pdbqTX struct {
	PerRequest bool   `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
	Isolation  string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
}

// pdbqPlugins is a plugin settings struct shaped like a product's: one
// section per configurable plugin.
type pdbqPlugins struct {
	AdvancedFilters struct {
		Relations bool `koanf:"relations" doc:"Filter on related rows."`
		MaxDepth  int  `koanf:"max_depth" doc:"Deepest relation filter."`
	} `koanf:"advanced_filters" doc:"advanced_filters plugin."`
}

type pdbqConfig struct {
	Database Database             `koanf:"database"`
	Server   pdbqServer           `koanf:"server"`
	Schema   Schema               `koanf:"schema"`
	Filters  Filters              `koanf:"filters"`
	Auth     Auth                 `koanf:"auth"`
	Roles    Roles                `koanf:"roles"`
	Claims   Claims               `koanf:"claims"`
	TX       pdbqTX               `koanf:"transactions"`
	Watch    Watch                `koanf:"watch"`
	Errors   Errors               `koanf:"errors"`
	Plugins  Plugins[pdbqPlugins] `koanf:"plugins"`
	Log      Log                  `koanf:"log"`
	Metrics  Metrics              `koanf:"metrics"`
}

func pdbqDefaults() pdbqConfig {
	return pdbqConfig{
		Database: Database{MaxConns: 10, ConnectTimeout: 10 * time.Second, StatementTimeout: 25 * time.Second},
		Server: pdbqServer{
			Addr: ":8080", RequestTimeout: 30 * time.Second, MaxBodyBytes: 1 << 20,
			MaxDepth: 15, MaxCost: 10000, MaxPageSize: 100,
		},
		Schema:  Schema{Schemas: []string{"public"}, Functions: true},
		Filters: Filters{IndexedOnly: true},
		Auth:    DefaultAuth("X-Pdbq-Claim-"),
		Roles:   DefaultRoles(),
		Claims:  DefaultClaims(),
		TX:      pdbqTX{Isolation: "read_committed"},
		Watch:   Watch{PollInterval: 5 * time.Second, Channel: "pdbq_ddl"},
		Errors:  Errors{Detail: "prod"},
		Plugins: Plugins[pdbqPlugins]{},
		Log:     Log{Level: "info", Format: "text"},
		Metrics: DefaultMetrics(),
	}
}

// secretFile holds "file-secret" and a trailing newline.
const secretFile = "testdata/jwt-secret"

var update = stdflag.Bool("update", false, "rewrite testdata/pdbq.example.yaml")

func TestExampleYAMLMatchesPdbqGolden(t *testing.T) {
	golden := filepath.Join("testdata", "pdbq.example.yaml")
	got := ExampleYAML(pdbqDefaults())
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("ExampleYAML drifted from pdbq's example body:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestExampleYAMLDocOverride(t *testing.T) {
	out := ExampleYAML(pdbqDefaults(), WithDoc("errors.detail", "custom detail doc"))
	if !strings.Contains(out, "# custom detail doc\n  detail: \"prod\"") {
		t.Fatalf("override not applied:\n%s", out)
	}
	if strings.Contains(out, "error detail in error responses") {
		t.Fatal("original doc must be replaced")
	}
}

// squashTX is the shape pdbq could use to embed the shared TX section.
type squashTX struct {
	TX         `koanf:",squash"`
	PerRequest bool `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
}

type squashConfig struct {
	TX squashTX `koanf:"transactions"`
}

func TestSquashEmbedding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	if err := os.WriteFile(path, []byte("transactions:\n  isolation: serializable\n  per_request: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDBX_TRANSACTIONS_MAX__RETRIES", "3")
	cfg := squashConfig{TX: squashTX{TX: TX{Isolation: "read_committed"}}}
	if err := Load("PDBX_", path, &cfg, WithFlags(map[string]any{"transactions.max_retries": "5"})); err != nil {
		t.Fatal(err)
	}
	if cfg.TX.Isolation != "serializable" || !cfg.TX.PerRequest || cfg.TX.MaxRetries != 5 {
		t.Fatalf("squash load = %+v", cfg.TX)
	}
	// ExampleYAML inlines the embedded fields, but embedded fields can only
	// come before or after the wrapper's own fields.
	out := ExampleYAML(cfg)
	want := "transactions:\n" +
		"  # Transaction isolation level: read_committed, repeatable_read, serializable.\n  isolation: \"serializable\"\n" +
		"  # Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back.\n  max_retries: 5\n" +
		"  # Use one transaction for the whole request instead of one per operation.\n  per_request: true\n\n"
	if out != want {
		t.Fatalf("squash example:\n%s\nwant:\n%s", out, want)
	}
}

func TestLoadLayering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pdbq.yaml")
	yaml := `
database:
  url: "postgres://file/db"
server:
  addr: ":9999"
roles:
  switch: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDBQ_SERVER_ADDR", ":7777") // env overrides file
	t.Setenv("PDBQ_LOG_LEVEL", "debug")
	cfg := pdbqDefaults()
	if err := Load("PDBQ_", path, &cfg, WithFlags(map[string]any{"log.level": "warn"})); err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://file/db" {
		t.Errorf("file value lost: %q", cfg.Database.URL)
	}
	if cfg.Server.Addr != ":7777" {
		t.Errorf("env did not override file: %q", cfg.Server.Addr)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("changed flag did not override env: %q", cfg.Log.Level)
	}
	if cfg.Errors.Detail != "prod" {
		t.Errorf("unchanged flag clobbered default: %q", cfg.Errors.Detail)
	}
	if cfg.Server.MaxDepth != 15 {
		t.Errorf("default lost: %d", cfg.Server.MaxDepth)
	}
	if cfg.Roles.Switch {
		t.Error("file bool not applied")
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg := pdbqDefaults()
	err := Load("PDBQ_", filepath.Join(t.TempDir(), "nope.yaml"), &cfg)
	if err == nil || !strings.HasPrefix(err.Error(), "config: read ") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvKeyMapper(t *testing.T) {
	m := EnvKeyMapper("PDBQ_")
	cases := map[string]string{
		"PDBQ_DATABASE_URL":         "database.url",
		"PDBQ_SERVER_MAX__DEPTH":    "server.max_depth",
		"PDBQ_RLS_AUTH_JWT__SECRET": "rls.auth.jwt_secret",
	}
	for in, want := range cases {
		if got := m(in); got != want {
			t.Errorf("EnvKeyMapper(%q) = %q, want %q", in, got, want)
		}
	}
	if got := EnvKeyMapper("PDBR_")("PDBR_FILTERS_INDEXED__ONLY"); got != "filters.indexed_only" {
		t.Errorf("PDBR_ mapper = %q", got)
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pdbq.yml"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := Discover("pdbq.yaml", "pdbq.yml"); got != "pdbq.yml" {
		t.Fatalf("Discover = %q", got)
	}
	if got := Discover("none.yaml"); got != "" {
		t.Fatalf("Discover(missing) = %q", got)
	}
}

func TestParseTrustedProxy(t *testing.T) {
	for _, s := range []string{"10.0.0.0/8", "10.0.0.5", "::1", "fe80::/10"} {
		if _, err := parseTrustedProxy(s); err != nil {
			t.Errorf("%s rejected: %v", s, err)
		}
	}
	if p, _ := parseTrustedProxy("10.0.0.5"); p.Bits() != 32 {
		t.Errorf("single IP must be /32, got %v", p)
	}
	if _, err := parseTrustedProxy("not-an-ip"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestValidateDefaultsClean(t *testing.T) {
	cfg := pdbqDefaults()
	cfg.Auth.JWT.Secret = "s"
	var errs []string
	errs = append(errs, ValidateErrors(cfg.Errors)...)
	errs = append(errs, ValidateIdentity(cfg.Auth, cfg.Roles, cfg.Claims)...)
	errs = append(errs, ValidateTX(TX{Isolation: cfg.TX.Isolation, MaxRetries: cfg.TX.MaxRetries})...)
	errs = append(errs, ValidateWatch(cfg.Watch, cfg.Schema.CachePath)...)
	errs = append(errs, ValidateSchema(cfg.Schema)...)
	errs = append(errs, ValidateLog(cfg.Log)...)
	errs = append(errs, ValidateMetrics(cfg.Metrics, cfg.Server.Addr)...)
	if err := InvalidConfig(errs); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if InvalidConfig(nil) != nil {
		t.Fatal("empty errs must be nil")
	}
	cfg.Errors.Detail = "strict"
	if errs := ValidateErrors(cfg.Errors); len(errs) != 0 {
		t.Fatalf("strict rejected: %v", errs)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		run  func() []string
		want string
	}{
		"errors.detail": {func() []string { return ValidateErrors(Errors{Detail: "verbose"}) },
			`errors.detail: "verbose" is not 'dev', 'prod' or 'strict'`},
		"auth.mode": {func() []string { return ValidateIdentity(Auth{Mode: "oauth"}, Roles{}, Claims{}) },
			`auth.mode: "oauth" is not 'jwt', 'headers' or 'none'`},
		"jwt secret": {func() []string {
			return ValidateIdentity(Auth{Mode: "jwt", JWT: JWT{RoleClaimPath: "role"}}, Roles{}, Claims{})
		}, "auth.jwt.secret, auth.jwt.secret_file or auth.jwt.jwks_url is required in auth mode jwt (use auth.mode: none to accept no credentials)"},
		"secret and file": {func() []string {
			return ValidateIdentity(Auth{Mode: "none", JWT: JWT{Secret: "s", SecretFile: secretFile}}, Roles{}, Claims{})
		}, "auth.jwt.secret and auth.jwt.secret_file are mutually exclusive"},
		"secret file missing": {func() []string {
			return ValidateIdentity(Auth{Mode: "none", JWT: JWT{SecretFile: "/nonexistent/secret"}}, Roles{}, Claims{})
		}, "auth.jwt.secret_file: open /nonexistent/secret: no such file or directory"},
		"jwks url": {func() []string {
			return ValidateIdentity(Auth{Mode: "jwt", JWT: JWT{JWKSURL: "idp.example/jwks", RoleClaimPath: "role"}}, Roles{}, Claims{})
		}, `auth.jwt.jwks_url: "idp.example/jwks" is not an http(s) URL`},
		"leeway": {func() []string {
			return ValidateIdentity(Auth{Mode: "none", JWT: JWT{Leeway: -time.Second}}, Roles{}, Claims{})
		}, "auth.jwt.leeway must be >= 0"},
		"role claim path": {func() []string {
			return ValidateIdentity(Auth{Mode: "jwt", JWT: JWT{Secret: "s", RoleClaimPath: "a..b"}}, Roles{}, Claims{})
		}, `auth.jwt.role_claim_path: invalid role claim path "a..b": empty name`},
		"mint_type needs secret": {func() []string {
			return ValidateIdentity(Auth{Mode: "none", MintType: "public.jwt"}, Roles{}, Claims{})
		}, "auth.mint_type requires auth.jwt.secret or auth.jwt.secret_file: minting signs with the HMAC secret (JWKS keys are verify-only)"},
		"mint_type qualified": {func() []string {
			return ValidateIdentity(Auth{Mode: "none", MintType: "jwt", JWT: JWT{Secret: "s"}}, Roles{}, Claims{})
		}, `auth.mint_type: "jwt" must be schema-qualified, e.g. public.jwt`},
		"none needs anonymous": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{Switch: true}, Claims{})
		}, "roles.anonymous is required in auth mode none while roles.switch is on: every request runs as the anonymous role (set roles.switch: false to run as the connection role)"},
		"trusted proxy": {func() []string {
			return ValidateIdentity(Auth{Mode: "none", Headers: Headers{TrustedProxies: []string{"10.0.0.0/8", "not-an-ip"}}}, Roles{}, Claims{})
		}, `auth.headers.trusted_proxies: "not-an-ip" is not a valid IP or CIDR block`},
		"anonymous role none": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{Anonymous: "none"}, Claims{})
		}, `roles.anonymous: "none" is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role`},
		"default role pg_": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{Default: "pg_read_all_data"}, Claims{})
		}, `roles.default: "pg_read_all_data" is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role`},
		"allowed role none": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{Allowed: []string{"app_user", "NONE"}}, Claims{})
		}, `roles.allowed: "NONE" is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role`},
		"claims prefix": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{}, Claims{Prefix: "plpgsql"})
		}, `claims.prefix: invalid claims prefix "plpgsql": "plpgsql" is a server or extension settings namespace`},
		"claims json setting": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{}, Claims{JSONSetting: "claims"})
		}, `claims.json_setting: invalid claims setting "claims": use a dotted name such as "request.jwt.claims"`},
		"claims json under prefix": {func() []string {
			return ValidateIdentity(Auth{Mode: "none"}, Roles{}, Claims{Prefix: "jwt.claims", JSONSetting: "JWT.claims.all"})
		}, `claims.json_setting: "JWT.claims.all" lies under claims.prefix "jwt.claims", where a claim of that name would overwrite it`},
		"header prefix empty": {func() []string {
			return ValidateIdentity(Auth{Mode: "headers", Headers: Headers{RoleClaim: "role", TrustedProxies: []string{"10.0.0.1"}}}, Roles{}, Claims{})
		}, `auth.headers.prefix: "" must be letters, digits and hyphens ending in a hyphen, e.g. X-Claim-`},
		"header prefix no trailing hyphen": {func() []string {
			return ValidateIdentity(Auth{Mode: "headers", Headers: Headers{Prefix: "X-Pdbq-Claim", RoleClaim: "role", TrustedProxies: []string{"10.0.0.1"}}}, Roles{}, Claims{})
		}, `auth.headers.prefix: "X-Pdbq-Claim" must be letters, digits and hyphens ending in a hyphen, e.g. X-Claim-`},
		"header prefix underscore": {func() []string {
			return ValidateIdentity(Auth{Mode: "headers", Headers: Headers{Prefix: "X_Claim-", RoleClaim: "role", TrustedProxies: []string{"10.0.0.1"}}}, Roles{}, Claims{})
		}, `auth.headers.prefix: "X_Claim-" must be letters, digits and hyphens ending in a hyphen, e.g. X-Claim-`},
		"header role claim": {func() []string {
			return ValidateIdentity(Auth{Mode: "headers", Headers: Headers{Prefix: "X-Pdbq-Claim-", RoleClaim: "AppRole", TrustedProxies: []string{"10.0.0.1"}}}, Roles{}, Claims{})
		}, `auth.headers.role_claim: "AppRole" cannot be sent as a claim header: use lower-case letters and digits joined by single underscores`},
		"header claims": {func() []string {
			return ValidateIdentity(Auth{Mode: "headers", Headers: Headers{Prefix: "X-Pdbq-Claim-", RoleClaim: "role", Claims: []string{"user_id", "user-id"}, TrustedProxies: []string{"10.0.0.1"}}}, Roles{}, Claims{})
		}, `auth.headers.claims: "user-id" cannot be sent as a claim header: use lower-case letters and digits joined by single underscores`},
		"headers needs trusted proxies": {func() []string {
			return ValidateIdentity(Auth{Mode: "headers", Headers: Headers{Prefix: "X-Pdbq-Claim-", RoleClaim: "role"}}, Roles{}, Claims{})
		}, "auth.headers.trusted_proxies is required in auth mode headers: list the gateway addresses allowed to send claim headers"},
		"watch channel length": {func() []string { return ValidateWatch(Watch{Enabled: true, Channel: strings.Repeat("c", 64)}, "") },
			`watch.channel: "` + strings.Repeat("c", 64) + `" is longer than 63 bytes`},
		"isolation": {func() []string { return ValidateTX(TX{Isolation: "chaos"}) },
			`transactions.isolation: "chaos" is not 'read_committed', 'repeatable_read' or 'serializable'`},
		"max_retries": {func() []string { return ValidateTX(TX{Isolation: "serializable", MaxRetries: -1}) },
			"transactions.max_retries must be >= 0"},
		"watch+cache": {func() []string { return ValidateWatch(Watch{Enabled: true, Channel: "c"}, "x") },
			"watch.enabled cannot be combined with schema.cache_path"},
		"watch channel": {func() []string { return ValidateWatch(Watch{Enabled: true, Channel: "bad$$chan"}, "") },
			`watch.channel: "bad$$chan" invalid: only letters, digits and underscores are allowed`},
		"no schemas": {func() []string { return ValidateSchema(Schema{}) },
			"schema.schemas must list at least one schema"},
		"log level": {func() []string { return ValidateLog(Log{Level: "loud", Format: "text"}) },
			`log.level: "loud" is not 'debug', 'info', 'warn' or 'error'`},
		"log format": {func() []string { return ValidateLog(Log{Level: "info", Format: "jsn"}) },
			`log.format: "jsn" is not 'text' or 'json'`},
		"plugin names": {func() []string {
			return ValidatePluginNames([]string{"postgis", "advanced-filters"}, []string{"postgis", "advanced_filters"})
		},
			`plugins.disabled: unknown plugin "advanced-filters", did you mean "advanced_filters"?`},
		"metrics path relative": {func() []string { return ValidateMetrics(Metrics{Path: "metrics"}, "") },
			`metrics.path: "metrics" must start with / and name a path other than /`},
		"metrics path root": {func() []string { return ValidateMetrics(Metrics{Path: "/"}, "") },
			`metrics.path: "/" must start with / and name a path other than /`},
		"metrics path query": {func() []string { return ValidateMetrics(Metrics{Path: "/metrics?x=1"}, "") },
			`metrics.path: "/metrics?x=1" must not contain a query or fragment`},
		"metrics addr": {func() []string { return ValidateMetrics(Metrics{Path: "/metrics", Addr: "9090"}, "") },
			`metrics.addr: "9090" is not a host:port listen address, e.g. :9090`},
		"metrics addr no port": {func() []string { return ValidateMetrics(Metrics{Path: "/metrics", Addr: "localhost:"}, "") },
			`metrics.addr: "localhost:" is not a host:port listen address, e.g. :9090`},
		"metrics addr is server addr": {func() []string { return ValidateMetrics(Metrics{Path: "/metrics", Addr: ":8080"}, ":8080") },
			`metrics.addr: ":8080" is the main server address; leave it empty to serve metrics there`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := tc.run()
			if len(errs) != 1 || errs[0] != tc.want {
				t.Fatalf("errs = %q, want [%q]", errs, tc.want)
			}
		})
	}
	err := InvalidConfig([]string{"a", "b"})
	if err == nil || err.Error() != "invalid config:\n  - a\n  - b" {
		t.Fatalf("InvalidConfig = %v", err)
	}
}

func TestAuthConfig(t *testing.T) {
	a := Auth{
		Mode: "headers",
		JWT:  JWT{SecretFile: secretFile, JWKSURL: "https://idp/jwks", Issuer: "i", Audience: "a", Leeway: time.Second, RoleClaimPath: "app.role"},
		Headers: Headers{Prefix: "X-Pdbr-Claim-", Claims: []string{"user_id"}, RoleClaim: "app_role",
			TrustedProxies: []string{"10.0.0.0/8", "garbage", "192.168.1.1"}},
	}
	r := Roles{Switch: false, Default: "d", Anonymous: "anon", Allowed: []string{"x"}}
	cfg, err := AuthConfig(a, r)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "headers" || cfg.HeaderPrefix != "X-Pdbr-Claim-" || cfg.HeaderRoleClaim != "app_role" || cfg.RoleClaimPath != "app.role" ||
		cfg.JWTSecret != "file-secret" || cfg.JWKSURL != "https://idp/jwks" || cfg.JWTIssuer != "i" || cfg.JWTAudience != "a" || cfg.Leeway != time.Second ||
		cfg.DefaultRole != "d" || cfg.AnonymousRole != "anon" || len(cfg.AllowedRoles) != 1 || !cfg.SkipRoleSwitch ||
		len(cfg.HeaderClaims) != 1 || cfg.HeaderClaims[0] != "user_id" {
		t.Fatalf("AuthConfig = %+v", cfg)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1].Bits() != 32 {
		t.Fatalf("TrustedProxies = %v (invalid entries dropped, singles /32)", cfg.TrustedProxies)
	}
	if _, err := AuthConfig(Auth{JWT: JWT{SecretFile: "/nonexistent"}}, r); err == nil {
		t.Fatal("unreadable secret file accepted")
	}
	if cfg, _ := AuthConfig(Auth{}, Roles{Switch: true}); cfg.SkipRoleSwitch {
		t.Fatal("roles.switch on must switch roles")
	}
}

func TestResolveSecret(t *testing.T) {
	if s, err := (JWT{Secret: "inline"}).ResolveSecret(); err != nil || s != "inline" {
		t.Fatalf("inline = %q, %v", s, err)
	}
	if s, err := (JWT{SecretFile: secretFile}).ResolveSecret(); err != nil || s != "file-secret" {
		t.Fatalf("file = %q, %v", s, err)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (JWT{SecretFile: empty}).ResolveSecret(); err == nil || !strings.HasSuffix(err.Error(), "is empty") {
		t.Fatalf("empty file err = %v", err)
	}
}

func TestIdentityWarnings(t *testing.T) {
	if w := IdentityWarnings(DefaultAuth("X-C-"), Roles{Switch: true, Allowed: []string{"a"}}); len(w) != 0 {
		t.Fatalf("warnings = %q", w)
	}
	if w := IdentityWarnings(DefaultAuth("X-C-"), Roles{Switch: false}); len(w) != 1 || !strings.HasPrefix(w[0], "roles.switch is off") {
		t.Fatalf("warnings = %q", w)
	}
	if w := IdentityWarnings(DefaultAuth("X-C-"), Roles{Switch: true}); len(w) != 1 || !strings.HasPrefix(w[0], "roles.allowed is empty") {
		t.Fatalf("warnings = %q", w)
	}
	if w := IdentityWarnings(Auth{Mode: "none"}, Roles{Switch: true, Anonymous: "a"}); len(w) != 0 {
		t.Fatalf("warnings = %q", w)
	}
}

func TestPluginsDisabledSet(t *testing.T) {
	set := Plugins[struct{}]{Disabled: []string{"a", "b"}}.DisabledSet()
	if !set["a"] || !set["b"] || set["c"] {
		t.Fatalf("set = %v", set)
	}
}

func TestLoadSplitsEnvLists(t *testing.T) {
	t.Setenv("PDBX_SCHEMA_SCHEMAS", "public, audit")
	t.Setenv("PDBX_DATABASE_URL", "postgres://a,b@h/db")
	cfg := pdbqDefaults()
	if err := Load("PDBX_", "", &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Schema.Schemas) != 2 || cfg.Schema.Schemas[0] != "public" || cfg.Schema.Schemas[1] != "audit" {
		t.Errorf("schemas = %v", cfg.Schema.Schemas)
	}
	if cfg.Database.URL != "postgres://a,b@h/db" {
		t.Errorf("non-list value split: %q", cfg.Database.URL)
	}
}

func TestValidateMetricsAccepts(t *testing.T) {
	for _, m := range []Metrics{
		DefaultMetrics(),
		{Enabled: true, Path: "/internal/metrics", Addr: ":9090"},
		{Enabled: true, Path: "/metrics", Addr: "127.0.0.1:9090"},
		{Enabled: true, Path: "/metrics", Addr: "[::1]:9090"},
	} {
		if errs := ValidateMetrics(m, ":8080"); len(errs) != 0 {
			t.Errorf("%+v rejected: %v", m, errs)
		}
	}
}

func TestValidateContracts(t *testing.T) {
	ok := []Contract{
		{Name: "internal", Path: "/"},
		{Name: "public"},
		{Name: "partner-v2", Default: "exclude", Path: "/partners/v2"},
	}
	if errs := ValidateContracts(ok, "", true); len(errs) != 0 {
		t.Fatalf("valid contracts rejected: %v", errs)
	}
	if errs := ValidateContracts(ok[1:], "/api", false); len(errs) != 0 {
		t.Fatalf("valid contracts under a base path rejected: %v", errs)
	}
	if got := (Contract{Name: "public"}).URLPath("/api/"); got != "/api/public" {
		t.Errorf("URLPath = %q", got)
	}
	if got := (Contract{Name: "public"}).URLPath(""); got != "/public" {
		t.Errorf("URLPath = %q", got)
	}
	cases := []struct {
		cs         []Contract
		rootShared bool
		want       string
	}{
		{[]Contract{{Name: ""}}, true, `contracts[0].name: "" must be 1-63 lower-case letters, digits, _ or -, starting with a letter`},
		{[]Contract{{Name: "Public"}}, true, `contracts[0].name: "Public" must be 1-63 lower-case letters, digits, _ or -, starting with a letter`},
		{[]Contract{{Name: "a"}, {Name: "a", Path: "/b"}}, true, `contracts[1].name: "a" is used by another contract`},
		{[]Contract{{Name: "a", Default: "deny"}}, true, `contracts[0].default: "deny" is not 'include' or 'exclude'`},
		{[]Contract{{Name: "a", Path: "/a/"}}, true, `contracts[0].path: "/a/" must be / or start with / and name path segments of letters, digits, _ . ~ or -, without a trailing /`},
		{[]Contract{{Name: "a", Path: "/x/../y"}}, true, `contracts[0].path: "/x/../y" must be / or start with / and name path segments of letters, digits, _ . ~ or -, without a trailing /`},
		{[]Contract{{Name: "a", Path: "/{x}"}}, true, `contracts[0].path: "/{x}" must be / or start with / and name path segments of letters, digits, _ . ~ or -, without a trailing /`},
		{[]Contract{{Name: "a", Path: "/x"}, {Name: "b", Path: "/x"}}, true, `contracts[1].path: "/x" is also the path of contract "a"`},
		{[]Contract{{Name: "a", Path: "/x"}, {Name: "b", Path: "/x/y"}}, true, `contracts[1].path: "/x/y" overlaps the path "/x" of contract "a"`},
		{[]Contract{{Name: "a", Path: "/"}, {Name: "b"}}, false, `contracts[1].path: "/b" overlaps the path "/" of contract "a"`},
	}
	for _, tc := range cases {
		errs := ValidateContracts(tc.cs, "", tc.rootShared)
		if !slices.Contains(errs, tc.want) {
			t.Errorf("%+v: errors %q, want %q", tc.cs, errs, tc.want)
		}
	}
	if c, ok := FindContract(ok, "public"); !ok || c.Name != "public" {
		t.Error("FindContract")
	}
	if _, ok := FindContract(ok, "nope"); ok {
		t.Error("FindContract found a missing contract")
	}
	if s := (Contract{Name: "p", Default: "exclude"}).Spec(); s.Name != "p" || s.Default != "exclude" {
		t.Errorf("Spec = %+v", s)
	}
}

func TestLoadContracts(t *testing.T) {
	type cfg struct {
		Contracts []Contract `koanf:"contracts"`
	}
	path := filepath.Join(t.TempDir(), "c.yaml")
	yaml := `
contracts:
  - name: public
  - name: partner
    default: exclude
    path: /partners
  - internal
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := Load("PDBX_", path, &c); err != nil {
		t.Fatal(err)
	}
	want := []Contract{{Name: "public"}, {Name: "partner", Default: "exclude", Path: "/partners"}, {Name: "internal"}}
	if !slices.Equal(c.Contracts, want) {
		t.Errorf("yaml contracts = %+v, want %+v", c.Contracts, want)
	}

	t.Setenv("PDBX_CONTRACTS", "public,partner")
	c = cfg{}
	if err := Load("PDBX_", "", &c); err != nil {
		t.Fatal(err)
	}
	if want := []Contract{{Name: "public"}, {Name: "partner"}}; !slices.Equal(c.Contracts, want) {
		t.Errorf("env contracts = %+v, want %+v", c.Contracts, want)
	}
}
