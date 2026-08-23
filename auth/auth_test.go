package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret"

func testAuth() *Authenticator {
	return New(Config{
		Enabled:       true,
		AnonymousRole: "anonymous",
		RoleClaim:     "role",
		Mode:          "jwt",
		JWTSecret:     testSecret,
	})
}

func signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestJWTWithoutExpRejected(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{"role": "app_user"}))
	if _, _, err := a.Authenticate(r); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token without exp: err = %v, want ErrInvalidToken", err)
	}
}

func TestJWTExpiredRejected(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(-time.Minute).Unix(),
	}))
	_, _, err := a.Authenticate(r)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token: err = %v, want ErrInvalidToken", err)
	}
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("expired token: cause not preserved: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "invalid token: ") {
		t.Fatalf("expired token message = %q", err.Error())
	}
}

func TestJWTValidExpAccepted(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	}))
	claims, role, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if role != "app_user" {
		t.Fatalf("role = %q, want app_user", role)
	}
	if claims["role"] != "app_user" {
		t.Fatalf("claims missing role: %v", claims)
	}
}

func TestAllowedRoles(t *testing.T) {
	newAuth := func(allowed ...string) *Authenticator {
		return New(Config{
			Enabled:       true,
			AnonymousRole: "anonymous",
			DefaultRole:   "app_default",
			RoleClaim:     "role",
			AllowedRoles:  allowed,
			Mode:          "jwt",
			JWTSecret:     testSecret,
		})
	}
	cases := []struct {
		name    string
		allowed []string
		role    string
		wantErr bool
	}{
		{"empty list allows any", nil, "app_user", false},
		{"listed role allowed", []string{"app_user"}, "app_user", false},
		{"unlisted role rejected", []string{"app_user"}, "app_admin", true},
		{"default role always allowed", []string{"app_user"}, "app_default", false},
		{"anonymous role always allowed", []string{"app_user"}, "anonymous", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAuth(tc.allowed...)
			r := httptest.NewRequest("POST", "/graphql", nil)
			r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{
				"role": tc.role,
				"exp":  time.Now().Add(time.Hour).Unix(),
			}))
			_, role, err := a.Authenticate(r)
			if tc.wantErr {
				if !errors.Is(err, ErrRoleNotAllowed) {
					t.Fatalf("role %q: err = %v, want ErrRoleNotAllowed", tc.role, err)
				}
				if errors.Is(err, ErrInvalidToken) {
					t.Fatalf("role %q: must not also be ErrInvalidToken", tc.role)
				}
				if err.Error() != `role "app_admin" is not allowed` {
					t.Fatalf("role %q: message = %q", tc.role, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("role %q rejected: %v", tc.role, err)
			}
			if role != tc.role {
				t.Fatalf("role = %q, want %q", role, tc.role)
			}
		})
	}
}

func TestNoTokenIsAnonymous(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	_, role, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	if role != "anonymous" {
		t.Fatalf("role = %q, want anonymous", role)
	}
}

func headersAuth(trusted ...string) *Authenticator {
	var prefixes []netip.Prefix
	for _, t := range trusted {
		p, err := parseTrustedProxy(t)
		if err != nil {
			panic(err)
		}
		prefixes = append(prefixes, p)
	}
	return New(Config{
		Enabled:        true,
		AnonymousRole:  "anonymous",
		RoleClaim:      "role",
		Mode:           "headers",
		HeaderPrefix:   "X-Pdbq-Claim-",
		TrustedProxies: prefixes,
	})
}

func parseTrustedProxy(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func headersRequest(remoteAddr string) *http.Request {
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.RemoteAddr = remoteAddr
	r.Header.Set("X-Pdbq-Claim-Role", "app_user")
	return r
}

func TestHeadersNoTrustedProxiesAcceptsAnyPeer(t *testing.T) {
	a := headersAuth()
	_, role, err := a.Authenticate(headersRequest("203.0.113.7:4444"))
	if err != nil {
		t.Fatalf("legacy behavior broken: %v", err)
	}
	if role != "app_user" {
		t.Fatalf("role = %q, want app_user", role)
	}
}

func TestHeadersTrustedPeerAccepted(t *testing.T) {
	for _, tc := range []struct{ trusted, remote string }{
		{"10.0.0.0/8", "10.1.2.3:9999"},
		{"10.0.0.5", "10.0.0.5:80"},
		{"10.0.0.0/8", "[::ffff:10.0.0.5]:80"}, // 4-in-6 mapped peer
	} {
		a := headersAuth(tc.trusted)
		_, role, err := a.Authenticate(headersRequest(tc.remote))
		if err != nil {
			t.Fatalf("trusted=%s remote=%s rejected: %v", tc.trusted, tc.remote, err)
		}
		if role != "app_user" {
			t.Fatalf("trusted=%s remote=%s role = %q, want app_user", tc.trusted, tc.remote, role)
		}
	}
}

func TestHeadersUntrustedPeerWithClaimsRejected(t *testing.T) {
	a := headersAuth("10.0.0.0/8")
	_, _, err := a.Authenticate(headersRequest("203.0.113.7:4444"))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("claim headers from untrusted peer: err = %v, want ErrInvalidToken", err)
	}
	if err.Error() != "claim headers from untrusted peer" {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestHeadersUntrustedPeerWithoutClaimsAnonymous(t *testing.T) {
	a := headersAuth("10.0.0.0/8")
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.RemoteAddr = "203.0.113.7:4444"
	_, role, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("claimless request rejected: %v", err)
	}
	if role != "anonymous" {
		t.Fatalf("role = %q, want anonymous", role)
	}
}

func TestHeadersUnparseableRemoteAddrFailsClosed(t *testing.T) {
	a := headersAuth("10.0.0.0/8")
	if _, _, err := a.Authenticate(headersRequest("not-an-address")); err == nil {
		t.Fatal("unparseable RemoteAddr accepted; must fail closed")
	}
}

func TestMalformedAuthorizationHeader(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Basic abc")
	_, _, err := a.Authenticate(r)
	if !errors.Is(err, ErrInvalidToken) || err.Error() != "malformed Authorization header" {
		t.Fatalf("err = %v", err)
	}
}

func TestDisabledIsNoop(t *testing.T) {
	a := New(Config{Enabled: false, Mode: "jwt"})
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer garbage")
	claims, role, err := a.Authenticate(r)
	if err != nil || claims != nil || role != "" {
		t.Fatalf("disabled auth = (%v, %q, %v), want (nil, \"\", nil)", claims, role, err)
	}
}

func TestModeNoneIsAnonymous(t *testing.T) {
	a := New(Config{Enabled: true, Mode: "none", AnonymousRole: "anonymous"})
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer garbage")
	_, role, err := a.Authenticate(r)
	if err != nil || role != "anonymous" {
		t.Fatalf("mode none = (%q, %v)", role, err)
	}
}
