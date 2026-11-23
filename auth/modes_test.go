package auth

import (
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := map[string]struct {
		cfg  Config
		want string
	}{
		"unknown mode":         {Config{Mode: "oauth"}, `unknown auth mode "oauth"`},
		"empty mode":           {Config{}, `unknown auth mode ""`},
		"jwt without key":      {Config{Mode: ModeJWT}, "jwt mode needs a JWT secret or a JWKS URL"},
		"headers no proxies":   {Config{Mode: ModeHeaders, HeaderPrefix: "X-Claim-"}, "headers mode needs at least one trusted proxy"},
		"headers no prefix":    {Config{Mode: ModeHeaders, TrustedProxies: proxies}, "headers mode needs a claim header prefix"},
		"headers role claim":   {Config{Mode: ModeHeaders, HeaderPrefix: "X-Claim-", TrustedProxies: proxies, HeaderRoleClaim: "App-Role"}, `role claim "App-Role" cannot be sent`},
		"bad role claim path":  {Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: "a..b"}, "invalid role claim path"},
		"negative leeway":      {Config{Mode: ModeNone, Leeway: -time.Second}, "negative leeway"},
		"unterminated quoted":  {Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: `"abc`}, "unterminated quoted name"},
		"bad index":            {Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: "roles[x]"}, "index must be"},
		"trailing garbage":     {Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: "roles[0]x"}, "unexpected"},
		"empty quoted segment": {Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: `a.""`}, "empty name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a, err := New(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) || a != nil {
				t.Fatalf("New = (%v, %v), want error containing %q", a, err, tc.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"jwt", "headers", "none"} {
		if m, err := ParseMode(s); err != nil || string(m) != s {
			t.Errorf("ParseMode(%q) = %q, %v", s, m, err)
		}
	}
	if _, err := ParseMode("JWT"); err == nil {
		t.Error("ParseMode is case-sensitive")
	}
}

func TestAnonymousRefusedWithoutAnonymousRole(t *testing.T) {
	jwtAuth := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret})
	hdrAuth := mustNew(Config{Mode: ModeHeaders, HeaderPrefix: "X-Claim-", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	for name, a := range map[string]*Authenticator{"jwt": jwtAuth, "headers": hdrAuth} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:1"
		claims, role, err := a.Authenticate(r)
		if !errors.Is(err, ErrAuthRequired) || errors.Is(err, ErrInvalidToken) || claims != nil || role != "" {
			t.Fatalf("%s: Authenticate = (%v, %q, %v), want ErrAuthRequired", name, claims, role, err)
		}
		if got := Challenge(err); got != "Bearer" {
			t.Fatalf("%s: Challenge = %q", name, got)
		}
	}
}

func TestModeNoneWithoutAnonymousRoleRunsAsConnectionRole(t *testing.T) {
	a := mustNew(Config{Mode: ModeNone})
	claims, role, err := a.Authenticate(httptest.NewRequest("GET", "/", nil))
	if err != nil || role != "" || len(claims) != 0 {
		t.Fatalf("Authenticate = (%v, %q, %v)", claims, role, err)
	}
}

func TestChallenge(t *testing.T) {
	a := testAuth()
	r := bearerRequest("garbage")
	_, _, err := a.Authenticate(r)
	if got := Challenge(err); got != `Bearer error="invalid_token"` {
		t.Fatalf("invalid token challenge = %q (err %v)", got, err)
	}
	notAllowed := &authError{kind: ErrRoleNotAllowed, msg: "x"}
	if got := Challenge(notAllowed); got != `Bearer error="insufficient_scope"` {
		t.Fatalf("role challenge = %q", got)
	}
	if got := Challenge(errors.New("other")); got != "" {
		t.Fatalf("other challenge = %q", got)
	}
}

func TestBearerSchemeCaseInsensitive(t *testing.T) {
	a := testAuth()
	tok := signToken(t, jwt.MapClaims{"role": "app_user", "exp": time.Now().Add(time.Hour).Unix()})
	for _, h := range []string{"Bearer " + tok, "bearer " + tok, "BEARER " + tok, "bEaReR  " + tok} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", h)
		if _, role, err := a.Authenticate(r); err != nil || role != "app_user" {
			t.Errorf("%q: (%q, %v)", h[:8], role, err)
		}
	}
	for _, h := range []string{"Bearer", "Bearer ", "Bearer a b", "Bearertoken", "Basic " + tok, "Bearer\t" + tok} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", h)
		if _, _, err := a.Authenticate(r); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%q accepted: %v", h, err)
		}
	}
}

func TestLeeway(t *testing.T) {
	tok := signToken(t, jwt.MapClaims{"role": "app_user", "exp": time.Now().Add(-10 * time.Second).Unix()})
	strict := testAuth()
	if _, _, err := strict.Authenticate(bearerRequest(tok)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token without leeway: %v", err)
	}
	lenient := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret, AnonymousRole: "anonymous", Leeway: 30 * time.Second})
	if _, role, err := lenient.Authenticate(bearerRequest(tok)); err != nil || role != "app_user" {
		t.Fatalf("expired token within leeway: (%q, %v)", role, err)
	}
}

func TestRoleClaimPath(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	claims := jwt.MapClaims{
		"exp":                       exp,
		"role":                      "top_role",
		"app_metadata":              map[string]any{"role": "meta_role"},
		"realm_access":              map[string]any{"roles": []any{"first_role", "second_role"}},
		"https://example.com/role":  "url_role",
		"https://example.com/roles": map[string]any{"db": "url_nested"},
		"num":                       7,
	}
	cases := []struct{ path, want string }{
		{"", "top_role"},
		{"role", "top_role"},
		{".role", "top_role"},
		{"app_metadata.role", "meta_role"},
		{".app_metadata.role", "meta_role"},
		{"realm_access.roles[1]", "second_role"},
		{`"https://example.com/role"`, "url_role"},
		{`."https://example.com/roles".db`, "url_nested"},
		{"missing.role", "default_role"},
		{"realm_access.roles[5]", "default_role"},
		{"role.nested", "default_role"},
	}
	for _, tc := range cases {
		a := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: tc.path, DefaultRole: "default_role"})
		_, role, err := a.Authenticate(bearerRequest(signToken(t, claims)))
		if err != nil || role != tc.want {
			t.Errorf("path %q: (%q, %v), want %q", tc.path, role, err, tc.want)
		}
	}
	for _, path := range []string{"num", "app_metadata", "realm_access.roles"} {
		a := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret, RoleClaimPath: path, DefaultRole: "default_role"})
		if _, _, err := a.Authenticate(bearerRequest(signToken(t, claims))); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("path %q to a non-string: err = %v, want ErrInvalidToken", path, err)
		}
	}
}

func TestEmptyRoleClaimRejected(t *testing.T) {
	a := testAuth()
	tok := signToken(t, jwt.MapClaims{"role": "", "exp": time.Now().Add(time.Hour).Unix()})
	if _, _, err := a.Authenticate(bearerRequest(tok)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("empty role claim: %v", err)
	}
	h := mustNew(Config{Mode: ModeHeaders, HeaderPrefix: "X-Claim-", AnonymousRole: "anonymous", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	r := claimRequest(map[string][]string{"X-Claim-Role": {""}})
	if _, _, err := h.Authenticate(r); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("empty role header: %v", err)
	}
}

func TestNoRoleResolvedRefused(t *testing.T) {
	a := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret})
	tok := signToken(t, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	if _, _, err := a.Authenticate(bearerRequest(tok)); !errors.Is(err, ErrRoleNotAllowed) {
		t.Fatalf("token without role and no default: err = %v, want ErrRoleNotAllowed", err)
	}
	a = mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret, SkipRoleSwitch: true})
	claims, role, err := a.Authenticate(bearerRequest(tok))
	if err != nil || role != "" || claims["sub"] != "u1" {
		t.Fatalf("SkipRoleSwitch: (%v, %q, %v)", claims, role, err)
	}
}

func TestSkipRoleSwitch(t *testing.T) {
	a := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret, AnonymousRole: "anonymous", AllowedRoles: []string{"app_user"}, SkipRoleSwitch: true})
	exp := time.Now().Add(time.Hour).Unix()
	claims, role, err := a.Authenticate(bearerRequest(signToken(t, jwt.MapClaims{"role": "app_user", "sub": "u1", "exp": exp})))
	if err != nil || role != "" || claims["sub"] != "u1" {
		t.Fatalf("verified token: (%v, %q, %v)", claims, role, err)
	}
	if _, _, err := a.Authenticate(bearerRequest(signToken(t, jwt.MapClaims{"role": "app_admin", "exp": exp}))); !errors.Is(err, ErrRoleNotAllowed) {
		t.Fatalf("disallowed role: %v", err)
	}
	if _, _, err := a.Authenticate(bearerRequest("garbage")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid token: %v", err)
	}
	if _, role, err := a.Authenticate(httptest.NewRequest("GET", "/", nil)); err != nil || role != "" {
		t.Fatalf("anonymous: (%q, %v)", role, err)
	}
	refusing := mustNew(Config{Mode: ModeJWT, JWTSecret: testSecret, SkipRoleSwitch: true})
	if _, _, err := refusing.Authenticate(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("anonymous without anonymous role: %v", err)
	}
}

func TestValidateClaimPath(t *testing.T) {
	for _, p := range []string{"role", ".role", "a.b.c", "a[0]", "a[0][1].b", `"x.y"`, `"a b".c`, "a-b:c/d"} {
		if err := ValidateClaimPath(p); err != nil {
			t.Errorf("ValidateClaimPath(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", ".", "a.", "a..b", "[0]", "a[", "a[-1]", "a[+1]", "a b", `a"b"`, "a]"} {
		if err := ValidateClaimPath(p); err == nil {
			t.Errorf("ValidateClaimPath(%q) = nil, want an error", p)
		}
	}
}

func FuzzClaimPath(f *testing.F) {
	for _, s := range []string{"role", ".a.b[0]", `"x.y".z[12]`, "a[", `"`, "[0]"} {
		f.Add(s)
	}
	claims := map[string]any{"a": map[string]any{"b": []any{"r"}}, "role": "x"}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := parseClaimPath(s)
		if err != nil {
			return
		}
		if len(p) == 0 {
			t.Fatalf("%q parsed to an empty path", s)
		}
		p.lookup(claims)
	})
}
