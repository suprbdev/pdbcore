// Package confx is the configuration toolkit shared by the products: koanf
// loading with the YAML < env < changed-flags precedence, the env key
// mapper, reference-YAML generation from `koanf`+`doc` struct tags, and the
// config sections (with validation) that both products expose verbatim.
//
// Products own their root Config, DefaultConfig, Validate and any
// product-only sections (pdbq's Server/TX.PerRequest, pdbr's Server).
package confx

import "time"

// Database is the connection section.
type Database struct {
	URL              string        `koanf:"url" doc:"PostgreSQL connection URL, e.g. postgres://user:pass@host:5432/db. Also honours standard PG* env vars when empty."`
	MaxConns         int32         `koanf:"max_conns" doc:"Maximum pooled connections."`
	ConnectTimeout   time.Duration `koanf:"connect_timeout" doc:"Timeout for establishing a connection."`
	StatementTimeout time.Duration `koanf:"statement_timeout" doc:"Per-statement timeout applied to every request (0 disables)."`
}

// Schema selects what is introspected and how.
type Schema struct {
	Schemas   []string `koanf:"schemas" doc:"PostgreSQL schemas to expose."`
	CachePath string   `koanf:"cache_path" doc:"Boot from this schema cache file instead of introspecting (see pdbq schema dump)."`
	Functions bool     `koanf:"functions" doc:"Expose PostgreSQL functions as custom queries/mutations."`
}

// Filters is the filterable-column policy.
type Filters struct {
	IndexedOnly  bool                `koanf:"indexed_only" doc:"Only allow filtering on columns covered by an index (default policy)."`
	AllowColumns map[string][]string `koanf:"allow_columns" doc:"Per-table extra filterable columns, keyed by schema.table, overriding indexed_only."`
}

// RLS is the row-level-security / identity section.
type RLS struct {
	Enabled       bool     `koanf:"enabled" doc:"Run each request as a switched role with claims exposed via set_config (SET LOCAL). Disabling uses the privileged connection directly and logs a loud warning."`
	DefaultRole   string   `koanf:"default_role" doc:"Role assumed for authenticated requests without a role claim."`
	AnonymousRole string   `koanf:"anonymous_role" doc:"Role assumed for unauthenticated requests."`
	RoleClaim     string   `koanf:"role_claim" doc:"JWT claim (or header name in header mode) carrying the database role."`
	AllowedRoles  []string `koanf:"allowed_roles" doc:"Roles a request may assume via the role claim. Empty allows any role (subject to database grants); default_role and anonymous_role are always allowed."`
	ClaimsPrefix  string   `koanf:"claims_prefix" doc:"set_config namespace for request claims, e.g. pdbq.claims."`
	Auth          Auth     `koanf:"auth"`
}

// Auth is the claim source (nested under rls.auth).
type Auth struct {
	Mode           string        `koanf:"mode" doc:"Claim source: 'jwt', 'headers' (behind a trusted gateway), or 'none'."`
	JWTSecret      string        `koanf:"jwt_secret" doc:"HMAC secret for HS256/384/512 verification (jwt mode; ignored when jwks_url is set)."`
	JWKSURL        string        `koanf:"jwks_url" doc:"JWKS endpoint for asymmetric JWT verification (RS256/384/512, ES256/384/512). Takes precedence over jwt_secret."`
	JWKSCacheTTL   time.Duration `koanf:"jwks_cache_ttl" doc:"How long fetched JWKS keys are cached; an unknown kid triggers an early refresh (key rotation)."`
	JWTIssuer      string        `koanf:"jwt_issuer" doc:"Expected iss claim; empty skips the check."`
	JWTAudience    string        `koanf:"jwt_audience" doc:"Expected aud claim; empty skips the check."`
	HeaderPrefix   string        `koanf:"header_prefix" doc:"Header prefix mapped to claims in headers mode, e.g. X-Pdbq-Claim-."`
	TrustedProxies []string      `koanf:"trusted_proxies" doc:"CIDR blocks (or single IPs) allowed to supply claim headers in headers mode. Requests from other peers that carry claim headers are rejected. Empty trusts every peer and logs a startup warning."`
	JWTType        string        `koanf:"jwt_type" doc:"Schema-qualified composite type (e.g. public.jwt) minted into a signed JWT: any function returning it yields an HS256 token string built from the composite's fields (an exp field becomes the token expiry). Requires jwt_secret; jwt_issuer/jwt_audience are embedded when set. Empty disables minting."`
}

// TX is the transaction policy shared by both products. pdbq adds
// per_request in its own wrapper.
type TX struct {
	Mutations  bool   `koanf:"mutations" doc:"Wrap every mutation in a transaction."`
	Isolation  string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
}

// Watch is the DDL watch (dev mode) section.
type Watch struct {
	Enabled      bool          `koanf:"enabled" doc:"Re-introspect and hot-swap the schema on DDL changes (dev only; refuses to combine with schema.cache_path)."`
	PollInterval time.Duration `koanf:"poll_interval" doc:"Poll interval used when event triggers cannot be installed."`
	Channel      string        `koanf:"channel" doc:"NOTIFY channel used by the DDL event trigger."`
}

// Errors is the error-detail policy.
type Errors struct {
	Detail string `koanf:"detail" doc:"'dev' exposes full PostgreSQL error detail in GraphQL errors; 'prod' sanitizes but passes constraint-violation and bad-input messages through; 'strict' hides every database error message (constraint and column names never leak)."`
}

// Plugins disables and configures plugins by name.
type Plugins struct {
	Disabled []string                  `koanf:"disabled" doc:"Plugin names to disable."`
	Settings map[string]map[string]any `koanf:"settings" doc:"Per-plugin configuration, keyed by plugin name."`
}

// DisabledSet returns the disabled-plugin set for registry filtering.
func (p Plugins) DisabledSet() map[string]bool {
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
