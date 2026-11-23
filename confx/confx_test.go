package confx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"
)

// pdbqServer mirrors pdbq's Server section so the reference-YAML golden
// (testdata/pdbq.example.yaml, the body of pdbq's committed example) proves
// ExampleYAML reproduces pdbq's output byte for byte over the shared
// sections.
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

// pdbqTX is pdbq's own 4-field TX (per_request sits second, which struct
// embedding cannot express — see TestSquashEmbedding).
type pdbqTX struct {
	Mutations  bool   `koanf:"mutations" doc:"Wrap every mutation in a transaction."`
	PerRequest bool   `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
	Isolation  string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
}

type pdbqConfig struct {
	Database Database   `koanf:"database"`
	Server   pdbqServer `koanf:"server"`
	Schema   Schema     `koanf:"schema"`
	Filters  Filters    `koanf:"filters"`
	RLS      RLS        `koanf:"rls"`
	TX       pdbqTX     `koanf:"transactions"`
	Watch    Watch      `koanf:"watch"`
	Errors   Errors     `koanf:"errors"`
	Plugins  Plugins    `koanf:"plugins"`
	Log      Log        `koanf:"log"`
	Metrics  Metrics    `koanf:"metrics"`
}

func pdbqDefaults() pdbqConfig {
	return pdbqConfig{
		Database: Database{MaxConns: 10, ConnectTimeout: 10 * time.Second, StatementTimeout: 30 * time.Second},
		Server: pdbqServer{
			Addr: ":8080", RequestTimeout: 30 * time.Second, MaxBodyBytes: 1 << 20,
			MaxDepth: 15, MaxCost: 10000, MaxPageSize: 100,
		},
		Schema:  Schema{Schemas: []string{"public"}, Functions: true},
		Filters: Filters{IndexedOnly: true},
		RLS: RLS{
			Enabled: true, AnonymousRole: "anonymous", RoleClaim: "role", ClaimsPrefix: "pdbq.claims",
			Auth: Auth{Mode: "jwt", HeaderPrefix: "X-Pdbq-Claim-", JWKSCacheTTL: time.Hour},
		},
		TX:      pdbqTX{Mutations: true, Isolation: "read_committed"},
		Watch:   Watch{PollInterval: 5 * time.Second, Channel: "pdbq_ddl"},
		Errors:  Errors{Detail: "prod"},
		Plugins: Plugins{Settings: map[string]map[string]any{}},
		Log:     Log{Level: "info", Format: "text"},
		Metrics: DefaultMetrics(),
	}
}

func TestExampleYAMLMatchesPdbqGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "pdbq.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := ExampleYAML(pdbqDefaults())
	if got != string(want) {
		t.Fatalf("ExampleYAML drifted from pdbq's example body:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestExampleYAMLDocOverride(t *testing.T) {
	out := ExampleYAML(pdbqDefaults(), WithDoc("errors.detail", "custom detail doc"))
	if !strings.Contains(out, "# custom detail doc\n  detail: \"prod\"") {
		t.Fatalf("override not applied:\n%s", out)
	}
	if strings.Contains(out, "GraphQL errors") {
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
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Bool("transactions.mutations", true, "")
	if err := fs.Parse([]string{"--transactions.mutations=false"}); err != nil {
		t.Fatal(err)
	}
	cfg := squashConfig{TX: squashTX{TX: TX{Mutations: true, Isolation: "read_committed"}}}
	if err := Load("PDBX_", path, ChangedFlags(fs), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.TX.Isolation != "serializable" || !cfg.TX.PerRequest || cfg.TX.MaxRetries != 3 || cfg.TX.Mutations {
		t.Fatalf("squash load = %+v", cfg.TX)
	}
	// ExampleYAML inlines the embedded fields, but embedded fields can only
	// come before or after the wrapper's own fields — pdbq's per_request
	// sits between mutations and isolation, so byte-identical output needs
	// pdbq to keep its own TX struct.
	out := ExampleYAML(cfg)
	want := "transactions:\n" +
		"  # Wrap every mutation in a transaction.\n  mutations: false\n" +
		"  # Transaction isolation level: read_committed, repeatable_read, serializable.\n  isolation: \"serializable\"\n" +
		"  # Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back.\n  max_retries: 3\n" +
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
rls:
  enabled: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDBQ_SERVER_ADDR", ":7777") // env overrides file
	t.Setenv("PDBQ_LOG_LEVEL", "debug")
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("log.level", "", "")
	fs.String("errors.detail", "", "")
	fs.String("config", "", "")
	if err := fs.Parse([]string{"--log.level=warn", "--config=" + path}); err != nil {
		t.Fatal(err)
	}
	cfg := pdbqDefaults()
	if err := Load("PDBQ_", path, ChangedFlags(fs, "config"), &cfg); err != nil {
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
	if cfg.RLS.Enabled {
		t.Error("file bool not applied")
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg := pdbqDefaults()
	err := Load("PDBQ_", filepath.Join(t.TempDir(), "nope.yaml"), nil, &cfg)
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
		if _, err := ParseTrustedProxy(s); err != nil {
			t.Errorf("%s rejected: %v", s, err)
		}
	}
	if p, _ := ParseTrustedProxy("10.0.0.5"); p.Bits() != 32 {
		t.Errorf("single IP must be /32, got %v", p)
	}
	if _, err := ParseTrustedProxy("not-an-ip"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestValidateDefaultsClean(t *testing.T) {
	cfg := pdbqDefaults()
	cfg.RLS.Auth.JWTSecret = "s"
	var errs []string
	errs = append(errs, ValidateErrors(cfg.Errors)...)
	errs = append(errs, ValidateRLS(cfg.RLS)...)
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
		"auth.mode": {func() []string { return ValidateRLS(RLS{Auth: Auth{Mode: "oauth"}}) },
			`rls.auth.mode: "oauth" is not 'jwt', 'headers' or 'none'`},
		"jwt secret": {func() []string {
			return ValidateRLS(RLS{Enabled: true, AnonymousRole: "a", Auth: Auth{Mode: "jwt"}})
		}, "rls.auth.jwt_secret or rls.auth.jwks_url is required when rls.enabled and auth mode is jwt"},
		"jwt_type needs secret": {func() []string {
			return ValidateRLS(RLS{Auth: Auth{Mode: "none", JWTType: "public.jwt"}})
		}, "rls.auth.jwt_type requires rls.auth.jwt_secret: minting signs with the HMAC secret (JWKS keys are verify-only)"},
		"jwt_type qualified": {func() []string {
			return ValidateRLS(RLS{Auth: Auth{Mode: "none", JWTType: "jwt", JWTSecret: "s"}})
		}, `rls.auth.jwt_type: "jwt" must be schema-qualified, e.g. public.jwt`},
		"anon role": {func() []string {
			return ValidateRLS(RLS{Enabled: true, Auth: Auth{Mode: "none"}})
		}, "rls.anonymous_role is required when rls.enabled: an empty role would run unauthenticated requests as the privileged connection role, bypassing RLS"},
		"trusted proxy": {func() []string {
			return ValidateRLS(RLS{Auth: Auth{Mode: "none", TrustedProxies: []string{"10.0.0.0/8", "not-an-ip"}}})
		}, `rls.auth.trusted_proxies: "not-an-ip" is not a valid IP or CIDR block`},
		"anonymous role none": {func() []string {
			return ValidateRLS(RLS{Enabled: true, AnonymousRole: "none", Auth: Auth{Mode: "none"}})
		}, `rls.anonymous_role: "none" is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role`},
		"default role pg_": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", DefaultRole: "pg_read_all_data", Auth: Auth{Mode: "none"}})
		}, `rls.default_role: "pg_read_all_data" is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role`},
		"allowed role none": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", AllowedRoles: []string{"app_user", "NONE"}, Auth: Auth{Mode: "none"}})
		}, `rls.allowed_roles: "NONE" is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role`},
		"claims prefix": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", ClaimsPrefix: "plpgsql", Auth: Auth{Mode: "none"}})
		}, `rls.claims_prefix: invalid claims prefix "plpgsql": "plpgsql" is a server or extension settings namespace`},
		"header prefix empty": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", RoleClaim: "role", Auth: Auth{Mode: "headers"}})
		}, `rls.auth.header_prefix: "" must be letters, digits and hyphens ending in a hyphen, e.g. X-Pdbq-Claim-`},
		"header prefix no trailing hyphen": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", RoleClaim: "role", Auth: Auth{Mode: "headers", HeaderPrefix: "X-Pdbq-Claim"}})
		}, `rls.auth.header_prefix: "X-Pdbq-Claim" must be letters, digits and hyphens ending in a hyphen, e.g. X-Pdbq-Claim-`},
		"header prefix underscore": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", RoleClaim: "role", Auth: Auth{Mode: "headers", HeaderPrefix: "X_Claim-"}})
		}, `rls.auth.header_prefix: "X_Claim-" must be letters, digits and hyphens ending in a hyphen, e.g. X-Pdbq-Claim-`},
		"header role claim": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", RoleClaim: "AppRole", Auth: Auth{Mode: "headers", HeaderPrefix: "X-Pdbq-Claim-"}})
		}, `rls.role_claim: "AppRole" cannot be sent as a claim header: use lower-case letters and digits joined by single underscores`},
		"header claims": {func() []string {
			return ValidateRLS(RLS{AnonymousRole: "anon", RoleClaim: "role", Auth: Auth{Mode: "headers", HeaderPrefix: "X-Pdbq-Claim-", HeaderClaims: []string{"user_id", "user-id"}}})
		}, `rls.auth.header_claims: "user-id" cannot be sent as a claim header: use lower-case letters and digits joined by single underscores`},
		"watch channel length": {func() []string { return ValidateWatch(Watch{Enabled: true, Channel: strings.Repeat("c", 64)}, "") },
			`watch.channel: "` + strings.Repeat("c", 64) + `" is longer than 63 bytes`},
		"isolation": {func() []string { return ValidateTX(TX{Isolation: "chaos"}) },
			`transactions.isolation: "chaos" invalid`},
		"max_retries": {func() []string { return ValidateTX(TX{Isolation: "serializable", MaxRetries: -1}) },
			"transactions.max_retries must be >= 0"},
		"watch+cache": {func() []string { return ValidateWatch(Watch{Enabled: true, Channel: "c"}, "x") },
			"watch.enabled cannot be combined with schema.cache_path"},
		"watch channel": {func() []string { return ValidateWatch(Watch{Enabled: true, Channel: "bad$$chan"}, "") },
			`watch.channel: "bad$$chan" invalid: only letters, digits and underscores are allowed`},
		"no schemas": {func() []string { return ValidateSchema(Schema{}) },
			"schema.schemas must list at least one schema"},
		"log level": {func() []string { return ValidateLog(Log{Level: "loud"}) },
			`log.level: "loud" invalid`},
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
	rls := RLS{
		Enabled: true, RoleClaim: "role", DefaultRole: "d", AnonymousRole: "anon", AllowedRoles: []string{"x"},
		Auth: Auth{Mode: "headers", HeaderPrefix: "X-Pdbr-Claim-", HeaderClaims: []string{"user_id"}, TrustedProxies: []string{"10.0.0.0/8", "garbage", "192.168.1.1"}},
	}
	cfg := AuthConfig(rls)
	if !cfg.Enabled || cfg.Mode != "headers" || cfg.HeaderPrefix != "X-Pdbr-Claim-" || cfg.RoleClaim != "role" ||
		cfg.DefaultRole != "d" || cfg.AnonymousRole != "anon" || len(cfg.AllowedRoles) != 1 ||
		len(cfg.HeaderClaims) != 1 || cfg.HeaderClaims[0] != "user_id" {
		t.Fatalf("AuthConfig = %+v", cfg)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1].Bits() != 32 {
		t.Fatalf("TrustedProxies = %v (invalid entries dropped, singles /32)", cfg.TrustedProxies)
	}
	if AuthConfig(RLS{}).TrustedProxies != nil {
		t.Fatal("no proxies configured must stay nil (trust every peer)")
	}
	if p := AuthConfig(RLS{Auth: Auth{TrustedProxies: []string{"junk"}}}).TrustedProxies; p == nil || len(p) != 0 {
		t.Fatalf("all-invalid list must be empty non-nil (trust nobody), got %v", p)
	}
}

func TestPluginsDisabledSet(t *testing.T) {
	set := Plugins{Disabled: []string{"a", "b"}}.DisabledSet()
	if !set["a"] || !set["b"] || set["c"] {
		t.Fatalf("set = %v", set)
	}
}

func TestLoadSplitsEnvLists(t *testing.T) {
	t.Setenv("PDBX_SCHEMA_SCHEMAS", "public, audit")
	t.Setenv("PDBX_DATABASE_URL", "postgres://a,b@h/db")
	cfg := pdbqDefaults()
	if err := Load("PDBX_", "", nil, &cfg); err != nil {
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
