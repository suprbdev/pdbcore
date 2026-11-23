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
	return mustNew(Config{
		AnonymousRole: "anonymous",
		Mode:          "jwt",
		JWTSecret:     testSecret,
	})
}

func mustNew(cfg Config) *Authenticator {
	a, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return a
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
		return mustNew(Config{
			AnonymousRole: "anonymous",
			DefaultRole:   "app_default",
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
	return mustNew(Config{
		AnonymousRole:  "anonymous",
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

func TestModeNoneIsAnonymous(t *testing.T) {
	a := mustNew(Config{Mode: ModeNone, AnonymousRole: "anonymous"})
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer garbage")
	_, role, err := a.Authenticate(r)
	if err != nil || role != "anonymous" {
		t.Fatalf("mode none = (%q, %v)", role, err)
	}
}

func claimRequest(headers map[string][]string) *http.Request {
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	for k, v := range headers {
		r.Header[k] = v
	}
	return r
}

func TestHeadersClaimMapping(t *testing.T) {
	a := headersAuth("10.0.0.0/8")
	r := claimRequest(nil)
	r.Header.Set("x-pdbq-claim-role", "app_user")
	r.Header.Set("X-Pdbq-Claim-User-Id", "42")
	r.Header.Set("X-PDBQ-CLAIM-TENANT", "acme")
	r.Header.Set("X-Pdbq-Claim-Org2-Unit3", "u")
	r.Header.Set("X-Other", "ignored")
	claims, role, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	if role != "app_user" {
		t.Fatalf("role = %q", role)
	}
	want := map[string]any{"role": "app_user", "user_id": "42", "tenant": "acme", "org2_unit3": "u"}
	if len(claims) != len(want) {
		t.Fatalf("claims = %v, want %v", claims, want)
	}
	for k, v := range want {
		if claims[k] != v {
			t.Fatalf("claims = %v, want %v", claims, want)
		}
	}
}

func TestHeadersRejectsCollidingClaims(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string][]string
		msg     string
	}{
		{"issue 36: User-Id vs User_id", map[string][]string{
			"X-Pdbq-Claim-User-Id": {"gateway-42"}, "X-Pdbq-Claim-User_id": {"client-1"},
		}, "malformed claim header"},
		{"issue 37: User-Id vs User_Id", map[string][]string{
			"X-Pdbq-Claim-User-Id": {"2"}, "X-Pdbq-Claim-User_Id": {"1"},
		}, "malformed claim header"},
		{"underscore alone", map[string][]string{"X-Pdbq-Claim-User_id": {"1"}}, "malformed claim header"},
		{"underscore role claim", map[string][]string{"X-Pdbq-Claim-App_Role": {"admin"}}, "malformed claim header"},
		{"non-canonical case key", map[string][]string{
			"X-Pdbq-Claim-Role": {"app_user"}, "X-Pdbq-Claim-ROLE": {"admin"},
		}, "conflicting claim headers"},
		{"lower-case prefix key", map[string][]string{
			"X-Pdbq-Claim-User-Id": {"2"}, "x-pdbq-claim-user-id": {"1"},
		}, "conflicting claim headers"},
		{"repeated values (gateway appended)", map[string][]string{
			"X-Pdbq-Claim-User-Id": {"1", "2"},
		}, "repeated claim header"},
		{"double hyphen", map[string][]string{"X-Pdbq-Claim-User--Id": {"1"}}, "malformed claim header"},
		{"trailing hyphen", map[string][]string{"X-Pdbq-Claim-User-": {"1"}}, "malformed claim header"},
		{"leading hyphen", map[string][]string{"X-Pdbq-Claim--User": {"1"}}, "malformed claim header"},
		{"empty suffix", map[string][]string{"X-Pdbq-Claim-": {"1"}}, "malformed claim header"},
		{"dot", map[string][]string{"X-Pdbq-Claim-User.Id": {"1"}}, "malformed claim header"},
		{"non-ASCII", map[string][]string{"X-Pdbq-Claim-Usér": {"1"}}, "malformed claim header"},
	}
	a := headersAuth("10.0.0.0/8")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 50 {
				claims, _, err := a.Authenticate(claimRequest(tc.headers))
				if !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("err = %v (claims %v), want ErrInvalidToken", err, claims)
				}
				if err.Error() != tc.msg {
					t.Fatalf("message = %q, want %q", err.Error(), tc.msg)
				}
			}
		})
	}
}

func TestHeadersUntrustedPeerCheckedFirst(t *testing.T) {
	a := headersAuth("10.0.0.0/8")
	r := claimRequest(map[string][]string{"X-Pdbq-Claim-User_id": {"1"}})
	r.RemoteAddr = "203.0.113.7:4444"
	if _, _, err := a.Authenticate(r); err == nil || err.Error() != "claim headers from untrusted peer" {
		t.Fatalf("err = %v, want untrusted peer", err)
	}
}

func TestHeadersEndToEndWireCanonicalization(t *testing.T) {
	var got map[string]any
	var gotErr error
	a := headersAuth("127.0.0.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _, gotErr = a.Authenticate(r)
	}))
	defer srv.Close()

	send := func(headers [][2]string) {
		t.Helper()
		req, err := http.NewRequest("POST", srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range headers {
			req.Header[h[0]] = append(req.Header[h[0]], h[1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	send([][2]string{{"X-Pdbq-Claim-User-Id", "2"}, {"X-Pdbq-Claim-User_Id", "1"}})
	if !errors.Is(gotErr, ErrInvalidToken) {
		t.Fatalf("underscore spelling over the wire: claims %v, err %v", got, gotErr)
	}
	send([][2]string{{"X-Pdbq-Claim-User-Id", "2"}, {"x-pdbq-claim-user-id", "1"}})
	if !errors.Is(gotErr, ErrInvalidToken) || gotErr.Error() != "repeated claim header" {
		t.Fatalf("case variant over the wire: claims %v, err %v", got, gotErr)
	}
	send([][2]string{{"x-pdbq-claim-user-id", "2"}})
	if gotErr != nil || got["user_id"] != "2" {
		t.Fatalf("single header over the wire: claims %v, err %v", got, gotErr)
	}
}

func TestHeadersClaimAllowlist(t *testing.T) {
	a := mustNew(Config{
		AnonymousRole:   "anonymous",
		HeaderRoleClaim: "app_role",
		TrustedProxies:  []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Mode:            "headers",
		HeaderPrefix:    "X-Pdbr-Claim-",
		HeaderClaims:    []string{"user_id"},
	})
	r := claimRequest(map[string][]string{"X-Pdbr-Claim-User-Id": {"1"}, "X-Pdbr-Claim-App-Role": {"app_user"}})
	claims, role, err := a.Authenticate(r)
	if err != nil || role != "app_user" || claims["user_id"] != "1" {
		t.Fatalf("allowlisted claims = (%v, %q, %v)", claims, role, err)
	}
	r = claimRequest(map[string][]string{"X-Pdbr-Claim-User-Id": {"1"}, "X-Pdbr-Claim-Is-Admin": {"true"}})
	if _, _, err := a.Authenticate(r); !errors.Is(err, ErrInvalidToken) || err.Error() != "claim header not allowed" {
		t.Fatalf("unlisted claim: err = %v", err)
	}
}

func TestValidHeaderClaim(t *testing.T) {
	for claim, want := range map[string]bool{
		"role": true, "user_id": true, "org2_unit3": true, "a": true,
		"": false, "User_id": false, "user__id": false, "_user": false, "user_": false,
		"user-id": false, "user.id": false, "rôle": false,
	} {
		if got := ValidHeaderClaim(claim); got != want {
			t.Errorf("ValidHeaderClaim(%q) = %v, want %v", claim, got, want)
		}
	}
}

func TestHeaderClaimName(t *testing.T) {
	for _, tc := range []struct{ prefix, claim, want string }{
		{"X-Pdbq-Claim-", "user_id", "X-Pdbq-Claim-User-Id"},
		{"x-pdbr-claim-", "role", "X-Pdbr-Claim-Role"},
	} {
		if got := HeaderClaimName(tc.prefix, tc.claim); got != tc.want {
			t.Errorf("HeaderClaimName(%q, %q) = %q, want %q", tc.prefix, tc.claim, got, tc.want)
		}
		key, ok := headerClaimKey(tc.want[len(tc.prefix):])
		if !ok || key != tc.claim {
			t.Errorf("round trip %q -> (%q, %v), want %q", tc.want, key, ok, tc.claim)
		}
	}
}
