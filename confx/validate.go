package confx

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/contract"
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

// ValidateIdentity checks the auth, roles and claims sections together:
// the auth mode; in jwt mode a secret, secret file or JWKS URL (a secret
// file must be readable), the role claim path and a non-negative leeway;
// in headers mode the prefix, the claim names (auth.ValidHeaderClaim) and
// at least one trusted proxy; every trusted_proxies entry; mint_type's
// shape and secret; the role names (pgexec.ValidRole), and an anonymous
// role in mode none while roles switch; the claims prefix
// (pgexec.ValidateClaimsPrefix) and JSON setting
// (pgexec.ValidateClaimsSetting), which must not lie under the prefix.
func ValidateIdentity(a Auth, r Roles, c Claims) []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if _, err := auth.ParseMode(string(a.Mode)); err != nil {
		add("auth.mode: %q is not 'jwt', 'headers' or 'none'", a.Mode)
	}
	j := a.JWT
	if j.Secret != "" && j.SecretFile != "" {
		add("auth.jwt.secret and auth.jwt.secret_file are mutually exclusive")
	}
	if j.SecretFile != "" {
		if _, err := j.ResolveSecret(); err != nil {
			add("auth.jwt.secret_file: %v", err)
		}
	}
	if j.JWKSURL != "" {
		if u, err := url.Parse(j.JWKSURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			add("auth.jwt.jwks_url: %q is not an http(s) URL", j.JWKSURL)
		}
	}
	if j.Leeway < 0 {
		add("auth.jwt.leeway must be >= 0")
	}
	if a.Mode == auth.ModeJWT {
		if j.Secret == "" && j.SecretFile == "" && j.JWKSURL == "" {
			add("auth.jwt.secret, auth.jwt.secret_file or auth.jwt.jwks_url is required in auth mode jwt (use auth.mode: none to accept no credentials)")
		}
		if err := auth.ValidateClaimPath(j.RoleClaimPath); err != nil {
			add("auth.jwt.role_claim_path: %v", err)
		}
	}
	for _, p := range a.Headers.TrustedProxies {
		if _, err := ParseTrustedProxy(p); err != nil {
			add("auth.headers.trusted_proxies: %q is not a valid IP or CIDR block", p)
		}
	}
	if a.Mode == auth.ModeHeaders {
		errs = append(errs, validateHeadersMode(a.Headers)...)
	}
	if a.MintType != "" && j.Secret == "" && j.SecretFile == "" {
		add("auth.mint_type requires auth.jwt.secret or auth.jwt.secret_file: minting signs with the HMAC secret (JWKS keys are verify-only)")
	}
	if mt := a.MintType; mt != "" && (strings.Count(mt, ".") != 1 || strings.HasPrefix(mt, ".") || strings.HasSuffix(mt, ".")) {
		add("auth.mint_type: %q must be schema-qualified, e.g. public.jwt", mt)
	}
	if a.Mode == auth.ModeNone && r.Switch && r.Anonymous == "" {
		add("roles.anonymous is required in auth mode none while roles.switch is on: every request runs as the anonymous role (set roles.switch: false to run as the connection role)")
	}
	roles := []struct{ key, role string }{{"roles.anonymous", r.Anonymous}, {"roles.default", r.Default}}
	for _, ar := range r.Allowed {
		roles = append(roles, struct{ key, role string }{"roles.allowed", ar})
	}
	for _, kr := range roles {
		if kr.role != "" && !pgexec.ValidRole(kr.role) {
			add("%s: %q is not a role requests may assume: use letters, digits and underscores, not none, public or a pg_ role", kr.key, kr.role)
		}
	}
	if err := pgexec.ValidateClaimsPrefix(c.Prefix); err != nil {
		add("claims.prefix: %v", err)
	}
	if c.JSONSetting != "" {
		if err := pgexec.ValidateClaimsSetting(c.JSONSetting); err != nil {
			add("claims.json_setting: %v", err)
		} else if c.Prefix != "" && strings.HasPrefix(strings.ToLower(c.JSONSetting), strings.ToLower(c.Prefix)+".") {
			add("claims.json_setting: %q lies under claims.prefix %q, where a claim of that name would overwrite it", c.JSONSetting, c.Prefix)
		}
	}
	return errs
}

// IdentityWarnings returns the startup warnings for a valid identity
// configuration that is legal but risky: role switching off, and any role
// claim accepted in jwt or headers mode.
func IdentityWarnings(a Auth, r Roles) []string {
	var out []string
	if !r.Switch {
		out = append(out, "roles.switch is off: every request runs as the connection role, so row-level security policies for request roles do not apply")
	}
	if r.Switch && a.Mode != auth.ModeNone && len(r.Allowed) == 0 {
		out = append(out, "roles.allowed is empty: a valid credential may assume any role the connection role can switch to; list the roles requests may use")
	}
	return out
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

// ValidateContracts checks the contracts list: every name is a valid
// contract name and unique, every default is empty, include or exclude,
// and every path (see [Contract.URLPath], resolved against base) is a
// clean absolute URL path, unique, and neither nested in another
// contract's path nor containing one, so a request URL selects one
// contract. With rootShared, a contract served at "/" may sit next to
// others (pdbq's contracts own fixed endpoints below their path); without
// it "/" contains every other path.
func ValidateContracts(cs []Contract, base string, rootShared bool) []string {
	var errs []string
	names := map[string]bool{}
	type seen struct{ path, name string }
	var paths []seen
	for i, c := range cs {
		key := fmt.Sprintf("contracts[%d]", i)
		if !validContractName(c.Name) {
			errs = append(errs, fmt.Sprintf("%s.name: %q must be 1-63 lower-case letters, digits, _ or -, starting with a letter", key, c.Name))
		} else if names[c.Name] {
			errs = append(errs, fmt.Sprintf("%s.name: %q is used by another contract", key, c.Name))
		}
		names[c.Name] = true
		switch contract.Policy(c.Default) {
		case "", contract.Include, contract.Exclude:
		default:
			errs = append(errs, fmt.Sprintf("%s.default: %q is not 'include' or 'exclude'", key, c.Default))
		}
		p := c.URLPath(base)
		if !validContractPath(p) {
			errs = append(errs, fmt.Sprintf("%s.path: %q must be / or start with / and name path segments of letters, digits, _ . ~ or -, without a trailing /", key, p))
			continue
		}
		for _, o := range paths {
			if o.path == p {
				errs = append(errs, fmt.Sprintf("%s.path: %q is also the path of contract %q", key, p, o.name))
			} else if root := p == "/" || o.path == "/"; (!root || !rootShared) && (nestedPath(p, o.path) || nestedPath(o.path, p)) {
				errs = append(errs, fmt.Sprintf("%s.path: %q overlaps the path %q of contract %q", key, p, o.path, o.name))
			}
		}
		paths = append(paths, seen{p, c.Name})
	}
	return errs
}

func validContractName(s string) bool {
	if s == "" || len(s) > 63 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		ok := r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func validContractPath(p string) bool {
	if p == "/" {
		return true
	}
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			ok := r == '_' || r == '-' || r == '.' || r == '~' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				return false
			}
		}
	}
	return true
}

// nestedPath reports whether path p lies below parent ("/" contains
// everything).
func nestedPath(p, parent string) bool {
	return parent == "/" || strings.HasPrefix(p, parent+"/")
}

// ValidateSchema checks schema.schemas.
func ValidateSchema(s Schema) []string {
	if len(s.Schemas) == 0 {
		return []string{"schema.schemas must list at least one schema"}
	}
	return nil
}

// ValidateLog checks log.level and log.format.
func ValidateLog(l Log) []string {
	var errs []string
	switch l.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("log.level: %q is not 'debug', 'info', 'warn' or 'error'", l.Level))
	}
	switch l.Format {
	case "text", "json":
	default:
		errs = append(errs, fmt.Sprintf("log.format: %q is not 'text' or 'json'", l.Format))
	}
	return errs
}

// ValidatePluginNames checks that every plugins.disabled entry names a
// plugin in known, suggesting the closest name for a typo.
func ValidatePluginNames(disabled, known []string) []string {
	var errs []string
	set := make(map[string]bool, len(known))
	for _, k := range known {
		set[k] = true
	}
	for _, d := range disabled {
		if set[d] {
			continue
		}
		msg := fmt.Sprintf("plugins.disabled: unknown plugin %q", d)
		if s := closest(d, known); s != "" {
			msg += fmt.Sprintf(", did you mean %q?", s)
		}
		errs = append(errs, msg)
	}
	return errs
}

func validateHeadersMode(h Headers) []string {
	var errs []string
	if !validHeaderPrefix(h.Prefix) {
		errs = append(errs, fmt.Sprintf("auth.headers.prefix: %q must be letters, digits and hyphens ending in a hyphen, e.g. X-Claim-", h.Prefix))
	}
	if !auth.ValidHeaderClaim(h.RoleClaim) {
		errs = append(errs, fmt.Sprintf("auth.headers.role_claim: %q cannot be sent as a claim header: use lower-case letters and digits joined by single underscores", h.RoleClaim))
	}
	for _, claim := range h.Claims {
		if !auth.ValidHeaderClaim(claim) {
			errs = append(errs, fmt.Sprintf("auth.headers.claims: %q cannot be sent as a claim header: use lower-case letters and digits joined by single underscores", claim))
		}
	}
	if len(h.TrustedProxies) == 0 {
		errs = append(errs, "auth.headers.trusted_proxies is required in auth mode headers: list the gateway addresses allowed to send claim headers")
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

// ResolveSecret returns the HMAC secret: Secret, or the contents of
// SecretFile with trailing newlines stripped. A secret file that cannot be
// read or is empty is an error.
func (j JWT) ResolveSecret() (string, error) {
	if j.SecretFile == "" {
		return j.Secret, nil
	}
	b, err := os.ReadFile(j.SecretFile)
	if err != nil {
		return "", err
	}
	s := strings.TrimRight(string(b), "\r\n")
	if s == "" {
		return "", fmt.Errorf("%s is empty", j.SecretFile)
	}
	return s, nil
}

// AuthConfig converts the auth and roles sections into an auth.Config,
// reading the secret file. Invalid trusted_proxies entries are dropped
// (ValidateIdentity rejects them; skipping here only shrinks the trusted
// set, never widens it).
func AuthConfig(a Auth, r Roles) (auth.Config, error) {
	secret, err := a.JWT.ResolveSecret()
	if err != nil {
		return auth.Config{}, fmt.Errorf("auth.jwt.secret_file: %w", err)
	}
	cfg := auth.Config{
		Mode:            a.Mode,
		JWTSecret:       secret,
		JWKSURL:         a.JWT.JWKSURL,
		JWKSCacheTTL:    a.JWT.JWKSCacheTTL,
		JWTIssuer:       a.JWT.Issuer,
		JWTAudience:     a.JWT.Audience,
		Leeway:          a.JWT.Leeway,
		RoleClaimPath:   a.JWT.RoleClaimPath,
		HeaderPrefix:    a.Headers.Prefix,
		HeaderClaims:    a.Headers.Claims,
		HeaderRoleClaim: a.Headers.RoleClaim,
		DefaultRole:     r.Default,
		AnonymousRole:   r.Anonymous,
		AllowedRoles:    r.Allowed,
		SkipRoleSwitch:  !r.Switch,
	}
	for _, p := range a.Headers.TrustedProxies {
		if pfx, err := ParseTrustedProxy(p); err == nil {
			cfg.TrustedProxies = append(cfg.TrustedProxies, pfx)
		}
	}
	return cfg, nil
}
