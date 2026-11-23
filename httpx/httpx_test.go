package httpx

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func health() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Healthz)
	return mux
}

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	rec := do(health(), "GET", "/healthz", nil)
	if rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("healthz = %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	h := CORS(CORSPolicy{Origins: []string{"https://example.com"}}, health())
	rec := do(h, "GET", "/healthz", map[string]string{"Origin": "https://example.com"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want https://example.com", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatalf("Vary = %q", rec.Header().Get("Vary"))
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	h := CORS(CORSPolicy{Origins: []string{"https://example.com"}}, health())
	rec := do(h, "GET", "/healthz", map[string]string{"Origin": "https://evil.com"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin on every response", got)
	}
}

func TestCORSWildcard(t *testing.T) {
	h := CORS(CORSPolicy{Origins: []string{"*"}}, health())
	rec := do(h, "GET", "/healthz", map[string]string{"Origin": "https://anything.example"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin for wildcard config", got)
	}
}

func TestCORSNoOriginHeader(t *testing.T) {
	h := CORS(CORSPolicy{Origins: []string{"*"}}, health())
	rec := do(h, "GET", "/healthz", nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty without Origin", got)
	}
}

func TestPreflight(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("OPTIONS /graphql", Preflight(CORSPolicy{Origins: []string{"https://example.com"}, AllowMethods: "POST, GET, OPTIONS"}))
	rec := do(mux, "OPTIONS", "/graphql", map[string]string{
		"Origin":                         "https://example.com",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type, authorization",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type, authorization" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want echoed request headers", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "POST, GET, OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("Access-Control-Max-Age = %q", got)
	}
}

func TestCORSExposeHeaders(t *testing.T) {
	p := CORSPolicy{Origins: []string{"https://example.com"}, ExposeHeaders: []string{"Content-Range", "ETag", "Location"}}
	rec := do(CORS(p, health()), "GET", "/healthz", map[string]string{"Origin": "https://example.com"})
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Content-Range, ETag, Location" {
		t.Fatalf("Access-Control-Expose-Headers = %q", got)
	}
	rec = do(CORS(p, health()), "GET", "/healthz", map[string]string{"Origin": "https://evil.com"})
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "" {
		t.Fatalf("Access-Control-Expose-Headers = %q for a disallowed origin", got)
	}
	rec = do(CORS(CORSPolicy{Origins: []string{"*"}}, health()), "GET", "/healthz", map[string]string{"Origin": "https://a.example"})
	if _, ok := rec.Header()["Access-Control-Expose-Headers"]; ok {
		t.Fatal("Access-Control-Expose-Headers set without ExposeHeaders")
	}
}

func TestCORSAndGzipKeepBothVary(t *testing.T) {
	p := CORSPolicy{Origins: []string{"*"}}
	hdr := map[string]string{"Origin": "https://a.example", "Accept-Encoding": "gzip"}
	for name, h := range map[string]http.Handler{
		"cors outside": CORS(p, Gzip(health())),
		"gzip outside": Gzip(CORS(p, health())),
	} {
		rec := do(h, "GET", "/healthz", hdr)
		got := rec.Header().Values("Vary")
		if len(got) != 2 || !varies(rec.Header(), "Origin") || !varies(rec.Header(), "Accept-Encoding") {
			t.Errorf("%s: Vary = %q, want Origin and Accept-Encoding once each", name, got)
		}
	}
}

func TestAddVary(t *testing.T) {
	h := http.Header{}
	h.Set("Vary", "accept-encoding, Cookie")
	AddVary(h, "Accept-Encoding", "Origin", "origin")
	if got := h.Values("Vary"); len(got) != 2 || got[0] != "accept-encoding, Cookie" || got[1] != "Origin" {
		t.Fatalf("Vary = %q", got)
	}
	h = http.Header{}
	h.Set("Vary", "*")
	AddVary(h, "Origin")
	if got := h.Values("Vary"); len(got) != 1 {
		t.Fatalf("Vary = %q, want * alone", got)
	}
}

func TestPreflightDisallowedOrigin(t *testing.T) {
	h := Preflight(CORSPolicy{Origins: []string{"https://example.com"}, AllowMethods: "POST, GET, OPTIONS"})
	rec := do(h, "OPTIONS", "/graphql", map[string]string{"Origin": "https://evil.com"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed preflight origin", got)
	}
}

func TestCORSWithPreflight(t *testing.T) {
	h := CORSWithPreflight(CORSPolicy{Origins: []string{"*"}, AllowMethods: "GET, POST, OPTIONS"}, health())
	rec := do(h, "OPTIONS", "/anything", map[string]string{
		"Origin": "https://a.example", "Access-Control-Request-Method": "POST",
	})
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Methods") != "GET, POST, OPTIONS" {
		t.Fatalf("preflight not answered: %d %v", rec.Code, rec.Header())
	}
	rec = do(h, "GET", "/healthz", map[string]string{"Origin": "https://a.example"})
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://a.example" {
		t.Fatalf("plain request: %d %v", rec.Code, rec.Header())
	}
}

func TestGzipCompressesWhenAccepted(t *testing.T) {
	h := Gzip(health())
	rec := do(h, "GET", "/healthz", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip body: %v", err)
	}
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("unexpected decompressed body: %q", body)
	}
}

func TestGzipSkippedWithoutAcceptEncoding(t *testing.T) {
	rec := do(Gzip(health()), "GET", "/healthz", nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when client does not accept gzip", got)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestGzipSkipsCORSPreflight(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("OPTIONS /graphql", Preflight(CORSPolicy{Origins: []string{"*"}, AllowMethods: "POST, GET, OPTIONS"}))
	h := Gzip(CORS(CORSPolicy{Origins: []string{"*"}}, mux))
	rec := do(h, "OPTIONS", "/graphql", map[string]string{"Origin": "https://example.com", "Accept-Encoding": "gzip"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty on preflight", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("preflight body = %q, want empty", rec.Body.String())
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate", false},
		{"*", true},
		{"gzip;q=0", false},
		{"gzip;q=0.5", true},
		{"identity;q=1, gzip;q=0", false},
		{"br;q=1.0, gzip;q=0.8", true},
	}
	for _, tc := range cases {
		if got := AcceptsGzip(tc.header); got != tc.want {
			t.Errorf("AcceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestListenAndServeShutsDownOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ListenAndServe(ctx, health(), ServeOptions{Addr: addr, RequestTimeout: time.Second}) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}
