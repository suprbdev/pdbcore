package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func rsaJWK(t *testing.T, kid string, pub *rsa.PublicKey) map[string]string {
	t.Helper()
	return map[string]string{
		"kty": "RSA",
		"kid": kid,
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func ecJWK(t *testing.T, kid string, pub *ecdsa.PublicKey) map[string]string {
	t.Helper()
	point, err := pub.Bytes() // 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	size := (len(point) - 1) / 2
	return map[string]string{
		"kty": "EC",
		"kid": kid,
		"use": "sig",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(point[1 : 1+size]),
		"y":   base64.RawURLEncoding.EncodeToString(point[1+size:]),
	}
}

// jwksServer serves the given keys and counts fetches.
func jwksServer(t *testing.T, keys *[]map[string]string, fetches *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": *keys})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jwksAuth(url string) *Authenticator {
	return New(Config{
		Enabled:       true,
		AnonymousRole: "anonymous",
		RoleClaim:     "role",
		Mode:          "jwt",
		JWKSURL:       url,
		JWKSCacheTTL:  time.Hour,
	})
}

func bearerRequest(token string) *http.Request {
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestJWKSRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := []map[string]string{rsaJWK(t, "k1", &key.PublicKey)}
	var fetches atomic.Int32
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "k1"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	_, role, err := a.Authenticate(bearerRequest(signed))
	if err != nil {
		t.Fatalf("RS256 token rejected: %v", err)
	}
	if role != "app_user" {
		t.Fatalf("role = %q, want app_user", role)
	}
	// Second verification hits the cache.
	if _, _, err := a.Authenticate(bearerRequest(signed)); err != nil {
		t.Fatal(err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 (cache)", n)
	}
}

func TestJWKSES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := []map[string]string{ecJWK(t, "e1", &key.PublicKey)}
	var fetches atomic.Int32
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "e1"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Authenticate(bearerRequest(signed)); err != nil {
		t.Fatalf("ES256 token rejected: %v", err)
	}
}

func TestJWKSRejectsWrongKeyAndHMAC(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := []map[string]string{rsaJWK(t, "k1", &key.PublicKey)}
	var fetches atomic.Int32
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	// Signed by a different key.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "k1"
	signed, _ := tok.SignedString(other)
	if _, _, err := a.Authenticate(bearerRequest(signed)); err == nil {
		t.Fatal("token signed by wrong key accepted")
	}

	// HS256 must be off the algorithm allowlist in JWKS mode (no downgrade
	// to HMAC-with-public-key).
	hmacTok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("secret"))
	if _, _, err := a.Authenticate(bearerRequest(hmacTok)); err == nil {
		t.Fatal("HS256 token accepted in JWKS mode")
	}
}

func TestJWKSRotationRefetch(t *testing.T) {
	key1, _ := rsa.GenerateKey(rand.Reader, 2048)
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := []map[string]string{rsaJWK(t, "k1", &key1.PublicKey)}
	var fetches atomic.Int32
	srv := jwksServer(t, &keys, &fetches)
	a := jwksAuth(srv.URL)

	sign := func(key *rsa.PrivateKey, kid string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, _, err := a.Authenticate(bearerRequest(sign(key1, "k1"))); err != nil {
		t.Fatal(err)
	}
	// Rotate: new kid appears at the endpoint. Force the refetch window open
	// (unknown kids refetch at most once per minute).
	keys = []map[string]string{rsaJWK(t, "k2", &key2.PublicKey)}
	a.jwks.mu.Lock()
	a.jwks.attempted = time.Now().Add(-2 * time.Minute)
	a.jwks.mu.Unlock()
	if _, _, err := a.Authenticate(bearerRequest(sign(key2, "k2"))); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("JWKS fetched %d times, want 2 (initial + rotation)", n)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// logBuffer is a concurrency-safe sink for a text slog handler.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// switchableJWKS serves keys while healthy and 503 otherwise, after an
// optional delay and after its gate (if set) is closed, and counts fetches.
type switchableJWKS struct {
	srv     *httptest.Server
	keys    []map[string]string
	fetches atomic.Int32
	failing atomic.Bool
	delay   atomic.Int64

	mu   sync.Mutex
	gate chan struct{}
}

func (s *switchableJWKS) setGate(g chan struct{}) {
	s.mu.Lock()
	s.gate = g
	s.mu.Unlock()
}

func (s *switchableJWKS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.fetches.Add(1)
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	time.Sleep(time.Duration(s.delay.Load()))
	if s.failing.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": s.keys})
}

type jwksFixture struct {
	key   *rsa.PrivateKey
	jwks  *switchableJWKS
	auth  *Authenticator
	clock *fakeClock
	logs  *logBuffer
}

// newJWKSFixture serves one RSA key (kid k1) and drives the cache from a
// fake clock. A non-nil gate holds every fetch until it is closed.
func newJWKSFixture(t *testing.T, ttl time.Duration, gate chan struct{}) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &switchableJWKS{keys: []map[string]string{rsaJWK(t, "k1", &key.PublicKey)}, gate: gate}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	logs := &logBuffer{}
	a := New(Config{
		Enabled:       true,
		AnonymousRole: "anonymous",
		RoleClaim:     "role",
		Mode:          "jwt",
		JWKSURL:       s.srv.URL + "/internal/idp/jwks.json",
		JWKSCacheTTL:  ttl,
		Logger:        slog.New(slog.NewTextHandler(logs, nil)),
	})
	clock := &fakeClock{t: time.Now()}
	a.jwks.now = clock.now
	return &jwksFixture{key: key, jwks: s, auth: a, clock: clock, logs: logs}
}

func (f *jwksFixture) token(t *testing.T, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": "app_user", "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = kid
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// forgedToken has a valid shape and exp but a garbage signature, so it
// reaches the key lookup without any signing key.
func forgedToken(kid string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256","kid":"`+kid+`"}`)) + "." + enc([]byte(`{"exp":9999999999}`)) + ".AAAA"
}

func TestJWKSFailureErrorIsSanitized(t *testing.T) {
	f := newJWKSFixture(t, time.Hour, nil)
	f.jwks.failing.Store(true)
	_, _, err := f.auth.Authenticate(bearerRequest(forgedToken("k1")))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
	if err.Error() != "invalid token" {
		t.Fatalf("client-facing message = %q, want %q", err.Error(), "invalid token")
	}
	if !errors.Is(err, errJWKS) {
		t.Fatalf("cause not preserved: %v", err)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "jwks fetch failed") || !strings.Contains(logs, f.jwks.srv.URL+"/internal/idp/jwks.json") {
		t.Fatalf("fetch failure not logged with URL:\n%s", logs)
	}
}

func TestJWKSUnknownKidMessageIsSanitized(t *testing.T) {
	f := newJWKSFixture(t, time.Hour, nil)
	_, _, err := f.auth.Authenticate(bearerRequest(f.token(t, "nope")))
	if !errors.Is(err, ErrInvalidToken) || err.Error() != "invalid token" {
		t.Fatalf("unknown kid: err = %v", err)
	}
	if !errors.Is(err, errJWKSNoKey) {
		t.Fatalf("unknown kid: cause = %v, want errJWKSNoKey", err)
	}
}

func TestJWKSValidationReasonsKept(t *testing.T) {
	f := newJWKSFixture(t, time.Hour, nil)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": "app_user", "exp": time.Now().Add(-time.Minute).Unix(),
	})
	tok.Header["kid"] = "k1"
	signed, _ := tok.SignedString(f.key)
	_, _, err := f.auth.Authenticate(bearerRequest(signed))
	if !errors.Is(err, jwt.ErrTokenExpired) || !strings.HasPrefix(err.Error(), "invalid token: ") {
		t.Fatalf("expired JWKS token: err = %v", err)
	}
}

func TestJWKSConcurrentRequestsShareOneFetch(t *testing.T) {
	f := newJWKSFixture(t, time.Hour, nil)
	f.jwks.failing.Store(true)
	const delay = 300 * time.Millisecond
	f.jwks.delay.Store(int64(delay))

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := time.Now()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.auth.Authenticate(bearerRequest(forgedToken("k1")))
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	for i, err := range errs {
		if !errors.Is(err, ErrInvalidToken) || err.Error() != "invalid token" {
			t.Fatalf("request %d: err = %v", i, err)
		}
	}
	if got := f.jwks.fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetched %d times for %d concurrent requests, want 1", got, n)
	}
	if elapsed > 3*delay {
		t.Fatalf("%d concurrent requests took %v with a %v fetch; they were serialized", n, elapsed, delay)
	}
}

func TestJWKSRequestWaitBoundedByContext(t *testing.T) {
	gate := make(chan struct{})
	f := newJWKSFixture(t, time.Hour, gate)
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := f.auth.Authenticate(bearerRequest(f.token(t, "k1")).WithContext(ctx))
	if !errors.Is(err, ErrInvalidToken) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung fetch: err = %v, want ErrInvalidToken wrapping DeadlineExceeded", err)
	}
	if err.Error() != "invalid token" {
		t.Fatalf("client-facing message = %q", err.Error())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request waited %v, want about its own 100ms deadline", elapsed)
	}

	open()
	if _, _, err := f.auth.Authenticate(bearerRequest(f.token(t, "k1"))); err != nil {
		t.Fatalf("after the shared fetch completed: %v", err)
	}
	if got := f.jwks.fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 (the abandoned fetch is reused)", got)
	}
}

func TestJWKSFailureBackoff(t *testing.T) {
	f := newJWKSFixture(t, time.Hour, nil)
	f.jwks.failing.Store(true)
	auth := func() error {
		_, _, err := f.auth.Authenticate(bearerRequest(f.token(t, "k1")))
		return err
	}

	for range 5 {
		if err := auth(); !errors.Is(err, errJWKSUnavailable) {
			t.Fatalf("err = %v, want errJWKSUnavailable", err)
		}
	}
	if got := f.jwks.fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetched %d times during the first backoff, want 1", got)
	}

	want := int32(1)
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		f.clock.advance(wait - time.Millisecond)
		_ = auth()
		if got := f.jwks.fetches.Load(); got != want {
			t.Fatalf("fetched %d times before the %v backoff elapsed, want %d", got, wait, want)
		}
		f.clock.advance(time.Millisecond)
		_ = auth()
		want++
		if got := f.jwks.fetches.Load(); got != want {
			t.Fatalf("fetched %d times after the %v backoff, want %d", got, wait, want)
		}
	}

	f.clock.advance(time.Hour)
	f.jwks.failing.Store(false)
	if err := auth(); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
	f.auth.jwks.mu.Lock()
	failures := f.auth.jwks.failures
	f.auth.jwks.mu.Unlock()
	if failures != 0 {
		t.Fatalf("failures = %d after a successful fetch, want 0", failures)
	}
}

func TestJWKSBackoffCappedAtMinRefetch(t *testing.T) {
	c := newJWKSCache("http://unused", time.Hour, slog.Default())
	for failures, want := range map[int]time.Duration{
		1: time.Second, 2: 2 * time.Second, 6: 32 * time.Second, 7: time.Minute, 50: time.Minute,
	} {
		c.failures = failures
		if got := c.backoffLocked(); got != want {
			t.Errorf("backoff after %d failures = %v, want %v", failures, got, want)
		}
	}
}

func waitFetches(t *testing.T, s *switchableJWKS, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.fetches.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("JWKS fetches = %d, want %d", s.fetches.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitIdle(t *testing.T, c *jwksCache) {
	t.Helper()
	c.mu.Lock()
	done := c.inflight
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

func TestJWKSServesStaleKeysWhileRefreshFails(t *testing.T) {
	const ttl = 10 * time.Minute
	f := newJWKSFixture(t, ttl, nil)
	tok := f.token(t, "k1")
	if _, _, err := f.auth.Authenticate(bearerRequest(tok)); err != nil {
		t.Fatal(err)
	}

	f.jwks.failing.Store(true)
	f.clock.advance(ttl + time.Second)
	if _, _, err := f.auth.Authenticate(bearerRequest(tok)); err != nil {
		t.Fatalf("stale keys not served during endpoint failure: %v", err)
	}
	waitFetches(t, f.jwks, 2)
	waitIdle(t, f.auth.jwks)
	if _, _, err := f.auth.Authenticate(bearerRequest(tok)); err != nil {
		t.Fatalf("stale keys not served after a failed refresh: %v", err)
	}

	f.clock.advance(ttl)
	_, _, err := f.auth.Authenticate(bearerRequest(tok))
	if !errors.Is(err, errJWKSUnavailable) {
		t.Fatalf("keys past the grace period: err = %v, want errJWKSUnavailable", err)
	}
}

func TestJWKSStaleRefreshDoesNotBlock(t *testing.T) {
	const ttl = 10 * time.Minute
	f := newJWKSFixture(t, ttl, nil)
	tok := f.token(t, "k1")
	if _, _, err := f.auth.Authenticate(bearerRequest(tok)); err != nil {
		t.Fatal(err)
	}

	block := make(chan struct{})
	f.jwks.setGate(block)
	t.Cleanup(func() { close(block) })
	f.clock.advance(ttl + time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := f.auth.Authenticate(bearerRequest(tok).WithContext(ctx)); err != nil {
		t.Fatalf("request with stale keys waited for the refresh: %v", err)
	}
	waitFetches(t, f.jwks, 2)
}

func TestJWKSUnknownKidFloodRateLimited(t *testing.T) {
	f := newJWKSFixture(t, time.Hour, nil)
	if _, _, err := f.auth.Authenticate(bearerRequest(f.token(t, "k1"))); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(2 * time.Minute)
	for i := range 50 {
		_, _, err := f.auth.Authenticate(bearerRequest(forgedToken(fmt.Sprintf("forged-%d", i))))
		if !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("forged token %d: err = %v", i, err)
		}
	}
	if got := f.jwks.fetches.Load(); got != 2 {
		t.Fatalf("JWKS fetched %d times for 50 unknown kids, want 2 (initial + one refetch)", got)
	}
}
