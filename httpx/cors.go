// Package httpx holds the transport-neutral HTTP scaffolding shared by the
// products: CORS, gzip, the health handler, and ListenAndServe with the
// standard timeouts and graceful shutdown. Routing and payloads stay in the
// products.
package httpx

import (
	"net/http"
	"strings"
)

// CORSPolicy is the cross-origin policy of a set of routes.
type CORSPolicy struct {
	// Origins is the allowlist: exact origins, or "*" to allow any. An
	// allowed origin is echoed in Access-Control-Allow-Origin.
	Origins []string
	// AllowMethods is the literal Access-Control-Allow-Methods value of
	// preflight responses, e.g. "POST, GET, OPTIONS".
	AllowMethods string
	// ExposeHeaders lists the response headers, beyond the CORS-safelisted
	// ones, that browsers let scripts read (Access-Control-Expose-Headers),
	// e.g. Content-Range or ETag.
	ExposeHeaders []string
}

// OriginAllowed reports whether origin matches the allowlist: exact match,
// or "*" to allow any.
func OriginAllowed(origins []string, origin string) bool {
	for _, o := range origins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// AddVary adds names to the Vary header of h, keeping the values already
// there and skipping names it already lists (compared case-insensitively),
// so middlewares that each vary on a request header compose in any order.
func AddVary(h http.Header, names ...string) {
	for _, name := range names {
		if !varies(h, name) {
			h.Add("Vary", name)
		}
	}
}

func varies(h http.Header, name string) bool {
	for _, v := range h.Values("Vary") {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok == "*" || strings.EqualFold(tok, name) {
				return true
			}
		}
	}
	return false
}

// CORS adds the cross-origin headers of p to responses: Vary: Origin on
// every response (the headers depend on it), and for a request whose Origin
// is allowed, Access-Control-Allow-Origin echoing it and
// Access-Control-Expose-Headers listing p.ExposeHeaders. Preflight requests
// are not answered here: mount Preflight on the paths that need it, or wrap
// with CORSWithPreflight.
func CORS(p CORSPolicy, next http.Handler) http.Handler {
	expose := strings.Join(p.ExposeHeaders, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		AddVary(h, "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && OriginAllowed(p.Origins, origin) {
			h.Set("Access-Control-Allow-Origin", origin)
			if expose != "" {
				h.Set("Access-Control-Expose-Headers", expose)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Preflight answers CORS preflight (OPTIONS) requests: 204 with the allow
// headers for an allowed origin, a bare 204 otherwise. The allowed request
// headers echo Access-Control-Request-Headers.
func Preflight(p CORSPolicy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		AddVary(h, "Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers")
		origin := r.Header.Get("Origin")
		if origin == "" || !OriginAllowed(p.Origins, origin) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Methods", p.AllowMethods)
		if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
			h.Set("Access-Control-Allow-Headers", reqHeaders)
		}
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
	}
}

// CORSWithPreflight combines CORS with a catch-all preflight: OPTIONS
// requests carrying Access-Control-Request-Method are answered by Preflight
// on every path; everything else flows through CORS to next.
func CORSWithPreflight(p CORSPolicy, next http.Handler) http.Handler {
	preflight := Preflight(p)
	cors := CORS(p, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			preflight(w, r)
			return
		}
		cors.ServeHTTP(w, r)
	})
}
