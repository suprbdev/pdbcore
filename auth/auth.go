// Package auth turns an HTTP request into verified claims and a database
// role: a JWT (HS* via shared secret, RS*/ES* via a cached JWKS endpoint),
// trusted-gateway claim headers, or nothing. It is transport-neutral apart
// from reading *http.Request headers and RemoteAddr.
package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Mode is the credential source of an Authenticator.
type Mode string

const (
	// ModeJWT verifies a bearer JWT: HS* with a shared secret, RS*/ES*
	// with keys from a JWKS endpoint.
	ModeJWT Mode = "jwt"
	// ModeHeaders reads claims from headers set by a trusted gateway.
	ModeHeaders Mode = "headers"
	// ModeNone accepts no credentials: every request is anonymous.
	ModeNone Mode = "none"
)

// ParseMode returns the Mode spelled s, or an error naming the valid modes.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeJWT, ModeHeaders, ModeNone:
		return m, nil
	}
	return "", fmt.Errorf("unknown auth mode %q: use jwt, headers or none", s)
}

// ErrInvalidToken marks a present-but-unusable credential: malformed
// Authorization header, bad signature, expired, wrong issuer/audience, JWKS
// miss or outage, a role claim that is not a string, claim headers from an
// untrusted peer, or malformed, repeated, conflicting or unlisted claim
// headers. Products map it to 401.
var ErrInvalidToken = errors.New("invalid token")

// ErrAuthRequired marks a request without credentials while anonymous
// access is off (Config.AnonymousRole is empty). Products map it to 401.
var ErrAuthRequired = errors.New("authentication required")

// ErrRoleNotAllowed marks a valid credential whose role claim is outside the
// AllowedRoles policy, or that resolves to no role at all. Products map it
// to 403 (or 401 for pdbq parity).
var ErrRoleNotAllowed = errors.New("role not allowed")

// authError carries the sentinel for errors.Is while keeping the message
// text free of the sentinel's own wording (products print it verbatim).
type authError struct {
	kind  error
	cause error
	msg   string
}

func (e *authError) Error() string { return e.msg }

func (e *authError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.kind}
	}
	return []error{e.kind, e.cause}
}

func invalidToken(msg string, cause error) error {
	return &authError{kind: ErrInvalidToken, cause: cause, msg: msg}
}

// Config is the credential and role configuration; products fill it from
// their auth and roles config sections (see confx.AuthConfig).
type Config struct {
	// Mode selects the credential source; New rejects any other value.
	Mode Mode
	// JWTSecret verifies HS256/384/512 tokens; ignored when JWKSURL is set.
	JWTSecret string
	// JWKSURL enables asymmetric verification (RS*/ES*) with a key cache.
	JWKSURL      string
	JWKSCacheTTL time.Duration
	// JWTIssuer / JWTAudience are checked when non-empty.
	JWTIssuer   string
	JWTAudience string
	// Leeway is the clock skew tolerated on exp, nbf and iat.
	Leeway time.Duration
	// RoleClaimPath locates the role in a JWT's claims (see
	// ValidateClaimPath); empty means "role".
	RoleClaimPath string
	// HeaderPrefix is the claim-header prefix in headers mode
	// (pdbq: X-Pdbq-Claim-, pdbr: X-Pdbr-Claim-), matched
	// case-insensitively. The rest of the header name must be
	// alphanumeric words joined by single hyphens; the claim key is that
	// suffix lower-cased with hyphens turned into underscores
	// (X-Pdbq-Claim-User-Id is user_id), so every claim has exactly one
	// header spelling. A request is rejected with ErrInvalidToken when a
	// prefixed header has any other shape (for example an underscore), is
	// repeated, or maps to the same claim as another header.
	//
	// The gateway in front must remove every client-supplied header that
	// starts with the prefix, in any letter case, before adding its own.
	HeaderPrefix string
	// HeaderClaims, when non-empty, lists the claim keys (user_id, not
	// User-Id) that claim headers may carry; HeaderRoleClaim is always
	// allowed. A request with any other claim header is rejected with
	// ErrInvalidToken.
	HeaderClaims []string
	// HeaderRoleClaim is the claim carrying the role in headers mode;
	// empty means "role".
	HeaderRoleClaim string
	// TrustedProxies are the peers allowed to send claim headers; headers
	// mode requires at least one.
	TrustedProxies []netip.Prefix
	// DefaultRole is the role of an authenticated request without a role
	// claim.
	DefaultRole string
	// AnonymousRole is the role of a request without credentials (and of
	// every request in ModeNone, and of an authenticated request when
	// DefaultRole is empty too). Empty refuses requests without
	// credentials with ErrAuthRequired.
	AnonymousRole string
	// AllowedRoles restricts claim-supplied roles; empty allows any.
	AllowedRoles []string
	// SkipRoleSwitch makes Authenticate return "" as the role of every
	// accepted request, so it runs as the connection role. Credentials are
	// still verified, roles resolved and AllowedRoles enforced.
	SkipRoleSwitch bool
	// Logger receives JWKS fetch failures, with the endpoint URL and cause
	// that clients never see; nil uses slog.Default().
	Logger *slog.Logger
}

// Authenticator turns an HTTP request into (claims, role) per the configured
// credential source: verified JWT, trusted gateway headers, or none.
type Authenticator struct {
	cfg Config
	// jwks verifies asymmetric tokens when JWKSURL is set.
	jwks *jwksCache
	// rolePath locates the role in JWT claims.
	rolePath claimPath
	// headerClaims is the HeaderClaims allowlist plus HeaderRoleClaim; nil
	// allows every claim.
	headerClaims map[string]bool
}

// New builds an Authenticator. It returns an error for an unknown mode, a
// JWT mode without a secret or JWKS URL, a headers mode without a header
// prefix or trusted proxies, an invalid role claim path or header role
// claim, and a negative leeway.
func New(cfg Config) (*Authenticator, error) {
	if _, err := ParseMode(string(cfg.Mode)); err != nil {
		return nil, err
	}
	if cfg.Leeway < 0 {
		return nil, fmt.Errorf("auth: negative leeway %s", cfg.Leeway)
	}
	if cfg.RoleClaimPath == "" {
		cfg.RoleClaimPath = "role"
	}
	if cfg.HeaderRoleClaim == "" {
		cfg.HeaderRoleClaim = "role"
	}
	a := &Authenticator{cfg: cfg}
	switch cfg.Mode {
	case ModeJWT:
		if cfg.JWTSecret == "" && cfg.JWKSURL == "" {
			return nil, errors.New("auth: jwt mode needs a JWT secret or a JWKS URL")
		}
		p, err := parseClaimPath(cfg.RoleClaimPath)
		if err != nil {
			return nil, err
		}
		a.rolePath = p
		if cfg.JWKSURL != "" {
			log := cfg.Logger
			if log == nil {
				log = slog.Default()
			}
			a.jwks = newJWKSCache(cfg.JWKSURL, cfg.JWKSCacheTTL, log)
		}
	case ModeHeaders:
		if cfg.HeaderPrefix == "" {
			return nil, errors.New("auth: headers mode needs a claim header prefix")
		}
		if len(cfg.TrustedProxies) == 0 {
			return nil, errors.New("auth: headers mode needs at least one trusted proxy")
		}
		if !ValidHeaderClaim(cfg.HeaderRoleClaim) {
			return nil, fmt.Errorf("auth: role claim %q cannot be sent as a claim header", cfg.HeaderRoleClaim)
		}
		if len(cfg.HeaderClaims) > 0 {
			a.headerClaims = make(map[string]bool, len(cfg.HeaderClaims)+1)
			for _, c := range cfg.HeaderClaims {
				a.headerClaims[c] = true
			}
			a.headerClaims[cfg.HeaderRoleClaim] = true
		}
	}
	return a, nil
}

// Mode returns the credential source.
func (a *Authenticator) Mode() Mode { return a.cfg.Mode }

// Authenticate never fails open: a present-but-invalid credential is an
// error, and an absent one yields the anonymous role, or ErrAuthRequired
// when there is none. Errors wrap ErrInvalidToken, ErrAuthRequired or
// ErrRoleNotAllowed. The role is "" with SkipRoleSwitch.
func (a *Authenticator) Authenticate(r *http.Request) (map[string]any, string, error) {
	switch a.cfg.Mode {
	case ModeJWT:
		return a.fromJWT(r)
	case ModeHeaders:
		return a.fromHeaders(r)
	}
	return map[string]any{}, a.switchTo(a.cfg.AnonymousRole), nil
}

// Challenge returns the WWW-Authenticate value (RFC 6750) of the response
// to an Authenticate error: Bearer for ErrAuthRequired, with
// error="invalid_token" for ErrInvalidToken and error="insufficient_scope"
// for ErrRoleNotAllowed; "" for any other error.
func Challenge(err error) string {
	switch {
	case errors.Is(err, ErrAuthRequired):
		return "Bearer"
	case errors.Is(err, ErrInvalidToken):
		return `Bearer error="invalid_token"`
	case errors.Is(err, ErrRoleNotAllowed):
		return `Bearer error="insufficient_scope"`
	}
	return ""
}

// anonymous answers a request that carries no credentials.
func (a *Authenticator) anonymous() (map[string]any, string, error) {
	if a.cfg.AnonymousRole == "" {
		return nil, "", &authError{kind: ErrAuthRequired, msg: "authentication required"}
	}
	return map[string]any{}, a.switchTo(a.cfg.AnonymousRole), nil
}

func (a *Authenticator) switchTo(role string) string {
	if a.cfg.SkipRoleSwitch {
		return ""
	}
	return role
}

// bearerToken extracts the token of an "Authorization: Bearer <token>"
// header; the scheme is case-insensitive (RFC 7235).
func bearerToken(authz string) (string, bool) {
	scheme, token, ok := strings.Cut(authz, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimLeft(token, " ")
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func (a *Authenticator) fromJWT(r *http.Request) (map[string]any, string, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		return a.anonymous()
	}
	tokenStr, ok := bearerToken(authz)
	if !ok {
		return nil, "", invalidToken("malformed Authorization header", nil)
	}
	claims := jwt.MapClaims{}
	// WithExpirationRequired: a token without an exp claim would otherwise
	// never expire — stolen tokens would stay valid until secret rotation.
	methods := []string{"HS256", "HS384", "HS512"}
	keyfunc := func(t *jwt.Token) (any, error) {
		return []byte(a.cfg.JWTSecret), nil
	}
	if a.jwks != nil {
		// Asymmetric verification: the algorithm allowlist swaps entirely so
		// an attacker cannot downgrade to HMAC-with-public-key.
		methods = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}
		keyfunc = func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			return a.jwks.key(r.Context(), kid)
		}
	}
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods(methods),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(a.cfg.Leeway),
	}
	if a.cfg.JWTIssuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(a.cfg.JWTIssuer))
	}
	if a.cfg.JWTAudience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(a.cfg.JWTAudience))
	}
	_, err := jwt.ParseWithClaims(tokenStr, claims, keyfunc, parserOpts...)
	if err != nil {
		if errors.Is(err, errJWKS) {
			return nil, "", invalidToken("invalid token", err)
		}
		return nil, "", invalidToken("invalid token: "+err.Error(), err)
	}
	role, found, err := a.jwtRole(claims)
	if err != nil {
		return nil, "", err
	}
	return a.resolve(map[string]any(claims), role, found)
}

func (a *Authenticator) jwtRole(claims map[string]any) (string, bool, error) {
	v, ok := a.rolePath.lookup(claims)
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false, invalidToken("invalid token: role claim is not a non-empty string", nil)
	}
	return s, true, nil
}

func (a *Authenticator) fromHeaders(r *http.Request) (map[string]any, string, error) {
	prefix := a.cfg.HeaderPrefix
	present := false
	for name := range r.Header {
		if hasPrefixFold(name, prefix) {
			present = true
			break
		}
	}
	if !present {
		return a.anonymous()
	}
	if !a.peerTrusted(r.RemoteAddr) {
		return nil, "", invalidToken("claim headers from untrusted peer", nil)
	}
	claims := map[string]any{}
	for name, vals := range r.Header {
		if !hasPrefixFold(name, prefix) {
			continue
		}
		key, ok := headerClaimKey(name[len(prefix):])
		if !ok {
			return nil, "", invalidToken("malformed claim header", nil)
		}
		if len(vals) != 1 {
			return nil, "", invalidToken("repeated claim header", nil)
		}
		if _, dup := claims[key]; dup {
			return nil, "", invalidToken("conflicting claim headers", nil)
		}
		if a.headerClaims != nil && !a.headerClaims[key] {
			return nil, "", invalidToken("claim header not allowed", nil)
		}
		claims[key] = vals[0]
	}
	role, found := claims[a.cfg.HeaderRoleClaim].(string)
	if found && role == "" {
		return nil, "", invalidToken("empty role claim header", nil)
	}
	return a.resolve(claims, role, found)
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// headerClaimKey maps a claim-header suffix to its claim key. The suffix
// must be one or more ASCII alphanumeric words joined by single hyphens;
// the key is the suffix lower-cased with hyphens replaced by underscores.
// Restricting the suffix this way makes the mapping injective up to letter
// case, which HTTP header names do not carry.
func headerClaimKey(suffix string) (string, bool) {
	if suffix == "" {
		return "", false
	}
	b := make([]byte, len(suffix))
	for i := 0; i < len(suffix); i++ {
		c := suffix[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b[i] = c
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		case c == '-' && i > 0 && i < len(suffix)-1 && suffix[i-1] != '-':
			b[i] = '_'
		default:
			return "", false
		}
	}
	return string(b), true
}

// ValidHeaderClaim reports whether claim can be carried by a claim header:
// lower-case ASCII alphanumeric words joined by single underscores, the
// exact image of the header-name mapping described on Config.HeaderPrefix.
func ValidHeaderClaim(claim string) bool {
	k, ok := headerClaimKey(strings.ReplaceAll(claim, "_", "-"))
	return ok && k == claim
}

// HeaderClaimName returns the header that carries claim in headers mode,
// e.g. ("X-Pdbq-Claim-", "user_id") is X-Pdbq-Claim-User-Id. The result is
// only meaningful when ValidHeaderClaim(claim) holds.
func HeaderClaimName(prefix, claim string) string {
	return http.CanonicalHeaderKey(prefix + strings.ReplaceAll(claim, "_", "-"))
}

// peerTrusted reports whether the direct peer may supply claim headers: its
// address lies in a TrustedProxies prefix. An unparseable RemoteAddr fails
// closed.
func (a *Authenticator) peerTrusted(remoteAddr string) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false
	}
	addr := ap.Addr().Unmap()
	for _, pfx := range a.cfg.TrustedProxies {
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// resolve picks the request role: the claimed role when found, else
// DefaultRole, else AnonymousRole. A claimed role must pass AllowedRoles,
// and a request that resolves to no role is refused unless SkipRoleSwitch
// is set (it would otherwise run as the connection role).
func (a *Authenticator) resolve(claims map[string]any, role string, fromClaim bool) (map[string]any, string, error) {
	if !fromClaim {
		role = a.cfg.DefaultRole
		if role == "" {
			role = a.cfg.AnonymousRole
		}
	}
	if fromClaim && !a.roleAllowed(role) {
		return nil, "", &authError{kind: ErrRoleNotAllowed, msg: fmt.Sprintf("role %q is not allowed", role)}
	}
	if role == "" && !a.cfg.SkipRoleSwitch {
		return nil, "", &authError{kind: ErrRoleNotAllowed, msg: "credential carries no role and no default role is configured"}
	}
	return claims, a.switchTo(role), nil
}

// roleAllowed applies AllowedRoles: empty list allows everything; otherwise
// the role must be listed, or be the configured default/anonymous role (the
// operator chose those explicitly).
func (a *Authenticator) roleAllowed(role string) bool {
	if len(a.cfg.AllowedRoles) == 0 || role == a.cfg.DefaultRole || role == a.cfg.AnonymousRole {
		return true
	}
	for _, r := range a.cfg.AllowedRoles {
		if r == role {
			return true
		}
	}
	return false
}
