// Package httpx holds the transport-neutral HTTP scaffolding shared by the
// products: CORS, gzip, the health handler, and ListenAndServe with the
// standard timeouts and graceful shutdown. Routing and payloads stay in the
// products.
package httpx

import "net/http"

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

// CORS sets Access-Control-Allow-Origin on responses whose Origin header
// matches an allowed origin (exact match, or "*" to allow any). Preflight
// requests are not answered here — mount Preflight on the paths that need
// it (products differ in method sets), or wrap with CORSWithPreflight.
func CORS(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && OriginAllowed(origins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}

// Preflight answers CORS preflight (OPTIONS) requests: 204 with the allow
// headers for an allowed origin, a bare 204 otherwise. allowMethods is the
// literal Access-Control-Allow-Methods value (e.g. "POST, GET, OPTIONS").
func Preflight(origins []string, allowMethods string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !OriginAllowed(origins, origin) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", allowMethods)
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
func CORSWithPreflight(origins []string, allowMethods string, next http.Handler) http.Handler {
	preflight := Preflight(origins, allowMethods)
	cors := CORS(origins, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			preflight(w, r)
			return
		}
		cors.ServeHTTP(w, r)
	})
}
