// Package auth turns an HTTP request into verified claims and a database
// role: a JWT (HS* via shared secret, RS*/ES* via a cached JWKS endpoint),
// trusted-gateway claim headers, or nothing. It is transport-neutral apart
// from reading *http.Request headers and RemoteAddr.
package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken marks a present-but-unusable credential: malformed
// Authorization header, bad signature, expired, wrong issuer/audience, JWKS
// miss, or claim headers from an untrusted peer. Products map it to 401.
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
	// (pdbq: X-Pdbq-Claim-, pdbr: X-Pdbr-Claim-).
	HeaderPrefix string
	// TrustedProxies gates claim headers by peer address; nil trusts every
	// peer, an empty non-nil slice trusts none.
	TrustedProxies []netip.Prefix
	// RoleClaim names the claim carrying the database role.
	RoleClaim     string
	DefaultRole   string
	AnonymousRole string
	// AllowedRoles restricts claim-supplied roles; empty allows any.
	AllowedRoles []string
}

// Authenticator turns an HTTP request into (claims, role) per the configured
// claim source: verified JWT, trusted gateway headers, or none.
type Authenticator struct {
	cfg Config
	// jwks verifies asymmetric tokens when JWKSURL is set.
	jwks *jwksCache
}

// New builds an Authenticator. Invalid TrustedProxies entries cannot occur
// (they are typed); a nil slice means every peer is trusted.
func New(cfg Config) *Authenticator {
	a := &Authenticator{cfg: cfg}
	if cfg.JWKSURL != "" {
		a.jwks = newJWKSCache(cfg.JWKSURL, cfg.JWKSCacheTTL)
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
			return a.jwks.key(kid)
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
		return nil, "", invalidToken("invalid token: "+err.Error(), err)
	}
	return a.resolve(map[string]any(claims))
}

func (a *Authenticator) fromHeaders(r *http.Request) (map[string]any, string, error) {
	claims := map[string]any{}
	prefix := http.CanonicalHeaderKey(a.cfg.HeaderPrefix)
	for name, vals := range r.Header {
		if strings.HasPrefix(name, prefix) && len(vals) > 0 {
			key := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, prefix), "-", "_"))
			claims[key] = vals[0]
		}
	}
	if len(claims) == 0 {
		return map[string]any{}, a.cfg.AnonymousRole, nil
	}
	if !a.peerTrusted(r.RemoteAddr) {
		return nil, "", invalidToken("claim headers from untrusted peer", nil)
	}
	return a.resolve(claims)
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
