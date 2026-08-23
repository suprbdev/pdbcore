package confx

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/suprbdev/pdbcore/auth"
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
// secret/JWKS presence, jwt_type shape, and the anonymous role.
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
