package confx

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/pgexec"
)

// InvalidConfig joins validation messages into pdbq's error shape, or nil
// when there are none.
func InvalidConfig(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
}

// ValidateErrors checks errors.detail.
func ValidateErrors(e Errors) []string {
	switch e.Detail {
	case "dev", "prod", "strict":
		return nil
	}
	return []string{fmt.Sprintf("errors.detail: %q is not 'dev', 'prod' or 'strict'", e.Detail)}
}

// ValidateRLS checks the rls section: auth mode, trusted proxies, JWT
// secret/JWKS presence, jwt_type shape, the headers-mode prefix and claim
// names (auth.ValidHeaderClaim), the anonymous role, the configured role
// names (pgexec.ValidRole) and claims_prefix (pgexec.ValidateClaimsPrefix).
func ValidateRLS(c RLS) []string {
	var errs []string
	switch c.Auth.Mode {
	case "jwt", "headers", "none":
	default:
		errs = append(errs, fmt.Sprintf("rls.auth.mode: %q is not 'jwt', 'headers' or 'none'", c.Auth.Mode))
	}
	for _, p := range c.Auth.TrustedProxies {
		if _, err := ParseTrustedProxy(p); err != nil {
			errs = append(errs, fmt.Sprintf("rls.auth.trusted_proxies: %q is not a valid IP or CIDR block", p))
		}
	}
	if c.Auth.Mode == "headers" {
		errs = append(errs, validateHeadersMode(c)...)
	}
	if c.Enabled && c.Auth.Mode == "jwt" && c.Auth.JWTSecret == "" && c.Auth.JWKSURL == "" {
		errs = append(errs, "rls.auth.jwt_secret or rls.auth.jwks_url is required when rls.enabled and auth mode is jwt")
	}
	if c.Auth.JWTType != "" && c.Auth.JWTSecret == "" {
		errs = append(errs, "rls.auth.jwt_type requires rls.auth.jwt_secret: minting signs with the HMAC secret (JWKS keys are verify-only)")
	}
	if jt := c.Auth.JWTType; jt != "" && (strings.Count(jt, ".") != 1 || strings.HasPrefix(jt, ".") || strings.HasSuffix(jt, ".")) {
		errs = append(errs, fmt.Sprintf("rls.auth.jwt_type: %q must be schema-qualified, e.g. public.jwt", jt))
	}
	if c.Enabled && c.AnonymousRole == "" {
		errs = append(errs, "rls.anonymous_role is required when rls.enabled: an empty role would run unauthenticated requests as the privileged connection role, bypassing RLS")
	}
	roles := []struct{ key, role string }{{"rls.anonymous_role", c.AnonymousRole}, {"rls.default_role", c.DefaultRole}}
	for _, r := range c.AllowedRoles {
		roles = append(roles, struct{ key, role string }{"rls.allowed_roles", r})
	}
	for _, r := range roles {
		if r.role != "" && !pgexec.ValidRole(r.role) {
			errs = append(errs, fmt.Sprintf("%s: %q is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role", r.key, r.role))
		}
	}
	if err := pgexec.ValidateClaimsPrefix(c.ClaimsPrefix); err != nil {
		errs = append(errs, "rls.claims_prefix: "+err.Error())
	}
	return errs
}

// ValidateTX checks transactions.isolation and transactions.max_retries.
func ValidateTX(t TX) []string {
	var errs []string
	switch t.Isolation {
	case "read_committed", "repeatable_read", "serializable":
	default:
		errs = append(errs, fmt.Sprintf("transactions.isolation: %q invalid", t.Isolation))
	}
	if t.MaxRetries < 0 {
		errs = append(errs, "transactions.max_retries must be >= 0")
	}
	return errs
}

// ValidateWatch checks the watch section against schema.cache_path and the
// channel name.
func ValidateWatch(w Watch, cachePath string) []string {
	var errs []string
	if w.Enabled && cachePath != "" {
		errs = append(errs, "watch.enabled cannot be combined with schema.cache_path")
	}
	if w.Enabled && !ValidChannelName(w.Channel) {
		errs = append(errs, fmt.Sprintf("watch.channel: %q invalid: only letters, digits and underscores are allowed", w.Channel))
	}
	if w.Enabled && len(w.Channel) > 63 {
		errs = append(errs, fmt.Sprintf("watch.channel: %q is longer than 63 bytes", w.Channel))
	}
	return errs
}

// ValidateMetrics checks the metrics section: path must start with "/" and
// not be "/" itself, and addr, when set, must be a host:port listen address
// other than serverAddr (the main listener; "" skips that comparison).
// Path collisions with product routes are the product's check.
func ValidateMetrics(m Metrics, serverAddr string) []string {
	var errs []string
	if !strings.HasPrefix(m.Path, "/") || m.Path == "/" {
		errs = append(errs, fmt.Sprintf("metrics.path: %q must start with / and name a path other than /", m.Path))
	}
	if strings.ContainsAny(m.Path, "?#") {
		errs = append(errs, fmt.Sprintf("metrics.path: %q must not contain a query or fragment", m.Path))
	}
	if m.Addr != "" {
		if _, port, err := net.SplitHostPort(m.Addr); err != nil || port == "" {
			errs = append(errs, fmt.Sprintf("metrics.addr: %q is not a host:port listen address, e.g. :9090", m.Addr))
		} else if serverAddr != "" && m.Addr == serverAddr {
			errs = append(errs, fmt.Sprintf("metrics.addr: %q is the main server address; leave it empty to serve metrics there", m.Addr))
		}
	}
	return errs
}

// ValidateSchema checks schema.schemas.
func ValidateSchema(s Schema) []string {
	if len(s.Schemas) == 0 {
		return []string{"schema.schemas must list at least one schema"}
	}
	return nil
}

// ValidateLog checks log.level.
func ValidateLog(l Log) []string {
	switch l.Level {
	case "debug", "info", "warn", "error":
		return nil
	}
	return []string{fmt.Sprintf("log.level: %q invalid", l.Level)}
}

func validateHeadersMode(c RLS) []string {
	var errs []string
	if p := c.Auth.HeaderPrefix; !validHeaderPrefix(p) {
		errs = append(errs, fmt.Sprintf("rls.auth.header_prefix: %q must be letters, digits and hyphens ending in a hyphen, e.g. X-Pdbq-Claim-", p))
	}
	if c.RoleClaim != "" && !auth.ValidHeaderClaim(c.RoleClaim) {
		errs = append(errs, fmt.Sprintf("rls.role_claim: %q cannot be sent as a claim header: use lower-case letters and digits joined by single underscores", c.RoleClaim))
	}
	for _, claim := range c.Auth.HeaderClaims {
		if !auth.ValidHeaderClaim(claim) {
			errs = append(errs, fmt.Sprintf("rls.auth.header_claims: %q cannot be sent as a claim header: use lower-case letters and digits joined by single underscores", claim))
		}
	}
	return errs
}

func validHeaderPrefix(p string) bool {
	if len(p) < 2 || p[0] == '-' || p[len(p)-1] != '-' {
		return false
	}
	for _, r := range p {
		ok := r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// ValidChannelName restricts the watch NOTIFY channel to [a-zA-Z0-9_]; the
// value is interpolated into trigger DDL (see package watch) where broader
// characters cannot be safely escaped.
func ValidChannelName(s string) bool {
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}

// AuthConfig converts the rls section into an auth.Config. Invalid
// trusted_proxies entries are dropped (ValidateRLS rejects them; skipping
// here only shrinks the trusted set, never widens it); nil when none were
// configured, which auth treats as "trust every peer".
func AuthConfig(c RLS) auth.Config {
	cfg := auth.Config{
		Enabled:       c.Enabled,
		Mode:          c.Auth.Mode,
		JWTSecret:     c.Auth.JWTSecret,
		JWKSURL:       c.Auth.JWKSURL,
		JWKSCacheTTL:  c.Auth.JWKSCacheTTL,
		JWTIssuer:     c.Auth.JWTIssuer,
		JWTAudience:   c.Auth.JWTAudience,
		HeaderPrefix:  c.Auth.HeaderPrefix,
		HeaderClaims:  c.Auth.HeaderClaims,
		RoleClaim:     c.RoleClaim,
		DefaultRole:   c.DefaultRole,
		AnonymousRole: c.AnonymousRole,
		AllowedRoles:  c.AllowedRoles,
	}
	if len(c.Auth.TrustedProxies) > 0 {
		cfg.TrustedProxies = make([]netip.Prefix, 0, len(c.Auth.TrustedProxies))
		for _, p := range c.Auth.TrustedProxies {
			if pfx, err := ParseTrustedProxy(p); err == nil {
				cfg.TrustedProxies = append(cfg.TrustedProxies, pfx)
			}
		}
	}
	return cfg
}
