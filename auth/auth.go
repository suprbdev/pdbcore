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

// ErrInvalidToken marks a present-but-unusable credential: malformed
// Authorization header, bad signature, expired, wrong issuer/audience, JWKS
// miss or outage, claim headers from an untrusted peer, or malformed,
// repeated, conflicting or unlisted claim headers. Products map it to 401.
var ErrInvalidToken = errors.New("invalid token")

// ErrRoleNotAllowed marks a valid credential whose role claim is outside the
// AllowedRoles policy. Products map it to 403 (or 401 for pdbq parity).
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

// Config is the claim-source configuration; products fill it from their own
// config sections (see confx.RLS / confx.Auth).
type Config struct {
	// Enabled false makes Authenticate a no-op returning no claims and no
	// role (the product runs as the connection role).
	Enabled bool
	// Mode is "jwt", "headers", or "none".
	Mode string
	// JWTSecret verifies HS256/384/512 tokens; ignored when JWKSURL is set.
	JWTSecret string
	// JWKSURL enables asymmetric verification (RS*/ES*) with a key cache.
	JWKSURL      string
	JWKSCacheTTL time.Duration
	// JWTIssuer / JWTAudience are checked when non-empty.
	JWTIssuer   string
	JWTAudience string
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
	// User-Id) that claim headers may carry; RoleClaim is always allowed.
	// A request with any other claim header is rejected with
	// ErrInvalidToken.
	HeaderClaims []string
	// TrustedProxies gates claim headers by peer address; nil trusts every
	// peer, an empty non-nil slice trusts none.
	TrustedProxies []netip.Prefix
	// RoleClaim names the claim carrying the database role.
	RoleClaim     string
	DefaultRole   string
	AnonymousRole string
	// AllowedRoles restricts claim-supplied roles; empty allows any.
	AllowedRoles []string
	// Logger receives JWKS fetch failures, with the endpoint URL and cause
	// that clients never see; nil uses slog.Default().
	Logger *slog.Logger
}

// Authenticator turns an HTTP request into (claims, role) per the configured
// claim source: verified JWT, trusted gateway headers, or none.
type Authenticator struct {
	cfg Config
	// jwks verifies asymmetric tokens when JWKSURL is set.
	jwks *jwksCache
	// headerClaims is the HeaderClaims allowlist plus RoleClaim; nil
	// allows every claim.
	headerClaims map[string]bool
}

// New builds an Authenticator. Invalid TrustedProxies entries cannot occur
// (they are typed); a nil slice means every peer is trusted.
func New(cfg Config) *Authenticator {
	a := &Authenticator{cfg: cfg}
	if cfg.JWKSURL != "" {
		log := cfg.Logger
		if log == nil {
			log = slog.Default()
		}
		a.jwks = newJWKSCache(cfg.JWKSURL, cfg.JWKSCacheTTL, log)
	}
	if len(cfg.HeaderClaims) > 0 {
		a.headerClaims = make(map[string]bool, len(cfg.HeaderClaims)+1)
		for _, c := range cfg.HeaderClaims {
			a.headerClaims[c] = true
		}
		if cfg.RoleClaim != "" {
			a.headerClaims[cfg.RoleClaim] = true
		}
	}
	return a
}

// Authenticate never fails open: a present-but-invalid credential is an
// error; an absent credential yields the anonymous role. Errors wrap
// ErrInvalidToken or ErrRoleNotAllowed.
func (a *Authenticator) Authenticate(r *http.Request) (map[string]any, string, error) {
	if !a.cfg.Enabled {
		return nil, "", nil
	}
	switch a.cfg.Mode {
	case "jwt":
		return a.fromJWT(r)
	case "headers":
		return a.fromHeaders(r)
	default:
		return map[string]any{}, a.cfg.AnonymousRole, nil
	}
}

func (a *Authenticator) fromJWT(r *http.Request) (map[string]any, string, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		return map[string]any{}, a.cfg.AnonymousRole, nil
	}
	tokenStr, ok := strings.CutPrefix(authz, "Bearer ")
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
	return a.resolve(map[string]any(claims))
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
		return map[string]any{}, a.cfg.AnonymousRole, nil
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
	return a.resolve(claims)
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

// peerTrusted reports whether the direct peer may supply claim headers. With
// no trusted proxies configured every peer is trusted (legacy behavior; the
// product logs a startup warning). An unparseable RemoteAddr fails closed.
func (a *Authenticator) peerTrusted(remoteAddr string) bool {
	if a.cfg.TrustedProxies == nil {
		return true
	}
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

// resolve extracts the database role from claims (falling back to
// DefaultRole, then AnonymousRole) and enforces the AllowedRoles policy on
// claim-supplied roles.
func (a *Authenticator) resolve(claims map[string]any) (map[string]any, string, error) {
	role := a.cfg.DefaultRole
	fromClaim := false
	if v, ok := claims[a.cfg.RoleClaim]; ok {
		if s, ok := v.(string); ok && s != "" {
			role = s
			fromClaim = true
		}
	}
	if role == "" {
		role = a.cfg.AnonymousRole
	}
	if fromClaim && !a.roleAllowed(role) {
		return nil, "", &authError{kind: ErrRoleNotAllowed, msg: fmt.Sprintf("role %q is not allowed", role)}
	}
	return claims, role, nil
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
