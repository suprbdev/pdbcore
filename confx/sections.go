// Package confx is the configuration toolkit shared by the products: koanf
// loading with the YAML < env < changed-flags precedence, the env key
// mapper, reference-YAML generation from `koanf`+`doc` struct tags, and the
// config sections (with validation) that both products expose verbatim.
//
// Products own their root Config, DefaultConfig, Validate and any
// product-only sections (pdbq's Server/TX.PerRequest, pdbr's Server).
package confx

import (
	"strings"
	"time"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/contract"
)

// Database is the connection section.
type Database struct {
	URL              string        `koanf:"url" doc:"PostgreSQL connection URL, e.g. postgres://user:pass@host:5432/db. Also honours standard PG* env vars when empty."`
	MaxConns         int32         `koanf:"max_conns" doc:"Maximum pooled connections; 0 uses the pgx pool default (the larger of 4 and the number of CPUs)."`
	ConnectTimeout   time.Duration `koanf:"connect_timeout" doc:"Timeout for establishing a connection (must be positive)."`
	StatementTimeout time.Duration `koanf:"statement_timeout" doc:"Per-statement timeout applied to every request (0 disables). Keep it below the HTTP request timeout so a slow statement fails with SQLSTATE 57014 (a timeout error the client can act on) before the request is cut off."`
}

// Schema selects what is introspected and how.
type Schema struct {
	Schemas   []string `koanf:"schemas" doc:"PostgreSQL schemas to expose."`
	CachePath string   `koanf:"cache_path" doc:"Boot from this schema cache file (written by the schema dump command) instead of introspecting."`
	Functions bool     `koanf:"functions" doc:"Expose PostgreSQL functions as custom queries/mutations."`
}

// Filters is the filterable-column policy.
type Filters struct {
	IndexedOnly  bool                `koanf:"indexed_only" doc:"Only allow filtering on columns covered by an index (default policy)."`
	AllowColumns map[string][]string `koanf:"allow_columns" doc:"Per-table extra filterable columns, keyed by schema.table, overriding indexed_only."`
}

// Auth is the credential section: how a request proves who it is.
type Auth struct {
	Mode     auth.Mode `koanf:"mode" doc:"Credential source: 'jwt' (verify a bearer JWT), 'headers' (claims set by a trusted gateway) or 'none' (no credentials: every request is anonymous)."`
	JWT      JWT       `koanf:"jwt" doc:"JWT verification (jwt mode) and the key minting signs with."`
	Headers  Headers   `koanf:"headers" doc:"Gateway claim headers (headers mode)."`
	MintType string    `koanf:"mint_type" doc:"Schema-qualified composite type (e.g. public.jwt) minted into a signed JWT: any function returning it yields an HS256 token string built from the composite's fields (an exp field becomes the token expiry). Requires jwt.secret or jwt.secret_file; jwt.issuer/jwt.audience are embedded when set. Empty disables minting."`
}

// JWT is the auth.jwt section.
type JWT struct {
	Secret        string        `koanf:"secret" doc:"HMAC secret verifying HS256/384/512 tokens (ignored for verification when jwks_url is set) and signing minted tokens."`
	SecretFile    string        `koanf:"secret_file" doc:"File holding the HMAC secret (trailing newlines are stripped), instead of secret."`
	JWKSURL       string        `koanf:"jwks_url" doc:"JWKS endpoint for asymmetric JWT verification (RS256/384/512, ES256/384/512). Takes precedence over secret."`
	JWKSCacheTTL  time.Duration `koanf:"jwks_cache_ttl" doc:"How long fetched JWKS keys are cached; an unknown kid triggers an early refresh (key rotation) at most once a minute. After the TTL, keys refresh in the background and stay in use for up to one more TTL while the endpoint fails; failed fetches back off from 1s to 1m."`
	Issuer        string        `koanf:"issuer" doc:"Expected iss claim; empty skips the check."`
	Audience      string        `koanf:"audience" doc:"Expected aud claim; empty skips the check."`
	Leeway        time.Duration `koanf:"leeway" doc:"Clock skew tolerated when checking exp, nbf and iat."`
	RoleClaimPath string        `koanf:"role_claim_path" doc:"Path of the claim carrying the database role: dot-separated keys with optional array indexes and double-quoted keys, e.g. role, app_metadata.role, realm_access.roles[0] or \"https://example.com/role\". A missing claim falls back to roles.default; a claim that is not a non-empty string is rejected."`
}

// Headers is the auth.headers section.
type Headers struct {
	Prefix         string   `koanf:"prefix" doc:"Header prefix mapped to claims, e.g. X-Claim-. The rest of the name maps to one claim (User-Id is user_id); requests with a claim header containing an underscore or other punctuation, repeated, or folding onto another claim header are rejected. The gateway must strip every client-supplied header with this prefix, in any letter case."`
	Claims         []string `koanf:"claims" doc:"Claim names (e.g. user_id) accepted from claim headers; role_claim is always accepted. Requests carrying any other claim header are rejected. Empty accepts any claim."`
	RoleClaim      string   `koanf:"role_claim" doc:"Claim carrying the database role (role is sent as <prefix>Role). A missing claim falls back to roles.default."`
	TrustedProxies []string `koanf:"trusted_proxies" doc:"CIDR blocks (or single IPs) allowed to send claim headers; required in headers mode. Requests from other peers that carry claim headers are rejected."`
}

// Roles is the database role section: which role a request runs as.
type Roles struct {
	Switch    bool     `koanf:"switch" doc:"Run each request as its resolved role (SET LOCAL ROLE). Disabling runs every request as the connection role, with credentials still verified and claims still set; it logs a loud warning."`
	Anonymous string   `koanf:"anonymous" doc:"Role of requests without credentials (every request in auth mode none). Empty refuses requests without credentials with 401 (auth modes jwt and headers)."`
	Default   string   `koanf:"default" doc:"Role of authenticated requests without a role claim; empty falls back to anonymous, and a request that resolves to no role is refused."`
	Allowed   []string `koanf:"allowed" doc:"Roles a request may assume via its role claim. Empty allows any role (subject to database grants); default and anonymous are always allowed."`
}

// Claims is the section exposing request claims to SQL.
type Claims struct {
	Prefix      string `koanf:"prefix" doc:"Namespace of the per-claim settings: claim user_id is current_setting('jwt.claims.user_id', true) with the default (PostGraphile's). Empty sets no per-claim settings."`
	JSONSetting string `koanf:"json_setting" doc:"Setting that receives every claim as one JSON object ('{}' without credentials), e.g. request.jwt.claims (PostgREST's: current_setting('request.jwt.claims', true)::json->>'sub'). Empty sets none."`
}

// DefaultClaimsPrefix is the default claims.prefix, PostGraphile's
// namespace, so policies reading current_setting('jwt.claims.<claim>') port
// unchanged.
const DefaultClaimsPrefix = "jwt.claims"

// DefaultAuth is the auth section's default: jwt mode, a one-hour JWKS
// cache, the role in the top-level role claim, and claim headers named
// headerPrefix + claim.
func DefaultAuth(headerPrefix string) Auth {
	return Auth{
		Mode:    auth.ModeJWT,
		JWT:     JWT{JWKSCacheTTL: time.Hour, RoleClaimPath: "role"},
		Headers: Headers{Prefix: headerPrefix, RoleClaim: "role"},
	}
}

// DefaultRoles is the roles section's default: role switching on and no
// anonymous access.
func DefaultRoles() Roles {
	return Roles{Switch: true}
}

// DefaultClaims is the claims section's default: per-claim settings under
// DefaultClaimsPrefix and no JSON setting.
func DefaultClaims() Claims {
	return Claims{Prefix: DefaultClaimsPrefix}
}

// TX is the transaction policy shared by both products, which add their
// own keys in their own sections.
type TX struct {
	Isolation  string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
}

// Watch is the DDL watch (dev mode) section.
type Watch struct {
	Enabled      bool          `koanf:"enabled" doc:"Re-introspect and hot-swap the schema on DDL changes (dev only; refuses to combine with schema.cache_path)."`
	PollInterval time.Duration `koanf:"poll_interval" doc:"Poll interval used when event triggers cannot be installed (must be positive)."`
	Channel      string        `koanf:"channel" doc:"NOTIFY channel used by the DDL event trigger."`
}

// Errors is the error-detail policy.
type Errors struct {
	Detail string `koanf:"detail" doc:"'dev' exposes full PostgreSQL error detail in error responses; 'prod' sanitizes but passes constraint-violation and bad-input messages through; 'strict' hides every database error message (constraint and column names never leak)."`
}

// Plugins disables and configures plugins by name. S is the product's
// settings struct: one koanf-tagged field per configurable plugin, keyed by
// plugin name, each holding that plugin's typed settings.
type Plugins[S any] struct {
	Disabled []string `koanf:"disabled" doc:"Plugin names to disable."`
	Settings S        `koanf:"settings" doc:"Per-plugin settings, one section per plugin."`
}

// DisabledSet returns the disabled-plugin set for registry filtering.
func (p Plugins[S]) DisabledSet() map[string]bool {
	out := map[string]bool{}
	for _, n := range p.Disabled {
		out[n] = true
	}
	return out
}

// Log is the logging section.
type Log struct {
	Level  string `koanf:"level" doc:"Log level: debug, info, warn, error."`
	Format string `koanf:"format" doc:"Log format: text or json."`
}

// Metrics is the Prometheus endpoint section (see package metrics).
type Metrics struct {
	Enabled bool   `koanf:"enabled" doc:"Serve Prometheus metrics (HTTP requests, database operations, pool statistics, schema rebuilds)."`
	Path    string `koanf:"path" doc:"URL path of the metrics endpoint."`
	Addr    string `koanf:"addr" doc:"Separate listen address for the metrics endpoint, e.g. :9090, keeping it off the public listener; empty serves it on the main server address."`
}

// DefaultMetrics is the metrics section's default: disabled, at /metrics on
// the main listener.
func DefaultMetrics() Metrics {
	return Metrics{Path: "/metrics"}
}

// Contract is one entry of the contracts list: a named, audience-specific
// view of the API served under its own path (see package contract).
type Contract struct {
	Name    string          `koanf:"name" doc:"Contract name, matched against @contract and @behavior ±contract:<name> smart tags: lower-case letters, digits, _ and -."`
	Default contract.Policy `koanf:"default" doc:"Policy for tables and functions without a tag for this contract: 'include' (the default: the contract is everything not tagged out) or 'exclude' (only what is tagged in)."`
	Path    string          `koanf:"path" doc:"URL path the contract is served under; defaults to its name."`
}

// UnmarshalText makes a bare string a contract of that name with the
// default policy and path, so contracts can be listed by name in YAML
// (contracts: [public, partner]) and in environment variables
// (PDBQ_CONTRACTS=public,partner).
func (c *Contract) UnmarshalText(b []byte) error {
	*c = Contract{Name: string(b)}
	return nil
}

// URLPath returns the contract's path: Path when set, else base joined
// with the contract's name ("/" + name for an empty base).
func (c Contract) URLPath(base string) string {
	if c.Path != "" {
		return c.Path
	}
	return strings.TrimSuffix(base, "/") + "/" + c.Name
}

// Spec returns the catalog filter of the contract.
func (c Contract) Spec() contract.Contract {
	return contract.Contract{Name: c.Name, Default: c.Default}
}

// FindContract returns the contract named name, or false.
func FindContract(cs []Contract, name string) (Contract, bool) {
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return Contract{}, false
}
