package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	// jwksFetchTimeout bounds one JWKS fetch, independent of any request.
	jwksFetchTimeout = 10 * time.Second
	// jwksMinRefetch is the minimum spacing of fetches triggered by an
	// unknown kid, and the cap on the failure backoff.
	jwksMinRefetch = time.Minute
	// jwksMinBackoff is the wait after the first failed fetch; it doubles
	// with each consecutive failure up to jwksMinRefetch.
	jwksMinBackoff = time.Second
)

// errJWKS marks every error from jwksCache.key. Their messages may name the
// endpoint, so fromJWT replaces them with a fixed client-facing message.
var errJWKS = errors.New("jwks")

// jwksError wraps a key-lookup failure so errors.Is matches errJWKS and the
// cause.
type jwksError struct{ cause error }

func (e *jwksError) Error() string   { return "jwks: " + e.cause.Error() }
func (e *jwksError) Unwrap() []error { return []error{errJWKS, e.cause} }

var (
	errJWKSNoKey       = errors.New("no key for kid")
	errJWKSUnavailable = errors.New("signing keys unavailable")
)

// jwk is the subset of RFC 7517 needed to build RSA and EC public keys.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) publicKey() (any, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad modulus: %w", k.Kid, err)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad exponent: %w", k.Kid, err)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("jwks: key %q: unsupported curve %q", k.Kid, k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad x: %w", k.Kid, err)
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: bad y: %w", k.Kid, err)
		}
		point := append(append([]byte{4}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: invalid EC point: %w", k.Kid, err)
		}
		return pub, nil
	default:
		return nil, fmt.Errorf("jwks: key %q: unsupported key type %q", k.Kid, k.Kty)
	}
}

// jwksCache fetches and caches a JWKS endpoint's signing keys.
//
// Fetches never run under mu and never on a request's context: one
// background fetch at a time is shared by every caller that needs it, and
// each caller waits for it only as long as its own context allows. Keys
// are fresh for ttl after a successful fetch; once stale they keep being
// served for up to another ttl while a background refresh runs, so an
// endpoint outage shorter than ttl does not fail verification. A failed
// fetch is not retried before a backoff (jwksMinBackoff doubling to
// jwksMinRefetch) and callers in that window get the cached failure. An
// unknown kid triggers an early fetch (key rotation) at most once per
// jwksMinRefetch, so a flood of forged tokens cannot hammer the endpoint.
type jwksCache struct {
	url    string
	ttl    time.Duration
	client *http.Client
	log    *slog.Logger
	now    func() time.Time

	mu sync.Mutex
	// keys are from the last successful fetch at fetched.
	keys    map[string]any
	fetched time.Time
	// attempted is when the last fetch started; failures counts
	// consecutive failed fetches, the last at failedAt with error lastErr.
	attempted time.Time
	failures  int
	failedAt  time.Time
	lastErr   error
	// inflight is closed when the running fetch finishes; nil when idle.
	inflight chan struct{}
}

func newJWKSCache(url string, ttl time.Duration, log *slog.Logger) *jwksCache {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &jwksCache{
		url:    url,
		ttl:    ttl,
		client: &http.Client{Timeout: jwksFetchTimeout},
		log:    log,
		now:    time.Now,
	}
}

// key returns the verification key for kid. An empty kid is accepted when
// the set holds exactly one key. Errors wrap errJWKS; they may name the
// endpoint and are not meant for clients.
func (c *jwksCache) key(ctx context.Context, kid string) (any, error) {
	c.mu.Lock()
	now := c.now()
	if c.usableLocked(now) {
		if now.Sub(c.fetched) > c.ttl {
			c.startFetchLocked(now)
		}
		if k, ok := c.lookupLocked(kid); ok {
			c.mu.Unlock()
			return k, nil
		}
		if c.inflight == nil && now.Sub(c.attempted) < jwksMinRefetch {
			c.mu.Unlock()
			return nil, &jwksError{errJWKSNoKey}
		}
	}
	done := c.inflight
	if done == nil {
		done = c.startFetchLocked(now)
	}
	if done == nil {
		err := c.failureLocked()
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return nil, &jwksError{fmt.Errorf("waiting for %s: %w", c.url, ctx.Err())}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.usableLocked(c.now()) {
		if k, ok := c.lookupLocked(kid); ok {
			return k, nil
		}
		return nil, &jwksError{errJWKSNoKey}
	}
	return nil, c.failureLocked()
}

// usableLocked reports whether the cached keys may still verify tokens.
func (c *jwksCache) usableLocked(now time.Time) bool {
	return c.keys != nil && now.Sub(c.fetched) <= 2*c.ttl
}

// failureLocked is the error for callers that found no usable keys.
func (c *jwksCache) failureLocked() error {
	if c.lastErr != nil {
		return &jwksError{fmt.Errorf("%w: %w", errJWKSUnavailable, c.lastErr)}
	}
	return &jwksError{errJWKSUnavailable}
}

// startFetchLocked starts a background fetch and returns the channel it
// closes when done. It returns the running fetch's channel if there is
// one, and nil while a failure backoff is in effect.
func (c *jwksCache) startFetchLocked(now time.Time) chan struct{} {
	if c.inflight != nil {
		return c.inflight
	}
	if c.failures > 0 && now.Sub(c.failedAt) < c.backoffLocked() {
		return nil
	}
	done := make(chan struct{})
	c.inflight = done
	c.attempted = now
	go c.refresh(done)
	return done
}

func (c *jwksCache) backoffLocked() time.Duration {
	d := jwksMinBackoff
	for i := 1; i < c.failures && d < jwksMinRefetch; i++ {
		d *= 2
	}
	return min(d, jwksMinRefetch)
}

func (c *jwksCache) refresh(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
	keys, err := c.fetch(ctx)
	cancel()

	c.mu.Lock()
	now := c.now()
	if err == nil {
		c.keys = keys
		c.fetched = now
		c.failures = 0
		c.lastErr = nil
		c.inflight = nil
		c.mu.Unlock()
		close(done)
		return
	}
	c.failures++
	c.failedAt = now
	c.lastErr = err
	c.inflight = nil
	attrs := []any{"url", c.url, "err", err, "consecutive_failures", c.failures,
		"retry_in", c.backoffLocked(), "serving_cached_keys", c.usableLocked(now)}
	c.mu.Unlock()
	c.log.Warn("jwks fetch failed", attrs...)
	close(done)
}

func (c *jwksCache) lookupLocked(kid string) (any, bool) {
	if kid != "" {
		k, ok := c.keys[kid]
		return k, ok
	}
	if len(c.keys) == 1 {
		for _, k := range c.keys {
			return k, true
		}
	}
	return nil, false
}

func (c *jwksCache) fetch(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", c.url, err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", c.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", c.url, resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", c.url, err)
	}
	keys := map[string]any{}
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			continue // skip unsupported entries (e.g. symmetric or OKP keys)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s: no usable signing keys", c.url)
	}
	return keys, nil
}
